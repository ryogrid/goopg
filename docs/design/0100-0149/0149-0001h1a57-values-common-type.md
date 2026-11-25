# M0149\-0001h1a57 — VALUES cells take the column's type

## Problem

goopg worked out a type for each VALUES column but left the cells as they were. A literal in a later
row stayed text, so `(values ('0'::numeric), ('4.2e9'))` printed `4.2e9`, and date arithmetic on such
a column failed. Columns of unrelated types were accepted.

## What PG does

`transformValuesClause` \(`analyze.c`\) runs `select_common_type` over each column and then
`coerce_to_common_type` on every cell. An unknown literal goes through the type's input function.
Types from different categories raise `VALUES types X and Y cannot be matched`, at the offending cell.

## Design

- `selectCommonType` and `checkCommonTypeCoercion` in `common_type.go` are taken out of the COALESCE
  code, so both callers share the rule. `exprStartPos` gives a cast's start, as `exprLocation` does.
- `coerceValuesColumn` is called by the standalone and the subquery VALUES planners. A string literal
  becomes a typed literal or a cast, and a cell of another type is cast. Integer widths are left
  alone because they share one representation. Types outside the modelled categories keep the
  previous behaviour.

## Numeric literals

A cast to numeric returned the input text, which is why an exponent survived. `evalCast` and the
`numeric 'x'` literal now produce a numeric value. NaN and the infinities are still carried as text.

That change exposed the variance aggregates. Finite values took the exact lane, which ignored an
infinity seen in the same group, and the lane read only the small mantissa form. A NaN or infinite
input now decides the result, and large values are read in full.

## Not covered

- The exact variance lane prints 12 fractional digits instead of using `select_div_scale`
  \(M0149\-0001h1a62\).
- `date 'x'` and the DateStyle hint remain with M0149\-0001h1a55.
