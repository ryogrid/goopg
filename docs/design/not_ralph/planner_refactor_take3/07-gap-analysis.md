# 07 — Gap analysis (take3 synthesis)

What separates goopg's planner from PostgreSQL 18.3's, as of HEAD `d5f8a6ff9`
(Sep 2026), synthesised from the take3 evidence base. Read
[01](01-pg-planning-pipeline.md), [02](02-pg-cost-model.md),
[03](03-pg-statistics-infrastructure.md) for the oracle and
[04](04-goopg-planning-pipeline.md), [05](05-goopg-cost-model.md),
[06](06-goopg-statistics-infrastructure.md) for goopg's current state first:
this document cites them and does not repeat them. Response:
[08-target-design.md](08-target-design.md); measurement contract:
[09-verification-and-acceptance.md](09-verification-and-acceptance.md).

Take2 sources consumed: `take2/07-gap-analysis.md` (structural inventory),
`take2/08-target-design.md` + `take2/TODO.md` (what landed, what was reverted,
what is blocked), `take2/impl/FINDING-*.md` (measurement history). Every
load-bearing claim below cites a take3 01–06 section or a take2 finding/commit;
uncited sentences are framing, not evidence.

---

## 1. How to read this document

### 1.1 Method

Take3 docs 01–03 re-derive the PG 18.3 oracle from the `postgres/` tree with
every `global -x` symbol re-checked on 2026-09-03 (take3 01 §1 preamble, 02
§1 preamble, 03 preamble). Take3 docs 04–06 re-verify goopg at HEAD `d5f8a6ff9`
with Serena symbol tools plus spot-reads; claims carried forward without
re-verification are marked **[carried]** (take3 04 preamble, 05 preamble, 06
preamble). This document therefore trusts 01–06 as the inventory and adds only
the difference, its per-query consequences, and a leverage ranking.

### 1.2 Three warnings, inherited from take2 §1 and still binding

1. **A capability census counts labels, not capabilities.** Take2 §1.A: the
   2026-08 bitmap census read `BitmapHeapScan = 0` while bitmap paths were
   already winning, because the EXPLAIN renderer had no arm. Take3 04 §11
   records the fix (P0-01, `7677faaed`: 18 missing arms + 2 `%T` arms + 4
   unwalked child sets). Any census below is therefore gated on renderer
   coverage, and renderer gaps are Phase 0 work, not planner gaps.
2. **An absent path and an infinitely expensive path are indistinguishable
   downstream.** Take2 §1: five wrong Q8 hypotheses asked "why is the bitmap
   cheaper" when the answer was "the index producer emitted nothing". Where
   this document says a plan is not chosen it distinguishes *not generated*
   from *generated and lost* where take2 measured it (P0-11 `DPPATH`
   provenance, take3 04 §6), and says so where it cannot.
3. **Some of the prior record is stale.** Take3 04 (Appendix) and 06
   (Appendix) each carry an explicit refutation list. Section 5 of this
   document re-states per-query evidence only where the measurement still
   holds; rows superseded by take2 landings are marked **STALE** with the
   refuting commit.

### 1.3 Status of the evidence base

| source | status at take3 HEAD |
|---|---|
| PG pipeline oracle | re-verified 2026-09-03 (take3 01 §1–§12) |
| PG cost oracle | re-verified `costsize.c` symbols (take3 02 §1–§9) |
| PG stats oracle | re-verified (take3 03 §1–§11) |
| goopg pipeline | re-verified at `d5f8a6ff9`, take2 landings absorbed (take3 04 §0–§13) |
| goopg cost model | re-verified, fidelity table updated (take3 05 §1–§10) |
| goopg stats | re-verified, fidelity table updated (take3 06 §1–§13) |
| goopg-vs-PG node-level plan diff | **still absent** (take3 04 §11: P0-05/06/07 open) |
| join-spine parity (`estimate-audit`) | last committed numbers 2026-08-05, re-measurement pending P0 (take2 09 §2) |

---

## 2. The measured baseline: what still holds vs what is stale

### 2.1 Timing baselines and their confounds

Take2 §2.1 (2026-08-31, `6c65ceb20`, S-cold, ±17% noise band) reported TPC-H
21 queries 227.0 s vs PG 22.9 s (**9.9×**) and TPC-DS SF0.5 95 queries 1173 s
vs 536 s (**2.2×**). That headline is **STALE as a comparison**, not as a
measurement, for a recorded reason: take2 `impl/FINDING-workmem-advantage.md`
§1 (landed as P0-12, `78ef045c8`, take3 04 §12.4, take3 05 §1.1) shows the
goopg bench cluster held an **8× `work_mem` advantage** (512 MB boot default
vs the PG reference cluster's explicit 64 MB). Aligned at 64 MB / 2 GB
(`effective_cache_size`), goopg moved **248.71 s → 403.27 s (+62%)** with row
counts identical. The honest ratio against PG's 22.9 s is nearer **17.6×**
(FINDING §3; take3 04 §12.4).

Take2 landings that moved the totals after the alignment (all take2 TODO
progress log; take3 05 §8, take3 06 §0.3–§0.4):

| work | effect | status |
|---|---|---|
| `f07c20b1f` pg_statistic decode fix (P1-11c) | TPC-H 288.10 s → 257.75 s (**−10.5%**), Q5 −32.2%, Q7 −17.2%, counts identical (take3 06 §0.3) | HOLDS — almost certainly every pre-Sep figure was measured blind on restarted servers |
| `dd22e656c` P1-29 ndistinct two-form fix | TPC-H **−8.1%**, TPC-DS aggregate −3.6%, 79 shapes moved, both runtime moves faster (take3 05 §3.3, 06 §0.4) | HOLDS |
| P2-03 `hash_mem_multiplier` (`7c95b2c83`) | session −37.1% block 390.70 s → 245.71 s; Q14 −96.1%, Q9 −85.2% (take2 TODO P2-03) | HOLDS, but re-read per FINDING-planner-settings §Why-invisible: partly a *default* change (budget 64 MB → 128 MB on the path that actually plans), not only a session-GUC change |
| `c281b0830` merge on `mergejointuples` | TPC-H 258.28 s → 240.73 s (take3 04 §7, 05 §5.4) | HOLDS |
| `13d53603f` dropped-clause wrong answers | correct at PG's `work_mem` (24 MATCH on values); perf-neutral at default 239.72 s vs 240.73 s (take2 TODO P2-02b) | HOLDS |
| P2-06 NL-inner-as-materialised (`788eda72b`) | TPC-H neutral, TPC-DS 95 PASS, Q54 14 s → 12 s, Q47 12 s → 11 s (take3 05 §4.6) | HOLDS |
| P2-07 `cost_rescan` (`5918fe094`) | TPC-H inside drift, TPC-DS 95 PASS, Q94 7 s → 2 s (take3 05 §4.7) | HOLDS |
| P2-11 hash bucket walk, ndistinct half (`bb32b976c`) | TPC-H best-of-bundle, TPC-DS −1.2%, 88 shapes moved; Q76/Q12 apparent regressions byte-identical plans = sweep variance (take3 05 §5.7; take2 TODO P2-11) | HOLDS |
| P2-12 merge END selectivities (`b3a53afe0`) | TPC-H neutral, 5 TPC-DS shapes moved, net −1 s on movers; +2.1% aggregate reading attributed to drift (take3 05 §5.4; take2 TODO P2-12) | HOLDS |
| P2-02b `work_mem` BootVal 512 MB → 4 MB (attempted, reverted) | +23.4% then **+23.1%** after the ndistinct fix, entirely Q9 (+47.1 s / 11.61 s → 51.68 s) + Q7 (+9.7 s / 8.68 s → 18.04 s); values correct (24 MATCH) post-`13d53603f` (take3 04 §12.4, 05 §1.1; take2 TODO P2-02b) | OPEN — purely performance, ordered after P0-12 + P4-01 (take2 `impl/MEASUREMENT-p202b-width-vs-gather.md`: width ~87% / lost Gather ~13%, §3.2) |
| P1-13 RangeQuery pairing (`71653da23`) | cardinality 2.04× over → 0.9% under; timing +0.45% neutral (take3 05 §7, 06 §6) | HOLDS as a negative result: estimate fix, no plan win on this corpus |
| P1-14 `nulltestsel` + P1-25 DISTINCT sizing | +0.88% combined, noise (take3 06 preamble) | HOLDS as negative results; judged by the estimate ratchet, not timing |

### 2.2 Plan-shape census: the NL gap is narrower than recorded, and stale

Take2 §2.2's census (Nested Loop 1 vs 25, Merge Join 5 vs 0) predates the
take2 cost landings. Take2 TODO P3-11 re-measured over TPC-H 22 at HEAD:
**goopg emits 18 Nested Loops vs PG's 30** (bundle-start binary already 19;
P2-06/P2-07 moved the census by one). The 1-vs-25 figure is **STALE**. The
remaining gap is real but the premise "NLI is being crushed" was refuted by
P0-11 provenance: search NLI offered 694× / accepted 23× (3.3%), losing to the
accepted path by **0.05%–12%** — narrowly, not systematically (take2 TODO
P3-11; take3 04 §6). No single mispricing to fix; the action is the remaining
`btcostestimate`/hash-bucket terms (take3 05 §3.4, §5.7).

Join-spine parity (`estimate-audit`, 2026-08-05: DP=1 `shape_mismatches` 46,
matched 32) is **STALE as a metric** pending P0: take2 09 §2 records the 46
was attributed to missing EXPLAIN dedup, while dedup *exists*
(`explain_names.go`) with a suffix-numbering divergence from
`select_rtable_names` (take3 04 §11: P0-04 open). Re-measure after P0-04
before treating it as a parity metric.

### 2.3 What "no instrument" still means

Take2 §2.3 holds unchanged: there is **no node-level goopg-vs-PG plan diff**
over either corpus (take3 04 §11: P0-05/06/07 open; `make plan-gate` and the
TPC-DS `plans` channel compare goopg against goopg). `scripts/pg-plan-shape-diff.sh`
was never created. The nightly `plan-diff LABEL=m0077-final` pin is four
months stale (take2 TODO P0-08). The project's stated objective still has no
instrument — 08 Phase 0 builds one before anything else changes.

---

## 3. Structural gaps, ranked by expected plan-parity leverage

Ordered by consequence (leverage = how many plan-shape diffs the gap can
plausibly close once fixed), not by subsystem size. Each item names the PG
reference (take3 01–03), goopg's current state (take3 04–06), and the take2
resume point.

### 3.1 G1 — The search covers part of the query (highest structural leverage)

**PG reference:** `deconstruct_jointree` + `make_outerjoininfo` +
`join_is_legal` (take3 01 §4.1, §6); `SpecialJoinInfo` fields
(`min_lefthand/min_righthand`, `syn_*`, `jointype`, `ojrelid`,
`commute_above/below_l/r`, `lhs_strict`, semi fields; take3 01 §4.1).

**goopg state:** two planners joined at `tryJoinSearch`
(`joinsearchseam.go:168`; take3 04 §0). Two mechanisms shrink the search
(take2 §3.1, unchanged at HEAD per take3 04 §0/§4.1/§4.4):

1. **Collapse default pins explicit JOINs.** `joinPinned` (`collapse.go:435`)
   returns `!collapseJoins` for `JoinInner`/`JoinCross`; `collapseJoins` reads
   `GOOPG_PGSHAPED_COLLAPSE`, default **off** (take3 04 §0). An n-way
   explicit-JOIN chain is n−1 nested two-relation problems; order is written
   order. Pre-registered blast radius of the flip: TPC-H `changed=0`, exactly
   TPC-DS Q72/Q75 moved (take2 TODO P0-13; take3 04 §0). The flip is Phase 0's
   positive control, not the fix.
2. **Outer-join peel + whole-statement decline.** `splitOuterSpine` lifts
   outer joins into a prefix spine; `extractSearchLeaves` walks INNER/CROSS
   only; a pinned outer join the peel cannot lift declines the **whole
   statement** (`relfromjoinlist.go`, comment quoted take2 §3.1; take3 04
   §4.4). Outer/semi/anti joins never reach `joinIsLegal` in production
   (take3 04 §4.4). P1-18's premise was corrected empirically: a LEFT JOIN
   probe reports `nrels=1, pairs=0` — the search never sizes an outer join,
   it never sees one (take2 TODO P1-18; take3 05 §6.2).

**Why this ranks first:** TPC-DS Q72's nine-relation explicit-JOIN chain
produces eight `searchOneProblem` calls, every one `nitems=2`; PG's plan
(inverse scan/probe sides on `inventory`/`catalog_sales`) is **unreachable at
any cost setting** (take2 §3.1 mechanism, carried take3 04 §0). This is also
the most likely major contributor to the residual NL gap (§2.2): a
two-relation problem offers far fewer parameterisation opportunities than an
eight-relation one, and GEQO is unreachable in practice for the same reason
(no problem near `geqo_threshold` 12; take3 04 §6).

**Resume points:** P3-01 scoped 2026-09-03 — the `SpecialJoinInfo` field set
already exists; what is missing is *population*, blocked on name resolution
(`makeSpecialJoinInfo` at `specialjoin.go:54`, called from `collapse.go:416`, runs before Vars carry relation
indexes), and a partial fix is **unsafe** (`min = syn` overestimates safely;
an underestimate permits PG-forbidden reorderings = wrong answers; take2
TODO P3-01; take3 04 §4.4). Then P3-02 (`distribute_qual_to_rels` with
`check_outerjoin_delay`), P3-03 (`join_is_legal` consulting real SJIs), P3-04
(delete `splitOuterSpine` + `pinnedOuter()` decline; unblocks P1-18), P3-05
(retire the collapse flag), P3-06 (`standard_qp_callback` analogue;
`has_useful_pathkeys` is one-arm-short, take3 04 §5/§9), P3-07
(`param_source_rels` hard-coded 0, take3 04 §7), P3-08
(`reduce_unique_semijoins`, blocked on SEMI left-only `Output()`
re-indexing, ledger 794).

### 3.2 G2 — No `PathTarget`: width is the leading cost-side hypothesis (highest cost leverage)

**PG reference:** `PathTarget{exprs, sortgrouprefs, cost, width}`,
`make_*_input_target`, `apply_scanjoin_target_to_paths` (take3 01 §3);
`set_rel_width` from `stawidth`/`get_typavgwidth` (take3 01 §5, 02 §2.5);
`get_rel_data_width` (take3 03 §1.3).

**goopg state:** `RelOptInfo` carries `Width`/`NCols` but no target list; no
range table; `baseLeaf`/`baseOffset` coordinate model + 56 KB
`joinlayout.go` + boundary assertions (take3 04 §8, §13 item 6). P4-01a
landed (per-path `NCols`/`AvgVarBytes` via `pathNCols`/`pathAvgVarBytes`,
`path.go:348`, `:360`; take3 04 §3, §8;
take3 05 §2.5). P4-01b (leaf narrowing) was **attempted, reverted, wrong
answers**: Q2/Q5 0 rows, Q18 wrong tuples, faster-and-wrong (take2 TODO
P4-01b; take3 04 §3). Lesson recorded: projection must be visible to
coordinate computation — real `PathTarget`/`setrefs` work, not a leaf swap.

**Evidence, with the correction preserved:** the "39× width" causal story was
asserted, **withdrawn**, and re-stated on a fair comparison — the full arc is
in take2 `impl/FINDING-planner-settings-not-propagated.md` (ROOT CAUSE then
CORRECTION) and take2 `impl/P4-A-pathtarget.md` rev 4, plus take2
`impl/FINDING-workmem-advantage.md` §2b and take2
`impl/MEASUREMENT-p202b-width-vs-gather.md`:

- Width figures are real: Q9 at equal cardinality (goopg 321,056 rows vs PG
  ~319k) shows **1098–3164 B vs PG's 23–81 B (14–39×)**; 97 MB / 8 batches
  vs 38 MB / 1 (P4-A rev 4; take3 05 §1.1; take3 04 §12.3).
- But batching/width were **identical** in goopg's fast and slow P2-02b arms,
  so width did not explain that flip; the flip was a join-method change
  (two-key hash → single-key merge, 6.0M → 24.0M rows, Gather lost) scoring
  2.8× *worse* by goopg's own model — fixed by `c281b0830` +
  `13d53603f` (FINDING CORRECTION; take2 TODO P2-02b).
- Remaining P2-02b cost (+23.1%, Q9+Q7 only) splits **width ~87% / lost
  Gather ~13%** in isolation (MEASUREMENT-p202b). P4-01 is the
  remaining blocker for P2-02b on evidence that survives the fair
  comparison — hypothesis promoted by measurement, not proven cause.
- Width model correction (P4-A §1): the driver is **48 B per column**
  (`EntryBytes = 48·ncols + 24 + avgVarBytes`), not byte width; the fix is
  "drop seven columns". `avgVarBytes` is second-order.

**Also in G2:** goopg widths are declared-types only, join width is full
concatenation, no target-list charge (take3 05 §2.8); `AvgWidth` is
payload-only for fixed-width types (take3 06 §2.6–2.8).

### 3.3 G3 — Settings stop at derived tables (blocks the BootVal correction)

**PG reference:** `PlannerGlobal`/`PlannerInfo` plumbed through
`subquery_planner` per level (take3 01 §1–§2).

**goopg state:** `PlannerSettings` carrier landed (P2-01) and session cost
GUCs reach the FROM-clause path (P2-02, `f93ea20dd`; take3 04 §12.2, 05
§1.1). But `planSelectWithParent` (`planner.go:13808`) calls the defaulting
`planSelect` (`:13828`), so subquery/set-op/scalar-subquery sites plan under
hard-wired defaults (take3 04 §12.3). Q9's entire join tree sits inside a
subquery — that is why P2-02b stays blocked. A mechanical threading attempt
was **reverted** (non-monotonic ⇒ threaded-from-wrong-scope bug); next
attempt threads by hand, one caller at a time (take2
`impl/FINDING-planner-settings-not-propagated.md` Progress; take3 04 §12.3).
Full evidence: `impl/FINDING-planner-settings-not-propagated.md`.

**Sequencing:** settings propagation before BootVal change (take2 TODO P2-02b
note; 08 §10.1 + §5.1). P2-02b additionally ordered after P4-01 per §3.2.

### 3.4 G4 — Cost-model remainder (faithful core, missing arms)

Core status per take3 05 §10: 14 identical/near-identical, 10 simplified, 2
unreachable-but-present, ~18 absent, 4 declined/blocked with reason.
Remaining gaps with plan-parity consequences:

| gap | state | resume |
|---|---|---|
| SAOP index paths (`num_sa_scans`) | **blocked, no consumer**: `x IN (…)` plans as seq scan + filter; no ScalarArrayOp index path exists (take3 04 §5, 05 §3.3; take2 TODO P2-09) | the missing *path* is the gap, not the term |
| per-tuple index qual cost | implemented, **+3.3% TPC-DS, reverted**; lands with the rest of `btcostestimate`, acceptance on the aggregate (take3 05 §3.3; take2 TODO P2-09) | batch with descent/`num_sa_scans` |
| hash MCV-frequency half of `estimate_hash_bucket_stats` | ndistinct half landed (P2-11); MCV half needs the MCV list at the cost site (take3 05 §5.7; take3 02 §5.4) | second plumbing step |
| `disabled_nodes` remainder | join methods live via `DisabledNodes` (P2-05, `656236ab1`; take3 04 §5, 05 §1.1); scans still producer-skip; `enable_sort/material/incremental_sort/gathermerge/parallel_hash/tidscan` registration-only (take3 05 §1.1) | port remaining setters |
| merge `rescanratio`/mark-restore, startup-prefix qual charge | absent (take3 05 §5.4) | after P3 (needs `inner_unique`/mark-restore support facts) |
| hash `disable_cost` MCV bail-out, `hashjointuples = approx_tuple_count`, SEMI/ANTI probe variants, parallel hash | absent (take3 05 §5.7) | SEMI/ANTI blocked on Phase 3 (P2-10); parallel hash Phase 5 |
| `cost_subplan` | **declined premature**: row-count proxy, zero production callers; prerequisite is SubPlan costs participating in path comparison (take3 05 §4.9; take2 TODO P2-08) | Phase 4 |
| `compute_semi_anti_join_factors` | **blocked**: no semi/anti paths (take3 05 §5.1; take2 TODO P2-10) | Phase 3 |
| `get_restriction_qual_cost` / `approx_tuple_count` | absent by mutual agreement of both rivals / joinrel-rows reuse (take3 05 §2.4, §2.7) | revisit only with evidence |
| `qualEvalCost` | conjunct × 0.0025; no procost/SAOP/SubPlan/caching (take3 05 §2.3) | per-clause costs with P1-14b |
| `cost_gather`/`cost_gather_merge` | present-but-unreachable / absent; post-pass decides (take3 05 §3.13) | Phase 5 |
| tree height + partial-index `indexTuples` synthesis | `relpages` superseded by storage (P1-01); height fanout-derived, `indexTuples = relTuples` (take3 05 §3.5b, 06 §1.4) | P1-02 rescoped (listed once in §3.5) |

### 3.5 G5 — Statistics remainder (absent moved time; inaccurate did not)

Phase-1 guidance from three A/Bs (take3 06 preamble; take2 TODO P1-14
guidance): restoring *absent* statistics moved TPC-H −10.5%; refining
*inaccurate* ones did not move it (P1-13 +0.45%, P1-14/P1-25 +0.88%).
Remaining items are judged by the estimate ratchet, not per-item timing.
Status per take3 06 §13 fidelity table:

| item | status |
|---|---|
| P1-11 TOAST (wide-text histograms dropped; `orders`/`customer`/`partsupp` lost rows + size row) | OPEN; must be **re-measured post-`f07c20b1f`** — wide-text case "may now behave differently" (take3 06 §2.11) |
| P1-14b remaining estimators: general `scalararraysel`, `patternsel`, `rowcomparesel`, `booltestsel`, `var_eq_non_const`; `DEFAULT_*` alignment | OPEN (take3 06 §5.4–§5.7) |
| P1-16 Q9 re-diagnosis with `estimate-audit` | OPEN; recorded single-`nd` explanation **retired** (both arms fold every pair since M0127-P5.6-f; take3 06 §7.7, take2 §3.11 item 3) |
| P1-22/23/24 extended statistics (build / `statext_clauselist_selectivity` / multivariate ndistinct) | OPEN; catalog rows real, `_data` permanently empty, planner never consumes (take3 06 §3.3, §9) |
| P1-27 CTE-agg statistics | RESCOPED: plain-CTE columns already resolve (`*CTEScan` arm); gap is **aggregated** CTE outputs (`year_total` shape → 0.005/conjunct, same as PG) + goopg's `rows ≤ 1` guard that helps (take3 06 §8.5) |
| P1-10 expression-index stats (`compute_index_stats`) | OPEN (take3 06 Appendix) |
| P1-02 tree height + partial-index tuples | RESCOPED (§3.4 table) |
| `get_variable_range` index endpoint probe + MCV widening | absent (take3 06 §4.3); histogram-endpoint drift for monotonic keys (TODO P1-30) |
| text `bucketFraction` 0.5, no clamp, linear scan | unchanged (take3 06 §5.3, §12.3); date half landed (P1-11b, worst case ~80×; text/network remainder TODO P1-31) |
| `nconst_ec` / nullfrac derate in FK formula | absent; reachable-as-future-concern after P1-20, not needed yet (take3 06 §10) |
| P1-21 `max(outer,inner)` fallback cap | OPEN with precondition **not met** (cap sits in the unmeasurable fallback P1-15 cannot reach; take3 05 §6.2; take2 TODO P1-21) |
| P1-17 semi arm | verified already-satisfied (take3 06 §7.1) |
| P1-06 two-stage sampling | **declined**: exact `reltuples` kept; sampling buys ~5.7× ANALYZE speed for planner-input accuracy, ANALYZE not in query path (take3 06 §2.1) |

### 3.6 G6 — Parallelism outside the cost model

Unchanged architecture (take3 04 §10): `MaybeAddGather` post-pass,
`ParallelSettings`, block-count size rule, `gatherCost` without callers, no
partial paths; `generateScanPaths` test-only (re-verified). Phase 5
(P5-01…P5-08) not started. New measured constraint: P2-02b's slow arm loses
its `Gather` because the plan moves onto index-scan-driven joins the
post-pass cannot drive (`drivingScan`, `parallel.go:441`, recognises
SeqScan/BitmapHeapScan/IndexOnlyScan plus `*Filter`/`*Project`/join-probe
wrappers — everything except plain IndexScan/NLI-probe shapes)
— P5-03's problem statement, measured early (take3 04 §10). `drivingScan`
still lacks plain `*IndexScan`, so PG's Parallel Index Scan has no counterpart
(take2 §3.6, carried). `sortPartialRootPays` decline of
`Gather Merge → Sort → Parallel scan` is a *permitted-divergence candidate*
with committed measurement (q16 0.9 s vs 1.6 s; take2 §8) — becomes a cost
comparison once both paths generate.

### 3.7 G7 — Upper planner is rules, not paths

Unchanged (take3 04 §3): no upper-rel paths, one node per stage plus rewrite
rules; `PathAgg`/`PathGather`/`PathGatherMerge` no `createPlanNode` arm;
`PathSort` merge-join-child only. `rows=` from legacy `EstimateRows`, LIMIT
through `tupleFraction` only at the search (take3 04 §3). Consequences: no
hashed-vs-sorted cost comparison, no bounded top-N sort (take2 §3.7: largest
single `ORDER BY … LIMIT` win), no Incremental Sort node (`MISSING-NODE`
bar), DISTINCT now sized (P1-25) but set-ops not (take3 06 §8.2).

### 3.8 G8 — GEQO, SAOP/index paths, memoize/NLI asymmetries

- **GEQO:** all seven knobs per-statement (P3-10, `ab6fef649`); still
  effectively unreachable under default collapse (take3 04 §6, §12.2);
  keyed on `len(items)` of one `searchOneProblem`. Reachability follows G1,
  not more wiring.
- **SAOP/index paths:** §3.4 row; additionally `join_collapse_limit = 1`
  still a no-op, collapse limits hard-coded 8 (take3 04 §4.1, §12.5).
- **Memoize/NLI:** `costMemoizeRescan` near-identical transcription, now
  settings-driven (take3 05 §4.8); SEMI/ANTI + `inner_unique` gates
  inexpressible/vacuous (INNER-only seam). NLI: `addNLIPaths` discharge
  rules unchanged, gate settings-aware (take3 04 §7); post-search
  `rewriteJoinsToNLI` + `rewriteScanInputsWithSingleTablePredicates`
  **attempted deletions failed** (Q20 6.5×, Q4 semi-join 12.5×; take2 TODO
  P6-03/P6-04; take3 04 §2 step 14/15, §13 item 24) — they compensate for
  shapes the search does not win. `reconcileNLILayout` is a **live tripwire
  oracle, must-not-delete** (P6-05; take3 04 §2.2).

---

## 4. Per-query evidence: holds vs stale

### 4.1 TPC-H (ratios from take2 §5.1, 2026-08-31 S-cold; causes re-verified)

| query | ratio | root cause | verdict |
|---|---|---|---|
| Q5 | 92.5× (37.0 s) | supplier probe flipped to bitmap with timing unchanged → away from scan choice; history of build-side memory blowup (M0077 600 s → 26 s) | **HOLDS as unattributed**; needs P0 instruments. Decode fix moved it −32.2% (take3 06 §0.3) without changing the attribution |
| Q9 | 81.7× (49.0 s) | recorded two-column-`nd` chain **RETIRED** (§3.5 P1-16 row); second half stands (consecutive 6M-row builds unpriced) + merge-cost bug fixed (`c281b0830`) + dropped-clause bug fixed (`13d53603f`) + residual P2-02b +23.1% (Q9+Q7) split width/Gather (§3.2) | **RE-DIAGNOSED, still open**; next measurement is `addPath` attribution at PG's budget (FINDING CORRECTION) |
| Q7 | 45.4× (22.7 s) | probe-seam re-materialisation class (executor) | HOLDS; executor-side (§6) + P2-02b co-victim with Q9 |
| Q12 | 29.4× (14.7 s) | no plan change; ANALYZE moved Hash → Merge; goopg 5 merges where PG 0 | HOLDS as join-method/cost |
| Q19 | 26.0× (2.6 s) | `{lineitem,part}` 131× under where PG 1.0×; missing preprocessing pass since fixed; plan still Hash+Seq vs PG Index+NL | HOLDS as stats-then-shape |
| Q18 | 9.9× (60.6 s) | estimate 42,837× over but PG 5,387× over — **not** estimate-parity defect | HOLDS as executor-side |
| Q21 | 3.5× (11.9 s, was timeout 612 s) | estimate-independent; plan-shape + hash spill | **RESOLVED** (take2 §5.1) |
| Q16 | 3.0× | plan + parallelism match PG; residual sort operator, halved by precomputed keys | HOLDS as executor residual |
| Q6 | identical plan | **23.40 s serial vs PG 0.99 s** (take2 §6) | HOLDS — decisive executor-residual datum (§6) |

Take2 §5.3 summary holds: q18+q09+q05 = 64% of TPC-H total sits largely
executor-side; plan parity is necessary and will not by itself close the time
gap.

### 4.2 TPC-DS SF0.5 (take2 §5.2; re-verify flags preserved)

| query | standing | verdict |
|---|---|---|
| Q72 82× | pairwise collapse (§3.1); bitmap-unpairing witness (73 s → timeout, take2 §4.2) now 105 s PASS with P2-13 verified in-tree (take3 05 §3.6) | HOLDS as search-coverage; cost half closed |
| Q23 78× / Q88 52× / Q28 31× | never targeted | HOLDS as unattributed |
| Q14 111 s abs | runtime-fusion incident query; P2-03 moved it −96.1% (now faster than PG at aligned settings per TODO) | **RE-MEASURE** — P2-03 changed the picture |
| Q47 537 s vs 3.4 s at SF=1 | single-key degeneracy (`i_category`, 10 distinct / 63,745 rows); single-key limitation **removed by M0127-P2.2** | **RE-VERIFY** (take2 §5.2) |
| Q78 8.5× | same degeneracy class, motivated multi-column keys | **RE-VERIFY** (take2 §5.2) |
| Q75 13× | qual placement, fixed 19 s → 11 s | RESOLVED (take2 §5.2) |
| Q99 7× | regressed 1 s → 6 s in parity window, never investigated | OPEN |

Isolation-wedge artifact (Q31/Q64/Q71 ERROR in sweep, PASS standalone), Q4
PG-timeout skip, Q36/Q70/Q86 `SKIP_QUERYGEN` — all carried (take2 §5.2).

---

## 5. What previous rounds established (take2 P0–P2 + reversions)

Landed (take3 04 §0 table, 05 §1, 06 §0): P0-01/02/03/04b/04d/04c/11/12,
P1-01/03/03b/05/07/08/11b/11c/12/13/14/15/19/20/25/26/28, P1-29 ndistinct
two-form fix, P2-01/02/03/04/05/06/07/09-partial/11-half/12-END/13-verified,
`c281b0830`, `13d53603f`, P3-09/10/11/12. Census rule: verified-satisfied
with no code change (P0-09, P1-04/09/17) are recorded in TODO + 06, not
repeated here. Declined/blocked with reason
(take3 04 §0, 05 §10, 06 §13): P2-08 (no consumer), P2-10 (no semi/anti
paths), P2-09 qual cost (reverted, batch later), P2-02b (open, perf-only),
P1-06 (exact reltuples kept), P1-18 (blocked on P3-04), P1-21 (precondition
unmet), P6-03/P6-04 (load-bearing, 6.5×/12.5×), P6-05 (must-not-delete
tripwire), `GOOPG_HASH_OUTER_JOIN` flip (safe now CKMISMATCH=0, wash +1 s net,
not flipped; take2 TODO P6-06).

Negative results that must be preserved (take2 §4.2 + take3 citations):

| attempt | result | preservation |
|---|---|---|
| M0126 penalty multipliers (`GOOPG_COST_DRIVEN_JOINORDER`, >2M-row build penalty + `inner_pages`) | Q9 118 s → 804 s; remediation took Q5 8.15 s → 600 s+ hang | **no penalty multipliers, no shape preferences** (08 §1 P3) |
| MHJ integrated into DP (`cost-model/15`) | correct, parity-green, **never wins** (DP prefers filtered-`part`-early, fracturing the MHJ subset) | same prohibitions |
| NDistinct-alone | 16/22 plans changed, slow queries unfixed; **correct form regressed Q5 +42%** (integer-DP weights calibrated to saturated regime) | structure-before-calibration; one variable per commit |
| bitmap double-charge removal alone (`ab8fbc334`) | oracle-correct in isolation; Q72 73 s → timeout + Q47/Q69 | **cancelling pairs move in one commit** (08 risk R2); now verified paired in-tree (take3 05 §3.6) |
| work_mem alignment (P0-12) | **+62%** at parity, counts identical | honest control kept; `shared_buffers` divergent by design (take3 04 §12.4) |
| histogram decode (`f07c20b1f`) | **−10.5%** TPC-H | every pre-Sep figure measured blind (take3 06 §0.3) |
| P2 block (`hash_mem_multiplier` etc.) | **−37.1%** session block | largest structural-budget win; re-read as partly default change |
| P2-02b | **+23.1%** (Q9+Q7), width ~87% / Gather ~13% | ordered after P4-01 + propagation |
| Q6 identical plan | 23.4 s vs 0.99 s | executor residual, out of scope (§6) |

Five lessons from take2 §4.3 carry forward unchanged: cost-term tuning never
fixed a query; missing statistics masquerade as bad calibration; verify both
candidates generated; row-count gates cannot see shape regressions;
one-variable-per-commit.

---

## 6. Out of scope: the executor residual (with pointers)

Recorded so this bundle is not read as promising time parity on plan work
alone (take2 §6; take3 04 §13 defers to it):

| item | evidence | pointer |
|---|---|---|
| 48-byte `Datum` vs PG's 8 | per-row tax under every identical-plan finding (Q6 23.40 s vs 0.99 s) | runtime representation; take2 TODO `take2-executor-residual` ledger row |
| probe-seam re-materialisation (~18M pool round-trips, ~2×2.3 GB `Datum` on Q9-class) | hash cascade re-materialises probe input per level, twice, on both paths | `analysis/cost-driven-second-try-200731` Stage 0 |
| sort operator (`sortOp.lessRows`/`sortTailWithCTIDs`, q16 34%) | planner side (bounded/incremental sort) in scope as P4-04/05 | executor sort speed out of scope |
| hash skew buckets (flat table vs PG MCV partitioning) | needs Phase 1 MCV input | executor; take2 TODO P7-03 ledger |
| build-side 2× memory (two passes + two maps) | peak build ~2× | executor |
| uncancellable probe loops (no cancel point; SIGKILL required) | measurement hazard: measure such arms per query, never one stream | executor + methodology (09 §6) |
| spill path (Q14 24×, Q3 11×, Q10 4× at parity; PG ~1 s each at same setting) | dominant remaining cost per FINDING-workmem §2 | executor spill, not planner (take3 04 §12.4) |

Each needs/keeps its ledger row (take2 TODO P7-03, 11 rows incl.
`take2-executor-residual`).

---

## 7. Measurement-reliability notes (instruments are part of the gap)

From take2 §7 + take3 04 §12 + `CLAUDE.md` hygiene (all bitten in practice):

1. **8× `work_mem` confound** — fixed by P0-12 alignment; any cross-engine
   cost comparison must state both `work_mem` and `effective_cache_size`
   (take3 04 §12.4; 09 §6.4 rule).
2. **S-cold vs WARM** — TPC-H gate runs S-cold vs ANALYZEd PG (bias vs
   goopg); Warm −15% at default; every artifact states
   `stats=S-cold|WARM`, A/B never mixes regimes (take2 09 §6.5).
3. **Sweep-tail collapse** — post-timeout server sits at GOMEMLIMIT with
   GOGC=off and thrashes GC (Q6 423.94 s post-Q5 vs 5.82 s clean, 73×);
   hold server age constant; fresh server per arm; reap orphans
   (`timeout N psql` kills client only; MATERIALIZED victim set before
   `pg_terminate_backend`); never `pkill -f goopg` (take2 09 §6.1).
4. **Row-count vs plan-shape gates** — `cost_index` loop_count arm: 21/21
   byte-identical, Q2 2.0 s → 87.3 s (take2 09 §1 #1); merge wrong-answers:
   175 rows correct count, wrong tuples, 4.02× sum (take2
   `impl/FINDING-CRITICAL-mergejoin-wrong-answers.md`); P4-01b faster-and-wrong
   (take2 TODO P4-01b). Value-level diff (`tpch-runner -diff`), never counts
   alone.
5. **`shape_mismatches` renderer confound** — 46 attributed to missing dedup;
   dedup exists, numbering diverges (P0-04 open; §2.2).
6. **Per-query 2× gate misses broad shallow moves** — P2-09 qual cost: every
   per-query gate green, sweep TOTAL +3.3% outside ±0.4% variance band
   (take2 TODO P2-09; take3 05 §3.3). TOTAL arm + plan capture are
   complementary, neither sufficient (take2 TODO P2-12).
7. **Attribution rule** — a runtime move is attributable only if the plan
   changed (P2-11 Q76/Q12 byte-identical; P2-12 Q61/Q45/Q12 unchanged; take2
   TODO P2-11/P2-12). Check the plan before believing a per-query move.
8. **Baseline hygiene** — sweep diffs against the most recent report, which
   may be a reverted run (P2-11 −4.3% flattered vs honest −1.2%; take2 TODO
   P2-11). Always state which report the delta used.
9. **TPC-H Q12/Q13 anchors** (`spotcheck_expected.env`) and
   `ci/batch/tpch-row-anchors.csv` are load-pinned; re-pin after any reload
   (`CLAUDE.md`); TPC-DS SF0.5 oracle git-tracked, no PG instance needed.

---

## 8. Ranked gap table (expected plan-parity leverage)

| rank | gap | § | why this rank |
|---|---|---|---|
| 1 | Search coverage: outer-join SJI/`join_is_legal`/P3-04 (unblocks P1-18) | §3.1 | only gap that makes PG plans *unreachable at any cost*; owns Q72 class + much of NL gap + GEQO reachability |
| 2 | `PathTarget`/projection (P4-01) | §3.2 | leading cost-side hypothesis at equal cardinality; blocks P2-02b; ~87% of its residual |
| 3 | Settings propagation to subqueries | §3.3 | Q9-shaped queries plan under defaults; must precede BootVal correction |
| 4 | `work_mem` BootVal (P2-02b) after P4-01 + propagation | §2.1 | +23.1% concentrated in two queries; fidelity fix, lands alone |
| 5 | Extended statistics (P1-22/23/24) | §3.5 | correlated predicates = where independence fails = TPC-DS |
| 6 | Upper-planner paths (grouping/sort/distinct/limit/window) | §3.7 | aggregation/sort strategy diffs; bounded sort win |
| 7 | Parallel paths/Gather/parallel hash (P5) | §3.6 | ~13% of P2-02b residual; PG Parallel Index Scan unreachable |
| 8 | SAOP/index paths + btcostestimate batch | §3.4 | `IN`-list shapes; qual-cost batch acceptance on aggregate |
| 9 | Hash MCV-frequency half (P2-11 remainder) | §3.4 | skewed-key pricing; needs MCV plumbing |
| 10 | Per-clause estimators (P1-14b) + Q9 re-diagnosis (P1-16) | §3.5 | ratchet-driven; timing-neutral expected |
| 11 | TOAST (P1-11, re-measured) + expr-index (P1-10) + P1-02 remainder + endpoint probe (P1-30) + text scalar (P1-31) + CTE-agg (P1-27) | §3.5 | correctness/completeness tail |
| 12 | `disabled_nodes` remainder (P2-16a…e) + GEQO observability + `RestrictInfo` caching (P6-08) | §3.4/§3.8 | A/B-ability + planning speed (Phase 6) |

---

## 9. Summary

goopg's planner is not missing PostgreSQL's machinery. It has `RelOptInfo`,
`Path`, `addPath` with upstream's dominance ordering, three-phase
`join_search_one_level`, GEQO, parameterized paths, pathkeys, and
`costsize.c`-derived cost functions — plus, since take2, real EXPLAIN costs,
a per-statement `PlannerSettings`, `DisabledNodes` for join methods,
`cost_rescan`, materialised-inner NL pricing, merge-on-`mergejointuples`,
END selectivities, the ndistinct half of hash bucket stats, MCV join pairing,
range pairing, `nulltestsel`, DISTINCT sizing, one resolver arm list, and
durable VACUUM/TRUNCATE stats with cache invalidation (take3 04 §0, 05 §1,
06 §0–§1).

What it is missing is **reach and projection**: the search sees an
inner-join prefix (§3.1); it carries whole tuples where PG projects (§3.2);
subqueries plan under defaults (§3.3); everything above FROM and all of
parallelism is decided outside the cost model (§3.6–§3.7); and none of it is
yet visible in a goopg-vs-PG plan diff (§2.3). The response is
[08-target-design.md](08-target-design.md): instrument, fix inputs, widen
reach, then delete everything that plans around it.

(End of file)
