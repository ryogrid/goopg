package executor

import (
	"strings"
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

// TestAlterTableDropNotNullOnPrimaryKeyColumnFails pins M0134-0005al: a
// column participating in the table's PRIMARY KEY can never lose its NOT
// NULL via ALTER TABLE ... ALTER COLUMN ... DROP NOT NULL. Mirrors
// dropconstraint_internal's CONSTRAINT_NOTNULL branch
// (postgres/src/backend/commands/tablecmds.c:14128-14159) and
// constraints.out:1072-1074 (notnull_tbl2): 42P16
// ERRCODE_INVALID_TABLE_DEFINITION, message is the column name only (no
// table name), no detail, no hint.
func TestAlterTableDropNotNullOnPrimaryKeyColumnFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_tbl2 (a INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_tbl2: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_tbl2 ALTER a DROP NOT NULL`)
	ee := requireExecError(t, err, "42P16", `column "a" is in a primary key`)
	if ee.Message != `column "a" is in a primary key` {
		t.Fatalf("Message = %q, want exactly %q", ee.Message, `column "a" is in a primary key`)
	}
	if ee.Detail != "" {
		t.Fatalf("Detail = %q, want empty", ee.Detail)
	}
	if ee.Hint != "" {
		t.Fatalf("Hint = %q, want empty", ee.Hint)
	}
}

// TestAlterTableDropNotNullOnPrimaryKeyLeavesCatalogUnchanged verifies the
// rejection is transactionally clean: after the 42P16 error, the column's
// NOT NULL is still enforced (the constraint was NOT cleared before the
// guard fired).
func TestAlterTableDropNotNullOnPrimaryKeyLeavesCatalogUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_pk_unchanged (a INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_pk_unchanged: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_pk_unchanged ALTER a DROP NOT NULL`); err == nil {
		t.Fatal("ALTER TABLE ... DROP NOT NULL over a PK column succeeded, want 42P16")
	}

	err := runDDL(t, ctx, `INSERT INTO notnull_pk_unchanged VALUES (NULL)`)
	if err == nil {
		t.Fatal("INSERT NULL into still-PK column succeeded — DROP NOT NULL guard leaked a catalog mutation")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "23502" {
		t.Fatalf("Code = %q, want 23502 (not_null_violation)", ee.Code)
	}
}

// TestAlterTableDropNotNullOnNonPrimaryKeyColumnSucceeds is the regression
// guard: a NOT NULL, non-PK column on a table that also has a PK must still
// allow DROP NOT NULL.
func TestAlterTableDropNotNullOnPrimaryKeySiblingColumnSucceeds(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_pk_and_plain (a INTEGER PRIMARY KEY, b INTEGER NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_pk_and_plain: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_pk_and_plain ALTER b DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE ... ALTER b DROP NOT NULL: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_pk_and_plain"})
	if !ok {
		t.Fatal("notnull_pk_and_plain not found")
	}
	for _, col := range tbl.Columns {
		if strings.EqualFold(col.Name, "b") && col.NotNull {
			t.Fatalf("column b still NotNull=true after DROP NOT NULL")
		}
	}

	// Confirm the PK column itself is still guarded.
	err := runDDL(t, ctx, `ALTER TABLE notnull_pk_and_plain ALTER a DROP NOT NULL`)
	requireExecError(t, err, "42P16", `column "a" is in a primary key`)
}

// TestAlterTableDropNotNullOnMultiColumnPrimaryKeyRejectsEachMember pins
// acceptance criterion 5: a multi-column PK rejects DROP NOT NULL on EACH
// member column independently.
func TestAlterTableDropNotNullOnPrimaryKeyMultiColumnRejectsEachMember(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_pk_multi (a INTEGER, b INTEGER, PRIMARY KEY (a, b))`); err != nil {
		t.Fatalf("CREATE TABLE notnull_pk_multi: %v", err)
	}

	errA := runDDL(t, ctx, `ALTER TABLE notnull_pk_multi ALTER a DROP NOT NULL`)
	requireExecError(t, errA, "42P16", `column "a" is in a primary key`)

	errB := runDDL(t, ctx, `ALTER TABLE notnull_pk_multi ALTER b DROP NOT NULL`)
	requireExecError(t, errB, "42P16", `column "b" is in a primary key`)
}

// TestAlterTableDropNotNullOnReplicaIdentityIndexColumnFails pins
// M0134-0005am: a column participating in the index chosen as the table's
// replica identity via `ALTER TABLE ... REPLICA IDENTITY USING INDEX` can
// never lose its NOT NULL via `ALTER TABLE ... ALTER COLUMN ... DROP NOT
// NULL`. Mirrors dropconstraint_internal's CONSTRAINT_NOTNULL branch
// (postgres/src/backend/commands/tablecmds.c:14161-14167): 42P16
// ERRCODE_INVALID_TABLE_DEFINITION, message is the column name only (no
// table name), no detail, no hint.
func TestAlterTableDropNotNullOnReplicaIdentityIndexColumnFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_ri (a INTEGER NOT NULL, b INTEGER)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_ri: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE UNIQUE INDEX notnull_ri_uidx ON notnull_ri (a)`); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX notnull_ri_uidx: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_ri REPLICA IDENTITY USING INDEX notnull_ri_uidx`); err != nil {
		t.Fatalf("ALTER TABLE notnull_ri REPLICA IDENTITY USING INDEX notnull_ri_uidx: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_ri ALTER a DROP NOT NULL`)
	ee := requireExecError(t, err, "42P16", `column "a" is in index used as replica identity`)
	if ee.Message != `column "a" is in index used as replica identity` {
		t.Fatalf("Message = %q, want exactly %q", ee.Message, `column "a" is in index used as replica identity`)
	}
	if ee.Detail != "" {
		t.Fatalf("Detail = %q, want empty", ee.Detail)
	}
	if ee.Hint != "" {
		t.Fatalf("Hint = %q, want empty", ee.Hint)
	}
}

// TestAlterTableDropNotNullOnReplicaIdentityIndexLeavesCatalogUnchanged
// mirrors TestAlterTableDropNotNullOnPrimaryKeyLeavesCatalogUnchanged for the
// replica-identity-index refusal: after the 42P16 error, the column's NOT
// NULL is still enforced.
func TestAlterTableDropNotNullOnReplicaIdentityIndexLeavesCatalogUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_ri_unchanged (a INTEGER NOT NULL, b INTEGER)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_ri_unchanged: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE UNIQUE INDEX notnull_ri_unchanged_uidx ON notnull_ri_unchanged (a)`); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX notnull_ri_unchanged_uidx: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_ri_unchanged REPLICA IDENTITY USING INDEX notnull_ri_unchanged_uidx`); err != nil {
		t.Fatalf("ALTER TABLE notnull_ri_unchanged REPLICA IDENTITY USING INDEX notnull_ri_unchanged_uidx: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_ri_unchanged ALTER a DROP NOT NULL`); err == nil {
		t.Fatal("ALTER TABLE ... DROP NOT NULL over a replica-identity-index column succeeded, want 42P16")
	}

	err := runDDL(t, ctx, `INSERT INTO notnull_ri_unchanged VALUES (NULL, 1)`)
	if err == nil {
		t.Fatal("INSERT NULL into still-guarded column succeeded — DROP NOT NULL guard leaked a catalog mutation")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("error is %T, want *ExecError: %v", err, err)
	}
	if ee.Code != "23502" {
		t.Fatalf("Code = %q, want 23502 (not_null_violation)", ee.Code)
	}
}

// TestAlterTableDropNotNullOnIdentityColumnFails pins M0134-0005am: a
// GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY column can never lose its NOT
// NULL via `ALTER TABLE ... ALTER COLUMN ... DROP NOT NULL`. Mirrors
// dropconstraint_internal's CONSTRAINT_NOTNULL branch
// (postgres/src/backend/commands/tablecmds.c:14169-14181): 55000
// ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE, message includes both the column
// name AND the relation name (unlike the PK and replica-identity messages).
func TestAlterTableDropNotNullOnIdentityColumnFails(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_ident (id int GENERATED ALWAYS AS IDENTITY NOT NULL, b INTEGER)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_ident: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_ident ALTER id DROP NOT NULL`)
	ee := requireExecError(t, err, "55000", `column "id" of relation "notnull_ident" is an identity column`)
	if ee.Message != `column "id" of relation "notnull_ident" is an identity column` {
		t.Fatalf("Message = %q, want exactly %q", ee.Message, `column "id" of relation "notnull_ident" is an identity column`)
	}
	if ee.Detail != "" {
		t.Fatalf("Detail = %q, want empty", ee.Detail)
	}
	if ee.Hint != "" {
		t.Fatalf("Hint = %q, want empty", ee.Hint)
	}
}

// TestAlterTableDropNotNullOrderingPKBeforeReplicaIdentityBeforeIdentity pins
// acceptance criterion 2: PG's exact ordering — PK -> replica-identity ->
// identity -> clear. A column that is BOTH the table's PRIMARY KEY and an
// identity column must report the PK error (42P16 "is in a primary key"),
// not the identity error (55000), proving the PK check fires first.
func TestAlterTableDropNotNullOrderingPKBeforeReplicaIdentityBeforeIdentity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_order (id int GENERATED ALWAYS AS IDENTITY NOT NULL PRIMARY KEY, b INTEGER)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_order: %v", err)
	}

	err := runDDL(t, ctx, `ALTER TABLE notnull_order ALTER id DROP NOT NULL`)
	requireExecError(t, err, "42P16", `column "id" is in a primary key`)
}

// TestAlterTableDropNotNullOnReplicaIdentityIndexSiblingColumnSucceeds is the
// regression guard: a NOT NULL, non-replica-identity-member column on a
// table that has a replica-identity index must still allow DROP NOT NULL.
func TestAlterTableDropNotNullOnReplicaIdentityIndexSiblingColumnSucceeds(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE notnull_ri_sibling (a INTEGER NOT NULL, b INTEGER NOT NULL)`); err != nil {
		t.Fatalf("CREATE TABLE notnull_ri_sibling: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE UNIQUE INDEX notnull_ri_sibling_uidx ON notnull_ri_sibling (a)`); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX notnull_ri_sibling_uidx: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_ri_sibling REPLICA IDENTITY USING INDEX notnull_ri_sibling_uidx`); err != nil {
		t.Fatalf("ALTER TABLE notnull_ri_sibling REPLICA IDENTITY USING INDEX notnull_ri_sibling_uidx: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TABLE notnull_ri_sibling ALTER b DROP NOT NULL`); err != nil {
		t.Fatalf("ALTER TABLE notnull_ri_sibling ALTER b DROP NOT NULL: %v", err)
	}

	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "notnull_ri_sibling"})
	if !ok {
		t.Fatal("notnull_ri_sibling not found")
	}
	for _, col := range tbl.Columns {
		if strings.EqualFold(col.Name, "b") && col.NotNull {
			t.Fatalf("column b still NotNull=true after DROP NOT NULL")
		}
	}
}
