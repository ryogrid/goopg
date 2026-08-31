# 0111-0005 — int2 Storage-Encode Out-of-Range Message Parity

## Status
Accepted (2026-05-24)

## Context

M0106-0010 switched goopg's heap-tuple storage to PG-native physical format,
introducing `encodeValuePG` (`internal/executor/codec.go`) as the single
storage-encode path. Each per-type arm validates range and emits the matching
PostgreSQL error.

PostgreSQL distinguishes two out-of-range error shapes for `smallint`:

- **`int2in` (the type input function)** — when a *string literal* is coerced
  into an int2 column, e.g. `INSERT INTO INT2_TBL(f1) VALUES ('100000')`:
  `ERROR: value "100000" is out of range for type smallint` (SQLSTATE 22003).
- **`int2pl` / `int2mul` (arithmetic operators)** — when an int2 expression
  overflows, e.g. `32767::int2 * 2`: `ERROR: smallint out of range` (22003).

goopg already raises the arithmetic wording from the expression evaluator
(`internal/executor/expr.go`, `exprnode.go`), which runs *before* the
storage-encode path. So `encodeValuePG`'s int2 arm is only reached by the
input-function (string-literal coercion) case and must use the `int2in`
wording.

## Problem

The int2 arm emitted a bare `fmt.Errorf("smallint out of range")` — the
arithmetic wording, and not even an `*ExecError` with a SQLSTATE. The sibling
int4 arm directly below it already did the right thing
(`value %q is out of range for type integer` as a 22003 `*ExecError`). The two
arms had drifted.

This surfaced as the residual diff in the `int2` regress case: the
`INSERT … VALUES ('100000')` line reported `smallint out of range` instead of
`value "100000" is out of range for type smallint`, leaving the case
`failed` at 4 normalized diff lines after the M0097-0037 fast-path overflow fix
took it from 44 → 4.

## Change

`encodeValuePG` int2/smallint arm now mirrors the int4 sibling:

```go
if v < -32768 || v > 32767 {
    return nil, &ExecError{Code: "22003",
        Message: fmt.Sprintf("value %q is out of range for type smallint",
            strings.TrimSpace(d.StringValue()))}
}
```

For the string-literal coercion path (`KindString`), `d.StringValue()` returns
the original text (`"100000"`), exactly matching `int2in`. Arithmetic overflow
is unaffected because it never reaches this arm.

## Result

- `int2` regress case: `failed` (4 diff lines) → **pass**.
- No change to arithmetic-overflow wording (`smallint out of range`), still
  asserted by `internal/executor/phase_c_test.go` (`int2_*_overflow` cases).

## Tests

`internal/executor/codec_int8_name_pg_test.go`:
`TestEncodeValuePGInt2OutOfRangeMessage` pins the input-function wording and
SQLSTATE for both `int2` and `smallint`, and that in-range string literals
still encode cleanly.

## Lesson

Sibling per-type arms in `encodeValuePG` must keep parity not just in storage
layout but in error wording — PostgreSQL's input-function vs. operator error
text is observable and regress-tested. When adding/editing one numeric arm,
diff it against its neighbours.
