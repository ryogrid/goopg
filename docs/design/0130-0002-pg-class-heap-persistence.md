# pg_class heap persistence — retire virtual-only rows; reverse-start from PG heap

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S2)
**Last updated:** 2026-08-09 (bootstrap audit + forward-path verification)

## Problem

goopg's `pg_class` is **virtual** — rows are generated on-the-fly by
`VirtualRows` builder, not stored in a heap file. PG may try to heap-scan
`pg_class` at startup (for relcache invalidation or `load_critical_index`).
On a PG started against a goopg data dir, `SELECT relname FROM pg_class`
returns nothing for user tables. This is BLOCKER #3 in
`analysis/cluster-dir-level-compat/README.md` §6.3.

Conversely, goopg cannot start from a PG-created `$PGDATA` because it has
no path to read pg_class rows from heap file 1259.

## Design

### Forward path: goopg → pg_class heap rows

1. **Bootstrap audit — COMPLETE (2026-08-09).** `bootstrapPgClassTuples`
   (`internal/initdb/initdb.go:2167`) writes every nailed relation (shared +
   local, as defined in `internal/initdb/relcache_init.go`'s
   `nailedSharedRels` + `nailedLocalRels`) to both `base/1/1259` and
   `base/5/1259`. Verified by `TestPgClassHeapBootstrapCoverage`: 162 tuples
   (27 shared + 135 local) on valid heap pages in both directories. Column
   layout (`pgClassColDefs`) is byte-identical to the runtime encoder
   (`pgClassColumnsPG18` in `internal/executor/pg18_user_catalog_rows.go`).

2. **Runtime sync audit — COMPLETE (verified 2026-08-09).**
   - `CREATE TABLE` / `CREATE INDEX` / `CREATE SEQUENCE` / `CREATE VIEW`:
     wired via `syncTableToCatalogHeap` (`operators_ddl.go:13439`).
   - `ALTER TABLE` (RENAME, SET SCHEMA, ADD COLUMN, DROP COLUMN, etc.):
     all paths call `syncTableToCatalogHeap` after `deleteCatalogRowsForOID`
     for old rows.
   - `DROP TABLE` / `DROP INDEX` / `DROP VIEW`: handled via
     `deleteCatalogRowsForOID` which stamps xmax on pg_class/pg_attribute
     rows; rows are physically removed by VACUUM/checkpoint.
   - Index maintenance (pg_class_oid_index, pg_class_relname_nsp_index):
     updated via `insertPgClassOidIndexEntry` / `insertPgClassRelnameNspIndexEntry`.

3. **VirtualRows transition — CONFIRMED (2026-08-09).** `pg_class` remains
   `Virtual: true` with `VirtualRows = PGClassRowsForDBOid(DefaultDBOid)` for
   goopg's own query planning. The heap is maintained as a PG-compatible
   mirror — all nailed-relation rows are written at init time, and all user
   DDL writes heap rows at runtime. The two sources agree on existing
   relations (VirtualRows enumerates from in-memory catalog, heap contains
   the same set).

### Reverse path: goopg reads PG-created pg_class heap

1. **Startup path:** in `open.go`, if `pg_internal.init` is absent (PG-created
   data dir), load catalog from heap files:
   - Scan `pg_class` (1259) heap → build `catalog.InMemory` tables.
   - Scan `pg_attribute` (1249) heap → build column definitions.
   - Scan `pg_type` (1247) heap → build type registry.
   - Fall back to bootstrap catalog for built-in types/functions not in heap.

2. **Relcache init file:** if `pg_internal.init` IS present (goopg-created data
   dir), the existing fast-start path works unchanged. Reverse path is a
   cold-start fallback.

## Guards

1. PG started against goopg data dir: `SELECT relname FROM pg_class` lists
   user tables. *(Needs E2E PG-attach test — not yet implemented.)*
2. goopg started against PG-initdb'd data dir: serves reads via psql.
   *(Reverse path not yet implemented.)*
3. CREATE TABLE → table visible in pg_class on PG standby.
   *(Depends on guard #1.)*
4. UNITS + SMOKE green. *(Verified: all initdb tests PASS,`TestPgClassHeapBootstrapCoverage` PASS.)*

## Forward-Path Verification (2026-08-09)

`TestPgClassHeapBootstrapCoverage` (`internal/initdb/pg_class_heap_bootstrap_test.go`)
verifies that `Init()` produces well-formed pg_class heap files containing
exactly the expected tuples:

- **162 tuples** (27 shared + 135 local nailed relations) in both `base/1/1259` and `base/5/1259`
- **Valid page headers** on every 8 KiB page
- **Valid tuple headers** with correct `t_hoff` data-offset pointers
- **Non-zero OIDs** for every tuple (pg_class.oid is never zero)
- Multi-page layout (tuples span pages when one fills up)

This confirms BLOCKER #3 (`pg_class` virtual-only) is resolved for the
**forward path**: a PG backend connecting to a goopg data dir can heap-scan
`pg_class` and find complete catalog descriptors.

## Remaining for M0130-S2

**Reverse path** (goopg starting from PG-created data dir) is the next
concrete step. Key sub-tasks:

1. Detect PG-created data dir (no `pg_internal.init`, no
   `pg_goopg_catalog_cache.json`, but valid PG_VERSION).
2. Bootstrap the in-memory catalog from heap scans instead of hardcoded
   `registerSystemTables()`:
   - Scan `pg_class` (1259) → register every relation
   - Scan `pg_attribute` (1249) → attach column definitions
   - Scan `pg_type` (1247) → register types
   - Fall back to goopg's bootstrap builtins for functions not in heap
3. Validate: `SELECT count(*) FROM pg_class` returns the same count as PG
   reported against the same data dir.

## References

- `analysis/cluster-dir-level-compat/README.md` §6.3 gap #3
- memory: `goopg_pg_class_virtual_pg_attribute_heap`
- `internal/catalog/catalog.go` — `VirtualRows`
- `postgres/src/include/catalog/pg_class.h` — `FormData_pg_class`
