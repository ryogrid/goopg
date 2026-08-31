# 0118-0072 — SELECT locks the scanned relation's indexes (partition-drop-index-locking blocker 2)

**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
**Spec:** `partition-drop-index-locking` — enabler, NOT a promotion.
**Status:** landed. Closes **blocker 2 of 4** (see 0118-0071 for the blocker map).

## Problem

PostgreSQL locks every index of a scanned relation in `AccessShare` — not only
the index a chosen plan probes. `get_relation_info` opens (`index_open`) all of a
relation's indexes during planning, taking `AccessShareLock` for a read, and the
lock is held for the life of the transaction. goopg's scan-open hooks
(`acquireScanReadLockTxn`, design 0118-0018) locked only the heap relation, so a
bare `SELECT` on a leaf partition never appeared in `pg_locks` holding a lock on
its indexes.

The `partition-drop-index-locking` spec's first `s3getlocks` snapshot expects the
open `SELECT * FROM part_drop_index_locking_subpart_child` to hold
`AccessShareLock | t` on the leaf table **and** both of its inherited indexes
(`…_subpart_child_id_idx`, `…_subpart_child_id_idx1`). Those two rows were missing
— blocker 2.

## Change

New helper `(*Context).acquireScanIndexReadLocksTxn(tbl *catalog.Table)`
(`internal/executor/context.go`): enumerates the relation's indexes
(`Catalog.IndexesOnTable`) and takes an `AccessShare` lock on each via the
existing `acquireScanReadLockTxn` hook (`Catalog.IndexRelFileNode`). It inherits
that hook's confinement exactly — held to end-of-transaction inside an explicit
transaction block, acquired+released transiently in autocommit, system catalogs
skipped — so it adds no held lock on the common autocommit read path.

Wired into all three scan-open paths that already take the table-level
`AccessShare` lock, keeping the sibling paths in sync (sibling-paths discipline):

- seq scan — `operators_storage.go` (`o.tbl`)
- index scan — `operators_index.go` `openPrep` (`o.plan.Table`)
- index-only scan — `operators_indexonly.go` (`o.plan.Table`)

`AccessShare` is self-compatible and conflicts only with `AccessExclusive`, so
concurrent reads never block each other; the only new cross-session block is a
reader parking behind an `AccessExclusive` index holder — i.e. exactly the
`DROP INDEX` the spec exercises (which `AccessExclusive`-locks the index relation
tree, design 0118-0071).

## Effect (live probe, 2026-06-24)

The first `s3getlocks` now shows the open SELECT holding `AccessShareLock | t` on
`part_drop_index_locking_subpart_child`, `…_subpart_child_id_idx` and
`…_subpart_child_id_idx1` — the three rows that were previously absent. The
DROP-side `AccessExclusive` rows (0118-0071) are unchanged.

## Does NOT promote `partition-drop-index-locking`

Remaining blockers (deferral ledger):

3. **`pg_stat_activity` idle-query retention** — goopg clears `query` to empty on
   return to idle (and drops the trailing `;`); PG retains the most-recent query
   text for idle-in-transaction backends, so the `query` column diverges.
4. **Transactional-DDL cross-session catalog visibility** (milestone-sized,
   shared with `alter-table-4` / partition-concurrent-attach) — after the DROP
   completes but before `s2commit`, the second `s3getlocks` must still see the
   dropped index's `pg_class` row + lock; goopg removes it from the shared
   in-memory catalog synchronously, so the `JOIN pg_class` drops that row (5 rows
   vs PG's 6).

## Blast radius

Every seq / index / index-only scan now also locks the relation's indexes. On the
hot path (autocommit pgbench / TPC-H) the lock is transient (acquire+immediate
release) and `AccessShare` is granted instantly unless a concurrent
`AccessExclusive` index DDL is in flight, so steady-state TPS is unaffected
(pgbench smoke 0-failed). Inside an explicit transaction the lock is held to
commit, matching PG.

## Gates

- New `TestSelectLocksLeafPartitionIndexes` (`partition_drop_index_lock_test.go`):
  builds the partition + index tree, runs `SELECT * FROM …_subpart_child` under an
  explicit-txn backend, asserts `tableLockMgr` holds `AccessShareLock` on the leaf
  table and both leaf indexes.
- `TestDropIndexLocksIndexRelationTree` / `TestCreateIndexRecursesPartitionTree`
  still green.
- No regression: `TestPort_Isolation{ReindexConcurrently,ReindexSchema,
  MultipleCic,DropIndexConcurrently1,InheritTemp,CreateTrigger,TruncateConflict,
  VacuumConflict,ClusterConflict,AlterTable1,AlterTable2,AlterTable3}`.
- `go build ./...` clean; `go test ./internal/executor/`; `-race ./internal/lockmgr/`;
  pgbench smoke (pre-commit hook).
