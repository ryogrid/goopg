# 0122-0003 — `pg_stat_all_tables` / `pg_stat_user_tables` / `pg_stat_sys_tables`

Status: accepted
Milestone: M0122-0003 (EXPLAIN output & pg_stat instrumentation)

## Problem

Client tools and monitoring queries read PostgreSQL's per-table access
statistics views — `pg_stat_all_tables`, `pg_stat_user_tables`,
`pg_stat_sys_tables` (upstream `src/backend/catalog/system_views.sql`). goopg
previously had no such views, so `SELECT ... FROM pg_stat_user_tables` failed
with an unknown-relation error, breaking dashboards and `\d`-adjacent tooling
that probes them.

## Upstream shape

In PostgreSQL these three views are thin `SELECT`s over `pg_stat_all_tables`,
which itself joins `pg_class C` (relkind `IN ('r','t','m','p')`) to
`pg_index I` and calls the `pg_stat_get_*` accessor functions backed by the
cumulative statistics system (`PgStat_StatTabEntry`). The split between
`user`/`sys` is purely a `schemaname` predicate:

- `pg_stat_sys_tables`: `schemaname IN ('pg_catalog','information_schema')
  OR schemaname ~ '^pg_toast'`
- `pg_stat_user_tables`: the negation of the above.

The view has 26 columns: `relid`, `schemaname`, `relname`, then the
scan/tuple/vacuum/analyze counters and `last_*` timestamps.

## goopg approach

goopg has **no incremental pgstat mutation counters** (no
`PgStat_StatTabEntry`), the same limitation that makes `pg_stat_io` emit honest
zeros for the I/O it does not instrument. This design follows that established
precedent rather than inventing a counter subsystem:

- `relid` / `schemaname` / `relname` are **real** (from the catalog `Table`).
- `n_live_tup` is backed by the table's ANALYZE-persisted live-tuple estimate
  (`TableStats.RowCount` / `reltuples`) — the best available signal absent a
  live counter.
- Every other per-tuple/scan counter is a faithful `0`; every `last_*`
  timestamp is `NULL`.

### Row builder — `catalog.PGStatTablesRowsForDBOid`

`internal/catalog/catalog.go` gains `StatTableScope`
(`StatScopeAll`/`StatScopeUser`/`StatScopeSys`) and
`PGStatTablesRowsForDBOid(dbOid, scope)`. It walks one database namespace's
tables under the catalog `RLock`, in sorted-key order for determinism, applying
the **same relation filter as `PGClassRowsForDBOid`** so the two agree on the
relkind `r`/`m`/`p` set: system-catalog virtual tables, sequences, plain (non
-materialized) views and foreign tables are skipped. The `scope` then applies
the upstream `schemaname` split via `isSystemStatSchema` (`pg_catalog`,
`information_schema`, `pg_toast*`).

goopg's system catalogs are storage-less virtual tables with no `TableStats`,
so `pg_stat_sys_tables` is effectively empty — a documented deferral, recorded
in the ledger.

### Per-connection database scoping — `executor.fetchStatTablesRows`

Like `pg_class` and `pg_stat_io`, these views must list the **connecting
database's own** tables, not always `DefaultDBOid`'s. The static catalog
`VirtualRows` fallback registered in `registerSystemTables` scopes to
`DefaultDBOid`; the live row set is swapped in at `valuesOp.Open`
(`internal/executor/operators.go`) by `fetchStatTablesRows(ctx, scope)`, which
unwraps `ctx.Catalog` (possibly a search-path wrapper) to the concrete
`*catalog.InMemory` and calls `PGStatTablesRowsForDBOid` with the connection's
`CurrentDatabaseOid`. This mirrors the existing `pg_class`/`pg_stat_io`
branches exactly.

## Tests

- `internal/catalog/pgstat_tables_test.go`:
  `TestPGStatTablesRowsBasicShape` (26-col width, real relid/schemaname/relname,
  `n_live_tup` = reltuples, untracked counters 0 / `last_*` NULL),
  `TestPGStatTablesScopeFilter` (public-schema table in all+user, not sys),
  `TestPGStatTablesExcludesNonTableRelkinds` (system-catalog virtual tables and
  sequences do not leak).
- `internal/executor/pgstat_tables_e2e_test.go`:
  `TestPgStatUserTablesEndToEnd` (full planner→executor SELECT resolves the view,
  the per-connection branch fires, the connecting DB's `items` table projects
  with real relname/schemaname and non-NULL `n_live_tup`),
  `TestPgStatSysTablesExcludesUserTable` (schemaname split end-to-end).

## Deferred

The scan/tuple/vacuum/analyze counters and `last_*` timestamps remain honest
zeros/NULLs until goopg grows a cumulative per-table statistics subsystem
(`PgStat_StatTabEntry` analog). Because system catalogs are storage-less,
`pg_stat_sys_tables` returns no rows even though upstream lists the catalog
relations. Both are recorded in `.ralph/deferral_ledger.md` with a resume
point.
