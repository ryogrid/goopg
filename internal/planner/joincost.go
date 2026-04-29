// Cost-driven algorithm selection for INNER joins.
//
// `chooseInnerJoinAlgo` scores hash, merge, and nested-loop
// against unit-row costs and returns the cheapest. When either
// input lacks a row-count estimate (because ANALYZE hasn't run
// on the underlying relation), the function declines to choose
// and the caller keeps its rule-layer default of JoinAlgoHash.
//
// See docs/design/0006-0004-join-algorithm-selection.md for the
// cost units and the explicit out-of-scope items.
package planner

import "math"

// chooseInnerJoinAlgo returns the cheapest INNER-join algorithm
// for the given input row counts. The second return value is
// false when stats were insufficient to make a choice — callers
// keep their rule-based default in that case.
func chooseInnerJoinAlgo(leftRows, rightRows int64) (JoinAlgo, bool) {
	if leftRows <= 0 || rightRows <= 0 {
		return JoinAlgoHash, false
	}
	costHash := costInnerHash(leftRows, rightRows)
	costMerge := costInnerMerge(leftRows, rightRows)
	costNL := costInnerNestLoop(leftRows, rightRows)

	best := JoinAlgoHash
	bestCost := costHash
	if costMerge < bestCost {
		best = JoinAlgoMerge
		bestCost = costMerge
	}
	if costNL < bestCost {
		best = JoinAlgoNestedLoop
	}
	return best, true
}

// costInnerHash: build on the smaller side (matches the
// build-side selection at the call site), probe with the
// larger. Each row is one unit of work — no page / IO weighting
// in v0.
func costInnerHash(l, r int64) float64 {
	build := l
	if r < l {
		build = r
	}
	probe := l + r - build
	return float64(build + probe)
}

// costInnerMerge: sort both sides then merge. Sorts dominate at
// modest scale; v0 doesn't track sortedness, so we always pay
// the full sort cost on both inputs.
func costInnerMerge(l, r int64) float64 {
	return rowSortCost(l) + rowSortCost(r) + float64(l+r)
}

// costInnerNestLoop: O(L * R) — no index-aided rescan modelled
// in v0 (the executor doesn't rebuild the inner relation per
// outer row, so the unit-row product is appropriate for what
// actually runs).
func costInnerNestLoop(l, r int64) float64 {
	return float64(l) * float64(r)
}

// rowSortCost is the unit-row N·log2(N) sort cost.
func rowSortCost(n int64) float64 {
	if n < 2 {
		return 0
	}
	return float64(n) * math.Log2(float64(n))
}
