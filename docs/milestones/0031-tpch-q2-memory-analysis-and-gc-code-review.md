# Milestone 0031 — TPC-H Q2 Memory Analysis & GC Leak Code Review

**Status:** accepted
**Depends on:** Milestone 0029 (HammerDB TPC-H run), Milestone 0029a (index support)
**Drives:** Root-cause analysis of WSL2 OOM crash during TPC-H Query 2 execution, and systematic identification of GC-uncollectable memory patterns in the executor.

## Context

TPC-H Query 2 (Minimum Cost Supplier) was executed on goopg at SF=1, VUSER=1. The query
caused sustained memory growth that escalated until the WSL2 VM itself was killed by the
host OS. The `GOMEMLIMIT=512MiB` soft limit was in place, but Go's GC cannot reclaim
memory that is still reachable — and prior observations (M0029) showed that `shared_buffers`
values above 256MB already trigger OOM during bulk load, suggesting systemic memory
pressure.

This milestone has two objectives:

1. **Memory estimation**: Calculate the theoretical minimum memory footprint for Q2 at
   SF=1 with ideal GC behaviour, based on the current operator implementation and the
   query's execution plan. This establishes a lower bound — if the lower bound already
   exceeds available memory, the query is fundamentally un-executable with the current
   approach.

2. **GC-leak code review**: Audit every operator and allocation site in `internal/executor/`
   for patterns where memory remains reachable (and thus uncollectable by GC) after it is
   no longer needed. Identify concrete fixes.

### TPC-H Query 2 (HammerDB variant)

```sql
SELECT s_acctbal, s_name, n_name, p_partkey, p_mfgr, s_address, s_phone, s_comment
FROM part, supplier, partsupp, nation, region
WHERE p_partkey = ps_partkey
  AND s_suppkey = ps_suppkey
  AND p_size = 15
  AND p_type LIKE '%BRASS'
  AND s_nationkey = n_nationkey
  AND n_regionkey = r_regionkey
  AND r_name = 'EUROPE'
  AND ps_supplycost = (
      SELECT min(ps_supplycost)
      FROM partsupp, supplier, nation, region
      WHERE p_partkey = ps_partkey
        AND s_suppkey = ps_suppkey
        AND s_nationkey = n_nationkey
        AND n_regionkey = r_regionkey
        AND r_name = 'EUROPE'
  )
ORDER BY s_acctbal DESC, n_name, s_name, p_partkey;
```

**Key structural properties:**
- 5-table join (part ↔ partsupp ↔ supplier ↔ nation ↔ region) in both outer and subquery.
- Outer filter: `p_size = 15 AND p_type LIKE '%BRASS' AND r_name = 'EUROPE'` reduces
  the outer result to at most the number of parts meeting the brass+size filter in Europe.
- The subquery `min(ps_supplycost)` is **correlated** on the outer `p_partkey`.
- Each outer row triggers a full subquery re-evaluation over PARTSUPP (~800K rows at SF=1
  after NATION/REGION/SUPPLIER join).
- ORDER BY requires a final sort on the outer result.

### Data cardinalities (SF=1)

| Table     | Rows       | Notes                                    |
|-----------|------------|------------------------------------------|
| region    | 5          | Filtered to 1 (r_name = 'EUROPE')        |
| nation    | 25         | ~5 after join with region                |
| supplier  | 10,000     | ~2,000 after nation join (5 nations)     |
| part      | 200,000    | Filtered by p_size=15 AND p_type LIKE '%BRASS' (~1% → ~2,000) |
| partsupp  | 800,000    | ~800 per part (4 suppliers per part)     |

**Rough estimate:** The outer query produces ~2,000 rows (filtered parts × filtered suppliers
with matching partsupp records). Each of these ~2,000 rows triggers one subquery execution.

The subquery executes a 4-table join (partsupp × supplier × nation × region) with the
correlated `p_partkey`. For a single part, PARTSUPP has ~4 matching rows. After joining
through supplier/nation/region and filtering to EUROPE, the subquery produces ~1-4 rows
per outer row. The `min()` aggregate reduces this to a single scalar.

### Why this matters

If goopg's planner does **not** unnest the correlated subquery — and the current
`planSubqueryExpr` in `internal/planner/planner.go:2379` treats it as a scalar subquery
evaluated per outer row — then for each of ~2,000 outer rows, the executor:

1. **Opens a full join operator** for the subquery's 4-table join.
2. **Drains (copies) all child rows** into `joinOp.rows` — this includes PARTSUPP rows
   after filtering by `p_partkey` (small, ~4 rows) but also after joining with
   SUPPLIER/NATION/REGION.
3. **Sorts the final outer result** (~2,000 rows, modest).

The key question: **does the planner execute the subquery once per outer row, or does
it cache/unnest?**

Looking at `internal/planner/planner.go:2379-2394`, the `planSubqueryExpr` creates a
SubqueryExpr with the inner plan. The executor's `evalSubquery` (`internal/executor/expr.go:617`)
pushes the outer row onto `ctx.OuterRows`, calls `subqueryImpl` which does Build → Open →
Next → Close for the inner plan. There is **no caching** — every outer row triggers a full
Build/Open/Close cycle. For ~2,000 outer rows × 4 rows in the subquery, the absolute work
is manageable (~8,000 rows processed), but the per-invocation allocations (join operator
construction, `drainRows` copies, hash table) multiply by 2,000.

If each subquery invocation allocates and retains memory that cannot be GC'd between
invocations (e.g., operator struct fields not nilled on Close), the retained memory
accumulates linearly with the number of outer rows.

## Required Design Docs

1. `docs/design/0031-0001-q2-memory-estimation.md` — Theoretical memory bound for Q2
   assuming ideal GC, tracing each operator's peak and retained allocation.
2. `docs/design/0031-0002-executor-gc-leak-review.md` — Leaf-by-leaf audit of all
   executor operators for GC-uncollectable retained memory, with fix proposals.

## Definition of Done

1. **Memory estimation complete**: A table in `0031-0001-*.md` showing per-operator
   expected heap allocation for Q2 at SF=1, breaking down:
   - Peak allocation per operator invocation
   - Number of invocations (outer × subquery)
   - Retained allocation after `Close()`
   - Total retained memory after full query execution (sum of all operators' retained state)

2. **Lower bound established**: The estimate shows whether the theoretical minimum
   (ideal GC) fits within the 512 MiB `GOMEMLIMIT`. If it does not fit, the document
   identifies which operators are responsible for the excess.

3. **GC-leak review complete**: Every operator in `internal/executor/` is audited for:
   - Fields that retain slice/map/row data after `Close()` returns.
   - Per-invocation allocations that are retained across re-Open cycles.
   - Array/slice growth patterns where backing arrays never shrink.
   - Missing `nil` assignments that would allow GC to reclaim memory.

4. **Fix proposals documented**: Each identified leak has a concrete fix proposal in
   `0031-0002-*.md`, prioritized by estimated heap impact for Q2.

5. **No implementation**: This milestone is **analysis-only**. The output is two design
   documents. Actual fixes are deferred to a follow-up milestone (TBD).

## Reference

- `internal/executor/expr.go:617-662` — `evalSubquery` / `subqueryImpl` (per-row subquery execution)
- `internal/executor/operators_join_agg.go` — Join operators (all algo), drainRows, concatRows
- `internal/executor/operators.go:172-251` — Sort operator
- `internal/executor/operators_window.go` — Window operator
- `internal/executor/operators_recursive_cte.go` — Recursive CTE operator
- `internal/executor/executor.go:21-169` — Build() dispatch
- `internal/planner/planner.go:2379-2394` — planSubqueryExpr (no unnesting)
- `internal/planner/planner.go:610-656` — Join algorithm selection
- Benchmark scripts: `bench/tpch/`
