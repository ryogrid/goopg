# 0107-0007t — Slice B foundation 13: `publishVisibility` drain-side composer

Status: landed 2026-05-21 (dead code; consumed by the slice B call-site rewrite)

Milestone: [M0107-0007] (`docs/milestones/0107-perf-optimize.md`,
`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 — 8-stripe WAL
insert locks).

Slice B foundation 13 of N. Composes [[0107-0007n]] `tailPublisher`'s
watermark computation with [[0107-0007q]] `walBuffer.publishTail` and
[[0107-0007o]] `MemRing.PublishUpTo` into the single action that the
slice B drain goroutine must perform to make stripe-written bytes
visible to readers.

## Problem

Under the slice B 8-stripe model, every stripe writer lands bytes via
[[0107-0007l]] `walBuffer.writeReserved` + [[0107-0007o]]
`MemRing.WriteReserved` without touching either ring's tail (that's
deliberate: out-of-order writers from disjoint LSN ranges must not
publish each other's bytes prematurely). Visibility — making the
written bytes drainable to disk and readable by walsenders — is
deferred to the drain goroutine, which:

1. Calls [[0107-0007n]] `tailPublisher.publishUpTo(upperBound,
   insertionTracker)` to compute the highest LSN below which every
   reservation has finished its byte write.
2. Calls [[0107-0007q]] `walBuffer.publishTail(safeTail)` to make the
   bytes-side ring's `[head, safeTail)` range drainable.
3. Calls [[0107-0007o]] `MemRing.PublishUpTo(safeTail)` to make the
   walsender mirror's `[head, safeTail)` range readable.

The three calls are mechanical; the order is load-bearing (`publishTail`
must use the publisher-returned watermark; `MemRing.PublishUpTo` must
use the same value so readers across both rings see a consistent
window). The slice B call-site rewrite would otherwise duplicate this
three-call chain everywhere the drain goroutine wakes — once per
periodic tick, once per group-commit nudge, once per fsync deadline,
once per shutdown drain — risking subtle drift (e.g., publishing the
MemRing before the walBuffer would let a walsender stream bytes a
disk reader cannot yet read, breaking the "byte rings observe the
same watermark" invariant the drain depends on).

Foundation 13 lifts the composition into one place so the call-site
rewrite installs the chain as a single call.

## Design

```go
func publishVisibility(
    publisher *tailPublisher,
    walBuf *walBuffer,
    memRing *MemRing,
    tracker *insertionTracker,
    upperBound int64,
) int64
```

Returns the safe-tail value published to both rings (the value
`tailPublisher.publishUpTo` returned, capped at `upperBound` by that
primitive).

Implementation (3 lines + nil-guards delegated to the composed
primitives):

```go
safeTail := publisher.publishUpTo(upperBound, tracker)
walBuf.publishTail(safeTail)
memRing.PublishUpTo(safeTail)
return safeTail
```

## Drain-chain placement

```
(drain goroutine, after stripe writers land bytes via writeReserved)
  safeTail := publishVisibility(publisher, walBuf, memRing, tracker, upperBound)
  walBuffer.readForDrain(safeTail - head, dst)
  writeAt(...)                              ← disk I/O
  walBuffer.advanceHead(safeTail - head)    ← reclaim ring slots
```

Why `advanceHead` is NOT in the composer: publishing visibility and
reclaiming ring slots are different phases — visibility means
"drainable / readable", reclamation means "durably flushed to disk and
safe to overwrite." A drain that reclaims before flushing would let
stripe writers overwrite bytes the disk file does not yet contain.
Keeping `advanceHead` outside the composer matches the disk-IO
scheduling boundary the drain loop already enforces.

## Symmetry with foundation 12 (`emitSegmentPad`)

- [[0107-0007s]] `emitSegmentPad`: writer-side composer. Lands pad
  bytes into both rings without advancing visibility. Invoked from
  `insertPosTracker.onCrossSegment` under `posMu`.
- `publishVisibility` (this foundation): drain-side composer.
  Advances visibility for both rings without writing bytes. Invoked
  from the drain goroutine.

The two composers form a symmetric pair: one writes-without-publishing,
one publishes-without-writing. Together they cover the slice B model's
two distinct ring-manipulation phases.

## Nil-safety

Each composed primitive is independently nil-safe, so the composer
inherits the property:

- `publisher == nil` → `publishUpTo` returns 0; both rings receive
  `safeTail = 0`, which is `<= cur` for any ring with a positive
  watermark and short-circuits to a no-op. Composer returns 0.
  Production usage with a nil publisher would be a wiring bug; the
  defensive nil-safety only exists for transitional call-site
  rewrite states.
- `walBuf == nil` → `walBuffer.publishTail` returns 0 and does
  nothing. Supports `Config.WALBuffers == 0`.
- `memRing == nil` → `MemRing.PublishUpTo` returns without acting.
  Supports `wal_sender_memory_buffer == 0`.
- `tracker == nil` → `publishUpTo` treats as "all stripes idle"
  (sentinel composition); `safeTail = upperBound`. Supports
  transitional state during call-site rewrite when the tracker is
  wired in after the publisher.

## Why no error return

The three composed primitives are infallible by construction:

- `publishUpTo` is a CAS loop; the loop has no failure mode.
- `publishTail` is a single atomic store (monotonic by branch).
- `PublishUpTo` is a write-locked monotonic store with internal
  head-clamp when residency would exceed cap.

The composer is therefore infallible too, which is exactly what the
drain goroutine wants: every tick advances visibility unconditionally.

## Lock-ordering tier

```
(drain goroutine, separately from stripe-writer chain:)
  publishVisibility(publisher, walBuf, memRing, tracker, upperBound)
    → tailPublisher.publishUpTo(upperBound, insertionTracker)  (lock-free)
    → walBuffer.publishTail(safeTail)                          (atomic store)
    → MemRing.PublishUpTo(safeTail)                            (memRing.mu write)
  walBuffer.readForDrain(safeTail - head, dst)
  writeAt(...)
  walBuffer.advanceHead(safeTail - head)
```

`publishVisibility` takes no locks of its own. The composed primitives
take only what they need; the only lock acquired is
`MemRing.PublishUpTo`'s internal write lock for the head-clamp window.

## PG counterpart

PG distributes this role across multiple call sites in
`postgres/src/backend/access/transam/xlog.c`:

- `WaitXLogInsertionsToFinish(upTo)` walks per-stripe `insertingAt`
  and returns the watermark — equivalent to goopg's
  `tailPublisher.publishUpTo`.
- `XLogCtl->LogwrtResult.Write = …` updates the published watermark
  — equivalent to goopg's `walBuffer.publishTail`.
- Walsender's snapshot view of recent records uses the same watermark
  — equivalent to goopg's `MemRing.PublishUpTo`.

PG composes these inline at every drain / flush call site. goopg
composes them into one function because the slice B call-site rewrite
needs the chain at multiple drain entry points (periodic tick,
group-commit, fsync deadline, shutdown drain) and a single composer
keeps them consistent.

## Tests

`internal/wal/publish_visibility_test.go` (10 tests):

- `TestPublishVisibilityIdleTrackerAdvancesBothRings` — happy path; idle
  tracker → both rings advance to upperBound; publisher.load reflects.
- `TestPublishVisibilityActiveStripeCapsBothRings` — active stripe@600
  caps both rings at 600; after idle, second publish advances to 1000.
- `TestPublishVisibilityMonotonicAcrossCalls` — six-step monotonic
  sequence; both rings track the publisher in lock-step.
- `TestPublishVisibilityRegressingUpperBoundDoesNotRegressRings` —
  publisher's return value can decrease (cap at lower upperBound), but
  neither ring's tail regresses.
- `TestPublishVisibilityNilWalBufStillAdvancesMemRing` —
  `Config.WALBuffers == 0` path.
- `TestPublishVisibilityNilMemRingStillAdvancesWalBuf` —
  `wal_sender_memory_buffer == 0` path.
- `TestPublishVisibilityBothRingsNil` — degenerate "publisher only"
  case; publisher still advances.
- `TestPublishVisibilityNilPublisherReturnsZero` — defensive nil-safety
  contract; rings not advanced past 0.
- `TestPublishVisibilityNilTrackerActsAsAllIdle` — transitional
  contract; safeTail collapses to upperBound.
- `TestPublishVisibilityExposesWriteReservedBytesEndToEnd` —
  end-to-end: writeReserved into both rings → readers miss → publishVisibility
  → readers hit in both rings with byte-identical payload.
- `TestPublishVisibilitySentinelComposesWithMin` — math.MaxInt64-1
  upperBound with idle tracker; both rings receive the value without
  leaking the sentinel.
- `TestPublishVisibilityConcurrentWithStripeWriters` — 8 stripes × 5000
  oscillations + publisher goroutine; pins monotonicity + cross-ring
  consistency; 5-second watchdog.

Verified: `go test -race -count=1 -run 'TestPublishVisibility'
./internal/wal/` PASS (1.02 s); `go test -race -count=1 ./internal/wal/`
PASS (3.16 s).

## Out of scope

- Mounting `publishVisibility` on `Writer` and consuming it from the
  drain/flush goroutine (multi-loop because `state.append` currently
  advances `walBuf.tail` and `memRing.tail` synchronously inside
  `appendMu`; the rewrite splits the four invariants — writePos /
  walBuf / memRing / writeLSN — into per-stripe local state vs.
  shared state).
- Drain coordination with concurrent stripe writes
  (`drainBufferBytes` currently runs under `appendMu`; the rewrite
  must let drain run concurrently with stripe writes by consuming
  publishVisibility's return as the drain ceiling for `readForDrain`
  / `writeAt` / `advanceHead`).
- Whether `lsnAllocator` ([[0107-0007h]]) becomes dead-code-removed
  once the call-site converges on `insertPosTracker` +
  `insertionTracker` + `tailPublisher` + `reserveAndPublish` +
  `publishTail` + `emitSegmentPad` + `publishVisibility` — `reserve`
  remains in the API as a callable primitive without a tracker.
