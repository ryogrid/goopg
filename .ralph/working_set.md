Task: M0119-0009 — UPDATE/DELETE/MERGE/upsert conflict-wait sibling-path
parity (the loop #44 ledger row's own resume point). COMPLETE and committed
this loop (#46).

Files: internal/executor/operators_storage.go (updateWithFrom,
deleteWithUsing gain a waitForConflictingRowLock call before the trigger
fire, matching updateViaIndex/updateOp.Next/deleteOp.Next's existing
pattern); internal/executor/operators_merge.go (mergeApplyUpdate,
mergeApplyDelete gain the wait after their unlock/unpin, before trigger
fire; new multixact import); internal/executor/operators_upsert.go
(upsertOp.applyUpdate gains the wait at function entry; new multixact
import); internal/executor/merge_upsert_conflict_wait_test.go (new, 3
tests); internal/executor/update_from_delete_using_conflict_wait_test.go
(new, 2 tests); docs/design/0119-0009-update-delete-merge-upsert-conflict-wait.md
(new) + docs/design/README.md + docs/design/0118-0011-*.md (cross-ref
addendum); .ralph/fix_plan.md (new M0119-0009 [x] item) +
.ralph/deferral_ledger.md (row 327 flipped resolved, new residual row
appended).

Key symbols: waitForConflictingRowLock/conflictingRowLockHolders
(operators_storage.go, pre-existing M0118-0003 helper, reused not
modified), stampUpdaterXmaxNonHOT/stampUpdaterXmaxPreservingLockers (the
producer these wait calls now precede at all 8 call sites total).

Findings: the ledger row's claim ("UPDATE/DELETE never waits on a
conflicting locker") was already false for the 3 canonical write sites
(M0118-0003 predates the ledger row); the REAL gap was 5 sibling sites
never wired. Confirmed RED->GREEN via git-stash for the 2 MERGE sites
(genuine gaps). The other 3 (updateWithFrom/deleteWithUsing/upsert
applyUpdate) already had SOME pre-existing protection (scanMatching's
older non-conflict-aware M0021 lockmgr block for the first two; upsert's
own arbiter-conflict pre-wait for the third) discovered mid-investigation
via a goroutine-stack dump when a naive test hung — so those 3 tests are
regression/smoke coverage, not discriminating proof, and the design doc
says so explicitly (independently re-verified true by review agent).
3 residual gaps ledgered, not fixed: (a) updateWithFrom/deleteWithUsing's
narrow Step2/Step3 scan-then-stamp race window (no test — needs 3-session
precise timing); (b) upsert's NND arbiter path (probeArbiterNND) has no
wait of its own before the scan; (c) scanMatching's lockedByForeign gate
is NOT conflict-aware (over-blocks a no-key UPDATE vs non-conflicting FOR
KEY SHARE on the seqscan path — latent, no forcing spec today).

Next step: idle — nothing in flight for THIS task. Per the M0119-0004
"Still open" list (unchanged from loop #45): (1) dump object-ordering
(milestone-sized, not a slice); (2) btree/hash's own amadjustmembers (no
forcing fixture); (3) builtin-operator catalog (extend only when a
fixture needs it). M0119-0002 (CLOG store swap Part B, dedicated full-gate
session) and this loop's (b)/(c) residuals are also independently
resumable alternatives.

Gates run this loop: go build ./... clean; go vet clean; race batch
(-race ./internal/executor -run
'Multixact|Tuplelock|LockUpdate|UpdateLocked|PropagateLock|LockCommitted|
EvalPlanQual|SkipLocked|Nowait|Merge|StampUpdater|HOTUpdate|Upsert|
ConflictingRowLock|BlocksOnForeign|ConflictWait') PASS; full
internal/executor suite PASS; -race internal/mvcc+internal/multixact+
internal/wal PASS; internal/catalog+internal/planner+internal/server
PASS; TestPort_PgDumpConnectionSetup PASS; TPC-H spotcheck Q12=2/Q13=33
PASS; make ralph-state-guard OK (self-repaired usual stale marker);
independent agent review of design doc + diff: no bugs, RED->GREEN claim
independently re-verified. pgbench smoke runs at commit time via the
pre-commit hook.
