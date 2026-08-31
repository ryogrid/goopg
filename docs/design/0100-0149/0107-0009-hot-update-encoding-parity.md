# 0107-0009 — HOT Update Encoding Parity Fix

**Status**: accepted  
**Milestone**: M0107 — Performance Optimization Refactor  
**Filed**: 2026-05-21

## Problem

`tryApplyHOTUpdate` always used `EncodeRow` (goopg big-endian format)
regardless of `ctx.LogCanonical`.  `writeHeapRowReturning` used `EncodeRowPG`
(PG native little-endian format) when `ctx.LogCanonical != nil`.  This mixed
encoding caused the dual-format decoder (`decodeRowIntoMctx` → tries goopg
first, then PG) to occasionally "succeed" on PG-encoded rows with wrong values.

### Root cause

`decodeGoopgRowIntoMctx` had a trailing-bytes check only for `off < len(data)`
(trailing bytes remain).  It did NOT check `off > len(data)` (over-read).

Under the goopg decoder's loop guard (`if off >= len(data) → NullDatum, no
off advance`), `off` can end up > `len(data)` after the loop if the guard fires
on a column that was reached after `off` had already consumed flag+value bytes
of the PRECEDING column incorrectly.  In that state:

- `off > len(data)` → post-loop `off < len` check is FALSE → no error!
- Decoder "succeeds" with wrong column values (intermediate columns misread,
  final column(s) set to NullDatum)

This caused silent data corruption in the `updateViaIndex` path:

1. Non-HOT INSERT stores row in PG format, `natts=len(cols)` in Infomask2.
2. Subsequent HOT UPDATE reads the row via `DecodeRowIntoMctxPGTuple`
   (fast path → `decodeRowIntoMctx`).
3. goopg decoder accidentally "succeeds" on PG data → wrong old-row values.
4. HOT UPDATE computes `newRow` from wrong old values → corrupted new row.
5. The corrupted row is stored; subsequent reads may see NULL filler columns
   or wrong counter values.

**Observed symptom (pgbench c=100 SU, M0107-0007 async drain)**: "truncated
4-byte varlena header" errors in server logs and incorrect pgbench results due
to lost or corrupted updates.

## Fix

### Fix A — `decodeGoopgRowIntoMctx` over-read guard

`internal/executor/codec.go`: changed the post-loop check from

```go
if off < len(data) {  // trailing bytes
    return error
}
```

to

```go
if off != len(data) {  // either trailing bytes OR over-read
    return error
}
```

The new `off > len(data)` path returns "overread by N bytes" so the caller
falls through to `decodePhysicalPGRowIntoMctx` which correctly decodes the
PG-encoded data.

### Fix B — HOT update PG format consistency

`internal/executor/operators_storage.go` `tryApplyHOTUpdate`: when
`ctx.LogCanonical != nil`, uses `EncodeRowPG` + `NullBitmapPG` + `SetNatts` +
`HeapXmaxInvalid` — identical to `writeHeapRowReturning`'s canonical-WAL path.
When `ctx.LogCanonical == nil`, behaviour is unchanged (goopg format).

This eliminates the encoding mismatch so the fast-path decoder never needs to
fall back to the over-read scenario.

## Tests

- `internal/executor/codec_pg_format_fallback_test.go`:
  - `TestDecodeRowGoopgOverreadDetected` — PG-encoded (int4, int4) data: goopg
    decoder rejects it; full `DecodeRow` succeeds with correct values.
  - `TestDecodeRowGoopgOffEqualLenIsValid` — valid goopg null+value encoding:
    decoder still succeeds (the `off == len` path is not broken).
- `internal/server/hot_update_encoding_test.go`:
  - `TestHOTUpdateEncodingConsistency` — 50 sequential updates on 20 rows;
    filler column must remain non-NULL and unchanged.
  - `TestHOTUpdateEncodingConsistencyConcurrent` — 10 concurrent workers × 30
    updates on 10 shared rows; filler must not become NULL.

## PG-compat impact

None.  The HOT tuple's `HeapOnlyTuple` flag is preserved.  The PG format body
is byte-compatible with the non-HOT path and with PG standby reads.  No WAL
format changes.
