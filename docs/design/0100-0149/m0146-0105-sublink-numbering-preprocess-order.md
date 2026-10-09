# M0146-0105 — SubPlan/InitPlan numbers follow PG's preprocess order

Status: done 2026-10-08 (29526f12d). Parent: M0146-0042.

## Problem

TPC-DS Q6 filters on `d.d_month_seq = (select distinct d_month_seq …)` and
then `i.i_current_price > 1.2 * (select avg(…) where j.i_category =
i.i_category)`. PG prints `InitPlan 1` and `SubPlan 2`; goopg printed
`InitPlan 2` and `SubPlan 1`.

goopg reserved the numbers in plan pre-order (M0146-0005bv,
`reservePGPlanIDs`). Its plan evaluates the correlated SubPlan on a Gather
above the date\_dim scan that reads the InitPlan, so the pre-order reached
the SubPlan first.

## PG behaviour

`build_subplan` (subselect.c) gives a sublink the next plan_id when the
sublink is planned, after the sublink's own body. Within a query level,
`subquery_planner` (planner.c) plans sublinks as `preprocess_expression`
reaches them:

1. the targetlist, which also holds the ORDER BY and GROUP BY items as
   resjunk entries;
2. the jointree's quals (`preprocess_qual_conditions`: JOIN/ON quals,
   then WHERE);
3. HAVING;
4. window clauses, then LIMIT / OFFSET.

Each part goes in source order. FROM subqueries that remain query levels
are planned afterwards, by `set_base_rel_sizes`, so their sublinks number
after every sublink of the enclosing level.

Each arm of a set operation is a level of its own. `recurse_set_operations`
plans it as a subquery, and an appendrel member keeps its own
`init_plans`. So PG prints an arm's InitPlan on the arm's top node, under
the Append.

## Change

- **Per-level numbering.** `reservePGPlanIDs` (explain\_plan\_ids.go)
  numbers one query level at a time, using the level-top marks M0146-0104
  added (`optimizer.IsQueryLevelTop`).
  - It collects the level's own sublinks, stopping at nested level tops.
  - It orders them by `preprocessRank`:
    - 0: targetlist-carrying nodes (Project, Result, Sort, Incremental
      Sort, Aggregate, WindowAgg);
    - 1: quals;
    - 2: a Filter over an Aggregate, i.e. HAVING;
    - 3: Limit.
  - Within a rank it orders by source position, and keeps plan order when
    any sublink lacks a position.
  - Each sublink's body is numbered before the sublink itself; the nested
    levels follow in plan order.
- **Set-operation arms.** The charge walk (`chargeInitPlans`) treats each
  arm of a `SetOp` chain as a query level. M0146-0104 had hoisted a UNION
  ALL's arm InitPlans to the Append. They now print on each arm, as in
  PG, and each arm carries its own initPlan charge.

## Verification

- **Probe** (20 statements on scratch tables). Plan lines differing from
  PG 18.3 fell from 80 at HEAD to 22. The fixed cases:
  - two quals on joined relations;
  - an ORDER BY sublink;
  - a targetlist, WHERE and HAVING sublink in one statement;
  - UNION ALL, UNION, and UNION ALL inside FROM.

  What remains was already different at HEAD, or is ledgered: qual order,
  Group Key qualification, a flattened subquery's InitPlan level, and an
  EXISTS pulled up into a semi join.
- **Test.** `TestExplainSublinkNumberingFollowsQueryOrder`. Disabling the
  source-order sort, the preprocess rank, or the set-operation arm levels
  each fails it.
- **TPC-DS fire set** (results identical). Q6 and Q14 change.
  - SubPlan/InitPlan label sets that differ from PG: 1 → 0 at both
    scales (Q6).
  - Q14's HAVING InitPlan is now 4, after the WHERE clause's 3, as in PG.
  - InitPlan placements that differ from PG: SF0.25 1 → 0; SF1 2 → 1
    (Q23's Partial/Finalize parent label).
  - The classifier does not score numbering, so its categories are flat.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (14 files). Identical, except rowsecurity's
  pointer-valued text.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25
  sweep (96 PASS; only Q6 and Q14 change shape) and ea-ratchet all PASS.

## Deferred (ledgered)

- **EXISTS pulled up into a semi join.**
  `… where exists (select … where ip2.w = (select 1)) and m = (select 2)`
  is numbered `m` 1, `w` 2 by PG. goopg numbers the EXISTS body's
  sublink first, as HEAD did.
- **A sublink in LIMIT / OFFSET.** goopg rejects `LIMIT (select 5)` with
  "subqueries are not supported in this context"; PG plans it as an
  InitPlan.
