# M0146-0102 — the keyless grouped aggregate

Status: done 2026-10-08 (6483d380b). Parent: M0146-0005.

## Problem

TPC-DS Q44's InitPlan is
`select avg(ss_net_profit) from store_sales where ss_store_sk = 4 and … group by ss_store_sk`.
PG prunes the key (`ss_store_sk` is equal to a constant) and plans
`Finalize GroupAggregate -> Gather -> Partial GroupAggregate`, with no
Group Key and no Sort.

goopg's `redundantConstGroupKeys` (M0146-0005ak) prunes constant-pinned
keys, but it refused to prune the last one. goopg's zero-key aggregate
always emits one row over empty input, while PG's keyless grouped
aggregate emits none. So goopg kept the key and sorted on it.

## PG behaviour

- **The key is dropped.** `standard_qp_callback` builds the group
  pathkeys with `remove_redundant = true`, and `processed_groupClause`
  keeps only the GROUP BY items whose pathkey survives. An item whose
  equivalence class holds a constant is dropped, and with every item
  pinned the list is empty.
- **The query stays grouped.** `parse->groupClause` is still set. The Agg
  node is `AGG_SORTED` with zero grouping columns, labelled
  `GroupAggregate`, or `Group` when there is no aggregate
  (`create_group_path`).
- **Empty input returns no row.** `nodeAgg.c` emits a group only from
  input rows, unlike `AGG_PLAIN`, which emits its single row even over
  empty input.

## Change

- **Pruning.** `redundantConstGroupKeys` may prune every key. When the
  statement has a GROUP BY and no key remains, the planner marks the node
  `Aggregate.GroupedNoKeys`.
- **Executor.** `aggregateOp.Open` skips the pre-created empty group for
  a `GroupedNoKeys` node, so its one group forms only from input rows.
- **Parallel split.** The Partial/Finalize split copies the node, so both
  halves carry the flag. An empty Partial contributes no state, and the
  Finalize, which builds its groups from the merged states, emits nothing.
- **EXPLAIN.** The node is labelled `GroupAggregate` (`Group` without
  aggregates), with the Partial/Finalize prefixes and no Group Key line.
- **Rewrite exclusion.** The constant-target rewrite for an aggregate with
  no keys and no aggregates (planner.go) excludes the node, because that
  rewrite assumes the one-row ungrouped semantics.
- **Uniqueness proofs.** They still see the pruned keys through
  `PrunedGroupInputs` (M0146-0101), as PG's `query_is_distinct_for` reads
  the original groupClause.

## Verification

- **Probe.** Plans and results are identical to PG 18.3 for:
  - a pinned GROUP BY with an aggregate, and with the key selected;
  - one without aggregates (`Group`);
  - one with HAVING;
  - one in a scalar subquery;
  - each over empty and non-empty input.
  Under forced parallel settings the shape is `Finalize GroupAggregate ->
  Gather -> Partial GroupAggregate`, with 0 rows over an empty filter.
- **Tests.** `TestKeylessGroupedAggregate` and
  `TestKeylessGroupedAggregateParallel` cover this; dropping the executor
  rule fails four empty-input checks.
- **TPC-DS fire set.** Only Q44 changes, and it is a full MATCH at SF1
  (34 → 35).
  - SF0.25: sort-strategy 24 → 23, parallelism 25 → 24, rendering 3 → 2.
  - SF1: join-order 52 → 51, sort-strategy 26 → 25, parallelism 39 → 38,
    rendering 3 → 2.
- **TPC-H.** Plans are byte-identical; acceptance arm values identical.
- **Regress A/B.** Twelve files, including aggregates and groupingsets,
  are identical apart from a join row-order flap.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep and ea-ratchet all PASS.
