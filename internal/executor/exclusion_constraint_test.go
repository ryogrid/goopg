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

// TestExclusionConstraintGistOverlapFires verifies that an EXCLUDE USING gist
// (col WITH &&) constraint on a box column raises 23P01 when an overlapping
// box is inserted. checkGistOverlapExclusion (operators_storage.go) is the
// runtime enforcement path this exercises.
func TestExclusionConstraintGistOverlapFires(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE boxes (b box, EXCLUDE USING gist (b WITH &&))`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO boxes VALUES ('(2,2),(0,0)')`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	// Non-overlapping box: must succeed.
	if err := runDDL(t, ctx, `INSERT INTO boxes VALUES ('(10,10),(8,8)')`); err != nil {
		t.Fatalf("non-overlapping INSERT should succeed: %v", err)
	}
	// Overlapping box: must raise 23P01.
	err := runDDL(t, ctx, `INSERT INTO boxes VALUES ('(3,3),(1,1)')`)
	if err == nil {
		t.Fatal("overlapping box INSERT should have failed with exclusion violation; got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23P01" {
		t.Errorf("Code=%q want 23P01", ee.Code)
	}
}

// TestExclusionConstraintGistOverlapRejectsUnsupportedType verifies that an
// EXCLUDE USING gist (col WITH &&) constraint on a non-box column is rejected
// at DDL time (42704, mirroring PostgreSQL's "data type ... has no default
// operator class for access method" error) rather than being silently
// accepted and then never enforced — checkGistOverlapExclusion only
// understands box values, so accepting any other type here would create a
// constraint whose overlap check silently fails closed on every INSERT.
func TestExclusionConstraintGistOverlapRejectsUnsupportedType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	err := runDDL(t, ctx, `CREATE TABLE ints (i int, EXCLUDE USING gist (i WITH &&))`)
	if err == nil {
		t.Fatal("EXCLUDE USING gist (i WITH &&) on an int column should have been rejected; got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "42704" {
		t.Errorf("Code=%q want 42704", ee.Code)
	}
	if !strings.Contains(ee.Message, "no default operator class") {
		t.Errorf("Message=%q should mention 'no default operator class'", ee.Message)
	}
}

// TestAlterTableAddExcludeRejectsExistingConflict verifies M0134-0005af
// (bucket F9): `ALTER TABLE t ADD EXCLUDE (col WITH =)` over a table that
// already holds two conflicting rows must FAIL with 23P01, mirroring PG's
// build-time check_exclusion_or_unique_constraint newIndex branch
// (execIndexing.c:893-918) — before this fix, execAlterTableAddExclude built
// the backing btree with unique=false, which was also the sole gate on
// collectBTreeEntries' duplicate-row scan, so no existing-row check ever ran
// and the ALTER silently succeeded over conflicting data.
func TestAlterTableAddExcludeRejectsExistingConflict(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t9 (f1 int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t9 VALUES (3)`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t9 VALUES (3)`); err != nil {
		t.Fatalf("second INSERT: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE t9 ADD EXCLUDE (f1 WITH =)`)
	if err == nil {
		t.Fatal("ALTER TABLE ADD EXCLUDE over conflicting existing rows should have failed; got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("want *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "23P01" {
		t.Errorf("Code=%q want 23P01", ee.Code)
	}
	if !strings.Contains(ee.Message, `could not create exclusion constraint "t9_f1_excl"`) {
		t.Errorf("Message=%q want to contain could not create exclusion constraint \"t9_f1_excl\"", ee.Message)
	}
	if ee.Detail != "Key (f1)=(3) conflicts with key (f1)=(3)." {
		t.Errorf("Detail=%q want %q", ee.Detail, "Key (f1)=(3) conflicts with key (f1)=(3).")
	}
}

// TestAlterTableAddExcludeSucceedsOverCleanData is the over-rejection guard
// for TestAlterTableAddExcludeRejectsExistingConflict (M0134-0005af): the
// same ALTER over conflict-free data must still SUCCEED, proving the new
// checkDup duplicate scan does not reject valid data.
func TestAlterTableAddExcludeSucceedsOverCleanData(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE t9b (f1 int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t9b VALUES (1)`); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	if err := runDDL(t, ctx, `INSERT INTO t9b VALUES (2)`); err != nil {
		t.Fatalf("second INSERT: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE t9b ADD EXCLUDE (f1 WITH =)`); err != nil {
		t.Fatalf("ALTER TABLE ADD EXCLUDE over conflict-free rows should have succeeded: %v", err)
	}
}
