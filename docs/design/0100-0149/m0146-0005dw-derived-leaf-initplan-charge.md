# M0146-0005dw — a derived table is charged its own initPlans before the search

Status: done (2026-10-04, `3dc614ac7`). Parent: M0146-0005. Filed by slice
115.

## The filed premise, refuted

Slice 115 routed TPC-DS Q44 as "a merge join over WindowAgg outputs claims
the window column's order". Probing showed that goopg does price the sort a
merge join needs over a WindowAgg output: the outer merge's startup includes
both sorts. The Sort is absorbed into the merge plan (`absorbMergeSort`),
because goopg's merge executor sorts its own inputs, so it is never printed.
That absorption is deliberate and ledgered with M0146-0005bd.

## The real divergence

| | Merge Join on rnk |
|---|---|
| PG 18.3 | 80931.41..83170.75 |
| goopg before | 48000.02..50292.12 |
| goopg now | 81874.61..84166.71 |

Before, goopg's merge started below the two WindowAgg inputs' startups
(2 × 40101). Each ranked derived table's HAVING compares against an
uncorrelated InitPlan worth about 16k, and the search priced the derived
leaves without it.

PG plans a subquery in FROM as a query level of its own.
`SS_charge_for_initplans` (`./postgres/src/backend/optimizer/plan/subselect.c`)
adds the level's initPlans to every path of its final rel. So the parent's
SubqueryScan path, and every join above it, pays for them.

M0146-0005dr charges levels at `Plan()`'s tail, recognising them by their
Subquery Scan wrappers. goopg's plan keeps no Subquery Scan over a
derived-table leaf of the join search, so that tail walk filed the leaf's
initPlans under the statement's top. The search had also already priced the
leaf without them, which is the residual 0005dr ledgered.

## Change

- **`chargeDerivedLeafLevel`** is called from `costSubplanLeaf`, before the
  leaf's cost is read. It charges the leaf as its own level, using the same
  idempotent `initPlanChargeWalk`, and marks the leaf's top with
  `InitPlanCharge.levelTop`.
- **The tail walk** treats a marked node below a level's top as a level of
  its own. It charges the node there, not again at the statement's top.

Repro: two ranked derived tables, each filtered by
`y > (SELECT avg(y) FROM w)` (an InitPlan of 339.01), merge-joined on the
rank. The join's startup now carries both InitPlans: 3038.11 → 3716.14,
where PG's is 3516.50.

## Effect

- **Fire set:** flat at both scales. Q14, Q44 and Q58 fired with cost
  changes only.
- **Q44:** the Merge Join is now within 1.2% of PG's.
- **Q14 and Q58:** their top costs rose toward PG's.
- **Other gates:** ea-ratchet PASS, sweep 96/96, TPC-H arm 24/24, regress 14
  planner cases identical to HEAD.

Test: `TestDerivedLeafChargesItsInitPlans` fails before the change. It plans
the query with a 339.01 InitPlan and with a free one: the join's startup
used to differ by nothing, and now differs by both InitPlans.

## Not done (ledgered)

- **Absorbed sorts.** goopg's merge joins never print the Sort children PG
  shows; the executor sorts internally. 6 TPC-DS queries at SF0.25 and 5 at
  SF1 have a Sort under a PG merge join.
- **The WindowAgg line.** In the repro it still prints 1101.39..1234.71
  without its charge, though the node carries it; PG prints 1151.48. The
  display path that drops it was not found.
- **Placement.** The InitPlan is printed under the filtered scan; PG prints
  it at the level's top.
