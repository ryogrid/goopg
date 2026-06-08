package executor

import (
	"strings"
	"testing"
)

// TestExclusionConstraintBtreeEqualityFires verifies that an EXCLUDE USING btree
// (col WITH =) constraint raises 23P01 when a duplicate key is inserted.
func TestExclusionConstraintBtreeEqualityFires(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE tbl (c1 int, c2 int, c3 int, c4 int,
		EXCLUDE USING btree (c1 WITH =) INCLUDE(c3,c4))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// First insert should succeed.
	if err := runDDL(t, ctx, `INSERT INTO tbl VALUES (1, 2, 30, 40)`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}

	// Second insert with same c1=1 should fail with 23P01.
	err := runDDL(t, ctx, `INSERT INTO tbl VALUES (1, 20, 300, 400)`)
	if err == nil {
		t.Fatal("second INSERT with c1=1 should have failed with exclusion violation; got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23P01" {
		t.Errorf("Code=%q want 23P01", ee.Code)
	}
	if !strings.Contains(ee.Message, "exclusion constraint") {
		t.Errorf("Message=%q should mention 'exclusion constraint'", ee.Message)
	}
}
