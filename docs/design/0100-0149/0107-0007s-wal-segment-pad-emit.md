# 0107-0007s — Slice B foundation 12: `emitSegmentPad` cross-segment composer

Status: landed 2026-05-21 (dead code; consumed by the slice B call-site rewrite)

Milestone: [M0107-0007] (`docs/milestones/0107-perf-optimize.md`,
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 — 8-stripe WAL
insert locks).

Slice B foundation 12 of N. Composes three earlier foundations into the
single action that [[0107-0007k]] `insertPosTracker`'s
`onCrossSegment` hook fires synchronously under `posMu` when a stripe
reservation crosses a segment boundary.

## Problem

`insertPosTracker.reserve` / `reserveAndPublish` ([[0107-0007p]]) bumps
a cross-segment reservation to the start of the new segment and reports
the gap `[gapStart, boundary)` to its `onCrossSegment(start, boundary,
prev)` hook so the caller can fill the gap with an XLOG_NOOP pad record.
Without a pad record:

- the bytes-side ring (`walBuffer`) contains an uninitialised hole
  between `gapStart` and `boundary` — a drain that publishes past
  `gapStart` would write garbage to the WAL segment;
- the walsender mirror (`MemRing`) has the same hole, so a streaming
  walsender's `ReadAt` returns garbage or misses;
- the xl_prev chain is broken — the immediately-following stripe
  reservation receives `prev=gapStart` from
  `insertPosTracker.reserveLocked`, pointing at non-existent bytes.

Each `onCrossSegment` invocation needs to (1) build a single XLOG_NOOP
record of exactly `boundary - gapStart` bytes with `xl_prev = prev`,
(2) write the pad bytes into the bytes-side ring at `gapStart`, (3)
mirror the pad bytes into the walsender ring at the same LSN — all
without advancing either publication watermark, because the drain
goroutine's [[0107-0007q]] `tailPublisher` chain is the sole authority
on visibility.

The three actions are each separately implemented (foundations j / l /
o) but a per-call-site composition would have the call-site rewrite
duplicate the boundary check, the nil-guards, and the error
propagation. Foundation 12 lifts the composition into one place.

## API

```go
func emitSegmentPad(
    walBuf *walBuffer,
    memRing *MemRing,
    gapStart, boundary, gapPrev uint64,
) error
```

`walBuf` and `memRing` are independently nil-safe so the composer can
be wired even when `Config.WALBuffers == 0` (no walBuf) or
`wal_sender_memory_buffer == 0` (no memRing). Both nil → no-op after
the builder runs (so malformed padLen still surfaces). The builder
runs regardless of ring presence; the rationale is to keep contract
violations visible to tests even with zero-sized rings.

`gapPrev` is stamped into the pad record's `xl_prev` slot; the
immediately-following stripe reservation receives `prev=gapStart` (the
pad record's start LSN) from `insertPosTracker.reserveLocked`.

Returns the first error encountered from: composer-level boundary
check (`boundary > gapStart`), `buildSegmentPadRecord` (padLen below
`SizeOfXLogRecord` or padLen == 25), `walBuf.writeReserved` (out of
window), or `memRing.WriteReserved` (out of window). Any error under
posMu is fatal — the hook fires exactly once per crossing and a
failed pad write breaks the xl_prev chain.

## Lock-ordering tier

When invoked from the planned call-site rewrite (option A from
[[0107-0007m]] §"Pre-reserve race", now sealed by [[0107-0007p]]
`reserveAndPublish`):

```
appendLockSet.lockByProcNum
  → insertPosTracker.reserveAndPublish     (posMu held)
      → (cross-segment slow path) emitSegmentPad
          → buildSegmentPadRecord           (no lock; pure builder)
          → walBuffer.writeReserved         (no lock; bytes write only)
          → MemRing.WriteReserved           (RLock for memcpy)
      → tracker.setInsertingAt(stripe, start)   (BEGIN edge)
    (posMu released)
  → walBuffer.writeReserved                 (triggering reservation)
  → MemRing.WriteReserved
  → insertionTracker.setInsertingAt(stripe, lsnIdle)
→ drop stripe lock
```

`emitSegmentPad` runs entirely under posMu. The read-only writeReserved
/ WriteReserved primitives take their own locks (walBuffer is mutex-
free under the slice B model; MemRing takes its RLock for the duration
of the memcpy).

## Why publication stays with the drain goroutine

A pad-side publication advance ahead of the drain would let `readAt` /
`MemRing.ReadAt` see pad bytes before the stripe reservation that
triggered the crossing has finished its own `writeReserved` — the same
hazard that prompted [[0107-0007l]] / [[0107-0007o]] to leave tail
untouched in the first place. The drain goroutine's
[[0107-0007n]] `tailPublisher` + [[0107-0007q]] `walBuffer.publishTail`
+ `MemRing.PublishUpTo` chain is the single point that decides when
pad bytes — and the surrounding stripe bytes — become visible.

## PG counterpart

`postgres/src/backend/access/transam/xlog.c` —
`AdvanceXLInsertBuffer` + `XLogInsertRecord` together emit the pad
record into the shared WAL buffer (and, when streaming, the walsender
snapshot view) under the WAL insert lock. goopg's composer matches
that single-call shape so the `insertPosTracker.onCrossSegment` hook
stays a one-liner.

## Out of scope

- Mounting `emitSegmentPad` as
  `insertPosTracker.onCrossSegment` on `Writer` (the call-site
  rewrite — multi-loop scope).
- Splitting `state.appendMu`'s four invariants (writePos / walBuf /
  memRing / writeLSN) into per-stripe local state vs. shared state
  (the call-site rewrite).
- Drain coordination with concurrent stripe writes
  (`drainBufferBytes` currently runs under `appendMu` — the rewrite
  must let drain run concurrently with stripe writes by consuming
  `tailPublisher.publishUpTo`'s return as drain ceiling for both
  `walBuffer.publishTail` / `walBuffer.advanceHead` /
  `MemRing.PublishUpTo`).
- Deciding whether [[0107-0007h]] `lsnAllocator` becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker` + `insertionTracker` + `tailPublisher` +
  `reserveAndPublish` + `publishTail` + `emitSegmentPad` — `reserve`
  remains in the API as a callable primitive without a tracker.

## Tests

`internal/wal/segment_pad_emit_test.go` (10 tests):

- `TestEmitSegmentPadWritesIntoBothRings` — happy path; pad bytes
  land at gapStart in both rings, decode to a well-formed XLOG_NOOP
  with `Prev == gapPrev`, neither ring's publication watermark
  advances.
- `TestEmitSegmentPadNilWalBufOnlyMemRing` — partial-ring path under
  `Config.WALBuffers == 0`.
- `TestEmitSegmentPadNilMemRingOnlyWalBuf` — symmetric for
  `wal_sender_memory_buffer == 0`.
- `TestEmitSegmentPadBothNilIsNoop` — both nil + malformed padLen
  still surfaces builder error.
- `TestEmitSegmentPadRejectsNonPositiveGap` — composer-level
  defence-in-depth for `boundary <= gapStart`.
- `TestEmitSegmentPadPropagatesBuilderErrors` — table-driven over
  `{8, 23, 25}` confirming "below minimum" and "1-byte body" errors
  surface unchanged.
- `TestEmitSegmentPadPropagatesWalBufOutOfWindow` — gapStart below
  walBuf.base surfaces `errWALBufferReservedOutOfRange`.
- `TestEmitSegmentPadPropagatesMemRingOutOfWindow` — gapStart below
  memRing.head surfaces `errMemRingReservedOutOfRange`.
- `TestEmitSegmentPadDoesNotPublishViaWalBuf` — pins tail/head
  unchanged; readAt of unpublished pad returns 0 bytes.
- `TestEmitSegmentPadDoesNotPublishViaMemRing` — pins the symmetric
  MemRing case.
- `TestEmitSegmentPadAcrossPadLengths` — header-only / short-chunk /
  long-chunk paths exercised end-to-end through the composer; pad
  bytes byte-identical between walBuf and memRing.

Verified: `go test -race -count=1 -run 'TestEmitSegmentPad'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.15 s).
