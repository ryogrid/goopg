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

## I/O sibling: `pg_statio_all_tables` / `pg_statio_{user,sys}_tables` and `pg_statio_all_indexes` / `pg_statio_{user,sys}_indexes`

The `pg_statio_*` views are the buffer-pool I/O counterparts of the access-stat
views above, reporting per-relation heap/index/toast block reads and hits.
Upstream (`system_views.sql`, `WHERE C.relkind IN ('r','t','m')`) columns:

```sql
-- pg_statio_all_tables (11 cols)
relid, schemaname, relname,
heap_blks_read, heap_blks_hit, idx_blks_read, idx_blks_hit,
toast_blks_read, toast_blks_hit, tidx_blks_read, tidx_blks_hit
-- pg_statio_all_indexes (7 cols)
relid, indexrelid, schemaname, relname, indexrelname,
idx_blks_read, idx_blks_hit
```

goopg has **no per-relation buffer-pool counters**. The shared-buffer hit/read
counters `pg_stat_io` exposes (`storage.Pool.sharedHitCount`/`sharedReadCount`)
are process-wide pool totals, not attributable to an individual relation, so
every block counter here is a faithful `0` — the same honest-0 discipline as the
untracked `pg_stat_io` cells. The identity cells are real (`relid`/`schemaname`/
`relname` for tables; those plus `indexrelid`/`indexrelname` for indexes).

- Row builders — `catalog.PGStatioTablesRowsForDBOid(dbOid, scope)` (11-col) and
  `catalog.PGStatioIndexesRowsForDBOid(dbOid, scope)` (7-col): each applies the
  *same* relation filter as its access-stat twin (relkind `r`/`m`/`p`; the index
  builder enumerates `AllIndexes(dbOid)` and filters `idx.Table`), reusing
  `StatTableScope` for the identical `schemaname` user/sys split. Index rows are
  sorted by `(schemaname, relname, indexrelname)`.
- Per-connection scoping — `executor.fetchStatioTablesRows(ctx, scope)` /
  `fetchStatioIndexesRows(ctx, scope)`: the exact `ctx.Catalog`-unwrap +
  `ctx.CurrentDatabaseOid` twins of the access-stat fetchers, swapped in at
  `valuesOp.Open`'s six new `pg_statio_*` branches; static `VirtualRows`
  fallbacks (OIDs 9087–9092) scope to `DefaultDBOid`.

Tests: `internal/catalog/pgstatio_test.go`
(`TestPGStatioTablesRowsBasicShape` — 11-col width, real identity cells, all 8
block counters `0`; `TestPGStatioTablesScopeFilter`; `TestPGStatioIndexesRowsBasicShape`
— 7-col width) and `internal/executor/pgstatio_e2e_test.go`
(`TestPgStatioUserTablesEndToEnd`, `TestPgStatioUserIndexesEndToEnd`,
`TestPgStatioSysTablesExcludesUserTable`).

## Sequence sibling: `pg_statio_all_sequences` / `pg_statio_{user,sys}_sequences`

The sequence I/O trio completes the `pg_statio_*` family. Upstream
(`system_views.sql`) selects `pg_class` `WHERE relkind = 'S'` and exposes five
columns:

```sql
-- pg_statio_all_sequences (5 cols)
relid, schemaname, relname, blks_read, blks_hit
```

This is the only `pg_statio_*` view whose relation filter *selects* sequences
rather than skipping them: `catalog.PGStatioSequencesRowsForDBOid(dbOid, scope)`
loops the per-DB tables keeping exactly `t.IsSequence`, applies the same
`StatTableScope` user/sys `schemaname` split, and emits real
`relid/schemaname/relname` with `blks_read`/`blks_hit` a faithful `0` (no
per-relation buffer attribution). `executor.fetchStatioSequencesRows` is the
per-connection `ctx.Catalog`-unwrap + `ctx.CurrentDatabaseOid` twin, wired at
`valuesOp.Open`'s three new `pg_statio_*_sequences` branches; static
`VirtualRows` fallbacks (OIDs 9093–9095) scope to `DefaultDBOid`.

Tests: `internal/catalog/pgstatio_test.go`
(`TestPGStatioSequencesRowsBasicShape` — 5-col width, real identity cells, both
block counters `0`, plain table excluded; `TestPGStatioSequencesScopeFilter`) and
`internal/executor/pgstatio_e2e_test.go` (`TestPgStatioUserSequencesEndToEnd` —
sequence projected, plain table excluded).

The block counters stay `0` until goopg grows per-relation buffer-pool
attribution (a `BufferUsage`-per-relation analog), shared with the table/index
gap above.

## Function sibling: `pg_stat_user_functions` / `pg_stat_xact_user_functions`

The two function-statistics views are the last per-object stat views in the
family. Upstream (`system_views.sql`) exposes six columns:

```sql
-- pg_stat_user_functions / pg_stat_xact_user_functions (6 cols)
funcid, schemaname, funcname, calls, total_time, self_time
```

They differ from the table/index/sequence views in one decisive way: their
`WHERE` clause is `pg_stat_get_function_calls(oid) IS NOT NULL` (respectively the
`pg_stat_get_xact_function_calls` variant). That builtin returns `NULL` for any
function with no collected call statistics, and with the default
`track_functions = none` **no** function is ever tracked — so on a stock
PostgreSQL 18.3 cluster out of the box both views are **empty**. goopg has no
per-function call/time tracking at all, so the faithful behaviour is identical:
`catalog.PGStatUserFunctionsRows()` returns no rows unconditionally, and the two
virtual views (OIDs 9096–9097) are registered with the exact 6-column tupledesc
so a client can introspect them and query them for `0` rows instead of hitting an
unknown-relation error.

Because the views are always empty there is no per-database scoping to do — the
static `VirtualRows` builder is sufficient and no `valuesOp.Open` per-connection
twin is needed (unlike the table/index/sequence views, whose rows must be scoped
to the connecting database).

Tests: `internal/catalog/pgstat_functions_test.go`
(`TestPGStatUserFunctionsRowsAlwaysEmpty`,
`TestPGStatUserFunctionsViewsRegistered` — both views registered, 6-col
tupledesc, empty `VirtualRows`) and
`internal/executor/pgstat_functions_e2e_test.go`
(`TestPgStatUserFunctionsEndToEnd` — both views resolve through the
planner/executor and return `0` rows).

Both views stay empty until goopg grows a cumulative per-function statistics
subsystem (a `PgStat_StatFuncEntry` analog) *and* wires it to a
`track_functions`-equivalent GUC — a genuinely new subsystem, not a wiring slice.

## Transaction sibling: `pg_stat_xact_all_tables` / `pg_stat_xact_{user,sys}_tables`

The three `pg_stat_xact_*_tables` views are the per-*transaction* counterpart of
the cumulative `pg_stat_*_tables` views. Upstream (`system_views.sql`) they select
the same relation set (`pg_class` LEFT JOIN `pg_index`, `relkind IN
('r','t','m','p')`) but every counter comes from the `pg_stat_get_xact_*` builtins
— the deltas accumulated by the *current backend's in-progress transaction* rather
than the cluster-lifetime cumulative totals. Consequently the shape is narrower:

```sql
-- pg_stat_xact_all_tables (12 cols) — no n_live_tup / last_* / vacuum cells
relid, schemaname, relname,
seq_scan, seq_tup_read, idx_scan, idx_tup_fetch,
n_tup_ins, n_tup_upd, n_tup_del, n_tup_hot_upd, n_tup_newpage_upd
```

There are **no** `n_live_tup`, `last_*` timestamp or `vacuum_count` columns — the
xact views carry pure transaction-local deltas, not snapshot estimates or
maintenance history. goopg has no per-transaction pgstat accumulator (no
`PgStat_TableXactStatus` analog), so all nine delta counters are a faithful `0` —
exactly what a stock cluster reports for a relation the current transaction has not
yet touched. The three real cells are `relid` / `schemaname` / `relname`.

`catalog.PGStatXactTablesRowsForDBOid(dbOid, scope)` reuses the **identical**
relation filter + `StatTableScope` user/sys split as
`PGStatTablesRowsForDBOid`, so the cumulative and xact table views always agree on
which relations they surface. `executor.fetchStatXactTablesRows` is the
per-connection twin (same `ctx.Catalog`-unwrap + `ctx.CurrentDatabaseOid` scoping),
wired at `valuesOp.Open`'s three new `pg_stat_xact_*_tables` branches; the static
`VirtualRows` fallback (OIDs 9098–9100) scopes to `DefaultDBOid`.

Tests: `internal/catalog/pgstat_xact_tables_test.go`
(`TestPGStatXactTablesRowsBasicShape` — 12-col shape, real identity cells, 0 delta
counters; `TestPGStatXactTablesScopeFilter`; `TestPGStatXactTablesExcludesNonTableRelkinds`;
`TestPGStatXactTablesViewsRegistered`) and
`internal/executor/pgstat_xact_tables_e2e_test.go`
(`TestPgStatXactUserTablesEndToEnd`, `TestPgStatXactSysTablesExcludesUserTable`).

The delta counters stay `0` until goopg grows a per-transaction table-statistics
accumulator (`PgStat_TableXactStatus` analog) that folds into commit-time cumulative
stats — the same not-yet-built subsystem the cumulative views' non-identity columns
wait on.

## Global cluster views (`pg_stat_bgwriter` / `pg_stat_archiver`)

Two of the remaining unregistered stat views are *global* single-row cluster
summaries rather than per-object row sets, so they follow the simpler
`pg_stat_wal`/`pg_stat_slru` precedent (a static `VirtualRows` returning one row
with honest zeros/NULLs) — no per-connection executor twin, no relation scoping.

`pg_stat_bgwriter` (PG 17+ shape, after the checkpoint columns split out into the
already-registered `pg_stat_checkpointer`) carries four columns:

```
buffers_clean, maxwritten_clean, buffers_alloc, stats_reset
```

goopg runs a background writer (`storage.Pool`'s `WriteDirtyPages`) but has no
counter accumulator attributing clean writes / buffer allocations to these
columns, so — exactly like `pg_stat_wal` reports `wal_records = 0` despite goopg
writing WAL — the three counters are a faithful `0` and `stats_reset` is the
fixed boot timestamp (`2026-01-01 00:00:00+00`). OID 3406.

`pg_stat_archiver` (from upstream `pg_stat_get_archiver()`) carries seven columns:

```
archived_count, last_archived_wal, last_archived_time,
failed_count, last_failed_wal, last_failed_time, stats_reset
```

goopg has no WAL archiver (`archive_mode` is unsupported), so on a fresh cluster
the two counts are `0`, both `last_archived_*` and `last_failed_*` cells are NULL
(nothing has ever been archived or failed), and `stats_reset` is the fixed boot
timestamp — matching a real PG 18.3 cluster with `archive_mode = off` out of the
box. NULLs use the `catalog.VirtualNull` sentinel. OID 3407.

Both are registered in `registerSystemTables` (`internal/catalog/catalog.go`)
right after `pg_stat_wal`. Tests:
`internal/catalog/pgstat_global_test.go` (`TestPGStatBgwriterViewRegistered`,
`TestPGStatArchiverViewRegistered` — column shape + honest-0/NULL row) and
`internal/executor/pgstat_global_e2e_test.go` (`TestPgStatBgwriterEndToEnd`,
`TestPgStatArchiverEndToEnd` — full SELECT resolves the view). A live
bgwriter/archiver counter subsystem is deferred (ledger).

## Deferred

The scan/tuple/vacuum/analyze counters and `last_*` timestamps remain honest
zeros/NULLs until goopg grows a cumulative per-table statistics subsystem
(`PgStat_StatTabEntry` analog). The index views share the same gap: `idx_scan` /
`idx_tup_read` / `idx_tup_fetch` / `last_idx_scan` stay `0`/`NULL` until a
`PgStat_StatIndEntry` analog exists. Because system catalogs are storage-less and
carry no user indexes, `pg_stat_sys_tables` and `pg_stat_sys_indexes` return no
rows even though upstream lists the catalog relations. All are recorded in
`.ralph/deferral_ledger.md` with a resume point.
