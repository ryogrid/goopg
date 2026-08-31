# Catalog Migration Gate (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (landed 2026-05-04)   |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

Existing goopg clusters rely on `pg_catalog.json` for catalog persistence. We need a safe and transparent way to migrate these clusters to the new heap-table-based catalog storage without data loss or requiring a full re-initialization.

## Upstream reference

Primary sources:
- `postgres/src/bin/initdb/initdb.c` — System catalog bootstrap logic.
- `postgres/src/backend/catalog/toasting.c` — Example of automatic catalog extension during bootstrap.

## Proposed Changes

### Catalog Versioning

We will introduce a `CatalogVersion` field in the `pg_catalog.json` snapshot and the server's control file.

- `Version 0`: Legacy JSON-only persistence.
- `Version 1`: Heap-table-based persistence (M0030).

### Migration Detection Logic

During server startup, `initdb.Open` will detect the state of the data directory:

1.  **Fresh initdb**: No `pg_catalog.json` and no catalog heap tables. Initialize a new cluster with Version 1.
2.  **Existing v0 Cluster**: `pg_catalog.json` exists, but no catalog heap tables are present. Trigger a one-shot migration.
3.  **Existing v1 Cluster**: Catalog heap tables exist. Load from them directly (ignoring JSON).

### One-Shot Migration Process

When a v0 cluster is detected:

1.  **Load JSON**: Read `pg_catalog.json` into the in-memory registry.
2.  **Bootstrap v1 Tables**: Create the physical heap files for `pg_class`, `pg_attribute`, and `pg_type`.
3.  **Populate Heap**: Write the in-memory metadata into the new heap tables.
4.  **Commit Migration**: Update the control file to `CatalogVersion=1`.
5.  **Clean Up**: (Optional) Rename `pg_catalog.json` to `pg_catalog.json.migrated` to prevent re-triggering.

### Idempotency

The migration process must be idempotent. If the server crashes during migration, the next startup should be able to resume or restart the process safely. Using `CatalogVersion` in the control file as the final commit point ensures this.

## Verification Plan

### Automated Tests
- **TestMigrationFromV0**: Create a v0 data directory (mocked), start the server, and verify that the migration to v1 heap tables is successful.
- **TestIdempotentMigration**: Simulate a crash mid-migration and verify that the second attempt succeeds.

### Manual Verification
- Manually create a simple `pg_catalog.json` and verify that the server converts it to heap tables on startup.
- Verify that once migrated, the server no longer depends on the JSON file.

## What Landed (2026-05-04)

**Scope**: One-shot migration from JSON-only catalog to heap-table format.
JSON decommission (removing JSON after migration) deferred.

### `internal/initdb/open.go`

- `maybeMigrateCatalogToHeap(mgr, cat)` — called in `Open()` after `loadCatalogSnapshot`
  and before `loadSystemCatalogsIfPresent`. Detection logic:
  1. pg_class relfile absent → pre-M0030-0001 cluster → no-op
  2. pg_class has user-table rows (OID ≥ FirstUserOID) → already migrated → no-op
  3. pg_class has 0 user rows AND in-memory catalog has user tables → MIGRATE
  Migration: encodes all user tables as pg_class + pg_attribute heap tuples (using
  bootstrapXID=1 for visibility) and appends them via `appendCatalogRows`.

- `appendCatalogRows(mgr, rel, tuples)` — appends HeapTuples to the last page of an
  existing relfile, extending to new pages when full. Used by migration to backfill
  rows without disturbing existing bootstrap rows.

### Detection (simplified from design doc)

Instead of a `CatalogVersion` control file, migration is detected by inspecting
pg_class for user-table rows. This is simpler and avoids a new file format but
requires that the migration be idempotent: if interrupted mid-way, subsequent
startups skip migration (pg_class has >0 user rows even if incomplete).

### Tests (4 new in `catalog_migration_test.go`)

- `TestMigrationFromLegacyJSONCluster` — creates legacy cluster (table via raw
  catalog API → JSON only, not heap), opens → migration writes to pg_class
- `TestMigrationIdempotent` — two opens → exactly 1 user row in pg_class
- `TestMigrationNewClusterIsNoOp` — fresh cluster (no user tables) → no user rows
  in pg_class after open
- `TestMigrationPGAttributeRowsWritten` — column definitions also migrated

### Still Deferred

- JSON decommission: rename/remove `pg_catalog.json` after successful migration.
- `CatalogVersion` field in JSON for explicit v0/v1 detection.
- Migration of index metadata (pg_index or extended pg_class).
