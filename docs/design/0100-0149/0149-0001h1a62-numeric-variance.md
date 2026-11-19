# M0149\-0001h1a62 — numeric variance and standard deviation

## Problem

`var_pop` and `var_samp` over numeric input always printed 12 fractional digits, and `stddev_pop` and
`stddev_samp` printed 15 significant digits through a binary floating\-point square root. The values
were text, not numeric.

## What PG does

`numeric_stddev_internal` \(`numeric.c`\) works from N, sumX and sumX2. sumX carries the largest input
display scale d, and sumX2 carries 2d, so `N * sumX2 - sumX * sumX` is exact. It divides by `N * (N - 1)` or
`N * N` at the scale from `select_div_scale`, rounding half away from zero. The standard deviation is
`sqrt_var` of that rounded variance at the same scale. A numerator that is not positive gives 0 at scale 0.

## Design

`exactNumericVariance` follows those steps with exact integers and shares `pgSelectDivScale` and
`pgSqrtScaled` with the integer variance path. `pgSelectDivScale` now takes the numerator's scale, which
also sets the lower bound of the result scale.

`aggRuntime.numericScale` holds the largest input scale. It is merged in `combineVariance` and carried in
the serialized partial state, because a parallel worker may see only some of the inputs.

## Verification

150 randomized input sets of up to six values, plus NaN, infinities, one\-row and NULL input, window frames
and grouped queries, match PG 18.3 for all six aggregates.
