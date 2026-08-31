# Optimizer (Part 2) — Bug Review 2026-08-31

Files: flaglabels.go, foldconst.go, groupagg_hashagg.go, groupagg_indexorder.go,
groupagg_presorted.go, groupby_alias_key.go, groupingsets.go,
inner_join_qual_pushdown.go, join_exec_keys.go, join_hash_keys.go,
join_hashkey.go, joincost.go, joinkeyproof.go, joinlayout.go, joinorder.go,
joinpaths.go, joinpathsmemoize.go, joinpathsmerge.go, joinpathsmergeouter.go,
joinpathsnli.go, joinrelsize.go, joinrestrict.go, joinsearch.go,
joinsearchlevel.go, joinsearchseam.go, joinsearchtrace.go, joinselectivity.go,
likeprefix.go, local_filters.go, memoize.go, nl_index_join.go,
nl_index_join_selectivity.go, notnull_qual_reduce.go, parallel.go,
parallel_agg.go, path.go, pathbitmap.go, pathgen.go, pathindexcarrier.go,
pathindexclauses.go, pathindexonly.go, pathindexonlyneed.go,
pathindexordered.go, pathkeys.go, pathkeysindex.go, pathparam.go,
pathparamindex.go, plan.go, planner.go, predicate_implication.go, predp.go,
pushdown.go, qual_canonical.go, reduce_outer_joins.go, relfromjoinlist.go,
relsize.go, scan_input_rewrite.go, searchedtree.go, selectivity.go,
small_dimension.go, specialjoin.go, subplan_cost.go, subplan_lower.go,
subplan_lower_walk.go, tuplefraction.go, unnest.go, view_dml.go,
view_privilege.go, walk_export.go, with.go

Findings count: 1

---

### `foldconst.go:foldCaseExpr` — dead THEN body under a NULL simple-CASE operand is folded and can raise where PG does not

- **Bug**: In `foldCaseExpr`, when the folded operand is a `*NullConst` (e.g. `CASE NULL WHEN 1 THEN 1/0 END`), `toLiteralValue` returns `(zero, false)` for a `NullConst` (it is not covered by the type switch at lines 422-432). The simple-CASE constant-comparison shortcut (lines 375-393) is therefore skipped, and every WHEN's THEN body is folded as "potentially reachable" at line 395-397. A THEN containing a constant-fold error (1/0) then panics with `foldEvalPanic` → plan error 22012. PostgreSQL avoids this by only performing the constant CASE simplification when the case argument is a NON-NULL Const: `eval_const_expressions_mutator` (postgres/src/backend/optimizer/util/clauses.c) checks `if (caseexpr->arg != NULL && IsA(caseexpr->arg, Const) && !((Const *) caseexpr->arg)->constisnull)` before attempting to fold. With a NULL operand, PG keeps the CASE unfolded and never evaluates the dead THEN.

- **When it triggers**: `CASE <NULL constant> WHEN <literal> THEN <expr-with-const-div-by-zero> ...` — e.g. `SELECT CASE NULL::int WHEN 1 THEN 1/0 ELSE 2 END`. goopg raises "division by zero" at plan time; PG returns 2.

- **Fix**: When the folded operand is a `*NullConst`, treat every WHEN as dead (a NULL operand can never match any WHEN), skip folding THEN bodies, and return the folded ELSE (or NULL if no ELSE).

- **Severity**: low (rare shape, but a genuine correctness divergence)

---

## Minor observations (not confirmed bugs)

- `foldconst.go:tryFoldUnaryOp` — numeric unary negation of `NumericConst{"0"}` produces `NumericConst{"-0"}`. The numeric text is preserved verbatim without normalisation. Cosmetic only.

---

## Reviewed, no confirmed bug

- `flaglabels.go` — provenance table rendering; `shellSingleQuote`, `FlagProvenanceTable`, `flagProvenanceExempt` all consistent.
- `groupagg_hashagg.go` — `applyEnableHashAggRule` conditions mirror the executor's `openSorted` routing exactly.
- `groupagg_indexorder.go` — index-ordered grouping; `GroupKeyOrder` mapping, `buildIndexOrderedScan` narrowed schema, `remapChecked` via `cloneExprRefs` with `scopeVeto`.
- `groupagg_presorted.go` — presorted-aggregate greedy algorithm; `comparePathkeysDim` / `dimBetter1`/`dimBetter2` semantics used correctly. `makeCandidatePathkeys` constant dedup. `pathkeysContainVolatile` walker correct.
- `groupby_alias_key.go` — qualified group key via reflective walk; `maxStructuralKeyDepth` backstop.
- `groupingsets.go` — `groupingCallMasks` bit indexing (`1<<(n-1-i)`) correct for up to 64 args. `commonGroupingSlots` intersection correct.
- `inner_join_qual_pushdown.go` — side selection, positional-by-name validation, `deriveConstAcrossJoinEquality` operand orientation; `shiftConjunctForInput` delta correct.
- `join_exec_keys.go` — `ExecHashKeyPlan`/`ExecMergeKeyPlan`; `Keys[0]` always included, `safe` list for hash-safe pairs, `residualExcluding` via `conjunctIsOneOfKeys`. `pairIsHashSafe` int-family rule (`isMachineIntTypeName`) correct.
- `join_hash_keys.go` — `fillOneJoinHashKeys` pointer-pinned `HashKeys[0]`, clone of extra pairs, `residualExcluding`, `conjunctIsOneOfKeys` orientation.
- `join_hashkey.go` — `mergedKeyColumn`, `isMachineIntTypeName`.
- `joincost.go` — cost functions; `costInnerHash` reduces to l+r but correct.
- `joinkeyproof.go` — base-column resolver, superkey/FK proof, `coveringJoinPairs` chicken-out, `columnsSubset`.
- `joinlayout.go` — pos-map family, `reresolveJoinByName`, `reconcileNLILayout`. `lookupColumnIndexByName` ambiguity detection.
- `joinorder.go` — greedy reorder, connectivity mode, deterministic tie-breaks (`k < best`). `commonEquijoinsAcrossOr` intersection correct.
- `joinpaths.go` — `addPathsToJoinrel` direction rules, `splitJoinClauses`, `isKeyableFor`.
- `joinpathsmemoize.go` — `getMemoizePath` gates, `costMemoizeRescan`. `memoizeCacheKeys` / `memoizeKeyNDistinct` correct. Nil-s `examineJoinVar` safe.
- `joinpathsmerge.go` — `mergeKeyGroups` EC dedup, `mergeInnerSortKeys`, `sortInnerAndOuter`. `rotateToFront` correct.
- `joinpathsmergeouter.go` — `matchUnsortedOuterMerge`, `generateMergeJoinPaths`, `trimMergeClausesForInnerPathkeys`, `demoteDroppedMergeClauses`.
- `joinpathsnli.go` — `addNLIPaths`, `nestloopResidualClauses`, `probeEnforcedClauses`. `allowStarSchemaJoin`/`paramSourceRels`.
- `joinrelsize.go` — `calcJoinrelSize`, `superkeyJoinSelectivity`, `keyImpliedRowsBound`, `oneClausePerEquivClass`. `joinKeyPairOf` operand-side test correct.
- `joinrestrict.go` — `buildRestrictInfos`, `clausesFor`, `hasRelevantJoinClause`, `selectivityClauses`, `relidsOfExpr`.
- `joinsearch.go` — level lists, `buildInitialRels`, `finalPath`/`getCheapestFractionalPath`. `initialRelRows` CTE fallback.
- `joinsearchlevel.go` — phase 1/2/3 enumeration, `makeJoinRel`, `joinIsLegal`. Mirror-image `first` offset correct.
- `joinsearchseam.go` — outer-spine peel, `extractSearchLeaves`, `chainCarriesLateral`. `searchConsumes`/`partitionConjunctsForJoinPlanning`.
- `joinsearchtrace.go` — trace rendering; nil-safe methods.
- `joinselectivity.go` — `examineJoinVar`/`resolveJoinVarColumn` nil-safe. `getVariableNumDistinct` correct.
- `likeprefix.go` — `ExtractLikePrefix`/`IncrementString`/`injectLikeRangePredicates` (LIKE residual preserved, range is a superset, correct).
- `local_filters.go` — `partitionConjunctsForJoinPlanning`, `conjunctIsLocalEligible`, `localizeExprToLeaf`. scopeVeto consistent.
- `memoize.go` — `maybeAttachMemoize` gate; `singleRow` detection correct (full-key unique probe).
- `nl_index_join.go` — `tryBuildNLI`, `extractEquiKeys`, `pickInnerSide`, `collectCrossSideEquiKeys`, `pickIndexCoveringLeadingPrefix`, `nliConsumedByProbe`, `splitFilterPredicateForNLI`. `cloneExprShiftIdx` with `*OuterColumnRef`/`*CTIDExpr` veto.
- `nl_index_join_selectivity.go` — `rebindPassesSelectivityGuard`, `origTypeMatches`.
- `notnull_qual_reduce.go` — NOT-NULL truth classification; `singleClauseTruth`/`orClauseTruth` correct.
- `parallel.go` — `MaybeAddGather`, `findPartialSubtree`, `drivingScan`, `hashJoinIsPartialCapable`, `computeParallelWorkers` (overflow guard `(1<<31-1)/3` = MaxInt32/3, correct), `splitAggregate`, `stampParallelScan`. Non-mutating discipline.
- `parallel_agg.go` — `AggregateIsDecomposable` whitelist, `aggregateSplitIsSafe`, `groupsToRowsRatio`.
- `path.go`, `pathbitmap.go`, `pathgen.go`, `pathindexcarrier.go`, `pathindexclauses.go`, `pathindexonly.go`, `pathindexonlyneed.go`, `pathindexordered.go`, `pathkeys.go`, `pathkeysindex.go`, `pathparam.go`, `pathparamindex.go` — path substrate, parameterised paths, index-only paths, bitmap paths, merge ordering. All consistent.
- `predicate_implication.go` — `provePartialIndexPredicate` only proves identical `col <op> const` ≡ `col <op> const`. `asColOpConst` flips correctly. Sound.
- `predp.go` — `runJoinSearchBelowPinned` splice + re-resolve; `layoutPosMap` by (Name, SourceTableIdx) correct.
- `pushdown.go` — CROSS→INNER promotion, `classifyConjunctSide`, `allColumnRefNamesInScope`, `pushOuterQualsIntoLaterals`. `walkColumnRefsImpl` enumerates 23 expression types.
- `qual_canonical.go` — `canonicalizeQual`/`processDuplicateOrs`. Winner detection, degenerate case, `andChain`/`orChain` correct.
- `reduce_outer_joins.go` — RIGHT→LEFT flip, LEFT→ANTI demotion. Propagation rules correct.
- `relfromjoinlist.go` — `makeRelFromJoinlist`, `searchOneProblem`, `leafRel` bounds check. Boundary hole-filler.
- `relsize.go` — `estimateRelSize` (table_block_relation_estimate_size port), `typeWidth`, `estScanPages`, `baseRelRows`.
- `scan_input_rewrite.go` — `absorbConjunctsIntoSubtree`, `findUniqueSeqScanByColumn`, `replaceNodeAtParentSlot`. Equality conjunct dropped, range conjunct kept. Strict/lenient bound handling correct.
- `selectivity.go` — clause selectivity helpers.
- `small_dimension.go` — `smallDimensionTag` derived from row count; un-gated fallback (design choice).
- `specialjoin.go` — `makeSpecialJoinInfo`, `collectSpecialJoinInfos`.
- `subplan_cost.go` — `estimateSubplanCostPerCall` shape classification.
- `subplan_lower.go` — `lowerSubPlanParams`, `slotFor` forwarding via chain. Level arithmetic correct. `analyzeSublink`/`rewriteSublinkPlan` consistent.
- `subplan_lower_walk.go` — `lowerTraverseNode`/`lowerTraverseExpr` exhaustive switch. Bail on unknown types.
- `tuplefraction.go` — `preprocessLimit`, `getCheapestFractionalPath`, `compareFractionalPathCosts`.
- `with.go` — `preplanWithClause`, `planRecursiveCTE`, `validateRecursiveMember`. `recRefWalker` structural checks.
- `view_dml.go`, `view_privilege.go`, `walk_export.go`, `searchedtree.go`, `unnest.go` — reviewed, no bugs found.