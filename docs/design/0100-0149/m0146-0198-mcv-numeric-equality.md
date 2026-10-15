# M0146-0198 — MCV matching compares numbers by value

Status: done 2026-10-16 — `statLiteralEqual` compares number-family MCV
entries with the literal by value, as `var_eq_const` does through the type's
equality operator. TPC-DS Q43 matches PG.

## Problem

TPC-DS Q43 filters `store` on `s_gmt_offset = -5`. The column is
`numeric(5,2)`, and at SF0.25 its only MCV is `{-5.00}` with frequency 1, so
PG estimates all 12 rows of `store`. goopg estimated 1 row, joined
`store_sales ⋈ store` first and nested-looped `date_dim` (join-method). PG
joins `store_sales ⋈ date_dim` first and hash-joins `store` last.

PG's `var_eq_const` (`./postgres/src/backend/utils/adt/selfuncs.c`) tests each
MCV against the constant with the type's equality operator (`numeric_eq`), so
the textual forms `-5.00` and `-5` are equal. goopg's `statLiteralEqual`
(`internal/optimizer/selectivity.go`) compared the ANALYZE-stamped text with
the literal's rendering byte for byte, after only the bpchar blank-padding
rule. The MCV never matched, and the estimate fell to the non-MCV branch.

## Change

- `statLiteralEqual` keeps the byte compare as its fast path. When that
  misses, it asks `statNumericEqual`.
- `statNumericEqual` compares by value:
  - numeric and the integer types exactly, through `big.Rat`;
  - float4/float8 as float64.

  It reports `ok=false` for any other type, or when either side does not parse
  (numeric `NaN`, `Infinity`). In that case the text result stands.
- Test: `TestStatLiteralEqualComparesNumbersByValue` covers `-5.00` vs `-5`,
  NaN, int4 `07`, float8 and text, plus `eqSelectivityForColumn` over the MCV
  `{-5.00}` freq 1 against the integer constant `-5` (selectivity 1).

## Measured

Binary `tmp/m0198-bin`, captures in `analysis/m0146/m0146-0198/`, compared with
the M0146-0197 baselines:

- TPC-DS SF0.25: match 80 → 84 (Q33, Q43, Q56, Q60). Categories excluding
  matches: join-order 17 → 13, join-method 12 → 9, scan-type 7 → 5,
  parameterisation 8 → 6, aggregation 4 → 1, sort 9 → 6, parallelism 9 → 5.
- TPC-DS SF1: match 67 → 69 (Q33, Q60). Q56 drops from six categories to
  sort-strategy only.
- TPC-H: plan text byte-identical (15/22).

## Residual

The other join-method records named by the census have their own mechanisms:
SF1 Q68, and TPC-H Q18 (`customer` scanned in parallel and joined last in PG).
Neither moved, and both stay with the M0146-0014 census.
