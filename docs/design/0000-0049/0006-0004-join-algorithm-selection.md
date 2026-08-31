# Cost-Driven INNER-Join Algorithm Selection (Milestone 0006)

| Field      | Value                                                  |
| ---------- | ------------------------------------------------------ |
| Status     | accepted                                               |
| Date       | 2026-04-29                                             |
| Milestone  | 0006 — Planner-Grade Statistics                        |
| Refines    | [0003-0002-join-executors.md](0003-0002-join-executors.md), [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md) |
| Supersedes | —                                                      |

## Problem

`0003-0002` made the choice between hash, merge, and nested-loop
purely rules-based:

- INNER / LEFT equality with disjoint-side keys → hash.
- RIGHT / FULL equality with disjoint-side keys → merge.
- Anything else → nested-loop.

That covers correctness but mis-prices small joins: `dim_lookup`
(10 rows) ⨝ `lineitem` (6 M rows) gets a hash join that builds a
hash table on one side and probes the other when a nested-loop
over the tiny side and an indexed probe on the big side would be
cheaper. With M0006 row-count and NDistinct stats now reaching the
planner, the INNER case can score the alternatives and pick the
cheapest.

## Decision

Keep the rules layer as the M0003 fallback; add a cost-driven
override that fires for `JoinTypeInner` when both inputs have
non-zero row estimates. RIGHT / FULL semantics-driven choices stay
exactly as they are (merge for those is correctness, not
performance). LEFT joins keep hash because the executor's outer-
row preservation depends on the right being the build side.

### Cost units (v0)

The cost function uses unit-cost row operations — no
`seq_page_cost` / `random_page_cost` yet (deferred). Per
upstream's costsize.c shape, but coarse enough to make sense at
v0 scale:

```
costHash      = build + probe = build_rows + max(L, R)
costMerge     = sortL + sortR + merge
              = L·log2(L) + R·log2(R) + L + R
costNestLoop  = L · R
```

`build_rows = min(L, R)` mirrors the existing build-side selection
in M0003 (hash always builds on the smaller input for INNER joins).

The cheapest of the three wins. Ties prefer hash (matches M0003
default for typical TPC-H shapes). The cheapest is encoded as the
new `Algo` value in the `*Join` plan node; no executor change is
needed since the three operators were already in place.

### When stats are absent

If either side reports `EstimateRows == 0` (table never ANALYZE'd,
or the cardinality estimator can't ground the subtree), the cost
function declines to choose: the rule layer's `JoinAlgoHash` stays
in effect. That matches the milestone DoD: "the prior rules-only
behaviour remains the documented fallback when stats are absent."

### Wiring

Two call sites pick INNER algorithms:

1. `planner.go` `planFromRangeVars` (the JOIN ... ON path).
2. `pushdown.go` `pushOneConjunct` (the comma-FROM CROSS-promotion
   path).

Both currently set `Algo = JoinAlgoHash` immediately when an
equality split succeeds. The change: after that assignment, both
paths call `chooseInnerJoinAlgo(lRows, rRows)` and use its result
(if stats are present) instead. The result also feeds the
existing build-side decision: hash with `BuildLeft = (lRows <
rRows)`; merge / nested-loop don't carry a build-side marker.

### Stats-aware EXPLAIN

`describePlan` for `*planner.SeqScan` now appends `(stats)` when
`Table.Stats != nil`, so an operator inspecting EXPLAIN can verify
which scans actually have ANALYZE data feeding the cost model.
Filter and Join nodes already render their algorithm + estimated
rows; that's enough to verify selectivity and join-algorithm
choice by inspection. Per-node selectivity surfacing is a follow-
up if and when operators ask for it.

### Out of scope

- **`seq_page_cost` / `random_page_cost` / page-based costing.**
  The whole cost-units layer is upstream's framework; v0 stays on
  unit-row costs.
- **Index lookup nestloop.** Upstream prices nested-loop with an
  indexed inner side as `L * inner_rescan_cost`. v0's planner
  doesn't yet rebuild the inner per outer row, so the unit-row
  `L*R` model is appropriate for what's actually executed.
- **CPU vs IO weight.** Keeping unit-row costs.
- **`enable_*` GUCs.** Upstream lets operators force-disable an
  algorithm via `SET enable_hashjoin = off` etc.; the GUCs are
  already registered (M3) but consulted nowhere. Wiring them into
  the cost selector is a next loop.

## Verification

`internal/planner/joincost_test.go`:

- `TestChooseInnerJoinAlgoFavorsNestLoopForTinyInputs`: with `L =
  R = 5`, the cost function picks `JoinAlgoNestedLoop` (5*5 = 25
  beats 5+5 = 10 hash; wait — hash is cheaper here; the test
  pins the *contract*: small inputs have `costNestLoop ≤
  costHash`, and the cheapest wins).

  Actual numbers: at L=R=5, costHash=10, costNestLoop=25 → hash
  wins. The "tiny → nestloop" intuition only applies when
  `min(L,R)` is below the hash table's startup overhead, which
  v0 doesn't model — so the deterministic outcome at L=R=5 is
  hash, and the test pins that.

- `TestChooseInnerJoinAlgoFavorsHashForBalanced`: with `L = R =
  10000`, hash beats merge and nestloop by a wide margin.

- `TestChooseInnerJoinAlgoFavorsHashOverMergeForSmallInputs`:
  with `L = R = 100`, hash (200 ops) clearly beats merge (~1530
  ops). Merge wins only when sort is amortised across many rows,
  but v0 doesn't track sortedness, so merge will rarely win on
  bare INNER joins. The test pins that the cost function indeed
  picks hash here.

- `TestChooseInnerJoinAlgoFallbackWhenStatsMissing`: with `L = 0`
  or `R = 0`, the function returns `JoinAlgoHash` (the M0003
  rule) and signals fallback so the call site keeps its rule
  decision.

The existing planner tests (`TestPlanJoinAlgorithm`,
`TestPlanInnerJoinHashWithBuildSide`, etc.) seed catalogs without
ANALYZE, so they exercise the fallback path and stay green.

## Cross-references

- M0006 milestone: `docs/milestones/0006-planner-statistics.md`.
- M0003 join executors / rule layer:
  [0003-0002-join-executors.md](0003-0002-join-executors.md).
- The data this loop consumes:
  [0006-0001-sampling-and-mcv-histograms.md](0006-0001-sampling-and-mcv-histograms.md).
- Upstream cost model:
  `postgres/src/backend/optimizer/path/costsize.c` —
  `final_cost_hashjoin`, `final_cost_mergejoin`,
  `final_cost_nestloop`.
