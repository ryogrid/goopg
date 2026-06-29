package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// TestExclusionConstraintPartialWhereRoundTrip is the parse→catalog→deparse
// integration twin of TestBuildConstraintDefExclusionWhere (DU-002 slice 310):
// a `CREATE TABLE ... EXCLUDE USING btree (a WITH =) WHERE (b > 0)` must thread
// the partial-index predicate onto the backing catalog index so
// pg_get_constraintdef (and hence pg_dump) re-emit ` WHERE (b > 0)`. Before the
// fix the parser silently dropped the WHERE clause, downgrading the restored
// constraint to one applying to every row.
func TestExclusionConstraintPartialWhereRoundTrip(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE pex (a int, b int,
		CONSTRAINT pex_excl EXCLUDE USING btree (a WITH =) WHERE (b > 0))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("catalog is %T, want *catalog.InMemory", cat)
	}
	idx, ok := im.LookupIndex(parser.ObjectName{Name: "pex_excl"})
	if !ok {
		t.Fatal("EXCLUDE index pex_excl not registered")
	}
	if !idx.IsExclusion {
		t.Errorf("idx.IsExclusion = false, want true")
	}
	if !idx.HasPredicate {
		t.Errorf("idx.HasPredicate = false, want true (partial EXCLUDE WHERE dropped?)")
	}
	if idx.PredicateString != "(b > 0)" {
		t.Errorf("idx.PredicateString = %q, want %q", idx.PredicateString, "(b > 0)")
	}
	if got, want := buildConstraintDefString(idx), "EXCLUDE USING btree (a WITH =) WHERE (b > 0)"; got != want {
		t.Errorf("buildConstraintDefString = %q, want %q", got, want)
	}
}
