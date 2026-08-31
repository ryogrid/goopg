# M0134-0151 — `polygon.sql` poly_in-faithful parsing (PARKED)

**Date:** 2026-08-25
**Status:** contained fix landed, PARKED — matches the M0134-0094 (`box.sql`)
/ M0134-0098 (`circle.sql`) / M0134-0136 (`line.sql`) / M0134-0137 (`lseg.sql`)
/ M0134-0149 (`path.sql`) / M0134-0150 (`point.sql`) precedent. This closes
out the 7-type "core geometry" family — all seven primitives now have a real
`*_in`-faithful validate+canonicalize chokepoint.

## Result

`scripts/pg-regress-runner.sh polygon` against PG 18.3: diff went from
**405 lines (0% parity)** to **354 lines**. Like point.sql, does NOT reach
zero residual — the INSERT-validation and column-storage portion of the diff
is fully fixed, but the standing geometric-operator-lexer/dispatch gap and
GiST/SPGiST plan-integration gap dominate the remainder (both pre-existing,
cross-type blockers, not polygon-specific).

## Root cause

`polygon` was a raw-varlena pass-through with **zero validation at all** —
the same state box/circle/line/lseg/path/point were each in before their own
M0134 slices. This was the LAST unaudited core geometry primitive
(`.ralph/working_set.md`'s standing recommendation item #5 named it
explicitly as "next task" after point.sql closed). Garbage input like
`'(0,1,2)'` (odd point count with a stray coordinate) or `'asdf'` was stored
and echoed back verbatim; `pg_input_is_valid` answered `t` for that same
garbage.

## What landed

1. **`parsePolygonLiteral`/`polygonCanonicalText`**
   (`internal/executor/expr.go`) — a faithful port of `poly_in`/`poly_out`
   (`postgres/src/backend/utils/adt/geo_ops.c`):
   - `poly_in` computes `npts` via the same `pair_count` as `path_in` (odd,
     positive comma count required — even/zero is rejected up front), then
     calls `path_decode(str, opentype=false, npts, ..., isopen, endptr_p=NULL,
     ...)`.
   - `opentype=false` means a leading `'['` (the "open path" delimiter) is
     rejected outright rather than accepted as an open form — a polygon is
     **always closed**, unlike path.
   - `endptr_p == NULL` means, like `point_in`, the **entire** string must be
     consumed — no trailing text tolerated.
   - Unlike `path_in`, `poly_in` does **not** strip a single leading paren
     before calling `path_decode` (that "quick entry" unwrap — e.g. turning
     `"(1,2,3,4)"` into `"1,2,3,4"` before decode — is `path_in`-specific);
     the only wrapper polygon accepts is `path_decode`'s own doubled-paren
     `"((...))"` detection.
   - `polygonCanonicalText` mirrors `poly_out`'s `path_encode(PATH_CLOSED,
     npts, p)` — reused verbatim via the existing `pathCanonicalText(points,
     true)` helper (no new formatting logic needed).

2. **Wiring**, mirroring the box/circle/line/lseg/path/point chokepoint
   pattern:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — new `"polygon"`
     case.
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"polygon"` cases.
   - `evalCast`'s `::polygon` arm (`expr.go`) — newly added.
   - `evalTypedStringLit`'s `polygon '...'` arm (`expr.go`) — newly added.
   - `internal/parser/select.go` `tryTypedLiteral`'s typed-literal keyword
     whitelist — added `"polygon"` (same sibling gap `lseg`/`path` had
     before M0134-0150: a bare `polygon '...'` in *expression* context, e.g.
     `p |>> polygon '((300,300),...)'`, previously parsed as two unrelated
     tokens instead of one typed literal).

## Not fixed this loop

The residual 354-line diff has the same two causes point.sql's diff had,
both pre-existing cross-type blockers, not polygon-specific:

1. **Geometric operator lexer/dispatch family** (dominant cause) — `<<`,
   `&<`, `&&`, `&>`, `>>`, `<<|`, `&<|`, `|&>`, `|>>`, `<@`, `@>`, `~=`, `<->`
   against `polygon` operands either fail to lex (unknown token) or
   mis-dispatch to an unrelated overload (e.g. `<<` resolves to the integer
   left-shift operator: `operator << requires integer operands`). Same
   standing gap already recorded against line.sql/lseg.sql/point.sql's
   M0134-0136/-0137/-0150 ledger rows and geometry.sql's M0134-0125 sizing.

2. **GiST/SPGiST physical-index plan integration** — `quad_poly_tbl_idx`'s
   SP-GiST index is catalog-only, so every indexed-scan query plans as
   `Seq Scan` instead of `Bitmap Index Scan`/`Index Scan`. Same standing gap
   as working_set.md's item #1.

3. **`polygon(circle(...))` constructor function chain** — `SELECT ...
   polygon(circle(point(x*10,y*10), ...))` fails with `function circle does
   not exist` (a 3-arg `point`→`circle`→`polygon` conversion chain, not the
   type's own I/O). Out of scope for this slice — a scalar-function gap, not
   a parsing/validation gap.

## Resume points

- Operator lexer/dispatch: `.ralph/deferral_ledger.md`'s M0134-0136/-0137/
  -0150/-0151 rows.
- GiST/SPGiST plan integration: working_set.md standing recommendation
  item #1 (AM is catalog-only).
- `circle`/`polygon` constructor functions: separate scalar-function
  registration gap, not yet ledgered as its own item.
