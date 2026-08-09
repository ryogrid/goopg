# Catalog heap sync coverage for remaining DDL paths

**Status:** draft
**Date:** 2026-08-09
**Milestone:** M0130 (S3)

## Problem

Runtime DDL operations in goopg update the in-memory catalog but do not
always sync to the corresponding on-disk heap files. PG standby consumers
see stale or missing catalog data. Specific gaps from the analysis README
and deferral ledger:

| Operation | Catalog | Ledger Row | Status |
|---|---|---|---|
| `ALTER TABLE ADD COLUMN` | pg_attribute (1249) | #404 | not synced |
| `CREATE SCHEMA` | pg_namespace (2615) | #50 | not synced |
| `CREATE COLLATION` | pg_collation (3456) | #389 | in-memory only |
| `CREATE FOREIGN DATA WRAPPER` | pg_foreign_data_wrapper (2328) | #390 | in-memory only |
| `CREATE FOREIGN SERVER` | pg_foreign_server (1417) | #391 | in-memory only |
| `CREATE USER MAPPING` | pg_user_mapping (1418) | #392 | in-memory only |
| `CREATE EXTENSION` | pg_extension (3079) | #393 | in-memory only |
| `CREATE TABLESPACE` | pg_tablespace (1213) | #45 | B4.1 landed — verify |

## Design

### Pattern

Each DDL site follows the same pattern (established by M0113 for pg_index):

1. After the in-memory catalog mutation, call `syncTableToCatalogHeap(catName)`.
2. The sync function writes a PG-format heap row (or deletes one for DROP)
   using the existing `catalog.BuildHeapRow` / `writeHeapRow` primitives.
3. The write is WAL-logged via the existing `EncodeHeapInsertPG` /
   `EncodeHeapDeletePG` paths (so the standby replays it).
4. Reload: `loadFromHeap` function reads rows at startup to repopulate the
   in-memory catalog.

### ADD COLUMN (S3.1)

- Emit site: `internal/executor/operators_ddl.go` — after
  `catalog.AddColumn`, call `syncPgAttributeHeapRow(relOid, attnum)`.
- The new pg_attribute row (with `attnum`, `attname`, `atttypid`, `atttypmod`,
  etc.) is written to heap 1249.

### CREATE SCHEMA (S3.2)

- Emit site: `CREATE SCHEMA` executor path → after `catalog.CreateNamespace`,
  call `syncTableToCatalogHeap("pg_namespace")`.
- pg_namespace rows are written to heap 2615.

### Collation / FDW / Server / User Mapping / Extension (S3.3–S3.4)

- Each registry (in `internal/catalog/`) needs a `syncToHeap` function.
- Registries that are currently in-memory-only need a heap-backed reload path
  (`loadCollationsFromHeap`, `loadFDWsFromHeap`, etc.).

### pg_tablespace verification (S3.5)

- B4.1 landed pg_tablespace heap rows. Verify completeness: do CREATE/DROP
  TABLESPACE sync to heap? If not, add the emit site.

## Guards

1. Each DDL survives restart (verify with `TestSurvivesRestart*` pattern).
2. Each DDL replays on a real PG standby (after S6 retires rmid-128).
3. UNITS green.

## References

- `analysis/cluster-dir-level-compat/README.md` gaps #2, #9
- `.ralph/deferral_ledger.md` rows #50, #389–#393, #404
- `internal/executor/operators_ddl.go` — DDL emit sites
- `internal/catalog/` — namespace, collation, FDW registries
