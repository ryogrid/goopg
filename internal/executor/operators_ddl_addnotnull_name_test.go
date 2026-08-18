package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableAddNotNullRepeatSameNameNoOp verifies constraints.sql:627 —
// re-specifying the SAME name via `ADD CONSTRAINT <existing-name> NOT NULL
// col` on a column that already carries that exact not-null constraint is a
// silent no-op (pg_constraint.c:AdjustNotNullInheritance, check 3 only fires
// on a DIFFERING name). M0134-0005n D1.
func TestAlterTableAddNotNullRepeatSameNameNoOp(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_tbl1 (a INTEGER NOT NULL NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ADD CONSTRAINT notnull_tbl1_a_not_null NOT NULL a`); err != nil {
		t.Fatalf("repeat same-name ADD NOT NULL should be a no-op, got error: %v", err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	if !ok {
		t.Fatal("notnull_tbl1 not found")
	}
	if len(tbl.NotNullConstraints) != 1 {
		t.Fatalf("NotNullConstraints = %+v, want exactly 1 (no duplicate row)", tbl.NotNullConstraints)
	}
	if tbl.NotNullConstraints[0].Name != "notnull_tbl1_a_not_null" {
		t.Errorf("NotNullConstraints[0].Name = %q, want %q", tbl.NotNullConstraints[0].Name, "notnull_tbl1_a_not_null")
	}
}

// TestAlterTableAddNotNullDifferingNameRejected verifies D1 check 3 —
// constraints.sql:629 — a differently-named ADD NOT NULL on a column that
// already carries a not-null constraint raises PG's exact 55000 message and
// DETAIL (pg_constraint.c:788-795).
func TestAlterTableAddNotNullDifferingNameRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_tbl1 (a INTEGER NOT NULL NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ADD CONSTRAINT nn NOT NULL a`),
		"55000", `cannot create not-null constraint "nn" on column "a" of table "notnull_tbl1"`)
	if ee.Detail != `A not-null constraint named "notnull_tbl1_a_not_null" already exists for this column.` {
		t.Errorf("Detail = %q, want the exact PG DETAIL", ee.Detail)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	if !ok {
		t.Fatal("notnull_tbl1 not found")
	}
	if len(tbl.NotNullConstraints) != 1 || tbl.NotNullConstraints[0].Name != "notnull_tbl1_a_not_null" {
		t.Errorf("NotNullConstraints after rejected ADD NOT NULL = %+v, want unchanged single row", tbl.NotNullConstraints)
	}
}

// TestAlterTableAddNotNullNoInheritMismatchRejected verifies D1 check 1: a
// NO INHERIT flag differing from the existing constraint's flag raises
// PG's exact 55000 message + HINT (pg_constraint.c:761-767).
func TestAlterTableAddNotNullNoInheritMismatchRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_ni (a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nn_ni ADD CONSTRAINT nn_ni_a_not_null NOT NULL a`); err != nil {
		t.Fatalf("ADD NOT NULL: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE nn_ni ADD CONSTRAINT nn_ni_a_not_null NOT NULL a NO INHERIT`),
		"55000", `cannot change NO INHERIT status of NOT NULL constraint "nn_ni_a_not_null" on relation "nn_ni"`)
	wantHint := "You might need to make the existing constraint inheritable using ALTER TABLE ... ALTER CONSTRAINT ... INHERIT."
	if ee.Hint != wantHint {
		t.Errorf("Hint = %q, want %q", ee.Hint, wantHint)
	}
}

// TestAlterTableAddNotNullNotValidMismatchRejected verifies D1 check 2: the
// existing constraint is NOT VALID but the incoming ADD wants a valid one —
// 55000 + HINT (pg_constraint.c:773-779).
func TestAlterTableAddNotNullNotValidMismatchRejected(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_nv (a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nn_nv ADD CONSTRAINT nn_nv_a_not_null NOT NULL a NOT VALID`); err != nil {
		t.Fatalf("ADD NOT NULL NOT VALID: %v", err)
	}

	ee := requireExecError(t, runDDL(t, ctx, `ALTER TABLE nn_nv ADD CONSTRAINT nn_nv_a_not_null NOT NULL a`),
		"55000", `incompatible NOT VALID constraint "nn_nv_a_not_null" on relation "nn_nv"`)
	wantHint := "You might need to validate it using ALTER TABLE ... VALIDATE CONSTRAINT."
	if ee.Hint != wantHint {
		t.Errorf("Hint = %q, want %q", ee.Hint, wantHint)
	}
}

// TestAlterTableAddColumnDuplicateNotNullNameRejected verifies D2 —
// constraints.sql:632 — ADD COLUMN with an inline CONSTRAINT name already
// used on the relation raises 42710 and adds no column (heap.c:2641-2652).
func TestAlterTableAddColumnDuplicateNotNullNameRejected(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_tbl1 (a INTEGER NOT NULL NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	requireExecError(t, runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ADD COLUMN b INT CONSTRAINT notnull_tbl1_a_not_null NOT NULL`),
		"42710", `constraint "notnull_tbl1_a_not_null" for relation "notnull_tbl1" already exists`)

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1"})
	if !ok {
		t.Fatal("notnull_tbl1 not found")
	}
	for _, c := range tbl.Columns {
		if c.Name == "b" {
			t.Fatalf("rejected ADD COLUMN left a phantom column: %+v", tbl.Columns)
		}
	}
	if len(tbl.NotNullConstraints) != 1 || tbl.NotNullConstraints[0].Name != "notnull_tbl1_a_not_null" {
		t.Errorf("NotNullConstraints after rejected ADD COLUMN = %+v, want unchanged single row", tbl.NotNullConstraints)
	}
}

// TestAlterTableAddColumnNotNullRegistersConstraint verifies criterion 2:
// `ADD COLUMN c INT NOT NULL` with no explicit constraint name on an empty
// table still succeeds and now registers a NamedNotNullConstraint row with
// the synthesised `<table>_<column>_not_null` name — PG registers a
// pg_constraint row for both the named and unnamed inline forms
// (heap.c:AddRelationNewConstraints). Design doc §18.6/§21.5.
func TestAlterTableAddColumnNotNullRegistersConstraint(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_addcol (a int)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE nn_addcol ADD COLUMN c INT NOT NULL`); err != nil {
		t.Fatalf("ADD COLUMN c INT NOT NULL: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "nn_addcol"})
	if !ok {
		t.Fatal("nn_addcol not found")
	}
	found := false
	for _, nc := range tbl.NotNullConstraints {
		if nc.ColName == "c" {
			found = true
			if nc.Name != "nn_addcol_c_not_null" {
				t.Errorf("NotNullConstraints[c].Name = %q, want %q", nc.Name, "nn_addcol_c_not_null")
			}
		}
	}
	if !found {
		t.Fatalf("no NOT NULL constraint recorded for column c: %+v", tbl.NotNullConstraints)
	}
}
