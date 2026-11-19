# M0149\-0001h1a66 — generate\_series row estimates

## Problem

A `generate_series` in FROM had no row estimate, so EXPLAIN showed `rows=1` for every form, including the
integer one, and joins above it were sized from one row.

## What PG does

`set_function_size_estimates` asks the function's planner support function \(SupportRequestRows\) and falls
back to `prorows`, 1000 for every `generate_series` variant. The support functions first run
`estimate_expression_value` on each argument, which also folds stable calls such as `now()`, then:

- integers \(`generate_series_int4_support`, `_int8_support`\): `floor((finish - start + step) / step)`;
- numeric \(`generate_series_numeric_support`\): `trunc((stop - start) / step) + 1` when step has the sign of
  `stop - start` \(zero counts as positive\), else 0, and no estimate for NaN or infinity;
- timestamps \(`generate_series_timestamp_support`\): `floor(span / step + 1)`, both measured with 30\-day months,
  and no estimate for an infinite end.

A NULL argument gives 0 rows, a zero step no estimate. The result goes through `clamp_row_est`.

## Design

`EstimateRows` has a `*GenerateSeries` arm, `generateSeriesRows`. It evaluates the arguments through the
`optimizer.EstimateSeriesRows` hook, which the executor registers \(`internal/executor/series_estimate.go`\),
when `seriesArgsEstimable` accepts them: literals, casts, operators and built\-in calls that are neither
volatile nor set\-returning, with no column, parameter or subquery. Otherwise the estimate is 1000.

## Verification

Row estimates for integer, numeric and timestamp forms, NULL and zero steps, `now()` arguments, volatile and
correlated arguments match PG 18.3. Costs still differ in places \(M0149\-0001h1a80\).
