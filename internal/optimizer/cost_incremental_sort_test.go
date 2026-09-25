package optimizer

// M0141-S7 Finding 3, table row 2 — `cost_incremental_sort`
// (costsize.c:2000-2126) composed over the existing `costSortRunWithWidth`
// (`cost_tuplesort`). `costIncrementalSort` has zero production callers as
// of this test (same groundwork posture as `pathkeysCountContainedIn`,
// Finding 3 row 1) — it is pinned here against an independent
// transliteration of the upstream C formula, not against itself, so the
// test cannot pass by construction.

import (
	"math"
	"testing"
)

// wantIncrementalSort reimplements costsize.c:2000-2126 directly (not by
// calling costIncrementalSort or costSortRunWithWidth) so the test is an
// independent check of the formula, matching the in-memory quicksort branch
// of cost_tuplesort (comparisonCost folded to 0 per costIncrementalSort's
// own doc comment; the disk/bounded branches are exercised indirectly
// through costSortRunWithWidth's own dedicated test files, so this
// reimplementation only needs the plain-quicksort arm to keep the two
// derivations independent without duplicating the whole branch structure).
func wantIncrementalSort(cp costParams, inputStartup, inputTotal, inputTuples, inputGroups float64) (startup, total float64) {
	if inputTuples < 2.0 {
		inputTuples = 2.0
	}
	comparisonCost := 2.0 * cp.cpuOperatorCost
	inputRunCost := inputTotal - inputStartup
	groupTuples := inputTuples / inputGroups
	if groupTuples < 2.0 {
		groupTuples = 2.0
	}
	groupInputRunCost := inputRunCost / inputGroups

	groupStartupCost := comparisonCost * groupTuples * math.Log2(groupTuples)
	groupRunCost := cp.cpuOperatorCost * groupTuples

	startup = groupStartupCost + inputStartup + groupInputRunCost
	run := groupRunCost + (groupRunCost+groupStartupCost)*(inputGroups-1) + groupInputRunCost*(inputGroups-1)
	run += cp.cpuTupleCost * inputTuples
	run += 2.0 * cp.cpuTupleCost * inputGroups
	return startup, startup + run
}

func almostEqual(a, b float64) bool {
	if a == b {
		return true
	}
	d := math.Abs(a - b)
	scale := math.Max(math.Abs(a), math.Abs(b))
	return d <= scale*1e-9
}

// TestCostIncrementalSort_MatchesUpstreamFormula pins costIncrementalSort's
// in-memory quicksort branch against an independent transliteration of
// costsize.c:2000-2126 across a matrix of group counts, including the
// single-group (fully presorted) and one-row-per-group extremes.
func TestCostIncrementalSort_MatchesUpstreamFormula(t *testing.T) {
	cp := defaultCostParams()
	inputCost := Cost{Startup: 12.5, Total: 500.0}
	inputTuples := 10000.0
	ncols := 3
	for _, groups := range []float64{1, 2, 10, 100, 10000} {
		got := costIncrementalSort(cp, inputCost, inputTuples, groups, ncols, 0, -1, 0)
		wantStartup, wantTotal := wantIncrementalSort(cp, inputCost.Startup, inputCost.Total, inputTuples, groups)
		if !almostEqual(got.Startup, wantStartup) || !almostEqual(got.Total, wantTotal) {
			t.Errorf("groups=%v: got (%v,%v), want (%v,%v)", groups, got.Startup, got.Total, wantStartup, wantTotal)
		}
	}
}

// TestCostIncrementalSort_NeverExceedsSingleGroupCost pins the shape PG's
// own comment implies: splitting the input into more (smaller) groups
// shrinks the dominant `groupTuples*log2(groupTuples)` sort term faster
// than it adds the two small per-tuple/per-group overhead terms, until
// group size approaches 1 — this is the entire economic argument for
// Incremental Sort existing. The relationship is NOT monotonic all the way
// to one-row-per-group (the fixed reset overhead eventually turns the curve
// back up, confirmed by direct probing at inputGroups==inputTuples), but it
// must never cost MORE than the fully-presorted (inputGroups=1) baseline.
func TestCostIncrementalSort_NeverExceedsSingleGroupCost(t *testing.T) {
	cp := defaultCostParams()
	inputCost := Cost{Startup: 0, Total: 1000.0}
	for _, inputTuples := range []float64{10, 500, 50000} {
		baseline := costIncrementalSort(cp, inputCost, inputTuples, 1, 4, 0, -1, 0)
		for _, groups := range []float64{2, 5, 20, 100, 1000, 25000, 50000} {
			if groups > inputTuples {
				continue
			}
			got := costIncrementalSort(cp, inputCost, inputTuples, groups, 4, 0, -1, 0)
			if got.Total > baseline.Total {
				t.Errorf("inputTuples=%v groups=%v: total %v exceeds single-group baseline %v", inputTuples, groups, got.Total, baseline.Total)
			}
		}
	}
}

// TestCostIncrementalSort_SingleGroupNearsFullSort pins the boundary PG's
// own comment implies: at inputGroups=1 the incremental-sort formula
// degenerates to (approximately) a single full sort of the whole input plus
// the two small per-tuple/per-group overhead terms — it must never be
// cheaper than a from-scratch full sort of the same input (that would let
// the optimizer under-price Incremental Sort and pick it wrongly).
func TestCostIncrementalSort_SingleGroupNearsFullSort(t *testing.T) {
	cp := defaultCostParams()
	inputCost := Cost{Startup: 0, Total: 0}
	inputTuples := 20000.0
	ncols := 2
	got := costIncrementalSort(cp, inputCost, inputTuples, 1, ncols, 0, -1, 0)
	full := costSortRunWithWidth(cp, inputTuples, ncols, 0, -1, 0, "full-sort-reference")
	if got.Total < full.Total {
		t.Errorf("single-group incremental sort total %v must not undercut a full sort's %v", got.Total, full.Total)
	}
}

// TestCostIncrementalSort_ClampsDegenerateGroupCounts pins the defensive
// clamp: a caller-supplied inputGroups outside [1, inputTuples] must not
// divide by zero or produce a negative/NaN cost.
func TestCostIncrementalSort_ClampsDegenerateGroupCounts(t *testing.T) {
	cp := defaultCostParams()
	inputCost := Cost{Startup: 1, Total: 100}
	inputTuples := 1000.0
	for _, groups := range []float64{0, -5, math.Inf(1), inputTuples * 10} {
		got := costIncrementalSort(cp, inputCost, inputTuples, groups, 2, 0, -1, 0)
		if math.IsNaN(got.Total) || math.IsInf(got.Total, 0) || got.Total < 0 {
			t.Errorf("groups=%v: got degenerate total %v", groups, got.Total)
		}
	}
}
