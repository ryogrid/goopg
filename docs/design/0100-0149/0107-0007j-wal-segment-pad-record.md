# 0107-0007j — Phase D4: WAL segment pad-record builder (slice B foundation 3)

**Status**: accepted (foundation; not yet wired)
**Milestone**: M0107-0007 — Phase D4 WAL insert striping + FSM page distribution
**Slice**: B, foundation 3 of N
**Parent design**: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](../../perf-optimize/07-wal-fsm-insert.md) §2

## 1. Scope

Add a pure byte-builder for the `RmgrXLog`/`XLOG_NOOP` record that the
`lsnAllocator` ([[0107-0007h]]) onCrossSegment hook will emit to pad
the `[start, boundary)` gap left when a cross-segment reservation hops
to the start of a new segment. The parent design §2 (cross-segment slow
path) references this padding step verbatim:

> Cross-segment slow path … pads the current segment's tail with a
> NOOP WAL record, advances `next` past the boundary, and reserves
> from the new segment.

Out of scope (deferred to later slice B work):
- Mounting `lsnAllocator` + `appendLockSet` on `wal.Writer` and
  rewriting `Append` to take a stripe lock + `lsnAllocator.reserve`
  with this builder as the onCrossSegment hook.
- Splitting `state.appendMu`'s four invariants (writePos, walBuf state,
  memRing append, writeLSN advance) into per-stripe local state vs.
  shared state.
- `prevRecPtr` chain integrity under per-stripe locks (the pad record's
  xl_prev slot is filled by the caller; this foundation only consumes
  the value).

## 2. PG counterpart

`XLOG_NOOP` is defined as `0x20` in
`postgres/src/include/catalog/pg_control.h:70` and dispatched as a
no-op in `postgres/src/backend/access/transam/xlog.c:8508`:

```c
else if (info == XLOG_NOOP)
{
    /* nothing to do here */
}
```

PG itself uses this record sparingly (the runtime path that emits it
on the primary is rare in practice; the recovery path that skips it is
present in every PG18 build). The key property the foundation relies
on is the recovery semantics — a PG18 standby replaying a goopg WAL
stream that contains a goopg-emitted NOOP record will skip it without
error, preserving the PG-compat byte-stream-replay guarantee from the
parent milestone's §6.

## 3. Design

### 3.1 Signature

```go
func buildSegmentPadRecord(padLen int, prev uint64) ([]byte, error)
```

- `padLen` — the exact number of bytes the returned slice must occupy.
  This equals `boundary - start` in the caller, where `boundary` is
  the first LSN of the next segment and `start` is the LSN at which
  the pad record will be written.
- `prev` — the LSN of the preceding record. Stamped into the pad
  record's `xl_prev` so the segment tail's prev-chain stays
  continuous; without this a reader that walks the prev-chain
  backwards from the new segment's first real record would stall.

### 3.2 Encoding choice per `padLen`

| `padLen` (p)           | body layout                                            |
|------------------------|--------------------------------------------------------|
| `p == 24`              | header only — no body, no chunk header                 |
| `p == 25`              | **rejected** — 1-byte body cannot carry a chunk header |
| `26 <= p <= 281`       | header + `xlrBlockIDDataShort` (1 B tag + 1 B length) + `p-26` zero data bytes |
| `p >= 282`             | header + `xlrBlockIDDataLong` (1 B tag + 4 B length) + `p-29` zero data bytes  |

`SizeOfXLogRecord = 24`; the body is everything after the 24-byte
header. The short chunk's length field is `uint8`, so the maximum
short-chunk body is `2 + 255 = 257` bytes; padLen up to `24 + 257 =
281` fits short. Larger pads fall through to the long chunk header
(`uint32` length).

The 25-byte rejection is the singular hole: a 1-byte body cannot
carry the 2-byte short chunk header (let alone the 5-byte long chunk
header). The parent §2 design pairs this foundation with
`maxAlignXLog` (records are 8-byte aligned on disk), so reservations
always produce `padLen ∈ {24, 32, 40, …}` — every such value is
encodable. The explicit error is defence-in-depth for any future
caller that bypasses the alignment rule.

### 3.3 Body bytes are zero

Every byte past the chunk header (or the entire body for `padLen ==
24`) is zero. This is deliberate:

- A byte-diff replay test between two goopg builds running the same
  workload will see identical pad records (CRC included), because the
  CRC is a pure function of the bytes.
- A future RmgrXLog handler that tries to interpret the body as
  structured data would see all-zero bytes — well-defined per the
  protocol (no block references, zero-length main data with the chunk
  header consumed).

### 3.4 Lock ordering

The future call-site rewrite will hold:

```
appendLockSet.lockByProcNum(procNum)                               // append stripe
    ↳ (rare) lsnAllocator.rotateMu                                 // cross-segment
        ↳ buildSegmentPadRecord(padLen, prev) → write bytes        // pad
```

`buildSegmentPadRecord` itself takes no locks; it's a pure function
of `(padLen, prev)`. The rare cross-segment slow path takes
`rotateMu`, calls this builder, and writes the resulting bytes into
the WAL buffer at the LSN range `[start, boundary)` — all under the
stripe lock the caller already holds.

## 4. Regression coverage

`internal/wal/segment_pad_test.go`:

- **`TestBuildSegmentPadRecordMinSize`** — pins the `padLen == 24`
  edge: 24-byte output, header fields correct
  (TotLen/Rmid/Info/Prev/XID/CRC), and the CRC validates against the
  empty body via `VerifyXLogRecordCRC`. Without this pin a regression
  could silently drop the empty-body case (which is the most common
  alignment-driven shape).

- **`TestBuildSegmentPadRecordRoundTripSizes`** — table-driven across
  every encoding-branch boundary (`24`, `26`, `100`, `281`, `282`,
  `1024`, `64 KiB`). For each it asserts the byte length, header
  rmid/info, CRC validation, and a full structured-decoder round trip
  via `decodeRecordXLogDetailed` (parsing the xlrBlockIDData chunk
  header and confirming `MainData` length and all-zero contents).
  This is the cross-section that catches an off-by-one in the chunk
  header length computation (a particularly easy bug to introduce
  given the `-2` / `-5` constants).

- **`TestBuildSegmentPadRecordRejectsTooSmall`** — table-driven over
  `{0, 1, 8, 16, 23}`: every sub-header padLen returns an error
  containing "below minimum". The caller has no recovery strategy
  for these values, so the explicit error keeps the contract loud.

- **`TestBuildSegmentPadRecordRejects1ByteBody`** — pins the 25-byte
  hole. Without an explicit check `make([]byte, 1)` would silently
  produce a 1-byte body containing only `xlrBlockIDDataShort = 255`
  and no length, which the parser would reject with a confusing
  "truncated short xlog data header" error.

- **`TestBuildSegmentPadRecordPrevPropagated`** — `prev ∈ {0, 1, ...
  0xFFFF_FFFF_FFFF_FFFF}`. Pins the xl_prev stamp; the segment-tail
  prev-chain depends on this for backward walks from the new
  segment's first real record.

- **`TestBuildSegmentPadRecordBodyAllZeroAfterChunkHeader`** —
  `padLen ∈ {100, 1024}`, asserts every byte past the chunk header is
  zero. The byte-diff replay invariant for the parent milestone
  depends on this.

- **`TestBuildSegmentPadRecordCRCDeterministic`** — two builds with
  identical `(padLen, prev)` produce byte-identical output (CRC
  included). Without determinism the byte-diff replay test would
  falsely fail across runs.

- **`TestBuildSegmentPadRecordCRCDetectsCorruption`** — a single bit
  flip in the body invalidates the CRC. The CRC computation in
  `EncodeXLogRecordHeader` covers `(body || header[:20])`; without
  this test a regression that dropped the body from the CRC pre-image
  could ship undetected.

Verified: `go test -race -count=1 -run 'TestBuildSegmentPadRecord' ./internal/wal/` PASS (1.02 s);
`go test -race -count=1 ./internal/wal/` PASS (3.13 s).

## 5. PG-compat impact

None for this foundation — purely an in-memory byte-builder. The
record this function produces is byte-format-compatible with PG18's
`RM_XLOG_ID` / `XLOG_NOOP`:

- `xl_rmid = RmgrXLog (0)`
- `xl_info = 0x20 (XLOG_NOOP)`
- `xl_tot_len = padLen`
- body uses the standard `xlrBlockIDDataShort` / `xlrBlockIDDataLong`
  chunk tags from `postgres/src/include/access/xlogrecord.h`

A PG18 standby replaying a goopg-produced stream containing this
record falls through `xlog_redo`'s `info == XLOG_NOOP` branch and
performs no on-disk effect — matching the parent milestone's §6
"WAL record format guarantee".

## 6. Cross-references

- Parent chapter: [[07-wal-fsm-insert]] §2 (cross-segment slow path).
- Slice B foundation 1: [[0107-0007h]] (`lsnAllocator`) — the
  consumer of this builder.
- Slice B foundation 2: [[0107-0007i]] (`paddedMutex` /
  `appendLockSet`) — the stripe lock that orders above `rotateMu`.
- Slice A landed: [[0107-0007a]] (executor's `paddedMutex` /
  `heapExtendLockSet`).
- Slice C landed: [[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]]
  foundations + [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]]
  consumers.
- PG counterparts:
  `postgres/src/include/catalog/pg_control.h:70` (`#define XLOG_NOOP
  0x20`); `postgres/src/backend/access/transam/xlog.c:8508`
  (`xlog_redo` NOOP branch);
  `postgres/src/include/access/xlogrecord.h`
  (`XLR_BLOCK_ID_DATA_SHORT` / `XLR_BLOCK_ID_DATA_LONG`).
