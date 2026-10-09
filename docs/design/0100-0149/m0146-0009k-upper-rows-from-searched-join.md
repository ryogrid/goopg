# M0146-0009k — a node above a searched join reads the join's searched rows

Status: done (2026-10-03, `577a4a97b`). Parent: M0146-0009 (banner item 3,
interleaveable statistics/cardinality work).

## The PG behaviour

An upper path is sized from its input path's `rows`, and every path of a
joinrel carries `rel->rows` (`set_joinrel_size_estimates`). For example,
`create_sort_path` (`./postgres/src/backend/optimizer/util/pathnode.c:3221`)
calls `cost_sort(…, subpath->total_cost, subpath->rows, …)` and copies
`subpath->rows`.

## The defect

goopg's search stamps the joinrel size it chose on the lowered join node
(`stampPlanCost`), and the join's EXPLAIN line prints it. But legacy-priced
upper nodes read their input through `EstimateRows`, and its `*Join` /
`*NestedLoopIndexJoin` arms returned the pre-search estimators
`estimateJoin` / `estimateNLIndexJoin`.

So a Sort, Limit, Aggregate group count or WindowAgg above a searched join
was sized from a different estimator than the join itself. TPC-DS Q59's
Sort read 4811 rows over its 15-row Hash Join, and 14 SF0.25 queries
showed the disagreement (recon M0146-0005ds).

## What landed

Both arms take `stampedUpperRows` first, the preference M0146-0005bg
already gave `*Distinct` / `*DistinctOn`. An unstamped node (the legacy
pipeline) or a per-worker stamp keeps the estimator.

## Verification

- `TestUpperNodeReadsSearchedJoinRows`: a Sort directly over a searched
  join reports the join's rows. It fails at HEAD (21919 vs 22000).
- SF0.25 sweep 96/96: values unchanged.
- Fire set: 12 queries at SF0.25 and 14 at SF1 change estimate text. One
  shape changed: SF0.25 Q72's join order above the resized node. match
  39/28 and `CATEGORIES-EXCL-MATCH` are unchanged, with no timeouts.
- ea-ratchet 10/10; TPC-H arm PASS; spotcheck.
- Regress A/B against HEAD: aggregates, select, subselect, union, window
  and groupingsets are byte-identical. In `join`, three EXPLAIN shapes that
  already diverged from PG reshuffle (+5 lines); no result row changed.

## Residuals (ledgered)

- The executor's hash-build sizing fallback
  (`operators_join_agg.go` `buildGeometry`) reads `EstimateRows` only
  when the plan carries no build rows. It now sees the stamped size there
  too, which is PG's input.
- Q59's join-order election (M0146-0005ds finding 1) was not re-measured
  as its own question; the root M0146-0005 is held under S4.
