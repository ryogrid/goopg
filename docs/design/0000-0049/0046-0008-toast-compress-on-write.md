# 0046-0008 — TOAST compress-on-write (PGLZ out-of-line compression)

Status: accepted
Date: 2026-07-12
Milestone/source: M0122 / `unimplemented_feat.json` #151 (DU-002 slice 183);
builds directly on [0046-0007](0046-0007-pglz-varlena-compression.md) (the
`internal/pglz` leaf package) and [0046-0006](0046-0006-toast.md) (out-of-line
TOAST storage).

## Problem

goopg's out-of-line TOAST path ([0046-0006](0046-0006-toast.md)) chunked an
oversized varlena and stored it **raw**. Compression metadata round-tripped
through `pg_dump` for schema fidelity, but no value was ever actually
compressed — the `unimplemented_feat.json` #151 gap ("actual data compression
is not performed", confirmed-open at `operators_ddl.go:7335`).

Real PostgreSQL, before pushing an attribute out-of-line
(`heap_toast_insert_or_update` → `toast_tuple_try_compression` →
`toast_compress_datum`, `src/backend/access/heap/heaptoast.c` +
`heap/heaptuple.c`), first PGLZ-compresses the value (unless the column's
`STORAGE` is `EXTERNAL`), and keeps the compressed form when it is strictly
smaller than the raw bytes. The out-of-line value stored in the TOAST relation
is therefore the *compressed* varlena.

The previous loop ([0046-0007](0046-0007-pglz-varlena-compression.md)) landed
the PG-faithful `internal/pglz` compressor/decompressor precisely so this write
path could be closed.

## Approach

Compress in the out-of-line store path, keeping the on-disk pointer format
unchanged so no persistence-format or codec sibling is touched.

`internal/executor/toast.go`:

1. **`ToastLargeColumnsIfNeeded`** — for a value that exceeds `ToastThreshold`
   and whose column `Storage != "external"`, attempt
   `pglz.Compress(data)` and wrap the result with
   `pglz.BuildCompressedVarlena(comp, len(data))` (a full `VARATT_IS_4B_C`
   inline-compressed varlena that self-describes its raw size in `va_tcinfo`).
   Keep the compressed blob only when `len(blob) < len(data)` — mirroring
   `toast_compress_datum`'s `VARSIZE(tmp) < VARSIZE(value)` acceptance test.
   An incompressible value (or a `STORAGE EXTERNAL` column) is stored raw,
   exactly as before.

2. **`toastStore`** — gains a `compressed bool` parameter. The stored bytes are
   the compressed blob (or the raw value); `total_len`/`num_chunks` in the
   12-byte pointer describe the *physically stored* bytes as before. The
   **high bit of the `num_chunks` word** carries the compressed flag
   (`toastCompressedFlag = 1<<31`). This is free: `num_chunks` is bounded by
   `maxDetoastChunks` (`1<<20`) and never approaches `2^31`.

3. **`DetoastValue`** — reads the flag from the `num_chunks` word (masking it
   off before the chunk-count sanity checks), reassembles the chunks, and, when
   the flag is set, runs the reassembled blob through
   `pglz.DecodeInlineCompressed` to recover the raw value.

### Why the flag lives in the pointer, not in-band

The stored blob is a valid `VARATT_IS_4B_C` varlena, but a *raw* large `bytea`
value could coincidentally begin with bytes matching a compressed-varlena
header, so in-band detection is unsafe. An explicit flag bit is required. The
`num_chunks` high bit keeps the pointer at 12 bytes, so the 13-byte physical
encoding in `codec.go` (`encodeRowPG`/`decodePhysicalPGVarlena`) and its
`wal/pgoutput.go` decode sibling are untouched — and every existing 12-byte
on-disk pointer decodes exactly as before (backward compatible; the flag bit is
always 0 on legacy pointers → uncompressed path).

## Tests (`internal/executor/toast_test.go`)

- `TestToastCompressionRoundTrip` — a 40 KiB compressible value stores as a
  single chunk with the compressed flag set (vs 21 chunks raw) and detoasts to
  the exact original; end-to-end INSERT→SELECT round-trips a compressible `text`
  column (stored compressed) and an incompressible `bytea` column (stored raw).
- `TestToastStorageExternalSkipsCompression` — a `STORAGE EXTERNAL` column is
  stored out-of-line **uncompressed** (flag clear) and still round-trips.
- `TestToastChunkInsertsAreIndividuallyWALLogged` — reworked to use
  `incompressibleString(...)` so the per-chunk WAL-logging regression retains a
  genuinely multi-chunk value (a compressible value would now shrink to one
  chunk and defeat the test's intent).
- The pre-existing `TestToastRoundTripDoD` (1 MiB of `X`) now implicitly
  exercises the compressed path with full-fidelity assertions.

Non-vacuousness: the tests reference `toastStore`'s new `compressed` parameter
and `toastCompressedFlag`, so they fail to **compile** without the change.

## Deferred (see `.ralph/deferral_ledger.md`)

- **Inline-compression-stays-inline**: PG keeps a value inline (compressed) when
  compression brings the whole tuple under the toast target, only pushing
  out-of-line if still too big. goopg always pushes an over-threshold column
  out-of-line — a storage optimization, not a correctness gap.
- **`STORAGE PLAIN`/`MAIN` full semantics**: the toast *decision* still keys
  purely on `isToastableType` + length; only the compression decision honors
  `EXTERNAL`. `PLAIN` (never toast) and `MAIN` (prefer inline) nuances are
  unmodeled.
- **LZ4** (`default_toast_compression = lz4`) — `internal/pglz` is PGLZ only.
