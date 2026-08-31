# 0054-0005 — Hash-Join Small-Side Build Estimation

**Status:** Draft. Sub-task of M0054-0010.
**Author:** goopg perf-analysis branch (run-013 finding).
**Date:** 2026-05-06.

## 1. Problem

TPC-H joins frequently include `nation` (25 rows) and `region`
(5 rows). A hash join between such a tiny side and a much larger
side should ALWAYS build the hash on the small side (25 entries)
and probe from the large side (millions of entries). Today's goopg
planner sets `Join.BuildLeft` based on `EstimateRows`, but the
heuristic interacts poorly in two cases:

1. **Stats absent.** Without ANALYZE-fed cardinality, EstimateRows
   returns -1 / 0 / a constant; the planner picks the default
   build side (right) even when the right side is the larger of
   the two.
2. **Join order rotation.** When a left-deep join tree puts nation
   at depth ≥2 (e.g. `((supplier × partsupp) × nation)`), the
   planner sees an intermediate join result (cardinality
   estimated as max of inputs) on the left and `nation` on the
   right. It builds on `nation` correctly here. BUT the symmetric
   rotation `(nation × (supplier × partsupp))` flips the build
   side to the (large) inner result. Q5/Q7/Q8/Q9 all join nation
   in different orders depending on the bushy enumeration; some
   orderings inadvertently build on the multi-million-row side.

The cumulative effect is wasted memory + CPU: a 6 M lineitem ×
25 nation hash join with the wrong build side allocates a 6 M-entry
hash table when 25 would suffice.

## 2. Goal

Strengthen the small-side build estimate so:

- `nation`, `region`, and any catalog-known small dim (≤1000 rows
  by row-count estimate) is ALWAYS on the build side of any hash
  join it participates in.
- Even when stats are absent, a catalog-cardinality fallback is
  used: `catalog.Table.RowCount` (if populated), else a heuristic
  by table name from the TPC-H schema dimensions, else current
  EstimateRows behaviour.
- The chosen build side is correct under arbitrary join-order
  rotation.

## 3. Implementation outline

### 3.1 Cardinality estimator

`internal/planner/cardinality.go::EstimateRows` is the existing
entry. Extend it to consult, in priority order:

1. ANALYZE-populated `Table.Stats.RowCount` (via M0006 stats
   pipeline once that lands SF=1 stats).
2. Catalog-level `Table.PhysicalRowCount` (heuristic, from
   `pg_class`-like row count estimate — already present?).
3. A fallback table for "definitely tiny" tables that ships in
   the catalog as a hint flag, e.g. `Table.SmallDimension bool`.

The fallback table is intentionally small (region, nation, any
table where the user explicitly tagged via `CREATE TABLE t (...)
WITH (small_dim = true)` — speculative).

Without M0006 stats, the small-dim flag is the lever that lets
the planner make the right build-side choice for run-013.

### 3.2 Build-side selection refinement

`internal/planner/join.go::chooseBuildSide` (or the inline
`BuildLeft` decision in bushy.go) — augment to:

```
if leftSizeKnown && rightSizeKnown:
    BuildLeft = leftSize <= rightSize
else if leftSmallDim && !rightSmallDim:
    BuildLeft = true
else if !leftSmallDim && rightSmallDim:
    BuildLeft = false
else:
    keep current default
```

The small-dim path is the new addition: it bypasses the size
estimator when the size estimator would otherwise return
indistinguishable values for the two sides.

### 3.3 MultiHashJoin integration

`*planner.MultiHashJoin` chains N tables; each edge has a build
side implied by the chain order. Extend
`internal/planner/bushy.go::collectMultiHashTables` so that any
small-dim table gets pinned to a non-probe Tables[i] position,
i.e., it's a hash-build participant rather than the probe driver.

### 3.4 EXPLAIN

`describePlan` for `*Join` already prints the join algorithm.
Extend the rendering to include `(BuildLeft)` / `(BuildRight)` so
EXPLAIN baselines surface the build-side choice for regression
detection.

## 4. Acceptance

- `internal/planner/join_buildside_test.go` (new) — a binary hash
  join between a 25-row table (`nation`-shaped: with the
  small-dim hint) and a 10000-row table emits `BuildLeft = true`
  if nation is the LEFT child; `BuildLeft = false` (i.e.
  build-on-right) if nation is the RIGHT child. The chosen side
  is invariant under join-order rotation.
- `analysis/tpch-explain-baseline.md` Q5/Q7/Q8/Q9/Q20/Q21 rows
  show nation on the build side of every join it participates in
  (rendered by the augmented EXPLAIN).
- No regressions on existing planner tests (`go test ./internal/planner/...`).

## 5. Out of scope

- A full cost-based join-order selector. The bushy enumeration in
  `bushy.go` does limited reordering today; this design narrows
  the build-side selection independently of which join order is
  ultimately chosen.
- Statistics pipeline integration with M0006. The small-dim flag
  is a stop-gap until ANALYZE-fed cardinalities are reliable.

## 6. Critical files

- `internal/planner/cardinality.go` — `EstimateRows`.
- `internal/planner/join.go` / `bushy.go` — build-side selection.
- `internal/catalog/catalog.go` — `Table` struct, possibly add
  `SmallDimension bool`.
- `internal/executor/operators_explain.go::describePlan` — render
  build-side flag.
- `internal/planner/join_buildside_test.go` (new).

## 7. References

- TPC-H Q5/Q7/Q8/Q9/Q20/Q21 reference plans (Postgres / DuckDB).
- run-013 EXPLAIN baseline showing nation in each Q's join tree.
