# EX2-03 — pool sizing: measure-only close-out (no code change justified)

```
label: EX2-03-measure | date: 2026-09-04
method: unit/micro (AllocsPerRun + -bench -benchtime=300000x -count=3);
  throwaway test created, run, DELETED (no stray files)
```

## Measured numbers

| arm | w=7 | w=16 |
|---|---|---|
| pool hit (acquire+release cycle) | 1 alloc/op, 24 B/op, ~39 ns | 1 alloc/op, 24 B/op, ~45 ns |
| acquire-never-release (A12/C17 miss) | 2.0 allocs/op | 2.0 allocs/op |
| plain `make(Row, w)` | 1 alloc/op, 352 B/op, ~78–82 ns | 1 alloc/op, 896 B/op, ~209–231 ns |

- A hit never reaches 0 allocs: `Put(row any)` boxes the slice header
  (1× 24 B/cycle). `releaseRow` zeroes + `acquireRow` re-zeroes
  (~0.7 KB memset at w7, noise in the ~40 ns cycle).
- Return discipline (13 acquires / 10 releases, static census): all 10
  releases pair 1:1 with a lifetime acquire; drops by rule (nil,
  width 0, width >64 — unreachable, TPC-H tops ~50 vs cap 64).
- Per-row pool-hit rate ≈ 0% BY CONSTRUCTION: retained buffers (all
  C-family dsts, A12 scratch, resultOp overwrite) never return — and
  correctly so (pooling retained rows = pure overhead, zero reuse).
- Hottest Q9 path `ownedBuildRow` (~6 M rows) bypasses the pool
  entirely (`make` non-arena lane, `operators_join_agg.go:935`).

## Verdict

Already optimal. Per-width buckets 0–64 pool every width identically,
so P4-01's 10→7 / 18→7 only moves traffic between identical buckets.
Predicted effect on the EX0-04 clone slice (Q9 21.4%): ~0 — that slice
lives in `ownedBuildRow(make)` + `MaterializeArena` + arena-path
`cloneRowOwned`, none of which consult bucket sizing. Closed as
measure-only per "Measure, do not guess". Revisit only if a later
alloc arm shows a pool residual (same rule as EX1-04 Cut 1).

Side note: full-suite `-bench` surfaced 2 order-pollution failures
(`TestPgGetSerialSequenceSerialColumn`,
`TestPgStatGetXactTuplesInsertedSQL`) — both pass in isolation,
pre-existing, unrelated.
