# DDL WAL Record Kinds (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (landed 2026-05-04)   |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

Currently, goopg's DDL operations are not WAL-logged. This means schema changes are not crash-safe and cannot be replicated to standby servers or logical subscribers. To solve this, we need to introduce WAL records for catalog mutations and physical storage changes.

## Upstream reference

Primary sources:
- `postgres/src/include/catalog/storage_xlog.h` — Definition of `xl_smgr_create` and `xl_smgr_truncate`.
- `postgres/src/backend/catalog/storage.c` — `log_smgrcreate` and `log_smgrtruncate` implementations.
- `postgres/src/backend/access/heap/heapam.c` — `heap_redo` for catalog heap pages.

## Proposed Changes

### New WAL Record Kinds

We will define new record kinds in `internal/wal/recovery.go` to capture catalog mutations and physical file operations.

| Kind | Name | Description |
| ---- | ---- | ----------- |
| 11   | `RecordKindCatalogInsert` | Insert a row into a system catalog heap table. |
| 12   | `RecordKindCatalogDelete` | Delete a row from a system catalog. |
| 13   | `RecordKindCatalogUpdate` | Update a row in a system catalog. |
| 14   | `RecordKindSmgrCreate`   | Physical relation file creation (mirrors `xl_smgr_create`). |
| 15   | `RecordKindSmgrTruncate` | Physical relation file truncation (mirrors `xl_smgr_truncate`). |

### DDL Executor Wiring

The DDL executor (`internal/executor/operators_ddl.go`) will be updated to emit these records.

- `execCreateTable`: Emits `RecordKindSmgrCreate` for the new relfile and `RecordKindCatalogInsert` for the `pg_class` and `pg_attribute` entries.
- `execDropTable`: Emits `RecordKindCatalogDelete` for the catalog entries. (Physical file deletion is handled by checkpoint/vacuum later, or logged via `SmgrTruncate` if immediate cleanup is desired).

### Redo Handlers

`internal/wal/recovery.go` will implement handlers for these new kinds:

- `replayCatalogInsert/Delete/Update`: Similar to `replayHeapInsert/Delete`, but specifically targeting system catalogs.
- `replaySmgrCreate`: Calls `mgr.Extend` (or a new `mgr.Create`) to ensure the physical file exists.
- `replaySmgrTruncate`: Calls `mgr.Truncate`.

### WAL Classifier Integration

The WAL classifier (`internal/wal/classifier.go`) will be updated to recognize these new kinds. This is critical for logical replication:
- When a `RecordKindCatalogInsert` for `pg_class` is seen, the logical decoder can emit a `Relation` message to the subscriber, allowing it to stay in sync with the publisher's schema.

## Verification Plan

### Automated Tests
- **TestDDLWALRoundTrip**: Perform a `CREATE TABLE` and verify that the corresponding WAL records are emitted and can be decoded.
- **TestCatalogRedo**: Crash the server after a DDL operation but before a checkpoint, and verify that the schema change is recovered upon restart.
- **TestLogicalDDLDecoding**: Verify that the WAL classifier correctly identifies catalog changes and drives the logical decoder.

### Manual Verification
- Use `pg_waldump` (if/when implemented for goopg) or a custom tool to inspect the WAL stream and verify the presence of `RecordKindCatalog*` and `RecordKindSmgr*` records.
