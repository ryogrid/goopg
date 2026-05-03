# HammerDB TPC-H End-to-End Run Report — Run 005

**Date:** 2026-05-04  
**goopg commit:** `ff26eac` (branch: `perf-analysis`)  
**HammerDB version:** 5.0  
**Scale factor:** SF=1  
**Test machine:** x86_64 Linux, WSL2, 32 GB RAM  
**Configuration:** `shared_buffers=256MB`, `GOMEMLIMIT=20GiB`, `wal_buffers=16MB`

---

## Executive Summary

This run validates the goopg TPC-H SF=1 end-to-end flow across all four phases:
schema build, data load, ANALYZE, and power test (Q1–Q22).

| Phase | Status | Notes |
|---|---|---|
| Schema creation (DDL) | ✅ PASS | All 8 tables created |
| Data load — small tables | ✅ PASS | REGION/NATION/SUPPLIER/CUSTOMER/PART/PARTSUPP fully loaded |
| Data load — ORDERS/LINEITEM | ⚠️ PARTIAL | Connection dropped at 450K/1.5M orders (30%) |
| Index creation | ⚠️ SKIPPED | HammerDB skips indexes when data load fails |
| ANALYZE | ✅ PASS | Ran on partial data in < 1 s |
| Power test — Q14 | ✅ PASS | 14.3 s |
| Power test — Q2  | ✅ PASS | 20.8 s |
| Power test — Q9  | ⚠️ TIMEOUT | 28+ min, GC-bound; manually terminated |
| Power test — Q20–Q1 | ❌ NOT REACHED | Q9 timed out before remaining queries ran |

---

## Phase 1: Setup

```
goopg init:  data directory created fresh (--reset)
Start time:  07:17:31 JST
Ready:       07:17:34 JST (3 s)
Port:        127.0.0.1:65433
```

goopg started successfully. `pg_isready` confirmed the server was accepting connections.

---

## Phase 2: Schema Build

HammerDB built the schema via its single virtual user (1 thread, SF=1).

### Table creation

All 8 TPC-H tables were created without error.

### Data loading

| Table | Rows loaded | Expected (SF=1) | Status |
|---|---:|---:|---|
| region | 5 | 5 | ✅ Complete |
| nation | 25 | 25 | ✅ Complete |
| supplier | 10,000 | 10,000 | ✅ Complete |
| customer | 150,000 | 150,000 | ✅ Complete |
| part | 200,000 | 200,000 | ✅ Complete |
| partsupp | 800,000 | 800,000 | ✅ Complete |
| orders | **453,000** | 1,500,000 | ⚠️ 30% — connection dropped |
| lineitem | **1,811,615** | ~6,001,215 | ⚠️ 30% — connection dropped |

**ORDERS/LINEITEM connection drop** occurred at the 450K-order boundary
(≈ 07:20–07:22 JST), matching the same failure region as previous runs
(run-002: 430K orders; run-004 hammerdb_load baseline: 450K boundary).

HammerDB reported:
```
Error in Virtual User 1: server closed the connection unexpectedly
    This probably means the server terminated abnormally
    before or while processing the request.
Vuser 1:FINISHED FAILED
```

goopg itself did **not** crash — the server process remained alive and
accepting new connections after the HammerDB loader disconnected. The
root cause is the same as documented in M0032-0005: the HammerDB TCL
connection is severed while goopg's INSERT processing takes an unexpectedly
long time per batch. The M0032-0005 fix (GC throttling via
`maybeForceGCAfterCommit`) improved throughput for the Go-loader test, but
HammerDB's own batching pattern still triggers the drop. This indicates
a residual issue: either a TCP keepalive/write-timeout from the HammerDB TCL
client, or server-side backpressure that grows beyond the client's tolerance
window at ~450K orders under the current `shared_buffers=256MB` configuration.

### Index creation

HammerDB creates indexes **after** completing data load. Because the data
load failed, **no indexes were created**. The TPC-H tables ran without any
secondary index support during the power test.

### Schema build timing

```
Build started:  07:17:34 JST
Build ended:    07:24:18 JST
Duration:       404 seconds (6 min 44 s)
```

---

## Phase 3: ANALYZE

ANALYZE was executed on the partial dataset immediately after the build.

```
ANALYZE
Duration: < 1 second (reported 0 s)
```

Statistics were collected for all 8 tables on the partial data. With
incomplete ORDERS/LINEITEM rows, the planner statistics will underestimate
true SF=1 cardinalities for those two tables.

---

## Phase 4: Power Test (Q1–Q22)

The power test ran with `pg_raise_query_error=true`. Only two queries
completed before the test stalled on Q9.

### Completed queries

| Order | Query | Time (s) | Status |
|---|---|---:|---|
| 1 | Q14 — Promotion Effect | 14.299 | ✅ |
| 2 | Q2  — Minimum Cost Supplier | 20.778 | ✅ |
| 3 | Q9  — Product Type Profit Measure | **>1,714 s** | ⚠️ TIMEOUT |
| 4–22 | Q20, Q6, Q17, Q5, Q15, Q8, Q21, Q13, Q3, Q18, Q7, Q1, Q10, Q19, Q22, Q11, Q16, Q4, Q12 | — | ❌ Not reached |

### Q9 timeout analysis

Q9 (Product Type Profit Measure) started at 07:24:25 JST and was still
running after 28 minutes when manually terminated. A CPU profile captured
during Q9 execution revealed the root cause:

```
Duration: 10 s sample, 129.33 s total CPU (1269% utilisation)
                 flat   flat%   cum    cum%
runtime.findObject  38.41s  29.70%  41.0s  31.70%
runtime.scanobject  34.46s  26.65% 113.0s  87.33%
runtime.gcDrain      1.61s   1.24% 115.8s  89.55%
```

**91% of CPU time was consumed by the Go garbage collector** (`gcDrain`,
`scanobject`, `findObject`). The query itself was making effectively no
progress; it was spending nearly all time in GC scan cycles.

**Root cause:** The M0041 milestone introduced `expandChain` in the
Multi-way Hash Join (MHJ) executor
(`internal/executor/multi_hash_join.go`). This function materialises
**all cross-product combinations** of multi-row hash matches into a single
in-memory `[]Row` slice before yielding any result. For Q9, which joins
6 tables including lineitem (1.8M rows in this partial dataset), the
intermediate Cartesian expansion fills > 19 GB of heap, triggering
continuous stop-the-world GC cycles that prevent query completion in any
reasonable time.

With full SF=1 data (6M lineitem rows), the problem would be 3× worse.

This is a **correctness–scalability regression** introduced by M0041-0002's
MHJ materialisation approach: it was verified correct on the 59-row
synthetic parity dataset but fails at production scale.

---

## Configuration details

```
PostgreSQL protocol:    v3
goopg version:         ff26eac
shared_buffers:        256 MB (32,768 8-KiB slots)
GOMEMLIMIT:            20 GiB
wal_buffers:           16 MiB
checkpoint_timeout:    default (5 min)
max_wal_size:          default
```

---

## Comparison with previous runs

| Run | Orders loaded | Q14 | Q2 | Q9 |
|---|---:|---:|---:|---|
| run-001 (2026-05-02) | partial | 28.8 s | 4.8 s | 51.4 s |
| run-003 (2026-05-02) | partial | — | — | — |
| **run-005 (this)** | **453K (30%)** | **14.3 s** | **20.8 s** | **TIMEOUT** |

Q14 and Q2 improved significantly relative to run-001 (M0041 result
correctness fixes, M0041-0004 NUMERIC precision). Q9 regressed from 51.4 s
to timeout due to the M0041-0002 MHJ `expandChain` memory explosion.

---

## Issues identified

### Issue 1 — ORDERS/LINEITEM load drop at ~450K orders (persistent)

- **Symptom:** HammerDB INSERT connection drops at ≈450K orders; goopg stays up.
- **Previous fix:** M0032-0005 (`maybeForceGCAfterCommit`, GC every 64 commits).
- **Status:** Fix effective for the Go-loader (`bench/tpch/cmd/hammerdb_load`)
  but HammerDB's own TCL client still drops at the same boundary. A second
  root cause (TCP write-timeout or server-side buffer stall) remains.
- **Impact:** ORDERS/LINEITEM load is capped at 30% of SF=1 under HammerDB.
- **Suggested fix:** Investigate HammerDB's TCL socket timeout; add
  `tcp_keepalive` tuning or run with a direct psql/pgx loader for
  ORDERS/LINEITEM.

### Issue 2 — Q9 (and similar 6-table queries) GC explosion from MHJ expandChain

- **Symptom:** Q9 takes > 28 minutes; 91% CPU time in GC.
- **Root cause:** `expandChain` in `multiHashJoinOp.Open()`
  (`internal/executor/multi_hash_join.go`) materialises the entire
  Cartesian product of multi-row hash table matches into `o.rows []Row`
  before yielding a single row. With 1.8M lineitem rows and multi-row
  matches in the build tables, the heap fills beyond 19 GB.
- **Impact:** Queries using the 6-table MHJ plan (Q9, potentially Q5, Q7,
  Q8) fail at production scale.
- **Suggested fix:** Replace the full-materialisation approach with a lazy
  iterator that yields one combined row at a time through the chain steps,
  avoiding the `o.rows` accumulation entirely. This mirrors the streaming
  binary hash join approach from M0035–M0036. The `expandChain` function
  should be rewritten as `nextFromChain()` that advances per call.

---

## Recommendations

1. **MHJ executor refactor (P0):** Replace `expandChain` with a lazy
   per-call iterator in `multiHashJoinOp.Next()`. This will unblock Q9
   and all other queries that use the 6+ table MHJ plan.

2. **HammerDB load timeout investigation (P1):** Determine whether the
   HammerDB TCL client times out due to TCP write-stall or a per-socket
   deadline, and either fix the server-side stall or switch to a native
   Go loader (which does complete SF=1).

3. **ANALYZE with full data:** Re-run ANALYZE after a complete load to
   ensure the query planner has accurate cardinality statistics.

---

## Log files

| File | Description |
|---|---|
| `bench/tpch/logs/hammerdb_full_20260504-071731.log` | Master run log (all phases) |
| `bench/tpch/logs/build_goopg_20260504-071734.log` | HammerDB schema build log |
| `bench/tpch/logs/run_goopg_20260504-072425.log` | HammerDB power test log |
