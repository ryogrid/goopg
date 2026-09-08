package optimizer

// C-19g's REMAINDER — the upper-rel-resident partial-aggregation PATH.
//
// The rules of partialaggpaths_test.go apply unchanged: assert through the
// named `costParams` fields, never a literal, and show the candidate EXISTS
// before reading any verdict off it.

import (
	"testing"
)

// upperSplitSettings is a PlannerSettings that permits the parallel candidate:
// a top-level SELECT, parallelism enabled, four workers.
func upperSplitSettings() PlannerSettings {
	ps := DefaultPlannerSettings()
	ps.ParallelStatementOK = true
	ps.MaxParallelWorkersPerGather = 4
	ps.ParallelLeaderParticipation = true
	return ps
}

// addSplitFor runs the producer the way `createGroupingPaths` does and returns
// the GROUP_AGG rel plus the split path (nil when the producer declined).
func addSplitFor(t *testing.T, agg *Aggregate, ps PlannerSettings) (*RelOptInfo, *Path) {
	t.Helper()
	cp := ps.costParams()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	sizeGroupingRelFromAgg(grouped, agg)
	child := agg.Child
	seed := newPrebuiltPath(grouped, child)
	seed.Rows = float64(EstimateRows(child))
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}
	if seed.Cost.Total <= 0 {
		// A fixture scan with no legacy price would make every candidate cost
		// the same thing; give the seed the seq-scan price its own rows imply.
		seed.Cost = costSeqscan(cp, estScanPages(seed.Rows, 32), seed.Rows, 0)
	}
	return grouped, addPartialAggSplitPath(u, grouped, seed, agg, child, cp, ps)
}

// TestUpperSplitPathIsGeneratedAndPriced is the "the candidate exists" pin —
// the one that stops every verdict test below from passing vacuously.
func TestUpperSplitPathIsGeneratedAndPriced(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2) // TPC-H Q1's shape
	grouped, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no Finalize->Gather->Partial candidate on the GROUP_AGG rel")
	}
	if split.Kind != PathFinalizeAgg {
		t.Errorf("split candidate has kind %v, want PathFinalizeAgg", split.Kind)
	}
	if split.ParallelWorkers <= 0 {
		t.Errorf("split candidate plans %d workers", split.ParallelWorkers)
	}
	if split.Cost.Total <= 0 {
		t.Errorf("split candidate priced at %v", split.Cost)
	}
	if len(split.Children) != 1 || split.Children[0].Kind != PathGather {
		t.Fatal("split candidate is not Finalize over Gather")
	}
	gather := split.Children[0]
	if len(gather.Children) != 1 || gather.Children[0].Kind != PathAgg {
		t.Fatal("split candidate's Gather is not over a partial PathAgg")
	}
	if !pathIsFiled(grouped, split) {
		t.Error("the split candidate was generated but did not survive add_path")
	}
}

// TestUpperSplitWinsForLowCardinalityGrouping: with the parallel candidate
// filed on the same rel as C-15's serial ones, `setCheapest` must pick it for
// TPC-H Q1's shape — 5.9 M rows into four groups. This is the win C-19g
// measured (8.57 s -> 4.14 s) delivered as a PATH rather than by the post-pass.
func TestUpperSplitWinsForLowCardinalityGrouping(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	ps := upperSplitSettings()
	cp := ps.costParams()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	sizeGroupingRelFromAgg(grouped, agg)
	seed := newPrebuiltPath(grouped, agg.Child)
	seed.Rows = float64(EstimateRows(agg.Child))
	seed.Cost = costSeqscan(cp, estScanPages(seed.Rows, 32), seed.Rows, 0)

	addGroupingPaths(grouped, seed, agg, agg.Child, nil, cp, ps)
	serialOnly := len(grouped.Pathlist)
	if serialOnly == 0 {
		t.Fatal("no serial candidate: the comparison would be vacuous")
	}
	split := addPartialAggSplitPath(u, grouped, seed, agg, agg.Child, cp, ps)
	if split == nil {
		t.Fatal("no parallel candidate: the comparison would be vacuous")
	}
	setCheapest(grouped)
	if grouped.CheapestTotal != split {
		t.Errorf("the serial aggregate won for Q1's shape: split=%v cheapest=%v",
			split.Cost, grouped.CheapestTotal.Cost)
	}
}

// TestUpperSplitLosesWhenGroupingReducesNothing: every row its own group, so
// nothing is pre-aggregated and the group states that cross the boundary are
// the input itself. DESIGN §3.4's second sanity reading.
func TestUpperSplitLosesWhenGroupingReducesNothing(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	const rows = 6_000_000
	agg := sizedAggFixture(t, rows, rows, 1, 1)
	ps := upperSplitSettings()
	cp := ps.costParams()
	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	sizeGroupingRelFromAgg(grouped, agg)
	seed := newPrebuiltPath(grouped, agg.Child)
	seed.Rows = rows
	seed.Cost = costSeqscan(cp, estScanPages(seed.Rows, 32), seed.Rows, 0)

	addGroupingPaths(grouped, seed, agg, agg.Child, nil, cp, ps)
	split := addPartialAggSplitPath(u, grouped, seed, agg, agg.Child, cp, ps)
	if split == nil {
		t.Skip("producer declined outright; the verdict is the same either way")
	}
	setCheapest(grouped)
	if grouped.CheapestTotal == split {
		t.Errorf("the split won for a grouping that reduces nothing: %v", split.Cost)
	}
}

// TestUpperSplitOffersTheGatheredArmForANonSplittableAggregate: a
// `count(distinct …)` cannot be split, but the Gather still belongs BELOW it —
// which is what `terminatesPartial` makes the post-pass do. Gating the whole
// producer on decomposability cost TPC-H Q16 its Gather in the C-19h census.
func TestUpperSplitOffersTheGatheredArmForANonSplittableAggregate(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	for i := range agg.Aggs {
		agg.Aggs[i].Distinct = true
	}
	if aggregateSplitIsSafe(agg) {
		t.Skip("fixture is still decomposable; the case this test exists for is unreachable")
	}
	grouped, split := addSplitFor(t, agg, upperSplitSettings())
	if split != nil {
		t.Error("a non-decomposable aggregate must not get a split candidate")
	}
	gathered := false
	for _, p := range grouped.Pathlist {
		if p.Kind == PathAgg && len(p.Children) == 1 && p.Children[0].Kind == PathGather {
			gathered = true
		}
	}
	if !gathered {
		t.Error("no Agg-over-Gather candidate: the producer refused parallelism outright")
	}
}

// TestUpperSplitFailsClosed enumerates every refusal the producer owes. Each
// one is a case where building a Gather would be wrong, not merely unprofitable
// — so a regression that turns one into a silent "yes" is the class this test
// exists to catch.
func TestUpperSplitFailsClosed(t *testing.T) {
	base := upperSplitSettings()
	cases := []struct {
		name string
		mode partialAggMode
		mut  func(*PlannerSettings)
	}{
		{"knob off", partialAggPathsOff, nil},
		{"not a top-level select", partialAggPathsOn,
			func(ps *PlannerSettings) { ps.ParallelStatementOK = false }},
		{"max_parallel_workers_per_gather = 0", partialAggPathsOn,
			func(ps *PlannerSettings) { ps.MaxParallelWorkersPerGather = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := setPartialAggPathsModeForTest(tc.mode)
			defer restore()
			ps := base
			if tc.mut != nil {
				tc.mut(&ps)
			}
			agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
			if _, split := addSplitFor(t, agg, ps); split != nil {
				t.Error("the producer filed a parallel candidate it must refuse")
			}
		})
	}
}

// TestUpperSplitBuildsTheSameShapeAsThePostPass is rule #2 (sibling paths must
// agree) as a test: the path model's `createPlan` arm and `MaybeAddGather` must
// emit the same node shape, because they call the same constructor. A future
// edit that re-implements the construction inside the arm breaks this.
func TestUpperSplitBuildsTheSameShapeAsThePostPass(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	_, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no parallel candidate")
	}
	node, _ := createPlanNode(split)
	final, ok := node.(*Aggregate)
	if !ok {
		t.Fatalf("the arm built a %T, want *Aggregate", node)
	}
	if final.Mode != AggModeFinal {
		t.Errorf("root aggregate mode is %v, want AggModeFinal", final.Mode)
	}
	if final.PartialSource == nil {
		t.Fatal("the Finalize node has no PartialSource; the Partial emits no rows and the plan returns nothing")
	}
	g, ok := final.Child.(*Gather)
	if !ok {
		t.Fatalf("the Finalize node's child is a %T, want *Gather", final.Child)
	}
	partial, ok := g.Child.(*Aggregate)
	if !ok || partial.Mode != AggModePartial {
		t.Fatalf("the Gather's child is %T, want a Partial *Aggregate", g.Child)
	}
	if partial != final.PartialSource {
		t.Error("PartialSource does not point at the aggregate the Gather runs")
	}
	if drivingScan(partial.Child) == nil {
		t.Error("the partial subtree has no driving scan: every worker would read the whole relation")
	}
}

// TestStripGatherFoldsASplitAggregateBack is the post-cache enforcement half.
// Dropping the Gather alone would leave a Finalize over a node that emits NO
// ROWS at all, so the fold-back to the simple aggregate is not tidiness.
func TestStripGatherFoldsASplitAggregateBack(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	_, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no parallel candidate")
	}
	node, _ := createPlanNode(split)
	stripped := StripGather(node)
	simple, ok := stripped.(*Aggregate)
	if !ok {
		t.Fatalf("StripGather returned a %T, want *Aggregate", stripped)
	}
	if simple.Mode != AggModeSimple || simple.PartialSource != nil {
		t.Errorf("the split did not fold back: mode=%v partialSource=%v",
			simple.Mode, simple.PartialSource != nil)
	}
	if _, isGather := simple.Child.(*Gather); isGather {
		t.Error("a Gather survived StripGather")
	}
	if scan, isScan := simple.Child.(*SeqScan); !isScan || scan.Parallel {
		t.Errorf("the driving scan is %T and still carries the Parallel label", simple.Child)
	}
	// And the enforcement path callers actually take.
	if got := MaybeAddGather(node, ParallelSettings{MaxWorkersPerGather: 0}); got == node {
		t.Error("MaybeAddGather returned a parallel plan for a session with max_parallel_workers_per_gather = 0")
	}
}
