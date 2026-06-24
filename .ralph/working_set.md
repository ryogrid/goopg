Loop #25: M0118-0008 — `DROP INDEX` partition-tree locking enabler (design 0118-0067).
COMMITTED + pushed at loop end. NOT a promotion.

## What landed (enabler)
Non-CONCURRENTLY `DROP INDEX` now takes a txn-scoped `AccessExclusiveLock` on the
index's table + recursively on every partition descendant (top-down) before the
catalog/heap drop, mirroring PG `RangeVarCallbackForDropRelation`. New
`execDropIndex` call (gated `!s.Concurrent`) → `lockDropIndexTableTree(idx)` →
`lockPartitionSubtreeAccessExcl(im, tbl, visited)` (locks `idx.Table` then recurses
`im.PartitionChildren`, `acquireDDLLockTxn(AccessExclusive)` each). Rides the same
`tableLockMgr` as `LOCK TABLE` (`acquireRelLockTxn`), so `partition-drop-index-locking`'s
`s2drop`/`s2dropsub` now `<waiting ...>` behind `s1`'s ACCESS SHARE on the leaf and
complete on `s1commit` — byte-match PG. No-op in autocommit (TxnLockBackendID==0) ⇒
zero hot-path blast radius; CONCURRENTLY excluded.

Files: internal/executor/operators_ddl.go (execDropIndex + lockDropIndexTableTree +
lockPartitionSubtreeAccessExcl), docs/design/0118-0067-drop-index-partition-tree-locking.md
+ README index, .ralph/deferral_ledger.md + fix_plan note.

Key symbols: execDropIndex, lockDropIndexTableTree, lockPartitionSubtreeAccessExcl,
acquireDDLLockTxn, tableLockMgr, catalog.InMemory.PartitionChildren, idx.Table.

Gates: live probe — first divergence advanced "s2drop does not wait" → s3getlocks
returns 0 rows (the pg_locks bridge). No regression: DropIndexConcurrently1 (excluded),
ReindexConcurrently, DetachPartitionConcurrently3, CreateTrigger, AlterTable1,
InheritTemp, TruncateConflict, ClusterConflict all PASS. go test ./internal/executor/
PASS; go build ./... clean; pgbench smoke = pre-commit hook.

## partition-drop-index-locking next step (resume point)
The ONLY remaining divergence is `s3getlocks` (returns 0 rows). Needs:
1. pg_locks → real-tableLockMgr bridge: add `LockManager.AllLocks()` enumerating
   `lm.states` (tag → per-backend mode mask + waiters), wire into
   `catalog.RelationLockRowsFunc` ALONGSIDE the existing globalRelLockMgr rows, with
   real `granted` (t for holders, f for waiters) and a `BackendID→pid` map so the
   rows join pg_stat_activity. Today relation_locks.go hardcodes pid="0", granted="t".
2. Partitioned-index child-index creation with PG auto-naming
   (`<table>_<col>_idx`, deduped `_idx1`) — expected output references
   `..._subpart_child_id_idx` / `..._id_idx1`.

## M0118-0008 hard tail (all Effort-L, deferred — ledger has full blocker maps)
- partition-drop-index-locking: pg_locks bridge + partitioned-index children (above).
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session catalog
  visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: needs real TOAST relations (reltoastrelid=0; text inline);
  post-0118-0066 wall = routine_column_usage 42P01.
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.

Next step: pick another bounded enabler. The pg_locks→real-tableLockMgr bridge (item 1
above) is the most broadly reusable and is now the SOLE blocker for promoting
partition-drop-index-locking — strong candidate for next loop.
