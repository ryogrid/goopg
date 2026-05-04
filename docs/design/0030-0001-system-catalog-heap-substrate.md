# System Catalog Heap Substrate (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (Phase 1 landed 2026-05-04) |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

goopg's catalog is currently in-memory, with persistence handled by a JSON snapshot of the entire catalog state at shutdown and restored at startup. This approach lacks crash safety, transactional DDL, and doesn't support WAL-based replication of schema changes.

PostgreSQL solves this by storing the catalog metadata in real heap tables (e.g., `pg_class`, `pg_attribute`, `pg_type`). This design doc covers Phase 1 of Milestone 0030: establishing these system catalogs as real heap relations.

## Upstream reference

Primary sources:
- `postgres/src/include/catalog/pg_class.h` — Definition of `pg_class` (RelationRelationId = 1259).
- `postgres/src/include/catalog/pg_attribute.h` — Definition of `pg_attribute` (AttributeRelationId = 1249).
- `postgres/src/include/catalog/pg_type.h` — Definition of `pg_type` (TypeRelationId = 1247).
- `postgres/src/include/access/transam.h` — `FirstNormalObjectId = 16384`.
- `postgres/src/backend/catalog/heap.c` — `heap_create_with_catalog` and bootstrap logic.

## Proposed Changes

### Fixed OIDs and Reserved Range

We will reserve a range of OIDs for system catalogs to ensure they have stable identities across cluster initializations.

```go
const (
    TypeRelationId      = 1247
    AttributeRelationId = 1249
    RelationRelationId  = 1259
    FirstNormalObjectId = 16384
)

func IsSystemRelation(oid uint32) bool {
    return oid < FirstNormalObjectId
}
```

### Heap Table Creation at initdb

During `initdb`, we will physically create the heap files for these catalogs under the `base/` directory.

1.  **Relation Creation**: Use `storage.Manager.Extend` to allocate the first block for each catalog table.
2.  **Initial Seeding**:
    -   `pg_class`: Entries for `pg_class` itself, `pg_attribute`, and `pg_type`.
    -   `pg_attribute`: Column definitions for the three catalogs.
    -   `pg_type`: Initial set of supported types (int4, int8, text, etc.).

### In-Memory Catalog Population

On startup, the `catalog.InMemory` registry will be populated by scanning these heap tables instead of loading from the JSON snapshot.

1.  **Open Catalogs**: The storage manager opens the reserved relfilenodes.
2.  **Scan and Register**: Iterate through tuples in `pg_class`, `pg_attribute`, and `pg_type` to rebuild the Go maps in memory.

### Virtual View Integration

Existing virtual views like `pg_tables` and `pg_indexes` will be updated to source their data from the heap-backed catalogs. This ensures that SQL queries against system catalogs return data consistent with the physical storage.

## Verification Plan

### Automated Tests
- **TestCatalogHeapCreation**: Verify that `pg_class`, `pg_attribute`, and `pg_type` relfiles exist after `initdb`.
- **TestCatalogSeeding**: Query the system catalogs via SQL to ensure initial rows are correctly populated.
- **TestStartupFromHeap**: Initialize a cluster, add a table (which currently uses JSON), then modify the startup to load from heap (verified with mock data) to ensure the logic works.

### Manual Verification
- Run `initdb` and inspect the `base/` directory for the reserved OID files.
- Use `goopg` to query `pg_class` and verify the output.

## What Landed (Phase 1 — 2026-05-04)

**Scope**: OID constants + `IsSystemRelation` helper + heap file creation at `initdb` time.
Catalog row codec, startup-load switch, and DDL-sync wiring are deferred to Phase 2 (M0030-0002+).

### `internal/catalog/catalog.go`

- Added `TypeRelationId = 1247`, `AttributeRelationId = 1249`, `RelationRelationId = 1259`
  matching upstream's `pg_type.h` / `pg_attribute.h` / `pg_class.h`.
- Added `IsSystemRelation(oid uint32) bool` — returns true when `oid < FirstUserOID`.
  Used by executor and bootstrap code to gate system-relation behaviour.

### `internal/initdb/initdb.go`

- Added `bootstrapSystemCatalogs(dataDir string) error`:
  creates one empty heap relfile for each system catalog using `storage.Manager.Extend`.
  Files land at `base/<DefaultDBOid>/<oid>` (e.g. `base/1/1259` for pg_class).
  Each file is exactly `BlockSize` bytes (one `InitPage`-initialised blank page).
- `Init()` now calls `bootstrapSystemCatalogs` after directory/file creation.
- Added `import "github.com/goopg/goopg/internal/storage"`.

### Tests

- `catalog_test.go`: `TestSystemCatalogOIDConstants`, `TestIsSystemRelation`,
  `TestSystemRelationOIDsBelowFirstUserOID` — all pass.
- `initdb_test.go`: `TestInitCreatesSystemCatalogRelfiles` (file existence + size),
  `TestSystemCatalogRelfilesAreValidHeapPages` (non-zero + `!IsNew`) — all pass.

### Deferred

- Catalog row codec (encode/decode `pg_class`/`pg_attribute`/`pg_type` tuples as heap rows).
- Row seeding (initial `pg_class` entry for itself, `pg_attribute` column definitions, etc.).
- Startup-load switch: populate `catalog.InMemory` from heap tables instead of JSON snapshot.
- DDL-sync wiring: `CREATE TABLE`/`CREATE INDEX` writes rows into the system catalog heap tables.
- Virtual-view integration: source `pg_tables`, `pg_indexes` from heap-backed tables.
