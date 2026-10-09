# M0146-0131 — the LIMIT fraction elects an ordered grouping input (TPC-DS Q35)

Status: done 2026-10-09 (a949c38e5). Parent: M0146-0014a.

## Problem

TPC-DS Q35 groups on `(ca_state, cd_gender, cd_marital_status,
cd_dep_count, cd_dep_employed_count, cd_dep_college_count)`. It orders by
the same keys and keeps 100 rows.

PG 18.3 plans this shape at both scales:

    Limit  (cost=1095385.36..4090274.17 rows=100)
      ->  GroupAggregate
            ->  Incremental Sort  (Presorted Key: ca.ca_state)
                  ->  Nested Loop  (Join Filter: ca.ca_address_sk = c.c_current_addr_sk)
                        ->  Gather Merge -> Sort (ca_state) -> Parallel Seq Scan on customer_address ca
                        ->  Materialize -> (the store_sales / customer / customer_demographics chain)

Its total, 54.67M, is above the plan with a full Sort. Under `LIMIT 100`,
however, `get_cheapest_fractional_path` prices it at 4.09M. goopg kept a
full `Sort` over its cheapest-total join, at 58.33M (SF0.25), and the
parity diff showed six categories.

## What the trace showed

A DP trace of the private SF0.25 clone found the following.

- **The join search builds PG's path.** It is the accepted final-rel path
  `outer={ca} inner={rest}`, with `pathkeys=1` (`ca_state`), startup 23746
  and total 59.88M.
- **Grouping already offers it.** `addGroupingPaths`' search-candidate arm
  (M0146-0006/0027) files it as `upper.groupagg.searchcand`: an AGG_SORTED
  over an Incremental Sort, startup 1.197M and total 59.88M, accepted.
- **The ORDERED rel lost it.** `electOrderedGrouping` asks
  `groupingEmissionPathkeys` for each sorted candidate's emission order.
  That function translated only a `PathSort` or `PathGatherMerge` child,
  so it declined the Incremental Sort candidate (`keys=0
  contained=false`). ORDER BY then stacked a full Sort on it, whose
  startup equals the total (59.88M), and the candidate lost.

## Change

### Emission order through an Incremental Sort or a presorted input

`groupingEmissionPathkeys` (upperorderedgrouping.go) now also translates
two more child kinds:

- **`PathIncrementalSort`.** An Incremental Sort delivers its FULL
  pathkeys: `create_incremental_sort_path` sets `pathkeys = pathkeys`,
  not the presorted prefix.
- **`PathPrebuilt` already sorted on the group keys.** This is the
  presorted search candidate and the is_sorted arm of `addGroupingPaths`.
  Its pathkeys were validated against the input schema when it was filed.

`create_agg_path` (pathnode.c) copies `subpath->pathkeys` for AGG_SORTED
whatever the subpath is. The node-level twin `aggregateEmissionPathkeys`
(upperorderedinput.go) already admitted both shapes, through
`inputNodePathkeys` and `searchedTreePathkeys`. The positional group-key
check still decides. An input without pathkeys still declines, such as
the index variant's seed, which carries a narrowed `GroupKeyOrder` and
no pathkeys.

### EXISTS→ANY below pass-through wrappers

The new plan shape exposed a second gap.

**The symptom.** Q35's `customer` probe holds `EXISTS(...) OR EXISTS(...)`.
Under `Incremental Sort -> Nested Loop -> Materialize`, the probe kept
its per-row SubPlans, and each customer row re-ran the parallel
`web_sales ⋈ date_dim` hash join. Q35 went from 684 ms to over 600 s on
the fire set. EXPLAIN showed `EXISTS(SubPlan 1) OR EXISTS(SubPlan 2)`;
PG prints the hashed ANY form.

**The cause.** `rewriteExistsToAnyNode`'s node switch is fail-open by
design: an unknown node keeps its SubPlans. It had no arm for
`IncrementalSort`, `Memoize` or `Result`, and now has all three. Q35
runs in 3.5 s (SF0.25 sweep), and its rows are byte-identical to the old
plan's.

## Verification

- **Tests.**
  - `TestGroupingEmissionTranslatesIncrementalSortAndPresortedChild`:
    both new child kinds translate, and an Incremental Sort on other
    leading keys declines.
  - `TestRewriteExistsToAnyNodeDescendsPassThroughWrappers`: the walk
    reaches a Filter below each new wrapper.
- **TPC-DS fire set.**
  - Q35 at SF0.25 goes from six categories to MATCH; plan matches go
    from 49 to 50.
  - SF0.25 CATEGORIES-EXCL-MATCH: join-order 40 → 39, scan-type 23 → 22,
    parameterisation 19 → 18, sort-strategy 21 → 20, parallelism
    21 → 20, qual-placement 8 → 7.
  - Q6 and Q24 fire on cost noise only (≤ 0.05).
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only order changes.
  - In `join`, the unordered `ss1 left join ss2 on true` query flips
    back. It flipped in M0146-0130 too, so it is nondeterministic.
  - In `stats_ext`, a statistics-object name listing changes order.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **Q35 at SF1.** goopg's SF1 `customer_address` is about 955 pages,
  below `min_parallel_table_scan_size` (1024 pages); PG's copy is about
  1136. goopg therefore files no partial path for `ca`, and so no Gather
  Merge sort on `ca_state` to drive the ordered nested loop. This is the
  RELPAGES route, which the owner's SF1 reload resolves.
- **Runtime.** The new Q35 plan runs in 3.5 s, where the old plan took
  0.68 s. The materialized inner's Join Filter is evaluated per (ca, inner)
  pair until 100 groups complete. PG makes the same choice; the
  per-pair cost of goopg's executor is the gap.
- **The walker is still fail-open.** Any node kind it does not know still
  hides EXISTS quals below it. A completeness pin against every plan node
  type with a child would close the class.
