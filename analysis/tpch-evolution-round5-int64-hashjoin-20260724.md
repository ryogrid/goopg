# TPC-H SF1 — goopg Evolution, Round 5: int64 hash-join fast-path (default-config snapshot)

**Date:** 2026-07-24
**Scope:** the current codebase measured *in its default/normal state* — the successor
to round 4. The round-5 body of work is the cost-model-enhancement branch
(`costmodel-enhance1`): an always-on int64 hash-join fast-path, an experimental
cost-driven join-order planner (**OFF by default**; measured separately in **§6**), and the
parallel per-gather default raised 2→4. The main measurement is **one configuration** — the
shipping default — not a sweep; §6 adds the cost-driven planner as an addendum.
**Predecessor:** [`tpch-evolution-round4-parallel-query-20260722.md`](tpch-evolution-round4-parallel-query-20260722.md)
(the parallel-query bundle, w0/w2/w4/w8). Read its §1 first; this document inherits its
caveats.

| milestone | column | goopg commit | evidence |
|---|---|---|---|
| round 4 (parallel query) | `R4 w4` / `R4 best` | `648f5e47` | [`sf1-r4-w4.txt`](../docs/design/parallel-query/evidence/sf1-r4-w4.txt) |
| round 5 (this snapshot) | `R5` | **`cb37d166`** | [`sf1-r5-default-cb37d166.txt`](../docs/design/cost-model/evidence/sf1-r5-default-cb37d166.txt), plans [`r5-default.txt`](../plan_snapshots/r5-default.txt) |

`git log 648f5e47..HEAD` is the cost-model bundle. Only three of its 45 commits touch
what this default-config measurement sees: **`0aeb7613`** (the int64 hash-join fast-path —
always-on), **`9106121e`** (an int64-overflow fix in the cardinality/cost math — shifts
displayed `rows=` estimates by ~1 %), and **`cb37d166`** (the 2→4 parallel default). The
§2/veto/composite-NLI-keep planner work is gated behind `GOOPG_COST_DRIVEN_JOINORDER` and
is **invisible** in the default-config measurement (§0–§5); it is measured on its own in the
**§6 addendum**.

---

## 0. The one-sentence result

**Round 5 is a clean, monotone improvement over round 4 with no plan changes and no
correctness changes: the always-on int64 hash-join fast-path makes the shipping default
(integer planner, 4 workers) ~4 % faster on the stream — and faster on nearly every
individual query — than round 4's equivalent 4-worker sweep, while returning identical
rows and identical plan shapes.**

The single-configuration R5 stream total (**1086 s**) beats *every* round-4 full-sweep
total (w2 1167 s, w4 1128 s, w8 1136 s) despite not cherry-picking a worker count per
query. The headline is that a broad, structural executor optimisation moved the whole
board at once, in the direction it was designed to.

---

## 1. Read this before the numbers

Inherits round 4's §1 (different PG data load; PG column is a plan-shape study, not a
benchmark; single sweeps, no error bars) and adds four caveats of its own.

**1.1 This is one configuration, not a sweep.** Round 4 measured w0/w2/w4/w8 to separate
statistics from parallelism. Round 5 measures only the **shipping default**: integer-DP
planner, `max_parallel_workers_per_gather = 4` (the new boot default), parallelism on,
cost-driven join-order **off**. The user asked for "how the current codebase plans and
runs in its normal state," so the R5 column is that state directly — no per-query
worker-count selection. Where round 4 quotes a `best` cell it is the fastest of four
worker counts *for that query*; the R5 cell is the single honest default, which makes
R5's stream total *beating* even round 4's cherry-picked best-per-query total (§2, by ~33 s)
the stronger statement.

**1.2 The delta vs round 4 is the int64 hash-join fast-path, and nothing else structural.**
Every INNER binary hash join whose join keys are int64-representable now hashes an `int64`
instead of allocating a per-row `datumKey` string — removing the allocation + GC that
dominated large probes (commit `0aeb7613`; the same lesson the parallel MHJ already
embodied). It is **not** gated on any planner flag, so it applies to every query, serial
and parallel. This is the one lever that moved R5, and it is the safe kind of lever: it
changes *how fast a plan runs*, never *which plan runs* (§5 confirms zero shape changes).

**1.3 Statistics are on, and R5 is in the same stats regime as round 4.** ANALYZE of all
eight tables was run before the sweep, exactly as round 4. The tell is Q5 = 6.4 s and
Q4 = 257 s: the **stats-on** regime (round-4 `w0` was Q5 ≈ 18 s / Q4 ≈ 269 s; the
stats-less R3 was Q5 ≈ 415 s / Q4 ≈ 3.4 s). So R5 carries round 4's statistics-driven
join orders — including its five regressions (Q4/Q8/Q22/Q2/Q12), which are a cost-model
problem (round 4 §5) that this round did **not** touch. int64 shaves a few percent off
each but the structural over-estimate that makes them slow is unchanged.

**1.4 Correctness is flawless.** Every one of the 24 row-count slots matches round 4 (and
round 3) exactly — Q1=4, Q3=11175, Q4=5, Q9=175, Q10=20522, Q13=33, Q15a=10000, Q16=18192,
Q22=7, … The int64 fast-path preserves results (it also fixed a *parallel* shared-build
bug where an int64-finalised table read empty in workers — caught by the identity suite,
not by this benchmark), and the parallel degree change alters worker counts, never rows.

---

## 2. Runtime

Times in seconds. `PG 18.3`, `R3` (stats-less serial), and `R4 best` (fastest of round 4's
w0/w2/w4/w8) are carried verbatim from round 4 §2 for context. `R4 w4` is round 4 at the
*same* 4-worker degree R5 runs — the like-for-like column. `R5` is this snapshot. `vs PG`
uses R5.

| Q | PG 18.3 | R3 | R4 w4 | R4 best | **R5 (4w)** | vs PG | rows |
|---|---:|---:|---:|---:|---:|---:|---:|
| Q1 | 1.75 | 29.1 | 4.4 | 3.8 @8 | **4.47** | 3× | 4 |
| Q2 | 0.26 | 2.6 | 53.5 | 52.1 @8 | **51.23** | 197× | 459 |
| Q3 | 0.36 | 22.5 | 9.6 | 9.5 @2 | **9.14** | 25× | 11175 |
| Q4 | 0.19 | 3.4 | 266.9 | 266.7 @8 | **256.96** | 1352× | 5 |
| Q5 | 0.34 | 415.2 | 6.6 | 6.2 @8 | **6.43** | 19× | 5 |
| Q6 | 0.34 | 16.2 | 3.3 | 2.8 @8 | **3.08** | 9× | 1 |
| Q7 | 0.43 | 150.1 | 141.5 | 141.5 @4 | **138.36** | 322× | 4 |
| Q8 | 0.20 | 3.8 | 187.9 | 187.9 @4 | **181.36** | 907× | 2 |
| Q9 | 0.91 | 95.2 | 27.3 | 27.3 @4 | **25.69** | 28× | 175 |
| Q10 | 0.67 | 25.3 | 9.8 | 9.8 @4 | **9.61** | 14× | 20522 |
| Q11 | 0.08 | 2.6 | 1.8 | 1.8 @4 | **1.66** | 21× | 785 |
| Q12 | 0.90 | 27.2 | 104.7 | 104.7 @4 | **100.13** | 111× | 2 |
| Q13 | 1.51 | 96.9 | 103.1 | 102.7 @0 | **98.14** | 65× | 33 |
| Q14 | 0.26 | 47.8 | 4.5 | 3.9 @8 | **4.07** | 16× | 1 |
| Q15a | 0.89 | 17.0 | 3.8 | 3.3 @8 | **3.58** | 4× | 10000 |
| Q15b | 1.79 | 33.4 | 34.1 | 34.0 @0 | **34.13** | 19× | 1 |
| Q16 | 0.40 | 6.4 | 1.1 | 1.1 @4 | **1.08** | 3× | 18192 |
| Q17 | 1.50 | 47.7 | 4.6 | 4.0 @8 | **4.28** | 3× | 1 |
| Q18 | 3.74 | 36.9 | 29.9 | 27.6 @2 | **28.24** | 8× | 7 |
| Q19 | 0.06 | 52.1 | 6.0 | 5.5 @8 | **5.60** | 93× | 1 |
| Q20 | 0.21 | 2.0 | 2.0 | 2.0 @4 | **2.03** | 10× | 92 |
| Q21 | 0.86 | 27.8 | 20.7 | 20.3 @0 | **20.08** | 23× | 370 |
| Q22 | 0.06 | 0.8 | 101.2 | 101.2 @4 | **96.89** | 1615× | 7 |

**Stream totals:** R3 1162 s, R4 w4 **1128 s**, R4 best-per-query 1120 s (cherry-picked
across worker counts), **R5 1086 s** (single default config). R5 beats R4 w4 by 42 s
(3.7 %) at the *same* worker degree, and beats even round 4's cherry-picked
best-per-query total (by 33 s) while being a single honest configuration.

---

## 3. The decomposition — where the 42 seconds came from

The right comparison is **R4 w4 → R5**: identical 4-worker degree, identical stats, identical
plans (§5). The only moving part is the int64 fast-path (plus the ~1 % cardinality-display
shift, which cannot change runtime). A factor **> 1 is a speedup**.

**The aggregate signal is where the attribution is solid.** The eight 100-second-class
hash-heavy queries all moved the same direction (faster), and their *absolute* savings carry
the round:

| Q | R4 w4 → R5 | Δ seconds | what it hashes |
|---|---:|---:|---|
| Q4 | 1.04× | −9.9 | 6 M-row `lineitem` hash-semi build |
| Q8 | 1.04× | −6.5 | eight-table hash tree with a buried `lineitem` MHJ |
| Q13 | 1.05× | −5.0 | large GROUP-BY hash |
| Q12 | 1.05× | −4.6 | `lineitem`↔`orders` hash |
| Q22 | 1.04× | −4.3 | `customer`/`orders` hash + NOT-IN anti-join |
| Q7 | 1.02× | −3.1 | subquery-bound; only the hash parts speed up |
| Q2 | 1.04× | −2.3 | (also a default-planner stats-regression cell) |
| Q9 | 1.06× | −1.6 | the flagship: int64 keys on the 6 M-row `lineitem` cascade |
| **subtotal** | | **−37.3** | **≈ 89 % of the whole 42 s stream delta** |

Eight queries, all faster, all hash-heavy, together ~89 % of the improvement — a coordinated
same-direction move on the largest cells that run-to-run noise does not produce. **That
aggregate attribution — "≈4 % of the stream from the int64 fast-path" — is defensible.**

**The per-query signature, however, is NOT clean, and the report should not claim it is.**
The remaining ~5 s is spread across small queries whose few-percent moves are at or below the
noise floor — and that floor is set by queries the fast-path *cannot* touch. Q6 and Q15a have
**no join at all** (pure `Partial Aggregate → Seq Scan lineitem`), so their motion is pure
run-to-run variance, yet they moved **+7.1 % (Q6, 3.3→3.08 s)** and **+6.1 % (Q15a, 3.8→3.58 s)**
— *favourable* and *as large as* the biggest small-query "gains" (Q14 +10.6 %, a real
2-table `lineitem`⋈`part` hash; Q11 +8.4 %; Q17 +7.5 %; Q19 +7.1 %). The no-join noise floor
spans roughly **−1.6 % (Q1) … +7.1 % (Q6)**. So a specific small query's 2–8 % move cannot be
attributed to int64 with confidence — only the coordinated shift of the eight large cells can.
No query regressed beyond that noise floor.

**What did *not* change.** The five statistics regressions (Q2 51 s, Q4 257 s, Q8 181 s,
Q12 100 s, Q22 97 s) are still the worst cells in the document. int64 trimmed each by a few
percent but they remain 100–1600× PG because their cause — a stats-driven join order goopg
has no absolute cost model to reject (round 4 §5) — is untouched by this round. The
still-open follow-up round 4 named (a cost model) is still the open follow-up.

---

## 4. Correctness

Every R5 row count equals round 4 across all 24 slots (§1.4). This re-confirms, on SF1 data
at scale, the two properties round 4 established — parallel ≡ serial, and plan changes
preserve results — now also under the int64 fast-path and at 4-way (vs round 4's 2-way
default) parallelism. The one new correctness surface this round touched, the parallel
shared-build carrying the int64-finalised table, is exercised by the executor identity
suite (`TestParallelHashJoinIdentity` and friends), which is green; this benchmark's
matching row counts are the end-to-end confirmation.

---

## 5. Plan evolution — no shape change, more workers

Captured in [`plan_snapshots/r5-default.txt`](../plan_snapshots/r5-default.txt) (stats on,
server default 4 workers). Diffed against round 4's
[`plan_snapshots/csq-r4-parallel.txt`](../plan_snapshots/csq-r4-parallel.txt), normalising
the worker degree, the join **tree is identical** for all 22 queries — same operators, same
join order, same MHJ/Gather/Hash-Semi placements. Two second-order differences only:

1. **Worker degree 2 → up to 4.** Round 4's snapshot capped every Gather at 2; R5 shows the
   PG-faithful `compute_parallel_worker` log3 progression at the new cap of 4:
   **13 queries plan 4 workers**, **2 plan 3** (Q11, Q16 — mid-size driving scans), **4 plan 2**
   (Q2, Q8, Q13, Q22 — smaller driving relations), and **3 stay serial** (Q9, Q20, Q21 — no
   Gather placed). This is the intended effect of the 2→4 default and matches PostgreSQL's
   size-driven worker count.
2. **`rows=` estimates shifted ~1 %** (e.g. a semi-join estimate 25 932 866 → 25 686 741),
   from the int64-overflow fix in the cardinality math (`9106121e`). These are *displayed
   estimates* only; they did not flip a single join order (the tree is identical), and
   EXPLAIN's `cost=0.00..0.00` remains a literal — goopg still has no absolute cost model.

No `Gather Merge` fired on TPC-H (unchanged from round 4 — every `ORDER BY` sits above an
aggregate or join, so `findPartialSubtree` declines). The MHJ ceiling round 4 documented
(per-worker dimension rebuild caps useful degree at 2–4) is *why* raising the default to 4
is safe but not transformative for the MHJ queries (Q3/Q10/Q18): they were already near
their plateau.

---

## 6. Addendum — the cost-driven planner (`GOOPG_COST_DRIVEN_JOINORDER=1`)

Everything above is the shipping default (integer-DP planner). This section measures the
**experimental cost-driven join-order planner** on the same HEAD, same 8-table ANALYZE, same
4-worker default — the round's headline work, off by default. It answers a question round 4
§5 left open ("statistics regressions demand a cost model") with a real, mixed result.

**Methodology (differs from the default sweep, and must):** a cost-driven plan that drops
MHJ can build a binary-cascade intermediate that memory-thrashes, and — measured here — such
a query **does not honor cancellation** (the runner's 300 s timeout was ignored on Q5/Q21;
the server stayed pinned at ~10 GB RSS). So this arm used a **per-query-isolated harness**:
each query in its own runner process, 300 s per-query timeout with a 340 s external hard cap,
and a **full server restart + re-ANALYZE between queries** to clear the retained heap. Stats
reached the cost-driven planner (82 `(stats)` annotations in
[`plan_snapshots/r5-costdriven.txt`](../plan_snapshots/r5-costdriven.txt); Q9's plan shows
the composite `partsupp_pk` NLI kept, per cost-model ch. 15). Evidence:
[`sf1-r5-costdriven-cb37d166.txt`](../docs/design/cost-model/evidence/sf1-r5-costdriven-cb37d166.txt).

| Q | R5 default | cost-driven | rows | verdict |
|---|---:|---:|---:|---|
| Q1 | 4.47 | 4.40 | 4 | ~ |
| **Q2** | 51.23 | **2.72** | 459 | **WIN 18.8×** |
| Q3 | 9.14 | 5.58 | 11175 | win 1.6× |
| Q4 | 256.96 | 256.62 | 5 | ~ |
| **Q5** | 6.43 | **HANG (>300 s)** | – | **REGRESSION (memory-thrash)** |
| Q6 | 3.08 | 3.12 | 1 | ~ |
| Q7 | 138.36 | 264.37 | 4 | regr 1.9× |
| **Q8** | 181.36 | **44.33** | 2 | **WIN 4.1×** |
| **Q9** | 25.69 | **>300 s (cancelled)** | – | **REGRESSION** |
| **Q10** | 9.61 | 109.27 | 20522 | **regr 11.4×** |
| Q11 | 1.66 | 1.36 | 785 | win |
| Q12 | 100.13 | 100.40 | 2 | ~ |
| Q13 | 98.14 | 97.74 | 33 | ~ |
| Q14 | 4.07 | 4.13 | 1 | ~ |
| Q15a | 3.58 | 3.63 | 10000 | ~ |
| Q15b | 34.13 | 33.60 | 1 | ~ |
| Q16 | 1.08 | 1.06 | 18192 | ~ |
| Q17 | 4.28 | 4.26 | 1 | ~ |
| **Q18** | 28.24 | 120.26 | 7 | **regr 4.3×** |
| Q19 | 5.60 | 5.21 | 1 | ~ |
| Q20 | 2.03 | 2.09 | 92 | ~ |
| **Q21** | 20.08 | **HANG (>300 s)** | – | **REGRESSION (memory-thrash)** |
| Q22 | 96.89 | 96.81 | 7 | ~ |

**Correctness holds where it completes:** every completing query returns the same rows as the
default (and round 4). Three did not complete (Q5, Q9, Q21).

**The result is a coin flip — and the two planners are complementary, not ranked.** The
tally: **4 wins, 6 regressions (3 of them non-completing), 12 neutral.**

- **Cost-driven *fixes* the integer planner's own worst cells.** Q2 (51→2.7 s, 18.8×) and Q8
  (181→44 s, 4.1×) are exactly two of the five "statistics wrecked it" regressions round 4 §5
  named (Q2 "26× slower", Q8 "53× slower"). The default planner's stats-less small-dimension
  heuristic mis-orders these; a cost-based order avoids the large intermediate. This is the
  direct measured evidence that a cost model is the right answer to round 4 §5.
- **Cost-driven *creates* new regressions on the star/snowflake queries** — Q5/Q21 thrash to a
  hang, Q9 times out, Q10 (11.4×), Q18 (4.3×), Q7 (1.9×). Cause: the cost-driven mode drops
  MultiHashJoin (cost-model ch. 12), so a fact-with-many-dims query becomes a binary hash
  cascade that materialises the wide 6 M-row intermediates a single MHJ probe-pass would
  stream (the exact pathology of cost-model ch. 15). Some build large enough to memory-thrash
  under the cgroup cap.
- **The 12 neutral queries** are ≤2-table joins or shapes where both planners pick the same
  order — cost-driven's DP only engages for 3+-table joins.

So neither planner dominates: cost-driven and the integer planner **fix each other's failure
modes** (stats-driven mis-orders vs MHJ-drop cascades). This is precisely why cost-driven
ships **off by default** (this session's decision): its regressions — two of them
non-completing hangs — outweigh its wins as a blanket default, even though its wins are real
and large. The path to making it a net win is cost-model ch. 15's open item (re-admit MHJ to
the cost-driven DP for star shapes), which this session found could not be forced without
distorting the other queries.

---

## 7. Summary judgement

| dimension | verdict |
|---|---|
| correctness | **flawless.** All 24 row counts match round 4, under the int64 fast-path and at the new 4-worker default. |
| int64 hash-join fast-path (the round's shipped, always-on win) | **a real aggregate gain, cleanly attributed at the stream level.** The eight 100 s-class hash-heavy queries all move faster and carry ~89 % of the −3.7 % stream delta (§3); per-query small-cell moves (2–10 %) sit at the run-to-run noise floor and are not individually attributable. No plan changes; no query slower beyond noise. |
| parallel default 2 → 4 | **correct and PG-faithful.** Worker counts now follow `compute_parallel_worker` up to 4; the MHJ per-worker-rebuild ceiling (round 4) means the gain over 2 is modest for the MHJ queries but free elsewhere. |
| cost-driven join-order planner (OFF by default; measured in §6) | **a coin flip — complementary to the default, not better.** Fixes the integer planner's stats-driven regressions (Q2 18.8×, Q8 4.1×) but creates star-query regressions from dropping MHJ (Q5/Q21 hang, Q9 timeout, Q10 11.4×, Q18 4.3×): 4 wins / 6 regressions / 12 neutral. Correct where it completes. Ships off by default; the fix is cost-model ch. 15's open item. |
| net on the stream total | **1128 → 1086 s (−3.7 %)** vs round 4 at the same worker degree; a single default config now beats round 4's best full sweep. |
| vs PG overall | still 3×–1615×; the worst cells remain the **statistics regressions** (Q22 1615×, Q4 1352×, Q8 907×) — unchanged in kind from round 4, trimmed a few percent by int64. |

**The most actionable follow-up is unchanged from round 4: an absolute cost model.** This
round delivered exactly the kind of win that is available without one — a structural
executor optimisation that speeds every plan uniformly — and it moved the whole board a
clean few percent. But the five 100-second-plus cells are still statistics-driven join
orders goopg cannot cost, and no amount of per-row hash speedup reaches them. The int64
fast-path is the ceiling of what execution-layer work can do here; the next real jump is
planner-layer, and it needs a cost model.

---

## 8. Provenance

- **HEAD:** `cb37d166` (branch `costmodel-enhance1`). Round-4 baseline `648f5e47`.
- **goopg R5 default sweep:** `cmd/tpch-runner` at HEAD, one server start under
  `scripts/csq-bench-server.sh` (cgroup-capped, `127.0.0.1:65433`, data dir
  `bench/tpch/runtime_goopg/data` = the `postgres` DB), one per-table ANALYZE of all 8
  tables (bare `ANALYZE;` is a no-op on goopg — each table analysed by name), then a single
  sweep: `tmp/tpch-runner --db postgres --per-query-timeout=600s --parallel-workers=-1`
  (`-1` = the server boot default of 4 — the true default state). Cost-driven join-order
  **not** enabled (`GOOPG_COST_DRIVEN_JOINORDER` unset). Evidence:
  [`docs/design/cost-model/evidence/sf1-r5-default-cb37d166.txt`](../docs/design/cost-model/evidence/sf1-r5-default-cb37d166.txt).
- **goopg R5 cost-driven arm (§6):** same HEAD/server/ANALYZE with
  `GOOPG_COST_DRIVEN_JOINORDER=1` exported to the server, but a **per-query-isolated** harness
  (one `tmp/tpch-runner --queries=N --per-query-timeout=300s` per query, 340 s external hard
  cap, full server restart + re-ANALYZE between queries) — required because a memory-thrashing
  cost-driven plan does not honor cancellation. Evidence:
  [`docs/design/cost-model/evidence/sf1-r5-costdriven-cb37d166.txt`](../docs/design/cost-model/evidence/sf1-r5-costdriven-cb37d166.txt);
  plans [`plan_snapshots/r5-costdriven.txt`](../plan_snapshots/r5-costdriven.txt).
- **goopg R5 default plans:** `make plan-snapshot-capture LABEL=r5-default PLAN_DB=postgres` →
  [`plan_snapshots/r5-default.txt`](../plan_snapshots/r5-default.txt), stats on, server
  default 4 workers.
- **Round 4 columns (PG / R3 / R4 w4 / R4 best):** carried verbatim from
  [`tpch-evolution-round4-parallel-query-20260722.md`](tpch-evolution-round4-parallel-query-20260722.md)
  §2; `R4 w4` from [`sf1-r4-w4.txt`](../docs/design/parallel-query/evidence/sf1-r4-w4.txt).
  PG times are that document's separate-cluster plan-shape study — order-of-magnitude only.
- **Data load:** the 2026-06-13 HammerDB build (Q13 = 33), unchanged from round 4;
  reltuples verified before the sweep (lineitem 5 999 786, orders 1 500 000, …).

Single sweep, no error bars. Every numeric claim traces to the evidence file or the plan
snapshot above.
