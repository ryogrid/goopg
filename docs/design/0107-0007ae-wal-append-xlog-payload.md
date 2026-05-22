# 0107-0007ae — `(*stripeWriterCore).AppendXLogPayload` top-level PG-compat WAL append composer

Status: implemented (slice B foundation 22 of N).
Parent: [M0107-0007 Phase D4 — WAL insert striping](perf-optimize/07-wal-fsm-insert.md).
Prior foundations: [[0107-0007h]] through [[0107-0007ad]] (minus the
dead-code-removed [[0107-0007h]] / [[0107-0007x]]).

## Problem

The slice B call-site rewrite needs ONE entry point at
`state.append`'s PG-compat write path, not the four-step
predict → reserve → encode → emit dance the twenty-one
earlier foundations exposed as separate primitives. The
sequencing is exact and the pre-existing composer
([[0107-0007ab]] `AppendBuiltEmitted`) takes a build closure that
the call-site cannot meaningfully customise — every caller would
ship the same `encodeRecordXLog + emitWithPageHeaders` body.

The two failed alternatives ([[0107-0007ad]] §Problem) — encode-
then-reserve (2× encode tax) and stash-and-patch (CRC defeat) —
both have to be rejected here as well; the only sound path is
predict-then-reserve via [[0107-0007ad]] `predictXLogRecordLen`
feeding [[0107-0007ab]] `AppendBuiltEmitted`. Packaging that
two-step into a single method closes the foundation chain.

## Solution

Add `appendXLogPayload(c *stripeWriterCore, procNum int32, payload
[]byte, segSize int64, sysID uint64, tli uint32) (start, prev
uint64, total, leading int, err error)` plus the
`(*stripeWriterCore).AppendXLogPayload(...)` method wrapper.
The composer is a 4-line delegation:

```go
_, paddedLen := predictXLogRecordLen(payload)
return c.AppendBuiltEmitted(procNum, paddedLen,
    func(start, prev uint64, total, leading int) ([]byte, error) {
        record, realRecLen, eerr := encodeRecordXLog(payload, prev)
        if eerr != nil {
            return nil, eerr
        }
        out, _ := emitWithPageHeaders(record, realRecLen,
            int64(start), segSize, sysID, tli)
        return out, nil
    })
```

`paddedLen == len(encodeRecordXLog(payload, prev))` for any
`prev` value — `encodeRecordXLog`'s output length is
`maxAlignXLog(xlogRecordHeaderSize + wrappedLen)`, independent of
`prev`. The `len(out) == total` assertion inside
`stripeAppendBuiltEmitted` (foundation 19) catches drift between
the predict path and the encode path.

## Why a method, not just the standalone function

The standalone `appendXLogPayload` is the unit-testable surface
([[0107-0007u]] / [[0107-0007y]] / [[0107-0007ab]] follow the
same pattern: standalone function in one file, method wrapper
on the core in `stripe_writer_core.go`). The method makes the
call-site rewrite read as `s.core.AppendXLogPayload(procNum,
payload, segSize, sysID, tli)` — one method call replacing
today's roughly 10 lines of inline encode + emit at each of the
three PG-compat call sites (`state.append`, `state.tryAppend`,
`state.appendBatch`).

## PG-compat contract

Byte-identical to today's `state.append` PG-compat path:

- `encodeRecordXLog` produces a MAXALIGN-padded `XLogRecord` with
  the post-reservation `prev` stamped into `xl_prev`; `xl_crc`
  covers `(wrapped || header[:20])`.
- `emitWithPageHeaders` stamps standard / long page headers at
  `start`-relative page boundaries with the cluster's `sysID` +
  `tli`, identical to today's output.
- Cross-segment crossings emit an XLOG_NOOP pad record (built by
  [[0107-0007j]] `buildSegmentPadRecord`, dropped under `posMu` by
  [[0107-0007s]] `emitSegmentPad` in the `onCrossSegment` hook of
  [[0107-0007k]] `insertPosTracker`) at the gap, and the triggering
  reservation lands at the boundary with a long PHD.

## Nil-safety and edge cases

- nil receiver → `errStripeWriterCoreNil` (matches
  `AppendBuiltEmitted`'s contract).
- nil payload → `errStripeAppendEmptyRecord` (`predictXLogRecordLen
  (nil) == (0, 0)`; `AppendBuiltEmitted` rejects `recordLen<=0`).
- empty-but-non-nil payload (`[]byte{}`) proceeds normally with
  `paddedLen = maxAlignXLog(xlogRecordHeaderSize + 2) = 32` — a
  legitimate body-less WAL record.

## Lock-ordering tier

Inherits [[0107-0007ab]] `AppendBuiltEmitted`'s chain verbatim;
no new locks taken:

```
core.AppendXLogPayload(procNum, payload, segSize, sysID, tli)
  → core.AppendBuiltEmitted(procNum, paddedLen, build)
    → appendLockSet.lockByProcNum               (one of 8 stripes)
      → insertPosTracker.reserveEmittedAndPublish (posMu held)
          → (rare) onCrossSegment(start, boundary, gapPrev)
              → emitSegmentPad → buildSegmentPadRecord +
                walBuffer.writeReserved + MemRing.WriteReserved
          → insertionTracker.setInsertingAt(stripe, start)
        (posMu released)
      → build closure runs:
          encodeRecordXLog(payload, prev)
          emitWithPageHeaders(record, realRecLen, start, segSize,
                              sysID, tli)
      → walBuffer.writeReserved
      → MemRing.WriteReserved
      → insertionTracker.setInsertingAt(stripe, lsnIdle)
    → drop stripe lock
```

## Test coverage

`internal/wal/append_xlog_payload_test.go`:

- `TestAppendXLogPayloadHappyPathReturnsPredictedSizes` — first
  reservation lands at start=0 with long PHD; total matches
  `predictEmittedSize(paddedLen, 0, segSize)`.
- `TestAppendXLogPayloadTwoRecordsFormChain` — two contiguous
  reservations; second's `prev` field equals first's `start`; on-
  wire `xl_prev` byte field (header bytes 8..16) confirms the
  build closure stamped the value.
- `TestAppendXLogPayloadBytesLandInWalBuf` — composer output
  byte-identical to direct `encodeRecordXLog` + `emitWithPageHeaders`
  for the same `(payload, start, prev)` tuple.
- `TestAppendXLogPayloadNilReceiverReturnsError` —
  `errStripeWriterCoreNil`.
- `TestAppendXLogPayloadNilPayloadReturnsEmptyRecordError` —
  `errStripeAppendEmptyRecord`.
- `TestAppendXLogPayloadEmptyByteSliceProceeds` — empty non-nil
  payload produces a valid 32-byte record (paddedLen = 32).
- `TestAppendXLogPayloadCrossSegmentBoundary` — pre-burn curr,
  reservation crosses, post-boundary start = segSize, long PHD on
  the new segment-aligned page (`segSize = 2*XLOGBlockSize` so the
  segment boundary coincides with a page boundary).
- `TestAppendXLogPayloadEncodeAndEmitSizesAgree` — 7-case payload
  matrix (empty, 1 byte, 8 bytes, 100 bytes, 0xFF / 0x100
  switchover, full block) pins composer `total ==
  predictEmittedSize(paddedLen, 0, segSize)`.

All seven tests plus four assertions pass under
`go test -race -count=1 -run 'TestAppendXLogPayload' ./internal/wal/`
(1.03 s); full `go test -race -count=1 ./internal/wal/` PASS
(4.11 s); `go vet ./internal/wal/` clean.

## Out of scope (deferred to call-site rewrite)

- Mounting `core.AppendXLogPayload` at `state.append` /
  `state.tryAppend` / `state.appendBatch`'s PG-compat write path
  (multi-loop because `state.appendMu`'s four invariants —
  writePos / walBuf / memRing / writeLSN — split into per-stripe
  local state vs. shared state).
- Mounting `core.PublishUpTo` in the drain goroutine's prelude
  (`drainBufferBytes` currently runs under `appendMu`; rewrite
  must let drain run concurrently with stripe writes by
  consuming the publisher's return as drain ceiling).
- Walreceiver replay (`appendRaw`) does not use page-header
  insertion — bytes arrive pre-encoded from the primary — so
  that path will continue to consume the size-explicit
  [[0107-0007p]] `reserveAndPublish` / [[0107-0007u]]
  `stripeAppend` instead.
