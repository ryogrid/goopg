# M0097-0023 — INCLUDE columns, `pg_get_indexdef`, `pg_get_constraintdef`, pg_constraint UNIQUE/PK rows

**Status:** Implemented (2026-06-07, loop37)
**Area:** parser / catalog / executor DDL+expr
**Predecessor:** `0097-0023-pg-constraint-population.md` (loop35-36)

## Problem

`index_including` regress test was at 204 diffs. The main gaps were:

1. `INCLUDE (cols)` in `CREATE INDEX` and all constraint forms was parsed but
   discarded — stored as empty lists everywhere.
2. `pg_get_indexdef(oid)` was not implemented.
3. `pg_get_constraintdef(oid)` was not implemented.
4. `pg_constraint` had no rows for UNIQUE or PRIMARY KEY constraints (only
   named CHECK constraints were emitted).
5. `CREATE INDEX CONCURRENTLY` parsed `CONCURRENTLY` as the index name.
6. `ALTER TABLE DROP COLUMN` left index orphans instead of dropping dependent
   indexes.

## Changes

### Parser (`internal/parser/`)

**`ast.go`** — new AST fields:
- `CreateIndexStmt.IncludeColumns []string`
- `CreateTableStmt.TableUniqueIncludes [][]string`, `PrimaryKeyInclude []string`
- `CreateTableStmt.NamedConstraints []TableConstraintDef`
- `AlterTableAction.IncludeColumns []string`
- New `AlterTableAddUnique` action kind
- New `TableConstraintDef` struct (Name, Columns, IncludeColumns, IsPrimary)

**`ddl.go`** — parsing:
- `CREATE INDEX … INCLUDE (cols)` now stores into `IncludeColumns`
- `CONSTRAINT name PRIMARY KEY/UNIQUE … INCLUDE` parsed for all create-table
  and ALTER TABLE ADD forms
- `ALTER TABLE ADD UNIQUE (cols) INCLUDE (incl)` → `AlterTableAddUnique`
- `CREATE INDEX CONCURRENTLY` — skip `CONCURRENTLY` token before optional name

### Catalog (`internal/catalog/catalog.go`)

- `Index` struct: added `IncludeColumns []string` and `IsConstraint bool`
- `Catalog` interface: added `AllIndexes() []*Index`
- `BuildIndexDef(idx *Index) string` — exported helper for `CREATE [UNIQUE]
  INDEX name ON schema.table USING method (key_cols) [INCLUDE (incl_cols)]`
- `pg_index` VirtualRows: full implementation (indkey, indclass, booleans)
- `pg_constraint` VirtualRows: added UNIQUE/PK rows from constraint-backed
  indexes (`IsConstraint == true`), with `contype = 'u'/'p'`, `conkey` as
  `{1,2}` int2[] literal of key column attnums

### Executor DDL (`internal/executor/operators_ddl.go`)

- All constraint index creation paths set `idx.IsConstraint = true`:
  named PRIMARY KEY/UNIQUE in CREATE TABLE, anonymous PK, inline column
  UNIQUE, table-level UNIQUE, `ALTER TABLE ADD PRIMARY KEY`, `ADD UNIQUE`,
  `ADD CONSTRAINT … UNIQUE/PRIMARY KEY`
- `execAlterTableAddUnique` — new function for `AlterTableAddUnique`
- `pgDeduplicateColNames` — mirrors PG's `ChooseIndexColumnNames` for
  auto-index-name deduplication (second `c1` becomes `c11`)
- `autoIndexNameWithIncludes` — includes INCLUDE cols in auto-name, with
  deduplication
- `execAlterDropColumn` — before rewriting the heap, now drops (from catalog
  and storage) all indexes that reference the dropped column in either key
  columns or INCLUDE columns

### Executor expr (`internal/executor/expr.go`)

- `pg_get_indexdef(oid)` — looks up index by OID in `AllIndexes()`, returns
  `BuildIndexDef` string
- `pg_get_constraintdef(oid [, pretty])` — looks up constraint-backed index
  by OID, returns `PRIMARY KEY (cols) [INCLUDE (incl)]` or
  `UNIQUE (cols) [INCLUDE (incl)]`
- `::regclass` cast OID→name fallback — also resolves index OIDs to index
  names (for cases where `relid::regclass` is called on an index OID)

## Result

`index_including` diffs: 204 → 78 (62% reduction).

Remaining 78 diffs are in categories that require further work:
- `\d index_name` key/non-key column display (9 lines)
- EXCLUDE constraint support (10 lines)
- `box(text)` constructor not implemented, causing cascading failures (≈55 lines)
- Float vs integer formatting for `c3 int` column (2 lines)
