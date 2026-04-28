package parser

import (
	"testing"
)

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
