package analyzer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestInsertValuesSRF pins transformInsertStmt's treatment of a VALUES list
// holding a set-returning function: a single row is a target list, planned
// as INSERT … SELECT <row> so the function expands to rows; a multi-row list
// is a VALUES RTE, where it is "set-returning functions are not allowed in
// VALUES" (0A000). A row with DEFAULT keeps its VALUES form. M0149-0023c.
func TestInsertValuesSRF(t *testing.T) {
	cat := analyzerCatalog(t)
	stmt := parseOne(t, "INSERT INTO pgbench_history (tid, bid) VALUES (generate_series(1, 3), 7)")
	if err := Analyze(stmt, cat); err != nil {
		t.Fatalf("single row: %v", err)
	}
	ins := stmt.(*parser.InsertStmt)
	if ins.Select == nil || ins.Rows != nil || len(ins.Select.Targets) != 2 {
		t.Fatalf("single row not rewritten to INSERT … SELECT: Select=%v Rows=%v", ins.Select, ins.Rows)
	}

	plain := parseOne(t, "INSERT INTO pgbench_history (tid, bid) VALUES (1, 7)")
	if err := Analyze(plain, cat); err != nil {
		t.Fatal(err)
	}
	if p := plain.(*parser.InsertStmt); p.Select != nil || len(p.Rows) != 1 {
		t.Errorf("a row without a set-returning function was rewritten")
	}

	expectAnalyzeCode(t, cat, "INSERT INTO pgbench_history (tid) VALUES (generate_series(1, 3)), (10)", "0A000")
}
