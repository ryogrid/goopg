# M0146-0142 — query pathkeys from ordered aggregates, and a presorted plain aggregate

Status: done 2026-10-09 (68627d4cc). Parent: M0146-0136.

## Problem

TPC-DS Q95 at SF0.25 computes `count(DISTINCT ws1.ws_order_number)` with
no GROUP BY.

- **PG** feeds the aggregate from a `Gather Merge` over a per-worker
  `Sort (ws1.ws_order_number)`, with no Sort above it.
- **goopg** asked the search for no order. Its plan differed in six
  categories: `[join-order, join-method, scan-type, parameterisation,
  sort-strategy, parallelism]`.

## PG behaviour

- **The query pathkeys.** `standard_qp_callback` (planner.c) builds
  `group_pathkeys` whenever there is a GROUP BY *or* ordered aggregates
  (`numOrderedAggs > 0`). It then runs `adjust_group_pathkeys_for_groupagg`:
  - Candidates are the ORDER BY / DISTINCT aggregates, skipping
    ordered-set aggregates, a FILTER whose arguments are not Vars or
    Consts, and volatile keys.
  - A greedy covering loop picks the key set that serves the most
    aggregates, appended behind the GROUP BY keys.
  - The result becomes `query_pathkeys`, so `generate_useful_gather_paths`
    files the worker-sorted Gather Merge.
- **The grouping step.** `add_paths_to_grouping_rel` runs
  `make_ordered_path` for AGG_PLAIN as well as AGG_SORTED. An input whose
  pathkeys already contain the required keys feeds the aggregate with no
  Sort.

## Change

- **`adjustGroupPathkeysForGroupAgg`** (querypathkeys.go) runs after the FD
  pruning in `deriveQueryPathkeySets` (not for grouping sets, where PG
  asserts it never runs).
  - It is the parser-level twin of the aggregate stage's
    `presortedAggKeysOrAbsent`, with the same candidate rules.
  - It honours `enable_presorted_aggregate`.
  - DISTINCT sort lists are the ORDER BY items followed by the arguments,
    as `aggregateSortlist` builds them; constants are dropped.
- **`bestCoveringAggPathkeys`** (groupingpaths.go) is the greedy loop,
  extracted from `presortedAggKeysOrAbsent` so both twins share one
  selection.
- **The AGG_PLAIN arm** of `addGroupingPaths` now applies the grouped
  arm's `is_sorted` test (`inputNodePathkeys` through the searched rel's
  equivalence classes). Before, it always sorted.
- **goopg-specific declines** (both keep the GROUP BY keys unchanged
  rather than claim a partial order):
  - a GROUP BY item that does not resolve to a searched column;
  - an ordered aggregate whose sort key is neither a column nor a
    constant. goopg's pre-search pathkeys name columns only, and dropping
    just that aggregate would change which set PG's loop elects.

## Verification

- **Tests.**
  - `TestDeriveQueryPathkeysOrderedAggregates` covers: no GROUP BY;
    appended behind GROUP BY; aggorder direction with a dropped constant;
    the most-covering set; a stronger set absorbing a weaker one; plain
    aggregates; the expression decline; FILTER; and the GUC off.
  - `TestAddGroupingPathsPlainPresortedInputTakesNoSort` fails without the
    PLAIN-arm change.
- **TPC-DS fire set.** Only Q95 at SF0.25 moves: 6 categories → 1
  (`[scan-type]`), with PG's Gather Merge/Sort shape and no extra Sort.
  SF1 has no fires. Matches are unchanged (SF0.25 53, SF1 39).
- **TPC-H.** Plans are byte-identical; the acceptance arm matches on
  values.
- **Regress A/B** (32 cases, `aggregates` included): no change.
  - `join` and `stats_ext` differed, but two HEAD runs differ from each
    other by the same lines.
  - The candidate's `join` matches the second HEAD run exactly.
- **Gates.** Units, TPC-H spotcheck, SF0.25 sweep (PASS=98, SKIP=1) and
  ea-ratchet (1) all PASS.

## Not covered (ledgered)

- **Q95's `[scan-type]`.** The `web_returns_pkey` probe is an Index Scan
  where PG uses an Index Only Scan. SF1 already had this residual.
- **Expression sort keys.** goopg's pre-search pathkeys cannot express
  `count(DISTINCT a + 1)`, so such queries keep no ordered-aggregate query
  pathkeys.
- **Ordered aggregates written only in the window spec or in a CASE.**
  These are not collected at all (M0146-0143).
