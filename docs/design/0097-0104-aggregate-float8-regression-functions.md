# 0097-0104 — Aggregate Float8 + Regression Functions

## Problem

`aggregates` regress test had 1234 diff lines due to four distinct failure categories:

1. **`sum/avg(x::float8)` fails with "requires numeric argument"**: When float8
   values come from `evalCast` (e.g. `VALUES ('1', 'infinity')` cast to float8),
   they arrive in `applyAgg` as `KindString` with the raw string. The switch only
   matched `"NaN"`, `"Infinity"`, `"-Infinity"` exactly — any other string
   (including regular numbers like `"1"`) hit the error case. This caused entire
   `sum/avg/var_pop` queries over float8 VALUES to fail silently (~80 missing diff
   lines in the `verify correct results for infinite inputs` section).

2. **`'inf'::numeric` rejected**: The numeric cast only checked `"nan"`,
   `"infinity"`, and `"-infinity"` (case-insensitive) but not the abbreviated
   `"inf"` and `"-inf"` forms that PostgreSQL accepts. `var_pop('inf'::numeric)`
   returned "invalid input syntax" error (~10 missing diff lines).

3. **`avg(float8)` type returned `numeric`**: The planner's `buildAggregateCall`
   always returned `numeric` for `avg`. So `avg(gpa)` on a float8 column got a
   `numeric`-typed schema column, which bypassed `appendFloat8Text`'s `%.15g`
   formatting and instead used the full-precision KindNumeric text
   (`3.40000000000000015` instead of `3.4`).

4. **Regression aggregate functions returned NULL**: `regr_syy(b, a)`, `covar_pop`,
   `corr`, etc. accepted two arguments syntactically but `AggregateCall.Arg` only
   captured the first argument. `finishAgg` had no case for these names and returned
   `NullDatum`.

5. **`appendFloat8Text` output `+Inf`/`-Inf`**: Go's `strconv.AppendFloat` outputs
   `+Inf`/`-Inf` for infinity, but PostgreSQL uses `Infinity`/`-Infinity`. Caused
   all float8-typed Infinity aggregates to display with the wrong string.

6. **`stddev_pop/var_pop` format**: Used `strconv.FormatFloat(v, 'f', -1, 64)`
   (full decimal precision) instead of `'g', 15` (15 significant digits matching
   PostgreSQL's `float8out`). Led to wide column output diverging from PG.

## Root Causes

- Sibling-path drift between `evalCast` (KindString pass-through for float8) and
  `applyAgg` (only handled special KindString values).
- Missing abbreviated `"inf"` form in numeric cast.
- Missing `Arg2` field in `AggregateCall` for two-argument aggregates.
- `avg` type inference not checking argument type.
- `appendFloat8Text` not normalizing Go's infinity representation.

## Fixes

### A. Fix `applyAgg` KindString for `sum/avg` (operators_join_agg.go)

Replaced the case-sensitive switch with a case-insensitive comparison that
also handles `"inf"`, `"+inf"` aliases. For regular numeric strings (e.g. `"1"`
from `evalCast` on float8), parse with `parseNumeric` and accumulate in
`numericSum` — same as the `KindNumeric` path.

### B. Fix `'inf'` in numeric cast (expr.go)

Extended the `evalCastTyped` numeric KindString check to recognize `"inf"`,
`"+inf"`, `"-inf"` (abbreviated infinity forms). Also normalizes the returned
KindString to canonical capitalization (`"Infinity"`, `"NaN"`) so downstream
switches match.

### C. Fix `avg(float8)` type inference (planner.go)

Added `isFloatTypeName()` helper and changed `buildAggregateCall` to return
`float8` type when the argument is float4/float8. Added `finishAgg` fast-path
for float8 avg: convert numericSum to float64 and format with `%.15g`.

### D. Add `Arg2 Expr` to `AggregateCall` (plan.go, planner.go)

Added second-argument capture to `buildAggregateCall` for all two-argument
functions. Updated `cloneAggregateCall`, `walkPlanExprs`, `cloneExprReplacingOuter`,
and the remapping passes in `bushy.go` and `foldconst.go` to process `Arg2`.

### E. Implement regression aggregates (operators_join_agg.go)

Added `aggDatumToFloat64` helper and regression state to `aggRuntime`:
`regrN`, `regrSumX`, `regrSumY`, `regrSumXX`, `regrSumXY`, `regrSumYY`.

`applyAgg` evaluates both `call.Arg` (y) and `call.Arg2` (x) per row.

`finishAgg` implements: `regr_count`, `regr_avgx`, `regr_avgy`, `regr_sxx`,
`regr_syy`, `regr_sxy`, `regr_r2`, `regr_slope`, `regr_intercept`,
`covar_pop`, `covar_samp`, `corr`.

### F. Fix `appendFloat8Text` infinity/NaN output (dispatch.go)

Added explicit checks for `math.IsInf`/`math.IsNaN` before `strconv.AppendFloat`,
emitting `"Infinity"`, `"-Infinity"`, `"NaN"` matching PostgreSQL's float8out.

### G. Fix stddev/var_pop format (operators_join_agg.go)

Changed `strconv.FormatFloat(v, 'f', -1, 64)` to `'g', 15` for all variance
and standard-deviation aggregates.

## Impact

`aggregates` regress diff: **1234 → 1072 lines** (−162).

All previously-passing regress tests verified unaffected.

## Known Remaining Differences

Regression aggregate *values* differ slightly from PostgreSQL (e.g. `regr_syy`
`68756.2162` vs `68756.2156`) because goopg accumulates float4 columns using
their text representation (`7.8`) rather than the binary float32 value
(`7.800000190734863`). Full parity would require storing and accumulating float4
columns as their exact IEEE 754 single-precision binary values.
