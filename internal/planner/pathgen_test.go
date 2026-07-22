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
	generateHashJoinPaths(joinRel, fact, dim, cp, 1)
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

func TestGenerateHashJoinPaths_NoChildCheapestIsNoop(t *testing.T) {
	cp := defaultCostParams()
	joinRel := newRelOptInfo(RelSet(0b11), 10, 10)
	outer := newRelOptInfo(RelSet(0b01), 10, 10) // no paths -> CheapestTotal nil
	inner := newRelOptInfo(RelSet(0b10), 10, 10)
	generateHashJoinPaths(joinRel, outer, inner, cp, 1)
	if len(joinRel.Pathlist) != 0 {
		t.Fatalf("no join paths should be generated without child cheapest paths")
	}
}
