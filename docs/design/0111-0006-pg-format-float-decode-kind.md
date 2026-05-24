# 0111-0006 — PG-Format Float Decode Returns KindString (numeric sort regression)

- **Milestone:** M0111 — PG-Format Codec Parity
- **Status:** accepted
- **Date:** 2026-05-24
- **Author:** Ralph (autonomous loop)

## Problem

The `numerology` regress case (one of the 6 cases regressed by the M0106-0010
PG-format physical-tuple codec switch) failed with 60 normalized diff lines.
The dominant symptom was that **float8 columns sorted lexicographically rather
than numerically**:

```
-- SELECT f1 FROM TEMP_FLOAT ORDER BY f1;  (TEMP_FLOAT.f1 is FLOAT8)
expected (numeric):           actual (goopg, string-sorted):
 -2147483647                   -1234
     -123456                   -123456
      -32767                   -2147483647
       -1234                   -32767
           0                   0
        1234                   1234
       32767                   123456
      123456                   2147483647
  2147483647                   32767
```

`"-1234" < "-123456"` lexicographically, but `-1234 > -123456` numerically —
the rows were ordered as text.

## Root cause

`decodePhysicalPGValueMctx` (`internal/executor/codec.go`) is the PG-native
physical decoder, made the **primary** heap-read path in M0111-0001. goopg
stores `float4`/`float8` as varlena **text** for v0 (M0111-0002 switched float
encoding from binary IEEE 754 to text to match the legacy `encodeValue` path).

When M0111-0002 added the float types to the decoder, it lumped them into the
shared `text`/`varchar`/`bpchar` case, which returns a **`KindString`** Datum:

```go
case "text", "varchar", ..., "float4", "real", "float8", "double precision", "double":
    ...
    return NewStringDatum(string(payload)), n, nil   // ← float decoded as text
```

A `KindString` Datum compares byte-lexicographically. Both legacy decoders
(`decodeValue` and the arena decoder) had always parsed float text back into
**`KindNumeric`** (added in M0097-0003 specifically "for correct ORDER BY
numeric sort"), but that behavior was lost in the PG-native path that M0111-0001
promoted to primary. So the regression surfaced only after M0111-0001 changed
which decoder runs first.

## Fix

Split the float types out of the shared text case into a dedicated case in
`decodePhysicalPGValueMctx` that parses the varlena-text payload back to
`KindNumeric`, mirroring the legacy decoders exactly:

```go
case "float4", "real", "float8", "double precision", "double":
    payload, n, err := decodePhysicalPGVarlena(data)
    if err != nil { return Datum{}, 0, err }
    text := string(payload)
    if v, scale, ok := parseNumericFast(text); ok {
        return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
    }
    if m, s, perr := parseNumeric(text); perr == nil {
        return newNumeric(m, int(s)), n, nil
    }
    return NewStringDatum(text), n, nil   // NaN / Infinity fall back to text
```

The TOAST short-header (`0x1B`) detection stays in the text case; floats never
TOAST so they do not need it.

### Why display stays correct

Scientific-notation values like `1.2345678901234e+200` parse via `parseNumeric`
into a giant `KindNumeric` integer (scale 0). Output formatting is keyed on the
**column type** (`float8`), not the Datum kind: the float8 output formatter
converts the value to `float64` and prints with `%.15g`, reproducing
`1.2345678901234e+200`. NaN/Infinity, which `parseNumeric` rejects, fall back to
`KindString` and print verbatim. This matches the pre-M0111 behavior that made
`numerology` pass under M0097-0003.

## Impact

- `numerology` regress case: `failed` (60 diff lines) → **pass**.
- `float4`: 680 → 676 diff lines; `float8`: 1031 → 1027 (residual diffs are
  unrelated I/O-format issues, not sort order).
- No other previously-passing case regressed (`int2`, `int4`, etc. still pass).

## Lesson

This is the same class of bug as M0111-0004 (decode arm drift after the codec
switch): when a value type is moved between decoder cases, the **Datum Kind**
the case produces is part of its contract. A float decoded as `KindString` is
silently wrong only for ordering/comparison — it round-trips and displays fine,
so it escapes round-trip tests. After any codec change, audit that each type's
decode produces the same `Kind` the comparison/sort layer expects, not just the
same bytes.

## Tests

- `TestDecodePhysicalPGFloatKind` (`internal/executor/codec_int8_name_pg_test.go`):
  asserts float4/float8/real/double decode to `KindNumeric`, plus a
  `compareDatum(-1234, -123456) > 0` proof that ordering is numeric, not
  lexicographic.
- Regress gate: `TestPort_RegressSuite/numerology` → PASS;
  `int2`/`int4` still PASS (no regression).
