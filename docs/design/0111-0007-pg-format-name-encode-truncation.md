# 0111-0007 — PG-Format Name Encode Skips NAMEDATALEN-1 Truncation

- **Milestone:** M0111 — PG-Format Codec Parity
- **Status:** accepted
- **Date:** 2026-05-24
- **Author:** Ralph (autonomous loop)

## Problem

The `name` regress case — the **last** of the 6 cases regressed by the
M0106-0010 PG-format physical-tuple codec switch — failed with 77 normalized
diff lines. The dominant symptom was that `name` columns were **one byte too
wide**: a 64-character input round-tripped as 64 chars instead of being
truncated to PostgreSQL's NAMEDATALEN-1 = 63:

```
-- SELECT f1 FROM NAME_TBL WHERE f1 LIKE '%34567890%';
expected (63 chars):                                              actual (goopg, 64 chars):
                               f1                                                                f1
 -----------------------------------------------------------------    ------------------------------------------------------------------
  1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890ABCDEFGHIJKLMNOPQ       1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890ABCDEFGHIJKLMNOPQR
```

The off-by-one cascaded:

1. **Column width** — the header underline ran 64 dashes instead of 63.
2. **Stored value** — the trailing `R` (byte 64) survived instead of being clipped.
3. **Row counts** — because the stored values were not truncated, `WHERE f1 =
   '<63-char literal>'` matched a different set of rows than PostgreSQL, so
   result sets reported `(7 rows)` / `(6 rows)` where PG reported `(5 rows)` /
   `(4 rows)`, and an exact-match `WHERE` that PG satisfied returned `(0 rows)`.

## Root cause

`encodeValuePG` (`internal/executor/codec.go`) is the PG-native physical-tuple
encoder, and its decode counterpart `decodePhysicalPGValueMctx` became the
**primary** heap path in M0111-0001. Its `name` arm built the fixed 64-byte
`NameData` buffer and copied the full input string in with no length clip:

```go
case "name":
    // PG NameData: fixed 64 bytes, '\0' padded
    s := d.StringValue()
    buf := make([]byte, 64)
    copy(buf, s)
    return buf, nil
```

When `s` is exactly 64 bytes, `copy` fills all 64 bytes and leaves **no NUL
terminator**. The decoder (which scans for the first `\0` within the 64-byte
field, falling back to 64) then reads all 64 bytes back. PostgreSQL's
`namein()` instead truncates input to NAMEDATALEN-1 = 63 bytes, reserving byte
64 as the terminator.

The sibling **storage-encode** path (`encodeValueStorage`, same file) already
did this correctly:

```go
case "name":
    // The "name" type silently truncates input to NAMEDATALEN-1 = 63 bytes,
    // matching PostgreSQL ... M0097-0003.
    ...
    if len(s) > 63 {
        s = s[:63]
    }
    return encodeVarlen([]byte(s)), nil
```

The two encoders had drifted apart since the M0106-0010 codec switch made
`encodeValuePG` the live path — exactly the same drift class as [[0111-0004]]
(missing decode arms) and [[0111-0006]] (wrong decode Kind), but on the
**encode** side this time.

## Fix

Mirror the storage-encode truncation (and its Kind handling) in the
`encodeValuePG` `name` arm: clip the string to 63 bytes before copying into the
64-byte buffer.

```go
case "name":
    var s string
    switch d.Kind {
    case KindString:
        s = d.StringValue()
    case KindBytes:
        s = string(d.BytesValue())
    case KindInt:
        s = fmt.Sprintf("%d", d.Int)
    default:
        s = d.StringValue()
    }
    if len(s) > 63 {
        s = s[:63]
    }
    buf := make([]byte, 64)
    copy(buf, s)
    return buf, nil
```

The decoder is unchanged: with at most 63 payload bytes + a NUL terminator it
correctly recovers the 63-char value. (Truncation is byte-wise; for the ASCII
inputs exercised by the regress suite this equals character truncation. A
future multibyte-clip refinement, like PG's `pg_mbcliplen`, is out of scope for
v0 and matches the existing storage-encode behaviour.)

## Verification

- **`name` regress case → pass** (`failed`, 77 diff lines → 0).
- Unit test `TestEncodePhysicalPGNameTruncation`
  (`internal/executor/codec_int8_name_pg_test.go`) asserts a 64-char input
  round-trips as exactly 63 chars and that ≤63-char inputs are preserved
  verbatim.
- No previously-passing regress case regressed: `int2`, `int4`, `numerology`,
  `select_implicit`, `portals_p2`, `char`, `varchar` all re-verified `pass`.
- This recovers the **last** of the 6 M0106-codec regressions; the M0111 codec
  parity work is now complete for the regress baseline.

## Lesson

After a codec change, audit that `encodeValuePG` and `encodeValueStorage`
agree on **type-specific normalization** (length clipping, padding, error
wording), not only that round-trip bytes match for short values. A fixed-width
type whose normalization is skipped only diverges at its exact boundary length
(here 64 bytes), so it passes short-value round-trip tests and surfaces only
end-to-end. Combined with [[0111-0004]] and [[0111-0006]], all three drift
classes (missing decode arm, wrong decode Kind, missing encode normalization)
have now been found between the legacy and PG-native codecs.
