# M0146-0006 — Incremental Sort election: make_ordered_path in the grouping arm, the third ORDER BY arm on by default

Status: done (2026-10-04). Parent: M0141-S7. Released when M0146-0005 closed
(slice 116).

## What the task held

The task carried two filed resume points: S2b-9 and S2b-8.

- **S2b-9: an Incremental Sort over the ORDER BY seed.** Already landed by
  M0146-0005bp: `addOrderedPaths` sorts the cheapest input incrementally
  when its pathkeys deliver a leading prefix of the ORDER BY.
  - Q4's own residue is no longer a missing candidate. At the top joinrel,
    PG's cheapest path is a nested-loop chain over a merge join on
    `customer_id`, and goopg's 0.15%-cheaper chain carries no ordering. That
    is a join-order cost tie, routed by slice 116.
- **S2b-8: the sorted grouping arm** offered only a full Sort.
  PG's `make_ordered_path` (`./postgres/src/backend/optimizer/plan/planner.c`)
  handles an input to a sorted aggregate as follows:
  - a fully presorted input feeds the aggregate as is;
  - an input partially presorted on the group keys gets an Incremental Sort
    (under `enable_incremental_sort`, default on);
  - an input with no presorted key is offered only if it is the cheapest
    one, with a full Sort.
- **The third ORDER BY arm**, an Incremental Sort over the other partially
  presorted search candidates (PG's `create_ordered_paths` loop), existed but
  was gated off by `GOOPG_INCREMENTAL_SORT`.

## Change

- **The grouping arm** (`addGroupingPaths`, `groupingpaths.go`):
  - the cheapest input whose derived pathkeys share a leading prefix with
    the group keys gets an Incremental Sort instead of the full Sort;
  - each searched candidate with a partial prefix gets an Incremental Sort
    beside the fully presorted ones M0146-0027 already offered.
- **The third arm is on by default.** `GOOPG_INCREMENTAL_SORT=off` is the
  escape hatch, and the arm now also honours `enable_incremental_sort`.
  `scripts/planner-flags.env` was regenerated.
- **Safety fix in the third arm.** It wrapped the raw searched path, which
  lowers in the search's inner column order, not the row the boundary
  committed. It now rebuilds each candidate through `searchedCandidateInput`,
  as the presorted arm does. Under the old gate this never ran, and on by
  default it would have sorted and emitted the wrong columns.

## Effect

- **Fire set.** Only Q83 fired, with costs only; matches and categories are
  flat at both scales. The TPC-DS SORT witnesses (Q4, Q11, Q35, Q78) do not
  move: none of their inputs is partially presorted at the decision point.
  Their divergences are upstream (join order, Q35's Gather Merge on
  `customer_address`).
- **Regress A/B.** PG's `incremental_sort` case was added; it is unchanged.
  `aggregates` changed (diff 517 → 527 lines):
  - **toward PG:** the `t1.w, t1.z, t1.x` grouping over a merge join now
    reads PG's `Incremental Sort (Presorted Key: t1.w)`;
  - **away from PG:** `agg_sort_order` under `enable_seqscan = off` now
    elects an Incremental Sort over the `pkey` scan, where PG keeps
    `Sort` over the `c2` index. The cause is goopg's estimate of
    `c2 < 100` on a 100-row unique column: 33 rows in one path and 99 in
    another, where PG says 99 throughout. That feeds 33 presorted groups
    into `cost_incremental_sort` where PG uses 99. Filed as M0146-0009r.
- **Other gates.**
  - The sweep passed 96/96.
  - The TPC-H arm matched 24/24, and tpch-spotcheck passed.
  - ea-ratchet passed. Its "Q92 fixed" line predates this change.

Tests:

- `TestGroupAggIncrementalSortOverPresortedInput` (executor) fails on HEAD.
  It checks PG's GroupAggregate fed by an Incremental Sort, with
  `Presorted Key: a` over an `a`-ordered input, and that the values equal
  the hashed plan's.
- `TestAddIncrementalSortPathsDeclinesACandidateTheBoundaryCannotRebuild`
  pins the safety rule.
- `TestAddIncrementalSortPathsOffIsInert` and `TestIncrementalSortModeFromEnv`
  pin the new default and its escape hatch.
- `TestHashJoinForcedSpillMatchesDefaultWorkMemThroughSQL` now compares the
  join's rows sorted in the test rather than through an `ORDER BY`. The
  ORDER BY let the planner elect a merge join plus Incremental Sort, which
  never reaches the hash join the test instruments.

## Not done (ledgered)

- **GROUP BY key reordering.** PG 17's `get_useful_group_keys_orderings`
  reorders the GROUP BY keys to match a presorted input. goopg offers the
  Incremental Sort only on the written key order.
- **M0146-0009r.** The inconsistent range estimate above.
