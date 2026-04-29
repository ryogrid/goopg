# 0020-0004 - Window EXPLAIN and Regression Coverage (Stage A)

**Status:** accepted (Stage A)
**Milestone:** [0020 - Window Function Support](../milestones/0020-window-functions-over-row-number-rank-lag-lead.md)
**Spans seam:** EXPLAIN renderer, cross-layer regression tests
**Cross-links:**
[0018-0002](0018-0002-explain-analyze-and-testing.md),
[0018-0003](0018-0003-explain-analyze-instrumentation.md),
[0020-0003](0020-0003-window-executor.md).

## Scope

This slice makes WindowAgg visible in EXPLAIN output and locks in
Stage-A window semantics with regression tests.

## EXPLAIN integration

`internal/executor/operators_explain.go` is extended with WindowAgg handling.

- `describePlan` now labels window nodes as:
  - `WindowAgg (<N> funcs)`
- `planChildren` now traverses `WindowAgg.Child`.

This applies to both TEXT and JSON renderers because both use the same
plan-label and child-walk helpers.

## Regression matrix

### Analyzer/Planner/Executor focused gates

- `go test ./internal/analyzer`
- `go test ./internal/planner`
- `go test ./internal/executor`

### New EXPLAIN regressions

- `TestExplainIncludesWindowAggNodeText`
- `TestExplainIncludesWindowAggNodeJSON`

### New window compatibility regressions

- `TestCompatWindowRowNumberPartitionOrder`
- `TestCompatWindowRankPeerGroups`
- `TestCompatWindowRankNullPeersAsc`

## What this pins

- WindowAgg is not silently hidden in explain trees.
- JSON `Plans` recursion includes WindowAgg nodes.
- Stage-A ranking behavior remains deterministic across ties,
  partitions, and NULL-bearing order keys.

## Deferred

- EXPLAIN ANALYZE per-window runtime counters specific to WindowAgg
  (rows/loops/timing exists at generic node instrumentation level; extra
  window-specific counters remain follow-up).
- Stage-B coverage for `lag`/`lead` and frame clauses.
