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
resolves names, types, and coerce expressions after parsing. Main package:
**61,192 LOC** across 22+ production files.

## Key Files (by LOC)

| File                   | LOC    | Role |
|------------------------|--------|------|
| `yacc_parser.go`       | 23,210 | The generated LALR(1) parser (goyacc output). |
| `ddl.go`               | 11,411 | Hand-written recursive-descent parser for DDL statements (CREATE TABLE, ALTER, DROP, GRANT, etc.). |
| `select.go`            | 5,304  | SELECT/INSERT/UPDATE/DELETE statement tree builder — the hand-written expression and query parser (`parseExprPrec` precedence ladder, window frames, set ops, GROUPING SETS). |
| `support.go`           | 4,170  | Parser utilities: keyword matching, position tracking, dotted-name splitting (`qname`), privilege/object-name parsing, `foldNegate` (unary-minus folding). |
| `ast.go`               | 4,064  | AST node types: 150+ statement structs (`Stmt` interface), `Expr` types, `ObjectName`, `RangeVar`, `JoinExpr`, `AlterTableAction`, and the enum classes (`JoinType`, `SetOpType`, `AlterTableActionKind`, …). |
| `parser.go`            | 2,629  | `Parse`/`ParseExpr` entry points, `SyntaxError` (42601 with Pos/Code/Hint), `tokenSlicePool`. |
| `interval.go`          | 1,284  | Interval-literal grammar (SQL-standard vs PG-abbreviated typmod packing). |
| `lexer.go`             | 1,236  | Tokenizer: keyword recognition, string/identifier scanning, operator lexing, positional tracking, unicode escapes, bit/hex strings. |
| `function.go`          | 1,089  | Function-call expression parsing (ordinary, aggregate, window, ordered-set, VARIADIC, named args). |
| `dispatch.go`          | 967    | Top-level `parseStatement` routing: classifies the first token and dispatches to the yacc grammar or hand-written DDL/PL/pgSQL parsers (`routeBatch`, `routeExpr`, `fragmentRouted`). |
| `expr.go`              | 750    | Expression parsing for the hand-written path (operators, casts, subqueries, lists, COLLATE, subscript). |
| `adapter.go`           | 644    | Adapter layer bridging the yacc parser's output to the hand-written code path (token mapping, error position). |
| `dml.go`               | 602    | INSERT/UPDATE/DELETE parsing for the hand-written path. |
| `copy.go`              | 383    | COPY statement parsing (direction, endpoint, options). |
| `op.go`                | 283    | Operator parsing and maximal-munch operator scanning. |
| `keywords.go` / `keywords_gen.go` | 223/539 | Keyword registry + generated keyword table. |
| `yacc_ctors.go`        | 1,053  | AST constructor functions invoked by yacc parser actions. |
| `token.go`             | 431    | Token type definitions (`TokenKind`), keyword constants, precedence table. |
| `tokennums_gen.go`     | 534    | Generated token-number table (token → terminal). |
| `with.go` / `tables.go`| 187/82 | WITH/CTE parsing; table-listing helpers. |
| `base_yylex.go`        | 74     | goyacc lexer glue (`yyLexer` adapter). |
| `parser_pool.go`       | 43     | `sync.Pool` for token slices, reducing allocation in the lexer. |
| `analyzer/analyzer.go` | —      | Post-parse name resolution, type inference, coerce insertion. |
| `analyzer/coerce.go`   | —      | Type coercion rules (implicit casts, binary coercion). |
| `sqlkeywords/keywords.go` | —    | SQL keyword registry (unreserved, reserved, type_func_name). |

## Public API

```go
// Entry points
func Parse(input string, mc ...*mmgr.Context) ([]Stmt, error)     // parse SQL
func ParseExpr(input string, mc ...*mmgr.Context) (Expr, error)   // parse a single expression
func SplitStatements(input string) ([]string, error)              // split on statement boundaries
```

The parser returns `[]Stmt` (a batch of statements from the input string).
Each `Stmt` is an AST node type (e.g., `*Select`, `*Insert`, `*CreateTable`,
`*AlterTable`). The analyzer resolves these into `*optimizer.Node` trees:

```go
// Analyzer (internal/parser/analyzer/)
func Analyze(stmt parser.Stmt, cat catalog.Catalog) error
func NumericCoercePrecedence(typeName string) int

// The analyzer is called from the planner after Parse:
//   stmts, _ := parser.Parse(sql)
//   for _, stmt := range stmts {
//       analyzer.Analyze(stmt, catalog)  // resolves names, types, coerces
//   }
```

Errors: `SyntaxError` carries `Pos` (byte offset), `Message` (mirrors upstream's
`syntax error at or near "TOKEN"` shape), `Raw` (suppress the wrapper — used for
semantic errors like `SELECT … INTO is not allowed here`), `Code` (non-42601
SQLSTATE override, e.g. opt_float's precision checks raise 22023), and `Hint`
(e.g. SIMILAR TO's 22025 escape-string error).

## Internal structure

The parser runs in two phases:

```mermaid
flowchart TD
    A[SQL text] --> B[lexer.go Lex]
    B --> C[Token stream via tokenSlicePool]
    C --> D{dispatch.go first-token routing}
    D -->|yacc grammar| E[yacc_parser.go LALR(1) state machine]
    D -->|hand-written DDL| F[ddl.go recursive descent]
    D -->|SELECT/DML| G[select.go / dml.go]
    D -->|PL/pgSQL body| H[pl/pgSQL parser]
    E --> I[AST via yacc_ctors.go]
    F --> I
    G --> I
    H --> I
    I --> J[analyzer.Analyze → name/type resolution]
    J --> K[optimizer.Node]
```

### 1. Lexing (`lexer.go`)

`Lex` reads the input string and produces a `Token` stream. `lexer` methods:
- `skipWhitespaceAndComments` (incl. `--` and `/* … */`, nested block comments).
- `next` — the main token scanner, switching on the first rune.
- String scanning: `scanPlainQuoteInto`, `tryQuoteContinuation` (newline
  continuation), `scanEscapeQuoteInto` (E'' escapes), `lexEscapeString`,
  `lexUnicodeEscapeQuote` + `decodeUnicodeEscapes` (U&'' escapes),
  `lexBitOrHexString` (B''/X'').
- Identifier/operator rune classifiers: `isIdentStart`, `isIdentCont`,
  `isOpRunChar`, `isDollarTagCont` ($tag$), `isDigit`, `isHexDigit`.
- Unicode surrogate pair handling (`isUTF16SurrogateFirst/Second`,
  `surrogatePairToCodepoint`) for U& escapes.

Token kinds (`token.go`): `TokenEOF`, `TokenIdent`, `TokenQuotedIdent`,
`TokenIntLit`, `TokenNumericLit` (decimal + scientific `1.5`, `1e10`, `0.5e-3`),
`TokenStringLit`, `TokenBitStringLit` (B'…'/X'…' with marker byte `'b'`/`'x'`),
`TokenParam` (`$N`), `TokenSymbol`, `TokenOperator`, `TokenKeyword`.

The lexer recycles `[]Token` backing arrays through `tokenSlicePool`
(pre-sized to 64 — typical pgbench queries produce 10-20 tokens).

### 2. Parsing (`dispatch.go`)

`dispatch.go` probes the first token and routes to either:

- The **yacc grammar** (`yacc_parser.go`), which handles the full SQL language
  via the LALR(1) state machine. AST constructors are factored into
  `yacc_ctors.go` (kept short so the .y stays diffable against upstream —
  porting-guide rule 3: anything >10 lines lives out-of-line in `support.go`).
- A **hand-written sub-parser** for DDL (`ddl.go`), which recursively descends
  into specific statement types (CREATE TABLE, ALTER TABLE, GRANT, REVOKE, …).
- Hand-written SELECT/DML (`select.go`/`dml.go`) — the expression engine.

`routeBatch`/`routeExpr` classify fragments; helper predicates decide whether a
fragment goes to the yacc path or a hand-written one:
`isWordTok`, `fragmentRouted`, `withFollowerRouted`, `isCTENameFollower`,
`explainInnerRouted`, `secondKeywordRouted`, `commentRouted`, `alterDomainRouted`,
`copyRouted`, `alterIndexRouted`, `alterViewRouted`, `alterMatviewRouted`,
`createRoutineRouted`, `alterTableActionsRouted`. `splitTopLevelCommas`
splits ALTER action lists respecting nesting.

### 3. Expression engine (`select.go`)

`parseExprPrec` is the precedence ladder (precedence constants in
`select.go`): `precOr`, `precAnd`, `precNot`, `precIs`, `precCompare`,
`precBitOr`, `precBitXor`, `precBitAnd`, `precBitShift`, `precConcat`,
`precJSON`, `precAddSub`, `precMulDiv`, `precPow`, `precUnary`. Notable
functions:

- Primaries: `parsePrimary`, `parseColumnOrCall`, `parseExpr`, `parseUnary`,
  `peekBinaryOp`, `peekQualifiedOp`, `parseAnyTail` (ANY/ALL), `parseInTail`,
  `parseBetweenTail`, `parseCaseExpr`, `parseExistsExpr`.
- Casts: `parseCastTail`, `parseTypeNameAfterCast`, `consumeTypeIdent`,
  `parseCastFuncExpr` (`CAST(x AS t)` vs `t(x)`).
- Functions: `parseFuncCallTail`, `maybeWindowTail` → `parseWindowDef` →
  `parseWindowSpecBody` → `parseFrameClause`/`parseFrameBound`/
  `parseFrameExclusion`, `parseGroupingCallExpr` (GROUPING()),
  `parseExtractExpr`, `parseSubstringFuncCall`, `buildSubstringSimilar`,
  `parseOverlayFuncCall`, `parsePositionFuncCall`, `parseTrimFuncCall`.
- SELECT: `parseSelect`, `parseTargetList`/`parseTargetEntry`,
  `parseFromList`/`parseFromItem`, `parseJoinClause`/`tryParseParenJoin`,
  `parseSetOpClause`, `parseGroupByElems`/`parseGroupingUnitList`/
  `parseGroupingSetsList` (ROLLUP/CUBE alternatives via
  `rollupAlternatives`/`cubeAlternatives`/`cartesianProductGroupingSets`),
  `parseSortList`, `parseSelectLimitClauses`, `parseLockingClause`.
- Typed literals: `tryTypedLiteral` (e.g. `DATE '…'`), interval qualifiers
  (`tryIntervalTypmodQualifier`, `tryConsumeIntervalPrecParen`),
  `synthesizeBareCharTypmod`.
- LIKE/SIMILAR: `wrapLikeEscape`, `parseOptionalEscape`, `buildSimilarTo`,
  `foldSubstringSimilar`.

### 4. DDL (`ddl.go`)

Hand-written recursive descent over CREATE TABLE (column defs, constraints,
partitioning, inheritance), ALTER TABLE action lists (`AlterTableActionKind`
enum), CREATE INDEX, sequences, extensions, collations, tablespaces, views,
matviews, types, domains, functions/procedures, triggers, rules, policies,
publications/subscriptions, event triggers, access methods, statistics,
default privileges (ACL), comments, and more. `splitTopLevelCommas` feeds
`alterActionRouted`-style action dispatch.

### 5. Interval grammar (`interval.go`)

SQL-standard (`INTERVAL '1 day'`) vs PG-abbreviated (`INTERVAL '1' DAY`)
syntax. `splitEmbeddedInterval`, `intervalRangeLowField`, `packIntervalCastTypmod`/
`packIntervalColumnTypmod`, `intervalRangeMask`, `DecodeIntervalCastTypmod` handle
the typmod packing (precision + field range).

### 6. Analyzer (`analyzer/`)

Post-parse pass resolving column references, inferring types, inserting
implicit coercion, and checking semantic constraints (e.g. `ON CONFLICT` arbiter
index match). `NumericCoercePrecedence` ranks numeric types for coercion.

## Dependencies

- **Used by** — `internal/optimizer` (planner consumes AST), `internal/executor`
  (DDL operators call `Parse` for SQL bodies), `internal/nodes` (serialization),
  `internal/postmaster` (SQL dispatch), `internal/pl/plpgsql` (SQL parsing in
  PL/pgSQL bodies), `internal/initdb` (bootstrap seeds).
- **Uses** — `internal/utils/mmgr` (memory-context allocation), `internal/nodes`
  (AST node types), `internal/parser/analyzer` (post-parse analysis),
  `internal/utils/adt/similarto` (SIMILAR TO folding).

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

- **`foldNegate` diverges from legacy** — unary minus on a numeric literal
  FOLDS into the constant (`SELECT -1` → `IntegerConst{-1}`), whereas the legacy
  hand-written parser built `UnaryOp{UnaryNeg, IntegerConst{1}}`. The divergence
  is deliberate and pinned by `TestKnownDiffUnaryMinusFold`; BETWEEN's signed
  bounds inherit the folding consistently.

- **`qname` 4-part degradation** — `columnRefFromParts`/`rangeVarFromName`
  interpret 1..3 parts; a 4-part name degrades to its last three parts instead
  of upstream's "improper qualified name" error (no differential fixture
  exercises it).

- **`tokenSlicePool` is heap-backed** — the earlier mctx token-arena fast path
  was retired as fundamentally GC-unsafe (doc 0107-0003d); steady-state lexing
  is allocation-free via the pool.

- **`TokenBitStringLit` carries a marker byte** — the value is `'b'`/`'x'` +
  raw unvalidated digits (mirroring scan.l's `addlitchar` convention); decoding
  happens later in `select.go`'s `decodeBitStringLit`.

- **Non-42601 SQLSTATEs from the grammar** — `SyntaxError.Code` lets a handful
  of parse-time errors report their real code (22023 for precision checks,
  22025 for SIMILAR TO escapes) instead of a generic syntax error, keeping
  `internal/parser` free of an sqlstate import.

- **`ParseExpr` rejects trailing tokens** — a caller passing `1 + 2; garbage`
  gets a clean syntax error so PL/pgSQL body parsing cannot silently ignore
  garbage.

- **British spelling alias** — `ANALYSE` is accepted as `ANALYZE` and `ABORT`/
  `END` alias `ROLLBACK`/`COMMIT`, matching upstream keyword tables.