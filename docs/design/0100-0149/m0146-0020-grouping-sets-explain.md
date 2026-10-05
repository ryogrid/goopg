# M0146-0020: grouping sets render as PG's MixedAggregate / HashAggregate

## PG behaviour

`consider_groupingsets_paths` (postgres/src/backend/optimizer/plan/planner.c)
decides the strategy.
- **Unsorted input:** every non-empty grouping set is hashed. An empty set
  (the grand total of a ROLLUP or CUBE) cannot be hashed and is computed in
  the sorted phase, which makes the strategy `AGG_MIXED`.
- **Sorted input:** PG also considers sorted rollups and mixed sort/hash
  combinations.

EXPLAIN (explain.c, show_grouping_set_keys) labels AGG_MIXED
`MixedAggregate` and AGG_HASHED `HashAggregate`. It prints one
`Hash Key:` line per hashed set in rollup order, then `Group Key: ()`
for the empty set (live capture on private PG 18.3):

```
MixedAggregate                 -- GROUP BY ROLLUP(dept, region)
  Hash Key: dept, region
  Hash Key: dept
  Group Key: ()
HashAggregate                  -- GROUP BY GROUPING SETS ((dept), (region))
  Hash Key: dept
  Hash Key: region
```

## goopg before

One hash table per set (M0125-0048), labelled `HashAggregate (N keys, M
grouping sets)` with no key lines.

## Change

The aggregate label is `MixedAggregate` when a set is empty and
`HashAggregate` otherwise. The detail lines are one `Hash Key:` per
non-empty set, listed largest first and otherwise in written order, then
`Group Key: ()`. Keys go through the same source chase as `Group Key:`,
and a non-Var key prints parenthesized, since PG's grouping columns
reference the input's target list. Execution is unchanged: goopg already
hashes every non-empty set and computes the empty set in the same pass.

## Results

- TPC-DS census: Q5, Q22, Q77 and Q80 move past the aggregate at both
  scales (to depth 4-6). Q27 matches PG at SF1. `aggregation-strategy`
  records drop 40 -> 36 (SF0.25) and 45 -> 40 (SF1).
- Row counts, TPC-H and the pass-required regress cases are unchanged.
  The `groupingsets` regress diff shrinks.
- Evidence: `analysis/m0146/m0146-0020/`.

## Not covered (ledgered)

- PG's sorted-input path: Q18 and Q27 (SF0.25) plan a sorted
  `GroupAggregate` over rollups, where goopg always hashes (M0146-0020a).
- The rollup order for CUBE over three or more columns follows
  `extract_rollup_sets`' chain matching, which largest-first ordering does
  not reproduce.

## M0146-0020a — the sorted strategy for a single rollup (2026-09-30)

With sorted input, PG's `consider_groupingsets_paths` also computes a rollup
(a chain of grouping sets, each contained in the next) in one sorted pass:
AGG\_SORTED, printed `GroupAggregate` with a `Group Key:` line per set,
largest first, above a Sort on the rollup order. TPC-DS Q18/Q27 (SF0.25)
plan it that way; goopg always hashed.

- `RollupChainOrder` (groupingsets\_sorted.go) recognises a single rollup
  and returns its column order. That is the smallest non-empty set's
  columns, then each larger set's additional columns, so every set is a
  prefix of it.
- The grouping upper rel (groupingpaths.go) offers the sorted rollup
  before the hashed arm, over the Sort of its input and over every
  searched input path already ordered that way. It is priced as
  `create_groupingsets_path` prices one rollup:
  - `cost_agg(AGG_SORTED)` over the longest set's columns and every set's
    groups;
  - no pathkeys: PG 18's grouped outputs are RTE\_GROUP expressions made
    nullable by the grouping step, so an ORDER BY above still sorts
    (Q27: `Sort -> GroupAggregate`).
- The executor still computes every set in one materialising pass. For a
  Sorted rollup it emits rows in AGG\_SORTED's order: along the rollup,
  detail groups before their rolled-up group, the grand total last. This
  was verified against PG 18.3 on `GROUP BY ROLLUP(k1, k2)`.
- EXPLAIN prints `GroupAggregate` and one `Group Key:` per set, including
  `Group Key: ()`.

Tests:

- `TestSortedRollupMatchesPG` pins PG 18.3's plan and row order with
  hash aggregation off.
- The C-10a strategy-gate tests become
  `TestC10aGroupingSetsSortedRollupComputesEveryLevel` (rollup order) and
  `TestC10aGroupingSetsStrategiesAgreeOnRows` (same row multiset).

Movement:

- TPC-DS PLAN-PARITY match SF0.25 26 → 27 and SF1 23 → 24; Q27 = PG at
  both scales.
- Q18 SF0.25 drops to a single scan-type record.
- SF0.25 join-order 56 → 54, join-method 31 → 29, aggregation-strategy
  21 → 19, sort-strategy 39 → 37, parallelism 37 → 35.
- SF1 join-order 61 → 60, join-method 29 → 28, aggregation-strategy
  23 → 22, sort-strategy 41 → 40, parallelism 48 → 47.
- TPC-H plans are byte-identical.
- groupingsets.sql moves toward PG (1047 → 994 diff lines; only plan
  lines change).

Evidence: `analysis/m0146/m0146-0020a/`.

Not ported (M0146-0020b):

- More than one rollup (CUBE, disjoint GROUPING SETS) and PG's
  hash\_mem-bounded mixed sort/hash choice across rollups.
- PG orders a rollup's added columns to match the query's ORDER BY
  (groupingsets.sql "reordering of grouping sets": `Group Key: v, b, a`
  where goopg prints `v, a, b`).
- On one groupingsets.sql rollup under a ProjectSet, PG keeps
  MixedAggregate where goopg now elects the sorted rollup.

## M0146-0020b slice 1 — PG's rollups and the multi-rollup sorted strategy (2026-10-05)

PG's `preprocess_grouping_sets` (planner.c) arranges the grouping sets into
rollups before any path is built:

- `extract_rollup_sets` covers the non-empty sets with the fewest chains,
  each set contained in the next. It removes duplicates, links each set to
  the smaller sets it contains, and runs `BipartiteMatch` (Hopcroft-Karp,
  `lib/bipartite_match.c`). Empty sets go to the head of the first chain.
- `reorder_grouping_sets` orders each chain's columns so that every set is
  a prefix of the largest. When there is a single chain it follows ORDER BY
  until ORDER BY names a column the next set does not add.

AGG\_SORTED then computes one rollup per pass. The first rollup reads the
input sorted on its order, and each later rollup sorts the input again
(`create_groupingsets_plan`'s `chain`). EXPLAIN prints the first rollup's
`Group Key:` lines, then for each later rollup a `Sort Key:` line with its
sets' `Group Key:` lines indented under it. The hashed strategies take the
sets in rollup order.

goopg:

- `ExtractGroupingRollups` (groupingsets\_rollups.go) ports both functions,
  including Hopcroft-Karp's search order, so the chains are PG's.
  `Aggregate.Rollups` holds the result for every grouping-sets aggregate.
  The ORDER BY slots come from `groupingSetsSortSlots`, which matches by
  expression as `processedGroupOrder` does.
- The sorted strategy covers several rollups.
  - `costSortedRollups` is `create_groupingsets_path`'s AGG\_SORTED price:
    each later rollup adds a `cost_sort` of the input rows (with no input
    cost) and a `cost_agg`, using its own column and group counts.
  - Several rollups carry no pathkeys.
  - The sorted-rollup paths now count disabled nodes: the input's, plus
    one per later rollup's sort when `enable_sort` is off. Without this,
    regress groupingsets' `enable_sort = off` CUBE lost PG's
    MixedAggregate.
- The executor emits AGG\_SORTED rows rollup by rollup, each along its own
  order. EXPLAIN prints the rollup chain and orders the hashed keys by
  rollup.

Witnesses:

- `TestSortedGroupingSetsRollupsMatchPG`, with PG 18.3's plans:
  - `GROUPING SETS ((ten), (two))` and `CUBE (ten, two, v)` with
    hashagg off. The CUBE's three chains are `ten, two, v`, then `two, v`,
    then `v, ten`; its 184 rows match PG's order by md5.
  - The hashed key order.
- `TestExtractGroupingRollups`.

Movement:

- regress groupingsets: 1875 → 1736 diff lines (sorted multi-rollup plans,
  the reordering test's `Group Key: v, b, a`).
- TPC-H and TPC-DS plans are unchanged: their grouping sets are single
  ROLLUPs.

Evidence: `analysis/m0146/m0146-0020b/`.

Still open (M0146-0020b):

- **The mixed strategy.** For sorted input, `consider_groupingsets_paths`
  chooses which rollups to hash with a knapsack bounded by `hash_mem`
  (groupingsets.sql's "test the knapsack" plans MixedAggregate). The
  unsorted arm's `unhashed_rollup` reads coincidentally sorted input that
  way too. goopg offers all-sorted or all-hashed only.
- Expansion order: `expand_grouping_sets` sorts the sets with `list_sort`,
  which is unstable from seven sets on. goopg's sort is stable, so equal-length sets
  could pair into different chains (ledgered).
