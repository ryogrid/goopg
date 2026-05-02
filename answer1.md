# Q2 Subquery Caching / Unnesting — Implementation Approaches

## Problem Structure

Q2's subquery is correlated on the outer `p_partkey` column:

```sql
ps_supplycost = (
  SELECT min(ps_supplycost)
  FROM partsupp, supplier, nation, region
  WHERE p_partkey = ps_partkey     -- p_partkey references the outer "part" table
    AND s_suppkey = ps_suppkey
    AND ...
)
```

The executor's current path (`evalSubquery` in `internal/executor/expr.go:617`) runs
`Build → Open → drainRows(buffer all children) → Next → Close` **per outer row**.
There is no caching. With 270K outer rows in a partial SF=1 load, this means 270K
full join-tree constructions, each draining the supplier (10K), nation (25), region (5),
and partsupp (800K) tables — hence memory explosion (~28 GB RSS observed).

## Approach A: Executor-Level Caching (simple, immediate)

In `evalSubquery`, cache the scalar result keyed by the correlated parameter values.
If the same `p_partkey` is encountered, return the cached value instead of re-executing
the subquery.

```go
// Pseudocode
type subqueryCacheKey struct {
    pPartkey int64
}

var subqueryCache = map[subqueryCacheKey]Datum{}

func evalSubquery(x *planner.SubqueryExpr, row Row, ctx *Context) (Datum, error) {
    key := subqueryCacheKey{pPartkey: extractOuterValue(row)}
    if cached, ok := subqueryCache[key]; ok {
        return cached, nil
    }
    result := subqueryImpl(x, ctx)  // existing evaluation
    subqueryCache[key] = result
    return result, nil
}
```

### Pros
- One file, ~30 lines of code.
- Q2 becomes executable immediately.
- No planner changes.

### Cons
- Not a true unnest — the subquery still runs at least once per distinct `p_partkey`.
- Cache grows linearly with distinct parameter values (up to ~200K for Q2 at SF=1).
  Needs a bounded LRU or scope-tied lifecycle (clear after outer query completes).
- Does not help other correlated subquery patterns (IN, EXISTS).
- Not PostgreSQL-compatible in approach.

---

## Approach B: Planner-Level Unnesting (correct, PostgreSQL-compatible)

The planner detects a correlated scalar subquery and rewrites it as a `GROUP BY` +
`JOIN` with the outer query. This eliminates per-row re-execution entirely —
the subquery runs **once**, producing a result set that is hash-joined with the outer
query.

### Transformation

Before:
```sql
SELECT ...
FROM part, supplier, partsupp, nation, region
WHERE ...
  AND ps_supplycost = (
    SELECT min(ps_supplycost)
    FROM partsupp, supplier, nation, region
    WHERE p_partkey = ps_partkey AND s_suppkey = ps_suppkey AND ...
  )
```

After (logical equivalent):
```sql
SELECT ...
FROM part, supplier, partsupp, nation, region
  JOIN (
    SELECT ps_partkey, min(ps_supplycost) AS mincost
    FROM partsupp, supplier, nation, region
    WHERE s_suppkey = ps_suppkey
      AND s_nationkey = n_nationkey
      AND n_regionkey = r_regionkey
      AND r_name = 'EUROPE'
    GROUP BY ps_partkey
  ) AS subq ON part.p_partkey = subq.ps_partkey
WHERE ...
  AND ps_supplycost = subq.mincost
```

### Implementation points

1. **Detection** in `planSubqueryExpr` (`internal/planner/planner.go:2379`):
   - Walk the subquery's WHERE clause for `OuterColumnRef` nodes.
   - If all outer refs are equijoins with subquery columns, the subquery is a candidate.
   - The outer column must come from a single outer table reachable through the join tree.

2. **Parameter extraction**: Identify which outer columns are used as GROUP BY keys.
   In Q2's case, `p_partkey` → `GROUP BY ps_partkey`.

3. **Subquery rewrite**:
   - Replace `OuterColumnRef` nodes in the subquery's WHERE with regular `ColumnRef`
     nodes (the subquery column they're equated with).
   - Add the subquery column as a GROUP BY key.
   - The subquery's output schema becomes `(group_key_column, aggregate_result)`.

4. **Outer query integration**:
   - Replace the `SubqueryExpr` in the outer WHERE with a `ColumnRef` to the joined
     subquery's aggregate output column.
   - Add a hash-join between the outer plan and the unnested subquery plan.
   - The join key is the outer column ↔ subquery GROUP BY column.

5. **Planner file scope**: Changes primarily in `internal/planner/planner.go`
   (`planSubqueryExpr`, `resolveColumnRef`, new `canUnnestSubquery` and
   `buildUnnestedSubqueryPlan` helpers). May also touch `internal/planner/plan.go`
   for new plan node types if needed.

### Pros
- **Correct**: matches PostgreSQL's subquery unnesting semantics.
- **Efficient**: subquery runs exactly once, O(1) invocations regardless of outer row count.
- **General**: applies to any scalar subquery with equijoin correlation, not just Q2.
- **Memory**: bounded to the subquery's result set (e.g., ~200K rows × 2 columns for Q2 SF=1).

### Cons
- Larger implementation scope (estimated 200–400 lines of planner code).
- Requires careful handling of edge cases:
  - Multiple correlated columns.
  - Nested subqueries.
  - Subqueries with aggregates and HAVING.
  - NULL semantics (outer rows with no match in the subquery result).

---

## Recommendation

| Timeline | Approach | Scope |
|----------|----------|-------|
| Immediate | Approach A (caching) | 1 file, ~30 lines. Enables Q2 to run at SF=1. |
| Follow-up | Approach B (unnesting) | Planner rewrite. PostgreSQL-compatible, general solution. |

Approach A gets Q2 working now. Approach B is the correct long-term fix and should
follow as a separate planner milestone.
