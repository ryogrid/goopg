package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// findAggregateGK returns the first Aggregate in the plan (depth-first).
func findAggregateGK(n Node) *Aggregate {
	if n == nil {
		return nil
	}
	if a, ok := n.(*Aggregate); ok {
		return a
	}
	for _, c := range gkChildren(n) {
		if a := findAggregateGK(c); a != nil {
			return a
		}
	}
	return nil
}

func gkChildren(n Node) []Node {
	switch x := n.(type) {
	case *Sort:
		return []Node{x.Child}
	case *Limit:
		return []Node{x.Child}
	case *Project:
		return []Node{x.Child}
	case *Filter:
		return []Node{x.Child}
	case *Gather:
		return []Node{x.Child}
	case *GatherMerge:
		return []Node{x.Child}
	case *Aggregate:
		return []Node{x.Child}
	}
	return nil
}

func hasSortNode(n Node) bool {
	if n == nil {
		return false
	}
	if _, ok := n.(*Sort); ok {
		return true
	}
	for _, c := range gkChildren(n) {
		if hasSortNode(c) {
			return true
		}
	}
	return false
}

// TestRedundantConstGroupKeyIsPruned pins M0146-0005ak's GROUP BY half: PG
// 16+ keeps in processed_groupClause only items whose pathkey is not
// redundant, so `GROUP BY ax, ay ... WHERE ax = 1` groups on ay alone and
// ax becomes a passthrough column. With every key pinned nothing is pruned
// (PG's keyless GroupAggregate returns no row on empty input).
func TestRedundantConstGroupKeyIsPruned(t *testing.T) {
	cat := scopeTestCatalog(t)
	agg := findAggregateGK(planSQL(t, cat, "SELECT ax, ay, count(*) FROM a WHERE ax = 1 GROUP BY ax, ay"))
	if agg == nil {
		t.Fatal("no aggregate")
	}
	if len(agg.GroupExprs) != 1 {
		t.Fatalf("group keys = %d, want 1 (ax pinned by WHERE)", len(agg.GroupExprs))
	}
	if cr, ok := agg.GroupExprs[0].(*ColumnRef); !ok || cr.Name != "ay" {
		t.Errorf("remaining key = %v, want ay", agg.GroupExprs[0])
	}
	if len(agg.Passthrough) == 0 {
		t.Error("pinned key must stay addressable as a passthrough column")
	}

	all := findAggregateGK(planSQL(t, cat, "SELECT ax, ay, count(*) FROM a WHERE ax = 1 AND ay = 2 GROUP BY ax, ay"))
	if all == nil || len(all.GroupExprs) != 2 {
		t.Error("with every key pinned no key may be pruned (keyless-group semantics)")
	}

	or := findAggregateGK(planSQL(t, cat, "SELECT ax, ay, count(*) FROM a WHERE ax = 1 OR ay = 2 GROUP BY ax, ay"))
	if or == nil || len(or.GroupExprs) != 2 {
		t.Error("an equality under OR pins nothing")
	}
}

// TestOrderItemPinnedByWhere pins the ORDER BY half: a pinned item sorts
// nothing (PG's redundant sort pathkey), so a query ordered only by pinned
// items needs no Sort.
func TestOrderItemPinnedByWhere(t *testing.T) {
	stmts, err := parser.Parse("SELECT ax FROM a WHERE a.ax = 1 AND ay > 3 ORDER BY ax, ay")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*parser.SelectStmt)
	if !orderItemPinnedByWhere(&parser.ColumnRef{Column: "ax"}, s) {
		t.Error("unqualified ax is pinned by a.ax = 1")
	}
	if orderItemPinnedByWhere(&parser.ColumnRef{Column: "ay"}, s) {
		t.Error("ay > 3 is not an equality")
	}
	if orderItemPinnedByWhere(&parser.ColumnRef{Table: "b", Column: "ax"}, s) {
		t.Error("a different relation's ax is not pinned")
	}
	gs, err := parser.Parse("SELECT ax, ay FROM a WHERE ax = 1 GROUP BY GROUPING SETS (ax, ay) ORDER BY ax, ay")
	if err != nil {
		t.Fatal(err)
	}
	if orderItemPinnedByWhere(&parser.ColumnRef{Column: "ax"}, gs[0].(*parser.SelectStmt)) {
		t.Error("grouping sets null out ax above the WHERE; it is not pinned")
	}
	cat := scopeTestCatalog(t)
	if hasSortNode(planSQL(t, cat, "SELECT ax, ay FROM a WHERE ax = 1 ORDER BY ax")) {
		t.Error("ORDER BY a pinned column alone needs no Sort")
	}
}

// TestSplitAggregateSortedUsesGatherMerge pins the run-time error fix: a
// sorted aggregate split by the parallel post-pass sits on a Gather Merge,
// never on a plain Gather (a sorted finalize needs merge-ordered input).
func TestSplitAggregateSortedUsesGatherMerge(t *testing.T) {
	agg := sizedAggFixture(t, 1000, 10, 1, 1)
	agg.Strategy = AggStrategySorted
	fin, ok := splitAggregate(agg, 2).(*Aggregate)
	if !ok {
		t.Fatal("split did not return an aggregate")
	}
	if fin == agg {
		t.Skip("fixture keys not expressible as merge keys")
	}
	if _, isGM := fin.Child.(*GatherMerge); !isGM {
		t.Fatalf("sorted finalize child = %T, want *GatherMerge", fin.Child)
	}
	if fin.Strategy != AggStrategySorted || fin.PartialSource == nil || fin.PartialSource.Strategy != AggStrategySorted {
		t.Error("both halves must stay sorted")
	}
}
