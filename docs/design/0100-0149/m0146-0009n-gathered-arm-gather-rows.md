# M0146-0009n — the gathered arm sizes both boundaries by compute_gather_rows

Status: done (2026-10-04, `9e1e56e5c`). Parent: M0146-0009. Recon: M0146-0009l
(`m0146-0009l-q56-gather-rows-recon.md`).

## PG behaviour

Every Gather or Gather Merge PG builds over a partial subpath takes its rows
from `compute_gather_rows` (`./postgres/src/backend/optimizer/path/costsize.c`):
`clamp_row_est(subpath->rows * get_parallel_divisor(subpath))`.
`generate_gather_paths`, `generate_useful_gather_paths` (allpaths.c) and the
grouping code (planner.c) all call it. Two boundaries over the same partial
input therefore always cross the same row count, and `cost_gather` /
`cost_gather_merge` charge `parallel_tuple_cost` on it.

## goopg before

`addPartialAggSplitPath`'s gathered (no-split) arm (partialaggupper.go) built
two boundaries over the same partial input `pseed`:

- the Gather `nsGather` took `inputRows`, the serial seed's rows;
- the worker-sort Gather Merge (`workerSortGatherMergePath`) took
  `perWorkerRows × d`, unclamped.

With a searched partial path (`partial != nil`) the per-worker rows are that
path's own, so the two counts differ. In TPC-DS Q56's store_sales branch the
Gather crossed 11 rows and the Gather Merge 10. That undercharged the Gather
Merge by `parallel_tuple_cost × 1.05` and handed it a 0.07-wide near-tie
that PG's Sort-over-Gather wins.

## Change

The arm computes `gatheredRows := clampRowEst(perWorkerRows × d)` once.

- `nsGather` takes it, and `gatherCost` charges it.
- The aggregates above `nsGather` (sorted, hashed, plain and ordered-plain)
  are costed on it.
- `workerSortGatherMergePath` clamps its count the same way.

Without a searched partial path, `perWorkerRows × d` is the seed's rows, so
only the rounding changes there.

Sibling producers checked:

- `partialaggpaths.go` and `partialsortpaths.go` build their leader-side
  Gather over the serial seed with no partial override, so their counts
  already agree.
- `gatherpaths.go` uses `computeGatherRows` itself.

## Effect

| | before | after |
|---|---|---|
| SF0.25 match | 38 | 39 (Q56) |
| SF1 match | 29 | 30 (Q93) |
| SF0.25 sort-strategy / parallelism / rendering | 30 / 34 / 12 | 27 / 31 / 11 |
| SF1 sort-strategy / parallelism | 32 / 46 | 29 / 46 |

The other moves are all toward PG: SF0.25 Q5, Q58 and Q60, and SF1 Q52 and
Q80 each lose sort-strategy or parallelism.

The exception is SF1 Q71, which changes from one non-PG shape to another.
Before, it ran `GroupAggregate → Gather Merge → Sort`; now it runs
`GroupAggregate → Sort → Gather`. With equal row counts, the second is
cheaper by PG's formulas (146245.94 vs 146246.02). PG's plan is the split,
`Finalize GroupAggregate → Gather Merge → Partial GroupAggregate → Sort`,
and goopg's split arm still loses it. That aggregation-strategy divergence
was already counted against Q71 (ledgered).

Gates:

- Values: sweep 96/96, TPC-H arm 24/24, ea-ratchet PASS.
- Regress: select\_parallel, aggregates, partition\_aggregate, groupingsets,
  write\_parallel and union all diff identically to HEAD.
- Fire set: PASS. The first run timed out SF1 Q74 on an unchanged plan;
  baseline Q74 already takes about 581 s of the 600 s cap, and the nightly
  lane was running. Two re-runs passed.

Test: `TestGatheredArmBoundariesShareComputeGatherRows` (fails before).

## Not done (ledgered)

- The split arm's Gather (`addPartialAggSplitArm`, `crossedRows :=
  partialGroups × d`) is not clamped; PG's `compute_gather_rows` of the
  partial aggregate path is.
- SF1 Q71: the split arm loses to the no-split shapes, where PG elects it.
