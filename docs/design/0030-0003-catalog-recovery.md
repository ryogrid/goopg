# WAL-Based Catalog Recovery (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | draft                          |
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
