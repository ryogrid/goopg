package parser

import (
	"testing"
)

// TestParseExplainWrapsInner pins that EXPLAIN parses as a
// statement wrapping the inner-statement AST verbatim.
func TestParseExplainWrapsInner(t *testing.T) {
	stmts, err := Parse("EXPLAIN SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	ex, ok := stmts[0].(*ExplainStmt)
	if !ok {
		t.Fatalf("stmt type=%T want *ExplainStmt", stmts[0])
	}
	if _, ok := ex.Inner.(*SelectStmt); !ok {
		t.Errorf("inner=%T want *SelectStmt", ex.Inner)
	}
}
