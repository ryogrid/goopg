# M0146-0170 — a kept sublink whose plan holds a lateral nested loop is not re-based

Status: landed 2026-10-13 (`566d9721c`).
Parent: M0146-0159 (found by, and blocking, that task's sf025 gate).

## Problem

TPC-DS Q32 returned NULL at SF0.25 where PostgreSQL 18.3 returns 2444.35. It
happened once M0146-0159's cost change gave its correlated SubPlan this
shape:

```
SubPlan 1
  -> Aggregate
       -> Nested Loop
            -> Bitmap Heap Scan on catalog_sales catalog_sales_1   (cs_item_sk = item.i_item_sk)
            -> Index Scan using date_dim_pkey on date_dim           (d_date_sk = catalog_sales.cs_sold_date_sk)
```

The SubPlan sat in a qual of a join whose other side (`item`) supplies the
correlation.

- **Repro on HEAD.** With nested loops forced, the minimal repro
  (`analysis/m0146/m0146-0159/q32-subplan-param-repro.sql`) gives NULL; PG
  gives 26457.
- **What was still correct.** The same subquery in a target list, or in a
  qual over a single relation.

## Cause

The SubPlan body holds two kinds of `OuterColumnRef{Level: 1}`:

- the correlation to the enclosing query (`item.i_item_sk`);
- the nestloop param of its own inner probe (`catalog_sales_1.cs_sold_date_sk`).

The nested loop is an R25 decomposed lateral `Join` (`outerParamKey`,
createplannl.go). The executor resolves its param from the frame the join
pushes onto `ctx.OuterRows` for each outer row.

The sublink pull-up post-pass (`rebaseQualKeptSubplans` → `rebaseOne` →
`rebaseNode`, pulledsublink.go) re-bases every ref with `Level > linkDepth`
as an escaping correlation. It therefore turned the nestloop param into a
SubPlan parameter (`ExecParamRef`) filled from the host row.

- **Instrumentation.** In the repro, `$1` read the outer `cs.ord` values
  instead of the inner row's `sold`, so every average covered the wrong
  dates and the outer filter rejected every row.
- **Why the gate missed it.** `keptSubplanAdmissible` refuses plans that
  contain a lateral join ("nested-sublink-lateral"). The post-pass, however,
  also reaches kept sublinks that gate never saw.

PG has no equivalent pass: its SubPlans keep `PARAM_EXEC` params assigned per
plan level (`SS_replace_correlation_vars`, subselect.c), and a nestloop param
(`replace_nestloop_params`, createplan.c) never becomes a SubPlan parameter.

## Change

- **`rebaseOne` refuses a lateral plan.** It returns false with the same
  reason `nested-sublink-lateral`, so the kept sublink keeps the stack path,
  where the lateral join's own push resolves the param.
- **Sibling coverage**, so the detector sees every probe key the rewriter
  touches:
  - `walkPlanExprs` (feeding `planContainsLateralJoin` and `planHasOuterRef`)
    now visits `IndexScan.Keys` and `SAOPKeys`;
  - the lowering walker (`subplan_lower_walk.go`) now rewrites `SAOPKeys`;
  - `NodeSubplans` (walk_export.go) now lists `SAOPKeys`.

  Without them, a correlated multi-column probe was invisible to the lateral
  and correlation checks. The `IsNonCorrelated` misclassification would cache
  the first outer row's result: the same failure class as M0134-0185's
  bitmap arm.

## Verification

- **New test.** `TestCorrelatedSubPlanKeepsInnerNestloopParam` (executor)
  covers the failing join-qual form plus the two forms that were already
  correct. It fails on HEAD (NULL against PG's 26457).
- **Regress A/B.**
  - `subselect`, `with`, `aggregates`, `select_distinct`, `groupingsets`,
    `union`, `window`, `rowtypes`, `rangefuncs` and `select` are identical.
  - `join` +1 line: the same rows and plan lines in a different order.
- **Gates.** Units; postmaster; tpch-spotcheck; the acceptance arm (values
  identical); sf025 PASS=99. The TPC-DS fire-set gate shows no plan change at
  HEAD's costs.
- **With M0146-0159 staged on top:** sf025 PASS=99, and Q32 is correct.

## Residuals (ledgered)

- **EXPLAIN rendering.** EXPLAIN prints a lateral nestloop param with the
  base table's name (`cs.sold`) where PG prints the inner relation's alias
  (`cs_1.sold`, or `x.sold` when aliased).
- **Stack path, not a lowered PARAM_EXEC block.** A kept sublink with a
  lateral nested loop now always stays on the stack path. Teaching
  `rebaseNode` to skip refs bound by an enclosing lateral join's frame would
  let such sublinks be re-based too.
