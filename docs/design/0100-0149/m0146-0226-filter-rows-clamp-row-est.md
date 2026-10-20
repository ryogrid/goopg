# M0146-0226 — a restricted relation's rows round like clamp_row_est (TPC-DS SF0.25 Q4)

Status: landed 2026-10-19. Filed by the S9 pass at fe64139d5
(`analysis/m0146/m0146-0014/routing-20261019b/`). Evidence: `analysis/m0146/m0146-0226/`.

## 1. Record

TPC-DS SF0.25 Q4, sort-strategy, SHAPE-DIFF at depth 1 (under Limit: PG Incremental Sort | goopg Sort). It
persists under PG statistics.

- PG: an `Incremental Sort` (Presorted Key `t_s_secyear.customer_id`) over five Nested Loops. At their bottom is a
  `Merge Join` of two sorted CTE scans (`t_s_secyear`, `t_c_secyear`, 30 rows each).
- goopg: a full `Sort` over Nested Loops of unsorted CTE scans.

## 2. Cause

The two plans' CTE scans cost the same (0.00..32853.81 and 0.00..29867.10), but their row counts differ: goopg had
9 and 29 rows where PG has 10 and 30.

- PG's CTE output columns have no statistics, so each Q4 filter takes the default selectivities. `sale_type = 's'
  AND dyear = 2000` is 0.005² = 2.5e-5, and the first-year scans add `year_total > 0` (1/3).
- goopg's selectivities were already PG's exactly; a trace of `clauseSelectivity` showed 2.5e-5 and 8.33e-6.
- The rows came from `EstimateRows(*Filter)`, which called `scaleByFloat`. That converts with `int64(n * sel)`, a
  truncation: 1194684 × 2.5e-5 = 29.87 → 29, and × 8.33e-6 = 9.96 → 9.
- PG sizes a restricted relation in `set_baserel_size_estimates` as `clamp_row_est(tuples * selectivity)`, which
  rounds with `rint`: 30 and 10.
- The join search (`joinsearch.go`, `joinsearchseam.go`) and EXPLAIN both read the leaf's rows through this arm.
- The sibling `applyLocalFilterSelectivity` (cardinality.go) already rounded. The two paths that size the same
  relation disagreed.

With 29/9 rows, PG's Merge Join + Incremental Sort arm lost to the plain chain of Nested Loops.

## 3. Change

The `*Filter` arm of `EstimateRows` now returns `saturateRowEst(clampRowEst(child * filterSelectivity))`. It uses
the same `clampRowEst` (relsize.go, `math.RoundToEven` for `rint`) the relation sizing already uses. The other
`scaleByFloat` callers are join-cardinality fallbacks and are left alone, so the movement stays attributable;
whether they should round as well is noted in the ledger.

## 4. Measured

Census `analysis/m0146/m0146-0226/census/` (m26), against census4's PG arms:

- SF0.25 PLAN-PARITY 91→92 (Q4 MATCH). Eleven plans change text, all through rows rounded up (Q1, Q4, Q8, Q11, Q21,
  Q23, Q39, Q41, Q73, Q75, Q81); no other verdict moves.
- SF1 PLAN-PARITY 80→81 (Q64 MATCH; Q64 is M0146-0214's record).
- TPC-H unchanged at 22/22.
- Q4 on an SF0.25 clone with PG statistics now prints PG's tree, with the Nested Loop at PG's cost
  (59735.67..188166.22).
- The fire set ran 25 queries; all 25 results are byte-identical between baseline and candidate.

Pin: `TestFilterRowsRoundLikeClampRowEst` (two default equalities over 1194684 tuples give 30; one gives 5973),
mutation-checked by restoring the truncation.

## 5. Open

- The year_total HashAggregate still costs less than PG's (46822.64..55046.66 vs 53401.85..61625.87 over the same
  input). The gap moves only the initPlan cost charged at the root, not the plan choice.
- goopg's single-relation display path for a materialized CTE scan charges no qual cost per input tuple and charges
  cpu_tuple_cost per output row. The search's `costKeptCTEScanLeaf` is `cost_ctescan` exactly. A plain
  `WITH … AS MATERIALIZED … WHERE` EXPLAIN shows the difference.

Both are ledger rows.
