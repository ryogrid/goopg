# REF-010: Parser & AST

…(existing content up to "Key Differences" unchanged)…

## PostgreSQL Implementation (Deep Dive)

### Keyword Categorisation

PostgreSQL classifies keywords into three categories that
control whether they can be used as identifiers:

| Category | Example | Can be used as identifier? |
|----------|---------|---------------------------|
| **Reserved** | `SELECT`, `FROM`, `WHERE` | No (must be quoted) |
| **Unreserved** | `BEGIN`, `COMMIT`, `VACUUM` | Yes |
| **Col-name** | `YEAR`, `MONTH`, `ZONE` | Yes, in column position |

goopg treats all registered keywords as reserved. This means
queries that use keywords like `begin`, `out`, `call` as column
names or table names may fail.

### Parse Analysis (`parse_analyze.c`)

After the raw parse tree is built, PostgreSQL runs it through
`parse_analyze.c`, which performs:

1. **Name resolution** — resolves `ColumnRef` to catalog columns.
2. **Type resolution** — determines output types for expressions.
3. **Permission checking** — verifies access rights.
4. **Coercion** — inserts implicit type casts.
5. **Subquery flattening** — merges simple subqueries.
6. **Window function validation** — checks window function
   placement and arguments.

goopg's `analyzer` does name resolution and some type checking
but omits permission checking, implicit coercion, and subquery
flattening.

### Raw Expression Tree Walker

PostgreSQL provides `raw_expression_tree_walker()` for
traversing raw parse trees. This is used by the analyser,
rewriter, and planner to walk the tree without knowing every
node type. goopg uses type switches in each pass.

### rewriter (`rewriteHandler.c`)

PostgreSQL has a **rewriter** phase between analysis and
planning. The rewriter applies:
- **View expansion** — replaces `FROM v` with the view's
  underlying SELECT.
- **Rule expansion** — applies CREATE RULE transformations.
- **CTE inlining** — decides whether to materialise or inline
  CTEs (controlled by `MATERIALIZED` / `NOT MATERIALIZED`).

goopg handles view expansion in the planner's
`planScanRangeVar` and handles CTEs in `preplanWithClause`.

## goopg Improvement Analysis

### P1: Keyword Categorisation

Add `isReserved` and `isColName` keyword categories. Allow
unreserved and col-name keywords as identifiers in appropriate
positions.

**Impact:** Improves SQL compatibility. Reduces the need to
quote column names that happen to match PG keywords.

### P2: Type Coercion

Add implicit type coercion in the analyzer:
- `INSERT int → text` column → auto-cast.
- `int2 + int4` → promote to `int4`.
- String literal `'42'` used as `int` → implicit parse.

**Impact:** Reduces false type-mismatch errors.

## References

- goopg: `internal/parser/parser.go`, `internal/parser/ast.go`
- PG grammar: `postgres/src/backend/parser/gram.y`
- PG parse analysis: `postgres/src/backend/parser/parse_analyze.c`
- PG rewriter: `postgres/src/backend/rewrite/rewriteHandler.c`
- PG keyword categories: `postgres/src/include/parser/kwlist.h`
