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

// TestElectOrderedDistinctOffersALoneCandidate: M0145-0006 slice 4 lowered
// the candidate minimum from 2 to 1, the same change M0144-0011a-2 made to
// the grouping twin and for the same reason — `create_ordered_paths` has no
// minimum, it iterates the whole input pathlist. A lone candidate whose
// emission order already delivers the ORDER BY must therefore be elected
// on the ORDERED rel, not declined into the legacy fallback. The lone
// candidate here is hashed, which carries no order since M0141-S2b-4d (PG's
// AGG_HASHED path has no pathkeys), so the ORDERED rel stacks a Sort on it.
func TestElectOrderedDistinctOffersALoneCandidate(t *testing.T) {
	u := newUpperRels()
	in := upperOrderedInput(1000)
	spec := distinctTestSpec(in)
	rel := fetchUpperRel(u, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(rel, spec)
	addPath(rel, &Path{Kind: PathDistinct, Distinct: spec, Rel: rel, Rows: 5,
		Cost: Cost{Total: 10}, Children: []*Path{newPrebuiltPath(rel, in)}}, "test")
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: in.Output()[0].Type}}}

	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("a lone candidate declined; PG offers it like one of many")
	}
	srt, isSort := got.(*Sort)
	if !isSort {
		t.Fatalf("winner is %T; want a Sort over the hashed *Distinct (hashed output is unordered)", got)
	}
	if _, isDistinct := srt.Child.(*Distinct); !isDistinct {
		t.Fatalf("Sort child is %T; want the lone hashed *Distinct", srt.Child)
	}

	// An EMPTY rel still declines: there is nothing to offer.
	empty := newUpperRels()
	emptyRel := fetchUpperRel(empty, UpperDistinct, 0, 0)
	sizeDistinctRelFromNode(emptyRel, spec)
	if got, ok := electOrderedDistinct(empty, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1); ok || got != nil {
		t.Fatalf("empty rel elected (ok=%v); want the decline", ok)
	}
}

// TestElectOrderedDistinctNoSortOnAscendingPrefixOrder: ORDER BY an
// ascending, nulls-last prefix of the output columns is the order the unique
// candidate's producer Sort already delivers, so the unique candidate
// competes as-is and the hashed one under a Sort (PG's create_ordered_paths
// over both). Whichever wins, no Sort may be stacked over the Unique.
func TestElectOrderedDistinctNoSortOnAscendingPrefixOrder(t *testing.T) {
	u, spec, _ := distinctFixture()
	kType := spec.Output()[0].Type
	keys := []SortKey{{Expr: &ColumnRef{Index: 0, Name: "k", Type: kType}}}
	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("ascending-prefix ORDER BY declined; want no-sort election")
	}
	switch w := got.(type) {
	case *DistinctOn:
	case *Sort:
		if _, ok := w.Child.(*Distinct); !ok {
			t.Fatalf("Sort over %T; a Sort may only sit over the hashed *Distinct", w.Child)
		}
	default:
		t.Fatalf("winner is %T; want a bare *DistinctOn or a Sort over the hashed *Distinct", got)
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
	stalePos := createOrderedPaths(u, preDistinct, keys, 0, DefaultPlannerSettings().costParams(), 0, -1, nil)
	if _, ok := stalePos.(*Sort); !ok {
		t.Fatalf("setup: pre-distinct createOrderedPaths returned %T, want *Sort (unsorted seed)", stalePos)
	}

	got, ok := electOrderedDistinct(u, spec, keys, 0, DefaultPlannerSettings().costParams(), 0, -1)
	if !ok || got == nil {
		t.Fatal("declined with a stale pre-distinct ORDERED entry present; want election to still succeed")
	}
	switch w := got.(type) {
	case *Distinct, *DistinctOn:
	case *Sort:
		// A Sort over the hashed candidate is legitimate (hashed output is
		// unordered); a Sort over anything else is the stale pre-distinct
		// candidate leaking back in.
		if _, ok := w.Child.(*Distinct); !ok {
			t.Fatalf("winner is a Sort over %T (the stale pre-distinct candidate leaked back in); want DISTINCT-rooted", w.Child)
		}
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
// Pathkeys are not one output-column reference per output column (the shape
// of the producer's distinct-clause Sort), plus the hashed candidate's
// translation: unordered, as PG's AGG_HASHED path (M0141-S2b-4d).
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
	// A Unique candidate whose pathkeys cover only part of the output, or
	// name one column twice, is not the producer's Sort: decline.
	short := &Path{Kind: PathDistinct, Distinct: spec, Unique: true,
		Pathkeys: []PathKey{{Expr: &ColumnRef{Index: 0, Name: "k"}, SortAsc: false}}}
	if got := distinctEmissionPathkeys(spec, short); got != nil {
		t.Errorf("partial unique pathkeys: translated %d keys, want nil", len(got))
	}
	dup := &Path{Kind: PathDistinct, Distinct: spec, Unique: true, Pathkeys: []PathKey{
		{Expr: &ColumnRef{Index: 0}}, {Expr: &ColumnRef{Index: 0}}, {Expr: &ColumnRef{Index: 2}}}}
	if got := distinctEmissionPathkeys(spec, dup); got != nil {
		t.Errorf("duplicate-column unique pathkeys: translated %d keys, want nil", len(got))
	}
	// A distinct-clause order led by a DESC ORDER BY key (column 1 first)
	// is trusted as-is.
	clause := &Path{Kind: PathDistinct, Distinct: spec, Unique: true, Pathkeys: []PathKey{
		{Expr: &ColumnRef{Index: 1}}, {Expr: &ColumnRef{Index: 0}, SortAsc: true}, {Expr: &ColumnRef{Index: 2}, SortAsc: true}}}
	if got := distinctEmissionPathkeys(spec, clause); len(got) != 3 {
		t.Errorf("distinct-clause unique pathkeys: translated %d keys, want 3", len(got))
	}
	// Hashed: translated (non-nil) but unordered.
	hashed := &Path{Kind: PathDistinct, Distinct: spec, Unique: false}
	got := distinctEmissionPathkeys(spec, hashed)
	if got == nil || len(got) != 0 {
		t.Fatalf("hashed translation = %v (nil=%v); want an empty, non-nil order", got, got == nil)
	}
}
