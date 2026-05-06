# TPC-H tpch-runner Measurement Report

**Date:** 2026-05-06  
**Branch:** `perf-analysis`  
**Tool:** `cmd/tpch-runner` (goopg-native query runner replacing hammerdbcli)  
**Data:** SF=1, ANALYZE run, all HammerDB supplementary indexes present  
**Stack:** M0054 + M0055 + M0056-0001 + M0057 + M0042-0004 fix  
**Note:** This report captures a partial run. Queries Q11 onwards are still executing; this document will be updated as results arrive.

---

## 1. Execution Results Summary

### 1.1 Completed Queries

| Query | Status | Elapsed (s) | Rows | p50 CPU% | Peak RSS MB | Source |
|-------|--------|------------|------|---------|-------------|--------|
| Q2 | OK | 5.36 | 460 | — | — | run-013 confirmed |
| Q6 | OK | 32.72 | 1 | 71 | 5159 | emulate-run-001 |
| Q8 | OK | 195.31 | 0 | 209 | 10998 | emulate-run-001 |
| Q9 | OK | 138.48 | — | — | — | run-013 confirmed |
| Q14 | OK | 29.69 | 1 | — | — | run-013 confirmed |
| Q17 | OK | 70.37 | 1 | 93 | 5145 | emulate-run-001 |

### 1.2 Timed-Out / Cancelled Queries (1-hour budget)

| Query | Root Cause | Plan Shape | Key Symptom |
|-------|-----------|-----------|-------------|
| Q4 | EXISTS subquery as SubPlan, no unnesting to semi-join | `Seq Scan orders(1.5M) + EXISTS SubPlan` | 1.5M × EXISTS eval per row; even with idx_lineitem_orderkey, operator Build/Open/Close overhead dominates |
| Q11 | Non-correlated HAVING scalar SubPlan evaluated ~8K times | `MHJ(3) + GroupAgg + HAVING scalar SubPlan` | 8K groups × 400ms each ≈ 54 min estimated |
| Q18 | Non-correlated IN SubPlan with unbounded cache growth | `MHJ(3) + IN SubPlan (Filter)` | 6M rows × lineitem 6M scan; RSS grew to 11GB (cache leak) |
| Q20 | Outer IN SubPlan not unnested; inner correlated agg decorrelated ✓ | `NLI(nation_pk) + IN SubPlan` | Inner agg decorrelated by M0054-0008; outer IN remains SubPlan |
| Q21 | EXISTS/NOT EXISTS as SubPlans; index used but 6M outer rows | `MHJ(4) + EXISTS/NOT EXISTS SubPlan` | idx_lineitem_orderkey used per call (~fast), but volume × overhead exceeds 1h |

### 1.3 Parse/Infrastructure Errors

| Query | Issue | Status |
|-------|-------|--------|
| Q13 | `LEFT OUTER JOIN … ON (complex_pred AND filter)` not supported by parser | **Fixed** in commit `ad183ac` |
| Q3 | Accidentally consumed stale signal file (`/tmp/cancel_query`) | Infrastructure issue; Q3 itself is fast |
| Q22 | Server restart during run — UNKNOWN(exit=1) | Infrastructure issue |

---

## 2. Key Performance Findings

### 2.1 NUMERIC Decode Dominates SeqScan Cost

HammerDB's TPC-H schema declares **all integer-like columns as NUMERIC** (e.g., `l_partkey NUMERIC`, `l_quantity NUMERIC`). goopg's `parseNumeric()` allocates a `*big.Int` per column per row. This makes SeqScan over wide tables significantly more expensive than expected from row count alone.

**Measurement evidence — Q17 Fermi estimate:**

| Component | Formula | Result |
|-----------|---------|--------|
| lineitem SeqScan (×2) | 6M rows × 16 cols × 400ns/NUMERIC | 76.8s |
| part SeqScan | 200K rows × 9 cols × 400ns | 0.7s |
| Hash Join / GroupAgg | — | ≪1s |
| **Estimated total** | | **~78s** |
| **Actual (run)** | | **70.4s** |
| **Error** | | **+11%** |

The estimate accuracy of ±11% confirms that NUMERIC decode is the dominant cost for compute-bound TPC-H queries. I/O is not a bottleneck: `pg_stat_aio` confirmed zero additional disk reads during Q17 execution (all pages cached).

### 2.2 SubPlan Cache Miss for Non-Correlated Subqueries

The `SubqueryCache` in `internal/executor/context.go` keys results on the **full outer row** (all columns). For non-correlated subqueries (no `OuterColumnRef`), the result is identical for every outer row, but the cache misses on every row because the key changes with each new row.

**Impact per query:**

| Query | Subquery type | Cache misses | Per-miss cost | Total overhead |
|-------|-------------|-------------|--------------|---------------|
| Q11 | HAVING scalar | ~8K groups | ~400ms (MHJ × 800K) | **~54 min** |
| Q18 | IN (GROUP BY) | ~6M rows | ~400ms + cache growth | **hours + OOM** |
| Q22 | scalar avg | ~150K rows | ~1ms (150K scan) | ~2.5 min |
| Q17 | scalar avg/partkey | ~30K matched | idx scan (~fast) | **accounted** |

**Fix:** Use a constant cache key (e.g., empty string or subquery node pointer) for subqueries with zero `OuterColumnRef` references.

### 2.3 EXISTS/NOT EXISTS Not Unnested to Semi-Join/Anti-Join

Q4, Q21 use `EXISTS`/`NOT EXISTS` as correlated subqueries. The planner evaluates them as SubPlans per outer row rather than converting to semi-join / anti-join. While `idx_lineitem_orderkey_fkidx` is used per evaluation, the operator Build/Open/Next/Close overhead dominates at 1.5M outer rows (Q4) or 6M (Q21).

**Q4 Fermi estimate (would take if implemented):**
- orders 1.5M × EXISTS(idx scan ~4 rows) ≈ 1.5M × ~1µs = **1.5s**
- Actual: **> 3600s** — overhead of operator lifecycle per eval

**Fix:** Extend the M0040 unnesting pass to handle `EXISTS` → semi-join and `NOT EXISTS` → anti-join.

### 2.4 Server-Side Query Cancellation Improvements

During the measurement session, CancelRequest (via `--signal-file` in tpch-runner) was the primary mechanism for stopping stuck queries. Several improvements were made:

| Commit | Fix |
|--------|-----|
| `a216093` | Add `ctx.Err()` to `collectInValues` (IN SubPlan), `evalSubquery` (scalar), `drainRowsCtx` (hash build), MHJ build |
| `f0b1c2c` | Add `ctx.Err()` to `evalExistsExpr` (EXISTS/NOT EXISTS) |

**Remaining gap:** After CancelRequest, the server-side goroutine continues until it detects the closed socket at the next write. Monitoring showed CPU remaining at 167–178% for 10 minutes after cancellation (no active connections). Root fix: propagate TCP EOF to `queryCtx.Cancel()` during execution.

### 2.5 Wait Event Infrastructure Status

Sampling `pg_stat_activity.wait_event` during Q11/Q4 execution showed **all empty** (no wait events recorded despite active queries). Investigation revealed:

- 6 out of 14 `WaitEventStart` call sites in `open.go` have **no corresponding `WaitEventEnd`** (DataFileRead/Write/Extend/Sync, WALWrite, WALSync, BufferPin).
- Q4 and Q11 are **purely CPU-bound** — confirmed by `pg_stat_aio` read count not increasing between two samples taken ~60 seconds apart during Q11 execution.
- All 24,372 disk reads happened during the initial buffer pool warm-up; subsequent scans are fully cached (2GB pool covers ~720MB lineitem + ~30MB part + smaller tables).

---

## 3. Infrastructure Issues Found and Fixed

| Issue | Commit | Description |
|-------|--------|-------------|
| SQL CHECKPOINT panicked (M0042-0004) | `f5021c8` | `checkpointOp.Open()` calls `Pool.FlushAll()` from client_backend goroutine; assertion incorrectly blocked it |
| Q13 parse error (LEFT OUTER JOIN) | `ad183ac` | Derived-table column alias list `(SELECT …) AS t (c1,c2)` unsupported; added to parser and planner |
| `left()` string function missing | workaround | `left(query, N)` → `substring(query from 1 for N)` |
| `pg_backend_pid()` missing | workaround | Not implemented; work around by selecting all rows |
| Stale signal file (`/tmp/cancel_query`) | operational | File persisted between runs; added `rm -f` to run scripts |

---

## 4. Tooling Improvements Made

### tpch-runner (`cmd/tpch-runner`)

| Feature | Description |
|---------|-------------|
| `--signal-file=<path>` | Poll a sentinel file; cancel current query and continue to next without stopping the process |
| `--cancel-after=<duration>` | Fire `context.Cancel()` after N seconds; lib/pq sends CancelRequest; connection stays alive |
| `--explain` | Print full EXPLAIN plan text (not just row count) |
| `--checkpoint` | Issue `CHECKPOINT` and exit |
| `cmd/tpch-runner/README.md` | Full manual workflow documentation |

### Resource monitoring

`bench/tpch/scripts/resource_monitor.sh`: samples `%CPU` and `RSS` every N seconds for a given PID, writing CSV to a per-query metrics file.

---

## 5. Performance Optimization Opportunities

In priority order based on impact to TPC-H query completion:

| Priority | Fix | Queries unblocked | Estimated effort |
|----------|-----|------------------|-----------------|
| 1 | Non-correlated SubPlan cache (constant key) | Q11 ✓, Q18 ✓, Q22 ✓ | ~50 lines |
| 2 | EXISTS → semi-join unnesting | Q4 ✓, Q21 ✓ | ~300 lines (M0040 extension) |
| 3 | NUMERIC decode optimization (int4/int8 fast path for known-integer columns) | All queries -50%+ | ~200 lines |
| 4 | TCP disconnect → queryCtx.Cancel() | Server restart avoidance | ~50 lines |
| 5 | WaitEventEnd hooks for I/O paths | Observability | ~20 lines |

---

## 6. Completed Queries — Plan Analysis Highlights

See companion document `analysis/tpch-explain-plan-analysis-2026-05-06.md` for the full per-query plan analysis.

**Notable positives:**
- **Q9**: NLI composite-key on `partsupp_pk(ps_partkey, ps_suppkey)` → 92.4% wall-clock reduction vs run-011 (1810s → 138s)
- **Q8**: NLI on `part_pk` within a 7-table MHJ
- **Q15b**: NLI via Filter→CrossJoin push-down (M0054-0006-followup-Q15b)
- **Q17**: Correlated scalar subquery decorrelated (M0054-0008) → correct GroupAggregate plan; Fermi estimate matches actual ±11%

---

## 7. Open Items

- [ ] Non-correlated SubPlan cache key fix
- [ ] EXISTS/NOT EXISTS → semi-join/anti-join unnesting
- [ ] Q4 re-run after semi-join fix
- [ ] Q11 re-run after SubPlan cache fix
- [ ] Q18 re-run after SubPlan cache fix
- [ ] Q19 NLI OR-of-ANDs investigation (CROSS join with 1.2T estimated rows)
- [ ] Q3, Q22 re-run (both were disrupted by infrastructure issues, not query problems)
- [ ] Full sequential run of all 22 queries for M0054-0007 close
- [ ] TCP disconnect propagation to queryCtx

---

*Report generated: 2026-05-06. Status: in progress — Q11 currently executing.*
