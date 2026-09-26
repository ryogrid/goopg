# M0146-0025: partial Group paths for aggregate-free GROUP BY

Status: landed 2026-09-26 (plain grouped input; Passthrough / expression
keys and nested scopes refused).

## PG behaviour

`create_partial_grouping_paths` (`postgres/src/backend/optimizer/plan/
planner.c:7351`) does not only file partial aggregates: when the query has
no aggregate calls (`!parse->hasAggs`) it files **partial Group paths** —
`create_group_path` on the partially_grouped_rel (planner.c:7570). Each
worker sorts its input partition and dedups it; `gather_grouping_paths`
(planner.c:7704) puts a Gather Merge over the sorted streams and a second
`create_group_path` collapses the merge:

    Group                 leader-side re-dedup
      -> Gather Merge
           -> Group       per-worker dedup (partial)
                -> Sort
                     -> per-worker input (driving scan partitioned)

TPC-DS Q37/Q82 take exactly this shape at both scales (oracle capture:
`Group -> Gather Merge -> Group -> Sort -> Nested Loop`).

## Why the existing split cannot express it

goopg's partial-aggregate model (C-19g, `addPartialAggSplitArm`) is a
**zero-row** partial: `AggModePartial` emits no rows and publishes per-group
transition state through the shared accumulator `PartialSource`, which
`AggModeFinal` merges leader-side. A group-only node has no transition
state, so `aggregateSplitIsSafe` refuses `len(a.Aggs) == 0` — correctly;
that refusal stays.

The PG shape is semantically different and simpler: the partial Group is an
**ordinary sorted dedup run once per worker**, re-deduplicated by a second
ordinary dedup above the merge. Deduplication is idempotent, so the pair
produces the serial result provided each worker's input is the partitioned
driving scan.

## Change

`internal/optimizer/plan.go` — `Aggregate.PartialGroup`: a planner-set
marker on the per-worker dedup. It is the ONLY thing that lets the
driving-scan walks descend through an aggregate: a marked node promises a
leader-side re-grouping consumes its output, so partitioning its input is
the intended semantics — not the `count(*) FROM (SELECT DISTINCT …)`
over-count an unmarked dedup inside a candidate partial subtree would
produce. The marker is read only by `drivingScan`, `stampParallelScan`,
`unstampParallelScan` and `drivingScanCrossesSort` (parallel.go); every
other aggregate stays a wall there.

`internal/optimizer/partialaggupper.go` — two producers:

- `addPartialGroupArm` files the full shape inside the shared producer:
  `sortPathForBounded` worker sort → `PathAgg`-sorted partial spec clone
  (marker set, `Pathkeys` = group keys) → `PathGatherMerge` priced on the
  partial's OUTPUT rows (`partialGroups × d`, not the input's — the whole
  economic argument) → a second `PathAgg` whose `GroupExprs` are rewritten
  to the partial output's positions (PG's setrefs OUTER_VAR step;
  `partialGroupKeyRefs` declines expression keys and Passthrough specs,
  which stay refusals rather than silently-wrong output).
- `addPartialGroupOnlyPath` is the second route for the case the shared
  subtree guard refuses: under `GOOPG_GATHER_PATHS=all` the join SEARCH may
  elect a Gather INSIDE the input (`NL(Gather(…), idx)` — both Q37 and
  Q82). `spliceGatherOnPartialSpine` removes one boundary along
  `drivingScan`'s own descent arms (pass-throughs, probe side of
  partial-capable joins, the marked aggregate itself), the spliced child
  re-passes `subtreeHasUnsafeNode` / `subtreeHasGather` / `drivingScan`
  verbatim, and only the group arm files — the split and no-split arms
  keep the refusal because their boundary lands on the gathered subtree
  their costing modelled. Scoped to top-level statements and
  aggregate-free grouping only.

Lowering is unremarkable on purpose: `createAggPlan` copies the spec and
`gatherChildPlan` stamps the driving scan through the marker; the executor
runs both nodes as the ordinary `AggModeSimple` sorted dedup they are, and
EXPLAIN prints `Group` at both levels. `StripGather` removes the boundary
and leaves the dedup pair, which still collapses to the right answer.

## Results

- SF0.25 EXPLAIN: Q37 and Q82 now emit the oracle's `Group -> Gather
  Merge -> Group -> Sort -> Nested Loop` (residual: `Workers Planned`
  3 vs PG's 1 — the worker-count sizing class, not this arm).
- Q37/Q82 execute identically: 0 rows, matching the SF0.25 oracle.
- Q97 (the gather-free-input aggregate-free GROUP BY) keeps its
  `Group -> Gather Merge -> Sort` plan — the main-route arm competes and
  loses to the R56 no-split arm, which is also the shape PG emits.
- Pinned by `TestPartialGroupArmFilesThePGShape`,
  `TestPartialGroupArmRefusals`, `TestPartialGroupWalkAgreement`,
  `TestPartialGroupLowering` (optimizer) and
  `TestPartialGroupGatherMergeIdentity` (executor: identical rows vs
  serial dedup at 1/2/4 workers, cross-worker duplicates collapsed).

## Open (ledgered)

1. Expression group keys and Passthrough columns refuse the arm
   (`partialGroupKeyRefs`) — the final spec would need expr-bearing
   OUTER_VAR stand-ins and passthrough re-positioning.
2. Nested scopes keep the statement-level refusal; a subquery-level
   `create_partial_grouping_paths` analog for group-only inputs is
   unfilled.
3. The spine splice removes ONE boundary; a subtree carrying gathers on
   more than one spine level, or off-spine (build side / SetOp branch),
   stays refused.
4. `Workers Planned` sizing (goopg 3 vs PG 1 on Q37/Q82 at SF0.25) is
   unchanged — the worker-count divergence class, not this arm.
