# M0148-0001e — GROUP BY keys reordered to the input's pathkeys

**Parent:** M0148-0001 `aggregates.sql` regress coverage closure · **Status:**
slice 1 landed `fed4f53ed` (run-2 loop #15, 2026-11-02); slice 2 (range-table
size qualification) landed `f1709f7ab` (run-2 loop #16); slice 3 (merge-join
outer Incremental Sort) landed `f26987104` (run-2 loop #17); slice 4
(reordering over presorted\-aggregate keys) landed `058e52bae` (run-2 loop
#18); slice 5 (sorted Agg paths inherit disabled\_nodes) landed `11579ae1d`
(run\-2 loop #19); slice 6 (VERBOSE prefix rule) landed `6d6fa7019`
(run\-2 loop #20); the task stays open
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

## Slice 2 — upper-node qualification by the range-table size

PG's EXPLAIN qualifies sort and group keys, join conditions and upper quals
when `es->rtable_size > 1` (`explain.c`: `list_length(plannedstmt->rtable)`
less the RTE\_GROUP entry). The flat range table keeps every RTE a plan node
no longer shows — a FROM subquery or view pulled up into its parent, a
Subquery Scan `setrefs.c` strips as trivial, a relation removed by join
removal. The `q1` case (`FROM (SELECT * FROM btg ORDER BY …) q1 GROUP BY …`)
prints `Group Key: btg.x, btg.y, btg.w, btg.z` in PG; goopg counted only the
plan-visible relations and printed bare `x, y, w, z` (corrected 2026-11-02:
slice 1's records had the direction reversed).

- Every RTE already consumes one RTID from the statement scope
  (`rtableScope`, recorded choice F6), so the scope's allocation count is the
  range-table size. `planWithRtableSize` (`internal/optimizer/planner.go`)
  reports it and the EXPLAIN branch stores it on `Explain.RtableSize`.
- `explainNames.qualify()` (`internal/executor/explain_names.go`) ORs it with
  the plan-visible relation count; `newExplainNamesSized` carries it from the
  text, ANALYZE, JSON and JSON\+stats renderers. Zero means unknown and keeps
  the plan-visible rule.
- Regress runner, HEAD vs change over 34 EXPLAIN-heavy suites: aggregates
  −3/−3 lines, incremental\_sort −66/−66, window −60/−60, groupingsets −7/−7,
  subselect −3/−3, join −2/−2, select\_distinct −1/−1; the other 27 suites
  unchanged. Lines left partly qualified (`Sort Key: unique1,
  tenk1.stringu1`) sit on plans that already differ and name a lateral
  subquery's derived column goopg cannot bind.
- Pin: `TestExplainQualifiesForStrippedSubqueryRTE`.

## Slice 3 — merge join sorts a presorted outer incrementally

The merge-join case (`btg t1 JOIN btg t2 ON t1.w = t2.w AND t1.x = t2.x AND
t1.z = t2.z GROUP BY t1.w, t1.z, t1.x` with hash and nested-loop joins off)
was not a merge-key election gap: goopg's `sortInnerAndOuter` already offers
one rotation per merge key, as `sort_inner_and_outer` does. What it lacked is
PG 18's incremental arm of `try_mergejoin_path` /
`try_partial_mergejoin_path` (`joinpath.c`): `pathkeys_count_contained_in`
counts how much of the outer sort ordering the outer path already delivers
(`outer_presorted_keys`), and with `enable_incremental_sort` on and a
non\-empty prefix `initial_cost_mergejoin` prices `cost_incremental_sort`
while `create_mergejoin_plan` builds an Incremental Sort. The inner never gets
one — incremental sort supports no mark/restore. goopg always priced a full
Sort, so the `x`\-first rotation (t1's (x, y) index scan presorted on x) never
beat the written (w, x, z) order.

- `mergeOuterIncrementalSort` (`internal/optimizer/joinpathsmerge.go`) builds
  `incrementalSortPathWithGroups` over the outer when its pathkeys cover a
  leading prefix of the merge ordering, with the presorted group count from
  `searchRelNumGroups` (as the Gather Merge arm does); nil falls back to the
  plain Sort.
- Used by `addMergeJoinPath` (the `sort_inner_and_outer` arm) and
  `tryPartialMergeJoinPath` (the partial twin); the
  `generate_mergejoin_paths` arm never sorts its already\-ordered outer.
- Regress runner, HEAD vs change over 34 suites: aggregates 390 → 377 diff
  lines (the block is byte\-identical: Merge Cond (x, w, z), Incremental Sort
  Presorted Key t1.x, GroupAggregate with no Sort), incremental\_sort
  326 → 307; the other 32 suites unchanged.
- Pin: `TestMergeJoinOuterIncrementalSort`.

## Slice 4 — reordering over the presorted\-aggregate keys

`adjust_group_pathkeys_for_groupagg` (`planner.c`) appends an ordered or
DISTINCT aggregate's sort keys behind the GROUP BY keys in
`root->group_pathkeys`; `group_keys_reorder_by_pathkeys` reorders only the
first `num_groupby_pathkeys` of that list (the walk over the input's pathkeys
stops at the first one past them) and `list_concat_unique_ptr` keeps the
aggregate keys behind the reordered prefix. Slice 1 offered no reordering at
all when the presorted\-aggregate keys were in use, so
`SELECT y, x, array_agg(distinct w) FROM btg WHERE y < 0 GROUP BY x, y`
sorted on (x, y, w) above the `btg_y_x_w_idx` Index Only Scan, where PG
groups as `y, x` straight off it.

- `groupKeyPositions` now always returns the GROUP BY positions;
  `groupKeyOrderings` reorders that prefix of the key list (checked to be the
  processed group clause, else only the written order is offered), compares
  the matched count with the GROUP BY count, and appends the aggregate keys
  behind the reordered prefix.
- Regress runner, HEAD vs change over 34 suites: aggregates 377 → 372 diff
  lines; the other 33 unchanged.
- Pin: `TestGroupByReorderKeepsPresortedAggregateKeys`.

## Slice 5 — sorted Agg paths inherit their input's disabled\_nodes

`cost_agg` (`costsize.c`) sets `path->disabled_nodes = input_disabled_nodes`
for every Agg path, plus one for `AGG_HASHED` under `enable_hashagg = off`.
goopg's hashed and rollup arms set their counts, but the sorted GROUP\_AGG
arms built their Agg with none, so a GroupAggregate over a Sort that
`enable_sort = off` disabled compared as enabled. `group by g%10000` with
`work_mem = 64kB`, `enable_hashagg` on and `enable_sort` off is a
HashAggregate in PG 18.3 — the Sort is disabled however much the hash
spills — and goopg elected GroupAggregate over a Sort on cost.

- `addAggPath` floors the path's `DisabledNodes` at its children's sum; arms
  that already count more (hashed under `enable_hashagg = off`, rollup sorts)
  keep theirs.
- Regress runner, HEAD vs change over 34 suites: aggregates 372 → 362 diff
  lines; join 15046 → 15048 is the known nondeterministic row order of an
  unordered hash left join (re\-run standalone, the base build shows the swap
  and the change does not); the other 32 suites unchanged.
- Pin: `TestSortedAggInheritsDisabledSort`.

## Slice 6 — EXPLAIN VERBOSE qualifies quals and keys

`explain.c` forces column prefixes under VERBOSE: `show_scan_qual` uses
`useprefix = IsA(plan, SubqueryScan) || es->verbose`, while
`show_upper_qual`, `show_sort_group_keys`, `show_grouping_sets`,
`show_window_def` / `show_window_keys` and `show_memoize_info` use
`es->rtable_size > 1 || es->verbose`; only `show_plan_tlist` (the Output:
list) stays on `rtable_size`. goopg never let VERBOSE force a prefix (ledger
M0125\-0039 arm a), so single\-relation VERBOSE plans printed `Group Key: y,
x` / `Index Cond: (y < 0)` where PG prints `btg.y, btg.x` / `(btg.y < 0)`
(`sqljson.out` shows the same rule: `Sort Key: ((i.i % 2))` beside a bare
`Output: ((i % 2)), i`).

- `explainNames` carries `es->verbose` (`newExplainNamesSized(n, rtableSize,
  verbose)`, set by the text, ANALYZE, JSON and JSON\+stats renderers);
  `qualify()` ORs it in for upper nodes, and `qualifyScan()` is
  `show_scan_qual`'s rule for a scan node's own quals (text walker and the
  JSON Filter property). Output: lines go through neither.
- Regress runner, HEAD vs change over 34 suites: aggregates 362 → 360,
  explain 592 → 578, groupingsets 872 → 868, join 15046 → 15030, limit
  126 → 120, subselect 1462 → 1438, window 1898 → 1892 diff lines; 27
  unchanged. Lines that stay different are prefixed lines inside hunks whose
  plans already differ.
- Pins: `TestExplainVerboseQualifiesKeysAndQuals`; the VERBOSE arm of
  `TestExplainSortGroupKeyParenthesisation` now asserts PG's
  `((agg_data.g % 10000))`.

## Remaining (task stays open)

- The `array_agg(DISTINCT w)` block's remaining lines: the Output line
  (M0148\-0001f) and a `Filter` that repeats the Index Cond.

- The verbose `array_agg(DISTINCT …)` case and the `Disabled: true` lines
  (the latter belong to M0148-0001f).
