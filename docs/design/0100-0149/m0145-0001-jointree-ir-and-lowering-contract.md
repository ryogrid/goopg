# M0145-0001 — the jointree-level IR and the lowering contract

Status: ESCALATED [!] 2026-09-24 (third time) — S4 lineage budget exhausted after the M0145-0008 flip landed (M0145-0004, -0026, -0026a, -0029, -0030 all Movement: none); three would-be children (0008b Q18/Q22 timing, 0008c Q95 ea finding, 0008d grouping direction) held in the escalation block in `.ralph/fix_plan.md` M0145-0001; owner decision pending. The 2026-09-23 escalation was answered option (a); the 2026-09-22 one GO — CONTINUE. Recon itself complete 2026-09-21.
ESCALATED `[!]` 2026-09-22 — the lineage budget for this root is exhausted:
its last five completed descendants (M0145-0006, M0145-0014, M0145-0015,
M0145-0016, M0145-0017) all carry `Movement: none`. No further descendant may
be filed or selected until the owner rules. The full write-up — what was
attempted, what each step proved, the two remaining walls (B-06 CTE-output
statistics, and the cost model that M0145-0018 ran into), expected movement if
unblocked, remaining size, and the deferred scope that could not be filed as
tasks — is in the M0145-0001 escalation block in `.ralph/fix_plan.md`.
Kind: recon
Parent: none
Movement: none

## 1. What this recon was asked to decide

The milestone doc (`docs/milestones/0145-jointree-first-planner-flow.md`) and
the medium-abstraction flow doc
(`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY4/plan-flow-medium-abstraction.md`,
divergences D1/D2/D4/D6) measured the same wall four ways: goopg's join search
consumes **lowered plan nodes**, where PG's consumes a **jointree of logical
relations**. The lateral-route recon
(`m0142-0008a-3i-lateral-route-recon.md`) pinned the earliest divergence to
`planner.go:1499` — `resolveExpr(whereQual, ctx)` runs `planExistsExpr` /
`planInExpr` / `planSubqueryExpr`, each of which calls `planSelectWithParent`
and keeps only the finished `Node`.

This recon's four deliverables:

1. The IR entry kinds — base-rel, semi/anti (SJInfo-equivalent), appendrel —
   plus `OuterColumnRef` re-basing semantics. §3.
2. Where the IR is built: it must precede `planner.go:1499`, so define
   whether resolution runs on the IR or the IR derives from
   resolved-but-unplanned subtrees. §4.
3. The Path→Node lowering contract: every piece of state the node-tree
   stages establish incrementally, each assigned a lowering-site owner. §5.
4. The retirement list: which seam guards die at which M0145 task. §6.

**Verdict up front:** the IR derives from `parser.FromExpr`/`parser.RangeVar`
items BEFORE `planFromClause` lowers anything, and **resolution runs on the
IR** — the binding pass resolves every clause expression against leaf spans
assigned at IR construction, while sublink bodies are bound recursively into
their own provisional scopes but left unplanned. That is PG's order
(rtable identity precedes Var binding; pull-up sees bound-but-unplanned
`Query`s) and it retires the rebase/remap family by construction rather than
by another coordinate translation.

## 2. The boundary defect, restated precisely

`planSelectWithSettings` today, WHERE arm (`internal/optimizer/planner.go`):

| line | statement | what it does |
|---|---|---|
| 1499 | `pred = resolveExpr(whereQual, ctx)` | binds names AND **plans every sublink body to a finished Node** via `planExistsExpr` (:15324) / `planInExpr` (:15282) → `planSelectWithParent` (:15666) |
| 1539 | `node = unnestSubqueriesInPlan(node)` | `unnest.go:424` — splices `Join{Semi/Anti}` over those finished plans |
| 1540 | `node = runJoinSearchBelowPinned(...)` | `predp.go:78` — Phase A/B searches node trees below/above the pinned spine |
| 1625 | `node = unnestSubqueriesInPlan(node)` | legacy-position second call |

PG's `subquery_planner` (`postgres/src/backend/optimizer/plan/planner.c:651`)
inverts the order: `pull_up_sublinks` (:737), `pull_up_subqueries` (:759),
`pull_up_simple_union_all` (`prepjointree.c:1617`), THEN
`SS_process_sublinks` (`subselect.c:2026`) plans only what pull-up refused,
THEN `query_planner` (:1654) searches the flattened jointree. The bodies PG
pulls up are analyzed-but-unplanned `Query` trees — route-a step 1 gave
goopg the equivalent retention (`ExistsExpr.Subquery`/`InExpr.Subquery`), so
the raw material for the same order now exists.

## 3. The IR (deliverable i)

### 3.1 Coordinate model: the flat space survives; spans move to construction

Keep `ColumnRef.Index` = position in a flat per-statement column space —
the evaluator, executor, and every downstream consumer already speak it.
What changes is **when** positions are assigned:

- **Today:** `planFromClause` assigns `rangeBinding.offset` while lowering
  each FROM item to a Node; any later structural change (splice, rewrite,
  demotion) invalidates coordinates and the rebase family
  (`remapWalkOrderFlatToSpans`, `rebaseChainQual`,
  `rebaseSemiAntiChainQual`, `layoutPosMap`, `remapByPosMap`,
  `remapSublinkOuterRefs`, `translateToLayout`) repairs them per pass.
- **IR:** leaf slots and spans `[lo,hi)` are assigned at IR construction —
  the leaf list IS the rtable analogue. Binding produces final coordinates
  once. Structural transforms **append** leaves (never insert mid-space),
  so every existing index stays valid — exactly PG's "pulled-up rtes
  append to the parent's range table" property.

Each leaf entry carries: `relid` (leaf-table index), `span`, `sourceIdx`
(the `SourceTableIdx` identity — allocated from the ONE statement scope, so
`remapSourceTableIdx`'s collision repair dies), `binding` (alias/table/
`columnOffsets`/`usingHidden`/`qualifiedOnly`), `schema`, and an `emits`
flag. **Non-emitting leaves** (semi/anti body leaves) get spans appended
after the emitting region — the out-of-band relocation `buildLeafSpans`
(`joinsearchseam.go:1752`) already implements, moved from
walk-reconstruction to construction.

### 3.2 Entry kinds

The IR is a jointree — PG's `FromExpr`/`JoinExpr`/`RangeTblRef` triad plus
the appendrel, mirroring `prepjointree.c`'s inputs. Node kinds:

| kind | PG analogue | carries |
|---|---|---|
| `jtLeafRef` | `RangeTblRef` | leaf-table index (relid) |
| `jtFromExpr` | `FromExpr` | flat item list + bound quals (the statement's top jointree) |
| `jtJoinExpr` | `JoinExpr` | jointype (incl. Semi/Anti), `larg`/`rarg` subtrees, bound ON/correlation quals, `sjinfo` for outer/semi/anti |
| `jtAppendRel` | `AppendRelInfo` parent | ordered child subtrees + per-child column maps |

And the leaf table entries (`jtLeaf`) — PG's `RangeTblEntry` kinds:

| leaf kind | source | schema source |
|---|---|---|
| table | `RangeVar` plain ref | catalog lookup (today `planScanRangeVar`, planner.go:4061) |
| view-expanded | view expansion | expansion body's bound tlist |
| derived (opaque) | `RangeVar.Subquery` | body's **bound target list** — see §4.3 |
| values / tablefunc | `ValuesRows` / `TableFuncRef` | row types / signature |
| cte-ref | `RangeVar` matching `planCTEs` | `plannedCTE` schema |
| sublink-body (opaque) | non-simple `ExistsExpr/InExpr.Subquery` | body's bound tlist |

Every leaf also carries `localQuals` (restriction conjuncts attributed to
it) and, for LATERAL bodies, `lateralDeps` — the relid set the bound body
references, replacing `chainCarriesLateral`'s node-walk detection with a
construction-time property.

### 3.3 `SpecialJoinInfo` is already the SJInfo — carry it

`internal/optimizer/specialjoin.go` already holds `MinLefthand`,
`MinRighthand`, `SynLefthand`, `SynRighthand`, `Jointype`, `Ojrelid`, the
outer-join commute sets, `LhsStrict`, and the semi-join operator metadata —
produced today by `deconstructJointreeScopedSJI` (`collapse.go:377`) for
outer joins and by `existsUnnestSJInfo`/`inUnnestSJInfo` (`unnest.go:4547`,
`:4649`) for unnested semi/anti. Under the IR both producers move to IR
construction: outer-join SJInfos at deconstruction (same walk), semi/anti
SJInfos at pull-up splice (same fields). The IR carries the statement's
`join_info_list` on the scope; `joinIsLegal` (`joinsearchlevel.go:198`,
the `join_is_legal` port) consumes it unchanged.

`joinlist`/`joinlistItem` (`collapse.go:153`) — `{rel, sub, jointype,
sjinfo}` over `parser.FromExprs` — is the existing proto-jointree and the
natural substrate: it already deconstructs at the right level. Its
`rel int` leaf indexing becomes IR leaf identity; what it lacks (leaf
schema/binding/body, appendrel members, bound quals) is what the IR adds.

### 3.4 `OuterColumnRef` re-basing semantics

`OuterColumnRef{Level, Index, Name, Type, SourceTableIdx}` (plan.go:489):
`Level` counts lexical scope hops (the `planParent` chain; `lateralSibling`
does not increment), `Index` is the flat position in the scope `Level` hops
up, `SourceTableIdx` pins the source range-var identity.

**Splice rule** (a body scope removed by flattening):

- `OuterColumnRef{Level:1}` → same-scope reference: `ColumnRef{Index:
  Index}`. `Index` already addresses the parent's flat space and spliced
  leaves append, so no existing index moves.
- `OuterColumnRef{Level:k≥2}` → `OuterColumnRef{Level:k-1}` — the body
  scope is gone, so the grandparent is now one hop up.
- Body-local `ColumnRef{Index:i}` → `ColumnRef{Index: leaf[i].span.lo +
  (i - leafLocalOffset(i))}` — each body leaf's local offset table maps
  into its assigned parent-space span. Same table the seam reconstructs
  today (`leafSpans`), assigned here at splice.

**Fail-closed firewall (Q78 `outer-over-derived`).** A re-based ref is
legal only if `(Index, SourceTableIdx)` resolves to an **emitting leaf in
the destination scope's own leaf table**. If the ref lands on a leaf inside
an opaque body's private space (a column that only exists below a derived
boundary), or the SourceTableIdx doesn't match the leaf the Index lands
in, the splice refuses — the sublink stays a sublink. This is the binding-
time expression of the firewall; PG's equivalent is that a Var's
`varno`/`varattno` either resolves in the rtable or the pull-up path never
runs.

**Appendrel direction is different.** Parent→child translation is not an
OCR operation: each appendrel child carries a `translated_vars` analogue —
per-child map from parent column position to child expression/column.
Outer quals bound to the parent column space translate per child when
pushed into child pathlists; child outputs map up through the same table
for the append output.

### 3.5 Type sketch

```go
type jtScope struct {              // PlannerInfo-analogue, per statement
    parent   *jtScope              // lexical parent — OCR Level semantics
    leaves   []jtLeaf              // rtable analogue; index = relid
    top      *jtFromExpr           // the jointree root
    sjinfos  []*SpecialJoinInfo    // join_info_list
    nextSrc  int16                 // SourceTableIdx allocator, scope-wide
    // statement-level search inputs (bound once, §5):
    neededCols, outputCols map[string]bool
    queryPathkeys                  []PathKey
    tupleFraction                  float64
}
type jtNode interface{ jt() }
type jtLeafRef   struct{ leaf int }
type jtFromExpr  struct{ items []jtNode; quals []Expr }
type jtJoinExpr  struct{ jointype JoinType; larg, rarg jtNode; quals []Expr; sjinfo *SpecialJoinInfo }
type jtAppendRel struct{ parent jtLeafRef; children []jtNode; colMaps [][]Expr }

type jtLeaf struct {
    binding  rangeBinding
    schema   Schema
    emits    bool
    kind     leafKind              // table|view|derived|values|tablefunc|cte|opaqueBody
    rv       *parser.RangeVar      // TableSample, Lateral, ONLY, alias
    body     *jtScope              // bound-but-unplanned body scope (derived/sublink bodies)
    planned  Node                  // filled when an opaque body is planned (§4.3)
    lateralDeps RelSet
    localQuals  []Expr
}
```

`joinlistProblem` (`relfromjoinlist.go`) already splits almost cleanly:
bindings/spans/SJInfos/neededCols/outputCols/pathkeys/tupleFraction/
catalog are **statement-level** (move to `jtScope`); scans/conjunct
partitions/relMap are **problem-level** (the search builds them FROM the
IR); `spineAbove`/`pinAbove`/`corrAbove` are **transitional seam artifacts**
that die with the spine (§6).

## 4. Where the IR is built (deliverable ii)

### 4.1 The decision: resolution runs ON the IR

Order in the new pipeline (per `planSelectWithSettings` invocation):

1. **IR skeleton** — deconstruct `s.FromExprs` into leaf entries +
   `jtJoinExpr`s; assign every leaf its relid/span/sourceIdx; run the
   jointree-level transforms that already operate at parser level
   (`reduceOuterJoins`/`demotedForPlan`, reduce_outer_joins.go:37/:102).
2. **Binding pass** — resolve ON quals, WHERE, target list against the
   leaf space. `resolveExpr` keeps its name-binding job and **loses the
   sublink-planning side effect** (§4.3).
3. **Pull-up transforms** — sublink pull-up (0003) splices flattenable
   bodies' leaf entries into the parent scope; appendrel flattening
   (0004) does the same for UNION ALL branches.
4. **Search** — the DP consumes the IR directly (0005).
5. **Upper-rel pathlists** (0006), then **one lowering** (0007).

### 4.2 Why the alternative was rejected

"IR derives from resolved-but-unplanned subtrees" means binding names
before leaf identity exists — but goopg's name binding IS the coordinate
assignment: `resolveExpr` resolves a `parser.ColumnRef{qualifier,name}` to
`ColumnRef{Index}` by looking it up in `ctx.bindings`, and `Index` is the
flat position the leaf's span defines. Binding without spans either
produces an intermediate coordinate space (yet another rebase family —
the thing this design retires) or forces spans to be assigned inside the
resolver anyway. PG settles the same dependency identically: the rtable
is assigned during parse analysis and `Var.varno` carries it. Resolution
on the IR is also the cheaper path: `deconstructJointreeScopedSJI`
already walks `parser.FromExprs` at the right level, and `planFromItem`'s
incremental binding-space construction (planner.go:3683) is the binding
half — only the Node-lowering half moves out.

### 4.3 The deferred-sublink mechanism

`resolveExpr`'s sublink arms currently call `planSelectWithParent` to a
finished Node. Under the new pipeline:

- `planExistsExpr`/`planInExpr` bind the expression and retain
  `.Subquery` (already retained) with `Plan` left nil — a bound
  `ExistsExpr`/`InExpr`, not a planned one.
- Each retained body gets a **provisional body scope**: its own `jtScope`
  built recursively (its FromExprs deconstructed, its quals bound against
  body leaves + parent scope → `OuterColumnRef{Level:1}` marks
  correlation). This is PG's "the SubLink's Query is analyzed but not
  planned" — and it is also what answers "schema without planning" for
  derived-table leaves: a leaf's output schema = its body's bound target
  list, no paths needed.
- `IsNonCorrelated` moves from `planHasOuterRef(planned)` to
  "the bound body contains no `OuterColumnRef`" — same information,
  earlier.
- **Pull-up** consumes the body scope: simple bodies
  (`sublinkBodyIsSimple`, sublinkpullup.go:73 — the `is_simple_subquery`
  port) splice per §3.4; non-simple bodies stay as `opaqueBody` leaves.
- **Survivors** — non-flattenable bodies and scalar-family sublinks —
  are planned by an `SS_process_sublinks` analogue AFTER pull-up: a
  recursive `planSelectWithSettings` on the body scope (which reuses its
  bound IR rather than re-resolving), producing the `Node` the leaf
  carries as `planned`. The semi/anti entry whose body failed flattening
  carries this Node as its inner — a real citizen, not a spine member.

The same mechanism covers FROM-clause derived tables (`pull_up_subqueries`
second call site): a derived leaf's body scope either flattens into the
parent jointree at the leaf's position or plans wholesale as the opaque
leaf. One asymmetry to flag: derived-table flattening substitutes outer
refs **by expression** — a ref to the derived leaf's column `k` maps to
the body's bound tlist entry `k` (an arbitrary expression over spliced
leaves), which is `pullup_replace_vars` (`prepjointree.c:2623`), a real
substitution pass, heavier than the index translation §3.4 gives for
sublink splices. OCRs inside substituted expressions re-base by the same
Level-decrement rule.

### 4.4 What happens to the existing functions

- `planFromClause` (planner.go:3422) splits: its binding/deconstruction
  half becomes IR construction; its Node-lowering half moves to lowering.
- `planScanRangeVar`/`planSubqueryRangeVar` (:4061/:5191) split the same
  way: schema+binding → leaf construction; Node → lowering/base-rel
  pathgen.
- `resolveExpr` (:15830) loses the three sublink-planning arms'
  `planSelectWithParent` calls on the new pipeline; the binding half is
  unchanged.
- `planSelectWithParent` (:15666) becomes the recursive entry the
  SS_process_sublinks analogue calls on survivor body scopes — and stays
  the CTE/derived boundary planner meanwhile.
- `mergeResolveContexts`/`planParent`/`lateralSibling` (:685) become
  scope-chain plumbing on `jtScope.parent`.

## 5. The Path→Node lowering contract (deliverable iii)

### 5.1 The shape

PG keeps `Path`s alive through base rels, join search, and every upper
rel, then converts ONCE in `create_plan`. goopg's `createPlanNode`
(`createplan.go:44`) is already the funnel — it takes a `*Path` and
returns `(Node, outputLayout)`. The contract: **the elected path tree is
lowered exactly once**, and every output-layout-dependent translation
happens inside that walk against the `outputLayout` each child returns —
the mechanism `translateToLayout` (`createplanjoin.go:205`) already
implements per join. What retires is not the mechanism but the *second
and subsequent* translations: nothing outside the lowering walk may
re-establish coordinates.

### 5.2 State inventory and owner assignment

Every piece of state the node-tree stages establish incrementally, its
current site, and its owner in the new pipeline. Owners: **IR-CON** = IR
construction, **BIND** = binding pass, **PULL** = pull-up transforms,
**SEARCH** = DP, **UPPER** = upper-rel pathlists, **LOWER** = single
lowering, **TAIL** = statement-tail passes (PG keeps equivalents, so
these survive).

| state item | current site | owner |
|---|---|---|
| leaf bindings / flat spans / `sourceIdx` | `planFromClause`/`planFromItem` walk | IR-CON (assigned once; append-only) |
| `joinlist` + `joinInfoList` | `deconstructJointreeScopedSJI` (collapse.go:377) | IR-CON — the IR's own deconstruction |
| outer-join reduction / demotion | `reduceOuterJoins`/`demotedForPlan` | IR-CON (already parser-level; mutates IR instead) |
| `antiForcedNullCols` + `stripForcingNullQuals` | ctx flag + pre-resolve strip (reduce_outer_joins.go:777) | BIND — produced by demotion, consumed at qual binding |
| `neededCols`/`outputCols` | `neededColumnNames`/`outputColumnNames` (pathindexonlyneed.go:34/:72) on `ctx` | BIND — computed once on the bound scope |
| `queryPathkeys`/`tupleFraction` | `deriveQueryPathkeys`/`searchTupleFraction` | BIND — scope fields |
| `canonicalizeQual`/`foldQualConstants` | pre-resolve + pre-search | BIND — `preprocess_expression` analogue |
| `injectLikeRangePredicates`/`reduceNotNullQuals` | `isSimpleSingle` arm (planner.go:1428/:1457) | BIND — leaf-qual preprocessing, all statements |
| sublink body `Node`s | `planExistsExpr`/`planInExpr` eager planning | PULL decides flatten-vs-defer; survivors planned by the SS_process_sublinks analogue |
| semi/anti `Join` nodes + SJInfo + `FlattenedRHS` | `unnestSubqueriesInPlan` family (unnest.go:424) | PULL — `jtJoinExpr{semi/anti}` entries |
| `SourceTableIdx` collision repair | `remapSourceTableIdx` (unnest.go:2035) | **dies** — single-scope allocation |
| UNION ALL `*SetOp` leaf / `setOpBranchTag` carrier | `createSetOpPaths` + `setopbranchrel.go` | PULL (0004) — `jtAppendRel` entry; top-level SetOp stays an UPPER producer |
| DP inputs (leaf rels, conjunct partition, ECs) | `extractSearchLeaves` + `partitionConjunctsForJoinPlanning` over node tree | SEARCH — built directly from IR; `inferTransitiveEqualities`/`deriveOuterLinkConstants` port as clause-machinery helpers |
| nullable-side placement proofs | `outerOnQualsOK`/`innerOnQualsBelowNullableOK`/`semiAntiOnQualsOK`/`prefixNullable`/`delayedAboveOJ` | SEARCH — the *semantics* migrate into legality + qual placement (`check_outerjoin_delay`/`distribute_restrictinfo_to_rels` analogues); the decline classes die |
| `outer-over-derived` firewall | seam decline class | BIND/SEARCH — fail-closed at re-base (§3.4) AND legality |
| NLI probe join construction | `rewriteJoinsToNLI` (nl_index_join.go:89) node rewrite | SEARCH — parameterised paths elect NLI; LOWER builds probe keys/`InnerIsUnique` from the elected path |
| `stampSemiProbePrices` | nlipricesplice.go:31 node pass | **dies** — probe cost rides the path |
| qual redistribution on nodes | `pushPredicatesIntoCrossJoins`, `pushSingleSideQualsIntoInnerJoinInputs`, `rewriteScanInputsWithSingleTablePredicates`, `pushOuterQualsIntoLaterals` | **dies** — clause distribution places quals on relsets pre-pathgen; lateral push semantics migrate into legality |
| coordinate rebase family | `remapWalkOrderFlatToSpans`, `rebaseChainQual`, `rebaseSemiAntiChainQual`, `localizeExprToLeaf`, `buildLeafSpans`, `remapSourceTableIdx`, `remapSubqueryColumnRefs` | **dies** — spans at construction; binding handles aliases |
| splice/re-resolution family | `spliceSearchedSpine`, `layoutPosMap`, `remapByPosMap`, `remapSublinkOuterRefs`, `reresolveJoinByName`, `applyJoinTreePosMap`/`remapPosMapAfterRewrite`/`remapWithBindings` | **dies** — single lowering translates once via `translateToLayout` |
| `OuterColumnRef` → outer-layout mapping | `remapOuterRefsInSubplan` (joinlayout.go:346) | LOWER — each lowered node publishes binding-space→`outputLayout` map; subplan OCRs translate once (the `SS_replace_correlation_vars` positioning analogue) |
| `OuterColumnRef` → `ExecParamRef` + `ParParam`/`Args` | `lowerSubPlanParams` (subplan_lower.go:112) | TAIL — survives verbatim; opportunistic/atomic contract stands |
| `rewriteExistsToAny` | exists_to_any.go:104 | TAIL — `convert_EXISTS_to_ANY` analogue for EXISTS that stayed a sublink |
| aggregate stage surface | `buildAggregateStage` (planner.go:8329) + `createGroupingPaths` election | UPPER — GROUP_AGG/PARTIAL_GROUP_AGG rel pathlists; HAVING = residual-qual placement |
| window stage | `buildWindowStage` (:7578) + `windowSurface` | UPPER — WINDOW rel |
| distinct / distinct-on | `createDistinctPaths`, `electOrderedDistinct`, `DistinctOn` construction (planner.go:2288-2371) | UPPER — DISTINCT/PARTIAL_DISTINCT rels; validation logic survives |
| ordered election | `createOrderedPaths`, `electOrderedGrouping`, `distinctOutputSatisfiesOrder`, `inputNodePathkeys` | UPPER — ORDERED rel over full input pathlist; the node-top gates die (0006) |
| sort/limit nodes | ORDER BY resolution + deferred LIMIT + `liftLimitAboveLockRows` | LOWER — fixed node order (Sort→Limit→LockRows per `create_plan`'s upper stages) |
| SRF / `ProjectSet` | `buildSelectSrfProjectSet` (:5451) + `selectSrfPreSort` | LOWER — fixed position in the stack (PG evaluates tlist SRFs in `create_plan`); flagged boundary item if 0006 finds it needs election |
| top-level target `*Project` | `resolveTargets` + final Project | LOWER — the elected path's tlist emission |
| `stampAggregateInputTarget`/`stampSortInputTarget`/`narrowOrderedRelWidths`/`window_sort_narrow`/`aggInputWidth` | pre-cost width stamps (group_input_target.go:268, sort_input_target.go:202) | UPPER — the `set_pathtarget_cost_width` inputs to path cost |
| `applyUpperNarrowing` | upper_narrow_apply.go:116 | TAIL — stays the commit step (D5 established costing-vs-commit) |
| index-only promotion | `tryPromoteIndexOnlyScan`/`tryPromoteOrderedIndexOnlyScan` | LOWER — index-only-ness rides the elected path; post-hoc wrap dies |
| rowmarks | `resolveLockedRels`/`wireRowMarkCtidColumns`/`rebaseRowMarkPlan`/`resolveRowMarkCtidResnos`/`liftLimitAboveLockRows` | BIND computes `LockedRel`s (`preprocess_rowmarks` analogue); LOWER wires ctid resjunk + places `LockRows`; `rebaseRowMarkPlan` dies |
| CTE cache/refcount/inline | `preplanWithClause`, `planCTEs`, `pushQualsThroughSingleRefCTEs`, `wrapDMLCTEPrefix` | scope mechanism survives — `cte-ref` leaves carry `plannedCTE`; refcount-1 qual push stays TAIL until D6 (`inline_cte`) is attempted |
| `fillJoinHashKeys` | join_hash_keys.go:106 | LOWER — folded into join construction from the elected path |
| `isSimpleSingle` bypass + `planIndexScanFromWhere` + `oneRelSearch` + `joinTreeHasOuterLink` | planner.go:1257/:1283/:1425/:17404 | **dies** — single rel flows through base-rel pathlists (`set_base_rel_pathlists` analogue, allpaths.c:333) |
| early skeleton rewrites | `tryPromoteAggSublink` (:7457), `rewriteMinMaxAggregates`/`wrapMinMaxOrderByDistinct` (:11086) | survive — `preprocess_minmax_aggregates`/`planagg.c` analogues on the bound skeleton, pre-search |
| `foldPlanConstants` | foldconst.go:105 | TAIL — final fold stays |
| `assertSearchedBoundariesIntact` | post-search check | TAIL — becomes an assertion over the lowered tree |
| `rtableScope`/RTID allocation | `scope *rtableScope` | IR-CON — leaf allocation consumes RTIDs |
| `pinAbove`/`spineAbove`/`corrAbove` | `joinlistProblem` narrowing-decline flags | **dies** — the problem knows its boundary |
| executor-capability refusals | parallel_hash refusal, partial-nestloop whitelist | SEARCH — stay in pathgen until D3 substrate lands (hard rule, §8) |

## 6. Retirement list (deliverable iv)

| mechanism | retires at | replacement / note |
|---|---|---|
| `GOOPG_JOINTREE_PIPELINE` knob | added 0002, retired 0008 | default flips at cutover |
| `whereEligibleForPreDPUnnest`, `preDPUnnested`, `GOOPG_UNNEST_PREDP` | 0003 | pull-up is unconditional on the new pipeline |
| `runJoinSearchBelowPinned` Phase A/B + `spliceSearchedSpine` + spine re-resolution (`reresolveJoinByName`, `remapSublinkOuterRefs`, `layoutPosMap`, spine `remapByPosMap`) | 0003 | no spine — semi/anti are entries |
| `admitSemiAnti` call-site restriction + `semiAntiChainLink`/`flattened`/`bodyQuals` synthetic-leaf contract | 0003 | real IR entries |
| `Join.FlattenedRHS` + `decomposeFlatBodyTree` + `flatBodyScopeProject` + `schemaIsLeafConcat` | 0003 | step-2 bridge ported onto IR — body leaves are first-class |
| `unnestSubqueriesInPlan`/`unnestExistsExpr`/`unnestInExpr`/`unnestNonCorrelatedInExpr` + `collectUnnestParamsAndResiduals`/`clonePlanReplacingOuter`/`stripOuterRefConjuncts`/`unwrapTrivialWrappers`/`findFilterContainingExistsExpr` | 0003 | IR pull-up; `existsUnnestSJInfo`/`inUnnestSJInfo` migrate to IR construction |
| `remapSourceTableIdx` | 0003 | single-scope allocation |
| `resolveExpr` eager `planExistsExpr`/`planInExpr` arm | 0003 | deferred-sublink mechanism (§4.3) |
| `leaf-count` + `nil-leaf` + `chain-not-flattenable` + `flat-leaf-not-scan` + `leaf-count-overflow` + `semianti-*` decline families | 0003–0005 | node-tree decomposition artefacts; `maxSearchRels` bit-width cap stays as a hard limit |
| `lateral` decline (`chainCarriesLateral`) | 0003–0005 | lateral = entry flag + `lateralDeps` relset; legality constrains |
| `setOpBranchTag`/`setOpBranchRelNode`/`setOpBranchRelOf`/`stampSetOpBranchRel` | 0004 (UNION-ALL-in-FROM half) | appendrel rels carry partial paths directly; residual for genuine SetOp dies at 0006–0007 |
| `splitOuterSpine` + `outer-spine`/`prefix-*`/`spine-width`/`offset-disagreement`/`residual-hits-pad` declines | 0005 | single-pass DP; outer joins are SJInfo entries |
| `tryJoinSearch`/`tryPGShapedJoinSearch`/`extractSearchLeaves`/`joinlistProblem`-over-walk | 0005 | problem built from IR; `planJoinlistSearch`/`joinSearch`/`makeJoinRel`/`joinIsLegal` survive and consume it |
| `outer-link-no-sjinfo`/`semianti-link-no-sjinfo` fail-closed checks | 0005 | SJInfos exist by construction |
| `isSimpleSingle` bypass + `GOOPG_ONEREL_SEARCH` + `joinTreeHasOuterLink` arm | 0005 | every statement searches |
| `heldAbovePrefix` Filter reconstruction | 0005 | DP emits residual quals, not Filter wrappers |
| `outer-on-qual`/`inner-on-qual-*`/`outer-over-derived`/`residual-hits-pad` decline classes | 0005 | **migrate** — semantics port into legality/clause placement, not declined |
| `pushPredicatesIntoCrossJoins`/`pushSingleSideQualsIntoInnerJoinInputs`/`rewriteScanInputsWithSingleTablePredicates`/`pushOuterQualsIntoLaterals` | 0005 | clause distribution |
| `localizeExprToLeaf`/`rebaseChainQual`/`rebaseSemiAntiChainQual`/`remapWalkOrderFlatToSpans`/`buildLeafSpans`-as-walk | 0005 | coordinate model makes them dead |
| stage-builder elections: `electOrderedGrouping` gates, `electOrderedDistinct` `cands<2`, `inputNodePathkeys` default-nil tops, `gate-precondition` skip | 0006 | upper-rel pathlists iterate full candidate sets |
| `wrapSetOpSortLimit` ordering | 0006–0007 | lowering's fixed stack |
| `rewriteJoinsToNLI` + `stampSemiProbePrices` | 0007 | NLI is a Path kind; probe cost rides the path |
| `fillJoinHashKeys` as a pass | 0007 | lowering's join arm |
| `remapSubqueryColumnRefs`, `applyJoinTreePosMap`/`remapPosMapAfterRewrite`/`remapWithBindings`/`remapExprRefsToMHJ`, `translateToLayout` as separate calls | 0007 | one translation inside `createPlanNode` |
| `tryPromoteIndexOnlyScan`/`tryPromoteOrderedIndexOnlyScan` post-hoc wraps | 0007 | elected-path property |
| `wireRowMarkCtidColumns`/`rebaseRowMarkPlan`/`resolveRowMarkCtidResnos`/`liftLimitAboveLockRows` as passes | 0007 | lowering's rowmark arm + fixed stack order |
| legacy pipeline wholesale: `tryJoinSearch`, `runJoinSearchBelowPinned`, unnest call sites (:1539/:1625), the decline census family, `GOOPG_UNNEST_PREDP`/`GOOPG_PGSHAPED_DP`/`GOOPG_ONEREL_SEARCH` knobs, `planFromClause` node-lowering, `joinlist` proto-IR | 0008 | deletion at cutover; `GOOPG_JOINTREE_PIPELINE` retires itself |
| `lowerSubPlanParams`, `rewriteExistsToAny`, `pushQualsThroughSingleRefCTEs`, `applyUpperNarrowing`, `foldPlanConstants`, `assertSearchedBoundariesIntact`, CTE machinery, early skeleton rewrites | **never** | PG keeps equivalents — assigned TAIL/scope owners in §5.2 |

## 7. Statement-level vs problem-level state

`joinlistProblem` (`relfromjoinlist.go`) is the boundary marker for what
the IR owns vs what a search problem owns:

- **Statement-level (moves to `jtScope`):** leaf bindings + spans,
  `joinInfoList`, `neededCols`, `outputCols`, `queryPathkeys`,
  `tupleFraction`, catalog, cost params.
- **Problem-level (search builds from IR):** rel set → `RelOptInfo` map,
  conjuncts partitioned to relsets, joinrels, the `searchCtx`.
- **Transitional (dies):** `spineAbove`/`pinAbove`/`corrAbove` — flags
  that exist only because the problem is carved out of a node tree below
  a pinned spine.

`RelOptInfo` (`path.go`) keeps its current contents — Relids/Rows/Width/
NCols/needed/output/keep column sets/AvgVarBytes/partial+candidate
pathlists — and gains nothing IR-level: the IR is what *produces* rels,
not what rels carry. `upperRels` (`upperrel.go:76`) stays per
`planSelectWithSettings` invocation (the registry's DESIGN §3.2 boundary
is correct and survives untouched).

## 8. Boundaries and risks

- **Q78 `outer-over-derived` firewall** — hard owner constraint, §3.4:
  re-base fails closed; additionally the qual-placement semantics the
  seam's decline classes encode (nullable-side placement, ON-qual
  position vs outer joins) must be *ported* into legality/clause
  distribution, not dropped. The declines are artefacts; the rules are
  not.
- **Executor substrate stays out of scope** — parallel-hash-build,
  row-emitting PartialAgg, Materialize sizing (D3, M0144-0011c): path
  generation keeps refusing shapes the executor cannot run. 0008 records
  the residual `unexpressible` set for the executor milestone.
- **Derived-table flattening needs `pullup_replace_vars`** — expression
  substitution, not index translation (§4.3). Sized as real work in
  0003's scope note if FROM-subqueries are pulled in the same task.
- **CTE boundary unchanged** — `inline_cte` (D6) is not an M0145 task;
  `cte-ref` leaves carry `plannedCTE` identity and refcount semantics
  stay.
- **Set-op halves split** — UNION ALL in FROM is an IR appendrel (0004);
  statement-level UNION/INTERSECT/EXCEPT stays an UPPER producer.
- **Binding-order dependency** — parent binding needs derived/body leaf
  schemas, which need body scopes bound first: leaf slots assign
  top-down, body scopes construct lazily during binding. Implementation
  detail for 0003, recorded here because it constrains construction
  order.

## 9. Reporting

- `CATEGORIES:` / `CATEGORIES-EXCL-MATCH:` / MATCH count — **N/A**: no
  production file changed; no pipeline behaviour altered; nothing to
  capture.
- Shape deltas, statistics epochs, seam-decline census — **N/A**, same
  reason.
- Planning route — **N/A**; this doc designs the route change.
- Wall time / ledger rows — **N/A**: no query's plan or runtime changed.
- Movement: none (recon).
