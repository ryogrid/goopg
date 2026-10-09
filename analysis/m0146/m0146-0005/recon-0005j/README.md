# M0146-0005j recon: the all-default max(l,r) join-size cap

`calcJoinrelSize` (internal/optimizer/joinrelsize.go) clips an inner
join's row estimate to max(outer rows, inner rows) when no key was proven
and every clause selectivity was a default guess. The cap came from
M0126-0010. PG's `calc_joinrel_size_estimate` has no counterpart. The
witness is TPC-DS Q44: after slice 9 its `rnk = rnk` merge join still read
5495 rows where PG reads 5424² / 200 = 147099.

## Precondition check

The ledger row (M0127-P5.6-c) conditions deletion on "the MCV arm
(P5.6-a) + an audit". The search's `eqJoinSelectivity` is still the
no-MCV branch; only the plan-node estimator has `eqjoinselInnerMCV`. But
the cap fires only when every clause's ndistinct is a default guess, i.e.
no statistics on either side, which is exactly when there are no MCVs. The
MCV arm therefore cannot change a case the cap governs. The precondition
does not bind here.

## A/B with the branch disabled (worktree only, not committed)

- `tpcds-fireset-cap-off.txt`: fires Q2, Q8, Q44, Q64 at both scales. No
  timeouts introduced.
- `census-diff-sf1.txt`: Q44's depth-1 record changes category from
  sort-strategy to parameterisation. SF0.25: no census change. No query
  got shallower.
- `q44-top-joins.txt`: Q44's merge join now estimates 150975 rows (PG
  147099). The remaining divergence is join order: goopg joins `item`
  below the rnk merge join, while PG joins the two ranked subqueries first
  and then nested-loops into `item_pkey`.
- `tpch-arm-cap-off.txt`: 24 MATCH on values. `tpch-plan-parity-cap-off.txt`:
  5/22, census identical. The FK-PK chain compounding the ledger feared
  does not appear, because TPC-H's joins are key-proven (`est.fired`).

## Recommendation

Retire the cap (filed as M0146-0005k). Its gates are the fire set above
plus the usual set.
