# M0146-0009l — recon: why Q56 lost its match after the semi clamp

Status: done (2026-10-04, recon). Parent: M0146-0009. Fix: M0146-0009n, done (`9e1e56e5c`), Q56 matches again.

## Symptom

After M0146-0009g (eqjoinsel's SEMI/ANTI clamp), TPC-DS Q56 at SF0.25 lost
its match. In the store_sales UNION branch:

| | branch shape | cost |
|---|---|---|
| PG 18.3 | GroupAggregate → Sort → Gather | 10769.14 |
| goopg | GroupAggregate → Gather Merge → Sort (per worker) | 13711.74 |

The other two branches match PG.

## What was ruled out

- **Not the LIMIT / tuple fraction.** The same plan comes out with `LIMIT
  100` removed.
- **Not cost_sort vs cost_gather_merge arithmetic.** For the same input
  rows, PG's formulas order the two shapes as PG does: Sort-over-Gather is
  about 0.03 cheaper.
- **Not add_path's tie-break.** `comparePaths` is PG's dominance table,
  and both candidates have equal rows (11) and parallel safety (false).

## Cause

The branch's grouped rel keeps three sorted candidates (DP trace,
`GOOPG_PGSHAPED_DP_TRACE=1`, exact rows):

| producer | total |
|---|---|
| `upper.groupagg.sort` (Sort over Gather) | 13711.814 |
| `upper.groupagg.sortinput` (Finalize over Gather Merge) | 13711.847 |
| `upper.groupagg.gathermerge` (GroupAggregate over Gather Merge → per-worker Sort) | **13711.742**, wins |

In the no-split gathered arm of `addPartialAggUpperPaths`
(partialaggupper.go), the two parallel boundaries are sized from different
row counts:

- the Gather (`nsGather`) uses `inputRows`, the serial seed's rows (11);
- the Gather Merge (`workerSortGatherMergePath`) uses `perWorkerRows × d`,
  the partial path's rows times the parallel divisor (10).

PG sizes both from the same partial subpath: `compute_gather_rows` is
`subpath->rows × parallel_divisor` (costsize.c), whichever boundary is
built. So goopg charges the Gather Merge `parallel_tuple_cost × 1.05`
for one row fewer, which is 0.105 cheaper. That is enough to flip a
0.07-wide near-tie. 0009g only moved the estimates enough to expose the
inconsistency.

## Fix (filed M0146-0009n)

Size `nsGather` as PG does: `Rows = clampRowEst(perWorkerRows × d)`, with
its `gatherCost` charged on that count. The serial seed's rows are not a
Gather's input.

Expected movement: Q56 SF0.25 back to match (branch Sort → Gather). Other
queries whose gathered-arm Gather and Gather Merge disagree may move too.
Measure with the fire set at both scales.
