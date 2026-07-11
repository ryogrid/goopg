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

## Sibling: `pg_stat_all_indexes` / `pg_stat_user_indexes` / `pg_stat_sys_indexes`

The per-index access-statistics views are the direct sibling of the per-table
views and land the same way. Upstream (`system_views.sql`):

```sql
CREATE VIEW pg_stat_all_indexes AS
    SELECT C.oid AS relid, I.oid AS indexrelid, N.nspname AS schemaname,
           C.relname AS relname, I.relname AS indexrelname,
           pg_stat_get_numscans(I.oid)          AS idx_scan,
           pg_stat_get_lastscan(I.oid)          AS last_idx_scan,
           pg_stat_get_tuples_returned(I.oid)   AS idx_tup_read,
           pg_stat_get_tuples_fetched(I.oid)    AS idx_tup_fetch
    FROM pg_class C JOIN pg_index X ON C.oid = X.indrelid
                    JOIN pg_class I ON I.oid = X.indexrelid
                    LEFT JOIN pg_namespace N ON N.oid = C.relnamespace
    WHERE C.relkind IN ('r', 't', 'm');
```

`pg_stat_{user,sys}_indexes` reuse the identical `schemaname` split as the table
views (keyed on the parent table's schema), so the row builder reuses the same
`StatTableScope`.

- Row builder — `catalog.PGStatIndexesRowsForDBOid(dbOid, scope)`: enumerates
  `AllIndexes(dbOid)`, filters each index's parent table (`idx.Table`) through
  the *same* relation predicate as `PGStatTablesRowsForDBOid` so the two views
  agree on the underlying relkind `r`/`m`/`p` set, and emits the 9-column row
  `relid / indexrelid / schemaname / relname / indexrelname / idx_scan /
  last_idx_scan / idx_tup_read / idx_tup_fetch`. The five identity cells are real;
  the three scan counters are a faithful `0` and `last_idx_scan` is `NULL`
  (honest-0/NULL, matching the table views and `pg_stat_io`). Rows are sorted by
  `(schemaname, relname, indexrelname)` since `AllIndexes` order is map-derived.
- Per-connection scoping — `executor.fetchStatIndexesRows(ctx, scope)`: the exact
  `ctx.Catalog`-unwrap + `ctx.CurrentDatabaseOid` twin of `fetchStatTablesRows`,
  swapped in at `valuesOp.Open`'s three new `pg_stat_*_indexes` branches; the
  static `VirtualRows` fallback scopes to `DefaultDBOid`.

Tests: `internal/catalog/pgstat_indexes_test.go`
(`TestPGStatIndexesRowsBasicShape` — 9-col width, real identity cells, counters
`0`/`last_idx_scan` NULL; `TestPGStatIndexesScopeFilter` — public-schema index in
all+user, not sys) and `internal/executor/pgstat_indexes_e2e_test.go`
(`TestPgStatUserIndexesEndToEnd` full planner→executor resolution;
`TestPgStatSysIndexesExcludesUserIndex` schemaname split end-to-end).

## Deferred

The scan/tuple/vacuum/analyze counters and `last_*` timestamps remain honest
zeros/NULLs until goopg grows a cumulative per-table statistics subsystem
(`PgStat_StatTabEntry` analog). The index views share the same gap: `idx_scan` /
`idx_tup_read` / `idx_tup_fetch` / `last_idx_scan` stay `0`/`NULL` until a
`PgStat_StatIndEntry` analog exists. Because system catalogs are storage-less and
carry no user indexes, `pg_stat_sys_tables` and `pg_stat_sys_indexes` return no
rows even though upstream lists the catalog relations. All are recorded in
`.ralph/deferral_ledger.md` with a resume point.
