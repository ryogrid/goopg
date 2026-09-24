# M0145-0008j: EXPLAIN renders every stacked Filter

Status: **LANDED 2026-09-24** (EXPLAIN renderer only; no plan change). Task:
`.ralph/fix_plan.md` M0145-0008j (Kind: impl, Parent: M0145-0008e). Follow-up
for PG's plan shape: **M0145-0008o**.

## Defect

`SELECT a FROM ht1 WHERE a > 1 AND EXISTS (SELECT 1 FROM ht3)` plans as a
residual `*Filter` (the uncorrelated EXISTS the pull-up declines) stacked on
the searched leaf's own `*Filter` (`a > 1`). `walkPlanFiltered`
(`internal/executor/operators_explain.go`) collapses Filter wrappers into
the next scan line. When two stacked, it kept only the outermost predicate
("render only the outermost predicate … for v0"), so goopg printed:

```
Seq Scan on sf1
  Filter: (EXISTS(SubPlan 1))
```

Both predicates executed, and results were correct, but EXPLAIN (the
parity instrument) hid a qual.

## Fix

Stacked wrappers now collapse into one `Filter:` line carrying their
conjunction, inner predicate first (it runs first). The inner one is the
wrapper closer to the scan. PG prints a node's whole qual list as one line
(`show_upper_qual` over `plan->qual`, `postgres/src/backend/commands/explain.c`).
The outermost wrapper stays the row source for the line's estimate: its
`EstimateRows` already scales through every inner wrapper.

```
Seq Scan on sf1
  Filter: ((a > 1) AND (EXISTS(SubPlan 1)))
  SubPlan 1
    ->  Seq Scan on sf3
```

Pinned by `TestExplainRendersEveryStackedFilter`
(`internal/executor/explain_stacked_filter_test.go`).

## What PG 18.3 actually plans (scratch cluster, 2026-09-24)

```
Result
  One-Time Filter: (InitPlan 1).col1
  InitPlan 1
    ->  Seq Scan on sf3
  ->  Seq Scan on sf1
        Filter: (a > 1)
```

Three mechanisms goopg lacks for this shape, filed as **M0145-0008o** and
ledgered:
1. **Pseudoconstant quals gate the plan.** A qual with no Vars of the
   current level and no volatile functions is a pseudoconstant.
   `create_gating_plan` (`postgres/src/backend/optimizer/plan/createplan.c`)
   places it on a `Result` as `resconstantqual`, above the scan, evaluated
   once. goopg's `Result{OneTimeFilter, Child}` exists, but only the
   min/max-agg constant arm and the NOT NULL `false` reduction build it.
2. **An uncorrelated EXISTS is an InitPlan.** `make_subplan` turns an
   uncorrelated `EXISTS_SUBLINK` into an initplan param
   (`postgres/src/backend/optimizer/plan/subselect.c`), rendered
   `(InitPlan 1).col1`. goopg's `subPlanName` names only a non-correlated
   scalar `SubqueryExpr` as an InitPlan; an `ExistsExpr` stays `SubPlan`.
3. Consequence: the per-row EXISTS evaluation in the scan's Filter becomes
   one evaluation per statement.

## Gates (staged tree)

- units PASS; `tpch-spotcheck` PASS.
- fire-set HEAD `83042677c` vs staged: `fires=none` at SF0.25, SF1 and
  TPC-H. No corpus plan's text changes, so the defect shape is absent from
  both corpora.
- `tpcds-sf025 sweep`: 96/96, PLAN-SHAPE `same=99`.

Movement: none.
