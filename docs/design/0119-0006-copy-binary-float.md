# Binary `COPY` of `float4` / `float8` (M0119-0006, 53rd slice)

Status: accepted · 2026-08-13 · milestone M0119-0006

## The gap

`copy_binary.go` dispatches on the column's declared type name in two siblings,
`datumToCopyBinary` and `copyBinaryToDatum`. Neither had a `float4` or `float8`
arm, so every float column fell through to the raw-bytes default.

That default is not harmless here. goopg has **no float `Datum` Kind**: the heap
decoder renders a stored IEEE-754 image through `PGFloatOut` (shortest
round-trip text) and parses it back with `floatTextDatum`, so a float value
travels the engine as `KindNumeric` (or `KindString` for `NaN`/`Infinity`). The
default's `KindString` case therefore shipped the **text** of the value under a
format declared *binary*:

| value | goopg payload (HEAD) | upstream `float4send`/`float8send` |
|---|---|---|
| `0` (float4) | 1 byte, `"0"` | 4 bytes, `00 00 00 00` |
| `2.5` (real) | 3 bytes, `"2.5"` | 4 bytes, `40 20 00 00` |
| `+Infinity` (float8) | 8 bytes, `"Infinity"` | 8 bytes, `7f f0 …` |

The `+Infinity` row is the sharpest illustration: the eight ASCII bytes of the
word "Infinity" happen to be a *valid-length* float8 field, so a real PG client
does not even reject the stream — it reads the value as `5.42e+45`. A
`COPY … (FORMAT binary)` of a float column produced silent corruption, not an
error. Coming back in, the decode default handed the raw bytes to
`NewStringDatum`, so a float column held four or eight bytes of binary garbage
as a string.

## Upstream

`float4send` (`postgres/src/backend/utils/adt/float.c:350`) is `pq_sendfloat4`,
which reinterprets the IEEE-754 image as a `uint32` through a union and sends it
big-endian (`postgres/src/backend/libpq/pqformat.c:252`); `float8send`
(`float.c:567`) is the 8-byte equivalent. `float4recv`/`float8recv`
(`float.c:339,556`) are `pq_getmsgfloat4`/`8`. There is no text escape hatch and
no special case for `NaN`/`Infinity` — the bits are the bits.

## What landed

**Both COPY arms, in both directions.** 4/8 big-endian IEEE-754 bytes; the
decode arms enforce the exact length locally (upstream gets that from the binary
COPY parser's per-attribute `pq_getmsgend`, not from the recv function) and
rebuild the Datum through `floatTextDatum(PGFloatOut(…))` — the *same*
expression the heap decoder uses — so a value that enters through binary COPY is
indistinguishable from the same value entering through `INSERT` in `Format()`,
CAST-to-text and every type-agnostic path.

**A shared coercion helper, `pgFloatFromDatum(d, bits)`.** The heap `float4` and
`float8` encode arms each carried an inline `KindInt`/`KindString`/`KindNumeric`
switch; rather than write a third and a fourth copy in `copy_binary.go`, the
switch was extracted and both heap arms now call it. The heap image and the
COPY payload are twins that differ only in byte order (Hard-won Rule #2), and
sharing the coercion is what makes that structural rather than aspirational.
`bits` selects both the `ParseFloat` width (hence float4's rounding) and the
type name PG uses in its own 22P02 text.

Two findings surfaced *while verifying* and are fixed here, because both are
encode/decode disagreements in the very path this slice is pinning:

**1. The declared name `float` was ENCODE-only in the heap codec.** PG's `float`
is `float8` (`gram.y` `opt_float`), and goopg stores a column's declared type
name verbatim (the 52nd slice's `serial` finding). `encodeValuePG` listed
`float` among the float8 spellings; `decodePhysicalPGValueMctx` did not. A
column declared `float` therefore wrote 8 fixed bytes and then **failed to read
them back** — the decoder fell to the varlena default and raised
`decode "float" as varlena: truncated 4-byte varlena` (measured with a throwaway
probe). One spelling added to the decode arm; the new COPY arms accept the same
six spellings as the heap encoder, and the round-trip test now covers each one.

**2. `NaN` was byte-divergent from PG.** `strconv.ParseFloat("NaN", 64)` yields
Go's NaN, whose low payload bit is **set** (`0x7ff8000000000001`); PG's
`get_float8_nan()` yields the canonical quiet NaN `0x7ff8000000000000`. The two
compare equal as `float64` and pass any `math.IsNaN` assertion, but they are not
equal as *bytes* — and both goopg float paths are byte-visible to real PG: the
heap image (a PG standby reading goopg's pages) and the `float8send` payload.
The E2E byte-compare below caught it: goopg's binary COPY of a float8 `'NaN'`
differed from PG 18.3's stream in exactly that one bit, at file offset 41.
Canonicalised in `pgFloatFromDatum`, so the fix covers the heap and COPY
together. float4 was already identical — the float32 narrowing discards the
payload, giving PG's `0x7fc00000` either way — and a test pins that too.

## Verification

Six fail-when-broken tests in `copy_binary_float_test.go`, each verified red at
HEAD by restoring the pre-change files (`float4_send(0)` shipped 1 byte;
`float8_send(+Inf)` decoded as `5.42e+45`; the round-trip Datum came back
`kind 3` where the heap gives `kind 7`). `TestCopyBinaryFloatAgreesWithHeapEncode`
is the sibling pin: the COPY wire bits must equal the heap bits, differing only
in endianness. `TestCopyBinaryFloatRoundTripMatchesHeapDatum` compares the COPY
decode against `decodePhysicalPGValueMctx`'s own output for all six spellings.

Oracle E2E on a capped throwaway server (5533) against PG 18.3 on 65432, over a
table carrying all six spellings (`float4, float8, real, double precision,
float`) and the values `0 / 1.5 / -2.25 / 3.5 / 12345.75 / 1e10 / -1e-10 / 1e300
/ NULL`, plus a second table of `NaN / Infinity / -Infinity` and both type
maxima:

- `COPY … TO … (FORMAT binary)` — **byte-identical** to PG's stream in both
  tables (the NaN table only after fix 2 above).
- Cross-load both ways: PG's binary file read by goopg's `COPY … FROM`, and
  goopg's read by PG's — identical rendered rows on both engines.

Gates: `go test ./internal/executor/` PASS, `RALPH_PRECOMMIT_SCOPE=units` PASS,
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), pgbench smoke via the commit
hook.

## Deferred

The binary-COPY arms still missing, unchanged from the 52nd slice's row minus
`float4`/`float8`: `oid`, `uuid`, `interval` (16-byte {micros, days, months} —
the heap codec already builds it), `jsonb` (leading version byte), `bpchar`.
The serial-alias storage finding (`smallserial`/`serial2`/`serial4`/`serial8`
stored as varlena TEXT) is unchanged and still needs its own slice. Ledger rows
carry both.
