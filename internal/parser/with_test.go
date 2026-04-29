package parser

import (
	"strings"
	"testing"
)

// TestParseSimpleWithClause: smoke test for the headline shape.
// `WITH a AS (SELECT 1) SELECT * FROM a` produces a SelectStmt
// with a non-nil With containing one CommonTableExpr named "a".
func TestParseSimpleWithClause(t *testing.T) {
	stmts, err := Parse("WITH a AS (SELECT 1) SELECT * FROM a")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts, want 1", len(stmts))
	}
	sel, ok := stmts[0].(*SelectStmt)
	if !ok {
		t.Fatalf("got %T, want *SelectStmt", stmts[0])
	}
	if sel.With == nil {
		t.Fatal("SelectStmt.With is nil; want non-nil")
	}
	if sel.With.Recursive {
		t.Error("With.Recursive = true, want false (no RECURSIVE keyword)")
	}
	if len(sel.With.CTEs) != 1 {
		t.Fatalf("got %d CTEs, want 1", len(sel.With.CTEs))
	}
	cte := sel.With.CTEs[0]
	if cte.Name != "a" {
		t.Errorf("CTE.Name = %q, want %q", cte.Name, "a")
	}
	if cte.Query == nil {
		t.Error("CTE.Query is nil")
	}
	if len(cte.Columns) != 0 {
		t.Errorf("CTE.Columns = %v, want empty", cte.Columns)
	}
}

// TestParseMultipleCTEs: comma-separated CTE list lands in
// declaration order so analyzer's left-to-right name resolution
// has the right input.
func TestParseMultipleCTEs(t *testing.T) {
	stmts, err := Parse("WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	if len(sel.With.CTEs) != 2 {
		t.Fatalf("got %d CTEs, want 2", len(sel.With.CTEs))
	}
	if sel.With.CTEs[0].Name != "a" || sel.With.CTEs[1].Name != "b" {
		t.Errorf("got names [%s, %s], want [a, b]", sel.With.CTEs[0].Name, sel.With.CTEs[1].Name)
	}
}

// TestParseWithRecursiveAccepted: parser accepts RECURSIVE even
// though execution support lands in 0016-0003. Sets the Recursive
// flag for the planner-level rejection path.
func TestParseWithRecursiveAccepted(t *testing.T) {
	stmts, err := Parse("WITH RECURSIVE r AS (SELECT 1) SELECT * FROM r")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	if !sel.With.Recursive {
		t.Error("With.Recursive = false, want true")
	}
}

// TestParseWithColumnAliases: optional `(col, col, ...)` after the
// CTE name populates Columns in declaration order. Without this,
// the analyzer can't validate CTE output arity.
func TestParseWithColumnAliases(t *testing.T) {
	stmts, err := Parse("WITH a(x, y) AS (SELECT 1, 2) SELECT * FROM a")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sel := stmts[0].(*SelectStmt)
	cte := sel.With.CTEs[0]
	if len(cte.Columns) != 2 || cte.Columns[0] != "x" || cte.Columns[1] != "y" {
		t.Errorf("Columns = %v, want [x, y]", cte.Columns)
	}
}

// TestParseWithRejectsDataModifyingBody pins the Stage A
// restriction: INSERT / UPDATE / DELETE bodies are not supported
// and surface as a parse error pointed at the inner-statement
// keyword, not a generic syntax error.
func TestParseWithRejectsDataModifyingBody(t *testing.T) {
	for _, sql := range []string{
		"WITH a AS (INSERT INTO t VALUES (1)) SELECT * FROM a",
		"WITH a AS (UPDATE t SET x = 1) SELECT * FROM a",
		"WITH a AS (DELETE FROM t) SELECT * FROM a",
	} {
		_, err := Parse(sql)
		if err == nil {
			t.Errorf("Parse(%q) returned nil error; want data-modifying-body rejection", sql)
			continue
		}
		if !strings.Contains(err.Error(), "data-modifying CTE bodies") {
			t.Errorf("Parse(%q) err = %v; want mention of data-modifying CTE bodies", sql, err)
		}
	}
}

// TestParseWithFlowsToInsertUpdateDelete: the With field attaches
// to InsertStmt / UpdateStmt / DeleteStmt — pins the four
// statement shapes the milestone DoD calls out (#1).
func TestParseWithFlowsToInsertUpdateDelete(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		kind string
	}{
		{"insert", "WITH a AS (SELECT 1) INSERT INTO t VALUES (1)", "*parser.InsertStmt"},
		{"update", "WITH a AS (SELECT 1) UPDATE t SET x = 1", "*parser.UpdateStmt"},
		{"delete", "WITH a AS (SELECT 1) DELETE FROM t", "*parser.DeleteStmt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			switch s := stmts[0].(type) {
			case *InsertStmt:
				if s.With == nil || len(s.With.CTEs) != 1 {
					t.Errorf("InsertStmt.With missing or empty: %+v", s.With)
				}
			case *UpdateStmt:
				if s.With == nil || len(s.With.CTEs) != 1 {
					t.Errorf("UpdateStmt.With missing or empty: %+v", s.With)
				}
			case *DeleteStmt:
				if s.With == nil || len(s.With.CTEs) != 1 {
					t.Errorf("DeleteStmt.With missing or empty: %+v", s.With)
				}
			default:
				t.Errorf("got %T, want %s", stmts[0], c.kind)
			}
		})
	}
}

// TestParseWithMissingASRejected pins the syntax-level diagnostic
// for the most common authoring mistake — forgetting AS before the
// parenthesised body.
func TestParseWithMissingASRejected(t *testing.T) {
	_, err := Parse("WITH a (SELECT 1) SELECT * FROM a")
	if err == nil {
		t.Fatal("Parse returned nil error for missing AS")
	}
}

// TestParseWithEmptyListRejected: the parser should not accept
// `WITH SELECT ...` with no CTE declarations.
func TestParseWithEmptyListRejected(t *testing.T) {
	_, err := Parse("WITH SELECT 1")
	if err == nil {
		t.Fatal("Parse returned nil for empty WITH list")
	}
}

// TestParseSelectWithoutCTEUnchanged is the regression guard:
// every existing SELECT shape produces a SelectStmt with
// With == nil. Pre-M0016 callers (planner, executor, tests)
// continue to ignore the field — pin the byte-for-byte invariant.
func TestParseSelectWithoutCTEUnchanged(t *testing.T) {
	stmts, err := Parse("SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*SelectStmt)
	if sel.With != nil {
		t.Errorf("plain SELECT got With=%v, want nil", sel.With)
	}
}
