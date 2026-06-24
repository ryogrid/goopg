# 0118-0074 — `DROP INDEX` defers catalog removal until COMMIT (M0118-0008 `partition-drop-index-locking` **PROMOTION**)

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Spec:** `postgres/src/test/isolation/specs/partition-drop-index-locking.spec`
- **Status:** accepted — **spec PROMOTED to pass-required** (`TestPort_IsolationPartitionDropIndexLocking`, `runIsoSpecStrict`)
- **Builds on:** enablers 0118-0067..0073 (partition-tree lock ordering, live `pg_locks` bridge, `SELECT` index locking, idle-query retention)

## Problem — the last blocker

The spec's observer session `s3` runs, twice, a join of `pg_locks → pg_class →
pg_stat_activity` filtered to `relname LIKE 'part_drop_index_locking%'`. The
*second* `s3getlocks` runs after `s2` has executed `DROP INDEX
part_drop_index_locking_idx` **inside an open transaction** (`s2begin … s2drop …
[second s3getlocks] … s2commit`). At that point PostgreSQL still reports the
dropped index's `pg_class` row, because the catalog tuple deletion is not visible
to other sessions' snapshots until `s2` commits — and `s2` holds an
`AccessExclusiveLock` on the index. So PG shows **6 rows**; goopg showed **5**.

Root cause: `execDropIndex` removed the index from the **shared in-memory
catalog synchronously** (`Catalog.DropIndex`), dropped its relfile, logged the
`DROP INDEX` WAL record and stamped `pg_class` xmax — all at statement time,
regardless of whether the statement ran inside an explicit transaction. goopg's
`pg_class` is served from the virtual builder over the live in-memory index set
(see memory `pg_class is virtual`), so the synchronous removal made the index's
`pg_class` row vanish for *every* session immediately, defeating the join.

This is the transactional-DDL cross-session catalog-visibility piece — the
blocker shared with `alter-table-4` / `partition-concurrent-attach`. A full
per-session MVCC catalog is milestone-sized, but the specific behaviour this spec
needs is bounded: **keep the dropped index in the catalog until the dropping
transaction commits**.

## Change — defer the removal to COMMIT

`DROP INDEX` (non-`CONCURRENTLY`) issued **inside an explicit transaction** now
defers its catalog/relfile/WAL/`pg_class`-xmax removal to COMMIT. The
`AccessExclusiveLock` taken by `lockDropIndexTree` (0118-0071) is already held to
commit, so until `s2` commits the index stays in the shared catalog (its
`pg_class` row remains visible to `s3`) and `s2` is shown holding the lock —
matching PG. In **autocommit** (`!InExplicitTransaction`) the removal stays
immediate: ordinary `DROP INDEX` keeps its historical non-blocking behaviour and
nothing changes.

### Mechanism

- **`executor/session.go`** — new `PendingIndexDrop` record (lookup name, OID,
  schema, bare name, relfile, savepoint depth) and a `BasicSession.pendingIndexDrops`
  slice with `AddPendingIndexDrop` / `TakePendingIndexDrops` /
  `CancelPendingIndexDropsToDepth`. `EndExplicitTransaction` now also nils the
  slice — the safety net that discards deferred drops on **any** rollback path
  (executor `execRollback`, server dispatch `TxRollback`, SSI/FK pre-commit
  aborts), all of which funnel through `EndExplicitTransaction`
  (`connTxState.End → sess.EndExplicitTransaction`).
- **`executor/operators_ddl.go`** — `execDropIndex` computes `deferSess` (the
  `*BasicSession` when `!s.Concurrent && InExplicitTransaction`); for each named
  index it still takes the full `lockDropIndexTree` lock set, then — when
  deferring — records a `PendingIndexDrop` and `continue`s instead of touching
  the catalog. The new `ApplyPendingIndexDrops(ctx, sess)` performs the real
  removal at commit, mirroring the immediate path exactly (catalog `DropIndex`,
  pool invalidate + `DropRelation`, `EncodeDropIndex` WAL, `SetRelcacheInvalPending`,
  `pg_class` xmax stamp via `MaterializeWriterXID` + `deleteCatalogRowsForOID`).
- **Commit paths (both, sibling paths kept in sync):** `ApplyPendingIndexDrops`
  is invoked **before** `TxnMgr.Commit` in (a) the executor `transactionOp.execCommit`
  and (b) the server simple-query dispatch `TxCommit` branch (which bypasses
  `execCommit`). Running before the commit ensures the drop's WAL precedes the
  commit record and the `pg_class` xmax stamp uses the still-live transaction XID.
  The isolation runner sends simple queries, so this spec exercises path (b).
- **Savepoints:** `ROLLBACK TO SAVEPOINT` (executor `rollbackToSavepointOp`)
  calls `CancelPendingIndexDropsToDepth(newDepth)` so a `DROP INDEX` issued inside
  a rolled-back savepoint is not still applied at the outer COMMIT — mirroring the
  existing `RollbackDDLDropsToDepth` convention for DROP TABLE.

## Scope / known limitation

The deferral keeps the index visible to **all** sessions (including the dropping
session) until commit, because goopg's catalog is shared and not yet per-session
MVCC-filtered. This spec never re-queries the index from `s2` after the drop, so
the behaviour is byte-identical to PG here. Full same-session "the dropper sees
it gone" visibility is the remaining MVCC-catalog milestone (still required by
`alter-table-4` and `partition-concurrent-attach`); this slice closes only the
cross-session-visibility-until-commit half that `partition-drop-index-locking`
needs. The change is correctness-positive regardless: deferring to commit also
makes a `DROP INDEX` + `ROLLBACK` naturally restore the index (no replay needed).

## Verification

- `TestPort_IsolationPartitionDropIndexLocking` (`runIsoSpecStrict`) — **PASS**,
  byte-identical to PG 18.3 across both permutations.
- No regression: `TestPort_IsolationDropIndexConcurrently1` (CONCURRENTLY
  excluded from deferral), `ReindexConcurrently`, `ReindexSchema`, `MultipleCic`.
- `go test ./internal/executor/` (full package) + `-race` on the
  DROP INDEX / transaction / savepoint / commit tests — PASS.
- `TestPort_RegressSuite` (autocommit `DROP INDEX` unaffected) — PASS.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

Mirrors PostgreSQL's transactional-DDL visibility: `index_drop` /
`RemoveRelations` delete the `pg_class`/`pg_index` tuples within the current
transaction's command, but the deletion is MVCC and only becomes visible to other
snapshots at commit, while `AccessExclusiveLock` is held to end-of-transaction
(`src/backend/catalog/index.c`, `src/backend/commands/tablecmds.c`
`RangeVarCallbackForDropRelation`).
