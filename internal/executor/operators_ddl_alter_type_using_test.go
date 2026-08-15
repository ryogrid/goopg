package executor

// operators_ddl_alter_type_using_test.go pins M0134-0002 C2 slice 5:
// `ALTER TABLE t ALTER [COLUMN] c TYPE <type> USING <expr>`. Before the slice
// the parser left USING unconsumed (`syntax error at or near ... (got using)`),
// and execAlterColumnType silently swallowed per-row cast errors and truncated
// the heap before re-encoding (the C10 data-loss). These tests exercise the
// USING success paths, the two PG coercion-failure messages + hints, and the
// row-count-preserved-on-failure invariant.

import (
	"errors"
	"strings"
	"testing"
)

// TestAlterColumnTypeUsingSuccess covers the USING success forms: a literal,
// a row-reference expression, a cast to the target type, and a multi-branch
// CASE expression. The text→text CASE case also pins the name-unchanged no-op
// bypass (PG still rewrites when USING is given even if the target type name is
// unchanged).
func TestAlterColumnTypeUsingSuccess(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// (a) USING literal + row-reference expression.
	if err := runDDL(t, ctx, "CREATE TABLE ta (a int, b int)"); err != nil {
		t.Fatalf("CREATE TABLE ta: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO ta VALUES (1, 10), (2, 20), (3, 30)"); err != nil {
		t.Fatalf("INSERT ta: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE ta ALTER COLUMN b TYPE bigint USING b + 100"); err != nil {
		t.Fatalf("ALTER b TYPE bigint USING b+100: %v", err)
	}
	rows := runQueryRows(t, ctx, "SELECT b::text FROM ta ORDER BY a")
	want := []string{"110", "120", "130"}
	for i, w := range want {
		if i >= len(rows) || rows[i][0].StringValue() != w {
			t.Errorf("USING b+100 row %d: got %+v want text %q", i, rows[i][0], w)
		}
	}
	// Unchanged column a must still be intact.
	rows = runQueryRows(t, ctx, "SELECT a::text FROM ta ORDER BY a")
	for i, w := range []string{"1", "2", "3"} {
		if i >= len(rows) || rows[i][0].StringValue() != w {
			t.Errorf("SELECT a row %d: got %+v want %q", i, rows[i][0], w)
		}
	}

	// (b) USING cast to target type: b is now bigint; re-cast to numeric via
	// b::numeric and change the column to numeric.
	if err := runDDL(t, ctx, "ALTER TABLE ta ALTER COLUMN b TYPE numeric USING b::numeric"); err != nil {
		t.Fatalf("ALTER b TYPE numeric USING b::numeric: %v", err)
	}
	rows = runQueryRows(t, ctx, "SELECT b FROM ta ORDER BY a")
	for i, w := range want {
		if i >= len(rows) || rows[i][0].Kind != KindNumeric || numericText(rows[i][0]) != w {
			t.Errorf("USING b::numeric row %d: got %+v want numeric %q", i, rows[i][0], w)
		}
	}

	// (c) Multi-branch CASE; the label column is text→text so only the USING
	// clause forces the rewrite (name-unchanged no-op bypass).
	if err := runDDL(t, ctx, "CREATE TABLE tc (id int, flag bool, label text)"); err != nil {
		t.Fatalf("CREATE TABLE tc: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO tc VALUES (1, true, 'x'), (2, false, 'y'), (3, NULL, 'z')"); err != nil {
		t.Fatalf("INSERT tc: %v", err)
	}
	if err := runDDL(t, ctx, "ALTER TABLE tc ALTER COLUMN label TYPE text USING CASE WHEN flag THEN 'IT WAS TRUE' WHEN NOT flag THEN 'IT WAS FALSE' ELSE 'IT WAS NULL!' END"); err != nil {
		t.Fatalf("ALTER label TYPE text USING CASE: %v", err)
	}
	rows = runQueryRows(t, ctx, "SELECT label::text FROM tc ORDER BY id")
	want = []string{"IT WAS TRUE", "IT WAS FALSE", "IT WAS NULL!"}
	for i, w := range want {
		if i >= len(rows) || rows[i][0].StringValue() != w {
			t.Errorf("USING CASE row %d: got %+v want %q", i, rows[i][0], w)
		}
	}
}

// TestAlterColumnTypeCoercionFailure covers both PG coercion-failure messages
// (with and without USING), the hints, SQLSTATE 42804, and the
// row-count-preserved-on-failure invariant (no heap truncation on error).
func TestAlterColumnTypeCoercionFailure(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// (a) WITH USING: a bool column cast to integer has no assignment cast.
	// evalCast(KindBool, integer) fails, so the error is mapped to PG's
	// "result of USING clause ..." message (tablecmds.c:14505-14510).
	if err := runDDL(t, ctx, "CREATE TABLE td1 (c bool)"); err != nil {
		t.Fatalf("CREATE TABLE td1: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO td1 VALUES (true), (false)"); err != nil {
		t.Fatalf("INSERT td1: %v", err)
	}
	err := runDDL(t, ctx, "ALTER TABLE td1 ALTER COLUMN c TYPE integer USING c")
	ee := assertExecError(t, err, `result of USING clause for column "c" cannot be cast automatically to type integer`)
	if ee.Hint != "You might need to add an explicit cast." {
		t.Errorf("WITH USING hint = %q, want %q", ee.Hint, "You might need to add an explicit cast.")
	}
	if ee.Code != "42804" {
		t.Errorf("WITH USING code = %q, want 42804", ee.Code)
	}
	// Table must be intact after the failed rewrite.
	rows := runQueryRows(t, ctx, "SELECT count(*) FROM td1")
	if rows[0][0].Int != 2 {
		t.Errorf("td1 row count after failed ALTER = %d, want 2 (table truncated?)", rows[0][0].Int)
	}
	rows = runQueryRows(t, ctx, "SELECT c::text FROM td1 ORDER BY c")
	if len(rows) != 2 || rows[0][0].StringValue() != "false" || rows[1][0].StringValue() != "true" {
		t.Errorf("td1 values after failed ALTER = %+v, want [false, true]", rows)
	}

	// (b) WITHOUT USING: text 'bb' cast to integer. The rewrite-time coercion
	// fails, mapped to PG's "column ... cannot be cast automatically" message
	// + the "USING c::integer" hint (tablecmds.c:14506-14511).
	if err := runDDL(t, ctx, "CREATE TABLE td2 (c text)"); err != nil {
		t.Fatalf("CREATE TABLE td2: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO td2 VALUES ('bb')"); err != nil {
		t.Fatalf("INSERT td2: %v", err)
	}
	err = runDDL(t, ctx, "ALTER TABLE td2 ALTER COLUMN c TYPE integer")
	ee = assertExecError(t, err, `column "c" cannot be cast automatically to type integer`)
	if ee.Hint != `You might need to specify "USING c::integer".` {
		t.Errorf("WITHOUT USING hint = %q, want %q", ee.Hint, `You might need to specify "USING c::integer".`)
	}
	if ee.Code != "42804" {
		t.Errorf("WITHOUT USING code = %q, want 42804", ee.Code)
	}
	rows = runQueryRows(t, ctx, "SELECT count(*) FROM td2")
	if rows[0][0].Int != 1 {
		t.Errorf("td2 row count after failed ALTER = %d, want 1 (table truncated?)", rows[0][0].Int)
	}
	rows = runQueryRows(t, ctx, "SELECT c::text FROM td2")
	if len(rows) != 1 || rows[0][0].StringValue() != "bb" {
		t.Errorf("td2 values after failed ALTER = %+v, want [bb] (data loss?)", rows)
	}
}

// assertExecError unwraps an expected *ExecError, checks the message and code,
// and returns it for further field assertions.
func assertExecError(t *testing.T, err error, wantMessage string) *ExecError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", wantMessage)
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if !strings.Contains(ee.Message, wantMessage) {
		t.Errorf("message = %q, want containing %q", ee.Message, wantMessage)
	}
	return ee
}
