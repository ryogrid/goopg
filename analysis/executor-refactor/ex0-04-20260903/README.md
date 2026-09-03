# EX0-04 published slices — witness shapes, serial S-cold

Harness: `bench/tpch/profile_slices.sh` + `bench/tpch/profile_slices_classify.py`
(stack-based `-traces` attribution; design
`docs/design/executor-ex0-04-harness/DESIGN.md`).

```
label: EX0-04-witness-slices | date: 2026-09-03 (22:22–22:36 JST)
goopg: f0e74aa24 +dirty (foreign WIP; binary tmp/goopg-ex002 built from tree)
suite: TPC-H SF=1 (Q6/Q9/Q13/Q4/Q7) | regime: stats=S-cold start, parallel=off
GOGC/GOMEMLIMIT: 100/12GiB (explicit override) | cgroup scope ex004 20G/24G/0
work_mem/effective_cache_size: 64MB/2GB | port 65433, pprof 6161
collection: 40 s windows (Q6 ×3, Q13/Q4/Q7 60 s), serial, one server per arm
```

## Q6 anchor (re-cut on current tree — take7 is reference only)

| run | prefilter | filterOp | residual-ratio | value |
|---|---|---|---|---|
| 1 | 22.69% (9.54s) | 3.35% (1.41s) | 12.88% | 102513054.4896 |
| 2 | 22.86% (9.68s) | 2.57% (1.09s) | 10.12% | identical |
| 3 | 23.33% (9.37s) | 2.86% (1.15s) | 10.93% | identical |

Anchor: residual-ratio **11.3%** (mean), filterOp **2.93%** of total (mean).
Observed run spread ±1.4pp ratio — wider than the design's proposed ±1pp,
so the gate calibrates from data: future runs pass within **±2pp absolute
of 11.3%** (ratio) and mean-of-3 within **±0.5pp of 2.93%** (pp-of-total).
Null control holds all runs: join/sort/spill = 0.00s.

## Witness slices (% of window CPU; `excluded` = futex/usleep sleep, stated)

| shape | decode | prefilter | filterOp | clone | probe | sort | spill | excl | other |
|---|---|---|---|---|---|---|---|---|---|
| Q6 (mean) | 39.9 | 22.9 | 2.9 | 1.9 | 0 | 0 | 0 | 9.4 | 23.5 |
| Q9 | 30.2 | 0.2 | 2.5 | 21.4 | 20.0 | 0 | 3.3 | 7.6 | 14.9 |
| Q13 | 9.5 | 0 | 8.3 | 11.1 | 22.8 | 0 | 9.4 | 25.2 | 13.6 |
| Q4 | 12.8 | 13.3 | 1.9 | 1.3 | 59.0 | 0 | 0 | 0.5 | 11.2 |
| Q7 | 14.8 | 5.0 | 2.5 | 11.2 | 13.1 | 0 | 10.3 | 20.3 | 22.8 |

Readings: Q9 clone 21.4% confirms G-EX2's Q9 weight; Q4 probe 59% is the
NLI/index-descent path (classifier gap found and fixed in-run — missing
it read 0% probe / 70% other); Q13/Q7 spill 9–10% at 64 MB work_mem;
sort-compare ~0 on all five (sorts are small-group; EX3's q16 case is the
sort witness, not these shapes). `other` = operator dispatch (`Next`
1.0s on Q6), runtime (memclr/mallocgc), visibility, storage scaffolding —
top entries recorded per run, no slice hiding (verified `--other-top`).

## G-EX6 type-by-type remainder list (empirical, from Q6 decode subtree)

Decode-subtree cut (9.28 s window): int/float **65.6%**, numeric **32.4%**,
date/time **1.1%**, text/TOAST **0.9%**. Remainder for EX1: numeric
compare/arith tails past the landed text→numeric fast path, int/float
decode bulk, date/time + text/TOAST-pointer (Q6 touches all four families;
TOAST-heavy TPC-DS shapes still to witness EX1-03). Filed as the empirical
input to EX1-01/02 scoping.

## Pin

`git diff --stat` on the close commit: bench/docs-only (no `internal/…`).
Value-identity held on every collection (Q6 ×3, Q9, Q13, Q4, Q7 — pinned
`diff`, not eyeballed). plan-gate 2026-09-03: **22/22 MATCH, changed=0**
(TPC-H, restarted server, bench/docs-only tree).
