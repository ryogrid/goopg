package optimizer

// C-19g's REMAINDER — the upper-rel-resident partial-aggregation PATH.
//
// The rules of partialaggpaths_test.go apply unchanged: assert through the
// named `costParams` fields, never a literal, and show the candidate EXISTS
// before reading any verdict off it.

import (
	"bufio"
	"io"
	"os"
	"strings"
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

// captureUpperSplit runs the producer the way `createGroupingPaths` does with
// the DP trace forced on and returns the split path plus the one `upper` line
// it wrote to stderr. `captureTracePath`'s shape (pathjointype_test.go): the
// gate is process-global by design, so the test pins and restores it.
func captureUpperSplit(t *testing.T, agg *Aggregate, ps PlannerSettings) (*Path, string) {
	t.Helper()
	enableDPTrace(t)
	oldErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	_, split := addSplitFor(t, agg, ps)
	os.Stderr = oldErr
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("read trace: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close read end: %v", err)
	}
	return split, strings.TrimSpace(line)
}

// TestUpperSplitAdmitsWithTraceLine: R54 Step-0's S3 record for the upper-rel
// route. TPC-H Q1's shape admits with a split candidate AND emits
// `gate=agg-upper verdict=split` — the positive control the measurement reads
// Q5/Q84's refusals against. Gate name "agg-upper" keeps this producer
// distinct from the post-pass "agg" consumer: independent verdicts over the
// same aggregate that must never be conflated.
func TestUpperSplitAdmitsWithTraceLine(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2) // TPC-H Q1's shape
	split, line := captureUpperSplit(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no Finalize->Gather->Partial candidate on the GROUP_AGG rel")
	}
	for _, want := range []string{"DPTRACE upper gate=agg-upper verdict=split", "workers=", "divisor="} {
		if !strings.Contains(line, want) {
			t.Errorf("upper line missing %q: %q", want, line)
		}
	}
}

// TestUpperSplitModeRefusalWithTraceLine: with the knob off the producer files
// nothing and the line says which gate refused — a silent nil would leave
// Step-0 unable to tell "mode off" from "subtree unusable".
func TestUpperSplitModeRefusalWithTraceLine(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOff)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	split, line := captureUpperSplit(t, agg, upperSplitSettings())
	if split != nil {
		t.Fatal("producer filed a candidate with the knob off")
	}
	if want := "DPTRACE upper gate=agg-upper verdict=refused gate=mode"; line != want {
		t.Errorf("upper line = %q, want %q", line, want)
	}
}

// TestUpperSplitVerdictNames: the admitted shape vocabulary — "split" files
// the Finalize->Gather->Partial candidate, "nosplit" files only the gathered
// no-split arms (an unsplittable aggregate still admits the round; cost
// adjudication downstream decides, not this producer).
func TestUpperSplitVerdictNames(t *testing.T) {
	if got := upperSplitVerdict(&Path{}); got != "split" {
		t.Errorf("upperSplitVerdict(non-nil) = %q, want split", got)
	}
	if got := upperSplitVerdict(nil); got != "nosplit" {
		t.Errorf("upperSplitVerdict(nil) = %q, want nosplit", got)
	}
	if got := upperSplitDetail(4, 2.5); got != "workers=4 divisor=2.5" {
		t.Errorf("upperSplitDetail = %q, want workers=4 divisor=2.5", got)
	}
}

// TestR56TracedDecompositionPins is R56 §4's first must-hold: the Q7-top
// traced decomposition (SCOPE §1.ii, `start-q78-top-r55.log`, ×2 stable)
// reproduced by unit arithmetic BEFORE the GatherMerge arm lands. Inputs are
// the trace's own — N=5874 leader / 1468 per worker, G=2 final groups,
// 1 agg, 3 group cols, workers=4, stock cost defaults — and every want is
// the trace's own add to the cent. Epsilons are 2dp-plus-slack; the bar they
// feed (SCOPE §3, [149350,149550]) is 200 wide.
//
// What this pins: costAgg contributes 58.76 to BOTH sides of the
// gathered-vs-sorted margin (zero, net — the NO-MECHANISM verdict), the
// entire 382.40 sits in the leader Sort at N=5874, and the SCOPE §3
// prediction reassembles from the same components to ~149460, in-bar.
func TestR56TracedDecompositionPins(t *testing.T) {
	cp := DefaultPlannerSettings().costParams()
	const (
		leaderRows    = 5874.0
		workerRows    = 1468.0
		partialGroups = 1468.0
		finalGroups   = 2.0
		nGroupCols    = 3
		nAggs         = 1
		workers       = 4
	)
	near := func(name string, got, want, eps float64) {
		t.Helper()
		if d := got - want; d < -eps || d > eps {
			t.Errorf("%s = %.4f, want %.4f (±%.4f)", name, got, want, eps)
		}
	}

	// costSortRun prices comparisons as STARTUP + cpuOperatorCost per tuple,
	// so a Sort node's add over its input is s.Total. Width only matters past
	// the spill threshold (64MB work_mem vs ~1.5MB here), so representative
	// column stats stand in for the trace's width-248.
	const repNcols = 8
	const repAvgVar = 32.0

	// The upper adds, isolated by pricing over a zero input.
	sortedAdd := costAgg(cp, AggStrategySorted, leaderRows, 0, 0,
		nGroupCols, finalGroups, nAggs, 0, 0).Total
	near("sorted upper add", sortedAdd, 58.76, 0.01)
	hashedAdd := costAgg(cp, AggStrategyHashed, leaderRows, 0, 0,
		nGroupCols, finalGroups, nAggs, 0, 0).Total
	near("hashed upper add", hashedAdd, 58.76, 0.01)
	if sortedAdd != hashedAdd {
		t.Errorf("sorted/hashed upper adds differ (%f vs %f): a strategy-dependent "+
			"term would own part of the margin", sortedAdd, hashedAdd)
	}
	// The partial add prices per-worker rows into per-worker groups (no
	// reduction at this level: 1468 groups out of 1468 rows).
	partialAdd := costAgg(cp, AggStrategyHashed, workerRows, 0, 0,
		nGroupCols, partialGroups, nAggs, 0, 0).Total
	near("partial add", partialAdd, 33.03, 0.01)
	// The split finalize prices the crossed group-states (1468×4=5872).
	finalizeAdd := costAgg(cp, AggStrategyHashed, workerRows*workers, 0, 0,
		nGroupCols, finalGroups, nAggs, 0, 0).Total
	near("split finalize add", finalizeAdd, 58.75, 0.01)

	// The margin itself: the leader Sort at N=5874.
	sortAdd := costSortRun(cp, leaderRows, repNcols, repAvgVar, -1).Total
	near("leader Sort(5874) add", sortAdd, 382.40, 0.05)
	workerSort := costSortRun(cp, workerRows, repNcols, repAvgVar, -1).Total
	near("worker Sort(1468)", workerSort, 80.89, 0.05)

	// The boundary runs over 5874 rows. GatherMerge's heap-creation startup
	// (~0.06) rides along; the run delta is what the prediction prices.
	sub := Cost{Startup: 100, Total: 1000}
	gm := gatherMergeCost(cp, sub, workers, leaderRows)
	gmRun := (gm.Total - gm.Startup) - (sub.Total - sub.Startup)
	near("GatherMerge run over 5874", gmRun, 699.66, 0.05)
	g := gatherCost(cp, sub, leaderRows)
	gatherRun := (g.Total - g.Startup) - (sub.Total - sub.Startup)
	near("Gather run over 5874", gatherRun, 587.40, 0.01)

	// SCOPE §3 reassembly: 149651.11 − [(587.40+382.40) − (80.89+699.66)]
	// ≈ 149460, inside [149350, 149550]. Setup (1000) and pseed cancel.
	predicted := 149651.11 - (gatherRun + sortAdd) + (workerSort + gmRun)
	if predicted < 149350 || predicted > 149550 {
		t.Errorf("predicted rival = %.2f, outside the [149350, 149550] bar", predicted)
	}
}

// TestR56GatherMergeArmFiledAndEvictsLeaderSort is the implementation round's
// second test: the candidate EXISTS on the GROUP_AGG rel with the
// GroupAgg→GatherMerge→Sort shape, and the old leader-sort sorted arm is GONE
// — the two sorted no-split arms carry identical pathkeys, so `add_path`
// keeps exactly one, the cheaper (SCOPE §2: "It competes in `add_path;
// setCheapest adjudicates"). Q7's shape: 3 group columns, 1 aggregate, 2
// groups; the row count echoes the Q1 fixture its neighbours use so admission
// (workers, divisor) is certain and only the sorted-arm contest is exercised.
func TestR56GatherMergeArmFiledAndEvictsLeaderSort(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	if partialAggGatherMergeProducer != "upper.groupagg.gathermerge" {
		t.Errorf("gathermerge producer = %q, want upper.groupagg.gathermerge",
			partialAggGatherMergeProducer)
	}

	agg := sizedAggFixture(t, 5_900_000, 2, 1, 3)
	grouped, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no split candidate: the comparison would be vacuous")
	}

	var sorted []*Path
	var gmArm *Path
	for _, p := range grouped.Pathlist {
		if p.Kind != PathAgg || p.AggStrategy != AggStrategySorted {
			continue
		}
		sorted = append(sorted, p)
		if len(p.Children) == 1 && p.Children[0].Kind == PathGatherMerge {
			gmArm = p
		}
	}
	if len(sorted) != 1 {
		t.Fatalf("%d sorted no-split arms survive on the GROUP_AGG rel, want exactly 1 (the gathermerge arm evicts the leader-sort arm)", len(sorted))
	}
	if gmArm == nil {
		t.Fatal("the surviving sorted arm is not the GroupAgg→GatherMerge→Sort shape")
	}
	if !pathIsFiled(grouped, gmArm) {
		t.Error("the gathermerge arm was generated but did not survive add_path")
	}
	gm := gmArm.Children[0]
	if len(gm.Pathkeys) == 0 {
		t.Error("GatherMerge arm carries no pathkeys: createPlan would panic it into a plain Gather")
	}
	if len(gm.Children) != 1 || gm.Children[0].Kind != PathSort {
		t.Fatal("GatherMerge arm is not over a worker Sort")
	}
	ws := gm.Children[0]
	if ws.ParallelWorkers <= 0 {
		t.Error("worker Sort plans no workers: gatherChildPlan would refuse it as single_copy")
	}
	if len(ws.Children) != 1 {
		t.Fatal("worker Sort has no input seed")
	}
	if gmArm.Cost.Total <= 0 || gm.Cost.Total <= 0 || ws.Cost.Total <= 0 {
		t.Errorf("gathermerge chain priced non-positive: agg=%v gm=%v sort=%v",
			gmArm.Cost, gm.Cost, ws.Cost)
	}
}

// TestUpperSplitInNestedScopeNeedsExistingGather pins M0146-0003a: a nested
// planning scope (ParallelStatementOK false — a subquery leaf such as TPC-DS
// Q90's `am`) splits an aggregate whose input the search already put under a
// Gather, because Finalize(Gather(Partial(X))) only moves the aggregation
// below that Gather; without one, the statement-level refusal stands, since a
// NEW Gather there could land under another parallel-aware node.
func TestUpperSplitInNestedScopeNeedsExistingGather(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	ps := upperSplitSettings()
	ps.ParallelStatementOK = false

	bare := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	if _, split := addSplitFor(t, bare, ps); split != nil {
		t.Error("nested scope without a Gather must keep the statement-level refusal")
	}

	gathered := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	gathered.Child = NewGather(0, gathered.Child, 2)
	_, split := addSplitFor(t, gathered, ps)
	if split == nil {
		t.Fatal("nested scope over an existing Gather must offer the Finalize->Gather->Partial split")
	}
	if len(split.Children) != 1 || split.Children[0].Kind != PathGather {
		t.Fatal("split candidate is not Finalize over Gather")
	}
}

// ── M0146-0025: partial Group arm (aggregate-free GROUP BY) ────────────────
//
// PG's create_partial_grouping_paths files create_group_path partial paths
// for !hasAggs (planner.c:7570): Group -> Gather Merge -> Group -> Sort.
// goopg's zero-row Partial/Finalize model cannot express it, so the arm
// files an ORDINARY sorted dedup once per worker — admitted only under the
// PartialGroup marker that lets the driving-scan walks see through it.

// partialGroupChain digs `Group -> Gather Merge -> Group -> Sort` out of a
// filed path, returning nil at the first wrong link.
func partialGroupChain(p *Path) (gm, partial, workerSort *Path) {
	if p == nil || p.Kind != PathAgg || p.AggStrategy != AggStrategySorted {
		return nil, nil, nil
	}
	if p.Agg == nil || p.Agg.PartialGroup {
		return nil, nil, nil
	}
	if len(p.Children) != 1 || p.Children[0].Kind != PathGatherMerge {
		return nil, nil, nil
	}
	gm = p.Children[0]
	if len(gm.Children) != 1 || gm.Children[0].Kind != PathAgg {
		return nil, nil, nil
	}
	partial = gm.Children[0]
	if partial.Agg == nil || !partial.Agg.PartialGroup {
		return nil, nil, nil
	}
	if len(partial.Children) != 1 || partial.Children[0].Kind != PathSort {
		return nil, nil, nil
	}
	workerSort = partial.Children[0]
	return gm, partial, workerSort
}

func TestPartialGroupArmFilesThePGShape(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 4, 0, 2)
	grouped, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("aggregate-free GROUP BY produced no parallel candidate")
	}
	gm, partial, workerSort := partialGroupChain(split)
	if gm == nil {
		t.Fatal("candidate is not Group -> Gather Merge -> Group -> Sort")
	}
	if partial.ParallelWorkers <= 0 || workerSort.ParallelWorkers <= 0 {
		t.Fatal("worker-side Group/Sort carry no planned workers")
	}
	if len(gm.Pathkeys) == 0 || len(partial.Pathkeys) == 0 {
		t.Fatal("the merge carries no pathkeys; the boundary is not ordered")
	}
	// The final spec's keys must be rewritten to the partial output's own
	// positions — PG's setrefs OUTER_VAR step for the leader Group.
	for i, e := range split.Agg.GroupExprs {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Index != i {
			t.Fatalf("final GroupExprs[%d] = %#v, want ColumnRef at partial output position %d", i, e, i)
		}
	}
	// The partial spec keeps input-coordinate keys and the marker.
	if cr, ok := partial.Agg.GroupExprs[0].(*ColumnRef); !ok || cr.Index != 0 {
		t.Fatalf("partial GroupExprs[0] = %#v, want input-coordinate ColumnRef", partial.Agg.GroupExprs[0])
	}
	if len(grouped.Pathlist) == 0 {
		t.Fatal("no path filed on the grouped rel")
	}
}

func TestPartialGroupArmRefusals(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	t.Run("expression key", func(t *testing.T) {
		agg := sizedAggFixture(t, 5_900_000, 4, 0, 1)
		agg.GroupExprs[0] = &FuncCall{Name: "lower", Args: []Expr{agg.GroupExprs[0]}}
		_, split := addSplitFor(t, agg, upperSplitSettings())
		if gm, _, _ := partialGroupChain(split); gm != nil {
			t.Fatal("an expression group key must not admit the partial-Group arm")
		}
	})
	t.Run("passthrough", func(t *testing.T) {
		agg := sizedAggFixture(t, 5_900_000, 4, 0, 1)
		agg.Passthrough = []Expr{&ColumnRef{Index: 2, Name: "v"}}
		_, split := addSplitFor(t, agg, upperSplitSettings())
		if gm, _, _ := partialGroupChain(split); gm != nil {
			t.Fatal("a passthrough column must not admit the partial-Group arm")
		}
	})
	t.Run("no group keys", func(t *testing.T) {
		agg := sizedAggFixture(t, 5_900_000, 4, 0, 0)
		if _, split := addSplitFor(t, agg, upperSplitSettings()); split != nil {
			t.Fatal("an ungrouped aggregate-free query admits no parallel candidate")
		}
	})
	t.Run("aggregate calls keep the split arm only", func(t *testing.T) {
		agg := sizedAggFixture(t, 5_900_000, 4, 1, 2)
		_, split := addSplitFor(t, agg, upperSplitSettings())
		if gm, _, _ := partialGroupChain(split); gm != nil {
			t.Fatal("an aggregate call must never take the partial-Group arm")
		}
	})
}

// TestPartialGroupWalkAgreement pins the sibling contract: drivingScan,
// stampParallelScan, unstampParallelScan and drivingScanCrossesSort admit
// exactly the producer-marked partial Group and treat every other aggregate
// as a wall.
func TestPartialGroupWalkAgreement(t *testing.T) {
	scan := sizedAggFixture(t, 100, 4, 0, 1).Child
	marked := &Aggregate{Child: scan, PartialGroup: true}
	unmarked := &Aggregate{Child: scan}

	if got := drivingScan(marked); got != Node(scan) {
		t.Fatalf("drivingScan(marked) = %#v, want the scan", got)
	}
	if got := drivingScan(unmarked); got != nil {
		t.Fatalf("drivingScan(unmarked) = %#v, want nil — an ordinary dedup is a wall", got)
	}

	stamped := stampParallelScan(marked)
	st, ok := stamped.(*Aggregate)
	if !ok {
		t.Fatalf("stampParallelScan returned %T, want *Aggregate", stamped)
	}
	if s, ok := st.Child.(*SeqScan); !ok || !s.Parallel {
		t.Fatal("stampParallelScan did not reach the scan under the marked partial Group")
	}
	if got := stampParallelScan(unmarked); got != Node(unmarked) {
		t.Fatal("stampParallelScan descended through an unmarked aggregate")
	}

	unstamped := unstampParallelScan(st)
	ut := unstamped.(*Aggregate)
	if s, ok := ut.Child.(*SeqScan); !ok || s.Parallel {
		t.Fatal("unstampParallelScan did not strip the label under the marked partial Group")
	}
	if !drivingScanCrossesSort(&Aggregate{PartialGroup: true, Child: &Sort{Child: scan}}) {
		t.Fatal("drivingScanCrossesSort must see the Sort under the marked partial Group")
	}
	if drivingScanCrossesSort(&Aggregate{Child: &Sort{Child: scan}}) {
		t.Fatal("drivingScanCrossesSort descended through an unmarked aggregate")
	}
}

// TestPartialGroupLowering runs the filed path through createPlanNode and
// pins the emitted node chain: leader Group -> Gather Merge -> per-worker
// marked Group -> Sort -> Parallel SeqScan.
func TestPartialGroupLowering(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 4, 0, 2)
	_, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("no candidate")
	}
	n, _ := createPlanNode(split)
	leader, ok := n.(*Aggregate)
	if !ok {
		t.Fatalf("top node is %T, want *Aggregate", n)
	}
	gm, ok := leader.Child.(*GatherMerge)
	if !ok {
		t.Fatalf("leader child is %T, want *GatherMerge", leader.Child)
	}
	partial, ok := gm.Child.(*Aggregate)
	if !ok || !partial.PartialGroup {
		t.Fatalf("merge child is %T (PartialGroup=%v), want marked *Aggregate", gm.Child, partial != nil && partial.PartialGroup)
	}
	if _, ok := partial.Child.(*Sort); !ok {
		t.Fatalf("partial child is %T, want *Sort", partial.Child)
	}
	scan := drivingScan(gm.Child)
	if scan == nil {
		t.Fatal("the worker subtree has no driving scan — the walks disagree with the producer")
	}
	if s, ok := scan.(*SeqScan); !ok || !s.Parallel {
		t.Fatalf("driving scan is %#v, want a parallel-stamped *SeqScan", scan)
	}
}
