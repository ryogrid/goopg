# 08 — Target design

How goopg gets from "a PG-shaped join search grafted into a rule-based
rewriter" to "PostgreSQL's planner, reimplemented in Go, that selects
PostgreSQL's plan for the same query on the same data".

Read [07-gap-analysis.md](07-gap-analysis.md) first for the evidence this
design responds to, and [09-verification-and-acceptance.md](09-verification-and-acceptance.md)
for how each step is measured. [TODO.md](TODO.md) is the executable form of
this document.

---

## 0. Thesis

**Make the Path/RelOptInfo search the only planner, feed it PostgreSQL's
statistics and PostgreSQL's cost inputs, and delete everything that plans
around it.**

goopg already has a genuine PG-shaped join search: `RelOptInfo`, `Path`,
`addPath` with `compare_path_costs_fuzzily`'s exact ordering, three-phase
`join_search_one_level`, GEQO, parameterized paths, pathkeys, and cost
functions ported term-by-term from `costsize.c`. It is on by default. The
problem is not that the machinery is missing — it is that the machinery is
**reachable for only part of the query, fed inputs PostgreSQL would not
recognise, and overridden before and after it runs**:

| what limits it | consequence |
|---|---|
| with `GOOPG_PGSHAPED_COLLAPSE` off (the default) every explicit `JOIN` is pinned, so an n-way join becomes n−1 nested two-relation problems; outer joins are additionally peeled, and one that cannot be peeled declines the **whole statement** | for explicit-`JOIN` syntax no join order is searched at all; TPC-DS Q72's plan is unreachable at any cost setting |
| session cost GUCs never reach `costParams` (2 production callers, neither with a session) | `SET random_page_cost` is inert; the cost model cannot be tuned or even tested |
| EXPLAIN prints a literal `cost=0.00..0.00 … width=0` and a `rows=` from the *legacy* estimator | no cost change is observable; a cost bug is indistinguishable from a candidate-generation bug |
| upper planning (agg, sort, distinct, window, setop, limit) is fixed rules on the node tree, not paths | aggregation and sort strategy are never costed against alternatives |
| parallelism is a post-pass over the finished tree; `PartialPathlist` is never populated in production | Gather placement and parallel-scan eligibility are outside the cost model |
| two cardinality estimators run simultaneously and are hand-mirrored | every selectivity fix must be written twice, and a green test on one twin proves nothing about the other |
| pre- and post-search heuristics (`reorderCommaFromByCardinality`, `rewriteScanInputsWithSingleTablePredicates`, `rewriteJoinsToNLI`) rewrite the search's input and its output | the search's decisions are not the plan |

Each of the seven is a phase below. The order is deliberate and is defended in
§2.3.

### 0.1 Non-goals

- **Time parity on its own.** TPC-H Q6 runs the node-for-node PG-identical plan
  and still takes 23.40 s against PG's 0.99 s serial. The per-row executor tax
  is real, catalogued in 07 §6, and out of scope here. This bundle targets plan
  parity (bars A1–A5 in 09 §7.1) and the time movement that follows from it.
- **Re-litigating settled no-gos.** Multi-way hash join as a plan node, runtime
  join fusion, and cost-driven join order over the old integer DP are all
  closed with measurements. See 07 §4.
- **A new cost currency.** PostgreSQL's constants are the target, unchanged.

---

## 1. Design principles

These are constraints, not preferences. Each was paid for.

**P1 — PostgreSQL is the specification, including its mistakes.** Where
`selfuncs.c` uses an approximation goopg could improve on, goopg reproduces the
approximation. Plan parity is the objective; a better estimate that produces a
different plan is a failure of this project even when it is a better estimate.
Deviations require a committed measurement and a ledger row (09 §7.4).

**P2 — Structure before calibration.** Every attempt to fix a slow query by
moving a cost term has failed: inferred-edge penalties (M0076), accurate
NDistinct standalone (regressed Q5 +42 %), MHJ cost integration with a 100×
materialisation penalty (`cost-model/15`), the >2M-row build penalty plus
`inner_pages` charge (M0126-0013, took Q5 from 8.15 s to 600 s+). Every win came
from structure: the right search space, the right candidate set, or a missing
statistic. When a query is wrong, ask which candidate was never generated
(09 §1 R4) before asking which term is mispriced.

**P3 — No query-specific forcing, no penalty multipliers, no shape
preferences.** Established by `cost-model/15` and confirmed by M0126: threshold
penalties make the search dodge the penalised operator by routing work through
extra passes rather than choosing the intended plan.

**P4 — Sibling paths move together.** encode↔decode, fast-path↔interpreted
evaluator, and — for this work specifically — the two cardinality estimators,
the three column-stats resolvers, and the two NLI routes. A change to one twin
that leaves the other is a defect even when tests pass. Phase 6 removes the
twins so the rule stops applying.

**P5 — One variable per commit, enforced by sequencing.** M0126 had to retire
MHJ before flipping join order because one env var moved both.

**P6 — Every deferral gets a ledger row.** `.ralph/deferral_ledger.md`, 7
columns, upstream citation, concrete resume point.

**P7 — A plan-shape change is timed on both suites.** 09 §1 R1/R2.

---

## 2. The central structural decision

### 2.1 What "one planner" means here

PostgreSQL's planner is a single pipeline: `subquery_planner` preprocesses,
`query_planner` builds `RelOptInfo`s and searches, `grouping_planner` adds
upper-rel paths, `create_plan` translates the winner. Every decision that
affects the output is a **path choice priced by a cost function**.

goopg's is two pipelines joined at `tryJoinSearch`. The target is PostgreSQL's:
one pipeline, where the only thing that decides a plan is `addPath`.

This is reached by **growing the search's coverage until the legacy path is
unreachable, then deleting it** — not by rewriting the planner in one motion.
Each phase widens what the search owns, and each widening is gated by a plan
diff plus timings. The legacy tree stays as the fallback until Phase 6, when it
is removed because nothing reaches it.

### 2.2 The seam, precisely

Two things shrink the search, and the bigger one is a default.

**The collapse default.** `joinPinned` (`internal/optimizer/collapse.go`)
returns `!collapseJoins` for `JoinInner` and `JoinCross`, and `collapseJoins` is
`GOOPG_PGSHAPED_COLLAPSE`, **off by default**. Every explicit `JOIN` node
therefore forces its own order: `deconstructFromItem` folds an n-way chain into
n−1 nested two-member pinned items and `makeRelFromJoinlist` calls
`searchOneProblem` on two relations at a time. Comma-separated FROM lists are
collapsed at the top level and do get a real search — which is exactly why
TPC-H (comma joins) gets one 8-way search on Q8 while TPC-DS (explicit `JOIN`)
gets pairwise problems throughout. GEQO is unreachable for the same reason: a
two-item problem never approaches `geqo_threshold`.

**The seam's own decline conditions.** `tryPGShapedJoinSearch`
(`internal/optimizer/joinsearchseam.go`) falls back to the syntactic shape when:

- fewer than 2 or more than `maxSearchRels` (16) base relations;
- `splitOuterSpine` cannot peel the outer joins into a prefix spine;
- `extractSearchLeaves` meets anything but INNER/CROSS, including a predicated
  inner link at a non-zero base offset — which is what `FROM a, b JOIN c ON …`
  produces;
- lateral references, offsets, or a nullable prefix in the wrong position;
- `makeRelFromJoinlist` meets a **pinned outer join** it cannot rebuild, which
  returns an error declining the whole statement, not just the item.

Together these are the TPC-DS Q72 mechanism and the reason its plan is
unreachable at any cost setting. Neither is a costing bug and no amount of
statistics work touches either. **Phase 3 is therefore the highest-value
structural change in this document**, and everything before it is either
instrumentation or an input that Phase 3 will consume.

### 2.3 Why the phases are ordered this way

The natural objection is: if Phase 3 is the biggest win, why is it third?

Because of Round-4. Running `ANALYZE` before the TPC-H stream fixed Q5 (415 s →
18 s) and simultaneously broke Q22 by 128×, Q4 by 79×, Q8 by 53×, Q2 by 26× and
Q12 by 4.4×. The conclusion recorded at the time was: *"Statistics are not safe
to keep on until the planner can cost the join orders they enable."* The
symmetric statement is the one that orders this document: **a wider search space
is not safe until the numbers that rank it are right.** Widening the search
first would enumerate more plans and rank them with a cost model whose GUCs are
inert and whose statistics are missing terms — and the failure would arrive as
an aggregate surprise across two suites.

So:

- **P0 instruments** first, because without EXPLAIN costs and a node-level
  goopg-vs-PG plan diff, no later phase is falsifiable. P0 changes no planner
  behaviour.
- **P1 statistics** next, because a missing statistic masquerades as bad
  calibration for as long as you let it. This is not speculative: index-scan
  correlation was lost across restart, `csquared` was 0 everywhere, and every
  index scan was priced at `max_IO_cost`. Fixing it moved Bitmap Heap Scan from
  1 to 6 — exactly PG's count — with **no cost change at all**, and retired a
  recorded cost/performance "trade" as an artifact of the missing statistic.
- **P2 cost inputs**, because the cost functions are largely faithful already;
  what is missing is that they cannot see the session, cannot express a disabled
  node, and have no `cost_material` or `cost_rescan`.
- **P3 search coverage**, now that ranking is trustworthy.
- **P4/P5** widen the search to the upper rels and to parallelism.
- **P6** deletes the twins and the overrides.

Round-4's failure mode is also directly mitigated by P0: with a per-query plan
diff and a timing arm, a P1 change that moves five plans is *seen* as five moved
plans, not as a slower total.

---

## 3. Phase 0 — Instruments

Full specification in 09 §3. Design notes:

**P0-1 node-level plan-parity diff.** New capture/diff mode covering both
suites, with the PG side committed as a fixture (like the SF0.5 oracle) so the
diff runs without a live PostgreSQL. Comparison is over the normalised plan
*tree*: node type including the `Parallel` prefix, target relation/index, join
type and method, sort/agg strategy, child order. Every diff is classified into
one of nine categories, because the later phases are organised by those
categories and their counts are the phase exit criteria.

**P0-2 EXPLAIN cost surfacing.** Replace the two literal
`(cost=0.00..0.00 rows=%d width=0)` sites in
`internal/executor/operators_explain.go` with the chosen `Path`'s real
`(Startup, Total)`, `Rows` and `Width`. This requires the plan node to carry a
back-reference to the path (or the numbers copied onto it at `createPlan`
time). Do the second: `createPlanNode` already has both in scope, and a
back-pointer would keep paths alive for the plan's lifetime.

Nodes the search does not produce (everything above the seam, today) must not
print zeros either — they get the legacy estimate with the cost fields computed
from it, and Phase 4 replaces them with real upper-rel path costs. A node
printing `cost=0.00..0.00` after P0 is a bug, and a test asserts it.

**P0-3 renderer parity.** De-duplication exists
(`internal/executor/explain_names.go` appends `_N` from the second occurrence of
a base name), with a documented divergence from `select_rtable_names` in how the
suffixes are numbered. Align the numbering, and re-measure
`shape_mismatches = 46` — the figure was attributed to *missing* dedup, so its
true tightness is unknown until the renderer matches. Also fix the mode
asymmetry where a plain `EXPLAIN` sources its `rows=` from
`attachedFilterNode` while `EXPLAIN ANALYZE` sources it from the node itself,
so the two modes can print different estimates for the same scan.

Add `GOOPG_INDEX_PROBE_MULT` to `internal/optimizer/flaglabels.go` so it
appears in the generated `scripts/planner-flags.env`. It is the one
plan-shaping knob the provenance mechanism does not cover, which means no
artifact can currently state which value of it was measured (09 §1 R5).

**P0-4/5/6 oracle hygiene.** Re-pin the nightly plan-gate baseline; re-capture
the TPC-DS oracle with sub-second timing; fix the inert TPC-DS row anchors
(`summarize.py` reads `rows`, the CSV column is `expected_rows`).

**P0 exit:** the parity instrument produces a committed baseline roll-up for
both suites, and `PP changed=0` against a pre-P0 goopg capture — P0 must not
move a single plan.

---

## 4. Phase 1 — Statistics fidelity

Target: the planner reads the same numbers PostgreSQL's planner would read on
the same data. Oracle: doc 03 in this bundle.

### 4.1 Relation and index statistics

| item | current | target |
|---|---|---|
| index `relpages`/`reltuples`/tree height | synthesised by `estimateIndexGeometry` from heap rows and declared key widths at btree default fillfactor | real per-index values collected by ANALYZE/VACUUM and stored on the catalog index, read like PG's `get_relation_info` fills `IndexOptInfo` |
| VACUUM and autoanalyze relstats durability | both update memory only; the durable sidecar is written by SQL `ANALYZE` alone, so a VACUUM- or autovacuum-maintained cluster plans from stale sizes after every restart | both persist `reltuples`/`relpages` like `vac_update_relstats` |
| `TRUNCATE` | does not reset `Table.Stats` | reset, as upstream does |
| `ANALYZE` and cached plans | `ANALYZE` is planned as a `Utility` statement and only DDL invalidates the plan cache, so fresh statistics do not reach a cached plan | `ANALYZE` invalidates the cached plans of the relations it touched |
| `allvisfrac` | `RelAllVisibleFunc` is wired, but the search rel does not receive it | plumbed to the `RelOptInfo` so index-only path costing uses the real fraction, per `cost_index`'s `indexonly` reduction |
| never-analyzed relations | `GOOPG_RELSIZE_FALLBACK` stage 2 reads the live smgr block count | keep, and align its density formula with `estimate_rel_size` including the 10-page floor; the flag becomes unconditional behaviour and retires |

`estimateIndexGeometry` is the single largest fabricated input in the cost
model: it decides the page count that `cost_index` charges random I/O for. Its
own comment names the resume point as "index-level catalog statistics, not a
better formula".

### 4.2 ANALYZE algorithm

- **Sampling**: goopg full-scans every block and reservoir-samples the visible
  tuples; PostgreSQL two-stage block-samples (`BlockSampler` + Vitter) at
  `300 × stattarget` rows. Adopt PG's, because the sample *distribution*, not
  just its size, determines the MCV list and the histogram bounds — and because
  full-scan ANALYZE does not scale to the sizes this project targets.
- **n_distinct**: already the Haas–Stokes `Duj1` estimator, branch for branch
  (07 §3.11 item 1). One defect to fix: the
  `ALTER TABLE … SET (n_distinct = …)` override writes only the absolute field
  while `StaDistinct()` consults the fraction first, so the override is
  silently ignored on any column whose sampled fraction exceeds 10 %.
- **MCV list**: adopt 18.3's `analyze_mcv_list` rule. goopg's current 1.25×
  margin over-admits entries on near-uniform columns, and every admitted MCV
  displaces a histogram bound.
- **Histogram**: equal-depth bounds over the non-MCV portion, count =
  `min(stattarget, ndistinct - num_mcv)`.
- **Correlation**: already PG-faithful and persisted as `stakind3`; keep.
- **Index statistics**: `compute_index_stats` for expression indexes, and
  `vac_update_relstats` for every index's pages/tuples.

### 4.3 Statistics storage

The blocking defect is that `persistStatsToPGStatistic` does not TOAST, so a
wide-text histogram exceeds one heap page and the row is silently dropped —
measured on `orders`, `customer` and `partsupp`, which lost trailing-column
rows *and* their size row. Two acceptable resolutions:

1. TOAST support on the catalog heap writer (PG-faithful, larger blast radius,
   and the end-state M0112 wants); or
2. bound the histogram's serialized width by reducing the bound count for wide
   text columns, recording the reduction.

Prefer (1); (2) is an acceptable interim **with a ledger row**, because a
missing histogram on `ps_comment`-class columns silently degrades every range
estimate on that column to a default.

Also in this phase: `goopg_relstats` is goopg-private and invisible to a
PostgreSQL standby. That is acceptable for now and already ledgered under
M0112; it is named here so the reviewer does not read its absence from the
target as an oversight.

### 4.4 Selectivity: restriction clauses

**`convert_to_scalar` comes first, before anything else in this section.**
goopg's `numericValue` handles only the numeric family, so `bucketFraction`
returns a flat **0.5** for `date`, `timestamp`, `text`, `varchar`, `char` and
`bool` — every histogram interpolation on a date column lands mid-bucket by
construction. Port `convert_to_scalar`'s four families:
`convert_numeric_to_scalar`, `convert_string_to_scalar` (with its
common-prefix and character-weighting behaviour),
`convert_timevalue_to_scalar`, and the network variants. Date and timestamp
range predicates are the dominant restriction shape in both benchmark suites,
so this is the highest-value single item in Phase 1.

Then port `clauselist_selectivity` (`clausesel.c`) as a real function rather
than an inlined AND-product. Three behaviours arrive with it, in this order:

1. **`RangeQueryClause` pairing** — `x > a AND x < b` on the same variable is
   estimated as `s1 + s2 - 1` with the `DEFAULT_RANGE_INEQ_SEL` floor, not as
   the product of two independent inequalities. Compounded with the flat 0.5
   above, a TPC-H Q6-shaped one-year window is currently priced near 0.31
   against a true ~0.14.
2. **Extended-statistics consultation** before the independence assumption
   (§4.6).
3. **`RestrictInfo` selectivity caching** (`norm_selec`/`outer_selec`).

Then the missing per-clause estimators. `nulltestsel` is first and cheapest:
`IS NULL` has no arm at all today and falls to a generic default, so the
`NullFrac` that ANALYZE collects and persists is never read for the one clause
it exists to answer. Then general `scalararraysel`, `patternsel` for LIKE and
regex beyond the access-path prefix rewrite, `rowcomparesel`, `booltestsel`,
`var_eq_non_const`, and PG's exact `DEFAULT_*` constants.

### 4.5 Selectivity: join clauses

The highest-value items, in the order they matter for the benchmark evidence:

- **MCV pairing in `eqjoinsel_inner`.** goopg implements only the no-MCV
  branch: `(1-nullfrac1)(1-nullfrac2)/max(nd1,nd2)`. PostgreSQL matches the two
  MCV lists pairwise through the equality operator and combines
  `matchprodfreq`, `unmatchfreq` and the non-MCV remainders. On skewed join
  keys the two differ by orders of magnitude.
- **Join type in the search's sizer.** `calcJoinrelSize` computes
  `outer.Rows × inner.Rows × sel` with **no join-type branch at all**: the
  LEFT/FULL floors and the SEMI/ANTI arms exist only in the legacy
  `estimateJoin`. So the arm that actually chooses the plan sizes an outer join
  as if it were an inner join, and goopg's LEFT can estimate *fewer* rows than
  its preserved input — not a bad estimate but an impossible one. Port
  `calc_joinrel_size_estimate`'s full jointype switch into the search arm. This
  is the sharpest instance of the two-estimator hazard and the highest-value
  item in this subsection.
- **`eqjoinsel_semi`** with its MCV arm and the `nd2`-clamped-by-inner-rows
  rule; the `(1-nullfrac1)` factor currently unread. Note that MCV pairing does
  exist in production today, but *only* in the semi/anti arm
  (`semiPairMatchFraction`) — the inner-join path is the no-MCV branch.
- **`isunique` in `examineJoinVar`** via `has_unique_index`, with PG's nullfrac
  derating in the FK formula.
- **`nconst_ec`**: EquivalenceClasses carry no const flag, so `1/ref_tuples`
  double-counts when `var = const` was already pushed down.
- **MCV equality by operator** (`oprcode`) rather than by rendered text, which
  silently matches nothing across type renderings.
- Delete the `max(outer,inner)` fallback cap (M0126-0010) once the MCV arm and
  the audit show the backstop unneeded. It has no upstream counterpart, but it
  is guarded — it fires only when no key was proven and every residual factor
  was a default constant — so this is a cleanup, not a correctness fix.

### 4.6 Extended statistics

`CREATE STATISTICS` is parsed and catalogued and **never read**. Port, in
order: `BuildRelationExtStatistics` during ANALYZE (ndistinct, dependencies,
MCV), `statext_clauselist_selectivity` with its `estimatedclauses` bitmap,
`dependencies_clauselist_selectivity`, `mcv_clauselist_selectivity`,
`choose_best_statistics`, `statext_is_compatible_clause`, and
`estimate_multivariate_ndistinct` for GROUP BY. This is the largest single
piece of new subsystem in Phase 1 and may be split into its own sub-phase; it
is required because correlated predicates are exactly where the independence
assumption fails, and TPC-DS is built from them.

### 4.7 Group and distinct estimation

`estimate_num_groups` exists; DISTINCT is not sized at all (rows pass through
unchanged). Wire DISTINCT and set-op outputs through it, and use
`estimate_multivariate_ndistinct` when extended stats are present.

### 4.8 Resolver consolidation

Three resolvers locate a column's statistics with divergent node-type arm lists
(`columnStatsForChild`, `columnNDistinctForChild`, `resolveBaseColumn`/
`examineJoinVar`), and at least one lacks an `*IndexScan` arm — so an
index-probed leaf carries no MCV or histogram at all. Collapse them into one
`examine_variable` analogue over a single arm list. This is a P4 sibling-rule
item and a prerequisite for trusting any selectivity measurement.

**P1 exit (09 §5):** estimate ratchet does not regress, the plan-parity mismatch
budget does not grow, no query slower than 1.2×, and the S-cold/WARM gap
narrows.

---

## 5. Phase 2 — Cost-model completeness

The cost functions are the most faithful part of goopg's planner. What is
missing is their inputs and three functions.

### 5.1 Session GUCs must reach the planner

`defaultCostParams()` hard-codes PG 18 boot values and has two production
callers, neither of which has a session in scope. Every cost GUC is therefore
**registered, settable, and inert**: `seq_page_cost`, `random_page_cost`,
`cpu_tuple_cost`, `cpu_index_tuple_cost`, `cpu_operator_cost`,
`effective_cache_size`, `work_mem`, `hash_mem_multiplier` (read nowhere),
`parallel_setup_cost`, `parallel_tuple_cost`.

Design: introduce a per-plan planner context — the `PlannerInfo`/`PlannerGlobal`
analogue — constructed once at `optimizer.Plan` entry from the session, carrying
`costParams`, the collapse limits, the GEQO parameters, the `enable_*` flags and
the parallel settings. Thread it to the seam and to `planner.go`'s second
call site. This replaces the current mixture of process-global atomics, a
catalog wrapper carrying four scan toggles, and a separately-constructed
`ParallelSettings`.

It also fixes a **soundness** problem, not only an inelegance. Six GUCs
(`enable_memoize`, `enable_nestloop_index`, `enable_hashagg`,
`enable_presorted_aggregate`, `geqo`, `geqo_threshold`) are bridged through
`registry.OnChange` into process-wide atomics, so `SET enable_hashagg = off` in
one session changes the planner for **every** session on the server — while the
plan cache neither keys on them nor bypasses for them, so the same `SET` may
also be ignored by a cached plan.

**`work_mem`'s default must be corrected in this phase too.** goopg registers
`BootVal: "512MB"` against PostgreSQL 18.3's `4096` KB — a **128×** divergence.
`costParams.workMem` is hard-wired to it, so the planner believes hash tables
fit in memory where PostgreSQL's would batch and sorts stay in memory where
PostgreSQL's would spill. This is also a standing violation of the project's own
rule that a GUC's `BootVal` equals PostgreSQL's default, and it diverges from
the *executor*, which reads `sessionWorkMem` — one level above the shared
`hashsize` package that exists to keep planner and executor in agreement.
Expect this single change to move many plans; it lands alone, with both suites
timed.

The GUC bridge design note in `internal/optimizer/parallel.go` states the
problem exactly: *"the existing GUC→planner bridge is a process-global atomic —
adequate for a boolean kill switch, wrong for a per-session integer."* The
planner context is the fix for the whole family, not only for parallelism.

Acceptance: every cost GUC demonstrably changes at least one plan
(09 §5, P2 row).

### 5.2 `enable_*` via `disabled_nodes`

PG 18 does not price disabled nodes with `disable_cost`; it counts them in
`Path.disabled_nodes` and `compare_path_costs_fuzzily` compares that count
*before* cost. goopg carries the field and never sets it, implementing
`enable_*` by **skipping the producer** instead.

Skipping the producer is not equivalent. PostgreSQL still generates the path and
still uses it when nothing else exists; goopg's `enable_seqscan=off` on a table
with no index produces no path at all. Set `DisabledNodes` in the cost functions
as upstream does and delete the producer-skipping. This makes `enable_hashjoin`,
`enable_mergejoin`, `enable_nestloop`, `enable_material`, `enable_sort`,
`enable_incremental_sort` and the rest live for the first time — they are
registered today and referenced nowhere but their own registration lists — which
is required for A/B-ing plan choices during later phases. It also lets the six
process-global bridges become ordinary per-session settings (§5.1) and removes
the `plannerScanTogglesActive` cache bypass that exists only because the toggles
are baked into the cached plan.

Also retire `enable_nestloop_index`, a goopg invention with no upstream
counterpart, once the NLI path is an ordinary nested-loop-over-parameterized-
inner path (§6.4).

### 5.3 Missing cost functions

| function | current | why it matters |
|---|---|---|
| `cost_material` | absent; `addNestLoopPath` over-charges the rescan instead | PostgreSQL's merge join decides whether to materialise the inner by comparing `mat_inner_cost` against the bare rescan; without the node the decision cannot be made |
| `cost_rescan` | absent | CTE and nested-loop inner re-execution is priced at **zero**. Combined with the missing Material node this is a systematic bias in nested-loop pricing, which is the standing NL 1-vs-25 gap's most likely cost-side contributor |
| `cost_subplan` | integer, legacy-shaped | needs `(startup, total)` in PG units and hashed-vs-not arms |
| `cost_incremental_sort` | absent (no node) | `create_incremental_sort_path`'s node type is one of the `MISSING-NODE` bar's entries |
| bounded/top-N sort | verify; if absent | `cost_sort`'s `limit_tuples` arm is the `ORDER BY … LIMIT` win named in the parallel-sort design |
| `compute_semi_anti_join_factors` | partial | semi/anti early-out in nestloop and hashjoin costing |
| `estimate_hash_bucket_stats` | absent | hash-join bucket-size and MCV-frequency skew terms |
| `mergejoinscansel` | absent | merge-join start/end selectivities; goopg's merge cost is a simplification |
| `btcostestimate` completeness | reduced to the no-index-qual forward-scan case | descent cost, `num_sa_scans` for ScalarArrayOp, the unique-index single-tuple clamp, per-tuple index qual cost |

### 5.4 Bitmap costing

`pg-plan-parity` §13.3 recorded two errors that cancel: with correlation
restored, re-applying the oracle-correct double-count removal selects 24 bitmap
scans against PG's 6, while leaving it out selects exactly 6 and covers four of
PG's five bitmap queries. Something else under-charges bitmap paths and the
double charge was compensating. The named suspects are `qpqual_cost` per tuple,
`cpu_operator_cost` per bitmap entry, and `computeBitmapPages` versus
`compute_bitmap_pages` on the `maxentries`/lossy path — and §12 records that
goopg drops lossy pages entirely, which is a correctness-shaped gap in the
model, not a calibration one.

**Both must move in one commit.** Landing the correct double-count fix alone is
a known regression (it took TPC-DS Q72 from 73 s to a timeout). Sequence:
implement lossy-page handling to match `tbm_calculate_entries`, then remove the
double charge, then measure — one commit, both suites, all plans timed.

**P2 exit:** every cost GUC changes a plan; parity budget does not grow; no
query slower than 1.2×.

---

## 6. Phase 3 — Search coverage

The structural phase. Target: every FROM clause PostgreSQL searches, goopg
searches, with the same legality rules.

### 6.1 `deconstruct_jointree` and `SpecialJoinInfo`

Port PostgreSQL's jointree deconstruction properly:

- `deconstruct_jointree` producing the joinlist with PG16+'s outer-join relids
  representation;
- `make_outerjoininfo` building a real `SpecialJoinInfo` per outer join
  (`jointype`, `min_lefthand`, `min_righthand`, `syn_lefthand`,
  `syn_righthand`, `lhs_strict`, `semi_can_hash`, `semi_operators`,
  `semi_rhs_exprs`);
- `distribute_qual_to_rels` with the outer-join delay rules
  (`check_outerjoin_delay`), placing each qual at the right level rather than
  copying it down;
- `join_is_legal` consulting those `SpecialJoinInfo`s so the DP can build
  outer, semi and anti joinrels directly.

This deletes `splitOuterSpine`, deletes the peel, and deletes the
`pinnedOuter()` whole-statement decline. A FROM list mixing comma joins with
`LEFT JOIN` becomes **one** search problem, which is precisely what TPC-DS Q72
needs and what `join_collapse_limit` is supposed to govern.

goopg has the beginnings of this: `specialjoin.go`, `joinIsLegal`,
`hasJoinRestriction`. M0128 landed a v1. What is missing is that special joins
are still pinned out of the search, so those functions describe a search space
nothing enters.

### 6.2 Collapse limits on by default

`GOOPG_PGSHAPED_COLLAPSE` gates the PG-shaped joinlist deconstruction and is
**off**, which — per §2.2 — is why no join order is searched for explicit
`JOIN` syntax at all. With §6.1 in place it becomes the only jointree path and
the flag retires. `from_collapse_limit` and `join_collapse_limit` then have
their upstream meaning, including `join_collapse_limit = 1` preserving explicit
JOIN order for users who want it.

**How much the flag itself owns is small, and the repo pins it.**
`TestCollapseIsAControlOnTheTPCHCorpus` asserts **0 of 22** TPC-H queries are
collapse-eligible and `TestCollapseEligibilityOfTheTPCDSCorpus` pins TPC-DS to
**{Q72, Q75} of 99** — because a flat comma list is one problem either way, and
once an outer join has pinned and folded its sides into a single joinlist item,
an inner `JOIN` beside it is a two-member problem in both regimes. The flip is
therefore scheduled in **Phase 0 as the parity instrument's positive control**
(TODO P0-13): a change with a pre-registered, tiny, named blast radius is
exactly what proves the instrument measures what it claims. The structural work
is §6.1, which removes the *unconditional* outer-join pin that no flag controls.

Note also that the flip was recorded as a NO-GO in
`analysis/leftdeep-joins/2026-08-06-p59m-README.md`, and that
`internal/optimizer/joinsearchseam.go` records that verdict as **void** — it
was "a no-go about a flag that could not move a plan", decidable only after
P5.9-r/s. Re-opening it is legitimate; presenting it as untried would not be.

### 6.3 Query orderings into the search

`has_useful_pathkeys` implements only the `joininfo`/`has_eclass_joins` arm, so
`root->query_pathkeys` and `root->group_pathkeys` never cross the seam and an
index ordering can never be motivated by the query's ORDER BY or GROUP BY.
Requires the planner context (§5.1) to carry them, and `standard_qp_callback`'s
computation of `query_pathkeys`, `group_pathkeys`, `window_pathkeys`,
`distinct_pathkeys`, `sort_pathkeys`.

### 6.4 Parameterization and nested loops

- `param_source_rels` (currently hard-coded 0) so parameterized paths are
  pruned as upstream prunes them.
- `reduce_unique_semijoins`: PostgreSQL converts a unique-inner SEMI join to an
  INNER join, gaining join-order freedom that goopg never has. Blocked today by
  SEMI's left-only `Output()` re-indexing.
- Retire `rewriteJoinsToNLI`, the post-search override, once `addNLIPaths`
  covers its cases. Two routes to a nested-loop-index join means the search's
  decision is not the plan.
- The standing question — *"why does the NLI arm lose 23 of 25 times"* — is
  answered with the P0 instruments (`addPath` provenance), not by argument.
  The NL 1-vs-25 census gap is the target.

### 6.5 Relation-count ceiling

`RelSet` is `uint16`: 16 base relations per search problem
(`maxSearchRels = 16`). PostgreSQL uses a `Bitmapset` and falls back to GEQO at
`geqo_threshold` (12). Widen `RelSet` to a 64-bit word or a small bitmap type.

GEQO's own wiring is partial rather than absent: `geqo` and `geqo_threshold`
*are* bridged from the GUC registry, but as process-global atomics (§5.1), and
`geqo_effort`, `geqo_pool_size`, `geqo_generations`, `geqo_selection_bias` and
`geqo_seed` reach nothing — `geqoSearch` is called with a hard-coded effort of
5. Move all seven onto the planner context. Note that GEQO is currently
**unreachable in practice** for a second reason: while every explicit `JOIN` is
pinned (§6.2), a search problem never approaches 12 items, so §6.2 must land
first for any of this to be observable.

### 6.6 Remove the pre-search heuristic

`reorderCommaFromByCardinality` rewrites the parse tree to order FROM items by
cardinality *before* the cost-based search runs, biasing the syntactic chain the
search then sees. With §6.1 and a trustworthy cost model this is not merely
redundant, it is harmful: it makes the search's input depend on a greedy
heuristic that no upstream counterpart applies. Delete it once §6.1 lands, in
its own commit, with both suites timed.

**P3 exit:** every PG-only join spine is OFFERED at its level or has a named
reason; Q72-class queries produce one search problem; `join-order` diffs
strictly decrease.

---

## 7. Phase 4 — The upper planner as paths

Everything above the FROM clause is currently fixed rules on the node tree.
Target: `grouping_planner`'s structure — upper `RelOptInfo`s
(`UPPERREL_GROUP_AGG`, `UPPERREL_WINDOW`, `UPPERREL_DISTINCT`,
`UPPERREL_ORDERED`, `UPPERREL_FINAL`), each with a pathlist, each choosing by
cost.

- **Grouping/aggregation**: `create_grouping_paths` /
  `add_paths_to_grouping_rel` offering sorted (`AGG_SORTED` over a sorted or
  presorted input) and hashed (`AGG_HASHED`) alternatives priced by `cost_agg`
  including the hash-spill arm, plus `create_partial_grouping_paths` for the
  parallel case (Phase 5). This replaces `applyIndexOrderedGroupingRule`,
  `applyPresortedAggregateRule` and `applyEnableHashAggRule`, whose Rule 3 is
  still open.
- **Sort**: `PathSort` already exists and has a `createPlanNode` arm, but is
  only ever constructed as a merge-join child — never as an upper-rel path
  competing with a hashed alternative. Promote it to a real upper-rel path with
  `cost_sort`, and add bounded/top-N (`cost_sort`'s `limit_tuples` arm, absent
  today so a top-N sort is priced roughly 4.6× high) and
  `create_incremental_sort_path`.
- **DISTINCT**: `create_distinct_paths` (hashed vs sorted vs unique-over-sorted),
  with `estimate_num_groups` sizing it.
- **LIMIT**: `tuple_fraction` end to end. `preprocessLimit` and
  `getCheapestFractionalPath` exist; the fraction must reach every upper rel,
  not just the join search.
- **Window functions and set operations**: `create_window_paths`,
  `create_setop_paths`, priced.
- **`PathTarget`**: the upper rels need a target model —
  `make_group_input_target`, `make_window_input_target`,
  `make_sort_input_target`, `apply_scanjoin_target_to_paths`. This is also what
  finally replaces the `baseLeaf`/`baseOffset` coordinate map (§9).

**P4 exit:** `aggregation-strategy` and `sort-strategy` diffs strictly decrease;
no correctness delta.

---

## 8. Phase 5 — Parallelism in the path model

Today `MaybeAddGather` walks the finished tree, finds the deepest
partial-capable subtree, and inserts a Gather. `PartialPathlist` is never
populated in production (`addPartialPath`'s only caller is `generateScanPaths`,
which itself has no production caller), `PathGather` is never constructed, and
`gatherCost` is unreachable.

Target: partial paths as upstream builds them.

- `consider_parallel` per rel (`set_rel_consider_parallel`).
- `create_plain_partial_paths` + `compute_parallel_worker`'s block-size ladder,
  honouring `min_parallel_table_scan_size`, `min_parallel_index_scan_size` and
  the `parallel_workers` reloption. goopg's `computeParallelWorkers` already
  implements the ladder; it moves into path generation.
- Parallel scan eligibility as a `consider_parallel` property rather than an
  explicit arm list. `drivingScan` admits SeqScan, BitmapHeapScan and
  `*IndexOnlyScan` (M0134-0189) but not plain `*IndexScan`, so PostgreSQL's
  Parallel Index Scan has no counterpart, and each new parallel-capable scan
  type has to be added by hand.
- `generate_useful_gather_paths` producing `Gather` and `Gather Merge`,
  including the `Gather Merge → Sort → Parallel scan` shape, priced by
  `cost_gather` / `cost_gather_merge`.
- Parallel hash join as a `parallel_aware` hash path, priced.
- Partial aggregation as paths (`create_partial_grouping_paths`), replacing
  `splitAggregate`.

**Note on a permitted divergence.** `sortPartialRootPays` currently declines
PG's `Gather Merge → Sort → Parallel scan` shape because measurement showed
leader-side sorting is faster for goopg (q16 0.9 s vs 1.6 s; q10 3.0 vs 3.4;
q13 4.8 vs 5.1). Under this design the choice becomes a cost comparison between
two generated paths rather than a hard-coded preference. If goopg's costs still
select the leader-side shape, that is a legitimate outcome under 09 §7.4 case 1
— the measurement already exists. What is not acceptable is keeping the
hard-coded decline once both paths can be generated.

**P5 exit:** `parallelism` diffs strictly decrease; the serial control arm is
unchanged.

---

## 9. Phase 6 — Consolidation and deletion

The phase that makes the sibling rule stop applying.

1. **One cardinality estimator.** Delete the legacy `estimateJoin`/`EstimateRows`
   arm and its FK/unique mirror in `joinkeyproof.go`; everything reads
   `calcJoinrelSize`. The file header calls the duplication out as a live
   hazard: *"BOTH arms now run in production… Both must move together."*
   Prerequisite: EXPLAIN takes its `rows=` from the path (P0-2), and every
   legacy consumer is gone (P4).
2. **`PathTarget` and a range table** replacing `baseLeaf`/`baseOffset`. The
   56 KB of `joinlayout.go` and the boundary assertions in `createplanroot.go`
   exist only because a `RelOptInfo` cannot say which columns it produces.
   Coordinate remapping is this project's single largest source of silent
   wrong-answer bugs (Q8 = 0 rows, Q9 = 0 rows, the M0077 chained-NLI type
   mismatch). Deleting the coordinate space deletes the bug class.
3. **Delete the overrides**: `rewriteScanInputsWithSingleTablePredicates`
   (covered by index path generation), `rewriteJoinsToNLI` (covered by
   `addNLIPaths`), `reorderCommaFromByCardinality` (covered by the DP), and the
   dead `reconcileNLILayout`.
4. **Retire the flags**: `GOOPG_PGSHAPED_DP`, `GOOPG_PGSHAPED_COLLAPSE`,
   `GOOPG_RELSIZE_FALLBACK`, `GOOPG_NLI_COSTGATE`, `GOOPG_INDEXKEY_HARVEST`,
   `GOOPG_HASH_OUTER_JOIN`, `GOOPG_INDEX_PROBE_MULT`, `enable_nestloop_index`.
   Each retirement regenerates `scripts/planner-flags.env` from
   `flaglabels.go`, per 09 §1 R5.
5. **A `setrefs` phase**, if §9.2 shows the executor still needs explicit
   column resolution after the target model lands.

**P6 exit:** byte-identical plans to the pre-deletion arm on both suites, or
every difference explained and timed.

---

## 10. Migration, kill switches, rollback

**Kill switches are temporary and each has a named retirement.** The lesson from
`GOOPG_COST_DRIVEN_JOINORDER` and `GOOPG_PGSHAPED_DP` is that a long-lived
default-off flag means two planners ship, and the off arm rots. Each phase that
changes plan selection gets one flag, default off, flipped in its own commit
after its acceptance arm, and deleted one commit later.

**Every flip is its own commit**, with the before/after plan-parity roll-up and
the timing table for every moved plan in the message (P5, P7).

**Rollback** is the flag until it is deleted, and `git revert` after. The
deletions in Phase 6 are the only irreversible steps, and each is gated on
byte-identical plans.

**Concurrency with the Ralph loop**: this bundle's work touches files a
concurrent loop may also edit. Stage by explicit pathspec, never `git add -A`,
and prefer a worktree off clean HEAD for a phase that spans many files.

---

## 11. Out of scope

Recorded so the bundle is not read as promising them. Details and evidence in
07 §6.

| item | why it matters | why not here |
|---|---|---|
| 48-byte `Datum` vs PG's 8 | the per-row tax under every "identical plan, still slower" finding (Q6: 23.40 s vs 0.99 s) | a runtime representation change, not a planner change |
| probe-seam re-materialisation | the cascade re-materialises its probe input at every level, twice, on two execution paths | executor; `analysis/cost-driven-second-try-200731` Stage 0 |
| hash skew buckets | PG partitions the build by MCV frequencies; goopg's table is flat | executor; needs P1's MCV work as an input |
| sort operator speed | `sortOp.lessRows`/`sortTailWithCTIDs` dominated q16 | executor; the *planner* side (bounded sort, incremental sort) is P4 |
| uncancellable join probe loops | `CancelRequest` is handled, the probe loop has no cancel point; SIGKILL required | executor; a measurement hazard for this work |
| TOAST on the catalog heap writer | blocks full statistics persistence | touched in P1 §4.3 only as far as statistics need |

### 11.1 In scope but not yet scheduled

Named by review as gaps in the phase plan rather than deliberate exclusions.
Each needs a TODO item before the phase it belongs to starts:

- **Grouping sets.** Twelve TPC-DS queries use them, including two of the
  slowest. `groupingsets.go` exists; how it interacts with the upper-rel path
  model of Phase 4 is unspecified here.
- **`remove_useless_joins`.** PostgreSQL removes a join to a relation whose
  columns are unused and whose key guarantees no fan-out. No goopg counterpart
  exists. It belongs in Phase 3 next to `reduce_unique_semijoins`.
- **`reduce_outer_joins`.** goopg *has* `reduce_outer_joins.go`, which this
  document does not mention. It converts an outer join to an inner join when a
  strict qual proves it safe, so it interacts directly with §6.1's
  `SpecialJoinInfo` work and with §2.2's two mechanisms — an outer join it
  converts never reaches the pin at all.
- **FROM-clause subquery pull-up.** `pull_up_subqueries` flattens a subquery in
  FROM into the parent jointree. If goopg's coverage is narrower than
  upstream's, an unflattened subquery is its own planning boundary — which
  would be a third mechanism shrinking the search alongside §2.2's two, and
  possibly a larger one. This must be measured before Phase 3 is scoped.
- **InitPlan / SubPlan / CTE alignment in the parity differ.** §3.1 of doc 09
  specifies the main plan tree; how initplans and subplans are matched between
  the two engines is unspecified, and TPC-H q17/q20/q22 and much of TPC-DS
  depend on it.
- **Generic vs custom plans** (`plan_cache_mode`) and cursor tuple fractions:
  PostgreSQL plans a prepared statement differently on the sixth execution.
  goopg's plan cache has no such notion, so a parity capture through a prepared
  statement would compare different things.

---

## 12. Risk register

| # | risk | evidence it is real | mitigation |
|---|---|---|---|
| R1 | A phase moves many plans at once and the net is negative | Round-4: ANALYZE fixed Q5 23× and broke five queries | P0 first; per-query plan diff and timing on both suites; phase flags flipped alone |
| R2 | Fixing one term exposes a second that was cancelling it | bitmap double-charge vs the under-charge (§5.4); Q72 73 s → timeout | move cancelling pairs in one commit; when a verified fix moves the system away from PG, look for the second bug |
| R3 | A wider search space finds a worse plan because the ranking is wrong | C4 binary-only: Q8 200 → 21 s, Q9 27 s → >250 s | stats and cost phases precede the search phase (§2.3) |
| R4 | Deleting a legacy pass loses a case the search does not cover | `rewriteJoinsToNLI` covers cases `addNLIPaths` may not | delete only after the parity diff shows the search reaches the same plan; byte-identical requirement in P6 |
| R5 | Coordinate remapping breaks correctness silently | Q8 = 0 rows, Q9 = 0 rows, both shipped green tests | keep `createplanroot.go`'s boundary assertions until §9.2 removes the coordinate space; value-level diff (`tpch-runner -diff`), never row counts |
| R6 | The statistics change alters every estimate at once | the NDistinct standalone fix changed 16/22 plans and regressed Q5 | land statistics items individually, each with a full plan diff; expect plan movement and time it |
| R7 | Session-scoped cost params change plan-cache semantics | the cache is keyed on SQL text + dbOid and only DDL invalidates it; `SET` and `ANALYZE` do not | make the planner context part of the cache key, or invalidate on GUC change; a test asserts `SET random_page_cost` changes the cached plan |
| R8 | Measurement noise swamps the signal | ±17 % single-run band | 09 §6; totals for suite claims, repeats or >1.2× for per-query claims |
| R9 | The work outgrows the bundle | 07 records four prior bundles, two of which stopped at design | TODO.md is one checkbox per commit; every phase lands value on its own and is independently revertible |
