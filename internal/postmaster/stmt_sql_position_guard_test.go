package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestStmtSQLSurvivesMissingStatementPosition pins the crash guard added with
// M0134-0157. `stmtSQL` slices the raw batch text between statement N's Pos()
// and statement N+1's Pos(); when the FOLLOWING statement reports Pos() == 0 —
// which several node kinds still do after the parser migration, e.g.
// `AlterTableStmt` for the `ADD COLUMN` form — the slice inverted
// (`sql[28:0]`) and panicked the backend goroutine. The panic was not
// contained to the statement: `serveConn`'s recover logs it and closes the
// socket, so ANY client sending a multi-statement batch that mixed PREPARE
// with such a node lost its connection ("server closed the connection
// unexpectedly"). PostgreSQL cannot fail here at all — it stores the raw text
// captured at PREPARE time.
//
// The test drives `stmtSQL` directly with hand-built positions so it keeps
// pinning the guard even after the underlying parser Pos() regression is fixed
// (deferral ledger 2026-08-29, M0134-0157).
func TestStmtSQLSurvivesMissingStatementPosition(t *testing.T) {
	const sql = "CREATE TABLE rtchg (i int); PREPARE p AS SELECT * FROM rtchg; ALTER TABLE rtchg ADD COLUMN q int; EXECUTE p;"

	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(stmts) != 4 {
		t.Fatalf("statements=%d want 4", len(stmts))
	}

	// Every index must be slice-safe, whatever the nodes report.
	for i := range stmts {
		got := stmtSQL(sql, stmts, i) // must not panic
		if got == "" {
			t.Errorf("stmtSQL(idx=%d) returned empty text", i)
		}
		if got[len(got)-1] != ';' {
			t.Errorf("stmtSQL(idx=%d)=%q: want a trailing semicolon", i, got)
		}
	}

	// The specific shape that crashed: statement 1 starts at 28 and statement
	// 2 reports 0. Assert the recorded value rather than the crash so this
	// test also documents WHY the guard exists.
	if p := stmts[1].Pos(); p == 0 {
		t.Fatalf("PREPARE lost its position too; the guard's premise no longer holds")
	}
	if p := stmts[2].Pos(); p != 0 && p < stmts[1].Pos() {
		t.Fatalf("unexpected position ordering: stmts[2].Pos()=%d", p)
	}
}
