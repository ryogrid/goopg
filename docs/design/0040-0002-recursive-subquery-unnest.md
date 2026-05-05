# 0040-0002 — Recursive Subquery Unnest Inside IN/SubqueryExpr Inner Plans

**Status:** accepted
**Parent milestone:** M0040
**Date:** 2026-05-04

## 1. Objective

After M0040-0001/0002, `unnestSubqueriesInPlan` can pull `SubqueryExpr` and
`InExpr` nodes up into hash joins at the top level of the plan. However,
it did NOT recursively process the inner plans of those expressions when
the expressions themselves could not be pulled up.

For Q20:
```sql
WHERE s_suppkey IN (
  SELECT ps_suppkey FROM partsupp
  WHERE ps_availqty > (SELECT 0.5*SUM(l_quantity) FROM lineitem
                        WHERE l_partkey = ps_partkey
                          AND l_suppkey = ps_suppkey
                          AND l_shipdate >= ...)
)
```

The outer `s_suppkey IN (partsupp)` is **non-correlated** (partsupp's filter
has no equijoin with supplier). `canUnnestInExpr` returns false → the outer
IN loop exits without calling `unnestSubqueriesInPlan(partsupp_plan)`. The
lineitem scalar subquery inside partsupp's filter was therefore never
processed, forcing per-row O(lineitem) evaluation for each partsupp row.

## 2. Root Cause

`unnestSubqueriesInPlan` walks plan nodes (Filter, Join, Project, etc.) but
DOES NOT descend into `SubqueryExpr.Plan` or `InExpr.Plan` when those
expressions remain in a filter predicate after the pull-up loops fail.

`unnestInExpr` (M0040-0002) already calls `unnestSubqueriesInPlan(innerPlan)`
recursively — but only when the outer IN **can** be unnested. When it cannot,
the inner plan is left unprocessed.

## 3. Fix

Add `walkSubqueryPlansInExpr(e Expr) Expr` that recursively visits every
`SubqueryExpr` and `InExpr` in an expression tree and calls
`unnestSubqueriesInPlan` on their inner plans:

```go
func walkSubqueryPlansInExpr(e Expr) Expr {
    switch x := e.(type) {
    case *SubqueryExpr:
        x.Plan = unnestSubqueriesInPlan(x.Plan)
    case *InExpr:
        x.Plan = unnestSubqueriesInPlan(x.Plan)
    case *BinaryOp:
        x.Left = walkSubqueryPlansInExpr(x.Left)
        x.Right = walkSubqueryPlansInExpr(x.Right)
    // ... UnaryOp, FuncCall, CaseExpr, ExtractExpr ...
    }
    return e
}
```

Called at the end of the `*Filter` case in `unnestSubqueriesInPlan`, after
both the SubqueryExpr and InExpr pull-up loops:

```go
case *Filter:
    // ... pull-up loops (M0033 + M0040-0002) ...
    n.Predicate = walkSubqueryPlansInExpr(n.Predicate)  // M0040-0004
```

This ensures that any `SubqueryExpr`/`InExpr` that **remains** in the
predicate after the pull-up loops (i.e. those that could not be pulled up
to the current level) still have their inner plans optimised recursively.

## 4. Interaction with existing logic

- `unnestInExpr` already calls `unnestSubqueriesInPlan(clonedInnerPlan)` when
  the outer IN **can** be unnested (line 887). No double-processing occurs
  because successfully unnested InExpr/SubqueryExpr nodes are removed from
  the predicate before `walkSubqueryPlansInExpr` runs.
- `walkSubqueryPlansInExpr` only touches the REMAINING nodes — those that
  could not be pulled up.

## 5. Q20 effect

With this fix, the Q20 plan changes from:

```
evalInExpr(s_suppkey IN Filter(partsupp, b_val > SubqueryExpr(lineitem_agg)))
```

to:

```
evalInExpr(s_suppkey IN Join(Hash, partsupp, Agg(lineitem GROUP BY l_partkey, l_suppkey)))
```

The lineitem aggregate is computed **once** per outer IN evaluation (not once
per partsupp row), reducing the inner complexity from
O(partsupp_rows × lineitem_rows) to O(lineitem_rows + partsupp_rows).
Combined with M0040-0001's IN subquery result caching, the whole Q20 scales
much more favourably.

## 6. Verification

- `TestRecursiveUnnestInsideNonUnnestableIN` (new) in `unnest_test.go`:
  3-table (a, b, c) schema; outer non-correlated IN; inner correlated scalar
  subquery. Asserts InExpr.Plan contains HashJoin+Aggregate (not SubqueryExpr)
  after planning.
- `TestTPCHResultParity`: identical=22, divergent=0, errored=0 — PASS.
- `TestRunTPCHQueriesAgainstSyntheticData`: 22/22 PASS.
- `TestBushyPlanWithUnnest`: PASS (no regression on Q2 unnesting).
