# M0146-0005di: a sublink in a qual is charged its cost_subplan cost

Status: landed 2026-10-03 (banner item 3, M0146-0005 slice 116). The
InitPlan half is filed as M0146-0005dr.

## Defect

`qualEvalOps`, goopg's `cost_qual_eval`, charged every sublink one
cpu_operator_cost. PG adds the planned SubPlan's `cost_subplan` startup and
per-call costs (costsize.c, `cost_qual_eval_walker`'s SubPlan arm).

A correlated scalar SubPlan in a CTE scan's filter therefore cost almost
nothing to the search. On TPC-DS Q1, `ctr1`'s filter costs about 7.8 per
row in PG and 0.0025 in goopg. With it, PG's hash and nested-loop choices
for the store join tie, and PG elects the nested loop; goopg saw no tie.

## Change (internal/optimizer/qualevalcost.go)

`subPlanCostOps` follows `cost_subplan`, in cpu_operator_cost units so it
sums with the operator counts:

| sublink | charge |
|---|---|
| correlated EXPR / ARRAY | run cost + startup, per call |
| correlated EXISTS | run cost / rows + startup, per call |
| ANY / ALL | 0.5 × run cost + 0.5 × rows × cpu_operator_cost per call; startup per call when correlated, once when not (goopg caches the output: ExecMaterializesOutput) |
| uncorrelated EXPR / ARRAY / EXISTS | nothing: upstream it is an InitPlan and the qual reads its Param |
| not yet planned | the old one-operator charge |

A hashable uncorrelated IN is priced as the plain alternative, not as the
hashed SubPlan goopg executes. `make_subplan` wraps the two forms in an
AlternativeSubPlan with the plain one first, and `cost_qual_eval_walker`
"arbitrarily use[s] the first alternative plan for costing". A first draft
charged the hashed form and was corrected before landing.

The plan's costs are its stamped path costs, or the legacy display estimate
where the search did not produce it.

## Verification

`TestQualEvalOpsChargesSubPlanCost` pins each arm against hand-computed
`cost_subplan` arithmetic.

Fire set, both scales: Q1, Q6, Q14, Q30, Q54, Q58 and Q81 change, all
values PASS.

- Q1 drops join-method / scan-type / parameterisation (SF0.25) and
  join-method / scan-type (SF1) from its divergence.
- CATEGORIES-EXCL-MATCH: join-method 29 → 28 and parameterisation 29 → 28
  (SF0.25); join-method 28 → 27 and scan-type 35 → 34 (SF1).
- PLAN-PARITY match is unchanged (39 / 28).

Regress A/B: `subselect`, `with`, `aggregates` and `select_parallel` are
unchanged; `join` shows only its known row flap.

Gates pass: units, spotcheck, sweep 96/96, arm (values-only under the
nightly batch, FORCE=1), fire set, ea-ratchet.

## Left open

- **InitPlan cost charged to the plan** (`SS_charge_for_initplans` /
  `SS_attach_initplans`). goopg's top nodes leave CTE and InitPlan costs
  out: Q30, Q57, Q58, Q64, Q75. Filed as M0146-0005dr.
- **Display cost of a Filter or scan the search did not produce.** In Q41
  the item scans print 180 (CPU only, no pages or quals), and the outer
  Filter's 18,000 SubPlan calls are not in its displayed cost. Ledgered.
- **Placement of the OR of hashed SubPlans in Q10 / Q35.** PG evaluates it
  at the customer scan. Its cost now follows PG, but the qual is still held
  above the search, so the plans did not move. Routed with M0146-0005dr's
  family.
- **Unit conversion.** Converting to operator units uses
  cpu_operator_cost's boot value; a session that changes the GUC prices
  the SubPlan part slightly off.
