# M0146-0005dr — a query level's top node carries its initPlans' cost

Status: done (2026-10-03, `874fbf863`). Parent: M0146-0005 (banner item 3).

## The PG behaviour

Every query level keeps a list of initPlans (`root->init_plans`):

- the uncorrelated sublinks `build_subplan` turned into InitPlans;
- one entry per kept CTE (`SS_process_ctes`,
  `./postgres/src/backend/optimizer/plan/subselect.c:869`, appends the
  CTE's SubPlan at `:1040` and prices it with `cost_subplan`).

`SS_charge_for_initplans` (`subselect.c:2248`), called from
`grouping_planner` (`./postgres/src/backend/optimizer/plan/planner.c:1236`),
adds `SS_compute_initplan_cost` (`subselect.c:2312`) to the startup and
total cost of every path of the level's final rel. That sum is
`startup_cost + per_call_cost` over the initPlans.

`cost_subplan` (`./postgres/src/backend/optimizer/path/costsize.c`) makes
that term:

- the plan's total cost for a non-hashed, parameterless sublink that reads
  every row (EXPR, CTE);
- `startup + run_cost / rows` for an EXISTS, which fetches one row;
- plus the test expression's cost.

`SS_attach_initplans` then hangs the initPlans on the level's top plan
node, which is where EXPLAIN prints them and where the charge shows.

On an empty `t`, PG 18.3 prints the following for
`WITH v AS MATERIALIZED (… ORDER BY a) SELECT * FROM v x JOIN v y ON x.a = y.a`:

    Merge Join  (cost=54.04..65.54 ...)
      CTE v
        ->  Sort  (cost=53.54..54.04 ...)

That is the CTE body's 54.04 on both costs of an 11.50 run.

## What landed

`chargeInitPlans` (internal/optimizer/initplancharge.go) runs at
`PlanWithSettings`' tail. It runs before `stripTrivialSubqueryScans`,
because the Subquery Scan wrappers still mark the levels PG keeps, and it
is skipped for the EXPLAIN wrapper, whose inner statement is charged by
its own recursive call.

- **Levels.** The statement, every sublink plan, every Subquery Scan body
  and every CTE body. A level is charged after its sub-levels, so it reads
  their charged cost.
- **InitPlans.** Each level's are the sublinks `optimizer.SublinkIsInitPlan`
  accepts on its own nodes (`NodeSublinks`) — the same predicate EXPLAIN
  uses to print `InitPlan N`. `initPlanCost` follows `cost_subplan`:
  the total cost, or the first-row cost for an EXISTS.
- **Kept CTE bodies** (CTE scans that are not `Inlined()`) are charged
  once each to the statement's top. EXPLAIN already hoists every
  `CTE <name>` section there (explain_cte.go), because goopg's plan
  carries no declaring-level marker.
- **Where the charge lives.** On the level's top printed node:
  `chargeTarget` descends through `Project`, which EXPLAIN never prints
  and which in PG is the final path's target, not a node. It is stored as
  an embedded `InitPlanCharge` in about 30 query-node types. Embedding
  `PlanCost` instead would have made `stampPlanCost` start stamping
  WindowAgg / SetOp / Limit, which the cost code relies on not happening.
- **Display.** `legacyDisplayCostOf` and `explainCostFields` add the charge
  to both costs, so a derived ancestor above a charged top includes it,
  as PG's do. The charge is set, never added, so re-entrant `Plan()`
  calls (view bodies, EXPLAIN) leave identical numbers.

## Verification

- `TestExplainChargesInitPlansToTopNode`:
  - Limit = Sort + kept CTE body;
  - a WHERE InitPlan's total appears as the scan's startup.
- `TestExplainMergeJoinMaterializesCTEInner` (M0146-0005bd) asserted the
  uncharged total. It now checks PG's relation: total minus the CTE body
  is the 11.50 run cost.
- TPC-DS SF0.25 top nodes, before → after (PG):

  | query | before | after | PG |
  |---|---|---|---|
  | Q75 | 550 | 65220 | 64209 |
  | Q1 | 2494 | 6940 | 7284 |
  | Q30 | 477 | 3430 | 2777 |
  | Q59 | 8196 | 50459 | 62385 |

- Fire set: 23 queries per scale change cost text only. match 39/28 and
  categories are unchanged, with no timeouts.
- Gates: units, spotcheck, SF0.25 sweep 96/96, TPC-H arm, ea-ratchet
  10/10. Regress with, subselect, explain, aggregates, select and
  misc_functions are byte-identical.

## Residuals (ledgered)

- **Search time is unchanged.** The charge is applied after the search.
  PG adds it to final-rel paths before `set_cheapest`, which is the same
  increment for all of a level's paths, but a parent level's SubqueryScan
  path then prices a charged child. goopg's searched ancestors (stamped
  by path) do not include a nested level's charge.
- **Nested WITH.** A nested WITH's bodies charge the statement's top, not
  their declaring level.
- **Not added:**
  - the test-expression cost;
  - a top node type outside the embedded set receives no charge;
  - the min/max rewrite: goopg's no-index rewrite prices its own InitPlan
    nested inside a Result, and PG keeps a plain Aggregate there
    (M0146-0005du).
- **HashAggregate.** goopg's prices 0.50 above PG's on a 200-group empty
  input (46.40 vs 45.90).
- **Q64** stays far below PG (82 vs 13874). Its CTE bodies are priced low
  themselves, which is not this mechanism.
