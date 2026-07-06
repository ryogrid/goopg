# 04 — Performance Baselines (the comparison data)

Snapshot 2026-07-06. These are the pinned reference numbers that
performance/benchmark regression checks compare against. **Re-read each cited
file before a live comparison** — several are load-dependent and get re-pinned.

---

## A. TPC-H Q12/Q13 row-count spot-check — `bench/tpch/spotcheck_expected.env`

The fast pre-commit tripwire (consumed by `scripts/tpch-spotcheck.sh`).
**Authoritative current values:**

```
Q12_EXPECTED=2
Q13_EXPECTED=33
```

- **Q12 = 2 is a structural invariant** — the query is `GROUP BY l_shipmode`
  with `l_shipmode IN ('MAIL','SHIP')`, so any correctly-loaded dataset returns
  exactly 2 rows.
- **Q13 is load-dependent** (`GROUP BY c_count` over a random order
  distribution). It was **35** on the May-2026 canonical dataset, and was
  **RE-PINNED to 33** on the 2026-06-13 reload (`build_goopg_20260613-144815.log`;
  lineitem=5,999,786 / orders=1,500,000; `tmp/spotcheck_run_20260613.log`). A
  fresh HammerDB build can legitimately shift Q13 by a few rows — after every
  `build_schema_goopg.sh`, re-pin `Q13_EXPECTED` from one trusted run. As long as
  **Q12=2 holds**, a small Q13 drift is a re-pin, not a regression.
- **Known FAILURE signature (revert the commit immediately): `Q12=0 / Q13=2`**
  (the m0071 Stage-B silent regression).

> ⚠️ **Documentation drift to be aware of:** the executor/planner *practice card*
> and `.ralph/PROMPT.md` still cite "Q13=35". The **env file (33) is
> authoritative** — the script reads it, the prose does not.

---

## B. Full 22-query execution times — `bench/tpch/logs/tpch_power_test_20260526.md`

**The newest full 22/22-pass power-test record.** Date 2026-05-26, commit
`26cf58d` (fix VARATT_IS_1B_E TOAST marker), branch `align-data-structure-with-pg`,
SF=1, HammerDB 5.0, run log `run_goopg_20260526-135117.log`. **FINISHED SUCCESS,
zero errors.**

| Order | Query | Time (s) | | Order | Query | Time (s) |
|--:|--:|--:|---|--:|--:|--:|
| 1 | Q14 | 20.728 | | 12 | Q22 | 84.918 |
| 2 | Q2 | 59.078 | | 13 | Q16 | 2.904 |
| 3 | Q9 | 56.059 | | 14 | Q4 | 217.190 |
| 4 | Q20 | 19.451 | | 15 | Q11 | 2.409 |
| 5 | Q6 | 13.116 | | 16 | Q15 | 36.701 |
| 6 | Q17 | 45.209 | | 17 | Q1 | 20.036 |
| 7 | Q18 | 36.773 | | 18 | Q10 | 18.524 |
| 8 | Q8 | 171.430 | | 19 | Q19 | 24.503 |
| 9 | Q21 | 295.057 | | 20 | Q5 | 18.603 |
| 10 | Q13 | 84.864 | | 21 | Q7 | 122.899 |
| 11 | Q3 | 16.789 | | 22 | Q12 | 100.535 |

- **Total elapsed: 1469 s (~24.5 min). Geometric mean: 36.30 s.**
- Slowest: Q21 (295s), Q4 (217s), Q8 (171s), Q7 (123s), Q12 (101s).
  Fastest: Q11 (2.4s), Q16 (2.9s).
- Context: this run is the reference *because* it is the first all-pass after the
  TOAST-marker fix — the prior run failed at Q11 (`column "inf" does not exist`)
  when `supplier`/`customer` returned 0 rows (the `0x1B` TOAST marker collided
  with the PG short-varlena header of any 12-char string, e.g. phone numbers).

**Treat this file as the current execution-time reference baseline.**

---

## C. Prior full sweep — `analysis/tpch/m0093-q1-q22-regression-sweep.md`

2026-05-11 post-M0093 lazy-XID refactor sweep. Method: `setup_goopg.sh --reset`
→ `build_schema_goopg.sh` (SF=1) → `tmp/tpch-runner -per-query-timeout 600s`
driving `internal/testutil/tpch.Queries()`. **22/22 succeeded in ~1248 s, zero
errors.** Still valid as **row-count correctness anchors** (times predate later
work; Q13=36 here predates the 2026-06-13 re-pin to 33).

Canonical SF=1 table row counts (preserve): lineitem 5,998,769 · orders
1,500,000 · customer 150,000 · part 200,000 · partsupp 800,000 · supplier
10,000 · nation 25 · region 5.

Selected per-query (elapsed s / rows) — the correctness anchors that matter most:

| Q | rows | Q | rows |
|--|--:|--|--:|
| Q1 | 4 | Q12 | 2 |
| Q3 | 11,686 | Q13 | 36 (now 33) |
| Q5 | 5 | Q16 | 18,360 |
| Q9 | 175 | Q18 | 9 |
| Q10 | 20,412 | Q21 | 397 |
| Q11 | 791 | Q22 | 7 |

History anchors: Q5 175→... (M0077 "Q5 cancel@600s → 26s"), Q9 "7→175", Q21
"0→381" (M0071-0009; magnitude match, HammerDB vs synthetic data).

---

## D. EXPLAIN plan baselines — `make plan-gate`

- Tool: `cmd/plan-snapshot/main.go`; baselines stored in `plan_snapshots/<label>.txt`.
- **Committed baselines:**
  - `plan_snapshots/m0077-final.txt` — **latest** (plan-gate picks `ls -t | head -1`);
    post-planner-fix reference (Q2 = nested Hash Joins + 3-table Multi-Way).
  - `plan_snapshots/m0076-baseline-ffc3429.txt` — prior (Q2 = 5-table Multi-Way Hash Join).
- Each holds per-query plan trees `=== Q1 … === Q22` with `(rows=N)`/`(stats)` annotations.
- Diff modes: `structural` (default; strips `(rows=N)` cost), `strict-text`
  (byte-for-byte), `semantic-cost` (structural + cost ±10 %).
- `plan-gate` diffs against the latest snapshot; **SKIP(0)** if no baseline or
  goopg unreachable on 65433 — never hard-blocks.
- Design: `docs/design/0076-0006-plan-snapshot-harness.md`.

---

## E. pgbench goopg self-baseline — `bench/pgbench-compare/results/m0099_matrix_summary.md`

Latest authoritative goopg-only TPS baseline (2026-05-12, commit 46f2fe1). Config
`-c 100 -j 100 -T 180 -s 100`, `shared_buffers=2560MB`, `wal_buffers=100MB`.

| Workload | M0098-0008 | M0099 (current) | Target |
|----------|-----------:|----------------:|-------:|
| Standard (TPC-B) | 443 | **447** ¹ | 1,500 |
| Simple Update | 420 | **410** | 1,500 |
| Select Only | 4,990 (cold) | **5,204** | 10,000 |

¹ Standard run aborted at ~114 s on "command N: no results" accumulation; TPS
from partial run. Failure rates M0099: Standard 0.651 %, Simple 0.001 %,
Select 0.000 %.

> **Do not confuse this with the pre-commit pgbench smoke.** The smoke uses a
> light config (`-c 2 -j 2 -T 30`) and is a *functional* gate (0 failed txns),
> not a TPS baseline. The 447/410/5204 numbers above are the heavy self-baseline.

Chronological self-baselines (for trend context):
`20260511_goopg_pgbench_summary.md` 69.81/94.998/400.07 →
`m0098_baseline` 229/228/6166 → `m0098_final` 443/420/4990 → **m0099 447/410/5204**.

pgbench pprof baselines: `bench/pgbench-compare/results/m0098_baseline_{heap,select_cpu,simple_update_cpu,standard_cpu}.pprof`.

---

## F. pprof / heap snapshots

- `bench/tpch/pprof/*` (2026-05-05) — full TPC-H profiling by phase (`load`,
  `idx`, `q9`, `q20`, `end`): `cpu_*.prof`, `heap_*.prof`, `allocs_*.prof`,
  `block_*.prof`, `mutex_*.prof` + a before/after pair `mutex_q9_{pre,post}.prof`.
- Historical profiling snapshots; useful for allocation/heap regression
  investigations, not a pass/fail gate.

---

## G. The "608 regression anchors" concept

Not a stored baseline file — a project-history metric
(`analysis/ralph-loop-kaizen/02-pain-points.md`: 608 mentions of
regression/revert/rework). It motivates the plan-gate / spotcheck / TPC-H sweep
gates (cited in `.ralph/PROMPT.md`, the executor-planner practice card, and the
deferral ledger) but is not itself a number to compare against.

---

## H. Baseline authority summary (current vs stale)

| Baseline | Authoritative record | Status |
|----------|----------------------|--------|
| TPC-H Q12/Q13 row counts | `bench/tpch/spotcheck_expected.env` (Q12=2, Q13=33) | **CURRENT** (re-pinned 2026-06-13) |
| Full 22-query execution times | `bench/tpch/logs/tpch_power_test_20260526.md` (1469s, geomean 36.30s) | **CURRENT** newest full pass |
| Q1..Q22 row-count anchors | `analysis/tpch/m0093-q1-q22-regression-sweep.md` (2026-05-11) | Valid row anchors; times/Q13 pre-re-pin |
| EXPLAIN plan shapes | `plan_snapshots/m0077-final.txt` (+ m0076 baseline) | **CURRENT** (m0077-final = latest) |
| pgbench goopg TPS | `bench/pgbench-compare/results/m0099_matrix_summary.md` (447/410/5204) | **CURRENT** self-baseline (2026-05-12) |
| pprof/heap | `bench/tpch/pprof/*`, `m0098_baseline_*.pprof` | Historical snapshots |
