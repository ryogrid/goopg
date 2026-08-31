# 0015-0001 — CREATE FUNCTION Parser Surface and AST

**Status:** accepted (step 1 — parser + AST only; analyzer /
catalog / runtime deferred)
**Milestone:** [0015 — PL/pgSQL Stored Routines (Function-First Delivery)](../../milestones/0015-plpgsql-stored-routines-function-first.md)
**Spans seam:** lexer dollar-quote support, parser CREATE/DROP
FUNCTION grammar, FuncCall AST, analyzer reject.
**Cross-links:**
[root-0010](../../root/root-0010-parser.md) (parser scaffolding),
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md),
[0020-0001](0020-0001-window-parser-and-ast.md)
(parser-only step-1 precedents).

## Context

goopg's parser handles SELECT/INSERT/UPDATE/DELETE plus DDL for
TABLE / INDEX / VIEW / PUBLICATION / SUBSCRIPTION, but it has no
syntactic surface for routine creation. PL/pgSQL function bodies
also need a lexer-level concept the existing tokenizer doesn't
have: dollar-quoted string literals (`$$body$$` / `$tag$body$tag$`),
the upstream-standard way to embed routine source without
escape-hell.

This slice introduces the **parser/lexer surface and AST nodes** for
`CREATE [OR REPLACE] FUNCTION` and `DROP FUNCTION` (Stage A scope).
No analyzer, catalog, or runtime work — just establishing the AST
shape and the dollar-quote lexer support so subsequent loops can
incrementally land:

1. Catalog: `pg_proc` registration and lookup.
2. PL/pgSQL parser + AST for routine bodies.
3. PL/pgSQL interpreter and SPI-style execution bridge.
4. Function invocation in expression contexts.

This step-1 split mirrors the M0016/M0017/M0018/M0020/M0021
precedent: parser surface lands first, body stored as raw source
string, analyzer refuses to silently degrade.

## Grammar

```
CreateFunctionStmt ::= CREATE [OR REPLACE] FUNCTION name '(' [arg_list] ')'
                       RETURNS rettype
                       (LANGUAGE lang | AS dollar_string)+
arg ::= [arg_name] [IN] type [DEFAULT expr]
DropFunctionStmt ::= DROP FUNCTION [IF EXISTS] name ['(' [arg_list] ')']
                     [CASCADE | RESTRICT]
```

Stage A scope:

- `IN` argument mode (the default) is the only mode accepted.
  `OUT` / `INOUT` / `VARIADIC` surface a specific Stage-A-only
  diagnostic at the parser level rather than a generic syntax
  error — they're explicitly Stage B (procedures + bidirectional
  binding work share that infrastructure).
- `LANGUAGE` and `AS` may appear in either order (matches
  upstream's flexible clause ordering).
- The function body **must** be a dollar-quoted string. Plain
  single-quoted bodies are upstream-legal but fragile; rejecting
  them keeps the parser surface narrow.
- `DROP FUNCTION` accepts a single name; multi-target drop
  (comma-separated names) is upstream syntax but rare and out of
  Stage A scope.

## Lexer: dollar-quoted strings

The pre-existing `$` case in `lexer.go` only handled positional
parameters (`$1`, `$2`). Extension: when `$` is followed by `$`
or an identifier-start char, attempt to lex a dollar-quoted
literal:

1. Scan tag chars after the leading `$`. Tag chars are
   identifier-chars **except** `$` itself (per upstream PG manual:
   "the tag ... cannot contain a dollar sign"). Empty tag is
   legal — the loop simply consumes zero bytes.
2. If the next byte is `$`, the opener is closed: the closing
   delimiter is the same `$<tag>$` sequence.
3. Scan body bytes until the closer matches; emit
   `TokenStringLit` with the body as the value.
4. If we don't see the closing `$` after the optional tag chars,
   rewind and fall back to positional-parameter parsing — this
   keeps `$1` / `$N` behaviour byte-identical for non-routine
   callers.

A dedicated `isDollarTagCont` predicate is added rather than
reusing `isIdentCont` (which includes `$` because identifiers like
`revenue$0` are legal) — that exclusion is what lets the lexer
detect the end of the opening tag.

## AST shape

```go
type CreateFunctionStmt struct {
    pos        int
    OrReplace  bool
    Name       ObjectName
    Args       []FunctionArg
    ReturnType ColumnType
    Language   string  // lower-cased, e.g. "plpgsql"
    Body       string  // raw source between the dollar-quote delimiters
}

type DropFunctionStmt struct {
    pos      int
    IfExists bool
    Name     ObjectName
    Args     []FunctionArg  // nil when no parenthesised arg list was given
    Behavior DropBehavior
}

type FuncArgMode int
const (
    FuncArgIn FuncArgMode = iota  // Stage A pins all args to IN
)

type FunctionArg struct {
    pos     int
    Name    string  // empty for positional-only args
    Mode    FuncArgMode
    Type    ColumnType
    Default Expr  // nil when no DEFAULT was given
}
```

`Args=nil` (no parenthesised list) is distinct from
`Args=[]FunctionArg{}` (explicit empty list). For
`CreateFunctionStmt` the parens are mandatory so this never
matters; for `DropFunctionStmt` it does — the future overload
resolver needs to distinguish "drop by name only" from "drop the
zero-arg overload".

## New keywords

```go
KwFunction Keyword = "function"
KwReturns  Keyword = "returns"
KwLanguage Keyword = "language"
```

`KwIn` / `KwDefault` / `KwAs` / `KwCascade` / `KwRestrict` /
`KwIf` / `KwExists` already exist. Procedure-side keywords
(`KwProcedure`, `KwCall`, `KwOut`, `KwInout`, `KwVariadic`,
`KwDeclare`, `KwBegin`*, `KwEnd`*) stay deferred to Stage B.

(* `KwBegin` / `KwEnd` already exist for transaction control;
PL/pgSQL block parsing reuses them.)

## Parser wiring

`parseCreate` / `parseDrop` (in `ddl.go`) gain a `KwFunction`
dispatch arm that delegates to new helpers in `function.go`:

- `parseCreateFunctionTail(pos, orReplace) → *CreateFunctionStmt`
- `parseDropFunctionTail(pos) → *DropFunctionStmt`
- `parseFunctionArgList()` — handles the optional `(arg, ...)`
  list, returning nil for "no parens" vs `[]` for "explicit empty"
- `parseFunctionArg()` — handles `[name] [IN] type [DEFAULT expr]`
  with explicit Stage-B-mode rejection
- `parseLanguageName()` — accepts both `LANGUAGE plpgsql` (ident)
  and `LANGUAGE 'plpgsql'` (string literal)
- `parseFunctionBody()` — requires a dollar-quoted string, rejects
  single-quoted bodies

`UNLOGGED` on `CREATE FUNCTION` surfaces a clean syntax error
(mirrors VIEW/INDEX guards already in place).

## Analyzer gate

`Analyze` (in `internal/analyzer/analyzer.go`) gains
`*parser.CreateFunctionStmt` and `*parser.DropFunctionStmt` cases
that return SQLSTATE `0A000` "CREATE FUNCTION is not supported in
v0 analyzer" / "DROP FUNCTION is not supported in v0 analyzer".
Mirrors the two-step gate from
M0017-0002→0003 / M0018-0002→0003 / M0020-0001→0002: parser
accepts the surface so diagnostics surface specific feature
names; the analyzer refuses to silently degrade.

## Tests

`internal/parser/function_test.go`:

- `TestParseCreateFunctionMinimal` — pins the smallest accepted
  shape (CREATE FUNCTION name() RETURNS rettype LANGUAGE plpgsql
  AS $$body$$).
- `TestParseCreateFunctionOrReplace` — OR REPLACE flag.
- `TestParseCreateFunctionWithArgs` — named args + positional-only
  arg + Mode=FuncArgIn invariant.
- `TestParseCreateFunctionExplicitIN` — handwritten functions
  using explicit `IN` parse cleanly.
- `TestParseCreateFunctionRejectsOutInoutVariadic` — Stage-A-only
  diagnostic for OUT / INOUT / VARIADIC (3 sub-cases).
- `TestParseCreateFunctionLanguageAsAnyOrder` — LANGUAGE / AS in
  either order.
- `TestParseCreateFunctionTaggedDollarQuote` — `$body$ ... $$inner$$
  ... $body$` (tag prevents the inner `$$` from closing).
- `TestParseCreateFunctionMissingBody` — missing AS surfaces a
  specific diagnostic.
- `TestParseDropFunctionMinimal` — bare `DROP FUNCTION f`.
- `TestParseDropFunctionIfExistsAndArgs` — IF EXISTS + arg list
  + CASCADE.
- `TestDollarQuoteEmptyTag` — `$$` empty body round-trips.
- `TestDollarQuoteUnterminated` — clear diagnostic on missing
  closer.
- `TestPositionalParameterStillParses` — guards against the
  dollar-quote support breaking `$1` / `$N`.

`internal/analyzer/analyzer_test.go`:

- `TestAnalyzeCreateFunctionRejected` — `CREATE FUNCTION ...`
  surfaces SQLSTATE `0A000`.
- `TestAnalyzeDropFunctionRejected` — symmetric guard.

Full `go test ./...` green.

## Out of scope

- Catalog wiring (`pg_proc` row insert, lookup) — Stage A step 2.
- PL/pgSQL parser + AST for routine bodies — Stage A step 3.
- PL/pgSQL interpreter and SPI-style execution bridge — Stage A
  step 4.
- Function invocation from expression contexts (the FuncCall
  resolver path) — Stage A step 5.
- `CREATE PROCEDURE` / `CALL` / `OUT` / `INOUT` / `VARIADIC` —
  Stage B (M0015 procedure follow-up).
- `LANGUAGE sql` body execution (SQL-language functions) — Stage A
  step 4 piggybacks SQL routines on the same execution bridge.
- Plain single-quoted function bodies (upstream-legal but rare).
- Multi-target `DROP FUNCTION` (comma-separated names).
