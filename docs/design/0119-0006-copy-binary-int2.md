# 0119-0006 — Binary COPY of `int2`

**Milestone:** M0119-0006 (deferral-ledger backlog consumption), 52nd slice
**Status:** implemented (2026-08-13)
**Area:** `internal/executor/copy_binary.go`

## The gap

`COPY … WITH (FORMAT binary)` dispatches on the column's type name in two
sibling functions, `datumToCopyBinary` (send) and `copyBinaryToDatum` (recv).
Neither had an `int2`/`smallint` arm, so a smallint column fell through to the
raw-bytes default in both directions.

The default is not inert for this type — it is *actively wrong*, which is what
made `int2` the cheapest of the seven arms the 51st slice's ledger row listed:

- **Send.** The default's `KindInt` escape emits a fixed **8** big-endian bytes
  (`copy_binary.go`'s old default arm), where upstream `int2send` emits exactly
  **2**. A real PG client reading that stream fails the per-attribute
  `pq_getmsgend` in `CopyReadBinaryAttribute`
  (`postgres/src/backend/commands/copyfromparse.c`) with *"incorrect binary
  data format"*.
- **Recv.** The default hands the payload to `NewStringDatum`, so a smallint
  column silently ended up holding raw bytes as a *string* Datum — the
  encode↔decode twin defect of Hard-won Rule #2.

Neither half errored, so binary COPY of a smallint was never correct and never
complained.

## Upstream behaviour

`postgres/src/backend/utils/adt/int.c`:

| function | wire shape |
|---|---|
| `int2send` (`:98`) | `pq_begintypsend` + `pq_sendint16(&buf, arg1)` — 2 bytes BE |
| `int2recv` (`:87`) | `pq_getmsgint(buf, sizeof(int16))` — 2 bytes BE, sign-extended to the `int16` |

`int2recv` has no range check of its own: two bytes cannot be out of range for
an `int16`. The *length* check is not local either — the binary COPY parser
runs `pq_getmsgend` after each attribute, so a field that is not exactly two
bytes is an error upstream rather than a truncated value. goopg's decoder
raises on `len(payload) != 2` directly, which is the same observable behaviour
with the check in reach of the arm that needs it.

## What this slice does

Two arms, added as an encode/decode pair:

```
send:  binary.BigEndian.PutUint16(b, uint16(int16(d.Int)))   // 2 bytes
recv:  int16(binary.BigEndian.Uint16(payload))               // requires len == 2
```

**The send arm range-checks and raises 22003** rather than truncating to 16
bits. Upstream needs no such check because PostgreSQL cannot hold an
out-of-range `int2` Datum in the first place; goopg's `Datum.Int` is an
`int64`, so a value outside `[-32768, 32767]` reaching this encoder is
goopg-side corruption. 22003 with the offending value is the wording the heap
`int2` arm (`codec.go:229-257`) already uses, so the two encoders agree on the
failure as well as on the success.

Type-name spellings match the heap twin exactly — `"int2"` and `"smallint"`,
nothing more. See the deferral below for why the `serial` aliases were *not*
added here.

## Verification

- Six fail-when-broken tests in `internal/executor/copy_binary_int2_test.go`,
  each confirmed red against HEAD before the fix (8-byte payload, `kind 3`
  round-trip, 8-vs-2 heap width mismatch, no length check, no range check,
  spelled-out name). `TestCopyBinaryInt2AgreesWithHeapEncode` is the twin pin:
  the COPY wire value must equal the heap value, differing only in byte order
  (heap little-endian, COPY wire big-endian).
- Oracle E2E on a capped throwaway server (5533) against PG 18.3 on 65432, over
  `0`/`1`/`-1`/`32767`/`-32768`/`1234`/`-12345`/`100`/NULL in a three-column
  smallint table:
  - goopg's `COPY … TO STDOUT (FORMAT binary)` is **byte-identical** to PG's
    (79 bytes, `cmp` clean);
  - PG's own stream loads through goopg's `COPY FROM` with identical rendering;
  - goopg's stream loads into PG with identical rendering.

## Deferred (ledger rows filed)

1. **The `serial` alias spellings are broken far below binary COPY.** goopg
   stores a column's *declared* type name, and while `codec.go`'s heap arms
   list `serial` (int4) and `bigserial` (int8), they list neither
   `smallserial`/`serial2` nor `serial4`/`serial8`. Measured at HEAD: a
   `smallserial`, `serial2`, `serial4` or `serial8` column encodes to the
   varlena **TEXT** `[0x05 '1']` in the heap, where `smallint` gives `[1 0]`
   and `serial` gives `[1 0 0 0]` — four spellings stored as text. A live
   probe confirms the same column also ships text through binary COPY
   (`00 00 00 01 31`) even though `pg_typeof` reports `smallint`. Adding the
   aliases to the binary-COPY arm alone would have *broken* the twin agreement
   this slice just established, so the fix belongs in one slice that changes
   the heap codec and every dispatcher together — and it is a storage-format
   change for existing clusters.
2. **The remaining binary-COPY arms**, unchanged from the 51st slice's row
   minus `int2`: `float4`/`float8`, `oid`, `uuid`, `interval`, `jsonb` (leading
   version byte), `bpchar`.
