# 0020-0001 — Window Function Parser Surface and AST

**Status:** accepted (step 1 — parser + AST only; analyzer /
planner / executor deferred)
**Milestone:** [0020 — Window Function Support](../milestones/0020-window-functions-over-row-number-rank-lag-lead.md)
**Spans seam:** SQL parser, FuncCall AST, analyzer reject.
**Cross-links:**
[root-0010](root-0010-parser.md) (parser scaffolding),
[0016-0001](0016-0001-with-parser-ast-and-name-resolution.md),
[0017-0001](0017-0001-on-conflict-parser-ast-and-analysis.md),
[0018-0001](0018-0001-explain-parser-options-and-ast.md),
[0021-0001](0021-0001-for-update-parser-analysis-and-ast.md)
(parser-only step-1 precedents).

## Context

goopg's parser handles aggregates, scalar functions, joins, and
subqueries but doesn't yet recognise the `OVER (…)` tail that
promotes a function call to a window function. Reporting
queries that need ROW_NUMBER / RANK / LAG / LEAD over partition
+ order tuples — common in BI tools, ORMs, and migration
workloads — fail at the lex/parse layer with a generic syntax
error.

This slice introduces the **parser surface and AST nodes**
without yet wiring the analyzer / planner / executor — mirrors
the M0016/M0017/M0018/M0021 step-1 precedents. Establishing
the AST shape in one well-tested commit lets later stages
(analyzer name resolution, planner WindowAgg node, executor
streaming-sort + per-partition state) land incrementally.

## Grammar

```
func_call    ::= func_name '(' [args] ')' [over_clause]
over_clause  ::= OVER '(' [partition_clause] [order_clause] ')'
partition_clause ::= PARTITION BY expr_list
order_clause     ::= ORDER BY sort_list
```

Stage A scope:

- Bare `OVER ()`, `OVER (PARTITION BY …)`, `OVER (ORDER BY …)`,
  `OVER (PARTITION BY … ORDER BY …)` — the four shapes
  ROW_NUMBER and RANK need.
- Frame clauses (`ROWS BETWEEN …`, `RANGE …`, `GROUPS …`,
  frame-exclusion) parse but are explicitly rejected.
  Stage B promotes them.
- Named-window references (`OVER win_name`) and named-window
  definitions (`WINDOW win AS (...)`) are out of step-1 scope.

## AST shape

```go
type FuncCall struct {
    pos      int
    Name     ObjectName
    Args     []Expr
    Star     bool
    Distinct bool
    Over     *WindowDef       // ← new in M0020-0001
}

type WindowDef struct {
    pos         int
    PartitionBy []Expr
    OrderBy     []SortBy
}
```

`FuncCall.Over` is nil for every pre-M0020 call. PartitionBy
and OrderBy reuse the existing `[]Expr` / `[]SortBy` shapes so
the executor's ordering logic doesn't have to learn new
sort-key plumbing for window functions.

## New keywords

```go
KwOver      Keyword = "over"
KwPartition Keyword = "partition"
```

`KwOrder` and `KwBy` already exist; `KwRows` / `KwRange` /
`KwGroups` stay deferred.

## Parser wiring

`parseFuncCallTail` ends with `)` then calls a new
`maybeWindowTail(fc)` helper:

```go
func (p *parser) maybeWindowTail(fc *FuncCall) (Expr, error) {
    if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOver) {
        return fc, nil
    }
    wd, err := p.parseWindowDef()
    if err != nil { return nil, err }
    fc.Over = wd
    return fc, nil
}
```

Returning `fc` unchanged when no `OVER` follows preserves the
byte-for-byte invariant for every existing caller.

`parseWindowDef` consumes `OVER ( [PARTITION BY exprs]
[ORDER BY sortlist] )` and errors on any token that isn't `)`
after the optional ORDER BY — that's how frame clauses surface
their explicit "not supported in v0" message instead of a
generic syntax error.

## Analyzer gate

`analyzeExpr`'s `*parser.FuncCall` arm rejects when `x.Over !=
nil` with SQLSTATE `0A000` "window functions are not supported
in v0 analyzer". Mirrors the two-step gate from M0017-0002 →
M0017-0003 / M0018-0002 → M0018-0003: parser accepts the
surface so diagnostics surface specific feature names; the
analyzer refuses to silently degrade to non-windowed evaluation.

## Tests

`internal/parser/window_test.go`:

- `TestParseWindowFuncBareOver` — `f() OVER ()` produces
  non-nil Over with empty PartitionBy / OrderBy.
- `TestParseWindowFuncPartitionBy` — `OVER (PARTITION BY x)`.
- `TestParseWindowFuncOrderBy` — `OVER (ORDER BY x DESC)`;
  pins the SortBy.Desc round-trip.
- `TestParseWindowFuncPartitionAndOrder` — both clauses
  combined.
- `TestParseWindowFuncCountStarOver` — `count(*) OVER ()`
  flows through the Star=true branch into the window tail.
- `TestParseWindowFuncRejectsFrameClause` — `ROWS BETWEEN …`
  and `RANGE UNBOUNDED PRECEDING` both surface parse errors.
- `TestParseWindowFuncWithoutOverUnchanged` — rollout
  guardrail for non-windowed calls.

`internal/analyzer/analyzer_test.go`:

- `TestAnalyzeWindowFunctionRejected` — `SELECT row_number()
  OVER ()` surfaces SQLSTATE `0A000`.

Full `go test ./...` green.

## Out of scope

- Analyzer: name resolution + type checking for ROW_NUMBER /
  RANK / LAG / LEAD — M0020-0002.
- Planner: WindowAgg plan node + partition / order key
  propagation — M0020-0003.
- Executor: per-partition streaming + sort + window-function
  evaluation — M0020-0004.
- Frame clauses (ROWS / RANGE / GROUPS / frame exclusion).
- Named-window references (`OVER win_name`) and named-window
  definitions (`WINDOW win AS (...)`).
- LAG/LEAD-specific argument shapes (offset, default).
