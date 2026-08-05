package planner

// M0127-P5.5-f-i — the search boundary (createplanroot.go).
//
// The boundary is inert in production (`GOOPG_PGSHAPED_DP` OFF, no `planSelect`
// caller), so these tests are its only observer. What they pin is the property
// the whole task exists for: a reference written in PRE-SEARCH BINDING
// coordinates — which is every reference the enclosing tree holds — must resolve
// to the same column after the search as before it, no matter how the search
// reordered the joins. Every fixture therefore chooses a join order the search
// would not have produced by accident, and asserts on column NAMES rather than
// indexes, because an index test passes for a plan that reads the wrong column.

import (
	"strings"
	"testing"
)

// cprHashRoot assembles a two-rel hash-join root with the given (outer, inner)
// orientation. It reuses createplanjoin_test.go's fixtures: relid 0 is `a` at
// binding columns 0-1, relid 1 is `b` at binding columns 2-4.
func cprHashRoot(outer, inner *RelOptInfo) *Path {
	key := equiClauseOn(relsetOf(0), relsetOf(1), 0, 3)
	return cpjHashPath(cpjLeafPath(outer), cpjLeafPath(inner), []*restrictInfo{key}, nil)
}

// schemaNames renders a schema as its column names, so a failure message says
// which columns were published rather than how many.
func schemaNames(s Schema) []string {
	out := make([]string, len(s))
	for i, c := range s {
		out[i] = c.Name
	}
	return out
}

// TestCreatePlanAtSearchRootPublishesBindingOrder is the central test.
//
// The search chose relid 1 (`b`, binding columns 2-4) as the OUTER side, so the
// join's own row is `b0 b1 b2 a0 a1`. The enclosing tree, however, was resolved
// against `a0 a1 b0 b1 b2` — so the boundary must republish that order, and the
// Project's target list is the map that does it.
//
// The assertion is on names: an index-only test would pass for a plan that
// permuted the columns to the right POSITIONS while reading the wrong ones.
func TestCreatePlanAtSearchRootPublishesBindingOrder(t *testing.T) {
	a, b := cpjTwoRel()
	n := createPlanAtSearchRoot(cprHashRoot(b, a), 5)

	p, ok := n.(*Project)
	if !ok {
		t.Fatalf("createPlanAtSearchRoot = %T, want a *Project reordering the search's row", n)
	}
	if _, isJoin := p.Child.(*Join); !isJoin {
		t.Fatalf("boundary Project's child = %T, want the *Join the search chose", p.Child)
	}
	want := []string{"a0", "a1", "b0", "b1", "b2"}
	if got := schemaNames(p.Output()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("boundary schema = %v, want the pre-search concatenation %v", got, want)
	}
	// The targets are the map itself: binding 0 (`a0`) sits at the join's
	// column 3, binding 2 (`b0`) at the join's column 0.
	wantIdx := []int{3, 4, 0, 1, 2}
	for bind, target := range p.Targets {
		cr, isCol := target.(*ColumnRef)
		if !isCol {
			t.Fatalf("boundary target %d = %T, want a pass-through *ColumnRef", bind, target)
		}
		if cr.Index != wantIdx[bind] {
			t.Errorf("boundary target %d reads join column %d, want %d (%s)", bind, cr.Index, wantIdx[bind], want[bind])
		}
		if cr.Name != want[bind] {
			t.Errorf("boundary target %d is named %q, want %q", bind, cr.Name, want[bind])
		}
	}
}

// TestCreatePlanAtSearchRootMakesEnclosingReferencesCorrectAgain states the
// property the Project buys, in the terms the caller cares about: an expression
// the enclosing tree already holds — written in binding coordinates, never
// touched by the search — addresses the same column through the boundary as it
// did before the search ran.
//
// This is the test that would fail if a later loop "optimised away" the Project
// without rewriting the enclosing tree.
func TestCreatePlanAtSearchRootMakesEnclosingReferencesCorrectAgain(t *testing.T) {
	a, b := cpjTwoRel()
	// The pre-search concatenation the enclosing tree was resolved against.
	binding := append(append(Schema{}, a.baseLeaf.Output()...), b.baseLeaf.Output()...)

	// The search reorders: b outer, a inner.
	root := createPlanAtSearchRoot(cprHashRoot(b, a), 5)
	got := root.Output()
	if len(got) != len(binding) {
		t.Fatalf("boundary publishes %d columns, the bindings had %d", len(got), len(binding))
	}
	for i := range binding {
		if got[i].Name != binding[i].Name {
			t.Errorf("an enclosing ColumnRef at index %d resolved to %q before the search and %q after it",
				i, binding[i].Name, got[i].Name)
		}
	}
}

// TestCreatePlanAtSearchRootElidesTheProjectWhenOrdersCoincide: a left-deep tree
// whose outer spine starts at FROM item 0 already publishes binding order, and
// there the Project would be a pure per-row copy. 03 §10 anticipated the
// elision; this pins that it actually happens rather than being described.
func TestCreatePlanAtSearchRootElidesTheProjectWhenOrdersCoincide(t *testing.T) {
	a, b := cpjTwoRel()
	n := createPlanAtSearchRoot(cprHashRoot(a, b), 5)
	if _, isProject := n.(*Project); isProject {
		t.Fatalf("createPlanAtSearchRoot added a boundary Project over an already-binding-ordered row")
	}
	if _, isJoin := n.(*Join); !isJoin {
		t.Fatalf("createPlanAtSearchRoot = %T, want the *Join unchanged", n)
	}
}

// TestCreatePlanAtSearchRootThreeRelPermutation: with two rels the permutation
// is its own inverse, so a boundary map applied in the WRONG direction still
// passes. Three rels in a bushy shape — (c ⋈ a) ⋈ b, i.e. binding ranges
// 5-6, 0-1, 2-4 — is the smallest fixture where composing the map backwards
// produces a different, still-runnable answer.
func TestCreatePlanAtSearchRootThreeRelPermutation(t *testing.T) {
	a := cpjLeafRel(0, 0, 2, "a")
	b := cpjLeafRel(1, 2, 3, "b")
	c := cpjLeafRel(2, 5, 2, "c")

	// (c ⋈ a) — join row `c0 c1 a0 a1`.
	inner := cpjHashPath(cpjLeafPath(c), cpjLeafPath(a),
		[]*restrictInfo{equiClauseOn(c.Relids, a.Relids, 5, 0)}, nil)
	// … ⋈ b — join row `c0 c1 a0 a1 b0 b1 b2`.
	root := cpjHashPath(inner, cpjLeafPath(b),
		[]*restrictInfo{equiClauseOn(inner.Rel.Relids, b.Relids, 0, 2)}, nil)

	n := createPlanAtSearchRoot(root, 7)
	want := []string{"a0", "a1", "b0", "b1", "b2", "c0", "c1"}
	if got := schemaNames(n.Output()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("boundary schema = %v, want the pre-search concatenation %v", got, want)
	}
}

// TestCreatePlanAtSearchRootRefusesAHole is why `bindingWidth` is a parameter
// and not `len(layout)`.
//
// The root below is entirely self-consistent — `a` then `b`, five columns, a
// clean permutation of [0,5) — and still WRONG, because the query had a third
// FROM item at binding columns 5-6 that never entered the search. Judged against
// its own width the root looks perfect; judged against the concatenation the
// enclosing tree was resolved against, it is missing two columns that references
// above it still address. That is the M0097-0058 shape exactly, and it is
// detectable only from the outside.
func TestCreatePlanAtSearchRootRefusesAHole(t *testing.T) {
	a, b := cpjTwoRel()
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "does not publish binding coordinate(s) [5 6]") {
			t.Fatalf("panic %q does not name the unpublished coordinates", r)
		}
	}()
	createPlanAtSearchRoot(cprHashRoot(a, b), 7)
}

// TestCreatePlanAtSearchRootRefusesAnOutOfRangeCoordinate: a coordinate past the
// binding concatenation means a leaf's recorded `baseOffset` disagrees with the
// space the enclosing tree was resolved in — caught before it can index past the
// end of a target list.
func TestCreatePlanAtSearchRootRefusesAnOutOfRangeCoordinate(t *testing.T) {
	a := cpjLeafRel(0, 0, 2, "a")
	b := cpjLeafRel(1, 9, 3, "b")
	// The key is written against the offsets as recorded, so the join arm's own
	// translation succeeds and the boundary is what refuses.
	root := cpjHashPath(cpjLeafPath(a), cpjLeafPath(b),
		[]*restrictInfo{equiClauseOn(a.Relids, b.Relids, 0, 9)}, nil)
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "outside the 5-column binding concatenation") {
			t.Fatalf("panic %q does not name the out-of-range coordinate", r)
		}
	}()
	createPlanAtSearchRoot(root, 5)
}

// TestCreatePlanAtSearchRootRefusesUnknownCoordinates: the C0 bridge's prebuilt
// subtree has no binding coordinates at all (see `outputLayout`'s doc), which is
// legitimate everywhere except here — a root whose column order nobody knows
// cannot be handed to a tree that indexes into it.
func TestCreatePlanAtSearchRootRefusesUnknownCoordinates(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "column coordinates are unknown") {
			t.Fatalf("panic %q does not name the unknown coordinates", r)
		}
	}()
	createPlanAtSearchRoot(newPrebuiltPath(&RelOptInfo{}, &SeqScan{schema: cpjSchema("a", 2)}), 2)
}

// TestAssertColumnRefsWithinSchema is 03 §10's tripwire on its own. The class it
// guards (M0097-0058) used to surface as an out-of-bounds slice access during
// EXECUTION — after the query had been accepted, costed and started; the point
// of the assertion is that it becomes an attributable plan-time bug instead.
func TestAssertColumnRefsWithinSchema(t *testing.T) {
	inRange := []Expr{&ColumnRef{Index: 0, Name: "a0"}, &ColumnRef{Index: 2, Name: "b0"}}
	assertColumnRefsWithinSchema("test", inRange, 3)

	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "references column 3 (b1) of a 3-column row") {
			t.Fatalf("panic %q does not name the out-of-bounds reference", r)
		}
	}()
	assertColumnRefsWithinSchema("test", []Expr{&ColumnRef{Index: 3, Name: "b1"}}, 3)
}
