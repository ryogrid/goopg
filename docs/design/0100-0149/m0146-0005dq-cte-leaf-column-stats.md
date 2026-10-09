# M0146-0005dq — a CTE or subquery leaf's column takes its base column's statistics

Status: done (2026-10-04, `ff218cc9c`; test `579b0197c`). Parent: M0146-0005.

## The divergence

TPC-DS Q95's outer `ws1.ws_order_number IN (SELECT ws_order_number FROM ws_wh)`:

| | top of plan | cost |
|---|---|---|
| PG 18.3 | Nested Loop Semi Join over CTE Scan on ws_wh, `Join Filter: (ws1.ws_order_number = ws_wh.ws_order_number)` | 85486 |
| goopg before | Hash Join over HashAggregate(CTE Scan ws_wh), the unique-ified inner | 88738 |

goopg did build the nested-loop semi path, but priced it at 106004.

## Cause

`final_cost_nestloop`'s semi arm (`./postgres/src/backend/optimizer/path/costsize.c`)
charges an outer row that is expected to match only
`inner_rows × 2/(match_count+1)` processed tuples. The expectation comes
from `outer_match_frac`, the `eqjoinsel_semi` selectivity, and
`rint(outer_rows × frac)` decides whether a one-row outer matches:

- PG reads `ws_wh.ws_order_number`'s statistics through the CTE.
  `examine_simple_variable`'s RTE_CTE arm (`./postgres/src/backend/utils/adt/selfuncs.c`)
  recurses into the CTE's planned body and finds `web_sales.ws_order_number`,
  so the selectivity is 1.0 and the row matches.
- goopg's join search resolved a derived leaf's column with no
  statistics. `get_variable_numdistinct` defaulted to 200, the semi
  selectivity punted to 0.5, and `rint(0.5) = 0` left the outer row
  unmatched. That charged the whole 1.75M-row CTE as processed tuples.

## Change

`derivedLeafColumnStats` (joinselectivity.go) is the recursion. For a
CTE or subquery leaf (leaf-local Filters stripped), it resolves the
column through `resolveBaseColumn` and returns the base column's
statistics. `examineJoinVar` applies it on both derived-leaf branches and
keeps the leaf's own `tuples`/`rows`, since PG's `vardata->rel` stays the
derived rel. A relative `stadistinct` therefore scales by the derived
rel's tuples, as upstream's does.

`resolveBaseColumn`'s arm list already encodes upstream's punts:

- no arm for set operations or DISTINCT;
- a GROUP BY Aggregate is refused (only a partial aggregate's group key
  passes);
- a Project is crossed only for a bare column, since an expression target
  is not a Var.

## Effect

- **Q95:** the top is PG's Nested Loop Semi Join with the Join Filter, cost
  85340.56 (PG 85486.28).
- **Fire set:** only Q95 changes, with no timeouts.
  - SF1: join-order 59→58, join-method 27→26, aggregation-strategy 23→22,
    and Q95 drops from 5 categories to 2 (scan-type, qual-placement).
  - SF0.25: aggregation-strategy 16→15, rendering 13→12, against
    parameterisation 28→29.
  - Matches are flat (38/29).
- **Values and regress:** sweep 96/96, TPC-H arm 24/24, ea-ratchet PASS.
  Regress with, subselect, union, select, aggregates and equivclass are
  identical to HEAD.
- **Runtime:** Q95 at SF0.25 goes 3s → 6s on PG's own plan. About 12 outer
  rows each rescan the 1.7M-row CTE to their first match, and goopg's CTE
  scan reads rows at about half PG's rate (PG runs the plan in 2.8s).
  Ledgered.
- **Test:** `TestCTELeafColumnTakesBaseStatistics` (PG: Hash Semi Join
  rows=150 over Seq Scan on cso rows=150).

## Not done (ledgered)

- PG's `security_barrier` stop (a view with that option keeps its
  statistics hidden). goopg's derived leaves carry no such flag.
- CTE-scan per-row speed on rescans.
