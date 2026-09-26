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
