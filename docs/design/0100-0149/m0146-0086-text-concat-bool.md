# M0146-0086 — `text || bool` renders the bool through its cast to text

Status: landed 2026-10-13.
Parent: M0146-0081 (item 2a rank via the M0146-0055 pin).

## Problem

`SELECT 'x' || true` returned `xt`. PostgreSQL 18.3 returns `xtrue`.
Column operands (`'v=' || b`), a parenthesised comparison (`'x' || (1 < 2)`)
and PL/pgSQL assignment (`t := 'x' || b`) behaved the same way. A WHERE
clause such as `'v=' || b = 'v=true'` therefore matched no rows.

PostgreSQL resolves `text || anynonarray` and `anynonarray || text` to
`anytextcat` / `textanycat`. Both are SQL functions, `$1::text || $2` and
`$1 || $2::text`, so the non-text operand goes through its cast to text. For
bool that cast is `booltext`, which gives `true` / `false`. The output
function `boolout` gives `t` / `f`, and goopg used it. `concat()` and
`format()` do use the output function, so they correctly keep `t` / `f`.

## Design

- **`concatOperandText`** (`expr.go`) renders one `||` operand in
  `evalConcat`'s text path. A `KindBool` datum becomes `true` / `false`.
  Every other datum keeps `formatDatumDateStyle`.
- **Both evaluator twins.** The interpreted path and the compiled twin
  (payload\[16\] bit 8, `concatText`) both reach `evalConcat`, so the one change
  covers both. `concat()` / `format()` do not go through `evalConcat`.

## Verification

- **PG 18.3 probe** (`tmp/m0086/probe.sql`): literal bools on either side, a
  NULL bool, bool / char(3) / int / date columns, `concat('x', true)`,
  `'x' || (1 < 2)`, PL/pgSQL assignment and `'x' || array[…]`. It is identical
  to PG except for the known missing caret on the parse-time
  malformed-array-literal error (an M0146-0125 residual).
- **New test.** `TestConcatBoolUsesTextCast` (postmaster) includes a
  WHERE-clause form that reaches the compiled twin. It fails on HEAD in all
  four arms.
- **Regress A/B.** `boolean`, `strings`, `text`, `select`, `json`, `jsonb`,
  `misc_functions`, `rangefuncs`, `arrays` and `polymorphism` are identical.
  `plpgsql` moved 4091 → 4087, which is its known `f1` run-context noise.
- **Gates.** Units; tpch-spotcheck; the acceptance arm (values identical);
  sf025 PASS=99.

## Residuals

A sweep of other types whose cast to text differs from the output function
(`tmp/m0086/probe3.sql`) found one more, plus an error-path gap:

- **inet**, filed as M0146-0166. Its cast to text is `network_show`, which
  always prints the mask length: `'1.2.3.4'::inet::text` is `1.2.3.4/32`, and
  `'x' || '1.2.3.4'::inet` is `x1.2.3.4/32`. goopg prints `1.2.3.4` for both,
  so its `::text` cast is wrong too. An inet datum is an untyped `KindString`
  at run time, so the fix needs the operand's static type: in the cast arm,
  and in `concatModeOf` for `||`.
- **`text || "char"`** is ambiguous in PG (`operator is not unique`). goopg
  returns `xa`. This is ledgered.
- numeric, float8, json, interval, name and char(n) match PG.
