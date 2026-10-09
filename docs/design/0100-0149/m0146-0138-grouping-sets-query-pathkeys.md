# M0146-0138 — grouping sets: first-rollup query pathkeys, and the ORDER BY election under a LIMIT

Status: done 2026-10-09 (5aab293b7). Parent: M0146-0014a.

## Problem

The M0146-0014a sweep found three grouping-sets divergences:

1. **Q67 at SF0.25.** PG feeds the ROLLUP from a `Gather Merge` over a
   per-worker Sort. goopg sorted above a `Gather`.
2. **Q67 at SF1.** goopg chose a MixedAggregate. Its hash part was priced
   as a single table where PG prices one per set, and it was not held to
   `hash_mem`.
3. **Q18 at SF1.** PG's sorted rollup GroupAggregate (about +8 over its
   input) lost to goopg's MixedAggregate.

## PG behaviour

- **Query pathkeys.** For grouping sets, `standard_qp_callback`
  (planner.c) sets `group_pathkeys` from "the first RollupData's
  groupClause", and they become `query_pathkeys`.
  `generate_useful_gather_paths` then files a partial path sorted in the
  rollup's order, which is Q67's worker-sorted Gather Merge.
- **Candidate choice.** `create_grouping_paths` keeps both the sorted
  rollup and the MixedAggregate. `create_ordered_paths` puts a Sort over
  each for the ORDER BY. Only then does the final rel's
  `get_cheapest_fractional_path` apply the LIMIT. Behind the Sort every
  candidate's startup equals its total, so the cheaper total wins (Q18).

## Change

- **Query pathkeys** (`groupingSetsClauseItems`, querypathkeys.go).
  `groupClauseItems` returned nil for grouping sets. It now builds the
  first rollup's order from the statement and produces default-ordered
  items:
  - It maps the sets onto the flattened GROUP BY slots.
  - It runs the aggregate builder's own `ExtractGroupingRollups`, steered
    by ORDER BY through `groupingSetsSortSlots`.
  - It returns nothing when a first rollup has no columns (only the grand
    total).

  The M0146-0134 FD pruning is skipped for grouping sets, because
  `remove_useless_groupby_columns` returns early for them.
- **The ORDER BY election** (upperorderedgrouping.go).
  `electOrderedGrouping` declined whenever no candidate's emission order
  translated, which is always the case for grouping sets. Its reasoning
  was that every candidate then needs the same Sort, so the loop could
  only re-price. That fails under a LIMIT: the grouping step had already
  picked by fraction before the Sort, where a hashed candidate's early
  startup counts. The loop now also runs when a fraction is set and there
  are several candidates, so the pick happens after the Sort, as in PG.

## Verification

- **Tests.**
  - `TestDeriveQueryPathkeysGroupingSetsUseTheFirstRollup` covers ROLLUP,
    a two-set chain, and the grand total alone. The older "grouping sets
    decline" case now expects the first rollup's keys.
  - `TestElectOrderedGroupingRunsUnderAFractionWithoutTranslation`: the
    loop declines without a fraction and elects the lower-total candidate
    under one.
- **TPC-DS fire set.**
  - Q67 → MATCH at both scales. At SF1 the sorted input also settles the
    MixedAggregate finding.
  - Q18 at SF1 goes from 6 categories to 3, with PG's sorted rollup.
    `qual-placement` now appears there, on the cd1/customer join order
    that was already different, once the upper plan aligns.
  - Q22's categories are unchanged.
  - Plan matches go SF0.25 52 → 53 and SF1 38 → 39. All results are
    identical.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases, `groupingsets` included): unchanged.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **MixedAggregate costing.** The hashed part is priced as one table
  rather than per set, and `hash_mem` is not enforced. Q67 no longer
  depends on it, but the costing is still wrong.
- **Q22's MixedAggregate.** The post-Sort election now picks a
  lower-total MixedAggregate candidate: startup 27796.94 and input width
  916, where PG shows startup 3071.50 and width 208. The shape and
  categories are unchanged.
- **Q18 at SF1.** Its remaining `[join-order, scan-type, qual-placement]`
  is the cd1/customer probe order.
