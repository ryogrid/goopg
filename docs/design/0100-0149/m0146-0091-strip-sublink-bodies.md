# M0146-0091 — strip trivial Subquery Scans inside sublink and InitPlan bodies

Status: done 2026-10-07 (b1c7d4881). Parent: M0146-0066 (class A of the
Subquery Scan recon). Evidence: `analysis/m0146/m0146-0091/`.

## Problem

`stripTrivialSubqueryScans` (M0146-0005w) is goopg's version of setrefs.c's
`trivial_subqueryscan`. It ran once, at `Plan()`'s tail, over the
statement tree, following Node children only (`planChildNodes`).

A sublink or InitPlan body hangs off an expression, so the pass never saw
it, and a trivial wrapper inside a body survived. The witness is TPC-DS
Q23's `Subquery Scan on __sq_1a7` under the InitPlan's Aggregate. PG 18.3
renders the HashAggregate directly there; probe case 1 in
`analysis/m0146/m0146-0066/probe-window-sublink.sql` shows the same shape.

## PG behaviour

- `set_plan_references` processes every subplan in `glob->subplans`, so
  `trivial_subqueryscan` / `clean_up_removed_plan_level` apply inside a
  sublink body exactly as they do in the main plan.
- Each subplan entered `create_plan` with `CP_EXACT_TLIST`.

## Change

- **`stripSublinkBodies`** (`subqueryscan_strip.go`) visits every plan node
  of the finished tree.
  - For each expression a node holds, it finds the sublinks
    (`exprChildSlots`).
  - It runs `stripTrivialSubqueryScans` over each body as a region of its
    own. A region root starts in the EXACT regime, which is the
    CP_EXACT_TLIST entry above.
  - It recurses into the stripped body, so nested sublinks are covered.
- **Writing back.** The result goes back through the sublink's plan slot.
  - The statement-level rebuild (`mapPlanChildren`) shallow-copies nodes but
    shares their expressions. A write to a sublink's `Plan` therefore
    reaches the finished tree.
  - A body held by two sublinks is stripped once (memoised by body root).
- **Shared enumeration.** `NodeSublinks`' per-node expression list is
  factored out as `nodeOwnExprs`, so the pass and EXPLAIN's sublink
  enumeration walk the same expression roots.

## Verification

- Probe case 1 now renders PG's plan exactly.
- Further shapes against PG 18.3 (`probe-sublink-bodies.sql`):
  - **Lose the wrapper, as in PG:** a correlated body, and a body whose
    leaf carries a group filter.
  - **Keep it, as PG does:** a Limit/Sort body that reads one of the
    leaf's two columns. This is a subset tlist in the pathtarget regime.
  - The other differences in that probe predate this change, as the HEAD
    build's output shows: Sort key rendering, a GroupAggregate election,
    and `ARRAY()` rendered as Values/SubPlan.
  - Query results are unchanged.
- `TestSubqueryScanStripsInsideSublinkBodies` covers the uncorrelated,
  correlated and kept cases. With the new call disabled, both strip cases
  fail.
- TPC-DS: Q23 is the only plan change in the SF1 fire set and the SF0.25
  sweep. Its Subquery Scan count went from 2 to 0, which is PG's count, and
  the executed results are identical. The categories are unchanged
  because Q23 still diverges on other categories.
- Regress A/B: subselect and with are identical. join differs only in the
  known row-order flap of an unordered result.

## Not covered

- Classes B–D of the M0146-0066 recon: window tlists (M0146-0092),
  join-member appendrel wrappers (M0146-0093), and upstream shape
  differences.
- CTE bodies were already visited through `CTEScan.Child` by the
  statement-level walk. This change does not alter them.
