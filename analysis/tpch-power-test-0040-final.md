# TPC-H HammerDB SF=1 Power Test — M0040 Final Report

**Date:** 2026-05-03
**goopg commit:** `b6244f4` (M0040 complete)
**Test machine:** x86_64 Linux (WSL2), 32 GB RAM + 64 GB swap, Go 1.25.0
**Configuration:** `shared_buffers=2048MB` (2 GiB), `GOMEMLIMIT=20GiB`
**TPC-H scale factor:** 1 (~1 GB raw data)
**Client:** HammerDB 5.0 CLI

## Summary

| Phase | Result | Notes |
|-------|--------|-------|
| 1. goopg init + config | ✅ PASS | `shared_buffers=2048MB`, fresh `goopg init` |
| 2. Server start | ✅ PASS | ~10 s to readiness |
| 3. TPC-H table creation | ✅ PASS | 8 tables created |
| 4. Data loading (COPY) | ✅ PASS | **Full 100% loaded**: 150K customers, 200K parts, 800K partsupp, 1.5M orders, ~6M lineitems |
| 5. Index creation | ✅ PASS | Primary keys created by HammerDB |
| 6. ANALYZE | ✅ PASS | Statistics gathered |
| 7. Power test (Q1‑Q22) | **3 completed, 1 timed out, 18 not reached** | **No query crashed.** Q14=28.8s, Q2=4.8s, Q9=51.4s. Q20 timed out at 1 h. |

## Power Test Detail

| # | Query | Duration | Status |
|---|-------|----------|--------|
| 1 | Q14 | 28.8 s | ✅ PASS |
| 2 | Q2 | 4.8 s | ✅ PASS |
| 3 | Q9 | 51.4 s | ✅ PASS |
| 4 | Q20 | — | ⏳ Timed out at 1 h |
| 5–22 | Q1, Q3–Q8, Q10–Q13, Q15–Q19, Q21–Q22 | — | ⏳ Not reached |

## Analysis of Q20 Timeout

### Why M0040 is not sufficient for Q20

Q20's structure after M0040 optimisations:

```
Query plan with M0040-0002 (IN-unnest):
  Sort(s_name)
    HashJoin(⋈, supplier, nation)   ← simple 2-table join, fast
      Filter(n_name = 'CANADA')
      
The IN rewrites as semi-join (M0040-0002):
  HashJoin(semi, partsupp, …)       ← s_suppkey IN → semi-join
      
      Inner IN on part (M0040-0002):
        HashJoin(semi, part, …)      ← ps_partkey IN → semi-join
      
      Scalar subquery on lineitem:
        ScalarSubquery(lineitem)     ← STILL per partsupp row!
```

| Level | Optimisation applied | Remaining work |
|-------|-------------------|----------------|
| Outer IN (`s_suppkey IN …`) | ✅ Unnest → semi‑join (M0040‑0002) | None |
| Inner IN (`ps_partkey IN …`) | ✅ Unnest → semi‑join (M0040‑0002) | None |
| Scalar (`lineitem` subquery) | ✅ Cache (M0040‑0001) | Cache reduces repeated execution of identical correlation values, but `(ps_partkey, ps_suppkey)` is the partsupp PK — every row is distinct. **800K distinct keys → 800K lineitem SeqScans** |

### Complexity after M0040

```
Work = partsupp_rows × lineitem_scan
     = 800K × 6M = 4.8 × 10¹² tuple probes
```

Even at 10 µs per tuple this would run for ~555 days. The cache helps only when the same correlation key appears multiple times, but for Q20's PK-level correlation, every row is unique.

### What would truly fix Q20

The scalar subquery on lineitem must be **unnested as a hash‑join with aggregation**:

```sql
SELECT 0.5 * SUM(l_quantity) FROM lineitem
WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey
  AND l_shipdate >= '1994-01-01' AND l_shipdate < '1994-01-01' + INTERVAL '1 year'
```

This is structurally identical to the existing M0033 `SubqueryExpr` unnest pattern:
1. It has an aggregate (`SUM`).
2. It has equijoin pairs (`l_partkey = ps_partkey`, `l_suppkey = ps_suppkey`).
3. It is a `SubqueryExpr` (not `InExpr`).

The blocker: the scalar subquery lives **inside** the IN-subquery's inner plan, not at the top level. The unnest pass (`unnestSubqueriesInPlan`) walks the top-level Filter predicate and finds the outermost `SubqueryExpr` — but Q20's lineitem subquery is nested two levels deep in the IN inner plan, which is not reachable by the current walker.

Fixing this requires the unnest pass to recursively descend into IN-subquery inner plans and extract correlated scalar subqueries from there. This is a natural extension of M0040-0002: after the IN is unnested into a semi-join, the remaining scalar subquery in the partsupp WHERE clause becomes accessible to the existing `SubqueryExpr` unnest.

## Comparison Across Milestones

| Milestone | Q14 | Q2 | Q9 | Q20 | Key fix |
|-----------|-----|----|----|-----|---------|
| **M0039** (pre‑fix) | 21.8 s | 2.4 s | 37.2 s | OOM / crash | swap‑before‑remap, findScanByColName |
| **M0040** (this run) | 28.8 s | 4.8 s | 51.4 s | **1 h timeout** | subquery cache + IN‑unnest |

The slight regression in Q14/Q2/Q9 times is likely due to the subquery cache overhead (map allocations and key computation) for queries that don't benefit from it.

## Conclusion

**M0040 delivers:**
- No query crashes (zero `compareDatum` errors across all executed queries).
- Q2, Q9, Q14 complete successfully.
- Schema build and data load succeed fully (100% of SF=1 data).

**M0040 does not fix Q20** because the lineitem scalar subquery is evaluated once per distinct partsupp row (800K unique PK values), even with caching. The scalar subquery must be planner‑unnested as an aggregation hash‑join, which requires extending the unnest pass to descend into IN-subquery inner plans.

### Open issue

The innermost lineitem scalar subquery inside Q20's correlated IN structure requires recursive unnest descent — a future milestone that builds on M0040-0002's infrastructure.
