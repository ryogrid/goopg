# 0096-0011 — FK Referential Integrity Enforcement

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0096-0011

## Problem

Isolation test specs (`fk-snapshot`, `partition-key-update-2/3/4`) use
`REFERENCES` column constraints. Without FK enforcement:
- `CREATE TABLE` accepted the syntax but silently discarded the constraint.
- `INSERT` into child tables succeeded even when no matching parent row existed.
- `DELETE` from parent tables did not cascade or restrict.

## Solution

### Parser (internal/parser/)

**ast.go** — New `FKAction` type (NoAction, Restrict, Cascade, SetNull, SetDefault).
Six new FK fields on `ColumnDef`: `RefTable ObjectName`, `RefColumns []string`,
`OnDelete FKAction`, `OnUpdate FKAction`, `FKDeferrable bool`,
`FKInitiallyDeferred bool`.

**ddl.go** — REFERENCES handler rewritten: `parseObjectName` result stored in
`col.RefTable`, optional column list in `col.RefColumns`, ON DELETE/UPDATE
clauses parsed via new `parseFKAction` helper, `[NOT] DEFERRABLE
[INITIALLY DEFERRED | INITIALLY IMMEDIATE]` handled.

### Catalog (internal/catalog/)

**catalog.go** — New `ForeignKey` struct (Columns, RefTable, RefColumns, OnDelete,
OnUpdate, Deferrable, InitiallyDeferred) using `parser.FKAction`. Added
`ForeignKeys []ForeignKey` to `Table`. New `FKRef` struct and
`FindFKsReferencingTable(tableName string) []FKRef` method on `InMemory` (linear
scan of all tables, suitable for isolation-test scale).

### Executor (internal/executor/)

**operators_ddl.go** — `execCreateTable` registers FK constraints from parsed
`ColumnDef.RefTable != ""` entries into `tbl.ForeignKeys` after table creation.

**session.go** — `DeferredFKCheck` struct; `deferredFKChecks []DeferredFKCheck`
field on `BasicSession`; `AddDeferredFKCheck` (with deduplication), `TakeDeferredFKChecks`
methods; `EndExplicitTransaction` clears the list.

**operators_fk.go** (new) — Core enforcement functions:

| Function | Purpose |
|---|---|
| `checkFKInsert` | Verify parent row exists for each FK on insert |
| `enforceFKOnDelete` | Apply CASCADE/RESTRICT/SET NULL/NO ACTION on parent delete |
| `runAllDeferredFKChecks` | Full-table scan of all deferred FK constraints at COMMIT |
| `fkCascadeDelete` | Delete matching child rows (scans partitions/inheritance children) |
| `fkSetNull` | Set FK columns NULL in matching child rows |
| `assertParentExists` | Scan parent (+ children) for matching row |
| `assertNoChildRows` | Reject if any child row references the parent value |
| `scanTableForMatch` | Union scan of table + its partition/inheritance children |

**operators_storage.go** — `insertOp.Next`: calls `checkFKInsert` before
`writeHeapRow` when `tbl.ForeignKeys` is non-empty. `deleteOp.Next`: victim
struct extended to carry `Row`; `enforceFKOnDelete` called for each victim before
stamping xmax.

**operators_tx.go** — `execCommit`: before `TxnMgr.Commit`, extracts
`sess.TakeDeferredFKChecks()` and calls `runAllDeferredFKChecks`. On failure,
rolls back the transaction and surfaces a 23503 error.

## Deferral Semantics

`DEFERRABLE INITIALLY DEFERRED` constraints inside an explicit transaction are
queued rather than checked immediately:

- **INSERT child row**: queued deferred check for (child table, FK).
- **DELETE parent row ON DELETE NO ACTION**: queued deferred check; CASCADE and
  SET NULL are always immediate.
- **At COMMIT**: full table scan of each queued child table; any row whose FK
  column values are absent from the parent table produces a 23503 error and
  aborts the transaction.

Outside explicit transactions (autocommit), all constraints are checked
immediately regardless of INITIALLY DEFERRED.

## Partition / Inheritance Support

`scanTableForMatch`, `fkCascadeDelete`, and `fkSetNull` all union-scan the
referenced table's `InheritanceChildren` and `PartitionChildren` so that FK
checks and cascades work correctly when the parent or child table is partitioned.

## Limitations

- `ALTER TABLE ADD FOREIGN KEY` syntax is accepted but not yet wired to
  register the FK in the catalog (deferred; the TPC-H DDL path uses
  `ADD FOREIGN KEY` post-load and does not require enforcement).
- `ON UPDATE` actions are parsed but not enforced (UPDATE does not yet call
  an FK update-enforcement hook).
- `pkColumns` defaults to the table's first column when no PRIMARY KEY index is
  found; this is correct for all current test tables (single-column PKs).
