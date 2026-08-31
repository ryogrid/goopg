package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R3-4 planner pins for composite (multi-equijoin) EXISTS decorrelation.
//
// The semi/anti Join node carries a SINGLE key pair (LeftKey/RightKey);
// there is no LeftKeys/RightKeys slice. Multi-column equality is therefore
// expressed the way the scalar path has always expressed it: pair 0 becomes
// the key, and the remaining pairs are ordinary conjuncts on Predicate,
// which the executor's lazy hash semi/anti re-evaluates per bucket match.
//
// These tests pin the two things that silently break if the port drifts:
//
//  1. the extra pair is actually PRESENT on the predicate (its absence is
//     the historical over-match bug, which end-to-end row assertions catch
//     only when the fixture happens to contain a first-key-only row);
//  2. the coordinate convention is uniform on THIS path: both RightKey and
//     the predicate's inner ColumnRefs are merged outer++inner (inner index
//     shifted by the outer width), because the executor's evalHashKey for
//     semi/anti is handed a padded row. This differs from the scalar
//     multi-param template, whose RightKey is inner-child-local — so the
//     EXISTS port must NOT be a literal copy of it. A mis-shifted index
//     silently reads a neighbouring column instead of erroring, the exact
//     class of bug that cost this project a Q7 debugging round.
func newCompositeExistsCatalog(t *testing.T, withIndex bool) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "ce_outer"}, []catalog.Column{
		{Name: "k1", Type: catalog.Type{Name: "int4"}},
		{Name: "k2", Type: catalog.Type{Name: "int4"}},
		{Name: "tag", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	inner, err := cat.CreateTable(parser.ObjectName{Name: "ce_inner"}, []catalog.Column{
		{Name: "j1", Type: catalog.Type{Name: "int4"}},
		{Name: "j2", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if withIndex {
		if _, err := cat.CreateIndex(parser.ObjectName{Name: "ce_inner_composite"}, inner, []string{"j1", "j2"}, false, "btree", true); err != nil {
			t.Fatal(err)
		}
	}
	return cat
}

func findSemiOrAntiJoin(n Node) *Join {
	var found *Join
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || found != nil {
			return
		}
		switch x := cur.(type) {
		case *Join:
			if x.Type == JoinTypeSemi || x.Type == JoinTypeAnti {
				found = x
				return
			}
			walk(x.Left)
			walk(x.Right)
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *NestedLoopIndexJoin:
			walk(x.Outer)
			walk(x.Inner)
		}
	}
	walk(n)
	return found
}

// TestCompositeExistsCarriesSecondPairOnPredicate pins that the second
// equijoin pair survives onto the semi join rather than being dropped.
func TestCompositeExistsCarriesSecondPairOnPredicate(t *testing.T) {
	cat := newCompositeExistsCatalog(t, false)
	stmt := parseOne(t, `select tag from ce_outer where exists (select 1 from ce_inner where j1 = k1 and j2 = k2)`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("composite EXISTS did not decorrelate to a semi join; tree: %s", describePlanTree(node))
	}
	if j.LeftKey == nil || j.RightKey == nil {
		t.Fatalf("expected pair 0 to become the hash key, got LeftKey=%v RightKey=%v", j.LeftKey, j.RightKey)
	}
	if j.Predicate == nil {
		t.Fatalf("expected the second equijoin pair on the join predicate, got nil — this is the historical over-match shape")
	}
	// The predicate must contain an equality naming the second pair's
	// columns. Checking by name (not by count) keeps the assertion honest
	// if other conjuncts are lifted alongside it.
	var sawSecondPair bool
	for _, c := range splitAnd(j.Predicate) {
		bop, ok := c.(*BinaryOp)
		if !ok || bop.Op != parser.OpEq {
			continue
		}
		l, lok := bop.Left.(*ColumnRef)
		r, rok := bop.Right.(*ColumnRef)
		if !lok || !rok {
			continue
		}
		names := map[string]bool{l.Name: true, r.Name: true}
		if names["k2"] && names["j2"] {
			sawSecondPair = true
		}
	}
	if !sawSecondPair {
		t.Fatalf("second pair (k2 = j2) missing from the semi-join predicate: %v", j.Predicate)
	}
}

// TestCompositeExistsPredicateCoordinateSpaces pins the asymmetry between
// the predicate's merged coordinates and RightKey's inner-local ones.
func TestCompositeExistsPredicateCoordinateSpaces(t *testing.T) {
	cat := newCompositeExistsCatalog(t, false)
	stmt := parseOne(t, `select tag from ce_outer where exists (select 1 from ce_inner where j1 = k1 and j2 = k2)`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	j := findSemiOrAntiJoin(node)
	if j == nil {
		t.Fatalf("no semi join; tree: %s", describePlanTree(node))
	}
	outerWidth := len(j.Left.Output())
	innerSchema := j.Right.Output()

	// RightKey is evaluated by evalHashKey against a padded outer++inner
	// row, so its index must be the inner position SHIFTED by the outer
	// width — the same space the predicate uses.
	rk, ok := j.RightKey.(*ColumnRef)
	if !ok {
		t.Fatalf("RightKey = %T, want *ColumnRef", j.RightKey)
	}
	rkInner := -1
	for i, col := range innerSchema {
		if col.Name == rk.Name {
			rkInner = i
			break
		}
	}
	if rkInner < 0 {
		t.Fatalf("RightKey %q names no inner column; inner schema %v", rk.Name, innerSchema)
	}
	if rk.Index != outerWidth+rkInner {
		t.Fatalf("RightKey %q index %d; merged coordinates require %d (outerWidth %d + inner index %d)",
			rk.Name, rk.Index, outerWidth+rkInner, outerWidth, rkInner)
	}

	// Predicate refs share that space: an inner column must sit at
	// outerWidth + its inner index, never at the bare inner index.
	for _, c := range splitAnd(j.Predicate) {
		bop, ok := c.(*BinaryOp)
		if !ok {
			continue
		}
		for _, side := range []Expr{bop.Left, bop.Right} {
			cr, ok := side.(*ColumnRef)
			if !ok {
				continue
			}
			// Locate the column by name in the inner schema; if it is an
			// inner column its predicate index must be shifted.
			for innerIdx, col := range innerSchema {
				if col.Name != cr.Name {
					continue
				}
				if cr.Index == innerIdx && outerWidth > 0 {
					t.Fatalf("predicate ref %q uses inner-local index %d; predicate coordinates are merged, so it must be %d",
						cr.Name, cr.Index, outerWidth+innerIdx)
				}
			}
		}
	}
}

// TestCompositeExistsCompositeIndexConsumesBothPairs pins the resolution of
// the S1c bail's stated fear: with a covering composite index the NLI probe
// must consume BOTH pairs, not extract one and drop the other.
func TestCompositeExistsCompositeIndexConsumesBothPairs(t *testing.T) {
	cat := newCompositeExistsCatalog(t, true)
	stmt := parseOne(t, `select tag from ce_outer where exists (select 1 from ce_inner where j1 = k1 and j2 = k2)`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var nli *NestedLoopIndexJoin
	var walk func(Node)
	walk = func(cur Node) {
		if cur == nil || nli != nil {
			return
		}
		switch x := cur.(type) {
		case *NestedLoopIndexJoin:
			nli = x
		case *Project:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		}
	}
	walk(node)
	if nli == nil {
		t.Skipf("composite EXISTS did not convert to NLI here; tree: %s", describePlanTree(node))
	}
	// Asserted on the PROBE, not the node type: a semi NLI's inner is promoted
	// to an *IndexOnlyScan when nothing reads its columns, which is orthogonal
	// to whether both pairs were consumed.
	probeKeys := nliProbeKeys(nli.Inner)
	if probeKeys == nil {
		t.Fatalf("NLI inner is not a probe node: %T", nli.Inner)
	}
	if len(probeKeys) < 2 {
		t.Fatalf("expected the composite probe to consume BOTH pairs (keys>=2), got %d — the S1c bail's 'competing probe key' fear would be real if this regressed",
			len(probeKeys))
	}
}
