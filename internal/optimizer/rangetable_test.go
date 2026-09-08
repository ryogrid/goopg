package optimizer

import (
	"strings"
	"testing"
)

// C-20b. The range table's job at the boundary is to answer "what belongs at
// binding coordinate b", and the assertion's job is to abstain everywhere it
// cannot answer honestly. Both halves are pinned here, because an assertion
// that abstains too much passes for the wrong reason and one that abstains too
// little aborts a correct plan.

// rtRoot is the two-rel fixture with the search having chosen `b` (binding
// columns 2-4) as the outer side — the reordering shape, so the layout is a
// non-trivial permutation rather than the identity.
func rtRoot() (*Path, *RelOptInfo, *RelOptInfo) {
	a, b := cpjTwoRel()
	return cprHashRoot(b, a), a, b
}

func TestRangeTableFromPathCollectsEveryBaseRel(t *testing.T) {
	p, _, _ := rtRoot()
	rt := rangeTableFromPath(p)

	// `a` occupies binding coordinates 0-1, `b` 2-4 — the pre-search
	// concatenation, which is what `baseOffset` records.
	want := []string{"a0", "a1", "b0", "b1", "b2"}
	for coord, name := range want {
		col, ok := rt.at(coord)
		if !ok {
			t.Fatalf("range table has no entry for binding coordinate %d (want %q)", coord, name)
		}
		if col.Name != name {
			t.Errorf("binding coordinate %d = %q, want %q", coord, col.Name, name)
		}
	}
	if _, ok := rt.at(5); ok {
		t.Errorf("range table answered for coordinate 5; only 0-4 are bound")
	}
}

// A rel reached through two paths is the normal case (paths share rels), so a
// repeat with the same column must be idempotent rather than ambiguous.
func TestRangeTableRepeatedRelIsIdempotent(t *testing.T) {
	a, b := cpjTwoRel()
	// Both children over the SAME rel `a`: not a legal plan, but exactly the
	// collector state a shared rel produces, and the shape that must not be
	// mistaken for a conflict.
	p := cpjHashPath(cpjLeafPath(a), cpjLeafPath(a), nil, nil)
	_ = b
	rt := rangeTableFromPath(p)
	col, ok := rt.at(0)
	if !ok || col.Name != "a0" {
		t.Fatalf("binding coordinate 0 = %q/%v after the same rel was collected twice, want a0/true", col.Name, ok)
	}
}

// Two DIFFERENT leaves claiming one coordinate is a producer bug, but it is
// `boundaryMap`'s duplicate panic to report. The identity check has no oracle
// there and must say nothing.
func TestRangeTableConflictingClaimAbstains(t *testing.T) {
	a := cpjLeafRel(0, 0, 2, "a")
	clash := cpjLeafRel(1, 0, 2, "z") // also starts at binding coordinate 0
	p := cpjHashPath(cpjLeafPath(a), cpjLeafPath(clash), nil, nil)

	rt := rangeTableFromPath(p)
	if _, ok := rt.at(0); ok {
		t.Errorf("range table answered for a coordinate two leaves claim; it has no unambiguous answer there")
	}
}

func TestBoundaryColumnIdentityAcceptsAWellFormedRoot(t *testing.T) {
	p, _, _ := rtRoot()
	// The real path: `createPlanAtSearchRoot` runs the assertion itself, so a
	// well-formed tree reaching the end is the pin.
	n := createPlanAtSearchRoot(p, 5)
	if n == nil {
		t.Fatalf("createPlanAtSearchRoot built no node")
	}
}

// The mismatch the check exists for: the layout says binding coordinate 2
// (`b0`), the built node emits something else there. `boundaryMap` sees a
// perfect permutation and passes; only the range table can tell.
func TestBoundaryColumnIdentityCatchesAMisplacedColumn(t *testing.T) {
	p, _, _ := rtRoot()
	n, lay := createPlanNode(p)
	rt := rangeTableFromPath(p)

	// Rename the emitted column at output position 0. The join chose `b` as
	// outer, so output 0 carries binding coordinate 2 = `b0`.
	j, ok := n.(*Join)
	if !ok {
		t.Fatalf("root = %T, want *Join", n)
	}
	sch := append(Schema{}, j.Output()...)
	sch[0].Name = "a0" // a real column name, from the other relation
	j.schema = sch

	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "the layout and the emitted schema disagree") {
			t.Fatalf("panic %q does not name the layout/schema disagreement", msg)
		}
	}()
	assertBoundaryColumnIdentity(j, lay, rt)
}

// The self-join case, which no name-based check in the planner can see: two
// instances of one relation, identical column names, distinguished only by
// `SchemaColumn.SourceTableIdx` — goopg's `varno`.
func TestBoundaryColumnIdentityCatchesASwappedSelfJoinInstance(t *testing.T) {
	l1 := cpjLeafRel(0, 0, 2, "l")
	l2 := cpjLeafRel(1, 2, 2, "l")
	stampSourceIdx(l1, 1)
	stampSourceIdx(l2, 2)
	key := equiClauseOn(relsetOf(0), relsetOf(1), 0, 2)
	p := cpjHashPath(cpjLeafPath(l1), cpjLeafPath(l2), []*restrictInfo{key}, nil)

	n, lay := createPlanNode(p)
	rt := rangeTableFromPath(p)
	j := n.(*Join)
	sch := append(Schema{}, j.Output()...)
	// Output 0 is l1's `l0` (source 1). Claim it comes from the second
	// instance: same name, wrong relation.
	sch[0].SourceTableIdx = 2
	j.schema = sch

	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "were swapped") {
			t.Fatalf("panic %q does not name the swapped self-join instance", msg)
		}
	}()
	assertBoundaryColumnIdentity(j, lay, rt)
}

// Abstention: an unnamed column is one the check cannot see. `ColumnRef.Name`
// and `SchemaColumn.Name` are "for diagnostics" and ARE empty on some
// construction paths, so an empty name must abstain rather than compare.
func TestBoundaryColumnIdentityAbstainsOnAnUnnamedColumn(t *testing.T) {
	p, _, _ := rtRoot()
	n, lay := createPlanNode(p)
	rt := rangeTableFromPath(p)
	j := n.(*Join)
	sch := append(Schema{}, j.Output()...)
	sch[0].Name = ""
	j.schema = sch

	assertBoundaryColumnIdentity(j, lay, rt) // must not panic
}

// Abstention: a source identity of zero means "unknown / derived"
// (plan.go:40), not "relation zero", so it must never be compared.
func TestBoundaryColumnIdentityAbstainsOnUnknownSourceIdentity(t *testing.T) {
	l1 := cpjLeafRel(0, 0, 2, "l")
	l2 := cpjLeafRel(1, 2, 2, "l")
	stampSourceIdx(l1, 1)
	stampSourceIdx(l2, 2)
	key := equiClauseOn(relsetOf(0), relsetOf(1), 0, 2)
	p := cpjHashPath(cpjLeafPath(l1), cpjLeafPath(l2), []*restrictInfo{key}, nil)

	n, lay := createPlanNode(p)
	rt := rangeTableFromPath(p)
	j := n.(*Join)
	sch := append(Schema{}, j.Output()...)
	sch[0].SourceTableIdx = 0
	j.schema = sch

	assertBoundaryColumnIdentity(j, lay, rt) // must not panic
}

// Abstention: a coordinate the range table has no entry for. A padded slot
// (M0134-0187) is licensed by construction and a real hole is already
// `boundaryMap`'s panic, so this arm says nothing either way.
func TestBoundaryColumnIdentityAbstainsOnAnUnknownCoordinate(t *testing.T) {
	p, _, _ := rtRoot()
	n, lay := createPlanNode(p)
	rt := rangeTableFromPath(p)
	delete(rt.cols, lay[0])

	j := n.(*Join)
	sch := append(Schema{}, j.Output()...)
	sch[0].Name = "a0"
	j.schema = sch

	assertBoundaryColumnIdentity(j, lay, rt) // must not panic
}

// stampSourceIdx gives every column of a fixture rel's leaf the FROM item
// identity the binder would have stamped, which is what makes the two halves
// of a self-join distinguishable.
func stampSourceIdx(rel *RelOptInfo, idx int16) {
	scan := rel.baseLeaf.(*SeqScan)
	sch := append(Schema{}, scan.schema...)
	for i := range sch {
		sch[i].SourceTableIdx = idx
	}
	scan.schema = sch
}
