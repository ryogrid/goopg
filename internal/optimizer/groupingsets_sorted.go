package optimizer

import "sort"

// M0146-0020a — the sorted grouping-sets strategy for a single rollup.
//
// PG's consider_groupingsets_paths (planner.c) splits the grouping sets into
// rollups — chains of sets each contained in the next (extract_rollup_sets) —
// and, over input sorted on a rollup's longest set, computes the whole chain
// in ONE sorted pass: AGG_SORTED, `GroupAggregate` with a `Group Key:` line
// per set, largest first (TPC-DS Q18/Q27's ROLLUP). goopg computes grouping
// sets by hashing; this file supplies the single-chain case's ordering, which
// the planner sorts its input by and the executor emits in.

// RollupChainOrder reports whether sets form one rollup — every set contained
// in every larger one — and returns the column order a sorted rollup reads
// its input in: the smallest non-empty set's columns, then each larger set's
// additional columns, each group in ascending GroupExprs position. Every set
// is then a prefix of that order, which is what lets one sorted pass close a
// set's group whenever its prefix changes. At least one set must be
// non-empty (a lone empty set is AGG_PLAIN in PG).
func RollupChainOrder(sets [][]int) ([]int, bool) {
	if len(sets) == 0 {
		return nil, false
	}
	bySize := make([][]int, len(sets))
	copy(bySize, sets)
	sort.SliceStable(bySize, func(i, j int) bool { return len(bySize[i]) < len(bySize[j]) })
	var order []int
	have := map[int]bool{}
	for _, set := range bySize {
		in := map[int]bool{}
		for _, c := range set {
			in[c] = true
		}
		// Containment: every column already in the order must be in this
		// (no smaller) set, or the chain breaks.
		for c := range have {
			if !in[c] {
				return nil, false
			}
		}
		var added []int
		for _, c := range set {
			if !have[c] {
				added = append(added, c)
			}
		}
		sort.Ints(added)
		for _, c := range added {
			have[c] = true
			order = append(order, c)
		}
	}
	if len(order) == 0 {
		return nil, false
	}
	return order, true
}

// rollupSortKeys is the ascending, NULLS LAST sort a sorted rollup needs on
// its input: GroupExprs in the first rollup's order.
func rollupSortKeys(aggNode *Aggregate) ([]SortKey, bool) {
	if aggNode == nil || aggNode.GroupingSets == nil {
		return nil, false
	}
	order, ok := RollupChainOrder(aggNode.GroupingSets)
	// M0146-0020b: the first rollup's order (preprocess_grouping_sets),
	// which follows ORDER BY for a single rollup and exists for several.
	if len(aggNode.Rollups) > 0 {
		order, ok = aggNode.Rollups[0].Order, len(aggNode.Rollups[0].Order) > 0
	}
	if !ok {
		return nil, false
	}
	keys := make([]SortKey, 0, len(order))
	for _, gi := range order {
		if gi < 0 || gi >= len(aggNode.GroupExprs) {
			return nil, false
		}
		keys = append(keys, SortKey{Expr: aggNode.GroupExprs[gi]})
	}
	return keys, true
}

// costSortedRollups is create_groupingsets_path's AGG_SORTED pricing
// (pathnode.c): the first rollup is cost_agg(AGG_SORTED) over the sorted
// input; each later rollup adds a cost_sort of the input rows (no input cost
// again) and a cost_agg(AGG_SORTED) over that sort, with its own column and
// group counts. One rollup is the M0146-0020a single-pass price unchanged.
func costSortedRollups(cp costParams, a *Aggregate, seed *Path, inputRows, inputStartup, inputTotal float64, firstCols int, numGroups float64) Cost {
	nAggs := len(a.Aggs)
	if len(a.Rollups) <= 1 {
		return costAggSortedRollup(cp, inputRows, inputStartup, inputTotal, firstCols, numGroups, nAggs)
	}
	perSet, ok := groupingSetGroupCounts(a, int64(inputRows))
	if !ok {
		return costAggSortedRollup(cp, inputRows, inputStartup, inputTotal, firstCols, numGroups, nAggs)
	}
	rollupGroups := func(r GroupingRollup) float64 {
		g := 0.0
		for _, si := range r.Sets {
			if si >= 0 && si < len(perSet) {
				g += float64(perSet[si])
			}
		}
		return g
	}
	out := costAggSortedRollup(cp, inputRows, inputStartup, inputTotal, len(a.Rollups[0].Order), rollupGroups(a.Rollups[0]), nAggs)
	for _, r := range a.Rollups[1:] {
		srt := costSortRunWithWidth(cp, inputRows, pathNCols(seed), pathAvgVarBytes(seed), -1, pathWidth(seed), "rollup")
		agg := costAggSortedRollup(cp, inputRows, srt.Startup, srt.Total, len(r.Order), rollupGroups(r), nAggs)
		out.Total += agg.Total
	}
	return out
}
