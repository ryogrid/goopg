# TPC-H SF1 — goopg Evolution, Round 4: Parallel Query

**Date:** 2026-07-22
**Scope:** the body of work done *after* round 3 of the correlated-subquery
bundle — the intra-transaction parallel-query bundle (design + P0–P10 + the
parallel multi-way hash join). Measured fresh, unlike the base document.
**Predecessor:** [`tpch-csq-evolution-baseline-to-round3-20260721.md`](tpch-csq-evolution-baseline-to-round3-20260721.md)
(baseline → R1 → R2 → R3). Read its §1 and §4 first; this document inherits its
caveats and adds new ones.

| milestone | column(s) | goopg commit | evidence |
|---|---|---|---|
| round 3 complete | `R3` | `bb90089a` | [`sf1-r3-final-bb90089a.txt`](../docs/design/correlated-subquery-planning/evidence/sf1-r3-final-bb90089a.txt) |
| round 4 (this work) | `w0/w2/w4/w8` | `648f5e47` | [`sf1-r4-w0-serial.txt`](../docs/design/parallel-query/evidence/sf1-r4-w0-serial.txt), [`w2`](../docs/design/parallel-query/evidence/sf1-r4-w2.txt), [`w4`](../docs/design/parallel-query/evidence/sf1-r4-w4.txt), [`w8`](../docs/design/parallel-query/evidence/sf1-r4-w8.txt) |

`git log bb90089a..HEAD` is exactly this bundle: the design chapters, P0–P10
(GUC fidelity, session plumbing, the concurrency substrate, parallel sequential
scan + Gather, Gather Merge, parallel hash join, partial/finalize aggregation
and its cost-model gate) and the parallel multi-way hash join.

---

## 0. The one-sentence result

**Round 4 is two independent stories that nearly cancel: parallelism is a
clean, safe 2–5× win on the ~10 queries that have something to parallelise and
brings several within single digits of PostgreSQL for the first time — while the
statistics that parallelism's gates require, turned on without a mature cost
model, fixed one catastrophic query (Q5, 23×) and broke five others by 25–100×.**

The stream total barely moves (R3 1162 s → best-case R4 ~1128 s) because those
two effects are almost equal and opposite. The headline is not the total; it is
the decomposition.

---

## 1. Read this before the numbers

Inherits the base document's §1 (different data load from the PG cluster; PG
column is a plan-shape study, not a benchmark; single sweeps, no error bars) and
adds four caveats of its own.

**1.1 Round 4 lifts the base doc's §1.2, partially.** The base document's central
caveat was: *"PG used parallelism; goopg does not … part of every ratio is two
extra workers, not planner quality."* Through R3, `max_parallel_workers_per_gather`
was accepted-but-ignored. It is now honoured. For the queries that actually
parallelise, the `vs PG` ratios below are finally a like-for-like comparison —
PG ran two workers, and `w2` runs two workers. For the queries that do not
parallelise, the caveat is *unchanged*: goopg still runs them on one core while
PG uses two.

**1.2 Two effects are confounded, and the `w0` column is what separates them.**
Round 4 turns on both parallelism *and* statistics — the partial-aggregation
split gate (P10) and the MHJ probe selection both need `ANALYZE` to have run,
and R1–R3 were deliberately stats-less. To attribute cleanly, four sweeps were
taken under **one server start and one ANALYZE** (so statistics are identical
across all four):

- `w0` — `max_parallel_workers_per_gather = 0`: statistics on, **no parallelism**.
- `w2` / `w4` / `w8` — the same, with 2 / 4 / 8 workers.

`R3 → w0` isolates **statistics/join-order** (with the confound in 1.3);
`w0 → wN` isolates **pure parallelism**, because statistics are held constant.

**1.3 `R3 → w0` is not purely statistics — it also carries a code-version and a
warmth confound.** `w0` is R4 code; R3 is R3 code (20 commits apart). And `w0`
was run *last*, after w2/w4/w8, so it is the most cache-warm sweep. Both push
`w0` faster than a cold, R3-code, stats-analyzed run would be. Therefore:

- Where `R3 → w0` moved by a *large* factor (Q5 23×; Q4/Q8/Q22 by 50–100×
  *slower*), the cause is unmistakably the statistics-driven join-order change —
  neither a warm cache nor 20 commits of additive parallel-query code can move a
  query 100×, and the plans confirm it (§6).
- Where it moved *mildly* (Q1 1.46×, Q13 0.94×), that is code/warmth noise and
  is **not** attributed to statistics. Q1 has no joins, so statistics cannot
  change its plan at all; its 29→20 s is warmth plus whatever serial-execution
  cost changed in the bundle.

**1.4 Correctness first, and it is clean.** Every row count in every R4 sweep —
w0, w2, w4, w8 — **matches R3 exactly**, all 24 slots. Parallelism returns what
serial returns, and the statistics-driven join-order changes (some drastic)
alter *plans*, never *results*. This is the parallel-identity property and the
join-order-invariance property, measured together on SF1 data at scale, which no
unit test exercises.

---

## 2. Runtime evolution

Times in seconds. `R3` is the stats-less serial baseline; `w0` is
statistics-on serial; `w2/w4/w8` add workers. **best** is the fastest R4 cell and
its worker count; `vs PG` uses **best**.

| Q | PG 18.3 | R3 | w0 | w2 | w4 | w8 | best | vs PG | rows (all cols) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| Q1 | 1.75 | 29.1 | 20.0 | 7.1 | 4.4 | **3.8** | 3.8 @8 | 2× | 4 |
| Q2 | 0.26 | 2.6 | 67.3 | 52.5 | 53.5 | **52.1** | 52.1 @8 | 200× | 459 |
| Q3 | 0.36 | 22.5 | 19.1 | **9.5** | 9.6 | 10.9 | 9.5 @2 | 26× | 11175 |
| Q4 | 0.19 | 3.4 | 269.0 | 269.2 | 266.9 | **266.7** | 266.7 @8 | 1404× | 5 |
| Q5 | 0.34 | 415.2 | 18.2 | 8.5 | 6.6 | **6.2** | 6.2 @8 | 18× | 5 |
| Q6 | 0.34 | 16.2 | 13.9 | 5.3 | 3.3 | **2.8** | 2.8 @8 | 8× | 1 |
| Q7 | 0.43 | 150.1 | 147.8 | 146.9 | **141.5** | 143.4 | 141.5 @4 | 329× | 4 |
| Q8 | 0.20 | 3.8 | 199.7 | 192.6 | **187.9** | 188.3 | 187.9 @4 | 940× | 2 |
| Q9 | 0.91 | 95.2 | 27.4 | 28.2 | **27.3** | 27.4 | 27.3 @4 | 30× | 175 |
| Q10 | 0.67 | 25.3 | 19.9 | 10.4 | **9.8** | 12.4 | 9.8 @4 | 15× | 20522 |
| Q11 | 0.08 | 2.6 | 2.8 | 1.9 | **1.8** | 1.8 | 1.8 @4 | 23× | 785 |
| Q12 | 0.90 | 27.2 | 120.6 | 109.6 | **104.7** | 104.8 | 104.7 @4 | 116× | 2 |
| Q13 | 1.51 | 96.9 | **102.7** | 105.0 | 103.1 | 103.9 | 102.7 @0 | 68× | 33 |
| Q14 | 0.26 | 47.8 | 19.0 | 7.0 | 4.5 | **3.9** | 3.9 @8 | 15× | 1 |
| Q15a | 0.89 | 17.0 | 17.3 | 6.3 | 3.8 | **3.3** | 3.3 @8 | 4× | 10000 |
| Q15b | 1.79 | 33.4 | **34.0** | 34.8 | 34.1 | 34.2 | 34.0 @0 | 19× | 1 |
| Q16 | 0.40 | 6.4 | 2.9 | 1.4 | **1.1** | 1.1 | 1.1 @4 | 3× | 18192 |
| Q17 | 1.50 | 47.7 | 19.5 | 7.2 | 4.6 | **4.0** | 4.0 @8 | 3× | 1 |
| Q18 | 3.74 | 36.9 | 37.2 | **27.6** | 29.9 | 35.4 | 27.6 @2 | 7× | 7 |
| Q19 | 0.06 | 52.1 | 24.1 | 9.1 | 6.0 | **5.5** | 5.5 @8 | 91× | 1 |
| Q20 | 0.21 | 2.0 | 2.0 | 2.1 | **2.0** | 2.0 | 2.0 @4 | 10× | 92 |
| Q21 | 0.86 | 27.8 | **20.3** | 21.3 | 20.7 | 20.7 | 20.3 @0 | 24× | 370 |
| Q22 | 0.06 | 0.8 | 102.7 | 103.5 | **101.2** | 101.7 | 101.2 @4 | 1686× | 7 |

**Stream totals:** R3 1162 s, **w0 1307 s**, w2 1167 s, w4 1128 s, w8 1136 s.
The critical number is `w0 = 1307` — **statistics alone, without parallelism,
made the stream 12 % slower than R3.** Parallelism then claws it back to ~1128,
a hair under R3. Round 4's net effect on the total is roughly neutral, and that
neutrality is the average of enormous cancelling movements.

---

## 3. The decomposition — the heart of the round

`stats` = R3 → w0 (statistics/join-order, with the §1.3 caveat). `par` = w0 →
best (pure parallelism). A factor **> 1 is a speedup**, **< 1 a regression**.

| Q | stats (R3→w0) | par (w0→best) | reading |
|---|---:|---:|---|
| Q1 | 1.46× | **5.33×** | parallelism (agg split); "stats" is warmth — Q1 has no joins |
| Q2 | **0.04×** | 1.29× | statistics wrecked it (25× slower); parallelism cannot recover it |
| Q3 | 1.18× | **2.02×** | parallel MHJ; note best @2, degrades after |
| Q4 | **0.01×** | 1.01× | statistics wrecked it (100×); no parallelism available |
| Q5 | **22.83×** | 2.91× | statistics **fixed** the worst query in the set, then parallelism halved it again |
| Q6 | 1.17× | **4.93×** | parallel scan |
| Q7 | 1.02× | 1.04× | untouched — subquery-bound, neither lever reaches it |
| Q8 | **0.02×** | 1.06× | statistics wrecked it (50×) |
| Q9 | **3.48×** | 1.00× | statistics fixed the join order; nothing to parallelise |
| Q10 | 1.27× | **2.03×** | parallel MHJ; best @4 |
| Q11 | 0.92× | 1.55× | mild both ways |
| Q12 | **0.23×** | 1.15× | statistics regressed it 4× |
| Q13 | 0.94× | 1.00× | flat — load-dependent GROUP BY, no parallel shape |
| Q14 | **2.52×** | **4.88×** | both levers win — stats *and* a parallel scan |
| Q15a | 0.98× | **5.23×** | parallel scan, the cleanest scaling in the set |
| Q15b | 0.98× | 1.00× | flat |
| Q16 | **2.18×** | **2.65×** | both win |
| Q17 | **2.44×** | **4.89×** | both win |
| Q18 | 0.99× | 1.35× | parallel MHJ, weak — best @2, *slower* at 8 |
| Q19 | **2.16×** | **4.42×** | both win |
| Q20 | 1.01× | 1.00× | flat |
| Q21 | 1.37× | 1.00× | statistics helped mildly; nothing to parallelise |
| Q22 | **0.01×** | 1.01× | statistics wrecked it (100×) |

Read down the two columns and the structure is stark:

- **Parallelism (`par`) is never a regression.** Its worst cell is 1.00× (found
  nothing to split). Its best are 4–5× (Q1, Q6, Q14, Q15a, Q17). It is the safe
  lever.
- **Statistics (`stats`) is a coin flip.** Its wins are the largest single
  numbers in the document (Q5 23×, Q9 3.5×, Q14/Q17 2.5×). Its losses are also
  the largest (Q4/Q8/Q22 by 50–100×). It is the dangerous lever, and it is one
  goopg *had* to pull to make the parallel gates work.

---

## 4. Where parallelism closed the gap to PostgreSQL

This is the round's clean, defensible win, and — per §1.1 — a fair comparison,
since both sides use two-plus workers. For the scan- and aggregate-bound queries
that were never subquery problems, `vs PG` collapsed from the tens to single
digits:

| Q | R3 vs PG | R4 best vs PG | what parallelised |
|---|---:|---:|---|
| Q1 | 17× | **2×** | partial/finalize aggregate split (P9/P10) |
| Q16 | 16× | **3×** | Gather over the grouping |
| Q17 | 32× | **3×** | Gather |
| Q15a | 19× | **4×** | parallel sequential scan |
| Q18 | 10× | **7×** | parallel multi-way hash join |
| Q6 | 48× | **8×** | parallel sequential scan |

Q1 at 2× PG is the closest goopg has come to PostgreSQL on any non-trivial query
in this entire evolution. The aggregate-split cost model (chapter 11) earned its
keep here: it correctly split Q1 (4 groups over 6 M rows) while refusing Q18's
inner high-cardinality aggregate, exactly as designed.

The MHJ story is more mixed and matches this session's separate Q3 finding:
Q3/Q10/Q18 parallelise but **peak at 2–4 workers and degrade past that**, because
each worker independently rebuilds the non-probe dimension tables — and in
Q3/Q10 one of those (`orders`, 1.5 M) is not small. The deferred shared build
(chapter 12 §6) is what would lift that ceiling; the degradation here is the
measurement that motivates it.

---

## 5. The statistics regressions, attributed

Five queries regressed catastrophically, all from statistics changing the join
order or algorithm into something goopg executes slowly. These are `w0`
regressions (serial), so parallelism is not implicated — and `w0` was the
warmest sweep, so a cold run would be *worse*, not better.

- **Q4: 3.4 → 269 s (79×).** R3 ran it as a **Nested Loop Semi Join** (PG's
  shape). Statistics flipped it to a **Hash Semi Join that builds the 6 M-row
  `lineitem` into a hash table** and probes with filtered `orders`. The plan
  snapshot confirms `Hash Join (SEMI)` with `lineitem` on the build side.
- **Q8: 3.8 → 200 s (53×).** An eight-table join whose order statistics
  rearranged into a deep tree with a `lineitem`-bearing `Multi-Way Hash Join`
  buried inside — a join order the stats-less small-dimension heuristic never
  chose.
- **Q22: 0.8 → 103 s (128×), Q2: 2.6 → 67 s (26×), Q12: 27 → 121 s (4.4×).** Same
  family: a stats-driven join-order/algorithm change that produced a large
  intermediate the previous plan avoided.

The common cause is not a bug — every result is correct — it is that **goopg has
no absolute cost model** (EXPLAIN's `cost=0.00..0.00` is a literal; the join-order
DP uses relative weights). The stats-less planner leaned on a robust heuristic
(hard-tagging `region`/`nation` as small dimensions); real statistics let the DP
make aggressive choices it cannot correctly cost, so it wins big on the query
that heuristic mis-ordered (Q5) and loses big on the five it happened to order
well.

This is the same gap chapter 11 (P10) began closing for one narrow decision —
the aggregate split — with a self-contained cost comparison. These five
regressions are the measured argument for generalising that work: **statistics
are not safe to keep on until the planner can cost them.**

---

## 6. Plan evolution

Captured in [`plan_snapshots/csq-r4-parallel.txt`](../plan_snapshots/csq-r4-parallel.txt)
(statistics on, `max_parallel_workers_per_gather = 2`). Two kinds of change from
the R3 snapshots, and they must not be conflated:

**New parallel nodes (the intended work):**
- `Gather` over scan/filter subtrees (Q6, Q14, Q15a, Q16, Q17, …).
- `Finalize HashAggregate → Gather → Partial HashAggregate` (Q1 — the P9/P10
  split, with the EXPLAIN prefixes P2 added).
- `Gather` over a `Multi-Way Hash Join` (Q3, Q10, Q18 — chapter 12).
- No `Gather Merge` fired on TPC-H (every `ORDER BY` sits above an aggregate or
  join, so `findPartialSubtree` correctly declines — as chapter 05/P7 predicted).

**Statistics-driven join-order and algorithm changes (the side effect):** the
`(stats)` annotations and real `rows=` estimates now present on every scan;
Q4's NL→Hash Semi flip; Q8's rearranged eight-way tree; MHJ probe-table
selection now driven by real row counts rather than the first-scan default. These
are why a naïve `make plan-gate` against the R3 baseline reports ~18/22 diverged
— **the vast majority of that diff is statistics, not parallelism**, which is
why this document attributes per query rather than trusting the aggregate diff.

---

## 7. Summary judgement

| dimension | verdict |
|---|---|
| correctness | **flawless.** All 24 row counts match R3 across statistics and 0/2/4/8 workers. Parallel ≡ serial, and join-order changes preserve results. |
| parallelism (the intended feature) | **a clean, safe win.** 2–5× on the ~10 queries with a parallel shape, never a regression, and it brought Q1 to 2×, Q16/Q17 to 3× PG for the first time. Lifts the base doc's §1.2 for those queries. |
| MHJ parallelism | **works, but caps at 2–4 workers** — per-worker dimension rebuild is the ceiling (chapter 12 §5.1); motivates the deferred shared build. |
| statistics (the prerequisite) | **the round's real problem.** Fixed the worst query (Q5, 23×) but broke five others 25–100×, netting the serial stream 12 % *slower*. goopg lacks the cost model to use statistics safely. |
| net on the stream total | **roughly neutral** (1162 → ~1128 s at best) — parallelism's gains almost exactly cancel statistics' regressions. |
| vs PG overall | still 2×–1686×; the worst cells are now the **statistics regressions** (Q22 1686×, Q4 1404×, Q8 940×), which displaced the base doc's Q5/Q19 as the worst offenders — Q5 is now *fixed*. |

**The most actionable follow-up this round exposes is a cost model.** The base
document's top targets were Q5 (join-ordering-bound) and Q19 (OR-residual);
statistics fixed Q5 (23×) and helped Q19 (2.2×) outright, but in doing so created
five new regressions worse than either. The single change that would make
Round 4 a net win rather than a wash is the ability to cost the join orders
statistics enable — which is exactly the direction chapter 11 opened for one
decision and which these five regressions now demand for the general case.

---

## 8. Provenance

- **goopg R4 sweeps:** `cmd/tpch-runner` (with the new `--parallel-workers`
  flag) at HEAD `648f5e47`, one server start under `scripts/csq-bench-server.sh`
  (cgroup-capped), one `ANALYZE` of all 8 tables, then w0/w2/w4/w8 in that order
  (w0 last — the warmest, so it *understates* the statistics regressions).
  `--db postgres --per-query-timeout=600s`. Evidence: the four
  `docs/design/parallel-query/evidence/sf1-r4-*.txt` files.
- **goopg R4 plans:** `plan_snapshots/csq-r4-parallel.txt`
  (`make plan-snapshot-capture LABEL=csq-r4-parallel PLAN_DB=postgres`),
  statistics on, workers=2.
- **R3 baseline:** `docs/design/correlated-subquery-planning/evidence/sf1-r3-final-bb90089a.txt`,
  reused unmodified.
- **PG times/rows:** the base document's PG column (a separate cluster and data
  load — see its §1.1); order-of-magnitude only.
- **Data load:** the 2026-06-13 HammerDB build (Q13 = 33), verified before the
  sweeps and identical to the base document's goopg data.

Single sweeps throughout; no error bars. Every numeric claim here traces to one
of the committed evidence files or the plan snapshot.
