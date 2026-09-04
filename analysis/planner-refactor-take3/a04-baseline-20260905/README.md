# A-04 baseline roll-up (bars A1/A2 starting numbers, 2026-09-05)

```
code: 4cf76db0b (cut-3 behavior; A-05 instrument-only since)
regime: fresh server per arm, GOGC=100/GOMEMLIMIT=12GiB, cgroup cap,
  S-cold serial, work_mem 64MB; TPC-H port 65433 tpch@tpch SF=1
```

## TPC-H SF=1 power timings (serial, values 24/24 MATCH)

| Q | s | Q | s | Q | s |
|---|---|---|---|---|---|
| Q1 | 17.17 | Q9 | 11.04 | Q17 | 0.52 |
| Q2 | 0.74 | Q10 | 7.70 | Q18 | 51.77 |
| Q3 | 20.00 | Q11 | 0.14 | Q19 | 8.45 |
| Q4 | 1.55 | Q12 | 12.75 | Q20 | 1.29 |
| Q5 | 32.12 | Q13 | 4.89 | Q21 | 12.79 |
| Q6 | 3.27 | Q14 | 1.78 | Q22 | 0.64 |
| Q7 | 11.71 | Q15b | 21.26 | Q16 | 0.99 |
| Q8 | 1.15 | | | | |

TOTAL ≈ 235 s (single-sample; ±17% band per ground rule 4 — a timing
claim inside the band is not a claim).

## TPC-DS SF0.5 sweep (values PASS=95 MISMATCH=0, SKIP=4)

Report: `/tmp/opencode/a04/ds/sweep-20260905-054518.txt` (run-local;
per-query attribution in the sweep file). Binary `tmp/goopg-a04base`
= HEAD code at roll-up time.

## Plan-parity starting roll-up (A-03 tool, same code)

`PLAN-PARITY: queries=22 match=5 shapediff=15 missingnode=2 error=0
timeout=0` (missing-node = PG-only Materialize: Q5/Q8).
Per-category monotone decrements (take3 09 §5) enforced from these
numbers; the budget is pinned in `scripts/pg-plan-parity-diff-test.py`.
