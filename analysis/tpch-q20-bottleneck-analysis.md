# TPC-H Q20 Bottleneck Analysis

**Date:** 2026-05-03
**goopg commit:** `63dbdf1`

## 1. The Query

```sql
select s_name, s_address from supplier, nation
where s_suppkey in (
    select ps_suppkey from partsupp
    where ps_partkey in (
        select p_partkey from part where p_name like 'forest%'
    )
    and ps_availqty > (
        select 0.5 * sum(l_quantity) from lineitem
        where l_partkey = ps_partkey
        and l_suppkey = ps_suppkey
        and l_shipdate >= date '1994-01-01'
        and l_shipdate < date '1994-01-01' + interval '1 year'
    )
)
and s_nationkey = n_nationkey and n_name = 'CANADA'
order by s_name
```

Q20 has three correlated subqueries nested in two levels:

| Level | Expression type | Correlated refs | Inner table(s) |
|-------|----------------|-----------------|----------------|
| 1 (outer) | `s_suppkey IN (…)` | `s_suppkey` | partsupp |
| 2 (middle) | `ps_partkey IN (…)` | `ps_partkey` | part |
| 2 (middle) | `ps_availqty > (scalar)` | `ps_partkey`, `ps_suppkey` | lineitem |

Scaled to TPC‑H SF=1 (partial data loaded for this session):

| Table | Approx. rows loaded | Full SF=1 rows |
|-------|-------------------|-----------------|
| supplier | 10,000 | 10,000 |
| nation | 25 | 25 |
| partsupp | 800,000 | 800,000 |
| part | 200,000 | 200,000 |
| lineitem | **4,423,244** | 6,000,000 |

## 2. How goopg v0 Executes Correlated Subqueries

### 2.1 Per‑outer‑row re‑execution (no caching)

Every correlated subquery is re‑executed from scratch **for each outer row**.
The executor mechanism (`executor/expr.go`):

```go
// collectInValues — drains the inner plan for EVERY outer row
func collectInValues(x *planner.InExpr, row Row, ctx *Context) ([]Datum, error) {
    ctx.OuterRows = append(ctx.OuterRows, row)     // push outer row
    defer func() { ctx.OuterRows = ctx.OuterRows[:len(ctx.OuterRows)-1] }()
    op, err := Build(x.Plan)                        // ← RE-BUILD per call
    op.Open(ctx)                                    // ← RE-SCAN from block 0
    for { r, _ := op.Next(); drain all rows }
    op.Close()
}
```

The same pattern applies to `evalSubquery` (lines 720‑755). No materialisation,
no caching — pure nested‑loop re‑execution.

### 2.2 SeqScan restarts from block 0 on every Open

The `seqScanOp.Open()` (`operators_storage.go:60`) initialises `curBlock = 0`,
and `Next()` walks sequentially from there.  Each `Open()` call therefore
triggers a full table scan of the inner relation.

### 2.3 The subquery unnest pass does not handle `IN (subquery)`

The unnest pass in `unnest.go` only looks for `*planner.SubqueryExpr` inside
top‑level `Filter` predicates (line 49). It does **not** recognise
`*planner.InExpr` (`column IN (subquery)`) — those are not even visited by
`findSubqueryInExpr`.  None of Q20's three subqueries are ever considered
for unnesting.

## 3. Complexity Analysis

Let `O` be the number of outer rows and `I_k` the rows in inner relation *k*.

The naive per‑row re‑execution multiplies:

```
work = O(outer) × [ O(partsupp) × ( O(part) + O(lineitem) ) ]
```

Concretely on the SF=1 partial dataset:

| Layer | Rows evaluated | Inner work per row | Total scanned |
|-------|---------------|-------------------|---------------|
| Outer (supplier ⋈ nation) | ~1–2 (CANADA filter) | Full IN‑subquery evaluation | — |
| Middle IN (partsupp) | 800,000 → but filtered by part IN & scalar | Scan part (200 K) + Scan lineitem (4.4 M) | **~3.7 × 10¹²** tuple probes |
| Inner IN (part) | 1 per partsupp row × whatever passes ps_partkey | Scan part only → 200 K | **~1.6 × 10¹¹** tuple probes |
| Scalar (lineitem) | 1 per partsupp row that passed part filter | Scan lineitem (4.4 M) | **~3.5 × 10¹²** tuple probes (dominant) |

The **dominant term** is the scalar `lineitem` subquery: **800,000 partsupp rows
× 4.4 million lineitem rows = ~3.5 trillion tuple‑visibility checks** under the
SeqScan / MVCC path.  Even at 1 µs/tuple this would run for ~41 days — far
beyond the 1‑hour test‑timeout.

The actual observed partial‑run reached Q20 and did not complete within the
1‑hour timeout, which is consistent with this analysis.

## 4. Why the Planner Cannot Help (Today)

| Planner component | Can it reduce Q20 work? | Reason |
|-------------------|------------------------|--------|
| Bushy DP (`bushy.go`) | **No** — never triggered | Needs ≥3 tables in a single FROM‑clause scope; Q20 has only 2 per scope |
| MultiHashJoin chain (`bushy.go:520`) | **No** — never triggered | Same reason — scans a binary‑join tree for ≥3 hash‑joined tables |
| Subquery unnesting (`unnest.go`) | **No** — not reached | Only processes `SubqueryExpr`, not `InExpr`; does not descend into subquery inner plans |

All Q20 subqueries execute as fully‑correlated, per‑row, nested‑loop evaluations
by the **executor**, with the planner providing no rewrite.

## 5. Enhancement Points

### 5.1 Level‑1: Unnest `IN (subquery)` expressions (high impact)

**What:** Extend `unnest.go` to recognise `*planner.InExpr` and rewrite it as a
semi‑join (`HashJoin` + dedup).  Goopg v0 already has the building blocks:

- `clonePlanReplacingOuter` turns `OuterColumnRef` → `ColumnRef`
- The hash join operator supports `JoinTypeSemi`
- `drainRows` already materialises build‑side rows

**Estimated effect on Q20:** Converts the outermost `s_suppkey IN (…)` into a
single `HashJoin(supplier⋈nation, partsupp…)`, eliminating the **O(outer_rows)**
factor for that level.

**Estimated effort:** Medium — requires extending `findSubqueryInExpr` to visit
`InExpr`, adding `isInExprUnnestable`, and wiring a semi‑join construction
similar to the existing `SubqueryExpr` → `Join(Aggregate, …)` path.

### 5.2 Level‑2: Materialise correlated subquery results once (high impact)

**What:** Cache the drained result of a correlated subquery keyed on the outer
column values.  The `collectInValues` / `evalSubquery` path could check a
`map[dkey][]Datum` (for `InExpr`) or `map[dkey]Datum` (for scalar) cache before
re‑opening the inner plan.

**Estimated effect on Q20:**  If the same `ps_partkey` value appears in multiple
partsupp rows, the part subquery would be evaluated only once per distinct value.

**Estimated effort:** Low — a `sync.Map` or similar added to `collectInValues`
and `evalSubquery`, keyed by `datumKey(outerRefValue)`.

### 5.3 Level‑3: IndexScan for non‑PK columns (medium impact, large effort)

**What:** Implement `IndexScan` for secondary (non‑primary‑key) indexes.  TPC‑H
Q20's `lineitem` scan filters on `l_shipdate` and correlates on `l_partkey`,
`l_suppkey`.  A composite index on `(l_partkey, l_suppkey, l_shipdate)` would
turn the 4.4M‑row SeqScan into a targeted index lookup.

**Estimated effect on Q20:**  Converts the dominant scalar subquery from
`O(lineitem_rows)` to `O(matching_lineitem_rows)` — typically ≤100 rows per
(l_partkey, l_suppkey) pair.

**Estimated effort:** Large — requires extending the catalog/index infrastructure,
adding an `IndexScan` plan node and operator, and integrating B‑tree index
descending into the access path.

### 5.4 Level‑4: Hash‑join the subquery's base tables in the planner (high impact, large effort)

**What:** Rewrite the planner so that correlated joins inside `IN` subqueries are
extracted into explicit joins in the outer plan.  For Q20 this would mean:

```
HashJoin(HashJoin(HashJoin(supplier, partsupp ON s_suppkey=ps_suppkey),
                   part ON ps_partkey=p_partkey),
         Aggregate(lineitem GROUP BY l_partkey, l_suppkey))
```

This is essentially the same transformation as the existing unnest pass but
generalised to correlated joins (not just scalar aggregates with GROUP BY).

**Estimated effort:** Large — requires significant planner extension.

## 6. Recommendation

The single highest‑impact fix for Q20 is **5.2 (materialise subquery results)**.
With the current 800K partsupp rows and a 4.4M lineitem table, caching the
lineitem aggregate by `(l_partkey, l_suppkey)` would reduce the dominant work
from `O(800K × 4.4M)` to `O(4.4M)` — a 5‑order‑of‑magnitude reduction.

This can be implemented purely in the executor (no planner changes) and applies
to **all** correlated subqueries, not just TPC‑H Q20.
