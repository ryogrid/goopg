Task: M0118-0004 (deadlock detection) — `tuplelock-upgrade-no-deadlock` continues.
This loop landed the WRITE-SIDE EPQ-wait multixact-aware fix (design 0118-0010).
Spec NOT promoted (perms 2/3/9 still fail — producer side unimplemented).

DONE this loop (committed):
- BUG FIX (write side, NOT a promotion): the UPDATE/DELETE/MERGE EvalPlanQual
  retry loops captured RAW `xmax := <tuple>.Header.Xmax` and fed it to epqWait
  (-> WaitForXID + wait-for graph) and to snap.HasInProgress/HasAbortedXID/
  IsXIDActive. When xmax is an updater-bearing MultiXactId the raw value is a
  MultiXactId not a TransactionID -> waiter blocked on a bogus xid. FIX: new
  shared `concurrentModifierXID(hdr, mxs)` resolves the real updater member;
  used at all 9 EPQ-wait sites (7 storage + 2 merge). Twin of read-side 0118-0009.
- Files: internal/executor/operators_storage.go (+concurrentModifierXID near
  multixactFirstActiveMember + 7 sites: HOT race check, idx/seq UPDATE
  delete+insert initial+post-write, idx/seq DELETE, UPDATE...FROM),
  internal/executor/operators_merge.go (2 sites), docs/design/0118-0010-*.md
  (NEW) + README index, fix_plan + deferral ledger annotated.

GATES (all PASS): go build ./...; go vet ./internal/executor; gofmt — both files
pre-existing-dirty at HEAD (go1.25/1.26 noise), my lines NOT in gofmt diff (do
NOT gofmt -w); unit batch with -race (Multixact*/Tuplelock*/LockUpdate*/
UpdateLocked*/PropagateLock*/LockCommitted*/EvalPlanQual*/SkipLocked*/Nowait*/
Merge*) PASS; internal/multixact -race PASS; TestPort_IsolationMultixactNoDeadlock
PASS (no regression); TestPort_IsolationTuplelockUpgradeNoDeadlock still deferred.
ralph-state-guard OK. DO NOT stage: postgres, weekly_loc.*, requirements.txt,
weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0004 — PRODUCER side; the spec's blocker):
    KEY DISCOVERY: the spec's `update tlu_job set name=...` does NOT change an
    index key -> HOT-eligible -> the old-tuple stamp goes through
    storage.PageStampHotOldTuple inside tryApplyHOTUpdate (operators_storage.go
    ~L2137), NOT the PageSetHeapTupleXmax sites at :2638/:3279. So the producer
    needs a NEW storage primitive, not just wiring the delete+insert sites.
    PLAN: (1) add storage.PageStampHotOldTupleMulti(page, oldSlot, multi,
    infomaskBits, infomask2Bits, newBlk, newSlot) (or extend PageStampHotOldTuple)
    that sets a multi xmax + CTID->new + HEAP_HOT_UPDATED. (2) shared
    stampUpdaterXmaxPreservingLockers gated on a pre-existing FOREIGN
    non-conflicting ACTIVE lock-only xmax (single or multi) forming {Tx.XID
    updater + survivors} via the new primitive; common no-locker case keeps the
    plain stamp (bounded blast radius). Reuse multixact.Store.CreateFromMembers +
    multixact.HintBits + lockOnlyMemberStatus/updaterMemberStatus (all in
    operators_lockrows.go). (3) wire into tryApplyHOTUpdate (spec path) AND the
    plain :2638/:3279 + merge/upsert twins (sibling-paths). (4) make the UPDATE
    conflict-wait multixact-aware (waitForConflictingRowLock already per-member
    conflict-aware; verify it leaves only non-conflicting survivors). MANDATORY
    full gates: pgbench smoke + full regress-port (UPDATE hot path). THEN perm 9
    savepoint-driven lock retry (heap_lock_tuple HeapTupleUpdated re-run after
    rollback-to-savepoint). Then deadlock-parallel — DEFER (needs lock groups).
    Per-spec promotion workflow: fix->green->CSV failed->pass (rationale
    COMMA-FREE) -> regen gen-oracle-inventory + gen-isolation-coverage -> design
    slice + README + ledger.

GOTCHAS: CSV rationale MUST be comma-free [[serena_replace_content_dotall_eats_file]];
prefer built-in Edit for Go code. gofmt version mismatch — never gofmt -w
[[goopg_gofmt_version_mismatch_no_w]]. goopg DELETE keeps initial CTID
{InvalidBlockNumber,0} [[goopg_delete_no_heap_keys_updated]]. tpch-spotcheck
INFRA-BLOCKED (SLRU backfill >60s); row-lock path never fires in pgbench TPC-B/
TPC-H so TPS blast radius nil. Existing producers to mirror: stampMultiLock /
stampMultiUpdaterLock (operators_lockrows.go ~L1328/L1406) build {updater+
lockers} multis on the LOCK path — the UPDATE path is the missing twin.
