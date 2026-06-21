Task: M0118-0004 (deadlock detection) — this loop made PARTIAL progress on
`tuplelock-upgrade-no-deadlock` (crash fixed, spec NOT promoted). M0118-0004 continues.

DONE this loop (committed):
- BUG FIX (not a promotion): `tuplelock-upgrade-no-deadlock` perm 5
  (s1_keyshare s2_for_update s3_keyshare s3_delete s1_rollback s3_commit
  s2_rollback) crashed with `ERROR: short read at block`. ROOT CAUSE: s2's
  waking FOR UPDATE followed the committed-DELETE's t_ctid chain; a goopg DELETE
  leaves the initial CTID {InvalidBlockNumber,0} (PageSetHeapTupleXmax stamps
  only xmax), which is NOT self-pointing, so stampLockInner's self-only "no
  successor" guard fell through and Pinned block InvalidBlockNumber → ErrShortRead.
  FIX: extracted isChainTailCTID(ctid,curBlk,curSlot) (InvalidBlockNumber ||
  Offset==0 || self) — used in BOTH epqFollowChainFull AND stampLockInner
  (sibling-paths); deleted-row return now epqSkipped=true so drainAndStamp drops
  the row → s2 sees (0 rows). Perm 5 now correct.
- Files: internal/executor/operators_storage.go (+isChainTailCTID, epqFollowChainFull
  uses it), internal/executor/operators_lockrows.go (stampLockInner uses it +
  epqSkipped=true), internal/executor/chain_tail_ctid_test.go (NEW unit test —
  CI guard since spec stays t.Skip), internal/testport/isolation_port_test.go
  (+TestPort_IsolationTuplelockUpgradeNoDeadlock skip-anchor), design NEW
  0118-0008 + README index, fix_plan + deferral ledger annotated.

GATES (all PASS): go build ./...; go vet ./internal/executor; gofmt clean on
changed lines (pre-existing go1.25/1.26 alignment noise elsewhere left untouched
— do NOT gofmt -w); go test ./internal/executor incl TestIsChainTailCTID;
regression batch LockUpdateDelete/LockUpdateTraversal/UpdateLockedTuple/
TuplelockConflict/TuplelockUpdate/TuplelockPartition/PropagateLockDelete/
MultixactNoDeadlock/SkipLocked{,2,3,4}/Nowait{,2,3,4,5}/LockCommitted{Update,
Keyupdate}/Merge{Update,Delete,MatchRecheck}/EvalPlanQual{,Trigger} PASS; -race
on LockUpdateDelete/MultixactNoDeadlock/TuplelockConflict PASS; ralph-state-guard
OK. DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004, finish tuplelock-upgrade-no-deadlock):
    Two failures remain (spec CSV still `failed`, test still t.Skip):
    (1) WAIT-QUEUE UPGRADE PRIORITY (perms 2,3): after s1 rolls back, the existing
        key-share holder s3 upgrading its OWN lock must wake BEFORE the pure
        waiter s2 (PG: s3_update completes first, THEN s2_for_update). goopg wakes
        s2 first. FIX target: stampLockInner committed-updater branch (~L719 in
        operators_lockrows.go) does NOT special-case a multixact xmax carrying
        updater+lockers — make it multixact-aware (when xmax IS_MULTI w/ updater,
        resolve members via shared store, wait only on conflicting ACTIVE members,
        let an existing holder's self-upgrade proceed). Mirror stampMultiUpdaterLock.
    (2) SAVEPOINT lock-retry (perm 9): s1_fornokeyupd times out / bad connection
        where PG re-runs the tuple-lock algorithm after `rollback to savepoint`
        changes multixact membership (heap_lock_tuple HeapTupleUpdated retry).
    Then `deadlock-parallel` — DEFER (needs parallel-query lock-group abstraction).
    Per-spec workflow: capture live diff; fix → green → CSV failed→pass (rationale
    COMMA-FREE) → regen gen-oracle-inventory + gen-isolation-coverage → design slice
    + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. gofmt version mismatch — never gofmt -w
[[goopg_gofmt_version_mismatch_no_w]]. goopg DELETE omits HEAP_KEYS_UPDATED + keeps
self... no, keeps {InvalidBlockNumber,0} CTID [[goopg_delete_no_heap_keys_updated]].
tpch-spotcheck INFRA-BLOCKED (SLRU backfill >60s); row-lock path never fires in
pgbench TPC-B/TPC-H so TPS blast radius nil. Pre-existing forvar lint nit at
isolation_port_test.go:50 UNRELATED.
