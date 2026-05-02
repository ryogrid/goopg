# Milestone 0035 — Streaming Hash Join & Bushy-Unnest Correctness

**Status:** planned
**Depends on:** M0032 (Go heap arena), M0033 (subquery unnesting), M0034 (DP bushy joins)
**Drives:** Cut peak memory of hash joins by ~50% by eliminating drainRows on the probe side. Verify that the M0033 unnest pass correctly processes M0034's bushy plan trees.

## Context

The TPC-H final run (`analysis/tpch-final-run-004.md`) confirmed that M0034's DP bushy
join optimization eliminates CROSS joins in Q2, and M0033's subquery unnesting eliminates
per-row re-execution. However, Q2 still exhausts memory (28 GB RSS) because:

1. **`joinOp.Open()` drains BOTH children into memory** via `drainRows` before
   executing the hash join. For a hash join on `part(200K) ⋈ partsupp(800K)`, both
   the build side (800K rows) and the probe side (200K rows) are deep-copied.
   Across 4 hash-join levels in Q2's bushy plan, this compounds to ~3.4 GB of
   deep-copied row data, plus hash table overhead and Go heap fragmentation.

   A streaming hash join only needs the **build side** fully buffered — the probe
   side can stream one row at a time through the hash table, eliminating ~50% of
   peak memory at each join level.

2. **The interaction between M0033 unnest and M0034 bushy DP needs verification.**
   When bushy DP consumes all equality predicates from the WHERE clause, the Filter
   is removed. The unnest pass (`unnestSubqueriesInPlan`) then runs on a pure bushy
   join tree (not a Filter-wrapped CROSS chain). Code review suggests this works
   correctly — the unnest pass handles `*Join`, `*Filter`, and other node types —
   but no explicit test verifies the combined path.

## Required Design Docs

1. `docs/design/0035-0001-streaming-hash-join.md` — Streaming hash join executor:
   signature change for `runHashJoin`/`runHashJoinBuildLeft`, one-pass probe via
   `Operator.Next()`, LEFT JOIN unmatched-row tracking.

## Definition of Done

### Part A: Streaming hash join

1. **`joinOp.Open()` modified**: Only `drainRows` the build side. Probe side
   remains a streaming `Operator` whose `Next()` is called in a loop.
2. **`runHashJoin` signature changed**: Accepts `(buildRows []Row, probeOp Operator, ...)`
   instead of `(leftRows, rightRows []Row, ...)`.
3. **LEFT JOIN semantics preserved**: When the probe side streams through, unmatched
   rows produce `concatRows(l, nullRight)`. A `matched` set (e.g., `map[string]bool`
   keyed on datumKey) tracks which build rows were matched for RIGHT/FULL join support
   in future follow-ups.
4. **`runHashJoinBuildLeft` also streaming**: Symmetric change when left is build side.
5. **No regression**: All 22 TPC-H queries build and execute. `go test ./...` passes
   (pre-existing analyzer failure excluded).
6. **Memory measurement**: With `shared_buffers=2048MB`, Q2 on partial SF=1 data
   (4.5M lineitem rows) shows substantially lower peak RSS than the 28 GB observed
   in M0034-0002.

### Part B: Bushy + unnest interaction verification

7. **Unit test**: `TestBushyPlanWithUnnest` — Plan Q2 with ANALYZE stats. Verify
   the final plan tree contains zero `SubqueryExpr` nodes AND zero `JoinTypeCross`
   nodes. The unnest pass must fire successfully on the bushy plan tree.
8. **Plan shape test**: The Q2 plan contains a `HashJoin` whose right child is an
   `Aggregate` (unnest result), AND no CROSS joins in the outer 5-table tree.

## Reference

- `internal/executor/operators_join_agg.go:40-75` — `joinOp.Open()` (drainRows both sides)
- `internal/executor/operators_join_agg.go:126-180` — `runHashJoin` (build right, probe left)
- `internal/executor/operators_join_agg.go:189-217` — `runHashJoinBuildLeft` (build left, probe right)
- `internal/planner/unnest.go:1-43` — `unnestSubqueriesInPlan` (handles *Filter and *Join)
- `internal/planner/bushy.go:30-80` — `tryBushyDP` (returns bushy plan, may remove Filter)
- `internal/planner/planner.go:329-357` — integration point (bushy DP then unnest)
- `analysis/tpch-final-run-004.md` — Q2 28 GB RSS, drainRows identified as bottleneck
