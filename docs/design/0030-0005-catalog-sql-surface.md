# Catalog SQL Surface and OID Resolution (Milestone 0030)

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | draft                          |
| Date        | 2026-05-01                     |
| Milestone   | 0030 — Catalog Persistence and DDL WAL |
| Refines     | [docs/milestones/0030-catalog-persistence-and-ddl-wal.md](../milestones/0030-catalog-persistence-and-ddl-wal.md) |
| Supersedes  | —                              |

## Problem

Currently, `pg_attribute` and `pg_type` are virtual views, and type resolution in `pgoutput` is hard-coded. By transitioning these to real heap tables, we can provide a more robust SQL interface for metadata discovery (supporting tools like JDBC/ODBC) and a dynamic type system.

## Upstream reference

Primary sources:
- `postgres/src/include/catalog/pg_attribute.h` — `pg_attribute` structure.
- `postgres/src/include/catalog/pg_type.h` — `pg_type` structure.
- `postgres/src/include/catalog/pg_index.h` — `pg_index` structure.

## Proposed Changes

### SQL-Queryable pg_attribute and pg_type

Since `pg_attribute` and `pg_type` are now real heap tables (Phase 1), they are natively queryable via SQL.

- **pg_attribute**: Supports queries like `SELECT attname, atttypid FROM pg_attribute WHERE attrelid = 16407`.
- **pg_type**: Supports queries like `SELECT typname, oid FROM pg_type`.

### pg_index as a Real Heap Table

We will migrate index metadata (currently stored in Go `*Index` structs) to a real `pg_index` heap table. This allows for SQL discovery of index properties (uniqueness, primary key status, indexed columns).

### Dynamic OID Resolution

We will replace the hard-coded `pgoTypeOIDFor()` mapping in `internal/wal/pgoutput.go` with a real lookup against the `pg_type` heap table.

- **Initialization**: At slot creation or startup, the pgoutput plugin reads the `pg_type` table to build its internal name→OID map.
- **Dynamic Updates**: If new types are added (not in v0 scope, but supported by the architecture), the plugin can refresh its map.

### Extending Catalog Views

We will extend existing views in `pg_catalog` (like `pg_tables`, `pg_indexes`) to include more standard columns expected by external tools, sourcing them directly from the heap tables.

## Verification Plan

### Automated Tests
- **TestAttributeQuery**: Verify that `pg_attribute` returns correct column metadata for a user-defined table.
- **TestTypeResolution**: Verify that `pgoutput` correctly uses OIDs from `pg_type` for all supported data types.
- **TestIndexCatalog**: Verify that `pg_index` correctly reflects the properties of created indexes.

### Manual Verification
- Connect to `goopg` using `psql` and run `\d` and `\dt` commands to ensure they work correctly against the new heap-backed catalogs.
- Verify that `SELECT * FROM pg_type` returns a comprehensive list of system types.
