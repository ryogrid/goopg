# System Catalog Heap Substrate (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (Phase 1+2+3+4 landed 2026-05-04) |
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

### Deferred (from Phase 1)

- Catalog row codec (encode/decode `pg_class`/`pg_attribute`/`pg_type` tuples as heap rows).
  → **Landed in Phase 2** — see below.
- Row seeding (initial `pg_class` entry for itself, `pg_attribute` column definitions, etc.).
  → **Landed in Phase 2** — see below.
- Startup-load switch: populate `catalog.InMemory` from heap tables instead of JSON snapshot.
- DDL-sync wiring: `CREATE TABLE`/`CREATE INDEX` writes rows into the system catalog heap tables.
- Virtual-view integration: source `pg_tables`, `pg_indexes` from heap-backed tables.

## What Landed (Phase 2 — 2026-05-04)

**Scope**: Catalog row codec + initial seeding at `initdb` time.
Startup-load switch and DDL-sync wiring are deferred to Phase 3+.

### `internal/catalog/codec.go` (new file)

Three row types and their binary encoder/decoder, compatible with
`executor.EncodeRow`/`DecodeRowInto` so a SeqScan on the system catalog
files produces correct values without special-casing:

- `PGClassRow` + `EncodePGClassRow` / `DecodePGClassRow`  
- `PGAttributeRow` + `EncodePGAttributeRow` / `DecodePGAttributeRow`  
- `PGTypeRow` + `EncodePGTypeRow` / `DecodePGTypeRow`

Column-schema functions: `PGClassColumns()` (8 cols), `PGAttributeColumns()` (6),
`PGTypeColumns()` (7).

Namespace OID constants: `PGCatalogNamespaceOID = 11`, `PublicNamespaceOID = 2200`.

Built-in type OID constants: `OIDBool=16`, `OIDInt8=20`, `OIDInt2=21`, `OIDInt4=23`,
`OIDText=25`, `OIDOID=26`, `OIDBpChar=1042`, `OIDVarChar=1043`,
`OIDTimestamp=1114`, `OIDNumeric=1700`.

Encoding format (per column): `null_byte(0x00) || value_bytes` where
int4=4-byte BE, bool=1-byte, text=4-byte-BE-len+UTF-8.

### `internal/initdb/catalog_seed.go` (new file)

Bootstrap row definitions for the three system catalogs:
- `pgTypeBootstrapRows()` — 10 built-in type entries
- `pgClassBootstrapRows()` — 3 self-referential catalog entries
- `pgAttributeBootstrapRows()` — 21 column definitions (8+6+7)
- `typeOIDForCatalogColumn()` — maps v0 type-name strings to OIDs

### `internal/initdb/initdb.go` changes

`bootstrapSystemCatalogs` rewritten to seed rows:
- Calls `extendWithRows(mgr, relOID, rows)` per catalog
- `extendWithRows`: inits page, appends `NewHeapTuple(bootstrapXID=1, ...)` per row,
  calls `Manager.Extend` with the seeded page
- Bootstrap tuples use `xmin=1` (BootstrapTransactionID) so they are
  always visible to all subsequent sessions

### Tests (12 new)

Codec round-trip tests in `codec_test.go`:
`TestEncodePGClassRowRoundTrip`, `TestEncodePGClassRowRelIsSharedTrue`,
`TestEncodePGAttributeRowRoundTrip`, `TestEncodePGAttributeRowDropped`,
`TestEncodePGTypeRowRoundTrip` (4 type cases), column count tests (×3),
`TestBuiltinTypeOIDs`.

Seeding read-back tests in `initdb_test.go`:
`TestBootstrappedPGTypeRowsReadable` (verifies 7 required types present),
`TestBootstrappedPGClassRowsReadable` (pg_type/pg_attribute/pg_class entries),
`TestBootstrappedPGAttributeRowsReadable` (8+6+7 column counts + spot-check).

### Still Deferred (from Phase 2)

- Startup-load switch: replace `loadCatalogSnapshot` with heap-table scan.
  → **Landed in Phase 3** — see below.
- DDL-sync wiring: `CREATE TABLE`/`CREATE INDEX`/`DROP TABLE` write to pg_class/pg_attribute.
- Virtual-view integration: source `pg_tables`, `pg_indexes` from heap-backed tables.

## What Landed (Phase 3 — 2026-05-04)

**Scope**: Startup-load switch — register pg_type and pg_attribute as real heap-backed
catalog tables at Open time. DDL-sync wiring deferred to Phase 4.

### `internal/catalog/catalog.go` changes

- `RegisterRealTable(t *Table) error` — new method to install a system catalog table
  (non-virtual, OID below FirstUserOID). Idempotent on duplicate with same OID.

### `internal/catalog/persist.go` changes

- `Snapshot()` now skips tables where `IsSystemRelation(t.OID)` is true, so pg_type
  and pg_attribute are never written to the JSON snapshot. They are re-registered
  from heap relfiles on every startup.

### `internal/initdb/open.go` changes

- `loadSystemCatalogsIfPresent(dataDir, cat)` — new function called in `Open()` after
  `loadCatalogSnapshot`. Checks if the M0030-0001 relfiles exist under
  `base/<DefaultDBOid>/`; if present, registers the corresponding tables:
  - pg_type (OID 1247): columns from `PGTypeColumns()`
  - pg_attribute (OID 1249): columns from `PGAttributeColumns()`
- On old clusters (pre-M0030-0001, no relfiles), the function is a no-op — backward
  compatible.
- Called AFTER `loadCatalogSnapshot`/`Restore()` to survive the catalog reset.

### Effect

After `goopg init` + `goopg start`:
- `SELECT * FROM pg_type` now scans the heap file and returns the 10 seeded built-in
  types. No special virtual-table registration needed.
- `SELECT * FROM pg_attribute WHERE attrelid = 1259` returns the 8 pg_class columns.
- pg_class remains a virtual table (dynamic user-table listing still required).

### Tests (4 new in `open_test.go`)

- `TestOpenRegistersSystemCatalogHeapTables` — pg_type and pg_attribute present, non-virtual, correct OID
- `TestOpenPGTypeIsNotVirtual` — VirtualRows is nil  
- `TestOpenSystemCatalogNotInJSONSnapshot` — Save + reopen, no duplication in JSON
- `TestOpenOldClusterWithoutM0030FilesStillWorks` — files removed, Open succeeds

### Still Deferred (from Phase 3)

- DDL-sync wiring: `CREATE TABLE` writes pg_class + pg_attribute rows.
  → **Landed in Phase 4** — see below.
- `CREATE INDEX`: writes pg_class row for the index relation.
  → **Landed in Phase 4** — see below.
- `DROP TABLE`: marks rows with xmax (delete-stamp). Still deferred.
- Virtual-view integration: source `pg_tables`, `pg_indexes` from heap. Still deferred.

## What Landed (Phase 4 — 2026-05-04)

**Scope**: DDL-sync wiring — `CREATE TABLE` and `CREATE INDEX` write rows into
pg_class / pg_attribute heap files. `DROP TABLE`/`DROP INDEX` sync deferred.

### `internal/catalog/codec.go` changes

- `TypeNameToOID(typName string) uint32` — maps goopg type name strings
  (int4, int8, text, bool, varchar, etc.) to canonical pg_type OIDs.

### `internal/executor/operators_ddl.go` changes

Three new package-private helpers:
- `catalogHeapSyncAvailable(ctx) bool` — checks if pg_attribute is registered
  as non-virtual (proxy for M0030-0001 relfiles being present).
- `syncTableToCatalogHeap(ctx, tbl)` — writes one pg_class row + one
  pg_attribute row per column via `writeHeapRow`.
- `syncIndexToCatalogHeap(ctx, idx)` — writes one pg_class row (relkind='i').
- `namespaceOIDForSchema(schema) uint32` — maps schema name to namespace OID.

Wiring:
- `execCreateTable`: captures returned `*catalog.Table`, calls
  `syncTableToCatalogHeap` when sync is available.
- `createBTreeIndex`: calls `syncIndexToCatalogHeap` after backfill succeeds.
- `catalogHeapSyncAvailable` guards both — no-op on pre-M0030-0001 clusters.

### Tests (3 new in `ddl_catalog_sync_test.go`)

Integration tests using `initdb.Init + Open + full executor pipeline`:
- `TestCreateTableSyncsToPGClass` — pg_class row present with correct OID/name/relkind/relnatts
- `TestCreateTableSyncsToPGAttribute` — pg_attribute rows for all columns with correct type OIDs
- `TestCreateIndexSyncsToPGClass` — pg_class row for index with relkind='i'

### Still Deferred (from Phase 4)

- `DROP TABLE`/`DROP INDEX` sync: set xmax on catalog rows.
- DDL-sync for `ALTER TABLE ADD COLUMN` / `DROP COLUMN`.
- Startup-load of user tables from pg_class/pg_attribute (requires
  DDL-sync to be complete — pg_class would then have all tables).
- Virtual-view integration: replace the dynamic virtual pg_class/pg_tables
  with heap-backed scanning.
