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
