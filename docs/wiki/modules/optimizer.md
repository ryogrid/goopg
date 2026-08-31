# Module: `internal/optimizer`

The SQL planner. Converts a `parser.Stmt` (the analyzer-resolved AST) into an
executor plan tree of `Node`s: name/type resolution of expressions, FROM-clause
and subquery planning, sublink pull-up / unnesting to semi/anti joins, a
PG-shaped join-order search with cost-based cardinality estimation, plan-node
construction, and late rewrite passes (NLI conversion, min/max-to-index-scan,
parallel Gather, PARAM_EXEC lowering).

## Responsibilities

- Statement dispatch (SELECT/INSERT/UPDATE/DELETE/MERGE, DDL passthrough, utility).
- `planSelect` pipeline — FROM → WHERE → unnest → join search → aggregate/window
  → sort/distinct/limit.
- Join-order search (a faithful reproduction of PG's `standard_join_search` level
  lists since M0127-P5.9), capped at 16 base relations per join problem.
- Per-path cost modelling and per-node cardinality estimation (PG-default
  selectivities when stats are absent).
- Plan IR: every executor-facing `Node`/`Expr` type.

## Key Files

- `planner.go` (15,262) — `Plan()` entry, statement dispatch, `planSelect`
  pipeline, view-recursion guard, min/max rewrite.
- `plan.go` (2,672) — the plan IR: `Node`/`Expr` interfaces, `Schema`, and every
  plan-node struct (`SeqScan`, `IndexScan`, `Join`, `NestedLoopIndexJoin`,
  `Aggregate`, `WindowAgg`, `Sort`, `Limit`, insert/update/delete/merge, bitmap
  scans, `Gather`).
- `unnest.go` (4,311) — subquery pull-up and correlated-subquery decorrelation
  to semi/anti joins.
- `joinsearch.go` (636) / `joinsearchlevel.go` (602) / `joinsearchseam.go` (636)
  — the PG-shaped join-search substrate (`searchCtx`, level lists,
  `joinSearchOneLevel`).
- `nl_index_join.go` (1,418) — `rewriteJoinsToNLI` rule pass.
- `cardinality.go` (1,330) — `EstimateRows` bottom-up row estimation.
- `path.go` (709) — cost-model substrate: `Path`, `RelOptInfo`, `Cost`,
  `RelSet`, `PathKind`, add_path/set_cheapest dominance.
- `cost_funcs.go` / `joincost.go` / `costindex.go` / `costbitmap.go` — PG cost
  function ports (`costParams`, `costSeqscan`, `hashJoinCost`, …).
- `joinlayout.go` (1,350) — column-coordinate translation (pos-map family,
  by-name re-resolvers).
- `createplan*.go` — `createPlan`/`createPlanNode`: Path → executor-Node.

## Public API

The exported surface is intentionally tiny (~10 functions); most machinery is
unexported.

```go
func Plan(stmt parser.Stmt, cat catalog.Catalog) (Node, error)          // planner.go:89
func PlanSchemaOnly(s *parser.SelectStmt, cat catalog.Catalog) (Node, error) // planner.go:51
func ResolveIndexPredicate(predicate parser.Expr, tbl *catalog.Table) (Expr, error)
func ResolveAlterColumnTypeUsing(table *catalog.Table, e parser.Expr) (Expr, error)
type PlanError struct{ Pos, Code, Message, Hint, Detail }               // planner.go:28
func EstimateRows(n Node) int64                                         // cardinality.go:43
func IsSmallDimensionSide(n Node) bool                                  // cardinality.go:482
```

Feature gates (all wrap package atomics): `SetUnnestPreDPEnabled`,
`SetSubqueryUnnestEnabled`, `SetIndexKeyHarvestEnabled`, `SetNLIEnabled`,
`SetNLICostGateLegacy`, `SetParallelEnabled`/`ParallelEnabled`, and the
`FlagProvenanceTable`/`RenderFlagProvenanceEnv` benchmark-artefact helpers.

## Internal structure

- **Statement dispatch** — `planStmt` (`planner.go:142`) switches on `parser.Stmt`;
  DDL becomes a passthrough `*DDL`; transactions map to `*Transaction`/`*Utility`.
- **`planSelect` pipeline** (`planner.go:741`) — roughly mirrors PG's
  `subquery_planner`/`grouping_planner`: grouping-set normalization → CTE
  pre-planning → set-op flattening → FROM planning → WHERE (aggregate-rejection
  42803, `canonicalizeQual`, single-table fast paths) → join search → rule passes
  → aggregate/window stages → sort/distinct/limit.
- **Join search** — `tryJoinSearch` → `tryPGShapedJoinSearch` → `newSearchCtx` +
  `buildInitialRels` → `joinSearch` (levels 2..nrels via `joinSearchOneLevel`,
  phase 1 left/right-sided + clauseless, phase 2 bushy only when clause-connected,
  phase 3 empty-level escape) → `setCheapest` per rel.
- **Path → plan** — `createPlanNode` switches on `PathKind`, threading an
  `outputLayout` coordinate map; a `searchedTree` marker stops legacy pos-map
  passes from double-firing on searched subtrees.
- **Representation bounds** — `RelSet uint16` → `maxSearchRels = 16`; no GEQO.

## Dependencies

- **Uses** `internal/catalog`, `internal/parser` (+ `parser/analyzer`),
  `internal/executor/hashsize` (work_mem / hash-entry sizing shared with the
  executor), `internal/storage`.
- **Used by** `internal/executor` (30+ files) — builds operators by
  type-switching on `optimizer.Node`, evaluates `optimizer.Expr`, and re-enters
  the planner for dynamic SQL (PL/pgSQL bodies, FK/partition DDL, `CALL`).

## Notable patterns / gotchas

- **Two planning worlds must agree** — the legacy rule-driven pipeline and the
  PG-shaped cost search coexist; the `searchedTree` tag is what stops legacy
  passes from double-firing (a searched tree re-remapped would read ColumnRefs
  against the wrong coordinate space).
- **Coordinate spaces are the #1 silent-bug source** — column refs are resolved
  in binding-offset space; the search reorders rels; `outputLayout` and the
  `joinlayout.go` pos-maps are the translators.
- **Env-flag kill-switch culture** — every plan-shaping feature is gated by a
  process-start env var (`GOOPG_PGSHAPED_DP`, `GOOPG_UNNEST_PREDP`,
  `GOOPG_EXISTS_TO_ANY`, `GOOPG_MEMOIZE`, `GOOPG_PARALLEL`, …); benchmark gates
  stamp `planner-flags:` provenance lines.
- **Cost model is PG's, deliberately shallow at `pathkeys`** — `Path` dominance
  uses `stdFuzzFactor = 1.01`; `pathkeys` are syntactic, not equivalence-class
  driven (a false-negative on sort elimination, never a wrong plan).
- **Cardinality defaults match PG** when stats are absent (eq = 1/200,
  ineq/generic = 1/3); `EstimateRows` returns 0 meaning "no stats", not "zero rows".
- **Parallel pass is non-mutating** — `MaybeAddGather` wraps a finished plan
  because the plan cache is process-wide/cross-session.
