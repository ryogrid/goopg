# M0146-0044 — exact mod(numeric) and `numeric % numeric`

Status: done (2026-10-03). Parent: M0146 (banner item 2a, third S2 batch).

## The defect

- **`mod()`.** The function read `a.Int % b.Int`, the integer lane of the
  Datum. So `mod(10.5::numeric, 3)` returned 0,
  `mod(12345678901234567890::numeric, 123)` returned 11, and
  `mod(999999999999999999999::numeric, 10^21)` returned 10. PG 18.3 returns
  1.5, 78 and 999999999999999999999.
- **`%`.** The operator raised `operator % not supported on numeric`;
  PG runs numeric_mod.
- **Result type.** `mod()` was typed `int8` whatever its arguments.

## The fix

- **`numericMod`** (numeric.go) is PG's `mod_var`, x − trunc(x/y)·y,
  exact. Aligned to the larger display scale, it is the remainder of the
  aligned integers (`big.Int.Rem` truncates, so the result takes the
  dividend's sign) at scale max(dscale_x, dscale_y). A zero divisor raises
  22012.
- **`%`** on numeric routes to it (evalBinary).
- **`mod(a, b)`** routes to it when either argument is numeric; two
  integers keep the integer path, like PG's int2/int4/int8 overloads.
  Its division-by-zero error carries no cursor position, as PG's doesn't.
- **Planner constant folding** (`foldNumericArith`, foldconst.go) folds
  `%` with the same function, so a folded `10.5 % 3` equals the runtime
  value.
- **`modResultType`** (planner.go) resolves mod's overload as PG does: a
  numeric argument gives numeric; otherwise the widest integer, an integer
  literal counting as int4 when it fits.

## Verification

- Scratch probes against PG 18.3 are identical:
  - the three reported values;
  - `%` over mixed signs and scales (`-10.5 % 3` = −1.5, `1.000 % 0.3` =
    0.100, `-7 % 2.5` = −2.0);
  - integer `mod`;
  - a VALUES column;
  - both division-by-zero errors;
  - `pg_typeof` of each overload;
  - the column types of a CTAS.
- `TestNumericMod` pins values, types and errors.
- Regress `numeric` shrinks 3800→3786 lines: its `%` / mod cases now
  print PG's values. `int2`, `int4` and `int8` are byte-identical.
- Gates: units, spotcheck, sweep 96/96, fire set (no plan changes), TPC-H
  arm.

## Residuals

- goopg has no numeric NaN / ±Infinity, so `mod(NaN, …)` and `mod(x,
  'Infinity')` (PG: NaN, x) cannot arise.
- `gcd`, `lcm`, `abs` and `div` are still typed `int8` regardless of
  their arguments (ledgered).
- **Found, filed as M0146-0039a (S2):** `CREATE TEMP TABLE … AS` creates a
  PERMANENT relation (`relpersistence = 'p'`) that survives a restart.
  Plain `CREATE TEMP TABLE` is correct (`t`).
