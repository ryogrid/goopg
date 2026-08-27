package parser

import (
    "testing"
)

func TestSortByUsingDescFlag(t *testing.T) {
    stmts, err := Parse("SELECT 1 ORDER BY 1 USING >")
    if err != nil {
        t.Fatalf("Parse error: %v", err)
    }
    if len(stmts) != 1 {
        t.Fatalf("Expected 1 stmt, got %d", len(stmts))
    }
    sel, ok := stmts[0].(*SelectStmt)
    if !ok {
        t.Fatalf("Expected SelectStmt, got %T", stmts[0])
    }
    if len(sel.OrderBy) != 1 {
        t.Fatalf("Expected 1 sort item, got %d", len(sel.OrderBy))
    }
    sb := sel.OrderBy[0]
    if sb.UsingOp != ">" {
        t.Errorf("Expected UsingOp '>', got %q", sb.UsingOp)
    }
    if !sb.Desc {
        t.Errorf("Expected Desc=true for '>' operator")
    }
    t.Logf("SortBy: Desc=%v UsingOp=%q", sb.Desc, sb.UsingOp)
}
