# Module: `internal/pl/plpgsql`

The **PL/pgSQL** language parser and AST — the frontend of the PL/pgSQL
interpreter. This package provides the `Parse` function that turns a
PL/pgSQL function-body string into a typed AST; the executor
(`internal/executor/plpgsql_runtime.go`) interprets the AST at function-call
time.

The language is a Go port of PostgreSQL's `src/pl/plpgsql/src/pl_gram.y`
and `pl_scanner.c`. It supports blocks, declarations, variable assignments,
`SELECT INTO`, `EXECUTE`, `FOR`/`WHILE`/`LOOP` loops, `IF`/`CASE` conditionals,
`RETURN`/`RETURN NEXT`/`RETURN QUERY`, `RAISE`, `PERFORM`, `EXIT`/`CONTINUE`,
exception blocks, and nested sub-blocks.

## Key Files

- `parser.go` (1,345) — the hand-written recursive-descent parser:
  `bodyParser`, `Parse`, `parseTopBlock`, `parseStmtList`, `parseStmt`,
  `parseNestedBlock`, `parseFor`, `parseWhile`, `parseLoop`, `parseIf`,
  `parseReturn`, `parseRaise`, `parseSQLStmt`, `parseExecute`,
  `parseDeclSection`, `parseDeclaration`, `scanExprToKeyword`, `scanExprTo`.
- `ast.go` (323) — the AST node types: `Block`, `Stmt`, `StmtList`,
  `Declaration`, `Assign`, `If`, `Case`, `Loop`, `While`, `For`, `ForQuery`,
  `Exit`, `Continue`, `Return`, `ReturnNext`, `ReturnQuery`, `Raise`,
  `Perform`, `Execute`, `SQLStmt`, `Open`, `Fetch`, `Close`, `GetDiag`,
  `NullStmt`, `ExceptionBlock`, `ExceptionHandler`.

## Public API

```go
func Parse(input string) (ast.Block, error)   // parse a PL/pgSQL body
func parseExprFromTokens(tokens []Token, ...) (expr, error)  // inline expression
```

## Internal structure

- **Lexer** — the parser has its own token scan over the input string,
  recognizing PL/pgSQL keywords (`DECLARE`, `BEGIN`, `EXCEPTION`, `RETURN`,
  `RAISE`, `PERFORM`, `EXECUTE`, `FOR`, `WHILE`, `LOOP`, `IF`, `CASE`, …)
  and delegating embedded SQL fragments to the main SQL parser
  (`parseSQLStmt` → `parser.Parse`).
- **Parser** — a hand-written recursive-descent parser (not yacc). The entry
  is `Parse`, which returns a `Block` (declarations + statements + exception
  block). Statements are parsed by `parseStmt`, which examines the first token
  and dispatches to the specific `parse*` function.
- **SQL stmts** — `parseSQLStmt` captures a SQL statement between the current
  position and the next PL/pgSQL keyword token (`LOOP`, `END`, `INTO`, …),
  then sends it to the main SQL parser (`parser.Parse`) for semantic analysis.
- **Expression scanning** — `scanExprToKeyword`/`scanExprTo` scans tokens until
  a keyword delimiter, used for inline expressions in assignments, `IF`
  conditions, `RETURN` values, etc.

## Dependencies

- **Used by** — `internal/executor` (plpgsql_runtime.go interprets the AST),
  `internal/parser` (function-body parsing).
- **Uses** — `internal/parser` (SQL parsing for `parseSQLStmt`), `internal/nodes`
  (expression types).

## Notable patterns / gotchas

- **Keyword scanning trap** — `parseFor`'s `FOR rec IN <query> LOOP` scan stops
  at the first depth-0 `KwLoop` token, but `loop` is a registered keyword
  (`KwLoop`), so a `loop` identifier used as a column alias inside the SELECT
  truncates the query (M0134-0110).
- **SQL vs PL/pgSQL boundary** — the PL/pgSQL parser must find the boundary
  between PL control flow (`LOOP`, `END`, `IF`, …) and embedded SQL (`SELECT`,
  `INSERT`, …). `parseSQLStmt` captures the SQL text and delegates to the main
  parser; the captured SQL may include `loop`/`begin`/`end` as identifiers
  (not keywords), which is why the keyword scan must be depth-aware.
- **Exception blocks** — `parseExceptionBlock` handles `EXCEPTION WHEN …
  THEN …` with a list of exception handlers, each matching a condition name
  (`SQLSTATE`, `SQLEXCEPTION`, `OTHERS`).
- **`SCROLL` / `NO SCROLL` / `CURSOR`** — `parseDeclaration` handles cursor
  declarations (`CURSOR [SCROLL|NO SCROLL] FOR <query>`); `parseOpen`/`parseFetch`
  /`parseClose` handle cursor operations.