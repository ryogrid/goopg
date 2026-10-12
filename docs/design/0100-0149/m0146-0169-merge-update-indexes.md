# M0146-0169 — a MERGE UPDATE maintains the indexes and checks uniqueness

Status: landed 2026-10-13 (`7958cf698`).
Parent: M0146-0159 (found by, and blocking, that task's units gate).

## Problem

After `MERGE … WHEN MATCHED THEN UPDATE`, every index access path missed the
updated row.

- With `enable_seqscan = off`, `SELECT … WHERE a = 1` returned no rows. A
  Seq Scan still found the row.
- A key change onto an existing key was accepted, leaving two live rows with
  the same primary key. PostgreSQL 18.3 raises 23505.

Plain UPDATE and `INSERT … ON CONFLICT DO UPDATE` were unaffected.

The defect surfaced through M0146-0159. Retiring the probe-cost multiplier
turned `TestDMLTriggersFireInPGOrder`'s final `SELECT … ORDER BY a` into an
ordered Index Scan on the primary key, and that scan lost the row the test's
MERGE had updated.

## PG behaviour

A MERGE update action goes through `ExecUpdate`
(`./postgres/src/backend/executor/nodeModifyTable.c`). For a non-HOT version
it inserts index tuples into every index (`ExecInsertIndexTuples`,
`./postgres/src/backend/executor/execIndexing.c`), and the unique indexes
enforce uniqueness there.

## Change

`mergeApplyUpdate` (`operators_merge.go`) wrote the new version non-HOT and
linked `t_ctid`, but did neither step. It now mirrors UPDATE's index path:

- **Unique check.** `checkUniqueIndexesForUpdate` runs after the old version
  is stamped dead and before the write. It is forced to probe every unique
  index for a cross-partition move.
- **Index entries.** `maintainUniqueIndexesForInsert` runs after the write
  and files an entry in every index of the destination table.
- **Destination table.** `routeModNewRow` now also returns the destination
  partition's table, so a cross-partition move maintains the destination's
  indexes.

## Verification

- **PG 18.3 probe** (`tmp/m0159/merge2.sql`). It covers the primary key and a
  secondary index after an update, a key change onto an existing key, a key
  change to a free key, and a cross-partition move under a partitioned index.
  The output is byte-identical to PG; HEAD differs in every arm.
- **New test.** `TestMergeUpdateMaintainsIndexes` (postmaster) fails on HEAD.
- **Regress.** `merge`, `with`, `triggers`, `updatable_views`, `returning` and
  `insert_conflict` are identical.
- **Isolation.** `merge-update`, `merge-delete`, `merge-insert-update`,
  `merge-match-recheck`, `merge-join` and `EvalPlanQual` PASS.
- **Gates.** Units; tpch-spotcheck; the acceptance arm (values identical);
  sf025 PASS=99.

## Residuals

Clusters that ran a MERGE update before this fix may hold heap rows with no
index entry, and possibly duplicate keys. `REINDEX` restores the missing
entries; duplicates need manual repair. Recorded in the task's escalation.
