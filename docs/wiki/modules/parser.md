# Module: `internal/parser`

The SQL parser and lexical analyzer — a goyacc-generated LALR(1) parser with a
hand-written lexer, AST, and analyzer. The grammar lives in `grammar/*.y` and is
compiled into `yacc_parser.go` by `make gen-parser`. It is a port of
PostgreSQL's `gram.y`, not a copy: it carries synthetic terminals (`TYPEDLIT`,
`CHECKBODY`, `*_LA`), its own position primitive (`$<p>N`), and ~1.7% of
statement classes deliberately left on hand-written token scanners.

The parser is dual-engine: a **yacc grammar** for the bulk of SQL syntax and a
set of **hand-written recursive-descent parsers** for DML expressions, DDL
statements, and PL/pgSQL bodies. The analyzer (`internal/parser/analyzer/`)
resolves names, types, and coerce expressions after parsing.

## Key Files

- `yacc_parser.go` (23,210) — the generated LALR(1) parser (goyacc output).
- `yacc_ctors.go` (1,053) — AST constructor functions invoked by parser actions.
- `ddl.go` (11,411) — hand-written recursive-descent parser for DDL statements
  (CREATE TABLE, ALTER, DROP, GRANT, etc.).
- `select.go` (5,304) — SELECT/INSERT/UPDATE/DELETE statement tree builder.
- `support.go` (4,170) — parser utilities, keyword matching, position tracking,
  dotted-name splitting, privilege/object-name parsing.
- `ast.go` (4,064) — AST node types (Stmt, Select, Insert, CreateTable, etc.).
- `lexer.go` (1,236) — tokenizer: keyword recognition, string/identifier scanning,
  operator lexing, positional tracking.
- `function.go` (1,089) — function-call expression parsing (ordinary, aggregate,
  window, ordered-set, VARIADIC).
- `dispatch.go` (967) — top-level `parseStatement` routing: classifies the first
  token and dispatches to the yacc grammar or hand-written DDL/PL/pgSQL parsers.
- `expr.go` (750) — expression parsing for the hand-written path (operators,
  casts, subqueries, lists, COLLATE, subscript).
- `adapter.go` (644) — adapter layer bridging the yacc parser's output to the
  hand-written code path (token mapping, error position).
- `dml.go` (602) — INSERT/UPDATE/DELETE parsing for the hand-written path.
- `interval.go` (1,284) — interval-literal grammar (SQL-standard vs PG-abbreviated).
- `token.go` (431) — token type definitions, operator precedence table.
- `parser_pool.go` — sync.Pool for token slices, reducing allocation in the lexer.
- `analyzer/analyzer.go` — post-parse name resolution, type inference, coerce insertion.
- `analyzer/coerce.go` — type coercion rules (implicit casts, binary coercion).
- `sqlkeywords/keywords.go` — SQL keyword registry (unreserved, reserved, type_func_name).

## Public API

```go
// Entry points
func Parse(input string, mc ...*mmgr.Context) ([]Stmt, error)     // parse SQL
func ParseExpr(input string, mc ...*mmgr.Context) (Expr, error)   // parse expression
```

The parser returns `[]Stmt` (a batch of statements from the input string).
Each `Stmt` is an AST node type (e.g., `*Select`, `*Insert`, `*CreateTable`,
`*AlterTable`). The analyzer resolves these into `*optimizer.Node` trees:

```go
// Analyzer (internal/parser/analyzer/)
func Analyze(ctx *Context, stmts []Stmt) ([]optimizer.Node, error)
func runCoerce(expr Expr, targetType string) (Expr, error)
```

## Internal structure

The parser runs in two phases:

1. **Lexing** — `lexer.go` reads the input string, recognizes keywords (via
   `keywords_gen.go`), scans identifiers, numbers, strings, operators, and
   produces a `Token` stream. The tokenizer uses a pre-allocated pool
   (`tokenSlicePool`) to reduce GC pressure.

2. **Parsing** — `dispatch.go` probes the first token and routes to either:
   - The **yacc grammar** (`yacc_parser.go`), which handles the full SQL
     language (SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, DROP, etc.)
     via the LALR(1) state machine.
   - A **hand-written sub-parser** for DDL (`ddl.go`), which recursively
     descends into specific statement types (CREATE TABLE, ALTER TABLE,
     GRANT, REVOKE, etc.) that the yacc grammar routes to an extendable
     "raw" parse path.
   - The analyzer (`analyzer/analyzer.go`) then transforms the AST into
     the optimizer's `Node` IR, resolving schema references, type inference,
     and implicit coercion.

## Dependencies

- **Used by** — `internal/optimizer` (planner consumes AST), `internal/executor`
  (DDL operators call `Parse` for SQL bodies), `internal/nodes` (serialization),
  `internal/postmaster` (SQL dispatch), `internal/pl/plpgsql` (SQL parsing in
  PL/pgSQL bodies), `internal/initdb` (bootstrap seeds).
- **Uses** — `internal/utils/mmgr` (memory-context allocation), `internal/nodes`
  (AST node types), `internal/parser/analyzer` (post-parse analysis).

## Notable patterns / gotchas

- **Dual-engine parser** — the yacc grammar and hand-written parsers are sibling
  paths; a statement class must be handled by exactly one (yacc → `yacc_parser.go`,
  hand-written → `ddl.go` / `select.go` / `dml.go`). The dispatch at
  `dispatch.go:parseStatement` decides which path to take.
- **Synthetic terminals** — `TYPEDLIT`, `CHECKBODY`, `*_LA` (e.g., `SELECT_LA`,
  `FOR_LA`) are goopg-specific additions to the grammar. They have no upstream
  counterpart in `gram.y` and exist to resolve LALR conflicts.
- **Position tracking** — `$<p>N` is goopg's stand-in for yacc's `@n`. The
  `lastConsumedPos()` helper silently returns the wrong token in specific reduce
  states — the `parser_pool.go` fixture and `ErrorResponse.Position` tests
  exercise this.
- **`make gen-parser`** — always build with `make gen-parser`, never `go build`
  alone, which compiles a stale generated parser. The oracle is
  `internal/parser/testdata/parity_goldens.txt`; regenerate with
  `GOOPG_UPDATE_GOLDENS=1 go test ./internal/parser/`.
- **analyzer.go** — the analyzer is a separate pass that walks the parser AST
  and produces the optimizer IR. It resolves column references, infers types,
  inserts implicit casts, and checks semantic constraints (e.g., `ON CONFLICT`
  arbiter index match).
- **Conflict pin** — `grammar/goopg_ext.y` carries a conflict pin document
  (currently 60 shift/reduce, 0 reduce/reduce). Changes that add new conflicts
  must be justified and the pin updated.