# 0018-0001 — EXPLAIN Parser Options and AST

**Status:** accepted (parser/AST step)
**Milestone:** [0018 — EXPLAIN / EXPLAIN ANALYZE Support](../../milestones/0018-explain-and-explain-analyze.md)
**Spans seam:** parser AST extension, option-list grammar, byte-position
diagnostics for unsupported / malformed option syntax.
**Cross-links:**
[root-0010](../../root/root-0010-parser.md) (parser baseline),
[0003-0007](0003-0007-explain.md) (existing EXPLAIN renderer this slice's
options will eventually drive),
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md) (recent
parser-step-1 pattern this slice mirrors).

## Context

goopg's parser today accepts only the bare `EXPLAIN <stmt>` form.
PostgreSQL also supports a keyword-style `EXPLAIN ANALYZE [VERBOSE]
<stmt>` and a richer parenthesised form `EXPLAIN (option [VALUE]
[, ...]) <stmt>` covering ANALYZE, VERBOSE, COSTS, BUFFERS,
SETTINGS, TIMING, SUMMARY, FORMAT TEXT|JSON, etc.

This step 1 lands **just the parser AST + grammar**. The option
values flow through to the existing executor's `explainOp` — the
renderer remains TEXT-format, ANALYZE-disabled — for now; runtime
instrumentation (Stage B) and JSON / VERBOSE rendering land in
subsequent slices (0018-0002 / 0018-0003 / 0018-0004).

## AST

```go
// ExplainOptions carries the parsed EXPLAIN options. All flags
// default false (matching upstream's defaults for the keyword
// form). Format defaults to ExplainFormatText.
type ExplainOptions struct {
    Analyze  bool
    Verbose  bool
    Costs    bool
    Buffers  bool
    Settings bool
    Timing   bool
    Summary  bool
    Format   ExplainFormat
}

type ExplainFormat int
const (
    ExplainFormatText ExplainFormat = iota
    ExplainFormatJSON
)
```

`ExplainStmt` grows an `Options ExplainOptions` field. `nil`
(zero-value) options preserves byte-for-byte AST shape for the
bare-EXPLAIN form so existing tests pass through unchanged.

## Grammar

Two surface forms coexist:

### Keyword form

```
EXPLAIN [ANALYZE] [VERBOSE] <stmt>
```

`ANALYZE` and `VERBOSE` may appear in either order — upstream's
`opt_analyze`/`opt_verbose` allow `EXPLAIN VERBOSE ANALYZE` too.
Other options (BUFFERS, FORMAT, …) are ONLY available via the
parenthesised form, matching upstream.

### Parenthesised form

```
EXPLAIN ( option_list ) <stmt>
option_list := option ("," option)*
option := name [value]
value := identifier | string | TRUE | FALSE | integer
```

The option name set goopg accepts is the union of:

| Name      | Value                            | AST flag       |
|-----------|----------------------------------|----------------|
| ANALYZE   | bool (default true if omitted)    | Analyze         |
| VERBOSE   | bool                              | Verbose         |
| COSTS     | bool (default true)               | Costs           |
| BUFFERS   | bool                              | Buffers         |
| SETTINGS  | bool                              | Settings        |
| TIMING    | bool                              | Timing          |
| SUMMARY   | bool                              | Summary         |
| FORMAT    | TEXT \| JSON                      | Format          |

Boolean values accept `ON`/`OFF`, `TRUE`/`FALSE`, `1`/`0`, mirroring
upstream's `defGetBoolean`. Omitting the value defaults to true
(matches upstream — `EXPLAIN (ANALYZE) ...` enables ANALYZE).

Unknown option names error with SQLSTATE 22023
("invalid_parameter_value") at the option's position. Unsupported
FORMAT values (e.g. `XML`, `YAML` — upstream supports them, goopg
doesn't yet) error 0A000 with the unsupported-format message.

## Errors

- Unknown option name → 22023 at option position.
- Empty parenthesised list (`EXPLAIN () SELECT 1`) → 42601 at the
  closing paren's position.
- Conflicting forms (`EXPLAIN ANALYZE (ANALYZE off) SELECT 1`) —
  the parser accepts both syntaxes and the parenthesised value
  wins (matches upstream); no error.
- Invalid bool value for a bool option → 22023.
- Trailing comma → 42601.

## Out of scope (this step)

- Static plan rendering improvements (VERBOSE, FORMAT JSON output) —
  0018-0002.
- ANALYZE runtime instrumentation — 0018-0003.
- JSON snapshot regression strategy — 0018-0004.
- Catalog-level statistics tags in EXPLAIN output (filter
  predicates, key ordering) — 0018-0002.

## Tests

- `TestParseExplainBareUnchanged` — `EXPLAIN SELECT 1` produces an
  ExplainStmt whose Options is the zero value (Analyze=false,
  Format=Text). Regression guard for byte-for-byte invariance of
  pre-M0018 callers.
- `TestParseExplainAnalyzeKeyword` — `EXPLAIN ANALYZE SELECT 1`
  sets Options.Analyze=true.
- `TestParseExplainVerboseKeyword` — `EXPLAIN VERBOSE SELECT 1`
  sets Verbose=true.
- `TestParseExplainAnalyzeVerbose` — both keywords in either
  order set both flags.
- `TestParseExplainParenAnalyzeBare` — `EXPLAIN (ANALYZE) SELECT 1`
  defaults Analyze=true.
- `TestParseExplainParenAllOptions` — every option flag flips
  correctly via the parenthesised form.
- `TestParseExplainFormatJSON` — `EXPLAIN (FORMAT JSON) SELECT 1`
  sets Format=ExplainFormatJSON.
- `TestParseExplainRejectsUnknownOption` — `EXPLAIN (UNKNOWN_OPT)
  SELECT 1` errors at the option's position.
- `TestParseExplainRejectsEmptyOptionList` — `EXPLAIN () SELECT 1`
  errors.
- `TestParseExplainBoolValueForms` — ON/OFF/TRUE/FALSE/1/0 all
  parse correctly to the right bool.
