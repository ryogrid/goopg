# 0014-0001 — XLOG Page and Segment Layout Compatibility

**Status:** accepted
**Milestone:** [0014 — PostgreSQL-Compatible WAL On-Disk Format](../milestones/0014-wal-compatibility-with-pg.md)
**Spans seam:** XLOG page header layout, segment filename convention,
encode/decode helpers.
**Cross-links:**
[root-0008](root-0008-wal-and-recovery.md) (current WAL writer baseline),
[0007-0001](0007-0001-wal-segment-preallocation.md) (segment preallocation
and EOS sentinel — the page-header convention will replace the bare-record
EOS sentinel in a later slice),
[0013-0001](0013-0001-wal-buffers-architecture.md) (buffer is byte-addressed
and unaffected by per-page header insertion).

## Context

goopg currently writes a flat byte stream of records to segment files
with no per-page structure. Upstream PostgreSQL groups records into
8-KiB pages whose first 24 bytes are an `XLogPageHeaderData`, with a
40-byte `XLogLongPageHeaderData` on the very first page of every
segment. Filenames encode `(TimelineID, log_no, seg_no)` rather than a
single 24-hex-char counter.

This slice introduces the upstream-compatible **types and helpers**
without yet changing the writer's append path — that's a follow-up
slice. Establishing the symbol names, constants, and filename encoding
in one well-tested commit means subsequent loops can wire them through
the writer/reader/recovery paths incrementally, with each step having a
known landing point.

## Target upstream major

PostgreSQL 18.x. Magic value `xlp_magic = 0xD119` (upstream's
`XLOG_PAGE_MAGIC` for 18). Bumped per-major upstream so a later
opt-in to PG19/PG20 is one constant change.

## Page header layout

Mirrors `postgres/src/include/access/xlog_internal.h`:

```go
type XLogPageHeader struct {
    Magic    uint16  // xlp_magic — XLOG_PAGE_MAGIC for the target major
    Info     uint16  // xlp_info — flag bits (XLP_*)
    TLI      uint32  // xlp_tli — timeline of the first record on this page
    PageAddr uint64  // xlp_pageaddr — absolute byte LSN of this page
    RemLen   uint32  // xlp_rem_len — bytes remaining of a continued record
    // (4-byte tail-pad — MAXALIGN'd to 24 bytes)
}

type XLogLongPageHeader struct {
    Std        XLogPageHeader  // 24 bytes
    SysID      uint64          // xlp_sysid — pg_control system identifier
    SegSize    uint32          // xlp_seg_size — cross-check
    XLogBlcksz uint32          // xlp_xlog_blcksz — cross-check
}
```

- `SizeOfXLogShortPHD = 24` (after MAXALIGN of 20-byte fields).
- `SizeOfXLogLongPHD  = 40`.
- `XLOG_BLCKSZ        = 8192` — page size in bytes.

### Flag bits

```go
const (
    XLP_FIRST_IS_CONTRECORD            = 0x0001
    XLP_LONG_HEADER                    = 0x0002
    XLP_BKP_REMOVABLE                  = 0x0004
    XLP_FIRST_IS_OVERWRITE_CONTRECORD  = 0x0008
    XLP_ALL_FLAGS                      = 0x000F
)
```

### Endianness

PostgreSQL writes WAL in **host byte order** (no endianness mandate
in xlog.c). For pg_waldump compatibility on the same machine we must
match the host's endianness. Linux/x86_64 and Linux/aarch64 are both
little-endian, so for v0 we hard-code little-endian and document that
cross-arch WAL transfer is not supported (matches upstream's de-facto
limitation). Encoding helpers use `binary.LittleEndian` directly.

## Segment filename layout

Upstream `XLogFileName`:

```
<TLI:%08X><Log:%08X><Seg:%08X>
```

Where:

- `Log = (segno * segsize) / 0x100000000`  — high 32 bits of byte offset
- `Seg = segno % (0x100000000 / segsize)` — segment within the log

For the upstream default 16 MiB segment size, `0x100000000/0x1000000 = 256`,
so each "log" is 256 segments. New helpers:

```go
func XLogFileName(tli uint32, segno uint64, segSize int64) string
func ParseXLogFileName(name string) (tli uint32, segno uint64, ok bool)
```

Both are pure functions; tests pin them against upstream-derived
expected values for representative TLI/segno combinations.

## Coexistence with the legacy filename

The current `formatSegmentName` / `parseSegmentName` produce raw
24-hex-char counters. The new helpers live alongside them so the
writer/reader switchover in a later slice can flip atomically without
churning unrelated code paths first. Until then the new helpers are
unused by production paths — only tests reference them.

## Encode/decode helpers

```go
// EncodeXLogPageHeader writes a 24-byte short page header to dst[:24].
func EncodeXLogPageHeader(dst []byte, h XLogPageHeader) error

// DecodeXLogPageHeader parses the first 24 bytes of src.
func DecodeXLogPageHeader(src []byte) (XLogPageHeader, error)

// EncodeXLogLongPageHeader writes a 40-byte long page header with the
// short header followed by sysid/seg_size/xlog_blcksz cross-check.
func EncodeXLogLongPageHeader(dst []byte, h XLogLongPageHeader) error

func DecodeXLogLongPageHeader(src []byte) (XLogLongPageHeader, error)
```

Encode validates that the page header's `Info` flags are all in
`XLP_ALL_FLAGS`. Decode validates the magic value matches
`XLOG_PAGE_MAGIC` for the target major; mismatches return a sentinel
`ErrInvalidPageHeader` so the legacy-format detector
(M0014-0004) has a typed failure to branch on.

## Tests

- `TestXLogFileNameRoundTrip`: 24-character upstream filename for
  representative `(tli=1, segno=0)` → `000000010000000000000000`,
  `(tli=2, segno=257)` → `000000020000000100000001` (with 16 MiB
  segments), and the round-trip via `ParseXLogFileName`.
- `TestXLogPageHeaderRoundTrip`: encode → decode preserves every
  field. Magic/Info/TLI/PageAddr/RemLen.
- `TestXLogLongPageHeaderRoundTrip`: long header round-trip
  preserves SysID/SegSize/XLogBlcksz and the embedded short header.
- `TestDecodeXLogPageHeaderRejectsBadMagic`: planted wrong magic
  returns `ErrInvalidPageHeader`.

## Out of scope

- Wiring the page-header into the writer's append path (the
  per-page header insertion + cross-page record continuation) —
  M0014-0001b.
- The XLogRecord header replacing the current 8-byte
  length+CRC frame — M0014-0002.
- Recovery / streaming integration — M0014-0003.
- Legacy-format detection and migration guardrails — M0014-0004.
- Cross-architecture WAL transfer (big-endian readers) — out of
  M0014 entirely; matches upstream's de-facto limitation.
