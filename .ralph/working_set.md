Loop #29: M0118-0008 — DROP INDEX locks the index relation tree (design 0118-0071).
COMMITTED + pushed at loop end. NOT a promotion (closes "blocker 1" of 4).

## What landed (enabler)
execDropIndex (!s.Concurrent) now calls lockDropIndexTree(idx) acquiring locks in
PG RangeVarCallbackForDropRelation order:
1. lockDropIndexSelf — AccessExclusiveLock on the target index's own RelFileNode
   (IndexRelFileNode), granted before any blocking.
2. lockDropIndexTableTree — pre-existing heap partition-tree walk; BLOCKS on a leaf
   partition a reader holds in ACCESS SHARE.
3. lockDropIndexChildren — recurses InMemory.IndexPartitionChildren (the
   PartitionParentOID/RegisterIndexPartitionChild linkage from 0118-0068),
   AccessExclusiveLock each descendant child index; reached only after (2) unblocks.
Order is load-bearing: acquireRelLockTxn genuinely blocks (AcquireWithTimeout), so
1st s3getlocks shows target index granted + child indexes ABSENT, 2nd shows child
indexes granted once DROP completes — both match PG. New locks are real tableLockMgr
locks surfaced by the 0118-0070 bridge; no bridge/dedup change.

Files: internal/executor/operators_ddl.go (lockDropIndexTree + lockDropIndexSelf +
lockDropIndexChildren, comment update at execDropIndex), partition_drop_index_lock_test.go
(new), docs/design/0118-0071-*.md + README index, deferral_ledger.md.

Key symbols: lockDropIndexTree, lockDropIndexSelf, lockDropIndexChildren,
catalog.InMemory.IndexPartitionChildren, IndexRelFileNode, acquireDDLLockTxn.

Gates: new TestDropIndexLocksIndexRelationTree + TestCreateIndexRecursesPartitionTree
PASS; no regression DropIndexConcurrently1/LockNowait/AlterTable1/2/3/CreateTrigger
(59s); go test -race ./internal/lockmgr/; go build ./... clean; pgbench smoke=pre-commit.

## Probe result (2026-06-24)
1st s3getlocks now has part_drop_index_locking_idx AccessExclusive|t (was missing);
2nd has _subpart_child_id_idx + _subpart_id_idx AccessExclusive|t. DROP-side rows
match PG. Remaining first-getlocks gap = 2 SELECT-index AccessShare rows (blocker 2)
+ empty query column (blocker 3).

## partition-drop-index-locking remaining blockers (resume point)
2. **SELECT locks the leaf's INDEXES** (mechanical, NEXT): acquireScanReadLockTxn
   (context.go:760) locks the table only; missing rows = _subpart_child_id_idx{,1}
   AccessShare|t in 1st s3getlocks. Lock the leaf's indexes during the scan-open.
3. **pg_stat_activity idle-query retention**: s.query empty for idle-in-txn backends;
   goopg clears Query on return to idle (activity/registry.go UpdateState
   `else if state=="idle"`); PG retains the most-recent query for idle-in-txn.
4. **Transactional-DDL cross-session catalog visibility** (MILESTONE-SIZED, shared
   with alter-table-4 / partition-concurrent-attach): 2nd s3getlocks must still show
   the dropped index's pg_class row + locks until s2commit; goopg removes from the
   shared in-memory catalog synchronously.

Next step: blocker (2) — SELECT taking AccessShare on the leaf's indexes during scan
open (mechanical). Then idle-query retention (3) + the txnl-DDL visibility milestone (4).

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: blockers 2/3/4 above.
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session
  catalog visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: real TOAST relations (reltoastrelid=0; text inline).
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.
