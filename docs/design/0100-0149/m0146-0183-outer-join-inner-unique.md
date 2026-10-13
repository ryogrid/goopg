# M0146-0183 — an outer join with a unique inner takes PG's early-exit costing

Status: closed 2026-10-14.

## Problem

TPC-DS Q5's `wsr` branch computes `web_returns LEFT JOIN web_sales ON
(wr_item_sk = ws_item_sk AND wr_order_number = ws_order_number)`. Both
engines plan it as a Hash Right Join that builds `web_returns` and probes
`web_sales`, with the same Hash startup (780.00). At SF0.25 the totals
differed:

- PG 18.3: 8268.33;
- goopg: 11325.71, about 4049 above its inputs where PG adds 945.

## Diagnosis

PG's total decomposes exactly:

- 780.00, the startup;
- 6543.56, the probe scan;
- 899.78, `cpu_operator_cost × 2 hash clauses × 179956 probe rows`;
- 44.99, the rest.

That last term is `final_cost_hashjoin`'s early-exit branch, which
applies to SEMI and ANTI joins and to any join whose inner rel is unique
(`extra->inner_unique`):

- `web_returns` is unique on its primary key (wr_item_sk, wr_order_number),
  which is exactly the hash clause set.
- `compute_semi_anti_join_factors` takes the LEFT join's own
  SpecialJoinInfo, so `eqjoinsel` uses its inner-join arm.
  `outer_match_frac` is the tiny inner-join selectivity, so
  `outer_matched_rows` rounds to 0 and no rows are charged as joined tuples.
- Every probe row is priced as unmatched:
  0.005 × 179956 × `clamp_row_est(17920 / 32768)` (= 1) × 0.05 = 44.99.

goopg's `hashJoinFinalCostInputFor` proved inner-uniqueness only for INNER
joins; its comment recorded the outer-join case as not ported. The join
therefore took the generic bucket walk and charged `approx_tuple_count`'s
tuples.

PG's `add_paths_to_joinrel` (joinpath.c) sets `extra.inner_unique` through
`innerrel_is_unique` for every join type its switch sends to the default
arm: INNER and the outer joins LEFT, RIGHT and FULL. SEMI and ANTI are
false. `final_cost_nestloop` reads the same flag. goopg's merge-join arm
already asked for every join type except SEMI and ANTI, so the hash and
nested-loop arms were the siblings out of step.

## Change

- `innerUniqueJoinType` (hashjoin_innerunique.go) names the join types PG
  asks `innerrel_is_unique` for: INNER, LEFT, RIGHT and FULL.
- `hashJoinFinalCostInputFor` and the nested-loop arm in `joinPairPaths`
  (joinpaths.go) use it instead of `jt == JoinInner`.
- The proof (`innerRelProvenUnique`) and the factors
  (`innerUniqueMatchFactors`, the inner-join selectivity and the inner's
  row count) are unchanged. For an outer join PG computes the same values,
  because its SpecialJoinInfo sends `eqjoinsel` to the inner-join arm.
- goopg's planner-internal RIGHT_SEMI and RIGHT_ANTI orientations stay on
  the generic bucket walk (ledgered).

## Evidence

Files are in `analysis/m0146/m0146-0183/`.

- **Q5 at SF0.25:** the Hash Right Join costs 780.00..8221.33 against PG's
  780.00..8268.33. The 47.00 left is the probe scan's own difference
  (6496.56 against 6543.56). The plan shape is unchanged and Q5 still
  matches PG.
- **Fixture:** a LEFT join to a table unique on the two join keys, with
  merge and nested-loop joins disabled. PG 18.3 prints
  `Hash Right Join (cost=305.00..2371.01)`, and goopg now prints the same
  figures. HEAD hashed the large side instead (`Hash Left Join
  (cost=3041.00..7021.00)`).
- **Fire set:** match 76 / 64 is unchanged. SF0.25 join-method goes
  9 → 10, from Q77 (see below). Q5, Q40, Q75 and Q80 fire on cost text
  only.
- **SF0.25 sweep:** 99 of 99 pass. Q77 is the only structural change.
- **TPC-H:** the acceptance arm is value-identical.
- **Regress A/B:**
  - `subselect` and `aggregates` are identical.
  - `join` moves 15 lines closer to PG's expected output. Its changed cases
    are join-removal queries, where PG removes the join and goopg switches
    between Merge and Hash Left Join.
  - `partition_join` moves 7 lines closer.
- **Tests:**
  - `TestExplainOuterJoinInnerUniqueHashCost` checks PG's orientation and
    cost bound on the fixture.
  - `TestHashJoinFinalCostInputNonInnerJoinTypes` checks that LEFT, RIGHT
    and FULL get INNER's inner-unique factors and that RIGHT_SEMI keeps the
    generic walk.
  - Both fail with the join-type widening reverted.

## Q77 near-tie (filed M0146-0185)

Q77's web channel joins two grouped subqueries (`ws`, `wr`) on
`wp_web_page_sk` with a LEFT join. `wr` is grouped by that key, so it is
unique (`groupedLeafDistinctFor`) and the hash join now prices like PG's.
In goopg's numbers the Hash Left Join (11030.86) beats the Merge Left Join
(11030.96) by 0.10, and PG's `add_path` makes the same choice on such a
tie (`compare_path_costs_fuzzily` at 1.0000000001). PG keeps the merge
because its inputs are different and cheaper: each side scans
`web_page_pkey` with an Index Only Scan under a nested loop with a Join
Filter, where goopg seq-scans `web_page` and hash or merge joins. That
input divergence is pre-existing; it is filed as M0146-0185.

## Not ported (ledgered)

- `innerrel_is_unique` for goopg's planner-internal RIGHT_SEMI and
  RIGHT_ANTI orientations.
- The executor does not stop at the first match for an inner-unique outer
  join. Only the costing changes.
