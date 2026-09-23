# M0141-S2b-16 — per-worker `rows=` on partial scans the planner stamps late

Status: complete 2026-09-23. Task: `.ralph/fix_plan.md` M0141-S2b-16 (Kind:
impl, Parent: M0141-S2b-15). Deferred from S2b-15 (ledger
`s2b15-partial-path-display`).

## Problem

PG's EXPLAIN shows a partial scan's rows **per worker**: `cost_seqscan`'s
parallel arm sets `path->rows = clamp_row_est(rows / get_parallel_divisor)`
(costsize.c:335-353). TPC-DS SF0.25 `store_sales` is `rows=232218` in PG
(719 876 / 3.1). Scans lowered from a real partial scan path already show
that; 61 such `store_sales` scans matched on the current tree. But a scan can
also be labelled `Parallel` *after* planning, by `stampParallelScan`, while
still carrying its serial `PlanCost`. Those rendered `rows=719876`.

## Where the late stamping happens

Measured on the SF0.25 plan capture:

| route | example | why the scan is serial |
|---|---|---|
| legacy post-pass `rebuildWithGather` (`MaybeAddGather`) | Q84 `store_returns` | the whole subtree was planned serially |
| path-model `gatherChildPlan` over a prebuilt input | Q6, Q22, Q58, Q83 | an upper-rel seed (`newPrebuiltPath` marked partial) wraps a serial subtree; the seed's own rows are per worker, the scan inside is not |
| path-model split aggregate (`PathFinalizeAgg` → `splitAggregate`) | Q59 | same, under Finalize → Gather → Partial |
| partial-Append members | Q5 | the members were serial scans under a SetOp driving node |

## Change

- `PlanCost.PerWorker` records that a node was stamped from a partial path
  (`stampPlanCost`: `p.ParallelWorkers > 0`), so its figures are already one
  participant's share.
- `perWorkerDisplayRows(stamped, divisor)` walks `drivingScans` of a freshly
  stamped subtree. It never divides a SetOp node itself, and it divides the
  rows of every scan not yet `PerWorker` by the divisor, then marks it.
- It is called on all three stamping routes: `rebuildWithGather` (plain,
  GatherMerge and split branches, with `ParallelSettings.LeaderParticipates`
  threaded in), `gatherChildPlan`, and the `PathFinalizeAgg` lowering.
- `gatherPathDivisor` recovers the divisor on the path-model routes, which
  have no `costParams`. `computeGatherRows` set the Gather path's rows to
  `clampRowEst(sub.Rows × divisor)`, so the leader setting that reproduces
  them exactly is the one in force (default: leader participating).

Display-only by construction. The Gather above sizes itself from
`EstimateRows`, which reads table statistics, and display-cost parents read
a child's cost, not its rows. The stamped scan is always the copy
`stampParallelScan` made, never a node of the cached serial plan.

## Result (TPC-DS SF0.25, values unchanged)

- Q5, Q6, Q22, Q58, Q59, Q83, Q84: every late-stamped partial scan now shows
  PG's per-worker figure: `store_sales` 719 876 → 232 218, `catalog_sales`
  360 397 → 116 257, `web_sales` 179 956 → 74 982, `store_returns`
  71 676 → 42 162 or 23 121 by worker count, `inventory` 2 355 000 →
  759 677. No `Parallel Seq Scan on store_sales rows=719876` remains.
- `make ea-ratchet`: PASS, 53 → 52 findings (Q71 fixed), none new.

## Not done (ledgered)

- **Cost** stays the serial figure on these scans. PG divides only the CPU
  term; the disk term is charged in full. Splitting a finished node's total
  needs the relation's page and tuple inputs, which the plan does not carry.
- **Intermediate nodes** inside a prebuilt partial subtree (joins, filters
  above the driving scan) still show serial rows. PG shows per-worker rows
  on every node below a Gather.
- **Q5's Append** still shows the total (791 552). PG's `Parallel Append`
  shows the per-worker figure. The partial-Append producer's own rows and its
  members' CPU-only cost (7198.76, no disk term) are M0140-0006's.
- S2b-17(a) (split-agg `rows=1` PlanCost gap) is a separate site.
