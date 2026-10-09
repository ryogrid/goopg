# M0146-0130 — a correlated scalar sublink filters its own leaf, and its SubPlan is parallel-restricted

Status: done 2026-10-09 (a6905e365). Parent: M0146-0014a.

## Problem

TPC-DS Q6 has the conjunct

    i.i_current_price > 1.2 * (SELECT avg(j.i_current_price) FROM item j
                               WHERE j.i_category = i.i_category)

PG filters it on `item i`: the outer Nested Loop probes `item_pkey` per
outer row, and the probe holds the SubPlan as its Filter. The parallel
part (store_sales, date_dim, customer, customer_address) runs below a
Gather Merge sorted on `ca_state`, and the probe runs above it.

goopg kept the conjunct above the whole join. The SubPlan ran inside the
workers, uncosted, on a Gather Filter, and the `item_pkey` probe was never
built. The parity diff showed `[join-order, parameterisation,
sort-strategy, parallelism]` at both scales. Q92 at SF0.25 diverged the
same way.

## PG behaviour

- **Qual placement.** `distribute_qual_to_rels` (initsplan.c) places a
  qual at the lowest level whose relids cover it. A SubPlan's outer
  references (its `args`) count toward the qual's relids, so the Q6
  conjunct belongs to `{i}` and becomes a restriction of that base rel.
- **Parallel safety.** `max_parallel_hazard_walker` (clauses.c) treats a
  SubPlan whose `args` carry PARAM_EXEC values as parallel-restricted
  (`subplan->parallel_safe` is false). `set_rel_consider_parallel` then
  makes the rel non-parallel, so no partial path reads it. A Gather placed
  below the rel is still legal.

## Change

### Localizing at a non-zero binding

`correlatedScalarSublinkLeaf` (local_filters.go) localized a correlated
sublink conjunct only at binding 0, the scope's first leaf. Q6's `item i`
is not first in its FROM list.

- **Non-zero bindings.** The function now also accepts the leaf `b` that
  the conjunct's own same-scope columns name (`tableForCol(c, spans)`),
  provided the sublink's correlation resolves inside that leaf. The check
  lives in `correlatedScalarSublinkBinding(c, spans, b)`, which is the old
  body parameterized on `b`.
- **Own column required.** A non-zero binding needs a column of its own.
  A sublink-only conjunct carries no column of its own, so the seam's
  outer-join guard could not see that its correlation reaches a LEFT
  JOIN's nullable side through a PlaceHolderVar. Such a conjunct stays
  above the join (`TestCorrelatedSublinkOverFlattenedSubqueryKeepsItsArgs`).
- **EXISTS.** `ExistsExpr` stays on binding 0 (see Not covered).

### The outer row seen by the sublink

A leaf at binding `b` sees its own row, which starts at column 0 of the
leaf. The sublink's correlated `ColumnRef`s, however, index the scope row,
where the leaf starts at `spans[b].lo`. The translation needs three
pieces:

- **Planner.** `SubqueryExpr.OuterRowPad` and `ExistsExpr.OuterRowPad`
  record the offset. `localizeExprToLeaf` sets them when it rewrites the
  conjunct by `binding.offset`.
- **Executor.** `padOuterRow` (expr.go) prepends that many NULLs to the
  row pushed on `ctx.OuterRows`. This happens in `subqueryWithScope`,
  `existsWithScope` and the row-constructor comparison, so the sublink's
  indexes stay valid unchanged.
- **PARAM_EXEC lowering.** `sublinkHandle.pad` is subtracted when the
  argument `ColumnRef` is built for the Param slot (subplan_lower.go
  `slotFor`), so the SubPlan's argument reads the leaf column.

### Parallel restriction

`subtreeHasParallelRestrictedQual` (parallel.go) reports a Filter, Join or
NestedLoopIndexJoin qual that `isParallelSafeExpr` rejects, which covers a
correlated SubPlan. It applies only at the producers that run the whole
child subtree inside workers:

- the partial aggregate splits in partialaggupper.go;
- the partial distinct splits in distinctpaths.go;
- `findPartialSubtree`'s aggregate split.

The node-kind gate `subtreeHasUnsafeNode` is unchanged. A first attempt
added the qual check to that gate. It also stopped a Gather from going
BELOW a correlated Join Filter, which PG allows, and Q32/Q92 at SF1 lost
their Gather.

## Verification

- **Tests.**
  - `TestCorrelatedSublinkRestrictsOffsetBinding` (executor) checks that
    `FROM ca, cb` and `FROM cb, ca` both filter on the `ca` scan and
    return equal non-zero counts. It also checks that the conjunct on the
    nullable side of a LEFT JOIN keeps the inner-join count.
  - `TestCorrelatedSubPlanQualIsParallelRestricted` checks the correlated
    and uncorrelated (InitPlan) cases. It also pins that
    `subtreeHasUnsafeNode` stays node-kind only.
- **TPC-DS fire set.**
  - Q6 goes from 4 categories to `[parameterisation]` at both scales, and
    the plan is PG's node for node.
  - Q92 at SF0.25 goes from 5 categories to `[join-order, scan-type]`.
  - No other query fires.
- **TPC-H.** Plans are byte-identical (analysis/leftdeep-joins/m130-new).
- **Regress A/B** (32 cases): only `join` changes. One unordered query
  returns the same 5 rows in a different order.
- **Gates.** units, TPC-H spotcheck, acceptance arm (values), fire set,
  SF0.25 sweep (96/96) and ea-ratchet (1, unchanged) all PASS.

## Not covered (ledgered)

- **SubPlan argument rendering.** Q6's remaining `[parameterisation]` is
  EXPLAIN text. goopg prints the SubPlan's correlated argument as
  `(i_category = $0)`, and PG prints `(i_category = i.i_category)`.
  Binding-0 sublinks render the same way, so the gap is in the EXPLAIN
  exec-param owner resolution, not in placement.
- **EXISTS at a non-zero binding.** A correlated `EXISTS` inside a
  conjunct is still localized only at binding 0. The EXISTS→ANY pass
  rewrites it into an `InExpr`, which carries no `OuterRowPad`. PG would
  place such a conjunct on its own rel.
- **Escaping references.** A sublink that reaches past its scope (a
  grandparent reference) escapes at every binding, as before.
- **SubPlan cost at SF1.** The probe's SubPlan costs 965.78 against PG's
  1513.81. That is item's relpages drift, tracked by the owner SF1 reload
  (RELPAGES route).
