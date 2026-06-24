# 0118-0071 — `DROP INDEX` locks the index relation tree (M0118-0008 partition-drop-index-locking enabler)

Status: **enabler landed** (NOT a spec promotion). Part of the
`partition-drop-index-locking` chain (after 0118-0067 DROP INDEX partition-tree
locking, 0118-0068 CREATE INDEX recursion, 0118-0069 `LockManager.AllLocks()`,
0118-0070 live `pg_locks` bridge). Closes "blocker 1" of the spec's remaining
divergence.

## Problem

After 0118-0070 the live `pg_locks` bridge surfaced the real relation locks
`DROP INDEX` takes, so the spec's first `s3getlocks` advanced to 4 of 7 expected
rows. Three rows were still missing, two of which come from `DROP INDEX` itself:

```
DROP INDEX part_drop_index_locking_idx; | part_drop_index_locking_idx                  | AccessExclusiveLock | t
... and in the second s3getlocks (drop completed) the child indexes:
DROP INDEX part_drop_index_locking_idx; | part_drop_index_locking_subpart_child_id_idx | AccessExclusiveLock | t
DROP INDEX part_drop_index_locking_idx; | part_drop_index_locking_subpart_id_idx       | AccessExclusiveLock | t
```

`lockDropIndexTableTree` (0118-0067) locked the index's heap **table** tree
(parent → sub-partition → leaf) but never the **index** relations themselves, so
`part_drop_index_locking_idx` and its partition-descendant child indexes never
appeared in `pg_locks`.

PostgreSQL's `RangeVarCallbackForDropRelation` locks the **target index first**
(before descending the table tree) and the **descendant child indexes after** the
table tree (so when the DROP blocks on a leaf partition held by a concurrent
reader, the snapshot shows the target index granted and the child indexes absent
— they are acquired only once the DROP unblocks).

## Change

`execDropIndex`'s `!s.Concurrent` arm now calls a new `lockDropIndexTree(idx)`
that acquires locks in PG's order:

1. `lockDropIndexSelf` — `AccessExclusiveLock` on `idx`'s own `RelFileNode`
   (`IndexRelFileNode`). Granted before any blocking.
2. `lockDropIndexTableTree` — the pre-existing heap partition-tree walk
   (`lockPartitionSubtreeAccessExcl`), which **blocks** on a leaf partition a
   concurrent reader holds in `ACCESS SHARE`.
3. `lockDropIndexChildren` — recurses `InMemory.IndexPartitionChildren`
   (the parent→child index linkage 0118-0068 built via `PartitionParentOID` /
   `RegisterIndexPartitionChild`), `AccessExclusiveLock` on each descendant child
   index. Reached only after step 2 unblocks.

Each acquire goes through `acquireDDLLockTxn`, so it is a **no-op in autocommit**
(`TxnLockBackendID==0`) and for system catalogs — ordinary `DROP INDEX` keeps its
historical non-blocking behaviour. The new index locks are real `tableLockMgr`
locks, surfaced by the 0118-0070 bridge with a real PID; no bridge/dedup change
was needed.

The ordering is load-bearing: because `acquireRelLockTxn` genuinely blocks
(`AcquireWithTimeout`), locks taken before the leaf-partition block are visible in
a concurrent `pg_locks` snapshot and locks taken after are not — exactly matching
PG's first-`s3getlocks` (target index granted, child indexes absent) and
second-`s3getlocks` (child indexes granted once the DROP completes).

## Effect (live probe, 2026-06-24)

First `s3getlocks` now contains `part_drop_index_locking_idx`
`AccessExclusiveLock | t`; the second contains
`part_drop_index_locking_subpart_child_id_idx` and `..._subpart_id_idx`
`AccessExclusiveLock | t`. The DROP-side rows match PG.

## Does NOT promote `partition-drop-index-locking`

Remaining blockers (deferral ledger):

2. **SELECT locks the leaf's indexes** — `acquireScanReadLockTxn` (0118-0018)
   locks only the table; the two `…_subpart_child_id_idx{,1}` `AccessShare | t`
   rows are still missing from the first `s3getlocks`.
3. **`pg_stat_activity` idle-query retention** — goopg clears `query` to empty on
   return to idle; PG retains the most-recent query for idle-in-transaction
   backends, so the SELECT/DROP rows show an empty `query` column.
4. **Transactional-DDL cross-session catalog visibility** (milestone-sized,
   shared with `alter-table-4` / partition-concurrent-attach) — after the DROP
   completes but before `s2commit`, the second `s3getlocks` must still see the
   dropped index's `pg_class` row (`part_drop_index_locking_idx`) and lock; goopg
   removes it from the shared in-memory catalog synchronously, so the
   `JOIN pg_class` drops that row.

## Gates

- New `internal/executor/partition_drop_index_lock_test.go`
  (`TestDropIndexLocksIndexRelationTree`): builds the partition + index tree,
  runs `DROP INDEX` under an explicit-txn backend, asserts `tableLockMgr` holds
  `AccessExclusiveLock` on the target index, its two partition-descendant child
  indexes, and the heap partition tree — and **not** on a child index belonging to
  a different index tree.
- `TestCreateIndexRecursesPartitionTree` still green (linkage unchanged).
- No regression across `TestPort_IsolationDropIndexConcurrently1` /
  `LockNowait` / `AlterTable1` / `AlterTable2` / `AlterTable3` / `CreateTrigger`
  (59 s); `go test -race ./internal/lockmgr/`; `go build ./...` clean; pgbench
  smoke = pre-commit hook.
