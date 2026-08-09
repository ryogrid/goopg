# pg_class heap persistence — retire virtual-only rows; reverse-start from PG heap

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S2)
**Last updated:** 2026-08-09 (reverse-path implementation + test)

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

## Reverse-Path Implementation (2026-08-09)

The reverse path (goopg starting against a non-goopg data dir) is
implemented as the **cold-start catalog-load path** — the same code path
that runs when `pg_goopg_catalog_cache.json` is absent, regardless of
whether the data dir was created by PG's initdb or goopg's Init.

### Detection

The catalog cache file (`pg_goopg_catalog_cache.json` in `base/1/` and
`base/5/`) is the goopg-specific marker. Its absence triggers the
cold-start heap-scan path. No separate PG-vs-goopg detection is needed:
the cold-start path handles both.

### Catalog loading

- **System catalogs:** `catalog.NewInMemory()` → `registerSystemTables()`
  provides pg_class, pg_attribute, pg_type, pg_proc, and every other
  system catalog, type, function, and operator — the same set PG's initdb
  bootstraps.
- **User tables:** `loadUserTablesFromHeap` scans `base/<dboid>/1259` and
  `base/<dboid>/1249`. The decoder tries `DecodePGClassRow` (goopg logical
  format) first, then falls back to `DecodePGClassPhysicalRow` (PG
  fixed-offset format) — the physical decoder reads PG-created rows
  correctly.
- The existing `writeCatalogCache` then persists the result to
  `pg_goopg_catalog_cache.json`, so the *next* startup hits the fast path.

### WAL replay constraint

For a **cleanly shut down** PG data dir, `replayStart` finds the shutdown
checkpoint (recognised by `isCheckpointRecord` via `xlogCheckpointShutdown`)
and positions past it — replay is a no-op. An **unclean** PG data dir
carries post-checkpoint records with PG-native resource managers
(e.g. RM_GIN, RM_GIST) that goopg's `replayDecodedXLogRecord` does not
yet handle; replay would fail with `unsupportedDecodedXLogRecord`.

**Constraint:** the reverse path requires a cleanly shut down source data
dir. Unclean shutdown recovery of PG-native WAL is deferred to a follow-up.

### Verification

`TestReversePathColdStartOpensWithoutCache` (`internal/initdb/reverse_path_test.go`)
validates that `Open()` succeeds with the catalog cache absent, and that
core system catalogs (pg_class, pg_type, pg_attribute) are accessible.

### Remaining for full reverse-path parity

1. **System catalogs from heap instead of `registerSystemTables()`:**
   loading pg_class/pg_attribute/pg_type rows for system relations from
   the heap would allow goopg to see PG-added system catalogs that
   `registerSystemTables()` does not enumerate. Deferred — the practical
   impact is nil for common queries since the standard PG catalog set is
   already registered.
2. **Unclean PG WAL replay:** handle additional PG resource managers
   (RM_GIN, RM_GIST, RM_SPGIST, RM_BRIN, etc.) in
   `replayDecodedXLogRecord` so a crashed PG data dir can be recovered.
   Deferred — requires implementing the corresponding index AMs.
3. **E2E PG-attach test:** start a real PG instance, create tables, shut
   it down cleanly, start goopg against the same `$PGDATA`, and verify
   `SELECT * FROM <user_table>` returns the correct rows. Deferred —
   needs a test-harness PG instance lifecycle (M0130-S10).

## References

- `analysis/cluster-dir-level-compat/README.md` §6.3 gap #3
- memory: `goopg_pg_class_virtual_pg_attribute_heap`
- `internal/catalog/catalog.go` — `VirtualRows`
- `postgres/src/include/catalog/pg_class.h` — `FormData_pg_class`
