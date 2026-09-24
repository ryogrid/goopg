# TPC-H SF=1 — Per-Query Execution-Time Trend (seconds)

All values are wall-clock seconds from `tpch-runner`-format records
(`OK elapsed=Ns`), serial submission (one query per connection) on a
fresh capped server, per-query cap 600 s unless noted. `work_mem=64MB`,
`shared_buffers=2GB`, `ecs=2GB` for all records in the window
(P0-12 era) except where noted. `T` = timeout/error at the cap.
`Q15cv`/`Q15a`/`Q15b` are the three statements of Q15.

## A. goopg — natural election (`GOOPG_PGSHAPED_DP=1`, parallel-capable)

Columns: 09-05 `a04-baseline` (**S-cold AND serial plans, mpwg=0** —
included as the earliest full record but it is a different regime from
the other columns on two axes), 09-06 e11 `d0-r1`,
09-08 `before-best` / `after-best` (optimize-row-decode A/B),
09-14 r128 `sf1-values-ON`, 09-19 `acceptance-staged` (**old dataset**),
09-20 `arm-on-20260920` (**new dataset**, per-query cap **900 s**),
09-22 `loop77`, 09-23 `s2b17-arm`, 09-24 `m0145-0008a-arm`.

| Q | 09-05 S-cold | 09-06 e11 | 09-08 bef | 09-08 aft | 09-14 r128 | 09-19 old | 09-20 new | 09-22 | 09-23 | 09-24 |
|---|---|---|---|---|---|---|---|---|---|---|
| Q1 | 17.17 | 8.82 | 2.69 | 2.77 | 3.23 | 5.55 | 5.02 | 4.71 | 5.01 | 5.75 |
| Q2 | 0.74 | 0.97 | 0.71 | 0.72 | 0.91 | 1.79 | 1.77 | 1.71 | 1.99 | 1.69 |
| Q3 | 20.00 | 2.80 | 1.34 | 1.38 | 1.33 | 3.24 | 3.67 | 3.81 | 3.37 | 3.84 |
| Q4 | 1.55 | 1.64 | 1.51 | 1.61 | 1.50 | 1.62 | 1.56 | 0.41 | 0.39 | 0.43 |
| Q5 | 32.12 | 3.83 | 2.99 | 3.22 | 1.44 | 0.71 | 0.69 | 0.68 | 0.70 | 0.95 |
| Q6 | 3.27 | 0.86 | 0.71 | 0.75 | 0.57 | 0.62 | 0.60 | 0.61 | 0.66 | 0.68 |
| Q7 | 11.71 | 5.54 | 3.49 | 3.57 | 2.93 | 1.12 | 1.04 | 1.09 | 1.18 | 1.38 |
| Q8 | 1.15 | 0.49 | 0.43 | 0.43 | 1.60 | 1.71 | 1.22 | 1.29 | 1.32 | 1.77 |
| Q9 | 11.04 | 8.04 | 1.98 | 1.89 | 4.71 | 5.18 | 2.54 | 2.83 | 3.03 | 3.33 |
| Q10 | 7.70 | 2.79 | 2.11 | 1.92 | 1.00 | 0.90 | 4.31 | 4.77 | 5.09 | 4.75 |
| Q11 | 0.14 | 0.17 | 0.17 | 0.17 | 0.13 | 0.15 | 0.14 | 0.15 | 0.16 | 0.16 |
| Q12 | 12.75 | 13.07 | 15.36 | 9.05 | 1.29 | 1.80 | 1.60 | 1.76 | 1.81 | 1.83 |
| Q13 | 4.89 | 5.05 | 4.24 | 4.37 | 7.26 | 3.75 | 3.89 | 4.68 | 4.56 | 4.51 |
| Q14 | 1.78 | 0.49 | 0.45 | 0.44 | 0.38 | 0.42 | 0.37 | 0.41 | 0.43 | 0.44 |
| Q15cv | — | 0.01 | 0.01 | 0.00 | 0.01 | 0.01 | 0.01 | 0.00 | 0.00 | 0.01 |
| Q15a | — | 2.60 | 2.44 | 2.62 | 1.98 | 2.15 | 1.91 | 2.15 | 2.14 | 2.16 |
| Q15b | 21.26 | 22.99 | 23.07 | 22.64 | 10.79 | 11.92 | 11.74 | 11.76 | 12.24 | 12.06 |
| Q16 | 0.99 | 0.73 | 0.35 | 0.36 | 0.28 | 0.31 | 0.30 | 0.29 | 0.30 | 0.32 |
| Q17 | 0.52 | 0.52 | 0.57 | 0.54 | 0.43 | 0.41 | 0.42 | 0.39 | 0.41 | 0.42 |
| Q18 | 51.77 | 35.04 | 29.66 | 22.59 | 19.04 | 18.18 | 16.91 | 17.68 | 18.47 | 6.82 |
| Q19 | 8.45 | 2.12 | 2.12 | 2.01 | 1.62 | 1.65 | 1.48 | 1.62 | 1.62 | 1.69 |
| Q20 | 1.29 | 1.26 | 0.21 | 0.18 | 0.13 | 0.14 | 0.13 | 0.14 | 0.18 | 0.14 |
| Q21 | 12.79 | 13.61 | 11.42 | 11.36 | 11.36 | 2.85 | 2.53 | 2.66 | 2.73 | 2.80 |
| Q22 | 0.64 | 0.68 | 0.61 | 0.67 | 0.41 | 0.46 | 0.50 | 0.35 | 0.39 | 0.39 |
| **Total** | ~224 | ~134 | ~108.6 | ~95 | ~74 | 66.6 | 64.4 | 65.9 | 68.2 | **58.3** |

Notable per-query arcs: **Q12** 15.4→1.3 s between 09-08 and 09-14
(the largest single collapse); **Q5** 32→0.7 s by 09-14; **Q18**
51.8→16.9 s steadily, then 18.5→6.8 s in the final 09-24 arm;
**Q21** 12.8→2.5 s at the 09-19/20 boundary (also the dataset break —
see README caveat 4); **Q10** moved the other way (0.9→4.3 s across
the same dataset break).

## B. goopg — forced legacy-DP arms (`GOOPG_PGSHAPED_DP=0`, diagnostic)

These deliberately disable the PG-shaped join search — they are the
"plans NOT trying to look like PG" side of the comparison, and the
cost of that is visible directly (Q9 always hits the 600 s cap):

| Q | 09-18 p0e7-on | 09-20 m0143-0010-b | 09-22 m0145-0020a |
|---|---|---|---|
| Q1 | — | 5.37 | 5.02 |
| Q2 | — | 5.20 | 6.23 |
| Q3 | — | 2.56 | 2.51 |
| Q5 | — | 9.13 | 10.53 |
| Q7 | — | 5.02 | 7.11 |
| Q8 | — | 18.73 | 19.25 |
| Q9 | **T 600** | **T 600** | **T 600** |
| Q12 | — | 4.24 | 4.02 |
| Q18 | — | 12.74 | 13.86 |
| Q21 | — | 18.76 | 4.80 |

(Full tables in `analysis/m0142/p0e7-admitsemianti-{on,off}.txt`,
`tmp/arm-m0143-0010-b.txt`, `tmp/m0145-0020a-accept.txt`. Q9 = NL
query the legacy enumerator cannot order; `GOOPG_PGSHAPED_DP=0` costs
Q9 alone >600 s — the flagship datum for why the PG-shaped search
exists. Note these run the **same executor**, so the delta is purely
plan quality.)

## C. Serial-pinned records (mpwg=0 — true serial plans)

| Date | Record | Values |
|---|---|---|
| 09-03 | `ex0-02-20260903` Q6-only, S-cold | 5.88/7.30/6.31/5.48/5.48/5.49 → headline 5.48 s (vs 0.86 s parallel-capable on 09-06) |
| 09-06 | e11 `s0-r1` subset (Q1,3,6,12,14,19,22; 900 s cap) | Q1 16.52, Q3 9.23, Q6 3.77, Q12 15.05, Q14 2.05, Q19 9.41, Q22 0.63 |
| 09-07 | spill-calibration `serial-{pre,post}` (5-q subset) | in `analysis/planner-spill-cost-calibration/cut3-deferred-20260907/` |

Serial-vs-parallel contrast for the same query/engine: **Q6** 5.48 s
(serial) vs 0.86 s (parallel-capable), **Q1** 16.5 s vs 8.8 s,
**Q3** 9.2 s vs 2.8 s — the Gather paths the planner elects are worth
~2–6× on the big scans.

## D. Warm-pair reference, 09-06 (`bench/tpch/timings/`)

One discarded pass + two measured passes on the same server
(pass-2 sees older server state — cite pairs, not singles).
Both engines `work_mem=64MB`, `shared_buffers=2GB`, parallel-capable.

| Q | goopg p1 | goopg p2 | PG p1 | PG p2 |
|---|---|---|---|---|
| Q1 | 7.84 | 7.72 | 0.74 | 0.72 |
| Q5 | 4.05 | 4.13 | 0.33 | 0.34 |
| Q9 | 6.86 | 6.81 | 0.46 | 0.43 |
| Q12 | 13.04 | 12.63 | 0.41 | 0.42 |
| Q13 | 5.94 | 5.87 | 1.51 | 1.46 |
| Q18 | 35.04 | 34.03 | 6.10 | 6.24 |
| Q21 | 14.09 | 13.72 | 0.55 | 0.66 |
| **Total (21)** | 107.4 | 105.5 | 14.5 | 14.7 |

(Full 21-query tables in the files themselves.)

## E. PG 18.3 references

| Date | Record | Total | Notes |
|---|---|---|---|
| 09-06 | `timings/20260906-warm-pg.txt` | 14.7 s (21 labels) | warm pair, see D |
| 09-08 | `optimize-row-decode/measurements/tpch-pg18-baseline.txt` | ~10.5 s | cold-ish single pass; Q18 5.94, Q13 1.40, rest <1.5 s |

PG's per-query dominance is broad but concentrated: on 09-08 PG beats
goopg's *after* arm by ≥10× on Q12 (0.42 vs 9.05), Q15b (1.10 vs
22.64), Q18 (5.94 vs 22.59), Q21 (0.55 vs 11.36). By 09-24 the goopg
side had closed Q12 (1.83 s) and Q18 (6.82 s) substantially; Q15b
(~12 s vs PG ~1.1 s) and Q13 (~4.5 s vs ~1.4 s) remain the big gaps.

## F. Record inventory (by date)

- **09-02**: report-level only — `analysis/planner-refactor-take2/perf-*-20260902.md`
  (arm totals 288→258→249→**403** (P0-12)→395→**246**; per-query
  callouts Q14 11.76→0.46, Q9 87.2→12.9, Q7 31.9→9.6)
- **09-03**: `acceptance-20260903` (totals 245.7→213.8; wm4 arm
  215.6→265.4), `ex0-0{2,4,5,6}-20260903` single-focus records
  (ex0-02 Q6 is `mpwg=0` S-cold serial — see §C)
- **09-04**: `planner-refactor-take3/wrapup-20260904/PERF-REPORT.md`
  + per-slice READMEs (Q9 serial 63.8→14.7→10.8 progression)
- **09-05**: `tmp/d04/arm-*` (Q9 probes), `tmp/d05{,p2,p3}/*` sweeps,
  `a04-baseline-20260905` full table, `PERF-REPORT-20260905_ja.md`
- **09-06**: warm pair (above), e11 depth sweep (15 d-arms + 8 s-arms),
  `minimize-datum/d05-buildcost` (6 arms), `tmp/d05p4/*`
- **09-07**: take3 acceptance 4-file warm/scold × before/tip set,
  spill-calibration 9 arms, tracke census (Q9 ERROR 600 s),
  `tmp/c19*`/`c20b*` arms
- **09-08**: optimize-row-decode measurements (above), `fix-pworker-bug/tpch-ab.txt`
- **09-09 → 09-13**: no TPC-H timing records (TPC-DS sweeps only)
- **09-14**: r128 `sf1-values-{ON,OFF}`
- **09-15 → 09-17**: none (:65433 lost; 09-18 host OOM)
- **09-18**: `m0142/p0e7-admitsemianti-{on,off}` (DP=0)
- **09-19**: `tmp/acceptance-{base,staged}` (old dataset, last records)
- **09-20**: `arm-on-20260920`, `arm-headbase-20260920`, `arm-m0143-*`,
  `acc-*`, `arm-m0144-*` — new dataset era begins
- **09-21**: `arm-on-20260921-loop{41..50}`, `arm-m0145-*`, `arm-q4-*`,
  `arm-{semicost,nligate,classify,census,...}` (~40 files)
- **09-22**: `arm-on-20260922-loop{53..77}` (~16), `m0145-0020a*`,
  `m0145-0004-*` (DP=0 arms), `m0145-0004-on`
- **09-23**: 19-arm acceptance chain (`ra`→`src`), `s2b{16,17}`,
  `m0145-00{18,21,26,27,28,30}` arms, `m0122-*` arms
- **09-24**: `s2b4{a,b,c,d}`, `nullkey`, `reindex`, `discard`, `pub`,
  `nk`, `desc`, `s3`, `idxdef-collate`, `ssl-gucs`, `m0145-0008a`
  (final gate PASS)

Duplicate subtree: `tmp/m0145-0020a-ea-head/` is a 09-22 snapshot
containing byte-identical copies of many of the above.
