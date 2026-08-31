# Parallel sort work — measured effect on TPC-H and TPC-DS

Covers `6fa1f400d` (gather-merge NULL-ordering fix) and `M0134-0191`
(precomputed sort keys). Design and rationale:
[`DESIGN.md`](./DESIGN.md).

## Method

| | |
|---|---|
| before | `cce5a1bbc` — parallel Index Only Scan, pre-sort-work |
| after | this branch — NULL fix + precomputed sort keys |
| TPC-H | SF=1, goopg :65433 vs PG 18.3 :65432 (db `tpch`). Fresh server per arm through `scripts/goopg-test-run.sh` (`GOGC=100 GOMEMLIMIT=12GiB`), S-cold, one run per query, machine quiescent. |
| TPC-DS | SF=0.5, `scripts/tpcds-sf05-regression.sh sweep`. PG times from the git-tracked oracle (integer seconds, so sub-second PG queries read `0s` and no ratio is formed — shown `—`). |

Single-run wall clock: treat anything under ~10 % as noise.

## TPC-H

| query | PG | before | after | Δ | after ÷ PG |
|---|---:|---:|---:|---:|---:|
| q01 | 2.4s | 8.5s | 8.3s | -2.4% | 3.5x |
| q02 | 2.1s | 2.7s | 1.0s | -63.0% ⬅ | 0.5x |
| q03 | 0.9s | 3.3s | 3.3s | +0.0% | 3.7x |
| q04 | 0.2s | 1.8s | 2.0s | +11.1% | 10.0x |
| q05 | 0.4s | 36.7s | 37.0s | +0.8% | 92.5x |
| q06 | 0.3s | 0.8s | 0.8s | +0.0% | 2.7x |
| q07 | 0.5s | 22.4s | 22.7s | +1.3% | 45.4x |
| q08 | 0.5s | 0.6s | 0.6s | +0.0% | 1.2x |
| q09 | 0.6s | 47.7s | 49.0s | +2.7% | 81.7x |
| q10 | 0.5s | 3.2s | 3.0s | -6.3% | 6.0x |
| q11 | 0.2s | 0.2s | 0.1s | -50.0% ⬅ | 0.5x |
| q12 | 0.5s | 14.9s | 14.7s | -1.3% | 29.4x |
| q13 | 1.5s | 5.4s | 5.1s | -5.6% | 3.4x |
| q14 | 0.2s | 0.5s | 0.5s | +0.0% | 2.5x |
| q16 | 0.3s | 1.4s | 0.9s | -35.7% ⬅ | 3.0x |
| q17 | 1.8s | 0.6s | 0.6s | +0.0% | 0.3x |
| q18 | 6.1s | 59.7s | 60.6s | +1.5% | 9.9x |
| q19 | 0.1s | 2.5s | 2.6s | +4.0% | 26.0x |
| q20 | 0.3s | 1.4s | 1.5s | +7.1% | 5.0x |
| q21 | 3.4s | 11.7s | 11.9s | +1.7% | 3.5x |
| q22 | 0.1s | 0.8s | 0.8s | +0.0% | 8.0x |
| **total** | **22.9s** | **226.8s** | **227.0s** | **+0.1%** | **9.9x** |

Read this table honestly:

- **q16 1.4s → 0.9s (−36 %) is the result.** It is the query the work
  targeted, it was measured twice in independent A/B runs (−36 % and −35.7 %),
  and it puts goopg at **3.0x** PG on q16 rather than 4.7x.
- **The suite total does not move** (226.8s → 227.0s, +0.1 %). Three queries
  the sort work does not touch — q18 60s, q09 49s, q05 37s — are 64 % of the
  total, so a large win on a 1.4s query is invisible in the sum. Anyone
  quoting a headline number from this work should quote q16, not the total.
- **q02 2.7s → 1.0s should be discounted.** Earlier runs put q02 at 1.6–2.0s,
  so the 2.7s "before" is a single-run outlier and the true gain is smaller.
  It is left in the table as measured rather than silently re-run.
- q11 0.2 → 0.1s is at timer resolution; it is *nominally* faster than PG but
  the measurement cannot support the claim.
- Everything else moves within noise. The change is executor-only and **no
  plan moved**: all 21 EXPLAIN outputs are byte-identical to before, and all
  21 result sets are byte-identical to the reference.

## TPC-DS SF=0.5

Both sweeps: `PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4`.

| query | PG | before | after | Δ | after ÷ PG |
|---|---:|---:|---:|---:|---:|
| Q1 | 53s | 1s | 1s | +0% | 0.0x |
| Q2 | 0s | 15s | 15s | +0% | — |
| Q3 | 0s | 1s | 1s | +0% | — |
| Q4 | TIMEOUT (PG TIMEOUT) | skipped | skipped | — | — |
| Q5 | 1s | 18s | 18s | +0% | 18.0x |
| Q6 | 69s | 16s | 16s | +0% | 0.2x |
| Q7 | 2s | 10s | 10s | +0% | 5.0x |
| Q8 | 1s | 8s | 8s | +0% | 8.0x |
| Q9 | 1s | 23s | 23s | +0% | 23.0x |
| Q10 | 11s | 13s | 13s | +0% | 1.2x |
| Q11 | 53s | 17s | 17s | +0% | 0.3x |
| Q12 | 1s | 3s | 3s | +0% | 3.0x |
| Q13 | 1s | 9s | 9s | +0% | 9.0x |
| Q14 | 16s | 111s | 111s | +0% | 6.9x |
| Q15 | 0s | 5s | 5s | +0% | — |
| Q16 | 0s | 15s | 15s | +0% | — |
| Q17 | 1s | 10s | 10s | +0% | 10.0x |
| Q18 | 0s | 24s | 24s | +0% | — |
| Q19 | 0s | 0s | 0s | 0% | — |
| Q20 | 0s | 6s | 6s | +0% | — |
| Q21 | 0s | 1s | 1s | +0% | — |
| Q22 | 2s | 10s | 10s | +0% | 5.0x |
| Q23 | 1s | 78s | 78s | +0% | 78.0x |
| Q24 | 0s | 14s | 14s | +0% | — |
| Q25 | 1s | 5s | 5s | +0% | 5.0x |
| Q26 | 0s | 7s | 7s | +0% | — |
| Q27 | 0s | 10s | 10s | +0% | — |
| Q28 | 1s | 31s | 31s | +0% | 31.0x |
| Q29 | 0s | 6s | 6s | +0% | — |
| Q30 | 4s | 0s | 0s | 0% | 0.0x |
| Q31 | 4s | 14s | 14s | +0% | 3.5x |
| Q32 | 0s | 5s | 5s | +0% | — |
| Q33 | 1s | 15s | 15s | +0% | 15.0x |
| Q34 | 0s | 7s | 7s | +0% | — |
| Q35 | 0s | 18s | 18s | +0% | — |
| Q36 | SKIP_QUERYGEN (no query generated) | skipped | skipped | — | — |
| Q37 | 0s | 3s | 3s | +0% | — |
| Q38 | 2s | 15s | 15s | +0% | 7.5x |
| Q39 | 2s | 15s | 15s | +0% | 7.5x |
| Q40 | 0s | 2s | 2s | +0% | — |
| Q41 | 2s | 8s | 8s | +0% | 4.0x |
| Q42 | 0s | 0s | 0s | 0% | — |
| Q43 | 0s | 2s | 2s | +0% | — |
| Q44 | 1s | 5s | 5s | +0% | 5.0x |
| Q45 | 0s | 3s | 3s | +0% | — |
| Q46 | 0s | 10s | 10s | +0% | — |
| Q47 | 2s | 14s | 14s | +0% | 7.0x |
| Q48 | 0s | 8s | 8s | +0% | — |
| Q49 | 0s | 16s | 16s | +0% | — |
| Q50 | 0s | 1s | 1s | +0% | — |
| Q51 | 1s | 12s | 12s | +0% | 12.0x |
| Q52 | 0s | 0s | 0s | 0% | — |
| Q53 | 0s | 1s | 1s | +0% | — |
| Q54 | 10s | 15s | 15s | +0% | 1.5x |
| Q55 | 0s | 0s | 0s | 0% | — |
| Q56 | 0s | 14s | 14s | +0% | — |
| Q57 | 1s | 7s | 7s | +0% | 7.0x |
| Q58 | 0s | 22s | 22s | +0% | — |
| Q59 | 1s | 18s | 18s | +0% | 18.0x |
| Q60 | 0s | 16s | 16s | +0% | — |
| Q61 | 0s | 3s | 3s | +0% | — |
| Q62 | 0s | 3s | 3s | +0% | — |
| Q63 | 1s | 1s | 1s | +0% | 1.0x |
| Q64 | 0s | 41s | 41s | +0% | — |
| Q65 | 0s | 16s | 16s | +0% | — |
| Q66 | 1s | 8s | 8s | +0% | 8.0x |
| Q67 | 3s | 17s | 17s | +0% | 5.7x |
| Q68 | 1s | 8s | 8s | +0% | 8.0x |
| Q69 | 0s | 16s | 16s | +0% | — |
| Q70 | SKIP_QUERYGEN (no query generated) | skipped | skipped | — | — |
| Q71 | 0s | 16s | 16s | +0% | — |
| Q72 | 1s | 82s | 82s | +0% | 82.0x |
| Q73 | 0s | 7s | 7s | +0% | — |
| Q74 | 256s | 12s | 12s | +0% | 0.0x |
| Q75 | 1s | 13s | 13s | +0% | 13.0x |
| Q76 | 0s | 2s | 2s | +0% | — |
| Q77 | 0s | 15s | 15s | +0% | — |
| Q78 | 2s | 17s | 17s | +0% | 8.5x |
| Q79 | 0s | 7s | 7s | +0% | — |
| Q80 | 0s | 15s | 15s | +0% | — |
| Q81 | 15s | 0s | 0s | 0% | 0.0x |
| Q82 | 0s | 3s | 3s | +0% | — |
| Q83 | 0s | 3s | 3s | +0% | — |
| Q84 | 0s | 1s | 1s | +0% | — |
| Q85 | 1s | 2s | 2s | +0% | 2.0x |
| Q86 | SKIP_QUERYGEN (no query generated) | skipped | skipped | — | — |
| Q87 | 0s | 16s | 16s | +0% | — |
| Q88 | 1s | 52s | 52s | +0% | 52.0x |
| Q89 | 0s | 2s | 2s | +0% | — |
| Q90 | 0s | 4s | 4s | +0% | — |
| Q91 | 0s | 4s | 4s | +0% | — |
| Q92 | 0s | 2s | 2s | +0% | — |
| Q93 | 0s | 2s | 2s | +0% | — |
| Q94 | 0s | 6s | 6s | +0% | — |
| Q95 | 6s | 13s | 13s | +0% | 2.2x |
| Q96 | 0s | 6s | 6s | +0% | — |
| Q97 | 0s | 13s | 13s | +0% | — |
| Q98 | 0s | 5s | 5s | +0% | — |
| Q99 | 1s | 7s | 7s | +0% | 7.0x |
| **total (95)** | **536s** | **1173s** | **1173s** | **+0.0%** | **2.2x** |

TPC-DS is unchanged in total (1173s both sides) — its queries are not
sort-bound in the way q16 is, and the sweep's integer-second resolution
cannot show sub-second movement anyway.

## Queries that do not complete on PG

TPC-DS **Q4 times out on PG 18.3 itself** (612 s in the oracle) and is
skipped on both sides. **Q36, Q70, Q86** are `SKIP_QUERYGEN` — never
generated for this dataset. TPC-H q15 is outside the 21-query harness.
Everything else completes on both engines.

## What did not work, and is not shipped

Re-enabling P7 so `Sort` becomes the partial root — PG's
`Gather Merge → Sort → Parallel <scan>` — was implemented and **measured
worse even after the comparator got cheap**:

| query | leader-side sort | per-worker sorts |
|---|---:|---:|
| q16 | **0.9s** | 1.6s (+78 %) |
| q10 | **3.0s** | 3.4s (+13 %) |
| q13 | **4.8s** | 5.1s (+6 %) |

Stage 1 removed the sort's dominance, and with it the motivation for
splitting the sort: one leader-side sort over a parallel scan beats N
worker sorts plus a k-way merge. `sortPartialRootPays` therefore stays.
This is recorded because the earlier design predicted the opposite.
