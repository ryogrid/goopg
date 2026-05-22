# 0107-0007p — Phase D4: `insertPosTracker.reserveAndPublish` joint-atomic reserve + stripe-publish (M0107-0007, slice B foundation 9)

Status: accepted
Milestone: M0107-0007 (Phase D4 — 8-stripe WAL insert striping + FSM page distribution)
Parent chapter: [[07-wal-fsm-insert]] §2
Related foundations: [[0107-0007h]], [[0107-0007i]], [[0107-0007j]], [[0107-0007k]], [[0107-0007l]], [[0107-0007m]], [[0107-0007n]], [[0107-0007o]]

## Problem

The slice B per-stripe insert contract sketched in [[0107-0007m]] is:

```
1. take appendLockSet.lockByProcNum(procNum)
2. start, prev := insertPosTracker.reserve(size)        // advances curr
3. insertionTracker.setInsertingAt(stripe, start)       // publishes stripe slot
4. walBuffer.writeReserved(start, bytes)
5. (optionally) MemRing.WriteReserved(start, bytes)
6. insertionTracker.setInsertingAt(stripe, lsnIdle)
7. drop stripe lock
```

Between steps 2 and 3 there is a race window. A drain reader that calls
`tailPublisher.publishUpTo(upperBound, tracker)` at this moment observes
`lowestActiveLSN == lsnNoActive` for the in-flight reservation (because
the stripe slot is still idle) and may advance the published watermark
past the reservation's still-being-written bytes.

PG closes this race in `xlog.c` by holding the WAL insert lock (the
stripe lock) across both `ReserveXLogInsertLocation` and
`WALInsertLockUpdateInsertingAt`. goopg's stripe lock is held at the
call-site layer, but the primitive layer below (between
`insertPosTracker` and `insertionTracker`) treats the two updates as
independent — opening the race within the primitives.

## Closure (option A from [[0107-0007m]] §"Pre-reserve race")

Move the publication step under `posMu` so the (curr, prev) update and
the `setInsertingAt(stripe, start)` publish appear to all observers as
one indivisible action. The closure point is the `posMu.Unlock` at the
end of `reserveAndPublish`: any thread that subsequently acquires `posMu`
(notably `insertPosTracker.load`, which a drain reader uses to obtain
`upperBound`) observes both the advanced `curr` and the published
`insertionTracker[stripe]` together.

Option B (pre-reserve marker — write a floor LSN before calling `reserve`,
then refine after) was considered and rejected: it requires the caller to
know `curr` ahead of time (defeating the point of having `posMu` be
authoritative over `curr`), and adds a second atomic write per insert
where option A adds zero (the `setInsertingAt` happens regardless of
which option is chosen; option A just moves it inside `posMu`).

## API

```go
// internal/wal/insert_pos_publish.go

func (t *insertPosTracker) reserveAndPublish(
    size uint64, stripe int, tracker *insertionTracker,
) (start, prev uint64)
```

Contract:

- `size` must satisfy `0 < size <= segSize`. Matches `reserve`.
- `tracker` MUST be non-nil. If the call site has no insertion tracker
  (e.g. legacy non-striped path), it calls `reserve` instead. Panics on
  nil rather than silently skipping publication — silent degradation
  would defeat the foundation's purpose.
- `stripe` must satisfy `0 <= stripe < appendLockStripes`. Matches
  `insertionTracker.setInsertingAt`'s panic semantics.

Behaviour (single posMu critical section):

1. Validate inputs (outside posMu; cheap, fail-fast on programmer error).
2. Take `posMu`.
3. Call `reserveLocked(size)` — the existing reservation logic refactored
   out of `reserve` into a shared helper. The (curr, prev) update and any
   `onCrossSegment` hook fire as before.
4. Call `tracker.setInsertingAt(stripe, int64(start))` — sequenced after
   the curr update.
5. Release `posMu`.

The END `setInsertingAt(stripe, lsnIdle)` is **NOT** part of this
primitive — it remains a separate call by the caller, sequenced after
the byte write. Closing the race only requires sealing the BEGIN side
under `posMu`; the END side has a natural happens-before chain through
the stripe lock that the caller holds across the entire insert.

## Cross-segment crossings

The published stripe value is the **new reservation's start** (the
post-boundary LSN), NOT the gap's start. The pad record at
`[gap, boundary)` is fed via the `onCrossSegment` hook (which the caller
drains by writing the pad record's bytes into the buffer); it is a
gap-fill emitted synchronously under `posMu`, not a stripe reservation.
The reservation that triggered the crossing lands at `boundary` with
`prev = gap`, and that LSN is what the stripe slot publishes — matching
PG's behaviour.

## Refactor: extracted `reserveLocked`

`insertPosTracker.reserve` is refactored to call a private
`reserveLocked(size)` helper that assumes `posMu` is held. Both `reserve`
and `reserveAndPublish` call `reserveLocked`, so the joint-atomicity
rule (the (curr, prev) update happens together with the optional hook)
lives in exactly one place. No behavioural change to existing `reserve`
callers; the test suite for `reserve` still passes verbatim.

## Cost

One additional `atomic.Int64.Store` under the existing posMu critical
section. The Store does not extend the critical section meaningfully
(a single instruction on amd64/arm64); the cost is dominated by the
existing posMu Lock/Unlock pair. No new locks introduced.

## Tests

Eight regression tests in `internal/wal/insert_pos_publish_test.go`:

- `TestInsertPosTrackerReserveAndPublishBasic` — single reservation
  publishes to the named stripe, other stripes remain idle.
- `TestInsertPosTrackerReserveAndPublishMultiStripe` — three
  reservations on distinct stripes populate independent slots;
  `lowestActiveLSN` returns the minimum.
- `TestInsertPosTrackerReserveAndPublishCrossSegmentPublishesNewStart`
  — the published stripe value is the post-boundary reservation
  start, not the pad-record start; the onCross hook still fires with
  the expected `(gap, boundary, gapPrev)` payload.
- `TestInsertPosTrackerReserveAndPublishInvalidSizePanics` —
  `{0, segSize+1, 2·segSize}` panic.
- `TestInsertPosTrackerReserveAndPublishNilTrackerPanics` — nil
  tracker panics (no silent degradation).
- `TestInsertPosTrackerReserveAndPublishInvalidStripePanics` — bad
  stripe indices on the write path panic.
- `TestInsertPosTrackerReserveAndPublishInteropWithReserve` — mixing
  `reserve` and `reserveAndPublish` on the same tracker preserves
  (curr, prev) chain integrity; the tracker only reflects the
  `reserveAndPublish` calls.
- `TestInsertPosTrackerReserveAndPublishConcurrentChain` — 32
  goroutines × 100 × 16 B; starts form a contiguous permutation;
  every goroutine pairs its `reserveAndPublish` with a matching
  `setInsertingAt(idle)`, so `lowestActiveLSN` returns to the sentinel
  at the end.
- `TestInsertPosTrackerReserveAndPublishConsistentSnapshot` — the
  main race-closure test. 8 writers × 2000 reservations. A reader
  takes `posMu` directly (legal in-package), reads `curr` and every
  stripe slot inside the critical section, asserts every non-idle
  slot value v satisfies `v < curr` and `(v - startCurr) % size == 0`.
  Under the old un-coupled reserve + setInsertingAt sequence the
  reader would observe `curr advanced + stripe still idle` for an
  in-flight reservation, breaking the invariant. With
  `reserveAndPublish`, the BEGIN edge is sealed under `posMu` so the
  snapshot is consistent.
- `TestInsertPosTrackerReserveAndPublishWatchdog` — 5-second watchdog
  on the concurrent race-closure scenario; a regression that
  deadlocks (e.g. by inverting lock order or nesting `posMu` inside
  the stripe mutex) surfaces here rather than via the package-level
  test timeout.

Verified: `go test -race -count=1 -run 'TestInsertPosTracker'
./internal/wal/` PASS (1.04 s); `go test -race -count=1
./internal/wal/` PASS (3.15 s).

## Lock-ordering tier after foundation 9

```
appendLockSet.lockByProcNum  (one of 8 stripes)
  → insertPosTracker.reserveAndPublish
       posMu held:
         reserveLocked(size)                   (curr, prev) update
         tracker.setInsertingAt(stripe, start) BEGIN publish
       posMu released
  → walBuffer.writeReserved(start, bytes)      stripe-concurrent write
  → MemRing.WriteReserved(start, bytes)        stripe-concurrent mirror
  → insertionTracker.setInsertingAt(stripe, lsnIdle)  END publish
drop stripe lock

(separately on drain goroutine, after the above):
  tailPublisher.publishUpTo(upperBound, insertionTracker)
  walBuffer.advanceHead(published - prior)
  MemRing.PublishUpTo(published)
```

## PG-compat

None. In-memory primitive; WAL record / file format / catalog / wire
unchanged. The lock geometry mirrors PG's: `ReserveXLogInsertLocation`
+ `WALInsertLockUpdateInsertingAt` together under the WAL insert lock
in `postgres/src/backend/access/transam/xlog.c`.

## Out of scope (later slice B foundations and call-site rewrite)

- Mounting `reserveAndPublish` on `Writer` and rewriting `state.append`
  to consume it (multi-loop work because `state.append` currently
  advances `walBuf.tail` and `memRing.tail` synchronously inside
  `appendMu`; the rewrite splits the four invariants — writePos /
  walBuf / memRing / writeLSN — into per-stripe local state vs. shared
  state).
- The END-edge race (publishing idle): the END `setInsertingAt(idle)`
  is intentionally outside `posMu` because the END only needs the
  stripe lock for synchronisation; the publication-walker invariant
  the END must respect is "no observation of lsnIdle without observing
  the preceding byte writes," which the atomic Load on the slot
  already provides under Go's memory model. No further sealing needed
  on the END side.
- Drain coordination with concurrent stripe writes (`drainBufferBytes`
  currently runs under `appendMu` — the rewrite must let drain run
  concurrently with stripe writes by consuming `tailPublisher.publishUpTo`'s
  return as drain ceiling for both `walBuffer.advanceHead` and
  `MemRing.PublishUpTo`).
- Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
  dead-code-removed once the call-site converges on `insertPosTracker`
  + `insertionTracker` + `tailPublisher` + `reserveAndPublish` — `reserve`
  remains in the API as a callable primitive without a tracker.
