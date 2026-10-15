# M0146-0210 — a Gather's rows are its path's compute_gather_rows; the DISTINCT seed reads its input path's cost

Status: done 2026-10-16. TPC-DS SF1 Q54 matches PG.

## Problem

TPC-DS Q54's `my_customers` CTE is `SELECT DISTINCT c_customer_sk, c_current_addr_sk` over a parallel join. Its
partial result is 1 row per worker under 1 planned worker. PG and goopg diverged in three steps.

1. **Rows.** PG's Gather above that join has `rows = compute_gather_rows(subpath) = clamp_row_est(1 × 1.7) = 2`
   (`postgres/src/backend/optimizer/path/costsize.c:6625`). `create_gather_plan` copies it into `plan_rows`
   (`copy_generic_path_info`). The DISTINCT is sized by `estimate_num_groups` over its input rel's rows, so it keeps
   2. goopg's search built the Gather path with rows = 2. But `EstimateRows(*Gather)` re-estimated the child, the
   per-worker join (1). The DISTINCT came out at 1, and every join above it shrank to 1 row, which flipped the join
   order: `store_sales` was joined first instead of last.
2. **Seed cost.** `create_distinct_paths` (planner.c:4816) prices its arms on `cheapest_input_path` at its own cost.
   goopg's DISTINCT seed instead read the legacy display cost of the unstamped Project wrapper over the Gather, which
   added a per-row charge (+0.04). PG has no node there: `apply_scanjoin_target_to_paths` applies the target to the
   paths themselves, below the Gather when it is parallel-safe (dummypp).
3. **Gathered rows of the partial arms.** The partial-DISTINCT arms passed unclamped gathered rows (1 × 1.7) to
   `gatherMergeCost`. PG files those paths through `generate_useful_gather_paths` → `create_gather_merge_path`, with
   `compute_gather_rows` = 2. Together with step 2, that priced the partial arm 0.04 under the serial one. goopg chose
   `Unique → Gather Merge → Sort`, where PG plans `Unique → Sort → Gather`.

## Change

- **`EstimateRows` (cardinality.go).** `*Gather` and `*GatherMerge` read their stamped whole-node rows
  (`stampedUpperRows`, which skips per-worker stamps). Only an unstamped node falls back to the child's estimate.
- **`createDistinctPaths` (distinctpaths.go).** The seed's cost is `limitInputPath(child)`, the stamped search node
  reached through Project wrappers, as `create_limit_path` already reads it. The legacy display cost is now only the
  fallback.
- **`addPartialDistinctPaths`.** `crossed` and `sortedCrossed` are `clampRowEst(rows × divisor)`, matching the
  partial-aggregate twin (M0146-0009n, partialaggupper.go).
- **Tests** (`gather_rows_distinct_test.go`). Each fails with its fix reverted.
  - `TestEstimateRowsGatherReadsItsPathRows`: covers stamped, unstamped and per-worker stamps.
  - `TestPartialDistinctGatheredRowsAreClamped`: the 400000-row fixture plans 1 worker; unclamped it gives 45.9.
  - `TestDistinctSeedLooksThroughProject`.

## Not done (ledgered, M0146-0211)

Five other upper producers still seed from `legacyDisplayCostOf(child)`: `groupingpaths.go`, `partialaggpaths.go`
(×2), `partialsortpaths.go`, `upperordered.go` and `upperrel.go`. Each would charge a Project wrapper in the same way.
Moving all of them is a broader corpus change and needs its own measurement.

## Measured

Captures in `analysis/m0146/m0146-0210/`. The fire-set A/B ran HEAD `36e72a5ad` against the staged change.

- **TPC-DS SF1:** match 72 → 73 (Q54). CATEGORIES-EXCL-MATCH qual-placement 4 → 3, join-order 22 → 21, scan-type
  16 → 15, parameterisation 7 → 6, sort-strategy 11 → 10, parallelism 12 → 11.
- **TPC-DS SF0.25:** match 85 → 85 and categories unchanged. Eight plans changed estimates only (Q6, Q14, Q38, Q41,
  Q51, Q54, Q71, Q87); none lost its verdict.
- **TPC-H:** plan text byte-identical (20/22).
- **Values:** SF0.25 sweep PASS=99 (61 ck-verified); TPC-H acceptance arm 24 MATCH; fire-set executions
  introduced=none.
