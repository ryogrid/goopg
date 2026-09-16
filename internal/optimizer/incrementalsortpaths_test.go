package optimizer

import (
	"strings"
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

// TestAddIncrementalSortPathsOffByDefaultIsInert pins the default-off gate:
// with GOOPG_INCREMENTAL_SORT unset, createOrderedPaths' ordered.Pathlist is
// exactly what it was before this file existed (seed's full Sort alone) even
// though a genuinely partial-prefix-matching search candidate is present. Run
// end-to-end through createOrderedPaths (including its createPlanNode call)
// because the flag being off means PathIncrementalSort can never be
// constructed, so there is nothing unsafe about letting the winner
// materialize here.
func TestAddIncrementalSortPathsOffByDefaultIsInert(t *testing.T) {
	if incrementalSortPathsMode != incrementalSortOff {
		t.Fatalf("incrementalSortPathsMode = %v, want the package default off", incrementalSortPathsMode)
	}
	cp := defaultCostParams()
	u, in, _, _ := incrementalSortFixture()

	createOrderedPaths(u, in, upperOrderedKeys(), 0, cp, 0, -1)

	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	if len(ordered.Pathlist) != 1 {
		t.Fatalf("ordered.Pathlist = %d entries, want 1 (the seed's full Sort only) — the third arm must stay off by default", len(ordered.Pathlist))
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

// TestAddIncrementalSortPathsOffersAGenuinePartialPrefixCandidate is this
// arm's positive gate: with the flag on, cand1 (partial-prefix match) gets an
// Incremental Sort candidate offered to the ORDERED tournament, priced by
// costIncrementalSort over cand1's own cost — while cand2 (zero shared
// prefix) is skipped, since a zero-prefix candidate has nothing this arm
// prices more cheaply than arm 2's Sort already does.
func TestAddIncrementalSortPathsOffersAGenuinePartialPrefixCandidate(t *testing.T) {
	withIncrementalSortMode(t, incrementalSortOn)
	cp := defaultCostParams()
	sortPathkeys := pathkeysForSortKeys(upperOrderedKeys())
	ordered, seedNode, seed := incrementalSortOrderedFixture()

	vKey := PathKey{Expr: &ColumnRef{Index: 1, Name: "v", Type: catalog.Type{Name: "text"}}, SortAsc: false}
	kKeyOnly := PathKey{Expr: &ColumnRef{Index: 0, Name: "k", Type: catalog.Type{Name: "int4"}}, SortAsc: true}
	cand1 := &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 40}, Pathkeys: []PathKey{vKey}}
	cand2 := &Path{Kind: PathSeqScan, Rows: 500, Cost: Cost{Startup: 1, Total: 50}, Pathkeys: []PathKey{kKeyOnly}}
	ordered.SearchCandidates = []*Path{cand1, cand2}
	ordered.SearchCandidateKeys = [][]PathKey{cand1.Pathkeys, cand2.Pathkeys}

	lines := captureTrace(t, func() {
		addOrderedPaths(ordered, seed, sortPathkeys, cp, -1)
	})
	lines = dppathLines(lines)
	var incLines []string
	for _, l := range lines {
		if strings.Contains(l, "producer="+upperOrderedIncrementalSortProducer+" ") {
			incLines = append(incLines, l)
		}
	}
	if len(incLines) != 1 {
		t.Fatalf("incremental-sort trace lines = %d, want exactly 1 (one for cand1, none for cand2): %v", len(incLines), lines)
	}

	var got *Path
	for _, p := range ordered.Pathlist {
		if p.Kind == PathIncrementalSort {
			got = p
		}
	}
	if got == nil {
		t.Fatalf("ordered.Pathlist has no PathIncrementalSort entry: %+v", ordered.Pathlist)
	}
	if len(got.Children) != 1 || got.Children[0] != cand1 {
		t.Fatalf("PathIncrementalSort.Children = %v, want [cand1] by identity", got.Children)
	}
	if got.Rows != cand1.Rows {
		t.Fatalf("PathIncrementalSort.Rows = %v, want cand1.Rows %v (a Sort projects nothing)", got.Rows, cand1.Rows)
	}
	if len(got.Pathkeys) != 2 {
		t.Fatalf("PathIncrementalSort.Pathkeys = %d entries, want the full 2-key requirement", len(got.Pathkeys))
	}
	want := costIncrementalSort(cp, cand1.Cost, cand1.Rows,
		float64(estimateNumGroups([]Expr{sortPathkeys[0].Expr}, seedNode, int64(cand1.Rows))),
		pathNCols(cand1), pathAvgVarBytes(cand1), -1, pathWidth(cand1))
	if !approx(got.Cost.Total, want.Total) || !approx(got.Cost.Startup, want.Startup) {
		t.Fatalf("PathIncrementalSort.Cost = %+v, want costIncrementalSort's own %+v", got.Cost, want)
	}
	// Both candidates carry the SAME Pathkeys (sortPathkeys in full — an
	// Incremental Sort still delivers the whole requirement, only its
	// cost differs) and the same RequiredOuter/ParallelSafe/DisabledNodes,
	// so add_path's dominance rule correctly prunes arm 2's full Sort here:
	// cand1's prefix credit makes the Incremental Sort strictly cheaper on
	// both axes. That eviction is the entire point of this arm — a real,
	// cost-driven tournament, not a "both survive" plumbing check.
	if len(ordered.Pathlist) != 1 || ordered.Pathlist[0].Kind != PathIncrementalSort {
		t.Fatalf("ordered.Pathlist = %+v, want exactly the dominant PathIncrementalSort (arm 2's costlier Sort correctly pruned)", ordered.Pathlist)
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

// TestIncrementalSortModeFromEnv pins the env-string contract every other
// mode knob in this package shares: unrecognised input fails closed to off.
func TestIncrementalSortModeFromEnv(t *testing.T) {
	cases := map[string]incrementalSortMode{
		"":      incrementalSortOff,
		"off":   incrementalSortOff,
		"bogus": incrementalSortOff,
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
