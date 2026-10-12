# M0146-0125 — `||` on arrays is array_cat / array_append / array_prepend, which are not strict

Status: landed 2026-10-12.
Parent: none (S2, placed into banner item 2a by the owner on the evening of
2026-10-12).

## Problem

PostgreSQL resolves `||` with an array operand to one of three functions
(array\_userfuncs.c):

- `array_cat` for array ‖ array;
- `array_append` for array ‖ element;
- `array_prepend` for element ‖ array.

None of them is strict (`proisstrict = f`). A NULL array operand acts as an
empty array, a NULL element is appended as a NULL element, and `array_cat`
returns NULL only when both operands are NULL.

goopg returned NULL for any `||` with a NULL operand:

- `array[1] || null`, `array[1] || null::int` and `null || array[2]` all gave
  NULL. PG gives `{1}`, `{1,NULL}` and `{2}`. Column operands behaved the
  same way, so this was not only constant folding.
- There were two NULL paths:
  - `evalConcat`'s first line returned NULL for any NULL operand;
  - `tryFoldBinaryOp` folded any operator with a bare NULL operand to NULL
    at plan time.
- Multi-dimensional concatenation was wrong too.
  `array[1,2] || array[[3,4]]` gave `{1,2,{3,4}}`, where PG nests the
  1-D array as one row: `{{1,2},{3,4}}`.
- The `array_cat()` function returned `{}` for two NULLs, broke 2-D inputs
  (`{1,2,"{3","4}",…}`), and swallowed argument errors.
- An appended text element was not quoted, so `{x,a b}` was produced where PG
  gives `{x,"a b"}`.

## Design

- **Three array modes for `concatModeOf`.** It now returns
  `concatArrayCat`, `concatArrayAppend` or `concatArrayPrepend`, decided from
  the operands' static types, as PG's operator resolution does:
  - array beside array, or beside an untyped NULL or string literal (the
    unknown literal adopts the array type): `array_cat`;
  - array beside a typed non-array operand: `array_append` or
    `array_prepend`;
  - a polymorphic or unresolved operand keeps the existing shape-based path.
  - `ExprResultType` now types an `ARRAY[…]` constructor as an array of its
    first element's type, which is what lets these literal forms reach a
    mode.
- **`evalArrayConcat`** (`concat_mode.go`) implements the three functions on
  goopg's text array form.
  - NULL handling follows each function's non-strict rule.
  - Equal dimension counts concatenate along the first dimension.
  - Arrays one dimension apart nest the smaller array as an element.
  - A larger gap raises 2202E `cannot concatenate incompatible arrays` with
    PG's DETAIL.
  - A non-array untyped literal raises 22P02 `malformed array literal`.
  - Appended elements are rendered as `array_out` would: NULL as the bare
    token, and quoting for empty strings, `NULL`, quotes, backslashes, braces,
    commas and whitespace.
- **Both evaluator twins.** `evalConcat` dispatches the array modes before
  its strict NULL check. The compiled twin carries the mode in payload\[16\]
  bits 32 / 64 / 128, alongside the existing text (8) and jsonb (16) bits.
- **The `array_cat()` function** shares `evalArrayConcat` and propagates
  argument errors.
- **Plan-time fold.** `tryFoldBinaryOp` no longer folds `||` to NULL when an
  operand is array-typed (`exprIsArrayTyped`). Text and jsonb `||` remain
  strict and still fold.

## Verification

- **PG 18.3 probes.**
  - Probe 1 (literal, cast and column forms; the function forms;
    multi-dimensional; both-NULL) is byte-identical to PG.
  - Probe 2 (element quoting, 3-D, nesting both ways, untyped array
    literals, strict text and jsonb `||`, text columns) is identical except
    for the caret on the parse-time malformed-literal error.
- **New test.** `TestArrayConcatNonStrict` (postmaster) includes WHERE-clause
  forms that reach the compiled twin. It fails on HEAD in every arm.
- **Regress.** `arrays` 3171 → 3142 lines: `array_cat` and `||` across
  dimensions now match. `polymorphism` is identical when run alone, and
  `plpgsql` is run-context noise. The other 15 cases are identical.
- **Gates.** Units; tpch-spotcheck; the acceptance arm (values identical);
  sf025 PASS=99; the TPC-DS fire-set gate (no plan changed).

## Residuals (ledgered)

- **Element dimensions are not checked.** Concatenating arrays of equal
  dimension count but different inner extents does not raise "Arrays with
  differing element dimensions …".
- **The other array functions still build text by hand.** The
  `array_append()` / `array_prepend()` function arms keep their own text
  builders, and neither quotes elements.
- **Error position.** The malformed-literal error carries no caret position.
- **Untyped operands.** Operands whose types cannot be resolved still take
  the shape-based path.
