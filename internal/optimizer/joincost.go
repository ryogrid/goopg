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
package optimizer

import (
	"math"
	"os"
)

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

// hashOuterJoinEnabled gates whether the planner may SELECT a hash
// path for RIGHT/FULL. The executor's ability to run one is
// unconditional as of M0127-P4.2; what is gated is the default
// plan-shape change, and the reason is measured rather than
// cautious — see chooseOuterFillJoinAlgo's second paragraph.
//
// GOOPG_HASH_OUTER_JOIN=1 turns it on for the A/B; the default
// flip is M0127-P5's, once doc 04's cost currency can say when a
// sort is actually cheaper (deferral ledger 2026-08-04
// M0127-P4.2).
var hashOuterJoinEnabled = hashOuterJoinFromEnv(os.Getenv("GOOPG_HASH_OUTER_JOIN"))

// hashOuterJoinFromEnv is the flag's polarity, factored out so the provenance
// table (flaglabels.go) renders the unset default from the same function
// production resolves it with; see memoizeFromEnv.
func hashOuterJoinFromEnv(v string) bool { return v == "1" }

// chooseOuterFillJoinAlgo is the same decision for the two join
// types whose preserved side the hash executor could not fill
// until M0127-P4.2 (design leftdeep-joins/07 §3): RIGHT and FULL.
//
// It scores only hash against merge. Nested loop is excluded on
// purpose — it is legal for these types (runNestedLoop implements
// both) but it drains BOTH inputs into memory, and the unit-row
// model that lets costInnerNestLoop win on tiny inputs does not
// know that. Unpinning RIGHT/FULL from merge is this milestone's
// point; unpinning them onto an unbounded operator is not.
//
// Declining (ok=false) leaves the caller on merge.
//
// The gate is what the MEASUREMENT said, not caution. Flipping the
// default outright keeps every row (the multisets are identical —
// that is what the executor tests assert) but changes their ORDER
// on unordered queries, and PG 18.3 picks `Merge Right Join` /
// `Merge Full Join` for exactly the fixtures the regress `join`
// test is built from (verified directly against the oracle on
// J1_TBL/J2_TBL). Measured on the regress outer-join files, an
// unconditional flip moves `join` 210 diff lines FURTHER from
// upstream's expected output, all of it row order. goopg picks
// hash there because costInnerMerge charges a full sort of both
// sides with no constant factors, so an 11-row sort prices like a
// real one; PG's model knows better. Closing that gap is doc 04's
// "one cost currency", i.e. M0127-P5 — until then the capability
// ships and the default does not.
func chooseOuterFillJoinAlgo(leftRows, rightRows int64) (JoinAlgo, bool) {
	if !hashOuterJoinEnabled || leftRows <= 0 || rightRows <= 0 {
		return JoinAlgoMerge, false
	}
	if costInnerHash(leftRows, rightRows) <= costInnerMerge(leftRows, rightRows) {
		return JoinAlgoHash, true
	}
	return JoinAlgoMerge, true
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
