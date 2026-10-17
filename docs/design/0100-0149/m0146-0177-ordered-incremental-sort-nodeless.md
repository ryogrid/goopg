# M0146-0177 — an Incremental Sort over a node-less grouping candidate

Status: closed 2026-10-13.

## Problem

TPC-DS Q60 (SF0.25) ends in `ORDER BY i_item_id, total_sales` over a
GroupAggregate grouped by `i_item_id`, so the input is presorted on the first
key.

- PG 18.3 builds an Incremental Sort.
- goopg built a full Sort, which won by about 0.6 cost units (0.002%).

PG never offers both. `create_ordered_paths` (planner.c) says "We'll just do a
sort if there are no presorted keys and an incremental sort when there are
presorted keys".

## Cause

`addOrderedPaths` (upperordered.go) already followed that rule for its seed,
but only when the input path carried a finished node. The node is what
`cost_incremental_sort`'s `estimate_num_groups` reads statistics from.

- The grouping election (`electOrderedGrouping`, upperorderedgrouping.go)
  offers node-less `PathAgg` candidates.
- The DISTINCT election offers node-less `PathDistinct` candidates.

A presorted candidate from either therefore took the full-Sort arm.
`addIncrementalSortPaths` skips node-less inputs as well, so no Incremental
Sort was offered at all.

## Change

- `addOrderedPaths` takes a `statsNode`, the node the sort runs over:
  - `createOrderedPaths` passes its input;
  - the grouping election passes the aggregate's ordered input node;
  - the DISTINCT election passes its distinct node.

  The presorted arm builds the Incremental Sort whenever that node is known.
- The DISTINCT election's winner switch accepts an Incremental Sort over
  `Distinct`/`DistinctOn`, mirroring its Sort case. The grouping election
  already accepted one.
- **Sibling:** `upperNarrowChildren`, the B-01c upper narrowing descent
  (upper\_narrow\_apply.go), had a `Sort` arm and no `IncrementalSort` arm.
  - With an Incremental Sort on top, the aggregate below kept its full input
    width.
  - TPC-DS Q54's inner Sort then printed
    `sum(store_sales.ss_ext_sales_price) / 50` instead of PG's
    `my_revenue.revenue / 50`.
  - The arm is added. The descent only reaches narrowing sites below, and an
    Aggregate's output does not change when its input is narrowed.

## Evidence

Files are in `analysis/m0146/m0146-0177/`.

- **TPC-DS fire set:**
  - SF0.25 match 74 → 75: Q60 matches, sort-strategy 13 → 12.
  - Q54's top is now PG's Incremental Sort.
  - SF1 match is unchanged at 46. SF1's Q60 diverges elsewhere because of the
    stale load.
- **TPC-H:** no plan changes. The acceptance arm is value-identical (22/22).
- **Regress A/B:** `aggregates`, `incremental_sort`, `select_distinct`, `limit`
  and `subselect` are identical.
- **Tests:**
  - `TestOrderedPresortedAggOffersIncrementalSortOnly` (optimizer) fails with
    the old `input.node` gate.
  - `TestExplainIncrementalSortOverGrouping` (postmaster) fails with either
    half removed.

## Known residue

- **M0146-0181:** Q58 (SF0.25) already had an Incremental Sort on top. Its
  aggregates are now narrowed, and EXPLAIN's Join Filter deparse prints the
  narrowed `sum()` arguments with the wrong names
  (`sum(ss_items.item_id)`).
  - Values are identical: a widened Q58 returns the same rows before and
    after.
- **Inside a subquery:** goopg prints a bare `c` where PG prints `r77.c`
  (`Group Key` / `Sort Key`). This predates this change and is ledgered.
