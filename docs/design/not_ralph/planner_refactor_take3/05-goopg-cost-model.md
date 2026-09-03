# 05 — goopg cost model (current state, HEAD Sep 2026)

*The goopg counterpart of [02 — PostgreSQL cost model](02-pg-cost-model.md).
Top-level §§1, 5, 6, 7, 8 align with doc 02; §§2, 3, 4, 9, 10 do not
(mapping table below — the take2 "one-to-one" header was false for these).
Base:
`docs/design/not_ralph/planner_refactor_take2/05-goopg-cost-model.md`
(2026-09-02). Re-verified at HEAD `d5f8a6ff9` with Serena symbol tools plus
spot-reads; `path:line` citations re-pinned at HEAD `b4e68c574` by file
search + targeted reads; every commit hash below was resolved with `git show`. Claims
carried forward without re-verification are marked **[carried]**; timings and
sweep figures are as measured, not re-verified.*

| doc 02 section | doc 05 section | correspondence |
|---|---|---|
| §2.1–2.5 (clamps, parallel divisor, qual_eval, byte helpers, widths) | §2.1–2.8 | split and partial: 2.1–2.3 align; 05 §2.4 (`get_restriction_qual_cost` ABSENT) and §2.7 (`approx_tuple_count` ABSENT) have no 02 counterpart item; 05 §2.6 (`indexPagesFetched`) and §2.8 (width) are 02 §2.4–2.5 material reorganised |
| §3 (`cost_index` at §3.2) | §3.1–3.13 | reordered: 02 §3.2 `cost_index` is 05 §3.3; 05 §3.2 (`cost_samplescan` ABSENT) has no 02 §3.2 counterpart |
| §4.2–4.4 (sort/material/rescan/memoize/subplan/agg in two subsections) | §4.1–4.13 | split one-to-many: each 02 subsection maps to several 05 subsections |
| §9 (checklist, 53 items) | §9 (EXPLAIN surface) + §10 (fidelity table) | no counterpart: 05 §9 is new EXPLAIN-surface content; the checklist pointer is 05 §10, keyed to 02 §9 |
| (no §10; checklist is §9) | §10 (fidelity table) | 05-only section; "Item" cites take3 02 §9 |

**Scope note (unchanged).** Only the PG-shaped join search has a cost model in
PG's units. Everything above the FROM clause and everything the seam declines
is planned by rewrite rules with no cost, or by the integer "unit-row"
estimator in `joincost.go`. This document covers all three.

**Three facts that colour every section below (updated).**

1. `costParams` is **no longer always `defaultCostParams()`**. Both real cost
   sites read a per-statement `PlannerSettings` (P2-01/P2-02); session
   `seq_page_cost`/`work_mem`/… demonstrably move plans. Only the
   display-only `DeriveLegacyDisplayCost` still calls `defaultCostParams()`.
   The remaining gap is *propagation* (derived tables still plan under
   defaults — 04 §12.3), not plumbing.
2. `Path.DisabledNodes` is **now assigned** for join-method paths (P2-05):
   `enable_hashjoin`/`mergejoin`/`nestloop` produce the path and increment
   the count, as PG 18 does. Scan toggles are still honoured by skipping
   producers.
3. **EXPLAIN prints real costs** (P0-02/P0-03): the chosen path's
   `(startup, total)` stamped on the node at `createPlan` time
   (`stampPlanCost`), legacy nodes via the monotone
   `DeriveLegacyDisplayCost`. `rows=` still comes from the legacy
   `optimizer.EstimateRows`, not from the `Path`.

## Landed take2 work this document absorbs

P0-02/P0-03 `9cbc7661b` (PlanCost carrier + display fallback) · P0-12
`78ef045c8` (bench alignment, +62% at parity) · P1-20 `7ef387324` (constant
propagation reaches costing via the seam) · P2-01/P2-02 (settings carrier +
session GUCs; FROM-clause slice `f93ea20dd`) · six-bridge removal P2-02c
`62a5006c7`/`d69765485`/`294b82ec9` · P2-03 `7c95b2c83` (`hash_mem_multiplier`,
budget doubled) · P2-04 (cost-GUC cache bypass) · P2-05 `656236ab1`
(`DisabledNodes` for join methods) · P2-06 `788eda72b` (NL inner priced as
materialised) · P2-07 `5918fe094` (`cost_rescan`) · P2-09 partial `10ee792b0`
(unique single-tuple clamp) · P2-11 `bb32b976c` (hash bucket walk) · P2-12
`b3a53afe0` (merge END selectivities) · P2-13 verified done in-tree (lossy
pages + no double charge) · `c281b0830` (merge on `mergejointuples`) ·
`13d53603f` (dropped-clause wrong answers fixed) · `dd22e656c` (ndistinct
two-form fix, −8.1% TPC-H) · P1-12/13 `71653da23` (conjunction + RangeQuery
pairing) · P1-14 `13430fc3a` (`nulltestsel`) · P1-15 `b0097a2af` (eqjoin MCV)
· P1-19 `ca9328ed0` (isunique) · P1-25 (DISTINCT sizing). Declined/blocked:
P2-08 (no consumer), P2-10 (no semi/anti paths), P2-09 qual cost (reverted,
+3.3%), P2-02b (open, +23.1% perf-only after `13d53603f`).

---

## 1. Cost currency and GUCs

### 1.1 Cost GUCs — `costParams` / `PlannerSettings`

`internal/optimizer/cost_funcs.go:47` (`costParams`), `:98`
(`defaultCostParams`); `internal/optimizer/plannersettings.go:28`
(`PlannerSettings`), `:118` (`DefaultPlannerSettings`), `:160`
(`costParams()`).

| field | default | PG 18 boot | agrees? |
|---|---|---|---|
| `seqPageCost` | 1.0 | 1.0 | yes |
| `randomPageCost` | 4.0 | 4.0 | yes |
| `cpuTupleCost` | 0.01 | 0.01 | yes |
| `cpuIndexTupleCost` | 0.005 | 0.005 | yes |
| `cpuOperatorCost` | 0.0025 | 0.0025 | yes |
| `parallelSetupCost` | 1000.0 | 1000 | yes |
| `parallelTupleCost` | 0.1 | 0.1 | yes |
| `effectiveCacheSize` | `4 GiB / 8192 = 524288` pages | 524288 blocks | yes |
| `workMem` | `HashMemLimit(512MiB, 2.0)` = **1 GiB** | **4 MB × 2** | **no** |

Two things changed since take2. First, the budget is now
`work_mem × hash_mem_multiplier` (P2-03): `hashsize.HashMemLimit`
(`hashsize.go:150`) is the single shared expression the planner's
`costParams()` and the executor's `buildGeometry` both call — the invariant
`TestCostParamsWorkMemMatchesExecutorFallback` guards it, and
`TestDefaultPlannerSettingsMatchTheHardWiredParams` guards the
double-application trap (`DefaultPlannerSettings` stores the *raw*
`WorkMem`; `costParams()` multiplies once). With PG's default multiplier
2.0, **every hash table in goopg previously had half PostgreSQL's memory**
(Q14 −96.1%, Q9 −85.2% when fixed — the session's largest single win).
Second, `costParams` gained the method toggles and all seven GEQO knobs
(`cost_funcs.go:82-95`).

**Session reachability: yes.** The take2 "proof of no" (two
`defaultCostParams()` callers, no session in scope) is superseded:
`dispatch.go:1936-1951` fills a `PlannerSettings` from the session
(`work_mem` KB→bytes, `effective_cache_size` KB→blocks) and every
`planSelectWithSettings`-family function threads it to the cost sites.
(The field comment at `cost_funcs.go:77-79` still says the per-session value
does not reach the planner — stale since P2-02.)
`SET seq_page_cost = 1000` flips a parallel hash join to a merge join over
index scans live; `SET work_mem = '64kB'` reprices a hash join 14835 →
23478. The known hole is *propagation*: derived-table / set-op /
scalar-subquery sites still plan under defaults (04 §12.3), so P2-02's gates
passed on single-level statements while Q9-shaped queries still plan
subqueries at defaults.

**The `work_mem` divergence is still real.** `defaults.go:785` still
registers `BootVal: "512MB"` (not PG's 4 MB) and
`hashsize.DefaultMemLimitBytes` is still `512 << 20` (`hashsize.go:83`) —
P2-02b is open. After `13d53603f` it is purely performance (+23.1%,
entirely Q9+Q7): at PG's budget the plan flips to a single-key merge join
(6.0M → 24.0M rows, Gather lost) that scores 2.8× *worse* by goopg's own
model — so the next measurement is `addPath` attribution, not costing.

**P0-12 alignment.** Bench confs now carry explicit `work_mem = 64MB` /
`effective_cache_size = 2GB` matching the PG reference (`78ef045c8`).
Result: **goopg 62% slower at parity (248.71s → 403.27s), row counts
identical**. The old headline measured an 8× memory advantage; the honest
ratio is nearer 17.6×. `shared_buffers` stays divergent by design.

#### `hash_mem_multiplier` — consumed (P2-03)

Take2's "declared, never consumed" is refuted: `HashMemMultiplier` rides
`PlannerSettings` (`plannersettings.go:109`), zero means "use the default",
and the P2-04 cache guard covers it like `work_mem`.

#### `enable_*` handling

Join methods (`hashjoin`, `mergejoin`, `nestloop`) are now PG-style
`DisabledNodes` (P2-05); `memoize`, `nestloop_index`, `hashagg`,
`presorted_aggregate`, `geqo*` are per-statement settings (P2-02c) — the
process-global-atomic hazard is gone. Scan toggles remain producer-skips.
Still registration-only: `enable_sort/material/incremental_sort/
gathermerge/partitionwise_*/parallel_hash/tidscan` and friends **[carried]**.

#### Environment calibration

`GOOPG_INDEX_PROBE_MULT` (default 1.0) still multiplies every
`random_page_cost` charge in `costIndexScan` and nothing else; it is now a
stamped provenance row (P0-04c) **[carried otherwise]**.

### 1.2 `Cost` and the comparison

Unchanged **[carried]**: `(Startup, Total)` float64, 1.01 fuzz,
`disabled_nodes`-first ordering (now over live counts for join paths),
`getCheapestFractionalPath` (`tuplefraction.go:267`).

---

## 2. Helpers

### 2.1 Clamps

`clampRowEst` (`relsize.go:494`) still verbatim `clamp_row_est`
**[carried]**; legacy `clampRowEstF`/`saturateRowEst`/`scaleByFloat` and
`clampSelectivity` unchanged **[carried]**.

### 2.2 `getParallelDivisor`

Unchanged and still reachable only through the test-only `generateScanPaths`
**[carried]**.

### 2.3 `qualEvalCost` — goopg's `cost_qual_eval`

Unchanged: conjunct count × 0.0025, no procost table, no SAOP arm, no
SubPlan charge **[carried]**. Tuple-count discipline unchanged (cross
product for NL, output rows for hash/merge — merge now on `mergeTuples`,
§5.4).

### 2.4 `get_restriction_qual_cost` — **ABSENT**

Unchanged **[carried]**: base restriction quals uncharged in the Path model
by mutual agreement of both rivals.

### 2.5 Byte-size helpers

Unchanged: column-count model (`EntryBytes(ncols, avgVarBytes) = ncols*48 +
24 + avgVarBytes`), `spillPages`, `estScanPages`, `baseRelPages` preferring
stored `relpages` **[carried]**. Plus P4-01a: per-path `NCols`/`AvgVarBytes`
(`pathNCols`/`pathAvgVarBytes`) so an index-only path no longer sizes its
hash geometry at the relation's full width.

### 2.6 `indexPagesFetched`

Unchanged, branch-for-branch **[carried]**.

### 2.7 `approx_tuple_count` — **ABSENT**

Unchanged: joinrel rows reused **[carried]**.

### 2.8 Width and target cost

Unchanged **[carried]** (`typeWidth` declared-types only; join width = full
concatenation; no target-list charge). P4-01 (`PathTarget` analogue) still
open — and now measured as the leading width hypothesis behind P2-02b
(1098–3164 B vs PG's 23–81 B at equal cardinality), though the causal chain
is explicitly *hypothesis, not measurement* (FINDING correction).

---

## 3. Scan costs

### 3.1 `cost_seqscan`

Formula identical; inputs as take2 (post-filter rows, `estScanPages`,
`numQualOps = 0`) **[carried]**. Parallel arm still test-only and still
divides the disk term (§8.5) **[carried]**.

### 3.2 `cost_samplescan` — **ABSENT [carried]**

### 3.3 `cost_index`

I/O interpolation near-identical **[carried]**; `heapPagesAfterVM` at all
four sites **[carried]**; partial indexes still declined **[carried]**. Two
P2-09 deltas:

- **Landed:** unique-index single-tuple clamp via `fullyBound`
  (`pathparamindex.go:360-389`) — equality on every key column of a UNIQUE
  index prices one tuple. The selectivity route could not reach 1.0 alone
  (per-column floors multiply), so multi-column unique probes were priced
  as range scans. 99/99 TPC-DS shapes unchanged, one runtime move (faster).
- **Reverted:** per-tuple index-qual cost (`numIndexTuples ×
  cpu_operator_cost × len(indexQuals)`, selfuncs.c:7228-7234). Faithful,
  symmetric (seq scans already pay via `numQualOps`), not double-charged —
  and TPC-DS **+3.3%** with 60 shapes moved, outside the ±0.4% sweep
  variance band while every per-query gate stayed green. Lesson recorded:
  the per-query 2× gate misses broad shallow moves; the sweep TOTAL arm is
  complementary. Lands with the rest of `btcostestimate`, acceptance-tested
  on the aggregate.
- **Blocked:** `num_sa_scans` — no ScalarArrayOp index path exists (`IN`
  lists plan as seq scan + filter), so the term has no consumer. The
  missing *path* is the real gap. Same probe also fixed the ndistinct
  two-form bug (`dd22e656c`): `eqSelectivityForColumn` and
  `resolveBaseColumn` read the absolute `NDistinct` alone, so every key
  column scaling with its relation read as ndistinct zero and fell to
  `DEFAULT_EQ_SEL` (`IN (1..5)` estimated 5000, PG says 5). Both now use
  `ResolvedNDistinct(tuples)` (PG's `-stadistinct × ntuples` convention):
  TPC-H −8.1%, TPC-DS aggregate −3.6%, 79 shapes moved, both runtime moves
  faster, zero regressions.

### 3.4–3.5 `genericcostestimate` / `btcostestimate` → `btreeIndexAMCost`

Table from take2 with status updates:

| PG term | status now |
|---|---|
| `ceil(log2(tuples)) × cpu_operator_cost` descent | **absent** (unchanged) |
| page-descent `(treeHeight+1) × 50 × cpu_operator_cost` | **present** (was already present; P2-09 scoping confirmed — take2's "descent" row conflated the two) |
| `num_sa_scans` + `ceil(pages/3)` clamp | **absent, blocked** (no SAOP path) |
| per-tuple qual op cost | **absent, reverted** (faithful but +3.3%; lands with the batch) |
| `indexStartupCost = qual_arg_cost` | **absent** (startup = descent only) |
| unique `numIndexTuples = 1` clamp | **landed via selectivity** (`fullyBound`, §3.3) |
| Mackert–Lohman over index pages for `num_scans > 1` | **absent** (heap side amortises; index side full cost per iteration — §8.1 recomputed below, unchanged) |

`indexCorrelationFor` still identical in arithmetic **[carried]** — and its
input is now real: the `f07c20b1f` decode fix restores MCV/histogram (and
correlation, stakind3) across restarts (TPC-H −10.5%), and `pg_stats`
renders correlation since P1-28 (`86b3b96a2`).

### 3.5b `estimateIndexGeometry`

Still synthesising, but P1-01 narrowed the gap: real `relpages` via
`IndexRealPages` wins whenever storage answers (verified on the bench
cluster); `indexTuples = relTuples` assumed; tree height from log-fanout;
no bloat model **[carried otherwise]**.

### 3.6 `cost_bitmap_heap_scan` and friends

**P2-13 verified done in-tree (no new commit):** `computeBitmapPages*`
computes lossy pages and *uses* them, and `costBitmapHeapScan` returns
`startup + run` with no second `indexCost.Total`. Census over TPC-H 22:
**goopg 9 bitmap scans vs PG's 6** (was 22–24 vs 6 with the removal alone);
TPC-DS Q72 — the 73s→timeout witness for unpaired landing — runs 105s PASS.
Residual 9-vs-6 is plan-parity work, not cost error. Formula details (both
`loop_count` arms, `csquared` interpolation, 320-byte TBM entry vs PG's
64-byte at 128× the default `work_mem`) unchanged **[carried]**.

### 3.7–3.12 TID / subquery / function / values / CTE / recursive scans — **ABSENT [carried]**

`PathKind` enum unchanged; non-heap FROM items still enter as
`PathPrebuilt` priced as a seq scan over `EstimateRows`.

### 3.13 `cost_gather` / `cost_gather_merge`

Unchanged: `gatherCost` present without callers; parallelism decided by the
`MaybeAddGather` size rule; `parallel_setup_cost`/`parallel_tuple_cost`
dead constants **[carried]**.

---

## 4. Sort and materialization

### 4.1–4.2 `cost_tuplesort` / `cost_sort` → `costSortRun`

Arithmetic identical **[carried]** (column-count bytes with `avgVarBytes =
0`, caller's comparison extra always 0, no bounded top-N arm). Reachability
unchanged: only merge-join input sorts via `sortPathFor`
(`joinpathsmerge.go:423`).

### 4.3–4.5 Incremental sort / append / merge-append — **ABSENT [carried]**

### 4.6 `cost_material` — landed sideways (P2-06, `788eda72b`)

No Material *path* was introduced, and the item records why one would be
wrong: goopg's executors materialise unconditionally on both NL sides and
buffer per key-group on merge — a path-level Material would buffer twice.
What landed is the substance where goopg pays it: the nested loop's inner
is priced as materialised (build once at `2 × cpu_operator_cost × tuples`
+ spill, rescans at `cpu_operator_cost × tuples`); parameterised inners
excluded (genuinely re-executed; PG's `create_material_path` likewise
unreachable). Gates: TPC-H neutral, TPC-DS 95 PASS, 27 shapes changed, Q54
14s→12s, Q47 12s→11s.

### 4.7 `cost_rescan` — landed (P2-07, `5918fe094`)

The defect was the *opposite* of the item's framing: rescans were not free
but charged the inner's **full total incl. startup on every outer row**.
`pathRescanCost` (`joinpathsmemoize.go:392`) is `cost_rescan`
(costsize.c:4638) with the Material/Sort arm and the default re-execute
arm (Memoize arm pre-existed). Gates: TPC-H inside drift, TPC-DS 95 PASS,
Q94 7s→2s. CTE/WorkTable arm still open (no goopg path kind to attach to —
Phase 4 upper-planner work).

### 4.8 `cost_memoize_rescan` → `costMemoizeRescan`

Near-identical transcription **[carried]**; `getMemoizePath` now reads
`EnableMemoize` from `costParams` (P2-02c) instead of the process-global
atomic. SEMI/ANTI and `inner_unique` gates still inexpressible/vacuous
(INNER-only seam) **[carried]**.

### 4.9 `cost_subplan` — declined premature (P2-08)

`estimateSubplanCostPerCall` still a row-count proxy with **zero production
callers** (11 self-recursion sites + one comment reference). Converting it
to `(startup, total)` would write a cost function nothing consults; the
prerequisite is SubPlan costs participating in path comparison at all
(Phase 4). Same shape as P2-10.

### 4.10 `cost_agg` → `aggCost`

Still AGG_HASHED-arm-only with no production caller **[carried]**; the
three grouping *rules* (now settings-threaded) still compare shapes.

### 4.11–4.13 Window / group / set-op costs — **ABSENT [carried]**

---

## 5. Join costs

No two-stage split, still fully-costed arms **[carried]**.

### 5.1 `compute_semi_anti_join_factors` — declined blocked (P2-10)

No semi/anti *paths* exist (`splitOuterSpine` peels them; neither
`hashJoinInputs` nor `nestloopCost` takes a join type), so there is nowhere
to attach the factors. Prerequisite is P3's `SpecialJoinInfo`-in-DP work.

### 5.2–5.3 Nested loop → `nestloopCost`

Formula as take2 plus two landed corrections: rescan via `pathRescanCost`
(§4.7) and materialised-inner pricing (§4.6). Still absent: `(outer_rows−1)`
first-scan split nuance, all SEMI/ANTI early-out logic, `enable_nestloop`
count — the last now **present** via `DisabledNodes` (P2-05).

### 5.4–5.5 Merge join → `mergeJoinCost`

Still "one pass per input + emit per output row", but the two load-bearing
errors are fixed: **costed on `mergejointuples`**
(`joinselectivity.go:433`, `joinpathsmerge.go:379`; `c281b0830` — TPC-H
258.28s→240.73s as measured) and **residual charged on `mergeTuples`** (`:385`), with
**END scan selectivities** scaling both inputs' run costs
(`mergeJoinScanSel`, P2-12; TPC-H neutral, 5 TPC-DS shapes moved, net −1s on
movers). START selectivities reported 0 by design. Still absent:
`rescanratio` duplicate modelling, mark/restore, startup-prefix qual charge
**[carried]**. Plus the wrong-answers fix: `demoteDroppedMergeClauses`
(`13d53603f`) — a trimmed merge-clause list no longer drops the remainder
nowhere-evaluated (Q9 correct at PG's `work_mem`, 24 MATCH).

### 5.6–5.7 Hash join → `hashJoinCost` + `hashJoinInputs`

Present-and-faithful list from take2 holds (startup shape, probe term,
multi-batch I/O conditioned on the shared `hashsize.Choose`) **[carried]**.
New:

- **Bucket walk charged (P2-11, `bb32b976c`):**
  `estimateHashBucketSize` is `estimate_hash_bucket_stats` reduced to the
  ndistinct-derived fraction; `hashJoinCost` charges `cpu_operator_cost ×
  numHashClauses × outerRows × clamp(innerRows × bucketsize) × 0.5`,
  computed per orientation and arriving as a closure (`addHashJoinPath`
  has no `searchCtx`). Zero fraction ("no usable statistic", including
  `isdefault` ndistinct) *suppresses* the term — stats-less plans cost
  exactly as before. MCV-frequency half still open (needs the MCV list at
  the cost site). Gates: TPC-H best-of-bundle, TPC-DS aggregate −1.2% (as measured),
  88 shapes moved. The two apparent per-query regressions (Q76, Q12) had
  **byte-identical plans** — sweep-context variance, not cost regression;
  procedure fixed: a runtime move is attributable only if the plan changed.
- **Budget doubled (P2-03):** §8.2 recomputed below — at the default the
  lineitem⋈orders build no longer batches.
- Still absent: SEMI/ANTI probe variants, `disable_cost` MCV bail-out,
  `hashjointuples = approx_tuple_count`, parallel hash **[carried]**.

### 5.8 NLI costing

Structure as take2 (per-probe `ppi_rows` discipline, `loopCountFor`,
star-schema refusal) **[carried]**, gate now settings-aware (04 §2.15).
P3-11 finding stands: search NLI offered 694× / accepted 23×, margins
0.05%–12% — calibration, not a defect.

### 5.9 The legacy costing paths — still reachable, still separate currencies

`chooseInnerJoinAlgo` (unit-rows) and `nliCostGateAccepts` (row-unit
heuristic) unchanged **[carried]**. Deletion attempts failed by measurement
(P6-03/P6-04: Q20 6.5×, Q4 12.5×) — they compensate for shapes the search
does not win, and retire only when the search selects NLI on its own merits.

---

## 6. Cardinality inside costs

### 6.1 Base relations

`estimateRelSize` transcription unchanged **[carried]**; analyzed relations
still not rescaled by live blocks (deliberate, flag-honesty invariant).
`estimateBaseRelInfo` + `applyLocalFilterSelectivity` + the `reliable` gate
unchanged **[carried]** (doc 06 §6). `initialRelRows` unchanged **[carried]**.
P1-05: `relSizeFallbackRows` now unconditional; staging reader + test hook
remain (04 §12.1 note).

### 6.2 Join relations — `calcJoinrelSize`

Unchanged shape (superkey first, residual loop, `rowsBound`, guess-only
`max(l,r)` cap) **[carried]**; still INNER-only in the Path model (P1-18
blocked on P3-04 — verified: the peeled-outer DPTRACE shows `nrels=1`,
`pairs=0`). Merge arm now consumes `mergeJoinTuples` separately (§5.4).
P1-21 (delete the fallback cap): precondition **not** met — the cap sits in
the unmeasurable fallback P1-15 cannot reach; deleting it now would move
big-input joins from `min(l·r·0.005, max)` to `l·r·0.005`. Open.

### 6.3 The legacy estimator

Unchanged, still the `rows=` source **[carried]**; DISTINCT arms now sized
(§6.5/P1-25).

### 6.4 Rows from `compute_bitmap_pages`

Unchanged **[carried]** (lossy-corrected CPU term charged; path `Rows` from
selectivity, as PG).

---

## 7. Selectivity entry points the cost model calls

| entry point | status now |
|---|---|
| `clauseSelectivity` / `WithSource` | unchanged pair **[carried]** |
| `joinClauseSelectivity(Ext)`, `eqJoinSelectivity(Ext)` | unchanged; inner MCV arm added (doc 06 §7) |
| `getVariableNumDistinct` | unchanged + isunique branch (doc 06 §4) |
| `estimateNumGroups` | unchanged; now also sizes DISTINCT (P1-25) |
| `varEqNonConstSelectivity` | unchanged **[carried]** |
| `superkeyJoinSelectivity` | unchanged **[carried]** |
| `conjunctionSelectivity` (`rangequery.go`) | **new (P1-12): IS `clauselist_selectivity`** — flattened conjuncts, per-variable range pairing, remainder multiplied. `RestrictInfo` caching still absent (planning-speed only → Phase 6) |
| `RangeQueryClause` pairing | **new (P1-13, `71653da23`)**: one-year lineitem window 1855086 → 902018 vs actual 910180 (2.04× over → 0.9% under); timing neutral (+0.45%, noise) — recorded as a negative result |

---

## 8. Costing walkthrough examples

Same five cases as doc 02 §8, goopg defaults (`work_mem` 512MB unless
stated; multiplier now doubles the hash/sort-tbm budgets per P2-03).

### 8.1 Equality index scan, `loop_count = 1` and `1000`

Unchanged from take2 (startup 0.375 / total 8.390 at loop 1 vs PG 0.425 /
8.4425; +7.4%/iteration at loop 1000 from the missing `num_scans` arm)
**[carried — formulas untouched]**.

### 8.2 Hash join `lineitem` (6e6) ⋈ `orders` (1.5e6) — RECOMPUTED (P2-03)

Widths/bytes as take2: `EntryBytes(9,0) = 456`, `EntryBytes(16,0) = 792`,
`innerBytes = 684,000,000` (3.35× PG's 204M — column-count model,
unchanged). CPU terms unchanged: build 18,750; probe 15,000 (skew-blind —
P2-11 now adds the bucket walk on top where stats allow); emit 60,000.

Budget is now `512MiB × 2.0 = 1,073,741,824`:
`bucketBytes = 100,663,296`; `684,000,000 + 100,663,296 = 784,663,296 <
1,073,741,824` → **single batch** (`NBatch = 1`, `NBuckets = 2,097,152`).

> **goopg at default: startup 18,750; run 75,000 (+ residual qual).**
> **PG 512 MB: startup 18,750, run 90,000.**

The "batches at 512MB where PG does not" finding is **gone at the default**
— that was the P2-03 win (Q14 −96.1%). At `work_mem = 4 MB` (budget 8MB):
`NBatch = 128`, spill charge as take2 (startup +83,497; run +1,243,655 —
the step shape is unchanged, only the magnitude's byte model is goopg's).
Take2's (a)/(b)/(c) findings now read: (a) spill charge still ~3.8× PG's at
matched small budgets (byte model); (b) the default-regime batching
divergence fixed by the multiplier; (c) step shape matches PG (both
`NBatch`-independent charges).

### 8.3 Bitmap heap scan at 1% on a 100,000-page table

Unchanged at the default (startup 1605.5 / total 105,973.1 vs PG
1855.56 / 169,397.2) **[carried — formulas untouched]**; at 4 MB the
goopg-320B-entry lossifies harder than PG's 64B one (take2 numbers stand).
Double-charge removal + lossy use verified in-tree (P2-13).

### 8.4 Sort of 1,000,000 rows

Unchanged (in-memory at default: startup 99,657.8 / total 102,157.8; 2.1×
PG at 4 MB from the 408-vs-128 byte model; no bounded top-N arm — P4-04
open) **[carried]**.

### 8.5 Parallel seqscan with 4 workers

Unchanged (partial 50,000 vs PG 125,000 — the disk-term division PG refuses;
no Gather; block-count rule decides) **[carried]**.

---

## 9. EXPLAIN surface

Rewritten (P0-01/02/03/04b/04d). `internal/executor/operators_explain.go`:
`explainCostFields` (`:1896`) returns stamped `PlanCost` or the monotone
legacy derivation — no literal remains on any of the three render sites
(text, ANALYZE, JSON all carry cost keys). `rows=` still from
`EstimateRows` over the collapsed filter-or-node (mode agreement fixed,
P0-04b). `COSTS OFF` handling as take2 **[carried]**. Relation-name dedup
as take2 **[carried]**; suffix-numbering alignment (P0-04) and JSON
Project/Filter collapse (P0-04e) still open. Settings rendering exists
(`EXPLAIN (SETTINGS)`, M0122-0003) — take2's "UNVERIFIED" row is now
verified present. Because costs print, a cost regression is now visible in
EXPLAIN — take2's "only plan-shape diffs" consequence is lifted, with the
caveat that `rows=` and legacy-region costs are still estimates, not path
accounting.

---

## 10. Fidelity table

"Item" cites take3 doc 02 §9 (53 items); `03:NN` cites take3 doc 03 §11;
`—` means the PG checklist has no standalone item (PG symbol only).
Deltas vs take2 marked **(new)**; all else **[carried]**.

| PG function | items | goopg symbol | verdict | differing terms |
|---|---|---|---|---|
| `clamp_row_est` | 4 | `relsize.go:clampRowEst` | identical | — |
| `get_parallel_divisor` | 18 | `cost_funcs.go:getParallelDivisor` | identical (unreachable) | — |
| `cost_qual_eval*` | 10–14 | `cost_funcs.go:qualEvalCost` | simplified | conjunct × 0.0025; no procost/SAOP/SubPlan/caching |
| `get_restriction_qual_cost` | 14–15 (no standalone item) | — | **absent** | whole qpqual term |
| `relation_byte_size` / `page_size` | 5 | `hashsize.EntryBytes` + `spillPages` | simplified | 48 B/Datum model; per-path narrowing **(new)** |
| `index_pages_fetched` | 19 | `costindex.go:indexPagesFetched` | identical | searched-rels-only `total_table_pages` |
| `approx_tuple_count` | 47 (mentioned) | — | **absent** | joinrel rows reused |
| `set_rel_width` | 16 | `relsize.go:typeWidth` | simplified | no `stawidth`; concatenation width (P4-01 open) |
| `cost_seqscan` | 17 | `cost_funcs.go:costSeqscan` | formula identical / inputs simplified | post-filter rows/pages; `numQualOps = 0` |
| `cost_index` | 20 | `costindex.go:costIndexScan` | near-identical | no qpqual; `GOOPG_INDEX_PROBE_MULT`; **unique clamp landed (new)** |
| `genericcostestimate`/`btcostestimate` | 21–22 | `btreeIndexAMCost` | simplified | no `num_scans` ML branch; **qual-op cost reverted (new)**; startup = descent (descent confirmed present) |
| `btcost_correlation` | 22 (part) | `indexCorrelationFor` | identical | input now restored across restarts (`f07c20b1f`) **(new)** |
| index geometry | 8 | `estimateIndexGeometry` | substituted | **real relpages win when storage answers (new)**; height + partial-tuples synthesised |
| `cost_bitmap_*` / `compute_bitmap_pages` | 23–25 | `costbitmap.go` | identical shape | 320 B entries; **lossy use + no double charge verified (new)** |
| `cost_tidscan` etc. | 26 | — | **absent** | — |
| non-heap scan costs | 27 | — | **absent** | Prebuilt-as-seqscan |
| `cost_recursive_union` | 28 | — | **absent** | — |
| `cost_gather` | 29 | `gatherCost` | present, unreachable | post-pass decides |
| `cost_gather_merge` | 29 | — | **absent** | — |
| `cost_tuplesort`/`cost_sort` | 30–31 | `costSortRun` + `sortPathFor` | near-identical | column-count bytes; no bounded arm (P4-04 open) |
| `cost_material` | 35 | (no path; NL-inner-as-materialised) | **landed sideways (new)** | executor already materialises; CTE arm open |
| `cost_rescan` | 35 (rescan table) | `joinpathsmemoize.go:pathRescanCost` | **landed (new)** | Material/Sort + re-execute arms; CTE arm open |
| `cost_memoize_rescan` | 36 | `costMemoizeRescan` | identical | byte model substituted; settings-driven enable **(new)** |
| `cost_subplan` | 37 | `estimateSubplanCostPerCall` | **declined (new)** | row proxy, zero callers, no consumer |
| `cost_agg` | 38 | `aggCost` | present, unreachable | HASHED arm only |
| nestloop | 41 | `nestloopCost` + rescan/materialised | simplified | no SEMI/ANTI; **`DisabledNodes` live (new)** |
| `compute_semi_anti_join_factors` | 42 | — | **blocked (new)** | no semi/anti paths (Phase 3) |
| mergejoin | 43–44 | `mergeJoinCost` | simplified, corrected | **`mergejointuples` (new)**; **END scan-sel (new)**; dropped-clause fix **(new)**; no rescanratio/mark-restore |
| hashjoin | 45, 47 | `hashJoinCost` | partly identical | **bucket walk, ndistinct half (new)**; **×2 budget (new)**; no MCV half / SEMI-ANTI / `disable_cost` |
| `ExecChooseHashTableSize` | 46 | `hashsize.Choose` | identical algorithm | goopg constants; shared with executor |
| `estimate_hash_bucket_stats` | 48 | `estimateHashBucketSize` | **partial (new)** | ndistinct fraction only; MCV open |
| parallel hash | — | — | **absent** | — |
| `set_baserel_size_estimates` | 49 | `estimateBaseRelInfo` | simplified | `reliable` gate |
| `get_parameterized_baserel_size` | 49 (cap) | `parameterizedBaserelRows` | near-identical | **+ `fullyBound` unique clamp (new)** |
| `calc_joinrel_size_estimate` | 50 | `calcJoinrelSize` | INNER only | guess-only cap retained (P1-21 open) |
| (legacy) join sizing | — | `estimateJoin` + floors | separate model | still the `rows=` source |
| `get_parameterized_joinrel_size` | — | — | **absent** | refused, not sized |
| `get_foreign_key_join_selectivity` | 51 | `superkeyJoinSelectivity` | extended | composite-UNIQUE evidence; no `nconst_ec`/nullfrac-derate |
| `estimate_rel_size` | — (see 02 §1.3) | `estimateRelSize` | identical formula | staging retired; no live rescale |
| `clause_selectivity_ext` | 03:35 | `clauseSelectivity*` | simplified | no caching/scoping |
| `clauselist_selectivity` | 03:34 | `conjunctionSelectivity` | **landed (new)** | no `RestrictInfo` caching (Phase 6) |
| restriction/join selectivity dispatch | 03:35 | `switch` on `OpCode` | simplified | no `oprrest`/`oprjoin` |
| `estimate_num_groups` | 03:39 | `estimateNumGroups` | simplified | **also sizes DISTINCT (new)** |
| EXPLAIN cost output | 53 | `explainCostFields` | **landed (new)** | real + monotone-fallback; `rows=` legacy |
| legacy algo choice / NLI gate | — | `joincost.go` / `nliCostGateAccepts` | extra live models | unit-row/row-unit currencies; deletions failed by measurement |

### Summary counts

- **Identical or near-identical:** 14 (prior 12 + `cost_rescan` + `clauselist_selectivity`).
- **Present but simplified:** 10 (qual cost, seqscan, btcostestimate, sort, nestloop, mergejoin, hashjoin, baserel sizing, joinrel sizing, selectivity dispatch).
- **Present but unreachable:** 2 (`cost_gather`, `cost_agg` — down from 3; rescan graduated).
- **Absent:** ~18 (whole non-scan/non-join node family; jointypes other than INNER; parallel hash; SAOP paths).
- **Declined/blocked with reason:** 4 (subplan, semi/anti factors, qual-op cost batching, SAOP pricing).
- **Extra models goopg has that PG does not:** 3 (unchanged) plus the parallel size rule.
