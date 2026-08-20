package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005at bundles two AlterTableAddCheck fixes ledgered by M0134-0005as
// (7eb8e5a1):
//
// (A) `ALTER TABLE ONLY p ADD CONSTRAINT c CHECK (...)` on a parent that has
// children must be REJECTED with 42P16 "constraint must be added to child
// tables too" — mirroring the ADD COLUMN twin (operators_ddl.go:11053-11054)
// and PG's ATAddCheckNNConstraint (tablecmds.c:9998-10023). Before this fix,
// goopg never read s.Only in the ADD-CHECK case and cascaded anyway.
//
// (B) An anonymous `ALTER TABLE t ADD CHECK (x > 0)` must resolve the SAME
// name/OID for the PARENT's own NamedChecks entry as it does for the
// cascaded child copy. Before this fix, :8759 passed the raw (empty)
// act.ConstraintName to AddCheckFull/allocConstraintOID for the parent,
// leaving Name=="" and OID==0 there.
//
// This file covers (A); (B)'s parent-name assertion lives alongside the
// existing child-name assertion in
// TestCheckAddCascadeAnonymousNameResolved (operators_ddl_check_add_cascade_test.go).

// TestCheckAddOnlyRejectedWithPlainInheritsChild covers acceptance criterion
// 1's first arm: ONLY on a parent with a plain-INHERITS child must reject.
// FAIL-pre: before the fix, this succeeded and cascaded to the child.
func TestCheckAddOnlyRejectedWithPlainInheritsChild(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cao_parent (a int, b int)`,
		`CREATE TABLE cao_child (a int, b int) INHERITS (cao_parent)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE ONLY cao_parent ADD CONSTRAINT b_chk CHECK (b > 0)`)
	wantExecError(t, err, "42P16", "constraint must be added to child tables too")

	// Catalog-clean: neither parent nor child carries the constraint.
	parent, ok := cat.LookupTable(parser.ObjectName{Name: "cao_parent"})
	if !ok {
		t.Fatal("cao_parent not found")
	}
	if len(parent.NamedChecks) != 0 {
		t.Errorf("cao_parent.NamedChecks = %d entries after rejected ONLY ADD, want 0", len(parent.NamedChecks))
	}
	child, ok := cat.LookupTable(parser.ObjectName{Name: "cao_child"})
	if !ok {
		t.Fatal("cao_child not found")
	}
	if len(child.NamedChecks) != 0 {
		t.Errorf("cao_child.NamedChecks = %d entries after rejected ONLY ADD, want 0", len(child.NamedChecks))
	}
}

// TestCheckAddOnlyRejectedWithPartitionChild covers acceptance criterion 1's
// second arm: ONLY on a partitioned parent WITH partitions must also
// reject — hasInheritanceChildren must see partitions, not just plain
// INHERITS children. Ports the upstream regress reject case
// (postgres/src/test/regress/sql/alter_table.sql:2862-2863, list_parted2
// WITH partitions).
func TestCheckAddOnlyRejectedWithPartitionChild(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE cao_ptop (a int, b int) PARTITION BY LIST (a)`,
		`CREATE TABLE cao_part1 PARTITION OF cao_ptop FOR VALUES IN (1,2)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	err := runDDL(t, ctx, `ALTER TABLE ONLY cao_ptop ADD CONSTRAINT b_chk CHECK (b > 0)`)
	wantExecError(t, err, "42P16", "constraint must be added to child tables too")

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "cao_ptop"})
	if !ok {
		t.Fatal("cao_ptop not found")
	}
	if len(parent.NamedChecks) != 0 {
		t.Errorf("cao_ptop.NamedChecks = %d entries after rejected ONLY ADD, want 0", len(parent.NamedChecks))
	}
	part, ok := cat.LookupTable(parser.ObjectName{Name: "cao_part1"})
	if !ok {
		t.Fatal("cao_part1 not found")
	}
	if len(part.NamedChecks) != 0 {
		t.Errorf("cao_part1.NamedChecks = %d entries after rejected ONLY ADD, want 0", len(part.NamedChecks))
	}
}

// TestCheckAddOnlySucceedsWithoutChildren covers acceptance criterion 2:
// ONLY on a childless parent succeeds (parent-only, no cascade needed since
// there is nothing to cascade to) — proves the guard is not over-broad.
// Ports the upstream regress allow case
// (postgres/src/test/regress/sql/alter_table.sql:2873-2877).
func TestCheckAddOnlySucceedsWithoutChildren(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE cao_solo (a int, b int)`); err != nil {
		t.Fatalf("setup cao_solo: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE ONLY cao_solo ADD CONSTRAINT b_chk CHECK (b > 0)`); err != nil {
		t.Fatalf("ALTER TABLE ONLY cao_solo ADD CONSTRAINT b_chk: %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "cao_solo"})
	if !ok {
		t.Fatal("cao_solo not found")
	}
	if len(parent.NamedChecks) != 1 {
		t.Fatalf("cao_solo.NamedChecks = %d entries, want 1", len(parent.NamedChecks))
	}
	if parent.NamedChecks[0].Name != "b_chk" {
		t.Errorf("cao_solo's constraint name = %q, want %q", parent.NamedChecks[0].Name, "b_chk")
	}
}

// TestCheckAddOnlyNoInheritDoesNotError covers acceptance criterion 3: the
// NO INHERIT gate is evaluated BEFORE the ONLY-refusal (PG tablecmds.c
// :10004-10005 precedes :10020-10023), so `ONLY ... NO INHERIT` on a table
// WITH children does not error.
func TestCheckAddOnlyNoInheritDoesNotError(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE caoni_parent (a int, b int)`,
		`CREATE TABLE caoni_child (a int, b int) INHERITS (caoni_parent)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	if err := runDDL(t, ctx, `ALTER TABLE ONLY caoni_parent ADD CONSTRAINT b_chk CHECK (b > 0) NO INHERIT`); err != nil {
		t.Fatalf("ALTER TABLE ONLY caoni_parent ADD CONSTRAINT b_chk NO INHERIT: %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "caoni_parent"})
	if !ok {
		t.Fatal("caoni_parent not found")
	}
	if len(parent.NamedChecks) != 1 {
		t.Fatalf("caoni_parent.NamedChecks = %d entries, want 1", len(parent.NamedChecks))
	}
	child, ok := cat.LookupTable(parser.ObjectName{Name: "caoni_child"})
	if !ok {
		t.Fatal("caoni_child not found")
	}
	if len(child.NamedChecks) != 0 {
		t.Errorf("caoni_child.NamedChecks = %d entries, want 0 (NO INHERIT must not propagate)", len(child.NamedChecks))
	}
}
