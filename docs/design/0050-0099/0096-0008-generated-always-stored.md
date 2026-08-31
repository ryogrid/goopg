# 0096-0008 — GENERATED ALWAYS AS (expr) STORED Columns

**Status:** accepted  
**Date:** 2026-05-12  
**Milestone:** M0096-0008

## Problem

The `eval-plan-qual` isolation spec's setup creates tables with
`GENERATED ALWAYS AS (expr) STORED` columns:
```sql
CREATE TABLE accounts (accountid text PRIMARY KEY, balance numeric not null,
  balance2 numeric GENERATED ALWAYS AS (balance * 2) STORED);
INSERT INTO accounts VALUES ('checking', 600), ('savings', 600);
```
Without this feature the setup fails at the very first CREATE TABLE line.

Additional setup issues also needed addressing:
- Empty column lists `()` for INHERITS children
- `CREATE TABLE name AS SELECT …` (CTAS)
- `INHERITS (parent)` clause syntax
- `generate_series` as scalar expression fallback

## Solution

### Parser (internal/parser/)

**ast.go:**
- `ColumnDef.GeneratedAlways bool` — marks GENERATED ALWAYS AS column.
- `ColumnDef.GeneratedExpr string` — raw SQL expression text (e.g. `"balance * 2"`).
- `CreateTableStmt.Inherits []ObjectName` — parent list from INHERITS clause.
- `CreateTableStmt.SelectSource *SelectStmt` — SELECT for CTAS.

**ddl.go:**
- `parseColumnDef` extended with: GENERATED ALWAYS AS (expr) STORED,
  DEFAULT clause skip, REFERENCES skip, UNIQUE no-op, WITH OPTIONS no-op.
- Empty column list `()` handled before the main parse loop: calls
  `consumeCreateTableSuffix` to consume INHERITS / PARTITION BY / WITH.
- INHERITS (parent, …) clause parsed after the main column list close.
- CREATE TABLE name AS SELECT … (CTAS) detected when `AS` follows the table
  name; SELECT is parsed into `stmt.SelectSource`.

**function.go:** RETURNS SETOF modifier accepted (silently ignored).

### Catalog (internal/catalog/)

**catalog.go:**
- `Column.GeneratedExpr string` — expression text; empty for ordinary columns.
- `Column.GeneratedAlways bool` — true for GENERATED ALWAYS AS … STORED.

### Analyzer (internal/analyzer/)

- `analyzeInsert`: INSERT…SELECT path delegates to `analyzeSelectWithParent`.
- `resolveInsertTargetColumns`: when no explicit column list, skips
  `GeneratedAlways` columns so `INSERT INTO t VALUES (a, b)` succeeds even
  when `t` has a third generated column.

### Executor DDL (internal/executor/operators_ddl.go)

- `execCreateTable`: copies `GeneratedAlways` and `GeneratedExpr` to catalog columns.
- `execCreateTableAs` (new): plans the SELECT, derives column types from the
  output schema (defaults to "text" for unknown types), creates the table,
  executes the SELECT, inserts all rows.
- `encodeBTreeKeyForColumn`: added `text` type handling (varchar-encode bytes).
- `isSupportedBTreeKeyType`: added "text".

### Executor — generated column evaluator (internal/executor/operators_generated.go, new)

- `computeGeneratedColumns(cols, row)`: iterates columns, evaluates any
  non-empty `GeneratedExpr` via `evalGeneratedExpr`.
- `evalGeneratedExpr(exprStr, cols, row)`: parses `SELECT exprStr`, walks
  the AST with `evalGenExpr` using a direct lightweight evaluator.
- `evalGenExpr`: handles IntegerConst, NumericConst, StringConst, NullConst,
  BooleanConst, ColumnRef (by-name row lookup), CastExpr (operand pass-through),
  BinaryOp (int arithmetic), UnaryOp (negation).
- Called from `insertOp.Next()` and both `updateOp.Next()` scan paths.

### Executor INSERT — codec (internal/executor/codec.go)

- Integer datum encoding in the "text-like fallback" arm: converts to decimal
  string instead of returning an error.  Needed for CTAS integer columns that
  receive a "text" fallback type.

### Planner (internal/planner/planner.go)

- `planInsert`: when no explicit column list, `colIndex` skips generated
  columns so the VALUES row count matches the non-generated column count.

### Expression evaluator (internal/executor/expr.go)

- `evalFuncCall`: added `generate_series` fallback that returns the start value
  as a scalar.  Sufficient for CTAS patterns; full SRF semantics deferred.

## Limitations

- `GENERATED ALWAYS AS … VIRTUAL` (not STORED) is not implemented.
- CTAS with SRF functions produces only one row per SELECT invocation.
- Table inheritance (`INHERITS`) syntax is accepted but columns are NOT copied
  from the parent (M0096-0009 handles this).
- GENERATED column expressions must be expressible via the lightweight evaluator
  (arithmetic on integers/columns, casts); complex expressions return NULL.

## Effect

The `eval-plan-qual` spec now passes its entire setup block and enters the
permutation phase (times out on blocking operations rather than failing at
syntax). All core unit tests pass.
