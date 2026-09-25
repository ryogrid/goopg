# 01 — What the programme has learned

*Durable technical knowledge about goopg's planner relative to PostgreSQL 18.3,
assembled from rounds R0–R130. Every finding names the round that established it
and, where one exists, the round that later corrected or narrowed it — this
directory's convention is that superseded claims stay legible next to their
replacement.*

*Part A is the summary. Part B is the detail. Part C records the findings that
were established and then refuted, because the refutation is itself knowledge.*

---

# Part A — Summary

| # | finding | established | later narrowed by |
|---|---|---|---|
| **F1** | All-match is a **conjunction**: every non-matching query differed from PG in 4–7 categories at once, so no single fix flips a query. TPC-H now has genuine low-end exceptions (Q9 at 1, Q4 at 2). | ROADMAP §3 | METHODOLOGY2 §3, FRONTIER §1 |
| **F2** | goopg's **statistics set and candidate set are essentially PG's**. The divergence is in *costing*. Rev 1's "missing candidates" thesis was falsified by review. | root-causes rev 2 | — |
| **F3** | The **selectivity model is PG's formula verbatim** at the sites tested — `eqjoinsel_inner`'s no-MCV branch, agreeing to 4 decimals on shared inputs. | R118, R119 | — |
| **F4** | **Width is the dominant residual on every witness query** — 17× (Q10), 28× (Q4), ~100× (Q96), 743 MB build (Q9). | R70, R72, R119, R120 | — |
| **F5** | **Cost-input narrowing ≠ executor narrowing.** Narrowing what the cost model is *told* was driven to completion and moved nothing. | R120–R124 | R128 (R122's half *was* real) |
| **F6** | Opening the join-order **candidate** space moves join-order by **zero**; pricing is the only live dimension. | R51, K26 §9.2 | R53, R68, R96 |
| **F7** | Elections are decided by **tiny exact margins**, and PG's `STD_FUZZ_FACTOR 1.01` genuinely decides real plans. | R53, R81, R96 | — |
| **F8** | goopg's **ANALYZE is structurally different and often more accurate than PG's**, so parity may require reproducing PG's errors. | R79 | R130 (second, sharper arrival) |
| **F9** | goopg does not keep a sub-problem's **pathlist alive across the planning boundary** — CTE/derived/subquery rels arrive as one opaque `PathPrebuilt`. | R123 | — |
| **F10** | A whole **legacy/prebuilt planning route runs in parallel** to the path search; forced-shape queries never reach the cost seam. | R115, R116, R118 | — |
| **F11** | The **display seam is a planner input**, not cosmetics — an un-stamped node's childless display cost feeds the aggregate election. | R73 | R77 (fixed for the EXISTS family) |
| **F12** | goopg parallelises by **stamping a Gather over a serial subtree**; PG builds **partial paths and lets them win**. Three distinct worker semantics were needed. | K16, R94, R95 | K80, K82, K92 |
| **F13** | A filed partial path is **not an executable one**; path-kind admission ≠ node-side executability, and five mirror walks must change together. | R60, R86, R92, R93, R95 | — |
| **F14** | goopg's **two aggregation rivals were priced in different currencies** — and correcting that alone over-charges ~17× and is net-negative. | R120 | R124 §7 |
| **F15** | PG's `GroupAggregate` preference is **pathkey-driven, not spill-driven**. | R120 §5 (inference), K12 | — |
| **F16** | Aggregate input width is **unreachable from `Path.NCols` by invariant**. | R121 scope | R124 §7 |
| **F17** | PG's planner **never consults `convalidated`**; and FK evidence **does not fix join-order**. | R125, step-(d) recon | — |
| **F18** | **Rendering is cheap, separable and high-yield** — 8 → 1 blocked TPC-H queries across four renderer rounds, two of them producing MATCHes. | R65, R66, R85 | — |
| **F19** | Several PG cost terms are **structurally absent** from goopg; several others are faithful ports verified by measurement. | R57, R58, R69, R89 | — |
| **F20** | **Values-green sweeps cannot detect qual-placement loss**, and two real wrong-rows bugs were found incidentally by parity work. | R56, R63, R83 | — |

---

# Part B — Detail

## F1 — All-match is a conjunction, and the low end has now cracked

`ROADMAP-to-all-match.md` §3 established the governing arithmetic in
2026-09-09: **no query had join-order as its only divergence — zero** — and every
non-matching query differed from PG in four to seven categories at once.
Therefore no single fix flips any query to MATCH, and progress shows up as
categories falling, converting to matches only at the very end.

This is why R1 (qpqual currency), R3 (hashagg spill), R6 (window Sort) and R21
slice 2b were each correct, PG-faithful, and each moved the match count by zero.
That was arithmetic, not failure.

`METHODOLOGY2.md` §3 recorded the first crack: the category-count distribution
over shapediff queries showed TPC-H `{1:1, 2:1, 3:1, 4:5, 5:5, 6:4, 7:3}` — real
nearest-misses had appeared. `FRONTIER.md` §1 sharpened this to the current
position: **Q9 at one category, Q4 at two**.

`METHODOLOGY2.md` §3 also recorded a measurement subtlety that still matters: **a
MATCH can carry a category tag**, so raw category counts overstate "blocked" by
the number of tagged matches. Its illustration was Q13, which matched while
carrying `[rendering]` — note that example is now stale (R72 records *"Slice-2's
Q13 MATCHED no longer holds"*); the live carrier of the residual `rendering 1` is
**Q10**. Future
recounts should report `blocked-excluding-matches` alongside the raw tool line.

## F2 — The statistics and the candidate set are essentially PG's

`plan-parity-root-causes.md` rev 2 is the authoritative statement, and its history
is part of the finding. **Rev 1 blamed a missing candidate set** — index paths not
reaching the search — and an adversarial review falsified it completely. The cause
of that error is recorded rather than dropped: rev 1 read `generateScanPaths`
(`pathgen.go`), assumed it was production code, and never checked its callers.
Every caller is a test. The production seed is `newPrebuiltPath`.

What rev 2 establishes:

- **Statistics**: `NDistinct`/`NDistinctFrac`, `NullFrac`, `AvgWidth`, `MCV`,
  `Histogram`, `Correlation` and extended (multivariate) statistics are all
  collected *and* consumed. Nothing is computed but never read.
- **Candidates**: `addBaseRelIndexPaths` (`pathindexordered.go:40-49`) is, by its
  own header, "the whole of `create_index_paths`", running five producers
  including an ungated bitmap enumerator. The sequential rival is in the same
  comparison.
- **The narrow structural gaps rev 2 identified have since been closed by
  measurement, not by code.** Rev 2 named two: no unconditional plain-index-scan
  arm, and `addOrderedIndexPaths` gated on `hasUsefulPathkeys`. **R22 DECLINED
  both on 2026-09-09 as provably unwinnable** — implemented, pinned, probed at
  145 offers / **0 survivals** across TPC-H, A/B zero bytes, reverted in full: a
  full-fetch index scan is strictly dominated by seq on both cost axes whenever a
  seq path exists, and *PG's own comparator prunes the same way* (live PG shows
  zero cond-less index scans on TPC-H). Its sibling, **R21** (the index-leaf
  repricing hole, K2), was **DECLINED for want of a witness**: an instrumented
  census counted 1,282 leaf pricings across both corpora with **zero index
  leaves**. One residue is genuinely open and is easy to lose: *absorbed-leaf
  `index_qual_cost` needs its own design — Filter-chain counting cannot reach it
  by construction* (02 §N54).

Since then the architecture has moved further toward PG: `internal/optimizer/` is
now **132 non-test files / ~82k lines** (390 files / ~157k lines including tests)
with a faithful `join_search_one_level` / `standard_join_search` port wired ON by
default (`GOOPG_PGSHAPED_DP`, see
`joinsearchlevel.go`'s header). So the join-order gap is **not** missing
enumeration — see F6.

## F3 — The selectivity model is PG's, verbatim

R118 attributed Q96's entire lower-join cardinality divergence — 384 rows — to a
single input: a **0.05% ANALYZE-measured nullfrac difference amplified by a
719,876-row outer**. The equation was frozen: `719876 × (1−0.043633334 −
(1−0.044166666)) = 719876 × 0.000533332 = 384`. No model gap at the pricing site;
no MCV; no superkey firing; no saturation.

R119 then read PG's oracle and closed the question: `eqjoinsel_inner`'s no-MCV
branch is `MIN(1/nd1, 1/nd2) × (1−nullfrac1) × (1−nullfrac2)` — **the same model
as goopg's `pairNullSelectivity/nd` (`cardinality.go:1080`)**. Plugging shared
numbers agrees to four decimals (0.00013277 vs 0.0001328287).

Two consequences, both load-bearing:

1. There is **no transcription gap** at the selectivity site, and "fixing"
   R118's inputs would mean overriding faithful ANALYZE measurements.
2. **PG's own preference does not come from selectivity either** — PG's hdem-first
   lower join is *bigger* (22,198 vs 18,504) and its total is *smaller* by ~176.
   R119 therefore redirected the whole Q96 investigation at **widths**.

## F4 — Width is the dominant residual, arrived at four times independently

| witness | round | measurement |
|---|---|---|
| TPC-H Q12 | K65 | `orders` width 448 / `lineitem` 550 vs PG's 22 / 17; a 1.5M-row `orders` build = 641 MB vs PG's 31 MB |
| TPC-H Q9 | R70 | 303,093 × 41 cols × 461 varB → entry 2,453 B → **743 MB, NBatch 8**, spill charge 375,608 |
| TPC-H Q4 | R72, R81 | semi output 448 B vs PG's 16 B — "widths ratio 28× **exceeds** the rows ratio 16.6×" |
| TPC-H Q10 | R120 | HashAgg entry 2,112 B → 3,888 B under the currency fix; **PG's entry for the same node is ≈224 B — a 17× gap after the "fix"** |
| TPC-DS Q96 | R119 | PG widths 4–8 B vs goopg's 428–1,104 B |
| TPC-DS Q95 | R128 | width **564 → 16** under narrowing |

K65 names the blast radius precisely: hash geometry, spill/batch counts, Gather
transfer volume, and sort footprints — i.e. it corrupts four cost families at
once. K67 adds the critical qualifier: **column pruning is necessary but not
sufficient.** Even narrowed to one column goopg is 72 B/row → 103 MB and still
spills at `work_mem=64MB`, where PG is 22 B/row → 31 MB. The residue is
`DatumBytes = 48` (`internal/executor/hashsize/hashsize.go:46`) against PG's
~22-byte MinimalTuple.

R97's timing survey independently named the same thing: the row-width gap is the
**prime suspect for the worst executor cliffs** (TPC-DS Q61 70×, Q58 26×, Q55
23×, Q88 21×).

R112 adds a boundary condition that must not be forgotten: **there is no
defensible whole-planner "PG Datum size" substitution.** Each cost family has a
different PG representation, so `DatumBytes` / `hashsize.EntryBytes` /
`Path.OutputWidth` must never be generalised to one universal value. R112's own
recommendation was Sort-only as the single defensible follow-up — which R113 then
built and measured as parity-inert.

## F5 — Cost-input narrowing is not executor narrowing

This is the distinction the R120–R124 chain never crossed, and naming it is
probably the chain's most valuable output.

- **R121 (Slice A)** narrowed base-rel cost widths. TPC-H moved **literally
  zero** (byte-identical including costs); TPC-DS produced 152 diff lines and real
  join-order changes with every category identical.
- **R122 (Slice B)** propagated narrowing through join paths and produced **the
  first parity improvement of the chain**: TPC-H `join-method` 10→9 and
  `scan-type` 9→8, both from Q3, whose join subtree became shape-identical to
  PG's. Mechanism measured, not inferred: a 141,795-row `orders ⋈ customer` build
  went 17 cols / 1,052 B / `nbatch=2` (SPILLS) to 6 cols / 321 B / `nbatch=1`
  (FITS). This **refuted R121's own story** that TPC-H had nothing near a batch
  boundary — R121 saw nothing because its narrowing never reached a join.
- **R123** resolved the TPC-DS confound to one cause (see F9).
- **R124** finished the job: the mixed-currency bucket went **42,679 → 0** — and
  *not one cost number moved in 99+22 queries*. The reason, measured: for **22 of
  32 rels `kept == full`**, so the "mixed currency" that had consumed two rounds
  was a **census label**, not a currency mismatch.
- **R128** promoted the flag to default ON, banking R122's two categories at a
  throughput cost of **+0.216%** — indistinguishable from zero against the run's
  own ±26% relative noise floor, and not the ~10% the round had been scoped to
  accept (that figure belonged to a *different* implementation,
  `tmp/d05p3-costside-narrow.patch`).

`FRONTIER.md` §3 states the conclusion: **R120–R124 narrowed what the cost model
is TOLD; R81's blocker is narrowing what the EXECUTOR PRODUCES.** Corroborated by
the structural fact that *inside a join tree there is no `*Project` above the scan
at all* — there is nowhere to hang a projection today.

A partial correction worth keeping: R124 §4b refuted the chain's own claim that
"join-order and parallelism are not width-driven" — `joinpathsparallel.go:210-213`
consumes the width triple directly, and three TPC-DS plans do change shape under
the flag. The defensible claim is narrower: **width changes do move TPC-DS join
order and method — just not toward PG.**

## F6 — Candidate generation moves join-order by zero; pricing is the live dimension

`K26-join-order-implied-equalities.md` measured join-order's cause: goopg's seam
withheld transitive `a = c` equalities from the DP search, so it declined
`{part}|{partsupp}` with `reason=no-join-clause` (20 such declines) where PG
synthesises them via EquivalenceClasses. The obstacle was reframed three times by
measurement — "breaks plan layouts" → "breaks a remap" → "one helper does not
recognise one inner shape" — and turned out to be a test helper pinning a node
kind its own file documents as an optimisation.

**R51 landed the candidate half** (a one-line flag flip at
`joinsearchseam.go:458`, now `:461`).
Result: TPC-H `join-method` 11→9, **`join-order` 18→18**; TPC-DS `join-order`
95→95. K26 §9.2 confirmed empirically: *the DP now enumerates PG's pairs but still
prices other orders.* Candidate generation is necessary and **not sufficient**.

Three attribution rounds then independently converged on pricing:

- **R53 (Q9)**: sizing OUT (L5 ties at 303,093 both spines), admission OUT (PG's
  L6 partition offered, zero L6 declines), parameterisation OUT (`reqouter={}` on
  every winner). **Pricing IN** — a 2.5% margin at L6.
- **R68 (Q9, re-measured)**: the L-table had changed completely (NLI now wins
  L3–L6), and R53's 2.5% question is **superseded, not answered**. Same
  adjudication: sizing/admission/parameterisation OUT, pricing IN, two-sided.
- **R96 (Q96)**: margin **157.50 (0.68%)**; rows ruled out (5,477 vs PG's ≈5,484).

R53 also contributed a framing that recurs: **a locally-cheap relset is not a
globally-cheap plan** — PG's L4 relset is 5× cheaper absolutely and still loses at
L6.

**Do not re-read K26 §9.3 as current.** That section is a pre-R51 snapshot
describing the seam as constants-only. R51 landed the closure and adjudicated both
named tests against PG (`TestSlice3LiveQ9ShapeDerivation` re-baselined 10→8 as
JUSTIFIED; `TestSlice3SelfJoinInDerivedTable`'s pin updated with the F4 loop
untouched). At HEAD `joinsearchseam.go:461` calls `inferTransitiveEqualities`
**unconditionally**, under a comment ending *"the candidate half is now open."*
What remains open is the **costing** half only.

## F7 — Tiny exact margins, and PG's fuzz factor really decides plans

Elections turn on fractions of a percent:

| contest | margin |
|---|---|
| Q9 L6 hash-vs-hash (R53) | 616,861.02 vs 632,364.99 — **2.5%** |
| Q96 L3 (R96) | 23,021.71 vs 23,179.21 — **157.50, or 0.68%** |
| Q96 forced-order, valid oracle (R111) | PG prefers hdem-first by 175.91; goopg prefers **store-first** by 7.68 — *an inverted preference* |
| Q4 grouping (R72) | the outlier — a **4.26×** gap, no tie at all |

`add_path` uses a 1% fuzzy comparator, but **`setCheapest` takes the exact raw
minimum on both engines** (R96 PROBE) — so fuzz admits, and exactness elects.

R81 and R72 established that PG's `STD_FUZZ_FACTOR 1.01` decides real plans. Q4:
PG's totals *and* startups tie inside fuzz, so pathkeys decide and sorted wins
with no top Sort. goopg's pair is fuzz-equal on both axes (total 1.0095, startup
1.0086), so its `M0129-S1` tie-break fires and picks the actual-cheaper — hashed.
PG's own startup ratio is 1.0118, **outside** fuzz. A hygiene item was recorded
and explicitly not proposed: **goopg's tie-break never returns `costsEqual`**,
diverging from PG's `COSTS_EQUAL → pathkeys decide` rule.

## F8 — goopg's estimator is sometimes better than PG's, and that is a problem

Two independent arrivals, five rounds apart.

**R79 (the ndistinct sampler).** goopg does a full block scan plus an Algorithm-R
reservoir; PG samples up to `targrows` *random blocks*. Same Duj1 estimator,
different block representation. On insertion-ordered `l_orderkey` goopg's uniform
sample is f1-rich: goopg reads flat ~1.17–1.21M at every *usable* statistics
target (at target=1 it returns the degenerate `-1`), PG reads 343,831 / ~336k /
~1.216M, and **true distinct is ≈1.5M**. goopg is closer to truth; **PG's
undercount is the load-bearing "error"** behind the 0.2317 semi selectivity Q4
needs. R79's verdict was **(b): keep the superior statistics and close Q4
elsewhere** — replicating PG's sampling would deliberately degrade every
ndistinct consumer corpus-wide to chase one derived fraction.

**A qualifier that bears directly on B7.** The two curves **converge at target
1000** — 1.206M vs 1.216M, ~0.8% apart — via PG's by-design absolute→fraction
switch. So on *this* witness PG's undercount is substantially a
`default_statistics_target` artefact rather than an irreducible estimator
difference. That does not dissolve R130's question (Q9's 344× is not a
sampling-target effect), but the two arrivals are **not the same finding** and
must not be merged into one argument.

**R130 (Q9's ground truth).** TPC-H Q9 at SF=1 returns **175 rows** — read out of
R128's already-committed `sf1-values-{ON,OFF}.txt` (identical in both arms), not
newly measured:

| estimate source | estimate | vs actual |
|---|---|---|
| goopg, no FKs | 122 | 1.4× low |
| **goopg, current default** | **97** | **1.8× low** |
| goopg, 8 FKs declared | 5,000 | 28.6× high |
| **PG 18.3** | **60,125** | **344× high** |

So *"a more accurate Q9 cardinality produces a less PG-like plan"* is **false** —
the FK arm moved the estimate 20× *further* from truth and produced a worse plan.
The correct framing is the inverse: **matching PG's Q9 plan may require
reproducing a 344× PG estimation error.**

The programme's goal accepts slower plans. It says nothing about adopting PG's
mistakes. R130 recorded this as a question for the goal's owner and did not
resolve it. It remains open — see `02-open-problems.md` B7.

## F9 — The sub-problem pathlist does not survive the planning boundary

R123's census resolved TPC-DS's 42,679 mixed-currency comparisons to **one cause,
at 100%**: they all root at `PathKind 0` = `PathPrebuilt`.

The mechanism: goopg does not keep a sub-problem's pathlist alive across the
boundary. A CTE body, a derived table or a FROM-subquery is planned by its own
`planSelect` and republished as a **Node**, then admitted as a single
`PathPrebuilt` rel with `ri.table == nil` — so `ColVarBytes` is never populated
and the rel declines narrowing.

Just **32 declining rels produce all 42,679 mixed comparisons** (combinatorial DP
pairings). Their measured identity: **CTEScan 14, Project 9, SetOp 7, Filter 2,
ordinary-table-lacking-stats 0**.

R123 also refuted two hypotheses, including its own reviewer's: the collector-arm
hypothesis (144 arm-(a) declines are search-problem-uniform, so they land in
BOTH-UNNARROWED and contribute **zero** mixed pairs — the pre-registered falsifier
did not fire) and the set-op/aggregate-wrapper hypothesis (**zero** instances).

This is a genuine architectural difference from PG's RelOptInfo model, and it is
the same structural fact K31 approaches from the other side: goopg **materialises**
CTEs where PG **inlines** them (PG 12+ `inline_cte`) — TPC-DS goopg 111 `CTE Scan`
vs PG 68, with 40 of 63 declarations single-reference and inlinable.

## F10 — A parallel legacy planning route exists, and forced shapes never reach the cost seam

R115 ran a negative attribution experiment with a live positive control and found
that Q96's two forced-order forms emitted **zero** path-cost records, while the
control on the same binary and server emitted two plus `DPPATH join.hash`.

Mechanism: `tryJoinSearch` (`joinsearchseam.go`) preserves the syntactic node when
`tryPGShapedJoinSearch` declines, and the latter has an explicit
`chainCarriesLateral` decline that Q96's final `JOIN LATERAL time_dim` satisfies.
R116 and R118 confirmed the route: all four selected joins are Hash/BuildRight via
the `legacy-direct` `planner.go` join site — **3× `legacy-join`, zero path-backed
firings**.

This retroactively invalidated the attribution in R106 and R108 and will invalidate
any successor that uses forced `join_collapse_limit=1` forms without first
re-establishing which route the query takes.

The seam has **18 distinct decline reasons** (`grep traceSeamDecline
internal/optimizer/*.go`): `outer-over-derived`, `size-or-no-joinlist`,
`outer-spine`, `prefix-exceeds-bindings`, `prefix-size`, `prefix-not-a-prefix`,
`chain-not-flattenable`, `leaf-count`, `lateral`, `nil-leaf`,
`offset-disagreement`, `spine-offset-disagreement`, `inner-on-qual-above-outer`,
`outer-link-no-sjinfo`, `outer-on-qual`, `inner-on-qual-under-nullable`,
`residual-hits-pad`, `spine-width`. K27(b) states why this matters as a *parity*
signal, not just a performance one: **a declined query falls to the legacy path
and cannot converge on PG's plan by any amount of costing work.**

Current decline census: **TPC-H 0, TPC-DS 5** (`outer-over-derived` 3 blocked on
the separate B-06 CTE-stats workstream per K68; `outer-spine` 2 per K70), down
from 13 at R26. R29's method note binds: decline censuses are only comparable at
equal timeouts.

## F11 — The display seam is a planner input

R73's attribution is one of the sharper findings in the programme. **No SEMI path
is ever filed** — zero `addPath` calls with `Jointype == JoinSemi`. The emitted
`*NestedLoopIndexJoin` bypasses `stampPlanCost`, so `DeriveLegacyDisplayCost`
prices it **childless**: `0 + 0.01 × 57,066 = 570.66`.

And `groupingpaths.go:77` reads `legacyDisplayCostOf(child)` — so the **aggregate
and ORDER BY elections price on a seam number no planner ever computed**.

R74 traced the fork (`unnestExistsExpr` consumes the EXISTS subquery into a legacy
`Join{SEMI, Hash}` before any search joinrel exists, so no sjinfo → no joinrel →
no path → no price), R75 proved no new machinery was needed, R76 probed it, and
**R77 landed a two-site splice**. The dual placement was not decoration:
end-of-pipeline alone left **stale upper stamps**, with an ordered winner at
1,426.78 sitting above a repriced 491,169 HashAgg — a parent cheaper than its own
child.

K63 records the general form and it is still open: goopg's EXPLAIN reports a scan
cost the planner did not use (60,299.79 rendered vs 271,421.24 consumed — 4.5×).
It does not affect plan choice, but it corrupts every cost-based artefact.

## F12 — goopg stamps Gathers; PG builds partial paths

K16 is foundational and was established by falsifying its own predecessor. K15
predicted the parallel gap was a missing field on `optimizer.Join`; after the field
was plumbed, goopg emitted `Parallel Hash Join` **zero** times (PG: 9 TPC-H, 139
TPC-DS). The real difference: **goopg parallelises by STAMPING a Gather over a
serial subtree (`stampParallelScan`); PG parallelises by building PARTIAL PATHS and
letting them win.**

Scale of the divergence (K37): 66 of 99 TPC-DS queries PG plans parallel and goopg
plans serial — **zero the other way**. `Parallel Hash`: goopg 0, PG 314. It feeds
`join-method`, `scan-type`, `aggregation-strategy` and `sort-strategy`
simultaneously.

Three corrections have narrowed the lever since:

- **K80**: `Parallel Hash Join` needs no new `parallel_hash` work —
  `addPartialHashJoinPath` already sets `ParallelAware: true`. It is dead solely
  because `GOOPG_GATHER_PATHS` is default-OFF.
- **K82**: TPC-H Q14 is **byte-identical under the flip** — its Gather comes from
  the partial-aggregate path. The two tracks are **independent**, refuting the
  earlier sequencing claim.
- **K92**: relabelling goopg's leader-prebuild as `Parallel Hash` would be a
  **misdescription** — it asserts a partial inner path consumed by the Gather's
  own worker set, which is not what goopg does. Stamping it would be exactly the
  arbitrary plan-forcing the goal forbids. **Q14's third match is not cheap**; it
  needs PG's real execution model.

R54's admission census added the operational rules: *"plan has a Gather" does not
imply join-level admission fired* — attribute per Gather-site, not per plan; a
starved leaf kills starved-**led** orientations only; and `terminatesPartial`
stopping at `*Limit` makes the sort route unreachable **by construction, not by
cost**.

The R86→R95 campaign then established that **three distinct worker semantics** are
required: partial hash/merge (pre-existing), ordinary INNER NL (R94), and
lateral-probe-over-a-worker's-own-partition (R95). Only the third unlocked Q96 —
which then planned `Finalize Aggregate → Gather(3) → Partial Aggregate → Nested
Loop` with parallel hash joins, **chosen by cost, not forced**, closing four
categories (aggregation-strategy, join-method, sort-strategy, parallelism) at
once.

## F13 — A filed partial path is not an executable one

The single most expensive lesson of the R86–R95 campaign, learned in five
instalments:

- **R60**: `cpgather "admitted"` means **candidacy only**. 257 partial NL paths
  were filed and the TPC-DS plan channel read `same=99, changed=0`.
- **R86**: `partialPathDrivingKind` refusing `PathNestLoop` is **correct** until
  every mirror walk can claim only the NLI outer scan — otherwise every worker
  reads the whole relation and emits duplicates.
- **R92**: a `PathPrebuilt` wrapping a **shared** node is an exact representation
  barrier to copy-safe reconstruction.
- **R93**: **path-kind admission ≠ node-side executability.** Admitting
  `PathNestLoop` to the generic Gather reader by path kind produced a hard planner
  error (`PathGather over a subtree with no driving scan`). The change was removed.
- **R95**: `HasShareableHashJoin` not descending approved-NL outers silently
  produced **workers building partial hash tables with missing rows** — a
  wrong-results hole that existed latently for R94's shape too.

The durable rule: `partialPathDrivingKind`, `terminatesPartial`, `drivingScan`,
`stampParallelScan` and `attachParallelScan` form a **five-way mirror set that must
change together** (stated in R54 Step-1 §7, R60, R86 §4, R94 and R95). This is a
specific instance of the repo's standing `pattern_sibling_paths_must_agree` rule,
and K81 states its inverse hazard: if the planner emits `parallel_hash=true` where
the executor's `parallelBuildEligible` declines, the plan claims parallelism the
executor does not perform.

## F14 — The aggregation rivals were priced in different currencies

R120 confirmed a real defect from source: `hashAggEntrySize`'s parameter is
literally named `tupleWidth` and the `pages` comment cites
`relation_byte_size(input_tuples, input_width)`, yet both received only the
*variable* payload — while goopg's **sorted** rival pays full
`EntryBytes = 48 × ncols + 24 + avgVar`.

And correcting it made things worse. Three of five pre-registered bars failed:
TPC-DS `aggregation-strategy` **69 → 71**, and TPC-H **match 6 → 5** (Q10 lost).
Mechanism, measured: Q10's entry went 2,112 B → 3,888 B, taking `groups × entry`
from 92.9% to **171%** of the 128 MiB budget and flipping HashAggregate to
GroupAggregate + Sort. The `48 × ncols` term alone contributes 1,776 B.

The durable finding: **correcting the currency while `ncols` is the full
concatenated relation width converts an under-charge into an over-charge** — and
PG's entry for the same node is ≈224 B, so the "fixed" figure is still 17× out.

R120 pre-registered a pairing hypothesis (that ncols narrowing was the missing
half). **R124 §7 refuted it by measurement**: narrowing + R120's arm reproduces
R120's arm alone *exactly* (GroupAgg 29, HashAgg 123, `aggregation-strategy` 71).
Verdict: **delete `GOOPG_HASHAGG_WIDTH_CURRENCY`** — which has not been done (see
02 §N19).

## F15 — PG's GroupAggregate preference is pathkey-driven

R120 §5 states this as a well-supported inference rather than a corpus
measurement: a currency now over-charging ~17× relative to PG should have
over-fired if PG's ~126 GroupAggregates were spill-driven, and it moved only 6.

This corroborates K12, the dominant aggregation cause: **PG picks
`GroupAggregate` mostly because it delivers an ordering something above needs, not
because the hash spills** — Q81's and Q12's group counts fit trivially. **goopg's
upper planner does not model ordering requirements.**

K24 ties this to the parallelism track through one root cause: the upper planner
receives a **finished `Node`**, not the join rel's paths. K23 needs
`PartialPathlist`; K12(B) needs `Pathkeys`. Same fix — and K24 warns explicitly
that they must not be scheduled as independent rounds. Slices 1, 2a and 2b landed;
**slice 3 (the ordering contest) is still open** and is, per K24, the largest
single item in the workstream.

K96 and K97 record why the obvious route is closed. The shape PG picks is
**unreachable by construction**: `rebuildWithGather` has mutually exclusive arms
and `splitAggregate` hardcodes `NewGather` while copying the original strategy, so
`Finalize GroupAggregate` cannot be produced (goopg 0/0 vs PG 5/18 and 9/85). R45
proposed a fix and was **rejected on review as architecturally impossible**:
goopg's Partial aggregate emits **zero rows** (it uses a side channel,
`o.rows = nil`), so a Sort between Partial and Gather would sort an empty stream
and Q1 would return unordered rows. K97's trap is worth repeating: `aggregateOp`
already sorts for determinism — **do not mistake incidental ordering for a
pathkey.** The real item is PG's `AGGSPLIT_INITIAL_SERIAL` /
`AGGSPLIT_FINAL_DESERIAL` — a multi-round executor programme.

## F16 — Aggregate input width is unreachable from `Path.NCols` by invariant

`aggInputWidth` reads `len(child.Output())` off the **built node**, and that node
comes from `createPlanAtSearchRootRange`, which must publish the full binding
concatenation and panics on any hole (`createplanroot.go:100-140`).

R121's scope established this *before* implementation, refuting R120's pairing
plan on paper; R124 §7 then confirmed it by measurement. This is a good example of
the programme working as intended — a design-stage refutation that saved an
implementation round.

## F17 — PG ignores `convalidated`, and FK evidence does not fix join-order

**R125** read the oracle: `get_relation_foreign_keys` (`plancat.c:642-644`) skips
only `!conenforced`; `RelationGetFKeyList` never carries `convalidated` into
`ForeignKeyCacheInfo`; and `grep convalidated postgres/src/backend/optimizer/` is
empty. goopg had been refusing `NOT VALID` FKs at two sites; both now gate on
`NotEnforced` alone. A pin was deliberately **reversed**
(`TestCalcJoinrelSizeInvalidFKIgnored` → `…HonouredLikePG`) and mutation-tested.

**R126** then found and fixed a silent correctness bug: after a restart
`pg_constraint` showed **0 rows** *and referential integrity was silently
unenforced* — an orphan INSERT succeeded. Two independent causes. Its review found
two more instances of the same bug elsewhere (ATTACH PARTITION's cloned FKs were
never journalled; non-public schemas silently lost FK persistence). The round's
own diagnosis of why it existed is worth quoting: **"nothing in the suite crossed
an `Open → Close → Open` boundary with an FK declared."**

**And the thesis the chain was built on is refuted.** The step-(d) recon declared
all eight TPC-H FKs on the real SF=1 corpus and measured: match **6 → 6**;
`join-order` stays **14** — the category the chain was aimed at — while
`join-method` 10→11, `scan-type` 9→10 and `qual-placement` 4→5 each move the
**wrong way**. The FK arm is live and is *not* redundant with the index arm (the
index arm supplies an upper bound, which cannot bind on an under-estimate).

Two facts outlive the chain:

- **The whole FK programme is TPC-H-only.** TPC-DS's base DDL declares 24 PRIMARY
  KEYs and **zero** FOREIGN KEYs; its FKs live in `tools/tpcds_ri.sql`, which
  nothing under `bench/tpcds/`, `scripts/` or the `Makefile` references.
- A new pre-existing bug: `ALTER TABLE … DROP CONSTRAINT` on an FK **reports
  success and does nothing** — see 02 §N1.

## F18 — Rendering is cheap, separable and high-yield

Four renderer rounds took TPC-H's `rendering`-blocked set from 8 to 1, with
**values 24/24 at every binary step and zero structural moves**:

| round | change | result |
|---|---|---|
| R65 | Sort-key OUTER_VAR expansion + InitPlan value | **Q11 → MATCH**; rendering 8 → 5 |
| R66 Slice 1 | `sortGroupKeySource`, Star/Distinct admission, `count(*)` rendering | 5 → 4 |
| R66 Slice 2 | `resolveKeySource` chase with a two-half boundary rule | 4 → 1 (only Q10 left) |
| R85 | `PARAM_EXEC` deparsing from the owning SubPlan | **TPC-DS Q41 → MATCH**; `match=1 → 2` |

Durable rendering rules discovered: Sort keys in the group section render
`GroupExprs[idx]` in written order; the key chase must **stop** at sublink-predicate
Filters and CTE-output qualifiers (both PG-adjudicated live); computed targets
containing table-0 refs must decline; `PARAM_EXEC` deparsing must be owner-scoped
with atomic validation and `$N` fallback everywhere unproved.

K11(d) was the originating observation — Sort/Group keys rendered as output
aliases where PG renders source expressions.

R67's re-triage of `Materialize` is the counter-example that proves the category
discipline: live PG uses `Materialize` **exactly once** across TPC-H (Q5's 1-row
region NL inner) and goopg prints zero — but **Materialize is not a verdict
category**, no matching query needs it, and a 36-site census found no coinciding
site. A perfect producer would move zero categories and zero matches.

## F19 — Which PG cost terms exist, and which do not

**Verified faithful by measurement** (not assumed): `get_parallel_divisor` (R57);
`compute_parallel_worker`'s log3 ladder (R58); partial-join outer-takes-all worker
inheritance and `Gather num_workers = subpath->parallel_workers` (R58);
`parallelSetupCost = 1000` and `parallelTupleCost = 0.1`, derived independently
from traces (R54); `initial_cost_hashjoin`'s common component, exactly, on all
four Q96 input rows (R89); per-row NL join increments (R96: 0.00353 vs 0.00356).

**Mis-transcriptions found and fixed**: the missing `(outer − 1) × rescan_startup`
term in `nestloopCost` (R69, `costsize.c:3299-3302`); `genericcostestimate`'s
`num_scans > 1` arm and numSA clamp ordering (R59); the unique-index superkey
shortcut substituting FK selectivity for bare uniqueness without null fractions
(R87 → R88).

**Structurally absent and still absent**: SEMI/ANTI early-stop in nestloop costing;
qual startup as a separate term; tlist per-output cost; general `QualCost`
startup/per-tuple split; MCV-frequency suppression; join pathtarget startup and
per-tuple costs (R69 P0, R89, R90, R91). R89's phrase for this is the right one:
the search **never marks a join inner-unique**, so its cost function always
executes the non-unique bucket-walk formula — a gap R90 then partly closed for
proved INNER bare-unique inners.

**Deliberate, justified divergences**: `indexProbeCostMultiplier = 2.0`, scaling
heap-I/O bounds only, because goopg's executor materialises the whole TID list
eagerly (R58) — see 02 §B8 for the conflict this creates; and goopg's zero
unmatched-bucket walk charge, because its canonical map key has no candidate slice
(R90).

**Not mirrored, deliberately**: `add_partial_path_precheck` — goopg files and then
dominates astronomically-priced plain-NL arms PG would have bailed on before
costing (R60). Planner CPU waste, not a correctness or parity issue.

Two cost-model facts from the K-ledger belong here. K60: goopg prices seq scans
**two different ways**, differing by the entire page term (`lineitem` 196,405.55
vs 60,012.55) — but K61's conclusion that this decided Q12 was **withdrawn**, and
K62 established by instrumentation that the search used the **correct** cost. K61
described the *rendering*, not the decision. The method note both rounds produced
is now standing: **instrument the term, never infer it from the sum.**

## F20 — The values channel is under-specified relative to the plan channel

Three separate proofs, and together they are the strongest process argument in the
record.

1. **R56**: Q78 lost `Filter: (d_year = 1998)` on three `date_dim` scans —
   GroupAgg rows 549 → 269,574 — and **the sweep checksums still PASSED**,
   because the residual outer filters select the same rows. The
   defect cost estimates, not results. Root cause:
   `pushConjunctIntoCTEBody` had no `*GatherMerge` arm. `*Gather` crossing in
   `pushConjunctTraced` is still deliberately excluded.
2. **R63**: the pre-commit `tpch-spotcheck.sh` caught **Q13 returning 33 rows, not
   34** — a pre-existing executor bug where a Memoize above the inner side of a
   RIGHT JOIN drops null-extended rows. Latent since 2026-09-03, when take2 P2-02c
   flipped Q13 onto the Memoize path; **that round's gate should have caught
   34 → 33**. R64 fixed it (the defect was the *direction*: `addNLIPaths` admitted
   `JoinRight` over a parameterised inner, which is always wrong) and Q13's plan
   became PG's own `Hash Right Join`.
3. **R83**: Limit-below-Unique truncates pre-distinct rows — wrong whenever
   duplicates exceed the limit, and **masked on current data by 78 < 100**. Pinned
   by a synthetic FAIL-before/PASS-after test. The `ParamRef` case was
   deliberately not fixed and **the values bug persists there** (02 §C1).

All three were found by parity work, not by the correctness gates.

---

# Part C — Established, then refuted

Recorded because the refutations are knowledge, and because this directory's
convention is that superseded claims stay legible.

| claim | asserted | refuted by | what was actually true |
|---|---|---|---|
| Base rels receive only a seq-scan path (missing candidate set) | root-causes rev 1 | adversarial review | `generateScanPaths` is **test-only**; the production seed is `newPrebuiltPath`. "Read code, assumed it ran, never checked the callers." |
| The planner never sets `AggStrategySorted`; worker count is not computed | K11(a), K11(b) | R3 §0, R4 §0 | a costing inversion (`costAgg` had no spill arm); and a `relpages` **input** divergence (K14/K39) |
| Q12's margin is the legacy-vs-search seq-scan price | K61 | K62 (instrumented) | the search used the correct cost; K61 described the rendering |
| Q12's hash-join margin is hash-build overhead | K64 | K65 | the margin is **width** — no column pruning, rows 20–32× too wide |
| join-order is not estimate-driven | K49 | R35 FINDINGS §1, and K49's own correction | **the evidence is void**, not weak — R35: "the experiments could not have shown movement whatever the estimates did." Do not cite in either direction. |
| `tryFoldBinaryOp` does not fold arithmetic | K84's predecessor | K84 | it does; the gap is a **type-domain** gap in `toLiteralValue` |
| goopg's optimizer-side temporal evaluator does not exist | K87(2) | R44 step B | the pieces were already importable |
| Q14's parallelism gap is a missing field on `optimizer.Join` | K15 | K16 | goopg stamps Gathers; PG builds partial paths |
| The gather-paths flip must precede Q14 | K79/R43 rev 2 | K82 | Q14 is **byte-identical** under the flip; the tracks are independent |
| Q1's gap is a costing divergence, not a disabled capability | K94 | K96 | the shape PG picks is **unreachable by construction** |
| ncols narrowing is the missing half of R120's currency fix | R120 §7 | R121 scope, R124 §7 | unreachable by invariant; and the paired result equals R120 alone exactly |
| The collector arm causes TPC-DS's mixed pairs | R122 §7, R123 rev 1 | R123 | arm (a) contributes **zero** mixed pairs |
| Eliminating the mixed bucket makes TPC-DS readable | R124 rev 1 | R124 §3 | 22 of 32 rels were a census **label**; zero cost movement |
| join-order and parallelism are not width-driven | R122/R124 rev 1 | R124 §4b | `joinpathsparallel.go:210-213` consumes the width triple |
| Q4's election is rows-driven | R71 | R72 P4 | R71's anchors were **unreproducible** ("PG 0.23" was a selectivity misreported as a cost); its conclusion is **unsound** — though R71's *direction* was independently re-confirmed |
| goopg skips the hash bucket walk when `innerBucketSize == 0` | R96 SLICE | R96 STEP1 | bucket sizes are valued; and `innerUnique=true` throughout, so the plain branch was never active |
| FK evidence fixes Q9's join order | R125/R126 premise | step-(d) recon | match unchanged; three categories worse |
| A more accurate Q9 cardinality is less PG-like | R130 rev 3 | R130 ground truth | the FK arm moved the estimate 20× *further* from truth |
| `make plan-gate` diffs goopg against live PG | R126, R128 rev 1 | R128 §5 | it is a **goopg-vs-committed-goopg baseline pin** (`Makefile:431-453`) |
| The narrowing chain was parity-neutral | R124, FRONTIER | R128 | R122's two categories are real; only R124's *increment* was neutral |
| Q96's margin is a join-method / build-side decision | R116 premise | R116 | all four joins are Hash/BuildRight |
| Q96's margin is a selectivity defect | R117/R118 premise | R119 | PG uses the identical formula |
