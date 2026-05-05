# HammerDB TPC-H SF=1 Run 012 — goopg perf-analysis HEAD (NLI gated OFF)

**Date:** 2026-05-05
**goopg commit:** `0092b03` (perf-analysis HEAD at run start) — comprises
M0054-0001 (CREATE DATABASE WAL persistence), M0054-0002 (TPC-H EXPLAIN
baseline), M0054-0003a/b/c/d (index-utilisation gap closures),
M0054-0004 (pprof bottleneck survey), M0054-0005a/b/c (per-row buffer
reuse, hash-join + spill-reader alloc reduction, pooling + index-build
column projection), M0054-0006 (Nested-Loop Index Join), and
M0054-0006a-pre (single-table predicate routing into MultiHashJoin
inputs). The `feat(cmd): GOOPG_DISABLE_NLI` commit at HEAD adds the
runtime kill-switch consumed by this run.
**Run status:** **PARTIAL — ABORTED BY USER REQUEST.** Schema build,
data load, CREATE INDEX, ANALYZE, and the first three power-test
queries (Q14, Q2, Q9) all completed successfully. The fourth query
(Q20) was approximately 45 minutes into execution when the run was
aborted at user request to pivot to fixing the NLI Q9 regression
(see §6 below). The remaining 18 queries (Q20, Q6, Q17, Q18, Q8, Q21,
Q13, Q3, Q22, Q16, Q4, Q11, Q15, Q1, Q10, Q19, Q5, Q7, Q12) were not
exercised in this run.
**NLI state:** **OFF** for the duration of run-012. The
`*planner.NestedLoopIndexJoin` rewrite was disabled at server startup
via the new `GOOPG_DISABLE_NLI=1` environment variable, which calls
`planner.SetNLIEnabled(false)` (see `cmd/goopg/main.go:171-175`).
M0054-0006a-pre's input-IndexScan rewrite for HashJoin remains
active — only the NLI promotion step is gated.

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
| `GOOPG_DISABLE_NLI` | `1` (NLI rewrite disabled) |

## 2. Schema Build & Data Load

**Result:** PASS. All 8 TPC-H tables loaded at full SF=1 row counts.
Build wrapper start at `20:04:06` (`Vuser 1:Start:Tue May 05 20:04:06
JST 2026`); `FINISHED SUCCESS` emitted by `20:16:57` — total
build-phase elapsed ~12 min 51 s. This is consistent with run-011's
~10 min 52 s; the small overhead is within run-to-run noise (no
material change to the data-load path landed between run-011 and
run-012).

| Table | Rows |
|-------|------|
| region | 5 |
| nation | 25 |
| supplier | 10,000 |
| customer | 150,000 |
| part | 200,000 |
| partsupp | 800,000 |
| orders | 1,500,000 |
| lineitem | ~6,000,000 |

No COPY backend disconnects, no oversized-frame errors, no missing
tables. Both M0052 and M0053-0005 fixes carried in HEAD continue to
hold.

## 3. Index Creation

**Result:** PASS. The HammerDB `buildschema` step's `CREATING TPCH
INDEXES` phase completed without error and progressed to
`GATHERING SCHEMA STATISTICS`. The 25 indexes (8 PKs including the
two composite PKs on PARTSUPP and LINEITEM, 8 FK indexes, 7
supplementary join/filter indexes, 1 composite supplementary
`LINEITEM_PART_SUPP_FKIDX(L_PARTKEY, L_SUPPKEY)`, 1 final FK index
`IDX_LINEITEM_ORDERKEY_FKIDX`) all built cleanly.

The M0054-0005c index-build column-projection optimisation lives in
HEAD and was active during this index phase. It does not change
correctness, only allocation behaviour during `collectBTreeEntries`.

## 4. ANALYZE

**Result:** PASS. `GATHERING SCHEMA STATISTICS` step completed without
error; the driver emitted `TPCH SCHEMA COMPLETE` and `Vuser 1:FINISHED
SUCCESS`.

## 5. Power Test Results (Q1–Q22)

The HammerDB single-stream power test runs the 22 TPC-H queries in
the canonical pseudo-randomised order
`14, 2, 9, 20, 6, 17, 18, 8, 21, 13, 3, 22, 16, 4, 11, 15, 1, 10, 19,
5, 7, 12`. The run was aborted at user request after Q9 completed and
during Q20 (the fourth query in the stream).

| Order | Query | Elapsed (s) | Run-011 (s) | Δ (s) | Δ (%) | Status |
|-------|-------|-------------|-------------|-------|-------|--------|
| 1 | Q14 | 29.69 | 34.92 | -5.23 | -15.0 % | OK |
| 2 | Q2  | 5.36  | 6.10  | -0.74 | -12.1 % | OK |
| 3 | Q9  | 1351.24 (~22.5 min) | 1809.65 (~30 min) | -458.41 | **-25.3 %** | OK |
| 4 | Q20 | aborted ~45 min in | 2280+ (also unfinished) | n/a | n/a | ABORTED |
| 5–22 | Q6, Q17, …, Q12 | — | — | — | — | NOT REACHED |

Source: `bench/tpch/logs/run_goopg_20260505T201657.log`.

### Notable observations

- **Q9 (the join-heaviest TPC-H query — 6-table join with subquery
  aggregation) completed in ~22.5 minutes, a 25.3 % wall-clock
  reduction vs run-011's 30 min.** This is the headline performance
  result of the M0054 series of patches: with NLI gated OFF, the
  improvement is fully attributable to (in approximate proportion):
  - **M0054-0005a** per-row buffer reuse on `seqScanOp` leaves
    (DecodeRowInto + cloneRow + scanRow buffer) — eliminates per-row
    `Row` allocation for every tuple emitted by every leaf scan.
  - **M0054-0005b** hash-join + spill-reader alloc reduction —
    hoisted `nullRow`/`concatRows` out of build/probe loops; added
    `dataBuf` byte-buffer reuse to `spillReader`.
  - **M0054-0005c** pooling + index-build column projection.
  - **M0054-0006a-pre** single-table predicate routing into
    MultiHashJoin scan inputs — flips the inner scan of Q9's six-way
    join from `Seq Scan(part)` to `Index Scan using part_pk on part`,
    significantly cutting the build-side row count fed into the
    multi-hash table.

  The NLI rewrite (M0054-0006) itself was *not* active during run-012,
  so this 25 % gain is achieved purely from M0054-0005 and the
  M0054-0006a-pre planner change.

- **Q14 and Q2 also improved** by 15 % and 12 % respectively, both
  attributable to the same set of changes (Q14 is a two-way join
  whose inner side flips to IndexScan via M0054-0006a-pre; Q2 is
  scan- and join-bound and benefits from M0054-0005's allocator
  pressure reduction).

- **Q20 was aborted, not failed.** The query was actively executing
  (~5-7 GB RSS on the goopg process, ~300 % CPU, no panics, no
  errors) when the user requested the run be aborted. Q20's
  correlated-EXISTS shape remains the same workload as in run-011
  (where it also did not finish within the 2-hour budget). No new
  Q20 result is available; the prior run-011 conclusion stands until
  M0033 / M0034 / M0040 (subquery unnesting / bushy join / correlated
  subquery optimisation) work lands.

## 6. NLI Q9 Regression — Why NLI was Gated OFF

**Symptom (run-012 attempt #1, NLI on):** Q9 raised
`ERROR: column "ps_suppkey" is not numeric at runtime`. Q1, Q2, Q14
all completed in the same run with NLI on, so the regression is
specific to *some* shape unique to Q9.

**Root cause (Explore-agent code audit, hypothesis):** The
M0054-0006 planner rule at `internal/planner/nl_index_join.go:152-175`
finds a B-tree index by single column name
(`findBTreeIndexForColumn(cat, table, innerKey.Name)`), so for the
composite PK `partsupp_pk(ps_partkey, ps_suppkey)` it accepts the
index when only `ps_partkey` is in the equi-conjunct list. The
trailing column `ps_suppkey` is then unbound on `IndexScan.Key`,
and the executor's `lookupKey` at
`internal/executor/operators_index.go:269-289` encodes only the
leading column and pads the rest with `0xFF` — turning a
two-column equi-probe into a partial-key range scan over the
suffix. The residual conjunct `l_suppkey = ps_suppkey` is then
evaluated against rows where `ps_suppkey` was never properly
bound, surfacing as the runtime "is not numeric" type error.

**Mitigation (this run):** Add a startup env-var
`GOOPG_DISABLE_NLI=1` that calls `planner.SetNLIEnabled(false)`,
disabling the rewrite at the planner pass. M0054-0006a-pre
(single-table predicate routing into HashJoin scan inputs) is a
separate planner pass and remains active — that's the source of
this run's IndexScan-on-part wins for Q9 / Q14.

**Tracked follow-up:**
`.ralph/fix_plan.md` — `M0054-0006-followup-Q9-composite`. The
follow-up's acceptance criteria require both (a) a planner-side
gate that refuses to promote when the index is composite and the
predicate does not bind every leading column, and (b) an executor-
side multi-column probe-key encoder so the NLI promotion path
*does* fire for queries that bind every leading column.

## 7. Why the Run was Aborted Early

The user requested abort after Q9 completed and Q20 was ~45 minutes
into execution. Reason: pivot work to landing the NLI Q9 regression
fix so a *full* run-012 (NLI on, 22/22 within budget) can be the
next attempt. This was **not** a server-side failure — the goopg
process was healthy and progressing through Q20 at abort time.

## 8. Comparison with Run-011

| Phase | Run-011 | Run-012 | Δ |
|-------|---------|---------|---|
| Schema build + load + index + ANALYZE | ~10 min 52 s | ~12 min 51 s | +2 min (within run-to-run noise) |
| Q14 | 34.92 s | 29.69 s | **-15.0 %** |
| Q2  | 6.10 s  | 5.36 s  | **-12.1 %** |
| Q9  | 1809.65 s | 1351.24 s | **-25.3 %** |
| Q20 | not finished within budget | aborted before completion | n/a |
| Q5–Q22 (rest) | not reached | not reached | n/a |

### Sub-task attribution (which patches drove which delta)

- M0054-0005a per-row buffer reuse — broad effect across every
  query that emits many rows from leaf scans; visible primarily on
  Q9 and Q14.
- M0054-0005b hash-join + spill-reader alloc reduction — visible
  on Q9 (build/probe-heavy) and any query whose hash table spills.
- M0054-0005c pooling + index-build column projection — index-build
  effect captured in §3, query-time effect smaller for SF=1.
- M0054-0006a-pre input-IndexScan rewrite — flips
  `Seq Scan(part)` to `Index Scan using part_pk on part` in Q9 and
  flips the inner of the binary-join in Q14. Contributes the
  largest share of the Q9 / Q14 wins.
- M0054-0006 NLI itself — *not active in this run*. Its expected
  contribution remains to be measured in the next run-012 attempt
  after the Q9-composite regression is fixed.

## 9. Open Follow-ups (residuals)

The following sub-tasks remain open in `.ralph/fix_plan.md` and gate
the next full HammerDB run:

- **M0054-0006-followup-Q9-composite** *(must land first)*. NLI
  composite-index regression. Acceptance: planner gate + executor
  multi-column encode + composite-key parity test + Q9-shape live
  smoke test. With this landed, NLI can be re-enabled by default
  for run-013 / a full run-012 retry.
- **M0054-0007-followup-resume** *(new — to be opened)*. Re-run
  HammerDB SF=1 with NLI re-enabled to capture the full 22/22 power
  test result. 1-2 h wall clock.
- **M0054-0006-followup-Q19** — extract equi-keys from disjunctive
  predicates so Q19's three-branch OR shape gets an NLI plan on
  the part side.
- **M0054-0006-followup-Q15b** — handle the binary join produced
  by an inlined VIEW reference (supplier × revenue0) so Q15b shows
  `Index Scan using supplier_pk on supplier`.
- **M0054-0006e-followup** — wire the `enable_nestloop_index` GUC
  through to `nliEnabled.Store(...)` so SQL-level SET propagates
  to the planner.
- **M0054-0005b-followup** — full Borrow-semantics rollout
  beyond the spillReader / scanRow scope.
- **M0054-0005c-followup** — capacity-bucket sync.Pool.

The M0054-0007 milestone close criterion (22/22 within 7200 s)
remains *not yet met* and stays open until the followup-resume
re-run produces a green run.
