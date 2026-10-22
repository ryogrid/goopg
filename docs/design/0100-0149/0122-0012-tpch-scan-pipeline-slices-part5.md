# M0122-0012 — scan pipeline slices, part 5 (TPC-DS executor gaps)

Continues [0122-0012-tpch-scan-pipeline-slices-part4.md](0122-0012-tpch-scan-pipeline-slices-part4.md) (slices
34–45). Split out on 2026-10-22 under the 800-line design-doc limit; slice numbering is unchanged. Targets still
come from the per-query gap census of the TPC-DS SF0.25 corpus against the `:65438` reference (PG warm times,
goopg's latest sweep), and TPC-H is re-measured for every slice.

## Slice 46 (2026-10-22): integer variance and stddev are PG's numeric_stddev_internal — TPC-DS Q39

**Target, named up front:** TPC-DS Q39, 2.5 s in the sf025 sweep against PG's 0.59 s. It is two statements, and
each one's time is a CTE: a hash aggregate of 390 000 inventory rows into ~90 000 (warehouse, item, month)
groups, each computing `stddev_samp` and `avg` of an integer column. PG computes that CTE in ~0.39 s; goopg took
~2.05 s.

**Cause.** `exactIntVariance` was 28 % of the profile. It is the integer-input finisher for `var_pop`,
`var_samp`/`variance` and `stddev_pop`/`stddev_samp`/`stddev`, and it did two things:

- **The square root.** It ran a 15-step Newton-Raphson in 128-bit `big.Float`, allocating three Floats per step,
  for every group. Its `big.Float.Quo` alone was 15 % of the query.
- **Not PG's digits.** The variance was printed to 12 decimals with trailing zeros dropped, and the stddev to 18
  significant digits. PG prints both at `select_div_scale`'s rscale:

  | aggregate | goopg | PG |
  |---|---|---|
  | `var_samp(1,2,4)` | `2.333333333333` | `2.3333333333333333` |
  | `stddev_samp(1,2,4)` | `1.52752523165194667` | `1.5275252316519467` |
  | `var_pop` of five bigints | `21704` | `21704.000000000000` |

  The window-function test had pinned goopg's text as a known, ledgered gap: the 2026-07-27
  "tpcds-round2 stddev-precision" row.

PG's path for integer input is `int2/int4/int8_accum` → `numeric_poly_*` / `numeric_*` → `numeric_stddev_internal`
(`numeric.c`), with sumX and sumX2 at display scale 0.

**Rule.** `exactIntVariance` is now that function, entirely in exact `big.Int` arithmetic:

1. NULL for N = 0, or N ≤ 1 for the sample forms, checked before anything else.
2. `num = N·sumX2 − sumX²`. A non-positive numerator gives `0` at display scale 0, as PG's
   `make_result(&const_zero)` does.
3. `den = N·(N−1)`, or `N²` for the population forms.
4. `rscale = select_div_scale(num, den)` (`pgSelectDivScaleInt`):
   - take the NBASE (10⁴) weights `w1`, `w2` and leading NBASE digits `f1`, `f2`;
   - `qweight = w1 − w2`, minus 1 when `f1 ≤ f2`;
   - `rscale = 16 − 4·qweight`, clamped to [0, 1000].
5. **Variance:** `div_var(num, den, rscale, round)`, i.e. the quotient rounded half away from zero at rscale.
6. **Stddev:** `sqrt_var(variance, rscale)` (`pgSqrtScaled`).
   - PG's Karatsuba square root yields the exact truncated square root at F fractional NBASE digits:
     `F = res_ndigits − res_weight − 1`, which is `rscale/4 + 1`, or more when the clamp to one result digit
     applies.
   - `round_var` then rounds that to rscale.
   - Here that is `big.Int.Sqrt` (an exact floor square root) of the variance scaled by 10^(8F), followed by the
     same half-away rounding.

The result is a numeric Datum at scale rscale, as `avg(int)` already returns, instead of a string.

**Pins.**

- `TestExactIntVarianceMatchesPGDigits` holds eleven cases whose wants were read back from PG 18.3. They include
  negatives, a two-row stddev that keeps 16 decimals, an int8-extreme group whose root rounds to an integer, and a
  variance past int64. HEAD fails nine of them.
- `TestWindowAggregateFunctions`' variance/stddev cases now assert PG's `window.out` text instead of goopg's.
- `TestExactIntVarianceZeroAndDegenerate` keeps the Q39 zero-variance crash guard. It now reads the result through
  `Format()` and expects PG's `2.0000000000000000` for `var_samp(1,3)`.
- A live probe matched PG byte for byte on all 696 output lines: 11 variance/stddev forms over ~680 `store_sales`
  groups (int2, int4 and int8 inputs, negatives, products past int64) plus `date_dim` groups and edge rows.

**Measured.** TPC-DS Q39, warm, two interleaved rounds of three runs:

| build | Q39 (s) |
|---|---|
| HEAD | 3.01–3.89 |
| candidate | 2.10–2.40 |

In the sf025 sweep, Q39 went from 2.53 to 1.89 s, with its checksum equal to the PG oracle's as before (the
checksum normalises numeric text, which is why the old digits never tripped it). The sweep sum went from 125.2 to
120.8 s. TPC-H values are identical (acceptance 24 MATCH), and the sweep's plan shapes are identical.

**Left.**

- **Numeric input.** `exactNumericVariance` keeps `big.Rat` accumulators without the inputs' display scale. PG
  starts from `sumX.dscale`, so that path cannot be ported exactly until its state carries it. It still prints
  12 decimals / 15 significant digits.
- **Float input.** The float path prints with `strconv 'g' 15`, not `float8out`.
- Both are recorded in the deferral ledger.

## Slice 47 (2026-10-22): CASE compiles — TPC-DS Q59, Q2

**Target, named up front:** TPC-DS Q59, 2.2–2.4 s warm against PG's 0.72 s, the largest absolute gap in a fresh
warm re-time of the ratio candidates. The others re-timed were Q78 1.65 vs 0.49 s, Q97 1.05 vs 0.11, Q16 0.97
vs 0.08, and Q8 1.0 vs 0.14.

**Cause.** EXPLAIN ANALYZE put 2.27 s of Q59 in its CTE `wss`, against PG's 0.55 s. The CTE is a hash aggregate
of 687 k `store_sales ⋈ date_dim` rows computing seven
`sum(CASE WHEN d_day_name = 'Sunday' THEN ss_sales_price ELSE NULL END)` columns.

`evalCaseExpr` took 46 % of the profile, and `runtime.convTslice` 11 %. CASE was not in the compiled evaluator:
`buildExpr` made it an ExprAdapter, so each of the 4.8 M CASE evaluations ran through
`evalExprSlot → evalCaseExpr(x, slotToRow(slot), ctx)`. That flattened the slot into a Row, then boxed the Row
into a fresh `rowSlotView` interface for every WHEN and THEN it evaluated.

**Rule.** A new compiled kind, `ExprCase`, carries an `exprCaseInfo` built at slab-build time:

- the slab indices of the operand (noExpr for the searched form), each WHEN/THEN pair and the ELSE;
- the declared-type facts the simple form's implicit `=` needs: the operand's and each WHEN's bpchar width
  (`declaredBpcharTypmod`) and bare-literal flag (`isBareStringLit`).

These are the facts `evalCaseExpr` reads from the expressions. `evalFastCase` is its twin, arm for arm:

- the operand is evaluated once;
- each WHEN, in order, is a boolean predicate (searched form), or `operand = WHEN` resolved through the same
  `comparisonOperandsAsBpchar` and `compareEq` (simple form);
- a NULL or non-bool result is no match;
- the first match's THEN is evaluated and returned, otherwise the ELSE, otherwise NULL.

Only the chosen arm is evaluated. A CASE with a nil arm keeps the adapter. Every compiled call site gains the
node: aggregate arguments, projections, quals and join filters. The batch evaluator reaches it through its
per-row fallback.

**Pin.** `TestCaseCompiledMatchesInterpreted` compares `evalFastCase` with `evalCaseExpr` on every row of a
fixture. The fixture covers:

- the searched form, Q59's shape;
- several arms with no ELSE;
- a simple CASE over a `char(9)` operand, where padded values meet bare literals and a WHEN is NULL;
- simple CASEs over int and numeric operands with mixed-kind WHENs;
- a non-bool WHEN in the searched form;
- a CASE nested in a THEN.

It asserts at least eight distinct results. Passing a zero bpchar width in place of the compiled one fails it on
the padded `char(9)` rows.

**Measured.** Warm, two interleaved rounds of three runs:

| query | HEAD (s) | candidate (s) |
|---|---|---|
| TPC-DS Q59 | 2.32–2.37 | 1.80–1.89 |
| TPC-DS Q43 | 0.48–0.72 | 0.44–0.55 |

In the sf025 sweep, Q59 went from 2.30 to 1.82 s and Q2 (also day-name CASE sums) from 0.93 to 0.68 s. The
sweep's Q7/Q57/Q14 rises did not repeat in an A/B (equal or better on the candidate). TPC-H values are identical
(acceptance 24 MATCH), and the sweep's plan shapes are identical.

**Left.** In Q59's profile, the CASE WHENs' `d_day_name = 'Sunday'` comparisons are now the largest term:
`evalFastBinaryApply` takes 23 % on the bpchar arm, which normalises the operands' padding before comparing.
They are a next candidate.
