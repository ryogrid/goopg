# M0146-0005 slice 3: diagnosis of TPC-H Q17's join method

State at HEAD `28a4a715a`: TPC-H PLAN-PARITY match 5/22 (`tpch-parity-head.txt`).
First-divergence census: `census-tpch-head.txt`. Q17's first divergence is
at depth 1, `join-method`: PG plans a Hash Join, goopg a Nested Loop
(`q17-pg.plan.txt`, `q17-goopg.plan.txt`).

PG side measurements use the private instrumented PG 18.3
(`tmp/pg18-optdebug`, port 5560, database `tpch` holding `part` and
`lineitem` copied read-only from the TPC-H reference, reproducing the
reference plan). Two temporary traces, gated on `debug_plan_candidates`, are
in `pg-instrumented-trace.patch`.

## Measured (`pg-forced-join-costs.txt`, `pg-hjcost-trace.txt`)

| join filter | forced hash join | forced nested loop |
|---|---|---|
| none | 212338 | 31877 |
| `l_quantity < 5` | 212887 | 31835 |
| `l_quantity < (SubPlan)` | 213576 (+1238) | 778121 (+746k) |

- **Nested loop:** PG charges the correlated SubPlan's per-call cost
  (`cost_subplan`: EXPR_SUBLINK, correlated, so run + startup = 123.76) on
  every row the inner index scan filters. The inner goes from 122.23 to
  3835.09, which is 30 × 123.76.
- **Hash join:** the same filter is charged on only `hashjointuples` = 10
  tuples, because `part` is inner-unique. `final_cost_hashjoin` uses
  `outer_matched_rows = rint(outer_rows × semifactors.outer_match_frac)`.
  The SubPlan charge is therefore 10 × 123.76 = 1238.

## Why PG's outer_match_frac is so small

`compute_semi_anti_join_factors` asks `clauselist_selectivity` for JOIN_SEMI
but passes the join's own `sjinfo`, whose `jointype` is JOIN_INNER, and
`eqjoinsel` switches on `sjinfo->jointype`. `jselec` is therefore the plain
inner-join selectivity of the whole restrict list: 1/200000 × 1/3.

- `outer_match_frac` = joinrel.rows / (outer.rows × inner.rows).
- `match_count` = nselec × inner.rows / jselec, which here is inner.rows.

## goopg's two gaps

1. **No SubPlan cost in qual evaluation.** `qualEvalCost`
   (`internal/optimizer/cost_funcs.go`) charges a flat `cpu_operator_cost`
   per conjunct at every join-qual site, so the nested loop's 5,940 SubPlan
   evaluations look free and it wins at 31572.
2. **Wrong inner-unique match fraction.** `hashJoinFinalCostInputFor`
   (`hashjoin_innerunique.go`) sets
   `outerMatchFrac = joinrel.Rows / outer.Rows`, missing the `/ inner.Rows`,
   and `hashJoinCost` hard-codes `match_count = 1`. The hash join's
   join-filter charge is also not taken on `outer_matched`.
