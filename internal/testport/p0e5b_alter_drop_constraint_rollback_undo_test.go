package testport

// M0143-0008b — regression coverage for the DROP-direction rollback-undo of
// `ALTER TABLE ... DROP CONSTRAINT` on the three constraint kinds P0-E5 left
// out: CHECK, FOREIGN KEY, and NOT NULL.
//
// P0-E5 (M0143-0008) added rollback-undo for ADD CONSTRAINT ... {PRIMARY
// KEY|UNIQUE} USING INDEX and for DROP CONSTRAINT's three index-backed forms
// (PK/UNIQUE/EXCLUDE, via the existing DDLDropUndoEntry/RestoreIndex
// map-removal mechanism). The CHECK/FOREIGN KEY/NOT NULL branches of
// execAlterTableDropConstraint (internal/executor/operators_ddl.go) instead
// mutate fields/slices directly on a live *catalog.Table pointer
// (tbl.CheckConstraints/NamedChecks/ForeignKeys/NotNullConstraints,
// tbl.Columns[i].NotNull) — a shape DDLDropUndoEntry cannot undo — so before
// this fix `BEGIN; ALTER TABLE t DROP CONSTRAINT <check|fk|notnull>;
// ROLLBACK;` reported success but left the constraint permanently dropped,
// same session, no restart involved.
//
// DropConstraintUndoEntry (session.go) now snapshots a table's whole
// CHECK/FK/NOT NULL constraint state before each of these three mutations
// and ProcessRollbackUndos (operators_tx.go) restores it verbatim on
// ROLLBACK.

import "testing"

// TestPort_M0143_0008b_DropCheckConstraintRollbackUndo verifies a ROLLBACKed
// `DROP CONSTRAINT` on a named CHECK constraint restores enforcement: an
// INSERT violating the check must still fail after the ROLLBACK.
func TestPort_M0143_0008b_DropCheckConstraintRollbackUndo(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0008b-check")
	if err := runSQLSimple(t, r, "CREATE TABLE ck_t(id int, val int, CONSTRAINT val_chk CHECK (val > 0))"); err != nil {
		t.Fatalf("setup CREATE TABLE: %v", err)
	}

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE ck_t DROP CONSTRAINT val_chk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the CHECK undo did not fire, val_chk is gone and this INSERT
	// (which violates val > 0) succeeds instead of failing 23514.
	if err := runSQLSimple(t, r, "INSERT INTO ck_t VALUES (1, -1)"); err == nil {
		t.Fatalf("INSERT violating the CHECK succeeded after ROLLBACK — " +
			"the ROLLBACKed ALTER TABLE DROP CONSTRAINT's CheckConstraints/NamedChecks " +
			"mutation was not undone (M0143-0008b)")
	}
}

// TestPort_M0143_0008b_DropForeignKeyRollbackUndo verifies a ROLLBACKed
// `DROP CONSTRAINT` on a named FOREIGN KEY restores enforcement: an INSERT
// referencing a non-existent parent row must still fail after the ROLLBACK.
//
// Deliberately runs against the cluster's DEFAULT database handle (c, not
// r): catalog.InMemory.DropForeignKeyConstraint hardcodes DefaultDBOid
// (catalog.go:22241), a separate, already-filed bug (M0143-0002 —
// "ALTER TABLE ... DROP CONSTRAINT on an FK reports success and does
// nothing" on a non-default database) that execAlterTableDropConstraint's
// FK branch does not check the return value of, so on a non-default DB
// (r, as p0e5_alter_rollback_undo_test.go's siblings use) the DROP silently
// no-ops and this test could not tell "undone by ROLLBACK" apart from
// "never actually dropped" — confirmed live before writing this test
// (COMMITted, non-ROLLBACKed DROP CONSTRAINT on db "r" left FK enforcement
// active; the identical statement against the default db correctly
// disabled it). Once M0143-0002 is fixed this test should be re-verified
// against db "r" too.
func TestPort_M0143_0008b_DropForeignKeyRollbackUndo(t *testing.T) {
	c, _ := startP0E4Cluster(t, "m0143-0008b-fk")
	for _, stmt := range []string{
		"CREATE TABLE fk_parent(id int PRIMARY KEY)",
		"CREATE TABLE fk_child(id int, pid int, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES fk_parent(id))",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	res, err := c.PSQL("-c", "BEGIN; ALTER TABLE fk_child DROP CONSTRAINT child_fk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the FK undo did not fire, child_fk is gone and this INSERT
	// (referencing a non-existent parent id) succeeds instead of failing 23503.
	if err := runSQLSimple(t, c, "INSERT INTO fk_child VALUES (1, 999)"); err == nil {
		t.Fatalf("INSERT with a dangling FK reference succeeded after ROLLBACK — " +
			"the ROLLBACKed ALTER TABLE DROP CONSTRAINT's ForeignKeys mutation " +
			"was not undone (M0143-0008b)")
	}
}

// TestPort_M0143_0008b_DropNotNullConstraintRollbackUndo verifies a
// ROLLBACKed `DROP CONSTRAINT` on a named NOT NULL constraint restores
// enforcement: an INSERT omitting the column (defaulting to NULL) must
// still fail after the ROLLBACK.
func TestPort_M0143_0008b_DropNotNullConstraintRollbackUndo(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0008b-notnull")
	for _, stmt := range []string{
		"CREATE TABLE nn_t(id int, val int)",
		"ALTER TABLE nn_t ADD CONSTRAINT val_nn NOT NULL val",
	} {
		if err := runSQLSimple(t, r, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE nn_t DROP CONSTRAINT val_nn; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the NOT NULL undo did not fire, val is nullable again and this
	// INSERT (which omits val, so it defaults to NULL) succeeds instead of
	// failing 23502.
	if err := runSQLSimple(t, r, "INSERT INTO nn_t(id) VALUES (1)"); err == nil {
		t.Fatalf("INSERT of a NULL val succeeded after ROLLBACK — " +
			"the ROLLBACKed ALTER TABLE DROP CONSTRAINT's NotNullConstraints/" +
			"Columns[i].NotNull mutation was not undone (M0143-0008b)")
	}
}
