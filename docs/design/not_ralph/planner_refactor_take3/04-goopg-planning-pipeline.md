# 04 — goopg planning pipeline (current state, HEAD Sep 2026)

Scope: what goopg's planner actually does today, section-by-section against
doc `01-pg-planning-pipeline.md`. Section numbers mirror doc 01 so a later
gap-analysis document can diff them one to one. Every claim carries an
`internal/optimizer/<file>.go:<Symbol>` citation (or another package's path
where the code lives outside the optimizer).

Base: `docs/design/not_ralph/planner_refactor_take2/04-goopg-planning-pipeline.md`
(HEAD 2026-09-02, branch `review-bug-fix`). Re-verified at HEAD `d5f8a6ff9`
(branch `planner-refac-bigbang`, Sep 2026) with Serena symbol tools plus
spot-reads; `path:line` citations re-pinned at HEAD `b4e68c574` by file
search + targeted reads. Items carried forward without re-verification are marked
**[carried]**; everything else was re-read at this HEAD. Timings and sweep
figures are as measured, not re-verified; stale code comments that contradict
the text are noted inline rather than fixed.

Notation: "the search" = the PG-shaped Path/RelOptInfo join search; "the legacy
tree" = the `optimizer.Node` executor-operator tree that `planSelect` builds and
rewrites.

## Landed take2 work this document absorbs

| Item | Commit | Effect on this document |
|---|---|---|
| P0-01 EXPLAIN node coverage | `7677faaed` | §1, §11: every node type has a render arm |
| P0-02/P0-03 real costs on every node | `9cbc7661b` | §1, §8, §11: `PlanCost` on the node, `DeriveLegacyDisplayCost` fallback |
| P0-04b EXPLAIN `rows=` mode asymmetry | (gate `TestWalkersAgreeOnRowEstimate`) | §11 |
| P0-04d schema qualification follows verbosity | `2a63fbe21` | §11 |
| P0-04c flag-provenance detector | `f2ac4fdfc` | §12 |
| P0-11 path provenance (`DPPATH`) | (gates `TestPathTraceRecordsProducerAndVerdict`, …) | §6, §8 |
| P0-12 bench memory alignment (+62% finding) | `78ef045c8` | §12 |
| P1-01 real index relpages in `pg_class` | `287232e17` | §5 |
| P1-03 VACUUM persists relsize | `3bcac056c` | §1 (cache), doc 06 |
| P1-03b TRUNCATE reset + ANALYZE/VACUUM cache invalidation | `d3e12b3b4`, `ada899c38` | §1, §11 |
| P1-05 relsize flag retirement | `85bdad317` | §12, doc 06 |
| P1-20 equiv-class constant propagation | `7ef387324` | §0, §2, §4.3 |
| P1-26 resolver collapse | `4c8ea479f` | doc 06 (search clauses unaffected) |
| P1-28 `pg_stats.correlation` | `86b3b96a2` | §5 (correlation is consumed) |
| P2-01 planner-settings carrier | (gates `TestPlannerSettingsReachTheJoinSearch`, …) | §1, §2, §12 |
| P2-02 session cost GUCs reach the planner | `f93ea20dd` (FROM-clause slice) | §12 + known gap below |
| P2-02c six `registry.OnChange` bridges removed | `62a5006c7`, `d69765485`, `294b82ec9` | §12 |
| P2-03 `hash_mem_multiplier` | `7c95b2c83` | doc 05 (budget doubled) |
| P2-04 plan-cache guard for cost GUCs | (gates `TestPlannerCostGUCsOverriddenDetect…`, …) | §1 |
| P2-05 `enable_*` via `DisabledNodes` (join methods) | `656236ab1` | §5, §7, §8, §12 |
| P2-06 NL inner priced as materialised | `788eda72b` | §7, doc 05 |
| P2-07 `cost_rescan` | `5918fe094` | §7, doc 05 |
| P2-09 unique-index single-tuple clamp (partial) | `10ee792b0` | §5 |
| P2-11 hash bucket-size skew term (ndistinct half) | `bb32b976c` | §7, doc 05 |
| P2-12 merge END selectivities | `b3a53afe0` | §7, doc 05 |
| P2-13 bitmap lossy handling (verified done in-tree) | (no new commit) | doc 05 |
| Merge costed on `mergejointuples`, not `joinrel.Rows` | `c281b0830` | §7, doc 05 |
| Merge dropped-clause wrong answers | `13d53603f` | §7 |
| ndistinct two-form fix (`ResolvedNDistinct`) | `dd22e656c` | doc 06 |
| P3-09 `RelSet` → `uint32`, `maxSearchRels` 32 | `cb6a725d0` | §0, §6 |
| P3-10 all seven GEQO knobs on the settings | `ab6fef649` | §6, §12 |
| P3-11 NLI census answered (loses narrowly) | (docs only) | §7 |
| P3-12 `reorderCommaFromByCardinality` deleted | `6f5eefa85`, `362cfa91c` | §2 |
| P4-01a per-path width (`pathNCols`) | (gate `TestPathCarriesItsOwnWidth`) | §8, doc 05 |

Declined / blocked (recorded, not absorbed): P2-08 subplan costing
(premature, no consumer), P2-10 semi/anti factors (blocked on Phase 3),
P2-09 per-tuple index qual cost (implemented, measured +3.3%, reverted),
P2-02b `work_mem` BootVal correction (open — produces wrong answers at PG's
`work_mem` before `13d53603f`, now purely a +23% performance question),
P6-03/P6-04 deletions (attempted, load-bearing: 6.5× on Q20, 12.5× on Q4),
P6-05 (must NOT be done — the "dead" pass is a live tripwire oracle).

---

## 0. Executive summary — two planners, one seam

goopg still has **two planners stacked on one another**, joined at exactly one
function.

1. **The legacy single-shot rewriter.** `optimizer.Plan` (`planner.go:91`) →
   `planStmtWithSettings` (`planner.go:159`) → `planSelectWithSettings`
   (`planner.go:797`) builds exactly one `optimizer.Node` tree through a fixed
   sequence of greedy rewrite passes. New since take2: every one of these
   functions has a `WithSettings` twin taking an explicit `PlannerSettings`;
   the unsuffixed names are thin defaulting wrappers. The spine still owns
   everything above the FROM clause.
2. **A PG-faithful Path/RelOptInfo join search**, spliced into `planSelect` at
   one seam, with real `Path` / `RelOptInfo` (`path.go`), `add_path` /
   `set_cheapest` (`path.go:addPath`, `path.go:setCheapest`),
   `join_search_one_level` with all three phases
   (`joinsearchlevel.go:joinSearchOneLevel`), GEQO (`geqo.go:geqoSearch`),
   `costsize.c`-derived cost functions, parameterized paths, pathkeys, and a
   `create_plan` (`createplan.go:createPlanNode`).

**The seam.** `tryJoinSearch(node, pred, ctx, cat)`
(`joinsearchseam.go:168`) is still the only production door into the search:
call `tryPGShapedJoinSearch`; if `used`, return the searched tree and the
residual predicate; otherwise return `(node, pred)` untouched. The gate table
from take2 §0 is unchanged **[carried]** with one addition: before the search
runs, the seam now applies `inferEquivClassConstants(conjuncts)`
(`joinsearchseam.go:319`, P1-20) — the **constant half** of the equivalence
closure only (`a = b AND a = 42` synthesises `b = 42` for the search; the
transitive `a = c` half stays on its legacy caller `pushPredicatesIntoCrossJoins`).

`splitOuterSpine`, `extractSearchLeaves`, the eleven decline labels and the
`GOOPG_PGSHAPED_DP_TRACE=1` tracing are unchanged **[carried]**.

**The collapse default still pins explicit JOINs.** `joinPinned`
(`collapse.go:436`) still returns `!collapseJoins` for `JoinInner` /
`JoinCross`, and `pgShapedCollapseFromEnv` (`collapse.go:152`) is still
`v == "1"` — **default off**. So an n-way explicit-JOIN chain is still n−1
nested two-relation problems; TPC-DS Q72 still never enumerates as one
9-relation DP problem. The flip is now Phase 0's positive control (P0-13:
TPC-H `changed=0`, exactly Q72/Q75 moved on TPC-DS) rather than a Phase 3
item. `RelSet` is now `uint32` (`path.go:30`) and `maxSearchRels` is 32
(`joinsearch.go:52`, P3-09) — the ceiling moved from 16 to 32, which changes
no benchmark plan (nothing comes near 16 relations) and is a capability
change, not a plan change. (Stale code comments at the cited sites still say
`uint16`/16 — `joinsearch.go:34-36`, `path.go:26-29` — and
`joinsearchseam.go:161` still names the deleted
`reorderCommaFromByCardinality`/`joinorder.go`; the docs above describe the
code as it runs, not as those comments describe it.)

**Three headline facts, updated.**

- `EXPLAIN` now shows the cost model. `explainCostFields`
  (`internal/executor/operators_explain.go:1896`) returns the stamped
  `PlanCost` for search-produced nodes and `DeriveLegacyDisplayCost`
  (`plancost.go:116`) for everything the legacy rewriter built (P0-02/P0-03).
  `rows=` still comes from the legacy `EstimateRows`, not from the `Path`.
- The Path model still has **no parallelism**: `generateScanPaths`
  (`pathgen.go:27`) still has no production caller (re-verified: only
  `*_test.go` call sites), `PartialPathlist` is still always empty, and
  `MaybeAddGather` (`parallel.go:88`) is still a post-pass. Unchanged.
- Cost GUCs are **no longer inert**. Both real cost sites now read a
  `PlannerSettings` carrier (P2-01/P2-02); only the display-only
  `DeriveLegacyDisplayCost` still calls `defaultCostParams()`. Session
  `seq_page_cost`/`work_mem` demonstrably move plans. The remaining gap is
  propagation, not plumbing (see §12).

---

## 1. Entry: how the executor reaches `optimizer.Plan`

There is still no `PlannedStmt` and no `PlannerInfo`. `optimizer.Plan(stmt,
cat)` (`planner.go:91`) is now `PlanWithSettings(stmt, cat,
DefaultPlannerSettings())` (`planner.go:102`); every planner entry point has
the same shape (`planStmtWithSettings`, `planSelectWithSettings`,
`planFromClause`, `planFromItem`, `planScanRangeVar`, … all take an explicit
`PlannerSettings`). `newResolveContext` (`planner.go:527`) takes the settings
as a required constructor parameter and defaults to `DefaultPlannerSettings()`,
never the zero value (a zero `PlannerSettings` would price every page at 0.0).

**Call sites (production).** Unchanged from take2 **[carried]**:
`internal/postmaster/dispatch.go` simple-query / PREPARE / DESCRIBE sites and
`dispatch_extended.go:123`.

**The catalog wrapper is still the scan-toggle channel**, and is now also the
cost-GUC channel. `sessionPlanCatalog` sets the four `Disable*Scan` flags as
before; the session's cost GUCs travel separately via `ctxPlannerSettings`
(`dispatch.go:1865`, filled at `:1936-1951`, KB→bytes for
`work_mem`, KB→blocks for
`effective_cache_size`) built from `GetSetting`, with `sessionWorkMem`
(`dispatch.go:1679`) feeding the executor side as before.

**Plan cache** (`internal/postmaster/plancache.go`): structure, key and
doorkeeper admission unchanged **[carried]**. Two take2 landings change the
correctness story:

| Property | Current value |
|---|---|
| Bypass predicate | `plannerSessionInputsActive(sess)` (`dispatch.go:2011`) = scan toggles **or** any of 24 planner-input GUCs overridden (P2-04: the nine cost GUCs + `hash_mem_multiplier` + seven method toggles `enable_hashjoin/mergejoin/nestloop/memoize/nestloop_index/hashagg/presorted_aggregate` + seven GEQO knobs; `plannerCostGUCsOverridden`, `dispatch.go:1979-2005`). A session that `SET` one of these GUCs neither reads nor writes the shared cache. (The code comment at `dispatch.go:1975` still says "the nine cost GUCs" — stale; the list has 24 names.) |
| Invalidation | `planCacheInvalidatingStmt(node)` (`dispatch.go:3875`): DDL **plus** `ANALYZE` and `VACUUM` (P1-03b, `ada899c38`). Both plan as `*optimizer.Utility`, so the old DDL-only trigger missed them |
| Still open | Keying (rather than bypassing) on the planner context; `postgresql.conf` values are invisible to `plannerCostGUCsOverridden` (deliberate — conf values are uniform across sessions, so sharing is safe) |

**`MaybeAddGather` placement.** Unchanged **[carried]**: serial plans cached,
parallelism applied per-execution after lookup.

**EXPLAIN routing.** The `*Explain` node still wraps the real plan and
`MaybeAddGather` still descends through it. The renderer now prints real
costs (§11). Take2's gap list (§1, last paragraph) still holds for
`cursor_tuple_fraction`, scrollable-cursor `Material` and JIT thresholds
**[carried]**.

---

## 2. Preprocessing passes in actual call order

Order below is take2 §2 with take2 landings applied. Steps renumbered where
P3-12 deleted a step.

### 2.1 Inside `planSelectWithSettings` (`planner.go:797`)

| # | Pass | File:symbol | Change vs take2 |
|---|---|---|---|
| 1 | set-op fold | `planner.go` (`applySetOp` closure) | — **[carried]** |
| 2 | `isSimpleSingle` classification | `planner.go` | — **[carried]** |
| 3 | ~~`reorderCommaFromByCardinality`~~ | **DELETED** (P3-12, `6f5eefa85`; file removed in `362cfa91c`, 1327 lines out) | PG-counterpart-less heuristic is gone; only `joinorder_determinism_test.go` survives, retargeted at map-order determinism |
| 4 | `planFromClause(stmt, cat, plannerSet)` | call at `planner.go:1129`; def `:2494` | now settings-threaded (P2-02, `f93ea20dd`); `reduceOuterJoins`, `deconstructJointree`, `collectSpecialJoinInfos` as before |
| 5 | `canonicalizeQual` | `qual_canonical.go:67` | — **[carried]** |
| 6a/6b | single-table fast path / multi-table `*Filter` | `planner.go` | — **[carried]** |
| 7 | `ctx.tupleFraction = searchTupleFraction(…)` | `joinsearchseam.go:653` (def; the old `:629` sits inside `chainCarriesLateral`) | — **[carried]** (still parse-level literals only) |
| 8 | `ctx.neededCols = neededColumnNames(s)` | `pathindexonlyneed.go:34` | still gates index-only paths; the P4-01b resume point (why `known=false` on plain aggregates) is recorded in TODO P4-01b and unchanged |
| 9 | S5a pre-DP unnest branch | gated by `unnestPreDPOn` (`unnest.go:57`; set at `:45`, read via `unnestPreDPEnabled` at `:64`); `runJoinSearchBelowPinned` def at `predp.go:73` | — **[carried]** (`GOOPG_UNNEST_PREDP` still on; `unnest.go:424` is the post-pass `unnestSubqueriesInPlan`, not this branch) |
| 10 | legacy branch: `tryJoinSearch` + equiv-class constants at the seam | `joinsearchseam.go:168`, `:319` | **P1-20**: `inferEquivClassConstants` synthesises the constant half for the search (470× cost reduction on the `a = b AND a = 42` probe) |
| 11–13 | lateral pushdown, filterless-outer-join retry, post-search unnest | `pushdown.go`, post-pass `unnestSubqueriesInPlan` (`unnest.go:424`) | — **[carried]** |
| 14 | `rewriteScanInputsWithSingleTablePredicates` | `scan_input_rewrite.go:50` | still live; **P6-03 attempted deletion and failed** (Q20 6.5×, correlated SubPlan degenerates) — it serves the shapes the search declines |
| 15 | `rewriteJoinsToNLI(node, cat, plannerSet)` | `nl_index_join.go:89` | now takes `PlannerSettings` (P2-02c, `294b82ec9`); still live — **P6-04 attempted deletion and failed** (Q4 semi-join 12.5×) |
| 16–18 | coordinate repairs, inner-join qual pushdown | `joinlayout.go` | — **[carried]** |
| 19–20 | agg-sublink promotion, min/max rewrite | `planner.go` | — **[carried]** |
| 21–24 | aggregate stage + three grouping rules | `buildAggregateStage` (`planner.go:7084`), `groupagg_indexorder.go:63`, `groupagg_presorted.go:45`, `groupagg_hashagg.go:60` | all three rules now take `PlannerSettings` (P2-02c, `d69765485`) |
| 25–31 | window, sort, limit, project, lockrows, fold-constants, distinct | `planner.go` | — **[carried]** |

### 2.2 Tail of `Plan`, after `planStmt` returns

Unchanged list **[carried]** (`pushQualsThroughSingleRefCTEs`,
`rewriteExistsToAny`, `lowerSubPlanParams`, `fillJoinHashKeys`,
`assertSearchedBoundariesIntact`).

**P6-05 correction (must-not-do).** Take2 §2.2 called `reconcileNLILayout`
dead. It is not: `assertSearchedTreeNeedsNoReconcile`
(`searchedtree.go:200`) calls `reconcileNLILayoutBody` and **panics** if the
pass would move any column reference, running unconditionally on every
searched plan (`createplanroot.go:137`). The pass is the oracle for a live
correctness tripwire; deleting it removes the check, not just the code.

### 2.3 Divergences from doc 01 §2

Take2's list holds with two edits: the parse-level FROM reorder bullet is
**deleted** (P3-12 — the divergence no longer exists), and the sublink
pull-up bullet gains the seam constant-propagation half (P1-20). Constant
folding still runs last; PG's early `eval_const_expressions` still has no
counterpart **[carried]**.

---

## 3. Upper pipeline: rules, not paths

Unchanged architecture: no upper-rel paths, one node per stage plus rewrite
rules. `PathAgg`, `PathGather`, `PathGatherMerge` still have no
`createPlanNode` arm; `PathSort` still exists only as a merge-join child
**[carried]**.

Two P4 slices touch this section without changing the architecture:

- **P4-01a (landed):** paths carry their own `NCols`/`AvgVarBytes`, read
  through `pathNCols`/`pathAvgVarBytes` (`path.go:348`, `:360`). An
  index-only path no longer sizes its hash geometry at the relation's full
  width while the executor measures the narrowed schema — the planner/executor
  disagreement the shared `hashsize.Choose` exists to prevent.
- **P4-01b (attempted, reverted, wrong answers):** narrowing the leaf on the
  path's node while leaving the binding space wide produced 0-row Q2/Q5 and
  wrong-tuple Q18. Lesson recorded in TODO: the projection must be visible
  to whatever computes coordinates — i.e. real `PathTarget`/`setrefs` work
  (P4-01/P6-02/P6-07), not a leaf swap.

The LIMIT-through-`tupleFraction` bullet and the `rows=`-from-`EstimateRows`
consequence still hold **[carried]**.

---

## 4. `query_planner` analogue

### 4.1 Joinlist construction

`collapse.go` unchanged **[carried]**, including hard-coded
`from_collapse_limit = join_collapse_limit = 8` (`defaultCollapseLimits`)
and `SET join_collapse_limit = 1` remaining a no-op. `GOOPG_PGSHAPED_COLLAPSE`
still defaults off (§0).

### 4.2 restrictInfo

Unchanged **[carried]** (`buildRestrictInfos` at `joinrestrict.go:116`,
`relidsOfExpr` bucketing at `:419`, the out-of-window decline).

### 4.3 Equivalence classes

`equiv_class.go` union-find as before, plus **`inferEquivClassConstants`**
(`equiv_class.go:141`, P1-20): `inferTransitiveEqualities` restricted to its
constant-propagating half, applied at the seam (`joinsearchseam.go:319`).
`nconst_ec` itself is reachable as a future concern but not needed yet —
goopg has no FK `1/ref_tuples` shortcut to double-count. The absent list (no
EC object, no join domains, pathkeys not EC-based) is unchanged **[carried]**.

### 4.4 SpecialJoinInfo

Fields unchanged **[carried]**. **P3-01 scoped, not implemented**
(`1777d7df0`): the full field set already exists on the struct; what is
missing is *population* (`MinLefthand`/`MinRighthand` conservatively equal
`Syn*`, `LhsStrict`/`Commute*`/`Ojrelid`/`SemiOperators`/`SemiRhsExprs`
unpopulated). Blocker is name resolution — `makeSpecialJoinInfo`
(`specialjoin.go:54`, called from `collapse.go:416`; `collapse.go:398` is the
neighbouring `deconstructFromItem`) runs before Vars carry relation indexes — and a partial
fix is unsafe (`min = syn` overestimates, which only forbids orders; an
underestimate permits reorderings PG forbids = wrong answers). Outer/semi/anti
joins still never reach `joinIsLegal` in production **[carried]**.

### 4.5 Lateral

Unchanged **[carried]**.

### 4.6 Local filter attachment before search

Unchanged mechanism **[carried]** (`partitionConjunctsForJoinPlanning`,
`localizeExprToLeaf`, `numQualOps = 0` discipline).

### 4.7 Absent entirely from §4

Unchanged list **[carried]**.

---

## 5. Base rel paths

`buildInitialRels` still seeds exactly one `PathPrebuilt` per FROM item,
costed with `costSeqscan(cp, estScanPages(rows, width), rows, 0)`; the
`numQualOps = 0` discipline and its rationale are unchanged **[carried]**.
`generateScanPaths` still has no production caller (re-verified — only
`*_test.go` references); `addPartialPath` therefore still unreachable in
production.

`addBaseRelIndexPaths` producer table unchanged, with two take2 deltas:

- **P1-01 (`287232e17`):** index `relpages`/`reltuples` in `pg_class` were
  hard-wired `0`/`-1` and now report real figures (as measured: `part_pk`
  497 pages / 200000 tuples vs PG's 551 / 200000). The planner was already receiving real
  page counts via `catalog.IndexRealPages` → `RelNBlocksFunc`, so
  `estimateIndexGeometry`'s width synthesis is superseded whenever storage
  answers; remaining synthesis is tree height and the partial-index
  `indexTuples` case (P1-02, rescoped).
- **P2-09 partial (`10ee792b0`):** the unique-index single-tuple clamp
  landed. A `UNIQUE` index with equality on every key column matches at most
  one tuple whatever the selectivity arithmetic says; `fullyBound`
  (`pathparamindex.go:360`) is exactly PG's precondition. TPC-H best-of-session
  at landing, 99/99 TPC-DS shapes unchanged. The per-tuple index-qual cost was
  implemented, measured at **+3.3%** on TPC-DS, and **reverted** — a correct PG
  term that makes outcomes worse lands with the rest of `btcostestimate`, not
  alone. `num_sa_scans` is blocked: goopg builds no index path for a
  ScalarArrayOp at all (`x IN (…)` plans as seq scan + filter), so the term
  has no consumer.

`enable_*` handling changed shape (P2-05, `656236ab1`): the four **scan**
toggles are still producer-skipping via the catalog wrapper, but
`enable_hashjoin` / `enable_mergejoin` / `enable_nestloop` (previously
accepted-and-ignored) are now live **PG-style**: the path is still produced
and `DisabledNodes` incremented via `disabledNodesFor`
(`path.go:796`; `pathgen.go:97`, `:136`; `joinpathsmerge.go:389`). A query
whose only legal plan uses a disabled method still gets that plan. The
`Path.DisabledNodes`-always-0 claim from take2 no longer holds for join
methods; it still holds for scans (producer skip, not counting).
(The field comment at `path.go:124-128` still says goopg has no `enable_*`
GUCs and the count is always 0 — stale since P2-05.)

Consumer-side eligibility (`scanLeafFor`), the one-arm-short
`has_useful_pathkeys`, and derived index geometry are otherwise unchanged
**[carried]**.

---

## 6. Join search

`joinSearch` / `joinSearchOneLevel` three phases unchanged **[carried]**.
Deltas:

- **Ceiling:** `RelSet uint32`, `maxSearchRels = 32` (P3-09, §0).
- **GEQO:** all seven knobs (`geqo`, `geqo_threshold`, `geqo_effort`,
  `geqo_pool_size`, `geqo_generations`, `geqo_selection_bias`, `geqo_seed`)
  now travel on `PlannerSettings` → `costParams` (P3-10, `ab6fef649`); zero
  `BootVal`-0 semantics preserved (`GeqoPoolSize = 0` means derive, `GeqoSeed
  = 0` means the fixed state, plan-neutral at defaults). Still keyed on
  `len(items)` of one `searchOneProblem`, so still effectively unreachable
  under the default collapse regime; still no benchmark query near 12
  relations (99/99 TPC-DS shapes unchanged at landing).
- **Provenance:** `addPath(rel, newPath, producer)` (`path.go:601`) carries
  the producer as an argument and emits a `DPPATH` trace record (P0-11; a
  distinct tag, not a third `DPTRACE` kind, so older parsers do not count
  `Malformed`). `pathlistVerdict` checks the list tail, not its length.
  This is what answered P3-11: `nestloop.index` offered 694× / accepted 23×
  (3.3%), losing to the accepted path by 0.05%–12% — narrowly, not
  systematically. No single mispricing to fix; the action is the remaining
  `btcostestimate`/hash-bucket terms.
- **Merge-join correctness and costing** (see §7): `c281b0830` (cost on
  `mergejointuples`), `13d53603f` (dropped equi-clause evaluated again).
- Sub-problem collapse (one `PathPrebuilt`, `tupleFraction` forced to 0 at
  the recursion boundary) unchanged **[carried]** — and it is now also the
  named reason P2-02b stays blocked alongside width (FINDING, §12).

---

## 7. `addPathsToJoinrel` arms

`addPathsToJoinrel` (`joinpaths.go:139`) arm order and tie-break discipline
unchanged **[carried]**. Signatures and pricing changed:

| Arm | Delta vs take2 |
|---|---|
| `sortInnerAndOuter`, `matchUnsortedOuterMerge` | now take `mergeTuplesFor` + `scanSelFor` closures (`joinpathsmerge.go:238`, `joinpathsmergeouter.go:117`) |
| `addHashJoinPath` | unchanged shape; probe now charges the bucket walk (`(s *searchCtx).estimateHashBucketSize`, `joinselectivity.go:479`, P2-11 — ndistinct-derived fraction per orientation as a closure; zero fraction suppresses the term; MCV-frequency half still open) |
| `addNestLoopPath` | rescan is now `pathRescanCost` (`joinpathsmemoize.go:392`, P2-07: inner startup paid once, `outerRows−1` at run cost; Material/Sort arm + default re-execute arm; CTE/WorkTable arm still open). Plain-NL inner priced as **materialised** (P2-06: build once at `2 × cpu_operator_cost × tuples` + spill, rescans at `cpu_operator_cost × tuples`; parameterised inners excluded, as is PG's `create_material_path`) |
| `tryMergeJoinPath` | costs **`mergejointuples`, not `joinrel.Rows`** (`joinpathsmerge.go:362-385`, `c281b0830`; its own comment had named the right quantity) **and** END scan selectivities (`(s *searchCtx).mergeJoinScanSel`, `joinselectivity.go:533`, P2-12; START selectivities reported 0 — goopg's merge performs no seek, so the omission is a no-op). Residual quals charged on `mergeTuples` (`joinpathsmerge.go:385`). Dropped-clause fix: `demoteDroppedMergeClauses` (def `joinpathsmergeouter.go:296`; call sites `:234`/`:251`) keeps a trimmed merge clause's remainder in the residual so it is evaluated somewhere (`13d53603f` — was wrong answers, now fixed) |
| `addNLIPaths` | unchanged discharge rules **[carried]**; gate now settings-aware (§2 step 15) |

`paramSourceRels()` still returns 0; still no two-stage costing, no
`inner_unique`, no `Material` path (P2-06 deliberately: the executor
materialises both NL sides unconditionally and the merge buffers per key
group — a path-level Material would buffer twice) **[carried]**.

---

## 8. Path / RelOptInfo structs

`Cost`, `PathKind` enum (no new kinds), `comparePathCostsFuzzily` (1.01 fuzz,
`disabled_nodes`-first) unchanged **[carried]**. Deltas:

- **Per-path width (P4-01a):** `Path.NCols` / `Path.AvgVarBytes`
  (`path.go:116-120`); zero means "not narrowed". Read through
  `pathNCols`/`pathAvgVarBytes` (`path.go:348`, `:360`).
- **`DisabledNodes` is now set** for join-method paths (§5, P2-05); still
  always 0 for scans. (`path.go`'s own field comment at `:124-128` predates
  this — see §5.)
- **`addPath` takes `producer string`** (`path.go:601`) for the DPPATH trace
  (P0-11).
- `RelOptInfo` fields unchanged **[carried]** (`baseLeaf`/`baseOffset`
  coordinate model, `ConsiderParamStartup` permanently false, no `PathTarget`).

The coordinate-model subsection of take2 §8 (layouts, boundary assertions,
`joinlayout.go` repair passes) is unchanged **[carried]** — with the P4-01b
lesson appended: by-name re-basing plus typed-NULL padding is **not**
sufficient for a pruned leaf; the next attempt needs projection visible to
coordinate computation.

---

## 9. Pathkeys

Unchanged **[carried]**: `{Expr, SortAsc, NullsFirst}` matched by
`exprEqual`; no EC in a pathkey; query/group orderings still never cross the
search boundary (`pathindexordered.go:76-85`); `makeCandidatePathkeys` still
consumed only by the post-search presorted-aggregate rule. P3-06
(`standard_qp_callback` analogue) still open.

---

## 10. Parallel query

Unchanged **[carried]**: `MaybeAddGather` post-pass, `ParallelSettings`,
block-count size rule, `gatherCost` still without callers, no partial paths.
Phase 5 (P5-01…P5-08) not started. One new measurement constrains it: the
P2-02b diagnosis shows the slow arm losing its `Gather` because the plan
moves onto index-scan-driven joins the post-pass cannot drive
(`drivingScan`, `parallel.go:441`, recognises SeqScan/BitmapHeapScan/
IndexOnlyScan plus `*Filter`/`*Project`/join-probe wrappers — everything
except plain IndexScan/NLI-probe shapes) — P5-03's problem
statement, measured early.

---

## 11. `create_plan`

`createPlan(p)` → `createPlanNode(p)` now funnels through
**`stampPlanCost(n, p)`** (`createplan.go:50`, `plancost.go:82`): the winning
path's `(Startup, Total, Rows)` plus `TupleWidth(n.Output())` are embedded
on the emitted node (`PlanCost`, `plancost.go:28`; `CostSet` distinguishes
"priced at zero" from "never priced"). Nodes the search did not produce get
**`DeriveLegacyDisplayCost`** (`plancost.go:116`) — deliberately crude and
monotone (parent ≥ child; Sort/Aggregate blocking arms, everything else
pass-through) — so EXPLAIN never mixes real costs with `0.00`. Phase 4
replaces each arm with a real upper-rel path and deletes the function.

Per-kind table: unchanged from take2 (Memoize still panics by design;
Agg/Gather/GatherMerge still no arm) **[carried]**. The "no setrefs phase"
five-mechanism list still holds, with `assertSearchedBoundariesIntact` still
the final check **[carried]**.

**EXPLAIN rendering deltas (all P0):**

- Real `(cost, rows, width)` on all three render sites (text, ANALYZE,
  JSON) via `explainCostFields` (`operators_explain.go:1896`); `rows=`
  still from `EstimateRows` (switching to `Path.Rows` is a separate open item).
- Schema qualification follows verbosity (P0-04d): relations qualified in
  VERBOSE only, indexes never — threading the mode was load-bearing because
  `describePlanVerbose` delegates to `describePlan`.
- `rows=` walker agreement fixed (P0-04b, `TestWalkersAgreeOnRowEstimate`):
  the plain walker had folded the attached `Filter` while ANALYZE did not
  (200× overstatement on filtered scans in the captured mode).
- Node-type coverage: 18 missing arms + 2 `%T` arms + 4 unwalked child sets
  fixed; recursive CTEs render PG's `WorkTable Scan` (P0-01).
- Still open: P0-04 suffix numbering vs `select_rtable_names`, P0-04e JSON
  `Project`/`Filter` wrapper collapse (tree-shape, not row-estimate — does
  not block the parity instrument), P0-05/06/07 capture/diff/baseline.

---

## 12. Planner GUCs, env flags, and the settings carrier

### 12.1 `GOOPG_*` environment variables read in `internal/optimizer`

| Env var | Default | Change vs take2 |
|---|---|---|
| `GOOPG_PGSHAPED_DP` | **on** | — **[carried]** (kill-switch, `=0` = syntactic order; the old DP it once fell back to is deleted) |
| `GOOPG_PGSHAPED_COLLAPSE` | **off** | — **[carried]**; flip is P0-13 positive control |
| `GOOPG_PGSHAPED_DP_TRACE` | off | — **[carried]** |
| `GOOPG_PARALLEL` | **on** | — **[carried]** |
| `GOOPG_MEMOIZE` | **on** | — **[carried]** (env kill-switch survives; session control moved to settings) |
| `GOOPG_UNNEST_PREDP` | **on** | — **[carried]** |
| `GOOPG_EXISTS_TO_ANY` | **on** | — **[carried]** |
| `GOOPG_INDEXKEY_HARVEST` | **on** | — **[carried]** |
| `GOOPG_NLI_COSTGATE` / `…_DEBUG` | current / off | — **[carried]** |
| `GOOPG_HASH_OUTER_JOIN` | **off** | measured (`fd28dbb60`): flip safe now (CKMISMATCH=0) but a wash (+1s net) — **not flipped** |
| `GOOPG_RELSIZE_FALLBACK` | **retired** (`take2-P1-05`) | `relSizeFallbackRows` unconditional; provenance table stamps `retired(take2-P1-05)`. Note: the env reader + `SetRelSizeFallbackStage` test hook still exist in `relsize.go:62-118`, and `relsize.go`'s own header comment still documents `=0` as a reopen path — the retirement is recorded but the reader was not deleted. Related staleness: `relsize.go:128-129` and `joinsearchseam.go:338` still cite the deleted `bushy.go` ladder |
| `GOOPG_INDEX_PROBE_MULT` | 1.0 | now in the provenance table (P0-04c) — artefacts can state it |

Retired-but-stamped list unchanged **[carried]**.

### 12.2 `PlannerSettings`: the per-statement planner context (P2-01/P2-02/P2-02c)

`PlannerSettings` (`plannersettings.go:28`) is the session-settable planner
context — PG's `PlannerGlobal` analogue. (The file-header comment at
`:8-15` still says the nine cost GUCs "reach nothing" — stale since P2-02.) Fields: seven cost numbers
(`SeqPageCost` … `ParallelTupleCost`, `EffectiveCacheSize` in blocks,
`WorkMem` in bytes), three join-method toggles (P2-05), `EnableHashAgg` /
`EnablePresortedAggregate` / `Geqo*` seven (P2-02c/P3-10),
`EnableNestLoopIndex`, `EnableMemoize`, `HashMemMultiplier` (P2-03).
`DefaultPlannerSettings()` (`:118`) reads the seven cost numbers plus
`EffectiveCacheSize` from `cp := defaultCostParams()` — those fields only
are *defined as* what `defaultCostParams()` reads (not a second copy).
Everything else is a second source by construction: `WorkMem` comes from
`hashsize.DefaultMemLimitBytes` (`:149`, deliberately — see below),
`EnableHashAgg`/`EnablePresortedAggregate`/`EnableNestLoopIndex`/`Geqo`/
`GeqoThreshold` from process globals (`HashAggEnabled()`,
`PresortedAggEnabled()`, `NLIEnabled()`, `GeqoEnabled()`,
`GeqoThreshold()`), and `GeqoEffort: 5` / `GeqoSelectionBias: 2.0` are
literals (`:130`, `:133`). `TestDefaultPlannerSettingsMatchTheHardWiredParams`
pins equality of output, not provenance — a drift in a literal would still
be a second copy, and these are the next P2-02c/P3-10 hazard.
`costParams()` (`:160`)
applies `hashsize.HashMemLimit` (the P2-03 double-application trap —
`WorkMem` stored raw, multiplier applied once — is pinned by the same test).

Session reachability (P2-02): `SET seq_page_cost = 1000` switches a parallel
hash join to a merge join over index scans live; `SET work_mem = '64kB'`
reprices a hash join 14835 → 23478. Two channels were needed (session
registry + `GetSetting` at the simple-query site + recursive EXPLAIN
threading) — the unit-passing/live-failing gap is recorded in TODO.

All six `registry.OnChange` bridges are deleted (P2-02c); a process global
now supplies only the *default* for session-less planning (keeping env
kill-switches and test hooks working). Verified live: one session's `SET`
no longer steers another session.

### 12.3 Known gap: settings stop at derived tables

`PlanWithSettings` stamps its settings at the top-level context; the
FROM-clause path inherits them (`f93ea20dd`, gate
`TestPlannerSettingsReachSubqueryScan`). But **`planSelectWithParent`
(`planner.go:13808`) still calls the defaulting `planSelect(stmt, cat)`
(`:13828`)**, so every defaulting `planSelect` call site still plans under
hard-wired defaults: CTE bodies (`with.go:205`/`:264`/`:334`/`:395`), COPY's
inner query (`copy.go:41`), the top-level wrapper (`planner.go:58`),
set-operation operands (`planner.go:915`/`:943`/`:1047`), scalar-subquery
shapes (`planner.go:10933`), and every `planSelectWithParent` caller — not
just the three named families (derived tables / set-op operands /
scalar subqueries). That is Q9's
shape (entire join tree inside a subquery), which is why P2-02b remains
blocked. A mechanical threading attempt was **reverted** (non-monotonic
result ⇒ threaded-from-wrong-scope bug); the next attempt threads by hand,
one caller at a time. Full evidence:
`impl/FINDING-planner-settings-not-propagated.md` — including the withdrawn
"39× width" causal claim and its correction: at equal budget the arms differ
by a join-method flip (two-key hash → single-key merge, 6.0M → 24.0M rows,
Gather lost), and the slow plan scores 2.8× *worse* by goopg's own model, so
the next measurement is `addPath` attribution (was the hash candidate never
generated, or priced higher?), not cost-function comparison.

### 12.4 Memory-setting status (P0-12, P2-02b)

- Bench clusters aligned (`78ef045c8`): `work_mem` 512MB→64MB,
  `effective_cache_size` 4GB→2GB. **goopg is 62% slower at parity
  (248.71s → 403.27s), row counts identical** — the recorded 9.9× headline
  held an 8× `work_mem` advantage; the honest ratio is nearer 17.6×.
  (Timings as measured, not re-verified read-only.)
  `shared_buffers` stays divergent by design (Go-heap arena under
  GOMEMLIMIT).
- `work_mem` BootVal is still `512MB` (`defaults.go:785`) vs PG's 4MB, and
  the planner/executor fallback still `512<<20` (`hashsize.go:83`). P2-02b
  (BootVal correction) is open and ordered **after** P0-12: at PG's budget
  Q9 returns correct counts but wrong tuples without `13d53603f`; with it,
  values are correct (24 MATCH) and the item is purely performance (+23.1%,
  all Q9+Q7). Re-measured after the ndistinct fix: same gap, same two
  queries.

### 12.5 Planner GUC reachability (updated)

"Reaches planner" = a second, non-declaration reference exists.

| GUC | Reaches? | How (delta vs take2) |
|---|---|---|
| `enable_seqscan/indexscan/bitmapscan/indexonlyscan` | Yes | unchanged: catalog toggles → producer skip |
| `enable_hashjoin/mergejoin/nestloop` | **Yes (new)** | P2-05: `DisabledNodes`, not skipping |
| `enable_memoize/nestloop_index/hashagg/presorted_aggregate` | Yes | now **per-statement** via settings (P2-02c), no longer process-global |
| `geqo`, `geqo_threshold` + five tuning knobs | Yes | all seven per-statement (P3-10) |
| nine cost GUCs + `hash_mem_multiplier` | **Yes** | P2-02/P2-03 via `PlannerSettings`; `hash_mem_multiplier` consumed in `HashMemLimit` |
| `from_collapse_limit`, `join_collapse_limit` | No | unchanged (hard-coded 8, behind collapse flag) |
| `cursor_tuple_fraction`, `constraint_exclusion`, JIT, `plan_cache_mode` | No | unchanged **[carried]** |
| parallel family | Partially | unchanged: only what `ParallelSettings` carries **[carried]** |

---

## 13. Structural divergence summary

Take2's 31 items with take2 landings applied (dropped/struck items removed,
new state appended):

1. Two planners, one seam — holds (§0).
2. Silent total decline — holds (§0).
3. Explicit INNER joins pinned by default — holds (§0); flip is P0-13.
4. Mixed comma + `ON`-join declines the seam — holds **[carried]**.
5. Outer/semi/anti never enter the search — holds (§4.4); P3-01 scoped.
6. No range table, no `PathTarget` — holds (§8); P4-01a narrows per-path
   width, P4-01b reverted.
7. 16-relation ceiling → **32-relation ceiling** (P3-09).
8. Sub-problem crosses as one prebuilt node — holds **[carried]**.
9. One seed `PathPrebuilt` per base rel; `generateScanPaths` test-only —
   holds (re-verified).
10. No partial paths; parallelism a post-pass — holds (§10).
11. `enable_*` producer-skipping — **half superseded**: join methods now use
    `DisabledNodes` (P2-05); scans still skip.
12. Cost GUCs inert — **superseded**: nine cost GUCs + `hash_mem_multiplier`
    + six method/GEQO toggles reach the planner per-statement (§12.2); only
    display costing still uses `defaultCostParams()`.
13. Collapse limits hard-coded — holds (§4.1).
14. GEQO wired but unreachable — **half superseded**: all seven knobs reach
    the search (P3-10); still unreachable in practice under default collapse.
15. No upper-rel path stages — holds (§3).
16. Query orderings never reach the search — holds (§9).
17. Pathkeys not EC-based — holds (§9).
18. EC as inference-only union-find — holds, plus constant propagation at the
    seam (P1-20, §4.3).
19. Single-relation quals pre-attached to leaves — holds **[carried]**.
20. No two-stage join costing — holds **[carried]**.
21. `param_source_rels` unmodelled — holds **[carried]**.
22. No `Material` path — holds **by decision** (P2-06: executor already
    materialises; NL inner priced as materialised instead).
23. No `inner_unique` — holds **[carried]**.
24. Post-search rewrites override cost choice — holds **by measurement**
    (P6-03/P6-04 deletions attempted, 6.5×/12.5× regressions).
25. Pre-search FROM reorder — **removed** (P3-12).
26. Late constant folding — holds **[carried]**.
27. No `setrefs` phase — holds, now with stamped costs (§11).
28. Costs never reach the node — **superseded**: `PlanCost` stamped at
    `createPlan`; EXPLAIN prints real costs with a monotone legacy fallback.
29. Two cardinality estimators — holds (doc 06; P6-01 still open).
30. Plan-cache soundness gaps — **narrowed**: cost-GUC bypass (P2-04) +
    ANALYZE/VACUUM invalidation (P1-03b); keying still open.
31. `reconcileNLILayout` dead — **withdrawn**: live tripwire oracle (P6-05
    must-not-do).
32. **(new)** Settings stop at derived tables — `planSelectWithParent`
    re-defaults; subquery-heavy queries plan join search under defaults
    (§12.3).

---

## Appendix — take2 claims that no longer hold

Per the take3 method (re-verify, then mark), these take2 statements are
refuted at this HEAD — each with the refuting symbol or commit:

1. *"EXPLAIN prints a literal `(cost=0.00..0.00 … width=0)`"* — refuted by
   `explainCostFields` (`operators_explain.go:1896`) + `stampPlanCost`
   (`plancost.go:82`); `9cbc7661b`.
2. *"Cost GUCs are inert; `defaultCostParams()` has two production callers
   neither threading a session"* — refuted by `PlannerSettings.costParams()`
   (`plannersettings.go:160`) and the session fill (`dispatch.go:1936-1951`);
   display-only use remains in `DeriveLegacyDisplayCost`.
3. *"`Path.DisabledNodes` … is always 0"* — refuted by `disabledNodesFor`
   (`path.go:796`) with production writers (`pathgen.go:97`, `:136`,
   `joinpathsmerge.go:389`); `656236ab1`.
4. *"`reorderCommaFromByCardinality` runs on the parse tree before anything
   is planned"* — function and file deleted; `6f5eefa85`, `362cfa91c`.
5. *"`RelSet` is `uint16`; `maxSearchRels = 16`"* — now `uint32` / 32;
   `cb6a725d0`.
6. *"`reconcileNLILayout` is dead in production"* — live via
   `assertSearchedTreeNeedsNoReconcile` (`searchedtree.go:200`); `e86971083`.
7. *"A declined search … the statement silently keeps its syntactic FROM
   order"* — still true, but the searched arm now additionally receives
   synthesised constant clauses (`joinsearchseam.go:319`); `7ef387324`.
8. *"Merge over a many-to-many key is priced as one pass … charged on
   `joinrel.Rows`"* — now charged on `mergejointuples`
   (`joinpathsmerge.go:379-385`); `c281b0830`.
9. *"A rescan was free / charged full total"* — now `pathRescanCost`
   (`joinpathsmemoize.go:392`); `5918fe094`.
10. *"`enable_hashjoin` et al: `SET` succeeds and is ignored"* — now live via
    settings; `656236ab1`. Same for the six `OnChange` bridges ("last `SET`
    wins process-wide") — deleted; `62a5006c7`, `d69765485`, `294b82ec9`.
11. *"ANALYZE does not invalidate [the plan cache]"* — now invalidates via
    `planCacheInvalidatingStmt` (`dispatch.go:3875`); `ada899c38`. Same file
    also refutes *"TRUNCATE does not reset `Table.Stats`"* (`d3e12b3b4`).
12. *"The `work_mem` divergence … 512MB … agrees by accident"* — still true
    of the defaults, but the bench clusters no longer measure it
    (`78ef045c8`, +62% at parity).
