# Milestone 0030 — Catalog Persistence and DDL WAL

**Status:** planned
**Depends on:** Milestone 0008 (logical replication foundation — schema awareness for pgoutput), Milestone 0014 (PG-compatible WAL on-disk format — the XLogRecord framing this milestone's DDL records will use), Milestone 0015 (pg_proc catalog registry — patterns for system-catalog-as-Go-map are established)
**Drives:** Crash-safe catalog persistence, DDL visibility in WAL for logical replication, pg_attribute/pg_type SQL views, foundation for transactional DDL.

## Context

goopg's catalog (`internal/catalog/`) is entirely in-memory: schema
metadata lives in Go maps (`tables map[string]*Table`,
`indexes map[string]*Index`). Persistence is a whole-catalog JSON
snapshot written to `<DataDir>/global/pg_catalog.json` at shutdown
and restored at startup (`internal/catalog/persist.go`).

This approach has several gaps versus PostgreSQL:

- **No crash safety.** A crash between DDL and the next
  `SaveCatalog()` loses schema changes. There is no write-ahead log
  for catalog mutations.
- **No DDL WAL records.** Logical replication (M0008) cannot
  replicate DDL — the WAL stream contains HeapInsert/HeapDelete
  records for user data only. The output plugin has no schema-change
  events to relay.
- **No system catalog heap tables.** `pg_class`, `pg_attribute`,
  `pg_type` are virtual views synthesising rows from Go structs.
  Tooling that queries these tables via SQL (ODBC/JDBC metadata
  probes, `\d`, `information_schema`) gets a minimal subset.
- **No pg_attribute table.** Column definitions are inline in
  `catalog.Column` arrays on `*Table`. Neither SQL-queryable nor
  WAL-logged.
- **No pg_type table.** Types are strings (`Type{Name: "int4"}`).
  The pgoutput plugin uses a hard-coded `pgoTypeOIDFor()` mapping
  instead of a real type catalog.
- **DDL is non-transactional.** In-memory map mutations are
  immediate and cannot be rolled back.

PostgreSQL solves these by storing every system catalog as a real
heap table (`pg_class`, `pg_attribute`, `pg_type`, `pg_proc`,
`pg_index`, etc.) in the `base/` directory. Catalog tuples are
heap tuples with xmin/xmax, logged via standard `XLOG_HEAP_INSERT` /
`XLOG_HEAP_DELETE` / `XLOG_HEAP_UPDATE` WAL records. The only
explicit DDL WAL record type is `XLOG_SMGR_CREATE` (in
`postgres/src/backend/catalog/storage.c`), which logs physical
relation file creation — everything else is implicit in the
catalog heap page changes.

This milestone introduces a phased transition from JSON-snapshot
persistence to WAL-logged catalog heap tables, mirroring
PostgreSQL's approach while preserving backward compatibility at
each step.

## Required Design Docs

| Doc ID | Title | Status |
|--------|-------|--------|
| `0030-0001` | System catalog heap table substrate — pg_class, pg_attribute, pg_type as real heap relations | pending |
| `0030-0002` | DDL WAL record kinds and redo/undo | pending |
| `0030-0003` | WAL-based catalog recovery and checkpoint integration | pending |
| `0030-0004` | JSON-snapshot to heap-table migration gate | pending |
| `0030-0005` | pg_attribute / pg_type SQL surface | pending |
| `0030-0006` | Transactional DDL foundation | pending |

## Workflow

### Phase 1 — System catalog heap table substrate (0030-0001)

1. Define fixed OIDs for system catalogs matching upstream's
   conventions (`RelationRelationId = 1259` for pg_class,
   `AttributeRelationId = 1249` for pg_attribute,
   `TypeRelationId = 1247` for pg_type, etc.).
2. Implement `SysTableID(oid uint32) bool` — reserved range
   `[1, FirstNormalObjectId)` for system catalogs.
3. At `initdb` time, create the system catalog heap tables
   (`pg_class`, `pg_attribute`, `pg_type`) via the existing
   storage manager `Extend` path. These are real heap relations
   with `RelFileNode` entries in the data directory.
4. Seed initial rows: pg_class entries for the system catalogs
   themselves, pg_attribute entries for their columns,
   pg_type entries for the built-in type set.
5. On startup after Phase 1, the in-memory catalog is populated
   by *reading the heap tables* instead of restoring from JSON
   snapshot. The JSON snapshot remains as a fallback during the
   migration window (Phase 4).
6. Virtual catalog views (`pg_tables`, `pg_indexes`, etc.) now
   source their data from the heap-backed catalog tables rather
   than from Go map iteration.

**Reference:** `postgres/src/backend/catalog/heap.c` (`heap_create_with_catalog`,
`RelationCreateStorage`), `postgres/src/backend/catalog/pg_class.c`,
`postgres/src/backend/catalog/indexing.c` (catalog index maintenance),
`postgres/src/include/catalog/pg_class.h`, `postgres/src/include/catalog/pg_attribute.h`,
`postgres/src/include/catalog/pg_type.h`

### Phase 2 — DDL WAL record kinds (0030-0002)

1. Define new WAL record kinds in `internal/wal/recovery.go`:
   - `RecordKindCatalogInsert` — insert a row into a system catalog
     heap table (e.g. a new pg_class row for `CREATE TABLE`).
   - `RecordKindCatalogDelete` — delete a row from a system catalog.
   - `RecordKindCatalogUpdate` — update a row in a system catalog.
   - `RecordKindSmgrCreate` — physical relation file creation
     (mirrors upstream's `xl_smgr_create` from
     `postgres/src/include/catalog/storage_xlog.h`).
   - `RecordKindSmgrTruncate` — relation truncation
     (mirrors upstream's `xl_smgr_truncate`).
2. Wire the DDL executor path (`ddlOp.Next` in
   `internal/executor/operators_ddl.go`) to emit these records
   alongside each catalog mutation.
3. Implement redo handlers in `ApplyRecord` for the new kinds,
   re-applying catalog heap page mutations during crash recovery.
4. Register the new record kinds in the WAL classifier
   (`internal/wal/classifier.go`) so the logical decoder sees them:
   the pgoutput plugin should emit `Relation` messages when a new
   table is created, enabling subscriber-side schema tracking.

**Reference:** `postgres/src/backend/catalog/storage.c` (`log_smgrcreate`,
`RelationDropStorage`), `postgres/src/include/catalog/storage_xlog.h`,
`postgres/src/backend/access/heap/heapam.c` (`heap_redo` for catalog
heap pages), `postgres/src/backend/commands/tablecmds.c` (DDL command
implementations)

### Phase 3 — WAL-based recovery and checkpoint integration (0030-0003)

1. Remove the JSON snapshot as the authoritative persistence
   mechanism. Catalog state is recovered entirely from WAL replay,
   matching upstream's model.
2. The checkpoint record stores the LSN of the last catalog
   mutation, so `replayLimit` trims correctly.
3. `initdb.Open` replays WAL records for catalog heap pages
   through the same `ApplyRecord` path used for user data.
4. The `SaveCatalog`/`loadCatalogSnapshot` code path is retained
   as a fallback during the Phase 4 migration window but is no
   longer the primary recovery mechanism.
5. Add observability: `pg_stat_wal_io` gains a `catalog_records`
   counter tracking DDL WAL records emitted.

**Reference:** `postgres/src/backend/access/transam/xlogrecovery.c`,
`postgres/src/backend/access/transam/xlog.c` (checkpoint logic),
`postgres/src/backend/storage/smgr/smgr.c` (storage manager recovery)

### Phase 4 — JSON-snapshot to heap-table migration gate (0030-0004)

1. Add a `CatalogVersion` field to the JSON snapshot format.
2. On startup, detect the catalog storage format:
   - If `global/pg_catalog.json` exists and no heap-table system
     catalogs are present, run a one-shot migration that reads
     the JSON snapshot and writes the catalog state into the new
     heap tables, then marks the migration complete.
   - If heap-table system catalogs exist, load from them directly
     (skipping JSON).
   - If neither exists, perform a fresh initdb (new cluster).
3. After all clusters in the field have been migrated, remove
   the JSON-snapshot code path entirely in a follow-up milestone.

**Reference:** `postgres/src/bin/initdb/initdb.c` (system catalog
bootstrap), `postgres/src/backend/catalog/toasting.c` (TOAST table
auto-creation during bootstrap)

### Phase 5 — pg_attribute / pg_type SQL surface (0030-0005)

1. Register `pg_attribute` and `pg_type` as real heap tables
   (already done in Phase 1), then add virtual views if needed
   for backward-compatible column shapes.
2. Implement `pg_type` OID resolution: remove the hard-coded
   `pgoTypeOIDFor()` mapping in `internal/wal/pgoutput.go` and
   replace with a real lookup against the `pg_type` heap table.
3. Extend `pg_catalog` virtual views to expose the columns that
   ODBC/JDBC metadata probes and `\d` expect.
4. Add `pg_index` as a real heap table (currently index metadata
   is embedded in Go `*Index` structs).

### Phase 6 — Transactional DDL foundation (0030-0006)

1. DDL operations that modify the catalog heap tables execute
   within the same MVCC transaction context as user data changes.
2. Catalog heap tuples carry xmin/xmax, so uncommitted DDL is
   invisible to concurrent transactions.
3. `ROLLBACK` of a transaction that performed DDL restores the
   catalog heap pages to their pre-DDL state via WAL redo.
4. This enables `CREATE TABLE ...; INSERT ...; ROLLBACK;` to
   correctly undo both the schema change and the data insert.

**Reference:** `postgres/src/backend/commands/tablecmds.c`,
`postgres/src/backend/catalog/heap.c`, `postgres/src/backend/catalog/index.c`,
`postgres/src/backend/access/heap/heapam.c`

## Definition of Done

1. System catalog tables (`pg_class`, `pg_attribute`, `pg_type`)
   exist as real heap relations under `<DataDir>/base/` after
   initdb.
2. On startup, the catalog is loaded from the heap tables (not
   from JSON snapshot), with the JSON path retained as a
   one-shot migration fallback.
3. DDL operations (`CREATE TABLE`, `CREATE INDEX`, `ALTER TABLE`,
   `DROP TABLE`, etc.) emit WAL records that survive crash and
   recovery.
4. After a crash between DDL and the next checkpoint, restart
   recovery restores the catalog state to the last committed DDL
   (no schema loss).
5. `pg_attribute` is SQL-queryable: `SELECT attname, atttypid
   FROM pg_attribute WHERE attrelid = '<table_oid>'` returns
   correct column metadata.
6. `pg_type` is populated with built-in type entries and is
   SQL-queryable; the pgoutput plugin uses it for type OID
   resolution instead of the hard-coded map.
7. Logical replication: a subscriber receives `Relation` messages
   for newly created tables, enabling schema discovery without
   manual `CREATE TABLE` on the subscriber.
8. All existing `go test ./...` tests pass (including the
   pgbench workload, TPC-H schema build, and logical-replication
   e2e tests).
9. The JSON-snapshot migration path (`global/pg_catalog.json` →
   heap tables) is tested and produces a catalog byte-identical
   to a fresh initdb for the same schema.
10. `pg_stat_wal_io` or equivalent observability exposes the
    count of DDL/catalog WAL records emitted since startup.

## Key Risks and Mitigations

- **Performance regression** — Reading catalog heap pages on every
  table reference is slower than Go map lookup. Mitigation: keep
  the in-memory `InMemory` catalog as a cache layer, invalidated
  by WAL-flush signals (mirroring upstream's relcache invalidation
  via `SinvalRead`/`SinvalWrite`).
- **Migration complexity** — Existing clusters with JSON snapshots
  must transparently upgrade. Mitigation: the Phase 4 migration
  gate is a single-shot process that runs at startup and is
  idempotent.
- **WAL format compatibility** — New record kinds must not break
  existing WAL readers. Mitigation: `ApplyRecord`'s `default` arm
  already returns a descriptive error; new kinds are additive and
  `RecordKind*` constants are appended (not renumbered).
