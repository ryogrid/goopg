package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// M0134-0005l: COPY FROM wrote rows straight to the heap, scattering the
// listed columns and leaving everything else NULL, then calling the raw
// heap writer directly — it never applied column DEFAULTs, never enforced
// NOT NULL, never evaluated CHECK constraints, and never checked domain
// constraints. PostgreSQL calls ExecConstraints unconditionally for every
// COPY-inserted row (postgres/src/backend/commands/copyfrom.c:1352-1358)
// and fills defaults for every column absent from the column list
// (defmap/defexprs, ~1545-1833). These tests drive CopyFromExecutor
// directly (the same entry point the wire layer uses) against tables with
// a CHECK constraint, a DEFAULT, and a NOT NULL column, mirroring
// constraints.sql's COPY_TBL fixture (postgres/src/test/regress/sql/constraints.sql:255-267).

// TestCopyFromCheckConstraintViolationRejected — COPY_TBL twin. A row that
// violates the table's CHECK constraint must be rejected with 23514,
// matching constraints.out's "violates check constraint \"copy_con\"".
// FAIL before this change (row was written silently); PASS after.
func TestCopyFromCheckConstraintViolationRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE copy_con_t (x int, y text, z int,
		CONSTRAINT copy_con_t_check CHECK (x > 3 AND y <> 'check failed' AND x < 7))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "copy_con_t"})
	if !ok {
		t.Fatal("copy_con_t not found")
	}

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0, 1, 2},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	// x=4 passes (3<4<7), y is not 'check failed'.
	if err := cf.PushLine([]byte("4\t!check failed\t5")); err != nil {
		t.Fatalf("valid row rejected: %v", err)
	}
	// x=7 fails (x<7 is false) and y='check failed' also fails.
	err = cf.PushLine([]byte("7\tcheck failed\t6"))
	if err == nil {
		t.Fatal("expected CHECK violation, got nil error")
	}
	xe, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T want *ExecError", err)
	}
	if xe.Code != "23514" {
		t.Errorf("code=%q want 23514 (%v)", xe.Code, err)
	}
	if cf.RowsInserted() != 1 {
		t.Errorf("RowsInserted=%d want 1 (violating row must not be stored)", cf.RowsInserted())
	}
}

// TestCopyFromDefaultFilledForOmittedColumn — a column absent from an
// explicit COPY column list must get the column's DEFAULT, not NULL.
// FAIL before this change (row stored NULL); PASS after.
func TestCopyFromDefaultFilledForOmittedColumn(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE copy_default_t (a int, b int DEFAULT 42)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "copy_default_t"})
	if !ok {
		t.Fatal("copy_default_t not found")
	}

	// Explicit column list omits b, matching a `COPY t (a) FROM ...` shape.
	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := cf.PushLine([]byte("99")); err != nil {
		t.Fatal(err)
	}

	rows := runSQL(t, ctx, `SELECT a, b FROM copy_default_t`)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if rows[0][0].Int != 99 {
		t.Errorf("a: got %v want 99", rows[0][0].Int)
	}
	if rows[0][1].IsNull() {
		t.Fatal("b: got NULL want 42 — DEFAULT not applied by COPY")
	}
	if rows[0][1].Int != 42 {
		t.Errorf("b: got %v want 42", rows[0][1].Int)
	}
}

// TestCopyFromNotNullViolationRejected — COPY must enforce NOT NULL the
// same way INSERT does. FAIL before this change (NULL silently stored);
// PASS after.
func TestCopyFromNotNullViolationRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE copy_notnull_t (a int, b int NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "copy_notnull_t"})
	if !ok {
		t.Fatal("copy_notnull_t not found")
	}

	// Column list omits b (no DEFAULT for it), so it stays NULL and must
	// trip the NOT NULL check.
	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	err = cf.PushLine([]byte("1"))
	if err == nil {
		t.Fatal("expected NOT NULL violation, got nil error")
	}
	xe, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("err type=%T want *ExecError", err)
	}
	if xe.Code != "23502" {
		t.Errorf("code=%q want 23502 (%v)", xe.Code, err)
	}
	if cf.RowsInserted() != 0 {
		t.Errorf("RowsInserted=%d want 0", cf.RowsInserted())
	}
}

// TestCopyFromConstraintFreeTableFastPath — a table with no DEFAULTs, no
// NOT NULL columns, no CHECK constraints, and no domain-typed columns must
// still COPY cleanly through the fast path that skips the whole
// default/constraint sequence. Guards the performance-guard hoist: this
// must PASS both before and after the change (it is not itself a
// regression test for the bug), but exercises the exact fast-path branch
// the brief requires.
func TestCopyFromConstraintFreeTableFastPath(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE copy_plain_t (a int, b text)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "copy_plain_t"})
	if !ok {
		t.Fatal("copy_plain_t not found")
	}

	plan := &optimizer.Copy{
		Direction:   optimizer.CopyFrom,
		Table:       tbl,
		ColumnIndex: []int{0, 1},
		Endpoint:    optimizer.CopyEndpointStdin,
	}
	cf, err := NewCopyFromExecutor(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if cf.needsConstraints {
		t.Error("needsConstraints=true for a constraint-free table — fast path not selected")
	}
	if err := cf.PushLine([]byte("1\thello")); err != nil {
		t.Fatal(err)
	}
	if err := cf.PushLine([]byte("2\tworld")); err != nil {
		t.Fatal(err)
	}
	if cf.RowsInserted() != 2 {
		t.Errorf("RowsInserted=%d want 2", cf.RowsInserted())
	}
}
