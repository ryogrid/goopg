# 0107-0007a — Heap-extend lock striping (Phase D4 slice A)

Status: accepted (M0107-0007 partial — slice A)
Milestone: M0107-0007 (Phase D4 — WAL insert striping + FSM page distribution)
Parent chapter: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](../../perf-optimize/07-wal-fsm-insert.md) §4

## Summary

Replace `internal/executor/operators_storage.go`'s single per-relation
`sync.Mutex` (`heapExtendLocks sync.Map → *sync.Mutex`) with an 8-stripe
`heapExtendLockSet` (`[8]paddedMutex`). A backend extending a relation
picks `set.locks[procNum & 0x7]`, allowing up to 8 parallel extenders per
relation. PG counterpart: parent chapter §4 references `RelationExtensionLock`
as PG's single per-relation lock; goopg adds 8-way striping because Go
mutexes are heavier than PG LWLocks.

## Why slice A first

Parent chapter §2–§4 describes three contention surfaces:

| §  | Surface                            | Coupling                                |
| -- | ---------------------------------- | --------------------------------------- |
| §2 | 8-stripe WAL insert locks          | Requires splitting `wal.Writer.appendMu` invariants (writePos / walBuf / memRing / writeLSN must remain coherent per-record), plus atomic LSN reserve, plus `rotateMu` for segment-boundary CAS. Multi-loop. |
| §3 | FSM-driven page selection          | Requires `Pool.SlotPinCount(tag)` from [[0107-0006]] (lock-free bufmap consultation) + `FSM.GetCandidates` extension + batched `Pool.ExtendRelationBatch`. Multi-loop. |
| §4 | 8-stripe per-relation extend lock  | Self-contained: one variable, one function, two callers. **This slice.** |

Slice A is independently shippable and revertible per
[`docs/design/perf-optimize/09-migration-and-rollout.md`](../../perf-optimize/09-migration-and-rollout.md)
§9. It is also pre-requisite-free: `ctx.ProcNum` is already plumbed end-
to-end (`internal/executor/context.go:128`, set in `serveConn` per
M0107-0004); no struct layout change; no on-disk format touch; no WAL
record byte change.

## Concurrency-safety argument

The previous comment at the call site said:

> Serialise relation extension so concurrent writers don't race on
> PinNew and corrupt pin accounting for the freshly-grown tail block
> under heavy insert workloads.

That hazard no longer exists in the current `storage.Pool.PinNew`
(`internal/storage/bufpool.go:638`):

1. `pinMu.Lock()` is held while `claimVictim`/`evictVictim` runs.
2. `pinMu.Unlock()` is dropped for the disk I/O window (`InitPage` +
   `mgr.Extend`); `storage.Manager.Extend` is the authoritative block-
   number allocator — two concurrent callers get **distinct** block
   numbers, not the same one.
3. `pinMu.Lock()` is re-acquired to publish the slot's `tag`, set
   `slotValidBit|slotDirtyBit|pin=1|gen`, and `bm.Insert(tag, victimIdx,
   gen)`.
4. If `bm.Insert` reports a collision (another goroutine published the
   same tag while we were in `Extend`), the recovery path tries
   `bm.Lookup(tag)` and re-pins the winner's slot. Our local slot is
   reset to the empty state.

That is correct under any number of concurrent `PinNew` callers from
distinct stripes. The single per-relation extend mutex was therefore a
contention-reduction tool ("don't waste churn extending in parallel"),
not a correctness primitive. 8-stripe striping retains the same
contention-reduction property at higher concurrency: a relation
receiving inserts from N backends spreads them across `min(N, 8)`
parallel extension events instead of serialising on a single lock.

The new tail blocks created by parallel extenders are different blocks;
subsequent inserts spread across them naturally via the existing FSM
recording (`ctx.FSM.RecordFreeSpaceForPage(rel, blk, slot.Page())` at
`operators_storage.go:3139`).

## Data structure

```go
// paddedMutex is a 64-byte cache-line-padded mutex. Lined up in an array,
// adjacent stripes occupy distinct cache lines so contending writers do
// not pay coherence traffic on a stripe they did not intend to lock.
type paddedMutex struct {
    mu sync.Mutex
    _  [56]byte // pad sync.Mutex (8 B) to 64 B (one cache line)
}

const heapExtendLockStripes = 8

type heapExtendLockSet struct {
    locks [heapExtendLockStripes]paddedMutex
}

var heapExtendLocks sync.Map // map[storage.RelFileNode]*heapExtendLockSet

func lockHeapExtend(rel storage.RelFileNode, procNum int32) func() {
    v, _ := heapExtendLocks.LoadOrStore(rel, &heapExtendLockSet{})
    set := v.(*heapExtendLockSet)
    mu := &set.locks[uint32(procNum)&(heapExtendLockStripes-1)].mu
    mu.Lock()
    return mu.Unlock
}
```

`sync.Mutex` is 8 B on Linux/amd64 Go 1.25 (`state int32 + sema uint32`);
the explicit `[56]byte` pad rounds the struct to 64 B. The padding is
asserted at compile time via `unsafe.Sizeof(paddedMutex{}) == 64` in
the regression test below.

`uint32(procNum) & 7` (vs `procNum & 7` direct) is defensive: `procNum`
is an `int32` and the bitmask result must be unsigned for array
indexing — a negative `procNum` (background workers may pass −1 in
some call sites; PG's `INVALID_PROC_NUMBER` analog) would underflow
the array index otherwise.

## Call sites

Two: both in `internal/executor/operators_storage.go`,
`writeHeapRowReturning` and `writeHeapRowReturningPG`. Each one already
has `ctx *Context` in scope (passes `ctx.ProcNum`). No other caller of
`lockHeapExtend` exists in the tree (verified via Serena
`find_referencing_symbols`).

## Regression coverage

`internal/executor/extend_lock_stripe_test.go`:

1. **`TestPaddedMutexSize`** — `unsafe.Sizeof(paddedMutex{}) == 64` and
   `unsafe.Sizeof(heapExtendLockSet{}) == 64 * 8 == 512`. Catches a
   future struct-layout regression that would silently re-introduce
   false sharing across stripes.
2. **`TestLockHeapExtendStripesByProcNum`** — eight goroutines, each
   holding a stripe `i ∈ [0, 8)`, all simultaneously in the critical
   section. Peak concurrent extenders must equal 8. Under the previous
   single-mutex implementation peak would be 1; the test would hang
   waiting for the second acquisition.
3. **`TestLockHeapExtendCollidesOnSameStripe`** — procNum 0 and
   procNum 8 hash to the same stripe (`8 & 7 == 0`). With one holder
   alive, a second acquisition on the same stripe must remain parked.
   Catches a degenerate "no locking" implementation that the previous
   test alone would not.

The existing `writeHeapRowReturning`/`writeHeapRowReturningPG` callers
are exercised end-to-end by every INSERT-bearing test in the executor
package (the `concurrent_update_xmax_test.go` family, the
`insert_*_test.go` family, the isolation suite at
`internal/testport/`); the existing run of `go test -race
./internal/executor/...` continues to pass under the striped
implementation.

## Out of scope (deferred to later slices of M0107-0007)

- **Slice B** — 8-stripe `wal.Writer.appendLocks` per parent §2. Needs
  splitting the four invariants currently carried by `state.appendMu`
  (writePos, walBuf state, memRing append, writeLSN advance) into per-
  stripe local state vs. shared state, plus atomic `nextLSN.Add` and
  segment-boundary `rotateMu`.
- **Slice C** — FSM-driven page selection per parent §3. Requires
  `Pool.SlotPinCount(tag)` API ([[0107-0006]] consumer),
  `FSM.GetCandidates(rel, minBytes, n)`, and
  `Pool.ExtendRelationBatch(rel, n)`.
- **Pin-count-aware page ranking** — currently extension only triggers
  on FSM-miss + tail-full, identical to pre-refactor flow. Slice C
  adds the `hotPinThreshold = 4` rule.
- **Batch extension** — extending one page at a time, not 8. Slice C.

## PG-compat invariants ([[0107-0001-m0106-pg-compat-invariants]])

- On-disk heap page bytes: unchanged. The striping only affects which
  goroutine is the next extender; the bytes emitted by
  `storage.PageAddHeapTuple` + WAL `XLOG_HEAP_INSERT` are identical.
- WAL record framing: unchanged. The stripe lock guards no WAL code.
- Catalog tuples: unchanged.
- `TestE2E_FailoverGoopgToPG/async`: not exercised by this slice (the
  byte-emitter path is untouched). Will be re-run as part of the
  M0107-0007 milestone-close gate.

## Verification

- `go test -race -count=1 -run 'TestPaddedMutex|TestLockHeapExtend'
  ./internal/executor/` — PASS (1.08 s).
- `go test -race -count=1 ./internal/executor/` — PASS (re-run as part
  of this slice's landing).
- `make ralph-state-guard` — PASS.

The pgbench `c=100 SU TPS ≥ 500` gate from parent §8 belongs to the
M0107-0007 milestone-close suite (after slices B and C land); slice A
alone does not move that needle because extension is already infrequent
relative to insert-into-existing-page.
