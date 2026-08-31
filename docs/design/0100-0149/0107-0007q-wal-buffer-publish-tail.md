# 0107-0007q — Phase D4: `walBuffer.publishTail` bytes-side tail-publication primitive (M0107-0007, slice B foundation 10)

Status: accepted
Milestone: M0107-0007 (Phase D4 — 8-stripe WAL insert striping + FSM page distribution)
Parent chapter: [[07-wal-fsm-insert]] §2
Related foundations: [[0107-0007h]], [[0107-0007i]], [[0107-0007j]], [[0107-0007k]], [[0107-0007l]], [[0107-0007m]], [[0107-0007n]], [[0107-0007o]], [[0107-0007p]]

## Problem

Foundation 8 ([[0107-0007o]]) split the in-memory walsender mirror
(`MemRing`) into a reserve/publish pair: writers land bytes via
`WriteReserved` without advancing tail, and a drain goroutine
monotonically advances tail via `PublishUpTo` once it has consulted
[[0107-0007n]] `tailPublisher`. The same publish-after-write
discipline is required for the segment-staging WAL buffer
(`walBuffer`) — foundation 5 ([[0107-0007l]]) already landed
`writeReserved` (the bytes-write half), but the tail-publication
half is missing. Without it the call-site rewrite cannot promote
bytes from "written but invisible" to "drainable" without holding
the global `state.appendMu` that slice B is trying to retire.

The current `walBuffer` advances tail only inside `append` (which
also writes bytes), which is the wrong shape for stripe-concurrent
writers: a stripe holds its own per-stripe lock during `writeReserved`
but the drain goroutine — running outside any stripe lock — owns the
publication watermark.

## API

```go
// internal/wal/wal_buffer_publish_tail.go

func (b *walBuffer) publishTail(safeTail int64) int64
```

Behaviour:

1. If `b == nil`, return 0 (nil-safe convention shared with
   [[0107-0007l]] `walBuffer.writeReserved` and [[0107-0007o]]
   `MemRing.PublishUpTo`).
2. If `safeTail <= b.tail`, return `b.tail` unchanged (monotonic
   no-op — regressing values from a stale `tailPublisher` snapshot
   are silently ignored).
3. Otherwise, set `b.tail = safeTail` and return the new value.

`head` and `base` are NOT touched. Head can only advance via
`advanceHead` (the drain's post-`writeAt` step); `base` slides in
`advanceHead` only.

## Why no head-eviction

Unlike `MemRing.PublishUpTo` — where overflowing `safeTail - head > cap`
advances `head` so the ring evicts the oldest residents — walBuffer
must NOT auto-evict. Its residents represent bytes that have NOT yet
been written to a segment file; evicting them silently would lose
data. The contract therefore requires the caller to drain
(`readForDrain → writeAt → advanceHead`) before any publishTail that
would extend residents past `cap`. Today's Path A satisfies this via
the overflow-drain-then-append sequence in `state.append`; the slice
B call-site rewrite satisfies it by running drain on a dedicated
goroutine and pausing publication when the ring is full.

`TestWALBufferPublishTailDoesNotEvictPendingWrites` pins this
contract: a deliberate caller-side overflow (`publishTail(96)` on a
cap-64 buffer with head=0) leaves `head` at 0 — the primitive does
not paper over the caller's bug.

## Monotonic-store contract

`safeTail` is expected to come from `tailPublisher.publishUpTo`
([[0107-0007n]]), which is itself monotonic. The `<=` check defends
against stale snapshots that callers may hold; under the drain
goroutine's typical pattern (`safeTail := tailPublisher.publishUpTo(...);
walBuffer.publishTail(safeTail)`) a regression cannot happen in
practice, but the no-op is the right response if it does — matches
the equivalent guard in `MemRing.PublishUpTo`.

## Concurrency

This foundation lands the API surface only. `b.tail` remains a plain
`int64` for now; the eventual atomicity upgrade (so a drain
goroutine's `publishTail` and stripe writers' tail readers — via
`resident`, `readForDrain`, `readAt` — can coexist without a data
race) is a separate follow-on foundation, deliberately decoupled to
let the call-site rewrite wire publishTail in lock-step with the
atomic upgrade.

Under today's single-goroutine usage (`state.append` holding
`appendMu`), publishTail is trivially safe — every call is on the
writer goroutine that holds the lock.

The concurrent-scenario test
(`TestWALBufferPublishTailRaceFreeWithDisjointWriters`) drives the
canonical 8-stripe pattern: writers call `writeReserved` at disjoint
LSN ranges, a publisher goroutine drains a request channel and
forwards monotonic max LSNs to `publishTail`. Run under `-race`, the
test surfaces any new data race the primitive may have introduced
(none today — `publishTail` only mutates `b.tail`; `writeReserved`
touches only `b.buf` at disjoint offsets). The publisher goroutine
serialises `publishTail` calls so the test exercises the
publish→read pairing without yet asserting field-level atomicity.

## Lock-ordering tier after foundation 10

```
appendLockSet.lockByProcNum  (one of 8 stripes)
  → insertPosTracker.reserveAndPublish
       posMu held:
         reserveLocked(size)                    (curr, prev) update
         tracker.setInsertingAt(stripe, start)  BEGIN publish
       posMu released
  → walBuffer.writeReserved(start, bytes)       stripe-concurrent write
  → MemRing.WriteReserved(start, bytes)         stripe-concurrent mirror
  → insertionTracker.setInsertingAt(stripe, lsnIdle)  END publish
drop stripe lock

(separately, on drain goroutine, after the above):
  safeTail := tailPublisher.publishUpTo(upperBound, insertionTracker)
  walBuffer.publishTail(safeTail)               ← here (publisher side)
  walBuffer.advanceHead(safeTail - prior)
  MemRing.PublishUpTo(safeTail)
```

`publishTail` sits immediately before `advanceHead` in the drain
chain because `resident()` — which `drainBufferBytes` consults to
size the I/O — derives from `tail - head`, and the drain wants to
see all newly-published bytes before issuing the write batch.

## PG counterpart

PG mirrors this pattern in `postgres/src/backend/access/transam/xlog.c`:
the flush coordinator advances `XLogCtl->LogwrtResult.Write` after
`WaitXLogInsertionsToFinish` returns. Downstream readers (the write
loop, walsender snapshots) consult the published watermark before
issuing reads, so they never observe bytes still in-flight via
`CopyXLogRecordToWAL`. goopg's `walBuffer.publishTail` is the same
"published tail watermark" idea, with the addition that `head`
mutation stays separate (PG batches I/O issuance differently).

## Tests

Ten regression tests in `internal/wal/wal_buffer_publish_tail_test.go`:

- `TestWALBufferPublishTailAdvancesFromBase` — first publish on a
  freshly-reset buffer advances tail and returns it; head/base
  untouched.
- `TestWALBufferPublishTailMonotonicIgnoresRegression` — second
  publish with a lower value is a no-op; return value is the
  existing tail.
- `TestWALBufferPublishTailEqualIsNoop` — boundary case
  `safeTail == tail` is also a no-op (the `<=` guard matches
  MemRing.PublishUpTo).
- `TestWALBufferPublishTailDoesNotMutateHeadBase` — series of
  monotonic publications leaves head/base untouched.
- `TestWALBufferPublishTailNilReceiver` — nil-safe convention shared
  with [[0107-0007l]] / [[0107-0007o]]; returns 0.
- `TestWALBufferPublishTailExposesWriteReservedBytesToReadAt` —
  end-to-end pairing: bytes written via `writeReserved` are
  invisible to `readAt` until `publishTail` covers them. Pins the
  publication-is-the-visibility-edge invariant.
- `TestWALBufferPublishTailMakesResidentTrackTailMinusHead` —
  `resident()` reflects only published bytes; an unpublished
  `writeReserved` leaves `resident()` at zero.
- `TestWALBufferPublishTailComposesWithAdvanceHead` — drain pattern
  `publishTail → readForDrain → advanceHead` interleaves correctly;
  a second cycle confirms publishTail extends from the post-advance
  tail.
- `TestWALBufferPublishTailDoesNotEvictPendingWrites` — deliberate
  caller-side overflow leaves head at 0; the primitive does not
  auto-evict (matches the no-data-loss contract that differs from
  MemRing.PublishUpTo).
- `TestWALBufferPublishTailMonotonicUnderSerialisedAdvances` —
  scripted sequence of monotonic/regressing requests; tail follows
  the cumulative max.
- `TestWALBufferPublishTailRaceFreeWithDisjointWriters` — 8 writers
  × 50 records × 16 bytes via `writeReserved`; a serialiser
  goroutine forwards max-LSN requests to `publishTail`; race-clean
  under `-race`; final `readAt` confirms every record landed in
  the right slot. Body extracted to
  `runPublishTailDisjointWritersScenario` so the watchdog can
  re-run it.
- `TestWALBufferPublishTailWatchdog` — 5-second watchdog on the
  concurrent scenario, surfaces deadlock regressions before the
  package-level timeout (mirrors foundation 7 / 9's pattern).

Verified: `go test -race -count=1 -run 'TestWALBufferPublishTail'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.12 s).

## Out of scope (later slice B foundations and call-site rewrite)

- Upgrading `b.tail` to `atomic.Int64` so a single drain
  goroutine's `publishTail` and stripe writers' tail readers
  (`resident`, `readForDrain`, `readAt`) can coexist without a
  data race. Mechanical but ripples to 5 production sites + the
  existing test that pokes `b.tail` directly; lands in its own
  loop so this foundation's footprint stays minimal.
- Mounting `publishTail` on `Writer` and consuming it from the
  drain/flush goroutine (multi-loop because `state.append`
  currently advances `walBuf.tail` and `memRing.tail` synchronously
  inside `appendMu`; the rewrite splits the four invariants —
  writePos / walBuf / memRing / writeLSN — into per-stripe local
  state vs. shared state).
- Drain coordination with concurrent stripe writes
  (`drainBufferBytes` currently runs under `appendMu` — the rewrite
  must let drain run concurrently with stripe writes by consuming
  `tailPublisher.publishUpTo`'s return as drain ceiling for
  `walBuffer.publishTail` / `walBuffer.advanceHead` /
  `MemRing.PublishUpTo`).
- Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker` + `insertionTracker` + `tailPublisher` +
  `reserveAndPublish` + `publishTail` — `reserve` remains in the
  API as a callable primitive without a tracker.
