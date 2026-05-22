# 0107-0007e — `selectFSMCandidatePage` heap-insert page selection helper

Milestone: M0107-0007 (Phase D4: WAL insert striping + FSM page
distribution) — slice C executor consumer foundation.

Parent design: `docs/design/perf-optimize/07-wal-fsm-insert.md` §3
(FSM-driven page selection — `selectInsertPage` pseudocode).

## Why

The c=100 simple-update livelock observed in
`analysis/perf-optimize/04-contention.md` §4.4 traced back to all 100
backends converging on `pgbench_history`'s tail page: the bufpool
partition lock serialised lookup, then the per-slot content lock
serialised the actual insert. Even after extension, all writers
retargeted the new tail page and the queue re-formed.

The parent design's fix is two-part:

1. Replace the deterministic "FSM-or-tail" target with an FSM top-K
   query, then rank candidates by current pin count, biasing each
   inserter toward a page that nobody else is touching.
2. When all candidates are hot, batch-extend N pages so the next
   inserters find FSM-registered cold pages.

Slice C foundation 1 (`FSM.GetCandidates`), foundation 2
(`Pool.ExtendRelationBatch`), and foundation 3 (`Pool.SlotPinCount`)
landed the three primitives. This loop lands the **selection helper**
that combines (1) — the read-only FSM-query + pin-count-ranking step.
The call-site rewrite in `writeHeapRowReturning` and
`writeHeapRowReturningPG`, plus the batched-extend fallback, lands in
the next loop together with the PG-compat WAL byte-diff gate from the
parent milestone.

Decoupling selection from the call-site rewrite keeps two large changes
independent: selection is pure (no on-disk side-effects, no WAL emission)
so it is unit-testable in isolation; the call-site rewrite touches the
heap-insert hot path and must clear the byte-regression gate.

## What landed

`internal/executor/heap_insert_select.go`:

```go
const candidatesPerInsert = 4
const hotPinThreshold     = 4

func selectFSMCandidatePage(
    fsm *storage.FSM,
    pool *storage.Pool,
    rel storage.RelFileNode,
    minFreeBytes uint16,
) (storage.BlockNumber, bool) {
    if fsm == nil || pool == nil {
        return 0, false
    }
    candidates := fsm.GetCandidates(rel, minFreeBytes, candidatesPerInsert)
    if len(candidates) == 0 {
        return 0, false
    }
    var (
        best     storage.BlockNumber
        bestPin  int32 = math.MaxInt32
        haveBest bool
    )
    for _, blk := range candidates {
        pin := pool.SlotPinCount(storage.BufferTag{Rel: rel, Block: blk})
        if pin < bestPin {
            bestPin = pin
            best = blk
            haveBest = true
            if pin == 0 {
                break
            }
        }
    }
    if !haveBest || bestPin >= hotPinThreshold {
        return 0, false
    }
    return best, true
}
```

### Behaviour

- **Nil-safe.** Returns `(0, false)` when either `fsm` or `pool` is
  nil. The heap-insert path may run before the FSM is initialised (e.g.
  catalog bootstrap before `pg_init`), so the helper must not panic
  there.
- **FSM-driven.** Asks `FSM.GetCandidates` for up to four pages with at
  least `minFreeBytes` free, ordered by free-space desc with block-asc
  tie-break (deterministic). Worst-case work per insert is four
  `bufmap.Lookup` + four `state.Load` — both lock-free and ~5 ns each.
- **Pin-count ranked.** Picks the candidate with the lowest current pin
  count. Short-circuits on the first pin-0 candidate so the common case
  (an idle page in the top-K) costs one `bufmap.Lookup`.
- **Fall-through signal.** Returns `(0, false)` when every candidate's
  pin count is `>= hotPinThreshold` (4). The caller interprets this as
  "all FSM candidates are hot" and falls through to the heap-extend
  path; otherwise the inserter would re-pin a contended page and join
  the queue we are trying to escape.

### Why constants live here, not in storage

`candidatesPerInsert` and `hotPinThreshold` are heap-insert policy, not
FSM or bufpool policy. Keeping them in `internal/executor` means future
re-tuning (e.g. raising `hotPinThreshold` to 8 once batched extension
spreads inserters further) does not need to ripple into the storage
package's API surface.

### Lock discipline

No locks are held across the helper's body. `FSM.GetCandidates` takes
`f.mu.RLock` for its scan and returns a fresh slice. `Pool.SlotPinCount`
is lock-free (seqlock `bufmap.Lookup` + `atomic.Uint64.Load`). No buffer
pin is acquired — the returned block may be stale (another writer can
fill it before our caller pins), which is already tolerated by the
existing `PageAddItem` retry + FSM invalidation in
`writeHeapRowReturning`.

## What did NOT land this loop

- The call-site rewrite of `writeHeapRowReturning` and
  `writeHeapRowReturningPG` — replacing the current FSM/tail/extend
  cascade with `selectFSMCandidatePage` + `Pool.ExtendRelationBatch` +
  FSM `RecordFreeSpace` per added page. Defers to the next loop, which
  also lands the WAL byte-diff gate from the parent milestone (the
  on-disk WAL record format must remain identical pre/post the
  call-site change).
- The 8-stripe `wal.Writer.appendLocks` (parent §2). Splitting
  `state.appendMu`'s four invariants (writePos, walBuf state, memRing
  append, writeLSN advance) into per-stripe local state vs. shared
  state is multi-loop scope; deferred as slice B.

## Tests

Five regression tests in `internal/executor/heap_insert_select_test.go`,
all using a real `storage.Manager` + `storage.Pool` + `storage.FSM`
fixture (no mocks):

- `TestSelectFSMCandidatePageNilInputs` — `(nil, pool, …)` and
  `(fsm, nil, …)` both return `(0, false)` without panicking.
- `TestSelectFSMCandidatePageEmptyFSM` — `minFreeBytes` higher than every
  recorded entry returns `(0, false)` (fall-through signal).
- `TestSelectFSMCandidatePageRanksByPinCount` — three FSM entries with
  ample free space, pin counts 2/0/1 → selects block 1. Without
  pin-count ranking the helper would return the first candidate.
- `TestSelectFSMCandidatePageShortCircuitsOnPinZero` — four FSM entries;
  block 0 unpinned, block 1 pinned five times (over `hotPinThreshold`).
  The helper returns block 0 deterministically (GetCandidates ties
  break to lowest block first; block 0 is the first pin-0 hit).
- `TestSelectFSMCandidatePageRejectsHotCandidates` — four FSM entries,
  every block pinned `hotPinThreshold` times (4). The helper returns
  `(0, false)` so the caller falls through to extension; without this
  the caller would pick a hot page and contend.
- `TestSelectFSMCandidatePagePicksAmongModeratelyPinned` — three FSM
  entries with pin counts 3/1/2 (all below `hotPinThreshold`) → selects
  the pin=1 block. Pins the "lower is better" ranking independent of
  the pin-0 short-circuit.

Verified: `go test -race -count=1 -run 'TestSelectFSMCandidatePage'
./internal/executor/` PASS (1.03 s); `go test -race -count=1
./internal/executor/` PASS (2.79 s).

## PG counterpart

PG has FSM-driven page selection in `freespace.c::GetPageWithFreeSpace`
but no pin-count ranking: it relies on FSM freshness (autovacuum updates
FSM frequently) plus batched extension via
`RelationGetBufferForTuple`. goopg adds pin-count ranking because (a)
the lock-free buf-mapping table from M0107-0006 makes the probe cheap
(~5 ns vs PG's lwlock-protected lookup), and (b) goopg's autovacuum is
less mature so FSM staleness bites harder.

## File map

- `internal/executor/heap_insert_select.go` — helper + constants.
- `internal/executor/heap_insert_select_test.go` — five regression
  tests with real Manager/Pool/FSM fixture.

## Dependencies

- M0107-0006 loops 1-3 (lock-free bufmap) — `Pool.SlotPinCount` consumed
  by this helper depends on the lock-free `bufmap.Lookup` landed there.
- M0107-0007 slice C foundations 1-3 (`FSM.GetCandidates`,
  `Pool.ExtendRelationBatch`, `Pool.SlotPinCount`) — all three are now
  in tree; this helper consumes (1) and (3). Foundation (2) is consumed
  by the upcoming call-site rewrite.
