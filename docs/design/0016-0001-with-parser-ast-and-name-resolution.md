# 0016-0001 — WITH Parser, AST, and Name Resolution (Step 1: Parser & AST)

**Status:** accepted (parser/AST step)
**Milestone:** [0016 — WITH Clause (CTE) Support](../milestones/0016-with-clause-cte-support.md)
**Spans seam:** parser AST nodes, grammar entry-point, byte-position
diagnostics for malformed CTE syntax.
**Cross-links:**
[root-0010](root-0010-parser.md) (parser baseline),
[0003-0008](0003-0008-subqueries.md) (existing subquery infrastructure
this slice's AST sits next to),
[0003-0014](0003-0014-derived-tables.md) (FROM-clause parser).

## Context

goopg's parser today recognises `SELECT [DISTINCT] target FROM ...`
without `WITH`. M0016's Stage A (non-recursive CTEs) requires:

1. AST nodes for the WITH list and individual CTE definitions.
2. Parser support for `WITH cte_name [(col, ...)] AS (subquery) [, ...] SELECT ...`
   and `WITH RECURSIVE cte ...` (the parser accepts `RECURSIVE` even
   though execution is Stage B — clean syntax-level errors are the
   same regardless of execution support).
3. CTE references in `SELECT`, `INSERT ... SELECT`,
   `UPDATE ... FROM`, and `DELETE ... USING` (the four supported
   statement shapes the milestone DoD calls out).

This step 1 lands **just the parser/AST**. Analyzer name resolution,
planner integration, and execution are subsequent slices
(0016-0001 step 2 / 0016-0002).

## AST shape

```go
type CommonTableExpr struct {
    pos       int
    Name      string       // cte_name
    Columns   []string     // optional column-alias list (nil if absent)
    Query     *SelectStmt  // the parenthesised subquery
}

type WithClause struct {
    pos       int
    Recursive bool                 // WITH RECURSIVE flag
    CTEs      []*CommonTableExpr   // ordered, left-to-right declaration
}
```

`SelectStmt` (and the corresponding `InsertStmt` / `UpdateStmt` /
`DeleteStmt` shapes) grow a `With *WithClause` field. `nil` means
"no WITH clause" so existing tests are byte-for-byte unchanged.

The `*CommonTableExpr.Query` is a `*SelectStmt` because Stage A
restricts CTE bodies to `SELECT` queries — the milestone's
"data-modifying CTE chains beyond the supported subset" out-of-scope
clause covers `WITH x AS (INSERT ...)`. Step 1 enforces that at the
parser level: a `(` after `AS` followed by anything other than
`SELECT` (or another `WITH ... SELECT`) is a syntax error with a
clear "WITH (data-modifying) is not supported in v0" message.

## Grammar

```
WithClause      := "WITH" ["RECURSIVE"] CTEList
CTEList         := CTE ("," CTE)*
CTE             := Identifier ["(" IdentList ")"] "AS" "(" SelectStmt ")"
IdentList       := Identifier ("," Identifier)*
SelectWithCTE   := WithClause SelectStmt
InsertWithCTE   := WithClause InsertStmt
UpdateWithCTE   := WithClause UpdateStmt
DeleteWithCTE   := WithClause DeleteStmt
```

The parseStatement dispatch already keys on the first keyword. A
top-level `WITH` is caught at that layer: `parseStatement` peeks
ahead past the WithClause to determine the inner statement shape and
routes to `parseSelect` / `parseInsert` / `parseUpdate` /
`parseDelete`, which each accept an optional pre-parsed WithClause
parameter. This keeps the existing per-statement parsers intact and
adds one new entry point (`parseWithClause`).

## Parser entry point

```go
// parseWithClause is invoked by parseStatement when the first
// token is KwWith. Returns the parsed WithClause and leaves the
// parser's cursor positioned at the next token (typically SELECT /
// INSERT / UPDATE / DELETE) so the caller can dispatch on it.
func (p *parser) parseWithClause() (*WithClause, error)
```

The per-statement parsers each grow an unexported overload that
accepts a pre-parsed `*WithClause`:

```go
func (p *parser) parseSelectWithCTE(with *WithClause) (Stmt, error)
// ditto for Insert / Update / Delete
```

## Errors and byte-position diagnostics

- Empty CTE list (`WITH SELECT ...`) → `"expected CTE name after WITH"` at the SELECT keyword's position.
- Missing `AS` (`WITH foo (SELECT ...)`) → `"expected AS after CTE name"` at the `(` position.
- Missing parenthesised subquery (`WITH foo AS SELECT ...`) → `"expected ( after AS"` at the SELECT keyword's position.
- Non-SELECT body in Stage A (`WITH foo AS (INSERT ...)`) → `"data-modifying CTE bodies are not supported in v0 (Stage A only allows SELECT)"` at the inner statement's keyword position.
- Trailing CTE separator (`WITH foo AS (SELECT 1), SELECT ...`) → `"expected CTE name after ,"` at the SELECT keyword.
- `WITH RECURSIVE` is accepted at the parser level — analyzer rejects unsupported recursive shapes in Stage B prep work; for Stage A, the `Recursive` flag flows through but reading it at planner / executor time produces the SQLSTATE 0A000 error pinned by the milestone's gate-B fail-fast contract.

## Out of scope (this step)

- Analyzer name resolution (CTE scope rules, alias arity validation,
  shadowing diagnostics) — 0016-0001 step 2.
- Planner / executor for non-recursive CTEs — 0016-0002.
- Recursive execution semantics — 0016-0003.
- Observability hooks in EXPLAIN — 0016-0004.

## Tests

Add to `internal/parser/parser_test.go`:

- `TestParseSimpleWithClause` — `WITH a AS (SELECT 1) SELECT * FROM a` produces a `SelectStmt` with `With.CTEs[0].Name == "a"` and `With.Recursive == false`.
- `TestParseMultipleCTEs` — `WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM b` parses two CTEs in declaration order; positions match.
- `TestParseWithRecursiveAccepted` — `WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r` sets `With.Recursive = true`; planner-level rejection is a separate slice.
- `TestParseWithColumnAliases` — `WITH a(x, y) AS (SELECT 1, 2) SELECT * FROM a` populates `Columns = ["x", "y"]`.
- `TestParseWithRejectsDataModifyingBody` — `WITH a AS (INSERT INTO t VALUES (1)) SELECT * FROM a` returns a clear syntax error pointing at the INSERT keyword.
- `TestParseWithFlowsToInsertUpdateDelete` — confirm the WithClause attaches to InsertStmt / UpdateStmt / DeleteStmt for the supported shapes.
