package executor

import (
	"testing"
)

// TestAddNotNull{NoInheritMismatch,IncompatibleNotValid,DuplicateConstraintName}Pos
// and TestAddColumnNotNullDuplicateConstraintNamePos pin M0134-0005w Part A:
// the three AdjustNotNullInheritance checks
// (postgres/src/backend/catalog/pg_constraint.c:759-795, reached via
// ATAddCheckNNConstraint's ADD CONSTRAINT ... NOT NULL path) and the sibling
// ConstraintNameIsUsed check in AddRelationNewConstraints
// (postgres/src/backend/catalog/heap.c:2645-2652, reached via ADD COLUMN ...
// CONSTRAINT name NOT NULL) are all bare `ereport`s with no `errposition()`
// call, so goopg must leave ExecError.Pos at 0 rather than stamping
// act.Pos() (which would render a spurious `LINE N:`/`^` caret block that PG
// never emits). Mirrors the §28 convention established for
// mergeNotNullOnAttach (commit fa84e214).

func TestAddNotNullNoInheritMismatchPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE errpos_noinh (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE errpos_noinh ADD CONSTRAINT nn NOT NULL a NO INHERIT`); err != nil {
		t.Fatalf("ADD CONSTRAINT ... NOT NULL ... NO INHERIT: %v", err)
	}

	// Re-adding without NO INHERIT flips connoinherit and must be rejected.
	err := runDDL(t, ctx, `ALTER TABLE errpos_noinh ADD CONSTRAINT nn NOT NULL a`)
	if err == nil {
		t.Fatal("ADD CONSTRAINT ... NOT NULL a (dropping NO INHERIT) succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle pg_constraint.c:759-767)", ee.Pos)
	}
}

func TestAddNotNullIncompatibleNotValidPos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE errpos_notvalid (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE errpos_notvalid ADD CONSTRAINT nn NOT NULL a NOT VALID`); err != nil {
		t.Fatalf("ADD CONSTRAINT ... NOT NULL ... NOT VALID: %v", err)
	}

	// Re-requesting a validated constraint over an existing NOT VALID one is
	// incompatible.
	err := runDDL(t, ctx, `ALTER TABLE errpos_notvalid ADD CONSTRAINT nn NOT NULL a`)
	if err == nil {
		t.Fatal("ADD CONSTRAINT ... NOT NULL a (validated) over an existing NOT VALID constraint succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle pg_constraint.c:770-779)", ee.Pos)
	}
}

func TestAddNotNullDuplicateConstraintNamePos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE errpos_dupname (a int, b int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE errpos_dupname ADD CONSTRAINT nn NOT NULL a`); err != nil {
		t.Fatalf("ADD CONSTRAINT nn NOT NULL a: %v", err)
	}

	// Same column, same shape, but an explicit different name — rejected as
	// "already exists for this column".
	err := runDDL(t, ctx, `ALTER TABLE errpos_dupname ADD CONSTRAINT nn2 NOT NULL a`)
	if err == nil {
		t.Fatal("ADD CONSTRAINT nn2 NOT NULL a over an existing differently-named NOT NULL succeeded, want 55000")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "55000" {
		t.Fatalf("Code = %q, want 55000", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle pg_constraint.c:788-795)", ee.Pos)
	}
}

func TestAddColumnNotNullDuplicateConstraintNamePos(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE errpos_addcol_dup (a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE errpos_addcol_dup ADD CONSTRAINT dupnn NOT NULL a`); err != nil {
		t.Fatalf("ADD CONSTRAINT dupnn NOT NULL a: %v", err)
	}

	// ADD COLUMN ... CONSTRAINT dupnn NOT NULL reuses an already-used
	// constraint name on this relation.
	err := runDDL(t, ctx, `ALTER TABLE errpos_addcol_dup ADD COLUMN c int CONSTRAINT dupnn NOT NULL`)
	if err == nil {
		t.Fatal("ADD COLUMN ... CONSTRAINT dupnn NOT NULL with a name already in use succeeded, want 42710")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "42710" {
		t.Fatalf("Code = %q, want 42710", ee.Code)
	}
	if ee.Pos != 0 {
		t.Fatalf("Pos = %d, want 0 (no errposition in PG oracle heap.c:2645-2652)", ee.Pos)
	}
}
