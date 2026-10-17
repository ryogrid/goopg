package optimizer

import (
	"math"
	"sort"
)

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
	rollupGroups := func(r GroupingRollup) float64 { return rollupGroupCount(perSet, r) }
	out := costAggSortedRollup(cp, inputRows, inputStartup, inputTotal, len(a.Rollups[0].Order), rollupGroups(a.Rollups[0]), nAggs)
	for _, r := range a.Rollups[1:] {
		srt := costSortRunWithWidth(cp, inputRows, pathNCols(seed), pathAvgVarBytes(seed), -1, pathWidth(seed), "rollup")
		agg := costAggSortedRollup(cp, inputRows, srt.Startup, srt.Total, len(r.Order), rollupGroups(r), nAggs)
		out.Total += agg.Total
	}
	return out
}

// rollupGroupCount is a RollupData's numGroups: the sum of its sets' group
// estimates.
func rollupGroupCount(perSet []int64, r GroupingRollup) float64 {
	g := 0.0
	for _, si := range r.Sets {
		if si >= 0 && si < len(perSet) {
			g += float64(perSet[si])
		}
	}
	return g
}

// mixedGroupingRollups is consider_groupingsets_paths' sorted-input AGG_MIXED
// choice (planner.c): hash_mem is a knapsack, each rollup after the first (the
// one the input's order serves) an item weighing its hash table
// (estimate_hashagg_tablesize) and worth one saved sort. The chosen rollups'
// sets are hashed one set each — in reverse, as PG lcons them — and the rest
// stay sorted, the first reading the input. ok is false when nothing is
// worth hashing (one rollup, no hash_mem, or no item fits).
func mixedGroupingRollups(cp costParams, a *Aggregate, seed *Path, inputRows float64) (hashed, sorted []GroupingRollup, ok bool) {
	if len(a.Rollups) <= 1 || cp.workMem <= 0 {
		return nil, nil, false
	}
	perSet, ok := groupingSetGroupCounts(a, int64(inputRows))
	if !ok {
		return nil, nil, false
	}
	entry := hashAggEntrySize(len(a.Aggs), float64(pathWidth(seed)))
	avail := float64(cp.workMem)
	scale := math.Max(avail/(20.0*float64(len(a.Rollups))), 1.0)
	capacity := int(math.Floor(avail / scale))
	weights := make([]int, 0, len(a.Rollups)-1)
	for _, r := range a.Rollups[1:] {
		sz := entry * rollupGroupCount(perSet, r)
		weights = append(weights, int(math.Min(math.Floor(sz/scale), float64(capacity)+1.0)))
	}
	items := discreteKnapsack(capacity, weights)
	if len(items) == 0 {
		return nil, nil, false
	}
	sorted = []GroupingRollup{a.Rollups[0]}
	var hashSets []GroupingRollup
	for i, r := range a.Rollups[1:] {
		if !items[i] {
			sorted = append(sorted, r)
			continue
		}
		for _, si := range r.Sets {
			n := len(a.GroupingSets[si])
			hashSets = append(hashSets, GroupingRollup{Order: r.Order[:n:n], Sets: []int{si}})
		}
	}
	for i := len(hashSets) - 1; i >= 0; i-- {
		hashed = append(hashed, hashSets[i])
	}
	return hashed, sorted, true
}

// discreteKnapsack is DiscreteKnapsack (lib/knapsack.c) with every item worth
// 1: the item indexes that fit the most items into maxWeight, found by the
// same descending-capacity dynamic program, so ties resolve as PG's do.
func discreteKnapsack(maxWeight int, weights []int) map[int]bool {
	values := make([]float64, maxWeight+1)
	sets := make([]map[int]bool, maxWeight+1)
	for j := range sets {
		sets[j] = map[int]bool{}
	}
	for i, iw := range weights {
		for j := maxWeight; j >= iw && j >= 0; j-- {
			ow := j - iw
			if values[j] <= values[ow]+1 {
				if j != ow {
					cp := make(map[int]bool, len(sets[ow])+1)
					for k := range sets[ow] {
						cp[k] = true
					}
					sets[j] = cp
				}
				sets[j][i] = true
				values[j] = values[ow] + 1
			}
		}
	}
	return sets[maxWeight]
}

// costMixedRollups is create_groupingsets_path's AGG_MIXED pricing: the first
// hashed rollup reads the input under cost_agg(AGG_MIXED) — the sorted arm's
// startup plus the hash spill's — every other hashed set adds
// cost_agg(AGG_HASHED) without input cost, the first sorted rollup adds
// cost_agg(AGG_SORTED) without input cost, and each later sorted rollup its
// own sort and cost_agg. It also returns the disabled nodes those choices add:
// enable_hashagg = off counts every hashed rollup, enable_sort = off every
// rollup sort.
func costMixedRollups(cp costParams, a *Aggregate, hashed, sorted []GroupingRollup, seed *Path,
	inputRows, inputStartup, inputTotal float64, inWidth int, enableHashAgg bool) (Cost, int, bool) {
	perSet, ok := groupingSetGroupCounts(a, int64(inputRows))
	if !ok || len(hashed) == 0 || len(sorted) == 0 {
		return Cost{}, 0, false
	}
	nAggs := len(a.Aggs)
	disabled := 0
	if !enableHashAgg {
		disabled += len(hashed)
	}
	h0 := hashed[0]
	cost := costAgg(cp, AggStrategyHashed, inputRows, inputStartup, inputTotal, len(h0.Order),
		rollupGroupCount(perSet, h0), nAggs, inWidth)
	blocking := inputTotal + cp.cpuOperatorCost*float64(nAggs)*inputRows +
		cp.cpuOperatorCost*float64(len(h0.Order))*inputRows
	cost.Startup = inputStartup + (cost.Startup - blocking)
	for _, r := range hashed[1:] {
		c := costAgg(cp, AggStrategyHashed, inputRows, 0, 0, len(r.Order), rollupGroupCount(perSet, r), nAggs, inWidth)
		cost.Total += c.Total
	}
	c := costAggSortedRollup(cp, inputRows, 0, 0, len(sorted[0].Order), rollupGroupCount(perSet, sorted[0]), nAggs)
	cost.Total += c.Total
	for _, r := range sorted[1:] {
		srt := costSortRunWithWidth(cp, inputRows, pathNCols(seed), pathAvgVarBytes(seed), -1, pathWidth(seed), "rollup")
		agg := costAggSortedRollup(cp, inputRows, srt.Startup, srt.Total, len(r.Order), rollupGroupCount(perSet, r), nAggs)
		cost.Total += agg.Total
		if !cp.enableSort {
			disabled++
		}
	}
	return cost, disabled, true
}
