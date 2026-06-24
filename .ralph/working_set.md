Loop #26: M0118-0008 — CREATE INDEX partition-tree recursion enabler (design 0118-0068).
COMMITTED + pushed at loop end. NOT a promotion.

## What landed (enabler)
CREATE INDEX on a partitioned table now recurses into every existing partition
descendant, building a matching child index on each and attaching it to its
immediate parent index (PartitionParentOID + RegisterIndexPartitionChild, so
pg_inherits keeps children out of standalone pg_dump). Before this, execCreateIndex
built exactly ONE index on the named relation and ignored partitions — so the spec's
child indexes (`..._subpart_child_id_idx`, `..._id_idx1`) never existed. Children
auto-named `<partition>_<col>_idx`, deduped `_idx1` via autoIndexNameWithIncludes.

Files: internal/executor/operators_ddl.go (execCreateIndex wiring at "if tbl.PartitionMethod
!= ''" before the CONCURRENTLY drain + new recursive createPartitionChildIndexes helper
right after execCreateIndex), internal/executor/partition_create_index_recurse_test.go (new),
docs/design/0118-0068-create-index-partition-tree-recursion.md + README index,
.ralph/deferral_ledger.md.

Key symbols: execCreateIndex, createPartitionChildIndexes, createBTreeIndex,
autoIndexNameWithIncludes, catalog.InMemory.PartitionChildren / RegisterIndexPartitionChild /
IndexPartitionChildren, Index.PartitionParentOID, Table.PartitionMethod.

Gates: new TestCreateIndexRecursesPartitionTree PASS (3 child indexes + names +
PartitionParentOID linkage + parent direct-child registration); go test
./internal/executor/ ./internal/catalog/ PASS; go build ./... clean; pgbench smoke =
pre-commit hook.

## partition-drop-index-locking remaining blockers (resume point)
Two blockers remain for full promotion (ledger has the map):
1. **pg_locks → real tableLockMgr bridge** (the broadly-reusable one, its own loop):
   add `LockManager.AllLocks()` enumerating `lm.states` (tag → per-backend mode mask
   for holders=granted-t + waiters=granted-f), wire into catalog.RelationLockRowsFunc
   ALONGSIDE globalRelLockMgr, with a BackendID→pid map (TxnLockBackendID→session pid)
   so rows join pg_stat_activity. ALSO: SELECT's implicit AccessShare locks on a
   table's indexes are not surfaced. CAUTION: LOCK TABLE records in BOTH globalRelLockMgr
   AND tableLockMgr (operators_ddl.go:12459-12462) — naive add double-counts; must
   unify/dedup, and the change touches currently-PASSING pg_locks specs (lock-nowait,
   alter-table-*, vacuum/truncate/cluster-conflict) so re-run those.
2. **DROP INDEX cascade to child indexes**: 0118-0067 locks the partition TABLE tree
   (lockPartitionSubtreeAccessExcl) but does not cascade through IndexPartitionChildren
   to lock/drop the child indexes this enabler (0118-0068) creates. The 2nd s3getlocks
   shows the completed DROP holding AccessExclusive on every child index it removed.

## M0118-0008 hard tail (all Effort-L, deferred)
- partition-drop-index-locking: pg_locks bridge + DROP-index cascade (above).
- alter-table-4 + partition-concurrent-attach: transactional-DDL cross-session catalog
  visibility (milestone-sized MVCC catalog subsystem).
- reindex-concurrently-toast: needs real TOAST relations (reltoastrelid=0; text inline);
  post-0118-0066 wall = routine_column_usage 42P01.
- WHERE CURRENT OF positioned UPDATE/DELETE: project-wide, parsed (CurrentOf) no executor site.

Next step: the pg_locks→real-tableLockMgr bridge (blocker 1) — broadly reusable, but
mind the double-record + passing-spec regression risk; budget a full loop with the
lock-table spec suite as the gate.
