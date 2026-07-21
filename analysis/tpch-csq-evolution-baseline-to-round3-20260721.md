# TPC-H SF1 — goopg Evolution Across the Correlated-Subquery Rounds

**Date:** 2026-07-21
**Scope:** runtime and plan-shape evolution of all 22 TPC-H queries across the
four measured milestones of the correlated-subquery-planning work, compared
against PostgreSQL 18.3.

| milestone | column name | goopg commit | evidence |
|---|---|---|---|
| before round 1 | `baseline` | `a91d2a8d` | [`sf1-baseline-a91d2a8d.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-baseline-a91d2a8d.txt) |
| round 1 complete (S0–S3 + S6) | `R1` | `6e8da3c0` | [`sf1-final-6e8da3c0.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-final-6e8da3c0.txt) |
| round 2 complete (S4/S5a/D6.3/S7) | `R2` | `894485ba` | [`sf1-r2-final-894485ba.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-r2-final-894485ba.txt) |
| round 3 complete (the bug-fix round) | `R3` | `bb90089a` | [`sf1-r3-final-bb90089a.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-r3-final-bb90089a.txt) |

> **On the requested five columns.** Round 3 *was* the bug-fix round — its
> whole content was closing the five open ledger rows, two of which turned out
> to be live wrong-results bugs. So "stage 3 complete" and "bug fixes complete"
> are the same measurement point, and this table shows four goopg columns
> rather than five. No intermediate sweep was taken between them, and per
> instruction nothing was re-measured for this document.

---

## 1. Read this before the numbers

Three caveats decide how much any comparison here is worth. They are stated up
front because two of them materially change what the table appears to say.

**1.1 The PostgreSQL column is not from the same data load.** PG timings come
from the 2026-07-18 plan-comparison study, taken on a *separately provisioned*
PG 18.3 cluster (port 65432) — single warm-cache samples, explicitly labelled
there as "a plan-shape study, not a benchmark".

The two clusters hold **different data**, and this is documented rather than
inferred: `bench/tpch/spotcheck_expected.env` records that HammerDB's TPC-H
build "uses a different RNG draw" per load, that Q13 is load-dependent
(`GROUP BY c_count` over a random order distribution), and that successive
reloads on this machine produced **Q13 = 36 (2026-05-11), 35 (May canonical),
and 33 (2026-06-13)**. goopg's current data is the 2026-06-13 load (Q13 = 33);
the PG reference cluster reports 35. The row-count deltas in the table are
therefore expected artefacts of different draws — consistent with their going
in **both directions** (goopg higher on Q10/Q20, lower on Q11/Q13/Q18/Q21),
which a one-sided correctness bug could not produce.

The *query texts* are the same — spot-checked on Q18, where both sides use
HammerDB's `having sum(l_quantity) > 313` — so the deltas are attributable to
the data, not to different parameterisation.

Consequences: treat `vs PG` as an order-of-magnitude indicator, never a precise
ratio; and **do not read the ⚠ row-count deltas as goopg bugs** — but equally,
do not read them as proof of correctness. They are simply uninformative here.
The one genuine open item is Q21, whose 370 rows were cross-validated only
*internally* (decorrelated path vs SubPlan path both return 370), never against
PG on the same data.

**1.2 PG used parallelism; goopg does not.** `max_parallel_workers_per_gather=2`
was honored by PG and accepted-but-ignored by goopg. Part of every ratio below
is two extra workers, not planner quality.

**1.3 The R1 column's tail is degraded by a measurement artifact, not by the
planner** — see §4. *Which* artifact is not settled: the signature resembles the
documented throttle trap, but round 1's own report states its final sweep used
the safe wrapper defaults, and a CPU-contention explanation fits equally well.
What is established is that the degradation is systematic and absent from R2/R3.
This is the single most important thing to know before reading the R1 column.

---

## 2. Runtime evolution

Times in seconds. `DNF` = hit the 300 s per-query budget. ⚠ marks a row count
that differs from the PG reference cluster (see §1.1).

| Q | PG 18.3 | baseline | R1 | R2 | R3 | R3 vs PG | R3 rows | PG rows |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Q1 | 1.75 | 32.04 | 30.50 | 29.51 | 29.06 | 17× | 4 | 4 |
| Q2 | 0.26 | 11.55 | 2.59 | 2.52 | 2.57 | 10× | 459 ⚠ | 474 |
| Q3 | 0.36 | 24.38 | 23.25 | 22.67 | 22.54 | 62× | 11175 ⚠ | 11707 |
| Q4 | 0.19 | 3.97 | 3.45 | 3.42 | 3.39 | 18× | 5 | 5 |
| Q5 | 0.34 | **DNF** | 425.72 | 416.46 | 415.25 | 1221× | 5 | 5 |
| Q6 | 0.34 | 17.99 | 17.38 | 16.54 | 16.21 | 48× | 1 | 1 |
| Q7 | 0.43 | 173.23 ✗ | 151.46 | 150.77 | 150.10 | 350× | 4 | 4 |
| Q8 | 0.20 | 4.81 | 3.46 | 3.29 | 3.76 | 18× | 2 | 2 |
| Q9 | 0.91 | 115.42 | 103.64 | 100.08 | 95.25 | 105× | 175 | 175 |
| Q10 | 0.67 | 28.32 | 26.81 | 24.60 | 25.33 | 38× | 20522 ⚠ | 20005 |
| Q11 | 0.08 | 2.81 | 2.78 | 2.59 | 2.58 | 31× | 785 ⚠ | 1119 |
| Q12 | 0.90 | 36.37 | 30.05 | 27.59 | 27.24 | 30× | 2 | 2 |
| Q13 | 1.51 | **DNF** | 96.73 | 95.00 | 96.91 | 64× | 33 ⚠ | 35 |
| Q14 | 0.26 | 68.26 | 65.99 | 47.29 | 47.77 | 183× | 1 | 1 |
| Q15-CREATEVIEW | — | 4.52 | 0.04 | 0.02 | 0.02 | — | 0 | — |
| Q15a-VIEWBODY | 0.89 | 18.16 | 31.77 | 17.90 | 17.00 | 19× | 10000 | 10000 |
| Q15b-MAIN | 1.79 | 39.27 | 64.54 | 33.59 | 33.36 | 19× | 1 | 1 |
| Q16 | 0.40 | 7.24 | 11.26 | 6.27 | 6.36 | 16× | 18192 ⚠ | 18223 |
| Q17 | 1.50 | 58.25 | 90.51 | 47.77 | 47.72 | 32× | 1 | 1 |
| Q18 | 3.74 | 264.86 | 67.29 | 36.79 | 36.88 | 10× | 7 ⚠ | 13 |
| Q19 | 0.06 | 53.62 | 100.96 | 52.00 | 52.08 | 880× | 1 | 1 |
| Q20 | 0.21 | 13.60 | 4.00 | 2.02 | 2.04 | 10× | 92 ⚠ | 90 |
| Q21 | 0.86 | **DNF** | 50.29 | 27.75 | 27.82 | 32× | 370 ⚠ | 445 |
| Q22 | 0.06 | 12.05 | 1.52 | 0.75 | 0.78 | 13× | 7 | 7 |

✗ Q7's baseline time is not comparable: it returned **486 357 rows instead of
4** (a wrong-results bug fixed during round 1), so it was doing a different and
much larger amount of work.

**Stream totals.** baseline 991 s but **only over the 21 queries that
completed** — the three DNFs (Q5, Q13, Q21) are excluded, so this number is not
comparable to the others. R1 1406 s, R2 1167 s, R3 1162 s, all over 24/24
completed slots. PG's total is 17.7 s.

Every goopg column is a **single sweep**, and the PG column a single warm
sample; there are no run-to-run error bars behind any number in this table.

**The headline correctness result is the baseline column, not the times.**
Three queries could not complete at all and one returned six-figure garbage.
By R1 all 22 queries completed with stable row counts, and that has held
through R2 and R3.

---

## 3. Plan evolution vs PostgreSQL

Captured from the committed plan snapshots (`plan_snapshots/csq-*.txt`, at the
repository root — not under the design bundle) and the
PG reference plans. "SubPlan ×N" counts per-row subplan evaluations left in the
plan; a decorrelated shape has none.

| Q | PG 18.3 shape | baseline (S0) | R1 | R2 / R3 | verdict |
|---|---|---|---|---|---|
| Q2 | correlated **SubPlan** ×1 | SubPlan ×1 | **decorrelated, 0 SubPlans** | same | goopg **exceeds** PG (PG never decorrelates scalars) |
| Q4 | **Nested Loop Semi Join**, 0 SubPlans | SubPlan ×1 | **NL SEMI** | same | **matches PG** |
| Q17 | SubPlan ×1 | SubPlan ×1 | SubPlan ×1 | same | **matches PG** (correctly left correlated) |
| Q18 | `HashAggregate` dedupe + plain **Hash Join** — no semi node at all | Hash SEMI | Hash SEMI | same | semantically equivalent; goopg uses a semi join where PG unique-ifies the `IN` set first |
| Q20 | **Hash Right Semi Join** + SubPlan ×1 | Hash SEMI ×2 + SubPlan ×1 | same | same | **matches PG** |
| Q21 | **NL Anti + NL Semi**, 0 SubPlans | SubPlan ×2 | **NL ANTI + NL SEMI** | same | **matches PG** |
| Q22 | **NL Anti** + 1 uncorrelated **InitPlan** | SubPlan ×2 | **NL ANTI** + SubPlan ×1 | same | anti matches; one scalar SubPlan remains where PG hoists the equivalent to a one-shot InitPlan — the documented **G6** gap, deliberate |

`SubPlan ×N` counts *definitions* (`SubPlan N` header lines), not the
`Filter: … (SubPlan N)` reference lines that accompany them.

A caution on reading this table: identical *shape names* are not proof of
identical work. PG additionally runs Q18 and several others with two parallel
workers, and its plans carry real cardinality estimates while goopg's snapshots
show `cost=0.00..0.00 rows=1` throughout — goopg's costs are not populated in
these captures, so "same shape" here means the join/subquery strategy matches,
not that the optimiser reasoned the same way.

Plan-shape convergence happened **entirely in round 1**. Rounds 2 and 3 were
plan-stable by design — `make plan-gate` reported 22/22 MATCH at every stage of
both rounds — so the R2 and R3 columns are identical to R1 by construction, not
by coincidence.

---

## 4. The R1 column is contaminated — and it is not variance

The round-2 report attributed the R1→R2 improvement (1406 s → 1167 s) to
"warm-cache and run-to-run variance", reasoning that the plans were identical.
The per-query breakdown refutes that reading:

| queries | R1 → R2 change |
|---|---|
| Q1–Q13 | −0.5 % to −8.2 % |
| Q14–Q22 | **−28.3 % to −50.7 %** |

There is a sharp discontinuity between Q13 (−1.8 %) and Q14 (−28.3 %), after
which *every* remaining query improves by roughly 30–50 %. Random variance does
not produce a step function ordered by position in the stream.

**Control: it is not inherent to the harness.** Comparing the two *post-guard*
sweeps (R2 → R3, same wrapper, same cap) shows no such structure at all —
Q1–Q13 mean +0.7 %, Q14–Q22 mean +0.2 %, every query inside ±5 % except Q8
(+14.3 %, on a 3.3 s query). So
tail degradation is not something every sweep on this machine exhibits; it is
specific to the R1 run.

**One candidate is the documented throttle trap.** The round-1 report's §5
describes its symptom as "a healthy stream through Q14 followed by a collapsed
tail" — an *adjacent* boundary, not the same one: round 1 has Q14 healthy with
the collapse starting at Q15, whereas the deltas above already show Q14
degraded by 28 %. The mechanism is that cgroup `memory.high` below `GOMEMLIMIT`
with `GOGC=off` parks the scope in the kernel reclaim band once the heap passes
the cap, so damage accumulates toward the tail.

**One honest tension.** That same round-1 §5 states its *final* sweep — the R1
column here — used the safe wrapper defaults and therefore avoided the trap,
and indeed the R1 sweep shows none of the trap's severe symptoms (no DNFs,
CREATE VIEW at 0.04 s rather than 75–420 s). So either the safe defaults were
not fully sufficient for that run, or a related cumulative-heap/GC effect
degraded the tail without triggering the full collapse. **This document cannot
distinguish those two, and no re-measurement was taken to settle it.**

**A second alternative this document cannot exclude.** Because stream position
is also wall-clock order, the step function rules out i.i.d. run-to-run noise
but not *any* time-correlated external factor. CPU contention from an orphaned
backend is a documented failure mode on this machine — round 1's own bug list
records cancelled backends spinning at 100–227 % CPU and "starving later
queries", and round 1 describes two earlier collapsed sweep attempts that could
have left such backends running during the third (the R1 sweep). The
near-uniform ≈2× factor across queries as different as Q15b (1.92×), Q17
(1.89×), Q19 (1.94×), Q20 (1.98×) and Q22 (2.03×) fits a single CPU competitor
at least as well as a memory-reclaim band.

Note also that R1 → R2 was **not** a guard-only delta: seven commits landed
between them, including new NL semi/anti executor modes and a lowering fix.
Plan-gate proves no *plan* changed; it does not prove no *executor* behaviour
changed. What argues against a planner explanation is that the ≈45–50 % gain
lands equally on Q14, Q16 and Q19 — queries round 2 touched in no way at all.

What survives either reading, and is what matters for the table:

1. the R1 tail is degraded by something systematic, not by variance; and
2. whatever it was, it was absent from R2 and R3.

**Consequences for reading this document:**

- R1's tail numbers (Q14–Q22) are **too slow by roughly a factor of two** and
  should not be used to judge round-1 work. Round 1's real wins on Q17, Q19 and
  Q15b are hidden by the artifact; the apparent R1 *regressions* on Q15a, Q15b,
  Q16, Q17 and Q19 versus baseline are, on this evidence, the artifact rather
  than the planner.
- Round 2's apparent 239 s improvement is mostly **a measurement artifact
  disappearing**, not the S4/S5a/D6.3/S7 planner work. Round 2's own report was
  right that its planner changes moved no plan, and right not to claim a win —
  but its stated reason ("warm-cache and run-to-run variance") does not fit a
  step function, and should be read as corrected by this section.
- The least-compromised comparison of planner work is **baseline → R2/R3**,
  since R2/R3 are the columns known to be clean. But note baseline also predates
  the guard, and its Q15-CREATEVIEW at 4.52 s (vs 0.02–0.04 s in every later
  sweep) is a mild instance of the same signature — so baseline is **not**
  positively established as clean either.

This correction is recorded here rather than quietly applied because the
round-2 report's "variance" sentence is still in the tree and a future reader
would otherwise trust it.

---

## 5. What each change did, per query

### 5.1 Clear wins, attributable to a specific change

**Q21 — DNF → 27.8 s (round 1).** The largest single result. Its dual
`EXISTS`/`NOT EXISTS` correlation could not complete in 300 s at baseline. The
`IndexScan.Key` harvest (S1c) let the correlation be recognised at all — it had
been absorbed into the index probe key where the collectors never looked, which
is why decorrelation had *never* fired on the TPC-H schema — and NLI semi/anti
with residual support (S6) then gave it PG's exact shape. Cross-validated: the
decorrelated and SubPlan paths both return 370 rows.

**Q22 — 12.05 s → 0.78 s (15×).** NLI anti join replacing the per-row
`NOT EXISTS` SubPlan (baseline carried two SubPlan definitions; one remains).
The remaining scalar SubPlan is correct to keep — PG evaluates the same scalar
once, as an InitPlan.

**Q2 — 11.55 s → 2.57 s (4.5×).** Scalar decorrelation; goopg does something
PG does not attempt here.

**Q18 — 264.86 s → 36.88 s (7.2×).** *Not* a plan change: Q18's tree is
identical in all four snapshots — `Hash Join (SEMI)` was already the baseline
shape. The entire win is executor-side (the operator-reset fixes). Attributing
it to semi-join work would repeat exactly the error §5.3's Q19 entry avoids.

**Q20 — 13.60 s → 2.04 s (6.7×).** The probe-cheap scalar policy gave it a
rescan path; the S6 restriction to Semi/Anti kept it from the Q9 trap.

**Q5 — DNF → 415 s.** Completes, but see §5.3.

**Q13 — DNF → 96.9 s.** Completes with stable row counts.

**Q7 — wrong results → correct.** 486 357 rows → 4. Two `tryBuildNLI` bugs:
the inner `IndexScan` lost its `Alias` (self-join columns shifted into the
neighbouring table) and the join residual was silently discarded. Fixed while
*keeping* the NLI rewrite, per instruction. The 173 s → 150 s change is not a
speedup — the baseline was doing vastly more work.

### 5.2 Changes that were deliberately invisible on TPC-H

Round 2 and round 3 predicted, and plan-gate confirmed, **zero TPC-H plan
change** at every stage. That was the design, not a disappointment:

- **S4** (residual lifting, nested-sublink tolerance) unlocks shapes TPC-H does
  not contain — zero-equijoin EXISTS, scalar residuals, nested sublinks.
- **S5a** (pull-up before join-order search) converges to identical trees on a
  stats-less server.
- **D6.3** (cost gates) exists to *preserve* round-1 decisions while making two
  measured 71×-class traps structurally impossible.
- **S7 Memoize** attaches to zero TPC-H queries — and so does PG's, at SF1.
- **Round 3's five fixes** touch shapes TPC-H has none of: no LEFT join with a
  cross-relation ON residual, no numerics past int64, no composite-correlated
  EXISTS, and no DML at all.

For these, "the sweep did not move" is the pass condition. Their value is
correctness (two live wrong-results bugs fixed in round 3, one in round 1) and
coverage (the dual-path semantics matrix grew M13 → M22 → M26).

### 5.3 Where the result fell short of hope

**Q5 — 415 s, ≈1221× PG. The worst query in the set by a wide margin.**
Round 1 made it complete for the first time, which was the goal at the time,
but nothing since has improved it and it now dominates the stream total (36 %
of all time). Its cost is not correlated-subquery work at all — it is join
ordering (the M0077 history) — so this bundle's rounds could not have helped
it. It is the single most valuable remaining target and belongs to a different
work stream.

**Q19 — 52 s, ≈880× PG. The worst ratio after Q5.** Its runtime is unchanged
across every milestone (53.62 → 52.08 s), and the plan snapshots show why: the
tree is **structurally identical at all four points** — `Nested Loop (INNER)` with a
full `lineitem` seq scan on the outer side and a `part_pk` index probe inner,
carrying the whole three-branch OR as a residual filter. That OR-factored NLI
shape **predates this bundle**; no round targeted Q19, so the flat line is
expected rather than anomalous. (The only visible change is cosmetic: R2-0
taught EXPLAIN to print the `Filter:` line, which is why the residual appears
in the R2 snapshot and not the earlier ones.)

The cost is ~6 M per-row index probes each evaluating a large OR predicate —
a join-strategy problem, not a subquery one, and therefore untouched by this
bundle by construction. It is the clearest remaining target after Q5.

**Q7 — 150 s, ≈350× PG.** Correctness was restored, performance was not
addressed. The NLI rewrite is retained per instruction, but a 350× gap says the
shape is not the fast one for this data.

**Q14 — 47.8 s, ≈183× PG.** Improved only by the harness fix; no planner change
reached it.

**Q9 — 95 s, ≈105× PG.** Round 2 deliberately *declined* to unwrap its
`Filter{SeqScan}` inner (the `%green%` LIKE would become ~6 M per-probe
evaluations — measured as 115 s → DNF when tried). Declining was correct, but
it means the query keeps a shape that is 105× off PG. The cost gate protects
the status quo rather than improving it.

**The bulk-operator floor.** Q1 at 17× and Q6 at 48× contain no subqueries at
all. This is the executor's constant factor, explicitly out of this bundle's
scope, and it sets a floor no amount of planner work can go below.

---

## 6. Summary judgement

| dimension | verdict |
|---|---|
| correctness | **strongest result.** 3 DNFs and 1 six-figure wrong answer at baseline; 24/24 complete and stable from R1 through R3. Three live wrong-results bugs found and fixed (Q7 alias/residual in R1; NLI LEFT residual and big-numeric hash keys in R3) |
| plan-shape parity with PG | **achieved in round 1** for every subquery-bearing query, and held stable since (plan-gate 22/22 MATCH throughout R2 and R3) |
| speed on subquery queries | **large wins where decorrelation applies** — Q22 15×, Q18 7×, Q20 6.7×, Q2 4.5×, Q21 DNF→27.8 s |
| speed overall vs PG | **still 10×–1221×.** The median is ≈31×, but Q5 (1221×), Q19 (880×), Q7 (350×) and Q14 (183×) are dominated by non-subquery concerns |
| rounds 2 and 3 on TPC-H | **no measurable effect, by design.** Their value is defensive (cost gates, harness guard) and correctness/coverage, and the timing table is a no-regression check |

The most actionable follow-ups this table exposes are **Q5** (36 % of stream
time, join-ordering-bound) and **Q19** (880×, ~6 M per-row index probes under a
large OR residual, in a plan shape that has never changed) — neither of which
is correlated-subquery work, and both of which now dominate the stream far more
than anything this bundle owns.

---

## 7. Provenance

- goopg sweeps: the four evidence files listed at the top, all run under
  `scripts/csq-bench-server.sh` (cgroup-capped) at SF1 with a 600 s per-query
  budget (300 s for the baseline sweep).
- goopg plans: `plan_snapshots/csq-s0-explain.txt` (baseline),
  `csq-s6-harvest.txt`, `csq-s8-params.txt` (R1), `csq-r2-0-nli-display.txt`
  (R2, unchanged through R3).
- PG times, rows and plans:
  `analysis/tpch/goopg-pg-tpch-plan-compare-260718/raw/{pg_time_rows.tsv,pg_explain.txt}`
  on `origin/master` — a separate cluster and data load; see §1.1.
- Round reports: [round 1](tpch-csq-s0s3-verification-20260721.md),
  [round 2](tpch-csq-round2-verification-20260721.md),
  [round 3](tpch-csq-round3-verification-20260721.md).

Nothing was re-measured for this document.
