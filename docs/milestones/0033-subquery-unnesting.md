# Milestone 0033 — Planner-Level Subquery Unnesting

**Status:** planned  
**Depends on:** Milestone 0031 (Q2 memory analysis identifies the bottleneck), Milestone 0003 (planner foundation and hash join)  
**Drives:** Eliminate per-row subquery re-execution in Q2 and similar correlated scalar subqueries, making TPC-H Q2 executable at SF=1 without unbounded memory growth.

## Context

TPC-H Query 2 contains a correlated scalar subquery:

```sql
ps_supplycost = (
    SELECT min(ps_supplycost)
    FROM partsupp, supplier, nation, region
    WHERE p_partkey = ps_partkey   -- p_partkey resolves to outer "part" table
      AND s_suppkey = ps_suppkey
      AND s_nationkey = n_nationkey
      AND n_regionkey = r_regionkey
      AND r_name = 'EUROPE'
)
```

The current implementation (`planSubqueryExpr` in `internal/planner/planner.go:2387`)
plans the subquery as a `SubqueryExpr` wrapping the inner plan tree. At execution time,
`evalSubquery` (`internal/executor/expr.go:627`) calls `Build → Open → Next → Close`
for **every outer row**. There is no caching and no unnesting.

With SF=1 data (~2,000 outer rows in the complete dataset, ~270K in the partial load),
each invocation drains all subquery child operators (partsupp 800K, supplier 10K, etc.)
into memory. The allocation rate exceeds GC reclaim rate, causing RSS to grow to
~28 GB (observed in `analysis/tpch-hammerdb-run-003.md`). The original memory
estimation (M0031-0001) predicted this behaviour.

## Objective

Detect correlated scalar subqueries at plan time whose correlated columns participate
in equijoin predicates with subquery columns. Rewrite the plan so the subquery is
executed **once** with a `GROUP BY` on the equijoin column, and its result is joined
back into the outer query via a hash join.

### Transformation example

**Before** (per-row subquery evaluation):
```
Filter(ps_supplycost = SubqueryExpr)
  └── Join...(outer tables → 2,000 rows)
```

**After** (single evaluation + hash join):
```
HashJoin(p_partkey = ps_partkey)
  ├── Join...(outer tables → 2,000 rows)
  └── Aggregate(min(ps_supplycost) GROUP BY ps_partkey)
        └── Filter(r_name = 'EUROPE')
              └── Join...(partsupp, supplier, nation, region)
```

The subquery aggregate runs once over the full partsupp join, producing one row per
distinct `ps_partkey`. The outer query probe-joins on `p_partkey`, replacing the
scalar subquery with a simple column reference to the aggregate result.

## Required Design Docs

1. `docs/design/0033-0001-subquery-unnesting.md` — Detection of unnestable subqueries,
   parameter extraction, subquery rewrite, and outer-query integration. Covers the
   planner-level algorithm, new plan node shapes (if any), and executor-side adjustments.

## Definition of Done

1. **Detection**: `canUnnestSubquery(expr, outerCtx)` walks a resolved `SubqueryExpr`'s
   inner plan for `OuterColumnRef` nodes. A subquery is unnestable when:
   - It is a scalar subquery (`SubqueryExpr`, not `InExpr` or `ExistsExpr` in this milestone).
   - Every `OuterColumnRef` participates in an equijoin (`=`) with a column from the
     subquery's own FROM scope.
   - The subquery is a simple aggregate (single aggregate call, no `HAVING`, no
     `DISTINCT` in the aggregate).

2. **Rewrite**: `unnestSubquery(expr, outerCtx)` produces:
   - A new plan tree for the subquery with `OuterColumnRef` nodes replaced by the
     corresponding subquery-side `ColumnRef` (the equijoin partner column).
   - A `GROUP BY` on the subquery-side equijoin columns, wrapped in an `Aggregate` node.
   - The aggregate output schema: `(group_col, agg_result_col)`.

3. **Outer integration**: The planner replaces the `SubqueryExpr` in the outer WHERE
   with:
   - A `HashJoin` between the outer plan and the unnested subquery plan.
   - Join key: the outer `ColumnRef` (what was the `OuterColumnRef`) ↔ the subquery's
     GROUP BY output column.
   - A `ColumnRef` to the aggregate result column, replacing the original `SubqueryExpr`
     in the filter predicate.

4. **Regression tests**: All 22 TPC-H queries remain parseable/plannable. Q2's EXPLAIN
   output shows a `Hash Join` with an `Aggregate` child instead of a `Subquery`.

5. **Memory verification**: Q2 executed against SF=1 partial data (270K outer rows)
   completes without unbounded RSS growth. RSS stays within the 20 GiB GOMEMLIMIT.

6. **Performance verification**: Q2 execution time is bounded (subquery runs once,
   not per outer row). Query completes in seconds rather than timing out.

7. **Design doc accepted**: `docs/design/0033-0001-subquery-unnesting.md` written
   and indexed in `docs/design/README.md`.

## Reference

- `internal/planner/planner.go:2379-2396` — `planSubqueryExpr` (current scalar subquery planning)
- `internal/planner/planner.go:2611-2723` — `resolveColumnRef` / `resolveColumnRefAt` (OuterColumnRef creation)
- `internal/planner/planner.go:610-656` — Join algorithm selection (hash join construction)
- `internal/planner/plan.go:129-137` — `SubqueryExpr` plan node
- `internal/planner/plan.go:190-205` — `OuterColumnRef` plan node
- `internal/executor/expr.go:617-662` — `evalSubquery` / `subqueryImpl` (per-row execution)
- `internal/planner/pushdown.go` — `pushPredicatesIntoCrossJoins` (pattern reference for WHERE rewrite)
- `analysis/tpch-hammerdb-run-003.md` — Q2 memory growth observation (28 GB RSS)
- `docs/design/0031-0001-q2-memory-estimation.md` — Q2 memory lower-bound analysis
