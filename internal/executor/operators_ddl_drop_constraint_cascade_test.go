package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestAlterTableOnlyDropConstraintWalksChildren pins the fix for
// M0134-0005ah slice B (ledger row M0134-0005ae / M0134-0005ag): `ALTER
// TABLE ONLY parent DROP CONSTRAINT c` must still walk inheritance/partition
// children to decrement coninhcount and flip conislocal — it must NOT skip
// the child walk entirely. Mirrors constraints.sql:801-811's
// notnull_tbl5/notnull_tbl5_child (plain INHERITS) case for a CHECK
// constraint. postgres/src/backend/commands/tablecmds.c:14300-14343
// dropconstraint_internal.
func TestAlterTableOnlyDropConstraintWalksChildren(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE cc_parent (a int, CONSTRAINT ann CHECK (a > 0))`); err != nil {
		t.Fatalf("CREATE TABLE cc_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE cc_child () INHERITS (cc_parent)`); err != nil {
		t.Fatalf("CREATE TABLE cc_child: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "cc_child"})
	if !ok {
		t.Fatal("cc_child not found")
	}
	idx := findNamedCheck(t, child, "ann")
	if child.NamedChecks[idx].InhCount != 1 || child.NamedChecks[idx].IsLocal {
		t.Fatalf("precondition: cc_child.ann InhCount/IsLocal = %d/%v, want 1/false",
			child.NamedChecks[idx].InhCount, child.NamedChecks[idx].IsLocal)
	}

	if err := runDDL(t, ctx, `ALTER TABLE ONLY cc_parent DROP CONSTRAINT ann`); err != nil {
		t.Fatalf("ALTER TABLE ONLY cc_parent DROP CONSTRAINT ann: %v", err)
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "cc_child"})
	if !ok {
		t.Fatal("cc_child not found after DROP CONSTRAINT")
	}
	idx = findNamedCheck(t, child, "ann")
	if child.NamedChecks[idx].InhCount != 0 {
		t.Errorf("cc_child.ann InhCount = %d, want 0 (parent's copy is gone, ONLY must still decrement)",
			child.NamedChecks[idx].InhCount)
	}
	if !child.NamedChecks[idx].IsLocal {
		t.Errorf("cc_child.ann IsLocal = false, want true (orphaned by ONLY drop, becomes local)")
	}
}

// TestAlterTableOnlyAlterColumnDropNotNullWalksChildren is the NOT NULL
// column-name-path twin (AlterTableDropNotNull) — previously had ZERO
// cascade logic at all. Mirrors constraints.sql:801-805's
// notnull_tbl5/notnull_tbl5_child case exactly (plain INHERITS, drop by
// column name).
func TestAlterTableOnlyAlterColumnDropNotNullWalksChildren(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_parent (a int CONSTRAINT ann NOT NULL, b int CONSTRAINT bnn NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE nn_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nn_child () INHERITS (nn_parent)`); err != nil {
		t.Fatalf("CREATE TABLE nn_child: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "nn_child"})
	if !ok {
		t.Fatal("nn_child not found")
	}
	idxB := findNotNull(t, child, "b")
	if child.NotNullConstraints[idxB].InhCount != 1 || child.NotNullConstraints[idxB].IsLocal {
		t.Fatalf("precondition: nn_child's b-constraint InhCount/IsLocal = %d/%v, want 1/false",
			child.NotNullConstraints[idxB].InhCount, child.NotNullConstraints[idxB].IsLocal)
	}

	if err := runDDL(t, ctx, `ALTER TABLE ONLY nn_parent ALTER b DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE ONLY nn_parent ALTER b DROP NOT NULL: %v", err)
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "nn_child"})
	if !ok {
		t.Fatal("nn_child not found after DROP NOT NULL")
	}
	idxB = findNotNull(t, child, "b")
	if child.NotNullConstraints[idxB].InhCount != 0 {
		t.Errorf("nn_child's b-constraint InhCount = %d, want 0", child.NotNullConstraints[idxB].InhCount)
	}
	if !child.NotNullConstraints[idxB].IsLocal {
		t.Errorf("nn_child's b-constraint IsLocal = false, want true (orphaned by ONLY drop)")
	}
}

// TestAlterTableOnlyDropConstraintByNameWalksChildren covers the
// constraint-name path (execAlterTableDropConstraint's NOT NULL branch,
// "3.7") — `ALTER TABLE ONLY ... DROP CONSTRAINT <name>` where the resolved
// constraint is a NOT NULL.
func TestAlterTableOnlyDropConstraintByNameWalksChildren(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn2_parent (a int CONSTRAINT ann NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE nn2_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nn2_child () INHERITS (nn2_parent)`); err != nil {
		t.Fatalf("CREATE TABLE nn2_child: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE ONLY nn2_parent DROP CONSTRAINT ann`); err != nil {
		t.Fatalf("ALTER TABLE ONLY nn2_parent DROP CONSTRAINT ann: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "nn2_child"})
	if !ok {
		t.Fatal("nn2_child not found")
	}
	idx := findNotNull(t, child, "a")
	if child.NotNullConstraints[idx].InhCount != 0 {
		t.Errorf("nn2_child's a-constraint InhCount = %d, want 0", child.NotNullConstraints[idx].InhCount)
	}
	if !child.NotNullConstraints[idx].IsLocal {
		t.Errorf("nn2_child's a-constraint IsLocal = false, want true")
	}
}

// TestAlterTableRecursingDropConstraintDeletesChildRow is the mandatory
// over-fix guard (brief acceptance criterion 3): the NON-ONLY (recursing)
// path must still delete the child's own copy outright — it must NOT be
// downgraded to a decrement-only walk by this fix.
func TestAlterTableRecursingDropConstraintDeletesChildRow(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE rc_parent (a int, CONSTRAINT ann CHECK (a > 0))`); err != nil {
		t.Fatalf("CREATE TABLE rc_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE rc_child () INHERITS (rc_parent)`); err != nil {
		t.Fatalf("CREATE TABLE rc_child: %v", err)
	}

	// No ONLY — recurse=true — the child's inherited copy must be deleted
	// entirely (coninhcount was 1, not-local), not just decremented.
	if err := runDDL(t, ctx, `ALTER TABLE rc_parent DROP CONSTRAINT ann`); err != nil {
		t.Fatalf("ALTER TABLE rc_parent DROP CONSTRAINT ann: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "rc_child"})
	if !ok {
		t.Fatal("rc_child not found")
	}
	for _, nc := range child.NamedChecks {
		if nc.Name == "ann" {
			t.Fatalf("rc_child still has constraint %q after recursing DROP CONSTRAINT — should have been deleted, got %+v", nc.Name, nc)
		}
	}
}

// TestAlterTableRecursingAlterColumnDropNotNullClearsChild is the NOT NULL
// column-path twin of the over-fix guard above.
func TestAlterTableRecursingAlterColumnDropNotNullClearsChild(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE rcn_parent (a int CONSTRAINT ann NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE rcn_parent: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE rcn_child () INHERITS (rcn_parent)`); err != nil {
		t.Fatalf("CREATE TABLE rcn_child: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE rcn_parent ALTER a DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE rcn_parent ALTER a DROP NOT NULL: %v", err)
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "rcn_child"})
	if !ok {
		t.Fatal("rcn_child not found")
	}
	for _, nc := range child.NotNullConstraints {
		if nc.ColName == "a" {
			t.Fatalf("rcn_child still has a not-null constraint on %q after recursing DROP NOT NULL — should have been cleared, got %+v", nc.ColName, nc)
		}
	}
	for i := range child.Columns {
		if child.Columns[i].Name == "a" && child.Columns[i].NotNull {
			t.Fatalf("rcn_child.a still has NotNull=true after recursing DROP NOT NULL")
		}
	}
}

// TestAlterTableOnlyDropConstraintMultiLevelInheritance covers a
// grandparent → parent → child chain: `ALTER TABLE ONLY parent DROP
// CONSTRAINT` must decrement the parent's own copy (removed from parent
// entirely by the direct-target branch) and the CHILD's copy — since parent
// still has its OWN inherited copy from grandparent removed (the direct
// target's row is deleted unconditionally regardless of recurse — only the
// CASCADE to further descendants is gated by recurse). The grandchild here
// is nn_gc, inheriting only from nn_mid (not from nn_gp directly), so the
// walk is exactly one level from the ONLY target (nn_mid) to its own child
// (nn_gc) — this does not exercise multi-level RECURSIVE cascade (that
// requires deleting an orphaned child's row under `recurse`, tested above);
// it exercises that the ONLY-mode decrement walk works when the dropped
// constraint's own coninhcount is itself >0 relative to a further-up
// ancestor. Per the oracle (dropconstraint_internal, tablecmds.c:14024-14030)
// the "cannot drop inherited constraint" 42P16 guard fires whenever the
// TARGET's own coninhcount>0 and the call is not itself a recursive
// (cascading) call — so a mid-level ONLY DROP on a constraint inherited from
// a grandparent is REFUSED, matching notnull_tbl5's flat (non-multi-level)
// shape in constraints.sql. This test pins that refusal rather than
// asserting a further multi-level cascade the oracle does not exercise here.
func TestAlterTableOnlyDropConstraintMultiLevelInheritance(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE nn_gp (a int CONSTRAINT ann NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE nn_gp: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nn_mid () INHERITS (nn_gp)`); err != nil {
		t.Fatalf("CREATE TABLE nn_mid: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE nn_gc () INHERITS (nn_mid)`); err != nil {
		t.Fatalf("CREATE TABLE nn_gc: %v", err)
	}

	// nn_mid's own copy of "ann" is inherited (coninhcount=1, not local) —
	// dropping it via ONLY on nn_mid itself must be refused with 42P16,
	// exactly like a direct inherited-constraint drop (the top-level guard
	// applies to the TARGET table regardless of ONLY/recurse).
	requireExecError(t, runDDL(t, ctx, `ALTER TABLE ONLY nn_mid DROP CONSTRAINT ann`),
		"42P16", `cannot drop inherited constraint "ann" of relation "nn_mid"`)
}

func findNamedCheck(t *testing.T, tbl *catalog.Table, name string) int {
	t.Helper()
	for i, nc := range tbl.NamedChecks {
		if nc.Name == name {
			return i
		}
	}
	t.Fatalf("table %q has no NamedChecks entry %q: %+v", tbl.Name, name, tbl.NamedChecks)
	return -1
}

func findNotNull(t *testing.T, tbl *catalog.Table, colName string) int {
	t.Helper()
	for i, nc := range tbl.NotNullConstraints {
		if nc.ColName == colName {
			return i
		}
	}
	t.Fatalf("table %q has no NotNullConstraints entry for column %q: %+v", tbl.Name, colName, tbl.NotNullConstraints)
	return -1
}
