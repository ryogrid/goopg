Task: M0118-0004 (deadlock detection) — `tuplelock-upgrade-no-deadlock` continues.
This loop landed the READ-SIDE half of the perm 2/3 wait-ordering fix. Spec NOT
promoted (perms 2,3,9 still fail). M0118-0004 continues.

DONE this loop (committed):
- BUG FIX (read side, NOT a promotion): lockRowsOp.stampLockInner's real-updater
  branch captured `xmax := tup.Header.Xmax` and fed that RAW value to
  TxnMgr.IsXIDActive/WaitForXID/HasAbortedXID. When xmax is a MultiXactId
  (HEAP_XMAX_IS_MULTI {updater, key-share holder}) the raw value is NOT a
  TransactionID → a FOR UPDATE waiter ignored a surviving co-holder and proceeded
  out of order. FIX: when IsHeapTupleXmaxMulti → resolve updater via updaterXID
  (used for abort/commit/chain), wait FIRST on every OTHER active member
  (activeLockHolders skips Tx.XID) honouring SKIP LOCKED/NOWAIT/blocking, then
  retry stampLockInner from scratch. Mirrors heap_lock_tuple MultiXactIdWait.
- Files: internal/executor/operators_lockrows.go (stampLockInner ~L800 +multiHolders
  branch), docs/design/0118-0009-rowlock-multixact-updater-wait.md (NEW) + README
  index, fix_plan + deferral ledger annotated.

GATES (all PASS): go build ./...; go vet ./internal/executor; gofmt clean on
changed lines (pre-existing go1.25/1.26 alignment noise elsewhere untouched — do
NOT gofmt -w); multixact/row-lock regression batch (LockUpdateDelete/
LockUpdateTraversal/UpdateLockedTuple/TuplelockConflict/TuplelockUpdate/
TuplelockPartition/PropagateLockDelete/MultixactNoDeadlock/SkipLocked*/Nowait*/
LockCommitted{Update,Keyupdate}/Merge{Update,Delete,MatchRecheck}/EvalPlanQual*)
exit 0 — NO regression. ralph-state-guard OK. DO NOT stage: postgres, weekly_loc.*,
requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004 — PRODUCER side of perms 2/3):
    Read side is done but observably a no-op until the producer preserves the
    co-locker. goopg's UPDATE stamps the OLD tuple with a SINGLE updater xid via
    `PageSetHeapTupleXmax(s.Page(), pu.slot, Tx.XID)` at operators_storage.go:2638
    AND :3279 (plus operators_merge.go:886/976 + operators_upsert.go:932/1287
    twins) — discarding any pre-existing non-conflicting lock-only locker (s3's
    key-share). PG's heap_update -> compute_new_xmax_infomask/MultiXactIdCreate
    forms {updater + surviving non-conflicting members}.
    PLAN: add shared `stampUpdaterXmaxPreservingLockers(ctx,rel,blk,slot)` that —
    ONLY when the old tuple already has a foreign non-conflicting ACTIVE lock-only
    xmax (single or multi) — forms {Tx.XID updater + survivors} via
    PageSetHeapTupleXmaxMulti instead of the plain stamp (common pgbench case = no
    foreign locker → plain stamp → bounded blast radius). Wire at the 2 storage
    sites + merge/upsert twins (sibling-paths). THEN make the UPDATE conflict-wait
    multixact-aware (wait on a conflicting member; let an existing member upgrade)
    so s3_update behaves. MANDATORY gates for this slice: pgbench smoke + FULL
    regress-port (UPDATE hot path). THEN perm 9 (savepoint-driven lock retry,
    heap_lock_tuple HeapTupleUpdated re-run after rollback-to-savepoint).
    Then `deadlock-parallel` — DEFER (needs parallel-query lock-group abstraction).
    Per-spec workflow on promotion: fix→green→CSV failed→pass (rationale COMMA-FREE)
    → regen gen-oracle-inventory + gen-isolation-coverage → design slice + README
    + ledger.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. gofmt version mismatch — never gofmt -w
[[goopg_gofmt_version_mismatch_no_w]]. goopg DELETE keeps initial CTID
{InvalidBlockNumber,0} [[goopg_delete_no_heap_keys_updated]]. tpch-spotcheck
INFRA-BLOCKED (SLRU backfill >60s); row-lock path never fires in pgbench TPC-B/
TPC-H so TPS blast radius nil. Pre-existing forvar lint nit at
isolation_port_test.go:50 UNRELATED.
