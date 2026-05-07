# Milestone 0059 — Executor BorrowRow Optimization

**Status:** planned
**Depends on:** Milestone 0054 (M0054-0005a BorrowSemantics foundation)
**Drives:** Lower executor copy/allocation pressure in Volcano tuple flow and improved GC efficiency on TPC-H query shapes.

## Context

goopg's executor already has a BorrowSemantics contract (`OwnedRow` /
`BorrowedRow`) and initial wiring on part of the pipeline, but borrow
propagation is still narrow and many paths keep defensive row copies or
materialize rows earlier than necessary.

Current behavior is correct, but the remaining copy churn shows up in
profile traces as high cumulative allocations and GC-heavy CPU share on
join/aggregate-heavy workloads. The objective of M0059 is to expand
BorrowRow usage where row lifetime permits it, while preserving the
correctness boundary for operators that retain tuples across `Next()`
calls.

## Scope

### Phase A — Contract hardening and lifetime matrix

- Document and codify row-lifetime expectations per operator class:
  pass-through, compute-only, retaining/materializing.
- Add regression guards that fail fast when a borrowed row is retained
  beyond the next `Next()` call on a borrow-enabled edge.

### Phase B — Borrow propagation widening

- Extend build-time borrow propagation so all safe pipeline edges are
  marked `BorrowedRow` by default.
- Preserve `OwnedRow` boundaries at retaining operators (sort, hash
  build, aggregate state retention, materialize/spill boundaries).

### Phase C — Operator-level copy elimination

- Remove redundant clone/copy in operators that can safely return
  borrowed rows.
- Prioritize hot paths measured in TPC-H and pprof baselines.

### Phase D — Measurement and parity verification

- Add before/after profile comparison for representative TPC-H queries.
- Prove no result-parity regression and no row-lifetime aliasing bugs.

## Required Design Docs

- `docs/design/0059-0001-borrowrow-volcano-row-lifetime-optimization.md`

## Definition of Done

- [ ] Borrow-lifetime matrix is documented and covered by focused tests.
- [ ] Borrow propagation is widened across safe edges with no semantic
      regressions.
- [ ] Targeted hot operators eliminate unnecessary row clones/copies.
- [ ] TPC-H verification run confirms result parity remains stable.
- [ ] Profile delta shows meaningful reduction in allocation and/or GC
      overhead on selected benchmark queries.
- [ ] `go test ./...` passes.

## Out of Scope

- New join algorithms or planner-level query rewrites unrelated to row
  lifetime semantics.
- Changes that alter SQL-visible behavior or tuple value semantics.
- Replacing all materialization with streaming where correctness relies
  on retained row ownership.