package planner

import "testing"

// Phase C0.1 — the pure Path library: add_path / set_cheapest dominance, the
// STD_FUZZ_FACTOR tie-break, and determinism. Nothing here touches live planning;
// these pin the substrate in isolation before C0.2 wires it in.

func costPath(startup, total float64) *Path {
	return &Path{Kind: PathSeqScan, Cost: Cost{Startup: startup, Total: total}}
}

func TestComparePathCostsFuzzily_DisabledNodesTrumpAll(t *testing.T) {
	// Fewer disabled nodes wins even at a vastly higher cost (pathnode.c:191).
	cheapDisabled := &Path{Cost: Cost{Total: 1}, DisabledNodes: 1}
	dearEnabled := &Path{Cost: Cost{Total: 1000}, DisabledNodes: 0}
	if got := comparePathCostsFuzzily(dearEnabled, cheapDisabled, stdFuzzFactor); got != costsBetter1 {
		t.Fatalf("enabled dear path should dominate disabled cheap path on disabled_nodes, got %v", got)
	}
}

func TestComparePathCostsFuzzily_TotalThenStartup(t *testing.T) {
	if got := comparePathCostsFuzzily(costPath(0, 10), costPath(0, 100), stdFuzzFactor); got != costsBetter1 {
		t.Fatalf("cheaper total should be better1, got %v", got)
	}
	// Equal total, cheaper startup decides.
	if got := comparePathCostsFuzzily(costPath(1, 100), costPath(50, 100), stdFuzzFactor); got != costsBetter1 {
		t.Fatalf("equal total, cheaper startup should be better1, got %v", got)
	}
}

func TestComparePathCostsFuzzily_Incomparable(t *testing.T) {
	// p1 dearer on total, cheaper on startup -> genuinely different.
	if got := comparePathCostsFuzzily(costPath(1, 100), costPath(50, 50), stdFuzzFactor); got != costsDifferent {
		t.Fatalf("startup/total trade-off should be costsDifferent, got %v", got)
	}
}

func TestComparePathCostsFuzzily_WithinFuzzIsEqual(t *testing.T) {
	// 100 vs 100.5 is within the 1% band on total; startups equal.
	if got := comparePathCostsFuzzily(costPath(0, 100), costPath(0, 100.5), stdFuzzFactor); got != costsEqual {
		t.Fatalf("within-fuzz costs should be equal, got %v", got)
	}
}

func TestAddPath_RejectsDominated(t *testing.T) {
	rel := &RelOptInfo{}
	addPath(rel, costPath(0, 10))
	addPath(rel, costPath(0, 100)) // strictly dearer -> rejected
	if len(rel.Pathlist) != 1 || rel.Pathlist[0].Cost.Total != 10 {
		t.Fatalf("dominated path not rejected: %+v", rel.Pathlist)
	}
}

func TestAddPath_RemovesDominatedIncumbent(t *testing.T) {
	rel := &RelOptInfo{}
	addPath(rel, costPath(0, 100))
	addPath(rel, costPath(0, 10)) // cheaper -> removes the dearer incumbent
	if len(rel.Pathlist) != 1 || rel.Pathlist[0].Cost.Total != 10 {
		t.Fatalf("dominated incumbent not removed: %+v", rel.Pathlist)
	}
}

func TestAddPath_KeepsIncomparable(t *testing.T) {
	rel := &RelOptInfo{}
	addPath(rel, costPath(1, 100))  // cheap startup, dear total
	addPath(rel, costPath(50, 50))  // dear startup, cheap total
	if len(rel.Pathlist) != 2 {
		t.Fatalf("incomparable paths should both survive, got %d", len(rel.Pathlist))
	}
}

func TestAddPath_RejectsExactDuplicate(t *testing.T) {
	rel := &RelOptInfo{}
	addPath(rel, costPath(5, 50))
	addPath(rel, costPath(5, 50)) // identical -> incumbent kept, new rejected
	if len(rel.Pathlist) != 1 {
		t.Fatalf("exact duplicate should not accumulate, got %d", len(rel.Pathlist))
	}
}

func TestAddPath_ParallelSafeIsNotDominatedByCheaper(t *testing.T) {
	rel := &RelOptInfo{}
	// A cheaper non-parallel-safe path and a dearer parallel-safe path are
	// incomparable: cheaper wins on cost, parallel-safe wins on the parallel
	// axis. Both must survive so C5 can gather the parallel-safe one.
	addPath(rel, &Path{Cost: Cost{Total: 10}, ParallelSafe: false})
	addPath(rel, &Path{Cost: Cost{Total: 20}, ParallelSafe: true})
	if len(rel.Pathlist) != 2 {
		t.Fatalf("parallel-safe path must not be dominated by a cheaper unsafe one, got %d", len(rel.Pathlist))
	}
}

func TestAddPath_WithinFuzzKeepsFirst(t *testing.T) {
	rel := &RelOptInfo{}
	first := costPath(0, 100)
	second := costPath(0, 100.5) // within 1% -> incumbent kept
	addPath(rel, first)
	addPath(rel, second)
	if len(rel.Pathlist) != 1 || rel.Pathlist[0] != first {
		t.Fatalf("within-fuzz should keep the first path deterministically, got %+v", rel.Pathlist)
	}
}

func TestSetCheapest(t *testing.T) {
	rel := &RelOptInfo{}
	addPath(rel, costPath(1, 100)) // cheapest startup
	addPath(rel, costPath(50, 50)) // cheapest total
	setCheapest(rel)
	if rel.CheapestTotal == nil || rel.CheapestTotal.Cost.Total != 50 {
		t.Fatalf("CheapestTotal wrong: %+v", rel.CheapestTotal)
	}
	if rel.CheapestStartup == nil || rel.CheapestStartup.Cost.Startup != 1 {
		t.Fatalf("CheapestStartup wrong: %+v", rel.CheapestStartup)
	}
}

func TestAddPath_Deterministic(t *testing.T) {
	// Adding the same set of paths in the same order always yields the same
	// surviving pathlist (guards the float tie-break, design ch. 07 §4).
	build := func() []*Path {
		rel := &RelOptInfo{}
		for _, p := range []*Path{costPath(1, 100), costPath(50, 50), costPath(0, 200), costPath(2, 90)} {
			addPath(rel, p)
		}
		return rel.Pathlist
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatalf("nondeterministic pathlist length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Cost != b[i].Cost {
			t.Fatalf("nondeterministic pathlist at %d: %v vs %v", i, a[i].Cost, b[i].Cost)
		}
	}
}

func TestComparePathkeysDim_EmptyIsEqual(t *testing.T) {
	if got := comparePathkeysDim(nil, nil); got != dimEqual {
		t.Fatalf("empty pathkeys should be dimEqual, got %v", got)
	}
}

func TestOuterDim_SubsetIsLessConstrained(t *testing.T) {
	// a requires {rel0}; b requires {rel0, rel1}. a ⊂ b, so a is less
	// constrained and better on the outer axis.
	if got := outerDim(RelSet(0b01), RelSet(0b11)); got != dimBetter1 {
		t.Fatalf("subset outer set should be dimBetter1, got %v", got)
	}
	if got := outerDim(RelSet(0b01), RelSet(0b10)); got != dimIncomparable {
		t.Fatalf("disjoint outer sets should be incomparable, got %v", got)
	}
}
