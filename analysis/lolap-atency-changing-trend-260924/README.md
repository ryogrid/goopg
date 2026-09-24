# Query-Latency Trend Through the Plan-Parity Programme (2026-08-24 → 2026-09-24)

Survey of every recorded per-query execution time for TPC-H (SF=1) and
TPC-DS (SF0.25 / SF0.5) found in the repository over the last month,
sampled at roughly 3-day intervals where records exist. Compiled
2026-09-24 by inventorying `bench/`, `tmp/`, `analysis/`, `docs/` and
`.ralph/fix_plan.md` timing records.

- `01-tpch-trend.md` — TPC-H tables (goopg natural-election series,
  forced/legacy-diagnostic arms, warm pair, PG 18.3 references)
- `02-tpcds-trend.md` — TPC-DS tables (SF0.25 sweep series ×6,
  SF0.5 warm pair, PG oracle, M0145-0024 A/B probe)
- `tpcds-sf025-table.md` — generated per-query table (seconds) backing 02

## 1. How to read the records — classification axes

### 1.1 "PG-shaped" is NOT a pinned plan

**No record in the window executes a plan pinned to a captured PG plan —
that mechanism does not exist.** What exists instead:

| Class | What it means | Which records |
|---|---|---|
| **Natural election** | The shipped planner elects plans by cost. `GOOPG_PGSHAPED_DP=unset(on)` means the *PG-shaped dynamic-programming join search* is the enumerator (`internal/optimizer/joinsearch.go:56-81`) — it models PG's `standard_join_search` but picks whatever its cost model prefers. | Essentially every sweep/arm/`arm-*.txt` file |
| **Deliberately-degraded diagnostic arms** | Arms exporting `GOOPG_PGSHAPED_DP=0` run the **legacy DP** (no join-order search; statement keeps syntactic FROM order). This is the *opposite* of PG-shaped — an A/B diagnostic, not production. | `tmp/arm-m0143-0010-b.txt`, `tmp/m0145-0020a-accept.txt`, `analysis/m0142/p0e7-admitsemianti-{on,off}.txt`, `analysis/planner-spill-cost-calibration/cut3-deferred-20260907/dparm-*` |
| **Other forcing knobs** | `GOOPG_GATHER_PATHS=off` (pre-M0140-0003 arm — disables parallel path placement), `GOOPG_JOINTREE_PIPELINE` (M0145 pipeline selector: `=0` = legacy pinned-spine; unset/`1` = jointree-first — during the whole window the default was **off**, i.e. every sweep ran the legacy pipeline; the flip to default-on is in-flight at writing), `GOOPG_DERIVED_FIREWALL=off`, `SF025_PLAN_PIN=1` (pins goopg's *own* plans for regression, not PG's). | TPC-DS sweeps before ~09-17 carry `GATHER_PATHS=unset(off)`; four executed knob-arm sweeps (`JOINTREE_PIPELINE=1`, `DERIVED_FIREWALL=off`) exist — see `02-tpcds-trend.md` §D |

The goopg-internal **"pinned spine"** term refers to the legacy pipeline
pinning outer/semi/anti joins syntactically above the searched prefix —
it is still natural cost election for the searched part, so all standard
records above remain "natural".

### 1.2 Serial vs parallel — two different axes

- **Submission axis**: almost every record is "serial" in the sense of
  one query per connection, sequential. This says nothing about
  in-query parallelism.
- **Plan axis** (`max_parallel_workers_per_gather`):
  - `mpwg=0` (true serial plans): `analysis/executor-refactor/ex0-02-20260903`
    Q6 record; e11 `s*` subset arms (09-06); spill-calibration
    `serial-{pre,post}` (09-07); every `estimate-audit -serial` executed
    audit (mandatory because goopg does not propagate instrumentation
    out of Gather workers).
  - `mpwg=4` (parallel-capable): everything else in-window. Canonical
    TPC-H parity switched serial→parallel on **2026-09-20**
    (M0144-0001 owner decision); TPC-DS captures pinned `=4` in-session
    since 2026-09-08 (K10).

### 1.3 work_mem eras (dominates comparability)

| Era | goopg | PG | Notes |
|---|---|---|---|
| ≤ 2026-09-01 | BootVal **512MB** | TPC-H conf 64MB / TPC-DS conf 4MB | up to 128× asymmetry |
| 2026-09-02 → 09-23 | **64MB** conf+session pins on TPC-H pair; TPC-DS pair pinned `64MB` in-session since 09-08 | 64MB | P0-12 alignment moved honest-control total +62% |
| 2026-09-24 → | **512MB** written into all 5 cluster confs, no session SET, SHOW-verified (commit `ef559808f`) | 512MB | deliberately measures neither engine's out-of-box default |

## 2. Headline trends

### TPC-H SF=1, goopg — full-sweep wall-clock totals (natural election, mpwg=4-capable plans)

| Date | Record | Total | Notes |
|---|---|---|---|
| 09-05 | `analysis/planner-refactor-take3/a04-baseline-20260905` | ~224 s | **S-cold AND serial plans (mpwg=0)** — a different regime from every later column on two axes |
| 09-06 | e11 `d0-r1` (default arm) | ~134 s | warm stats seeded; Q18 35.0s, Q15b 23.0s |
| 09-08 | `tpch-before-best` | ~108.6 s | optimize-row-decode "before" arm |
| 09-14 | r128 `sf1-values-ON` | ~74 s | |
| 09-19 | `tmp/acceptance-staged` | 66.6 s | **old dataset** |
| 09-20 | `tmp/arm-on-20260920` | 64.4 s | **new dataset** (:65433 restored from preloss clone — row counts shifted, see caveat 4) |
| 09-22 | `arm-on-20260922-loop77` | 65.9 s | |
| 09-23 | `s2b17-arm` | 68.2 s | |
| 09-24 | `m0145-0008a-arm` (gate PASS) | **58.3 s** | Q18 dropped 18.5→6.8 s |

Plus PG 18.3 reference points: **14.7 s** warm total (09-06, 21 labels)
and ~10.5 s (09-08 `tpch-pg18-baseline`). goopg remains ~4–7× slower
than PG on the same hardware, concentrated in Q18/Q15b/Q21/Q13/Q1.

### TPC-DS SF0.25, goopg — 99-query sweep totals (300s cap, natural election)

| Date | Sweep | Total | Notes |
|---|---|---|---|
| 09-11 | `sweep-20260911-190106` | 257.0 s | first in-window sweep; `GATHER_PATHS=unset(off)` |
| 09-14 | `sweep-20260914-080244` | 182.7 s | |
| 09-17 | `sweep-20260917-234551` | 142.5 s | `GATHER_PATHS=unset(all)` era begins |
| 09-20 | `sweep-20260920-231524` | 126.4 s | −63.7% step vs previous sweep that day (Q97 ERROR→1s, Q96 38s→0s) |
| 09-23 | `sweep-20260923-232800` | 126.5 s | |
| 09-24 | `sweep-20260924-121026` | 129.2 s | latest; post-work_mem-512MB conf era |

**≈2× improvement across two weeks**, driven by per-query collapses
(Q14 33.5→13.3s, Q23 30.8→12.3s, Q6 6.1→1.4s, Q78 9.1→4.0s) rather than
one fix. Largest remaining outliers: Q14 ~13s, Q23 ~12s, Q4 ~7-11s.

### TPC-DS SF0.5 warm pair (09-06, single measured pass, concurrency caveat)

Printed totals: goopg **1114.1 s** vs PG **584.9 s** — but PG's Q4 never
finished (>300 s in-run, >400 s on clean re-run). The source file's own
correction counts PG ≥985 s over 99 queries → **≈1.1×, not the 1.9× the
printed totals suggest** (`timings/20260906-warm-goopg-sf05.txt` notes).
PG also ran `work_mem=4MB` vs goopg's 512MB BootVal at the time.

## 3. Coverage gaps

- **Nothing before 2026-09-02** (Aug 24 – Sep 1 window is empty: the
  earliest records are the take2 perf reports of 09-02).
- **TPC-H goopg: no timing records 09-09 → 09-13 and 09-15 → 09-17**
  (the :65433 cluster was lost to the 09-17/09-18 incidents and the
  09-18 host-global OOM; it was restored 09-19 from
  `preloss-clone-20260915`).
- **TPC-DS SF=1**: no in-window per-query sweep exists; only plan
  captures and isolated fix_plan citations (Q77 9015→5306 ms,
  Q78 50885→48066 ms under M0145-0011; Q78 29 s→DNF under the
  M0145-0018 no-go).
- **PostgreSQL TPC-DS**: only the 09-06 SF0.5 warm pass and the 09-11
  SF0.25 `oracle.txt` (secs column) — no in-window PG re-measurement
  after that.

## 4. Caveats (order-of-magnitude relevant)

1. **work_mem era breaks comparability** across 09-02 (512→64MB) and
   09-24 (64→512MB). P0-12 alone moved the honest-control total +62%.
2. **S-cold vs warm stats**: records labelled S-cold (fresh server, no
   ANALYZE — TPC-H stats are per-connection) are a different regime
   from warm-stat sweeps; the 09-05 baseline (~235 s) is S-cold.
3. **Warm protocol confound**: `timings/` files discard one pass then
   record two; pass 2 sees an older server (GC/heap state left visible
   deliberately). Cite pairs+spread, not single numbers.
4. **TPC-H dataset break ~09-19/20**: row counts shifted (Q2 418→463,
   Q3 11415→11354, Q10 20451→20421, Q11 1302→755…) when :65433 was
   restored from the pre-loss clone. Pre/post-09-20 values compare
   plan performance on slightly different data.
5. **Timeout budgets differ**: 300 s (sf025 sweeps) vs 600 s (TPC-H
   arms, SF1) vs 900 s (rare probes — e.g. `arm-on-20260920`,
   e11 s-arms). A TIMEOUT at 300 s is not a 600 s failure.
5b. **GOGC regime differs**: acceptance arms (`tmp/arm-*`,
   `acceptance-*`) run `GOGC=off` via `tpch-acceptance-arm.sh:134`;
   A04, e11 and EX0-02 ran `GOGC=100`; `bench/tpch/env_goopg.sh`
   defaults off. EX0-02 documents GOGC alone moving Q6 5.48→3.79 s.
5c. **Pipeline regime**: every sampled SF0.25 sweep ran the **legacy
   pinned-spine pipeline** (`JOINTREE_PIPELINE=unset(off)`); the
   M0145-0008 flip to the jointree-first default was unblocked 09-24
   and is in-flight at writing — the 09-24 sweep is the last
   pre-cutover record.
5d. **The PG TPC-H baseline is a third dataset**: `tpch-pg18-baseline.txt`
   row counts (Q2 469, Q3 11356, Q11 517, Q20 92…) match neither goopg
   epoch — goopg-vs-PG comparisons cross a data boundary as well as an
   engine boundary.
6. **Memory-pressure incidents**: 09-24 cgroup throttling
   (`goopg-workloads.slice` ~25.4GB vs `memory.high` 23.6GB, PSI ~78)
   inflated that day's spotcheck/sweep times until co-resident servers
   were stopped; the 09-18 host OOM removed coverage entirely. The
   latest SF0.25 sweep (09-24 121026) ran *after* pressure was relieved.
7. **Concurrency inflation**: the 09-06 warm pairs ran goopg and PG
   captures concurrently on the same host — both totals inflated.
8. **Single-pass sweeps**: sf025 sweep files are one pass each —
   per-query jitter of ±20–30% between same-day sweeps is normal; only
   ≥2× moves are treated as real by the status-delta channel.

## 5. Source inventory (pointer, not exhaustive)

- TPC-H arm corpus: `tmp/arm-*.txt`, `tmp/*-acceptance-*.txt` (~290
  files, tpch-runner format, 24 items incl. Q15 split)
- TPC-DS sweeps: `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-*.txt`
  (300 files, 09-11→09-24)
- Warm pairs: `bench/tpch/timings/`, `bench/tpcds/timings/` (09-06)
- Report-level: `analysis/planner-refactor-take2/perf-*-20260902.md`,
  `analysis/planner-refactor-take3/*2026090{4,5,7}*/README.md`,
  `docs/design/not_ralph/optimize-row-decode/measurements/`
- PG references: `bench/tpch/timings/20260906-warm-pg.txt`,
  `docs/design/not_ralph/optimize-row-decode/measurements/tpch-pg18-baseline.txt`,
  `bench/tpcds/runtime_goopg/tpcds-results-sf025/oracle.txt`
