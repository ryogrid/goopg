package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// withIncrementalSortMode flips the package knob for one test and restores
// it, mirroring uniqueify_hash_builders_test.go's gatherPathsMode convention.
func withIncrementalSortMode(t *testing.T, m incrementalSortMode) {
	t.Helper()
	prev := incrementalSortPathsMode
	incrementalSortPathsMode = m
	t.Cleanup(func() { incrementalSortPathsMode = prev })
}

// incrementalSortFixture builds the same shape
// TestCreateOrderedPathsThreadsSearchCandidatesOntoOrderedRel /
// TestCreateOrderedPathsValidatesSearchCandidatePathkeys use — a searched-tree
// input over upperOrderedKeys() ("v" DESC, "k" NULLS FIRST) with one search
// candidate whose own ordering already satisfies a genuine partial prefix
// ("v" alone, matching upperOrderedKeys()[0]) and one with no shared prefix at
// all ("k" alone does not equal upperOrderedKeys()[0], "v").
func incrementalSortFixture() (u *upperRels, in *searchedPricedNode, cand1, cand2 *Path) {
	u = newUpperRels()

	vKey := PathKey{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, SortAsc: false}
	kKeyOnly := PathKey{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, SortAsc: true}

	searchRel := &RelOptInfo{}
	// cand1's leading key matches keys[0] ("v" DESC) exactly: nCommon = 1 of
	// 2, a genuine partial prefix — the case this arm exists for.
	cand1 = &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 40}, Pathkeys: []PathKey{vKey}}
	// cand2's only key does not match keys[0] at all: nCommon = 0, arm 2's
	// territory (a full Sort over the seed), not this arm's.
	cand2 = &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 50}, Pathkeys: []PathKey{kKeyOnly}}
	searchRel.Pathlist = []*Path{cand1, cand2}

	in = &searchedPricedNode{pricedNode: *upperOrderedInput(500)}
	in.markFromJoinSearch()
	in.setSearchRel(searchRel)
	return u, in, cand1, cand2
}

// TestAddIncrementalSortPathsOffIsInert pins the escape hatch: with
// GOOPG_INCREMENTAL_SORT=off, createOrderedPaths' ordered.Pathlist is exactly
// what it was before the third arm existed (seed's full Sort alone) even
// though a genuinely partial-prefix-matching search candidate is present.
// Default ON since M0146-0006.
func TestAddIncrementalSortPathsOffIsInert(t *testing.T) {
	if incrementalSortPathsMode != incrementalSortOn {
		t.Fatalf("incrementalSortPathsMode = %v, want the package default on (M0146-0006)", incrementalSortPathsMode)
	}
	withIncrementalSortMode(t, incrementalSortOff)
	cp := defaultCostParams()
	u, in, _, _ := incrementalSortFixture()

	createOrderedPaths(u, in, upperOrderedKeys(), 0, cp, 0, -1, nil)

	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if len(ordered.Pathlist) != 1 {
		t.Fatalf("ordered.Pathlist = %d entries, want 1 (the seed's full Sort only) — the third arm must stay off under the escape hatch", len(ordered.Pathlist))
	}
	if ordered.Pathlist[0].Kind != PathSort {
		t.Fatalf("ordered.Pathlist[0].Kind = %d, want PathSort", ordered.Pathlist[0].Kind)
	}
}

// incrementalSortOrderedFixture builds an ORDERED rel + seed the way
// TestAddOrderedPathsOffersExactlyOneProducerPerInput does, with
// SearchCandidates/SearchCandidateKeys set directly (S2b-2a/2b's own
// derivation off a searched-tree input is covered by their own tests) —
// isolating this arm's `addOrderedPaths` call from `createOrderedPaths`'s
// trailing `createPlanNode(best)` step. That step is deliberately NOT safe to
// run when the flag is on and a synthetic candidate is cheap enough to win
// (PathIncrementalSort has no createPlanNode arm yet, by design — see
// incrementalsortpaths.go's header), so these tests inspect the offered
// Pathlist directly rather than the materialized winner.
func incrementalSortOrderedFixture() (ordered *RelOptInfo, seedNode *pricedNode, seed *Path) {
	ordered = fetchUpperRel(newUpperRels(), UpperOrdered, 0, 0)
	seedNode = upperOrderedInput(500)
	sizeUpperRelFromNode(ordered, seedNode)
	seed = newPrebuiltPath(ordered, seedNode)
	// newPrebuiltPath leaves Cost zero (the C0 bridge never needed one);
	// createOrderedPaths always stamps the child's own cost before calling
	// addOrderedPaths (upperordered.go), so a fixture that skips this step
	// makes the seed's Sort look artificially free and wrongly dominates
	// every real candidate on cost alone.
	pc := legacyDisplayCostOf(seedNode)
	seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	return ordered, seedNode, seed
}

// TestAddIncrementalSortPathsDeclinesACandidateTheBoundaryCannotRebuild pins
// M0146-0006's safety rule for the third arm: a partially presorted search
// candidate is wrapped in an Incremental Sort only after it is rebuilt
// through the searched boundary (`searchedCandidateInput`), the same replay
// the presorted arm (M0146-0027) applies. A raw searched path lowers in the
// search's inner coordinate order, so offering it would sort and emit the
// wrong columns. Here the seed is not a searched root, so the rebuild
// declines and no Incremental Sort may be offered — only the seed's own
// full Sort. The positive case runs end to end in the executor's
// TestGroupAggIncrementalSortOverPresortedInput / the fire set.
func TestAddIncrementalSortPathsDeclinesACandidateTheBoundaryCannotRebuild(t *testing.T) {
	withIncrementalSortMode(t, incrementalSortOn)
	cp := defaultCostParams()
	sortPathkeys := pathkeysForSortKeys(upperOrderedKeys())
	ordered, _, seed := incrementalSortOrderedFixture()

	vKey := PathKey{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, SortAsc: false}
	cand1 := &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 40}, Pathkeys: []PathKey{vKey}}
	ordered.SearchCandidates = []*Path{cand1}
	ordered.SearchCandidateKeys = [][]PathKey{cand1.Pathkeys}

	addOrderedPaths(ordered, seed, sortPathkeys, cp, -1)

	for _, p := range ordered.Pathlist {
		if p.Kind == PathIncrementalSort {
			t.Fatalf("an un-rebuildable candidate was wrapped in an Incremental Sort: %+v", p)
		}
	}
	if len(ordered.Pathlist) != 1 || ordered.Pathlist[0].Kind != PathSort {
		t.Fatalf("ordered.Pathlist = %+v, want the seed's full Sort alone", ordered.Pathlist)
	}
}

// TestAddIncrementalSortPathsSkipsAFullyContainedCandidate: a candidate whose
// ordering already fully satisfies sortPathkeys is arm 1's case for the SEED
// only (a same-rel dominance question this slice deliberately leaves to
// M0141-S2b-2c's still-open "actual tournament" scope) — this arm must not
// double-offer it as an Incremental Sort of its own child.
func TestAddIncrementalSortPathsSkipsAFullyContainedCandidate(t *testing.T) {
	withIncrementalSortMode(t, incrementalSortOn)
	cp := defaultCostParams()
	sortPathkeys := pathkeysForSortKeys(upperOrderedKeys())
	ordered, _, seed := incrementalSortOrderedFixture()

	full := []PathKey{
		{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, SortAsc: false},
		{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, SortAsc: true, NullsFirst: true},
	}
	full1 := &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Total: 5}, Pathkeys: full}
	ordered.SearchCandidates = []*Path{full1}
	ordered.SearchCandidateKeys = [][]PathKey{full1.Pathkeys}

	addOrderedPaths(ordered, seed, sortPathkeys, cp, -1)

	for _, p := range ordered.Pathlist {
		if p.Kind == PathIncrementalSort {
			t.Fatalf("a fully-contained candidate must not be offered as an Incremental Sort: %+v", p)
		}
	}
}

// TestAddIncrementalSortPathsSkipsAZeroPrefixCandidate: a candidate sharing
// NO leading key with sortPathkeys gets no Incremental Sort offer — a full
// Sort (arm 2, over the seed) already prices that case, and stacking an
// Incremental Sort with nCommon=0 would just be `costSortRun` under a
// different name for a candidate this arm was never meant to touch.
func TestAddIncrementalSortPathsSkipsAZeroPrefixCandidate(t *testing.T) {
	withIncrementalSortMode(t, incrementalSortOn)
	cp := defaultCostParams()
	sortPathkeys := pathkeysForSortKeys(upperOrderedKeys())
	ordered, _, seed := incrementalSortOrderedFixture()

	kKeyOnly := PathKey{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, SortAsc: true}
	cand := &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Total: 5}, Pathkeys: []PathKey{kKeyOnly}}
	ordered.SearchCandidates = []*Path{cand}
	ordered.SearchCandidateKeys = [][]PathKey{cand.Pathkeys}

	addOrderedPaths(ordered, seed, sortPathkeys, cp, -1)

	for _, p := range ordered.Pathlist {
		if p.Kind == PathIncrementalSort {
			t.Fatalf("a zero-shared-prefix candidate must not be offered as an Incremental Sort: %+v", p)
		}
	}
}

// TestIncrementalSortModeFromEnv pins the env-string contract: default ON
// since M0146-0006 (PG's enable_incremental_sort defaults on), with `off`
// as the escape hatch.
func TestIncrementalSortModeFromEnv(t *testing.T) {
	cases := map[string]incrementalSortMode{
		"":      incrementalSortOn,
		"off":   incrementalSortOff,
		"OFF":   incrementalSortOff,
		"0":     incrementalSortOff,
		"false": incrementalSortOff,
		"bogus": incrementalSortOn,
		"on":    incrementalSortOn,
		"ON":    incrementalSortOn,
		"1":     incrementalSortOn,
		"true":  incrementalSortOn,
		" on ":  incrementalSortOn,
	}
	for in, want := range cases {
		if got := incrementalSortModeFromEnv(in); got != want {
			t.Errorf("incrementalSortModeFromEnv(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestAddOrderedPathsIncrementallySortsAPresortedSeed pins M0146-0005bp:
// create_ordered_paths gives the cheapest input an Incremental Sort — and no
// full Sort — when its pathkeys deliver a leading prefix of the ORDER BY and
// enable_incremental_sort is on (planner.c), and a full Sort when the GUC is
// off. Independent of the GOOPG_INCREMENTAL_SORT knob, which gates only the
// other-candidates arm.
func TestAddOrderedPathsIncrementallySortsAPresortedSeed(t *testing.T) {
	sortPathkeys := pathkeysForSortKeys(upperOrderedKeys())
	for _, on := range []bool{true, false} {
		ordered, _, seed := incrementalSortOrderedFixture()
		seed.Pathkeys = sortPathkeys[:1]
		cp := defaultCostParams()
		cp.enableIncrementalSort = on
		addOrderedPaths(ordered, seed, sortPathkeys, cp, -1)
		var kinds []PathKind
		for _, p := range ordered.Pathlist {
			kinds = append(kinds, p.Kind)
		}
		want := PathSort
		if on {
			want = PathIncrementalSort
		}
		if len(ordered.Pathlist) != 1 || ordered.Pathlist[0].Kind != want {
			t.Fatalf("enable_incremental_sort=%v: ordered.Pathlist kinds = %v, want exactly [%v]", on, kinds, want)
		}
		if on && ordered.Pathlist[0].PresortedCount != 1 {
			t.Fatalf("PresortedCount = %d, want 1", ordered.Pathlist[0].PresortedCount)
		}
	}
}
