# REF-010: Parser & AST

## Overview

The parser converts SQL text into an abstract syntax tree (AST). goopg uses a handwritten recursive-descent parser rather than a parser generator (yacc/bison). It supports the SQL subset needed by pgbench, TPC-H, HammerDB, and the goopg test suite.

## goopg Implementation

**Package:** `internal/parser/`

### Key Types

- `parser` — the recursive-descent state machine. Holds a token slice, current position, and helper methods (`acceptKeyword`, `expectSymbol`, `parseExpr`, etc.).
- `Token` / `TokenKind` — lexer output: identifier, keyword, integer literal, string literal, operator, etc.
- `Keyword` — a string enum for SQL keywords. Only keywords used by the supported statement families are registered.
- `Stmt` — interface for all statement AST nodes (SelectStmt, InsertStmt, CreateTableStmt, etc.).
- `Expr` — interface for all expression nodes (BinaryOp, ColumnRef, IntegerConst, etc.).

### Lexer

`lexer.go` produces a `[]Token` from SQL input:
- Handles identifiers, keywords, numeric literals, string literals, operators.
- Dollar-quote support (`$$body$$`, `$tag$body$tag$`) for PL/pgSQL routine bodies.
- Positional parameters (`$1`..`$N`) for prepared statements.
- Single-line (`--`) and block (`/* */`) comments.

### Parser

`parser.go` dispatches on the first keyword token:
```
parseStatement()
  ├─ KwBegin → parseBegin
  ├─ KwSelect → parseSelect
  ├─ KwInsert → parseInsert
  ├─ KwCreate → parseCreate (→ parseCreateTable, parseCreateView, parseCreateFunction, …)
  ├─ KwDrop → parseDrop (→ parseDropTable, parseDropFunction, …)
  ├─ KwCall → parseCallStatement
  ├─ KwExplain → parseExplain
  ├─ KwWith → parseStatementWithCTE
  └─ … (others)
```

Expression parsing uses a precedence-climbing approach (`parseExpr` → `parseBinaryOp` → `parsePrefix` → `parsePrimary`).

### AST

The AST is defined as Go struct types in `ast.go`:
- SelectStmt: targets, from, where, group-by, having, order-by, limit, set-operation.
- InsertStmt: table, columns, values (RowsExpr or SelectStmt), on-conflict, returning.
- UpdateStmt: table, set-clauses, where, returning.
- CreateFunctionStmt, CreateProcedureStmt, etc.

## PostgreSQL Implementation

PostgreSQL's parser (`gram.y`):

- **Grammar file** — a 15 000+ line bison grammar (`gram.y`) and a hand-written lexer (`scan.l`). The grammar is far more comprehensive than goopg's.
- **Transformation** — after parsing, the raw parse tree goes through parse analysis (`parse_analyze.c`) which resolves names, types, and permissions. goopg's analyzer (`internal/analyzer/`) does similar work but with a much smaller surface.
- **Keyword categories** — PostgreSQL classifies keywords as reserved/unreserved/col-name to control whether they can be used as identifiers. goopg makes all registered keywords reserved.
- **CTE scope** — PostgreSQL handles WITH clause scoping with a dedicated analysis pass. goopg follows the same approach.

### Key Differences

| Aspect | goopg | PostgreSQL |
|--------|-------|------------|
| Parser type | Hand-written recursive descent | bison-generated LALR(1) |
| Grammar size | ~4 000 lines (parser + lexer + helpers) | ~15 000 lines (gram.y alone) |
| Keyword handling | All registered keywords reserved | Categorised (reserved/unreserved/col-name) |
| Dollar quotes | Supported for routine bodies | Full dollar-quote support |
| Expression parser | Precedence climbing | bison-generated (shift/reduce) |
| Analyse pass | Separate `internal/analyzer/` | `parse_analyze.c` + `transform.c` |

## Potential Optimisations or Corrections

- **Keyword categorisation** would let more SQL pass through without syntax errors (e.g. `OUT` as a column name in contexts where it's not a reserved keyword).
- **CTE scoping in parser** — goopg currently handles CTE scoping mostly in the analyzer and planner. Upstream does more at parse time.

## References

- goopg: `internal/parser/parser.go`, `internal/parser/ast.go`, `internal/parser/lexer.go`
- PG grammar: `postgres/src/backend/parser/gram.y`
- PG lexer: `postgres/src/backend/parser/scan.l`
- PG parse analysis: `postgres/src/backend/parser/parse_analyze.c`
