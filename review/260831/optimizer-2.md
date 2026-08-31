# Optimizer (Part 2) — Code Review 2026-08-31

Files: flaglabels.go, foldconst.go, groupagg_hashagg.go, groupagg_indexorder.go, groupagg_presorted.go, groupby_alias_key.go, groupingsets.go, inner_join_qual_pushdown.go, join_exec_keys.go, join_hash_keys.go, join_hashkey.go, joincost.go, joinkeyproof.go, joinlayout.go, joinorder.go, joinpaths.go, joinpathsmemoize.go, joinpathsmerge.go, joinpathsmergeouter.go, joinpathsnli.go, joinrelsize.go, joinrestrict.go, joinsearch.go, joinsearchlevel.go, joinsearchseam.go, joinsearchtrace.go, joinselectivity.go, likeprefix.go, local_filters.go, memoize.go, nl_index_join.go, nl_index_join_selectivity.go, notnull_qual_reduce.go, parallel.go, parallel_agg.go, path.go, pathbitmap.go, pathgen.go, pathindexcarrier.go, pathindexclauses.go, pathindexonly.go, pathindexonlyneed.go, pathindexordered.go, pathkeys.go, pathkeysindex.go, pathparam.go, pathparamindex.go, plan.go, planner.go, predicate_implication.go, predp.go, pushdown.go, qual_canonical.go, reduce_outer_joins.go, relfromjoinlist.go, relsize.go, scan_input_rewrite.go, searchedtree.go, selectivity.go, small_dimension.go, specialjoin.go, subplan_cost.go, subplan_lower.go, subplan_lower_walk.go, tuplefraction.go, unnest.go, view_dml.go, view_privilege.go, walk_export.go, with.go
Findings count: 32

### groupagg_presorted.go:applyPresortedAggregateRule — Repeated grouppathkeys copy per candidate
- **Issue**: Inside the greedy loop, `appendPathKeys(append([]PathKey(nil), grouppathkeys...), candidates[ui].pathkeys)` re-copies the full `grouppathkeys` slice for every unprocessed candidate on every outer iteration.
- **Why**: `grouppathkeys` is a loop-invariant; only the candidate's pathkeys vary. Each copy allocates a fresh backing array of len(grouppathkeys).
- **Suggestion**: Precompute the base copy once outside the loop (`base := append([]PathKey(nil), grouppathkeys...)`) and reuse it, or build `currpathkeys` incrementally. Severity is low since candidate counts are small, but the allocation is trivially hoistable.
- **Severity**: low

### groupagg_presorted.go:aggregateSortlist — O(n²) duplicate detection
- **Issue**: For each argument, the dedup scan walks all previously appended `out` keys calling `exprEqual` on each (`for _, k := range out { if exprEqual(k.Expr, arg) ... }`).
- **Why**: This is quadratic in the number of ORDER BY/args; usually small, but `exprEqual` is a recursive tree comparison so repeated calls on identical subexpressions are wasteful.
- **Suggestion**: Acceptable at typical arg counts; if it ever matters, keying by `parserExprKey` first (cheap hash string) would short-circuit most comparisons.
- **Severity**: low

### foldconst.go:FoldConstants — Always allocates fresh nodes/slices even when nothing folds
- **Issue**: For `BinaryOp`, `UnaryOp`, `CastExpr`, `InExpr`, `FuncCall`, etc., the function unconditionally allocates a new node (and for InExpr/FuncCall a new arg slice) even when folding does not occur and all children are unchanged.
- **Why**: `FoldConstants` is invoked bottom-up over the whole plan tree (`foldPlanConstants`), so a plan with many non-foldable expressions allocates a full clone of the expression tree on every call. No reuse of unchanged children.
- **Suggestion**: Return the original node when the folded children are pointer-identical to the originals (e.g. `if l == x.Left && r == x.Right { return e }`). This preserves semantics and cuts allocation churn on non-foldable subtrees.
- **Severity**: medium

### groupagg_indexorder.go:applyIndexOrderedGroupingRule — Per-candidate map allocations inside index loop
- **Issue**: Inside the `for _, idx := range cat.IndexesOnTable(...)` loop, `prefixSet := make(map[string]bool, len(prefix))` is allocated fresh for every candidate index, and the `for c := range groupSet` membership loop re-walks `groupSet` per candidate.
- **Why**: `groupSet` is loop-invariant; the prefix-set could be checked with the already-computed `len(prefixSet) != len(groupSet)` plus a single walk, and `groupCols`/`groupSet` could be turned into a cheap lookup once.
- **Suggestion**: Hoist a single `groupSet` lookup structure outside the loop; skip per-index map allocation by checking prefix against the prebuilt set directly (build a sorted slice or map once). Low impact since index counts are small.
- **Severity**: low

### groupagg_indexorder.go:buildIndexOrderedScan — tableColumnByName linear scan per referenced column
- **Issue**: The schema-narrowing loop calls `tableColumnByName(seqScan.Table, sc.Name)` per referenced schema entry, each a linear scan of `tbl.Columns`.
- **Why**: If a table has C columns and R are referenced, this is O(C·R). Once per aggregate, so minor, but a single `map[string]catalog.Column` build would make it O(C+R).
- **Suggestion**: Build a name→Column map once when the table is large, or reuse an existing lookup if one exists.
- **Severity**: low

### groupby_alias_key.go / groupingsets.go — Reflective key walks rebuild strings repeatedly
- **Issue**: `qualifiedGroupKey` and `qualifiedAggregateCallKey` perform a full reflective walk (`appendRefQualifiers`) and build a new string on every call, and `groupingsets.go`'s `prepareGroupingSets`/`groupingSetIndices`/`groupingCallMasks` call these key builders repeatedly over the same expressions (e.g. `groupExprSlot` recomputes `qualifiedGroupKey` for each set member per set).
- **Why**: Reflective traversal is inherently slow and the results are never cached; the same expression is re-keyed once per grouping set that mentions it.
- **Suggestion**: Memoize key strings (e.g. map[parser.Expr]string) per statement pass where the same expressions recur across sets. Low severity as these run at planning time on small trees, but reflection is the most expensive lookup here.
- **Severity**: low

### join_exec_keys.go:ExecHashKeyPlan / ExecMergeKeyPlan — Two-pass loop over pairs with repeated predicate scan
- **Issue**: Both plans loop over `pairs` twice (once to build `keys`/`safe`, once for `Int64Keys`), and each `residualExcluding(safe)` rescans the whole `Predicate` conjunct list calling `conjunctIsOneOfKeys` (linear over `keys`).
- **Why**: For an N-conjunct join this is O(N·K) where K ≤ N, plus a full second pass. Small joins dominate, so severity is modest.
- **Suggestion**: Fold the Int64Keys check into the first loop; consider building a set of hash-safe pairs once. Only worth doing if joins with many key pairs are common.
- **Severity**: low

### join_hashkey.go:hashKeyIsInt64 — Redundant ToLower
- **Issue**: `hashKeyIsInt64` calls `strings.ToLower(t.Name)` then `isMachineIntTypeName` internally calls `strings.ToLower(name)` again.
- **Why**: Double lowercasing on every key-type check in the executor hot path (hash join build).
- **Suggestion**: Lower once at the call site, or have the callee take the already-lowered name.
- **Severity**: low

### joinrestrict.go:relidsOfExpr — Linear scan over cumOffsets per column ref
- **Issue**: For each ColumnRef visited, `relidsOfExpr` loops `for t := 0; t < len(cumOffsets)-1; t++` to find which table the index falls in.
- **Why**: Called per conjunct during `buildRestrictInfos`; a clause with many ColumnRefs does O(refs × tables). Table count is small (≤16), so it's bounded but repeated.
- **Suggestion**: Since cumOffsets is sorted, use binary search or a direct index computation. Minor.
- **Severity**: low

### joinsearchlevel.go:joinIsLegal / joinOrderRestricted — Iterate whole joinInfoList per pair with nested subset checks
- **Issue**: `joinIsLegal` (and `joinOrderRestricted`/`hasJoinRestriction`) scan the full `joinInfoList` on every candidate pair, each iteration performing several relset subset/overlap tests.
- **Why**: In a multi-level search this runs O(pairs × sjinfos). The fast path returns early when `joinInfoList` is empty, so only statements with outer joins pay. Relset ops are cheap bit ops.
- **Suggestion**: Acceptable given small lists; if special-join-heavy queries ever dominate, index sjinfos by relevant relids.
- **Severity**: low

### joinsearchseam.go:tryPGShapedJoinSearch — searchConsumes rebuilds restrict infos per conjunct
- **Issue**: For each conjunct in `searchConjuncts`, `searchConsumes` calls `buildRestrictInfos([]Expr{c}, 0, cumOffsets)` allocating a whole new `restrictInfoList` (and equivalence-class machinery) for a single conjunct.
- **Why**: This is done in a loop over every spanning conjunct on every searched statement — repeated allocation of lists/maps that could be built once.
- **Suggestion**: Build the restrict-info list once over all conjuncts, then ask membership of each clause by pointer identity.
- **Severity**: medium

### joinselectivity.go:examineJoinVar / columnStatsByName — Linear column lookup per operand
- **Issue**: `examineJoinVar` calls `columnStatsByName(info.table, cr.Name)` per join operand — a linear scan over the table's columns.
- **Why**: Called for both operands of every equality clause in `joinClauseSelectivityExt`, which runs for every residual clause during `calcJoinrelSize`, for every joinrel candidate in the search.
- **Suggestion**: Cache a `map[string]int` column index per table, or per search, and reuse. This is a real hot path in the join search.
- **Severity**: medium

### joinrelsize.go:bestProvableKey — Rebuilds equated map on every outer iteration
- **Issue**: `bestProvableKey` rebuilds the full `equated` map from `pairs`/`removed` on every call, and `superkeyJoinSelectivity` calls it in a loop until no key is provable.
- **Why**: Each iteration rescans all pairs and reallocates maps; the loop typically runs few times but the per-call cost is O(pairs × cols).
- **Suggestion**: Incrementally maintain the equated map as clauses are removed, or precompute once and update removals.
- **Severity**: low

### pathbitmap.go:chooseBitmapAnd — costBitmapTree recomputed inside sort comparator
- **Issue**: `sort.Slice(paths, func(i, j) { ci, _ := costBitmapTree(...); cj, _ := costBitmapTree(...) })` recomputes each path's tree cost on every comparator invocation — O(n log n) full cost-tree evaluations for n paths, when each path's cost is invariant.
- **Why**: Cost-tree evaluation walks the bitmap subtree; doing it inside the comparator is the classic sort-comparator side-effect/repeat pattern.
- **Suggestion**: Precompute `[]Cost` once, then sort on the precomputed slice.
- **Severity**: medium

### path.go:comparePaths — Allocates a slice per dominance comparison
- **Issue**: `comparePaths` allocates `dims := []dimensionCmp{...}` on every call, and `addToPathlist` calls `comparePaths` in inner loops over the pathlist for every path added during the whole join search (thousands of Paths).
- **Why**: Stack-allocatable fixed-size array; heap escape of the 4-element slice per comparison.
- **Suggestion**: Use a `[4]dimensionCmp` array instead of a slice.
- **Severity**: low

### parallel.go:findPartialSubtree — Repeated drivingScan re-walks of the same subtree
- **Issue**: At each loop iteration `drivingScan(cur)` is called; for an Aggregate candidate `computeParallelWorkers(agg.Child, s)` internally calls `drivingScan` again; `sortPartialRootPays` also calls `drivingScan(srt.Child)`. The same subtree is walked up to three times per node.
- **Why**: Each `drivingScan` is a recursive descent; the walk is cheap but redundant.
- **Suggestion**: Compute `ds := drivingScan(cur)` once per iteration and reuse it for both the partial-capable test and the worker sizing.
- **Severity**: low

### pathbitmap.go:matchBitmapIndexQuals / buildOneParameterizedBitmapPath — columnStatsByName linear lookup per matched column
- **Issue**: `matchBitmapIndexQuals` calls `columnStatsByName(tbl, colName)` per matched index column, and `buildOneParameterizedBitmapPath` calls it per column in the probe loop — a linear scan over the table's column list each time.
- **Why**: Same O(cols²) pattern as joinselectivity.go; runs per index per relation at path-generation time.
- **Suggestion**: Build a name→stats map once per (table) in the caller.
- **Severity**: low

### scan_input_rewrite.go:absorbConjunctsIntoSubtree — Re-walks the whole subtree per matching conjunct
- **Issue**: For every conjunct that `matchSingleTableConstantPredicate` accepts, `findUniqueSeqScanByColumn(parent.Child, col.Name, parent)` re-walks the entire Filter's subtree to locate (and check uniqueness of) the SeqScan.
- **Why**: The subtree is the same for every conjunct; the walk cost multiplies by the number of constant-RHS conjuncts (easily 10+ on TPC-H shapes).
- **Suggestion**: Collect the subtree's scan-output names (or a name→scan map) once before the loop, and only fall back to the walk on a non-unique match. Also `findBTreeIndexForColumn` is invoked twice for the same (scan, column) in the chosen loop and the apply loop — cache the index.
- **Severity**: medium

### reduce_outer_joins.go:applyDemotion — Per-join map allocations and repeated ON-clause walks
- **Issue**: `collectNonNullableTableNames(j.On, tableMap, cat)` and `collectForcedNullTableNames(j.On)` re-walk each ON clause per join and allocate fresh maps; `collectNonNullableWalk`/`collectForcedNullWalk` allocate a new `map[string]bool` per AND node and merge child maps into a new one.
- **Why**: Parse-time pass, so impact is limited, but for a join chain with N joins and deep AND trees the allocation churn is N×tree. `rangeVarNames(j.Right)` is also recomputed 2-3 times per iteration.
- **Suggestion**: Compute each join's localNN/localFN once and reuse; avoid the per-node map reallocation by passing a single accumulator map.
- **Severity**: low

### pushdown.go:pushOneConjunct — Per-conjunct whole-tree walks at every recursion level
- **Issue**: For each conjunct, `pushOneConjunct` recurses down the join spine and at every `*Join` node calls `classifyConjunctSide` (a full walk of the conjunct) and, when sideMixed, `allColumnRefNamesInScope` (two full subtree walks to build the name set).
- **Why**: With C conjuncts over a tree of depth D, this is O(C·D·|conjunct| + C·D·|subtree|). `allColumnRefNamesInScope` rebuilds the name map from scratch on every call.
- **Suggestion**: Hoist the name-map build for each Join once (or memoize per node); short-circuit the recursion since a conjunct already placed at a child returns true immediately.
- **Severity**: medium

### qual_canonical.go:processDuplicateOrs — Recomputes strictParserExprKey repeatedly
- **Issue**: `strictParserExprKey(c)` is recomputed for every conjunct in every arm during the winner scan (once to build `armKeys`, again in the per-reference-arm loop, and again when filtering `winnerKeys`).
- **Why**: Key computation is a tree walk; this is repeated O(branches × conjuncts) times for the same conjuncts.
- **Suggestion**: Precompute each conjunct's key once per arm and index by it, then the winner scan is pure map lookups.
- **Severity**: low

### relfromjoinlist.go / relsize.go / reduce_outer_joins.go / searchedtree.go / predicate_implication.go / pathkeys.go / pathkeysindex.go / pathparam.go / pathparamindex.go — No material efficiency issues found
- **Issue**: These files are small, single-purpose helpers. `pathkeys.go`/`pathkeysindex.go` do run exprEqual-based dedup loops (O(n²) over tiny key lists, acceptable); `pathparamindex.go`'s `boundClauseForColumn` linear scans are bounded by index width; `relfromjoinlist.go` allocates per-problem slices once.
- **Why**: — 
- **Suggestion**: —
- **Severity**: none

### selectivity.go:rangeOpSelectivity — histCmp recomputed for each MCV entry
- **Issue**: In the MCV loop, `histCmp(mcv.Value, literal, col.Type.Name)` is called twice per MCV entry — once in the `>= 0` condition and once inside the branch to classify the sign — and both calls recompute the same numeric parse (`numericValue` does `strconv.ParseFloat`).
- **Why**: `histCmp`/`numericValue` re-parse the value and literal on every call; the loop runs once per MCV entry.
- **Suggestion**: Compute `c := histCmp(...)` once per iteration and branch on it (the `>= 0`/`< 0` test is just `c`'s sign).
- **Severity**: low

### selectivity.go:histogramOpSelectivity — histCmp recomputed in loop and recursion
- **Issue**: The boundary scan calls `histCmp(b, literal, typeName)` per boundary, and the OpGt/OpGe arm recurses into `histogramOpSelectivity(flip, ...)`, which rescans the boundary list from scratch.
- **Why**: For the symmetric arms the scan runs twice over the whole histogram. For small histograms this is cheap.
- **Suggestion**: Find the boundary index once and compute the `>`/`>=` result from the `<`/`<=` index without a second full scan.
- **Severity**: low

### unnest.go:unnestSubqueriesInPlan — countSublinksInExpr re-walks predicate every loop iteration
- **Issue**: Each of the three driver loops calls `countSublinksInExpr(n.Predicate)` at loop top AND bottom (the `remaining >= before` belt), plus `findSubqueryInExpr`/`findInExprInExpr`/`findExistsExprInExpr` each walk the whole predicate. A predicate with K sublinks incurs O(K) full-predicate walks just for bookkeeping.
- **Why**: These are planner-time on small predicates, but the walk count triples per iteration.
- **Suggestion**: Since each loop only removes one sublink per iteration, the count could be decremented instead of recomputed, or the find + count could be fused into one walk.
- **Severity**: low

### unnest.go:planCloneSupported — Walked twice (node walk + walkPlanExprs)
- **Issue**: `planCloneSupported` first walks the plan nodes (checking node kinds) then runs `walkPlanExprs` over the same tree to check nested sublinks. Two full traversals of the same subtree.
- **Why**: Both are cheap walks; can be fused.
- **Suggestion**: Fuse into one traversal.
- **Severity**: low

### unnest.go:collectUnnestParamsAndResiduals — Repeated walkExprTree per conjunct and three full-plan walks
- **Issue**: For each non-equijoin conjunct, `walkExprTree` is invoked to collect refs; then `walkPlanExprsDeep` re-walks the whole plan to verify all refs are accounted for; `harvestIndexKeyParams` walks the whole plan separately.
- **Why**: Several O(plan) traversals per sublink candidate, plus per-conjunct walks.
- **Suggestion**: Acceptable at plan time for subquery bodies; a single walk that collects both params and residuals would halve it.
- **Severity**: low

### selectivity.go:eqSelectivityForColumn — MCV list scanned twice per call
- **Issue**: `eqSelectivityForColumn` scans `stats.MCV` once for the value match and again to sum `mcvMass`.
- **Why**: Two linear passes over the MCV list.
- **Suggestion**: Sum the mass in the first loop and return on a hit; or compute the mass once when stats are loaded.
- **Severity**: low

### selectivity.go:clauseSelectivity / clauseSelectivityWithSource — Near-identical duplicated implementations
- **Issue**: `clauseSelectivityWithSource` re-implements the entire dispatch of `clauseSelectivity` (every arm duplicated with a reliable flag), so any fix to one must be mirrored in the other (e.g. `rangeOpSelectivityWithSource` calls `normalizeColumnConstRange` then `rangeOpSelectivity` calls it again).
- **Why**: Duplicated code paths are a maintenance cost; `rangeOpSelectivityWithSource` calls the plain `rangeOpSelectivity` which re-runs `normalizeColumnConstRange` and `columnStatsForChild` a second time.
- **Suggestion**: Have `clauseSelectivity` delegate to the WithSource variant (drop the flag), or at least have `rangeOpSelectivityWithSource` reuse the normalized parts.
- **Severity**: medium

### plan.go / view_dml.go / view_privilege.go / walk_export.go / with.go / specialjoin.go / small_dimension.go / subplan_cost.go / subplan_lower.go / subplan_lower_walk.go / tuplefraction.go — No material efficiency issues found
- **Issue**: `plan.go` is mostly node definitions; `view_dml.go`'s `viewColumnMap` linear column scans are tiny; `view_privilege.go`/`walk_export.go` are thin walkers; `with.go` builds small maps; `subplan_lower.go`'s `slotFor` uses a per-scope map for dedup (good); `tuplefraction.go` is single-pass.
- **Why**: —
- **Suggestion**: —
- **Severity**: none

### planner.go:tryRangeIndexScan — Whole WHERE predicate resolved twice
- **Issue**: Inside the loop, `resolveExpr(keyExpr, ctx)` resolves each range bound's RHS expression. Then at the end, `resolveExpr(where, ctx)` re-resolves the ENTIRE WHERE predicate (including all the range bounds already resolved). Resolution is a full tree walk.
- **Why**: The two resolveExpr calls are redundant for the range-bound expressions that were already handled. For a single-conjunct range scan this is a 2× walk; for multi-conjunct queries it scales with the full predicate.
- **Suggestion**: Cache the resolved key expressions from the loop and reuse them in the final full predicate resolution, or restructure to resolve once. The final `fullPred` resolution is needed for the Filter predicate building.
- **Severity**: medium

### planner.go:tryPromoteOrderedIndexOnlyScan — idxColSet map and table-column linear scan per index
- **Issue**: For each candidate index, `tryPromoteOrderedIndexOnlyScan` builds an `idxColSet` map from scratch and then for each projected column calls a linear scan over `seqScan.Table.Columns` (the `for _, c := range seqScan.Table.Columns` closure) — repeated per index per projected column.
- **Why**: O(indexes × projected_cols × table_cols). Usually few indexes and few projected columns, but the map + column lookup is rebuilt every iteration.
- **Suggestion**: Build the column-name→Column map once outside the loop; the `idxColSet` map could also be reused by creating it outside the index loop if it simply accumulates all index columns.
- **Severity**: low

### planner.go:parserExprKey — fmt.Sprintf used for simple integer formatting
- **Issue**: `parserExprKey` uses `fmt.Sprintf("i:%d", x.Value)` and `fmt.Sprintf("p:%d", x.Number)` on every key construction. `strconv.FormatInt` would avoid the format-string parsing and reflection overhead.
- **Why**: `parserExprKey` is called heavily (dedup maps, GROUP BY slots, etc). The fmt.Sprintf overhead is small per call but can accumulate.
- **Suggestion**: Use `strconv.FormatInt(x.Value, 10)` prefixed with the type tag via string concatenation or builder.
- **Severity**: low

### planner.go:exprEqual — Full expression tree walk to build keys for every comparison
- **Issue**: `exprEqual` walks the entire expression tree via `exprIdentityKey` to build a string key before comparing — two full walks per comparison. `exprEqual` is used extensively in plan-building (dedup, clause placement, key matching).
- **Why**: String keys are simple and correct but walking two expression trees per comparison is O(|tree|) per call. `exprEqual` is called many times throughout planning.
- **Suggestion**: This is already a reasonable trade-off for correctness and simplicity; if profiling ever flags it, a structural comparison (hash-consing short-circuit) could skip the string build when pointers differ but structure is the same.
- **Severity**: low

## Summary

Total files reviewed: 70 (all assigned files in `internal/optimizer/`)
Total findings: 32
  - Medium severity: 8 (repeated-clause doubly-resolved in `tryRangeIndexScan`, `searchConsumes` rebuilds restrict info per conjunct, `pathbitmap.go:chooseBitmapAnd` recomputes cost in sort comparator, `foldconst.go:FoldConstants` allocates fresh nodes unnecessarily, `selectivity.go` duplicated implementations, `scan_input_rewrite.go` re-walks subtree per conjunct, `pushdown.go` per-conjunct whole-tree walks, `joinselectivity.go` linear column lookup per operand)
  - Low severity: 24
  - No issues: remaining files (small helpers, struct definitions, thin wrappers, parse-time passes)

Most findings are low severity: planner-time files by nature process small trees (join counts are bounded, column lists are small, clause counts are modest), so the constant factors matter less than algorithmic correctness. The medium findings represent either repeated work across entire conjunct/plan walks or allocations that scale with query complexity.

## Remaining files reviewed — no significant efficiency findings

The following files were read in full and contain no material instances of the two
review criteria (wasteful processing / obvious efficiency wins without logic
changes): `groupagg_hashagg.go`, `inner_join_qual_pushdown.go`, `join_hash_keys.go`,
`joincost.go`, `joinkeyproof.go`, `joinlayout.go`, `joinorder.go`, `joinpaths.go`,
`joinpathsmemoize.go`, `joinpathsmerge.go`, `joinpathsmergeouter.go`,
`joinpathsnli.go`, `joinsearch.go`, `joinsearchtrace.go`, `likeprefix.go`,
`local_filters.go`, `memoize.go`, `nl_index_join.go`,
`nl_index_join_selectivity.go`, `notnull_qual_reduce.go`, `parallel_agg.go`,
`pathgen.go`, `pathindexcarrier.go`, `pathindexclauses.go`, `pathindexonly.go`,
`pathindexonlyneed.go`, `pathindexordered.go`, `pathkeys.go`, `pathkeysindex.go`,
`pathparam.go`, `pathparamindex.go`, `plan.go`, `predicate_implication.go`,
`predp.go`, `relfromjoinlist.go`, `relsize.go`, `searchedtree.go`,
`small_dimension.go`, `specialjoin.go`, `subplan_cost.go`, `subplan_lower.go`,
`subplan_lower_walk.go`, `tuplefraction.go`, `view_dml.go`, `view_privilege.go`,
`walk_export.go`, `with.go`.

Small, single-purpose helpers whose loops are bounded by tiny inputs (join key
lists, index column counts, schema widths) were judged to be within acceptable
constants; the notable low-cost wins in those files (e.g. `joinpathsmemoize.go`'s
uncollapsed duplicate keys, `pathkeys.go`'s tiny O(n²) dedup loops, `memoize.go`'s
per-key stats lookups) are minor enough that they are documented here rather than
as separate findings.
