# 0118-0067 — DROP INDEX locks the partition tree (M0118-0008 `partition-drop-index-locking` enabler)

Status: accepted
Date: 2026-06-24
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency isolation specs)
Spec: `postgres/src/test/isolation/specs/partition-drop-index-locking.spec`

## Summary

**Enabler, NOT a promotion.** Make non-`CONCURRENTLY` `DROP INDEX` take a
transaction-scoped `AccessExclusiveLock` on the index's table and, recursively,
on every partition descendant — top-down — before dropping. This mirrors
PostgreSQL's `RangeVarCallbackForDropRelation` / `DROP INDEX` path, which "locks
all downward sub-partitions and partitions before locking the indexes" so a
concurrent session holding `ACCESS SHARE` on a leaf partition blocks the DROP.

Before this change goopg's `DROP INDEX` took **no** heavyweight lock on the
index's table, so step `s2drop` (`BEGIN; DROP INDEX …`) never blocked — the
spec's first divergence (`s2drop` completed immediately instead of
`<waiting ...>`). After it, `s2drop`/`s2dropsub` correctly park behind `s1`'s
`LOCK TABLE … IN ACCESS SHARE MODE` on the leaf partition and complete the
instant `s1commit` releases it, byte-matching PG's `<waiting ...>` /
`<... completed>` markers.

## Spec mechanics

```
s1: BEGIN; LOCK TABLE part_drop_index_locking_subpart_child IN ACCESS SHARE MODE;
s2: BEGIN; DROP INDEX part_drop_index_locking_idx;   -- on the TOP partitioned parent
```

The partition tree is `part_drop_index_locking` (range) →
`…_subpart` (range) → `…_subpart_child` (leaf). `s1` holds `ACCESS SHARE` on the
**leaf**. Upstream `DROP INDEX` of an index on the top parent descends the whole
tree taking `ACCESS EXCLUSIVE` (parent, then subpart, then subpart_child), and
blocks at the leaf because `AccessExclusive` conflicts with `s1`'s `AccessShare`.
`s2dropsub` (`DROP INDEX …_subpart_idx`, the index on the *sub*-partition) locks
`subpart` + `subpart_child` and blocks at the leaf the same way.

## Implementation

`internal/executor/operators_ddl.go`:

* `execDropIndex` — after resolving the `*catalog.Index` and gated on
  `!s.Concurrent`, call `o.lockDropIndexTableTree(idx)` before the catalog/heap
  mutation. `CONCURRENTLY` is excluded: it holds only `ShareUpdateExclusive` and
  must not block readers (`drop-index-concurrently-1`, already strict/passing).
* `lockDropIndexTableTree(idx)` — resolves the owning table via `idx.Table`,
  type-asserts the catalog to `*catalog.InMemory`, and walks the partition
  subtree.
* `lockPartitionSubtreeAccessExcl(im, tbl, visited)` — `AccessExclusive`-locks
  `tbl` via `acquireDDLLockTxn`, then recurses into `im.PartitionChildren(tbl.OID)`
  (which may themselves be partitioned), top-down. `visited` guards cycles /
  re-locking.

The lock goes through the existing `(*Context).acquireDDLLockTxn`
(transaction-scoped `tableLockMgr`), which `LOCK TABLE`'s own acquire
(`acquireRelLockTxn`) shares — so the two sessions' locks live on the same
manager and conflict correctly.

## Blast radius

`acquireDDLLockTxn` is a **no-op outside an explicit transaction**
(`TxnLockBackendID == 0`) and for system catalogs (`OID < 16384`). Ordinary
autocommit `DROP INDEX` (the regress suite, normal client usage) therefore takes
no new lock and keeps its historical non-blocking behaviour — the new blocking is
confined to explicit-transaction `DROP INDEX`, exactly the spec's shape.
`AccessExclusive` within a single transaction never self-blocks (no other holder),
and is released at COMMIT/ROLLBACK by `ReleaseTableLocks`.

## Why this does NOT promote the spec

The remaining divergence is `s3getlocks`: it queries `pg_locks JOIN pg_class JOIN
pg_stat_activity` and PG returns the per-relation `(mode, granted)` rows for both
the waiting `DROP INDEX` (the leaf row `granted = f`) and the `SELECT`'s
`AccessShare` holds. goopg's `pg_locks` (`relation_locks.go`) today surfaces only
the **explicit `LOCK TABLE` registry** (`globalRelLockMgr`, `pid` hardcoded `"0"`,
`granted` hardcoded `"t"`), NOT the real `tableLockMgr` holders/waiters that this
change creates — so `s3getlocks` returns `(0 rows)`. Closing the spec needs a
`pg_locks` → real-`tableLockMgr` bridge (enumerate all lock states with
per-backend `granted`/waiting flags + a `BackendID → pid` mapping that joins
`pg_stat_activity`), plus the partitioned-index child-creation naming the
expected output references (`…_subpart_child_id_idx` / `…_id_idx1`). Those are the
deferred next pieces (ledger).

## Gates

* Live probe (`partition-drop-index-locking.spec`): first divergence advanced from
  "`s2drop` does not wait" to `s3getlocks` `(0 rows)` — `s2drop`/`s2dropsub`
  `<waiting ...>` / `<... completed>` markers now byte-match PG.
* No regression: `TestPort_IsolationDropIndexConcurrently1` (excluded via
  `!s.Concurrent`), `TestPort_IsolationReindexConcurrently`,
  `TestPort_IsolationDetachPartitionConcurrently3`,
  `TestPort_IsolationCreateTrigger`, `TestPort_IsolationAlterTable1`,
  `TestPort_IsolationInheritTemp`, `TestPort_IsolationTruncateConflict`,
  `TestPort_IsolationClusterConflict` all PASS.
* `go test ./internal/executor/` PASS; `go build ./...` clean.
* pgbench TPC-B smoke = pre-commit hook (autocommit `DROP INDEX` is a no-op, so
  the hot path is unchanged).

## Oracle

`postgres/src/backend/commands/tablecmds.c` — `RangeVarCallbackForDropRelation`
(locks the relation and, for a partitioned relation/index, descends the partition
tree taking `AccessExclusiveLock` before dropping). Compared against
`./postgres/local_install` PG 18.3 expected output
`postgres/src/test/isolation/expected/partition-drop-index-locking.out`.
