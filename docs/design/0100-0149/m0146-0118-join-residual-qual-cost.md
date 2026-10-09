# M0146-0118 — a join's residual quals are priced by cost_qual_eval

Status: done 2026-10-09 (4451864d2). Parent: M0146-0116 (root M0146-0007).

## Problem

M0146-0116 ledgered part (a): `joinQualPerTuple` charged one
`cpu_operator_cost` per residual join conjunct, whatever the conjunct
evaluates. PG's `final_cost_nestloop`, `final_cost_mergejoin` and
`final_cost_hashjoin` charge `qp_qual_cost`, which is `cost_qual_eval` over
the join's non-key restrict clauses. `cost_qual_eval_walker` charges every
operator and function call at its procost.

TPC-DS Q4 and Q11 compare year-over-year ratios in a Join Filter, `CASE WHEN
… > 0 THEN … / … ELSE NULL END > CASE …`, which is several operators per
conjunct. goopg priced those joins well below PG.

## Change

- **`qualEvalOpsPriced(e, priceSublinks)`** (qualevalcost.go) is the existing
  `qualEvalOps` walk with a switch. With `priceSublinks` false it still
  counts an IN sublink's comparison, but leaves every SubPlan's own cost out.
  `qualEvalOps` is the `true` form.
- **`joinQualPerTuple`** (subplan\_cost.go) sums `qualEvalOpsPriced(clause,
  false)` plus `subPlanJoinQualOps(clause)` per conjunct. Sublink pricing
  keeps its correlated-only rule (M0146-0019a / Q45), so it is unchanged. A
  plain `a < b` costs exactly what it did.
- **Test housekeeping.**
  - The switch inventory key moves to `qualEvalOpsPriced`.
  - Two join-path fixtures that used clause-less restrictInfos get a
    one-operator clause, which keeps their charge at one operator.

## Verification

- **Test.** `TestJoinQualPerTupleCountsOperators` covers `a < b` (1),
  `(a + b) > c` (2), a CASE ratio (3) and two conjuncts (2).
- **TPC-DS fire set** (results identical). Q11 is a full MATCH at SF0.25:

  | | before | after |
  |---|---|---|
  | match | 46 | 47 |
  | join-order | 44 | 43 |
  | join-method | 21 | 20 |
  | sort-strategy | 23 | 22 |
  | qual-placement | 10 | 9 |

  The other 16 fired queries change cost only; their node lines are
  identical at both scales. SF1's categories are unchanged.
- **Estimates and TPC-H.** TPC-H plans are byte-identical; ea-ratchet 9/9.
- **Regress A/B** (19 files). Neutral; the only change is the known
  row-order flip in one unordered join result.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  and ea-ratchet all PASS.

## Not covered (ledgered)

- **Startup part.** `qp_qual_cost.startup` is not charged at join startup:
  a hashed IN list builds its table once. `joinQualPerTuple` returns only
  the per-tuple part.
- **Casts.** Casts still count 0 in `qualEvalOps`, its existing caveat. PG
  charges a cast function 1 and a CoerceViaIO 2.
