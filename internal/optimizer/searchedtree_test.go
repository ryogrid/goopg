package optimizer

// M0127-P5.5-f-ii-a — the searched-subtree tag (searchedtree.go).
//
// The tag is LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON, so real trees carry it and these tests are
// no longer its only observer. Two of them are unusual and
// deserve the note: `TestLegacyPosMapAlreadyStoppedAtTheBoundaryProject` and
// `TestElidedSearchRootIsTheHoleTheTagCloses` assert on what the code did
// BEFORE this task, because the value of the tag is exactly the difference
// between those two shapes — and a later loop that "simplifies" the tag away
// needs to be able to see which half was already covered and which was not.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// stNamedEqui is `equiClauseOn` with NAMED operands.
//
// The P5.5-e fixtures build their clause operands with `col(i)`, which leaves
// `ColumnRef.Name` empty — and `reresolveJoinByName` returns immediately on an
// unnamed ref. Reused here, every assertion about name resolution would pass
// vacuously. That is a fair model of nothing: the search's real clauses come
// from the resolved WHERE predicate (`joinrestrict.go`), whose ColumnRefs carry
// the column name. So these tests supply names, and the vacuity is recorded
// rather than inherited.
func stNamedEqui(left, right RelSet, leftCol, rightCol int, leftName, rightName string) *restrictInfo {
	ri := equiClause(left, right)
	ri.leftKey = &ColumnRef{Index: leftCol, Name: leftName, Type: catalog.Type{Name: "int4"}}
	ri.rightKey = &ColumnRef{Index: rightCol, Name: rightName, Type: catalog.Type{Name: "int4"}}
	return ri
}

// stHashRoot is `cprHashRoot` with a named clause: `a.a0 = b.b1`, written in
// binding coordinates as col(0) = col(3).
func stHashRoot(outer, inner *RelOptInfo) *Path {
	key := stNamedEqui(relsetOf(0), relsetOf(1), 0, 3, "a0", "b1")
	return cpjHashPath(cpjLeafPath(outer), cpjLeafPath(inner), []*restrictInfo{key}, nil)
}

// stBindings builds the FROM-clause bindings for createplanjoin_test.go's
// two-rel fixture: `a` at binding columns 0-1, `b` at 2-4.
func stBindings(a, b *RelOptInfo) []rangeBinding {
	return []rangeBinding{
		{table: a.baseLeaf.(*SeqScan).Table, alias: a.baseLeaf.(*SeqScan).Alias, offset: 0},
		{table: b.baseLeaf.(*SeqScan).Table, alias: b.baseLeaf.(*SeqScan).Alias, offset: 2},
	}
}

// stTargetIndices renders a Project's pass-through targets as the child columns
// they read, which is the boundary map in its stored form.
func stTargetIndices(t *testing.T, p *Project) []int {
	t.Helper()
	out := make([]int, len(p.Targets))
	for i, tg := range p.Targets {
		cr, ok := tg.(*ColumnRef)
		if !ok {
			t.Fatalf("boundary target %d = %T, want a pass-through *ColumnRef", i, tg)
		}
		out[i] = cr.Index
	}
	return out
}

// TestSearchRootCarriesTheTagInBothShapes: the tag must be a property of the
// SEARCH, not of whether a boundary Project happened to be needed. A caller
// that reordered gets a tagged `*Project`; a caller whose order already was
// binding order gets a tagged `*Join`. If only the first were tagged, the
// elided case — the common left-deep one — would be the unprotected one.
func TestSearchRootCarriesTheTagInBothShapes(t *testing.T) {
	a, b := cpjTwoRel()

	reordered := createPlanAtSearchRoot(stHashRoot(b, a), 5)
	if _, ok := reordered.(*Project); !ok {
		t.Fatalf("reordering root = %T, want the boundary *Project", reordered)
	}
	if !isSearchedTree(reordered) {
		t.Errorf("the boundary *Project is not tagged as a searched subtree")
	}

	a2, b2 := cpjTwoRel()
	elided := createPlanAtSearchRoot(stHashRoot(a2, b2), 5)
	if _, ok := elided.(*Join); !ok {
		t.Fatalf("elided root = %T, want the *Join unchanged", elided)
	}
	if !isSearchedTree(elided) {
		t.Errorf("the elided search root is not tagged; the legacy posmap family would walk into it")
	}

	// A tree nobody searched answers false, and so does a node kind that
	// cannot carry the tag at all.
	if isSearchedTree(&SeqScan{schema: cpjSchema("a", 2)}) {
		t.Errorf("an untouched scan reports itself as a searched subtree")
	}
	if isSearchedTree(&Limit{}) {
		t.Errorf("a node kind with no tag field reports true")
	}
}

// TestMarkSearchedTreeRefusesAnUntaggableRoot pins the loud failure. A future
// `createPlan` arm that returns a root kind nobody embedded `searchedTree` in
// would otherwise produce a SILENTLY untagged subtree — which is a plan that
// runs and returns wrong rows, the exact class this milestone removes.
func TestMarkSearchedTreeRefusesAnUntaggableRoot(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "cannot carry the searched-subtree tag") {
			t.Fatalf("panic %q does not name the untaggable root kind", r)
		}
	}()
	markSearchedTree(&Limit{})
}

// Four tests lived here until C-20b (take3 08 §9.2, 2026-09-07), and all four
// were pins on the searched subtree's OPACITY to the legacy bindings-posmap
// family: `TestLegacyPosMapAlreadyStoppedAtTheBoundaryProject`,
// `TestElidedSearchRootIsTheHoleTheTagCloses`,
// `TestBuildBindingsPosMapAdvancesPastASearchedTree` and
// `TestApplyJoinTreePosMapDoesNotDescendIntoASearchedTree`.
//
// They went with `buildBindingsPosMap` / `applyJoinTreePosMap` themselves —
// deleted on a census showing the family moved zero ColumnRefs over TPC-H and
// TPC-DS on both `GOOPG_PGSHAPED_DP` arms, with byte-identical plans without
// it (joinlayout.go carries the numbers). An opacity pin outlives neither the
// pass it is opaque to nor the map it declines to build.
//
// The tag itself is NOT deleted with them, and neither is the rest of this
// file: `isSearchedTree` has eight surviving consumers (narrowoutput.go,
// pushdown.go, createplanroot.go, nl_index_join.go, enclosingtree.go,
// scan_input_rewrite.go), and `reconcileNLILayout` — whose skip the next test
// pins — is still the oracle `assertSearchedTreeNeedsNoReconcile` runs on
// every searched plan.

// TestReconcileNLILayoutSkipsASearchedTree: the skip has to be observable, not
// just asserted. A join key is deliberately left pointing at a column whose
// name lives elsewhere; `reresolveJoinByName` would move it, and on a searched
// tree it must not run at all.
func TestReconcileNLILayoutSkipsASearchedTree(t *testing.T) {
	a, b := cpjTwoRel()
	root := createPlanAtSearchRoot(stHashRoot(a, b), 5).(*Join)
	// The right key is `b1`, correctly bound to merged column 3. Point it at
	// column 2 (`b0`) — an index that exists, so nothing crashes, and a plan
	// that joins on the wrong column.
	root.RightKey.(*ColumnRef).Index = 2

	reconcileNLILayout(root)
	if got := root.RightKey.(*ColumnRef).Index; got != 2 {
		t.Errorf("reconcileNLILayout re-resolved a searched join's key to %d; it must not run on a searched subtree", got)
	}

	// Untagged, the same call does move it — which is what makes the skip a
	// real behavioural difference rather than a decoration.
	root.fromJoinSearch = false
	reconcileNLILayout(root)
	if got := root.RightKey.(*ColumnRef).Index; got != 3 {
		t.Errorf("reconcileNLILayout left the key at %d even untagged — this test no longer demonstrates the skip", got)
	}
}

// TestAssertSearchedTreeNeedsNoReconcileAcceptsAWellFormedTree: the arms bound
// this tree's keys by coordinate arithmetic over `outputLayout`; name
// resolution, an unrelated mechanism, must reach the same indices. That
// agreement is the only evidence outside the arms' own tests that the
// arithmetic is right.
func TestAssertSearchedTreeNeedsNoReconcileAcceptsAWellFormedTree(t *testing.T) {
	a, b := cpjTwoRel()
	n, _ := createPlanNode(stHashRoot(b, a))
	assertSearchedTreeNeedsNoReconcile(n)
}

// TestAssertSearchedTreeNeedsNoReconcileCatchesADisagreement: an off-by-one in
// a join key — the plausible arm bug — is what the assertion exists to catch,
// and it catches it at plan time with the column named rather than as a wrong
// row count in a gate.
func TestAssertSearchedTreeNeedsNoReconcileCatchesADisagreement(t *testing.T) {
	a, b := cpjTwoRel()
	n, _ := createPlanNode(stHashRoot(b, a))
	j := n.(*Join)
	j.RightKey.(*ColumnRef).Index++

	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "searched subtree needed reconciliation") {
			t.Fatalf("panic %q does not report the disagreement", r)
		}
	}()
	assertSearchedTreeNeedsNoReconcile(n)
}

// TestBoundaryProjectSurvivesReconcileTargets records the measured margin the
// file header calls narrow: name resolution reproduces the boundary map only
// because each target carries the Name of the column it means. If a later
// change drops those names — say, by rebuilding targets as bare positional
// refs — the boundary would become genuinely fragile rather than merely
// skipped, and this is the test that would say so.
func TestBoundaryProjectSurvivesReconcileTargets(t *testing.T) {
	a, b := cpjTwoRel()
	p := createPlanAtSearchRoot(stHashRoot(b, a), 5).(*Project)
	before := stTargetIndices(t, p)

	p.fromJoinSearch = false
	reconcileNLILayoutBody(p)

	after := stTargetIndices(t, p)
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("boundary target %d moved from child column %d to %d under name resolution; the map is no longer recoverable from the targets' names",
				i, before[i], after[i])
		}
	}
}
