# METHODOLOGY3 — stocktake at R130

*Written 2026-09-14 on branch `plan-parity-with-pg-take2`, at HEAD `333bb6d7b`
(R130 withdrawn). This is **not a round**. It is a stocktake: what the programme
has learned, what is still open, what is wrong with how the work is being done,
and what should happen next.*

*It supersedes nothing. `METHODOLOGY.md` still owns the measurement pipeline and
the server traps; `METHODOLOGY2.md` owns the R50 recount; `TODO.md` remains the
authoritative round ledger and K-item knowledge base. This directory adds the
layer none of those hold: a programme-level view assembled by reading all ~130
round reports end to end.*

**No measurement was run for this document set.** Every number is quoted from an
existing artefact, with its provenance and — where the artefact states one — its
caveat.

---

## The files

| file | what it answers |
|---|---|
| [`01-what-we-learned.md`](01-what-we-learned.md) | What is now **known** about goopg's planner vs PG 18.3 — 20 durable findings, each with the round that established it and, where applicable, the round that later corrected it. |
| [`02-open-problems.md`](02-open-problems.md) | What is still **unfixed** — 12 blockers, 3 correctness risks, and ~50 smaller items. Verified-fixed work is excluded by construction. |
| [`03-process-retrospective.md`](03-process-retrospective.md) | What is wrong with **how** the work is being done, with the measured cost of each pathology. |
| [`04-forward-plan.md`](04-forward-plan.md) | The proposed re-plan: the decision the owner must make, a replacement operating model, and a sequenced programme. |

Each file leads with a summary section and then a detail section. Read the four
summaries (about ten minutes) before any detail.

---

## Executive summary

### 1. The goal, and the score

Every currently-executable TPC-H and TPC-DS query must produce **the same plan as
PG 18.3** — reached by the same statistics, the same cost computation and the
same planning logic. Never by forcing shapes. A slower plan that matches is not a
regression.

| corpus | start (`ROADMAP` §1, 2026-09-09) | now (post-R128) |
|---|---|---|
| TPC-H (22) | match **2** | match **6** — Q1, Q6, Q10, Q11, Q14, Q15a |
| TPC-DS (99) | match **0** | match **2** — Q9, Q41 (see the caveat below) |

TPC-H categories at the shipped default (`r128-parity-over-throughput/parity-ON.txt`):
`join-order 14, aggregation-strategy 10, join-method 9, sort-strategy 9,
scan-type 8, parameterisation 5, qual-placement 4, rendering 1, parallelism 0`.

TPC-DS categories (`TODO.md:4857-4861`): `join-order 89, parallelism 87,
sort-strategy 76, aggregation-strategy 69, join-method 63, scan-type 57,
parameterisation 48, rendering 20, qual-placement 16`.

**Two caveats on these numbers, both load-bearing:**

- **TPC-H `parallelism 0` is a protocol artefact, not a closed category.** The
  baseline is captured by `estimate-audit -plan-only` with `-serial`, which sets
  `max_parallel_workers_per_gather = 0` on **both** engines — `TODO.md:4857`
  stamps the vector "(serial protocol)". Parallelism is measured *out* of TPC-H,
  not solved: R43 rev 3 measured TPC-H `parallelism` 18→16 under the
  gather-paths flip (`METHODOLOGY2.md` §5).
- **TPC-DS `match=2` vs `match=1` is unreconciled.** Both come from the same
  capture-script family; the difference is the **reference and the session GUCs**
  — `match=2` is `r2-instrument/capture-tpcds.sh` on `:65437` against **live** PG
  `:65438` (`TODO.md:4851-4853`, R108, R113); `match=1` is against the committed
  `bench/tpcds/plans-pg` fixture (R128). R128 verified `match=1` reproduces
  against all three in-tree references, so it is a property of the methodology,
  not a broken capture. The programme quotes both. See 02 §N22.

### 2. The verdict in one paragraph

The programme has been **technically rigorous and strategically stalled**. It has
produced genuinely durable knowledge — goopg's statistics and candidate set are
essentially PG's, its selectivity model is PG's formula verbatim, and the real
divergence is concentrated in *costing* and, underneath costing, in *row width*.
It has also produced engine fixes nothing else would have found — R110's varchar
trailing-space fidelity, R64's and R83's two wrong-rows bugs, R104's grouped-JOIN
USING/LATERAL bindings, and R126's silently-unenforced foreign keys. But the last **29 rounds
(R100–R130) moved the TPC-H match count by zero** and the category count by
exactly two instances. The reason is now known and is not a matter of effort:
TPC-H's two closest queries both block on a capability that does not exist and is
not approved to be built.

### 3. The single most important fact

**The frontier has collapsed onto one blocker.** `r127-semijoin-selectivity/FRONTIER.md`
established that TPC-H's two nearest-miss queries —

- **Q9** (1 category from MATCH), and
- **Q4** (2 categories),

— both block on the same missing capability: **narrowing what the executor
*produces***, i.e. projection pushdown and/or a packed retention format. Every
cheaper lever aimed at those two queries has now been measured and rejected:

| lever | round | result |
|---|---|---|
| force Q4's semi rows (4 values) | R71 | HashAggregate at every point — "Rows theory DEAD" |
| wire semi-join selectivity | R78 | reachable bound is 1.28×, not the 4.2× needed |
| declare all 8 TPC-H foreign keys | R125/R126 + step-(d) recon | match 6→6; three categories got **worse** |
| narrow the planner's cost inputs corpus-wide | R120–R124, shipped R128 | +2 category instances, **no flip** |
| correct the hash bucket charge (48→96) | R129 | parity-inert |

And the enabling capability is **project-level declined**, not merely unbuilt:
`docs/design/not_ralph/minimize_datum/README.md` opens **"Status: NOT APPROVED TO
START"**, and its own review records that take3 declined a new row representation
with *"no stated re-proposal path at all"*.

So the honest statement of the position is: **TPC-H parity is capped near 6–7/22
until an executor-side narrowing capability exists**, and that capability is a
decision, not a task.

### 4. The second most important fact

**Width is the dominant residual on every witness query the programme has
examined** — arrived at independently four times:

| query | round | goopg | PG | ratio |
|---|---|---|---|---|
| Q10 HashAgg entry | R120 | 3,888 B | ≈224 B | **17×** |
| Q4 semi output | R81 / R72 | 448 B | 16 B | **28×** |
| Q96 join widths | R119 | 428–1,104 B | 4–8 B | **~100×** |
| Q9 hash build | R70 | 41 cols / 461 varB / 743 MB / NBatch 8 | — | 6-col floor still fragile |

And **cost-input narrowing is not the same thing as executor narrowing** — the
distinction that R120–R124 never crossed. R124 narrowed what the cost model is
told until the mixed-currency bucket went 42,679 → **0**, and *not one cost number
moved in 121 queries*.

### 5. The third most important fact — and it is a question for the owner

**goopg's cardinality estimator is sometimes dramatically better than PG's**, and
matching PG's plan may require reproducing PG's errors. Two independent arrivals:

- **R79** measured goopg's `l_orderkey` n_distinct at ~1.17M and PG's at ~347k
  against a truth of ≈1.5M, and ruled *keep the superior statistics* — PG's
  undercount is the load-bearing "error" behind Q4's selectivity.
- **R130** established TPC-H Q9's actual output as **175 rows** — read out of
  R128's already-committed `sf1-values-{ON,OFF}.txt`, not newly measured. goopg's
  default estimate is **97** (1.8× low). PG 18.3's is **60,125** — **344× high**.

The goal accepts slower plans. It says nothing about adopting PG's mistakes.
**Recorded, escalated, unanswered** — and it is load-bearing for Q9, for Q4, and
potentially for the whole join-order programme.

### 6. What is wrong with the method

Four pathologies, each with a measured cost (detail in
[`03-process-retrospective.md`](03-process-retrospective.md)):

1. **The round is mis-sized for its most common job.** 243 of 332 commits touch
   no code. Of the 29 rounds in R100–R130, roughly a dozen terminated with no
   code at all — STOP, inventory-only, measurement-only, UNOBSERVABLE or
   withdrawn — while the two chain-closing results of the whole late programme
   (the step-(d) FK recon and the R129 bucket recon) were **cheap recons run
   outside the cadence**, each closing an entire chain in one document.
2. **Knowledge retrieval fails, and exhortation has not fixed it.** R127 was
   withdrawn on 9 findings, 3 fatal, every one refuted by evidence already on
   disk in this directory — its author opened none of the eight prior Q4 rounds.
   R130 needed three revisions, one of which proposed a cut that had landed **642
   commits earlier** with a permanent source comment headed *"WHAT THIS DOES NOT
   BUY"*. The standing "grep the directory before scoping" warning was written
   *before* R130 and did not work.
3. **The metric is structurally blind to estimates, and the match count is a
   known-mis-specified success test.** `K50`: the parity differ normalises
   rows/cost/width out, so an estimate change registers **only insofar as it
   changes plan structure** — R34 corrected a 580× cardinality error and measured
   exactly zero. (The stronger form "no estimate change can ever move a verdict"
   is *false*: R30, R31b and R36 each moved categories.) Separately,
   `ROADMAP-to-all-match.md` §3 established in 2026-09-09 that no single fix flips
   any query, and `handover-OC-to-CC-090910.md` §4 states "`match` count is NOT a
   success criterion" — yet scopes still carry match clauses.
4. **Instruments and gates have decayed.** The K18 `$$`-tempfile trap produced
   false readings in R122, R123, R124 and R128 and is still unfixed.
   `make plan-gate` has been a standing opt-out since R65 against a baseline
   un-refreshed since `warm-pin-20260905`. `tpch-spotcheck.sh` was deferred four
   rounds running — while being the only gate that caught the Q13 33-vs-34
   wrong-rows bug. **Two real correctness bugs were found incidentally by parity
   work, not by the correctness gates.**

### 7. What should happen next

Detail in [`04-forward-plan.md`](04-forward-plan.md). In short:

**First, the owner decides** — this cannot be decided by another round. The
question is **(a) or (b)**; **(c) is a scheduling decision that should be taken
now regardless of which**:

- **(a) Build executor-side narrowing.** Infrastructure. No parity prediction on
  its first slice. Unblocks Q4, and — with a measured caveat on Q9, see 04 §1.1 —
  is per R97 also the prime suspect for the worst executor cliffs (goopg is 4.1×
  slower than PG on TPC-H SF1).
- **(b) Accept the ~6–7/22 TPC-H cap** and redirect at categories that do not
  depend on width.
- **(c) Split the goal — take this now, orthogonally.** TPC-DS's dominant
  blockers (`join-order 89`, `parallelism 87`) do **not** gate on the width
  programme, so TPC-DS work proceeds regardless of how (a)/(b) is answered. It
  does not *answer* (a)/(b); it stops one corpus idling while they are decided.
- And separately, **answer §5's question**, because Q9's route depends on it.

**Second, change the unit of work** from the round to a two-tier model: cheap
**recon notes** (hours, no implementation licence, no REPORT ceremony) for
hypothesis elimination, and rare **campaigns** (one capability, pre-registered
category prediction, multiple slices) for building. The evidence is that the
programme's two best late results were already recons, and its worst rounds were
campaigns wearing a round's clothes.

**Third, before any campaign, repair the instruments** — the K18 trap, capture
arm-stamping, the TPC-DS match=1/2 reconciliation, `plan-gate` re-baselining, and
deletion of the expired default-off arms. A campaign measured on these
instruments will not be believable.

**Fourth, fix knowledge retrieval mechanically** — build a per-query and
per-mechanism index over the 130 round directories. The warning approach has been
tried and has demonstrably failed.

---

## Review record

These documents were reviewed adversarially before commit. ~80 claims were
spot-checked against primary artefacts and the live tree; the review confirmed
most to the digit and returned one BLOCK and a set of MAJOR/MINOR findings. All
were applied, the substantive ones being:

- **BLOCK** — "the transitive-equality seam is reverted to constants-only" was
  **false at HEAD** and appeared in three files, one of them as a Phase-3 gate.
  `joinsearchseam.go:461` calls `inferTransitiveEqualities` unconditionally and
  R51 adjudicated both Slice-3 tests against PG; K26 §9.3 is a pre-R51 snapshot.
- TPC-H `parallelism 0` is a **serial-protocol artefact**, now stamped everywhere
  and added to 02 as blocker **B13**.
- The declined half of the width programme is a **packed retention format**, not
  "`DatumBytes`" — the proposal leaves `Datum` at 48 B, and conflating the two is
  the mispricing `minimize_datum/05` §1 warns about.
- `entrywidth.go`'s measured **"WHAT THIS DOES NOT BUY"** caveat — Q9's `nbatch`
  is non-monotone in entry width — was being cited only as a process anecdote. It
  is now stated as technical evidence in 02 §B1 and 04 §1.1, and it **weakens
  Q9's case for option (a)**.
- "No estimate change can ever move a verdict" is **false** (R30/R31b/R36 each
  moved categories); R120's and R128's match clauses are **floors, not rises**, so
  M4 now retires only the latter.
- The "5 failing tests at HEAD" list is R43-era and must be **re-measured, not
  inherited**; Campaign B's slice 0 is now an explicit scoping recon and the
  parallelism campaign leads Phase 1.
- Restored omissions: R22/R23's on-disk ledger rows under B3; R21's absorbed-leaf
  `index_qual_cost` residue (02 §N54); the R79 curves **converging at target
  1000**, which makes PG's ndistinct undercount substantially a
  `default_statistics_target` artefact and means the two arrivals at the
  "reproduce PG's errors?" question are **not the same finding**; and
  `minimize_datum`'s own blockers 2 and 3.
- Corrected: R22/R21 are **DECLINED with proof**, not surviving gaps; three
  default-off cost arms, not four; the R63 clone experiment is fresh-vs-**stale**;
  the 1.31× drift was produced *against* R130 rev 1, not by it; the TPC-DS
  `match=2` instrument; an unsourced runtime figure; 02's Part A index; and
  several counts, line cites and cross-references.
