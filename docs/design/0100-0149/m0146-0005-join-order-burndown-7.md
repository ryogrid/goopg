# M0146-0005 (part 7): slices 105+ — EXPLAIN text parity

Continuation of [m0146-0005-join-order-burndown-6.md](m0146-0005-join-order-burndown-6.md)
(slices 87-104), split per the design-doc size rule (D3). Same task and
census family. Progress is measured with `scripts/tpcds-text-identity.py`:
plans whose EXPLAIN text equals PG's with costs ignored.

## Slice 105: M0146-0005cz — an IN list over a text-only function folds to an array Const

Slice 87 taught EXPLAIN to print an all-literal IN list the way PG does.
transformAExprIn builds a ScalarArrayOpExpr, and eval_const_expressions
folds its ArrayExpr into one array Const: `= ANY ('{a,b}'::text[])`. The
fold needs the operand's type, which comes from `optimizer.ExprResultType`.
For a function call, that is an exact pg_proc lookup on the argument types.

`substr(ca_zip, 1, 5)` over a char/varchar column has no exact match.
pg_proc only has `substr(text, …)`, and PG reaches it through an implicit
coercion of the argument. So the lookup failed and the list printed as
the raw `= ANY ('85669', '86197', …)`. This affected TPC-DS Q8, Q15 and
Q45 at both scales.

- `formatInExprPG` falls back to `stringTypeName` when ExprResultType
  cannot resolve the operand. That is the classifier slice 93 already
  uses to print the argument's `(x)::text` coercion. Text-only functions
  (substr, substring, upper, lower, initcap) return text, so the list
  folds to `'{…}'::text[]`.
- The fix stays in the EXPLAIN layer. A general implicit-coercion pass in
  `funcCallResultType` (PG's func_select_candidate) would also change
  planner-side typing, which is out of scope here. The deferral ledger
  records it.

Test: `explain_in_list_array_test.go` gains `substr(varchar)`,
`substr(text)`, `upper(char)` and `a + 1` cases. Each expected line is
PG 18.3's output. The varchar and char cases fail without the fix.

Movement:

- Unfolded IN lists in the TPC-DS captures go 3 → 0 at both scales.
- Text-identical plans go 24 → 25 at SF0.25 and 13 → 14 at SF1 (Q15).
  Aligned lines go 2211 → 2214 and 2013 → 2016.
- CATEGORIES-EXCL-MATCH is unchanged (rendering 12 / 15).
- Regress A/B is not applicable: no PG expected output has an IN list
  over a text-only function.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q8 Q15 Q45)
  and ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice105/`.

## Slice 106: M0146-0005da — a non-canonical date literal prints as date_out's text

make_op coerces `d_date >= '2002-5-01'` to a date Const, and get_const_expr
prints that value with date_out: `'2002-05-01'::date`. goopg printed the
coerced literal only when its text was already ISO (`YYYY-MM-DD`), so
TPC-DS Q16, Q94 and Q95 (`'2002-5-01'`, `'2001-4-01'`) kept the bare
`'2002-5-01'` at both scales.

- `canonicalISODateText` returns the ISO text for a four-digit-year
  `Y-M-D` literal with one- or two-digit month and day. That form reads
  the same under every DateStyle field order, so the printed text does not
  depend on the session. Every other spelling declines, as before.
- It replaces the ISO-only check at all three date sites: a comparison
  operand (coerceLiteralText), an explicit cast (castLiteralConstText)
  and an IN-list array element (inListArrayConst).
- Side finding, filed and not worked: numeric literal arithmetic folds
  through float64 (Q21's `2.0/3.0` → `0.6666666666666666`), filed as
  M0146-0041 (S2). The `cast(... as date) + 60` bound in Q94 is
  M0146-0040's `date + integer` defect (S2), which this slice does not
  touch.

Test: `explain_coerced_literal_test.go` adds three date cases and
`explain_in_list_array_test.go` adds a date IN list, all with PG 18.3's
output. Three of them fail without the fix.

Movement:

- Uncoerced date literals in the TPC-DS captures go 3 → 0 at both
  scales.
- Text-identical plans go 25 → 26 at SF0.25 and 14 → 15 at SF1 (Q94).
- CATEGORIES-EXCL-MATCH is unchanged (rendering 12 / 15).
- Regress A/B is not applicable: no regress EXPLAIN uses a non-canonical
  date literal.
- Gates pass: units, spotcheck, sweep 96/96, arm, fire set (Q16 Q94 Q95)
  and ea-ratchet (10).

Evidence: `analysis/m0146/m0146-0005/slice106/`.
