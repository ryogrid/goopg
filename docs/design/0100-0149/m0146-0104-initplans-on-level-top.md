# M0146-0104 — a query level's InitPlans print on its top node

Status: done 2026-10-08 (b348cc254). Parent: M0146-0042.

## Problem

goopg printed each `InitPlan N` section under the node whose expression
reads the InitPlan. In TPC-DS Q58 each derived table filters `date_dim`
by `d_week_seq = (select d_week_seq … )`, and goopg printed the InitPlan
under that scan, below the Gather:

```
goopg:  GroupAggregate -> Gather Merge -> … -> Parallel Seq Scan on date_dim date_dim_1
                                                     Filter: (d_week_seq = (InitPlan 1).col1)
                                                     InitPlan 1 …
PG:     GroupAggregate
          Group Key: item.i_item_id
          InitPlan 1 …
          ->  Gather Merge -> … -> Parallel Seq Scan on date_dim date_dim_1
```

The same happened in Q54 (a Hash Join instead of the GroupAggregate), Q6
(a Parallel Seq Scan instead of the Limit) and Q14 (Nested Loops). The
0005dw ledger row recorded the gap: "the InitPlan prints under the
filtered scan, PG at the level's top (SS_attach_initplans)".

## PG behaviour

- `build_subplan` (subselect.c) adds every uncorrelated sublink it turns
  into an InitPlan to its query level's `root->init_plans`.
- `create_plan` ends with `SS_attach_initplans(root, plan)`, which hangs
  that whole list on the level's top plan node.
- `ExplainNode` prints a node's `initPlan` list after its own detail
  lines and before its children, in list order, i.e. plan_id order.
- A subquery kept as its own level carries its initPlans on its own top
  plan, which is where Q58 prints them. When setrefs removes a trivial
  Subquery Scan above it, `clean_up_removed_plan_level` (setrefs.c) moves
  any initPlans of the removed scan onto that child, ahead of the child's
  own.

## Change

- **Marking level tops.** `chargeInitPlans` (M0146-0005dr) already walks
  every query level and finds its top node (`chargeTarget`, below any
  Project) to charge the initPlans' cost there. The levels are the
  statement, sublink plans, Subquery Scan and CTE bodies, and derived
  leaves the search priced as levels. The walk now also marks that node
  (`InitPlanCharge.queryLevelTop`).
- **Collecting a level's InitPlans.** `optimizer.LevelInitPlansOf(n)`
  collects a marked top's InitPlans from the plan as it stands. The walk
  stops at another level's top and at a Subquery Scan or CTE scan body,
  and never enters a sublink's plan. They are read at EXPLAIN time rather
  than recorded during the charge walk, because `stripSublinkBodies` later
  replaces sublink expressions and their bodies. An early snapshot made
  TPC-DS Q23 print a stale copy as an extra `InitPlan 4`.
- **EXPLAIN.** Both walkers (plain and ANALYZE) call
  `claimLevelInitPlans` when they render a node. It checks the node and
  any Filter wrapper collapsed into it, and queues the level's InitPlans
  after the node's detail lines and CTE sections, sorted by number. The
  reading node's later reference reuses the assigned number and queues
  nothing.
- **One section per plan.** `subPlanReg.initPlanQueued` queues each
  InitPlan once per plan, however many expression copies reach it. Without
  it, a HAVING qual rendered from another copy of the sublink printed the
  section twice.

## Verification

- **Probe** (PG 18.3 and goopg, scratch tables). InitPlans are placed as
  in PG for:
  - a join whose scan filter reads an InitPlan;
  - Limit over Sort;
  - a grouped derived table;
  - HAVING;
  - an InitPlan inside a correlated SubPlan's body.

  The remaining text differences (Values vs Result, qual order, one
  numbering swap) are the same at HEAD.
- **Test.** `TestExplainInitPlanOnLevelTop`. Dropping the claim calls
  fails the placement checks; dropping the per-plan dedupe fails the
  HAVING check.
- **TPC-DS fire set** (results identical). Q6, Q14, Q54 and Q58 change.
  - InitPlan placements that differ from PG (a probe comparing each
    `InitPlan N`'s parent node): SF0.25 4 → 0, SF1 5 → 0.
  - What remains: Q6 numbers its InitPlan 2 where PG has 1, and Q23's
    parent differs only in its Partial/Finalize label.
  - The classifier does not score InitPlan placement, so its categories
    are unchanged.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (14 files):
  - aggregates' diff fell 413 → 405 lines and join's 15003 → 14997, with
    the InitPlans now on the top node as expected;
  - partition_prune moves `InitPlan 2` to the top Sort;
  - rowsecurity and window differ only in pointer text and a block move;
  - the rest are identical.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS; only Q6, Q14, Q54 and Q58 change shape) and ea-ratchet all PASS.

## Deferred (ledgered)

- **Flattened bodies.** A sublink inside a subquery that PG flattens into
  its parent is an InitPlan of the parent level, and PG prints it on the
  parent's top. This covers a simple derived table (`pull_up_subqueries`)
  and a single-reference CTE inlined by `inline_cte` and then pulled up.
  goopg still treats such a body as its own level: the charge walk's
  CTE-scan arm, and the derived-table wrapper present at charge time. So
  it prints the InitPlan on the scan, as HEAD did.
