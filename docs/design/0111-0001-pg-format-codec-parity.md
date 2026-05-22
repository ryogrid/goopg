# 0111-0001 — PG-Format Codec Parity (varlena decode, type coercion, TOAST)

## Status
Draft (2026-05-22)

## Context

M0106-0010 (batched-36) switched goopg's heap-tuple storage from a
private fixed-format encoding to PG-native physical format
(`EncodeRowPG` / `DecodeRowIntoMctxPGTuple`) so that a PG18 standby
attaching via `pg_basebackup` can read goopg's data pages through
WAL FPIs.

Three bugs in the PG-format codec path are blocking correctness and
benchmarking:

1. **DecodePhysicalPGRow: truncated varlena** — under concurrent UPDATE
   load, `decodePhysicalPGValueMctx` reads a varlena length prefix that
   exceeds the remaining tuple body bytes, causing `"filler: truncated
   varlena"`.  This aborts pgbench clients (TPC-B command 6 is an UPDATE
   on `pgbench_accounts`) and makes UPDATE-based benchmark numbers
   incomplete.

2. **encodeValuePG string→numeric coercion missing** (partially fixed
   2026-05-22) — the `encodeValuePG` path for `int2/int4/int8/oid/float4/float8`
   only accepted `KindInt` before the partial fix.  The `KindString` case
   was added for integer types but `float4`/`float8` PG-format encoding
   still loses rows (detoast or float-format mismatch suspected).

3. **TOAST store-and-retrieve broken** — `ToastLargeColumnsIfNeeded`
   successfully writes TOAST chunks to the auxiliary relation but the
   main tuple's TOAST pointer either points to chunks that are invisible
   (xmin mismatch) or that `DetoastValue` cannot find (`NBlocks == 0` for
   fresh TOAST relation).  `rows_affected = 1` is reported but the row
   is not visible to subsequent scans.  Values below `ToastThreshold`
   (2000 bytes) are unaffected.  This breaks `test_setup` table
   population (~40 regress tests) and the `delete` regress test.

### Impact

| Bug | Regress tests affected | Benchmark impact |
|-----|----------------------|-----------------|
| truncated varlena | ~30 UPDATE/INSERT tests | STANDARD/SIMPLE-UPDATE incomplete |
| encodeValuePG float coercion | ~5 (float4, float8) | pgbench_accounts UPDATE abalance |
| TOAST silent loss | ~40 (empty shared tables) + delete (5 diffs) | Data integrity |

## Design

### Part A: Fix varlena decode truncation

The PG-format varlena header is a 4-byte big-endian length that
includes itself (i.e. length >= 4 for an empty value).  Goopg's
`decodePhysicalPGVarlena` may misinterpret the length or the caller
(`DecodeRowIntoMctxPGTuple`) may pass incorrect offsets when
null-bitmap and fixed-width columns shift the varlena start position.

Investigation steps:
1. Add assertion logging around `decodePhysicalPGVarlena` to capture
   the raw data at the point of failure.
2. Compare the encoded tuple layout from `EncodeRowPG` against PG's
   `heap_fill_tuple` output for the same column types.
3. Fix the offset calculation, null-bitmap width, or varlena length
   decoding.

### Part B: Complete encodeValuePG type coercion

The goopg-format `encodeValue` already handles `KindString → float4/float8`
coercion (via `parseFloat / strconv.ParseFloat`).  Port this logic to
`encodeValuePG` for `float4` and `float8`.  Additionally, verify that
`KindNumeric` → float coercion and `KindInt` → float coercion work
correctly in both decode and encode paths.

The `float8` INSERT returning `rows_affected = 1` but `count(*) = 0`
suggests an additional decode-side issue: the 8-byte little-endian
IEEE 754 value written by `encodeValuePG` may not be correctly read
by `decodePhysicalPGValueMctx` (which expects a different format).

### Part C: Fix TOAST write/read round-trip

`toastStore` writes chunks via `writeHeapTupleToRel` which calls
`ctx.Pool.PinNew(toastRel)` to allocate pages.  The chunks are written
with `ctx.Tx.XID` as xmin.  After auto-commit, the main tuple is
visible but `DetoastValue` calling `ctx.Pool.NBlocks(toastRel)` returns 0
because the TOAST relation's `smgr` metadata was not properly updated
by `PinNew` → `Extend`.

Fix: ensure `manager.Extend` increments the in-memory `nBlocks` for
the TOAST relation so that `NBlocks` returns the correct count on the
next read.  Alternatively, scan the buffer pool's dirty pages for the
TOAST relation instead of relying solely on `NBlocks`.

## Verification

- `go test -race ./internal/executor/ ./internal/storage/ ./internal/server/` PASS
- pgbench STANDARD c=10 completes without client aborts
- pgbench SIMPLE-UPDATE c=10 completes without client aborts
- TOAST round-trip: INSERT >2000-byte value, SELECT returns the row
- `TestPort_RegressSuite/delete` → diff count drops from 5
- `TestPort_RegressSuite/float4` → diff count drops from 739
- `TestPort_RegressSuite/float8` → diff count drops from 1246
- Existing 16 PASS isolation tests unchanged
