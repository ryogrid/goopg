# 0033-0001 — Planner-Level Subquery Unnesting

**Status:** draft  
**Parent milestone:** M0033  
**Date:** 2026-05-02

## 1. Objective

Detect correlated scalar subqueries at plan time and rewrite them so the subquery
executes **once** as a `GROUP BY` aggregate, joined back to the outer query via a
hash join. This eliminates the per-row Build/Open/Close cycle that currently causes
unbounded memory growth on Q2.

## 2. Current Behaviour

### 2.1 Planner

`planSubqueryExpr` (`planner.go:2387`) calls `planSelectWithParent`, which:
1. Sets `planParent` to the outer `resolveContext`.
2. Plans the inner `SELECT` via `Plan()`.
3. Resolves column references via `resolveColumnRef`, which walks up `planParent`
   and emits `OuterColumnRef` nodes for columns from outer scopes.
4. Wraps the resulting plan tree in a `SubqueryExpr{Plan: inner}`.

No unnesting is attempted — every correlated subquery remains as a `SubqueryExpr`.

### 2.2 Executor

`evalSubquery` (`expr.go:627`) pushes the current outer row onto `ctx.OuterRows`,
calls `subqueryImpl`, which:
1. `Build(x.Plan)` — constructs a fresh operator tree.
2. `op.Open(ctx)` — opens and drains all children.
3. `op.Next()` — reads the scalar result.
4. `defer op.Close()` — closes the operator.

This happens for every outer row. No result caching. If the outer query produces
2,000 rows, the subquery's operator tree is built, opened, and drained 2,000 times.

### 2.3 Why this fails for Q2

| Factor | Impact |
|--------|--------|
| Subquery joins 4 tables (partsupp, supplier, nation, region) | Each invocation drains all 4 tables |
| `drainRows` deep-copies every row from child operators | 800K partsupp rows × 2,000 invocations = 1.6B row copies |
| `joinOp` allocates hash tables and result rows per invocation | GC cannot keep up |
| `GOMEMLIMIT=20GiB` tells GC "don't scavenge below 20 GB" | Heap grows to 28 GB before OOM risk |

## 3. Proposed Algorithm

### 3.1 Detection (`canUnnestSubquery`)

Walk the subquery's planned expression tree and identify whether the subquery
is a candidate for unnesting. A subquery is unnestable when:

1. **Type check**: `SubqueryExpr` only (not `InExpr` or `ExistsExpr` in this milestone).
2. **No outer-column references outside equijoins**: Every `OuterColumnRef` in the
   subquery's plan tree must appear as the left or right operand of a `BinaryOp("=")`.
   The other operand must be a `ColumnRef` from the subquery's own FROM scope.
   This ensures we can replace the outer column with the subquery's group-by column.
3. **Simple aggregate**: The subquery's root is an `Aggregate` node with exactly one
   aggregate call and no `HAVING` clause. The aggregate call must be `min`, `max`,
   `sum`, `avg`, or `count`.
4. **No DISTINCT in the aggregate**: The aggregate call must not have `Distinct=true`.
5. **Single FROM scope**: All equijoin columns come from within the subquery's
   top-level FROM (no nested subquery columns in the equijoin).

| Check | Q2 status |
|-------|-----------|
| SubqueryExpr | Yes (scalar subquery) |
| OuterColumnRef in `=` with subquery column | `p_partkey = ps_partkey` — `p_partkey` is OuterColumnRef, `ps_partkey` is ColumnRef |
| Simple aggregate | `min(ps_supplycost)` — single aggregate, no HAVING |
| No DISTINCT | min without DISTINCT |
| Single FROM scope | ps_partkey from partsupp, within subquery's FROM |

**Q2 passes all checks → unnestable.**

### 3.2 Parameter extraction (`collectUnnestParams`)

Walk the subquery's WHERE tree and collect `(outerRef, subqueryCol)` pairs for each
equijoin involving an `OuterColumnRef`.

For Q2:
```
(p_partkey [OuterColumnRef, part table], ps_partkey [ColumnRef, partsupp table])
```

Map: outer column → subquery GROUP BY column.

### 3.3 Subquery rewrite (`buildUnnestedSubquery`)

Given the original subquery plan tree and the extracted parameter map:

1. **Clone the subquery plan** (excluding the aggregate root).
2. **Replace `OuterColumnRef` nodes** with the corresponding `ColumnRef` from the
   subquery's FROM side (the equijoin partner). In Q2, replace `OuterColumnRef{p_partkey}`
   with `ColumnRef{ps_partkey}` in the WHERE tree.
3. **Add GROUP BY** on the subquery-side columns. Wrap in an `Aggregate` node with
   the same aggregate call.
4. **Output schema**: `[group_col, agg_result]`.

Resulting subquery plan (simplified):
```
Aggregate(min(ps_supplycost) GROUP BY ps_partkey)
  └── Filter(r_name = 'EUROPE' AND s_suppkey = ps_suppkey AND ...)
        └── Join INNER HASH (ps_suppkey = s_suppkey)
              ├── Join INNER HASH (ps_partkey = ...)  ← no more OuterColumnRef here
              │     ├── partsupp
              │     └── supplier
              └── Join INNER HASH (n_regionkey = r_regionkey)
                    ├── nation
                    └── region
```

### 3.4 Outer query integration

Replace the `SubqueryExpr` in the outer query's WHERE tree with a hash join:

1. **Create a `Join` node**: `JoinAlgoHash`, `JoinTypeInner`.
   - Left: outer plan tree (unchanged).
   - Right: the unnested subquery plan from step 3.3.
   - LeftKey: `ColumnRef` to the outer table column that was the `OuterColumnRef`
     (e.g., `part.p_partkey`).
   - RightKey: `ColumnRef` to the subquery's GROUP BY output column
     (e.g., subquery output column 0, `ps_partkey`).

2. **Replace the `SubqueryExpr`** in the outer filter with a `ColumnRef` pointing
   to the subquery's aggregate output column (e.g., subquery output column 1,
   `min(ps_supplycost)`).

3. **Insert the join** into the outer plan tree at the point where the `SubqueryExpr`
   was referenced. If the `SubqueryExpr` was in a `Filter` predicate, the `Filter`
   stays (with the SubqueryExpr replaced by the ColumnRef), and the `Join` is
   inserted between the `Filter` and its child.

Final plan tree for Q2 outer query (simplified):
```
Sort(s_acctbal DESC, ...)
  └── Project(targets)
        └── Filter(s_suppkey = ps_suppkey AND p_size = 15 AND ... AND ps_supplycost = subq.mincost)
              └── HashJoin(p_partkey = subq.ps_partkey)
                    ├── Join INNER HASH (s_suppkey = ps_suppkey)
                    │     ├── Join INNER HASH (p_partkey = ps_partkey)
                    │     │     ├── Cross(part, supplier)
                    │     │     └── partsupp
                    │     └── ...
                    └── Aggregate(min(ps_supplycost) GROUP BY ps_partkey)  ← runs ONCE
                          └── (subquery join tree, no OuterColumnRef)
```

## 4. Implementation Plan

### 4.1 New helper functions (`internal/planner/planner.go`)

| Function | Responsibility |
|----------|---------------|
| `canUnnestSubquery(sub *SubqueryExpr, outerCtx *resolveContext) bool` | Applies the 5 detection checks. Returns true if the subquery is a candidate. |
| `collectUnnestParams(sub *SubqueryExpr) ([]unnestParam, error)` | Walks the inner plan's WHERE for `(OuterColumnRef, ColumnRef)` equijoin pairs. |
| `buildUnnestedSubquery(sub *SubqueryExpr, params []unnestParam) (Node, Schema, error)` | Clones and rewrites the subquery: replaces OuterColumnRefs, adds GROUP BY. Returns the new plan tree and its output schema. |
| `integrateUnnestedSubquery(outer Node, subPlan Node, subSchema Schema, params []unnestParam, outerCtx *resolveContext) (Node, error)` | Inserts a hash join between the outer plan and the unnested subquery. Replaces the SubqueryExpr reference in the outer filter. |

### 4.2 Changes to existing code

| Location | Change |
|----------|--------|
| `planSubqueryExpr` (line 2387) | After planning the inner SELECT, call `canUnnestSubquery`. If true, call the unnest pipeline and return the rewritten expression node instead of a `SubqueryExpr`. |
| `planner/plan.go` | Add `unnestParam` struct type: `{OuterRef *OuterColumnRef, SubCol *ColumnRef, SubColIdx int}`. |
| `executor/expr.go` | No changes required — the executor never sees the `SubqueryExpr` after unnesting; it sees a normal `HashJoin` + `Aggregate` tree. |

### 4.3 Limitations (this milestone)

- Scalar `SubqueryExpr` only. `InExpr` and `ExistsExpr` unnesting deferred.
- Single aggregate call only (no multiple aggregates in the subquery).
- No `HAVING` clause support in the unnested subquery.
- Single correlated column only (Q2 has exactly one).
- No subquery in the SELECT target list (only WHERE clause).
- The outer column must have a single, unambiguous path through the outer join tree.
  Multi-table column resolution (where the outer column appears in multiple outer
  tables) is deferred.

## 5. Verification

### 5.1 Unit tests (planner level)

- `TestCanUnnestQ2`: Q2's SubqueryExpr passes `canUnnestSubquery`.
- `TestCannotUnnestNonEquijoin`: Subquery with `>` correlation (not `=`) is rejected.
- `TestCannotUnnestMultiAggregate`: Subquery with multiple aggregate calls is rejected.
- `TestCannotUnnestWithHaving`: Subquery with HAVING is rejected.
- `TestCannotUnnestExistsExpr`: EXISTS subquery is not handled by this pass.
- `TestUnnestQ2`: Full unnest of Q2's subquery — verify the resulting plan has
  a HashJoin with an Aggregate child, and no SubqueryExpr remains.

### 5.2 Integration tests (executor level)

- `TestQ2UnnestedExecution`: Q2 with sample data executes and returns results
  without OOM (RSS stays bounded).
- `TestQ2UnnestedVsOriginal`: Results from unnested Q2 match the original per-row
  evaluation (correctness).

### 5.3 EXPLAIN output

`EXPLAIN SELECT ...` (Q2) shows the unnest structure:
```
Hash Join (p_partkey = ps_partkey)
  ...
  Aggregate (min)
    ...
```

No `Subquery` node in the plan.

## 6. Reference

- `internal/planner/planner.go:2379-2396` — `planSubqueryExpr`
- `internal/planner/planner.go:2611-2723` — `resolveColumnRef` / `resolveColumnRefAt`
- `internal/planner/planner.go:610-656` — Join algorithm selection
- `internal/planner/plan.go:129-137` — `SubqueryExpr`
- `internal/planner/plan.go:190-205` — `OuterColumnRef`
- `internal/planner/pushdown.go:29-49` — `pushPredicatesIntoCrossJoins` (WHERE rewrite pattern)
- `internal/executor/expr.go:617-662` — `evalSubquery` / `subqueryImpl`
- `analysis/tpch-hammerdb-run-003.md` — Q2 memory growth (28 GB RSS)
- `docs/design/0031-0001-q2-memory-estimation.md` — Q2 memory lower-bound
