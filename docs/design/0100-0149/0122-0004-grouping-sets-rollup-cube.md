# 0122-0004 — GROUP BY GROUPING SETS / ROLLUP / CUBE

Status: accepted (2026-07-05)

## Problem

`GROUP BY ROLLUP(...)`, `CUBE(...)`, and explicit `GROUPING SETS (...)`
parsed successfully but were silently downgraded to a plain `GROUP BY` over
the listed columns: the parser consumed the construct and injected an
`IntegerConst(0)` sentinel (`internal/planner/planner.go`'s
`buildAggregateStage` skipped it), so a query never produced the
per-subtotal / grand-total rows the construct exists for. Confirmed-open
per `unimplemented_feat.json` (task `M0097-regress`) and tracked as one of
M0122-0004's remaining SQL-language gaps.

## Approach

SQL:1999 §7.9 defines the result of a GROUP BY clause using GROUPING
SETS/ROLLUP/CUBE as equivalent to independently grouping by each listed
set and taking the `UNION ALL` of the results. goopg implements this
literally, as an AST rewrite, rather than adding a multi-grouping-set
physical Aggregate operator:

1. **Parser** (`internal/parser/select.go`'s `parseGroupByElems` and
   helpers `parseGroupingUnitList`/`parseGroupingSetsList`/
   `rollupAlternatives`/`cubeAlternatives`/`cartesianProductGroupingSets`):
   expands the clause at parse time into `SelectStmt.GroupingSets
   *GroupingSetsSpec` (`internal/parser/ast.go`), a fully-materialized
   `[][]Expr` — one entry per generated grouping set. `ROLLUP(a,b,c)`
   yields the n+1 prefixes `{a,b,c},{a,b},{a},{}`; `CUBE(a,b,c)` yields
   all `2^n` subsets; explicit `GROUPING SETS (...)` is taken verbatim
   (nested `ROLLUP`/`CUBE` inside a `GROUPING SETS` list is supported —
   each nested construct's generated sets are folded into the enclosing
   list). Multiple comma-separated GROUP BY elements cross-multiply
   (`GROUP BY a, ROLLUP(b, c)` = `{a} x {[b,c],[b],[]}`), matching
   upstream. `SelectStmt.GroupBy` still holds the flattened union of every
   referenced expression, so pre-existing consumers (analyzer resolution,
   the `FOR UPDATE`/`GROUP BY` conflict check, positional `GROUP BY`) are
   unaffected. Expansion is capped at `maxGeneratedGroupingSets` (4096) to
   guard against a mistakenly large `CUBE` column list.

   `GROUPING(expr, ...)` is parsed as a dedicated `*parser.GroupingCall`
   node (like `EXTRACT`) rather than a generic `FuncCall`, since it isn't a
   real catalog function.

2. **Planner** (`rewriteGroupingSets`, `internal/planner/planner.go`,
   called from `planSelect` immediately after the indirection-star
   rewrite and before anything else — including the CTE preplan and the
   `s.SetOp != nil` check): for each generated set, builds a plain
   single-`GROUP BY` `*parser.SelectStmt` branch (same `From`/`Where`,
   `GroupBy` = that one set, `Targets`/`Having` copied). `s` itself
   becomes the head of the chain (its `GroupBy`/`Targets`/`Having` become
   the first branch's); the remaining branches thread through `s.SetOp` as
   an ordinary `UNION ALL` chain. Control then falls straight through into
   the pre-existing N-ary-UNION-chain planning code (segment flattening,
   per-branch column casts via `wrapSetOpBranchWithCasts`,
   `wrapSetOpSortLimit` for the original `ORDER BY`/`LIMIT`/`OFFSET`) —
   that machinery is completely unmodified and unaware the chain was
   synthesized rather than parsed. This is also why a nested-subquery
   `GROUPING SETS` clause works for free: every `SelectStmt` node flows
   through the same `planSelect` entry point, so the rewrite runs on it
   too.

   `substituteGroupingExpr` rewrites each branch's target list / `HAVING`:
   any subexpression matching one of the construct's "universe"
   expressions (parserExprKey-keyed, appears in *some* generated set) but
   absent from the *current* branch's active set is replaced with a bare
   `NULL` literal — the standard semantics for a dimension rolled up away
   at that grouping level. `wrapSetOpBranchWithCasts` (unmodified,
   pre-existing) then casts that branch's `NULL` to match the anchor
   (first) branch's real column type, since the first generated set is
   always the most detailed one and therefore has the real, non-`NULL`
   column type. `GROUPING(...)` calls resolve to a literal `IntegerConst`
   bitmask per branch (bit *i*, counting from the rightmost/least-
   significant arg, is 1 iff that arg is excluded from the active set) —
   its value depends only on which branch is active, never on data, so
   there is no executor cost. Arguments of aggregate function calls
   (built-in via `isAggregateFunc`, user-defined via
   `isUserAggregateFunc`) are left untouched: aggregates evaluate over the
   raw pre-grouping rows, not the rolled-up output value.

   The substitution walker (recursing through `BinaryOp`/`UnaryOp`/
   `IsNullExpr`/`IsBoolExpr`/`IsDistinctFromExpr`/`CollateExpr`/
   `CastExpr`/`RowExpr`/`CaseExpr`/non-aggregate `FuncCall`) covers common
   composite expressions (`upper(dept)`, `CASE WHEN dept IS NULL …`,
   arithmetic over grouped columns). An unrecognised node shape is
   returned unchanged rather than guessed at — see Deferred.

3. **Analyzer** (`internal/analyzer/analyzer.go`'s `analyzeExpr`):
   `*parser.GroupingCall` resolves its args (catching an unknown-column
   reference the same way a plain SELECT-list column would) and types the
   call `int4`. No further analyzer change was needed — `GroupBy` keeps
   its pre-existing flattened-real-expression shape, so the analyzer's
   per-element `analyzeExpr` loop validates a `GroupingSets` query exactly
   like an ordinary one.

No executor change was needed at all: the physical plan for a grouping-sets
query is just an ordinary `Aggregate`-per-branch `SetOp`-chain, both of
which already existed.

## Example

```sql
SELECT dept, region, SUM(amt), GROUPING(dept, region) AS g
FROM sales
GROUP BY ROLLUP(dept, region)
```

expands (conceptually) to:

```sql
SELECT dept,        region,      SUM(amt), 0 AS g FROM sales GROUP BY dept, region
UNION ALL
SELECT dept,        NULL::text,  SUM(amt), 1 AS g FROM sales GROUP BY dept
UNION ALL
SELECT NULL::text,  NULL::text,  SUM(amt), 3 AS g FROM sales GROUP BY ()
```

(the `NULL::text` casts are `wrapSetOpBranchWithCasts`, not literal SQL the
rewrite emits).

## Tests

- `internal/parser/select_test.go`: `TestParseGroupByRollupExpandsToPrefixSets`,
  `TestParseGroupByCubeExpandsToAllSubsets`,
  `TestParseGroupByMixedPlainAndRollupCrossMultiplies`,
  `TestParseGroupingSetsExplicitList`, `TestParseGroupingFuncCall` — pin the
  parse-time expansion shape.
- `internal/executor/grouping_sets_compat_test.go`:
  `TestCompatGroupByRollupGeneratesSubtotalsAndGrandTotal`,
  `TestCompatGroupByCubeGeneratesAllSubsetTotals`,
  `TestCompatGroupByExplicitGroupingSets`,
  `TestCompatGroupingFuncReportsRolledUpColumns` — end-to-end
  parse→plan→execute, asserting the actual subtotal/grand-total rows and
  `GROUPING()` bitmask values PostgreSQL 18.3 produces for the same shape.

Gates: `go build ./...` clean; `go test ./internal/parser/...
./internal/analyzer/... ./internal/planner/... ./internal/executor/...`
PASS; `scripts/tpch-spotcheck.sh` PASS (TPC-H uses no GROUPING SETS, so this
gate only guards against a planner-wide regression from the `planSelect`
hook and the `buildAggregateStage` sentinel-branch removal).

## Deferred

- The `substituteGroupingExpr` walker recurses through the common
  composite-expression shapes listed above but not every `parser.Expr`
  variant (e.g. `InExpr`, `ExistsExpr`, `ArraySubscriptExpr`,
  `ArrayConstructorExpr`). A target-list/HAVING expression built from one
  of those around an excluded grouping column would be left un-
  substituted rather than becoming `NULL`, which would either be silently
  wrong (if it happens to still resolve) or surface as a spurious "column
  must appear in GROUP BY" planner error (if it doesn't) — see resume
  point in `.ralph/deferral_ledger.md` (2026-07-05, M0122-0004).
- Window functions (`OVER (...)`) referencing an excluded grouping column
  in their `PARTITION BY`/`ORDER BY` are not substituted (only `.Args` is
  walked for a `FuncCall`, not `.Over`) — window functions combined with
  `GROUPING SETS` in the same query is a narrow, rare combination.
- `SELECT DISTINCT`/`DISTINCT ON` combined with `GROUPING SETS` is copied
  onto every branch as-is; PostgreSQL's actual behavior in this
  combination was not cross-checked (very rare in practice).
