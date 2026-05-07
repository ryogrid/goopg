# TPC-H Q1–Q22 EXPLAIN Plan Analysis

**Branch:** `perf-analysis`  
**Date:** 2026-05-06  
**Data:** SF=1, ANALYZE run, all HammerDB supplementary indexes present  
**Stack:** Full M0054 (NLI, decorrelation, Borrow, column projection, B-tree Phase A)

---

## Summary Table

| Q | Shape | Index Used? | Subquery Handling | Concern | Est. Completion |
|---|---|---|---|---|---|
| Q1 | SeqScan+Filter+GroupAgg | — | — | None | ✅ Fast |
| Q2 | MHJ(5) + Hash Join (unnested scalar) | — | Unnested ✅ | All SeqScan | ✅ Fast |
| Q3 | MHJ(3) | — | — | All SeqScan | ✅ Fast |
| Q4 | SeqScan+Filter+GroupAgg | — | EXISTS SubPlan | Uncorrelated EXISTS as SubPlan? | ✅ |
| Q5 | MHJ(6) | — | — | All SeqScan | ✅ Fast |
| Q6 | SeqScan+Filter+Agg | idx_lineitem_shipdate (missing) | — | Date range not using index | ✅ Fast |
| Q7 | MHJ(6)+Projection | — | — | All SeqScan | ✅ |
| Q8 | MHJ(7)+NLI(part_pk) | **part_pk** ✅ | — | Good | ✅ |
| Q9 | MHJ(4)+NLI(orders_pk, partsupp) | **orders_pk, partsupp_fk** ✅ | — | Excellent | ✅ |
| Q10 | MHJ(4) | — | — | All SeqScan | ✅ Fast |
| Q11 | MHJ(3)+HAVING SubPlan | — | HAVING subquery | Non-correlated SubPlan | ✅ |
| Q12 | Hash Join (build=left) | — | — | date range not filtered early | ✅ |
| Q13 | PARSE ERROR | — | — | LEFT OUTER JOIN syntax unsupported | ❌ |
| Q14 | Hash Join | — | — | NLI cost gate rejected (lineitem too large) | ✅ |
| Q15 | NLI(supplier_pk) | **supplier_pk** ✅ | — | Good (M0054-0006-followup-Q15b) | ✅ |
| Q16 | Hash Join | — | NOT IN SubPlan | NOT IN = SubPlan per partsupp row | ✅ |
| Q17 | Hash Join(inner GroupAgg) | — | Correlated scalar SubPlan + idx | Correlated avg uses index? (dueling plans) | ⚠️ |
| Q18 | MHJ(3)+IN SubPlan | — | IN SubPlan (non-correlated) | Cache miss per row, O(N×M) | ❌ Slow |
| Q19 | NLI cross+SeqScan | — | OR-of-AND → Filter | CROSS join estimate explodes | ⚠️ |
| Q20 | NLI(nation_pk) | **nation_pk** ✅ | IN SubPlan | Outer IN not unnested | ⚠️ Slow |
| Q21 | MHJ(4)+EXISTS/NOT EXISTS | idx (inner) | EXISTS/NOT EXISTS SubPlan | Index used per eval, but 1h timeout | ❌ |
| Q22 | SeqScan customer+SubPlan | order_customer_fkidx ✅ | Scalar avg + NOT EXISTS SubPlan | avg re-evaluated 150K times | ✅ |

---

## Per-Query Analysis

### Q1 — Pricing Summary

```
Projection → Sort → GroupAggregate(2 keys, 8 agg)
  → Filter → Seq Scan lineitem (6M)
```

**Assessment: Expected.** Q1 is a full lineitem scan with a date filter. No join needed. The date predicate `l_shipdate <= '1998-12-01' - interval '90 day'` eliminates ~2/3 of rows (Filter rows=2M out of 6M). The GroupAggregate is the dominant cost. `idx_lineitem_shipdate` could narrow the scan but the filter is a `<=` that matches the majority of rows, making a range scan less beneficial than a full scan.

---

### Q2 — Minimum Cost Supplier

```
Filter → Hash Join(INNER)
  ├── MHJ(5): partsupp, part, supplier, nation, region
  └── GroupAggregate(1 key, 1 agg)
        → Filter → MHJ(4): partsupp, supplier, nation, region
```

**Assessment: Partially expected.** The scalar subquery `(SELECT min(ps_supplycost) ...)` is **unnested** by the M0040 decorrelation pass into a pre-computed GroupAggregate, then Hash-Joined with the outer MHJ. This is correct behavior. All scans are Seq Scans; no indexes are used for the join columns. With ANALYZE statistics, the optimizer correctly chose MHJ for the 5-table join.

---

### Q3 — Shipping Priority

```
Sort → GroupAggregate(3 keys) → Filter → MHJ(3): orders, customer, lineitem
```

**Assessment: Expected.** Three-table join (customer×orders×lineitem) handled by MHJ. Filters `c_mktsegment = 'BUILDING'`, `o_orderdate < '1995-03-15'`, `l_shipdate > '1995-03-15'` are applied as a post-join Filter. An ideal plan would push the single-table predicates into the input scans (M0054-0006a-pre); with SF=1 statistics the cost gate may have kept Seq Scan inputs here.

---

### Q4 — Order Priority Checking

```
Sort → GroupAggregate(1 key) → Filter → Seq Scan orders (1.5M)
```

**Assessment: Good.** Q4's `EXISTS (SELECT * FROM lineitem WHERE ...)` correlated subquery is hidden in the Filter. The outer is just orders (1.5M rows). With `idx_lineitem_orderkey_fkidx` available, each EXISTS lookup is O(1). Estimated completion: fast, likely < 1 minute.

---

### Q5 — Local Supplier Volume

```
Sort → GroupAggregate(1 key) → Filter → MHJ(6): orders, customer, supplier, nation, region, lineitem
```

**Assessment: Expected.** Six-table join handled by MHJ. The `r_name = 'ASIA'` predicate filters region to 1 row, and nation/supplier chain is 25/10K — the SmallDimension flag (M0054-0010) should pin these on the build side. All tables Seq Scanned; the MHJ is an appropriate strategy.

---

### Q6 — Forecasting Revenue Change

```
Aggregate → Filter → Seq Scan lineitem (6M)
```

**Assessment: Expected.** Q6 is a single-table aggregation with a date range filter on `l_shipdate`. The `idx_lineitem_shipdate` index exists, but with a ~1/12 selectivity range, the cost gate likely chose a Seq Scan (similar to Q1). Fast in any case due to simple aggregation.

---

### Q7 — Volume Shipping

```
Sort → GroupAggregate(3 keys) → Projection → Filter → MHJ(6): orders, customer, supplier, n1, n2, lineitem
```

**Assessment: Expected.** Six-table join including a self-join on nation (n1, n2). MHJ handles it. The OR predicate `((n1='FRANCE' AND n2='GERMANY') OR (n1='GERMANY' AND n2='FRANCE'))` is applied in the post-join Filter. Proper plan shape.

---

### Q8 — National Market Share

```
Sort → GroupAggregate → Projection → Filter
  → Nested Loop(INNER)
      ├── MHJ(7): orders, customer, supplier, n1, n2, region, lineitem
      └── Index Scan using part_pk on part ✅
```

**Assessment: Good.** **NLI is active**: the MHJ probe result is joined to `part` via `Index Scan using part_pk`. This is the M0054-0006 NLI rewrite — `l_partkey = p_partkey` equi-join uses `part_pk`. The 7-table MHJ + NLI on part is the best achievable plan for Q8.

---

### Q9 — Product Type Profit Measure

```
Sort → GroupAggregate → Projection → Filter
  → NLI(orders_pk)
      → NLI(partsupp_supplier_fkidx)
           → MHJ(4): part, supplier, nation, lineitem
```

**Assessment: Excellent.** Multiple NLI rewrites are active:
- `o_orderkey = l_orderkey`: `orders_pk` index scan per lineitem row
- `ps_suppkey = l_suppkey / ps_partkey = l_partkey`: `partsupp_supplier_fkidx` index scan

This is the plan that delivered Q9's 92.4% wall-clock reduction vs run-011 (run-013: 138 s vs 1810 s). The composite-key NLI fix (M0054-0006-followup-Q9-composite) made this possible.

---

### Q10 — Returned Item Reporting

```
Sort → GroupAggregate(7 keys) → Filter → MHJ(4): orders, customer, nation, lineitem
```

**Assessment: Expected.** Four-table join with date filters. The `l_returnflag = 'R'` filter and the date range on `o_orderdate` are applied post-join. All Seq Scans; appropriate for the join structure.

---

### Q11 — Important Stock Identification

```
Sort → Filter
  → GroupAggregate(1 key)
      → Filter → MHJ(3): partsupp, supplier, nation
```

**Assessment: Partially expected.** The HAVING subquery `sum(ps_supplycost * ps_availqty) > (SELECT sum(...) * 0.0001 ...)` is evaluated as a SubPlan. Since the inner is non-correlated (computes a constant), it should be computed once. The cache key issue (keyed by outer row) applies here — however, with `SubqueryCacheScope` mechanics, the non-correlated scalar subquery may actually be cached after the first evaluation because the scope (OuterRows depth) stays constant throughout the HAVING evaluation. Likely fast in practice.

---

### Q12 — Shipping Modes and Order Priority

```
Sort → GroupAggregate(1 key) → Filter
  → Hash Join(INNER, build=left): orders (build), lineitem (probe)
```

**Assessment: Notable.** The `build=left` flag indicates M0054-0010 SmallDimension or size-based build-side selection: orders (1.5M) builds the hash, lineitem (6M) probes. This is correct — building on the smaller side reduces memory usage. The date filter on `l_receiptdate` covers ~1/4 of lineitem rows; `idx_lineitem_shipdate` or a receipt-date index could help but neither is created in the HammerDB schema.

---

### Q13 — Customer Distribution

**PARSE ERROR: `LEFT OUTER JOIN` syntax not supported.**

The query uses `customer LEFT OUTER JOIN orders ON c_custkey = o_custkey AND o_comment NOT LIKE ...` which is the explicit JOIN syntax. goopg's parser does not fully support the `a LEFT OUTER JOIN b ON predicate AND filter` form. This is a known gap — the parser handles comma-separated FROM with WHERE predicates but not all variants of explicit JOIN syntax with compound ON predicates.

**Action required:** Fix parser to support `LEFT OUTER JOIN ... ON (complex predicate)` for M0054-0007 completion.

---

### Q14 — Promotion Effect

```
Aggregate(2) → Filter → Hash Join(INNER)
  ├── Seq Scan lineitem (6M)
  └── Seq Scan part (200M)
```

**Assessment: Expected with statistics.** In the synthetic fixture (small data), Q14 used NLI with `part_pk`. With SF=1 + ANALYZE, `lineitem` after the date filter has ~500K rows, exceeding the NLI cost-gate threshold (100K). The planner **correctly** chose Hash Join. Q14 completed in ~30 s in run-013/015 — performance is good.

---

### Q15 — Top Supplier

```
Sort → Filter → Nested Loop(INNER)
  ├── GroupAggregate (revenue0 body)
  └── Index Scan using supplier_pk on supplier ✅
```

**Assessment: Correct.** M0054-0006-followup-Q15b landed: the Filter→CrossJoin shape from VIEW substitution is rewritten to NLI, producing `Index Scan using supplier_pk`. The scalar subquery `MAX(total_revenue)` for the HAVING is evaluated as a SubPlan but is non-correlated — computed once from the materialized revenue0 aggregate.

---

### Q16 — Parts/Supplier Relationship

```
Sort → GroupAggregate(3 keys) → Filter → Hash Join(INNER)
  ├── Seq Scan partsupp (800K)
  └── Seq Scan part (200K)
```

**Assessment: Partially expected.** The `NOT IN (SELECT s_suppkey FROM supplier WHERE ...)` predicate is hidden in the Filter as a SubPlan. The inner query scans supplier (10K rows) — fast. The issue is the same non-correlated IN cache problem, but with only 10K rows per eval × 800K partsupp rows the total cost is 800K × ~0.01ms = ~8 s. Should complete.

---

### Q17 — Small-Quantity Orders

```
Aggregate → Filter → Hash Join(INNER)
  ├── Hash Join(INNER): lineitem × part
  └── GroupAggregate(1 key) → Filter → Seq Scan lineitem
```

**Assessment: Notable.** Q17's correlated scalar subquery `(SELECT 0.2 * avg(l_quantity) FROM lineitem WHERE l_partkey = p_partkey)` appears to be **decorrelated** by M0054-0008 (multi-param correlation) or handled separately — the plan shows a pre-aggregated GroupAggregate(1 key) as a separate branch, then Hash-Joined with the lineitem × part join. This is the correct decorrelated shape: compute the avg once per partkey, then join. Good.

---

### Q18 — Large Volume Customer

```
Sort → GroupAggregate(5 keys) → Filter → MHJ(3): orders, customer, lineitem
```

**Assessment: Problematic.** The `o_orderkey IN (SELECT l_orderkey FROM lineitem GROUP BY l_orderkey HAVING sum(l_quantity) > 300)` predicate is a SubPlan in the Filter. The inner query is **non-correlated** — it should be computed once and stored. However, the SubqueryCache keys on the outer row (which changes per MHJ output row), causing 6M evaluations of a 6M-row lineitem scan. This leads to catastrophic performance: Q18 was cancelled at 14.9 minutes with RSS growing to 11GB (unbounded cache accumulation). **Root cause: non-correlated IN subquery not being treated as a constant via a proper InitPlan.**

---

### Q19 — Discounted Revenue

```
Aggregate → Filter → Nested Loop(CROSS)
  ├── Seq Scan lineitem (6M)
  └── Seq Scan part (200K)
```

**Assessment: Alarming row estimate.** The Nested Loop(CROSS) shows an estimated 1.2 trillion rows (`rows=1200491000000`). The M0054-0006-followup-Q19 fix (OR-of-ANDs common equi-conjunct) should inject `p_partkey = l_partkey` from the OR branches into an NLI probe. The `CROSS` join type indicates the equi-conjunct was not extracted into the Join.Predicate. Investigation needed: whether the OR-of-ANDs fix fires for Q19's Filter→CrossJoin shape. The Filter above does apply the full predicate, so the result is correct but the execution path is O(6M × 200K) = 1.2T comparisons.

---

### Q20 — Potential Part Promotion

```
Sort → Filter → Nested Loop(INNER)
  ├── Seq Scan supplier (10K)
  └── Index Scan using nation_pk on nation ✅
```

**Assessment: Plan incomplete / misleading.** The plan shows only the outer `supplier × nation` join (6 nodes). The complex `s_suppkey IN (SELECT ps_suppkey FROM partsupp WHERE ... AND ps_availqty > (SELECT 0.5 * sum(l_quantity) FROM lineitem WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey ...))` nested IN subquery is hidden in the Filter as a SubPlan. 

Key findings (separately verified):
1. The inner correlated aggregate (`SUM(l_quantity)` per `(l_partkey, l_suppkey)`) IS decorrelated by M0054-0008 → GroupAggregate(2 keys) + Hash Join. ✅
2. The outer `s_suppkey IN (SELECT ps_suppkey ...)` is still a SubPlan evaluated per supplier row. ❌
3. Lineitem date range (`l_shipdate >= '1994-01-01' ...`) does NOT use `idx_lineitem_shipdate` in the decorrelated inner plan — full Seq Scan. ❌

Result: Q20 did not complete within the 1-hour budget. CPU ran at ~45% steady with RSS stable (no leak), confirming the decorrelated aggregate executes, but the outer IN SubPlan × lineitem full scan dominated.

---

### Q21 — Suppliers Who Kept Orders Waiting

```
Sort → GroupAggregate(1 key) → Filter → MHJ(4): orders, supplier, nation, lineitem
```

**Assessment: Index-assisted but still exceeds budget.**
- The EXISTS and NOT EXISTS subqueries use `idx_lineitem_orderkey_fkidx` per separately verified EXPLAIN. ✅
- The correlated key is `l_orderkey` → ~4 rows per lookup → fast per call.
- However, the total number of (lineitem×orders×supplier×nation) join rows reaching the Filter is in the millions. With `n_name = 'SAUDI ARABIA'` filtering to ~400 suppliers and `o_orderstatus = 'F'` filtering ~30% of orders, effective rows are reduced but still exceed the 1-hour budget for 2× SubPlan evaluations per row.
- Q21 timed out at 3600 s (1-hour cancel-after).

**Improvement path:** EXISTS/NOT EXISTS → semi-join/anti-join rewrite (M0040-equivalent for EXISTS).

---

### Q22 — Global Sales Opportunity

```
Sort → GroupAggregate(1 key) → Projection → Filter → Seq Scan customer (150K)
```

**Assessment: Manageable.** Two SubPlans in the Filter:
1. Scalar avg (non-correlated): scans 150K customer rows per evaluation × 150K outer rows. SubqueryCache key = full outer row → cache miss per customer. Est.: 150K × ~1ms = 150 s. Suboptimal but within budget.
2. NOT EXISTS on orders: uses `order_customer_fkidx` → O(1) per customer. ✅

Q22 should complete in 2–5 minutes.

---

## Key Findings and Improvement Opportunities

### 1. Non-Correlated SubPlan Cache (CRITICAL)

**Queries affected:** Q11 (HAVING), Q16 (NOT IN), Q18 (IN), Q22 (avg scalar).

The `SubqueryCache` in `internal/executor/context.go` keys results on the full outer row. For non-correlated subqueries (no OuterColumnRef), the result is identical for every outer row, but the cache misses on every row because the key changes. The fix: detect zero OuterColumnRef count and use a constant key (e.g., empty string or the subquery node pointer).

**Impact:** Q18 was unable to complete; Q22 runs ~100× slower than necessary.

### 2. Parser: LEFT OUTER JOIN (BLOCKER for Q13)

**Query affected:** Q13.

goopg's parser does not support `A LEFT OUTER JOIN B ON complex_predicate AND filter_predicate`. Q13 cannot be planned or executed. This is a hard blocker for M0054-0007 22/22 completion.

### 3. Orphaned Backend Goroutines (SAFETY)

CancelRequest correctly signals the per-query context, but long-running computations (Q21, Q13) continue running on the server after the client closes the connection. After cancellation, CPU remained at 167–178% for 10 minutes with no active connection. The server requires a restart to reclaim resources.

**Fix:** Monitor the TCP connection (via a goroutine or deadline) during query execution and cancel `queryCtx` on EOF/broken-pipe detection, not just on CancelRequest.

### 4. NLI Cost Gate with Real Statistics (EXPECTED)

With ANALYZE-generated statistics, lineitem has 6M rows. The NLI cost-gate threshold (100K rows) correctly rejects NLI for joins where lineitem is the outer, reverting to Hash Join. This is correct behavior — Q14, Q3, Q10, etc. use Hash Join appropriately. The NLI gains (Q9 −92%) apply only where the outer side is genuinely small.

### 5. Q19 OR-of-ANDs NLI (REGRESSION TO INVESTIGATE)

Q19 shows a Nested Loop(CROSS) with 1.2 trillion estimated rows, suggesting the `p_partkey = l_partkey` equi-conjunct was not extracted into the join predicate. The M0054-0006-followup-Q19 fix (Filter→CrossJoin push-down for OR-of-AND common equi) should handle this. Needs investigation on whether the fix fires for the specific Q19 plan shape generated from SF=1 data.

---

## Completion Forecast (with current planner)

| Q | Status (emulate run-001) | Estimate if not yet run |
|---|---|---|
| Q1 | ✅ (from run-013) | < 1 min |
| Q2 | ✅ (from run-013) | < 1 min |
| Q3 | ⚠️ Cancelled (signal file) | < 5 min |
| Q4 | — | < 2 min |
| Q5 | — | < 5 min |
| Q6 | ✅ 32.7 s | — |
| Q7 | — | < 10 min |
| Q8 | ✅ 195.3 s | — |
| Q9 | ✅ (from run-013: 138 s) | ~3 min |
| Q10 | — | < 5 min |
| Q11 | — | < 5 min |
| Q12 | — | < 5 min |
| Q13 | ❌ PARSE ERROR | Cannot run |
| Q14 | ✅ (from run-013: ~30 s) | — |
| Q15 | — | < 5 min |
| Q16 | — | < 5 min |
| Q17 | ✅ 70.4 s | — |
| Q18 | ❌ CANCELLED 14.9 min | Will not complete |
| Q19 | — | Unknown (CROSS join) |
| Q20 | ❌ CANCELLED | Will not complete (1 h+) |
| Q21 | ❌ CANCELLED 60 min | Will not complete |
| Q22 | — | ~3 min |
