# Transactional DDL Foundation (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | draft                          |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

Currently, goopg's DDL operations are immediate and cannot be rolled back. This makes it impossible to perform safe schema migrations within a transaction. By using heap tables for catalogs, we can leverage the existing MVCC and WAL infrastructure to support transactional DDL.

## Upstream reference

Primary sources:
- `postgres/src/backend/commands/tablecmds.c` — DDL command implementations.
- `postgres/src/backend/catalog/heap.c` — Heap table management for catalogs.
- `postgres/src/backend/access/heap/heapam.c` — MVCC implementation for heap tables.

## Proposed Changes

### MVCC-Aware Catalog Mutations

Since system catalogs are now real heap tables, we will use the standard `heap.Insert`, `heap.Delete`, and `heap.Update` operations for catalog mutations. These operations automatically include the current transaction's `xmin` and `xmax`.

1.  **xmin Stamping**: New catalog tuples will be stamped with the `xmin` of the DDL transaction.
2.  **Visibility**: Other concurrent transactions will not see these tuples until the DDL transaction commits, thanks to existing `mvcc.TupleVisible` rules.

### Transactional Rollback

If a transaction performing DDL is rolled back:

1.  **Catalog Reversion**: Standard MVCC visibility ensures that the uncommitted catalog changes are ignored.
2.  **WAL Redo**: During crash recovery, if the commit record for a DDL transaction is missing, the catalog mutations are never finalized on disk.
3.  **Physical Cleanup**: (Optional) We will implement a mechanism to clean up physically created files for relations that were created in a rolled-back transaction (mirrors PostgreSQL's `PendingRelDelete` mechanism).

### Shared Catalog Cache Invalidation

One challenge with transactional DDL is maintaining the consistency of the in-memory catalog cache (`catalog.InMemory`).

1.  **Local Cache**: Each transaction will have a local "pending changes" list for the catalog.
2.  **Global Invalidation**: Upon commit, we will signal all other backends to invalidate their in-memory catalog caches so they can re-read the updated heap tables.

## Verification Plan

### Automated Tests
- **TestTransactionalCreateTable**: Run `BEGIN; CREATE TABLE t1; ROLLBACK;` and verify that `t1` does not exist and no physical file was left behind.
- **TestConcurrentDDLVisibility**: Verify that a concurrent transaction does not see a table being created until the creating transaction commits.
- **TestTransactionalDropTable**: Run `BEGIN; DROP TABLE t1; ROLLBACK;` and verify that `t1` still exists.

### Manual Verification
- Use `psql` to perform complex DDL operations inside transactions and verify the results after `COMMIT` and `ROLLBACK`.
- Verify that multiple sessions stay in sync with catalog changes.
