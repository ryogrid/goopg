# M0146-0107 — kept CTE and Subquery Scans name their columns in keys

Status: done 2026-10-08 (2d685acc0). Parent: M0146-0042.

## Problem

`get_variable` (ruleutils.c) names a column of a kept CTE scan, or of a
Subquery Scan that setrefs keeps, with that scan's own `set_rtable_names`
label. goopg printed several such keys bare:

| query | goopg | PG 18.3 |
|---|---|---|
| Q14 | `Index Cond: (ss_item_sk = ss_item_sk)`, `Group Key: ss_item_sk` | `cross_items_1.ss_item_sk` |
| Q23 | `Hash Cond: (c_customer_sk = …)` | `best_ss_customer.c_customer_sk` |
| Q95 | `Join Filter: (ws1.ws_order_number = ws_order_number)` | `ws_wh.ws_order_number` |
| Q44 | `Index Cond: (i_item_sk = item_sk)` | `v11.item_sk` |

Q14's line is a self-comparison as printed.

Three of the four come from an IN sublink over a kept CTE that became a
semi join. The inner side's columns carry the sublink level's binding
ids, so the name lookups found nothing to qualify them with.

## Change

All changes are in `internal/executor`.

- **Kept CTE scan is a boundary.** In the CTEScan arm of
  `resolveKeySourceAt`, a non-inlined CTE scan ends the key chase. Its
  column renders with the scan's label (`disambiguatedName`, else alias
  or name) through `boundaryKeyName`, as a kept Subquery Scan does
  already. Inlined CTE scans still chase into their body.
- **Project over a kept scan.** The Project arm of `resolveKeySourceAt`
  accepts a boundary hit at the position the target names, even though
  its relation id differs: the kept scan numbers its columns at its own
  query level. This is Q44's Sort, then narrowing Project, then
  `Subquery Scan on v11`.
- **Semi and anti join keys.** `joinResidualColumn` handles semi and anti
  joins, including the right variants. Such a join publishes one side,
  but its keys and residual index both inputs, so the first step maps the
  position onto the input that holds it. Before, the output-row arm
  (`concatJoinSide`) declined at the join itself.
- **The unique-ify.** `resolvedColumn` walks through a `DistinctOn`, the
  semi join's unique-ified inner, because it republishes its input
  position for position.
- **Probe keys.** `formatIndexCondKey` sends a plain-ColumnRef probe key
  (a bitmap probe's) that prints bare through `nestLoopParamThroughOuter`
  (M0146-0106). With `useLabel`, that function first answers from the
  outer plan's relation labels. `qualifyForeignColumns` does the same for
  the Recheck Cond's copy of the key.

## Verification

- **Probe** (PG 18.3 vs goopg; IN over a materialized CTE). Each shape is
  identical to PG:
  - Hash Semi Join with `(m107s.k = c.k)`;
  - the unique-ified inner Hash Join;
  - a nested loop with `Group Key: c.k` and `Index Cond: (k = c.k)`;
  - a bitmap probe with `Recheck Cond: (k = c.k)`.
- **Test.** `TestExplainSemiJoinOverKeptCTEKeys`. Disabling any one of
  these fails it: the CTE boundary, the semi-join mapping, the DistinctOn
  arm, the ColumnRef probe path, or the Recheck path.
- **Q44's arm has no fixture.** No fixture reproduces Q44's Sort, Project
  and kept Subquery Scan under a parameterised loop, because without a
  merge Sort both engines strip the Subquery Scan. Its witness is the
  fire set.
- **Redundant piece removed.** A positional Group Key fallback in the
  DistinctOn arm turned out redundant once the CTE boundary landed, and
  was removed.
- **TPC-DS fire set** (results identical). Q14, Q23, Q44, Q49, Q67 and
  Q95 change.
  - Every new text line appears in PG's plan, e.g. Q49's `web.return_rank`
    and Q67's `dw1.i_category`.
  - Cond/Filter/Key lines that differ from PG only in qualifiers: SF0.25
    50 → 30, SF1 40 → 24.
  - The classifier's categories are unchanged.
- **TPC-H.** Plans are byte-identical.
- **Regress A/B** (16 files).
  - with's `(a.unique1 = x.unique1)` and join's `(j1.id = j3.id)` now
    match PG's expected lines.
  - select\_distinct's Sort Key is qualified, `s.y, s.x`, against PG's
    `s.x, s.y`; the key order follows goopg's plan.
- **Gates.** units, TPC-H spotcheck, acceptance arm, fire set, SF0.25 sweep
  (96 PASS) and ea-ratchet all PASS.

## Remaining qualifier differences (not this change)

The 30 / 24 remaining lines are mostly:

- `_N` suffixes that follow PG's join order or numbering (`date_dim_2`
  vs `date_dim`);
- Q4/Q11's join-order-dependent Join Filters;
- Q64's `cs_ui.cs_item_sk`, where PG deparses through the aggregate
  subquery to `catalog_sales.cs_item_sk`.
