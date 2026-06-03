package executor

import (
	"testing"
)

// TestAlterColumnTypeInt4ToNumeric verifies that ALTER TABLE t ALTER COLUMN col
// TYPE numeric correctly re-encodes existing int4 rows and allows new rows to
// store decimal values. This exercises the heap-rewrite path in execAlterColumnType.
// M0097-0022.
func TestAlterColumnTypeInt4ToNumeric(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		`CREATE TABLE col_type_test (f1 int)`,
		`INSERT INTO col_type_test VALUES (1)`,
		`ALTER TABLE col_type_test ALTER COLUMN f1 TYPE numeric`,
		`INSERT INTO col_type_test VALUES (1.2)`,
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	rows := runQuery(t, ctx, `TABLE col_type_test`)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0][0].Format() != "1" {
		t.Errorf("row 0: want 1, got %q", rows[0][0].Format())
	}
	if rows[1][0].Format() != "1.2" {
		t.Errorf("row 1: want 1.2, got %q", rows[1][0].Format())
	}
}

// TestAlterColumnTypeSameType verifies that ALTER COLUMN TYPE to the same type
// is a no-op and does not error.
func TestAlterColumnTypeSameType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		`CREATE TABLE same_type_test (f1 int)`,
		`INSERT INTO same_type_test VALUES (42)`,
		`ALTER TABLE same_type_test ALTER COLUMN f1 TYPE int`,
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	rows := runQuery(t, ctx, `SELECT f1 FROM same_type_test`)
	if len(rows) != 1 || rows[0][0].Format() != "42" {
		t.Errorf("want 42, got %v", rows)
	}
}
