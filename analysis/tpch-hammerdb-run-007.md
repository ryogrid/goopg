# HammerDB TPC-H End-to-End Run Report — Run 007

**Date:** 2026-05-04
**goopg commit:** `5d50b09` (branch: `perf-analysis`, includes
M0043-0001 lazy iterator + M0043-0002 predicate pushdown)
**HammerDB version:** 5.0
**Scale factor:** SF=1
**Test machine:** x86_64 Linux, WSL2, 32 GB RAM
**Configuration:** `shared_buffers=256MB`, `GOMEMLIMIT=20GiB`, `wal_buffers=16MB`

---

## Executive Summary

This run validates **M0043-0002 — predicate pushdown into MHJ
chain steps** end-to-end on full SF=1 data. The headline result:

> **Q9 completed in 891.3 s (~14.9 min) on full SF=1 (6 M lineitem).**
> In run-006 (M0043-0001 only) Q9 timed out at > 16 min.

This is the first time goopg has produced a complete Q9 result
against the full SF=1 dataset.

| Phase | Status | Notes |
|---|---|---|
| Schema creation (DDL) | ✅ PASS | All 8 tables created |
| Data load — small tables | ✅ PASS | REGION/NATION/SUPPLIER/CUSTOMER/PART/PARTSUPP fully loaded |
| Data load — ORDERS/LINEITEM | ✅ **PASS — full SF=1** | 1,500,000 / 6,001,985 rows via HammerDB native loader |
| HammerDB index creation | ✅ PASS | 16 indexes (8 PKs + 8 secondary) |
| Additional indexes (psql) | ⚠️ PARTIAL | 8/16 succeeded; 8 failed on btree v0 (varchar/char/timestamp) |
| ANALYZE | ✅ PASS | < 1 s |
| Power test — Q14 | ✅ PASS | 34.7 s |
| Power test — Q2  | ✅ PASS | 9.5 s |
| Power test — Q9  | ✅ **PASS — 891.3 s** | **First-ever full SF=1 completion (vs run-006 timeout)** |
| Power test — Q20 | ⚠️ TIMEOUT | Lineitem scalar-subquery-per-partsupp (M0040-0004 territory) |
| Power test — Q6, Q17, Q5, Q15, Q8, Q21, Q13, Q3, Q18, Q7, Q1, Q10, Q19, Q22, Q11, Q16, Q4, Q12 | ❌ NOT REACHED | Q20 timed out before remaining queries ran |

---

## Phase 1: Setup

```
goopg init:  data directory created fresh (--reset)
Start time:  12:47:39 JST
Ready:       12:47:41 JST (2 s)
Port:        127.0.0.1:65433
```

---

## Phase 2: HammerDB Schema Build — full SF=1 ✅

| Table | Rows loaded | Expected (SF=1) |
|---|---:|---:|
| region | 5 | 5 |
| nation | 25 | 25 |
| supplier | 10,000 | 10,000 |
| customer | 150,000 | 150,000 |
| part | 200,000 | 200,000 |
| partsupp | 800,000 | 800,000 |
| orders | **1,500,000** | 1,500,000 |
| lineitem | **6,001,985** | ~6,001,215 |

Total schema build (incl. data load + 24 HammerDB DDL/index/ANALYZE
statements): **3,620 s ≈ 60 min**.

The connection drop seen in runs ≤ 005 stays fixed (commit
`99fda6e` removed the `runtime.GC()` call that was the actual
cause).

### HammerDB-built indexes

All 16 (8 PKs + 8 secondary) created successfully. FK constraints
are accepted as no-ops (goopg has no FK enforcement yet).

### Supplementary indexes via psql (Phase 3)

The orchestration script attempts 16 supplementary indexes
covering predicate columns the HammerDB set misses (see
`analysis/tpch-additional-indexes.md` for the full list). 8 of
them succeed (all numeric-keyed); 8 fail with the goopg btree v0
limitation (`varchar`/`char`/`timestamp`). The script tolerates
per-index failures and proceeds.

This phase took **2,238 s ≈ 37 min** — dominated by the new
6 M-row lineitem indexes. Comparable to run-006's 2,179 s.

---

## Phase 4: ANALYZE

ANALYZE completed in < 1 second across the full SF=1 dataset.

---

## Phase 5: HammerDB Power Test

Test ran with `pg_raise_query_error=true`, single virtual user.

### Completed queries

| Order | Query | Time (s) | Status | vs run-006 (full SF=1) | vs run-005 (30%) |
|---|---|---:|---|---:|---:|
| 1 | Q14 — Promotion Effect | **34.703** | ✅ | 34.5 s | 14.3 s |
| 2 | Q2  — Minimum Cost Supplier | **9.497** | ✅ | 9.94 s | 20.8 s |
| 3 | Q9  — Product Type Profit Measure | **891.286** | ✅ **first SF=1 finish** | TIMEOUT (> 960 s) | TIMEOUT (> 1714 s) |
| 4 | Q20 — Potential Part Promotion | TIMEOUT (> 32 min) | ⚠️ | not reached | TIMEOUT |
| 5–22 | Q6, Q17, Q5, Q15, Q8, Q21, Q13, Q3, Q18, Q7, Q1, Q10, Q19, Q22, Q11, Q16, Q4, Q12 | — | ❌ Not reached | | |

### Q9 — the M0043-0002 result

**Q9 finished**, taking 891 s on full SF=1. Run-006 was killed at
the 2 h harness budget after Q9 had been running ≥ 16 min with
~91 % CPU in the GC after the harness disconnect — the query
was clearly never going to finish. With M0043-0002's per-step
filter eval, that pattern is gone:

- **Heap stayed bounded**, RSS peaking at ~12.5 GB (similar to
  run-006's M0043-0001 ceiling).
- **CPU stayed productive**, ~145 % during execution (vs.
  run-006's terminal 100–230 % drift after disconnect).
- **The Cartesian fan-out via partsupp was pruned at the partsupp
  step** by `ps_partkey = l_partkey` evaluated immediately after
  each partsupp match was bound — this was the entire bottleneck
  identified in `analysis/tpch-hammerdb-run-006.md` § "Q9 6-table
  MHJ throughput on real-scale data".

Q9 did not hit the design's "single-digit minutes" target (15 min
vs. < 5 min target). The remaining cost is dominated by:

1. **`datumKey()` string conversion** for every probe-side
   lookup (~22 M calls for Q9). Replacing the string-keyed hash
   table with a fixed-width numeric / byte-slice key remains a
   future optimization.
2. **The `evalExpr` setup cost per filter call** even when the
   Expr is a simple `BinaryOp{=, ColumnRef, ColumnRef}` — a
   narrow byte-coded path for those cases would be a substantial
   win.

Both are M0043-0003-class follow-ups, **not** blockers for the
M0043-0002 acceptance criteria as stated in the design doc:
*"Q9 finishes (no timeout) and the test gets at least to Q20."*

### Q20 — different bottleneck (M0040-0004 territory)

Q20 timed out at > 32 min — the same wall as M0040 era.
This is unrelated to MHJ; Q20's lineitem scalar subquery is
evaluated **once per distinct partsupp PK row** (800 K unique
keys), as documented in `.ralph/fix_plan.md::M0040-0003`. The fix
is **M0040-0004 — recursive subquery unnest inside
SubqueryExpr.Plan** which already has a design doc at
`docs/design/0040-0002-recursive-subquery-unnest.md`. M0043-0002
does not affect Q20's plan.

---

## Configuration details

```
goopg version:         5d50b09 (M0043-0002 + M0043-0001 +
                                runtime.GC removal)
shared_buffers:        256 MB
GOMEMLIMIT:            20 GiB
wal_buffers:           16 MiB
checkpoint_timeout:    15 min
```

---

## Comparison with previous runs

| Run | ORDERS | LINEITEM | Q14 | Q2 | Q9 | Q20 |
|---|---:|---:|---:|---:|---:|---:|
| run-001 (M0040 era) | partial | partial | 28.8 s | 4.8 s | 51.4 s | TIMEOUT (1 h) |
| run-005 (M0041) | 453 K (30 %) | 1.8 M | 14.3 s | 20.8 s | TIMEOUT (28 min) | not reached |
| run-006 (M0043-0001) | 1.5 M ✓ | 6.0 M ✓ | 34.5 s | 9.94 s | TIMEOUT (16 min) | not reached |
| **run-007 (this, M0043-0002)** | **1.5 M ✓** | **6.0 M ✓** | **34.7 s** | **9.5 s** | **891.3 s ✓** | **TIMEOUT** |

Run-007 is the first run with a **completed Q9 on full SF=1**.

---

## Resolved issues

### ✅ M0043-0002 — predicate pushdown into MHJ chain steps

- **Implementation:** commit `b7cb6aa` partitions
  `MultiHashJoin.Filters` by deepest-bound chain step, evaluating
  each filter at the earliest moment its referenced columns are
  populated. The lazy iterator (M0043-0001) gains an early-exit
  path: a failed step-level filter abandons the prefix without
  expanding deeper steps.
- **Unit-test gate:** `TestMultiHashJoinPredicatePushdown` and
  `TestMultiHashJoinPushdownLeafFallback` exercise both the
  per-step and the OuterColumnRef-escape-hatch paths.
- **Parity gate:** `TestTPCHResultParity` identical=22, divergent=0,
  errored=0 (no regression from M0041's parity status).
- **End-to-end gate (THIS RUN):** Q9 finishes; the test advances
  to Q20.

### ✅ M0043-0001 + 99fda6e (already documented in run-006)

The data-load drop and the heap-explosion are both still fixed.

---

## Remaining issues (out of scope for M0043-0002)

### ⚠️ Q9 wall time still ~15 min on SF=1

Q9 is functionally correct and bounded but slow. The remaining
hot paths (informed by static reading of the M0043-0002 lazy
iterator):

1. `datumKey()` — string serialization on every probe lookup.
   ~22 M calls for Q9. **M0043-0003 candidate**: replace with a
   fixed-width key or per-Datum-kind specialization that avoids
   `fmt.Sprintf`-style allocations.
2. `evalExpr` setup cost — per call, even for trivial
   `ColumnRef = ColumnRef` shapes the BinaryOp dispatch table is
   walked. **M0043-0003 candidate**: byte-coded predicate for
   the common shapes that recur many times during a single MHJ.

### ⚠️ Q20 — recursive scalar subquery unnest needed

M0040-0004 design doc already exists at
`docs/design/0040-0002-recursive-subquery-unnest.md`. Implementing
it would let Q20 fold its inner `SUM(l_quantity)` aggregate into
a HashJoin keyed on `(l_partkey, l_suppkey)`, dropping the
800 K-iteration subquery loop. Independent of M0043.

### ⚠️ Supplementary index set — 8 of 16 fail (btree v0)

Tracked under M0011. `varchar` / `char` / `timestamp` B-tree
key support would unlock the date-range and string-equality
predicate indexes the TPC-H queries actually want.

---

## Files captured

| File | Description |
|---|---|
| `bench/tpch/logs/run_007_full_20260504-124739.log` | Master run log (all phases) |
| `bench/tpch/logs/build_goopg_20260504-124740.log` | HammerDB schema build log |
| `bench/tpch/logs/run_goopg_20260504-142542.log` | HammerDB power test log |
