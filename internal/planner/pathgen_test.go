package planner

import "testing"

// Phase C3.2 — path generation primitives. Pure functions over RelOptInfo;
// nothing live calls them yet (C4 wires them into the DP and switches selection).

func TestGenerateScanPaths_SerialOnly(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 100000, 40)
	generateScanPaths(rel, cp, 1000, 1, 0 /*no parallel*/, true)
	if len(rel.Pathlist) != 1 {
		t.Fatalf("want 1 serial scan path, got %d", len(rel.Pathlist))
	}
	if len(rel.PartialPathlist) != 0 {
		t.Fatalf("no partial path expected when workers=0, got %d", len(rel.PartialPathlist))
	}
	if rel.Pathlist[0].Kind != PathSeqScan || rel.Pathlist[0].ParallelSafe {
		t.Fatalf("serial scan path malformed: %+v", rel.Pathlist[0])
	}
}

func TestGenerateScanPaths_PartialIsDivided(t *testing.T) {
	cp := defaultCostParams()
	rel := newRelOptInfo(RelSet(1), 100000, 40)
	generateScanPaths(rel, cp, 1000, 1, 2 /*workers*/, true)
	if len(rel.PartialPathlist) != 1 {
		t.Fatalf("want 1 partial path, got %d", len(rel.PartialPathlist))
	}
	serial := rel.Pathlist[0].Cost.Total
	partial := rel.PartialPathlist[0].Cost.Total
	d := getParallelDivisor(2, true) // 2.4
	if partial >= serial {
		t.Fatalf("partial cost %v must be less than serial %v", partial, serial)
	}
	if diff := partial - serial/d; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("partial cost %v should be serial/%v = %v", partial, d, serial/d)
	}
	if rel.PartialPathlist[0].ParallelWorkers != 2 {
		t.Fatalf("partial path must carry the worker count")
	}
}

func TestGenerateHashJoinPaths_KeepsCheaperBuildSide(t *testing.T) {
	cp := defaultCostParams()
	// A big fact (outer) and a small dimension (inner). Building the small side
	// is cheaper, so the surviving cheapest path should hash the small inner.
	fact := newRelOptInfo(RelSet(0b01), 1000000, 60)
	dim := newRelOptInfo(RelSet(0b10), 100, 40)
	generateScanPaths(fact, cp, 10000, 0, 0, true)
	generateScanPaths(dim, cp, 2, 0, 0, true)
	setCheapest(fact)
	setCheapest(dim)

	joinRel := newRelOptInfo(RelSet(0b11), 1000000, 100)
	// One hash key, no residual. The clause's contents do not matter here —
	// only its count reaches hashJoinCost.
	generateHashJoinPaths(joinRel, fact, dim, cp, []*restrictInfo{{}}, nil)
	setCheapest(joinRel)

	if joinRel.CheapestTotal == nil {
		t.Fatalf("no join path generated")
	}
	// The cheapest orientation builds the small dimension: Children[1] is the
	// build side (Children[0] is the probe), so it should be the dim's path.
	build := joinRel.CheapestTotal.Children[1]
	if build != dim.CheapestTotal {
		t.Fatalf("cheapest hash join should build the small dimension, not the fact")
	}
	// Both orientations are generated; the dearer (build the fact) is pruned or
	// kept only if incomparable. At least the cheaper survives.
	if len(joinRel.Pathlist) < 1 {
		t.Fatalf("expected at least one surviving join path")
	}
}

// relWithScanCost makes a rel carrying a single scan path of the given cost, with
// setCheapest already run — a convenient stand-in for a DP child rel in tests.
func relWithScanCost(relids RelSet, rows float64, total float64) *RelOptInfo {
	rel := newRelOptInfo(relids, rows, 40)
	addPath(rel, &Path{Kind: PathSeqScan, Rel: rel, Rows: rows, Cost: Cost{Total: total}})
	setCheapest(rel)
	return rel
}

// TestGenerateNLIPath_RuinousForLargeOuter is the Q9 lesson (ch. 07 §4.5): an
// NL-index join is cheap when the outer is small (few probes) but ruinous when the
// outer is large — the distinction a binary-hash-only cost model could not make.
func TestGenerateNLIPath_RuinousForLargeOuter(t *testing.T) {
	cp := defaultCostParams()
	bigInner := relWithScanCost(RelSet(0b10), 1000000, 10000)

	smallOuter := relWithScanCost(RelSet(0b01), 100, 5)
	jSmall := newRelOptInfo(RelSet(0b11), 100, 40)
	generateNLIPath(jSmall, smallOuter, bigInner, cp)
	setCheapest(jSmall)
	nliSmall := jSmall.CheapestTotal.Cost.Total // ~ 100*8 + 5

	bigOuter := relWithScanCost(RelSet(0b01), 6000000, 60000)
	jLarge := newRelOptInfo(RelSet(0b11), 6000000, 40)
	generateNLIPath(jLarge, bigOuter, bigInner, cp)
	setCheapest(jLarge)
	nliLarge := jLarge.CheapestTotal.Cost.Total // ~ 6M*8

	if nliSmall > 10000 {
		t.Fatalf("NLI over a 100-row outer must be cheap, got %v", nliSmall)
	}
	if nliLarge < 1e6 {
		t.Fatalf("NLI over a 6M-row outer must be very expensive, got %v", nliLarge)
	}
	// The parameterized path records its outer dependency.
	if jLarge.CheapestTotal.RequiredOuter != bigInner.Relids {
		t.Fatalf("NLI path must record RequiredOuter = the indexed inner's relids")
	}
}

func TestGenerateMultiHashJoinPath(t *testing.T) {
	cp := defaultCostParams()
	probe := relWithScanCost(RelSet(0b1000), 6000000, 60000) // driving lineitem
	d1 := relWithScanCost(RelSet(0b0001), 1500000, 15000)    // orders
	d2 := relWithScanCost(RelSet(0b0010), 10000, 100)        // supplier
	d3 := relWithScanCost(RelSet(0b0100), 25, 1)             // nation
	joinRel := newRelOptInfo(RelSet(0b1111), 6000000, 100)
	generateMultiHashJoinPath(joinRel, probe, []*RelOptInfo{d1, d2, d3}, cp)
	setCheapest(joinRel)
	if joinRel.CheapestTotal == nil || joinRel.CheapestTotal.Kind != PathMultiHash {
		t.Fatalf("expected a MultiHashJoin path")
	}
	if len(joinRel.CheapestTotal.Children) != 4 {
		t.Fatalf("MHJ path should carry the probe + 3 dims, got %d children", len(joinRel.CheapestTotal.Children))
	}
}

func TestGenerateHashJoinPaths_NoChildCheapestIsNoop(t *testing.T) {
	cp := defaultCostParams()
	joinRel := newRelOptInfo(RelSet(0b11), 10, 10)
	outer := newRelOptInfo(RelSet(0b01), 10, 10) // no paths -> CheapestTotal nil
	inner := newRelOptInfo(RelSet(0b10), 10, 10)
	generateHashJoinPaths(joinRel, outer, inner, cp, []*restrictInfo{{}}, nil)
	if len(joinRel.Pathlist) != 0 {
		t.Fatalf("no join paths should be generated without child cheapest paths")
	}
}
