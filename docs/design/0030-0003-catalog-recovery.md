# WAL-Based Catalog Recovery (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (landed 2026-05-04)   |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

With Milestone 0030 Phase 1 and 2, catalog mutations are stored in heap tables and logged to WAL. However, the primary recovery mechanism is still the JSON snapshot. This phase transitions goopg to purely WAL-based catalog recovery, mirroring PostgreSQL.

## Upstream reference

Primary sources:
- `postgres/src/backend/access/transam/xlogrecovery.c` — Main recovery loop.
- `postgres/src/backend/access/transam/xlog.c` — Checkpoint logic and control file management.
- `postgres/src/backend/storage/smgr/smgr.c` — Storage manager integration with recovery.

## Proposed Changes

### Decommissioning JSON Snapshot

The JSON snapshot (`pg_catalog.json`) will no longer be the authoritative source of catalog state. While it will be retained as a fallback during the migration window (Phase 4), the primary startup path will bypass it.

### Checkpoint Integration

The checkpoint record (`RecordKindCheckpoint`) will be updated to store the LSN of the last catalog mutation. This ensures that recovery starts from the correct WAL position to rebuild the in-memory catalog cache.

### Recovery Flow in initdb.Open

1.  **Read Control File**: Determine the last checkpoint LSN.
2.  **WAL Replay**: Scan WAL records starting from the checkpoint.
3.  **Apply Catalog Records**: When `RecordKindCatalog*` records are encountered, the redo handlers will update the physical heap pages.
4.  **In-Memory Rebuild**: After WAL replay is complete, the in-memory catalog registry (`catalog.InMemory`) is populated by scanning the finalized heap pages of `pg_class`, `pg_attribute`, etc. (as designed in Phase 1).

### Observability

We will add a new counter to `pg_stat_wal_io` to track the number of catalog-related WAL records processed during recovery and normal operation.

## Verification Plan

### Automated Tests
- **TestPureWALRecovery**: Perform DDL, manually delete the `pg_catalog.json` file, crash the server, and verify that the catalog is correctly restored from WAL upon restart.
- **TestCheckpointCatalogLSN**: Verify that checkpoints correctly capture and persist the last catalog mutation LSN.

### Manual Verification
- Inspect the server logs during startup to ensure WAL replay is correctly processing `RecordKindCatalog*` records.
- Verify that `pg_stat_wal_io` shows non-zero counts for `catalog_records` after DDL operations.

## What Landed (2026-05-04)

**Scope**: Supplement JSON catalog load with heap scan for crash-recovered user tables.
Full JSON decommission (M0030-0004 migration gate) is deferred.

### `internal/catalog/codec.go`
- `OIDToTypeName(oid uint32) string` — inverse of `TypeNameToOID`, maps pg_type OIDs
  back to goopg type name strings for column reconstruction.

### `internal/catalog/catalog.go`
- `TryRegisterUserTable(tbl *Table) error` — installs a user table recovered from
  heap with its original OID. Idempotent: if the table is already in the catalog
  (e.g. loaded from JSON), returns nil without modifying state. Advances `nextOID`
  past `tbl.OID` to prevent future OID collisions.

### `internal/initdb/open.go`
- `loadUserTablesFromHeap(mgr, cat)` — called in `Open()` after
  `loadSystemCatalogsIfPresent`. Three-pass scan:
  1. pg_class blocks → collect rows with `relkind='r'` and `OID >= FirstUserOID`
  2. pg_attribute blocks → collect column rows by `attrelid`
  3. For each user table: sort columns by attnum, build `catalog.Table`, call
     `TryRegisterUserTable`
  Visibility rule: `xmin ≠ 0 && xmax = 0` (committed, not deleted — conservative
  for startup, full MVCC deferred to M0030-0006).
  Safe on old clusters (no pg_class relfile → immediate return).
  Ordering relative to JSON load: JSON loads first, heap supplements missing tables.

### Effect
After `goopg init` + DDL + crash (before `SaveCatalog`):
- WAL replay restores pg_class/pg_attribute heap pages.
- `loadUserTablesFromHeap` finds the user table rows and registers them.
- Table is accessible even without the JSON snapshot.

`TestCreateTableSurvivesRestartViaCatalogHeap` proves this end-to-end:
creates a table, saves catalog, deletes JSON, restarts — table still present.

### Still Deferred
- JSON decommission (replace JSON path entirely with heap) — M0030-0004.
- Index recovery from heap (requires pg_index or extended pg_class).
- Checkpoint record storing last catalog mutation LSN.
- `pg_stat_wal_io` catalog_records counter.
