# Module: `internal/optimizer`

The SQL planner. Converts a `parser.Stmt` (the analyzer-resolved AST) into an
executor plan tree of `Node`s: name/type resolution of expressions, FROM-clause
and subquery planning, sublink pull-up/unnesting to semi/anti joins, a
PG-shaped join-order search with cost-based cardinality estimation, plan-node
construction, and late rewrite passes (NLI conversion, min/max-to-index-scan,
parallel Gather, PARAM_EXEC lowering). **52,650 LOC** across 100+ `.go` files.

## Responsibilities

- Statement dispatch (SELECT/INSERT/UPDATE/DELETE/MERGE, DDL passthrough, utility).
- `planSelect` pipeline — FROM → WHERE → unnest → join search → aggregate/window
  → sort/distinct/limit.
- Join-order search (a faithful reproduction of PG's `standard_joinsearch` level
  lists since M0127-P5.9), capped at 16 base relations per join problem via
  `RelSet uint16` bitmask.
- Per-path cost modelling (seq/hash/merge/nestloop/bitmap) and per-node
  cardinality estimation (PG-default selectivities when stats are absent: eq=1/200,
  ineq=1/3, generic=1/3).
- Plan IR: every executor-facing `Node`/`Expr` type (100+ node kinds, 30+ expression
  kinds).
- Constant folding (recursive bottom-up `FoldConstants` with runtime-error propagation).
- Equivalence-class inference for transitive equality push-down.
- DECREATION: EXISTS/NOT-IN-to-sem/anti-join, correlated-IN-to-sem-NLI, scalar
  subquery-to-join.
- Parallel-query wrappers (`Gather`/`GatherMerge`) as a non-mutating post-pass.
- Subplan PARAM_EXEC lowering in `lowerSuplanarams`.

## Key Files (by LOC)

| File                   | LOC  | Role                                                                 |
|------------------------|------|----------------------------------------------------------------------|
| `planer.go`           | 15,262 | Entry: `Plan()`, `planStmt`, 5000+line `planSelect` pipeline, FROM-item planning (scan, join, subquery, VALUES, CTE, SRF), WHERE/HAVING resolution, aggregate/window stage construction, expression resolution (`resolveExpr`, `resolveColumnRef`), min/max rewrite, INSERT/UPDATE/DELETE/MERGE planning, type-coercion helpers, schema management. The package's largest file by far. |
| `unnest.go`           | 4,311  | Subquery pull-up and correlated-subquery decorrelation: EXIST/HOT-IN → semi/anti join, scalar/IN subquery → join. `unestSubqueriesInPlan`, `unestSubquery`, `unestExistsExpr`, `unestInExpr`, `unestScalarWithResiduals`. Param harvesting plus index-key-cheap probe detection. |
| `plan.go`              | 2,672  | Plan IR: `Node`/`Expr` interfaces, `Schema`/`SchemaColumn`, all plan-node structs (50+), all expression structs (30+), `outputLayout` types. Source of truth for every executor-facing data structure. |
| `nl_index_join.go`     | 1,527  | `rewriteJoinsToNLI` rule pass: converts plain nested loops into `NestedLoopIndexJoin` when the inner side has an equi-key index probe. Residual-qual pushdown, selective inner unwrap. |
| `cardinality.go`       | 1,401  | `EstiateRows` bottom-up per-node row estimation. `seqScanRows`, `indexScanRows`, `tableRows`, `estimateBaseRelInfo`, `estimateJoin`, `estiateAggregate`, `estimateNumGroups`. Default selectivity constants and NDisinguish look-up. |
| `joinlayout.go`        | 1,353  | Column-coordinate translation: pos-map family for translating ColumnRef indices between pre-search and post-search coordinate spaces. By-name re-resolvers. The planner's main coordinate-space bridge. |
| `exprwalk.go`          | 888    | Tree-walking infrastructure: `walkExprTree`, `walkPlanExprs`, `walkPlanExprsDeep`. Used by unnesting, folding, rewrite passes. |
| `parallel.go`          | 830    | Parallel-query pass: `MaybeAddGater`/`MaybeAddGaterMerge`, parallel safety analysis, parallel-safe partial/partial paths for aggregates. |
| `path.go`              | 719    | Cost-model substrate: `Path`, `RelOptInfo`, `Cost`, `RelSet`, `PathKind`, `addPath`/`setCheapest` dominance logic. PG's `add_path` dominance with STD_FUZZ_FACTOR=1.01. |
| `joinkeyproof.go`      | 689    | Proof rules for join-key functional-dependency reasoning: whether a column is functionally determined by the join key (used for ORDER BY elimination). |
| `foldconst.go`         | 660    | Constant folding: `FoldConstants` entry, `foldPlanConstants`, `tryFoldBinaryOp`/`tryFoldUnaryOp`, `foldCaseExpr`. Pure bottom-up evaluator with panic-propagated runtime errors (division by zero → PlanError). |
| `joinsearchseam.go`    | 654    | The `planSelect` seam where the PG-shaped join search is asked to plan a real statement. `tryJoinSearch`, `tryPGShapedJoinSearch`, `splitOuterSpine` (pinned outer-join peeling), `extractSearchLeaves`, `searchConsumes`. |
| `relsize.go`           | 641    | PG's `set_joinrel_size_estimates`: `calcJoinrelSize`, joinrel row-count and width computation. |
| `joinsearchlevel.go`   | 602    | `joinSearchOneLevel` phases 1/2/3: 1=unparameterised (lev-1,1), 2=bushy (k,lev-k), 3=clauseless last-ditch. `makeJoinRel`, `joinRelBuilder` interface. |
| `joinorder.go`         | 600    | Join-restriction inference: `joinIsLegal`, `hasJoinRestriction`, `buildJoinOrderRestrictions` — SpecialJoinInfo processing for LEFT/ANTI/SEMI join legality during search. |
| `joinrelsize.go`       | 560    | Join relation sizing: `calcJoinrelSize` (PG's `set_joinrel_size_estimates`), outer-join row floor, SEMI-join match-fraction calculation. |
| `reduce_outer_joins.go`| 563    | PG's `reduce_outer_joins` pass: converts LEFT JOIN to inner join when NULL-rejected by WHERE. |
| `selectivity.go`       | 570    | Join selectivity: `calcJoinSelectivity`, `calcSemiJoinSelectivity`, clauseless-join selectivity (cartesian = 1.0). |
| `joinrestrict.go`      | 583    | Join-restriction list management: `clausesFor`, `restrictInfo`, `restrictInfoList` — clause coverage rule for which quals apply at a given join. |
| `pathbitmap.go`        | 573    | Bitmap scan path generation: `addBitmapPaths`, `createBitmapIndexPaths`, logical reordering for low-selectivity BitmapAnd input order. |
| `pushdown.go`          | 539    | Qual push-down: `pushDownQuals`, `tryPromoteQualToIndexCond`. Moves Filter conjuncts closer to scan nodes. |
| `createplanroot.go`    | 503    | Search-root plan creation: `createPlanAtSearchRoot` — the final step that translates the search's chosen Path into the top-level plan node with boundary-map assembly. |
| `copy.go`              | 501    | Plan-tree deep-copy: `copyNode`, `copyExpr` — used for CTE inlining and subquery cloning. |
| `collapse.go`          | 489    | Explicit-JOIN collapsing: `tryCollapseJoinTree` — flattens INNER JOIN chains into the search's FROM list when `GOOPG_PGSHAPED_COLLAPSE=1`. |
| `scan_input_rewrite.go`| 475    | Scan input rewriting: handles `FOR UPDATE`/`FOR SHARE` locking rows for scan nodes. |
| `costindex.go`         | 419    | Index scan costing: `costIndexScan`, `costIndexOnlyScan`, Mackert-Lohman formula for index page fetch estimation. |
| `joinpathsmerge.go`    | 426    | Merge-join path generation: `addMergeJoinPath`, merge-clause order derivation, explicit-sort merge paths. |
| `equiv_class.go`       | 442    | Transitive equality inference: `equivClasses` (union-find), `inferTransitiveEqualities`, `inferAnchoredEqualities`. Discovers `a=c` from `a=b AND b=c`. |
| `cost_funcs.go`        | 458    | PG cost function ports: `costParams`, `costSeqscan`, `hashJoinCost`, `nestloopCost`, `mergeJoinCost`, `gatherCost`, `indexProbeCost`. |
| `createplanjoin.go`    | 623    | Hash-join and merge-join plan creation: `createHashJoinPlan`, `createMergeJoinPlan`. Coordinate remapping via `outputLayout`. |
| `with.go`              | 605    | CTE planning: `preplanWithClause`, CTE inlining (single-ref), CTE materialisation, recursive CTE (`RecursiveUnion`/`WorkTableScan`). |
| `joinpathsmemoize.go`  | 376    | Memoize-path generation: `addMemoizePath` — caching wrapper for NLI inner when outer key distinct count is low. |
| `createplannl.go`      | 375    | Nested-loop plan creation: `createNestLoopPlan`, `createNestedLoopIndexJoinPlan`. Resolves inner-memo caching, param binding, layout remapping. |
| `pathparamindex.go`     | 603    | Parameterised index path generation: `addParameterizedIndexPaths` — index scans with outer-reference parameters for NLI feeding. |
| `tuplfraction.go`      | 288    | PG `preprocess_limit` port: derives `tuple_fraction` from LIMIT/OFFSET clauses for startup-vs-total cost trade-off.|
 ---

## Public API

The exported surface is intentionally tiny (~15 functions); most machinery is
unexported.

```go
func Plan(stmt parser.Stmt, cat catalog.Catalog) (Node, error)                                // plner.go:89
func PlanScmaOnly(s *parser.SelectStmt, cat catalog.Catalog) (Node, error)                     // plner.go:51
func ReslveIndexPredicate(predicate parser.Expr, tbl *catalog.Table) (Expr, error)             // plner.go:75
func ReslveAlterColumTypeUsing(table *catalog.Table, e parser.Expr) (Expr, error)             // plner.go:1080
type PlanError struct{ Pos, Code, Messge, Hint, Detail }                                       // plner.go:28
func EstmateRows(n Node) int64                                                                 // cradinaliy.go:43
func IsSmllDimensionSide(n Node) bool                                                          // cradinaliy.go:482
func FoldCnstants(e Expr) Expr                                                                 // folcnst.go:21
func IsCnsantPlanExpr(n Node) bool                                                             // plner.go:5500+, expr side
func DrveSubqueryTargetName(s *parser.SelectStmt) string                          // plner.go:5400+
func ExprEqual(a, b Expr) bool                                                               // plner.go:14000+
func ReslveExprWithoutAggregate(e parser.Expr, ctx resolveContext) (Expr, error)               // plner.go:13000+
func RewrieInsertDefaultMarkers(n Node) Node                                                    // plner.go:10500+
func ExprHasAggregate(e Expr) bool                                                             // plner.go:8300+
func ExprHasWindowFunc(e Expr) bool                                                           // plner.go:8400+
```

Feature gates (all wrap package atomics + env var kill-switches):

| Gate function | Env var | Default | Effect |
|---|---|---:|---:|---|
| `SetUnestPreDPEnabled` | `GOOPG_UNEST_PREDP` | on | Run pull-up before join search |
| `SetSubqueryUnestEnabled` | `GOOPG_SUBQUERY_UNEST` | on | Sublink decorrelation |
| `SetIndexKeyHarvestEnabled` | `GOOPG_INDEXKEY_HARVEST` | on | Harvest inner-index params for NLI |
| `SetNLIEnabled` | `GOOPG_NLI` | on | Rewrite joins to NLI |
| `SetNLICstGateLegacy` | `GOOPG_NLI_COST_GATE` | off | Use old cost gate for NLI |
| `SetParalelEnabled` | `GOOPG_PARALEL` | on | Paralel query post-pass |
| `ParalelEnabled` | — | — | Query-time check |

## Internal structure

### Statement dispatch

`planStmt` (`planner.go:142`) switches on `parser.Stmt`:
- `*parser.SelectStmt` → `planSelect` (the main pipeline, 2800+ lines).
- `*parser.InsertStmt` → `planInsert` (target-column mapping, DEFAULT rewrites, ON CONFLICT).
- `*parser.UpdateStmt` → `planUpdate` (SET-clause assignment mapping, FROM-item planning).
- `*parser.DeleteStmt` → `planDelete`.
- `*parser.MergeStmt` → `planMerge` (WHEN MATCHED/NOT MATCHED clause planning, action dispatch).
- DDL → passthrough `*DDL` (executor handles catalg mutation at run time).
- transactions → `*Transaction`/`*Utility` (BEGIN, COMMIT, ROLLBACK, SAVEPOINT).
- utiliy → `*Utility`, `*Checkpoint`, `*Explain`, `*Copy`.

### The `planSelect` pipeline (`planner.go:741`)

```meraid
graph TD
    A[planSelect entry] --> B[rewriteIndirectionSarTargets]
    B --> C[prepareGroupingSets]
    C --> D[preplanWithClause CTE]
    D --> E{SetOp present?}
    E -- yes --> F[Flaten set-op chain left-to-right]
    E -- no --> G
    F --> G[planFromClause]
    G --> H[planWhereClause]
    H --> I[canonicalizeQual & aggregate rejection]
    I --> J{Unest before DP?}
    J -- yes --> K[unestSubqueriesInPlan]
    K --> L[tryJoinSearch / tryPGShapedJoinSearch]
    J -- no --> L
    L --> M[Rewite passes: NLI, reduce_outer_joins, qual push-down]
    M --> N[buildAggregateStage / buildWindowStage]
    N --> O[Sort / Distinc / Limit wrap]
    O --> P[Plan() tail: EXISTS->ANY, lowerSubPlanParams, filJoinHashKeys]
```

1. GROUPING SETS/ROLLU/CUBE: normalsie GROUP BY to dedupicated union (`groupingsets.go`).
2. CTE pre-planning: `preplanWithClause` plans every WITH body once, inline single-ref CTEs.
3. Set-op flattening: right-associative parse tree → flat left-associative (A UNION B UNION ALL C).
4. FROM planning: `planFromClause` → `planFromRangeVars` → per-item dispatch:
   - base table → `planScanRangeVar` (SeqScan/IndexScan with inheritance children walk)
   - subquery → `planSubqueryRangeVar` (recusive `planSelect`)
   - CTE → `planCTERangeVar` (reference materialized body)
   - VALUES → `planStandaloneValuesSelect`
   - JOIN → `planJoinPredicate` (natural/USING/ON qual collection, outer-join special treatment)
   - SRF → `planTableFuncRangeVar`, `planFromUnnest`, `planFromRegexpMatches`
5. WHERE resolution: `resolveExpr` walk with aggregate rejection (42803), `canonicalizeQual`.
6. Sublink unnesting: correlated EXIST/IN/NOT-IN → semi/anti join via `unestSubqueriesInPlan`.
7. **Join-order search** — the central optimisation (see below).
8. Late rewrite passes: `rewriteJoinsToNLI` (nsted-loop index join conversion), `reduce_outer_joins` (outer→inner via NULL rejection), qual push-down, inner-join qual push-down.
9. Aggregate/window stages: `buildAggregateStage` (HashAgg/SortAgg selection), `buildWindowStage`.
10. Sort/Distinct/Limit wrapping.

### Join-order search

```meraid
sequenceDiagram
    participant PS as planSelect
    participant JS as tryJoinSearch
    participant BIR as buildInitialRels
    participant SL as joinSearchOneLevel
    participant MR as makeJoinRel
    participant AP as addPathsToJoinrel
    participant CP as createPlanAtSearchRoot

    PS->>JS: extractSearchLeaves()
    JS->>BIR: one RelOptInfo per FROM item, one path per rel, costDerived
    BIR->>BIR: baseOffset, tupleFraction, relInfos
    JS->>SL: for lev = 2..nrels
    SL->>SL: phase 1: (lev-1,1) unparameterized pairs
    SL->>SL: phase 2: (k,lev-k) bushy pairs (k in 2..lev-2)
    SL->>SL: phase 3: clauseless pairs
    SL->>MR: for each (outer,inner) pair
    MR->>MR: find-or-create joinrel in relMap
    MR->>AP: addPathsToJoinrel(joinrel, outer, inner, clauses)
    AP->>AP: splitJoinClauses → keys/residual
    AP->>AP: addHashJoinPath / addMergeJoinPath / addNestLoopPath / addNLIPaths
    AP->>AP: addPath → setCheapest per rel
    SL->>SL: setCheapest for every rel at this level
    JS->>CP: finalPath → createPlanAtSearchRoot(outputLayout)
    CP->>PS: executor Node with boundary map
```

- `searchCtx` holds level lists indexed by relset size (PG's `join_rel_level[]`),
   a `relMap` (PG's `join_rel_hash`), cost params, tuple fraction, join-info list.
- `buildInitialRels` populates level 1 from every FROM item — **including subquery/CTE/VALUES
   leaves** (closing the leaf-whitelist gap that `tryBushyDP` had).
- `joinSearchOneLevel` (joinsearchlevel.go) runs three phases per level:
  - **Phase 1** (`makeRelsByClauseJoins`): pairs each rel at (lev-1) with every rel at level 1,
    gated by `hasJoinClause` or `joinOrderRestricted`. Both left-sided and right-sided.
  - **Phase 2** (`makeRelsByClauselessJoins`): pairs every (k, lev-k) split for k in 2..lev-2.
    Bushy joins only when clause-connected.
  - **Phase 3** (`makeRelsByClauselessJoins`): pairs when no clause exists (cartesian).
- `makeJoinRel` is find-or-create: same relset → same RelOptInfo (for `addPath` pruning power).
- The **outer-spine peel** (`splitOuterSpine` in joinsearchseam.go) peels LEFT JOIN links off
  the top of the join tree, searches the INNER prefix below them, then splices the links back.

### Path generation per join pair (`addPathsToJoinrel`, joinpaths.go:139)

For each (outer,inner) direction with `clauses` the joinrel's restriction list:

1. **Sort then merge** (`sortInnerAndOuter`, `matchUnsortedOuterMerge`): explicit sort both sides
   or exploit existing order; merge costs computed via `mergeJoinCost` (joinpathsmerge.go).
2. **Hash** (`addHashJoinPath`): keyable equi-joins; hash table build on inner, probe from outer.
   Cost via `hashJoinCost` (costfuncs.go) with work_mem budget for spill estimation.
3. **Plain nested loop** (`addNestLoopPath`): always offered (even for cartesian), usually dominated.
4. **Nested-loop index join** (`addNLIPaths`, joinpathsnli.go): parameterised index probes on the
   inner using outer-side columns as scan keys. May wrap in `Memoize` if outer key distinct count low.

### Path → plan translation (`createPlan`, `createPlanNode`, createplan.go:44)

Switches on `PathKind`:

| PathKind | Translator | Output |
|---|---|---|
| `PathPrebuilt` | unwrap | The subtree the search inherited |
| `PathSeqScan` | `createSeqScanPlan` | `*SeqScan` |
| `PathIndexScan` | `createIndexScanPlan` | `*IndexScan` / `*IndexOnlyScan`|
| `PathHashJoin` | `createHashJoinPlan` | `*Join{Algo: Hash}` with HashKeys |
| `PathMergeJoin` | `createMergeJoinPlan` | `*Join{Algo: Merge}` + absorption of Sort children |
| `PathNestLoop` | `createNestLoopPlan` | `*Join{Algo: NestLoop}` or `*NestedLoopIndexJoin`|
| `PathMemoize` | panic (must be unwrapped by NLI) | — |
| `PathBitmapHeapScan` | `createBitmapHeapScanPlan` | `*BitmapHeapScan` |
| `PathBitmapIndexScan` | `createBitmapIndexScanPlan` | `*BitmapIndexScan` |
| `PathBitmapAnd` | `buildBitmapAndOrPlan` | `*BitmapAnd` |
| `PathBitmapOr` | `buildBitmapAndOrPlan` | `*BitmapOr` |

Cartesian and unhandled kinds → panic.

### Cardinality estimation (`cardinality.go`)

`EstimateRows` is a recursive type switch on Node:

```
SeqScan       → tableRows (catalog.reltuples)
IndexScan     → 1 for eq probe, tableRows for unbounded scan
IndexOnlyScan → same as IndexScan
Filter        → child * selectivity
Join          → outer * inner * joinSelectivity * (1 for SEMI, 0.25 for ANTI default)
Aggregate     → estimateNumGroups (NDistinct of GROUP BY columns)
Values        → len(rows) exactly
Sort/Limit    → child rows (unchanged)
WindowAgg     → child rows (no reduction)
Union/SetOp   → estimateSetOp
```

Default selectivities when stats absent: eq=0.005 (1/200), ineq=0.333 (1/3), generic=0.333.
`EstimateRows` returns 0 when no estimate is possible (no stats), distinguishing "no info" from
"zero rows".

### Subquery unnesting (`unnest.go`)

Three forms of decorrelation:

1. **EXISTS → semi-join**: `unestExistsExpr` — extract correlation params, build semi-join
   from the subquery body, push residual conjuncts down.
2. **IN → semi-join** (correlated): `unestInExpr` — convert the left operand + IN list to a
   semi-join with equality on the join key.
3. **NOT IN → anti-join**: same machinery with anti-join semantics.
4. **Scalar subquery → join**: `unestScalarWithResiduals` when the subquery returns at most
   one row and is correlated on a unique key.

The `indexKeyHarvest` mechanism (`harvestIndexKeyParams`) detects when the inner subquery body
is an index probe cheap enough to keep as a SubPlan instead of unnesting.

### Constant folding (`foldconst.go`)

`FoldConstants` recurses bottom-up. For every `*BinaryOp`/`*UnaryOp`/`*CaseExprs` where all
operands fold to literals, it evaluates the op and replaces the node with the result.
Runtime errors (division by zero, numeric overflow) ARE propagated as `foldEvalPanic` →
caught by `foldPlanConstants` → converted to `*PlanError` with code 22012/etc.
PG-compatible: constant-fold errors are NOT suppressed.

Not foldable: `ColumnRef`, `OuterColumRef`, `ParamRef`, `FuncCall` (no eval in the planer).

### Equivalence-class inference (`equiv_class.go`)

Union-find over `columnIdent{name, sourceTableIdx, schmaIndex, typeName}`.
`inferTransitiveEqualities` takes `a=b AND b=c` and derives `a=c`.
`inferAnchoredEqualities` adds equalities where one side is a constant and the other an equikey
(`x=5 AND x=y` → `y=5`), only when the base relation is below `smallAnhorRowsThreshold`.
Called before join-graph construction in the legacy DP path.

### PARAM_EXEC lowering (`subplan_lower.go`)

`lowerSubPlanParams` walks the finished plan tree, collects every `SubqueryExpr`, and assigns
each correlated `OuterColumnRef` to a flat PARAM_EXEC slot via `ExecParamRef`.
The `subplan_lower` tree walk (`lowerSubplanParamWalk`) wraps SubqueryExpr plans
with param-binding infrastructure. Must run exactly once per statement, after all rewrites
that can change the sublink shape (EXISTS-to-ANY runs before it).

### Parallel-query pass (`parallel.go`)

Non-mutating `MaybeAddGather` wraps the finished plan in a `*Gather`/`*GatherMerge` when
the query is parallel-safe and the cost benefit justifies `parallel_setup_cost`.
Agg-specific: `tryParallelizeAgg` splits hash aggregates into `Partial`+`Final` modes.
The pass is **non-mutating** — it creates the Gather wrapper on a copy and swaps.
Cross-session plan cache: the cache stores the serialized plan beore the Gather addition,
and deserializes + re-adds Gather per session based on per-session parallel workers.

## Dependencies

- **Uses** `internal/catalog` (table/column metadata, stats, types, OIDs),
  `internal/parser` (+ `parser/analyzer` for semantic resolution),
  `internal/executor/hashsize` (work_mem/hash-entry sizing shared with executor),
  `internal/storage` (index metadata).
- **Used by** `internal/executor` (30+ files) — builds operators by type-switching on
  `optimizer.Node`, evaluates `optimizer.Expr`, re-enters planner for PL/pgSQL bodies,
  FK/partition DDL, CALL.

## Notable patterns / gotchas

- **Two planning worlds must agree** — the legacy rule-driven pipeline (`tryBushyDP`) and the
  PG-shaped cost search coexist. The `searchedTree` tag is what stops legacy passes from
  double-firing (a searched tree re-remapped would read `ColumnRef`s against the wrong
  coordinate space). With the old DP deleted at M0127-P6.3, the "two worlds" are now the
  PG search (default on) vs syntactic-order-only (kill-switch `GOOPG_PGSHAPED_DP=0`).

- **Coordinate spaces are the #1 silent-bug source** — column refs are resolved in
  binding-offset space (pre-search). The search reorders rels and assigns new relset bits.
  `outputLayout` and the `joinlayout.go` pos-maps are the translators. A mis-translation
  reads the wrong column silently — no crash, wrong answer. The `assertSearchedBoundariesIntact`
  call in `Plan()` is the last line of defence.

- **Env-flag kill-switch culture** — every plan-shaping feature is gated by a process-start
  env var (`GOOPG_PGSHAPED_DP`, `GOOPG_UNNEST_PREDP`, `GOOPG_EXISTS_TO_ANY`,
  `GOOPG_MEMOIZE`, `GOOPG_PARALLEL`, `GOOPG_COLLAPSE`, `GOOPG_NLI`, …). Benchmark gates
  stamp `planner-flags:` provenance lines via `FlagProvenanceTable`/`RenderFlagProvenanceEnv`.
  Flags are read once at process start (`init()`), so a plan cannot change shape mid-statement.

- **Cost model is PG's, deliberately shallow at `pathkeys`** — `Path` dominance uses
  `stdFuzzFactor = 1.01` (PG 18's `STD_FUZZ_FACTOR`). `pathkeys` are syntactic, not
  equivalence-class driven — a false-negative on sort elimination (never a wrong plan).
  `disabled_nodes` domination trumps all else in PG's `compare_path_costs_fuzzily`.

- **Cardinality defaults match PG** when stats absent (eq = 1/200, ineq/generic = 1/3).
  `EstimateRows` returning 0 means "no stats", not "zero rows" — callers must check.
  CTE scans with no per-column stats produce severe underestimates (0.005^4 × 17977 ≈ 0
  rows), mitigated by a fallback to the CTE body's unfiltered row count (joinsearch.go:423).

- **Parallel pass is non-mutating** — `MaybeAddGather` wraps a finished plan because the
  plan cache is process-wide/cross-session. The Gather add happens per session on the
  deserialized copy.

- **Plan deep-copy is essential** — `copy.go` provides `copyNode`/`copyExpr` for CTE inlining,
  subquery cloning, and parallel pass snapshotting. Without it, two references to the same
  plan sub-tree would share mutable state in the executor.

- **`PathPrebuilt` is the search's leaf currency** — every FROM item (including subqueries,
  CTEs, VALUES) becomes a `PathPrebuilt` in `buildInitialRels`. The pre-search pipeline has
  already chosen an access method and attached local quals; the search re-costs but keeps
  the node. This is how the leaf-whitelist gap is closed.

- **Outer-spine peel for LEFT JOIN** — `splitOuterSpine` in `joinsearchseam.go` peels the
  pinned outer-join links off the top of the chain, searches the inner prefix below them,
  and splices the links back above the searched subtree. Without it, every TPC-DS query
  with an outer join (all 12 of them) would be declined from the search.

- **Join restriction (SpecialJoinInfo)** — `joinOrderRestricted` (`joinsearchlevel.go:68`)
  implements PG's `have_join_order_restriction`: LEFT/FULL/ANTI/SEMI joins impose ordering
  constraints the search must respect. SEMI unique-ification is handled as skip logic
  (joinrels.c:1095-1102 equivalent).

- **`hashJoinCost` is live in production** — since `GOOPG_PGSHAPED_DP` defaulted ON
  (2026-08-06), every hash join path in every live plan is priced through `costfuncs.go`.
  A change to any cost function carries the full planner bar (units + spot + DS05), not a
  unit test alone.

- **BITMAP scan paths** — added at M0128-P2.4. `addBitmapPaths` generates BitmapAnd/Or
  combinators with logical reordering for low-selectivity input order. The bitmap path
  kinds (`PathBitmapIndexScan`/`PathBitmapHeapScan`/`PathBitmapAnd`/`PathBitmapOr`) are
  the only multi-child path kinds in the system.

- **`inferAnchoredEqualities`** — a small-relation optimisation that derives `x=5` from
  `x=y AND y=5` only when the base relation has fewer than `smallAnchorRowsThreshold` rows.
  Prevents over-selective estimation bloat.

- **MERGE planning** — `planMerge` handles the full WHEN MATCHED/NOT MATCHED clause matrix:
  INSERT/UPDATE/DELETE/DO NOTHING actions, `MergeActionExpr` for RETURNING, `MergeWholeRowRef`
  for old/new row access. Distinct from INSERT ON CONFLICT.

- **Assertions in createPlan** — unhandled path kinds and nil children panic rather than
  silently producing wrong plans. `PathMemoize` outside an NLI inner panics explicitly.