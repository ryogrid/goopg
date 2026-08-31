# 0107-0007k — WAL insert position tracker (PG-compat prevRecPtr chain)

Status: LANDED (foundation 4 of slice B; dead code until call-site rewrite).

## Context

`docs/design/perf-optimize/07-wal-fsm-insert.md` §2 calls for splitting
`wal.Writer.appendMu` into an 8-stripe `appendLocks [8]paddedMutex`
array, with LSN reservation lifted *out* of the stripe lock so two
different stripes can simultaneously write into disjoint LSN ranges
of the WAL buffer.

PG's counterpart, `ReserveXLogInsertLocation` in
`postgres/src/backend/access/transam/xlog.c`, advances two scalars
under one spinlock:

```c
SpinLockAcquire(&Insert->insertpos_lck);
startbytepos = Insert->CurrBytePos;
endbytepos = startbytepos + size;
prevbytepos = Insert->PrevBytePos;
Insert->CurrBytePos = endbytepos;
Insert->PrevBytePos = startbytepos;
SpinLockRelease(&Insert->insertpos_lck);
```

The (curr, prev) update is *atomic together* — peers must never observe
`curr` past LSN X without `prev` set to the start of the record that
reserved X. That joint atomicity is what the previous foundation
([[0107-0007h]] `lsnAllocator`) intentionally did not provide: that
primitive offers a CAS-fast-path for `next` only, with no prev
tracking, and is suitable for callers that don't need the xl_prev
chain.

The goopg WAL append path needs the chain. `state.tryAppend` in
`internal/wal/writer.go` reads `s.prevRecPtr`, threads it into
`encodeRecordXLog(payload, s.prevRecPtr)`, then updates
`s.prevRecPtr = start - 1` (start is 1-based, the RecPtr stamped into
the next record's `xl_prev` is 0-based). The whole sequence runs
under `state.appendMu`; the entire reason `appendMu` exists is to
keep (writePos, walBuf, memRing, writeLSN, prevRecPtr) mutually
consistent.

Under the future 8-stripe rewrite, the (curr, prev) pair must still
update atomically together, but the byte-copy into the WAL buffer
can happen outside that critical section under whichever stripe lock
the caller holds.

## Decision

Introduce a new primitive `insertPosTracker` in
`internal/wal/insert_pos.go`. Lock geometry mirrors PG's spinlock
pattern:

```go
type insertPosTracker struct {
    posMu          sync.Mutex
    curr           uint64
    prev           uint64
    segSize        uint64
    onCrossSegment func(start, boundary, prev uint64)
}

func (t *insertPosTracker) reserve(size uint64) (start, prev uint64)
```

`reserve` holds `posMu` for exactly the (curr, prev) update — typical
hold time is a handful of nanoseconds. Cross-segment crossings fire
`onCrossSegment` synchronously under `posMu` so the gap
`[gapStart, boundary)` is observed and (e.g.) padded with an
XLOG_NOOP via [[0107-0007j]] `buildSegmentPadRecord` exactly once
per crossing. The reservation that triggered the crossing then
lands at `boundary` with `prev = gapStart` — the pad record's
start — so the xl_prev chain remains unbroken across the boundary.

### Why a mutex instead of a CAS pair?

Two atomics can't be updated atomically together. Options were:

1. **Lock-free packed encoding** — pack (curr, prev) into one 128-bit
   value and CAS it. Go has no native 128-bit CAS; assembly is
   per-arch; portability cost outweighs the saved mutex.
2. **Sequence the prev swap after the curr CAS** — race-prone:
   if reservation A allocates `curr=100` and B allocates `curr=124`,
   B might call `prev.Store(124)` before A calls `prev.Store(100)`,
   and a third reserver C would see `prev=100 < 124`, producing
   a chain that violates `prev < curr` strict-less-than.
3. **A small mutex (this design)** — matches PG. Uncontended cost
   ~10 ns (vs ~2 ns for an atomic CAS). At the per-stripe rate
   (≤ 8 backends ever race here because the stripe locks above
   limit concurrency to 8), contention is bounded and negligible.

PG made the same choice for the same reason; we follow.

### Lock ordering

For the future call-site rewrite, the lock hierarchy is:

```
appendLockSet.lockByProcNum  ← one of 8 stripes, picked by procNum & 7
  → insertPosTracker.reserve  ← always, briefly under posMu
    → (rare, only on segment crossings) onCrossSegment hook,
       e.g. building an XLOG_NOOP pad record via
       buildSegmentPadRecord and emitting it into the WAL buffer.
```

Stripe locks are *above* `posMu`; flush coordination (group commit,
fdatasync) sits outside the stripe locks entirely and is unaffected
by this primitive.

## Coexistence with foundation 1 (`lsnAllocator`)

`lsnAllocator` ([[0107-0007h]]) and `insertPosTracker` are siblings,
not a replacement. `lsnAllocator` offers a CAS-fast-path `reserve` for
callers that do **not** need prev tracking — e.g. a future caller
that pre-allocates LSN ranges in bulk and stamps prev externally.
The WAL append path uses `insertPosTracker`. Both primitives share
the same segment-boundary contract: `0 < size ≤ segSize`, and
crossings fire an `onCross` hook for tail padding.

If a later loop confirms `lsnAllocator` has no consumer, it can be
removed; that's an independent decision and out of scope here.

## Tests

`internal/wal/insert_pos_test.go` covers nine contracts:

1. `TestInsertPosTrackerReserveContiguousMonotonic` — three back-to-
   back reservations yield monotonic starts and the expected chain.
2. `TestInsertPosTrackerReserveStartCurrPrev` — non-zero startCurr
   and startPrev (recovery resume) flow through to the first
   reservation.
3. `TestInsertPosTrackerCrossSegmentInvokesHook` — single crossing
   fires onCross once with `(gapStart, boundary, gapPrev)`; the
   reservation lands at boundary with prev=gapStart.
4. `TestInsertPosTrackerReserveAtExactBoundaryNoHook` — off-by-one:
   a reservation that ends exactly at the boundary stays in the
   fast path; onCross is silent.
5. `TestInsertPosTrackerReserveInvalidSizePanics` — size ∈
   {0, segSize+1, 2·segSize} all panic.
6. `TestInsertPosTrackerNewRejectsZeroSegSize` — constructor panics
   on segSize=0.
7. `TestInsertPosTrackerConcurrentReservesFormChain` — 32 goroutines
   × 100 × 16-byte reservations in a 1 MiB segment; pins (a) starts
   form a contiguous permutation, (b) chain map is self-consistent
   (one prev per start), (c) chain walk from the largest start
   reaches the root through every reservation exactly once.
8. `TestInsertPosTrackerConcurrentCrossSegmentHookOncePerBoundary`
   — 16 goroutines race 40-byte reservations across the same
   256-byte segment boundary; no reservation straddles a boundary;
   hook fires exactly `(lastSeg − firstSeg)` times.
9. `TestInsertPosTrackerCrossSegmentPrevIsCrossingStart` — pins
   the prev-chain integrity across a crossing: the pad record
   inherits the cumulative prev; the reservation that triggered
   the crossing receives prev = the pad record's start.

Verified: `go test -race -count=1 -run 'TestInsertPosTracker'
./internal/wal/` PASS (1.02 s); `go test -race -count=1
./internal/wal/` PASS (3.13 s).

## What this does not do

- **Does not mount anything on `Writer`.** Dead code until the
  call-site rewrite consumes it. Foundation-first pattern matches
  slice C ([[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]] all
  landed before [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]]
  consumed them).
- **Does not split `state.appendMu`'s four invariants.** That
  rewrite splits (writePos, walBuf, memRing, writeLSN) across the
  stripe locks, and is multi-loop scope.
- **Does not implement segment padding.** The onCross hook is a
  callback contract; an actual installation hooks in
  [[0107-0007j]] `buildSegmentPadRecord` plus the call-site's
  WAL-buffer write path. Out of scope for the foundation.
