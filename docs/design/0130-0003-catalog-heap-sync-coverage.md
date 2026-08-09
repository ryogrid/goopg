# Catalog heap sync coverage for remaining DDL paths

**Status:** accepted
**Date:** 2026-08-09
**Milestone:** M0130 (S3)

## Problem

Runtime DDL operations must sync to on-disk heap files so a PG standby consumer
(and a goopg restart) see the correct catalog state. The 2026-07-26 analysis
README identified several gaps; this design tracks which are already resolved
and which were closed by this milestone.

## Audit result (2026-08-09)

| Operation | Catalog | Ledger Row | Status |
|---|---|---|---|
| `ALTER TABLE ADD COLUMN` | pg_attribute (1249) | #404 | **FIXED this milestone** |
| `CREATE SCHEMA` | pg_namespace (2615) | #50 | Already synced (B1.1) |
| `CREATE COLLATION` | pg_collation (3456) | #389 | Already synced (B2.2 slice 4) |
| `CREATE FOREIGN DATA WRAPPER` | pg_foreign_data_wrapper (2328) | #390 | Already synced (B3.4) |
| `CREATE FOREIGN SERVER` | pg_foreign_server (1417) | #391 | Already synced (B3.4) |
| `CREATE USER MAPPING` | pg_user_mapping (1418) | #392 | Already synced (B3.4) |
| `CREATE EXTENSION` | pg_extension (3079) | #393 | **FIXED this milestone** |
| `DROP EXTENSION` | pg_extension (3079) | — | **FIXED this milestone** |
| `CREATE TABLESPACE` | pg_tablespace (1213) | #45 | Already synced (B4.1) |

## Implemented this milestone (M0130-S3)

### S3.1 — ALTER TABLE ADD COLUMN → pg_attribute heap sync

- **Emit site:** `internal/executor/operators_ddl.go` — `execAlterTableAddColumn`
  now calls `deleteCatalogRowsForOID` + `syncTableToCatalogHeap` after
  `addColumnRecursive`, following the same delete-old-rows + re-sync pattern as
  every other ALTER TABLE path (SET STORAGE, SET COMPRESSION, SET STATISTICS,
  DROP COLUMN, etc.).
- **Test:** `TestAddColumnSurvivesRestart` (`internal/initdb/ddl_catalog_sync_test.go`)
  — creates a table with one column, adds a second, restarts, and verifies both
  columns are present. Confirmed non-vacuous (fails pre-fix: `got 1`).
- **Gap closed:** deferral-ledger row #404.

### S3.2 — CREATE/DROP EXTENSION → pg_extension heap sync

- **Write path:** `internal/executor/sys_pg_extension.go` — new file with
  `writeExtensionCatalogRow` (heap INSERT into base/<db>/3079 + index entries
  3080/3081) and `deleteExtensionCatalogRow` (xmax stamp).
- **Emit site:** `execCreateExtension` (`operators_ddl.go`) now calls
  `writeExtensionCatalogRow` after `catalog.CreateExtension`.
- **Drop path:** `execDropCompat` "extension" case added — resolves the
  extension OID, calls `catalog.DropExtension` + `deleteExtensionCatalogRow`.
- **Catalog:** `DropExtension` + `CreateExtensionDuringRecovery` added to
  `catalog.InMemory`.
- **Reload:** `reloadUserExtensionsFromHeap` (`internal/initdb/catalog_heap_reload.go`)
  scans base/*/3079 and re-registers extensions via `CreateExtensionDuringRecovery`.
- **Gap closed:** deferral-ledger row #393 (extension had no durability record).

### Already resolved (no work needed — verified 2026-08-09)

- **CREATE SCHEMA:** `syncSchemaToCatalogHeap` (B1.1) called from
  `execCompatNoop` "schema" case.
- **CREATE/DROP/ALTER COLLATION:** `upsertCollationCatalogRow` /
  `deleteCollationCatalogRow` (B2.2 slice 4) called from `execCreateCollation` /
  `execDropCompat`.
- **CREATE/DROP FOREIGN DATA WRAPPER:** `writeFdwCatalogRow` /
  `deleteForeignRowByOID` (B3.4).
- **CREATE/DROP FOREIGN SERVER:** `writeForeignServerCatalogRow` /
  `deleteForeignRowByOID` (B3.4).
- **CREATE/DROP USER MAPPING:** `writeUserMappingCatalogRow` /
  `deleteForeignRowByOID` (B3.4).
- **CREATE/DROP TABLESPACE:** `writeTablespaceCatalogRow` /
  `deleteTablespaceCatalogRow` (B4.1), with pg_shdepend and xlog records.

## Design — general pattern

Each DDL site follows the pattern established by M0113 (pg_index) and
M0130-S2 (pg_class):

1. After the in-memory catalog mutation, call a per-catalog `write*Row`
   function (or `syncTableToCatalogHeap` for table-level catalogs).
2. The write function emits a PG-format heap row via `writeHeapRowCanonical`,
   plus index entries via `insertCanonicalSysBtreeLeaf`.
3. The write is WAL-logged via the existing `EncodeHeapInsertPG` path (so a
   PG standby replays it).
4. On DROP or re-sync (ALTER), old rows are stamped with xmax via
   `deleteCatalogRowsForOID` / `stampCatalogRows` / per-catalog delete helpers.
5. Reload: a `reload*FromHeap` function in `catalog_heap_reload.go` reads rows
   at startup and repopulates the in-memory catalog via `*DuringRecovery`
   helpers.

## Guards

1. ADD COLUMN survives restart — `TestAddColumnSurvivesRestart` PASS.
2. UNITS gate PASS (all packages).
3. SMOKE gate PASS (0 failed, all 3 pgbench workloads).

## References

- `analysis/cluster-dir-level-compat/README.md` gaps #2, #9
- `.ralph/deferral_ledger.md` rows #50, #389–#393, #404
- `internal/executor/operators_ddl.go` — DDL emit sites
- `internal/executor/sys_pg_extension.go` — new: extension heap sync
- `internal/initdb/catalog_heap_reload.go` — reload functions
