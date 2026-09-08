package optimizer

// C-19e (P5-05): the `Gather Merge -> Sort -> partial` verdict as a priced
// two-candidate tournament instead of a type switch.
//
// The tests below pin the three properties that make the replacement safe to
// carry at `off` and honest to flip at `on`:
//
//  1. INERTNESS. At the default the tournament is not consulted at all, so the
//     serial control arm and the default arm are bit-identical to C-19g's
//     behaviour. This is the property the whole flag exists to give.
//  2. The tournament REACHES A VERDICT from cost, and the verdict responds to
//     the cost inputs rather than to the node type — which is the difference
//     between a cost model and a rule wearing one's clothes.
//  3. The `mergeKeys` invariant survives: a per-worker Sort is produced ONLY
//     under a Gather Merge (parallel_merge_test.go's file header — a plain
//     Gather over per-worker Sorts returns unordered rows with no crash).

import (
	"testing"
)

// TestPartialSortModeFromEnvIsFailClosed pins the resolver's polarity. A typo
// must not silently enable a plan shape whose measurement has not been run.
func TestPartialSortModeFromEnvIsFailClosed(t *testing.T) {
	for _, v := range []string{"", "off", "OFF", "no", "yes", "onn", " ", "2"} {
		if got := partialSortModeFromEnv(v); got != partialSortPathsOff {
			t.Errorf("partialSortModeFromEnv(%q) = %v, want off", v, got)
		}
	}
	for _, v := range []string{"on", "ON", " on ", "paths", "true", "1"} {
		if got := partialSortModeFromEnv(v); got != partialSortPathsOn {
			t.Errorf("partialSortModeFromEnv(%q) = %v, want on", v, got)
		}
	}
	// The label must round-trip through the resolver — flaglabels.go's
	// contract: the token inside `unset(…)` re-exported reproduces the arm.
	for _, m := range []partialSortMode{partialSortPathsOff, partialSortPathsOn} {
		if got := partialSortModeFromEnv(partialSortModeLabel(m)); got != m {
			t.Errorf("label round-trip failed for %v: label=%q resolved=%v",
				m, partialSortModeLabel(m), got)
		}
	}
}

// TestPartialSortDefaultDelegatesToRetiredRule is the inertness proof. At `off`
// the verdict must be the retired type switch's, for every input — including
// the ones the tournament would answer differently.
func TestPartialSortDefaultDelegatesToRetiredRule(t *testing.T) {
	if partialSortPathsMode != partialSortPathsOff {
		t.Fatalf("package default is %v, want off — the flag ships off",
			partialSortPathsMode)
	}
	tbl := bigTable(t, "psp_default")
	srt := sortOver(seqScanOver(tbl), testKeys())
	for _, workers := range []int{0, 1, 2, 4} {
		want := sortPartialRootPays(srt)
		if got := partialSortRootPays(srt, workers, true); got != want {
			t.Errorf("workers=%d: partialSortRootPays = %v, retired rule = %v — "+
				"the off arm must delegate unchanged", workers, got, want)
		}
	}
}

// TestPartialSortTournamentPricesBothArms pins that the tournament actually
// builds the two plans the post-pass builds, with the shapes those plans have.
// A tournament that files one arm has not compared anything.
func TestPartialSortTournamentPricesBothArms(t *testing.T) {
	tbl := bigTable(t, "psp_shapes")
	child := seqScanOver(tbl)
	child.setPlanCost(PlanCost{StartupCost: 0, TotalCost: 50000, PlanRows: 1000000, PlanWidth: 8})
	srt := sortOver(child, testKeys())

	tour := createPartialSortPaths(srt, 4, true, DefaultPlannerSettings().costParams())
	if tour == nil {
		t.Fatal("createPartialSortPaths returned nil for a priced, worker-budgeted subtree")
	}
	if !tour.workerFiled && !tour.leaderFiled {
		t.Fatal("neither arm survived addPath — nothing was compared")
	}

	// worker-side: Gather Merge -> Sort -> seed
	if tour.workerSide.Kind != PathGatherMerge {
		t.Errorf("worker arm root is %v, want PathGatherMerge", tour.workerSide.Kind)
	}
	if len(tour.workerSide.Children) != 1 || tour.workerSide.Children[0].Kind != PathSort {
		t.Errorf("worker arm child is not a PathSort")
	}
	// The per-worker sort must be priced on per-worker rows, or the arm is not
	// the plan it claims to be.
	if got := tour.workerSide.Children[0].Rows; got != tour.perWorkerRows {
		t.Errorf("worker-side sort rows = %v, want per-worker %v", got, tour.perWorkerRows)
	}
	if tour.perWorkerRows >= tour.inputRows {
		t.Errorf("per-worker rows %v not below input rows %v at divisor %v",
			tour.perWorkerRows, tour.inputRows, tour.divisor)
	}

	// leader-side: Sort -> Gather -> seed
	if tour.leaderSide.Kind != PathSort {
		t.Errorf("leader arm root is %v, want PathSort", tour.leaderSide.Kind)
	}
	if len(tour.leaderSide.Children) != 1 || tour.leaderSide.Children[0].Kind != PathGather {
		t.Errorf("leader arm child is not a PathGather")
	}
	if got := tour.leaderSide.Rows; got != tour.inputRows {
		t.Errorf("leader-side sort rows = %v, want full %v", got, tour.inputRows)
	}

	// Both arms stand on the SAME seed object — the cancellation argument in
	// the file header is only valid if the input is literally shared.
	wSeed := tour.workerSide.Children[0].Children[0]
	lSeed := tour.leaderSide.Children[0].Children[0]
	if wSeed != lSeed {
		t.Error("the two arms do not share one input seed; the input price no " +
			"longer cancels and the comparison is not honest")
	}
}

// TestPartialSortVerdictIsCostDriven is the property that distinguishes this
// from the rule it replaces: the SAME node type must be able to produce both
// verdicts, decided by the numbers.
//
// The lever is the worker budget. `gather_merge` charges a k-way heap that
// grows with log(N+1) and a 5% IPC surcharge on every crossing row, while the
// per-worker sort saving is only the log factor — so the two arms genuinely
// trade off against each other as the divisor moves, and neither is pinned by
// construction.
func TestPartialSortVerdictIsCostDriven(t *testing.T) {
	restore := setPartialSortPathsModeForTest(partialSortPathsOn)
	defer restore()

	tbl := bigTable(t, "psp_cost")
	cp := DefaultPlannerSettings().costParams()

	seen := map[bool]int{}
	for _, rows := range []float64{10, 1000, 1000000, 100000000} {
		for _, workers := range []int{1, 2, 4, 8} {
			child := seqScanOver(tbl)
			child.setPlanCost(PlanCost{TotalCost: rows / 10, PlanRows: rows, PlanWidth: 8})
			srt := sortOver(child, testKeys())
			tour := createPartialSortPaths(srt, workers, true, cp)
			if tour == nil {
				t.Fatalf("rows=%v workers=%d: no tournament", rows, workers)
			}
			// Whatever the verdict, the winner must be the cheaper arm — the
			// tournament may not disagree with its own numbers.
			cheaperIsWorker := tour.workerSide.Cost.Total < tour.leaderSide.Cost.Total
			if got := tour.workerSideWins(); got != cheaperIsWorker {
				t.Errorf("rows=%v workers=%d: verdict %v but worker=%.2f leader=%.2f",
					rows, workers, got, tour.workerSide.Cost.Total, tour.leaderSide.Cost.Total)
			}
			seen[tour.workerSideWins()]++
		}
	}
	if seen[true] == 0 || seen[false] == 0 {
		t.Errorf("verdict never changed across the grid (%v) — a cost model that "+
			"always answers the same thing is a rule", seen)
	}
}

// TestPartialSortOnKeepsMergeKeysInvariant re-pins parallel_merge_test.go's
// safety property under the new authority: whenever the tournament accepts the
// Sort as the partial root, the built plan must carry a Gather MERGE, never a
// plain Gather. This is the wrong-results shape, so it is asserted against the
// real post-pass rather than against the tournament.
func TestPartialSortOnKeepsMergeKeysInvariant(t *testing.T) {
	restore := setPartialSortPathsModeForTest(partialSortPathsOn)
	defer restore()

	tbl := bigTable(t, "psp_invariant")
	root := sortOver(seqScanOver(tbl), testKeys())
	got := MaybeAddGather(root, parallelTestSettings())

	if gm := findGatherMerge(got); gm != nil {
		if _, ok := gm.Child.(*Sort); !ok {
			t.Fatalf("GatherMerge child is %T, want *Sort", gm.Child)
		}
		if len(gm.Keys) == 0 {
			t.Fatal("GatherMerge carries no keys — the leader would concatenate " +
				"the workers' runs instead of merging them")
		}
		return
	}
	// The tournament declined: then no per-worker Sort may exist below a plain
	// Gather. Walk down from the Gather and refuse a Sort under it.
	if g := findGatherNode(got); g != nil {
		if sortIsBelow(g.Child) {
			t.Fatal("a per-worker Sort sits under a plain Gather — the leader " +
				"would return unordered rows with no crash to point at")
		}
	}
}

// findGatherNode returns the plain Gather in a plan, or nil.
func findGatherNode(n Node) *Gather {
	if n == nil {
		return nil
	}
	if g, ok := n.(*Gather); ok {
		return g
	}
	for _, c := range parallelChildren(n) {
		if g := findGatherNode(c); g != nil {
			return g
		}
	}
	return nil
}

// sortIsBelow reports whether a *Sort appears anywhere below n.
func sortIsBelow(n Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*Sort); ok {
		return true
	}
	for _, c := range parallelChildren(n) {
		if sortIsBelow(c) {
			return true
		}
	}
	return false
}
