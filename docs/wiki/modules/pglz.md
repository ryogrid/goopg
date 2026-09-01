# Module: `internal/access/common/pglz`

PostgreSQL's **PGLZ (Lempel-Ziv) compression** — a faithful port of
`src/common/pg_lzcompress.c` / `src/include/varatt.h`, used for
inline-compressed varlena values (`VARATT_IS_4B_C`). This is the codec that
keeps goopg's compressed catalog blobs (e.g. bootstrap `pg_rewrite.ev_action`
`pg_node_tree`) readable by a real PG 18.3, and that lets goopg decompress
inline-compressed varlena values a real PG wrote (heap/catalog reads, logical
replication decode).

## Key Files

| File | LOC | Role |
|---|---|---|
| `pglz.go` | 220 | The whole codec: `Compress`, `Decompress`, `BuildCompressedVarlena`, `DecodeInlineCompressed`, and the PGLZ token-stream constants. |
| `pglz_test.go` | 181 | Round-trip tests spanning empty/single/short/long/repetitive/mixed inputs, PG-spec token-stream decode, high offset, corrupt-stream rejection, varlena framing, LZ4 rejection. |

## Public API

```go
func Compress(data []byte) []byte                    // raw PGLZ token stream (no varlena header)
func Decompress(src []byte, rawSize int) ([]byte, error)  // raw stream → original bytes
func BuildCompressedVarlena(compressed []byte, rawSize int) []byte  // full on-disk VARATT_IS_4B_C value
func DecodeInlineCompressed(data []byte) ([]byte, int, error)      // parse + decompress inline-compressed varlena
```

## Internal structure

```mermaid
flowchart TD
    subgraph Compress
        IN[raw bytes]
        LOOP[for i < len(data)]
        CTRL[emit control-byte placeholder]
        BIT[for 8 bits per control byte]
        SEARCH[scan history window [i-maxOff, i-1]<br/>for longest match ≥ 3 bytes]
        MATCH{bestLen ≥ minMatchLen?}
        MATCH -- yes --> TAG[emit match tag: 2 bytes<br/>(len nibble + off high nibble + off low byte)<br/>+ optional extension byte]
        MATCH -- no --> LIT[emit literal byte]
        TAG --> ADV[i += bestLen]
        LIT --> ADV[i++]
        ADV --> BIT
        BIT --> CTRL2[fill control byte<br/>(LSB-first match/literal bits)]
        CTRL2 --> LOOP
        LOOP --> OUT[token stream]
    end

    subgraph Decompress
        SRC[token stream]
        RAW[rawSize known upfront]
        CTRL_READ[read control byte]
        BIT_READ[for 8 bits]
        TST{ctrl bit == 1?}
        TST -- match --> DECODE_TAG[read 2 bytes + optional ext byte<br/>length = nibble+3, off = hi<<4|lo]
        TST -- literal --> DECODE_LIT[copy one byte verbatim]
        DECODE_TAG --> COPY[byte-by-byte copy<br/>start = len(dst)-off<br/>for k in 0..length:<br/>dst = append(dst, dst[start+k])]
        DECODE_LIT --> COPY
        COPY --> BIT_READ
        BIT_READ --> CTRL_READ
        CTRL_READ --> DST_CHECK{len(dst) == rawSize?}
        DST_CHECK -- yes --> DST[dst bytes]
    end
```

### Format

A compressed varlena is:

```
[4 B va_header  = (totalSize << 2) | 0x02]    VARATT_IS_4B_C
[4 B va_tcinfo  = rawSize | (method << 30)]
[compressed PGLZ token stream]
```

- `va_header` low 2 bits = `0b10` (the `VARATT_IS_4B_C` tag); the high 30 bits
  = total byte count of the entire compressed datum (header + token stream).
- `va_tcinfo` low 30 bits = original (decompressed) size;
  top 2 bits = `ToastCompressionId` (PGLZ = 0, LZ4 = 1 — only PGLZ is
  implemented).
- `BuildCompressedVarlena` produces the full 8+-byte on-disk value:
  `buf[0:4] = varatt header`, `buf[4:8] = va_tcinfo`, `buf[8:] = token stream`.
- `DecodeInlineCompressed` parses the header, validates `method == PGLZ`,
  validates `total <= len(data)`, calls `Decompress(data[8:total], rawSize)`,
  and returns the decompressed payload plus the total bytes consumed.

### Compressor

A **greedy longest-match encoder** over a 4095-byte window. For each position,
it scans the history window `[i-maxOff, i-1]` for the longest match of at least
`minMatchLen = 3` bytes up to `maxMatchLen = 273` (the saturating length
encoding limit). PG uses a hash-chain matcher with `good_match` heuristics, so
the byte output may differ, but any valid PGLZ token stream round-trips through
either decompressor.

Output is a stream of 8-bit-boundary **control bytes** followed by data byte
groups. Each control byte encodes 8 output events, LSB first:

- **Literal** (control bit = 0): one literal byte is emitted verbatim.
- **Match / back-reference** (control bit = 1): a 2-byte tag (plus optional
  extension byte) encoding `(length, offset)` for a run copied from the
  history window.

### Token format

A match tag is 2 bytes, with an optional third extension byte:

```
  b0: [off_hi:4][len:4]
  b1: [off_lo:8]
  b2: [ext_byte]   -- only when len nibble == 0x0f
```

- **Length**: 4-bit nibble in the low part of b0. `lenCode = matchLen - 3`;
  values `0..0x0e` encode lengths 3..17 directly. `0x0f` is a sentinel: base
  length = 18, and an extension byte (b2) adds 0..255 more, so maximum
  encodable length = 18 + 255 = **273**.
- **Offset**: 12 bits, split across the high nibble of b0 (upper 4 bits) and
  b1 (lower 8 bits). Maximum offset = 4095. A zero offset or one that reaches
  before the output start is corrupt (rejected by `Decompress`).
- **Overlapping copies**: when `offset < length`, the decompressor must perform
  a byte-by-byte copy from the already-expanded output (this is how single-byte
  RLE is encoded: off=1, any length up to 273). `Decompress` uses a per-byte
  `append` loop, not a `copy()`, because dst is pre-sized to `rawSize` and the
  append never reallocates.

### Decompressor

`Decompress(src, rawSize)` reads the token stream and produces exactly
`rawSize` bytes. It processes control bytes one at a time, bit by bit LSB-first.
For a match tag, it decodes length and offset, validates `off > 0` and
`off <= len(dst)`, clamps `length` to `remaining = rawSize - len(dst)`, and
performs a byte-by-byte run-length copy from `dst[start:start+k]` where
`start = len(dst) - off`. An `off == 0` or `off > len(dst)` error is returned
(which would otherwise loop forever). A truncated match tag (fewer than 2 bytes
remaining), truncated extension byte, or `len(dst) != rawSize` at end are all
errors.

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
- **Overlapping RLE** — when `offset < length`, a naive `copy(dst[start:],
  dst[:len])` would read from unexpanded bytes. The byte-by-byte append loop
  correctly handles this by always reading from already-expanded positions.
- **`DecodeInlineCompressed` vs raw `Decompress`** — the former parses the
  8-byte varlena header, validates the compression method, and validates the
  total size; the latter is the raw token-stream decoder used when the caller
  already knows `rawSize` (e.g. from a `varlena` header they parsed
  themselves).
- **Compression method encoding** — the top 2 bits of `va_tcinfo` distinguish
  PGLZ (0) from LZ4 (1) and future methods (2, 3). `DecodeInlineCompressed`
  rejects any method ≠ 0 with a specific error rather than attempting to
  "decompress" bytes that are not PGLZ tokens.
- **LZ4 is unimplemented** — PG 18 added LZ4 as an alternative compression
  method; goopg decompresses LZ4 values from a real PG's catalog by rejecting
  them. The constant `CompressionMethodPGLZ = 0` and the method bits in
  `va_tcinfo` are prepared for a future LZ4 port.