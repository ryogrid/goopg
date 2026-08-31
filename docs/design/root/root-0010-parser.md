# 0010 — SQL Parser and AST (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 6 opens with a parser/analyzer covering the subset of SQL
that pgbench's `pgbench -i` and the default+`--select-only` scripts
exercise, plus the transaction-control verbs and `VACUUM`/`ANALYZE`
that the existing storage stack already supports. Today the simple
query path lives in `internal/server/query.go` and special-cases
`SELECT 1`, `SHOW`, `SET`, `RESET`, and `BEGIN/COMMIT/ROLLBACK` via
hand-written prefix matching. That worked for the wire-protocol and
GUC milestones; it cannot scale to real DML.

Upstream PostgreSQL's parser is a Bison grammar (`gram.y`) feeding a
flex lexer (`scan.l`), producing a tree of `Node` structs that the
analyzer (`parse_*.c`) decorates with semantic information. We do not
re-vendor that — Bison output is huge, the licence dance is annoying,
and the upstream grammar accepts dozens of dialects we don't need.
v0 ships a hand-written recursive-descent parser sized to the
pgbench surface area.

References into upstream:

- `postgres/src/backend/parser/scan.l` — token taxonomy and
  keyword categorisation (RESERVED / TYPE_FUNC_NAME / COL_NAME /
  UNRESERVED).
- `postgres/src/backend/parser/gram.y` — production shape; we mirror
  the *ordering* of statement productions and operator precedence
  but not the LR rules.
- `postgres/src/include/nodes/parsenodes.h` — `RawStmt`, `SelectStmt`,
  `InsertStmt`, … structure layouts. v0 keeps the same names so a
  future port to `nodeRead`/`nodeOut` formats stays mechanical.

## Decision

### Layering

```
internal/parser/
    token.go        // Token, TokenKind, keyword table
    lexer.go        // Lexer: input string -> []Token
    ast.go          // AST node types (Node interface, statement nodes)
    parser.go       // Parser: []Token -> []Stmt (one Parse per statement
                    //   in a multi-statement query)
    parser_test.go
    lexer_test.go
```

The parser is independent of the executor and the protocol. Its only
upstream dependency is the SQLSTATE generator (for error category
codes; not yet wired). Downstream, the planner (0011) consumes
`parser.Stmt` trees.

### Token model

`Token` carries:
- `Kind` — discriminated enum (`TokenIdent`, `TokenKeyword`,
  `TokenIntLit`, `TokenStringLit`, `TokenOperator`, `TokenSymbol`,
  `TokenEOF`, …).
- `Value` — the source bytes of the token, lower-cased for keywords
  and identifiers (PostgreSQL is case-insensitive for unquoted
  identifiers; quoted identifiers preserve case via a separate token
  kind `TokenQuotedIdent`).
- `Pos` — byte offset in the input, retained for error messages
  (`syntax error at or near "FOO"` mirrors upstream).

The keyword table is a `map[string]TokenKeyword` of every reserved
and unreserved keyword v0 understands. Lookup happens after the
lexer has carved out an identifier; if the lower-cased text matches,
the kind is upgraded from `TokenIdent` to the keyword's specific kind.

### Lexer rules

Recognises:

- Whitespace and SQL line comments (`--…`) and block comments
  (`/* … */`, nestable per upstream).
- Identifiers: `[A-Za-z_][A-Za-z_0-9$]*`; quoted: `"…""…"` allowed.
- Numeric literals: integer (`123`), with negative sign handled by
  the parser as a unary operator. Floats deferred until pgbench
  needs them.
- String literals: single-quoted with `''` doubling for escape.
  Postgres `E'…'` extended escapes deferred — pgbench doesn't use them.
- Punctuation: `, ; ( ) . *`.
- Operators: `=`, `<`, `>`, `<=`, `>=`, `<>`, `!=`, `+`, `-`, `*`, `/`,
  `%`, `||` (string concat). Multi-character operators are matched
  greedily.
- Parameter placeholders: `$N` for prepared-statement bind slots
  (`TokenParam`), used by Bind / extended query.

Errors abort the lex with a `LexError` carrying `(message, pos)`.

### AST

A small `Node` interface (`Pos() int`) makes every node identifiable
in error messages. Statement nodes implement `Stmt`, an empty marker
interface that the parser's top-level returns.

Phase 1 statements (this milestone, in priority order):

1. `BeginStmt`, `CommitStmt`, `RollbackStmt` — bare verbs, no
   `WORK`/`TRANSACTION` clause yet beyond optional ignored token.
2. `VacuumStmt{Targets []ObjectName, Analyze bool, Verbose bool}` —
   accepts `VACUUM`, `VACUUM ANALYZE`, `VACUUM tablename`, etc.
3. `AnalyzeStmt{Targets []ObjectName}`.
4. `ShowStmt{Name string}`, `SetStmt{Local bool, Name, Value string}`,
   `ResetStmt{Name string}` — the existing GUC commands carved out
   of `internal/server/query.go` so the parser owns them too.

Phase 2 (next loop, decomposed in fix_plan):

- `CreateTableStmt`, `CreateIndexStmt`, `DropTableStmt`,
- `InsertStmt`, `UpdateStmt`, `DeleteStmt`, `SelectStmt`,
- expression tree (`ColumnRef`, `IntegerConst`, `StringConst`,
  `BinaryOp`, `FuncCall`, `ParamRef`).

Each statement carries a `Stmt.StatementType()` discriminator (similar
to upstream's `nodeTag`) for cheap switch dispatch in the planner.

### Parsing strategy

Recursive descent. One method per non-terminal. Operator precedence
for expressions handled with the standard "Pratt" / climbing approach
(`parseExpr`, `parseExprPrec(min)`). Statements are dispatched on the
first keyword.

The parser tracks current/peek tokens (`p.cur`, `p.peek`) and exposes
`expect(kind)`, `expectKeyword(kw)`, `accept(kind) bool`. Errors are
`SyntaxError{Pos, Message}`; the analyzer (when wired) will translate
these to SQLSTATE `42601` (`syntax_error`).

### Handling multi-statement queries

`Parse(input string) ([]Stmt, error)` splits by semicolon at the
parser level — *not* the lexer, since semicolons inside string
literals must not split. v0 reads statements until EOF; an empty
statement (just `;`) is dropped. The simple-query protocol path
emits one `CommandComplete` per non-empty statement.

### Error format

Upstream renders `syntax error at or near "TOKEN"\nLINE 1: …^…`. v0
matches the message text closely enough that psql users recognise
it, including the position pointer when stderr is a tty. Position
data lives on every token; the AST holds the position of its first
token so analyzer errors can pinpoint columns.

### Testing strategy

The parser is the second-most-tested package in the codebase (after
`internal/storage`):

- Lexer table tests covering each token kind, escapes, comments,
  keyword vs identifier upgrade.
- Parser table tests: one expected AST per input string for every
  statement the parser claims to accept. Round-tripping AST-to-
  string is *not* a goal yet; planner consumption is the spec.
- Negative tests: at least one syntax-error case per statement,
  pinning the error message and position.

### What this doc does NOT cover

- Semantic analysis (name resolution, type checking, function
  resolution). That's a separate analyzer pass living in
  `internal/parser/analyzer.go` once the catalog exists.
- Planner / optimizer (0011).
- Extended query protocol (0013).
- COPY (0014).

## Alternatives Considered

- **Vendor PostgreSQL's `gram.y`**: bigger than the rest of the
  codebase combined, requires Bison at build time, drags in goyacc
  + a custom lexer wrapper, and accepts a vastly larger grammar
  than v0 needs. Upstream's grammar is a future destination, not a
  starting point.
- **Use an existing Go SQL parser** (`pg_query_go`, `vitess/sqlparser`,
  `cockroachdb/parser`). `pg_query_go` is upstream's grammar exposed
  via Cgo — heavy and at odds with our pure-Go single-binary goal.
  CockroachDB's parser is closest in dialect but dragging in their
  AST shapes their planner expectations; we'd be fighting it.
- **PEG / parser combinator**: appealing for grammar evolution but
  the v0 grammar is small and operator precedence is awkward to
  express; recursive descent is faster to write and read.

## Consequences

- The simple-query path moves from prefix matching to a real AST,
  unblocking the executor and extended-query work.
- Error messages improve to PostgreSQL-shaped `syntax error at
  or near "X"` form, which pgbench's diagnostic checks rely on
  ("connection refused", "syntax error", etc. — pgbench parses
  driver errors as plain text).
- Adding new statements is local to the parser package; no
  protocol or storage code needs to change.
