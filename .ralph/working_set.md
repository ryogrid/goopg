Task: M0118-0003 multixact — LANDED the updater-bearing MultiXact PRODUCER
(stampLockInner branch (a)). This was the final ⛔ resume-point #2 item; the
producer gate is now fully closed end-to-end (producer + every consumer wired).

DONE this loop (#50), committed:
- `stampMultiUpdaterLock` (operators_lockrows.go): the updater-bearing twin of
  stampMultiLock. Branch (a) (FOR SHARE/KEY SHARE meets a non-lock-only no-key
  updater) no longer skips — it combines our share locker + the updater into a
  **non**-lock-only MultiXactId. Survivor filter mirrors MultiXactIdExpand: keep
  in-progress holders + a COMMITTED updater (committed ≡ !IsXIDActive &&
  !HasAbortedXID), drop dead lockers / aborted updater; no survivor → preserve
  the M0100-0005f skip. Helper `updaterMemberStatus(keysUpdated)` decodes the
  single-xid updater's status. Our member = lockMemberStatus() (StatusForShare;
  4-way distinction deferred — HintBits unaffected, updater dominates).
- Threaded the REAL *multixact.Store through the last two nil read consumers:
  executor `analyzeRelationWith` (ctx.MultiXact; analyzeRelation test-wrapper
  passes nil) and `vacuum.Analyze` (new param; autovacuum Launcher.MultiXact
  field — launcher is test-only in prod, so nil there is harmless).
- Tests (operators_lockrows_test.go): TestForShareJoinsInProgressUpdaterForms
  MultiXact (forms {updater@NoKeyUpdate, locker@ForShare}, GetUpdateXid=updater)
  + TestForShareSkipsAbortedUpdaterNoMultiXact (aborted updater → no multi).
- Design 0118-0002: status line; new "slice 3" section; resume #2 producer ⛔→✅.
  README index: status col + body slice-3 paragraph + deferred renumber.

GATES this loop (all PASS): go build ./...; go vet executor/vacuum/autovacuum;
full `go test ./internal/executor`; affected pkgs (mvcc/multixact/vacuum/
autovacuum/storage); btree (internal/access/btree); -race subset (executor row-
lock/FK/upsert + mvcc + multixact); **9 dedicated isolation row-lock specs PASS**
(LockCommittedUpdate/Keyupdate, MergeUpdate, MergeInsertUpdate, PredicateLockHot
Tuple, SkipLocked, Nowait, Nowait3, UpdateLockedTuple) — producer is byte-
identical to the prior skip for yielded rows, only ADDS the multixact record.
gofmt: fixed one self-introduced double blank line; rest of tree is pre-existing
go1.25/1.26 skew — do NOT gofmt -w. tpch-spotcheck INFRA-BLOCKED (SLRU backfill
>60s); pgbench pre-commit hook is the live guard. Stage ONLY code/doc/.ralph;
do NOT add stray `postgres`, weekly_loc.*, requirements.txt.

>>> NEXT STEP: with the producer live, the MultiXact-cluster specs are the
    payoff. Start by RUNNING (oracle-diff, not promoting yet) lock-update-
    traversal / propagate-lock-delete / multixact-no-forget / aborted-keyrevoke
    to measure the remaining gap. Most still need resume #3 (4-way FOR KEY SHARE
    / FOR SHARE / FOR NO KEY UPDATE / FOR UPDATE member status — goopg collapses
    to ShrLock/ExclLock; thread the distinction into lockStrength + member
    status) and/or #4 (deadlock detection across the row-lock wait graph for
    multixact-no-deadlock / tuplelock-upgrade-no-deadlock). Pick the spec whose
    only blocker is the 4-way distinction and land #3 as the next slice.

GOTCHAS: MultiXactId and TransactionID share uint32; HEAP_XMAX_IS_MULTI is the
ONLY disambiguator. Producer only fires with a foreign no-key updater + our
ShrLock (never pgbench/TPC-H) → TPS blast radius nil. Updater-bearing multis are
NOT visibility-transparent — if you touch any xmax reader, re-run the row-lock
isolation suite. WAL persistence of members still deferred (resume #4-was-now-2):
lock-only + updater-bearing multis are in-memory only, lost on crash (correct for
transient lock state; pg_multixact SLRU parity is the deferred work).
