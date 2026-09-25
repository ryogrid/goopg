# TPC-DS — Per-Query Execution-Time Trend

## A. goopg SF0.25 sweep series (primary trend data)

Source: `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-*.txt`
(300 files, 2026-09-11 → 09-24). Format: one pass over all 99 queries,
elapsed ms per query, correctness verdict (rows + value checksum vs
`oracle.txt`), per-query cap **300 s**. All runs are **natural
election** (`GOOPG_PGSHAPED_DP=unset(on)`), parallel-capable
(`GOOPG_PARALLEL=unset(on)`; `max_parallel_workers_per_gather` is the
goopg default 4 — the sf025 conf leaves the GUC commented). The
`GATHER_PATHS` stamp flips `unset(off)` → `unset(all)` mid-September
(M0140-0003 era) — noted per column below. **Every sweep ran the
legacy pinned-spine pipeline** (`JOINTREE_PIPELINE=unset(off)`) — the
series ends exactly at the M0145-0008 cutover boundary (flip unblocked
09-24, in-flight at writing). Six sweeps sampled at ~3-day intervals;
`SKIP` = oracle querygen skip (Q36/Q70/Q86).

Seconds per query (full table in `tpcds-sf025-table.md`):

- **09-11** `sweep-20260911-190106` — total **257.0 s**, GATHER_PATHS=off
- **09-14** `sweep-20260914-080244` — total **182.7 s**, GATHER_PATHS=off
- **09-17** `sweep-20260917-234551` — total **142.5 s**, GATHER_PATHS=all
- **09-20** `sweep-20260920-231524` — total **126.4 s** (−63.7% step vs
  same-day earlier sweep: Q97 ERROR→1.0 s, Q96 38→0.3 s)
- **09-23** `sweep-20260923-232800` — total **126.5 s**
- **09-24** `sweep-20260924-121026` — total **129.2 s** (first sweep of
  the work_mem=512MB conf era; run after the morning's cgroup-pressure
  incident was relieved)

### Largest movers (seconds; first → latest in-window)

| Q | 09-11 | 09-24 | Δ | Timing note |
|---|---|---|---|---|
| Q14 | 33.5 | 13.3 | −20.2 | two-statement query; progressive plan improvements |
| Q23 | 30.8 | 12.3 | −18.5 | same family as Q14 |
| Q78 | 9.1 | 4.0 | −5.1 | drop completed 09-11→09-14 — predates the M0145-0011/0018 firewall work (09-21), which slightly regressed it (§E) |
| Q6 | 6.1 | 1.4 | −4.7 | |
| Q4 | 9.5 | 10.7 | +1.2 | worse at latest — plan churn, watch item |
| Q58 | 4.7 | 1.8 | −2.9 | |
| Q59 | 6.4 | 0.7 | −5.7 | |
| Q99 | 2.5 | 0.3 | −2.2 | |
| Q74 | 4.8 | 1.6 | −3.2 | drop completed 09-11→09-14 — predates the CTE-fallback A/B (09-23, §D) |

### Same-day spread (jitter gauge)

300 sweeps include many same-day reruns; adjacent sweeps typically move
individual queries ±20–30% and totals a few %. The status-delta channel
treats ≥2× (floor 5 s) as real. Examples: Q4 oscillated 6.0–10.7 s and
Q7 0.7–2.3 s across the window without verdict changes.

## B. goopg SF0.5 records

| Date | Record | Result |
|---|---|---|
| 09-06 | `bench/tpcds/timings/20260906-warm-goopg-sf05.txt` | total **1114.1 s** (99 ok), `work_mem=512MB` BootVal era |
| 09-07 | `analysis/planner-refactor-take3/c20a-estimator-census-20260907/ea-capture` | EXPLAIN ANALYZE ETs; total dominated by Q79 127 s, Q4 87.7 s, Q64 52.4 s (SF0.5, commit 542a6b765) |
| 09-08/09 | `docs/design/not_ralph/plan_parity_fix_take2/r{1,2,3,6,7,9,25,27}/values-tpcds-*.txt` | 300 s sweeps; PASS=95 SKIP=4 (Q4 oracle-timeout + querygen) — per-query tables in files |
| 09-15 | `c20a-estimator-census-20260915/ea-capture*` | SF0.25 ETs + two post-fix recaptures (Q76 error in post0009fix) |

## C. PostgreSQL 18.3 references

| Date | Record | Result |
|---|---|---|
| 09-06 | `timings/20260906-warm-pg-sf05.txt` | SF0.5 total **584.9 s** (98 ok, Q4 FAIL >300 s) — `work_mem=4MB` (PG default era!), ran concurrently with the goopg capture |
| 09-11 | `tpcds-results-sf025/oracle.txt` | SF0.25 plain-run secs; notables: Q1 15.2 s, Q4 35.7 s, Q6 33.4 s, **Q74 53.5 s**, Q14 8.2 s |

goopg-vs-PG on SF0.25 (latest sweep vs oracle): goopg wins most
mid-size queries (Q74 1.6 vs 53.5 s, Q6 1.4 vs 33.4 s, Q4 10.7 vs
35.7 s) and loses on few — the gap is concentrated in Q14/Q23
(goopg ~13 s vs PG ~8 s) and Q38/Q41/Q78 (goopg 1.7/6.4/4.0 s vs
PG 0.3/1.8/0.5 s).

## D. Forced/knob A/B timing records (deliberate plan perturbation)

| Date | Record | Arm | Result |
|---|---|---|---|
| 09-23 | `tmp/m0145-0024/on/sweep-20260923-213658.txt` | `GOOPG_CTE_ROWS_FALLBACK=on` (subsetting Q74) | **Q74 2.27 s** |
| 09-23 | `tmp/m0145-0024/off/sweep-20260923-213744.txt` | fallback off | **Q74 29.61 s** — still faster than PG's 53.49 s on the same collapsed plan; basis for the owner decision to retire the guard for plan parity |
| 09-21 | `tmp/sf025-knob-arm/sweep-20260921-{143121,150532}.txt` | `GOOPG_JOINTREE_PIPELINE=1` (full 99-q sweeps) | executed jointree-first preview arms — per-query ms in files |
| 09-21 | `tmp/m0145-0004-sweep-on/sweep-20260921-042855.txt` | `GOOPG_JOINTREE_PIPELINE=1` | executed knob arm, full sweep |
| 09-21 | `tmp/m0145-0011/sweep-20260921-182447.txt` | `JOINTREE_PIPELINE=1` + `DERIVED_FIREWALL=off` | the executed arm behind §E's Q77/Q78 re-measure |
| 09-18 | `analysis/m0142/p0e7-admitsemianti-{on,off}` | `GOOPG_PGSHAPED_DP=0` legacy DP (TPC-H — see 01 file) | Q9 timeout both arms |

Note: knob arms that only produce EXPLAIN output
(`JOINTREE_PIPELINE=1` plansknob-*.txt, 51 files under
`tpcds-results-sf025/`) carry no execution times by design (G8 rule).

## E. Isolated SF=1 timing citations (fix_plan.md; no sweep files)

- **M0145-0011** (09-21): SF1 Q77 9015→5306 ms, Q78 50885→48066 ms with
  `outer-over-derived` firewall off; SF0.25 re-measure Q77 788→851 ms,
  Q78 3869→3931 ms.
- **M0145-0018 no-go** (09-21): SF1 Q78 29.6 s → DNF>1800 s on the
  C-04a NL+Join-Filter shape; SF0.25 clean — the incident that kept
  the firewall.
- **M0137-0012 B6** (09-15): Q72 4 s → 320 s TIMEOUT when the NL probe
  path lost Memoize under R59 repricing.
- **M0141-S2b-13** (09-18): SF0.25 sweep total 154→149 s; cites the
  original M0129-S1 gate Q74 99→14 s at SF0.5.

## F. Record inventory

- `bench/tpcds/runtime_goopg/tpcds-results-sf025/sweep-*.txt` — **300
  files** 09-11→09-24 (per-day counts: 09-11×3, 09-12×3, 09-14×9,
  09-15×8, 09-16×20, 09-17×35, 09-18×31, 09-19×24, 09-20×24,
  09-21×58, 09-22×35, 09-23×31, 09-24×19)
- Executed knob-arm sweeps under `tmp/`: `sf025-knob-arm/` (×2),
  `m0145-0004-sweep-on/`, `m0145-0011/`, `m0145-0024/{on,off}/` (§D)
- `bench/.../tpcds-results-sf025/plansknob-*.txt` — 51 EXPLAIN-only
  knob-arm captures, no times by design (G8)
- `docs/design/not_ralph/plan_parity_fix_take2/*/values-tpcds-sf05-*.txt`
  — 10 SF0.5 sweeps 09-08/09 (PASS=95 SKIP=4; r25-slice1 had 2
  TIMEOUTs before the ctefix sweep restored 95)
- `bench/tpcds/timings/20260906-warm-{goopg,pg}-sf05.txt` — warm pair
- `analysis/planner-refactor-take3/c20a-estimator-census-*/ea-capture-*.txt`
  — EXPLAIN ANALYZE ETs (SF0.5 09-07, SF0.25 09-15 ×3)
- `tmp/m0145-0024/{on,off}/sweep-*.txt` — Q74 subset probes (§D)
- No in-window SF1 sweep files; pre-window SF1 corpus:
  `analysis/tpcds-sf1-resweep-20260728/RESULTS.md` (07-28)
