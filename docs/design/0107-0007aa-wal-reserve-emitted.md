# 0107-0007aa — `reserveEmittedAndPublish` joint-atomic predict + reserve + publish

Status: accepted
Date: 2026-05-21
Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page distribution)
Slice: B (8-stripe WAL insert path)
Foundation: 18 of N

## Problem

Foundation 17 ([[0107-0007z]] `predictEmittedSize`) provides a pure
size-prediction helper so the slice B call site can learn the exact
emitted byte count for a record at a given start position before
reserving LSN space. That helper closes one chicken-and-egg behind the
slice B call-site rewrite but leaves another open: the race documented
in [[0107-0007z]] §"Out of scope" between the predict call and the
matching reserve call.

```go
// Racy two-step sequence using only foundation 17:
curr, _   := core.Load()                                      // (A)
total, _  := predictEmittedSize(recordLen, curr, segSize)     // (B)
// ← peer stripe lands a reservation, advancing `curr` past
//   the value (A) observed.
start, prev, err := core.AppendBuilt(procNum, total, build)   // (C)
```

Between (A) and (C) a peer stripe with the same-procNum-hash collision
risk profile can land its own `reserveAndPublish` and shift `curr` by
`total_peer` bytes. When (C) eventually acquires `posMu`, the predicted
`total` no longer matches the actual page-header / contrecord layout
at the (now-advanced) `curr` — the build closure would have to be
re-run with the fresh `prev`, and the size mismatch defence
(`errStripeAppendBuildSizeMismatch` in [[0107-0007y]]) would fire on
the way.

Loud retry-on-mismatch is not free. On the slice B hot path, retry
means re-encoding the record from scratch (the build closure typically
calls `encodeRecordXLog`, which copies the full chunk-header structure
plus the record body). Under high concurrency, half the inserts would
discover stale predictions and re-encode — a 2× cost on the most
common case.

## Resolution

Thread the prediction *into* the reserve operation, under the same
`posMu` critical section that `reserveAndPublish` already holds. A
peer reservation cannot land between predict and reserve because the
two happen atomically.

New method on `*insertPosTracker`:

```go
func (t *insertPosTracker) reserveEmittedAndPublish(
    recordLen int, stripe int, tracker *insertionTracker,
) (start, prev uint64, total, leading int)
```

Under `posMu`:

1. Compute `total, leading = predictEmittedSize(recordLen, t.curr, segSize)`.
2. If `t.curr + total` would straddle the next segment boundary:
   - Fire `onCrossSegment(t.curr, boundary, t.prev)` to fill the gap
     (the slice B call site installs [[0107-0007s]] `emitSegmentPad`
     as that hook).
   - Set `t.prev = t.curr` (the pad record's start is the prev pointer
     for the upcoming reservation).
   - Shift the candidate start to `boundary`.
   - Re-predict at `boundary`: `total, leading = predictEmittedSize(
     recordLen, boundary, segSize)`. The re-predict is mandatory
     because page-header schedule differs between mid-page and segment-
     boundary start positions (long PHD at the new start vs. somewhere
     in the middle of the original predict's byte stream).
3. Reserve: `start = candidate`, `prev = t.prev`, `t.curr = start +
   total`, `t.prev = start`.
4. Publish the stripe slot: `tracker.setInsertingAt(stripe, int64(start))`.

The whole sequence is observed by any subsequent `posMu` acquirer as
one indivisible action — the same race-closure pattern foundation 9
([[0107-0007p]] `reserveAndPublish`) used to close the pre-reserve
race between reserve and stripe-publish.

## Why re-predict at boundary?

Page-header layout depends on the start position's residue modulo
`XLOGBlockSize` (page-aligned vs. mid-page) and modulo `segSize`
(segment-aligned → long header vs. just page-aligned → short header).
A reservation that the caller predicted at `t.curr` (mid-page, no
leading header) but reserveLocked's slow path bumped to `boundary`
(segment-aligned, long leading header) has different per-page header
counts and therefore a different total emitted byte count.

The total byte counts often coincide numerically — both paths pay one
long-header tax for the segment crossing — but `leading` always
differs (0 at mid-page, 40 at segment boundary). The slice B caller
uses `leading` to know how many bytes to emit *before* the record
body in `emitWithPageHeaders`; getting it wrong corrupts the WAL byte
stream. The re-predict guarantees the returned `leading` matches the
actual start LSN.

## Why not couple this into `reserveAndPublish`?

[[0107-0007p]] `reserveAndPublish` takes an explicit `size` and
guarantees joint atomicity of `(curr, prev, stripe-slot)`. Some
callers — the walreceiver replay path, future internal reservations
that don't need page-header insertion — want exactly that
size-explicit primitive. `reserveEmittedAndPublish` is a strict
superset for the PG-compat insert path: it accepts `recordLen` (raw
record bytes) instead of `size` (emitted bytes), runs the predict
internally, and returns `(total, leading)` alongside `(start, prev)`.

Keeping them split lets each be unit-tested in isolation and lets
future calls choose the right primitive. The shared `posMu` discipline
guarantees they coexist correctly — both serialise on the same lock.

## PG counterpart

`XLogInsertRecord` in `postgres/src/backend/access/transam/xlog.c`
computes the `actualBytes` for header insertion *before* calling
`ReserveXLogInsertLocation` — but it does so while holding the
per-backend WAL insert lock, which is the same primitive that gates
`ReserveXLogInsertLocation`. The net effect is identical to foundation
18: prediction and reservation happen under the same serialising
primitive so no peer can advance `curr` between the two.

## Contract

- `recordLen > 0`. Empty records panic (no useful WAL meaning;
  `reserveLocked` would panic on `size == 0` anyway).
- `tracker != nil`. Panics rather than silently degrading to the
  racy path — callers of *this* method must have observable race-
  closure semantics.
- `0 <= stripe < appendLockStripes`. Matches
  `insertionTracker.setInsertingAt`.
- The emitted size at any reachable start position must be
  `<= segSize`. Panics otherwise. Records that exceed this are out
  of scope (PG enforces the same upper bound upstream; goopg's call
  site applies `maxAlignXLog` long before reaching here, so the
  emitted total is bounded by `recordLen` plus a few hundred bytes
  of page-header overhead).

## Cost

- One `predictEmittedSize` call per reservation (~tens of nanoseconds
  for typical records — a few loop iterations in a pure-arithmetic
  helper).
- Cross-segment reservations pay a second `predictEmittedSize` call;
  that path is rare (records straddling a 16 MiB segment boundary
  happen at most once per 16 MiB of WAL, dominated by the segment-
  rotation fsync cost).
- One additional `atomic.Int64.Store` (the stripe slot publish) under
  the existing `posMu` critical section. The Store does not extend
  the critical section meaningfully; the cost is dominated by the
  existing `posMu` Lock/Unlock pair.
- No new locks introduced.

## Lock ordering

After foundation 18 the writer chain reads:

```
appendLockSet.lockByProcNum  (one of 8 stripes)
  → insertPosTracker.reserveEmittedAndPublish  (posMu held)
      predict at curr
      → (rare, cross-segment) onCrossSegment hook → emitSegmentPad
      → re-predict at boundary
      reserveLocked-equivalent inline arithmetic
      → tracker.setInsertingAt(stripe, start)
    (posMu released)
  → walBuffer.writeReserved      (no lock; leaf)
  → MemRing.WriteReserved        (memRing.mu read-lock)
  → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock
```

The `reserveLocked-equivalent inline arithmetic` step does NOT call
`reserveLocked` itself — duplicating the (curr, prev) update inline
is the price of needing to look at `total` (from the predict) before
deciding whether `reserveLocked`'s cross-segment branch should fire.
Pulling the same update into a shared helper is a future cleanup;
right now `reserveLocked`'s body is six lines, so duplicating it is
cheaper than fragmenting it.

## Testing

Ten regression tests in `internal/wal/reserve_emitted_test.go`:

| Test | Pins |
|------|------|
| `…HappyPathMatchesStandalonePredict` | `(total, leading)` equal to a standalone `predictEmittedSize(recordLen, start, segSize)`; stripe slot published; other stripes untouched. |
| `…PageBoundaryGetsShortHeader` | `start = XLOGBlockSize`, `leading = SizeOfXLogShortPHD`, `total = short PHD + recordLen`. |
| `…SegmentBoundaryGetsLongHeader` | `start = segSize`, `leading = SizeOfXLogLongPHD`, `total = long PHD + recordLen`. |
| `…CrossSegmentEmitsPadAndRePredicts` | `onCrossSegment` fires once with `(startPos, boundary, oldPrev)`; reservation lands at boundary with `prev = startPos` and re-predicted `(total, leading)`. |
| `…CrossSegmentNoHookSkipsNotify` | Cross-segment shift still happens when hook is nil. |
| `…InvalidRecordLenPanics` | `{0, -1, -100}` all panic. |
| `…NilTrackerPanics` | Nil tracker panics (no silent skip of publication). |
| `…InvalidStripePanics` | `{-1, appendLockStripes, +1, MaxInt32}` all panic. |
| `…ConcurrentNoRaceMatchesPredictAtStart` | 8 stripes × 200 records: every returned `(total, leading)` matches a standalone predict at the returned `start` — pins race closure (the prediction inside `posMu` is always self-consistent regardless of peer advances). |
| `…ConcurrentChainAndStripePublishConsistent` | Race-closure assertion: a reader that takes `posMu` directly observes, for every non-idle stripe slot `v`, `v < curr` — the old uncoupled reserve + setInsertingAt admitted a window where curr advanced but the stripe slot still read idle. |
| `…CrossSegmentChainIntegrity` | Multi-record sequence with curr landing exactly at boundary: rec2 starts at boundary with long PHD; no spurious cross-segment fires when curr == boundary. |
| `…CrossSegmentLeadingDiffersFromPredictAtCurr` | Pins that re-predict at boundary returns the *boundary* leading header (long), not the *curr* leading header (zero) — load-bearing for byte-stream correctness. |
| `…Watchdog` | 5-second deadlock watchdog under contention (mirrors foundation 7 / 9 / 11 / 15 / 17 pattern). |

Verified:

```
go test -race -count=1 -run 'TestReserveEmittedAndPublish' ./internal/wal/  →  PASS (1.02 s)
go test -race -count=1 ./internal/wal/                                       →  PASS (4.05 s)
go vet ./internal/wal/                                                       →  clean
```

## PG-compat

None — pure in-memory primitive; produces no on-disk bytes, does not
interact with WAL record format, file format, catalog, or wire
protocol. The byte-stream emission still flows through
`emitWithPageHeaders` (or its byte-builder twin in the slice B
call-site rewrite), which is unchanged.

## Out of scope (deferred to call-site rewrite and later foundations)

- Mounting `reserveEmittedAndPublish` on `Writer` and switching
  `state.append` / `state.tryAppend` to call it (multi-loop scope —
  `state.appendMu`'s four invariants split into per-stripe local
  state vs. shared state).
- Mounting `core.PublishUpTo` in the drain goroutine's prelude.
- Walreceiver replay (`appendRaw`) does not use page-header insertion
  — incoming bytes already carry headers stamped by the primary —
  so `reserveEmittedAndPublish` is not on that path; `appendRaw` will
  continue to consume the size-explicit [[0107-0007p]]
  `reserveAndPublish`.
- Adding a `core.AppendBuiltEmitted(procNum, recordLen, build)`
  wrapper on `stripeWriterCore` that bundles
  `reserveEmittedAndPublish` + `build(prev)` + ring writes + END
  marker (foundation 19 candidate — keeps the call-site rewrite a
  one-liner).
