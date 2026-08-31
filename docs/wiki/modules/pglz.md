# Module: `internal/access/common/pglz`

PostgreSQL's **PGLZ (Lempel-Ziv) compression** — a faithful port of
`src/common/pg_lzcompress.c` / `src/include/varatt.h`, used for
inline-compressed varlena values (`VARATT_IS_4B_C`). This is the codec that
keeps goopg's compressed catalog blobs (e.g. bootstrap `pg_rewrite.ev_action`
`pg_node_tree`) readable by a real PG 18.3, and that lets goopg decompress
inline-compressed varlena values a real PG wrote (heap/catalog reads, logical
replication decode).

## Key Files

- `pglz.go` (220) — the whole package: `Compress`, `Decompress`,
  `BuildCompressedVarlena`, `ParseCompressedVarlena`, and the PGLZ token-stream
  constants.

## Public API

```go
func Compress(data []byte) []byte                 // raw PGLZ token stream (no varlena header)
func Decompress(data []byte) ([]byte, error)      // raw stream -> original bytes
func BuildCompressedVarlena(raw []byte) []byte    // full on-disk VARATT_IS_4B_C value
func ParseCompressedVarlena(b []byte) ([]byte, error)
```

## Internal structure

- **Format** — a compressed varlena is
  `[4B va_header=(totalSize<<2)|0x02] [4B va_tcinfo=rawSize|(method<<30)]`
  followed by the PGLZ token stream. `va_tcinfo`'s low 30 bits hold the
  original (decompressed) size; the top 2 bits hold the `ToastCompressionId`
  (PGLZ=0, LZ4=1 — only PGLZ is implemented).
- **Compressor** — a greedy longest-match encoder over a 4095-byte window
  (`Compress`). PG uses a hash-chain matcher with `good_match` heuristics, so
  the byte output may differ, but any valid PGLZ token stream round-trips
  through either decompressor.
- **Token format** — a match is a back-reference: a 4-bit length nibble
  (`minMatchLen=3`, saturating at `0x0f` → base 18, with an optional extension
  byte adding up to 255 more → `maxMatchLen=273`), a 12-bit offset
  (`maxOffset=4095`), and literal bytes emitted directly when no match ≥ 3
  exists.

## Dependencies

- **Used by** — `internal/executor` (TOAST / catalog blob writes),
  `internal/initdb` (bootstrap `ev_action` compression), `internal/catalog`
  (heap varlena reads).
- **Uses** — nothing inside `internal/` (leaf package); only the standard
  library (`encoding/binary`, `fmt`).

## Notable patterns / gotchas

- **`maxMatchLen` off-by-one** — the length nibble saturates at `0x0f` giving a
  base of 18, and the extension byte adds up to 255 more (so the max encodable
  run is 273, not 272); compressing a run longer than this requires emitting
  multiple matches. Decompress must accept exactly the lengths `Compress` can
  produce.
- **Window** — `maxOffset = 4095` means matches can only reference the last
  4095 bytes; a match that would reach further back must be split or emitted as
  literals.
- **Round-trip by contract** — goopg's encoder need not byte-match PG's, but
  its stream must be decodable by PG's `pglz_decompress`, and goopg's
  `Decompress` must decode any PG stream. Do not "optimize" the format.