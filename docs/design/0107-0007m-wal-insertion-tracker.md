# 0107-0007m — `insertionTracker` (M0107-0007, slice B foundation 6)

Status: accepted (2026-05-21)
Parent: [[07-wal-fsm-insert]] §2 "8-stripe WAL insert locks"; M0107-0007 slice B.
Companions:
[[0107-0007h]] (`lsnAllocator`),
[[0107-0007i]] (`paddedMutex` / `appendLockSet`),
[[0107-0007j]] (`buildSegmentPadRecord`),
[[0107-0007k]] (`insertPosTracker`),
[[0107-0007l]] (`walBuffer.writeReserved`).
PG counterpart: `WALInsertLock[i].insertingAt` and
`WaitXLogInsertionsToFinish` in
`postgres/src/backend/access/transam/xlog.c` (line numbers drift
between minor versions; the symbols are the anchors).

## Problem

Slice B's bytes-write primitive [[0107-0007l]]
(`walBuffer.writeReserved`) deliberately stops short of advancing
`tail`. The design doc spells out why: under concurrent stripe
writes, `tail` cannot safely advance past LSN X until every
reservation strictly below X has had its bytes written into the ring
by its owning stripe.

That requires a publication-aware data structure. PG's solution is
`WALInsertLock[i].insertingAt`: each stripe publishes the start LSN
of the record it is currently writing, and the flush coordinator
calls `WaitXLogInsertionsToFinish(target)` to spin/wait until every
stripe with `insertingAt < target` either advances past `target` or
goes idle.

goopg needs the same primitive — a per-stripe atomic LSN slot — but
not the wait loop (which the call-site rewrite will design once
publication policy is settled). This foundation lands the slot
machinery on its own so it can be exercised in isolation.

## Design

New file `internal/wal/insertion_tracker.go`:

```go
const (
    lsnIdle     = int64(0)
    lsnNoActive = int64(math.MaxInt64)
)

type insertionTracker struct {
    inserting [appendLockStripes]atomic.Int64
}

func newInsertionTracker() *insertionTracker
func (t *insertionTracker) setInsertingAt(stripe int, lsn int64)
func (t *insertionTracker) insertingAt(stripe int) int64
func (t *insertionTracker) lowestActiveLSN() int64
```

### Sentinel choice

- `lsnIdle = 0` exploits the zero value of `atomic.Int64` so
  `newInsertionTracker()` is a no-state-init constructor — the
  foundation pattern (see [[0107-0007h]] `lsnAllocator`,
  [[0107-0007k]] `insertPosTracker`) builds in a constructor anyway
  for future invariants but the zero value is already correct. LSN 0
  is invalid in production (PG's `InvalidXLogRecPtr == 0`), so
  conflating it with "idle" is safe.

- `lsnNoActive = math.MaxInt64` lets the publication walker write the
  computed tail as a single `min`:

  ```go
  safeTail = min(upperBound, tracker.lowestActiveLSN())
  ```

  When every stripe is idle, `lowestActiveLSN` returns `MaxInt64` and
  the `min` collapses to `upperBound`. Without the sentinel, the
  walker would need a branch for the "all idle" case, which is
  exactly the hot path under low concurrency.

### Per-stripe contract (consumed by the future call-site rewrite)

```text
1. take appendLockSet.lockByProcNum(procNum)  // stripe = procNum & 7
2. (start, prev) := insertPosTracker.reserve(size)
3. tracker.setInsertingAt(stripe, int64(start))  // publish active LSN
4. (rare) buildSegmentPadRecord + walBuffer.writeReserved on gap
5. walBuffer.writeReserved(start, recordBytes)
6. tracker.setInsertingAt(stripe, lsnIdle)        // publish idle
7. drop stripe lock
```

The atomic-store at step 3 happens-before the byte writes at
step 5 (program order), and the atomic-store at step 6 happens-
after the byte writes (program order). On the publication side:

- A walker that loads `tracker.insertingAt(i) >= someLSN` is
  guaranteed (sequential-consistency on the per-slot
  `atomic.Int64.Load`) to also observe every byte write that
  step 5 sequenced before step 6 for stripe `i` — i.e., the
  bytes for that record are present in the ring when the slot
  goes idle.

- A walker that loads `tracker.insertingAt(i) == lsnIdle` and
  re-loads `insertPosTracker.curr` afterwards has the joint
  guarantee that for every LSN ≤ curr-at-that-instant, either
  (a) stripe `i` was the writer and the bytes are present, or
  (b) some other stripe is/was the writer (whose state is read
  by the next iteration of the walker's loop).

### Why not couple this into `insertPosTracker`

`insertPosTracker` (step 2 above) holds `posMu` for the joint
`(curr, prev)` advance. If `setInsertingAt` were folded inside
`reserve`, the tracker would need a stripe index parameter and
the LSN reserve would inherit the per-stripe semantics. That
breaks the layering — `insertPosTracker` is a single-writer
abstraction over the LSN axis; the stripe identity is a separate
concern owned by `appendLockSet`. Keeping them split lets each
primitive be unit-tested in isolation and lets future call-site
rewrites compose them differently (e.g., a non-striped fallback
path can reuse `insertPosTracker` without an `insertionTracker`).

### Pre-reserve race (future-loop concern, not in this foundation)

There is a known race window in the per-stripe contract: between
step 2 (`insertPosTracker.reserve` returns) and step 3
(`setInsertingAt(stripe, start)`), the LSN is reserved but the
tracker still reports the stripe as idle. A publication walker
that reads `insertPosTracker.load().curr` in this window would
see "highest reserved LSN ≥ start" while `lowestActiveLSN`
returns `lsnNoActive` (or a higher value from peers) — and could
advance `tail` past `start` even though the bytes for `[start,
start+size)` are not yet written.

Closing this race is the call-site rewrite's responsibility,
not this foundation's. Two well-known closures:

1. **Pre-reserve marker**. Stripe writes a "floor" LSN to its
   slot *before* calling `insertPosTracker.reserve`, using the
   current `curr` value as the floor (monotonicity guarantees
   floor ≤ start). The walker then sees the floor, not `lsnIdle`,
   and stays below it. Refine to exact `start` after reserve
   returns.

2. **Reserve-and-publish hook**. Pass an `onReserve(start)`
   callback into `insertPosTracker.reserve` that fires under
   `posMu` and atomically updates the stripe's tracker slot
   alongside the (curr, prev) advance. This eliminates the race
   entirely but adds posMu-side coupling.

The choice is deferred to the call-site rewrite so it can be
validated under the full WAL append flow rather than designed
around a hypothetical workload. The tracker itself is forward-
compatible with both options (closure 1 just calls
`setInsertingAt(stripe, floor)` before reserve; closure 2 stores
the same way from inside the callback).

## Lock-ordering tier

Future slice B call-site rewrite:

```
appendLockSet.lockByProcNum (one of 8 stripes)
  → insertPosTracker.reserve (briefly under posMu)
    → insertionTracker.setInsertingAt(stripe, start)
      → (rare) buildSegmentPadRecord
      → walBuffer.writeReserved
    → insertionTracker.setInsertingAt(stripe, lsnIdle)
```

`insertionTracker` itself acquires no locks: each stripe writes
only its own slot, and publication walkers Load-only across all
slots. Per-slot `atomic.Int64` is the synchronisation primitive.

## Rejected alternatives

1. **`map[stripeIdx]int64` under a single mutex** — same data,
   but the publication walker would contend with stripe writers
   on the map mutex. Per-slot atomics let stripe writes proceed
   in parallel and let the walker run concurrent with writes.

2. **`[appendLockStripes]int64` without atomics** — would race
   under the publication walker. The Go memory model does not
   provide per-element ordering on a plain int64 array even on
   amd64 (the race detector would flag it; on weaker memory
   platforms the walker would observe torn or stale values).

3. **Embed `inserting` field on `paddedMutex`** — would conflate
   the lock with the publication state. They have different
   readers (the lock is only ever acquired by the stripe; the
   tracker is read by every publication walker) and different
   layouts (the lock needs cache-line padding, the tracker
   benefits from compact `[8]Int64` layout for the walker's
   loop). Keeping them separate matches PG's `WALInsertLockPadded`
   vs `WALInsertLock.insertingAt` split.

4. **Track per-stripe `(start, end)` range instead of just
   `start`** — the publication walker only needs to know "what
   is the lowest active start LSN", because a stripe that holds
   `[start, start+n)` has the entire range pending. Tracking
   `end` would be redundant (the walker computes it from
   `insertPosTracker.curr` if needed) and would double the
   per-slot atomic width.

## Verification

- `go test -race -count=1 -run 'TestInsertionTracker'
  ./internal/wal/` — all 11 tests pass.
- `go test -race -count=1 ./internal/wal/` — full wal package
  green (no regressions in adjacent foundations).

Tests in `internal/wal/insertion_tracker_test.go`:

- `TestInsertionTrackerNewIsAllIdle` — fresh tracker has every
  slot at `lsnIdle` and `lowestActiveLSN == lsnNoActive`. Pins
  the constructor's zero-cost contract.
- `TestInsertionTrackerSetReadback` — single-slot publish then
  read returns the published value; other slots untouched.
- `TestInsertionTrackerSetThenIdleClears` — round-trip
  publish→idle on every stripe; `lowestActiveLSN` returns to
  sentinel after all idle.
- `TestInsertionTrackerLowestActiveLSNAcrossStripes` — three
  active stripes; `lowestActiveLSN` returns the min; clearing
  the lowest shifts the answer; clearing all returns the
  sentinel.
- `TestInsertionTrackerLowestActiveSentinelComposesWithMin` —
  pins the publication-side formula
  `safeTail = min(upperBound, lowestActiveLSN())` works without
  branching for the "no active" case.
- `TestInsertionTrackerSetInsertingAtPanicsOutOfRange` — bad
  stripe indices panic on the write path (catches programmer
  error early, prevents silent neighbour-slot corruption).
- `TestInsertionTrackerInsertingAtPanicsOutOfRange` — same on
  the read path.
- `TestInsertionTrackerConcurrentStripeOwnership` — 8 stripes ×
  5000 iterations each writing only to their own slot;
  per-stripe reads stay within the stripe's emission range;
  race-clean under `-race`.
- `TestInsertionTrackerConcurrentPublicationReader` — 8 writer
  stripes oscillating active↔idle while a publication-walker
  reader observes `lowestActiveLSN`; every non-sentinel
  observation falls inside some stripe's emission range
  (no torn reads, no zero-on-idle bug).
- `TestInsertionTrackerSentinelConstants` — pins `lsnIdle == 0`
  and `lsnNoActive == math.MaxInt64`. Both are load-bearing for
  the constructor's zero-cost path and the publication-side
  `min` composition respectively.

## Out of scope (future slice B foundations)

- The publication walker itself (computing `safeTail` and
  advancing `walBuffer.tail`). Decided once the
  `WaitXLogInsertionsToFinish` equivalent or the
  publishedLSN-atomic policy is settled.
- The pre-reserve race closure (Pre-reserve marker vs
  reserve-and-publish hook). Owned by the call-site rewrite.
- Mounting `insertionTracker` on `Writer` and wiring the
  begin/end pair around the byte write. Multi-loop scope.
- `memRing` mirror handling under stripe-concurrent writes.
- Drain coordination with concurrent stripe writes.
- Deciding whether `lsnAllocator` ([[0107-0007h]]) becomes
  dead-code-removed once the call-site converges on
  `insertPosTracker` + `insertionTracker`.
