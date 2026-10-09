# M0146-0041: numeric literal arithmetic is folded with PG's numeric semantics

Status: landed 2026-10-02 (banner item 2a, S2).

## Defect

The planner's constant folder (`internal/optimizer/foldconst.go`,
`evalArith`) folded any arithmetic with a numeric operand by parsing both
sides with `strconv.ParseFloat` and formatting the float64 result.
`litCompare` compared numeric literals the same way. On PG 18.3 vs goopg:

| expression | PG 18.3 | goopg before |
|---|---|---|
| `0.1 + 0.2` | `0.3` | `0.30000000000000004` |
| `1.1 * 1.1` | `1.21` | `1.2100000000000002` |
| `12345678901234567890.5 + 1` | `12345678901234567891.5` | `12345678901234567000` |
| `1.50 + 1` | `2.50` | `2.5` |
| `1.0 / 3` | `0.33333333333333333333` | `0.3333333333333333` |
| `2.0 / 3.0 * 3` | `2.00000000000000000001` | `2` |
| `1.00000000000000000001 > 1` | `t` | `f` |

Division over numeric columns was already right; only folded constants
were wrong. TPC-DS Q21's `2.0/3.0` bound showed the bug in EXPLAIN.

## Change

PG folds a constant expression by calling the operator's function
(`eval_const_expressions` → `numeric_add` / `_sub` / `_mul` / `_div`).
goopg now does the same with the executor's numeric operators, so a folded
value has exactly the value and display scale a per-row evaluation gives:

- add/sub use the larger scale;
- mul sums the scales;
- div uses `select_div_scale`.

- `optimizer.NumericArith` is a hook the executor registers at init
  (`foldNumericArith` in `internal/executor/numeric.go`). The optimizer
  cannot import the executor.
  - It parses both literal texts with `parseNumeric`, applies
    `numericAdd/Sub/Mul/Div`, and returns `numericText`.
  - Division by zero still raises 22012 at plan time, as in PG.
  - Without a registered hook, numeric arithmetic is left unfolded rather
    than computed in float64.
- `litCompare` compares numeric literals exactly (`big.Rat`, PG's
  `cmp_numerics` by value).

`%` on numeric is still not folded (unchanged; see M0146-0044).

## Verification

`TestNumericConstantFoldMatchesPG` (executor) checks every row of the
table above, plus a `1e3/7` scale case and the EXPLAIN-visible
`0.66666666666666666667`, all against PG 18.3 output. It fails on the old
code.

- Regress A/B: `numeric` shrinks 2059 → 2042 diff lines (the
  float-mangled products and `trim_scale` lines are fixed). `case`,
  `expressions`, `select` and `aggregates` are unchanged.
- TPC-DS: Q21 is now text-identical to PG at both scales. Text-identical
  plans go 28 → 29 at SF0.25 and 17 → 18 at SF1. Q23 shows
  `0.95000000000000000000`, as PG does.
- Gates pass: units, spotcheck, sweep 96/96, arm (values identical to
  baseline), fire set (Q21, Q23) and ea-ratchet.

## Found alongside (filed, not worked)

M0146-0044: `mod(numeric, numeric)` returns wrong values
(`mod(10.5::numeric, 3)` = 0, PG 1.5), and `numeric % numeric` raises
"operator % not supported on numeric".
