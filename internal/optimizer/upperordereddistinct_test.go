package optimizer

// M0141-S2b-1 — electOrderedDistinct / distinctEmissionPathkeys.

import "testing"

// distinctFixture builds a real DISTINCT rel populated with the two
// candidates `addDistinctPaths` always builds (hashed, unique-over-sorted)
// over the shared 3-column (k int4, v text, w numeric) fixture schema.
func distinctFixture() (*upperRels, *Distinct, *RelOptInfo) {
	cp := defaultCostParams()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	u := newUpperRels()
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	seed := newPrebuiltPath(rel, in)
	seed.Rows = 1000
	seed.Cost = Cost{Total: 100}
	addDistinctPaths(rel, seed, spec, in, cp, DefaultPlannerSettings())
	setCheapest(rel)
	return u, spec, rel
}

// TestElectOrderedDistinctDeclinesFewerThanTwoCandidates: a DISTINCT rel
// with a single PathDistinct candidate (or none) must decline — mirrors
// electOrderedGrouping's own cands<2 gate.
func TestElectOrderedDistinctDeclinesFewerThanTwoCandidates(t *testing.T) {
	u := newUpperRels()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	addPath(rel, &Path{Kind: PathDistinct, Distinct: spec, Rel: rel, Rows: 5,
		Cost: Cost{Total: 10}, Children: []*Path{newPrebuiltPath(rel, in)}}, "test")
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: in.Output()[0].Type}}}
	if got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1); ok || got != nil {
		t.Fatalf("single-candidate rel elected (ok=%v); want decline", ok)
	}
}

// TestElectOrderedDistinctNoSortOnAscendingPrefixOrder: ORDER BY an
// ascending, nulls-last prefix of the output columns is exactly the order
// BOTH addDistinctPaths candidates already deliver (distinctOp's forced
// re-sort for hashed, distinctOnOp's streamed producer-Sort order for
// unique) — the loop must elect a bare *Distinct/*DistinctOn winner, never
// stacking a redundant Sort.
func TestElectOrderedDistinctNoSortOnAscendingPrefixOrder(t *testing.T) {
	u, spec, _ := distinctFixture()
	kType := spec.Output()[0].Type
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: kType}}}
	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("ascending-prefix ORDER BY declined; want no-sort election")
	}
	switch got.(type) {
	case *Distinct, *DistinctOn:
	default:
		t.Fatalf("winner is %T; want a bare *Distinct/*DistinctOn (no Sort)", got)
	}
}

// TestElectOrderedDistinctIgnoresStalePreDistinctOrderedEntry: the planner.go
// call site runs AFTER the generic ORDER BY block has already called
// `createOrderedPaths` once for the SAME keys over the PRE-distinct child —
// planner.go's own comment: "Applied after sorting so ORDER BY is
// respected" — which populates `u`'s shared `(UpperOrdered, 0)` rel before
// `electOrderedDistinct` ever runs. A caught-live bug: an earlier version of
// this function re-fetched THAT SAME shared rel instead of a private one,
// so `setCheapest` could pick the stale pre-distinct Sort right back out
// from under the election (its child is not `distinctNode` at all, tripping
// the shape gate below). Reproduce that pre-population here and assert the
// election still elects a real DISTINCT-rooted winner.
func TestElectOrderedDistinctIgnoresStalePreDistinctOrderedEntry(t *testing.T) {
	u, spec, _ := distinctFixture()
	kType := spec.Output()[0].Type
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: kType}}}

	// Simulate the generic ORDER BY block's own earlier createOrderedPaths
	// call over the pre-distinct child, landing a Sort candidate on the
	// SAME shared `(UpperOrdered, 0)` rel `u` owns. Priced far cheaper than
	// either real DISTINCT candidate (which both build on top of the
	// fixture's own Total:100 seed) so a cost-tournament bug that lets this
	// stale entry compete cannot go unnoticed by accident.
	preDistinct := upperOrderedInput(1000)
	preDistinct.setPlanCost(PlanCost{StartupCost: 1, TotalCost: 1, PlanRows: 1000, PlanWidth: 1})
	stalePos := createOrderedPaths(u, preDistinct, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if _, ok := stalePos.(*Sort); !ok {
		t.Fatalf("setup: pre-distinct createOrderedPaths returned %T, want *Sort (unsorted seed)", stalePos)
	}

	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("declined with a stale pre-distinct ORDERED entry present; want election to still succeed")
	}
	switch got.(type) {
	case *Distinct, *DistinctOn:
	case *Sort:
		t.Fatalf("winner is a bare *Sort (the stale pre-distinct candidate leaked back in); want *Distinct/*DistinctOn")
	default:
		t.Fatalf("winner is %T; want *Distinct/*DistinctOn", got)
	}
}

// TestElectOrderedDistinctAddsSortOnDescendingOrder: a DESC ORDER BY can
// never be satisfied by either candidate's ascending emission order, so the
// loop must stack a Sort over whichever candidate the ORDERED rel's own
// cost tournament prefers.
func TestElectOrderedDistinctAddsSortOnDescendingOrder(t *testing.T) {
	u, spec, _ := distinctFixture()
	kType := spec.Output()[0].Type
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: kType}, Desc: true}}
	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("descending ORDER BY declined; want a Sort-over-candidate election")
	}
	srt, isSort := got.(*Sort)
	if !isSort {
		t.Fatalf("winner is %T; want *Sort (no candidate can satisfy DESC as-is)", got)
	}
	switch srt.Child.(type) {
	case *Distinct, *DistinctOn:
	default:
		t.Fatalf("Sort child is %T; want *Distinct/*DistinctOn", srt.Child)
	}
}

// TestDistinctEmissionPathkeysGates pins distinctEmissionPathkeys' own
// negatives directly: nil inputs, wrong Kind, and a Unique candidate whose
// Pathkeys are not the full-column ascending/nulls-last positional shape
// addDistinctPaths always builds (defence against a future addDistinctPaths
// change reusing a differently-ordered pre-sorted child).
func TestDistinctEmissionPathkeysGates(t *testing.T) {
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	if got := distinctEmissionPathkeys(nil, &Path{Kind: PathDistinct, Distinct: spec}); got != nil {
		t.Errorf("nil distinctNode: translated %d keys, want nil", len(got))
	}
	if got := distinctEmissionPathkeys(spec, nil); got != nil {
		t.Errorf("nil candidate: translated %d keys, want nil", len(got))
	}
	if got := distinctEmissionPathkeys(spec, &Path{Kind: PathSort}); got != nil {
		t.Errorf("non-distinct kind: translated %d keys, want nil", len(got))
	}
	// Unique candidate with a DESC leading key (not the all-ascending shape
	// the real producer Sort always builds) must decline, not be trusted.
	badUnique := &Path{Kind: PathDistinct, Distinct: spec, Unique: true,
		Pathkeys: []PathKey{{Expr: &ColumnRef{Index: 0, Name: "k"}, SortAsc: false}}}
	if got := distinctEmissionPathkeys(spec, badUnique); got != nil {
		t.Errorf("descending unique pathkeys: translated %d keys, want nil", len(got))
	}
	// Hashed candidate (no Pathkeys stamp at all) always translates: the
	// executor contract (distinctOp) is fixed, not read off the Path.
	hashed := &Path{Kind: PathDistinct, Distinct: spec, Unique: false}
	got := distinctEmissionPathkeys(spec, hashed)
	if len(got) != len(spec.Output()) {
		t.Fatalf("hashed translation returned %d keys, want %d (one per output column)", len(got), len(spec.Output()))
	}
	for i, pk := range got {
		if !pk.SortAsc || pk.NullsFirst {
			t.Fatalf("hashed key[%d] = %+v, want ascending/nulls-last", i, pk)
		}
	}
}
