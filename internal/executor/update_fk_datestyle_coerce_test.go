package executor

import (
	"strings"
	"testing"
)

// TestUpdateCoercesDateLiteralBeforeFKCheck pins the UPDATE-side half of the
// M-NIGHTLY 2026-07-15 follow-up: insertOp.Next's coercion switch was
// extended to date/timestamp/timestamptz/numeric, but every UPDATE new-row
// construction site (updateViaIndex, updateOp.Next, updateWithFrom) built
// newRow straight from evalExpr(Set[i], ...) with no column-type coercion at
// all — an UPDATE ... SET on a DATE column left the FK-violation DETAIL line
// rendering the raw un-reformatted literal, ignoring `SET datestyle`, unlike
// the now-fixed INSERT path. This UPDATE has an equality WHERE on the PK, so
// it plans through updateViaIndex — the coerceRowForConstraintChecks call in
// its RangeScan callback is what's under test.
func TestUpdateCoercesDateLiteralBeforeFKCheck(t *testing.T) {
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
	if err := runDDL(t, ctx, "CREATE TABLE child (id int4 PRIMARY KEY, d date REFERENCES parent(d))"); err != nil {
		t.Fatalf("CREATE TABLE child: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO parent VALUES ('2026-07-14')"); err != nil {
		t.Fatalf("INSERT INTO parent: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO child VALUES (1, '2026-07-14')"); err != nil {
		t.Fatalf("INSERT INTO child: %v", err)
	}

	err := runDDL(t, ctx, "UPDATE child SET d = '2026-07-15' WHERE id = 1")
	if err == nil {
		t.Fatal("UPDATE child to a non-existent FK target unexpectedly succeeded")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if execErr.Code != "23503" {
		t.Fatalf("expected 23503, got %q: %v", execErr.Code, execErr)
	}
	if !strings.Contains(execErr.Detail, "(15.07.2026)") {
		t.Errorf("Detail = %q, want it to contain \"(15.07.2026)\" (German DateStyle, date-only)", execErr.Detail)
	}
	if strings.Contains(execErr.Detail, "00:00:00") {
		t.Errorf("Detail = %q, DATE value wrongly rendered with a time-of-day suffix", execErr.Detail)
	}
}

// TestUpdateCoercesNumericLiteralBeforeCheckConstraint covers the sibling
// NUMERIC case: a raw string literal assigned via SET must be coerced (and
// validated) before CHECK constraints run, not merely at storage-encode
// time — mirrors TestInsertCoercesNumericLiteralBeforeCheckConstraint for
// the UPDATE path.
func TestUpdateCoercesNumericLiteralBeforeCheckConstraint(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE numtest (id int4 PRIMARY KEY, n numeric CHECK (n > 0))"); err != nil {
		t.Fatalf("CREATE TABLE numtest: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO numtest VALUES (1, 5)"); err != nil {
		t.Fatalf("INSERT INTO numtest: %v", err)
	}

	if err := runDDL(t, ctx, "UPDATE numtest SET n = 'not_a_number' WHERE id = 1"); err == nil {
		t.Fatal("UPDATE with an invalid numeric literal unexpectedly succeeded")
	} else if execErr, ok := err.(*ExecError); !ok || execErr.Code != "22P02" {
		t.Fatalf("expected 22P02, got %T: %v", err, err)
	}
	if err := runDDL(t, ctx, "UPDATE numtest SET n = '-5' WHERE id = 1"); err == nil {
		t.Fatal("UPDATE violating the CHECK constraint unexpectedly succeeded")
	} else if execErr, ok := err.(*ExecError); !ok || execErr.Code != "23514" {
		t.Fatalf("expected 23514, got %T: %v", err, err)
	}
}

// TestUpdateCoercesInt4RangeOverflow is a regression guard for the int4/int8
// arm of coerceRowForConstraintChecks on the UPDATE path (mirrors INSERT's
// existing int-range coverage). The heap encoder already independently
// range-checks a fixed-width int4 column at write time, so this case did not
// actually reproduce the "silent overflow" the deferral ledger worried about
// — but the new coercion call now raises the same 22003 earlier
// (constraint-check time, matching insertOp.Next), so this pins that both
// layers agree rather than relying on the encoder alone.
func TestUpdateCoercesInt4RangeOverflow(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE inttest (id int4 PRIMARY KEY, v int4)"); err != nil {
		t.Fatalf("CREATE TABLE inttest: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO inttest VALUES (1, 5)"); err != nil {
		t.Fatalf("INSERT INTO inttest: %v", err)
	}

	err := runDDL(t, ctx, "UPDATE inttest SET v = 5000000000 WHERE id = 1")
	if err == nil {
		t.Fatal("UPDATE with an out-of-range int4 literal unexpectedly succeeded")
	}
	execErr, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if execErr.Code != "22003" {
		t.Fatalf("expected 22003, got %q: %v", execErr.Code, execErr)
	}
}
