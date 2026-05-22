# 0107-0007i — Phase D4: WAL `paddedMutex` + `appendLockSet` (slice B foundation 2)

**Status**: accepted (foundation; not yet wired)
**Milestone**: M0107-0007 — Phase D4 WAL insert striping + FSM page distribution
**Slice**: B, foundation 2 of N
**Parent design**: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](perf-optimize/07-wal-fsm-insert.md) §2

## 1. Scope

Introduce the cache-line-padded mutex type plus the 8-stripe lock set
that the slice B call-site rewrite will mount on `wal.Writer`. Lands
ahead of the call-site rewrite per the slice C foundation pattern
([[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]] all landed before
[[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]] consumed them).

Out of scope (deferred to later slice B work):
- Mounting `appendLockSet` on `Writer` and rewriting `Append` to take a
  stripe lock + `lsnAllocator.reserve` ([[0107-0007h]]) instead of the
  single `state.appendMu`.
- Splitting `state.appendMu`'s four invariants (writePos, walBuf state,
  memRing append, writeLSN advance) into per-stripe local state vs.
  shared state.
- `prevRecPtr` chain integrity under per-stripe locks.

## 2. Design

### 2.1 paddedMutex

```go
// internal/wal/padded_mutex.go
type paddedMutex struct {
    mu sync.Mutex
    _  [56]byte // pad sync.Mutex (8 B) to 64 B (one cache line)
}
```

`sync.Mutex` is 8 B on amd64/arm64; the 56 B trailer brings the total
to 64 B so that an `[8]paddedMutex` array occupies eight distinct cache
lines. Two stripes contending on the same cache line would generate
coherence traffic even when they intend to lock different stripes —
the padding is the design's contention-isolation invariant.

Duplicated from the executor's `paddedMutex` in
`internal/executor/operators_storage.go` (slice A, [[0107-0007a]])
rather than lifted to a shared package: the two stripe arrays sit in
different lock-ordering tiers (heap extend vs. WAL append) and a
shared alias would invite accidental cross-tier coupling.

### 2.2 appendLockSet

```go
const appendLockStripes = 8

type appendLockSet struct {
    locks [appendLockStripes]paddedMutex
}

func stripeForProcNum(procNum int32) int {
    return int(uint32(procNum) & (appendLockStripes - 1))
}

func (s *appendLockSet) lockByProcNum(procNum int32) (unlock func()) {
    stripe := stripeForProcNum(procNum)
    s.locks[stripe].mu.Lock()
    return s.locks[stripe].mu.Unlock
}
```

- `appendLockStripes = 8` matches PG's `NUM_XLOGINSERT_LOCKS = 8` (see
  `postgres/src/include/access/xlog.h` and `xlog.c` symbols
  `ReserveXLogInsertLocation` / `WALInsertLockAcquire`).
- The stripe selector uses the same `procNum &
  (appendLockStripes-1)` formula as the executor's `lockHeapExtend`
  (slice A) and PG's `MyProcNumber % NUM_XLOGINSERT_LOCKS`.
- `stripeForProcNum` is its own function so trace/log decoration can
  read the stripe without re-deriving the mask. The cast through
  `uint32` keeps the result in `[0, 8)` for the full int32 range,
  including the wraparound point and any negative procNums a
  pathological counter could produce.
- `lockByProcNum` returns the bare `sync.Mutex.Unlock` method value
  (no closure allocation; the compiler recognises method values on
  mutex receivers as a zero-allocation pattern). Single-shot — a
  double-call panics, matching the executor's convention.

### 2.3 Lock-ordering tier

The future call-site rewrite will hold:
```
appendLockSet.lockByProcNum(procNum)                   // append
    ↳ (rare) lsnAllocator.rotateMu (segment crossings) // rotate
```
The `rotateMu` from [[0107-0007h]] sits *below* the stripe lock; an
append holds the stripe lock, may dip into `rotateMu` to cross a
segment boundary, then releases both. Two stripes that both cross the
same boundary serialise on `rotateMu`; otherwise they proceed in
parallel.

Flush coordination is unchanged (parent §2 "Flush coordination"):
flush operates on the cumulative buffer content, not per-stripe, and
the group-commit waiter chain merges across all stripes naturally
because LSNs are globally ordered.

## 3. Regression coverage

`internal/wal/padded_mutex_test.go`:

- `TestPaddedMutexSize` — pins `unsafe.Sizeof(paddedMutex{}) == 64` and
  `unsafe.Sizeof(appendLockSet{}) == 64 * appendLockStripes`. Without
  this assertion a future maintainer could shrink `paddedMutex` and
  silently reintroduce false-sharing on adjacent stripes.
- `TestStripeForProcNumMaskedByStripes` — table-driven across
  procNums `{0, 1, 7, 8, 15, 16, -1, -8, INT32_MAX, INT32_MIN}` to
  pin the `& 0x7` formula and its uint32 cast.
- `TestAppendLockSetStripesByProcNum` — drives 8 goroutines on
  procNums `0..7` and observes peak in-CS concurrency == 8. The
  pre-slice-B single-mutex baseline would cap peak at 1.
- `TestAppendLockSetCollidesOnSameStripe` — procNum 3 and procNum 11
  (both hash to stripe 3) serialise (peak == 1). This is what makes
  the 8-stripe cap real rather than nominal — without the modulo,
  any per-backend lock would pass `…StripesByProcNum`.
- `TestAppendLockSetUnlockClosureReleasesStripe` — pins the
  single-shot semantics of the returned unlock by re-acquiring the
  same stripe from a peer goroutine immediately after unlock; a
  500 ms watchdog catches a leak.

Verified: `go test -race -count=1 -run 'TestPaddedMutex|TestStripeForProcNum|TestAppendLockSet' ./internal/wal/` PASS (1.05 s);
`go test -race -count=1 ./internal/wal/` PASS (3.11 s).

## 4. PG-compat impact

None for this foundation — purely an in-memory primitive. The WAL
record format, segment file layout, control file, catalog, and wire
protocol are unchanged. The byte stream a future striped Writer
produces under a given workload will be identical to the pre-stripe
Writer's byte stream modulo per-record stripe ordering — but that
divergence belongs to the call-site rewrite, gated by the parent
milestone's WAL byte-diff integration test.

## 5. Cross-references

- Parent chapter: [[07-wal-fsm-insert]] §2.
- Slice A landed: [[0107-0007a]] (executor's `paddedMutex` / `heapExtendLockSet`).
- Slice B foundation 1: [[0107-0007h]] (`lsnAllocator`).
- Slice C landed: [[0107-0007b]] / [[0107-0007c]] / [[0107-0007d]] foundations + [[0107-0007e]] / [[0107-0007f]] / [[0107-0007g]] consumers.
- PG counterpart: `postgres/src/backend/access/transam/xlog.c` —
  `WALInsertLockAcquire`, `ReserveXLogInsertLocation`,
  `NUM_XLOGINSERT_LOCKS` in `postgres/src/include/access/xlog.h`.
