# 0107-0007ab — slice B foundation 19: `stripeAppendBuiltEmitted` joint composer

**Status:** Landed 2026-05-21 — dead code until slice B call-site rewrite.
**Parent:** `docs/design/perf-optimize/07-wal-fsm-insert.md` §2 (8-stripe WAL
insert locks for M0107-0007 / Phase D4).
**Predecessor:** [[0107-0007aa]] `reserveEmittedAndPublish` (foundation 18 —
joint-atomic predict + reserve + publish under `posMu`).
**Symmetric counterpart:** [[0107-0007t]] `publishVisibility` (drain-side
composer; advances visibility for both rings without writing bytes).

## Problem

Foundation 18 ([[0107-0007aa]]) closed the predict-vs-reserve race by
threading `predictEmittedSize` into `reserveAndPublish` under `posMu`.
The call-site rewrite for PG-compat records would still need to compose
the result by hand on every site:

```
stripe := stripeForProcNum(procNum)
locks.locks[stripe].mu.Lock()
defer locks.locks[stripe].mu.Unlock()
defer insertTracker.setInsertingAt(stripe, lsnIdle)
start, prev, total, leading := posTracker.reserveEmittedAndPublish(recordLen, stripe, insertTracker)
out, err := build(prev, total, leading)
if err != nil { return ..., err }
if len(out) != total { return ..., errSizeMismatch }
if walBuf != nil { walBuf.writeReserved(int64(start), out) }
if memRing != nil { memRing.WriteReserved(int64(start), out) }
return start, prev, total, leading, nil
```

That is six concerns the slice B caller would have to get right at every
hot-path site (state.append for record inserts, tryAppend for
group-commit fast path, appendBatch for replay). The PR-mechanical
foundation pattern (slice C [[0107-0007e]] → [[0107-0007g]]; slice B
foundations 14 / 15) is to land the composer once so each call site is a
one-liner.

## Solution

`stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf,
memRing, procNum, recordLen, build) (start, prev uint64, total, leading
int, err error)` in `internal/wal/stripe_append_emitted.go`.

The composer:

1. Selects stripe via `stripeForProcNum(procNum)`.
2. Acquires `locks.locks[stripe].mu`.
3. Calls `posTracker.reserveEmittedAndPublish(recordLen, stripe,
   insertTracker)` — foundation 18's joint-atomic predict + reserve +
   stripe-publish under `posMu`. Returns `(start, prev, total, leading)`.
4. Invokes `build(prev, total, leading)` — the caller's encoder, which
   must materialise exactly `total` bytes of page-headered output
   (typically via `emitWithPageHeaders` with `prev` stamped into the
   record's `xl_prev` field).
5. Validates `len(out) == total`; mismatch →
   `errStripeAppendBuildSizeMismatch`.
6. Writes `out` into `walBuf` (nil-safe) and `memRing` (nil-safe).
7. Defer-fires `insertTracker.setInsertingAt(stripe, lsnIdle)` (END
   marker, LIFO before unlock).
8. Defer-releases stripe mutex.

`(*stripeWriterCore).AppendBuiltEmitted(procNum, recordLen, build)`
wraps it — nil-receiver returns `errStripeWriterCoreNil`.

## Build closure contract

```go
build func(prev uint64, total, leading int) ([]byte, error)
```

- `prev` is the post-reservation prev LSN to stamp into `xl_prev`.
- `total` is the reserved emit size (page headers + record body, derived
  from `predictEmittedSize`).
- `leading` is the byte count of the leading page header (`0` if start
  is not page-aligned; `SizeOfXLogShortPHD` at page boundaries;
  `SizeOfXLogLongPHD` at segment boundaries).
- Return value MUST be exactly `total` bytes — over publishes garbage
  into peer-stripe ranges, under publishes zeros into the WAL stream.
- Returning a non-nil error short-circuits with the END marker still
  fired so publication does not freeze.

## Why a new composer instead of extending `stripeAppendBuild`

[[0107-0007y]] `stripeAppendBuild` takes a precomputed `size` that the
caller derived via a separate `predictEmittedSize` call. Closing the
predict-vs-reserve race ([[0107-0007aa]]) requires threading
`recordLen` into the reservation primitive itself, which changes the
composer's signature — both the input shape (`recordLen` vs `size`) and
the return shape (`(start, prev, total, leading, err)` vs `(start,
prev, err)`). Keeping both composers lets the call-site rewrite migrate
PG-compat insert sites to `AppendBuiltEmitted` while leaving
already-built use cases (walreceiver replay's `appendRaw` consumes
plain `Append`; future test-only paths can still use `AppendBuilt`)
unaffected.

## Lock-ordering tier

```
stripeAppendBuiltEmitted(locks, posTracker, insertTracker, walBuf,
                         memRing, procNum, recordLen, build)
  → appendLockSet.lockByProcNum             (one of 8 stripes)
    → insertPosTracker.reserveEmittedAndPublish  (posMu held)
        → predictEmittedSize @ t.curr        (pure arithmetic)
        → (rare) onCrossSegment(start, boundary, gapPrev)
            → emitSegmentPad → buildSegmentPadRecord +
              walBuffer.writeReserved + MemRing.WriteReserved
        → predictEmittedSize @ boundary       (cross-seg re-predict)
        → insertionTracker.setInsertingAt(stripe, start)
      (posMu released)
    → build(prev, total, leading)            (caller encoder + page headers)
    → walBuffer.writeReserved                (no lock; leaf)
    → MemRing.WriteReserved                  (memRing.mu read-lock)
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock
```

## Tests

Twelve regression tests in `internal/wal/stripe_append_emitted_test.go`:

- `TestStripeAppendBuiltEmittedHappyPathReceivesPrevAndTotal` — pins the
  build-closure argument contract: prev advances across two reservations,
  the first lands at LSN 0 (segment-aligned → long PHD), the second
  mid-page → leading=0.
- `TestStripeAppendBuiltEmittedNilLocksReturnsError` /
  `…NilPosTrackerReturnsError` / `…NilInsertTrackerReturnsError` /
  `…NilBuildReturnsError` — defensive nil-guard sentinels match the
  shared stripeAppend family.
- `TestStripeAppendBuiltEmittedEmptyRecordReturnsError` — `recordLen ∈
  {0, -1, -100}` rejected before any side effect; posTracker untouched.
- `TestStripeAppendBuiltEmittedBuildErrorPropagatesAndClearsStripe` —
  build returning a sentinel error propagates, END marker still fires;
  reservation cannot be unwound (curr advances).
- `TestStripeAppendBuiltEmittedSizeMismatchReturnsError` — under and
  over-size builds both rejected with `errStripeAppendBuildSizeMismatch`;
  END marker fires.
- `TestStripeAppendBuiltEmittedNilWalBufStillWritesMemRing` /
  `…NilMemRingStillWritesWalBuf` — per-ring nil-safety propagates.
- `TestStripeAppendBuiltEmittedCrossSegmentEmitsPadAndRePredicts` — burns
  curr down to segSize-50 on a two-page segment; reservation
  cross-segment-shifts to the boundary with long PHD; pad lands at
  `[segSize-50, segSize)`; stamped prev=segSize-50.
- `TestStripeAppendBuiltEmittedConcurrentDisjointStripesProgressInParallel`
  — 8 stripes × 50 reservations each; every start LSN distinct;
  sorted starts disjoint by ≥ recordLen.
- `TestStripeWriterCoreAppendBuiltEmittedDelegatesToStripeAppendBuiltEmitted`
  — end-to-end via the core wrapper.
- `TestStripeWriterCoreAppendBuiltEmittedNilReceiverReturnsError` —
  nil-receiver returns `errStripeWriterCoreNil`.
- `TestStripeAppendBuiltEmittedWatchdog` — 5-second deadlock watchdog
  mirroring foundations 7 / 9 / 11 / 15 / 17.

## Verification

`go test -race -count=1 -run
'TestStripeAppendBuiltEmitted|TestStripeWriterCoreAppendBuiltEmitted'
./internal/wal/` PASS (1.04 s). `go test -race -count=1 ./internal/wal/`
PASS (4.16 s). `go vet ./internal/wal/` clean.

## PG-compat

None — in-memory composer; produces no on-disk bytes that differ from
the legacy single-mutex path. The byte stream the build closure returns
flows verbatim into `walBuf` / `memRing` via `writeReserved` (foundations
[[0107-0007l]] / [[0107-0007o]]) just as today's `state.append` flow
does, only under per-stripe locking instead of the global `appendMu`.

## Out of scope

- Mounting `core.AppendBuiltEmitted` at the PG-compat write entry point in
  `state.append` / `state.appendTryEnqueue` / `state.appendBatch` (slice
  B call-site rewrite parts 2/3 — multi-loop because `state.appendMu`'s
  four invariants split into per-stripe local state vs. shared state).
- Drain coordination with concurrent stripe writes — `drainBufferBytes`
  currently runs under `appendMu`; rewrite must let drain run
  concurrently with stripe writes by consuming `tailPublisher.publishUpTo`'s
  return as drain ceiling.
- Walreceiver replay (`appendRaw`) — incoming bytes already carry page
  headers stamped by the primary, so `appendRaw` will continue to consume
  the size-explicit [[0107-0007p]] `reserveAndPublish` /
  [[0107-0007u]] `stripeAppend` (no page-header insertion required).
