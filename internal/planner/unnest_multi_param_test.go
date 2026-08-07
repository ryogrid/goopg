package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestUnnestMultiParamCorrelation (M0054-0008) covers the Q20-shape
// correlated scalar subquery where the WHERE has TWO equi-conjuncts
// `inner.A = outer.X AND inner.B = outer.Y`. The unnest pass must
// hoist the correlated SUM into a GROUP BY (A, B) aggregate and
// bind BOTH per-pair equalities into the resulting join — not just
// the first pair (the prior-implementation bug that would have
// caused incorrect sums for Q20).
func TestUnnestMultiParamCorrelation(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "partsupp"}, []catalog.Column{
		{Name: "ps_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "ps_availqty", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}
	q := `SELECT ps_partkey, ps_suppkey FROM partsupp
	      WHERE ps_availqty >
	            (SELECT sum(l_quantity) FROM lineitem
	             WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey)`
	stmt := parseOne(t, q)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// After unnesting, no SubqueryExpr should remain anywhere in
	// the plan (the helper hasSubqueryExpr is defined in
	// bushy_test.go).
	if hasSubqueryExpr(node) {
		t.Fatalf("plan still contains SubqueryExpr after multi-param unnest; tree:\n%s", describePlanTreeForUnnest(node))
	}
	// Find the produced Join — the unnest pass replaces the
	// Filter(SubqueryExpr) with Filter(Join(outer, Aggregate)).
	j := findFirstJoin(node)
	if j == nil {
		t.Fatalf("no Join found after multi-param unnest; tree:\n%s", describePlanTreeForUnnest(node))
	}
	// Verify the Join.Predicate is an AND chain of TWO equalities,
	// matching both correlation params (l_partkey = ps_partkey AND
	// l_suppkey = ps_suppkey).
	conjuncts := splitAnd(j.Predicate)
	if len(conjuncts) != 2 {
		t.Fatalf("expected 2 join conjuncts (multi-param correlation), got %d", len(conjuncts))
	}
	for _, c := range conjuncts {
		bop, ok := c.(*BinaryOp)
		if !ok || bop.Op != parser.OpEq {
			t.Errorf("expected binary `=` conjunct, got %T %v", c, c)
		}
	}
}

// describePlanTreeForUnnest is a tiny formatter for diagnostic
// output in the test failure path.
func describePlanTreeForUnnest(n Node) string {
	if n == nil {
		return "nil"
	}
	switch x := n.(type) {
	case *Project:
		return "Project(" + describePlanTreeForUnnest(x.Child) + ")"
	case *Filter:
		return "Filter(" + describePlanTreeForUnnest(x.Child) + ")"
	case *Join:
		return "Join{" + describePlanTreeForUnnest(x.Left) + ", " + describePlanTreeForUnnest(x.Right) + "}"
	case *Aggregate:
		return "Agg(" + describePlanTreeForUnnest(x.Child) + ")"
	case *SeqScan:
		return "Seq(" + x.Table.Name + ")"
	}
	return "?"
}

// hasSubqueryExpr reports whether any SubqueryExpr remains in the plan.
// (It lived in bushy_test.go until M0127-P6.3 deleted that file with the old
// subset-bitmask DP; this test was the helper's only remaining user.)
func hasSubqueryExpr(node Node) bool {
	return findSubqueryInPlanRecursive(node)
}

func findSubqueryInPlanRecursive(node Node) bool {
	if node == nil {
		return false
	}
	return findSubqueryInPlan(node)
}
