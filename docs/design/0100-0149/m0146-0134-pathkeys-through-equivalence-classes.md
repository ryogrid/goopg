# M0146-0134 — pathkeys match through equivalence classes

Status: done 2026-10-09 (dd7e9df07). Parent: M0146-0014a.

## Problem

TPC-DS Q64's `cross_sales` CTE groups on `i_product_name, i_item_sk, …`
over a 16-relation join. Its driving input is a GroupAggregate ordered on
`catalog_sales.cs_item_sk`, which the join equates with
`store_sales.ss_item_sk` and `item.i_item_sk`.

- **PG** plans `GroupAggregate <- Incremental Sort (Presorted Key:
  item.i_item_sk)`.
- **goopg** sorted the 14-key group input fully.

## PG behaviour

PG's pathkeys point at equivalence classes (`make_canonical_pathkey`). An
ordering on any member of `{cs_item_sk, ss_item_sk, i_item_sk}` is the
same pathkey, so three steps keep and use the order:

1. **Truncation keeps it.** `truncate_useless_pathkeys` keeps the outer's
   ordering because it is a prefix of `query_pathkeys`.
2. **The group pathkeys lead with `i_item_sk`.** `query_pathkeys` is
   `group_pathkeys`, built by `standard_qp_callback` from the processed
   group clause. That runs after `remove_useless_groupby_columns`, which
   drops `i_product_name` because `item_pkey` determines it. So the
   group pathkeys start at `i_item_sk`.
3. **One ordering step is chosen.** `make_ordered_path` builds exactly
   one ordering step for the cheapest input: an Incremental Sort when it
   has presorted keys.
   - `cost_incremental_sort` clamps `input_tuples` to 2 before
     estimating the presorted groups.
   - So even a one-row input's startup is half its input's run.

## What goopg lacked

The search runs through GEQO (16 relations exceed `geqo_threshold`).
Five gaps lay between goopg and PG:

1. **GEQO tours lost query-level facts.** `freshEvalCtx` copied only part
   of them and dropped `queryPathkeys`, `parallelModeOK`, `cat`,
   `itemSpans`, `problemItems`, `semiDerivedRHS` and
   `orClauseSelDivisor`. With no `query_pathkeys`, every tour truncated
   every ordering as useless.
2. **Group pathkeys kept determined columns.** `deriveQueryPathkeys` kept
   `i_product_name` first.
3. **Pathkey comparison was syntactic.** This held at every consumer:
   truncation's ordering arm, the grouping arms and the ORDER BY arms.
4. **No two-tuple clamp before the group estimate.**
   `incrementalSortPathOver` estimated the presorted groups from the raw
   row count, so a one-row input counted one group and its startup
   equalled its total.
5. **Two competing ordering steps.** The grouping seed arm's syntactic
   check missed the presorted prefix and added a full Sort beside the
   search candidate's Incremental Sort. On the resulting fuzzy cost tie
   the Sort won.

## Change

- **GEQO** (geqo.go `freshEvalCtx`). Every query-level fact the DP search
  reads now rides into each tour.
- **Group pathkeys** (querypathkeys.go). `pruneUselessGroupPathkeys`
  applies `pruneUselessGroupByColumns`, the same pruning that shapes the
  aggregate's own group keys, to the derived group pathkeys.
- **Canonical comparison** (pathkeys_useful.go).
  `pathkeyUsefulness.countContainedIn` is `pathkeys_count_contained_in`
  over the rel's classes. A key matches an equal key, or another member of
  a class whose equality the rel enforces (the existing `equivalentWithin`
  of M0146-0005dv). Its callers:
  - `truncateUselessPathkeys`' ordering arm.
  - `addGroupingPaths`: the seed's is_sorted and presorted checks, and
    the search-candidate loop, all through `searchedJoinInputRelOf(child).usefulKeys`.
  - The ORDER BY arms (`addOrderedPaths` and `addIncrementalSortPaths`).
    They read the searched rel's classes from the new
    `RelOptInfo.SearchCandidateClasses`, which `createOrderedPaths` sets
    beside `SearchCandidates`. `electOrderedGrouping` replaces the
    candidates with aggregate-output ones and clears the field; it is
    restored on decline.
- **Incremental Sort costing** (incrementalsortpaths.go). The group
  estimate's tuple count is clamped to 2.

The coordinate assumption is the one the search-candidate arms already
make: candidate keys are validated against the input's published schema,
which truncates rather than translates. So they are written in the
search's binding coordinates, the same space as the classes' members.

## Verification

- **Tests** (eqclass_pathkeys_test.go): one pin per gap.
  - `TestGroupQueryPathkeysDropFunctionallyDependentColumns`
  - `TestCountContainedInThroughEquivalenceClass`
  - `TestGEQOTourContextCarriesQueryLevelFacts`
  - `TestIncrementalSortClampsTuplesBeforeGroupEstimate`
- **TPC-DS fire set at SF0.25.**
  - Q4 → MATCH; plan matches go from 50 to 51.
  - Q58 `[parameterisation, qual-placement]` → `[parameterisation]`,
    with `Incremental Sort, Presorted Key: item_1.i_item_id` as in PG.
  - Q64 loses sort-strategy.
  - CATEGORIES-EXCL-MATCH: join-order 39 → 38, join-method 18 → 17,
    sort-strategy 19 → 17, qual-placement 6 → 4.
- **TPC-DS fire set at SF1.**
  - Q58 and Q64 improve the same way.
  - Q4 and Q11 regress by `[join-method, qual-placement]`; see Not
    covered.
- **Results.** The SF0.25 sweep passes 96/96.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (32 cases): only `join`'s nondeterministic unordered row
  order changes.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **SF1 Q4/Q11 near-tie.** The canonical truncation keeps an ordered
  merge-join chain under the ORDER BY's Incremental Sort.
  - Its total is within fuzz of the plain Sort's (+39 for Q4, +6.5 for
    Q11), and its startup is far lower. `add_path` therefore keeps it, as
    `compare_path_costs_fuzzily` would.
  - PG's SF1 costs for these joins are 30–40% lower (the RELPAGES drift),
    so PG faces a different contest. At SF0.25 the same mechanism yields
    PG's plan.
  - This awaits the owner's SF1 reload.
- **The rollup arm** (`addGroupingPaths`' sorted grouping-sets loop) and
  `getCheapestFractionalPathOrdered` still compare syntactically.
- **Class scope.** goopg's classes are those a rel enforces
  (`equivalentWithin`), not PG's query-global classes. An ordering
  survives below the join that enforces its class through the merge arm
  (`usefulForMerging`) only.
- **Q64's remaining `[join-order, scan-type, qual-placement]`** is not
  routed yet; it goes to the next parity-closure sweep.
