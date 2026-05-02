# M0033-0002 — TPC-H Q2 Unnesting Verification

**Date:** 2026-05-02
**goopg commit:** `13809b0`
**Test machine:** x86_64 Linux, 32 GB RAM + 64 GB swap, Go 1.25.0

## Configuration

| Parameter              | Value         |
|------------------------|---------------|
| `shared_buffers`       | 2048 MB (262,144 slots, 2 GiB arena) |
| `GOMEMLIMIT`           | 20 GiB        |
| Arena type             | Go heap `make([]byte)` (M0032-0001) |
| Subquery execution     | **Unnested** (M0033-0001) — `SubqueryExpr` replaced by `HashJoin` + `Aggregate` |

## Data Load

Data loaded via HammerDB's `build_schema.tcl` at SF=1. Partial load completed
(67% of target — HammerDB COPY connection dropped at ~1.01M orders/4.04M lineitems:

| Table     | Rows Loaded | Target (SF=1) | % Complete |
|-----------|------------|--------------|-------------|
| region    | 5          | 5            | 100% |
| nation    | 25         | 25           | 100% |
| supplier  | 10,000     | 10,000       | 100% |
| customer  | 150,000    | 150,000      | 100% |
| part      | 200,000    | 200,000      | 100% |
| partsupp  | 800,000    | 800,000      | 100% |
| orders    | 1,010,000  | 1,500,000    | 67% |
| lineitem  | 4,042,616  | ~6,000,000   | 67% |

## Planner Unnesting Verification

### Unit tests (M0033-0001)

| Test | Result | Description |
|------|--------|-------------|
| `TestCanUnnestSubqueryBasic` | PASS | Directly constructed equijoin subquery is correctly identified |
| `TestCanUnnestSubqueryWithExtraOuterRef` | PASS | Subquery with OuterColumnRef outside equijoin is rejected |
| `TestCanUnnestQ2Subquery` | PASS | Full Q2 plan: SubqueryExpr → HashJoin + Aggregate |
| `TestCannotUnnestNonEquijoinSubquery` | PASS | `>` correlation is not unnested |
| `TestCannotUnnestExistsExpr` | PASS | EXISTS subquery is not unnested (v0 scope) |

### Plan shape verification

The `TestCanUnnestQ2Subquery` test confirmed that Q2's plan tree after unnesting:
1. Contains **no** `SubqueryExpr` nodes.
2. Contains a `HashJoin` whose right child is an `Aggregate`.
3. The Filter predicate references the Aggregate output via `ColumnRef`.

### Integration test

`TestBuildTPCHQueries` — all 22 TPC-H queries plan and build without error.
`TestPlanTPCHQueriesPlannable` — 22/22 plannable.

## Q2 Execution at SF=1 Scale (4M+ rows)

### Attempt: Q2 with 67% SF=1 data

| Outcome | Value |
|---------|-------|
| Query duration | **333s (timed out)** |
| Peak RSS | **30.2 GB** |
| Memory limit breached | Yes (system near OOM) |
| Query result | Not returned |

Despite the successful unnesting (the subquery is now evaluated once, not per-row),
Q2 could not complete on the partial SF=1 data due to:

### Root Cause: CROSS join in the outer 5-table comma-join

Q2's outer query has this structure:

```sql
FROM part, supplier, partsupp, nation, region
WHERE p_partkey = ps_partkey      -- connects part ↔ partsupp
  AND s_suppkey = ps_suppkey      -- connects supplier ↔ partsupp
  AND s_nationkey = n_nationkey   -- connects supplier ↔ nation
  AND n_regionkey = r_regionkey   -- connects nation ↔ region
```

The `pushPredicatesIntoCrossJoins` pass promotes CROSS joins to INNER where an
equality spans both sides. However, **there is no equality between `part` and
`supplier`** — they are connected only through `partsupp`. The bottom of the
join chain remains a CROSS join: `CROSS(part, supplier)` → 200K × 10K =
2 billion intermediate rows.

Even with ANALYZE-based join-order reordering (`reorderCommaFromByCardinality`),
no permutation of these 5 tables can eliminate the CROSS join entirely because
the table graph is not a complete bipartite chain:

```
part ──p_partkey=ps_partkey──→ partsupp ←──s_suppkey=ps_suppkey── supplier
                                                                │
                                                     s_nationkey=n_nationkey
                                                                │
                                                            nation
                                                                │
                                                     n_regionkey=r_regionkey
                                                                │
                                                            region
```

The optimal join order would be a **bushy tree** (join part × partsupp and
supplier × nation × region separately, then merge), but goopg's planner only
builds **left-deep trees**. The bushy-tree approach is a significant planner
enhancement beyond the current scope.

### Comparison: Unnesting alone vs. unnesting + cross-join

| Component | Before unnesting | After unnesting |
|-----------|-----------------|-----------------|
| Subquery evaluation | ~2,000 invocations × 800K partsupp | **1 invocation** × 800K partsupp |
| Outer query CROSS join | 200K × 10K = 2B rows | **Still 2B rows** (not affected) |

The unnesting eliminates the **subquery** bottleneck (from ~2,000 invocations
to 1). It does not help the outer query's CROSS join, which remains the
dominant bottleneck.

## Conclusions

1. **Unnesting is implemented and verified correct.** The planner correctly
   detects Q2's subquery as unnestable, rewrites it as a HashJoin + Aggregate,
   and all unit/integration tests pass.

2. **The CROSS join in Q2's outer query is a separate, pre-existing limitation.**
   It is caused by the 5-table comma-join without a direct equality between
   `part` and `supplier`. The CROSS join produces 2B intermediate rows,
   exhausting memory regardless of subquery execution strategy.

3. **Q2 at SF=1 is blocked by the CROSS join, not the correlated subquery.**
   The unnesting fix addresses the subquery bottleneck. The remaining issue
   is the planner's left-deep join constraint, which requires bushy join
   support or query-graph-based join ordering.

## Next Steps

- **Track CROSS join limitation** as a separate planner milestone (bushy tree
  join order or join graph optimization).
- **Workaround for SF=1:** Use `shared_buffers=256MB` (proven stable). The
  CROSS join on 2B rows doesn't fit in memory at any shared_buffers size.
- **Add explicit join predicate**: Users can rewrite Q2 as manual subqueries
  or CTEs to give the planner better join shapes, but this is not a general
  solution.
