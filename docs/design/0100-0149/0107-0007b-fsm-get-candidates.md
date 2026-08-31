# 0107-0007b — `FSM.GetCandidates` top-K free-space query (Phase D4 slice C foundation)

Status: accepted (M0107-0007 partial — slice C foundation)
Milestone: M0107-0007 (Phase D4 — WAL insert striping + FSM page distribution)
Parent chapter: [`docs/design/perf-optimize/07-wal-fsm-insert.md`](../../perf-optimize/07-wal-fsm-insert.md) §3

## Summary

Add `(*FSM).GetCandidates(rel, minFreeBytes, n)` returning up to N block
numbers whose registered free-space estimate is ≥ `minFreeBytes`, ordered
by free-space descending. This is one of three primitives the parent
chapter §3 needs to break the c=100 simple-update tail-page convergence:

| § | Primitive                                            | Status                                              |
| - | ---------------------------------------------------- | --------------------------------------------------- |
| 1 | `Pool.SlotPinCount(tag)` (lock-free pin-count probe) | Blocked on M0107-0006 (lock-free bufmap)            |
| 2 | `FSM.GetCandidates(rel, minBytes, n)`                | **This slice.**                                     |
| 3 | `Pool.ExtendRelationBatch(rel, n)`                   | Independent — separate slice                        |

Slice A ([[0107-0007a]]) shipped the 8-stripe per-relation extend lock.
Slice B (WAL insert striping) is deferred — it requires splitting four
invariants now carried by `wal.Writer.state.appendMu` into per-stripe
local state vs. shared state, plus atomic LSN reserve and segment-
boundary `rotateMu`; that is multi-loop scope.

The `FSM.GetCandidates` primitive is independently shippable and the
test suite stands on its own.  It is dead code until the parent §3
executor consumer (`selectInsertPage`) lands, which in turn waits for
the other two primitives; the foundation-first pattern matches the
M0107-0008 shim primitives (Nanotime, PinP, Sema) that landed in
[[0107-0008]] / [[0107-0008b]] / [[0107-0008c]] before their callers
followed in later loops.

## API

```go
// internal/storage/fsm.go
func (f *FSM) GetCandidates(
    rel RelFileNode,
    minFreeBytes uint16,
    n int,
) []BlockNumber
```

Returns up to `n` block numbers whose registered free-space estimate
is `≥ minFreeBytes`, ordered most-free first.  Among ties (equal
free-space estimates) the lowest block number wins.

Returns `nil` when `f == nil`, `n <= 0`, `minFreeBytes == 0`, the
relation has no FSM entries, or no page qualifies.

## Algorithm

The FSM stores `pages[rel] []uint16` indexed by block number; iteration
over this slice is already `O(blocks)` per page.  Top-K extraction runs
in `O(blocks · log K)` worst case via a small insertion-sort buffer of
length `K = n`:

1. For each `(blk, free)` in `pages`, skip if `free < minFreeBytes`.
2. If the kept slice is below capacity, append and bubble into sorted
   position (descending by `free`).
3. Otherwise, compare against the smallest kept entry (`kept[n-1]`):
   skip if not strictly larger; replace and re-bubble if strictly larger.
4. Among ties (`new.free == kept[i].free`), the `>` strict comparison
   preserves first-seen ordering — and the iteration order of the
   pages slice is ascending block number, so ties resolve to lowest
   block number first.

`n` is expected to be small (parent §3 picks `candidatesPerInsert = 4`),
so the kept buffer's bubble step is effectively `O(1)`.  At
`candidatesPerInsert = 4` the per-block hot loop is one comparison
plus an occasional 4-element memmove; the FSM scan dominates and
matches `GetPageWithFreeSpace`'s existing cost profile.

## Concurrency

Lock discipline mirrors `GetPageWithFreeSpace`:

```go
f.mu.RLock()
defer f.mu.RUnlock()
pages := f.pages[key]
// ... read-only iteration over pages ...
```

The method holds `f.mu.RLock` for the entire scan.  The new method does
not mutate any FSM state (`TestFSMGetCandidatesDoesNotMutateState`
pins this); a concurrent `RecordFreeSpace` blocks on the writer
mutex until the scan completes, identical to the existing read path.

The returned `[]BlockNumber` is freshly allocated and owned by the
caller (not aliased into the FSM's internal slice), so no further
locking is required to read it after `GetCandidates` returns.

## Staleness

Like `GetPageWithFreeSpace`, the returned block numbers may be stale:

- Another writer could have consumed the free space between the FSM
  read and the caller's insert attempt.
- The FSM's per-block free-space estimate is updated by
  `RecordFreeSpaceForPage` after a successful insert; it lags the
  page's true free space by one insert when contention is high.

Callers must handle a failed `PageAddItem` gracefully — invalidate
the FSM entry (`f.RecordFreeSpace(rel, blk, 0)`) and retry against
another candidate or extend.  The parent §3 `selectInsertPage` flow
codifies this retry loop: try each candidate in pin-count-best-first
order; fall through to extension only when all candidates either
fail or fall above the `hotPinThreshold = 4` pin-count gate.

## Regression coverage

`internal/storage/fsm_test.go`:

1. **`TestFSMGetCandidatesBasic`** — five blocks with varied free-space
   estimates; asserts top-3 ordering `(2, 3, 0)` against the known
   inputs `(4000, 200, 7000, 5500, 100)`; asserts `n=10` returns all
   qualifying entries; asserts an impossibly-high floor returns nil.
2. **`TestFSMGetCandidatesEdgeCases`** — nil receiver, `n=0`, `n<0`,
   `minFreeBytes=0`, empty relation, and the tie-breaking contract
   (three blocks at equal free-space → ascending block-number return).
3. **`TestFSMGetCandidatesLargeRelation`** — `N = 1000` blocks with a
   deterministic free-space distribution plus a known top-4 outlier
   set; asserts the insertion-sort window correctly extracts top-K
   from a large scan; asserts `minFreeBytes` filtering narrows the
   result set as expected.
4. **`TestFSMGetCandidatesDoesNotMutateState`** — record two blocks,
   call `GetCandidates`, then `GetPageWithFreeSpace`; verifies the
   second call still returns the originally-recorded entry, pinning
   the read-only invariant.

## PG-compat invariants ([[0107-0001-m0106-pg-compat-invariants]])

- On-disk heap page bytes: unchanged.  The FSM is in-memory state;
  `Save`/`Load` are unaffected by this addition.
- WAL record framing: unchanged.
- Catalog tuples: unchanged.
- FSM on-disk format: unchanged.  The header at `Save` writes
  `magic | version | numRels | …`; no field is added or shifted.

## Verification

- `go test -race -count=1 -run 'TestFSMGetCandidates'
  ./internal/storage/` — PASS (1.03 s).
- `go test -race -count=1 ./internal/storage/` — PASS (5.35 s).
- `make ralph-state-guard` — PASS.

The pgbench `c=100 SU TPS ≥ 500` gate from parent §8 belongs to the
M0107-0007 milestone-close suite (after slices B and C land
end-to-end); the foundation primitive on its own does not move that
needle.

## Out of scope (deferred to follow-up slices)

- **Pin-count-aware ranking** — `Pool.SlotPinCount(tag)`, the lock-free
  pin-count probe that ranks `GetCandidates`' return by current pinners,
  is blocked on M0107-0006's lock-free bufmap landing.
- **Batch extension** — `Pool.ExtendRelationBatch(rel, n)` is the third
  parent §3 primitive; an independent slice will add it.
- **Executor consumer** — `selectInsertPage` (parent §3 algorithm) is
  the consumer that wires the three primitives together; it lands
  after all three foundations exist so the call sites in
  `writeHeapRowReturning` / `writeHeapRowReturningPG` flip atomically.
