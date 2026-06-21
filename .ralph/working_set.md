Task: M0118-0004 `tuplelock-upgrade-no-deadlock` — PRODUCER side landed (design 0118-0011).
Spec advanced 0 → 8 of 9 permutations matching. Spec still `defer` (perm 9 fails).

DONE this loop (committed):
- NEW storage primitive `storage.PageStampHotOldTupleMulti` (heap.go) — multi xmax +
  multi hint bits + HOT chain CTID + HEAP_HOT_UPDATED; pd_prune_xid from updater xid.
- NEW shared helper `stampUpdaterXmaxPreservingLockers(ctx, hdr, keysUpdated)`
  (operators_storage.go) — gated on lock-only xmax; keeps only still-active foreign
  lockers NOT conflicting (multixact.StatusesConflict) with our updater; appends our
  member; builds {updater+survivors} multi. + non-HOT wrapper `stampUpdaterXmaxNonHOT`.
- Wired ALL old-tuple xmax stamp twins (sibling-paths): tryApplyHOTUpdate (spec path,
  keysUpdated=false); index/seqscan UPDATE delete-half; index/seqscan DELETE +
  DELETE…USING; UPDATE…FROM; merge update/delete; upsert update+delete. key-UPDATE/
  DELETE (StatusUpdate) conflicts with all modes → always plain-stamp (no-op there).
- Crash-safe: HOT/delete WAL records carry single updater xid → replay degrades to
  single-xid (transient lockers don't survive crash; 0118-0002 precedent).
- Unit tests: TestPageStampHotOldTupleMulti (storage), TestStampUpdaterXmaxPreservingLockers
  (executor). Files: heap.go, heap_test.go, operators_storage.go, operators_merge.go,
  operators_upsert.go, concurrent_update_xmax_test.go, design 0118-0011 + README, fix_plan,
  deferral ledger.

GATES (all PASS): build/vet/gofmt clean (my lines NOT in gofmt diff — do NOT gofmt -w);
new unit tests PASS; -race batch (Multixact*/Tuplelock*/LockUpdate*/UpdateLocked*/
PropagateLock*/LockCommitted*/EvalPlanQual*/SkipLocked*/Nowait*/Merge*/StampUpdater*/
*HOTUpdate*/Upsert*) PASS; internal/multixact+storage -race PASS; full executor+wal+mvcc
PASS; CI-parity pgbench smoke (standard/-N/-S) 0-failed. DO NOT stage: postgres,
weekly_loc.*, requirements.txt, weekkly_loc_history.py.

VERIFY of 8/9: runIsoSpec first divergence is expected L216, INSIDE perm 9 (starts L194)
→ perms 1-8 all match. Perm boundaries in expected .out: L3/34/61/88/114/139/164/179/194.

>>> NEXT STEP (continue M0118-0004 — PERM 9, the last failing perm):
    Perm 9 = `s1_keyshare s3_for_update s2_for_keyshare s1_savept_e s1_share s1_savept_f
    s1_fornokeyupd s2_fornokeyupd s0_begin s0_keyshare s1_rollback_f s0_keyshare
    s1_rollback_e s1_rollback s2_rollback s0_rollback s3_rollback`. It exercises the
    SAVEPOINT-driven tuple-lock RETRY: (a) `rollback to savepoint` must RELEASE a
    subtransaction's row lock so a blocked waiter becomes grantable; (b) s2 must re-run
    the whole tuple-lock acquisition after initially avoiding a deadlock. goopg has NO
    subtransaction-scoped row-lock lifecycle (row locks ride xmax/multixact + WaitForXID,
    not a savepoint-aware structure). PLAN: figure how goopg models savepoints/subxacts
    (search ROLLBACK TO / Savepoint / subxact in internal/executor + mvcc), and whether a
    subxact's multixact membership can be retracted on rollback-to-savepoint so the
    tuple's xmax members drop the rolled-back locker → blocked waiter re-probes and
    proceeds. This is a DISTINCT subsystem — its own loop. Likely large; may need a
    design slice first. SEPARATELY (lower priority, its own slice): make the UPDATE/DELETE
    conflict-WAIT on a CONFLICTING lock-only locker (today dropped by plain stamp).
    Then deadlock-parallel — DEFER (needs lock-group abstraction goopg lacks).
    Promotion workflow when perm 9 lands: fix→green full spec→CSV failed→pass (rationale
    COMMA-FREE) → regen gen-oracle-inventory + gen-isolation-coverage → design slice +
    README + ledger.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go. gofmt version mismatch — never gofmt -w
[[goopg_gofmt_version_mismatch_no_w]]. goopg DELETE keeps initial CTID
{InvalidBlockNumber,0} [[goopg_delete_no_heap_keys_updated]]. tpch-spotcheck
INFRA-BLOCKED (SLRU backfill >60s); locker-preservation gate never fires in pgbench
TPC-B/TPC-H so TPS blast radius nil. Existing producers to mirror: stampMultiLock /
stampMultiUpdaterLock (operators_lockrows.go ~L1328/L1406) — the LOCK path twins.
