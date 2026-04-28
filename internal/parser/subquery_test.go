package parser

import (
	"testing"
)

// TestParseInExprSubquery pins the TPC-H Q16/Q18/Q20 shape:
// `WHERE x IN (SELECT y FROM t)` parses to InExpr.Subquery.
func TestParseInExprSubquery(t *testing.T) {
	stmts, err := Parse("SELECT id FROM t WHERE val IN (SELECT v FROM s)")
	if err != nil {
		t.Fatal(err)
	}
	in := stmts[0].(*SelectStmt).Where.(*InExpr)
	if in.Negated {
		t.Errorf("Negated=true, want false")
	}
	if in.Subquery == nil {
		t.Errorf("Subquery is nil")
	}
}

// TestParseNotInExprList pins `NOT IN (val, val)` parses to
// InExpr with Negated=true and a populated List.
func TestParseNotInExprList(t *testing.T) {
	stmts, err := Parse("SELECT id FROM t WHERE val NOT IN (1, 2, 3)")
	if err != nil {
		t.Fatal(err)
	}
	in := stmts[0].(*SelectStmt).Where.(*InExpr)
	if !in.Negated {
		t.Errorf("Negated=false, want true")
	}
	if len(in.List) != 3 {
		t.Errorf("List len=%d, want 3", len(in.List))
	}
}

// TestParseExistsExpr pins `WHERE EXISTS (subquery)`.
func TestParseExistsExpr(t *testing.T) {
	stmts, err := Parse("SELECT 1 FROM t WHERE EXISTS (SELECT 1 FROM s)")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stmts[0].(*SelectStmt).Where.(*ExistsExpr); !ok {
		t.Errorf("WHERE=%T want *ExistsExpr", stmts[0].(*SelectStmt).Where)
	}
}

// TestParseCorrelatedExists pins TPC-H Q4 shape: `EXISTS
// (SELECT 1 FROM t WHERE t.col = outer.col …)`. The
// correlated reference is an unresolved ColumnRef at parse
// time — analyzer/planner walks it up the scope chain.
func TestParseCorrelatedExists(t *testing.T) {
	_, err := Parse("SELECT o_orderkey FROM orders o WHERE EXISTS (SELECT 1 FROM lineitem WHERE l_orderkey = o.o_orderkey)")
	if err != nil {
		t.Fatal(err)
	}
}

// TestParseSubqueryExpr pins the TPC-H Q15 shape:
// `WHERE x = (SELECT max(y) FROM t)` parses as a SubqueryExpr
// wrapping the inner SelectStmt verbatim.
func TestParseSubqueryExpr(t *testing.T) {
	stmts, err := Parse("SELECT id FROM t WHERE val = (SELECT max(val) FROM t)")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	bin, ok := sel.Where.(*BinaryOp)
	if !ok {
		t.Fatalf("WHERE=%T want *BinaryOp", sel.Where)
	}
	sq, ok := bin.Right.(*SubqueryExpr)
	if !ok {
		t.Fatalf("RHS=%T want *SubqueryExpr", bin.Right)
	}
	if sq.Inner == nil {
		t.Errorf("Inner is nil")
	}
}
