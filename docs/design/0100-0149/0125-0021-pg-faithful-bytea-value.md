# M0125-0021 — a `bytea` value, not escaped text

*Landed 2026-07-29 on `tpcds-fix2`. Discovered by M0125-0019 (ledger row
2026-07-29) while checking whether `string_agg` over a bytea column produced
PG's answer — it could not, because goopg had no bytea value to aggregate.*

## The defect in one line

`'\xaabb'::bytea` was the six-character **string** `\xaabb`, not the two bytes
`0xAA 0xBB`, and `encode()` was a stub that returned `''` for every input.

## Why it mattered

Two of the three symptoms are loud (a wrong number, a wrong sort order). The
third is not, and it is the reason this was filed as a defect rather than a gap:

```sql
select encode(payload, 'hex') from t;   -- goopg: '' for every row
```

`encode` is *the* way a caller hex-dumps a bytea. Returning the empty string
rather than raising `42883 function does not exist` meant a hex dump that
silently produced nothing looked like a table of empty payloads. The
`unimplemented` signal was thrown away at exactly the site where it was needed.

Measured against the PG 18.3 oracle (port 65438) before the fix:

| expression | goopg | PG 18.3 |
|---|---|---|
| `length('\xaabb'::bytea)` | `6` | `2` |
| `encode('\xaabb'::bytea,'hex')` | `''` | `aabb` |
| `encode('abc'::bytea,'base64')` | `''` | `YWJj` |
| `encode('abc'::bytea,'escape')` | `''` | `abc` |
| `substring('\xaabbcc'::bytea from 2 for 1)` | `x` | `\xbb` |
| `'\xzz'::bytea` | `\xzz` | `ERROR 22023` |
| `INSERT … VALUES ('\xaabb')` then `length(b)` | `6` | `2` |
| `order by b` over `{'\xaabb','abc'}` | `\xaabb` first | `abc` first |

## Root cause

`evalCast` (`internal/executor/expr.go`) had **no `bytea` arm**. A cast to an
unhandled type name falls through to the pass-through default, so the datum
kept the Kind it already had — `KindString`, holding the literal's characters.
`KindBytes` existed and was produced correctly by `decode()` and by the storage
decoder, so goopg had a bytea *representation*; what it lacked was any path
from a literal into it.

The storage encoder had the same hole from the other side: `encodeValuePG`
(`internal/executor/codec.go`) had no `case "bytea"`, so a bytea column value
fell to the text arm and `varlenaTextBytes` stored whatever characters the
datum happened to carry.

## The fix

A new `internal/executor/bytea.go` holds transliterations of the upstream
primitives, each cited in-file:

| goopg | upstream |
|---|---|
| `byteaIn` | `byteain` (`utils/adt/varlena.c`) |
| `hexDecodePG` | `hex_decode_safe` (`utils/adt/encode.c`) |
| `escDecodePG` | `esc_decode` (encode.c) |
| `b64DecodePG` | `pg_base64_decode` (encode.c) |
| `hexEncodePG` / `escEncodePG` / `b64EncodePG` | `hex_encode` / `esc_encode` / `pg_base64_encode` |
| `byteaOutHex` | `byteaout`, `bytea_output = hex` |

Call sites, in sibling pairs (Hard-won Rule #2 — a green test on one twin proves
nothing about the other):

| site | sibling it must agree with |
|---|---|
| `evalCast` `case "bytea"` | `encodeValuePG` `case "bytea"` (both call `byteaIn`) |
| `evalCast` `case "text"` from `KindBytes` (`byteaOutHex`) | the wire renderer's `case "bytea"` in `internal/server/dispatch.go` |
| `decode(…,'hex')` | `'\x…'::bytea` (both call `hexDecodePG`) |
| executor result `Kind` | planner `exprType`'s advertised column type |

Three details are easy to get wrong and are pinned by tests:

- **`encode(…, 'escape')` is not `byteaout`'s escape mode.** `esc_encode`
  escapes NUL, high-bit bytes and the backslash and nothing else, so
  `encode('\x0a'::bytea,'escape')` is a raw newline. They are separate upstream
  functions.
- **`pg_base64_encode` wraps at 76 characters**, which `base64.StdEncoding`
  does not, and because the wrap test runs only after a complete four-character
  group a 57-byte payload ends *with* a trailing newline. The decoder therefore
  has to skip whitespace — Go's `base64.StdEncoding.DecodeString` rejects the
  newlines PG's own encoder emits.
- **Two distinct error families.** Hex diagnostics come from encode.c and are
  `22023`; the escape-format parser inside `byteain` raises `22P02`. They are
  not collapsed into one helper.

Three pre-existing deviations in `decode()` were removed at the same time: it
stripped a `\x` prefix before hex-decoding (PG errors —
`decode('\xaabb','hex')` is `invalid hexadecimal digit: "\"`), it passed an
invalid backslash sequence through in escape format instead of raising, and its
base64 arm rejected the newlines `encode()` emits.

### The coercion decision

goopg's `Datum` does not distinguish PG's *unknown* literal type from `text`.
Wherever a `KindString` meets a bytea context — the cast, the storage encoder,
`||`, and the comparator — it is routed through `byteaIn`, which is what PG's
operator resolution does to an unknown literal. This is also right for a genuine
`text` value, because PG's `text → bytea` cast is `byteain` too
(`'\xaa'::text::bytea` is the single byte `0xAA`, oracle-verified).

The comparator arm is load-bearing rather than cosmetic. Once a bytea *column*
holds two bytes, `where b = '\xaabb'` compares two bytes against the six
characters of the literal and matches nothing. Fixing storage without fixing
comparison would have converted a wrong number into a silently empty result set
— strictly worse. Where the string is not valid bytea input the comparator keeps
its old raw comparison rather than failing the query, matching the surrounding
block's stated fall-back convention.

### Advertised column type

The wire renderer in `internal/server/dispatch.go` switches on the **column
type**, not the datum Kind: `case "bytea"` emits `\x` + hex, anything else
appends the payload verbatim. A `KindBytes` datum advertised as `text` therefore
reaches psql as raw bytes and prints as invisible garbage. `exprType`
(`internal/planner/planner.go`) gained `decode` → bytea, `encode` → text,
`substr`/`substring` → bytea when the argument is bytea, and `||` → bytea when
either operand is. The executor Kind and the advertised type are asserted
together in one test for this reason.

## Verification

A 27-statement matrix was run through `psql` against a throwaway goopg server
(port 5533) and against the PG 18.3 oracle (port 65438) and diffed: **byte-
identical**. The same matrix lives as by-value subtests in
`internal/executor/bytea_value_test.go`, which additionally assert the payload
bytes, the datum Kind, the advertised column type, and both SQLSTATE families.

## Deliberately not done

See `.ralph/deferral_ledger.md` (2026-07-29, M0125-0021):

- **Integer → bytea casts.** PG 11 added `int2/int4/int8 → bytea` via
  `int4send`: `length(123::bytea)` is `4` (`\x0000007b`). goopg's new `bytea`
  cast arm raises `42846 cannot cast type integer to bytea`.
- **`bytea_output = escape`.** goopg has no such GUC; output is always hex.
- **The remaining bytea operators** — `position()`, `overlay()`, `trim()`,
  `get_byte`/`set_byte`, `bit_length` — still take the text path.
