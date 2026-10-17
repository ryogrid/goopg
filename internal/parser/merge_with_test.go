package parser

import "testing"

// TestParseWithMerge pins M0146-0085: gram.y's MergeStmt takes an
// opt_with_clause, so a WITH clause may lead MERGE; the statement is anchored
// at the MERGE keyword (as `WITH … DELETE` is at DELETE).
func TestParseWithMerge(t *testing.T) {
	sql := "WITH s AS (SELECT 8 AS a) MERGE INTO t USING s ON t.a = s.a WHEN MATCHED THEN UPDATE SET a = 80"
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, ok := stmts[0].(*MergeStmt)
	if !ok {
		t.Fatalf("got %T, want *MergeStmt", stmts[0])
	}
	if m.With == nil || len(m.With.CTEs) != 1 || m.With.CTEs[0].Name != "s" {
		t.Fatalf("With = %+v, want one CTE s", m.With)
	}
	if m.Pos() != 26 {
		t.Errorf("Pos = %d, want 26 (the MERGE keyword)", m.Pos())
	}
	if m.Target.Name != "t" || m.Source.Name != "s" || len(m.Clauses) != 1 {
		t.Errorf("target %q source %q clauses %d", m.Target.Name, m.Source.Name, len(m.Clauses))
	}
}
