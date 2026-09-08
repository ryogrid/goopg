# Cut 3 wiring (derived spill byte model) — MEASURED, DEFERRED on the parallel keystone

```
label: spill-cut3-deferred | date: 2026-09-07
patch: cut3.patch (this dir) — applies to 16da44c66, unit-green
design: docs/design/planner-spill-cost-calibration/DESIGN.md (Cut 3, §6.2)
```

## What it is

`spillPages` split into `spillPagesInner` / `spillPagesOuter`, charging the
hash join's batch-file I/O through `hashsize.SpillInnerBytes` /
`hashsize.SpillBytes` — the on-disk model that transcribes what
`internal/executor/spill.go` writes — instead of `hashsize.EntryBytes`, which
measures the in-memory entry. `hashsize.Choose` is untouched, so the spill
DECISION still mirrors `joinOp.buildGeometry` (DESIGN §3.4). Retires the
ledgered approximation M0127-P5.7-a.

Three new arm tests (`spill_charge_test.go`) pin: each side charged through
its own writer's model; the correction is a 1.2x–5x RATIO, not a constant
(so a scalar multiplier is refuted at unit level, not only on paper); and the
spill decision does not move — asserted on a witness shape whose in-memory
footprint overflows the budget while its on-disk footprint fits.

## Measurement (TPC-H SF=1, shipped planner defaults, work_mem 64MB)

Private clone `/tmp/spill-data`, port 5561, fresh capped server per arm,
`GOOPG_ANALYZE_SEED=20260905`, autovacuum off, GOGC=off GOMEMLIMIT=12GiB.
Four interleaved arms (pre, post, pre, post) because a single A/B was
contaminated: the A/A control measured **-14.8% of pure warm-up drift** on the
suite total, with per-query swings to -37%. Figures below are best-of-2 per
arm, which cancels it.

| | best-of-2 pre | best-of-2 post | delta |
|---|---|---|---|
| TOTAL | 135.87 s | 111.20 s | **-18.2%** |
| Q12 (moved) | 13.45 | 5.18 | **-61.5%** |
| Q18 (moved) | 34.83 | 16.71 | **-52.0%** |
| Q7 (moved) | 5.82 | 4.99 | -14.3% |
| Q13 (moved) | 5.18 | 4.70 | -9.3% |
| Q9 (moved) | 6.06 | 9.85 | **+62.5%** |

Values 24/24 MATCH on VALUES in every arm. Five queries move shape
(Q7, Q9, Q12, Q13, Q18); all five were timed individually.

## Why it cannot land: Q9 is the parallel trap, not a ranking error

Q9's `Parallel Seq Scan` moves off `lineitem` (6.0M rows) onto `orders`
(1.5M), so each of the 4 workers re-scans the whole of `lineitem` instead of
its quarter. DESIGN §7 predicted exactly this class ("goopg's cost model has
no parallel dimension; `drivingScan` admits only a hash join under a Gather").

Proof that the spill model's RANKING is right and the loss is the unpriced
parallel dimension — same queries, `-parallel-workers 0`:

| Q | serial pre | serial post | |
|---|---|---|---|
| Q9 | 22.32 s | 19.06 s | **-14.6%** |
| Q12 | 14.91 | 13.03 | -12.6% |
| Q7 | 13.09 | 12.45 | -4.9% |
| Q13 | 4.75 | 4.76 | +0.2% |
| Q18 | 33.73 | 40.30 | +19.5% |

With parallelism removed Q9 is 14.6% FASTER post-change. The +62% is
therefore attributable to Gather placement, which C-19g / C-19h owns, not to
this charge. B2 (>1.2x per-query) fails on Q9 alone, so the wiring is held
rather than shipped — the same call the D-05 chain made when three correct
hash-join cost fixes each lost a Gather.

## The result that matters for B-13

At `work_mem = 4MB` (B-13's target, i.e. PG's real default), on top of E-16's
landed plumbing:

| arm | TOTAL | vs 64MB baseline |
|---|---|---|
| 64MB baseline | 140.02 s | — |
| 4MB, no calibration | 174.84 s | **+24.9%** |
| 4MB, with Cut 3 | 135.85 s | **-3.0%** |

Cut 3 recovers **-22.3%** of the 4MB suite. The catastrophes collapse:
Q14 16.53 -> 0.91 s, Q10 9.62 -> 3.00, Q3 8.41 -> 4.40, Q7 16.36 -> 9.50.
So the calibration IS the instrument B-13 needs, and it is confirmed as such
by measurement rather than by argument — but B2 still fails at 4MB on
Q2/Q3/Q7/Q8/Q9/Q14/Q16, so B-13 does not clear its bar either.

## A trap that voided a first round of measurement, recorded

The first four captures showed Cut 3 moving NOTHING — zero cost lines across
all 22 queries, at both 64MB and 4MB. The cause: the arm scripts exported
`GOOPG_PGSHAPED_DP=0`, while the shipped default (`scripts/planner-flags.env`)
is `unset(on)`. With the DP search off, `hashJoinCost` is never called at all
— instrumenting the function proved 0 calls, then 767 with the flag at its
default. `scripts/tpch-acceptance-arm.sh` defaults `PGSHAPED=0` for the same
reason it tells you to set it explicitly; a spill calibration measured on that
arm is measuring a planner that never prices a spill.

## Resume point

1. Re-measure on top of C-19g/C-19h once the parallel dimension is priced
   (`PathGather`/`PathGatherMerge`). The only question outstanding is Q9;
   everything else in the arm is a win or inside noise.
2. If Q9 still regresses with the Gather priced, the fault is `drivingScan`'s
   admission rule, not this charge — serial Q9 is already 14.6% faster.
3. Then B-13 (re-run the 4MB arm above; needs the seven >1.2x queries to come
   inside the bar) and B-15 in that order.
4. Not attempted here: Cut 2 (sort spill trigger). It is now LIVE where the
   design said it was inert — `costSortRun` has gained three more production
   callers (`partialsortpaths.go` x2, `windowsetoppaths.go`) plus the upper-rel
   sorts, so §6.1's "one caller, merge-join input sorts only" is stale. Note
   the sign of its error INVERTS with work_mem: `cp.workMem` is 128 MiB at
   bench (below `sortChunkBytes` 256 MiB, so the model over-charges) and 1 GiB
   at the default (above it, so it under-charges, as the design says).

Ledger: `spill-cut3-deferred`.
