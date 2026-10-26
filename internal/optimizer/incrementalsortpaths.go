package optimizer

// incrementalsortpaths.go — M0141-S2b-2c / S7's `addOrderedPaths` THIRD arm
// (planner.c's own `create_ordered_paths` loops every surviving candidate of
// the input rel, not just the cheapest-total one; upperordered.go's first two
// arms still handle only the single seed). Landed once both halves of the
// prerequisite chain existed: S2b-2a/2b (upperordered.go, upperorderedinput.go)
// publish and validate the search's full `Pathlist` onto the ORDERED rel, and
// S7's own groundwork (`pathkeysCountContainedIn`, pathkeys.go;
// `costIncrementalSort`, cost_funcs.go) gives a per-candidate prefix credit a
// Sort's flat cost cannot express. S2b-2's own recon
// (m0141-s2b-scoping-decomposition.md §"S2b-2 result") proved analytically
// that offering the full Pathlist WITHOUT that credit cannot move a single
// plan — this file is what makes the offer non-trivial.
//
// DEFAULT ON SINCE M0146-0006 (2026-10-04). It was gated off behind
// GOOPG_INCREMENTAL_SORT until the GroupAgg-shaped witnesses could reach it
// (the 2026-09-17h corpus measurement found zero Incremental Sort nodes with
// the flag on, because `electOrderedGrouping` bypassed `createOrderedPaths`);
// the design doc's 2026-09-17h update has the history. M0147-0043 retired the
// env switch: the arm is now gated only by PG's own `enable_incremental_sort`
// (`cp.enableIncrementalSort`, PlannerSettings.EnableIncrementalSort), exactly
// as planner.c's create_ordered_paths is.
//
// CHILDREN ARE ALREADY SAFE TO MATERIALIZE. The candidates this arm stacks a
// PathIncrementalSort over come from `ordered.SearchCandidates` — real
// `*Path` entries the search itself built (PathSeqScan / PathIndexScan /
// PathHashJoin / PathMergeJoin / PathNestLoop / …), every one of which already
// has a `createPlanNode` arm because the search materializes exactly these
// kinds when ITS OWN tournament picks one. Only the new wrapper kind is
// unhandled; nothing about this arm asks `createPlanNode` to translate a
// candidate it could not already translate on its own. Path construction here
// is also lazy by S2b-2b's own contract: building a `*Path` allocates no
// executor Node, so offering N-1 losing candidates costs nothing beyond the
// struct itself, and `createPlanNode` runs exactly once, on whichever path
// `setCheapest` picks — unchanged from every other upper rel in this package.

// upperOrderedIncrementalSortProducer is this arm's DPPATH trace string
// (pathtrace.go), sibling to upperOrderedInputProducer/upperOrderedSortProducer
// declared in upperordered.go.
const upperOrderedIncrementalSortProducer = "upper.ordered.incrementalsort"

// addIncrementalSortPaths is `addOrderedPaths`'s third arm: PG's own
// `create_ordered_paths` loop, restricted to the candidates goopg can see at
// this seam (`ordered.SearchCandidates`, published by S2b-2a) whose validated
// ordering claim (`ordered.SearchCandidateKeys`, S2b-2b) shares a genuine
// PARTIAL prefix with `sortPathkeys` — a full match is arm 1's case for the
// seed and buys nothing new here; zero shared prefix is a full Sort, which is
// what arm 2 already prices for the seed (a losing candidate with zero shared
// prefix competing on a full Sort is exactly the still-blocked "actual
// tournament" this arm intentionally does NOT re-open beyond the prefix
// credit that motivates it).
//
// `input` is the seed `*Path` (`newPrebuiltPath`'s wrapper over the finished
// child Node) `createOrderedPaths` already built — its `.node` is the
// materialized Node for THIS rel's relids, reused here as the `child Node`
// `estimateNumGroups` needs to look up column statistics. That reuse is sound
// because S2b-2's own recon (Finding 2) established that every candidate in
// one call's Pathlist is an alternate PHYSICAL strategy for the SAME logical
// relation — same base tables, same columns, same statistics — so the seed's
// materialized Node is a faithful stats source for a sibling candidate's own
// prefix, not an approximation specific to the seed.
func addIncrementalSortPaths(ordered *RelOptInfo, input *Path, sortPathkeys []PathKey, cp costParams, limitTuples float64) {
	if !cp.enableIncrementalSort {
		return
	}
	for i, candidate := range ordered.SearchCandidates {
		if candidate == nil || i >= len(ordered.SearchCandidateKeys) {
			continue
		}
		keys := ordered.SearchCandidateKeys[i]
		if len(keys) == 0 {
			continue
		}
		contained, nCommon := ordered.SearchCandidateClasses.countContainedIn(keys, sortPathkeys)
		if pathTraceEnabled {
			traceIncrementalSortCandidate(i, candidate.Kind, len(keys), contained, nCommon, candidate.Cost.Total)
		}
		if contained || nCommon == 0 || input == nil || input.node == nil {
			continue
		}
		// M0146-0006: rebuilt through the searched boundary before it is
		// wrapped, as the presorted arm in addOrderedPaths does
		// (M0146-0027). A raw searched path lowers in the search's inner
		// coordinate order, not the row the boundary committed, so stacking
		// the Incremental Sort on it directly would sort — and emit — the
		// wrong columns once this arm can win (the default since 0006).
		cNode := searchedCandidateInput(input.node, candidate)
		if cNode == nil {
			continue
		}
		cs := newPrebuiltPath(ordered, cNode)
		cs.Rows = candidate.Rows
		cs.Cost = candidate.Cost
		cs.Pathkeys = keys
		addPath(ordered, incrementalSortPathOver(ordered, cs, input.node, sortPathkeys, nCommon, cp, limitTuples),
			upperOrderedIncrementalSortProducer)
	}
}

// incrementalSortPathOver is create_incremental_sort_path over sub, whose
// ordering already delivers the first nCommon of sortPathkeys. statsNode is
// the materialized input `estimateNumGroups` reads column statistics from
// (the presorted prefix's group count is cost_incremental_sort's input).
func incrementalSortPathOver(ordered *RelOptInfo, sub *Path, statsNode Node, sortPathkeys []PathKey, nCommon int, cp costParams, limitTuples float64) *Path {
	groupExprs := make([]Expr, nCommon)
	for j := 0; j < nCommon; j++ {
		groupExprs[j] = sortPathkeys[j].Expr
	}
	// cost_incremental_sort clamps input_tuples to 2 BEFORE estimating the
	// presorted groups (costsize.c), so a one-row input still counts two
	// groups and its startup is half the input's run (M0146-0134, TPC-DS
	// Q64's cross_sales).
	groupTuples := sub.Rows
	if groupTuples < 2 {
		groupTuples = 2
	}
	groups := estimateNumGroups(groupExprs, statsNode, int64(groupTuples))
	return incrementalSortPathWithGroups(ordered, sub, sortPathkeys, nCommon, float64(groups), cp, limitTuples)
}

// incrementalSortPathWithGroups is create_incremental_sort_path given the
// presorted prefix's group count (cost_incremental_sort's input_groups) — for
// callers that estimate it from something other than a materialized node, as
// the join search does for a partial path (generateUsefulGatherPaths).
func incrementalSortPathWithGroups(ordered *RelOptInfo, sub *Path, sortPathkeys []PathKey, nCommon int, groups float64, cp costParams, limitTuples float64) *Path {
	groupTuples := sub.Rows
	if groupTuples < 2 {
		groupTuples = 2
	}
	sp := &Path{
		Kind: PathIncrementalSort,
		// `cost_incremental_sort` folds enable_sort's flag in via
		// `cost_sort` upstream (costsize.c:2144).
		DisabledNodes: disabledNodesFor(!cp.enableSort, sub),
		Rel:           ordered,
		// M0146-0149: cost_incremental_sort sets path->rows from the
		// CLAMPED input_tuples (costsize.c), so an Incremental Sort over a
		// one-row input carries two rows — TPC-DS Q70's window subquery is
		// 2 rows in PG and is hashed in a semi join for it. The node's
		// EstimateRows arm (cardinality.go) agrees.
		Rows: groupTuples,
		Cost: costIncrementalSort(cp, sub.Cost, sub.Rows, groups,
			pathNCols(sub), pathAvgVarBytes(sub), limitTuples, pathWidth(sub)),
		Pathkeys: sortPathkeys,
		// M0141-S7-exec-b: stashed for `createIncrementalSortPlan` — see
		// the field's own doc comment (path.go).
		PresortedCount: nCommon,
		Children:       []*Path{sub},
		RequiredOuter:  sub.RequiredOuter,
		ParallelSafe:   parallelSafeWith(sub.Rel, sub),
		// create_incremental_sort_path: parallel_workers =
		// subpath->parallel_workers (pathnode.c), so a per-worker sort of a
		// partial path stays partial under its Gather Merge.
		ParallelWorkers: sub.ParallelWorkers,
	}
	inheritNarrowedWidths(sp, sub)
	return sp
}
