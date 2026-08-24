# M0134-0094 — `box.sql`: `box_in`-faithful validation + canonicalization

## Status: PARKED (case still `failed`)

## Summary

`postgres/src/test/regress/sql/box.sql` sized live against the PG 18.3
oracle (`scripts/pg-regress-runner.sh --verbose box`) at a 768-line diff,
0% parity. Investigation found `box` was a **raw-varlena text pass-through**
with zero validation: `internal/executor/codec.go`'s decode/encode default
branch (the one covering every type without an explicit codec, called out by
its own comment for `point`/`path`) stored and returned whatever text was
given, unchanged. Concretely this meant:

- Every one of the file's five "badly formatted box inputs" cases (a lone
  coordinate pair, `[`/`]` delimiters, trailing garbage after a valid box,
  non-numeric text) silently **succeeded** where PG raises `22P02`.
- `SELECT * FROM BOX_TBL` echoed goopg's raw input spelling instead of PG's
  canonical, corner-normalized `(hx,hy),(lx,ly)` output.
- `box 'literal'` typed-string-cast syntax (used pervasively for the rest of
  the file's WHERE clauses) wasn't even in the parser's typed-literal
  allowlist — a bare syntax error blocked most of the file past the INSERT
  block.

This slice fixes all three, sharing one new PG-faithful parser/formatter
across all three entry points. The remainder of the file — `area()`,
area-based box comparison operators, the `&<`/`&>`/`<<|`/etc. operator
family, SP-GiST/GiST index support, and the point/box `<->` distance
operator — is each an independently large, separately-scoped subsystem; see
the deferral ledger row for the full six-item breakdown. The case stays
`failed`/PARKED per the established M0134 pattern (cf. M0134-0081..0093).

## Fix — `parseBoxLiteral` / `boxCanonicalText`

`internal/executor/expr.go` gains `parseBoxLiteral` (plus its `pairDecode`/
`singleDecode` helpers), reproducing `box_in`'s parse (via `path_decode`/
`pair_decode`, `postgres/src/backend/utils/adt/geo_ops.c`) closely enough to
match every case `box.sql` exercises:

- Accepts an optional outer wrapper paren — either doubled
  (`"((x1,y1),(x2,y2))"`) or single with no per-point parens
  (`"(x1,y1,x2,y2)"`) — and per-point optional parens independently.
- Requires the **entire** string to be consumed: `box_in` calls
  `path_decode` with a `nil` `endptr_p`, so trailing garbage
  (`"(1,2,3,4) x"`) is rejected exactly like PG does, not silently ignored.
- Reorders the two parsed points into `(high, low)` **per axis** — `if
  high.x < low.x { swap }`, independently for x and y — not a blind
  two-point swap; this matters for a "crossed" box like `((-8, 2), (-2,
  -10))` where the reordering isn't just "swap point 1 and point 2".

`boxCanonicalText` mirrors `box_out`/`path_encode(PATH_NONE, 2, &high)`:
`"(hx,hy),(lx,ly)"`, each coordinate formatted via the existing `PGFloatOut`
(the same Ryu-based float8out goopg already uses everywhere else — no new
float-formatting logic).

This is distinct from the pre-existing `parseBoxText` (`expr.go`), which
parses an already-canonical `"(x,y),(x,y)"` string for the exclusion-
constraint/GiST-overlap path (`&&`/`<@`/`@>`) and does **not** validate or
reorder — left untouched to avoid disturbing that working code path.

## Wiring — the three entry points a box value can arrive through

1. **Column-assignment coercion** — `internal/executor/codec.go`'s
   `coerceTextLikeDatum`, the existing chokepoint every string-shaped value
   passes through when stored into a text-like column (already had
   `varchar(n)`/`char(n)`/`bit(n)`/`varbit(n)` arms from prior M0134
   slices). New `box` arm: validate via `parseBoxLiteral`, error `22P02`
   `"invalid input syntax for type box: %q"` on failure, else store
   `boxCanonicalText`'s output instead of the raw input.
2. **`box 'literal'` typed-string cast** — `internal/parser/select.go`'s
   `tryTypedLiteral` allowlist gains `"box"` (one line; the existing generic
   `next.Kind != TokenStringLit` fallback path already handles the simple
   `typename 'string'` shape, no special multi-token grammar needed for
   box). `internal/executor/expr.go`'s `evalTypedStringLit` gains a `case
   "box":` calling the same `parseBoxLiteral`/`boxCanonicalText` pair.
3. **`pg_input_is_valid('...', 'box')`** — new `case "box":` in the
   introspection-function switch (`internal/executor/expr.go`), calling
   `parseBoxLiteral` for the boolean answer only (no canonicalization —
   `pg_input_is_valid` never stores anything).

All three share the same two functions — a single source of truth for
"what is a valid box, and what does it normalize to."

## Verification

Live, throwaway cgroup-capped goopg + psql against the PG 18.3 oracle:
`scripts/pg-regress-runner.sh --verbose box` diff shrank 768→738 lines,
`^ERROR` mismatch count 62→45. The file's INSERT-validation block (all five
badly-formatted-input rejects) and the canonicalized `SELECT * FROM
BOX_TBL` output are now byte-identical to the oracle; `box '...'` typed
literals throughout the rest of the file no longer raise a syntax error
(though most of those queries still fail downstream on the missing
operators/`area()` named below).

Tests: `internal/executor/box_literal_test.go` —
`TestBoxLiteralParseValidation` (all 5 of PG's documented "badly formatted"
rejects, plus corner-reordering across 5 well-formed shapes including the
crossed-corners case), `TestBoxColumnCoercionCanonicalizes` (INSERT/SELECT
round-trip through a real `box` column, plus the 22P02 reject path).

## Deferred (see `.ralph/deferral_ledger.md`, M0134-0094 row, for full detail)

(A) `area(box)` unregistered despite a `pg_proc` name-table entry.
(B) box comparison operators (`<`,`<=`,`=`,`>=`,`>`) are semantically WRONG
    — they fall through `compareDatum`'s generic lexicographic string
    compare instead of PG's area-based `box_lt`/`box_le`/etc.
(C) the `&<`/`&>` operator tokens aren't even lexable; `<<`/`>>` lex but
    have no box arm; `<<|`/`&<|`/`|&>`/`|>>`/`~=`/`<->` are all missing.
(D) SP-GiST/GiST index support for box columns is entirely absent.
(E) `pg_input_error_info('...', 'box')` — pre-existing cross-cutting stub,
    not box-specific.
(F) box coercion/typed-literal errors don't set `Pos` (missing `LINE N`/`^`
    pointer) — the same `Pos==0` sentinel-collision pattern already
    ledgered for M0134-0033/-0070/-0091.
