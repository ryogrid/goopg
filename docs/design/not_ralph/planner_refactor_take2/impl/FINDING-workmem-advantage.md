# Finding — the recorded goopg-vs-PG gap was measured with goopg holding 8× the memory

Established 2026-09-02 by landing P0-12 (aligning the TPC-H bench clusters'
planner-visible memory settings) and timing both arms.

---

## 1. The measurement

The PG reference cluster (:65432) has always set `work_mem = 64MB` and
`effective_cache_size = 2GB` explicitly. The goopg bench cluster (:65433) set
neither, so both sat at goopg's boot defaults — **512MB** and **4GB**. Every
recorded goopg-vs-PG TPC-H figure was therefore produced with goopg holding an
**8× `work_mem` advantage**.

Aligning them, everything else held constant (same binary, fresh server per
arm, same server age, `GOGC=100 GOMEMLIMIT=12GiB`):

| arm | TPC-H total, 24 timed items |
|---|---|
| `work_mem = 512MB` (goopg's boot default) | 248.71 s |
| `work_mem = 64MB` (PG's setting) | **403.27 s** |

**+62.2 %.** Row counts identical on all 24 items.

## 2. Where it lands

| query | goopg 512MB | goopg 64MB | | PG @ 64MB | goopg/PG |
|---|---|---|---|---|---|
| Q14 | 0.47 s | 11.42 s | **+2330 %** | 1.08 s | **10.6×** |
| Q3 | 2.57 s | 28.68 s | **+1016 %** | 1.10 s | **26×** |
| Q16 | 0.76 s | 3.57 s | +370 % | — | — |
| Q10 | 3.80 s | 14.54 s | +283 % | 0.58 s | **25×** |
| Q2 | 0.74 s | 1.77 s | +139 % | — | — |
| Q13 | 5.12 s | 9.45 s | +85 % | — | — |
| Q9 | 49.94 s | 90.79 s | +82 % | — | — |
| Q18 | 60.53 s | 93.37 s | +54 % | — | — |
| Q7 | 21.97 s | 33.09 s | +51 % | — | — |
| Q21 | 9.86 s | 14.34 s | +45 % | — | — |
| Q5 | 40.01 s | 47.46 s | +19 % | — | — |
| Q12 | 13.74 s | 15.76 s | +15 % | — | — |

PG runs the three worst in about a second each, at the **same** `work_mem`.

## 3. What it means

**The headline was flattered.** The recorded 227.0 s / 22.9 s = 9.9× compared a
goopg with 512MB of hash memory against a PG with 64MB. At a matched setting the
ratio against PG's recorded 22.9 s is roughly **17.6×** — and that is the honest
number, because a benchmark that gives one engine eight times the memory is not
measuring the engines.

**The dominant remaining problem is not the planner.** goopg does not degrade
gracefully when a hash table stops fitting: Q14 slows 24×, Q3 11×, Q10 4×, while
PG absorbs the same constraint in about a second. Those are hash joins that now
BATCH, and the difference is spill efficiency, not plan choice — the plans are
priced correctly now (P2-02 wired `work_mem` into the planner, so the planner
knows the tables will batch), and the row counts prove the answers are right.

This sharpens the conclusion already recorded from three selectivity A/Bs
(`perf-20260902-cumulative.md` §4b): estimator fidelity was not moving TPC-H
time, and now the reason is visible. **The executor's spill path is a
first-order cost**, and 07 §6 lists it as an out-of-scope "executor-side
residual". On this evidence that classification is wrong: it is not a residual,
it is the largest single lever available.

## 4. Why the alignment is kept

Making goopg 62 % slower looks like a regression and is not one. The 512MB arm
was measuring a configuration advantage, and every comparison drawn from it —
including any judgement about whether a planner change helped — was contaminated
by it. The alignment is kept, and the 403.27 s figure becomes the new control.

`shared_buffers` is deliberately **not** aligned (PG 512MB, goopg 2048MB).
goopg's buffer arena is a Go-heap object under `GOMEMLIMIT` (M0032-0001);
shrinking it to PG's value would measure Go's garbage collector rather than the
database. It is recorded as a permitted divergence.

## 5. Why this could not have been done earlier

P0-12 was blocked on P2-02, and that ordering was established by reading the
code rather than by a failure. Until P2-01/P2-02, `work_mem` reached the
**executor** (`hashsize.EffectiveMemLimit`) but not the **planner**
(`defaultCostParams()` was hard-wired at 512MB). Setting it in the bench conf
before those landed would have made the two disagree — the planner pricing
geometries the executor would not build, which is exactly the hazard
`cost_funcs.go`'s `workMem` comment names. The measurement above is only
meaningful because planner and executor now read the same value.

## 6. Resume points

1. Re-measure the PG side in full at the current configuration, rather than
   comparing against the recorded 22.9 s from 2026-08-31.
2. Profile Q14 and Q3 at `work_mem = 64MB`. They are the cleanest cases: a
   single hash join each, PG at ~1 s, goopg at 11 s and 29 s.
3. Reclassify 07 §6's executor-side spill residual as a first-order work item
   with this evidence attached.
