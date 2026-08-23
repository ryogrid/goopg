# M0134-0098 — `circle.sql`: `circle_in`-faithful validation + canonicalization

## Status: PARKED (case still `failed`)

## Summary

`postgres/src/test/regress/sql/circle.sql` sized live against the PG 18.3
oracle (`scripts/pg-regress-runner.sh --verbose circle`) at a 139-line diff,
0% parity. Investigation found `circle` was, like `box` before it
(M0134-0094), a **raw-varlena text pass-through** with zero validation:
`internal/executor/codec.go`'s decode/encode default branch stored and
returned whatever text was given, unchanged. Concretely:

- Every one of the file's five "bad values" INSERTs (negative radius, an
  unterminated literal, trailing garbage, a non-numeric string, a malformed
  nested pair) silently **succeeded** where PG raises `22P02`.
- `SELECT * FROM CIRCLE_TBL` echoed goopg's raw input spelling — including
  whitespace-padded and un-bracketed alternate spellings PG accepts and
  normalizes — instead of PG's canonical `<(x,y),r>` output.
- `center()`, `radius()`, and `diameter()` had `pg_proc` name-table entries
  (OIDs 1543/1470/1469) but no `evalFuncCall` dispatch arm at all —
  `function radius does not exist`.

This slice fixes all of the above, plus a **display-formatting bug** the fix
exposed: `radius()`/`diameter()`/`center()` resolved through
`evalFuncCall` but had no `exprType` (`internal/optimizer/planner.go`) arm,
so their SELECT-list column fell through to `"unknown"` — psql then rendered
the column left-justified/unpadded (`" 3"`) instead of right-justified
numeric (`"      3"`), a byte-for-byte divergence from the oracle even
though the runtime *value* was already correct. Same failure class as the
`pg_notification_queue_usage` gap fixed under M0134-0091.

The remainder of the file — `area()`, the point/circle `<->` distance
operator (not just an unregistered function: the operator token itself
isn't lexed), and the `LINE N`/`^` position echo on the INSERT-validation
errors (cross-cutting, shared with M0134-0094's box.sql) — is each an
independently-scoped subsystem; see the deferral ledger row. The case stays
`failed`/PARKED per the established M0134 pattern (cf. M0134-0089..0097).

## Fix — `parseCircleLiteral` / `circleCanonicalText`

`internal/executor/expr.go` gains `parseCircleLiteral`, reproducing
`circle_in`'s parse (`postgres/src/backend/utils/adt/geo_ops.c`) closely
enough to match every case `circle.sql` exercises, reusing box's
`pairDecode`/`singleDecode` helpers:

- Accepts the canonical `"<(x,y),r>"` form, an un-bracketed `"(x,y),r"` /
  doubled-paren `"((x,y),r)"` form, and the "quick entry" flat `"x,y,r"`
  form with no delimiters at all — all four spellings `circle.sql`'s
  `CIRCLE_TBL` fixture exercises, including heavily whitespace-padded
  variants (`" < ( 100 , 1 ) , 115 > "`).
- Rejects a negative radius (`22P02`) but **accepts NaN** — `radius < 0.0`
  is `false` under IEEE comparison when `radius` is NaN, exactly mirroring
  `circle_in`'s own comment ("We have to accept NaN").
- Requires the **entire** string to be consumed, matching `circle_in`'s
  `nil` `endptr_p` (trailing garbage like `"<(100,200),10> x"` is rejected).

This surfaced a `singleDecode` gap shared with `parseBoxLiteral`: it only
scanned a bare digit/exponent float syntax, so it neither recognized PG's
`NaN`/`Infinity`/`Inf` spellings (`float8in_internal`'s `strtod`-failure
fallback, `postgres/src/backend/utils/adt/float.c:395-511`) nor skipped
**trailing** whitespace after a parsed number (same function, "skip trailing
whitespace" — needed so `pairDecode`'s immediately-following `,`/`)`
delimiter check doesn't fail on inputs like `" 1 , 3 , 5 "`). Both are now
fixed directly in `singleDecode`, so `parseBoxLiteral` inherits the same
correctness improvement for free (box.sql's fixture happens not to exercise
either case, so this is a latent fix, not a regression risk).

`circleCanonicalText` mirrors `circle_out`/`pair_encode`+`single_encode`:
`"<(x,y),r>"`, each field via the existing `PGFloatOut`.

## Wiring — the three entry points a circle value can arrive through

Same three chokepoints as box (M0134-0094), each gaining a `circle` arm
that calls the same `parseCircleLiteral`/`circleCanonicalText` pair:
`coerceTextLikeDatum` (`internal/executor/codec.go`), the `circle
'literal'` typed-string cast (`tryTypedLiteral` allowlist in
`internal/parser/select.go` + `evalTypedStringLit` in `expr.go`), and
`pg_input_is_valid('...', 'circle')`.

## Fix — `center()`/`radius()`/`diameter()`

`internal/executor/expr.go`'s `evalFuncCall` gains three new cases (all
parse the argument via `parseCircleLiteral`): `center` returns a point
`"(x,y)"` string, `radius` and `diameter` return a float8-shaped datum via
the existing `floatTextDatum(PGFloatOut(...))` helper (the same one
`evalBinary`'s arithmetic-result path already uses) rather than a bare
`NewStringDatum`, so the value round-trips through numeric comparisons
(`WHERE radius(f1) < 5`) the same way `sqrt()`'s result already does.

`internal/optimizer/planner.go`'s `exprType` gains matching `"radius",
"diameter"` (→ `float8`) and `"center"` (→ `point`) cases — without these,
the SELECT-list column type falls through to `"unknown"` and psql renders
it left-justified/unpadded instead of right-justified numeric, even though
`evalFuncCall` already computes the correct value.

## Verification

Live, throwaway cgroup-capped goopg + psql against the PG 18.3 oracle:
`scripts/pg-regress-runner.sh --verbose circle` diff shrank 139→51 lines.
The INSERT-validation block (all five bad-value rejects, same error
message/SQLSTATE as the oracle), the canonicalized `SELECT * FROM
CIRCLE_TBL` output, `center()`/`radius()`/`diameter()` (including their
`WHERE radius(f1) < 5` / `WHERE diameter(f1) >= 10` predicate forms) are now
byte-identical to the oracle, including column alignment.

Tests: `internal/executor/circle_literal_test.go` —
`TestCircleLiteralParseValidation` (all of circle.sql's accepted spellings
incl. whitespace-padded and NaN, plus all 5 of PG's documented "bad values"),
`TestCircleColumnCoercionCanonicalizes` (INSERT/SELECT round-trip through a
real `circle` column, plus the 22P02 reject path).

## Deferred (see `.ralph/deferral_ledger.md`, M0134-0098 row, for full detail)

(A) `area(circle)` unregistered despite a `pg_proc` name-table entry — same
    gap as `area(box)` (M0134-0094).
(B) the point/circle `<->` distance operator: not lexable at all. `<-` isn't
    in the lexer's two-char operator table, so `<->` tokenizes as `<` then
    `->` (JSON arrow) instead of one 3-char operator token — a genuinely new
    lexer/parser/evaluator feature, not a dispatch gap.
(C) `pg_input_error_info('...', 'circle')` — pre-existing cross-cutting
    stub, not circle-specific.
(D) the `LINE N`/`^` position echo on the bad-value INSERT errors — the same
    gap ledgered for M0134-0094's box.sql (F) and M0134-0033/-0070/-0091.
