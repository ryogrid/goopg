# 0107-0007z — `predictEmittedSize` pure size-prediction helper

Status: accepted
Date: 2026-05-21
Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page distribution)
Slice: B (8-stripe WAL insert path)
Foundation: 17 of N

## Problem

The slice B call-site rewrite needs to reserve `[start, start+size)`
LSN bytes via `(*stripeWriterCore).AppendBuilt` before the record
bytes can be encoded — the encoder needs the `prev` LSN that the
reservation returns. This is the chicken-and-egg that
[[0107-0007y]] `stripeAppendBuild` was added to solve.

A second chicken-and-egg sits behind that one. The PG-compat write
path interleaves page headers into the byte stream (`emitWithPageHeaders`
in `xlog_emit.go`), and the number of bytes those headers add depends
on the reservation's start position:

  - If `startPos % XLOGBlockSize == 0`, a leading page header is
    emitted (short = 24 B, long = 40 B if also `startPos % segSize == 0`).
  - Every time the record body crosses a page boundary, a contrecord
    header is inserted (short = 24 B, long = 40 B at segment crossings).

So the reservation size is a function of (recordLen, startPos, segSize)
— but startPos is not known until the reservation is granted.

`stripeAppendBuild` takes a known `size` and a `build(prev)` closure.
To use it from the PG-compat path, the caller needs a way to compute
the exact emitted size BEFORE the reservation, given a candidate
startPos. The candidate startPos is what `posTracker.curr` holds under
`posMu` at the moment the reservation is made; the caller can read
that, predict the size, then call `core.AppendBuilt(procNum, size,
build)`. As long as the prediction happens under the same `posMu`
hold (or close enough that no peer can land a reservation in between),
the predicted size matches the actual emitted size.

## Solution

`predictEmittedSize(recordLen, startPos, segSize) (total, leading int)`
in `internal/wal/predict_emitted_size.go` is a pure function that
walks the same byte arithmetic as `emitWithPageHeaders` but counts
bytes instead of emitting them.

- No I/O, no allocation, no locks.
- Mirrors `emitWithPageHeaders` exactly: leading header at
  page-aligned startPos, contrecord headers at every page boundary the
  record crosses, segment-boundary headers are LONG (40 B), other
  page-boundary headers are SHORT (24 B).
- Reuses the existing `pageHeaderSizeAt(pos, segSize)` helper
  (defined in `xlog_emit.go` alongside `buildPageHeader`) so any
  future header-layout change applies everywhere atomically.

Returned values:

  - `total` — exact number of bytes the corresponding
    `emitWithPageHeaders` call would write to its output buffer
    (= leading + recordLen + sum of inserted contrecord headers).
  - `leading` — size of the leading page header (0 when startPos is
    mid-page; SizeOfXLogShortPHD or SizeOfXLogLongPHD when startPos
    is page-aligned).

Invalid inputs (recordLen ≤ 0, segSize ≤ 0, startPos < 0) return
(0, 0). This matches the input domain that real callers never
exercise — `recordLen` is always > 0 by the time `encodeRecordXLog`
runs, `segSize` is `Config.SegmentSize` which is > 0 by `withDefaults`,
and `startPos` is a non-negative LSN. The early return is
defence-in-depth, not a working invalid-input contract.

## Correctness argument

The core correctness check is a byte-for-byte round-trip against
`emitWithPageHeaders`: for every (recordLen, startPos) combination
in the test matrix, the predicted total equals the function's
returned `len(out)` and the predicted leading equals its returned
`leading`. The two share zero implementation surface — `predictEmittedSize`
counts bytes without touching `buildPageHeader`, `emitWithPageHeaders`
actually constructs the headers — so agreement across the input
matrix pins the arithmetic in one direction and detects drift in the
other.

Test matrix (16 startPositions × 10 recordLens = 160 cases) covers:

  - startPos = 0 (segment-boundary, also page-boundary → long leading)
  - startPos = 1, 23, 25 (mid-page, never page-boundary)
  - startPos = XLOGBlockSize - 1, XLOGBlockSize, XLOGBlockSize + 1
    (just-before / at / just-after a page boundary)
  - startPos = segSize - XLOGBlockSize, segSize - 1, segSize, segSize + 1
    (just-before / at / just-after a segment boundary)
  - startPos = 2 × XLOGBlockSize, 2 × segSize (multi-page / multi-
    segment offsets)
  - recordLens spanning 1, 24, 100, 1024, 8000, 8192, 8193, 16384,
    65536, segSize - 100 (single-byte → multi-page → segment-spanning)

## Usage shape (later loop)

The call-site rewrite will read like:

    realRecLen := encodeRecordXLog(...)  // unwrapped record bytes
    curr, _ := core.Load()                // current reservation cursor
    size, _ := predictEmittedSize(realRecLen, int64(curr), segSize)
    start, prev, err := core.AppendBuilt(procNum, size, func(prev uint64) ([]byte, error) {
        record, _, err := encodeRecordXLog(payload, prev)
        if err != nil {
            return nil, err
        }
        out, _ := emitWithPageHeaders(record, realRecLen, int64(start), segSize, sysID, tli)
        return out, nil
    })

A subtle race remains: between `core.Load` and the matching
`core.AppendBuilt`, a peer stripe could advance `curr`, making the
prediction wrong. The fix is to thread `predictEmittedSize` INTO
`reserveAndPublish` (compute size under `posMu`), or to expose a
`reservePredictively(...)` variant. Either is a separate slice — this
foundation lands the pure helper so the larger composition can be
designed in isolation. The current `core.AppendBuilt` would reject
the `build` closure with `errStripeAppendBuildSizeMismatch` if the
prediction was off, so the failure mode is loud, not silent
corruption.

## Out of scope (later slice B foundations / call-site rewrite)

- Threading the prediction under `posMu` (foundation 18 candidate, or
  call-site rewrite).
- Mounting `core.AppendBuilt` as the body of `state.append` / `state.tryAppend`
  for the PG-compat path.
- Mounting `core.PublishUpTo` in the drain goroutine's prelude.
- `appendRaw` (walreceiver replay) does not use page-header insertion
  — incoming bytes already carry headers stamped by the primary — so
  `predictEmittedSize` is not on that path.

## Verification

- `go test -race -count=1 -run 'TestPredictEmittedSize' ./internal/wal/`
  PASS (~2 s)
- `go test -race -count=1 ./internal/wal/` PASS (~4 s)
- `go vet ./internal/wal/` clean

## Files

- `internal/wal/predict_emitted_size.go` (new, ~70 lines)
- `internal/wal/predict_emitted_size_test.go` (new, ~170 lines, 5 tests)

## PG-compat

None. The helper is a pure size-prediction mirror of `emitWithPageHeaders`;
it produces no bytes and does not interact with on-disk WAL.

## Related foundations

[[0107-0007h]] lsnAllocator (since dead-code-removed) /
[[0107-0007i]] paddedMutex / [[0107-0007j]] buildSegmentPadRecord /
[[0107-0007k]] insertPosTracker / [[0107-0007l]] walBuffer.writeReserved /
[[0107-0007m]] insertionTracker / [[0107-0007n]] tailPublisher /
[[0107-0007o]] MemRing.WriteReserved/PublishUpTo /
[[0107-0007p]] insertPosTracker.reserveAndPublish /
[[0107-0007q]] publishVisibility / [[0107-0007r]] insertPosTracker recovery resume /
[[0107-0007s]] emitSegmentPad / [[0107-0007t]] publishVisibility wrapper /
[[0107-0007u]] stripeAppend / [[0107-0007v]] stripeWriterCore /
[[0107-0007w]] stripeWriterCore mount on Writer / [[0107-0007x]] lsnAllocator removal /
[[0107-0007y]] stripeAppendBuild
