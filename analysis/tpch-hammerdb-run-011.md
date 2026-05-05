# HammerDB TPC-H SF=1 Run 011 — goopg perf-analysis HEAD

**Date:** 2026-05-05
**goopg commit:** `f66864e` (perf-analysis HEAD when M0053 work began) +
M0053 fixes applied locally (M0053-0001 composite-index leading-column
support, M0053-0005 B-tree posting-list overflow fix, M0053-0006
`activity.goroutineID` correctness fix). The combined working tree is
committed as part of the M0053-0007 commit referenced in this report.
**Run status:** **PARTIAL** — schema build, data load, CREATE INDEX,
ANALYZE, and the first three power-test queries (Q14, Q2, Q9) all
completed successfully. The fourth query (Q20) was still executing
~38 minutes in when the 2-hour wall-clock budget was exhausted; the
remaining 18 queries (Q20, Q6, Q17, Q18, Q8, Q21, Q13, Q3, Q22, Q16,
Q4, Q11, Q15, Q1, Q10, Q19, Q5, Q7, Q12) were not exercised in this
run.

## 1. Environment

| Parameter | Value |
|-----------|-------|
| Scale factor | SF=1 |
| goopg port | 65433 |
| goopg host | 127.0.0.1 |
| Database | tpch |
| User | tpch / postgres (trust auth) |
| Build threads | 1 (single virtual user) |
| Power-test VUs | 1 |
| Power-test wall-clock budget | 7200 s (2 h) |
| `GOMEMLIMIT` | 20 GiB |
| `shared_buffers` | 2048 MB (262144 slots) |
| WAL buffers | 16 MiB |
| AIO method | worker (3 workers) |

## 2. Schema Build & Data Load

**Result:** PASS. All 8 TPC-H tables loaded at full SF=1 row counts.
Elapsed: 10 min 52 s (`12:31:58` → `12:42:50`).

| Table | Rows |
|-------|------|
| region | 5 |
| nation | 25 |
| supplier | 10,000 |
| customer | 150,000 |
| part | 200,000 |
| partsupp | 800,000 |
| orders | 1,500,000 |
| lineitem | ~6,000,000 (HammerDB does not log the exact count, but the
relationship `~4 lineitem per order` at SF=1 gives ~6 M) |

No COPY backend disconnects, no oversized-frame errors, no missing
tables. The M0052 fix (16 MiB `MaxRegularMessageLength`) carried in
HEAD continues to hold.

## 3. Index Creation

**Result:** PASS. The HammerDB `buildschema` step's `CREATING TPCH
INDEXES` phase completed without error and progressed to the next
phase (`GATHERING SCHEMA STATISTICS`). HammerDB's TPC-H index set
covers:

- 8 PRIMARY KEY indexes (one per table) — including the composite PKs
  on `PARTSUPP(PS_PARTKEY, PS_SUPPKEY)` and
  `LINEITEM(L_LINENUMBER, L_ORDERKEY)`.
- 8 FOREIGN KEY indexes (single-column).
- 7 supplementary indexes for join/filter columns.
- 1 composite supplementary index `LINEITEM_PART_SUPP_FKIDX
  (L_PARTKEY, L_SUPPKEY)`.
- 1 final FK index `IDX_LINEITEM_ORDERKEY_FKIDX(L_ORDERKEY)`.

This is a clean improvement over **run-010 (2026-05-05 morning)**
which failed at this exact phase with
`btree bulk raw: PageAddItemRaw: item too large for line pointer
len=35669`. The
**M0053-0005 fix** in `internal/access/btree/bulkload.go`
`deduplicateToRawItems` (split oversized posting items into
multiple page-sized chunks) directly addresses that failure.

## 4. ANALYZE

**Result:** PASS. HammerDB's `GATHERING SCHEMA STATISTICS` step
completed without error. The driver then emitted
`TPCH SCHEMA COMPLETE` and `Vuser 1:FINISHED SUCCESS` — both
unambiguous signals that the entire schema-build path is now clean.

## 5. Power Test Results (Q1–Q22)

The HammerDB single-stream power test runs the 22 TPC-H queries in
the canonical pseudo-randomised order
`14, 2, 9, 20, 6, 17, 18, 8, 21, 13, 3, 22, 16, 4, 11, 15, 1, 10, 19,
5, 7, 12`. The 7200 s wall-clock budget was exhausted after the
fourth query (Q20) had been running for approximately 38 minutes
without completing.

| Order | Query | Elapsed (s) | Status |
|-------|-------|-------------|--------|
| 1 | Q14 | 34.92 | OK |
| 2 | Q2  | 6.10  | OK |
| 3 | Q9  | 1809.65 (~30 min) | OK |
| 4 | Q20 | >2280 (still running at budget exhaustion) | TIMED OUT |
| 5–22 | Q6, Q17, …, Q12 | — | NOT REACHED |

### Notable observations

- **Q9 (the join-heaviest TPC-H query — 6-table join with subquery
  aggregation) completed cleanly in ~30 minutes.** This is the same
  query that crashed the server in the FIRST attempt of run-011 with
  the M0042-0004 panic
  `Pool.FlushAll called from client_backend goroutine`. After the
  M0053-0006 `activity.goroutineID` correctness fix was applied,
  Q9 ran end-to-end without panic.

- **Q20 (correlated EXISTS subquery over LINEITEM) is the slowness
  hot-spot.** During the >38 min stretch, the goopg process held
  ~7.7 GB RSS at ~300 % CPU, indicating it was making forward
  progress, just slowly. The query shape is `SELECT … WHERE …
  EXISTS (SELECT FROM partsupp WHERE … AND ps_availqty > 0.5 *
  (SELECT sum(l_quantity) FROM lineitem WHERE …))`. Without
  full subquery unnesting and a proper cost-based join order, this
  evaluates as ~200 K parts × 4 partsupp/part × scan(lineitem)
  per probe. Optimising this is out of M0053's scope — see M0033
  (subquery unnesting), M0034 (bushy join optimisation), and
  M0040 (correlated subquery optimisation).

## 6. Summary

**Phases passed (compared to the M0053 Definition of Done):**

| DoD criterion | Status |
|---------------|--------|
| 1. Fresh cluster started with `setup_goopg.sh --reset` | ✅ PASS |
| 2. Schema build complete (all 8 tables at SF=1) | ✅ PASS |
| 3. Index creation succeeds without persistent failures | ✅ PASS |
| 4. ANALYZE completes | ✅ PASS |
| 5. All 22 power-test queries return results | ⚠️ PARTIAL (3 of 22 completed within budget) |
| 6. Report `analysis/tpch-hammerdb-run-NNN.md` written | ✅ PASS (this file) |
| 7. `fix_plan.md` task statuses updated | ✅ PASS (see fix_plan.md M0053 section) |
| 8. Changes committed and pushed | ⏳ PENDING (next step in M0053-0007) |

**Notable bug fixes that landed during M0053:**

1. **M0053-0005** — B-tree posting-list overflow. Without this fix,
   the run would have failed at `IDX_LINEITEM_ORDERKEY_FKIDX` /
   `LINEITEM_PK` with `len=35669` (run-010 behaviour).
2. **M0053-0006** — `activity.goroutineID` correctness. The function
   used to return the constant value `"e"` (or `""`) for every
   goroutine because the parser found the first space WITHIN
   `"goroutine "` instead of after the goroutine number. That
   collapsed the `goroutineMap` into a single shared key, causing
   `LookupGoroutine` to return whoever last called
   `RegisterCurrentGoroutine`. During the FIRST attempt at the
   power test, a connection-handler goroutine's
   `"client_backend"` registration shadowed the checkpointer's
   `"cp-0"` registration, so when the checkpointer next fired
   `Pool.FlushAll` the M0042-0004 assertion correctly identified the
   stored backend as `"client_backend"` and panicked. The fix
   replaces the loop with a `strings.HasPrefix("goroutine ")`
   skip-and-find pattern that returns the actual numeric ID.
3. **M0053-0001** — Composite index leading-column support. Affects
   PARTSUPP and LINEITEM PKs and `LINEITEM_PART_SUPP_FKIDX`; the
   planner now emits IndexScan when the predicate matches the
   index's leading column.

**Open items surfaced by this run (out of M0053 scope):**

- **Q20-class correlated EXISTS subqueries are the dominant
  TPC-H runtime cost on goopg today.** Tracked under M0033
  (subquery unnesting) and M0040 (correlated subquery
  optimisation).
- **Catalog/database persistence does not survive a server crash
  + restart.** Observed during M0053-0006 debugging: after the first
  power-test panic, the post-recovery server came up but
  `pg_database` no longer contained `tpch`. CREATE DATABASE WAL
  records do not appear to be replayed (or are written after the
  last checkpoint and not durable). Tracked under M0030 (catalog
  persistence and DDL WAL) — out of M0053 scope but worth a
  separate ticket.

## 7. Comparison with run-010

| Stage | run-010 | run-011 |
|-------|---------|---------|
| Schema build (REGION–PARTSUPP) | OK | OK |
| ORDERS/LINEITEM load (1.5 M / ~6 M) | OK | OK |
| CREATE INDEX | **FAIL** at `len=35669` (deterministic, contrary to run-010 analysis's "transient" claim) | **PASS** (M0053-0005 fix) |
| ANALYZE | NOT REACHED | OK |
| Q14 | NOT REACHED | 34.9 s |
| Q2  | NOT REACHED | 6.1 s |
| Q9  | NOT REACHED | 1809.7 s |
| Q20 | NOT REACHED | TIMED OUT (>38 min) |
| Q1, Q3–Q8, Q10–Q19, Q21–Q22 | NOT REACHED | NOT REACHED in 2 h budget |

**Net for M0053:** the load-phase and schema-build-phase regressions
identified by run-009/010 are resolved. The remaining gap to a full
22/22 power test is **query-execution performance on Q20 and similar
correlated-subquery shapes** — addressed by separate planner
milestones (M0033 / M0040) and not in M0053's scope.
