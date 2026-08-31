# 0046-0007 — PGLZ varlena compression (encode + decode, PG-faithful)

Status: accepted (2026-07-12)

## Problem

goopg stored inline-compressed varlena values (`VARATT_IS_4B_C`) only via a
bootstrap-time compressor in `internal/initdb/pglz.go`, used for large
`pg_rewrite.ev_qual`/`ev_action` `pg_node_tree` blobs. That compressor was
**not** compatible with PostgreSQL's PGLZ wire format, and there was **no
decompressor** anywhere in the tree. Three concrete defects:

1. **Control-bit polarity inverted.** goopg emitted control bit `1` for a
   *literal* and `0` for a *match*; PostgreSQL's `pglz_decompress`
   (`src/common/pg_lzcompress.c`) uses bit `1` for a *match* tag and `0` for a
   literal.
2. **Match-tag nibble layout swapped.** goopg put `(len-3)` in the high nibble
   of the first tag byte and the offset high bits in the low nibble; PG uses
   the low nibble for `(len-3)` and the high nibble for the offset high bits
   (`off = ((b0 & 0xf0) << 4) | b1`, `len = (b0 & 0x0f) + 3`). goopg also
   capped matches at length 18 and never emitted the length **extension byte**
   PG uses for matches of 18..273 bytes.
3. **`va_tcinfo` layout wrong.** goopg wrote `(rawSize << 2) | method`; PG18
   (`src/include/varatt.h`) packs the original size in the **low 30 bits** and
   the `ToastCompressionId` in the **top 2 bits**
   (`VARLENA_EXTSIZE_BITS = 30`).

Consequences: a real PostgreSQL attached as a standby could not decompress
goopg's bootstrap `ev_action` blobs, and both varlena decode paths
(`internal/executor/codec.go:decodePhysicalPGVarlena` and
`internal/wal/pgoutput.go:pgoDecodePhysicalVarlena`) hard-errored with
`"compressed varlena not supported"` — so logical-replication decode or a
catalog read of any inline-compressed value failed.

## Change

New leaf package `internal/pglz` (stdlib-only, so `initdb`, `executor`, and
`wal` can all import it without a cycle) is a faithful port of the PGLZ wire
format:

- `Compress([]byte) []byte` — greedy longest-match over a 4095-byte window,
  emitting PG-format tokens (match = control bit set, low-nibble length with
  extension byte for len ≥ 18, 12-bit offset split high-nibble/low-byte). A
  greedy encoder may choose different matches than PG's hash-chain matcher, but
  the output is a valid PGLZ stream either decompressor accepts.
- `Decompress(src []byte, rawSize int) ([]byte, error)` — mirrors
  `pglz_decompress`, including the overlapping run-length copy for `off < len`
  and the corrupt-stream guards (`off == 0`, `off > produced`, truncation).
- `BuildCompressedVarlena` / `DecodeInlineCompressed` — wrap/parse the full
  on-disk value with the correct PG18 `va_header` (`(total<<2)|0x02`) and
  `va_tcinfo` (`rawsize | method<<30`); only PGLZ (method 0) is supported.

Wiring:

- `internal/initdb/pglz.go` `pglzVarlenaDatum` now delegates to
  `pglz.Compress` + `pglz.BuildCompressedVarlena` (correct `va_tcinfo`).
- The two decode siblings replace their `"compressed varlena not supported"`
  error with `pglz.DecodeInlineCompressed(data)`. These are twins that must
  agree (`pattern_sibling_paths_must_agree`); both are covered.

## Tests

- `internal/pglz/pglz_test.go`: round-trip over literal/RLE/long-match/mixed
  inputs; a **hand-authored** PG-spec token stream (independent of this
  package's own encoder, so a shared encoder/decoder bug can't hide) covering
  literals, LSB-first control byte, 2-byte match tag, extension byte, and
  overlapping copy; 12-bit offset reconstruction; corrupt-stream rejection;
  full varlena framing + PG18 bit-layout assertions; LZ4-method rejection.
- `internal/executor/codec_compressed_test.go` and
  `internal/wal/pgoutput_compressed_test.go`: each decode sibling now
  transparently decompresses a `BuildCompressedVarlena` blob.
- `internal/initdb/pg_rewrite_bootstrap_test.go` and
  `internal/initdb/btree_search_test.go`: `va_tcinfo` assertions updated to the
  PG18 low-30-bits / top-2-bits layout (both previously encoded the buggy
  `>> 2` layout).

Gates: `go build ./...`, `go vet` (pglz/initdb/wal) clean; full `internal/pglz`,
`internal/initdb`, `internal/wal`, `internal/executor` package tests pass;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).

## Deferred (see deferral ledger 2026-07-12)

- **User-data TOAST compression is still not performed.** goopg stores user
  varlena values uncompressed regardless of `attcompression` (unimplemented_feat
  "TOAST compression functionality is not implemented"). This change makes
  goopg able to *read* compressed values and produce PG-faithful compressed
  bootstrap blobs, but the write path for user tables is unchanged.
- **LZ4 compression** (`ToastCompressionId` 1) is rejected, not implemented.
- **External on-disk TOAST pointers** in logical-replication changes remain
  unsupported (separate `0x1B` / `VARATT_IS_1B_E` path, unchanged).
