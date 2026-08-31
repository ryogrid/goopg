# M0107-0007 Slice B Foundation 7 — `tailPublisher` Publication Watermark

## Status

PARTIAL. Foundation 7 of N for M0107-0007 slice B (Phase D4 — 8-stripe
WAL insert striping per `docs/design/perf-optimize/07-wal-fsm-insert.md`
§2). Foundations 1–6 ([[0107-0007h]] `lsnAllocator`, [[0107-0007i]]
`appendLockSet`, [[0107-0007j]] `buildSegmentPadRecord`, [[0107-0007k]]
`insertPosTracker`, [[0107-0007l]] `walBuffer.writeReserved`,
[[0107-0007m]] `insertionTracker`) landed first. The call-site rewrite
that mounts `tailPublisher` on `Writer` and drives it from the
drain/flush goroutine remains out of scope and is multi-loop work.

## Problem

`walBuffer.writeReserved` ([[0107-0007l]]) is a leaf primitive: it
writes bytes at a reserved LSN without advancing `walBuffer.tail`.
That deliberate omission was correct for foundation 5 — under
8-stripe concurrent writers, `tail` MUST NOT advance past LSN X until
every reservation strictly below X has had its bytes written by its
owning stripe. Otherwise drain (`readForDrain` / `advanceHead`) or
streaming-from-RAM (`readAt`, walsender's `MemRing`) would observe
uninitialised bytes in the gap between a reservation that advanced
`curr` past X and the stripe that has not yet copied its record into
the buffer.

Foundation 6 ([[0107-0007m]] `insertionTracker`) closed half the gap
by recording per-stripe `insertingAt` start LSNs under each stripe's
lock, and exposing `lowestActiveLSN()` for publication walkers.
What was still missing: the publication step itself — the primitive
that combines the highest reserved LSN (the upper bound) with the
lowest in-flight start LSN (from the tracker) and monotonically
publishes a watermark below which `tail` can safely advance.

## Design

```go
type tailPublisher struct {
    published atomic.Int64
}

func newTailPublisher() *tailPublisher
func (p *tailPublisher) load() int64
func (p *tailPublisher) publishUpTo(upperBound int64, tracker *insertionTracker) int64
```

`publishUpTo` computes the candidate as:

```
candidate = min(upperBound, tracker.lowestActiveLSN())
```

then CAS-loops to advance `published` monotonically. The return value
is `min(currentPublishedWatermark, upperBound)` — the caller's safe
upper bound, capped at their request so a peer that pushed the
internal watermark past `upperBound` does not leak that information
back as a drain-eligible LSN.

### Monotonicity

The internal `published` value never decreases. Once a publication
walker observes the watermark at LSN W, every reader (drain,
walsender, `readAt`) is permitted to treat `[base, W)` as a stable,
fully-written byte range. Allowing the watermark to regress would
race those readers against fresh inserts below W — exactly the
hazard that prompted `writeReserved` not to advance `tail` in
foundation 5.

The CAS loop early-returns on `candidate ≤ cur`, so a transient drop
in `lowestActiveLSN` (a new stripe entering at a low LSN after
watermark was advanced past it) cannot cause regression.

### Sentinel composition

`insertionTracker.lowestActiveLSN()` returns
`lsnNoActive = math.MaxInt64` when every stripe is idle. The min
collapses cleanly to `upperBound` in that case — no branch needed.
The sentinel cannot leak into the published value: only `candidate`
is ever stored, and `candidate = min(upperBound, …)` ≤ `upperBound`,
which a real caller bounds by the actual reserved LSN window.

### Cap-at-upperBound return contract

PG's `WaitXLogInsertionsToFinish(upTo)` returns a value ≤ `upTo`
even if the underlying watermark has advanced past it. goopg matches
the contract because the drain goroutine uses the return value
directly to bound `walBuffer.advanceHead(n)`; an uncapped return
could ask the drain to advance past the reservation window the
caller had in scope.

### nil-safety

A nil receiver returns 0. A nil tracker behaves as "all idle"
(safeTail = upperBound). These defaults mirror the foundation-level
nil-safety arguments from `lsnAllocator` ([[0107-0007h]]) and
`walBuffer.writeReserved` ([[0107-0007l]]) and let future `Writer`
constructors leave the publisher unset under `Config.WALBuffers == 0`.

## Lock ordering

The publisher takes no locks. It composes as a leaf reader at the
end of the slice B stripe chain:

```
appendLockSet.lockByProcNum  (one of 8 stripes)
  → insertPosTracker.reserve  (briefly under posMu)
    → insertionTracker.setInsertingAt(stripe, start)
      → walBuffer.writeReserved
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
  → drop stripe lock

(separately, on the drain goroutine, after the above sequence:)
  tailPublisher.publishUpTo(upperBound, insertionTracker)
  walBuffer.advanceHead(published - prior)
```

## Pre-reserve race carry-over

The race window documented in [[0107-0007m]] §"Pre-reserve race" —
between `insertPosTracker.reserve` returning and the matching
`insertionTracker.setInsertingAt(stripe, start)` — temporarily
raises the observed `lowestActiveLSN` above the true minimum. The
publisher cannot close this race by itself; the call-site rewrite
is responsible for either moving `setInsertingAt` under `posMu`
(option A in [[0107-0007m]]) or emitting a pre-reserve marker
(option B). The publisher's contract is "given an honest
`(upperBound, tracker)` pair, compute and monotonically publish the
safe tail."

## PG counterpart

PG does not have a dedicated `tailPublisher` type. The same role is
split across two pieces in `postgres/src/backend/access/transam/
xlog.c`:

  - `WaitXLogInsertionsToFinish(upTo)` walks `WALInsertLock[i].
    insertingAt` and busy-waits until every stripe finishes its
    in-flight insert below `upTo`. After the wait it returns the
    observed watermark.
  - `XLogCtl->LogwrtRqst.Write` (plus a few neighbouring atomics)
    holds the monotonic write-side watermark that flush coordinators
    advance.

goopg merges the two roles because Go's `atomic.Int64.CompareAndSwap`
makes the monotonic-publish loop trivial and because the
wait-vs-poll policy is owned by the caller (the drain loop already
has its own scheduling discipline). The publisher is therefore
non-blocking by construction; if a caller needs wait semantics it
loops on `publishUpTo` itself.

## Tests

`internal/wal/tail_publisher_test.go` — 11 tests:

  - `TestTailPublisherNewIsZero` — fresh publisher load returns 0;
    a no-op publish (upperBound=0, idle tracker) leaves it at 0.
    Guards against accidental non-zero seeding.
  - `TestTailPublisherIdleTrackerPublishesUpperBound` — sentinel
    composition: idle tracker means safeTail = upperBound.
  - `TestTailPublisherActiveStripeCapsSafeTail` — active stripe@600
    with upperBound=1000 caps safeTail at 600; after stripe idle,
    next publish advances to 1000.
  - `TestTailPublisherTakesMinAcrossStripes` — three active stripes
    at 500/300/700; safeTail = 300 (the minimum).
  - `TestTailPublisherMonotonicNeverRegresses` — first publish
    advances to 1000; later publish with active stripe@200 returns
    1000 (no regression).
  - `TestTailPublisherReturnsCurrentWhenCandidateLower` — when
    `candidate ≤ cur` (or `candidate == cur`), the early-return
    path returns the current capped watermark.
  - `TestTailPublisherNilReceiverReturnsZero` — defensive contract.
  - `TestTailPublisherNilTrackerActsAsAllIdle` — transitional
    state during call-site rewrite.
  - `TestTailPublisherAdvancesAcrossSequentialPublishes` — steady-
    state drain pattern: 5 sequential publishes with strictly
    increasing upperBounds advance the watermark in lock-step.
  - `TestTailPublisherConcurrentPublishesAreMonotonic` — 16
    goroutines × 1000 iters each picking a monotonically growing
    upperBound; per-worker return values are non-decreasing,
    final load equals the largest upperBound.
  - `TestTailPublisherConcurrentWithActiveStripes` — 8 stripe
    workers oscillating active/idle while 4 publish workers drive
    publishUpTo with growing upperBounds; per-worker returns are
    capped at upperBound and never regress; post-stop final
    publish reaches 1_000_000.
  - `TestTailPublisherSentinelDoesNotLeakIntoPublishedValue` — at
    upperBound = MaxInt64-1 with idle tracker, the published value
    is MaxInt64-1 (not MaxInt64); pins that the sentinel doesn't
    bleed in via the min.
  - `TestTailPublisherConcurrentCompletesUnderWatchdog` — 5-second
    watchdog on the concurrent stress; if the lock-free design
    ever regresses to live-lock, this surfaces it well before the
    default `go test` timeout.

Verified: `go test -race -count=1 -run 'TestTailPublisher'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.14 s).

## Out of scope

  - Mounting `tailPublisher` on `Writer` and consuming it from the
    drain/flush goroutine (this is multi-loop work — `state.append`
    currently advances `walBuf.tail` synchronously inside the
    `appendMu` critical section; switching to a separate publisher
    requires reordering against the existing `drainBufferBytes` /
    `flushUpTo` invariants).
  - Closing the pre-reserve race ([[0107-0007m]] §"Pre-reserve
    race") — owned by the call-site rewrite.
  - `memRing` mirror handling under stripe-concurrent writes
    (separate slice B foundation: the mirror is currently maintained
    by `state.append` inside `appendMu`; under stripe writers it
    needs either a parallel publication watermark or batching).
  - Drain coordination with concurrent stripe writes
    (`drainBufferBytes` currently runs under `appendMu`; the
    rewrite must let drain run concurrently with stripe writes by
    consuming `published` as the drain ceiling).
  - Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
    dead-code-removed once the call-site converges on
    `insertPosTracker` + `insertionTracker` + `tailPublisher`.
