package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0134-0005ak: `ALTER TABLE ... ALTER COLUMN ... SET NOT NULL` (the
// already-existing-constraint merge branch) and `ALTER TABLE ... VALIDATE
// CONSTRAINT` both flipped ONLY the named table's own NotValid flag,
// leaving every inheritance/partition descendant's copy at convalidated='f'
// where PG cascades the validation down the whole descendant tree
// (QueueNNConstraintValidation, postgres/src/backend/commands/tablecmds.c:
// 13220-13309). These tests pin both entry points against 3-level plain
// inheritance and a partitioned tree, plus the already-validated-descendant
// no-op, matching constraints.sql:908-917 and :920-939 (census hunks 10+11
// of tmp/regress-diffs/constraints.diff).

// TestAlterTableSetNotNullMergeBranchCascadesValidation covers the SET NOT
// NULL merge branch (act.ColumnName already has a NOT VALID named NOT NULL
// constraint) against a 3-level plain-inheritance tree, mirroring
// constraints.sql:908-917's notnull_inhparent/notnull_inhchild/
// notnull_inhgrand shape exactly.
func TestAlterTableSetNotNullMergeBranchCascadesValidation(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_inhparent (i int)`,
		`CREATE TABLE notnull_inhchild (i int) INHERITS (notnull_inhparent)`,
		`CREATE TABLE notnull_inhgrand () INHERITS (notnull_inhparent, notnull_inhchild)`,
		`ALTER TABLE notnull_inhparent ADD CONSTRAINT nn NOT NULL i NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	child, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_inhchild"})
	if !ok {
		t.Fatal("notnull_inhchild not found")
	}
	grand, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_inhgrand"})
	if !ok {
		t.Fatal("notnull_inhgrand not found")
	}
	if idx := findNotNull(t, child, "i"); !child.NotNullConstraints[idx].NotValid {
		t.Fatalf("precondition: notnull_inhchild's i-constraint already valid before SET NOT NULL")
	}
	if idx := findNotNull(t, grand, "i"); !grand.NotNullConstraints[idx].NotValid {
		t.Fatalf("precondition: notnull_inhgrand's i-constraint already valid before SET NOT NULL")
	}

	if err := runDDL(t, ctx, `ALTER TABLE notnull_inhparent ALTER i SET NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE notnull_inhparent ALTER i SET NOT NULL: %v", err)
	}

	parent, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_inhparent"})
	if !ok {
		t.Fatal("notnull_inhparent not found")
	}
	if idx := findNotNull(t, parent, "i"); parent.NotNullConstraints[idx].NotValid {
		t.Errorf("notnull_inhparent's nn.NotValid = true after SET NOT NULL, want false")
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "notnull_inhchild"})
	if !ok {
		t.Fatal("notnull_inhchild not found after SET NOT NULL")
	}
	if idx := findNotNull(t, child, "i"); child.NotNullConstraints[idx].NotValid {
		t.Errorf("notnull_inhchild's i-constraint.NotValid = true after parent SET NOT NULL, want false (cascade)")
	}

	grand, ok = cat.LookupTable(parser.ObjectName{Name: "notnull_inhgrand"})
	if !ok {
		t.Fatal("notnull_inhgrand not found after SET NOT NULL")
	}
	if idx := findNotNull(t, grand, "i"); grand.NotNullConstraints[idx].NotValid {
		t.Errorf("notnull_inhgrand's i-constraint.NotValid = true after parent SET NOT NULL, want false (2-level cascade)")
	}
}

// TestAlterTableSetNotNullMergeBranchCascadesValidationOnPartitions covers
// the SET NOT NULL merge branch against a partitioned tree, mirroring
// constraints.sql:920-939's notnull_tbl1/notnull_tbl1_1/notnull_tbl1_2/
// notnull_tbl1_3 shape: notnull_tbl1_3 carries its OWN locally-added NOT
// VALID constraint (reached via the merge branch, not the brand-new-add
// branch), so it is the one that needs the cascade fix.
func TestAlterTableSetNotNullMergeBranchCascadesValidationOnPartitions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE notnull_tbl1 (a int, b int) PARTITION BY LIST (a)`,
		`ALTER TABLE notnull_tbl1 ADD CONSTRAINT notnull_con NOT NULL a NOT VALID`,
		`CREATE TABLE notnull_tbl1_1 PARTITION OF notnull_tbl1 FOR VALUES IN (1,2)`,
		`CREATE TABLE notnull_tbl1_2(a int, CONSTRAINT nn2 NOT NULL a, b int)`,
		`ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_2 FOR VALUES IN (3,4)`,
		`CREATE TABLE notnull_tbl1_3(a int, b int)`,
		`ALTER TABLE notnull_tbl1_3 add CONSTRAINT nn3 NOT NULL a NOT VALID`,
		`ALTER TABLE notnull_tbl1 ATTACH PARTITION notnull_tbl1_3 FOR VALUES IN (NULL,5)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	tbl3, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1_3"})
	if !ok {
		t.Fatal("notnull_tbl1_3 not found")
	}
	if idx := findNotNull(t, tbl3, "a"); !tbl3.NotNullConstraints[idx].NotValid {
		t.Fatalf("precondition: notnull_tbl1_3's nn3 already valid before SET NOT NULL")
	}

	// TRUNCATE first so the parent-level SET NOT NULL's own existing-row
	// scan does not fail on notnull_tbl1_3's placeholder row (matching
	// constraints.sql:934's TRUNCATE before the OK case at :937 — this test
	// isolates the cascade behavior, not the scan-failure case already
	// covered by TestAlterTableSetNotNullOnPartitionedParentScansPartitions).
	if err := runDDL(t, ctx, `TRUNCATE notnull_tbl1`); err != nil {
		t.Fatalf("TRUNCATE notnull_tbl1: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TABLE notnull_tbl1 ALTER COLUMN a SET NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE notnull_tbl1 ALTER COLUMN a SET NOT NULL: %v", err)
	}

	tbl3, ok = cat.LookupTable(parser.ObjectName{Name: "notnull_tbl1_3"})
	if !ok {
		t.Fatal("notnull_tbl1_3 not found after SET NOT NULL")
	}
	if idx := findNotNull(t, tbl3, "a"); tbl3.NotNullConstraints[idx].NotValid {
		t.Errorf("notnull_tbl1_3's nn3.NotValid = true after parent SET NOT NULL, want false (partition cascade)")
	}
}

// TestAlterTableValidateConstraintCascadesToDescendants covers the VALIDATE
// CONSTRAINT entry point (the twin call site), against the same 3-level
// plain-inheritance shape as the SET NOT NULL test above, and additionally
// pins the already-validated-descendant no-op (acceptance criterion 3): a
// descendant with convalidated=t before the parent's VALIDATE CONSTRAINT
// must show no InhCount/IsLocal drift.
func TestAlterTableValidateConstraintCascadesToDescendants(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TABLE vnn_parent (i int)`,
		`CREATE TABLE vnn_child (i int) INHERITS (vnn_parent)`,
		`CREATE TABLE vnn_grand () INHERITS (vnn_parent, vnn_child)`,
		`ALTER TABLE vnn_parent ADD CONSTRAINT nn NOT NULL i NOT VALID`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Pre-validate the grandchild's own copy directly, so it enters the
	// cascade already convalidated=t, and record its InhCount/IsLocal for
	// the no-drift assertion.
	if err := runDDL(t, ctx, `ALTER TABLE vnn_grand VALIDATE CONSTRAINT nn`); err != nil {
		t.Fatalf("ALTER TABLE vnn_grand VALIDATE CONSTRAINT nn (pre-validate): %v", err)
	}
	grand, ok := cat.LookupTable(parser.ObjectName{Name: "vnn_grand"})
	if !ok {
		t.Fatal("vnn_grand not found")
	}
	idxG := findNotNull(t, grand, "i")
	if grand.NotNullConstraints[idxG].NotValid {
		t.Fatalf("precondition: vnn_grand's nn still NotValid after direct VALIDATE CONSTRAINT")
	}
	wantInhCount := grand.NotNullConstraints[idxG].InhCount
	wantIsLocal := grand.NotNullConstraints[idxG].IsLocal

	child, ok := cat.LookupTable(parser.ObjectName{Name: "vnn_child"})
	if !ok {
		t.Fatal("vnn_child not found")
	}
	if idx := findNotNull(t, child, "i"); !child.NotNullConstraints[idx].NotValid {
		t.Fatalf("precondition: vnn_child's i-constraint already valid before parent VALIDATE CONSTRAINT")
	}

	if err := runDDL(t, ctx, `ALTER TABLE vnn_parent VALIDATE CONSTRAINT nn`); err != nil {
		t.Fatalf("ALTER TABLE vnn_parent VALIDATE CONSTRAINT nn: %v", err)
	}

	child, ok = cat.LookupTable(parser.ObjectName{Name: "vnn_child"})
	if !ok {
		t.Fatal("vnn_child not found after VALIDATE CONSTRAINT")
	}
	if idx := findNotNull(t, child, "i"); child.NotNullConstraints[idx].NotValid {
		t.Errorf("vnn_child's i-constraint.NotValid = true after parent VALIDATE CONSTRAINT, want false (cascade)")
	}

	grand, ok = cat.LookupTable(parser.ObjectName{Name: "vnn_grand"})
	if !ok {
		t.Fatal("vnn_grand not found after VALIDATE CONSTRAINT")
	}
	idxG = findNotNull(t, grand, "i")
	if grand.NotNullConstraints[idxG].NotValid {
		t.Errorf("vnn_grand's i-constraint.NotValid = true after parent VALIDATE CONSTRAINT, want false")
	}
	if grand.NotNullConstraints[idxG].InhCount != wantInhCount {
		t.Errorf("vnn_grand's i-constraint.InhCount drifted from %d to %d for an already-convalidated descendant",
			wantInhCount, grand.NotNullConstraints[idxG].InhCount)
	}
	if grand.NotNullConstraints[idxG].IsLocal != wantIsLocal {
		t.Errorf("vnn_grand's i-constraint.IsLocal drifted from %v to %v for an already-convalidated descendant",
			wantIsLocal, grand.NotNullConstraints[idxG].IsLocal)
	}
}
