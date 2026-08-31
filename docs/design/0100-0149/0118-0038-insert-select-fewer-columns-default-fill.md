# 0118-0038 — `INSERT … SELECT` with fewer source columns than the table (crash fix + default-fill)

**Status:** accepted
**Date:** 2026-06-23
**Milestone:** M0118-0008 / M0118-0002 (isolation spec suite pass-through; this is an
enabler, not a spec promotion)
**Related spec:** `postgres/src/test/isolation/specs/index-only-bitmapscan.spec`
(global-setup crash; see "Probe ranking" below)

## Summary

Fixes a server-crashing correctness bug: `INSERT INTO t SELECT …` with **no
explicit column list**, where the `SELECT` produces **fewer** columns than the
table, panicked the backend with `index out of range` instead of filling the
trailing target columns with their `DEFAULT`.

PostgreSQL semantics (CREATE/transformInsertStmt): with no column list the
target columns are the table's columns in declared order, *or the first N of
them if only N expressions are supplied*; the remaining columns fall back to
their `DEFAULT` (or `NULL`). A source **wider** than the target is the error
`INSERT has more expressions than target columns`.

## The bug

`planInsert` (`internal/planner/planner.go`) built `ColumnIndex` from the table
columns (all non-generated columns) when no explicit column list was given, with
**no regard for the source arity**. For `INSERT INTO ios_bitmap SELECT g.i, g.i`
into `ios_bitmap(a, b, pad char(1024) DEFAULT '')`, `ColumnIndex = [0,1,2]` but
the `SELECT` row has width 2.

The executor (`insertOp.Next`, `operators_storage.go:1187`) then ran

```go
for srcIdx, tgtIdx := range o.plan.ColumnIndex { // srcIdx = 0,1,2
    row[tgtIdx] = src[srcIdx]                     // src[2] → panic, len(src)==2
}
```

→ `runtime error: index out of range [2] with length 2`, which the backend
panic handler turned into a dropped connection (`driver: bad connection`).

The crash is type-independent (`char(N)`, `text` all reproduce) and triggered
by any `INSERT … SELECT`/sub-query narrower than the table when no column list
is given. The VALUES path was unaffected — it already validates/pads arity in
`rewriteInsertDefaultMarkers` + the `len(r) != len(colIndex)` check.

## Fix

In `planInsert`, after planning the `SELECT` source, reconcile its width
(`sel.Output()`) with `colIndex`:

- `srcWidth > len(colIndex)` → `PlanError` `42601`
  *"INSERT has more expressions than target columns"* (both explicit and
  implicit column list).
- explicit column list **and** `srcWidth < len(colIndex)` → `PlanError` `42601`
  *"INSERT has more target columns than expressions"* (the symmetric error;
  truncation never applies to an explicit list).
- implicit column list and `srcWidth < len(colIndex)` → **truncate**
  `colIndex = colIndex[:srcWidth]`. The trailing columns are then left flagged
  in the executor's `insertMissing` slice, so the existing
  `applyDefaultsForMissing` rung fills their `DEFAULT` (or `NULL`).

SQLSTATE `42601` matches upstream `ERRCODE_SYNTAX_ERROR` for both arity errors
(`src/backend/parser/analyze.c:1071,1089`).

## Tests / gates

- Planner units (`internal/planner/planner_test.go`):
  `TestPlanInsertSelectFewerColumnsTruncatesColumnIndex` (ColumnIndex truncated
  to `[0,1]`), `TestPlanInsertSelectMoreColumnsErrors` (42601),
  `TestPlanInsertSelectExplicitListArityMismatchErrors` (42601).
- End-to-end: `INSERT INTO t SELECT g, g*10 FROM generate_series(1,3)` into
  `t(a,b,pad text default 'D')` yields `(1,10,'D'),(2,20,'D'),(3,30,'D')` —
  trailing default populated, rows readable (verified via throwaway probe).
- `go test ./internal/planner/ ./internal/executor/` green; pgbench CI-parity
  smoke 0-failed.

## Probe ranking — M0118-0008 remaining tail (recorded so future loops don't re-probe)

All 16 remaining M0118-0008 specs were probed (`RunAndCompare` first-divergence).
None is a single-loop promotion; each needs a milestone-sized subsystem:

| spec(s) | blocker (root, not surface) |
|---|---|
| `alter-table-1/2` | `ADD CONSTRAINT … NOT VALID` parse + `VALIDATE CONSTRAINT` + transactional DDL catalog visibility |
| `alter-table-4` | transactional DDL catalog visibility — goopg mutates the shared catalog immediately, so uncommitted `NO INHERIT`/`INHERIT` is visible to a concurrent session (returned `sum=1`, expected `11`); also per-child blocking scan locks |
| `truncate-/vacuum-/cluster-conflict{,-partition}` | table-level **privilege enforcement** for TRUNCATE/VACUUM/CLUSTER + `GRANT`/`SET ROLE` (goopg does not enforce ACLs). Setup also hits the CREATE-ROLE-batch-swallow bug below |
| `reindex-concurrently-toast` | real TOAST relations (`reltoastrelid`, `pg_toast.*`) + `allow_system_table_mods` GUC |
| `detach-partition-concurrently-1/2/3/4` | `DETACH PARTITION … CONCURRENTLY` parse + concurrent detach + `pg_backend_pid()`/cancel infra |
| `partition-concurrent-attach` | multi-level partitioning + default partitions + `ATTACH PARTITION` constraint revalidation + transactional attach visibility |
| `partition-drop-index-locking` | `pg_locks` view populated from `tableLockMgr` + recursive partition-tree `AccessExclusiveLock` on DROP INDEX |
| `vacuum-no-cleanup-lock` | faithful `pg_class.reltuples`/`relpages` accounting through VACUUM + buffer-pin cleanup-lock semantics + `vacuum_multixact_freeze_min_age` GUC |
| `plpgsql-toast` | TOAST detoasting + `COMMIT` inside PL/pgSQL procedures + VACUUM toast-chunk removal |

Adjacent bug found while probing (not yet fixed): in the parser-fail recovery
path (`dispatch.go` ~L131), `tryHandleRoleDDL` consumes an entire simple-query
batch `CREATE ROLE x; CREATE TABLE y;` and silently drops everything after the
role DDL (the parser rejects `CREATE ROLE`, so the whole string routes to the
role handler). This is why the `*-conflict` setups leave the table missing. A
proper fix needs statement-splitting in the recovery path; recorded in the
deferral ledger.
