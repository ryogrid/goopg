package optimizer

// P7 of docs/design/parallel-query/ — Gather Merge placement.
//
// Before P7, a Sort terminated partial-ness: the Gather went BELOW it, so the
// leader did the whole sort while the workers only scanned. P7 lifts the Sort
// into the workers and merges the ordered streams in the leader, which is
// PG's
//
//	Gather Merge -> Sort -> Parallel Seq Scan
//
// The property that makes this safe, and what these tests pin down, is that a
// per-worker Sort is ONLY ever produced together with a GatherMerge above it.
// A plain Gather over per-worker Sorts compiles, runs, and returns unordered
// rows — a wrong-results bug with no crash to point at.

import (
	"testing"
)

func sortOver(child Node, keys []SortKey) *Sort {
	return &Sort{Child: child, Keys: keys}
}

func testKeys() []SortKey {
	return []SortKey{{Expr: &ColumnRef{Name: "a"}}}
}

// findGatherMerge returns the GatherMerge in a plan, or nil.
func findGatherMerge(n Node) *GatherMerge {
	if n == nil {
		return nil
	}
	if gm, ok := n.(*GatherMerge); ok {
		return gm
	}
	for _, c := range parallelChildren(n) {
		if gm := findGatherMerge(c); gm != nil {
			return gm
		}
	}
	return nil
}

// TestSortBecomesGatherMerge is the core placement test: the Sort must end up
// BELOW the parallel boundary, not above it.
func TestSortBecomesGatherMerge(t *testing.T) {
	tbl := bigTable(t, "gm_t")
	root := sortOver(seqScanOver(tbl), testKeys())

	got := MaybeAddGather(root, parallelTestSettings())

	gm, ok := got.(*GatherMerge)
	if !ok {
		t.Fatalf("root is %T, want *GatherMerge — the Sort should have moved "+
			"into the workers, leaving the leader only the merge", got)
	}
	if _, ok := gm.Child.(*Sort); !ok {
		t.Fatalf("GatherMerge child is %T, want *Sort", gm.Child)
	}
	if len(gm.Keys) != len(testKeys()) {
		t.Errorf("GatherMerge carries %d keys, want %d — without the keys the "+
			"leader cannot merge", len(gm.Keys), len(testKeys()))
	}
	if gm.WorkersPlanned != 2 {
		t.Errorf("WorkersPlanned = %d, want 2", gm.WorkersPlanned)
	}
}

// TestGatherMergeUnderLimit covers the shape that motivates the feature:
// ORDER BY ... LIMIT. The Limit stays serial on the leader and stops pulling
// early, which is exactly what makes the merge cheaper than a leader-side sort.
func TestGatherMergeUnderLimit(t *testing.T) {
	tbl := bigTable(t, "gm_lim")
	root := &Limit{Child: sortOver(seqScanOver(tbl), testKeys())}

	got := MaybeAddGather(root, parallelTestSettings())

	lim, ok := got.(*Limit)
	if !ok {
		t.Fatalf("root is %T, want *Limit to stay on top", got)
	}
	if _, ok := lim.Child.(*GatherMerge); !ok {
		t.Fatalf("Limit child is %T, want *GatherMerge", lim.Child)
	}
}

// TestNoPlainGatherOverWorkerSort is the wrong-results guard.
//
// It asserts the invariant directly rather than a particular plan shape: in
// ANY plan the post-pass produces, a Sort below a parallel boundary must have
// a GatherMerge as that boundary, never a plain Gather.
func TestNoPlainGatherOverWorkerSort(t *testing.T) {
	tbl := bigTable(t, "gm_inv")

	for name, root := range map[string]Node{
		"sort":               sortOver(seqScanOver(tbl), testKeys()),
		"limit-sort":         &Limit{Child: sortOver(seqScanOver(tbl), testKeys())},
		"project-sort":       &Project{Child: sortOver(seqScanOver(tbl), testKeys())},
		"sort-over-filter":   sortOver(&Filter{Child: seqScanOver(tbl)}, testKeys()),
		"agg-over-sort":      &Aggregate{Child: sortOver(seqScanOver(tbl), testKeys())},
		"scan-only-no-sort":  seqScanOver(tbl),
		"filter-only-nosort": &Filter{Child: seqScanOver(tbl)},
	} {
		t.Run(name, func(t *testing.T) {
			got := MaybeAddGather(root, parallelTestSettings())
			if g := findPlainGatherOverSort(got); g != nil {
				t.Fatalf("plain Gather sits above a Sort: each worker would sort "+
					"its own partition and the leader would concatenate them, "+
					"returning unordered rows")
			}
		})
	}
}

// findPlainGatherOverSort returns a Gather that has a Sort anywhere beneath it.
func findPlainGatherOverSort(n Node) *Gather {
	if n == nil {
		return nil
	}
	if g, ok := n.(*Gather); ok && subtreeHasSort(g.Child) {
		return g
	}
	for _, c := range parallelChildren(n) {
		if g := findPlainGatherOverSort(c); g != nil {
			return g
		}
	}
	return nil
}

func subtreeHasSort(n Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*Sort); ok {
		return true
	}
	for _, c := range parallelChildren(n) {
		if subtreeHasSort(c) {
			return true
		}
	}
	return false
}

// TestGatherMergeIsNonMutating repeats the P6 property for the new path. The
// plan comes from a cross-session cache, so lifting the Sort must not edit the
// cached Sort node — another session may be executing it right now.
func TestGatherMergeIsNonMutating(t *testing.T) {
	tbl := bigTable(t, "gm_nm")
	scan := seqScanOver(tbl)
	srt := sortOver(scan, testKeys())

	got := MaybeAddGather(srt, parallelTestSettings())

	if got == Node(srt) {
		t.Fatal("post-pass returned the input unchanged")
	}
	if srt.Child != Node(scan) {
		t.Error("the cached Sort node was edited in place")
	}
	gm := findGatherMerge(got)
	if gm == nil {
		t.Fatal("no GatherMerge")
	}
	// The Sort itself is SHARED, not copied — it is below the boundary and
	// each worker builds its own operator from it, so sharing the plan node is
	// correct and copying would be waste.
	if gm.Child != Node(srt) {
		t.Errorf("GatherMerge child is %T, want the original *Sort shared", gm.Child)
	}
}

// TestSortWithoutEligibleScanStillTerminates: the Sort special-case must not
// swallow shapes it cannot parallelise. A Sort over an Aggregate has no
// partial-capable child, so it must fall back to the pre-P7 behaviour.
func TestSortWithoutEligibleScanStillTerminates(t *testing.T) {
	tbl := bigTable(t, "gm_agg")
	root := sortOver(&Aggregate{Child: seqScanOver(tbl)}, testKeys())

	got := MaybeAddGather(root, parallelTestSettings())

	if gm := findGatherMerge(got); gm != nil {
		t.Fatalf("built a GatherMerge over %T — the Aggregate is not "+
			"decomposable by the planner yet, so its input cannot be partial",
			gm.Child)
	}
}

// TestGatherMergeRespectsSizeGate: the merge path must use the same size rule
// as plain Gather, computed from the SCAN below the Sort rather than from the
// Sort node (which has no table).
func TestGatherMergeRespectsSizeGate(t *testing.T) {
	tbl := bigTable(t, "gm_small")
	root := sortOver(seqScanOver(tbl), testKeys())

	got := MaybeAddGather(root, parallelTestSettingsBlocks(8)) // below threshold

	if gm := findGatherMerge(got); gm != nil {
		t.Fatal("GatherMerge inserted for a relation below " +
			"min_parallel_table_scan_size; the worker count must be derived " +
			"from the scan under the Sort, not defaulted")
	}
}

// TestEnableGatherMergeOffFallsBackToGatherBelowSort pins B-17c.
//
// `enable_gathermerge` was a registered GUC with no consumer. PG's
// cost_gather_merge carries the flag (costsize.c:485); with no GatherMerge
// path in the search (P5-04 open) there is no count to carry, so the
// post-pass gates the P7 arm instead. Off must reproduce the pre-P7 shape —
// the Sort stays whole above a plain Gather — and must never produce the
// wrong-results shape (plain Gather over per-worker Sorts).
func TestEnableGatherMergeOffFallsBackToGatherBelowSort(t *testing.T) {
	tbl := bigTable(t, "gm_guc")
	root := sortOver(seqScanOver(tbl), testKeys())

	off := parallelTestSettings()
	off.DisableGatherMerge = true
	got := MaybeAddGather(root, off)

	if gm := findGatherMerge(got); gm != nil {
		t.Fatalf("a GatherMerge survived enable_gathermerge=off: %+v", gm)
	}
	if g := findPlainGatherOverSort(got); g != nil {
		t.Fatalf("plain Gather sits above a Sort — the fallback must keep " +
			"exactly one leader-side sort, not per-worker sorts")
	}
	srt, ok := got.(*Sort)
	if !ok {
		t.Fatalf("root is %T, want *Sort above the Gather (the pre-P7 shape)", got)
	}
	if _, ok := srt.Child.(*Gather); !ok {
		t.Fatalf("Sort child is %T, want *Gather below it", srt.Child)
	}
	// The control arm is unchanged: on still lifts the Sort into the workers.
	if _, ok := MaybeAddGather(root, parallelTestSettings()).(*GatherMerge); !ok {
		t.Fatal("enable_gathermerge=on stopped producing GatherMerge")
	}
}

// TestKillSwitchSuppressesGatherMerge — the kill switch must cover both
// variants, or disabling parallelism would leave ordered queries parallel.
func TestKillSwitchSuppressesGatherMerge(t *testing.T) {
	tbl := bigTable(t, "gm_kill")
	root := sortOver(seqScanOver(tbl), testKeys())

	prev := ParallelEnabled()
	SetParallelEnabled(false)
	defer SetParallelEnabled(prev)

	if gm := findGatherMerge(MaybeAddGather(root, parallelTestSettings())); gm != nil {
		t.Fatal("kill switch did not suppress GatherMerge")
	}
}
