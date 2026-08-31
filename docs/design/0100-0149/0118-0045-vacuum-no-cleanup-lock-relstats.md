# 0118-0045 — `vacuum-no-cleanup-lock`: pg_class reltuples / relpages from VACUUM

**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
**Status:** accepted
**Spec promoted:** `postgres/src/test/isolation/specs/vacuum-no-cleanup-lock.spec` → `pass`

## Problem

The `vacuum-no-cleanup-lock` spec asserts that `pg_class.relpages` / `reltuples`
reflect what a non-aggressive `VACUUM` observes, *even when a concurrent backend
holds a pin on the table's only heap page* (a cursor), so `VACUUM` cannot get a
cleanup lock. It reads:

```sql
SELECT relpages, reltuples FROM pg_class WHERE oid = 'smalltbl'::regclass;
```

at several points, expecting `1|20` then `1|21` as rows are inserted and the
table is VACUUMed.

Two gaps blocked it:

1. **Missing GUC.** The `vacuumer` session setup runs
   `SET vacuum_multixact_freeze_min_age = 0`; goopg only registered
   `vacuum_freeze_min_age`, so every permutation failed at setup with
   `unrecognized configuration parameter`.

2. **pg_class.relpages / reltuples were hard-coded `0`.** goopg serves `pg_class`
   from the virtual builder (`catalog.(*InMemory).VirtualRows`, the live read
   path — the heap `pg_class` row written at `CREATE TABLE` is only consulted by
   external tools, see [[goopg_pg_class_virtual_pg_attribute_heap]]). Both
   `relpages` and `reltuples` were literal `"0"`, and nothing updated them on
   `VACUUM`.

## Fix

### GUC

Registered `vacuum_multixact_freeze_min_age` (`config/defaults.go`, BootVal
`5000000`, range `0..1000000000`, `ContextUserset`) — the MultiXact-age analog
of `vacuum_freeze_min_age` — and added the matching commented line to
`postgresql.conf.sample` (the `TestSampleConfigCoversRegistry` parity gate
requires every registered GUC to appear there).

### relstats storage + publish

- `catalog.Table` already carries `Stats *TableStats{ RowCount, Pages, … }`
  (set by ANALYZE via `SetTableStats`). New `(*InMemory).UpdateRelStats(table,
  pages, tuples)` is the `vac_update_relstats` analog: it overwrites
  `Pages`/`RowCount` but **merges** into any existing `Stats` so per-column
  `pg_statistic` from a prior ANALYZE is preserved (upstream: both VACUUM and
  ANALYZE call `vac_update_relstats`, only ANALYZE rewrites `pg_statistic`). It
  pointer-replaces the struct so a concurrent reader never sees a torn value, and
  seeds a fresh `Stats` on first VACUUM of a never-analyzed relation. Added to
  the `Catalog` interface.

- The virtual `pg_class` builder reads `t.Stats` for `relpages`/`reltuples`,
  falling back to `0` when `Stats == nil` (a never-vacuumed/analyzed relation
  reads back `0|0`, unchanged — avoids churning every catalog-reading test).

- `vacuumOp.Next`, after a successful `VacuumWithOptions`, publishes the counts
  via `UpdateRelStats`.

### reltuples = visible count, NOT surviving-line-pointer count

The load-bearing subtlety. `VacuumWithOptions` returns `Stats.Live`, the count of
LP_NORMAL tuples that *survive the prune* (`storage.pagePruneCore`). That count
treats a tuple as live unless it is **removable** (`effXmax < oldestXmin`). But a
**recently-dead** tuple — deleted and committed, yet not removable because the
pin holder holds `OldestXmin` back — survives the prune while *not* being live.
Upstream's `reltuples` excludes recently-dead tuples (it counts
`HEAPTUPLE_LIVE`, not `RECENTLY_DEAD`). In permutation 3 of the spec
(`pinholder_cursor; dml_insert; dml_delete; dml_insert; vacuum`) the deleted row
is recently dead, so `Stats.Live` gives `22` where PG reports `21`.

So `vacuumOp` publishes `reltuples` from `vacuum.Analyze` — which counts tuples
visible to a **fresh `ReadCommitted` snapshot** (the "currently live" definition
the `vacuum.Analyze` doc-comment already cites for reltuples). A committed delete
is invisible to a fresh snapshot regardless of the held-back horizon, so it is
correctly excluded. `relpages` comes from the same call's block count. This
reuses existing, tested counting code rather than teaching the CLOG-free storage
prune layer about commit status.

## Blast radius

- The virtual `pg_class` change surfaces real `relpages`/`reltuples` only for
  tables that have been VACUUMed or ANALYZEd; `Stats == nil` ⇒ `0|0` as before,
  so untouched tables and the broad catalog-reading test surface are unchanged.
- The heap `pg_class` row written at `CREATE TABLE`
  (`buildUserPGClassRow`) still carries `0` — it is a write-once DDL snapshot for
  external tools, not the live read path, and is `Stats == nil` at creation
  anyway. Left as-is for consistency with current behavior.
- `vacuum.Analyze` opens a throwaway read transaction (same pattern the ANALYZE
  operator already uses); harmless in the autocommit VACUUM path.

## Tests / gates

- `TestPort_IsolationVacuumNoCleanupLock` — strict (`runIsoSpecStrict`), all
  permutations byte-identical to PG 18.3.
- `TestUpdateRelStatsPreservesColumns` (catalog) — the merge keeps column stats.
- `TestSampleConfigCoversRegistry` (config) — GUC sample parity.
- Sibling no-regression: `TestPort_IsolationVacuum{SkipLocked,ConcurrentDrop,Conflict}`
  + `TestPort_IsolationFreezeTheDead` strict PASS.
- `-race ./internal/vacuum/... ./internal/mvcc/...`; `./internal/executor`,
  `./internal/catalog`, `./internal/config` PASS.
- pgbench smoke = pre-commit hook.

## Deferred (M0118-0008 tail unchanged)

`alter-table-{1,2,4}` (NOT VALID / VALIDATE CONSTRAINT parse + lock semantics;
INHERITS), partition ATTACH/DETACH specs (transactional-DDL cross-session
visibility), `reindex-concurrently-toast` (`allow_system_table_mods` + TOAST
reindex), `plpgsql-toast` (PL/pgSQL transaction control). See the deferral
ledger.
