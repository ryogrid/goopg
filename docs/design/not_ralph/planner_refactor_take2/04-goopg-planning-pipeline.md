# 04 — goopg planning pipeline (current state, HEAD 2026-09-02)

Scope: what goopg's planner actually does today, section-by-section against
doc `01-pg-planning-pipeline.md`. Section numbers mirror doc 01 so a later
gap-analysis document can diff them one to one. Every claim carries a
`internal/optimizer/<file>.go:<Symbol>` citation (or another package's path
where the code lives outside the optimizer). Line numbers are given only where
they were re-verified at HEAD; symbol names are the stable citation.

Branch: `review-bug-fix`. Package `internal/optimizer` has 91 non-test `.go`
files.

Notation: "the search" = the PG-shaped Path/RelOptInfo join search; "the legacy
tree" = the `optimizer.Node` executor-operator tree that `planSelect` builds and
rewrites. "UNVERIFIED" marks anything I could not confirm from code at HEAD.

---

## 0. Executive summary — two planners, one seam

goopg has **two planners stacked on one another**, joined at exactly one
function.

1. **The legacy single-shot rewriter.** `optimizer.Plan` (`planner.go:89`) →
   `planStmt` (`planner.go:142`) → `planSelect` (`planner.go:741`) builds
   exactly one `optimizer.Node` tree through a fixed sequence of greedy rewrite
   passes. This is the spine: every statement goes through it, and it owns
   everything above the FROM clause — aggregation, DISTINCT, window functions,
   set operations, ORDER BY, LIMIT, subplan lowering, locking.

2. **A PG-faithful Path/RelOptInfo join search**, spliced into `planSelect` at
   one seam. It has real `Path` / `RelOptInfo` (`path.go`), `add_path` /
   `set_cheapest` (`path.go:addPath`, `path.go:setCheapest`),
   `join_search_one_level` with all three phases
   (`joinsearchlevel.go:joinSearchOneLevel`), GEQO (`geqo.go:geqoSearch`),
   `costsize.c`-derived cost functions (`cost_funcs.go`, `costindex.go`,
   `costbitmap.go`), parameterized paths (`pathparam.go`,
   `pathparamindex.go`), pathkeys (`pathkeys.go`), and a `create_plan`
   (`createplan.go:createPlanNode`).

**The seam.** `tryJoinSearch(node, pred, ctx, cat)`
(`joinsearchseam.go:168`) is the only production door into the search. Its body
is three lines: call `tryPGShapedJoinSearch`; if `used`, return the searched
tree and the residual predicate; otherwise return `(node, pred)` untouched. A
decline is therefore invisible — the statement silently keeps its syntactic
FROM order and the legacy rewrites act on it.

`tryPGShapedJoinSearch` (`joinsearchseam.go:182`) gates, in order:

| # | Gate | Decline trace label |
|---|---|---|
| 1 | `pgShapedDPEnabled()` (`joinsearch.go:pgShapedDPEnabled`, `GOOPG_PGSHAPED_DP != "0"`, default ON), `node != nil`, `ctx != nil` | (silent) |
| 2 | `2 <= len(ctx.bindings) <= maxSearchRels` (16) and `len(ctx.joinlist) > 0` | `size-or-no-joinlist` |
| 3 | `splitOuterSpine(node, ctx.joinlist)` must succeed | `outer-spine` |
| 4 | prefix width `>= 2` unless a spine was peeled | `prefix-size` |
| 5 | `jl.leafRange()` must be exactly `[0, nprefix)` | `prefix-not-a-prefix` |
| 6 | `extractSearchLeaves(chain)` must flatten the chain | `chain-not-flattenable` |
| 7 | `len(scans) == nprefix` | `leaf-count` |
| 8 | `!chainCarriesLateral(chain)` | `lateral` |
| 9 | per-leaf `ctx.bindings[i].offset == cumOffsets[i]` | `offset-disagreement` |
| 10 | spine's first relation begins where the prefix ends | `spine-offset-disagreement` |
| 11 | after the search: `len(searched.Output()) == len(low.Left.Output())` | `spine-width` |

Traces are emitted only under `GOOPG_PGSHAPED_DP_TRACE=1`
(`joinsearchtrace.go:45`, `traceSeamDecline`).

`splitOuterSpine` (`joinsearchseam.go:446`) peels LEFT/RIGHT links off the top
of BOTH representations (plan tree and joinlist) and requires them to agree
link-for-link (`spineLinkSearchable`). FULL, SEMI and ANTI are refused. If the
peeled spine contains any non-LEFT link, `prefixNullable` is true and the whole
`WHERE` is held above the spine as residual — goopg's spelling of
`check_outerjoin_delay`.

`extractSearchLeaves` (`joinsearchseam.go:551`) descends **only** `JoinTypeCross`
and `JoinTypeInner` links. Anything else — including a `LEFT` join sitting
*below* an inner/cross link — is appended to `scans` as an opaque leaf with its
full output width. An INNER link that carries a predicate is accepted only when
its coordinate origin `base == 0`, i.e. only when it is on the left spine of the
chain; otherwise the walk returns `ok = false`.

**Why a mixed FROM list decomposes into pairwise problems (TPC-DS Q72).**
This is the single most consequential structural fact, and it is a
default-flag consequence, not a bug:

- `joinPinned(t, collapseJoins)` (`collapse.go:429`) returns `!collapseJoins`
  for `JoinInner` / `JoinCross`, and `true` for everything else.
- `collapseJoins` is `pgShapedCollapseEnabled()` (`collapse.go:157`), which is
  `os.Getenv("GOOPG_PGSHAPED_COLLAPSE") == "1"` — **default off**
  (`scripts/planner-flags.env`: `GOOPG_PGSHAPED_COLLAPSE='unset(off)'`).
- Therefore **every explicit `JOIN` node in the FROM clause is pinned**, inner
  ones included. `deconstructFromItem` (`collapse.go:398`) folds a left-deep
  `a JOIN b JOIN c JOIN …` chain into nested two-member `pinnedItem`s
  (`collapse.go:pinnedItem`), one per link.
- `makeRelFromJoinlist` (`relfromjoinlist.go:257`) recurses on each such
  sub-joinlist. A pinned **inner** item is *not* refused (`pinnedOuter()` is
  false for `JoinInner`/`JoinCross`, `collapse.go:224`), so it recurses into a
  two-member sub-list and calls `searchOneProblem` (`relfromjoinlist.go:336`)
  on exactly **two** items.

Net effect for an n-way explicit-JOIN chain: **n−1 nested two-relation search
problems**, each of which chooses an access method and a join method but has no
join order to choose. The written SQL order is the plan order.

TPC-DS Q72 (`bench/tpcds/runtime_goopg/tpcds-data/queries/query72.sql`) is one
`FromExpr`: base `catalog_sales` plus 8 INNER joins plus 2 LEFT OUTER joins
(11 relations). Trace:

1. `deconstructFromItem` builds ten nested pins; the top two are LEFT.
2. `innerPrefixBelowOuterSpine` (`collapse.go:308`) peels the two LEFT pins →
   `spine = [LEFT, LEFT]`, `prefix` = the single pinned INNER item covering
   FROM items `[0,9)`.
3. `splitOuterSpine` peels the two matching `*Join` nodes; `chain` is the 9-way
   inner tree. `extractSearchLeaves` flattens it (all inner links are on the
   left spine, so `base == 0` throughout) → 9 leaves, 8 `ON` quals.
4. `planJoinlistSearch` → `makeRelFromJoinlist` recurses 8 levels deep, each
   producing one `searchOneProblem` over **two** items.

So Q72's 9-way inner join is never enumerated as a 9-relation DP problem. The
same code path also explains a second failure mode: a FROM clause mixing commas
with an explicit `ON`-join (`FROM a, b JOIN c ON …`) declines the seam outright,
because `planFromClause` puts the `b ⋈ c` node on the **right** of a CROSS link,
so `extractSearchLeaves` hits `base != 0` on a predicated inner link and returns
`ok = false` (`chain-not-flattenable`).

**Three further headline facts.**

- `EXPLAIN` never shows the cost model. `internal/executor/operators_explain.go:494`
  and `:1540` both emit a literal `(cost=0.00..0.00 rows=%d width=0)`; only
  `rows=` is real and it comes from the legacy estimator `EstimateRows`
  (`cardinality.go`), not from the chosen `Path`.
- The Path model has **no parallelism**. `addPartialPath` (`path.go:562`) has a
  single caller, `generateScanPaths` (`pathgen.go:27`), which itself has **no
  production callers** (all matches in the package are comments). Parallelism is
  a post-pass over the finished Node tree: `MaybeAddGather` (`parallel.go:88`).
- Cost GUCs are inert. `defaultCostParams()` (`cost_funcs.go:83`) has exactly two
  production callers — `joinsearchseam.go:337` and `planner.go:9241` — and
  neither threads a session; `cost_funcs.go:76-80` records the gap
  (ledger M0127-P5.7-a).

---

## 1. Entry: how the executor reaches `optimizer.Plan`

There is no `PlannedStmt` and no `PlannerInfo`. `optimizer.Plan(stmt, cat)`
returns an executor `Node` directly.

**Call sites (production).** `internal/postmaster/dispatch.go:1161` (simple
query, cache-miss path), `:3429`, `:4394`, `:834`, `:880` (PREPARE/DESCRIBE),
and `internal/postmaster/dispatch_extended.go:123` (extended protocol).

**The catalog wrapper is the only session channel into the planner.**
`sessionPlanCatalog(sess, base, dbOid)` (`dispatch.go:1799`) wraps the catalog
with:
- `WithSearchPath` (re-read per statement, so `SET search_path` takes effect),
- `DBOid` (per-database name resolution),
- `TempOwnerToken` (`"s"+UniqueID()`), for per-session temp inheritance,
- `DisableSeqScan`, `DisableIndexScan`, `DisableBitmapScan`,
  `DisableIndexOnlyScan` from the four `enable_*` scan GUCs.

`ctxPlanCatalog(ctx, base)` (`dispatch.go:1857`) is the same thing built from an
`executor.Context` instead of a session registry; it sets the same four toggles
(`dispatch.go:1886-1893`).

**Plan cache** (`internal/postmaster/plancache.go`):

| Property | Value |
|---|---|
| Structure | 16 shards × 32 entries = 512 total, FIFO eviction per shard |
| Key | `planCacheKey(sql, connDBOid)` = `NamespaceDBOid(connDBOid)` + `\x00` + `normalizeCompatSQL(sql)` (`dispatch.go:2437`) |
| Admission | doorkeeper filter: a key is cached only on its **second** `Put` (`plancache.go:admit`), 8192 slots |
| Invalidation | `Invalidate()` clears all shards; called only after a DDL statement (`dispatch.go:3618`, `dispatch_extended.go:619`) and as `ectx.OnCommitDDL` (`dispatch.go:652`) |
| Bypass | `plannerScanTogglesActive(sess)` (`dispatch.go:1786`) — any of the four scan toggles set to `off` neither reads nor writes the cache; also bypassed for temp-inheritance, pending partition detach, pending inheritance change |

`SET`, `ANALYZE` and VACUUM do **not** invalidate the cache. Cached plans embed
resolved `*catalog.Table` / `*catalog.Index` pointers, which is why the key
carries the namespace oid.

**`MaybeAddGather` placement.** The cache stores **serial** plans;
`applyParallelPostPass` (`dispatch.go:1492`) wraps every statement after the
cache lookup, so parallelism is per-session and per-execution. `MaybeAddGather`
is documented and implemented as non-mutating (`parallel.go:88`) precisely
because the cached child tree is shared across sessions.

**EXPLAIN routing.** The `*Explain` node wraps the real plan in `Child`;
`MaybeAddGather` descends through it (`parallel.go:96-106`) so `EXPLAIN` renders
the plan that would actually run. The renderer is
`internal/executor/operators_explain.go` (`walkPlan`, `walkPlanAnalyze`).

**Gap vs doc 01 §1.** No `cursor_tuple_fraction` handling (the GUC is registered
at `internal/utils/misc/defaults.go` and read nowhere), no `parallelModeOK`
computation at entry (safety is decided in the post-pass instead), no scrollable-
cursor `Material`, no JIT cost thresholds.

---

## 2. Preprocessing passes in actual call order

Doc 01 §2 describes `subquery_planner`'s fixed order. goopg's equivalent is the
body of `planSelect` plus the tail of `Plan`. Verified call order at HEAD:

### 2.1 Inside `planSelect` (`planner.go:741`)

| # | Pass (line) | File:symbol | PG analogue | Operates on |
|---|---|---|---|---|
| 1 | set-op fold (`:893`–`:970`) | `planner.go` (`applySetOp` closure) | `plan_set_operations` | parse tree, recursive `planSelect` per branch |
| 2 | `isSimpleSingle` classification (`:1008`) | `planner.go` | — (no PG counterpart) | parse tree |
| 3 | `reorderCommaFromByCardinality(s, cat)` (`:1071`) | `joinorder.go:116` | **none** — pure goopg heuristic | parse tree (on a copy of `SelectStmt`) |
| 4 | `planFromClause(stmt, cat)` (`:1078`) | `planner.go` | `add_base_rels_to_query` + jointree build | parse → Node; produces `ctx.bindings`, `root` |
| 4a | `reduceOuterJoins(s.FromExprs, s.Where, cat)` (`:2488`) | `reduce_outer_joins.go:36` | `reduce_outer_joins` | parse tree, inside `planFromClause` |
| 4b | `deconstructJointree(...)` (`:2489`) | `collapse.go:341` | `deconstruct_recurse` | parse tree → `ctx.joinlist` |
| 4c | `collectSpecialJoinInfos` (`:2490`) | `collapse.go` / `specialjoin.go:54` | `make_outerjoininfo` | joinlist → `ctx.joinInfoList` |
| 5 | `canonicalizeQual(s.Where)` (`:1119`) | `qual_canonical.go:67` | `canonicalize_qual` | parse tree (non-mutating) |
| 6a | single-table fast path (`:1122`–`:1178`): `injectLikeRangePredicates` → `planIndexScanFromWhere`; else `reduceNotNullQuals` → `Filter` | `planner.go`, `notnull_qual_reduce.go:149` | `create_index_paths` for one rel; `restriction_is_always_true/false` | parse tree + Node |
| 6b | multi-table path (`:1180`+): `resolveExpr(whereQual)` → wrap in `*Filter` | `planner.go` | `distribute_qual_to_rels` (very loosely) | Node |
| 7 | `ctx.tupleFraction = searchTupleFraction(s.Limit, s.Offset)` (`:1192`) | `joinsearchseam.go:629` | `preprocess_limit` | **unresolved parse** LIMIT/OFFSET |
| 8 | `ctx.neededCols = neededColumnNames(s)` (`:1193`) | `pathindexonlyneed.go:34` | `attr_needed` (partial: names only) | parse tree |
| 9 | **S5a branch** (`:1194`): if `unnestPreDPEnabled()` and `whereEligibleForPreDPUnnest(pred)` → `unnestSubqueriesInPlan(node)` then `runJoinSearchBelowPinned(node, origChain, ctx, cat)` | `unnest.go:424`, `predp.go:73` | `pull_up_sublinks` before `query_planner` | Node |
| 10 | **legacy branch** (`:1214`): `tryJoinSearch(f.Child, f.Predicate, ctx, cat)`; on full consumption drop the `*Filter`, else `pushPredicatesIntoCrossJoins` | `joinsearchseam.go:168`, `pushdown.go` | `make_one_rel` | Node |
| 11 | `pushOuterQualsIntoLaterals(node)` (`:1234`) | `pushdown.go` | (no direct analogue) | Node |
| 12 | **filterless outer-join tree** (`:1236`–`:1262`): when `s.Where == nil` and `joinTreeHasOuterLink(node)`, run `tryJoinSearch(node, nil, ctx, cat)` | `joinsearchseam.go` | — | Node |
| 13 | `unnestSubqueriesInPlan(node)` (`:1272`) if step 9 did not run | `unnest.go:424` | `pull_up_sublinks` | Node |
| 14 | `rewriteScanInputsWithSingleTablePredicates(node, cat)` (`:1286`) | `scan_input_rewrite.go:50` | **none** — post-hoc SeqScan→IndexScan | Node |
| 15 | `rewriteJoinsToNLI(node, cat)` (`:1293`) | `nl_index_join.go:84` | **none** — post-hoc Join→`NestedLoopIndexJoin`, own cost gate | Node |
| 16 | `remapColumnRefsAfterRewrite(node)` (`:1294`) | `joinlayout.go` | — (coordinate repair) | Node |
| 17 | `remapWithBindings(node, ctx.bindings)` (`:1298`) | `joinlayout.go:285` | — (coordinate repair) | Node |
| 18 | `pushSingleSideQualsIntoInnerJoinInputs(node)` (`:1314`) | `inner_join_qual_pushdown.go:84` | `distribute_restrictinfo_to_rels` | Node |
| 19 | `tryPromoteAggSublink(s, node, ctx, cat)` (`:1322`) | `planner.go` | — | parse + Node |
| 20 | `rewriteMinMaxAggregates(s, ctx, cat)` (`:1347`) | `planner.go:9508` | `preprocess_minmax_aggregates` | parse + Node |
| 21 | `buildAggregateStage` (`:1361`), then `remapAggExprsWithBindings` | `planner.go:7027` | `create_grouping_paths` (as a rule, not paths) | Node |
| 22 | `applyIndexOrderedGroupingRule(agg.node, cat)` (`:1387`) | `groupagg_indexorder.go:63` | `get_useful_group_keys_orderings` (sorted-input half) | Node |
| 23 | `applyPresortedAggregateRule(agg.node)` (`:1397`) | `groupagg_presorted.go:41` | `adjust_group_pathkeys_for_groupagg` | Node |
| 24 | `applyEnableHashAggRule(agg.node)` (`:1406`) | `groupagg_hashagg.go:55` | `cost_agg`'s disabled-AGG_HASHED arm | Node |
| 25 | `buildWindowStage` (`:~1507`) | `planner.go:6325` | `create_window_paths` | Node |
| 26 | ORDER BY `*Sort` (`:1566`, `:1607`) | `planner.go` | `create_ordered_paths` | Node |
| 27 | `*Limit` (`:1645`) | `planner.go` | `create_limit_path` | Node |
| 28 | `*Project` build (`:~1726`) | `planner.go` | target list application | Node |
| 29 | `*LockRows` (`:1814`) | `planner.go` | `create_lockrows_path` | Node |
| 30 | `foldPlanConstants(out)` (`:1820`) | `foldconst.go:99` | `eval_const_expressions` (late, not early) | Node |
| 31 | DISTINCT ON / DISTINCT `Unique` + implicit `Sort` (`:1831`+, `:1911`, `:1980`) | `planner.go` | `create_distinct_paths` | Node |

### 2.2 Tail of `Plan` (`planner.go:89`), after `planStmt` returns

```
pushQualsThroughSingleRefCTEs(node)     cte_inline_pushdown.go:49
node = rewriteExistsToAny(node)         exists_to_any.go:104
node = lowerSubPlanParams(node)         subplan_lower.go:112
fillJoinHashKeys(node)                  join_hash_keys.go:106
assertSearchedBoundariesIntact(node)    createplanroot.go:351
```

`reconcileNLILayout` (`joinlayout.go:1083`) lost its production call site at
M0127-P6.3 (`planner.go:92-100`); the only remaining references are its own
recursion — it is dead in production.

### 2.3 Divergences from doc 01 §2

- Order is **not** PG's. `reorderCommaFromByCardinality` runs on the parse tree
  before anything is planned, and there is no PG counterpart: PG never permutes
  the FROM list heuristically.
- Constant folding is a **final** pass (`foldPlanConstants`, step 30), not a
  preprocessing pass; PG runs `eval_const_expressions` before qual distribution.
  `FoldConstants` (`foldconst.go:21`) exists but is used on expressions, not as
  a pipeline stage.
- No `preprocess_expression` at all: no join-alias flattening, no hashed-SAOP
  conversion, no implicit-AND normalisation as a pass.
- No HAVING→WHERE migration.
- No `RTE_RESULT` removal, no UNION-ALL flattening into an appendrel, no
  subquery pull-up (`is_simple_subquery`) — a FROM-subquery is planned
  separately and enters the search as a `PathPrebuilt` leaf.
- Sublink pull-up (`unnestSubqueriesInPlan`) operates on the **resolved Node
  tree**, not the parse tree, and runs either before or after the join search
  depending on `GOOPG_UNNEST_PREDP` and `whereEligibleForPreDPUnnest`.
- `rewriteScanInputsWithSingleTablePredicates` and `rewriteJoinsToNLI` run
  **after** the cost-based search and can override its choices.

---

## 3. Upper pipeline: rules, not paths

Doc 01 §3's whole architecture — an ordered chain of upper `RelOptInfo`s, each
reading the previous rel's `pathlist` — **does not exist**. goopg builds one
node per stage and then applies rewrite rules to it.

**Verified: no upper-rel paths.** `createPlanNode` (`createplan.go:44`) has arms
for `PathPrebuilt`, `PathIndexScan`, `PathSeqScan`, `PathSort`, `PathHashJoin`,
`PathMergeJoin`, `PathNestLoop`, `PathBitmapHeapScan`, `PathBitmapIndexScan`,
`PathBitmapAnd`, `PathBitmapOr`. `PathMemoize` explicitly panics (its only legal
consumer is `createNestLoopPlan`, which unwraps it into
`NestedLoopIndexJoin.InnerMemo`). **`PathAgg`, `PathGather` and `PathGatherMerge`
have no arm** and fall into the `default:` panic — they are declared in the
`PathKind` enum (`path.go:43`) and never constructed. `PathSort` **is**
constructed, but only as a child of merge-join paths (`joinpathsmerge.go:sortPathFor`).

| Stage | goopg | PG analogue absent |
|---|---|---|
| Aggregation | `buildAggregateStage` (`planner.go:7027`) emits one `*Aggregate`; strategy then adjusted in place by three rules | `create_grouping_paths` / `add_paths_to_grouping_rel` (no sorted-vs-hashed cost tournament) |
| — index-ordered grouping | `applyIndexOrderedGroupingRule` (`groupagg_indexorder.go:63`): if some ordering of the GROUP BY keys is a leading prefix of a usable btree index, replace the child with an ascending IndexOnlyScan/IndexScan and set `AggStrategySorted` **without** a Sort. Runs first so the other two rules bail. | `get_useful_group_keys_orderings` |
| — presorted aggregates | `applyPresortedAggregateRule` (`groupagg_presorted.go:41`), gated on `enable_presorted_aggregate` | `adjust_group_pathkeys_for_groupagg` |
| — `enable_hashagg = off` | `applyEnableHashAggRule` (`groupagg_hashagg.go:55`) forces `AggStrategySorted` over a Sort | `cost_agg`'s disabled arm |
| DISTINCT | `*Unique` wrap plus an implicit `*Sort` when needed (`planner.go:~1911`, `:~1980`); DISTINCT ON validated against ORDER BY prefix | `create_distinct_paths` (no HashAggregate-vs-Unique choice) |
| Window | `buildWindowStage` (`planner.go:6325`) | `create_window_paths`, `make_window_input_target` |
| Set operations | `applySetOp` closure inside `planSelect` (`planner.go:899`), left-deep fold over segments, each branch a recursive `planSelect` | `plan_set_operations`, `generate_union_paths` |
| ORDER BY | `*Sort` (`planner.go:1566`, `:1607`) | `create_ordered_paths` (no incremental sort, no Gather Merge over partial paths) |
| LIMIT | `*Limit` (`planner.go:1645`) | `create_limit_path` |
| Locking | `*LockRows` (`planner.go:1814`) | `create_lockrows_path` |

Consequences:
- No `PathTarget` anywhere; there is no target-list model on a rel, so nothing
  corresponding to `make_group_input_target` / `make_window_input_target` /
  `make_sort_input_target` exists, and volatile/expensive expressions are never
  postponed past a Sort.
- `apply_scanjoin_target_to_paths` has no analogue: the search's chosen tree is
  spliced in and then projected by the boundary
  (`createplanroot.go:projectToBindingOrder`).
- `root->query_pathkeys` and `root->group_pathkeys` never reach the search
  (see §9).
- The LIMIT influences the search only through `ctx.tupleFraction`
  (`searchTupleFraction`, `joinsearchseam.go:629`), computed from **unresolved
  parse-level literals**; a `LIMIT $1` or `LIMIT 5+5` degrades to the 10 % punt.

---

## 4. `query_planner` analogue

### 4.1 Joinlist construction

`collapse.go`. `deconstructJointree(from, lim, collapseJoins)` (`:341`) walks the
comma-separated `[]parser.FromExpr` and applies upstream's merge rule verbatim:

```
if subMembers <= 1 || len(jl)+subMembers+remaining <= lim.fromCollapseLimit
```

`deconstructFromItem` (`:398`) handles one comma item's flat `Joins` slice as an
iteration (goopg's grammar admits no parenthesised right-hand join tree, so
`j.Right` is always a single range var). `combineJoinlists` (`:447`) is
upstream's tail of `deconstruct_recurse`'s `JoinExpr` arm.
`deconstructRangeVars(n)` (`:370`) is the JOIN-free spelling: `n` flat leaves,
limits irrelevant.

`collapseLimits` (`collapse.go:117`) / `defaultCollapseLimits()` (`:127`) hard-code
`from_collapse_limit = join_collapse_limit = 8`. **The session values never reach
here** — `Plan` takes no session (`collapse.go:107-115`), so
`SET join_collapse_limit = 1` is a no-op.

**`GOOPG_PGSHAPED_COLLAPSE` (default off).** `joinPinned` (`collapse.go:429`)
pins INNER and CROSS joins too when the flag is off. This is the §0 root cause.

### 4.2 restrictInfo

`joinrestrict.go:41`. Fields: `clause Expr`, `relids RelSet` (PG's
`required_relids`), `leftKey`/`rightKey` + `leftRelids`/`rightRelids` (the operand
split for a canonical cross-rel equality), `isEquijoin bool`, `inferred bool`
(synthesised by `inferAnchoredEqualities`), `ecID int`.

Built by `buildRestrictInfos(conjuncts, inferredCount, cumOffsets)`
(`joinrestrict.go:116`),
which resolves a clause's relset by bucketing each `ColumnRef.Index` against
`cumOffsets` (`relidsOfExpr`). A clause reaching outside the window is **declined**
— which is how nested sub-problems avoid placing a clause twice
(`relfromjoinlist.go` file header).

Absent vs PG's `RestrictInfo`: `is_pushed_down`, `outerjoin_delayed`,
`pseudoconstant`, `security_level`, `can_join`, `mergeopfamilies`,
`hashjoinoperator`, cached selectivity fields, `left_bucketsize`/`right_bucketsize`,
`num_base_rels`, `clone_group`.

### 4.3 Equivalence classes

`equiv_class.go`. A union-find over `columnIdent` (`newEquivClasses`, `find`,
`union`, `classes`), plus `inferTransitiveEqualities(conjuncts)` (`:126`) and
`inferAnchoredEqualities(conjuncts, rels)` (`:236`, with
`smallAnchorRowsThreshold`). Assigned onto restrictInfos by
`restrictInfoList.assignEquivClasses` (`joinrestrict.go:189`); consumed by
`eclassAlreadyUsed` (`pathparamindex.go:190`) to avoid generating two
parameterizations from the same class.

Absent: EC canonicalisation as an object with `ec_members` / `ec_sources` /
`ec_derives`, join domains, `ec_sortref`, `reconsider_outer_join_clauses`, broken
ECs, base implied equalities as a distinct pass, per-parameterization join
implied equalities. Pathkeys are **not** EC-based (see §9).

### 4.4 SpecialJoinInfo

`specialjoin.go:16`. Fields: `MinLefthand`, `MinRighthand`, `SynLefthand`,
`SynRighthand`, `Jointype`, `Ojrelid` (always 0 — goopg has no RT index for join
RTEs), `CommuteAboveL/R`, `CommuteBelowL/R`, `LhsStrict`, `SemiCanBtree`,
`SemiCanHash`, `SemiOperators`, `SemiRhsExprs`.

Built by `makeSpecialJoinInfo` (`specialjoin.go:54`) during deconstruction for
every non-INNER, non-CROSS link, and attached to the pinned joinlist item
(`collapse.go:~420`). Collected into `ctx.joinInfoList` and threaded through
`joinlistProblem.joinInfoList` → `buildInitialRels` → `searchCtx.joinInfoList`.

`joinIsLegal` (`joinsearchlevel.go:182`) is a genuine port of
`join_is_legal`'s scan: RHS-overlap fast path, "still building RHS", "SJ already
inside one input", the SEMI unique-ification rule, both LHS/RHS containment arms
with `reversed`, the both-inputs-overlap-RHS commutation assumption, the
LEFT-only RHS-association arm with `mustBeLeftJoin`, and the post-scan
`lhs_strict` check. `joinOrderRestricted` (`:68`) and `hasJoinRestriction`
(`:131`) are `have_join_order_restriction` analogues.

**But outer/semi/anti joins never reach it in production.** The seam peels the
outer spine (`splitOuterSpine`) before the search runs, and
`makeRelFromJoinlist` refuses a pinned outer item outright
(`relfromjoinlist.go:~265`, error `"joinlist item %d is a pinned %s join, which
the search cannot rebuild"`), which declines the whole statement back to the
syntactic tree. `makeJoinRel` (`joinsearchlevel.go:523`) documents the
consequence: "While the pin holds (03 §4.4) every searched rel is
inner-joinable, so this returns (nil, false, nil)." The reason is
representational: **the search builds only INNER joins**, so planning a pinned
outer subproblem would silently drop unmatched rows.

Semi/anti spines are handled earlier, by `runJoinSearchBelowPinned`
(`predp.go:73`), which descends below the pinned spine before the seam is
reached.

### 4.5 Lateral

`chainCarriesLateral(n)` (`joinsearchseam.go:~600`) declines any chain carrying a
LATERAL dependency, checking both spellings: `Join.Lateral` (set by
`planFromClause`/`planFromItem`) and `nodeReferencesOuter(leaf)` (a FROM-clause
SRF's outer references, which `extractSearchLeaves` would otherwise discard).
`spineLinkSearchable` (`joinsearchseam.go:~493`) applies the same test per spine
link. There is no `lateral_relids` / `lateral_referencers` model, no lateral
join-order enforcement inside the search.

### 4.6 Local filter attachment before search

Single-relation conjuncts are attached to the **leaf** before the search runs,
not carried as `baserestrictinfo`:

`partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)` (`local_filters.go:64`)
splits the conjunct list into `searchConjuncts` (multi-relation) and `locals`
(per-binding). The seam then wraps each leaf in
`&Filter{Child: scans[i], Predicate: …, LeafLocal: true}` with the predicate
re-based by `localizeExprToLeaf` (`local_filters.go:188`), and computes the
post-filter cardinality via `estimateBaseRelInfo` +
`applyRelSizeFallback` (`joinsearchseam.go:~305-325`).

Consequence: `buildInitialRels` calls `costSeqscan(cp, …, rows, 0)` with
`numQualOps = 0` — the local quals' per-tuple cost is deliberately not charged
again (`joinsearch.go:388-393`). It also means the restriction selectivity is
baked into the rel's `Rows` before any path exists, so a partial index or a
bitmap path cannot re-derive it from a clause list.

### 4.7 Absent entirely from §4

`remove_useless_groupby_columns`, `build_base_rel_tlists` / `attr_needed`,
PlaceHolderVars, `reconsider_outer_join_clauses`, `remove_useless_joins`
(left-join removal), `reduce_unique_semijoins`, `remove_useless_self_joins`
(PG18), foreign-key matching as a `root->fkey_list` pass (goopg proves FK/unique
superkeys inline in `joinrelsize.go:superkeyJoinSelectivity` and, on the legacy
arm, in `joinkeyproof.go`), `extract_restriction_or_clauses`,
`standard_qp_callback` and the whole `query_pathkeys` preference order.

---

## 5. Base rel paths

`buildInitialRels(bindings, scans, relInfos, cp, tupleFraction, joinInfoList)`
(`joinsearch.go:324`) creates one `RelOptInfo` per FROM item and gives each
**exactly one** path: a `PathPrebuilt` wrapping the pre-search leaf node
(`newPrebuiltPath`), costed with `costSeqscan(cp, estScanPages(rows, width),
rows, 0)` (`joinsearch.go:393`), then `addPath` + `setCheapest`
(`:394-395`). It also records `rel.baseLeaf` and `rel.baseOffset` (§8), `NCols`,
`AvgVarBytes`, and `ConsiderStartup = s.tupleFraction > 0`.

`generateScanPaths` (`pathgen.go:27`) — the function that would add a *real*
`PathSeqScan` plus a partial path — **has no production caller** (verified: all
package references are inside comments). Consequently `addPartialPath`
(`path.go:562`), its only caller's only use, is also dead, and
`RelOptInfo.PartialPathlist` is always empty.

`addBaseRelIndexPaths(cat)` (`pathindexordered.go:49`) is the single index-path
entry, called from `searchOneProblem` after `s.clauses` is published:

```go
indexOff := currentIndexScanDisabled(cat)
if !indexOff { s.addParameterizedIndexPaths(cat); s.addOrderedIndexPaths(cat) }
if !currentBitmapScanDisabled(cat) { s.addBaseRelBitmapPaths(cat); s.addParameterizedBitmapPaths(cat) }
if !indexOnlyScanRejected(cat) { s.addIndexOnlyPaths(cat) }
```

| Path kind | Producer | Cost function |
|---|---|---|
| Seq scan (as `PathPrebuilt`) | `buildInitialRels` (`joinsearch.go`) | `costSeqscan` (`cost_funcs.go:122`) |
| Ordered index scan | `addOrderedIndexPaths` (`pathindexordered.go:87`) → `addOneOrderedIndexPath` (`:132`) | `costIndexScan` (`costindex.go:112`) |
| Parameterized index scan | `addParameterizedIndexPaths` (`pathparamindex.go:220`) → `addOneParameterizedIndexPath` (`:306`) | `costIndexScan` with `loopCount` (`pathparamindex.go:490:loopCountFor`) |
| Index-only scan | `addIndexOnlyPaths` (`pathindexonly.go:29`) → `addOneIndexOnlyPath` (`:87`), with `indexCoversColumns` (`:132`) and `relAllVisibleFraction` (`:169`) | `costIndexScan` + `heapPagesAfterVM` |
| Bitmap index / heap | `addBaseRelBitmapPaths` (`pathbitmap.go:31`) → `buildOneBitmapPath` (`:102`), `matchBitmapIndexQuals` (`:249`) | `costBitmapIndexScan` / `costBitmapHeapScan` (`costbitmap.go`) |
| BitmapAnd | `chooseBitmapAnd` (`pathbitmap.go:319`) | `costBitmapAndCost` (`costbitmap.go`) |
| Parameterized bitmap | `addParameterizedBitmapPaths` (`pathbitmap.go:448`) → `buildOneParameterizedBitmapPath` (`:506`) | as above with `loopCount` |
| Parallel variants | **none** | — |
| TID scan | **none** | — |
| Append / MergeAppend | **none** | — |

**`enable_*` handling is producer skipping, not `disabled_nodes`.** The four scan
toggles arrive on the catalog wrapper and are read via `currentIndexScanDisabled`
/ `currentBitmapScanDisabled` / `indexOnlyScanRejected`; a disabled method simply
never produces a candidate. `Path.DisabledNodes` (`path.go:~116`) is carried in
the dominance order but is always 0.

**Consumer-side eligibility.** Every index producer gates on
`scanLeafFor(rel.baseLeaf)` (defined at `createplanindex.go:135`; called from
`pathindexordered.go:~101`, `pathparamindex.go`, `pathindexonly.go`,
`pathbitmap.go`): no path is costed over a leaf `createPlan` cannot rebuild.
This is why a `*CTEScan` or `*Values` leaf gets only its `PathPrebuilt`.

**`has_useful_pathkeys` is one arm short.** `addOrderedIndexPaths`
(`pathindexordered.go:76-85`) states the gate explicitly: only
`rel->joininfo != NIL || rel->has_eclass_joins` is implemented; `root->group_pathkeys`
and `root->query_pathkeys` "do not cross the search boundary". A rel with no
join clause produces no ordered index path at all
(`if s.clauses == nil || len(s.clauses.all) == 0 { return }`).

**Index geometry is derived, not catalogued.** `estimateIndexGeometry`
(`costindex.go:318`) computes pages/tuples/tree-height from the heap row count
and key widths at btree default fillfactor, because `catalog.Index` carries no
`relpages`/`reltuples` and ANALYZE does not visit indexes. `baseRelPages`
(`pathindexordered.go:305`) and `s.totalTablePages()` (`:282`) read live block
counts through `catalog.RelNBlocksFunc`.

Absent vs doc 01 §5: constraint exclusion / partition pruning before pathing,
`consider_param_startup` (permanently false, `path.go:~264`), subquery-RTE qual
pushdown as a path-level operation, append-rel handling, partial index
`predOK` proving (`predicate_implication.go` is a stub — deferred),
`amcanorderbyop` ordering, backward index scans as a separate candidate
(direction is carried on `Path.IndexScanDir` via `indexPathOrdering`).

---

## 6. Join search

`joinSearch(clauses, builder)` (`joinsearchlevel.go:271`) is
`standard_join_search`: levels 2..n, `joinSearchOneLevel(lev)` per level, then
`setCheapest` on every rel the level produced. On error the caller falls back to
the syntactic shape (03 §4.2) rather than failing the statement.

`joinSearchOneLevel(lev)` (`joinsearchlevel.go:328`) implements **all three
phases**:

- **Phase 1** (`tracePhaseLeftRight`): for each rel at level `lev-1`, if it has
  any relevant join clause or a join-order restriction → `makeRelsByClauseJoins`
  (`:466`) against the initial rels, with the level-2 `first = i+1` de-duplication;
  otherwise → `makeRelsByClauselessJoins` (`:491`), i.e. a Cartesian product with
  every initial rel. The branch is per **old rel**, matching `joinrels.c`.
- **Phase 2** (`tracePhaseBushy`): genuine bushy DP. For `k = 2 .. lev/2`, pair
  `levelRels(k)` with `levelRels(lev-k)`, halfway-mirror de-dup, **clause-connected
  pairs only** (no clauseless branch).
- **Phase 3**: last-ditch clauseless redo when the level came up empty; still
  empty → error → caller falls back.

`makeJoinRel(rel1, rel2)` (`joinsearchlevel.go:523`): overlap check →
`joinIsLegal` → swap on `reversed` → `buildJoinRelRestrictList` →
find-or-create the joinrel (`sizeJoinRel` fixes `Rows`/`Width` on **first**
creation only, with a `rows >= 1` floor; `NCols` and `AvgVarBytes` add;
`ConsiderStartup` copied from `tupleFraction > 0`) → **two** `addPaths` calls,
`(rel1, rel2)` and `(rel2, rel1)`, so both orientations are offered.

**GEQO.** `relfromjoinlist.go:370`:

```go
if GeqoEnabled() && len(items) >= GeqoThreshold() { s.builder = builder; return geqoSearch(s, builder, 5) }
```

`geqo.go` has `Pool`, `allocPool`, `poolSize`, `numberGenerations`, `initTour`,
`geqoSelection`, `linearRand`, `geqoEval`, `gimmeTree`, `mergeClump`.

**Correction to the code map:** `SetGeqoEnabled` / `SetGeqoThreshold`
(`geqo.go:23`, `:26`) **are** wired — `cmd/goopg/main.go:425-431` registers
`registry.OnChange("geqo", …)` and `registry.OnChange("geqo_threshold", …)`.
The bridge is **process-global** (`atomic.Bool` / `atomic.Int64`,
`geqo.go:11-20`), so the most recent `SET` in any session wins for all sessions
— not PG's per-session semantics. `geqo_effort` is hard-coded to `5` at the call
site; `geqo_pool_size`, `geqo_generations`, `geqo_selection_bias`, `geqo_seed`
are registered and unread.

Note the interaction with §0: because GEQO is keyed on `len(items)` of one
`searchOneProblem`, and the default collapse regime makes almost every problem
two items wide, GEQO is essentially unreachable on explicit-JOIN queries.

**Ceiling.** `RelSet` is `uint16` (`path.go:30`); `maxSearchRels = 16`
(`joinsearch.go:46`). `buildInitialRels` errors above it
(`joinsearch.go:182-183`), and the seam declines above it (gate 2).

**Final path.** `finalRel()` (`joinsearch.go:247`) requires exactly one rel at
level `nrels`; `finalPath()` (`joinsearch.go:273`) returns
`getCheapestFractionalPath(rel, s.tupleFraction)` (`tuplefraction.go:267`) — with
`tupleFraction == 0` this is `CheapestTotal` exactly.

**Sub-problem collapse.** `searchOneProblem` returns a `joinlistRel` whose
`node` is the tree built by `createPlanAtSearchRootRange` and whose `info` is a
deliberately table-less `baseRelInfo{bindingIdx: -1, sourceIdx: -1}`. The
enclosing problem admits it as **one `PathPrebuilt`**. The whole pathlist —
including differently-sorted and parameterized paths — is discarded at the
boundary, and the sub-problem is priced for "produce all rows" (`tupleFraction`
is 0 in every recursive `makeRelFromJoinlist` call). This is the documented,
ledgered divergence from PG, whose recursion returns a live `RelOptInfo`.

---

## 7. `addPathsToJoinrel` arms

`addPathsToJoinrel(s, joinrel, outer, inner, clauses, cp)` (`joinpaths.go:139`),
in order:

| # | Arm | Producer | Condition |
|---|---|---|---|
| 0 | `PATH_PARAM_BY_REL` refusals | `pathParamByRel` (`pathparam.go:42`) | outer parameterized by inner → `return nil` (whole direction dead). Inner parameterized by outer → skip arms 1–4, run arm 5 only. |
| 1 | `sortInnerAndOuter` | `joinpathsmerge.go:238` | `len(keys) > 0` |
| 2 | `matchUnsortedOuterMerge` | `joinpathsmergeouter.go:117` → `generateMergeJoinPaths` (`:147`) | `len(keys) > 0` |
| 3 | `addHashJoinPath` | `pathgen.go:65` | `len(keys) > 0`; **one orientation per call** — both orientations come from `makeJoinRel`'s two `addPaths` calls |
| 4 | `addNestLoopPath` | `pathgen.go:113` | always; keys rejoin residual, priced on the full cross product |
| 5 | `addNLIPaths` | `joinpathsnli.go:205` | always, unconditionally for every jointype; nested loop over a **parameterized** inner drawn from `inner.CheapestParameterized`; `getMemoizePath` (`joinpathsmemoize.go:216`) is considered here |

The arm order is load-bearing: `addPath` keeps the **incumbent** on an exact
cost tie (`addToPathlist`, `path.go:566`), so PG's arm order *is* the tie-break.
`joinpaths.go:184-197` states this explicitly.

`splitJoinClauses(outer, inner, clauses)` (`joinpaths.go:76`) partitions clauses
into `keys` (keyable equijoins, `isKeyableFor` `:96`) and `residual`. Only a
keyed operator (hash, and merge since P5.4c-i) fills `Path.HashKeys`; a plain
nested loop carries everything in `Residual`.

**Parameterization discharge.** `calcNestloopRequiredOuter(outerRelids,
outerParam, innerRelids, innerParam)` (`pathparam.go:65`) and
`calcNonNestloopRequiredOuter(outer, inner)` (`:78`).

**`paramSourceRels()` returns 0** (`joinpathsnli.go:44`) — PG's
`param_source_rels` filter (doc 01 §7 item 60) is not modelled, so a
parameterized inner is never rejected for having a parameter source outside the
current join. `allowStarSchemaJoin(outerRelids, innerParam)`
(`joinpathsnli.go:58`) and `joinClauseIsMovableInto(ri, currentRelids,
currentAndOuter)` (`:80`) are implemented.

Absent vs doc 01 §7: two-stage costing (`initial_cost_*` + `add_path_precheck`
+ `final_cost_*`) — every goopg arm costs fully then calls `addPath`;
`inner_unique` / `innerrel_is_unique` and the whole unique-join optimisation;
`Material` over the inner (`pathgen.go:105-110` records that
`addNestLoopPath` over-charges the rescan instead, a documented safe-direction
bias); partial/parallel hash join generation; `create_unique_path` for
`JOIN_UNIQUE_INNER/OUTER`; the FULL-JOIN-without-merge/hash-clause error;
partitionwise join.

---

## 8. Path / RelOptInfo structs

### `Cost` (`path.go:36`)

```go
type Cost struct { Startup, Total float64 }
```
PG units, `seq_page_cost = 1.0`.

### `PathKind` (`path.go:43`)

`PathPrebuilt`, `PathSeqScan`, `PathIndexScan`, `PathHashJoin`, `PathMergeJoin`,
`PathNestLoop`, `PathAgg`, `PathSort`, `PathGather`, `PathGatherMerge`,
`PathMemoize`, `PathBitmapIndexScan`, `PathBitmapHeapScan`, `PathBitmapAnd`,
`PathBitmapOr`.

### `Path` (`path.go:85`) — flat struct, fields verbatim

| goopg field | PG mapping |
|---|---|
| `Kind PathKind` | `Path.pathtype` (NodeTag) |
| `Rel *RelOptInfo` | `Path.parent` |
| `Cost Cost` | `startup_cost` / `total_cost` |
| `Rows float64` | `path->rows`, and **also** `ParamPathInfo.ppi_rows` for a parameterized path (documented at `path.go:~93-104`) |
| `Pathkeys []PathKey` | `Path.pathkeys` |
| `ParallelSafe bool`, `ParallelWorkers int` | `parallel_safe`, `parallel_workers` (never set to >0 in production) |
| `DisabledNodes int` | `Path.disabled_nodes` — **always 0** |
| `RequiredOuter RelSet` | `ParamPathInfo.ppi_req_outer` (the only part of `ParamPathInfo` modelled) |
| `HashKeys []*restrictInfo`, `Residual []*restrictInfo` | `HashPath.path_hashclauses` / `MergePath.path_mergeclauses` + `JoinPath.joinrestrictinfo`; **qual placement decided in path generation and therefore costed** |
| `IndexInfo *catalog.Index`, `IndexScanDir` | `IndexPath.indexinfo`, `.indexscandir` |
| `IndexClauses []indexPathClause` | `IndexPath.indexclauses`, in **index-column order** |
| `IndexOnly bool`, `IndexOnlyCovered []catalog.Column` | `create_index_path(..., indexonly=true)`; the covered list has no PG counterpart (goopg's node is narrower than the leaf) |
| `MemoizeInfo *memoizePathInfo` | `MemoizePath.est_entries` + rescan cost |
| `BitmapSelectivity float64` | `IndexPath.indexselectivity` / `BitmapAndPath.bitmapselectivity` |
| `PartialPredicate Expr` | `IndexOptInfo.indpred` (recheck side) |
| `Children []*Path` | `JoinPath.outerjoinpath`/`innerjoinpath`, `BitmapAndPath.bitmapquals`, `SortPath.subpath` |
| `node Node` | **no PG counterpart** — `PathPrebuilt` only |
| — | **absent**: `param_info` as an object, `pathtarget`, `parallel_aware` |

### `RelOptInfo` (`path.go:213`) — fields verbatim

`Relids RelSet`, `Rows float64`, `Width int`, `NCols int`, `AvgVarBytes float64`,
`Pathlist []*Path`, `PartialPathlist []*Path`, `CheapestTotal *Path`,
`CheapestStartup *Path`, `ConsiderStartup bool`, `ConsiderParamStartup bool`,
`CheapestParameterized []*Path`, `baseLeaf Node`, `baseOffset int`.

Divergences:
- **No `PathTarget`.** `Width` is bytes; `NCols` is a separate column count
  needed because a goopg hash entry is a `[]Datum` (48 B/column), not a packed
  `MinimalTuple` — `hashsize.Choose` needs both. `AvgVarBytes` is the summed
  per-column `ColumnStats.AvgWidth`.
- **No range table.** `baseLeaf` + `baseOffset` are the substitute (below).
- `ConsiderParamStartup` is permanently false (`path.go:~264-268`) because
  special joins are pinned out of the search.
- Absent: `reltarget`, `baserestrictinfo`, `joininfo`, `has_eclass_joins`,
  `lateral_relids`, `attr_needed`, `consider_parallel`, `unique_for_rels`,
  `part_scheme`, `serverid`/`fdwroutine`.

### add_path and dominance

`comparePathCostsFuzzily(p1, p2, fuzz)` (`path.go:389`) reproduces PG 18's
order: `disabled_nodes` first, then total, then startup, within
`stdFuzzFactor = 1.01` (`path.go:24`). `comparePaths(a, b)` (`path.go:520`)
folds in the pathkey dimension (`comparePathkeysDim`, `pathkeys.go:38`),
`ParallelSafe`, and `RequiredOuter` (`outerDim`). `addPath` (`path.go:555`) →
`addToPathlist` (`:566`) keeps the incumbent on an exact tie. `setCheapest`
(`path.go:661`).

`addPartialPath` (`path.go:562`) exists but is unreachable in production (§5).

### The coordinate model, and why it exists

PG addresses columns by `(varno, varattno)` through the range table, so any
reordering of the join tree is transparent. goopg's `Expr` uses a flat
`ColumnRef.Index` into the pre-search concatenation of all FROM-item schemas.
The search *reorders* that concatenation. Hence:

- `RelOptInfo.baseLeaf` records **what** a base relid means (the leaf node with
  its resolved schema and local quals); `RelOptInfo.baseOffset` records **where
  it used to be** (`rangeBinding.offset`). Both set only on level-1 rels, in
  `buildInitialRels` (`joinsearch.go:376-383`).
- `relidsOfExpr` (`joinrestrict.go`) buckets each `ColumnRef.Index` against
  `cumOffsets` to decide a clause's relset.
- `createPlanNode` threads an `outputLayout` — for each output column, the
  pre-search binding coordinate it came from — alongside the node
  (`createplan.go:44`, `createplanjoin.go:outputLayout`), so a join can re-base
  its quals onto the merged row.
- `createPlanAtSearchRootRange(p, base, width, fill)` (`createplanroot.go:108`)
  is the boundary: it asserts the searched root's layout is a **total**
  bijection onto `[base, base+width)` and panics on any hole
  (`:191`, `:195`, `:222`, `:282`) or duplicate, then emits a pass-through
  `*Project` restoring pre-search binding order (`projectToBindingOrder`) and
  tags the root (`markSearchedTree`, `searchedtree.go:107`). The one licensed
  exception is a column an index-only narrowing legitimately dropped **and**
  that is provably outside `neededCols` (`relfromjoinlist.go`'s `fill` closure).
- `assertSearchedBoundariesIntact(root)` (`createplanroot.go:351`) re-checks the
  map on the **finished** tree at the tail of `Plan`, because later passes can
  rewrite it (`:428`, `:443`, `:447`, `:454`).
- `joinlayout.go` (56 KB) exists solely to repair coordinates after the legacy
  rewrites (`remapWithBindings`, `applyJoinTreePosMap`).

This machinery has no PG counterpart and is the single largest structural tax
of the current design.

---

## 9. Pathkeys

`PathKey` (`pathkeys.go:18`) is three fields: `Expr Expr`, `SortAsc bool`,
`NullsFirst bool`. **There is no equivalence class in a pathkey** — matching is
`exprEqual` on the expression (`pathKeyEqual`, `pathkeys.go:27`). The file
header (`pathkeys.go:10-15`) records the consequence: a false negative
(a redundant Sort) is possible, a false positive is not.

Functions: `comparePathkeysDim` (`:38`), `pathkeysContainedIn(keys, required)`
(`:64`), `pathkeysForSortKeys` (`:82`), `pathkeyRedundantIn` (`:106`),
`appendPathKeys` (`:134`), `makeCandidatePathkeys(sortlist)` (`:150`),
`isPlainConst` (`:179`).

Index pathkeys: `buildIndexPathkeys(idx, colExprs, backward)`
(`pathkeysindex.go:92`), gated on `indexIsOrderable(idx)` (`:51`);
`pathkeyExprIndex` (`:131`). Direction and pathkeys are produced together by
`indexPathOrdering` (`pathindexcarrier.go`) so they cannot disagree.

**Which query orderings enter the search: none.** Verified at
`pathindexordered.go:76-85` — the `has_useful_pathkeys` gate implements only the
`joininfo`/`has_eclass_joins` arm. `root->group_pathkeys` and
`root->query_pathkeys` are explicitly named as not carried across the search
boundary. So an `ORDER BY` or `GROUP BY` ordering can never motivate an index
path inside the search; the only consumer of a query ordering is the
**post-search rule** `applyIndexOrderedGroupingRule`
(`groupagg_indexorder.go:63`), which rewrites the aggregate's child directly.

`makeCandidatePathkeys` has exactly one production caller,
`groupagg_presorted.go:69` — also a post-search rule, not a path producer.

Absent: canonical pathkey interning, `build_join_pathkeys`,
`truncate_useless_pathkeys`, incremental sort, GROUP BY key reordering to match
input order, DISTINCT reordering (PG18).

---

## 10. Parallel query

**Entirely outside the cost model.** `MaybeAddGather(root, ParallelSettings)`
(`parallel.go:88`) runs on the finished Node tree, from
`applyParallelPostPass` (`dispatch.go:1492`) and `dispatch_extended.go:155`.

`ParallelSettings` (`parallel.go:61`): `MaxWorkersPerGather`,
`MinTableScanBlocks`, `DebugParallelQuery`, `IsSerializable`,
`LeaderParticipates`, `BlocksForTable`.

Algorithm:
1. Kill switch `parallelOn` (`GOOPG_PARALLEL != "off"`, `parallel.go:43`).
2. Descend through `*Explain` (non-mutating copy) so EXPLAIN shows the executed
   plan.
3. Refuse if `MaxWorkersPerGather <= 0`, if `IsSerializable` (SSI predicate-lock
   acquisition is a real write on the scan side), or if
   `!statementIsParallelSafe(root)` (`:156` → `subtreeHasUnsafeNode` `:170`,
   `tableIsUnsafeForParallel` `:214`).
4. `findPartialSubtree(root, s)` (`:241`) locates the deepest subtree that can
   run partially, using `terminatesPartial` (`:340`) and `sortPartialRootPays`
   (`:334`).
5. `computeParallelWorkers(sized, s)` (`:588`) — PG's log₃ block ladder over
   `min_parallel_table_scan_size`, with the `parallel_workers` reloption
   override (`tableParallelWorkersReloption`, `:684`) and the
   `max_parallel_workers_per_gather` cap. Sizes come from
   `parallelRelationBlocks` (`:675`), a **live block count** via
   `BlocksForTable`, not from statistics (`dispatch.go:1516-1522` explains why:
   goopg's ANALYZE row count is not restored at startup).
6. `rebuildWithGather(root, tgt, workers)` (`:698`) emits `GatherMerge` or
   `Gather`; `stampParallelScan` (`:380`) / `drivingScan` (`:441`) mark the
   driving scan.
7. `splitAggregate(a, workers)` (`:732`) with `parallel_agg.go`:
   `AggregateIsDecomposable` (`:28`), `AggregateIsOrderSensitive` (`:96`),
   `aggregateSplitIsSafe` (`:113`), `parallelDivisor` (`:174`),
   `groupsToRowsRatio` (`:204`), `splitAggregateIsProfitable` (`:282`).
8. Parallel-shape detection helpers: `HasBitmapScan` (`:491`),
   `HasShareableHashJoin` (`:510`), `hashJoinIsPartialCapable` (`:557`) — these
   describe an already-built tree, they do not cost anything.

**`PartialPathlist` is never populated** (§5), so nothing in the Path model is
parallel. `gatherCost` (`cost_funcs.go:407`) is a faithful `cost_gather` port
with **zero callers** — verified: no reference in the package outside its own
definition. `getParallelDivisor` (`cost_funcs.go:104`) is reachable only through
`costSeqscan`'s parallel arm, which `buildInitialRels` calls with 0 workers.

Absent: `set_rel_consider_parallel`, `generate_gather_paths` /
`generate_useful_gather_paths` after each rel and join level, Gather Merge over
every ordered partial path, partial aggregation through
`UPPERREL_PARTIAL_GROUP_AGG`, `parallelModeNeeded`.

---

## 11. `create_plan`

`createPlan(p)` (`createplan.go:25`) → `createPlanNode(p)` (`:44`), returning
`(Node, outputLayout)`.

| PathKind | Node emitted | Builder |
|---|---|---|
| `PathPrebuilt` | the wrapped node, unchanged | — (`baseRelLayout`) |
| `PathIndexScan` | `*IndexScan` | `createIndexScanPlan` (`createplanindex.go`) |
| `PathSeqScan` | `*SeqScan` | `createSeqScanPlan` (`createplansimple.go`) |
| `PathSort` | `*Sort` | `createSortPlan` (`createplansimple.go`) |
| `PathHashJoin` | `*Join{Algo: Hash}` | `createHashJoinPlan` (`createplanjoin.go`) |
| `PathMergeJoin` | `*Join{Algo: Merge}`, absorbing explicit `PathSort` children | `createMergeJoinPlan` (`createplanjoin.go`) |
| `PathNestLoop` | `*Join` **or** `*NestedLoopIndexJoin`, depending on whether the inner child is parameterized | `createNestLoopPlan` (`createplannl.go`) |
| `PathMemoize` | **panics** — the cache is `NestedLoopIndexJoin.InnerMemo`, a field, not a node; only `createNestLoopIndexJoinPlan` may unwrap it | — |
| `PathBitmapHeapScan` | `*BitmapHeapScan` | `createBitmapHeapScanPlan` (`createplanbitmap.go`) |
| `PathBitmapIndexScan` | `*BitmapIndexScan` | `createBitmapIndexScanPlan` |
| `PathBitmapAnd` / `PathBitmapOr` | bitmap combinator | `buildBitmapAndOrPlan` |
| `PathAgg`, `PathGather`, `PathGatherMerge` | **no arm** — `default:` panics | — |

`createPlanAtSearchRoot(p, bindingWidth)` (`createplanroot.go:80`) is
`createPlanAtSearchRootRange(p, 0, bindingWidth, nil)` (`:108`); see §8 for the
boundary assertions.

**There is no setrefs phase.** Column references are resolved by:
1. `resolveExpr` at parse time, into the flat pre-search binding coordinate
   space;
2. the `outputLayout` threaded through `createPlanNode`, which re-bases each
   join's quals onto the merged row (`createplanjoin.go` calls itself
   "`set_join_references` at goopg's fidelity");
3. the pass-through `*Project` the search boundary emits to republish pre-search
   binding order;
4. the legacy repair passes `remapColumnRefsAfterRewrite` /
   `remapWithBindings` (`joinlayout.go`) after the post-search rewrites;
5. `assertSearchedBoundariesIntact` at the tail of `Plan` as a final check.

Absent vs doc 01 §11: `CP_*` flag propagation, `use_physical_tlist`,
`order_qual_clauses` (quals are not cost-ordered), gating `Result` for
pseudoconstant quals, `NestLoopParam` as an explicit list (correlations become
PARAM_EXEC slots via `lowerSubPlanParams`, `subplan_lower.go:112`, running over
the whole finished tree in `Plan`), `fix_indexqual_references` /
`indexqualorig` (goopg's `Path.IndexClauses` is already in index-column order),
hash-join skew table/column selection, a flat rtable with `rtoffset`,
OUTER_VAR/INNER_VAR/INDEX_VAR rewriting, trivial SubqueryScan removal,
`AlternativeSubPlan` selection, `plan_node_id` assignment, and
`copy_generic_path_info` (costs and rows are **not** copied from the path onto
the node — which is why EXPLAIN prints `cost=0.00..0.00`).

---

## 12. Planner GUCs and env flags

### 12.1 `GOOPG_*` environment variables read in `internal/optimizer`

All are read **once at process start** so a plan cannot change shape
mid-statement. Cross-checked against `scripts/planner-flags.env`, which is
generated from `internal/optimizer/flaglabels.go` and pinned by
`TestFlagProvenanceEnvIsGenerated`.

| Env var | Read at | Resolved default | Effect |
|---|---|---|---|
| `GOOPG_PGSHAPED_DP` | `joinsearch.go:68` (`pgShapedDPFromEnv`: `v != "0"`) | **on** | Master switch for the PG-shaped join search. `=0` ⇒ no join-order search at all; syntactic FROM order plus rule rewrites. |
| `GOOPG_PGSHAPED_COLLAPSE` | `collapse.go:147` (`v == "1"`) | **off** | When on, explicit INNER/CROSS `JOIN` chains flatten into the enclosing search problem. Off ⇒ every explicit JOIN is pinned (§0). |
| `GOOPG_PGSHAPED_DP_TRACE` | `joinsearchtrace.go:45` (`== "1"`) | off | Enumeration / decline provenance dump. |
| `GOOPG_PARALLEL` | `parallel.go:43` (`v != "off"`) | **on** | Gather post-pass kill switch. |
| `GOOPG_MEMOIZE` | `memoize.go:34` | **on** | Memoize attach (legacy path and `getMemoizePath`). |
| `GOOPG_UNNEST_PREDP` | `unnest.go:45` | **on** | Sublink pull-up **before** the join search (S5a), then `runJoinSearchBelowPinned`. |
| `GOOPG_EXISTS_TO_ANY` | `exists_to_any.go:79` | **on** | `convert_EXISTS_to_ANY` analogue. |
| `GOOPG_INDEXKEY_HARVEST` | `unnest.go:38` | **on** | Index-key harvesting during unnesting. |
| `GOOPG_NLI_COSTGATE` | `nl_index_join.go:55` | **current** (`legacy` selects the old gate) | Which cost gate `rewriteJoinsToNLI` applies. |
| `GOOPG_NLI_COSTGATE_DEBUG` | `nl_index_join.go:1383`, `:1448` (`== "1"`) | off | Tracing. |
| `GOOPG_HASH_OUTER_JOIN` | `joincost.go:52` | **off** | Legacy: allow hash for outer-fill joins. |
| `GOOPG_RELSIZE_FALLBACK` | `relsize.go:69` (`parseRelSizeFallbackStage`) | **2** | Stage of the block-count relsize fallback. |
| `GOOPG_INDEX_PROBE_MULT` | `cost_funcs.go:438` (`envFloatDefault`, `cost_funcs.go:13`) | **1.0** | Multiplier applied to the index probe cost in `costIndexScan`. **Not in `planner-flags.env`** — an unstamped calibration knob. |

Retired but still stamped (so old artefacts remain attributable):
`GOOPG_COST_DRIVEN_JOINORDER` (M0127-P5.9), `GOOPG_MHJ_PACKING_OFF`
(M0127-P6.2), `GOOPG_GS_SHARE_SOURCE` (M0125-0048).

Package-level setters with production callers in `cmd/goopg/main.go`:
`SetNLIEnabled` (`:399`), `SetMemoizeEnabled` (`:405`),
`SetPresortedAggEnabled` (`:411`), `SetHashAggEnabled` (`:419`),
`SetGeqoEnabled` (`:426`), `SetGeqoThreshold` (`:430`). All six are
**process-global atomics driven by `registry.OnChange`** — the last `SET` in any
session wins for every session. `SetParallelEnabled` and
`SetRelSizeFallbackStage` have no production caller.

### 12.2 Planner GUCs registered in `internal/utils/misc/defaults.go`

"Reaches planner" verified by grepping for a **second, non-declaration**
reference (the only other hit for the `No` rows is
`internal/catalog/catalog.go`'s virtual `pg_settings` row list, or
`internal/executor/reloptions_catalog.go`'s tablespace reloption table).

| GUC | BootVal | Reaches planner? | How |
|---|---|---|---|
| `enable_seqscan` | on | **Yes** | `catalog.DisableSeqScan` (`dispatch.go:1817`) |
| `enable_indexscan` | on | **Yes** | `DisableIndexScan` → `currentIndexScanDisabled` → producer skipped |
| `enable_bitmapscan` | on | **Yes** | `DisableBitmapScan` → producer skipped |
| `enable_indexonlyscan` | on | **Yes** | `DisableIndexOnlyScan` → producer skipped |
| `enable_memoize` | on | **Yes** | `OnChange` → `SetMemoizeEnabled` (process-global) |
| `enable_nestloop_index` (goopg-invented) | on | **Yes** | `OnChange` → `SetNLIEnabled` (process-global) |
| `enable_presorted_aggregate` | on | **Yes** | `OnChange` → `SetPresortedAggEnabled` |
| `enable_hashagg` | on | **Yes** | `OnChange` → `SetHashAggEnabled` |
| `geqo` | on | **Yes** | `OnChange` → `SetGeqoEnabled` (process-global, not per-session) |
| `geqo_threshold` | 12 | **Yes** | `OnChange` → `SetGeqoThreshold` (process-global) |
| `geqo_effort` | 5 | **No** | hard-coded `5` at `relfromjoinlist.go:371` |
| `geqo_pool_size`, `geqo_generations`, `geqo_selection_bias`, `geqo_seed` | 0/0/2/0 | **No** | registered only |
| `enable_hashjoin`, `enable_mergejoin`, `enable_nestloop`, `enable_sort`, `enable_material`, `enable_partition_pruning`, `enable_partitionwise_join`, `enable_partitionwise_aggregate`, `enable_parallel_hash`, `enable_parallel_append`, `enable_gathermerge`, `enable_incremental_sort`, `enable_async_append`, `enable_distinct_reordering`, `enable_group_by_reordering`, `enable_self_join_elimination`, `enable_tidscan` | on | **No** | registration-only; `SET` succeeds and is ignored |
| `seq_page_cost` 1.0, `random_page_cost` 4.0, `cpu_tuple_cost` 0.01, `cpu_index_tuple_cost` 0.005, `cpu_operator_cost` 0.0025, `effective_cache_size` 4GB, `parallel_setup_cost` 1000, `parallel_tuple_cost` 0.1 | PG 18 | **No** | `defaultCostParams()` (`cost_funcs.go:83`) hard-codes the same values; no session is in scope at cost time (`cost_funcs.go:76-80`, ledger M0127-P5.7-a) |
| `work_mem` 512MB | — | **No** for the planner | `costParams.workMem` defaults to `hashsize.DefaultMemLimitBytes`; the **executor** reads the session value (`dispatch.go:sessionWorkMem`) |
| `hash_mem_multiplier` 2.0 | 2.0 | **No** | registered and never read |
| `from_collapse_limit`, `join_collapse_limit` | 8 | **No** | `defaultCollapseLimits()` hard-codes 8; and the whole mechanism is behind `GOOPG_PGSHAPED_COLLAPSE` |
| `cursor_tuple_fraction` 0.1 | 0.1 | **No** | `searchTupleFraction` reads only the statement's own LIMIT/OFFSET |
| `constraint_exclusion` partition | — | **No** | no constraint-exclusion pass exists |
| `recursive_worktable_factor` 10 | — | **No** (UNVERIFIED whether any executor path reads it) | |
| `max_parallel_workers_per_gather` 4, `min_parallel_table_scan_size` 8MB, `parallel_leader_participation` on, `debug_parallel_query` off | PG 18 | **Yes** | → `ParallelSettings` (`dispatch.go:1496`) |
| `min_parallel_index_scan_size` 512kB, `max_parallel_workers` 8 | PG 18 | **No** in the planner (cluster/executor level) | |
| `jit`, `jit_provider`, `plan_cache_mode` | off / llvmjit / — | **No** | |

### 12.3 Plan-cache bypass

`plannerScanTogglesActive(sess)` (`dispatch.go:1786`) checks the four scan
toggles and, if any is `off`, the session neither reads nor writes the shared
plan cache. Necessary because the cache keys only on `(dbOid, normalized SQL)`
while those four GUCs are now real planner input (review/260831-2 X-8). The
other planner GUCs that *do* reach the planner (`enable_memoize`,
`enable_hashagg`, `geqo`, …) are process-global atomics and are **not** part of
the cache key and **do not** trigger a bypass — a documented soundness gap: a
`SET enable_memoize = off` in one session changes plans for every session and
can be masked by a cached plan.

---

## 13. Structural divergence summary

Numbered, atomic, each with a goopg citation and the doc-01 §13 checklist item
it violates. This is the list the gap-analysis document consumes.

1. **Two planners joined at one seam.** The PG-shaped Path search covers only
   the inner-join prefix of the FROM tree, reached exclusively through
   `tryJoinSearch` (`joinsearchseam.go:168`); everything else is a fixed
   sequence of Node rewrites in `planSelect` (`planner.go:741`). — violates
   §13 items 6, 19, 27.
2. **A declined search is silent and total.** `tryJoinSearch` returns
   `(node, pred)` unchanged on any of eleven gate failures (§0 table); the
   statement then runs on syntactic FROM order with no diagnostic outside
   `GOOPG_PGSHAPED_DP_TRACE=1` (`joinsearchtrace.go:45`). — no PG counterpart;
   PG always plans through `make_one_rel`.
3. **Explicit INNER joins are pinned by default**, so an n-way explicit-JOIN
   chain becomes n−1 nested **two-relation** problems
   (`collapse.go:joinPinned` with `pgShapedCollapseEnabled() == false`,
   `relfromjoinlist.go:makeRelFromJoinlist`). Join order is the SQL text order.
   TPC-DS Q72 root cause. — violates §13 item 29 (collapse limits) and item 54
   (DP by level).
4. **Mixing comma-FROM with an explicit `ON` join declines the seam entirely**:
   `planFromClause` places the joined item on the **right** of a CROSS link, so
   `extractSearchLeaves` (`joinsearchseam.go:551`) hits a predicated inner link
   at `base != 0` and returns `ok = false`. — violates §13 item 27.
5. **Outer, semi, anti and full joins never enter the search.** They are peeled
   (`splitOuterSpine`, `joinsearchseam.go:446`), refused
   (`relfromjoinlist.go` pinned-outer arm), or handled by a pre-DP pass
   (`predp.go:runJoinSearchBelowPinned`). `joinIsLegal`
   (`joinsearchlevel.go:182`) is a faithful port that always returns
   `(nil,false,nil)` in production. — violates §13 items 30, 31, 55, 57.
6. **No range table and no `PathTarget`.** Column identity is a flat index into
   the pre-search binding concatenation; `RelOptInfo.baseLeaf` /
   `baseOffset` (`path.go:~279-309`) plus `joinlayout.go` (56 KB) plus
   `createplanroot.go`'s totality assertions substitute for it. — violates
   §13 items 28, 82.
7. **16-relation hard ceiling.** `RelSet` is `uint16` (`path.go:30`),
   `maxSearchRels = 16` (`joinsearch.go:46`); the seam declines above it. — no
   PG counterpart.
8. **A sub-problem crosses the recursion boundary as ONE prebuilt node**, not a
   live pathlist: `searchOneProblem` returns a `joinlistRel` with a table-less
   `baseRelInfo`, and `tupleFraction` is forced to 0 for every recursive call
   (`relfromjoinlist.go:257`, `:336`). Differently-sorted and parameterized
   sub-problem paths are unreachable to the enclosing search. — violates
   §13 items 54, 68.
9. **Base rels get exactly one seed path, a `PathPrebuilt`**, costed as a seq
   scan (`joinsearch.go:~388-395`). The real producer `generateScanPaths`
   (`pathgen.go:27`) has no production caller. — violates §13 items 44, 53.
10. **No partial paths anywhere.** `addPartialPath` (`path.go:562`) is
    unreachable, `RelOptInfo.PartialPathlist` is always empty, `gatherCost`
    (`cost_funcs.go:407`) has zero callers. Parallelism is a post-pass over the
    finished Node tree (`parallel.go:88`). — violates §13 items 53, 67, 73, 75, 76.
11. **`enable_*` is producer-skipping, not `disabled_nodes` counting.**
    `addBaseRelIndexPaths` (`pathindexordered.go:49`) skips whole producers;
    `Path.DisabledNodes` is always 0 (`path.go:~116-120`) though it is compared
    first in `comparePathCostsFuzzily` (`path.go:389`). Only 4 of PG's 24
    `enable_*` GUCs reach the planner as scan toggles; 4 more reach it as
    process-global atomics. — violates §13 item 84.
12. **Cost GUCs are inert.** `defaultCostParams()` (`cost_funcs.go:83`)
    hard-codes PG 18 boot values and neither of its two production callers
    threads a session. `work_mem` and `hash_mem_multiplier` never reach the
    planner. — violates §13 item 84 and the whole of doc 02.
13. **`from_collapse_limit` / `join_collapse_limit` are hard-coded to 8** and
    only consulted under `GOOPG_PGSHAPED_COLLAPSE=1`
    (`collapse.go:defaultCollapseLimits`, `:pgShapedCollapseEnabled`).
    `SET join_collapse_limit = 1` is a no-op. — violates §13 items 29, 85.
14. **GEQO is wired but effectively unreachable.** `SetGeqoEnabled` /
    `SetGeqoThreshold` are bridged (`cmd/goopg/main.go:425-431`) as
    process-global atomics, but the threshold is compared against `len(items)`
    of a single `searchOneProblem` (`relfromjoinlist.go:370`), which the default
    collapse regime keeps at 2. `geqo_effort` is hard-coded to 5; four other
    geqo GUCs are unread. — violates §13 items 54, 85.
15. **No upper-rel path stages.** Aggregation, DISTINCT, windows, set-ops,
    ORDER BY and LIMIT are built as nodes and then patched by rules
    (`groupagg_indexorder.go`, `groupagg_presorted.go`, `groupagg_hashagg.go`).
    `PathAgg`, `PathGather`, `PathGatherMerge` are declared but have no
    `createPlanNode` arm and are never constructed (`createplan.go:117-121`
    `default:` panic). — violates §13 items 19–25.
16. **Query orderings never reach the search.** `has_useful_pathkeys` implements
    only the join-clause arm (`pathindexordered.go:76-85`); `group_pathkeys` and
    `query_pathkeys` do not cross the boundary, so ORDER BY / GROUP BY cannot
    motivate an index path. — violates §13 items 39, 50, 70, 71, 72.
17. **Pathkeys are not equivalence-class based.** `PathKey` is
    `{Expr, SortAsc, NullsFirst}` matched by `exprEqual` (`pathkeys.go:18`,
    `:27`), so an `a`-ordered path is not recognised as satisfying a `b`
    requirement across `a = b`. — violates §13 item 70.
18. **Equivalence classes exist only as a union-find used for inference.**
    `equiv_class.go` derives transitive and anchored equalities and tags
    `restrictInfo.ecID`; there is no EC object, no join domain, no base/join
    implied-equality generation per parameterization, no
    `reconsider_outer_join_clauses`, no broken-EC fallback. — violates
    §13 items 34, 35.
19. **Single-relation quals are attached to the leaf before the search**, not
    kept as `baserestrictinfo` (`local_filters.go:64`, `joinsearchseam.go` leaf
    loop). Restriction selectivity is baked into `RelOptInfo.Rows` before any
    path exists. — violates §13 items 42, 49.
20. **No two-stage join costing.** Every `addPathsToJoinrel` arm costs fully then
    calls `addPath`; there is no `initial_cost_*` / `add_path_precheck` /
    `final_cost_*` split (`joinpaths.go:139`). — violates §13 item 64.
21. **`param_source_rels` is not modelled.** `paramSourceRels()` returns 0
    (`joinpathsnli.go:44`), so a parameterized inner is never rejected for
    having its parameter source outside the current join. — violates
    §13 item 60.
22. **No `Material` path and no inner materialisation.** `addNestLoopPath`
    (`pathgen.go:113`) over-charges the rescan as a documented safe-direction
    bias. — violates §13 item 62.
23. **No `inner_unique` / unique-join optimisation, no `create_unique_path`**
    (`joinpaths.go` has no `innerrel_is_unique` analogue). — violates
    §13 items 36, 57, 59.
24. **Post-search rewrites can override the cost-based choice.**
    `rewriteScanInputsWithSingleTablePredicates` (`scan_input_rewrite.go:50`)
    turns a SeqScan into an IndexScan, and `rewriteJoinsToNLI`
    (`nl_index_join.go:84`) turns a Hash/NL join into a `NestedLoopIndexJoin`,
    both after `tryJoinSearch` and both with their own gates. Two independent
    routes exist to an NLI join. — no PG counterpart; violates §13 item 66's
    premise that `add_path` is the only arbiter.
25. **A pre-search parse-level heuristic reorders the FROM list.**
    `reorderCommaFromByCardinality` (`joinorder.go:116`) permutes comma-FROM
    items by cardinality/connectivity before anything is planned, biasing the
    syntactic chain the search later sees. — no PG counterpart.
26. **Constant folding is a final pass, not a preprocessing pass.**
    `foldPlanConstants` (`foldconst.go:99`) runs on the finished tree at
    `planner.go:1820`; there is no `eval_const_expressions` before qual
    distribution, so `LIMIT 5+5` is not a constant to `searchTupleFraction`
    (`joinsearchseam.go:629`). — violates §13 items 13, 18.
27. **No `set_plan_references` phase.** Column resolution is distributed across
    five mechanisms (§11), guarded by panicking totality assertions
    (`createplanroot.go:108`, `:351`) rather than by a flat range table. —
    violates §13 item 82.
28. **Costs are never copied from the path onto the node**, so `EXPLAIN` prints
    a literal `(cost=0.00..0.00 rows=N width=0)`
    (`internal/executor/operators_explain.go:494`, `:1540`) with `rows` from the
    *legacy* estimator `EstimateRows` (`cardinality.go`) rather than from the
    chosen `Path`. The cost model is unobservable from SQL. — violates
    §13 item 83.
29. **Two cardinality estimators run in production simultaneously**: the search
    arm `sizeJoinRel` → `calcJoinrelSize` / `superkeyJoinSelectivity`
    (`joinrelsize.go`) and the legacy arm `estimateJoin` (`cardinality.go`) with
    its own FK/unique-superkey re-implementation (`joinkeyproof.go`, whose file
    header documents the hazard). — violates §13 item 69.
30. **Plan-cache soundness gap for process-global planner GUCs.** The cache key
    is `(NamespaceDBOid, normalized SQL)` (`dispatch.go:2437`); only the four
    scan toggles trigger a bypass (`plannerScanTogglesActive`,
    `dispatch.go:1786`). `SET enable_memoize/enable_hashagg/geqo/…` mutate
    process-global atomics affecting every session and are neither keyed nor
    bypassed. `ANALYZE` and `SET` never invalidate. — no PG counterpart (PG's
    plancache revalidates per session).
31. **`reconcileNLILayout` is dead** in production (`joinlayout.go:1083`; call
    site lost at M0127-P6.3, recorded at `planner.go:92-100`), kept pending
    proof that a searched plan never needs it. — housekeeping, not a PG gap.

---

## Appendix — verified corrections to the input code map

The Explore-agent code map (`goopg-codemap.md`) was accurate on structure; these
specific claims are wrong or incomplete at HEAD:

| Map claim | Actual |
|---|---|
| "`SetGeqoEnabled`/`SetGeqoThreshold` have **no callers** outside the package — `geqo`/`geqo_threshold` GUCs registered but never wired" | **Wrong.** Both are bridged in `cmd/goopg/main.go:425-431` via `registry.OnChange`. The real divergence is that the bridge is process-global, not per-session, and that GEQO is unreachable in practice because the collapse default keeps every problem 2 items wide. |
| "`searchOneProblem` at `relfromjoinlist.go:336`" (implied free function) | It is a method: `func (prob *joinlistProblem) searchOneProblem(...)`, `relfromjoinlist.go:336`. Correct otherwise. |
| "`joinsearchseam.go:168 tryJoinSearch`, `:182 tryPGShapedJoinSearch`, `:446 splitOuterSpine`, `:551 extractSearchLeaves`, `:629 searchTupleFraction`" | All five verified exact at HEAD. |
| "`makeRelFromJoinlist(:257)`" | Verified exact. |
| "`enable_memoize`/`enable_nestloop_index` reach the planner" | Verified, plus two the map omits: `enable_presorted_aggregate` (`main.go:411`) and `enable_hashagg` (`main.go:419`). |
| "`generateScanPaths` and `generateHashJoinPaths` have no production callers" | Verified — every in-package reference is a comment. |
| "`addPartialPath` never called in production" | Verified transitively: its only caller is `generateScanPaths`. |
| "`gatherCost` unreachable" | Verified: zero references outside its own definition. |
| "`PathAgg`, `PathGather`, `PathGatherMerge` have no `createPlanNode` arm" | Verified. Note `PathSort` **does** have an arm and **is** constructed (merge-join children). |
| "`GOOPG_INDEX_PROBE_MULT` default 1.0" | Verified (`cost_funcs.go:438`), and additionally: it is the one plan-shaping env var **absent** from `scripts/planner-flags.env`, so an artefact cannot state which arm it measured. |
