# M0148\-0001i1 — views plan as subquery RTEs of the statement

**Parent:** M0148\-0001 · **Status:** landed 2026\-11\-04; closed (residue in
M0148\-0001i2)

## Divergence

`planScanRangeVar` planned a view's stored SELECT as a separate statement
(`PlanWithSettings`) and wrapped it in a renaming Project. aggregates.sql
`explain (costs off) select * from v_pagg_test order by y` therefore showed
three differences from PG:

| | PG | goopg (before) |
|---|---|---|
| top of plan | `GroupAggregate` (already ordered by y) | a redundant `Sort  Sort Key: y` above it |
| grouping column | `Group Key: pagg_test.y` | `Group Key: y` (the view body's own one\-entry range table) |
| the cast sort key | `(((unnest(…)))::integer)` under a `Result` | `t` with no `Result` |

The third row is a separate SRF\-targetlist issue; it is residue.

## PostgreSQL

- **Expansion.** The rewriter replaces a view reference with a subquery RTE
  holding the view's query (`fireRIRrules` / `ApplyRetrieveRule`). Its alias
  is the reference's refname, and its column aliases are the view's column
  names.
- **Planning.** The planner treats that RTE like any FROM subquery:
  - pulled up when simple (`pull_up_subqueries`);
  - otherwise planned in its own subroot, with its ordering exposed to the
    outer query (`convert_subquery_pathkeys`), so the ORDER BY needs no
    Sort;
  - its relations take their EXPLAIN names from the one flattened range
    table (`set_plan_references`).
- **Naming order.** A pulled\-up view's range table is appended after the
  parent's own entries (`pull_up_simple_subquery`), and a kept subquery's
  follows its level. Outer\-level relations therefore claim bare refnames
  before the view's.

## Change

- **Expansion** (`planScanRangeVar`, view branch). The reference becomes
  `RangeVar{Subquery: view body, Alias: refname, Columns: view column names
  overlaid by any alias list}`, planned by `planSubqueryRangeVar`, the
  derived\-table path.
  - That path uses the statement's settings, scope and pathkeys.
  - The view\-owner privilege tagging (`tagViewOwnerScans`, M0122\-0008)
    and the cycle guard are unchanged; so is the column\-count check.
  - Views are not pulled into the parent's join search.
- **Naming** (`rtableScope.pulledViewRoots`, `renumberRTIDsFlatRtableOrder`).
  An expanded view planned without a SubqueryScan boundary, i.e. one PG
  would pull up (or a `security_barrier` one), is a nested walk placed after
  its level's own entries.
  - Without this, the depth\-first allocation let a nested view's `r` claim
    the bare name ahead of the outer query's `r` (select\_views'
    credit\_usage / credit\_card).

## Verification

- `TestViewPlansAsSubqueryRTE` pins PG 18.3's plan (checked on a scratch
  PG): `GroupAggregate / Group Key: vt.y` over the Sort, with no top Sort.
- The probe on aggregates.sql's v\_pagg\_test now matches the inline
  derived\-table plan; only the `Result`/cast residue remains.
- Regress runner, HEAD → change, 20 suites (aggregates, create\_view,
  rules, select\_views, updatable\_views, privileges, rowsecurity, matview,
  with, subselect, join, union, window, groupingsets, alter\_table, inherit,
  create\_function\_sql, polymorphism, rangefuncs, tsrf):
  - aggregates: −6 lines;
  - select\_views: 1503 → 1502;
  - the rest byte\-identical.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no plan change, TPC\-H Q15 included.

## Remaining (M0148\-0001i2)

- **Pull\-up into the join search.** Simple, non\-barrier views are not
  pulled into the parent's join search (`expandDerivedPullups` works on the
  syntactic FROM list). PG flattens them, which can change the join order.
  The view\-owner `checkAsUser` must then travel with the pulled\-up RTEs.
- **The SRF\-targetlist `Result`.** PG evaluates a cast over an SRF output
  in a `Result` above the `ProjectSet` and sorts by the full expression
  (aggregates.sql v\_pagg\_test). goopg sorts by the subquery column
  name `t`.
- **Unrelated pre\-existing bug.** After a server restart, `DROP VIEW` of a
  view created before the restart fails with "does not exist" while the
  view is still listed. This also reproduces at HEAD; DROP INDEX has the
  same failure (recorded under M0148\-0001c3b).
