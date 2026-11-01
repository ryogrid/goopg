# M0148-0001e — GROUP BY keys reordered to the input's pathkeys

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed `fed4f53ed` (run-2 loop #15, 2026-11-02); the task stays open
for the remaining `btg`-region hunks (below)
**Target:** aggregates hunks around expected-lines 2607-2759 (the `btg`
block: `GROUP BY y, x` / `GROUP BY z, y, w, x` over `btg_x_y_idx`)

## Why

```
CREATE INDEX btg_x_y_idx ON btg(x, y);
SET enable_hashagg = off; SET enable_seqscan = off;
EXPLAIN (COSTS OFF) SELECT count(*) FROM btg GROUP BY y, x;
```

PG 17+ prints `GroupAggregate (Group Key: x, y)` directly over the
`Index Only Scan using btg_x_y_idx` — no Sort. goopg compared the GROUP BY
keys only in their written order, so it planned `Sort (Sort Key: y, x)` under
the aggregate. With two more keys (`GROUP BY z, y, w, x`) PG puts an
`Incremental Sort (Sort Key: x, y, z, w; Presorted Key: x, y)` over the index
scan; goopg did a full Sort.

## PG mechanism

`add_paths_to_grouping_rel` (planner.c) asks, for every input path,
`get_useful_group_keys_orderings` (pathkeys.c) for the orderings worth
trying, and builds each with `make_ordered_path` (no Sort when the path is
already ordered, an Incremental Sort when a prefix is, a full Sort
otherwise):

1. The written order (`root->group_pathkeys`) is always offered.
2. When `enable_group_by_reordering` is on and the path has pathkeys,
   `group_keys_reorder_by_pathkeys` walks the path's pathkeys and moves each
   one that is also a group key to the front, stopping at the first that is
   not; the remaining group keys follow in written order.
3. That ordering is kept when at least one key matched, the path's pathkeys
   are not already a prefix of the written order, and either incremental
   sort is enabled or every group key matched; duplicates of an ordering
   already in the list are dropped.

The reordered `PathKey` list becomes the Agg's `grpColIdx` order, so
`Group Key:` prints the reordered columns.

## goopg implementation

- `groupKeyOrderings` / `reorderGroupKeysByPathkeys`
  (`internal/optimizer/groupingpaths.go`) port steps 1-3 over the searched
  rel's equivalence-aware pathkey equality (`pathkeyUsefulness`). Each
  ordering carries the reordered `SortKey`s and the permutation of the
  aggregate's group positions.
- The sorted `GROUP_AGG` arms — the cheapest-input seed (presorted /
  incremental / full sort) and every searched candidate input — loop over the
  orderings; a reordered spec records the permutation in `GroupKeyOrder`
  (composed with any earlier order), so EXPLAIN prints the reordered keys
  while `GroupExprs` keep their written order (the output targetlist is
  unchanged).
- `enable_group_by_reordering` (registered, previously unconsumed) reaches
  `PlannerSettings.EnableGroupByReordering` and the plan-cache key in
  `internal/postmaster/dispatch.go`.

## Evidence

- Regress runner, HEAD vs change: aggregates −126/+150 → −105/+122 diff lines;
  partition\_aggregate, select\_distinct, incremental\_sort, groupingsets,
  join, union, window unchanged.
- The two `btg` shapes above are byte-identical with PG; the `q1` subquery
  case lost its Sort and Subquery Scan.
- Pins: `TestGroupByKeysReorderedToInputPathkeys`
  (`internal/executor/explain_group_by_reordering_test.go`: both shapes,
  values, and the GUC-off arm keeping `Group Key: y, x`);
  `TestIndexOrderedGroupingCanonical` now looks through the searched input's
  EXPLAIN-invisible boundary Project.
- Gates: tpch spotcheck, TPC-DS SF0.25 sweep (99 shapes same), TPC-H
  acceptance arm, fire set on SF0.25 / SF1 / TPC-H — fires=none.

## Remaining (task stays open)

- `q1` case: goopg prints `Group Key: btg.x, btg.y` where PG prints bare
  `x, y` (single-relation deparse context of the pulled-up subquery).
- The merge-join case: PG elects the merge keys in the order that lets the
  join output feed the reordered GROUP BY presorted; goopg keeps the written
  merge-key order and sorts above the join.
- The verbose `array_agg(DISTINCT …)` case and the `Disabled: true` lines
  (the latter belong to M0148-0001f).
