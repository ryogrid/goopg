# M0146-0027 slice 6 — ordered-PLAIN arm: `Gather Merge -> Sort` under ungrouped ordered aggregates (Q28)

Residual closed this slice: Q28's `PG Gather Merge | goopg Gather`
first-divergence record under `Aggregate`.

## What PG does

Q28 is six derived-table scalar aggregates — `avg`, `count(distinct
ss_list_price)`, `count`, `count(distinct ss_wholesale_cost)` — over
filtered `store_sales` scans. Each aggregate in PG 18.3 sits over

    Aggregate -> Gather Merge -> Sort(ss_list_price) -> Parallel Seq Scan

Mechanism: `adjust_group_pathkeys_for_groupagg` (planner.c:3201) appends
the aggregate's own ordering requirement (the DISTINCT sort list) to
`group_pathkeys`; those become `query_pathkeys`, so
`generate_useful_gather_paths` files the per-worker Sort + Gather Merge,
and `add_paths_to_grouping_rel` wraps every candidate input in
`make_ordered_path` (planner.c:7134-7160). Crucially
`GROUPING_CAN_USE_HASH` requires `groupClause != NIL` (planner.c:3848) —
an ungrouped `count(DISTINCT)` is never hashed upstream, so NO unsorted
`Agg -> Gather` candidate exists to win on price.

## What goopg did

`addPartialAggSplitPath`'s PLAIN arm filed exactly one candidate —
unsorted `Agg -> Gather` — regardless of `presortedAggKeysOrAbsent`. On
small filtered inputs it out-priced the sorted serial/searched arms,
yielding `Aggregate -> Gather -> Parallel Seq Scan`. (Unfiltered probes
already produced the right spine via the slice-1 gather.merge.sort arm +
P7 post-pass; the divergence was specifically the filed-candidate set of
the parallel upper-agg producer.)

## Change (`internal/optimizer/partialaggupper.go`)

- New shared helper `workerSortGatherMergePath(grouped, pseed, keys, cp,
  workers, d, perWorkerRows)` — worker `sortPathForBounded` +
  `ParallelWorkers`, crossing rows `perWorkerRows * d`, `gatherMergeCost`,
  subpath pathkeys, disabled-node accounting. The R56 group-keys arm was
  re-pointed at it; the two arms differ only in the SortKey list.
- PLAIN arm, when `presortedAggKeysOrAbsent` returns keys: suppress the
  unsorted candidate and file the two ordered inputs upstream's
  `make_ordered_path` iteration would have produced —
  `Agg -> Sort -> Gather` and `Agg -> Gather Merge -> Sort -> <pseed>`,
  both priced by the PLAIN arm's own `costAgg` call (hashed pricing, 0
  group cols / 1 group).

## Measured

- Q28: `SHAPE-DIFF` -> `MATCH` at both SF0.25 and SF1; 1 row,
  ck=58f05f6812160030 oracle-exact.
- Q16 (side effect, same mechanism): the aggregate gained PG's
  `Sort(cs_order_number) -> Gather` input; record shrank 6 -> 3
  categories, still MISSING-NODE on the pre-existing join spine.
- Census deltas and gate results: `gates.txt`.

## Files

- `q28-goopg-sf025.txt`, `q28-pg-sf025.txt` — plans after the change.
- `q16-goopg-sf025.txt`, `q16-pg-sf025.txt` — the agg-input convergence.
- `census-sf025-slice6.txt` — first-divergence census (fireset
  candidate capture): Q28's `depth=7 [parallelism] under Aggregate`
  record gone → MATCH; Q16's `depth=3 [sort-strategy] under Aggregate`
  record resolved → deeper `depth=4 [parallelism]` record on the join
  spine. Rollup: divergent 88→87, match 11→12, sort-strategy 33→32.
- `census-sf025-{baseline,candidate}-class.txt`,
  `census-sf025-candidate-diff.txt`, `census-sf1-candidate-diff.txt`.
