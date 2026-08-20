package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableAddNotNullCascadeNoInheritMismatchRejected verifies E1 —
// constraints.sql:707-717 — cascading an inheritable NOT NULL constraint
// down onto a child that already carries a NO INHERIT not-null constraint
// of the same name must be rejected with the same 55000 mismatch PG raises
// for a direct-target conflict (pg_constraint.c:741-767), except the
// relation named in the error is the DESCENDANT holding the conflicting
// constraint, not the ALTER target — PG's ATAddCheckNNConstraint
// (tablecmds.c:10012-10043) recurses into children and each level re-runs
// AdjustNotNullInheritance. M0134-0005ad E1.
func TestAlterTableAddNotNullCascadeNoInheritMismatchRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE ATACC2 (a int, CONSTRAINT a_is_not_null NOT NULL a NO INHERIT)`); err != nil {
		t.Fatalf("CREATE TABLE ATACC2: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE ATACC1 (a int)`); err != nil {
		t.Fatalf("CREATE TABLE ATACC1: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE ATACC3 (a int) INHERITS (ATACC2)`); err != nil {
		t.Fatalf("CREATE TABLE ATACC3: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE ATACC2 INHERIT ATACC1`); err != nil {
		t.Fatalf("ALTER TABLE ATACC2 INHERIT ATACC1: %v", err)
	}

	// Can't override: ATACC1's cascade recurses down into ATACC2, which
	// already holds a NO INHERIT "a_is_not_null" constraint on "a" — the
	// error must name atacc2, not atacc1.
	requireExecError(t, runDDL(t, ctx, `ALTER TABLE ATACC1 ADD CONSTRAINT ditto NOT NULL a`),
		"55000", `cannot change NO INHERIT status of NOT NULL constraint "a_is_not_null" on relation "atacc2"`)

	// Dropping the NO INHERIT constraint allows this to work — the earlier
	// rejected cascade must not have bumped a_is_not_null's InhCount, or
	// this DROP would be wrongly refused as "cannot drop inherited
	// constraint".
	if err := runDDL(t, ctx, `ALTER TABLE ATACC2 DROP CONSTRAINT a_is_not_null`); err != nil {
		t.Fatalf("DROP CONSTRAINT a_is_not_null should succeed: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE ATACC1 ADD CONSTRAINT ditto NOT NULL a`); err != nil {
		t.Fatalf("ADD CONSTRAINT ditto should now succeed: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "atacc2"})
	if !ok {
		t.Fatal("atacc2 not found")
	}
	found := false
	for _, nc := range tbl.NotNullConstraints {
		if nc.ColName == "a" {
			found = true
			if nc.Name != "ditto" {
				t.Errorf("atacc2's not-null constraint on a = %q, want %q (cascaded from atacc1)", nc.Name, "ditto")
			}
		}
	}
	if !found {
		t.Fatalf("atacc2 has no not-null constraint on a after successful cascade: %+v", tbl.NotNullConstraints)
	}
}

// TestCreateTableDuplicateExplicitNotNullNameRejected verifies E2 —
// constraints.sql:719 — two explicitly-named NOT NULL constraints on
// different columns within the same CREATE TABLE, sharing a name, raise
// PG's ConstraintNameIsUsed 42710 (pg_constraint.c:412; heap.c:2645-2652).
func TestCreateTableDuplicateExplicitNotNullNameRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	requireExecError(t, runDDL(t, ctx,
		`CREATE TABLE notnull_tbl2 (a INTEGER CONSTRAINT blah NOT NULL, b INTEGER CONSTRAINT blah NOT NULL)`),
		"42710", `constraint "blah" for relation "notnull_tbl2" already exists`)

	if _, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl2"}); ok {
		t.Fatal("rejected CREATE TABLE left a phantom relation notnull_tbl2")
	}
}
