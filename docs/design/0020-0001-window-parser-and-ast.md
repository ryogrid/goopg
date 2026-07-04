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

## Follow-up: named windows (2026-07-05, M0122-0004)

Implemented the two items this doc originally scoped out — `WINDOW
name AS (...)` clauses and the bare `OVER name` reference form —
without touching the planner or executor at all, by resolving the
reference entirely inside the analyzer before either sees the AST.

**AST:** `parser.SelectStmt` gained `WindowClause
[]NamedWindowDef` (`internal/parser/ast.go`); `NamedWindowDef{Name
string; Def *WindowDef}`. `WindowDef` (`internal/parser/expr.go`)
gained `RefName string` — set instead of `PartitionBy`/`OrderBy` for
the bare-name form, empty for the pre-existing anonymous
`OVER (...)` form (byte-for-byte unchanged for every non-named
query).

**Parser:** `parseWindowDef` (`internal/parser/select.go`) now
branches after consuming `OVER`: `(` → the existing anonymous body
(factored into a new shared `parseWindowSpecBody`, used by both the
anonymous and named forms so they can never drift apart — see
`pattern_sibling_paths_must_agree`); a bare identifier → `RefName`.
A new `WINDOW` clause is parsed via `acceptIdentKeyword("window")`
(mirrors the existing `WITHIN`/`FILTER` unreserved-keyword
precedent rather than adding a new reserved keyword token) between
`HAVING` and `ORDER BY`, matching upstream's grammar position.
`isAliasStart` gained a `"window"` exclusion alongside the
pre-existing `"fetch"` one — without it, `sum(x) OVER w WINDOW w AS
(...)` would swallow `window` as `sum(x)`'s implicit column alias
before the parser ever reached the WINDOW-clause branch.

**Analyzer:** a new `resolveNamedWindowRefs` (`internal/analyzer/
analyzer.go`) runs once per `SELECT`, immediately before
`analyzeTargets`. It builds a `name → *WindowDef` map from
`s.WindowClause` and walks every expression tree a window function
can legally appear in (Targets, GROUP BY, HAVING, ORDER BY — the
same set `exprHasWindowFunc` already checks), and for any
`FuncCall.Over.RefName != ""` copies the matching definition's
`PartitionBy`/`OrderBy` in place, raising `42P20` ("window %q does
not exist") for an unresolvable name. The traversal
(`resolveWindowRefsInExpr`) deliberately mirrors
`exprHasWindowFunc`'s node-type coverage — same sibling-consistency
rationale as the parser change.

Because the mutation happens on the *same* AST nodes the planner
and executor already consume, `internal/planner/planner.go`'s
`windowSpecKey`/window-grouping logic and
`internal/executor/operators_window.go`'s evaluator needed **zero**
changes — a named-window reference is indistinguishable from an
equivalent inline anonymous one by the time either stage sees it,
and functions sharing one named window correctly group into a
single `WindowAgg` node (same as writing the spec out twice inline).

**Tests:** `internal/parser/window_test.go`
(`TestParseWindowClauseNamedWindow`,
`TestParseWindowClauseMultipleNamedWindows`);
`internal/analyzer/analyzer_test.go`
(`TestAnalyzeNamedWindowClauseAccepted`,
`TestAnalyzeNamedWindowUndefinedRejected` — both the wrong-name and
right-name-not-defined-here cases); `internal/executor/
window_compat_test.go`'s `TestCompatWindowNamedWindowClause` proves
end-to-end that a named `OVER w` used by two different functions
produces byte-identical output to writing the same spec inline
twice.

**Still deferred (unchanged from the original scope):** frame
clauses, LAG/LEAD-specific default-shapes are already implemented
elsewhere — only ROWS/RANGE/GROUPS frame execution remains open,
tracked as its own `unimplemented_feat.json`/fix_plan M0122-0004
item. Combining forms (`OVER (win_name ORDER BY ...)` extending a
named window with additional clauses at the reference site, and a
named window definition itself referencing another named window as
its base) are **not** supported — only a bare `OVER name` and a
self-contained `WINDOW name AS (...)` body. Both are real upstream
syntax (see `postgres/src/test/regress/sql/window.sql`) not
exercised by this loop's tests; deferred to a follow-up if a real
query shape needs them.

## Follow-up: frame-consuming aggregate window functions (2026-07-05, M0122-0004)

Implemented `sum`/`count`/`avg`/`min`/`max` as window functions
(`sum(x) OVER (...)`, `count(*) OVER (...)`, etc.) — the natural
prerequisite this doc's own Follow-up section named for ROWS/RANGE/
GROUPS frame execution: before this change the only window functions
with executor support (row_number/rank/lag/lead) never consult a
frame at all, so there was no consumer to validate frame execution
against. This slice deliberately does **not** add frame-clause
parsing/execution — it implements PostgreSQL's *default* frame
instead (which needs no explicit ROWS/RANGE/GROUPS clause):
`RANGE UNBOUNDED PRECEDING` (cumulative, peer-group-inclusive) when
ORDER BY is present, otherwise the whole partition. Verified against
upstream PostgreSQL 18.3 directly (`postgres/local_install`) rather
than assumed.

**AST/planner:** `planner.WindowFunc` (`internal/planner/plan.go`)
gained `Star bool`, `Filter Expr`, and `InputType catalog.Type`
alongside the existing `Args []Expr`, so an aggregate window call can
carry the same shape `AggregateCall` needs. `buildWindowFunc`
(`internal/planner/planner.go`) gained a `"sum", "count", "avg",
"min", "max"` case that resolves the single argument (or `Star` for
`count(*)`) and the optional `FILTER (WHERE ...)` predicate, deriving
the output type with the same rules `buildAggregateCall` already uses
for the non-window path (`sum`→arg type, `avg`→float8 for float
input else numeric, `min`/`max`→arg type, `count`→int8). DISTINCT and
aggregate-internal ORDER BY are rejected with `0A000` — this mirrors
a genuine PostgreSQL restriction on aggregate window functions
(`parse_func.c`'s `transformAggregateCall`: "DISTINCT/aggregate ORDER
BY is not implemented for window functions"), not a v0 gap.
`windowCallKey` gained a `filter:` component — without it, two
`sum(x) FILTER (WHERE a) OVER (w)` / `sum(x) FILTER (WHERE b) OVER
(w)` calls with otherwise-identical signatures would collide onto
the same output column (latent bug in the pre-existing key, never
exercised because none of row_number/rank/lag/lead take FILTER).

**Analyzer:** `analyzeWindowFuncCall` (`internal/analyzer/
analyzer.go`) gained the mirror-image validation (same DISTINCT/
ORDER BY rejection, same output-type rules) — kept in sync with the
planner per `pattern_sibling_paths_must_agree` since both type-check
the same call independently.

**Executor:** `internal/executor/operators_window.go` reuses the
*existing* GROUP BY aggregate accumulator (`aggregateOp.applyAgg`/
`finishAgg` in `operators_join_agg.go`) instead of a second
implementation, via `windowFuncToAggregateCall` (adapts a
`WindowFunc` into an `AggregateCall`) and a bare `&aggregateOp{ctx:
o.ctx}` helper instance (its methods only touch `ctx`, verified by
reading both method bodies in full). This gets numeric-exact sums,
float4/float8 precision formatting, and NULL-skipping for free,
identical to non-window aggregates. Frame evaluation
(`evalFrameAggFuncs`) precomputes peer-group boundaries per partition
with a new `peerGroupBounds` helper — reusing the same `samePeer`
check `rank()` already used inline — then walks groups in order,
accumulating into one running `aggRuntime` per function so the
default cumulative frame falls out naturally: with no ORDER BY,
`samePeer` always returns `true`, so `peerGroupBounds` collapses to
a single group spanning the whole partition, giving the "whole
partition" default with no special-casing.

**Tests:** `internal/analyzer/analyzer_test.go`
(`TestAnalyzeWindowAggregateFunctionsAccepted`,
`TestAnalyzeWindowAggregateFunctionsRejected`;
`TestAnalyzeWindowFunctionUnsupportedRejected` repointed at
`first_value()`, a real still-unimplemented window function, since
`count(*) OVER ()` is no longer a valid rejection case);
`internal/executor/window_compat_test.go`
(`TestCompatWindowAggregatesDefaultFrame`,
`TestCompatWindowAggregateNoOrderByWholePartition`,
`TestCompatWindowAggregateFilterClause`) — all three pin exact
row values cross-checked against a scratch upstream PostgreSQL 18.3
instance, not just "no error".

**Still open:** ROWS/RANGE/GROUPS frame-clause parsing/execution
itself (now has a real consumer — a future loop can wire an explicit
frame clause into `evalFrameAggFuncs` by generalizing
`peerGroupBounds` into an arbitrary frame-bounds function).
`first_value`/`last_value`/`nth_value`/`ntile`/`cume_dist`/
`percent_rank` as window functions remain unimplemented. Combining
an explicit frame clause with row_number/rank/lag/lead (which
PostgreSQL rejects — those functions cannot have a non-default
frame) is not yet enforced since frame clauses aren't parsed at all.

## Follow-up: first_value/last_value/nth_value (2026-07-05, M0122-0004)

Implemented `first_value`/`last_value`/`nth_value` as window functions,
reusing the same default-frame infrastructure the previous Follow-up
section built for `sum`/`count`/`avg`/`min`/`max`. Per spec (and
`window_first_value`/`window_last_value`/`window_nth_value` in
`postgres/src/backend/utils/adt/windowfuncs.c`), these evaluate their
value expression at a specific row of the frame rather than
accumulating over it: `first_value` at the frame head, `last_value`
at the frame tail, `nth_value(expr, n)` at the n-th row from the frame
head (1-based, `NULL` if `n` is beyond the frame).

**AST/planner:** `buildWindowFunc` (`internal/planner/planner.go`)
gained `"first_value"`/`"last_value"` (exactly one argument, no
DISTINCT/star, return type = argument type via `inferExprType` — same
pattern as `lag`/`lead`) and `"nth_value"` (exactly two arguments:
value expression and `n`) cases. `analyzeWindowFuncCall`
(`internal/analyzer/analyzer.go`) mirrors the same arg-shape checks.

**Executor:** `internal/executor/operators_window.go`'s default frame
end (for a row's own peer group) is exactly the boundary
`evalFrameAggFuncs`'s `peerGroupBounds` already computes, so no new
frame-bounds logic was needed — only a new `frameEnd[]` per-partition
array (`hasFrameValueWindowFunc` gates its computation) mapping each
row's local index to its peer group's exclusive end, built once per
partition from the existing `peerGroupBounds` output. Given that:
`first_value` reads `o.rows[pStart]` (frame head is always the
partition start, since the default frame has no upper-bound-only
variant these functions would need); `last_value` reads
`o.rows[frameEnd[localIdx]-1]`; `nth_value` evaluates its `n`
argument per current row (same as `lag`/`lead`'s offset), rejects
`n <= 0` with `22016` (`argument of nth_value must be greater than
zero`, matching `window_nth_value`'s `ERRCODE_INVALID_ARGUMENT_FOR_
NTH_VALUE` exactly), and returns `NULL` when `pStart + n - 1` falls
at or past the frame end.

**Tests:** `internal/analyzer/analyzer_test.go`
(`TestAnalyzeWindowValueFunctionsAccepted`,
`TestAnalyzeWindowValueFunctionsRejected`;
`TestAnalyzeWindowFunctionUnsupportedRejected` repointed at `ntile()`
since `first_value()` is no longer a valid rejection case),
`internal/executor/window_compat_test.go`
(`TestCompatWindowValueFunctionsDefaultFrame`,
`TestCompatWindowNthValueOutOfFrameAndInvalidN`) — cross-checked
row-for-row (including the `nth_value(val, 0)` error text) against a
scratch upstream PostgreSQL 18.3 instance.

**Still open:** `ntile`/`cume_dist`/`percent_rank` as window functions
remain unimplemented (they exist today only as `WITHIN GROUP`
ordered-set aggregates, a different code path). ROWS/RANGE/GROUPS
frame-clause parsing/execution itself is still the largest remaining
piece of this bucket — see the previous Follow-up section.

## Follow-up: ntile/cume_dist/percent_rank (2026-07-05, M0122-0004)

Implemented the three remaining ranking window functions named as open
in the previous two Follow-up sections. Unlike `first_value`/
`last_value`/`nth_value`, none of these are frame-relative — they
needed their own per-partition computation rather than a drop-in onto
`peerGroupBounds`'s existing consumers, matching the prediction in the
`2026-07-05` `M0122-0003`-adjacent ledger row that this would not be
mechanical.

**AST/planner:** `buildWindowFunc` (`internal/planner/planner.go`)
gained `"cume_dist"`/`"percent_rank"` (zero arguments, no DISTINCT/star,
return type `float8` — matches `pg_proc.dat`) and `"ntile"` (exactly
one argument, return type `int4`) cases. `analyzeWindowFuncCall`
(`internal/analyzer/analyzer.go`) mirrors the same arg-shape checks.

**Executor (`internal/executor/operators_window.go`):**

- `ntile(nbuckets)` reproduces `window_ntile`
  (`postgres/src/backend/utils/adt/windowfuncs.c`) exactly: `nbuckets`
  is evaluated once per partition (from the partition's first row,
  matching upstream's first-call-only argument evaluation), rejects
  `nbuckets <= 0` with `22014`
  (`argument of ntile must be greater than zero`), and the first
  `total % nbuckets` buckets get one extra row rather than
  concentrating the remainder in the last bucket. New
  `evalNtileFuncs`/`evalNtileFunc`, called from `evalWindowFuncs`
  alongside the existing `evalFrameAggFuncs` pre-computation pass.
- `percent_rank()` = `(rank - 1) / (total_rows - 1)`, `0` when the
  partition has a single row (matches `window_percent_rank`'s
  divide-by-zero guard).
- `cume_dist()` = `NP / total_rows`, where `NP` is the 1-based count of
  rows at or before the current row's peer group — i.e. exactly the
  existing `frameEnd[]` boundary (`peerGroupBounds`) also used by
  `first_value`/`last_value`. `hasFrameValueWindowFunc` was extended to
  also gate `frameEnd[]` computation for `cume_dist`.
- Both `percent_rank`/`cume_dist` reuse the existing `rank` local
  (already computed for `rank()`/`dense_rank()`) rather than
  recomputing tie position.

**Tests:** `internal/analyzer/analyzer_test.go`
(`TestAnalyzeWindowRankingFunctionsAccepted`,
`TestAnalyzeWindowRankingFunctionsRejected`;
`TestAnalyzeWindowFunctionUnsupportedRejected` repointed at
`dense_rank()` since `ntile()` is no longer a valid rejection case),
`internal/executor/window_compat_test.go`
(`TestCompatWindowNtileBuckets`,
`TestCompatWindowNtileMoreBucketsThanRows`,
`TestCompatWindowNtileInvalidArgument`,
`TestCompatWindowPercentRankAndCumeDist`) — the bucket-sizing and
percent_rank/cume_dist cases pin exact values reasoned from upstream's
algorithm, including the non-obvious "remainder buckets get the extra
row, not the last bucket" rule.

**Still open:** `dense_rank()` as a window function (only its `WITHIN
GROUP` ordered-set-aggregate form exists) is now the only rejection
case left for `TestAnalyzeWindowFunctionUnsupportedRejected`.
ROWS/RANGE/GROUPS frame-clause parsing/execution itself remains the
largest item in this M0020 bucket — every ranking/value/aggregate
window function implemented so far still assumes PostgreSQL's default
frame; an explicit frame clause is not yet parseable at all.
