# 07 — Gap analysis

What separates goopg's planner from PostgreSQL 18.3's, measured rather than
asserted, and what four previous rounds of work already established about
closing it.

Documents [01](01-pg-planning-pipeline.md), [02](02-pg-cost-model.md) and
[03](03-pg-statistics-infrastructure.md) describe the oracle;
[04](04-goopg-planning-pipeline.md), [05](05-goopg-cost-model.md) and
[06](06-goopg-statistics-infrastructure.md) describe goopg. This document is
the difference and its consequences. [08](08-target-design.md) is the response.

---

## 1. How to read this

Three warnings, because this project has been wrong about its own planner
before in each of these specific ways.

**A capability census counts labels, not capabilities.** The 2026-08 bitmap
census read `BitmapHeapScan = 0` for two full runs while bitmap paths were
already winning, because the EXPLAIN renderer had no arm for the node. Every
count in §2 comes from a source that was checked against the Go type, or is
marked otherwise.

**An absent path and an infinitely expensive path are indistinguishable
downstream.** Five consecutive cost hypotheses about TPC-H Q8 were wrong
because the question was "why is the bitmap cheaper" when the answer was "the
index producer emitted nothing at that parameterisation". Where this document
says a plan is not chosen, it distinguishes *not generated* from *generated and
lost* wherever the evidence allows, and says so when it cannot.

**Some of the prior record is stale.** `internal/planner` no longer exists (the
package is `internal/optimizer`); `MultiHashJoin` is deleted; the PG-shaped DP
is default-on; GEQO landed; index-scan correlation is persisted. Documents
written before 2026-08-07 describe a tree that is gone. Where an older analysis
is cited here it is because its *reasoning* survived, not its inventory.

---

## 2. The measured baseline

### 2.1 Time

Latest full comparison, `docs/design/not_ralph/parallel-sort/PERF.md`,
2026-08-31, commit `6c65ceb20`. Fresh server per arm through the cgroup cap
(`GOGC=100 GOMEMLIMIT=12GiB`), S-cold, one run per query, ±17 % noise band.

| suite | goopg | PG 18.3 | ratio |
|---|---:|---:|---:|
| TPC-H SF=1, 21 comparable queries | 227.0 s | 22.9 s | **9.9×** |
| TPC-DS SF=0.5, 95 comparable queries | 1173 s | 536 s | **2.2×** |

TPC-H per query, goopg ÷ PG, worst first:

| q05 | q09 | q07 | q12 | q19 | q04 | q18 | q22 | q10 | q20 |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 92.5× | 81.7× | 45.4× | 29.4× | 26.0× | 10.0× | 9.9× | 8.0× | 6.0× | 5.0× |

goopg is **faster** than PostgreSQL on q17 (0.3×), q02 (0.5×) and q11 (0.5×),
and on TPC-DS Q1 (53 s → 1 s), Q6 (69 → 16), Q11 (53 → 17), Q74 (256 → 12) and
Q81 (15 → 0). The engine is not uniformly slow; it is slow in a shape.

**q18 + q09 + q05 = 64 % of the TPC-H total.** TPC-DS is concentrated the same
way: Q14 111 s, Q72 82 s, Q23 78 s, Q88 52 s.

### 2.2 Plan shape

The pre-work census (`pg-plan-parity/DESIGN.md` §3, both engines VACUUMed and
ANALYZEd) is the only node-level goopg-vs-PG comparison on record:

| node | goopg | PG | status at HEAD |
|---|---:|---:|---|
| Index Scan, parameterised | 2 | 24 | largely closed |
| Index Scan, plain | 23 | 0 | — |
| Nested Loop | 1 | 25 | **OPEN** |
| Hash Join | 44 | 26 | mirror of the NL gap |
| Merge Join | 5 | 0 | **OPEN** |
| Index Only Scan | 0 | 3 | closed (q13, q16, q22) |
| Bitmap Heap / Index Scan | 0 | 6 | closed (q02, q08, q11, q17, q20, q21) |

Every PG node type in that table now has a goopg counterpart, generated as a
real path and selected by `addPath` rather than by fiat. What remains is
**which** plan wins: goopg still prefers a hash join where PostgreSQL nested-loops,
by a factor of 25 to 1.

Join-spine parity, the only committed goopg-vs-PG instrument
(`cmd/estimate-audit`, 2026-08-05):

| arm | `parity_violations` | `shape_mismatches` | joinrels matched |
|---|---|---|---|
| `GOOPG_PGSHAPED_DP=0` | 0 | 67 | 21 |
| `GOOPG_PGSHAPED_DP=1` (default) | 0 | **46** | 32 |

`shape_mismatches` was recorded as an upper bound, on the grounds that goopg's
EXPLAIN did not de-duplicate repeated relation names, so Q8/Q17/Q18 lost their
final-joinrel comparison to rendering rather than to planning. That
attribution needs re-testing: de-duplication **does** exist
(`internal/executor/explain_names.go`, `register()` appends `_N` from the
second occurrence of a base name), with a documented divergence from
`select_rtable_names` in how the suffixes are numbered. Whether 46 is inflated,
and by how much, is a Phase 0 measurement rather than a settled fact.

### 2.3 The gap this document is really about

There is **no node-level goopg-vs-PG plan diff over either corpus.**
`make plan-gate` and the TPC-DS `plans` channel both compare goopg against
*goopg's own* previous capture. `scripts/pg-plan-shape-diff.sh`, specified in
`leftdeep-joins/09` §4, was never created. The nightly plan-gate diffs against
a May-2026 baseline and reports 22/22 diverged every night, carrying no signal.

So the project's stated objective — emit PostgreSQL's plan — currently has no
instrument. That is why 08's Phase 0 builds one before anything else changes.

---

## 3. Structural gaps

Ordered by consequence, not by subsystem size.

### 3.1 The search covers part of the query

goopg has two planners joined at one seam. `optimizer.Plan` → `planStmt` →
`planSelect` is a rule-based rewriter over the executor's node tree; the
PG-shaped Path search is invoked from inside it at `tryJoinSearch`
(`internal/optimizer/joinsearchseam.go`) and plans only the **inner-join prefix
of the FROM tree**. Everything it declines keeps the syntactic shape.

Two separate mechanisms shrink what the search sees. The broader one is not the
outer-join peel.

**Mechanism 1 — every `JOIN` node is pinned, so an n-way join becomes n−1
two-relation problems.** `joinPinned` (`internal/optimizer/collapse.go`) returns
`true` unconditionally for LEFT/RIGHT/FULL, and `!collapseJoins` for
`JoinInner`/`JoinCross`, where `collapseJoins` reads
`GOOPG_PGSHAPED_COLLAPSE` — default **off**. A pinned node forces its own
order: `deconstructFromItem` folds an n-way chain into n−1 nested two-member
items and `makeRelFromJoinlist` calls `searchOneProblem` on **two relations at
a time**, so join order is written order and only the access method per pair is
chosen.

| FROM written as | joinlist | what the search chooses |
|---|---|---|
| `FROM a, b, c, d` (comma list) | collapsed at the top level | join order over all four — this is why TPC-H Q8 gets one true 8-way search |
| `FROM a JOIN b ON … JOIN c ON …` | n−1 nested pinned pairs | **nothing**: order is written order |

**How much of this the collapse flag actually owns is small, and the repo
already pins the number.** Two git-tracked tests measure it:
`TestCollapseIsAControlOnTheTPCHCorpus` asserts **0 of 22** TPC-H queries are
collapse-eligible, and `TestCollapseEligibilityOfTheTPCDSCorpus` pins TPC-DS to
exactly **{Q72, Q75} of 99**. The reason is compositional: a flat comma list is
one problem in either regime, and once an outer join has pinned and folded its
two sides into a single joinlist item, an inner `JOIN` above or below it is a
two-member problem in both regimes too. So flipping `GOOPG_PGSHAPED_COLLAPSE`
changes two TPC-DS queries, not a dialect.

**The dominant mechanism is therefore the outer-join pin, which no flag
controls** — LEFT/RIGHT/FULL return `true` from `joinPinned` unconditionally,
because goopg cannot yet infer the `SpecialJoinInfo` that would let the DP
reason about them. That is what §6.1 of the design targets, and it is why the
cheap flag flip is a control rather than the fix.

**Mechanism 2 — outer joins are additionally peeled, and an unpeelable one
declines the statement.** `splitOuterSpine` lifts outer joins into a prefix spine;
`extractSearchLeaves` walks INNER and CROSS only; and when a joinlist item is a
pinned outer join the peel could not lift out, `makeRelFromJoinlist`
(`internal/optimizer/relfromjoinlist.go`) returns an error that declines **the
whole statement**, not just that item. Its own comment says so:

> Refusing here is a decline of the whole statement, not of this item —
> `planJoinlistSearch`'s error makes the seam fall back to the syntactic tree,
> which still carries the outer join on its own node.

A third, narrower decline: `FROM a, b JOIN c ON …` mixes the two forms, and
`planFromClause` puts the joined item on the *right* of a CROSS link, where
`extractSearchLeaves` refuses a predicated inner link at a non-zero base
offset.

**Consequence, measured:** TPC-DS Q72's FROM clause is a **nine-relation
explicit-`JOIN` chain** containing two `LEFT OUTER JOIN`s. PostgreSQL's
`deconstruct_jointree` flattens the inner-join subtree so all are candidates at
one level. Instrumenting the production predicates on the real query file gives
**eight `searchOneProblem` calls, every one with `nitems=2`**. Join order is
therefore written order, and only the access method is chosen. A rel can be parameterised
only by its single problem-mate: `inventory` by `catalog_sales`, which binds
`inv_item_sk` — the *second* column of `inventory_pkey` — leaving the leading
column unbound, so the prefix rule correctly declines an index path. goopg scans
`inventory` (4.71 M rows) and probes `catalog_sales`; PostgreSQL does the exact
inverse. **PG's Q72 plan is unreachable at any cost setting.** Q72 is 82× PG and
the single largest TPC-DS ratio.

This is not a costing bug, and no amount of statistics work touches it. It is
also the most likely major contributor to the standing Nested Loop 1-vs-25 gap,
since a two-relation problem offers far fewer parameterisation opportunities
than an eight-relation one — and GEQO is unreachable in practice for the same
reason, since a problem never reaches `geqo_threshold` items.

**PG reference:** `deconstruct_jointree`, `make_outerjoininfo`, `join_is_legal`
(01 §4.1, §6). **Response:** 08 §6.1, TODO P3-01…P3-04.

### 3.2 Cost GUCs do not reach the planner

`defaultCostParams()` (`internal/optimizer/cost_funcs.go`) hard-codes PG 18 boot
values and has exactly two production callers —
`internal/optimizer/joinsearchseam.go` and `internal/optimizer/planner.go` —
neither of which has a session in scope. The file says so, citing its own
ledger row: *"The per-session value does not reach the planner yet: cost time
has no session in scope."*

Every cost GUC is therefore registered, settable, and **inert**:
`seq_page_cost`, `random_page_cost`, `cpu_tuple_cost`, `cpu_index_tuple_cost`,
`cpu_operator_cost`, `effective_cache_size`, `work_mem`,
`parallel_setup_cost`, `parallel_tuple_cost`. `hash_mem_multiplier` is
registered and read nowhere at all.

**Consequence:** the cost model cannot be tuned, A/B-ed, or even tested through
its documented interface. An operator lifting a tuned `postgresql.conf` onto
goopg gets none of it. And any investigation that would normally start by
moving `random_page_cost` to see which way a plan flips has no such probe.

**Response:** 08 §5.1, TODO P2-01…P2-04.

### 3.3 EXPLAIN shows no costs

`internal/executor/operators_explain.go` emits a literal
`(cost=0.00..0.00 rows=%d width=0)` at two sites, and the `rows=` it prints
comes from the **legacy** estimator `optimizer.EstimateRows`, not from the
`Path` the search selected.

**Consequence:** three things become impossible at once. A cost-model change
cannot be observed except through which plan wins. `make plan-diff`'s
`semantic-cost` mode compares zeros. And a reviewer cannot separate a cost bug
from a candidate-generation bug — the exact confusion that cost five wrong
hypotheses on Q8.

**Response:** 08 §3 P0-2, TODO P0-02/03.

### 3.4 `enable_*` is implemented by skipping producers

PG 18 counts disabled nodes in `Path.disabled_nodes` and compares that count
before cost in `compare_path_costs_fuzzily`. goopg carries the field and never
sets it; `enable_*` is implemented by **not running the path producer**
(`internal/optimizer/pathindexordered.go`, whose comment calls this "this
seam's stand-in for upstream's disabled-node pricing").

The two are not equivalent. PostgreSQL still generates the disabled path and
uses it when nothing else exists; goopg's `enable_seqscan=off` on a table with
no usable index produces no path at all.

Worse, most of the family is not wired anywhere. Checking for a second,
non-declaration reference:

| GUC | consumed? | where |
|---|---|---|
| `enable_seqscan`, `enable_indexscan`, `enable_indexonlyscan`, `enable_bitmapscan` | **yes, per session** | `internal/postmaster/dispatch.go` → catalog wrapper → producer skip |
| `enable_memoize`, `enable_nestloop_index`, `enable_hashagg`, `enable_presorted_aggregate`, `geqo`, `geqo_threshold` | **yes, but process-global** | `cmd/goopg/main.go` `registry.OnChange` → a process-wide atomic |
| `enable_hashjoin`, `enable_mergejoin`, `enable_nestloop`, `enable_material`, `enable_sort`, `enable_incremental_sort`, `enable_gathermerge`, `enable_partition_pruning`, `enable_async_append`, `enable_parallel_hash`, … | **no** | a registration list in `internal/utils/misc/defaults.go` plus a `pg_settings` row in `internal/catalog/catalog.go`, and nothing else |

The six bridged ones go through a **process-global atomic**, which the
parallel-query design note already identifies as the wrong shape: *"adequate
for a boolean kill switch, wrong for a per-session integer."*

This is a soundness problem, not only an inelegance. `SET enable_hashagg = off`
in one session changes the planner for **every** session on the server. And the
plan cache — keyed on SQL text plus database OID, invalidated only by DDL —
neither keys on these values nor bypasses for them: only the four scan toggles
trigger the `plannerScanTogglesActive` bypass. So a `SET` can be simultaneously
too broad (it crosses sessions) and ineffective (a cached plan ignores it).

`enable_nestloop_index` is itself a goopg invention with no upstream
counterpart.

**Response:** 08 §5.2, TODO P2-05.

### 3.5 Two cardinality estimators run in production

`internal/optimizer/cardinality.go`'s `estimateJoin` (over `*Join` plan nodes)
and `internal/optimizer/joinrelsize.go`'s `calcJoinrelSize` (over `RelOptInfo`
and `restrictInfo`) both run, with hand-mirrored algorithms. The FK/unique-key
logic exists twice: `superkeyJoinSelectivity` for the search arm and
`joinkeyproof.go` for the legacy arm, whose header states the hazard:

> BOTH arms now run in production… The ALGORITHM below is deliberately the same
> one, arm for arm… Both must move together (the sibling-paths rule).

**Consequence:** every selectivity fix must be written twice and verified
twice, and a green unit test on one twin proves nothing about the other. This
is a standing multiplier on the cost of everything in Phase 1.

**And there is a third cost model, still live.** `joincost.go`'s
`chooseInnerJoinAlgo` — the integer-unit chooser that predates the Path
substrate — has two production callers
(`internal/optimizer/pushdown.go`, `internal/optimizer/planner.go`). Two more
estimators run in units that are not PostgreSQL's: `nliCostGateAccepts` (the
post-search NLI gate) and `estimateSubplanCostPerCall`, which returns an
integer row count rather than a `(startup, total)` pair. Phase 6's deletion
scope must include all of them, not only the second cardinality estimator.

**Response:** 08 §9.1, TODO P6-01 (after the legacy consumers are gone).

### 3.6 Parallelism is outside the cost model

`optimizer.MaybeAddGather` runs as a post-pass over the finished node tree,
after `optimizer.Plan` returns. In the Path model, `PartialPathlist` is never
populated in production — `addPartialPath`'s only caller is `generateScanPaths`
(`internal/optimizer/pathgen.go`), which itself has no production caller, since
base-rel scan paths come from `buildInitialRels` as a `PathPrebuilt`.
`PathGather` appears only in the `PathKind` enum declaration and is never
constructed, so `gatherCost` is unreachable.

**Consequence:** the number of workers, the placement of the Gather, and
whether a parallel plan is worth it at all are decided by a heuristic walk
rather than by comparing costed paths — `parallel_setup_cost` and
`parallel_tuple_cost` never enter the decision. `drivingScan` admits SeqScan,
BitmapHeapScan and — since M0134-0189 — `*IndexOnlyScan`, but **not plain
`*IndexScan`**, so PostgreSQL's Parallel Index Scan has no goopg counterpart.
Eligibility is an explicit arm list rather than a `consider_parallel` property,
so each new parallel-capable scan type must be added by hand.

**Response:** 08 §8, TODO P5-01…P5-08.

### 3.7 The upper planner is rules, not paths

Aggregation strategy, DISTINCT, window functions, set operations, sort and
LIMIT are built by fixed rules on the node tree
(`applyIndexOrderedGroupingRule`, `applyPresortedAggregateRule`,
`applyEnableHashAggRule`, and the node constructors in `planSelect`). There are
no upper `RelOptInfo`s and no upper-rel pathlists. `PathSort` is the one
exception: it has a `createPlanNode` arm and is constructed, but only as a
merge-join child, never as an upper-rel path competing with a hashed
alternative. `PathAgg`, `PathGather` and `PathGatherMerge` fall through to
`createPlanNode`'s `default:` panic and are never constructed at all.

**Consequence:** hashed versus sorted aggregation is a rule outcome, not a cost
comparison. There is no bounded (top-N) sort and no Incremental Sort node at
all — the `parallel-sort` design names bounded sort as "the largest single win
available on `ORDER BY … LIMIT` shapes". `estimate_num_groups` exists but
DISTINCT is not sized by it; rows pass through unchanged.

**Response:** 08 §7, TODO P4-01…P4-09.

### 3.8 No `PathTarget`, and a coordinate space instead of a range table

`RelOptInfo` carries `Width` (bytes) and `NCols` but no target list, and there
is no range table in the search. Instead each rel carries `baseLeaf` (what a
relid *means*) and `baseOffset` (where its columns *used to be*). The 56 KB of
`internal/optimizer/joinlayout.go` and the boundary assertions in
`createplanroot.go` exist to keep those coordinates consistent across every
reorder.

**Consequence:** this is the project's largest silent wrong-answer bug class.
TPC-H Q8 returned 0 rows from subset-key remapping; Q9 returned 0 rows from
NLI probe keys bound to a build-time outer schema that a later reorder
invalidated; M0077's chained-NLI type mismatch was the same shape. All of them
passed their tests. `RelSet` is `uint16`, so the search is capped at
**16 base relations** (`maxSearchRels = 16`).

**Response:** 08 §9.2, TODO P4-01 then P6-02.

### 3.9 Heuristics rewrite the search's input and its output

Three passes survive alongside the cost-based search:

- `reorderCommaFromByCardinality` rewrites the parse tree to order FROM items
  by cardinality **before** the search runs, biasing the chain it sees. No
  upstream counterpart.
- `rewriteScanInputsWithSingleTablePredicates` converts SeqScan to IndexScan
  **after** planning, for single-table constant-RHS equalities.
- `rewriteJoinsToNLI` converts Hash/NL joins to `NestedLoopIndexJoin` **after**
  the search, with its own cost gate (`GOOPG_NLI_COSTGATE`) — so there are two
  routes to a nested-loop-index join, one costed by `addNLIPaths` and one not.

**Consequence:** the search's decisions are not the plan.

**Response:** 08 §6.6, §9.3, TODO P3-12, P6-03/04.

### 3.10 Statistics and selectivity

Detail and fidelity verdicts are in [06](06-goopg-statistics-infrastructure.md);
the gaps with benchmark consequences are:

| gap | consequence |
|---|---|
| **No index-level statistics.** `catalog.Index` carries no `relpages`/`reltuples`; ANALYZE does not visit indexes; `estimateIndexGeometry` synthesises pages, tuples and tree height from heap rows and declared key widths at btree default fillfactor | the page count that `cost_index` charges random I/O for is fabricated. The function's own comment names the resume point as "index-level catalog statistics, not a better formula" |
| **No MCV pairing in `eqjoinsel`.** Only the no-MCV branch exists: `(1-nullfrac1)(1-nullfrac2)/max(nd1,nd2)` | on skewed join keys goopg and PostgreSQL differ by orders of magnitude |
| **No `convert_to_scalar` for non-numeric types.** `numericValue` (`internal/optimizer/selectivity.go`) handles only the numeric family; for `date`, `timestamp`, `text`, `varchar`, `char` and `bool`, `bucketFraction` returns a **flat 0.5** | every range predicate on a date or timestamp column interpolates to the middle of its histogram bucket regardless of where the bound actually falls. Date-window predicates are the dominant restriction shape in **both** suites. PostgreSQL has `convert_string_to_scalar`, `convert_timevalue_to_scalar` and the network variants |
| **No `clauselist_selectivity`.** The AND product is inlined, so there is no `RangeQueryClause` pairing and no extended-stats consultation | `x > a AND x < b` is estimated as two independent inequalities. Compounded with the row above, a TPC-H Q6-shaped one-year date window is priced at roughly 0.31 against a true ~0.14 |
| **`IS NULL` / `IS NOT NULL` has no selectivity arm at all** — it falls through to the generic default | `NullFrac` is collected by ANALYZE, persisted, and never read for the one clause type it exists to answer |
| **No `patternsel`.** LIKE has an access-path prefix rewrite but no selectivity estimator; no `booltestsel`, no `rowcomparesel`, no `var_eq_non_const` | |
| **The MCV admission rule is a 1.25× margin**, which is not 18.3's rule | goopg over-admits MCV entries on near-uniform columns, which then displace histogram bounds |
| **No extended statistics consumption at all.** `CREATE STATISTICS` is parsed, catalogued, WAL-logged, exposed through `pg_statistic_ext` — and never read by the planner | correlated predicates fall back to independence, which is exactly where TPC-DS lives |
| **Outer-join row floors exist only on the legacy arm.** `outerJoinRowFloor` (`internal/optimizer/cardinality.go`) implements them for `estimateJoin`; the search's `calcJoinrelSize` has no join-type branch at all | the arm that actually chooses the plan sizes an outer join as an inner join. See the `calcJoinrelSize` row below — this is the same defect seen from the statistics side |
| **`pg_statistic` rows over one page are silently dropped** (no TOAST in the catalog heap writer) | measured 2026-07-30: `orders`, `customer` and `partsupp` lost trailing-column rows *and* their size row. Wide-text columns like `ps_comment` have no histogram, so every range estimate on them defaults |
| **VACUUM's relstats are not durable** — updated in memory, while the durable sidecar is written by ANALYZE alone | a VACUUM-only maintenance regime leaves the planner reading stale sizes after restart |
| **Three column-stats resolvers** with divergent node-type arm lists, at least one lacking an `*IndexScan` arm | an index-probed leaf carries no MCV or histogram |
| **No `cost_material`, and no general `cost_rescan`** | nested-loop inner rescan *is* priced (`nestloopCost` takes an explicit rescan total, and `costMemoizeRescan` exists), but there is no Material path to compare against, so PostgreSQL's materialise-the-inner decision cannot be made; and CTE re-execution has no rescan cost |
| **`calcJoinrelSize` does not branch on join type at all.** The LEFT/FULL floors and the SEMI/ANTI arms exist only in the *legacy* `estimateJoin` | the search — the arm that actually chooses the plan — sizes an outer join as if it were an inner join. This is the sharpest instance of the two-estimator hazard in §3.5 |
| **`btreeIndexAMCost` ignores `loopCount`** | a parameterised index probe never amortises its index-side descent across the outer rows, so repeated probes are over-charged relative to `cost_index`'s treatment |
| **`work_mem` BootVal is 512MB against PostgreSQL's 4MB** — a **128×** divergence, and `costParams.workMem` is hard-wired to it rather than read from the session | the planner believes hash tables fit in memory where PostgreSQL's would batch, and sorts stay in memory where PostgreSQL's would spill. It also breaks the project's own rule that a GUC's `BootVal` must equal PostgreSQL's default, and it diverges from the *executor*, which reads `sessionWorkMem` — one level above the shared `hashsize` package that exists to keep the two in agreement |

### 3.11 Three claims in the older record that are now false

Doc [06](06-goopg-statistics-infrastructure.md) resolved these by reading the
code. They are recorded here because each has been repeated in design documents
and in the deferral ledger, and planning against them would waste a phase.

1. **"goopg has no Haas–Stokes n_distinct scale-up" (ledger row 777) — STALE.**
   `ndistinctEstimate` implements `compute_scalar_stats`' block branch for
   branch: `nmultiple == 0 → N`, `nmultiple == d → d`, otherwise Duj1
   `n·d/((n−f1) + f1·n/N)` clamped to `[d, N]`. Landed in `30293f788`.
   `NDistinct` is stored absolute with `NDistinctFrac` alongside, and PG's
   signed convention is reconstructed by `catalog.ColumnStats.StaDistinct()`.

   A **new** defect sits behind it: the `ALTER TABLE … SET (n_distinct = …)`
   override writes only `NDistinct`, while `StaDistinct()` tests
   `NDistinctFrac > 0.1` first — so on any column whose sampled fraction
   exceeds 10 %, the user's override is silently ignored by the planner,
   `pg_stats`, and the persisted row.

2. **"ANALYZE statistics are per-connection" — FALSE.** `SetTableStats` mutates
   the shared `*catalog.Table`, and the session catalog wrapper carries only
   scalar session state, returning the live pointer. Statistics are
   **process-wide, scoped per database**, and immediately visible to other
   sessions. What is *not* durable is narrower and more specific: SQL `ANALYZE`
   persists (`pg_statistic` heap plus the `goopg_relstats` sidecar, restored at
   startup for every database), while **VACUUM's `UpdateRelStats` and
   autoanalyze never persist at all**.

3. **"Multi-pair equality is priced on one `nd`" (ledger rows 779/781/784) —
   STALE.** Both estimator arms now fold every equi-pair: `estimateJoin` over
   `joinEquiPairs` with per-pair `pairNDistinct`, and `calcJoinrelSize` over
   `superkeyJoinSelectivity`'s residual list (M0127-P5.6-f). TPC-H Q9's
   cardinality error therefore needs a fresh diagnosis rather than this
   explanation.

A related correction: the `max(outer, inner)` fallback cap (M0126-0010) is
still a divergence with no upstream counterpart, but it is **guarded** — it
fires only when no key was proven *and* every residual factor was a default
constant, i.e. only when the estimate was admittedly a guess. It is less
dangerous than the older record implies, and removing it is a cleanup rather
than a correctness fix.

Two other items the plan-cache interacts with: **`ANALYZE` does not invalidate
cached plans** (it is planned as a `Utility` statement, and only DDL
invalidates), and **`TRUNCATE` does not reset `Table.Stats`**.

**Response:** 08 §4, TODO P1-*.

---

## 4. What was already tried

Four rounds. The value here is the negative results — they are the reason 08
does not propose them again.

### 4.1 Landed and kept

| work | outcome |
|---|---|
| **M0127 PG-shaped join search** (`docs/design/leftdeep-joins/`) | the level-wise DP with all three phases of `join_search_one_level`, default-on 2026-08-06. Acceptance: 23 plan matches, total **0.982×** (the new arm faster), worst query 1.36×, TPC-DS clean, ratchet violations 6 → 0 |
| **Multi-column hash keys** (P2.2) | `Join.HashKeys []JoinKeyPair`; deleted `reselectDegenerateHashKeys` and closed the single-key degeneracy class that made TPC-DS Q78 quadratic behind a PG-identical plan |
| **Hybrid hash spill** (P3) | `work_mem`-bounded; ended the Q21 OOM class |
| **Deletion of `MultiHashJoin`** (P6.2) | 42 files. The argument that won was not speed — it was deleting 28 `case *MultiHashJoin:` arms and the flatten-to-OID-ordered-list-and-remap round trip that generated this project's worst bugs |
| **Deletion of the subset-bitmask DP** (P6.3) | `bushy.go`, 2880 lines, plus the integer cost weights and the 12-table cap |
| **Correlation persisted** (`b48008455`) | index-scan correlation rides `pg_statistic` `stakind3`. Bitmap Heap Scan went 1 → 6, exactly PG's count; every TPC-H query same speed or faster; Q3 7.2 s → 3.6 s |
| **Bitmap and index-only paths** (M0134-0180…0189) | q02, q08, q11, q13, q16, q17, q20, q21, q22 reached PostgreSQL's node types; q08 9.1 s → 0.5 s, q11 1.0 → 0.1, q17 2.2 → 0.5, q13 6.8 → 4.2 |
| **GEQO** (`7609d15ef`) | implemented. `geqo` and `geqo_threshold` are bridged as process-global atomics (§3.4 pattern); the other five GEQO GUCs reach nothing and the effort is hard-coded to 5. It is also unreachable in practice while §3.1's collapse default keeps every search problem two relations wide |

### 4.2 Tried, measured, rejected

Each of these is a plan 08 does not make.

| attempt | result |
|---|---|
| **Cost-driven join order over the integer DP** (M0126, `GOOPG_COST_DRIVEN_JOINORDER`) | **FINAL NO-GO 2026-08-03.** Q9 cost-driven 804 s against 118 s. The remediation — a >2 M-row build penalty plus an `inner_pages` I/O charge — did not fix Q9 and took Q5 from 8.15 s to hang-class 600 s+. Threshold penalties make the search *dodge* the penalised operator by routing the intermediate through extra probe passes |
| **Multi-way hash join integrated into the DP** (`cost-model/15`) | correct and parity-green, and it **never wins on cost**. Deeper finding: the DP correctly prefers joining the filtered `part` early, which fractures the MHJ-eligible subset. Two prohibitions came from this: no new penalty multiplier on cost totals, no shape preference |
| **Runtime fusion of adjacent hash joins** (`analysis/cost-driven-second-try-200731`) | ADOPT THE GOAL, REJECT THE MECHANISM. Fusion does not touch join order, so on a bushy tree the predicate declines and the regression stands. Permanently disabled for correctness |
| **Accurate `NDistinct` as a standalone change** | changed 16 of 22 TPC-H plans, fixed none of the slow queries, and the *correct* form regressed Q5 by 42 %. The integer DP's weights had been calibrated to the saturated-NDistinct regime. Verdict: "DO NOT land standalone" |
| **`cost_index` `loop_count` arm** | 21/21 result sets byte-identical, census moved decisively toward PG, Q11 4.5× faster — and **Q2 went 2.0 s → 87.3 s**. Later resolved, but it is the canonical demonstration that a row-count gate cannot see a plan-shape regression |
| **Removing the bitmap double charge alone** (`ab8fbc334`) | oracle-correct in isolation; TPC-DS Q72 went 73 s → >400 s TIMEOUT, taking Q47 and Q69 with it. Two errors were cancelling. They must move together |
| **Delegated NLI costing inside the DP** (C4-pg-ii) | broke correctness: Q8 returned 0 rows |
| **Flipping `GOOPG_PGSHAPED_COLLAPSE` on** (`analysis/leftdeep-joins/2026-08-06-p59m-README.md`) | recorded as a **NO-GO** with a full arm pair — and that verdict was subsequently **voided**. `internal/optimizer/joinsearchseam.go` records why: it was "a no-go about a flag that could not move a plan", because at the time the seam could not act on the collapsed joinlist; P5.9-r/s made it decidable. So the flip is *re-openable*, not untried, and P0-13 re-opens it. Anyone re-reading the p59m report without the seam's note will conclude the opposite |

### 4.3 The five lessons 08 is built on

1. **Cost-term tuning alone has never fixed a query in this project.** Every
   win came from structure: the right search space, the right candidate set, or
   a missing statistic.
2. **A missing statistic masquerades as bad calibration.** Correlation was 0
   everywhere, so `csquared = 0` priced every index scan at `max_IO_cost`.
   Fixing the statistic — with no cost change — moved the bitmap count to
   PostgreSQL's exactly, and retired a recorded cost/performance "trade" as an
   artifact.
3. **Verify both candidates were generated before comparing their costs.**
4. **A row-count gate cannot catch a plan-shape regression**, by construction,
   and "no plan changed" is scoped to the suite that was run.
5. **One variable per commit, enforced by sequencing.** M0126 landed MHJ
   retirement before the order flip because one env var moved both.

---

## 5. Per-query evidence

Root causes for the queries that dominate the totals. Class is where the fix
must go, not where the symptom appears.

### 5.1 TPC-H

| query | ratio | root cause | class |
|---|---:|---|---|
| **q05** | 92.5× (37.0 s) | never targeted by the parity work. Its supplier probe flipped to a bitmap scan with timing **unchanged**, which points away from scan choice. Historically a build-side memory blowup (M0077 fixed it 600 s → 26 s; it regressed to non-completing by 2026-08-28 and is back to ~33 s) | unattributed — needs P0 instruments |
| **q09** | 81.7× (49.0 s) | **cardinality, cause now open.** The recorded chain estimate ran 1,250 → 37 M → 1.5e11 → 1.1e15 → 5.9e15 against an actual 175, and was attributed to single-`nd` pricing of a two-column join — an explanation that **§3.11 item 3 retires**, since both estimators now fold every equi-pair. Q9 must be re-measured with `estimate-audit` before a fix is designed. The second half of the diagnosis stands: even with correct counts the DP does not price building consecutive 6 M-row hash tables, and "the search-space change alone does not fix Q9" | stats (re-diagnose) + cost |
| **q07** | 45.4× (22.7 s) | never targeted; the probe-seam re-materialisation class | executor (§6) |
| **q12** | 29.4× (14.7 s) | no plan change; ANALYZE moved it Hash Join → Merge Join. goopg emits 5 merge joins where PG emits 0 | join method |
| **q19** | 26.0× (2.6 s) | the `{lineitem,part}` joinrel was **131× under** where PG is 1.0×; a missing preprocessing pass, since fixed. Plan is still `Hash Join + Seq Scan` against PG's `Index Scan + Nested Loop` | stats, then plan shape |
| **q18** | 9.9× (60.6 s) | largest absolute cost. Its estimate is 42,837× over — but **PG is 5,387× over on the same joinrel**, so it is not an estimate-parity defect. Untargeted | executor |
| **q16** | 3.0× (0.9 s) | plan *and* parallelism now match PG (`Parallel Index Only Scan using partsupp_pk`). The residual was the sort operator (34 % in `sortOp.lessRows`/`sortTailWithCTIDs`), halved by precomputed keys. What remains is unattributed and explicitly recorded as such | executor |
| **q21** | 3.5× (11.9 s) | was the only TPC-H timeout class (612 s, 14.4 GB peak). Proven **independent of both cardinality inputs** — neither relation sizes nor full stats rescued it. Needed a plan-shape fix plus hash spill | resolved |

### 5.2 TPC-DS

| query | ratio | root cause | class |
|---|---:|---|---|
| **Q72** | 82× (82 s) | **the join-order search collapses to pairwise** (§3.1). PG's plan is unreachable at any cost setting. Pre-existing, and explicitly not to be "fixed" by reverting the bitmap work | search coverage |
| **Q23** | 78× (78 s) | largest absolute cost on both engines; never targeted | unattributed |
| **Q88** | 52× (52 s) | never individually investigated | unattributed |
| **Q28** | 31× (31 s) | never individually investigated | unattributed |
| **Q14** | 6.9× (111 s) | largest absolute goopg cost; the query whose 100-vs-200-row failure killed runtime fusion | unattributed |
| **Q47** | 7× at SF0.5; **537 s vs 3.4 s at SF=1** | ~485 s of 537 s is one three-way CTE self-join. Recorded cause: the join degenerated to a single low-cardinality hash key (`i_category`, 10 distinct over 63,745 rows) where PG merge-joins on four keys at 5,667 distinct — a ~567× over-scan per probe, twice. **The single-key limitation was removed by M0127-P2.2**; Q47 must be re-measured before this row is trusted | stats/keys — **re-verify** |
| **Q78** | 8.5× (17 s) | the same single-hash-key degeneracy trap, and the incident that motivated multi-column keys. Also **superseded by P2.2**; re-verify | — **re-verify** |
| **Q75** | 13× (13 s) | qual placement: `d_year` filters left on the Hash Join instead of pushed into the two CTE Scans. Fixed (19 s → 11 s); the same firing set touched Q4, Q11, Q31, Q39, Q64, Q74 | resolved |
| **Q99** | 7× | regressed 1 s → 6 s inside the parity window and was never investigated. Recorded rather than absorbed | open |

Three TPC-DS queries (Q31, Q64, Q71) report `ERROR` inside a full sweep and
PASS standalone — the known isolation-wedge artifact, not an engine defect.
Q4 times out on PostgreSQL 18.3 itself (612 s) and is skipped on both sides.
Q36/Q70/Q86 are `SKIP_QUERYGEN`: the generator emits a `limit 100;` inside a
subquery and PostgreSQL rejects them too.

### 5.3 What the per-query evidence says as a whole

Sorting the TPC-H queries by cause rather than by ratio:

- **Search coverage**: Q72 class (TPC-DS), and probably a large share of the
  NL 1-vs-25 gap.
- **Statistics and selectivity**: q09, q19.
- **Join method / cost**: q12, and q09's second half.
- **Executor**: q05, q07, q18, q16 — and these are **q18 + q09 + q05 = 64 % of
  the TPC-H total**.

That last line is the honest summary: plan parity is necessary and will not
by itself close the TPC-H time gap. §6 says what else is needed, and 09 §7.2
states the bars accordingly.

---

## 6. Out of scope: the executor residual

Recorded here so the bundle is not read as promising time parity on plan work
alone. The decisive datum: **TPC-H Q6 runs the node-for-node PG-identical plan
and takes 23.40 s serial against PostgreSQL's 0.99 s.** No planner change
addresses that.

| item | evidence | note |
|---|---|---|
| **48-byte `Datum` against PG's 8** | the structural per-row tax under every "identical plan, still slower" finding | a runtime representation change |
| **Probe-seam re-materialisation** | the hash cascade re-materialises its probe input at every level, twice, on both of goopg's execution paths; the pooled row is never released on the legacy path that every aggregate-topped star query uses. ~18 M pool round-trips and ~2×2.3 GB of `Datum` traffic on a Q9-class query | `analysis/cost-driven-second-try-200731` Stage 0 |
| **Sort operator speed** | `sortOp.lessRows`/`sortTailWithCTIDs` dominated q16's profile | the *planner* side (bounded sort, incremental sort) is in scope as P4-04/05 |
| **Hash skew buckets** | PG partitions the build by MCV frequencies; goopg's table is flat | needs Phase 1's MCV work as input |
| **Build-side memory** | two build passes and two maps (`map[string][]Row` **and** `map[int64][]Row`), so peak build memory is ~2× | |
| **Uncancellable probe loops** | `CancelRequest` is handled but the probe loop has no cancel point; SIGKILL is required | a measurement hazard for this work: measure such arms per query, never as one stream |

Each needs a ledger row with a resume point (TODO P7-03).

---

## 7. Measurement gaps

Problems with the instruments themselves, all of which 08's Phase 0 addresses.

1. **No committed goopg-vs-PG plan diff** over either corpus (§2.3).
2. **The nightly plan-gate is broken by staleness** — baseline `m0077-final`,
   May 2026; 22/22 diverge nightly; no signal.
3. **`make plan-gate` silently exits 0** unless `PATH` is set and
   `PLAN_DB`/`PLAN_USER` are overridden.
4. **The TPC-DS PG oracle is integer-second and a month stale.** Only ~12 of 95
   queries have a PG time ≥ 1 s, so the TPC-DS ratio column is meaningful for a
   dozen queries at most.
5. **TPC-DS row anchors are inert**: `ci/batch/lib/summarize.py` reads
   `r["rows"]`, the CSV column is `expected_rows`.
6. **The SF0.5 gate has weak spots**: 12 queries have 0-row oracles (0 == 0
   passes trivially), 42 of 99 are count-only, and TIMEOUTs are reported rather
   than enforced.
7. **Every headline ratio is a single run** on a shared workstation, in a
   **±17 %** band measured from a proven-identical-plan pair.
7b. **The two TPC-H clusters are not configured alike.** PostgreSQL's
   `postgresql.conf` sets `work_mem = 64MB` and `effective_cache_size = 2GB`
   explicitly; goopg's leaves both at boot defaults — 512MB and 4GB. The
   headline 9.9× is therefore measured with goopg holding an **8× `work_mem`
   advantage**, so the real gap is wider than reported, and any
   `work_mem`-sensitive cost comparison between the engines is meaningless
   (09 §6.4). This has not been recorded before.
8. **The TPC-H gate runs S-cold against an ANALYZEd PostgreSQL** — a systematic
   bias in goopg's disfavour, and an open question inherited from
   `pg-plan-parity/TODO.md`. Warm is 15 % faster at the shipped default.
9. **PostgreSQL has never been re-swept** since the goopg re-sweep, so the §2.2
   census's two halves come from different captures.
10. **EXPLAIN's relation-name numbering diverges from `select_rtable_names`**
    (§2.2). De-duplication exists; the suffix scheme is not verified equal, and
    the `shape_mismatches = 46` figure was attributed to its absence.
11. **`GOOPG_INDEX_PROBE_MULT` is absent from the generated
    `scripts/planner-flags.env`**, so a benchmark artifact cannot state which
    value of it was measured — the one plan-shaping knob the provenance
    mechanism does not cover.
12. **A plain `EXPLAIN` and an `EXPLAIN ANALYZE` can print different `rows=`
    for the same filtered scan**: the plain walker takes the estimate from
    `attachedFilterNode` and the ANALYZE walker from the node itself. Any
    parity capture must fix one mode and stay in it.

---

## 8. Constraint inventory

What the tree carries today that constrains the design. Flags from
`scripts/planner-flags.env`, which is generated from
`internal/optimizer/flaglabels.go` and stamped into every benchmark artifact —
because a hand-typed flag label shipped wrong twice, the second time
mis-stamping the acceptance run of its own default flip.

| flag | default | fate under 08 |
|---|---|---|
| `GOOPG_PGSHAPED_DP` | on | retire (P6-06) — it gates the only planner |
| `GOOPG_PGSHAPED_COLLAPSE` | **off** | on by default, then retire (P3-05). Not a minor flag: while it is off, every explicit `JOIN` is pinned and no join order is searched for that syntax (§3.1) |
| `GOOPG_RELSIZE_FALLBACK` | 2 | unconditional, then retire (P1-05) |
| `GOOPG_MEMOIZE` | on | keep, bridged to `enable_memoize` |
| `GOOPG_PARALLEL` | on | keep as a kill switch |
| `GOOPG_UNNEST_PREDP` | on | keep |
| `GOOPG_EXISTS_TO_ANY` | on | keep |
| `GOOPG_INDEXKEY_HARVEST` | on | retire with P3-07 |
| `GOOPG_NLI_COSTGATE` | current | retire with `rewriteJoinsToNLI` (P6-04) |
| `GOOPG_HASH_OUTER_JOIN` | off | retire — outer joins enter the DP (P3-03) |
| `GOOPG_INDEX_PROBE_MULT` | 1.0 | retire (P6-06); stays at 1.0 meanwhile. **Not present in the generated `planner-flags.env`** — add it there first (P0), or artifacts cannot state the arm |
| `GOOPG_PGSHAPED_DP_TRACE`, `GOOPG_NLI_COSTGATE_DEBUG` | off | diagnostic, keep |
| `enable_nestloop_index` (GUC) | on | retire — no upstream counterpart (P2-05) |

Structural constraints: `RelSet uint16` caps the search at 16 base relations;
the plan cache is keyed on SQL text plus database OID and invalidated only by
DDL; `goopg_relstats` is a goopg-private sidecar invisible to a PostgreSQL
standby; `pg_class` is virtual while `pg_attribute` and `pg_statistic` are real
heaps.

Open deferral-ledger rows that this bundle consumes: 767, 769, 771, 777, 778,
779, 781, 784, 785, 790, 794, 798, 802, 803, 852, 853, 856, 871, 933, 935, 936,
944, 1385, 1445, 1976.

---

## 9. Summary

goopg's planner is not missing PostgreSQL's machinery. It has `RelOptInfo`,
`Path`, `addPath` with upstream's exact dominance ordering, a three-phase
`join_search_one_level`, GEQO, parameterized paths, pathkeys, and cost
functions ported term by term from `costsize.c`.

What it is missing is **reach and inputs**:

- the search sees an inner-join prefix, not the query — and for explicit `JOIN`
  syntax it sees two relations at a time and chooses no order at all (§3.1);
- its cost functions cannot see the session (§3.2) or express a disabled node
  (§3.4), and lack `cost_material`, `cost_rescan` and the skew terms; one of
  their central inputs, `work_mem`, is 128× PostgreSQL's default (§3.10);
- its statistics lack index geometry, MCV join pairing, multi-key equality,
  extended statistics, and outer-join floors (§3.10);
- everything above the FROM clause, and all of parallelism, is decided outside
  the cost model (§3.6, §3.7);
- and none of it is visible, because EXPLAIN prints zeros (§3.3) and no
  instrument compares a goopg plan to a PostgreSQL plan (§2.3).

The response is [08](08-target-design.md): build the instrument, fix the
inputs, widen the reach, then delete everything that plans around it.
