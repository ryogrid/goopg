Task: M0118-0003 multixact — LANDED the WRITE-PATH LOCK-WAIT HALF (resume point
#3, write-path half), completing update-chain lock propagation. The plain
UPDATE/DELETE write path now WAITS on a row lock propagated forward onto a live
version (slice 5 locker half) before stamping its own xmax. **lock-update-
traversal now PASSES** (TestPort_IsolationLockUpdateTraversal, all 3 perms).
This loop (#53) is COMPLETE and committed.

DONE this loop (#53):
- operators_storage.go: NEW `conflictingRowLockHolders(ctx,h,reqStatus)` (write-
  path analog of activeLockHolders: still-active foreign holders whose strength
  conflicts; single-xid via tupleLockConflicts, multi via per-member
  StatusesConflict; real updater→nil, left to isConcurrentlyUpdated/EPQ) +
  `waitForConflictingRowLock(ctx,rel,blk,slot,reqStatus,pos)` (re-read hdr,
  epqWait each holder until none — reuses write-path WFG deadlock detection).
- Wired at 3 sites: deleteOp.Next (pre per-victim loop, StatusUpdate),
  updateViaIndex (per-pu loop), updateOp.Next seqscan Phase-2 loop. reqStatus
  from hotUpdateEligible (SAME signal that stamps HEAP_KEYS_UPDATED — sibling
  path): !hotEligible→StatusUpdate, hotEligible→StatusNoKeyUpdate.
- acquireTupleLock RETAINED (no-op cross-statement; new wait is the real gate).
- Tests: NEW TestConflictingRowLockHoldersHonoursStrengthMatrix; UPDATED 3
  lockmgr-waiter tests to end the holder TRANSACTION (faithful unblock trigger).
- CSV inventory failed→pass + regen inventory.md/upstream-isolation-coverage.md.
- Design 0118-0002 slice 6 + resume-#3 status (write-path half ✅) + README index.
  Ledger row appended (123 lines).

GATES this loop (all PASS): go build ./...; go vet ./internal/executor; full
executor pkg + multixact unit suites; -race executor lock subset + mvcc;
TestPort_IsolationLockUpdateTraversal PASS (3 perms); 7 row-lock isolation specs
(LockCommittedUpdate/Keyupdate/Nowait/Nowait3/SkipLocked/UpdateLockedTuple/
UpdateConflictOut) re-verified PASS — NO regression. gofmt: only pre-existing
go1.25/1.26 struct/comment-alignment artifacts in operators_storage.go (NOT my
code — do NOT gofmt -w); my test-file double-blank-line fixed. ralph-state-guard
OK (auto-repaired prev-loop stale completed marker). tpch-spotcheck INFRA-BLOCKED
(SLRU backfill >60s). pgbench pre-commit smoke via hook on commit. Stage ONLY
code/doc/.ralph; do NOT add stray `postgres`, weekly_loc.*, requirements.txt.

>>> NEXT STEP (resume point #3 remainder + M0118-0003 bucket): pick ONE —
    (a) **savepoint/subxact members**: thread the subtransaction xid into the
        producer so a txn's main xid + a subxid locking the same tuple appear as
        DISTINCT multixact members (tuplelock-conflict savepoint perms need this).
    (b) **lock-update-delete / propagate-lock-delete**: layer advisory-lock
        chains / FK-INSERT lock propagation on top of this write-path wait.
    (c) WAL/pg_multixact persistence of members.
    The cross-statement row-lock gate is now conflictingRowLockHolders +
    waitForConflictingRowLock (operators_storage.go) for ANY propagated row lock.

GOTCHAS: MultiXactId & TransactionID share uint32; HEAP_XMAX_IS_MULTI is the ONLY
disambiguator — never pass a raw multi xmax to IsXIDActive/WaitForXID (helpers
resolve via Store.Members). Sibling-path: reqStatus classification ↔ HEAP_KEYS_
UPDATED stamp (both keyed on hotUpdateEligible). PageSetHeapTupleXmax CLEARS lock-
only/multi/invalid bits, so a DELETE/UPDATE over a previously-locked tuple is
correctly re-stamped as a real delete. lockmgr locks are statement-scoped
([[lockmgr_locks_are_statement_scoped]]) → cross-statement blocking rides the
persisted lock-only xmax + holder-txn WaitForXID, NOT acquireTupleLock.
