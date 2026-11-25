package executor

import (
	"errors"
	"testing"
)

// TestTypedTableRepeatedWithOptions pins M0148-0001j4 item (3b): PG's
// MergeAttributes merges the first `col WITH OPTIONS …` entry into the
// typed table's column from the type and raises 42701 "column \"name\"
// specified more than once" for a second entry on the same column (regress
// typed_table persons4); a single entry per column still works, and an
// unknown column stays 42703.
func TestTypedTableRepeatedWithOptions(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	if err := runDDL(t, ctx, "CREATE TYPE person_type AS (id int, name text)"); err != nil {
		t.Fatal(err)
	}
	var ee *ExecError
	err := runDDL(t, ctx, "CREATE TABLE persons4 OF person_type (name WITH OPTIONS NOT NULL, name WITH OPTIONS DEFAULT '')")
	if !errors.As(err, &ee) || ee.Code != "42701" || ee.Message != `column "name" specified more than once` {
		t.Fatalf("repeated WITH OPTIONS: err = %v, want 42701 column \"name\" specified more than once", err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE persons3 OF person_type (PRIMARY KEY (id), name WITH OPTIONS DEFAULT '')"); err != nil {
		t.Fatalf("single WITH OPTIONS per column: %v", err)
	}
	err = runDDL(t, ctx, "CREATE TABLE personsx OF person_type (myname WITH OPTIONS NOT NULL)")
	if !errors.As(err, &ee) || ee.Code != "42703" {
		t.Fatalf("unknown column: err = %v, want 42703", err)
	}
}
