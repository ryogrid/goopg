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
// Runs against the cluster's non-default database handle (r), matching
// p0e5_alter_rollback_undo_test.go's siblings. Originally ran against the
// default database handle (c) instead, because catalog.InMemory.
// DropForeignKeyConstraint hardcoded DefaultDBOid (catalog.go:22241, fixed by
// M0143-0002) and so silently no-opped the DROP on db "r" independent of
// ROLLBACK, making "undone by ROLLBACK" indistinguishable from "never
// actually dropped" — confirmed live before that fix landed (see
// m0143_0002_fk_drop_constraint_nondefault_db_test.go for the dedicated
// non-ROLLBACK regression coverage). Re-verified against db "r" now that
// M0143-0002 is fixed.
func TestPort_M0143_0008b_DropForeignKeyRollbackUndo(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0008b-fk")
	for _, stmt := range []string{
		"CREATE TABLE fk_parent(id int PRIMARY KEY)",
		"CREATE TABLE fk_child(id int, pid int, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES fk_parent(id))",
	} {
		if err := runSQLSimple(t, r, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE fk_child DROP CONSTRAINT child_fk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the FK undo did not fire, child_fk is gone and this INSERT
	// (referencing a non-existent parent id) succeeds instead of failing 23503.
	if err := runSQLSimple(t, r, "INSERT INTO fk_child VALUES (1, 999)"); err == nil {
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
