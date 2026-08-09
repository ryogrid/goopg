# pg_class heap persistence — retire virtual-only rows; reverse-start from PG heap

**Status:** draft
**Date:** 2026-08-09
**Milestone:** M0130 (S2)

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

1. **Bootstrap audit:** verify every system catalog and nailed relation has a
   corresponding pg_class row written to `base/<dbOid>/1259` at init time.
   `bootstrapPgClassTuples` already writes some rows; audit completeness
   against the PG 18.3 bootstrap image.

2. **Runtime sync audit:** audit every CREATE/ALTER/DROP path for pg_class
   heap sync:
   - `CREATE TABLE` / `CREATE INDEX` / `CREATE SEQUENCE` — already wired
     via `syncTableToCatalogHeap`? (audit first).
   - `ALTER TABLE RENAME` / `ALTER TABLE SET SCHEMA` — needs
     `resyncPgClassHeapRow`.
   - `DROP TABLE` / `DROP INDEX` — needs `deleteCatalogRow` for relid.

3. **VirtualRows transition:** keep `VirtualRows` as the *runtime* source for
   goopg's own query planning, but also maintain the heap as a PG-compatible
   mirror. The heap is the source of truth for PG consumers.

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
   user tables.
2. goopg started against PG-initdb'd data dir: serves reads via psql.
3. CREATE TABLE → table visible in pg_class on PG standby.
4. UNITS + SMOKE green.

## References

- `analysis/cluster-dir-level-compat/README.md` §6.3 gap #3
- memory: `goopg_pg_class_virtual_pg_attribute_heap`
- `internal/catalog/catalog.go` — `VirtualRows`
- `postgres/src/include/catalog/pg_class.h` — `FormData_pg_class`
