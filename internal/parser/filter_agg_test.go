package parser

import (
	"testing"
)

func TestParseFilterWithAggregate(t *testing.T) {
	sql := `select max(unique1) filter (where sum(ten) > 0) from tenk1`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("Expected 1 stmt, got %d", len(stmts))
	}
	sel, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("Expected *SelectStmt, got %T", stmts[0])
	}
	if sel.Where != nil {
		t.Errorf("sel.Where should be nil but got: %T %v", sel.Where, sel.Where)
	}
	if len(sel.Targets) != 1 {
		t.Fatalf("Expected 1 target, got %d", len(sel.Targets))
	}
	fc, ok := sel.Targets[0].Expr.(*FuncCall)
	if !ok {
		t.Fatalf("Expected *FuncCall, got %T", sel.Targets[0].Expr)
	}
	t.Logf("FuncCall: %q, Filter: %v", fc.Name, fc.Filter)
	if fc.Filter == nil {
		t.Error("FuncCall.Filter should not be nil - FILTER (WHERE sum(ten) > 0) not parsed")
	}
}
