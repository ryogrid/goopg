# 0015-0004 — PL/pgSQL Body Parser and AST

**Status:** accepted (step 4a — Block + RETURN; DECLARE / assignment
/ IF / loops / PERFORM / SELECT INTO / embedded SQL deferred to
subsequent slices)
**Milestone:** [0015 — PL/pgSQL Stored Routines (Function-First Delivery)](../../milestones/0015-plpgsql-stored-routines-function-first.md)
**Spans seam:** new `internal/plpgsql` package, `parser.ParseExpr`
public API, `KwReturn` keyword.
**Cross-links:**
[0015-0001](0015-0001-create-function-parser-and-ast.md)
(CREATE FUNCTION parser surface),
[0015-0002](0015-0002-pg-proc-catalog-and-routine-registry.md)
(routine registry),
[0015-0003](0015-0003-create-function-executor-wiring.md)
(executor wiring).

## Context

Step 3 lands routines as opaque-text rows in `cat.Routines()`.
Before the interpreter (step 5) can run them, the body bytes need
to parse into a structured AST the interpreter can walk. This
slice introduces the PL/pgSQL parser package — scoped to the
smallest useful subset (Block + RETURN) so subsequent loops can
add DECLARE / assignment / IF / loops / etc. without reworking the
package skeleton.

## Why a separate package

`internal/parser` handles SQL — `SelectStmt`, `InsertStmt`, etc. —
and its node interfaces are SQL-shaped. PL/pgSQL is structurally
distinct (control flow, local variables, exception blocks), and
shoehorning routine-body nodes into the SQL AST would either:

- Pollute SQL nodes with `IsRoutineBody bool`, or
- Force the planner to type-switch on PL/pgSQL nodes that never
  appear in top-level SQL.

A dedicated package keeps both surfaces clean. PL/pgSQL nodes
embed SQL `parser.Expr` for inline expressions (the `RETURN expr`
case here), so the existing type-checker / planner / executor
machinery is reused without translation.

## Reusing the SQL lexer

The PL/pgSQL parser calls `parser.Lex(src)` to produce tokens —
no need for a separate tokeniser. PL/pgSQL keywords (BEGIN / END /
RETURN; future DECLARE / IF / LOOP / etc.) live in
`internal/parser/token.go` alongside SQL keywords. This means
`BEGIN` and `END` (already there for transaction control) are
reused; `RETURN` is the only new keyword this slice adds.

Identifiers, integer / numeric / string literals, dollar-quoted
literals (already wired in step 1), parameter refs (`$1`),
operators, and qualified names all tokenise identically to SQL.

## Public API: parser.ParseExpr

```go
func parser.ParseExpr(input string) (Expr, error)
```

The PL/pgSQL parser scans forward for the next top-level `;` to
find the end of a `RETURN` expression, slices the source bytes,
and feeds them through `ParseExpr`. The resulting `parser.Expr` is
the same shape a SELECT target list would produce — keeping the
analyzer / planner / executor reusable when the interpreter
arrives.

Trailing-token check: `ParseExpr` rejects input with content past
the parsed expression so a caller passing `1 + 2; garbage` gets a
clean diagnostic.

## AST shape

```go
type Stmt interface {
    Pos() int
    plpgsqlStmtNode()
}

type Block struct {
    pos        int
    Statements []Stmt
}

type ReturnStmt struct {
    pos  int
    Expr parser.Expr
}
```

The unexported `plpgsqlStmtNode()` marker mirrors the SQL parser's
unexported `stmtNode()` — keeps the `Stmt` interface closed to
in-package types so future interpreter type-switches stay
exhaustive.

## Grammar (Stage A 4a)

```
body      ::= 'BEGIN' stmt_list 'END' [';']
stmt_list ::= ε
            | stmt_list stmt
stmt      ::= 'RETURN' sql_expr ';'
```

Trailing semicolon after `END` is optional — both forms are
upstream-legal and PL/pgSQL function bodies almost always include
it.

## Error model

`plpgsql.SyntaxError{Pos, Message}` is the typed sentinel. Pos is
a 0-based byte offset within the body source. Lexer errors from
`parser.Lex` are wrapped into the same envelope so callers don't
have to type-switch on `*parser.LexError` separately.

Specific Stage-A diagnostics:

- Missing `BEGIN` → `"expected BEGIN at start of PL/pgSQL body"`.
- Missing `END` → `"expected END to close PL/pgSQL block"`.
- Statement other than `RETURN` →
  `"unsupported PL/pgSQL statement (Stage A 4a accepts RETURN only)"`.
- `RETURN;` without value →
  `"RETURN requires an expression in Stage A"`.
- Bad expression inside RETURN → `"RETURN expression: <inner>"`,
  pinned at the expression's start position.
- Trailing tokens after `END` → `"unexpected tokens after END"`.

## Tests

`internal/plpgsql/parser_test.go` (11 cases):

- `TestParseReturnConstant` — `BEGIN RETURN 42; END` produces a
  `Block` with a single `ReturnStmt` whose expression is an
  `*parser.IntegerConst` of value 42.
- `TestParseReturnExpression` — `RETURN x + 1` produces a
  `*parser.BinaryOp`.
- `TestParseEmptyBlock` — `BEGIN END` parses cleanly with zero
  statements.
- `TestParseBlockTrailingSemicolon` — `BEGIN ... END;` is
  equivalent to `BEGIN ... END`.
- `TestParseMultipleStatements` — multiple `RETURN`s parse;
  framework guard for future-slice extensibility.
- `TestParseRequiresBegin` — missing leading `BEGIN` surfaces a
  specific diagnostic.
- `TestParseRequiresEnd` — missing `END` before EOF.
- `TestParseRejectsUnsupportedStatement` — `PERFORM` surfaces the
  Stage-A-4a-scope diagnostic.
- `TestParseRejectsBareReturn` — `RETURN;` without value.
- `TestParseReturnExpressionError` — bad expression pins the
  diagnostic at the expression's start.
- `TestParseLexErrorWrapped` — upstream `parser.LexError` round-
  trips through `errors.As(*SyntaxError)` so callers don't have to
  type-switch on the lexer error separately.

Full `go test ./...` green.

## Out of scope (Stage A step 4 follow-ups)

- `DECLARE` block + local variable typing — step 4b.
- Assignment `varname := expr;` — step 4b.
- `IF cond THEN ... [ELSIF ...] [ELSE ...] END IF;` — step 4c.
- `LOOP / WHILE / FOR / EXIT / CONTINUE` — step 4d.
- `PERFORM expr;` — step 4e.
- `SELECT ... INTO target_list` — step 4f.
- Embedded `INSERT / UPDATE / DELETE / SELECT` statements — step
  4g (delegates to the SQL parser via `parser.Parse`; needs a
  protocol for collecting bind-parameter references).
- Exception blocks (`EXCEPTION WHEN ...`) — Stage B.
- `RETURN NEXT` / `RETURN QUERY` (set-returning functions) —
  Stage B.

## Out of scope (M0015 step 5+)

- The interpreter that walks `*Block` nodes — step 5.
- The SPI bridge that routes embedded SQL through the existing
  planner / executor — step 5.
- Function invocation in expression contexts (the FuncCall
  resolver path) — step 6.
