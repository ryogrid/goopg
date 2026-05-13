# 0096-0007 — Partitioned Tables (LIST and RANGE)

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0096-0007

## Problem

Several isolation-test specs require `CREATE TABLE … PARTITION BY LIST/RANGE(col)`,
`CREATE TABLE child PARTITION OF parent FOR VALUES …`, and
`ALTER TABLE parent ATTACH PARTITION child FOR VALUES …`.  Without partition
support the global setup blocks fail immediately with a syntax error and none of
the specs can run.

## Solution

### Parser (internal/parser/)

**ast.go:**
- `PartitionByClause` — holds partition method (LIST/RANGE/HASH) and key columns.
- `PartitionOfClause` — holds parent name + LIST `InValues` or RANGE `FromValues`/`ToValues`.
- `CreateTableStmt.PartitionBy` — set when `PARTITION BY` clause present.
- `CreateTableStmt.PartitionOf` — set for `PARTITION OF parent FOR VALUES …`.
- `AlterTableActionKind.AlterTableAttachPartition` — new action kind.
- `AlterTableAction.AttachPartitionOf` — partition-of clause for ATTACH.

**ddl.go:**
- `parseCreateTableTail` intercepts `PARTITION OF parent` before the column list.
- `PARTITION BY {LIST|RANGE|HASH} (cols)` accepted after the closing `)`.
- `parsePartitionBoundValues()` handles MINVALUE/MAXVALUE and literal exprs.
- `parseAlterTableAction` handles `ATTACH PARTITION child FOR VALUES …`,
  `ATTACH PARTITION child DEFAULT`, and `DETACH PARTITION name` (no-op).

**function.go:**
- `RETURNS SETOF type` — optional SETOF prefix now accepted (silently ignored).

### Catalog (internal/catalog/)

**catalog.go:**
- `PartitionBound` struct — `InValues []string` for LIST, `From/To string` for RANGE.
- `Table.PartitionKey []string` — partition key columns (non-nil → partitioned table).
- `Table.PartitionMethod string` — "LIST" or "RANGE".
- `Table.PartitionParentOID uint32` — OID of parent if this is a partition child.
- `Table.PartitionBounds []PartitionBound` — routing bounds for this partition.
- `InMemory.partitionChildren map[uint32][]uint32` — parent → child OIDs.
- `InMemory.PartitionChildren(oid)` — returns child Table pointers.
- `InMemory.RegisterPartitionChild(parentOID, childOID)` — registers a child.
- `InMemory.FindPartitionForValue(oid, key)` — LIST routing.
- `InMemory.FindRangePartitionForValue(oid, key)` — RANGE routing.

### Executor DDL (internal/executor/operators_ddl.go)

- `execCreateTable` branches on `PartitionOf != nil` → calls `execCreatePartitionChild`.
- `execCreatePartitionChild` copies columns from parent, sets partition metadata, calls
  `RegisterPartitionChild`.
- `PartitionBy` metadata set on the parent table (PartitionMethod + PartitionKey).
- `execAlterTable` handles `AlterTableAttachPartition` — looks up child table and
  registers it with `RegisterPartitionChild`.
- `exprToString` helper converts IntegerConst/StringConst to string for bounds.

### Executor INSERT routing (internal/executor/operators_storage.go)

- `insertOp.Next()` detects partitioned tables (`len(plan.Table.PartitionKey) > 0`).
- `routeToPartition` evaluates the partition key column and calls
  `FindPartitionForValue` (LIST) or `FindRangePartitionForValue` (RANGE).
- Routed row is written to the child's RelFileNode with the child's column schema.

### Planner — partition-aware scan (internal/planner/planner.go)

- `planScanRangeVar` detects `tbl.PartitionKey != nil` after the virtual-table check.
- Calls `InMemory.PartitionChildren(tbl.OID)` to get the list of children.
- Builds a chain of `SetOp{All: true}` (UNION ALL) nodes over `SeqScan`s of each child.
- Falls back to a plain `SeqScan` of the parent if no children are registered yet.

## Limitations

- Hash partitioning is parsed but not routed (falls through to parent).
- Multi-column partition keys partially supported (routing uses key[0]).
- Partition pruning (WHERE clause optimisation) is not implemented — all children are
  always scanned.
- `FOR VALUES DEFAULT` partitions are parsed and registered but routing does not use
  them (a row with no matching child goes to the parent heap).
- `DETACH PARTITION` is parsed and silently ignored.
- Partition inheritance of NOT NULL constraints and indexes is not implemented.

## Effect

- `partition-key-update-1` setup advances past all PARTITION BY / PARTITION OF
  syntax and fails at CREATE TRIGGER (M0096-0012 prerequisite).
- `merge-update` setup advances past partition DDL and function definition and
  reaches a runtime error (`integer datum cannot encode as text`) during INSERT
  routing — closer to needing MERGE INTO (M0096-0010).
- All core unit tests pass.
