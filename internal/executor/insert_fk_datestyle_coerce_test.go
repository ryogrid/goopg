package executor

import (
	"strings"
	"testing"
)

// TestInsertCoercesDateLiteralBeforeFKCheck pins the M-NIGHTLY 2026-07-15
// follow-up gap discovered while verifying fkValsForDetail's DateStyle fix:
// insertOp.Next only coerced int2/int4/int8 columns before running FK/CHECK/
// domain constraint checks, so a DATE/TIMESTAMP/NUMERIC column stayed a raw
// KindString literal at constraint-check time. A FK violation on the
// INSERT-side check (assertParentExists via checkFKInsert) rendered the
// DETAIL line with the un-reformatted literal instead of honoring `SET
// datestyle`, unlike the already-fixed DELETE/UPDATE-parent-side and
// partition-detach checks (which operate on already-typed, storage-decoded
// Datums). Fix: extend insertOp.Next's coercion switch to also cover
// date/timestamp/timestamptz/numeric via the same evalCast pattern used for
// the integer cases.
func TestInsertCoercesDateLiteralBeforeFKCheck(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "datestyle" {
			return "German", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, "CREATE TABLE parent (d date PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE parent: %v", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4, d date REFERENCES parent(d))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO parent VALUES ('2026-07-14')"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}

	err := runDDL(t, ctx, "INSERT INTO child VALUES (1, '2026-07-15')")
	if err == nil {
		t.Fatal("INSERT INTO child with a non-existent FK target unexpectedly succeeded")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if execErr.Code != "23503" {
		t.Fatalf("expected 23503, got %q: %v", execErr.Code, execErr)
	}
	// Before the fix: "Key (d)=(2026-07-15) is not present ...", ignoring
	// the German (DD.MM.YYYY) datestyle. After the fix, must render
	// "15.07.2026" — and, since the coerced value must be tagged as a DATE
	// (not a bare timestamp), must NOT carry a spurious "00:00:00" suffix
	// (regression for the sibling evalCast "date"-case TimeSubDate bug this
	// same loop uncovered and fixed).
	if !strings.Contains(execErr.Detail, "(15.07.2026)") {
		t.Errorf("Detail = %q, want it to contain \"(15.07.2026)\" (German DateStyle, date-only)", execErr.Detail)
	}
	if strings.Contains(execErr.Detail, "00:00:00") {
		t.Errorf("Detail = %q, DATE value wrongly rendered with a time-of-day suffix", execErr.Detail)
	}
}

// TestInsertCoercesNumericLiteralBeforeCheckConstraint covers the sibling
// NUMERIC case from the same gap: a raw string literal for a numeric column
// must be coerced (and validated) before CHECK constraints run, not merely
// at storage-encode time.
func TestInsertCoercesNumericLiteralBeforeCheckConstraint(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE numtest (n numeric CHECK (n > 0))"); err != nil {
		t.Fatalf("CREATE TABLE numtest: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO numtest VALUES ('not_a_number')"); err == nil {
		t.Fatal("INSERT with an invalid numeric literal unexpectedly succeeded")
	} else if execErr, ok := err.(*ExecError); !ok || execErr.Code != "22P02" {
		t.Fatalf("expected 22P02, got %T: %v", err, err)
	}
	if err := runDDL(t, ctx, "INSERT INTO numtest VALUES ('-5')"); err == nil {
		t.Fatal("INSERT violating the CHECK constraint unexpectedly succeeded")
	} else if execErr, ok := err.(*ExecError); !ok || execErr.Code != "23514" {
		t.Fatalf("expected 23514, got %T: %v", err, err)
	}
}
