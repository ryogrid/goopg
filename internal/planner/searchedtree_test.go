package planner

// M0127-P5.5-f-ii-a — the searched-subtree tag (searchedtree.go).
//
// The tag is inert in production (`GOOPG_PGSHAPED_DP` OFF, no `planSelect`
// caller), so these tests are its only observer. Two of them are unusual and
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

// TestLegacyPosMapAlreadyStoppedAtTheBoundaryProject records the half that was
// already covered, and by what.
//
// M0125-0012 (TPC-DS Q8) made EVERY `*Project` in a join tree an opaque scope
// boundary on both sides of the map — `collect` advances past one, and
// `applyJoinTreePosMap` returns at one. The boundary Project inherited that for
// free. This test states the inheritance explicitly so that a later change to
// the Project rule, made for FROM-subquery reasons, cannot quietly remove the
// search's protection without a failure naming it.
func TestLegacyPosMapAlreadyStoppedAtTheBoundaryProject(t *testing.T) {
	a, b := cpjTwoRel()
	root := createPlanAtSearchRoot(stHashRoot(b, a), 5).(*Project)
	root.fromJoinSearch = false // as the pass would have seen it before this task

	if pm := buildBindingsPosMap(root, stBindings(a, b)); pm != nil {
		t.Errorf("buildBindingsPosMap descended into a boundary Project; the *Project arm is supposed to stop there")
	}
}

// TestElidedSearchRootIsTheHoleTheTagCloses is the other half: with no Project
// to stop at, the legacy family walks the searched joins.
//
// Untagged, `collect` reaches the leaves and builds a map — numerically the
// identity here, since an elided root by definition publishes binding order.
// The point is not the arithmetic. It is that reaching the leaves is what puts
// the searched `*Join` in `applyJoinTreePosMap`'s path, where
// `reresolveJoinByName` rebinds its keys by NAME over a layout that was derived
// by coordinate one node earlier.
func TestElidedSearchRootIsTheHoleTheTagCloses(t *testing.T) {
	a, b := cpjTwoRel()
	root := createPlanAtSearchRoot(stHashRoot(a, b), 5)
	bindings := stBindings(a, b)

	root.(*Join).fromJoinSearch = false
	if pm := buildBindingsPosMap(root, bindings); pm == nil {
		t.Fatalf("untagged, an elided search root was already opaque — this test no longer describes the hole it was written for")
	}

	root.(*Join).fromJoinSearch = true
	if pm := buildBindingsPosMap(root, bindings); pm != nil {
		t.Errorf("the tag did not make the elided search root opaque to buildBindingsPosMap")
	}
}

// TestBuildBindingsPosMapAdvancesPastASearchedTree: the searched subtree is an
// opaque LEAF, not a stop sign. A scan sitting to its right in the enclosing
// tree still needs an offset that accounts for the searched tree's full width —
// omitting the advance is the RC-2 / M0097-0058 defect (`off` too low, so every
// scan to the right is remapped into another table's columns).
func TestBuildBindingsPosMapAdvancesPastASearchedTree(t *testing.T) {
	a, b := cpjTwoRel()
	searched := createPlanAtSearchRoot(stHashRoot(b, a), 5) // 5 columns, tagged

	// A third relation `c` at binding columns 5-6, joined above the searched
	// subtree — the shape a pinned spine leaves behind (predp.go).
	c := cpjLeafRel(2, 5, 2, "c")
	top := &Join{Type: JoinTypeInner, Left: searched, Right: c.baseLeaf,
		schema: append(append(Schema{}, searched.Output()...), c.baseLeaf.Output()...)}

	bindings := append(stBindings(a, b), rangeBinding{
		table: c.baseLeaf.(*SeqScan).Table, alias: c.baseLeaf.(*SeqScan).Alias, offset: 5})

	pm := buildBindingsPosMap(top, bindings)
	if pm == nil {
		t.Fatalf("buildBindingsPosMap declined; `c` still needs remapping")
	}
	// `c` starts at binding column 5 and the searched tree publishes 5
	// columns, so `c`'s first column is at plan offset 5 — unchanged. Had the
	// advance been omitted it would have come out 0, landing inside `a`.
	for i := 0; i < 2; i++ {
		if got := pm(5 + i); got != 5+i {
			t.Errorf("binding column %d (c%d) maps to plan offset %d, want %d — the searched tree's width was not accounted for",
				5+i, i, got, 5+i)
		}
	}
	// The searched tree's own bindings have no scan entry, so the closure
	// leaves them alone — the identity, which is what the boundary published.
	for i := 0; i < 5; i++ {
		if got := pm(i); got != i {
			t.Errorf("binding column %d inside the searched tree was remapped to %d; it is already where the bindings put it", i, got)
		}
	}
}

// TestApplyJoinTreePosMapDoesNotDescendIntoASearchedTree: the applier must stop
// at the same node the collector stopped at (CLAUDE.md "sibling paths must
// change together" — and the explicit rule M0125-0012 wrote into the *Project
// arm). A deliberately non-identity map is used so that any descent shows up.
func TestApplyJoinTreePosMapDoesNotDescendIntoASearchedTree(t *testing.T) {
	a, b := cpjTwoRel()
	root := createPlanAtSearchRoot(stHashRoot(a, b), 5).(*Join)
	before := []int{root.LeftKey.(*ColumnRef).Index, root.RightKey.(*ColumnRef).Index}

	applyJoinTreePosMap(root, func(i int) int { return (i + 1) % 5 })

	after := []int{root.LeftKey.(*ColumnRef).Index, root.RightKey.(*ColumnRef).Index}
	if before[0] != after[0] || before[1] != after[1] {
		t.Errorf("applyJoinTreePosMap rewrote a searched join's keys from %v to %v", before, after)
	}
}

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
