# B-15 (P2-09b btcostestimate remainder) — DEFERRED on E1 failure

```
label: B-15-deferred | date: 2026-09-05
batch: batch.patch (this dir) — full R1-R5 implementation, unit-green,
  applies to 975ddc059
binaries: tmp/goopg-b14 (pre) vs tmp/goopg-b15 (post)
```

## Implementation (reverted, unit-green)

R1 per-tuple qual-op cost, R2 log2 descent, R3 rint÷numSA+[1,N] clamp,
R4 qual_arg_cost startup (=0, Const operands), R5 index-side ML branch
for numSA×loopCount>1. Pure cost batch (pathbitmap/pathparamindex/
planner hunks thread counts only; no path-generation change).

## Why it cannot land (values 24/24 MATCH — pure ranking failure)

14 TPC-H shapes flip toward Nested Loop; measured mid-sweep timings
(old → new, same protocol):

| Q | old shape/time | new shape/time |
|---|---|---|
| Q3 | Merge / 14.99 s | NL / 5.84 s (faster — legitimate win inside a failing batch) |
| Q10 | Gather+hash / 7.41 s | Hash, rows 359k→1.5M / 41.38 s (**5.6×**) |
| Q12 | Merge / 11.53 s | NL / 9.48 s |
| Q14 | Finalize+Gather / 1.76 s | NL 933k-outer + Memoize / 4.15 s (**2.4×**) |
| Q7 | Gather+hash / ~12 s | Hash (Gather lost) / 54.62 s (**~4×**) |
| Q9 | Hash+Gather / ~11 s | (moved) / 26.23 s (**2.3×**) |
| Q5 | (moved) / ~19 s | (moved) / 39.00 s (**2×**) |

E1/B2 (>1.2×) fails on Q5/Q7/Q9/Q10/Q14.

## Forensic mechanism hypothesis (for the resume)

R5's index-side ML pro-rating (`/ loopCount`) collapses looped-probe
page costs toward zero while the per-tuple CPU (R1) stays small; the
NL total then undercuts hash/merge on large outers (Q14: 933k probes).
PG's identical math does not pathologically prefer NL — the missing
counterweight is likely goopg's deliberately-zero heap qpqual term
(`buildInitialRels` passes numQualOps=0; costindex.go:181-187) and/or
probe calibration (`indexProbeCostMultiplier`).

## Resume point

1. Confirm R5-vs-heap-qpqual as the NL bias (unit-level: price a
   parameterized probe at loopCount 1 vs N with/without qpqual).
2. Calibrate (heap per-tuple term for probes and/or probe multiplier),
   re-gate per moved query with timing + the TPC-DS TOTAL arm
   (`scripts/tpcds-sweep-diff.py` arm 3, ±2.0%) + plans captures.
3. Consider landing R1+R2+R4 (uniform, non-shape-flipping?) separately
   only with per-term evidence — do not assume.
4. Process fix: drift checks must compare NORMALIZED plan text
   (strip cost/rows/width parentheticals), never verdicts — verdict-only
   comparison missed 10 DIFFER→DIFFER text moves here (Q5/Q7/Q9 et al).

Ledger: `take3-B-15-blocked`.
