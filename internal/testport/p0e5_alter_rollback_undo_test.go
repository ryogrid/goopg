package testport

// P0-E5 / M0143-0008 — regression coverage for the LIVE (same session, no
// restart) half of the ADD CONSTRAINT rollback-undo fix.
//
// Before P0-E5, adoptExistingIndexAsConstraint/finishPrimaryKeyConstraint
// (internal/executor/operators_ddl.go) mutated *catalog.Index and
// *catalog.Table fields (IsConstraint/Primary/Deferrable/InitiallyDeferred,
// per-column NotNull, NotNullConstraints) in place, and ProcessRollbackUndos
// (internal/executor/operators_tx.go) had no undo entry type for those
// mutations — only CREATE TABLE/CREATE INDEX (DDLUndoEntry) and DROP TABLE
// inside a savepoint (DDLDropUndoEntry) were undone. So
// `BEGIN; ALTER TABLE t ADD CONSTRAINT ... PRIMARY KEY USING INDEX ...;
// ROLLBACK;` left the constraint permanently applied in the SAME session,
// with no restart involved at all — the bug M0143-0008 was filed for.
//
// P0-E5 added AlterIndexUndoEntry and NotNullUndoEntry (session.go),
// recorded by adoptExistingIndexAsConstraint/finishPrimaryKeyConstraint and
// consumed by ProcessRollbackUndos, so both effects revert on ROLLBACK.
//
// The restart-only half (catalogRowLive/scanCatalogHeapRows discarding a
// non-zero-Xmax row unconditionally) is covered separately by
// p0e4_catalog_xmax_loss_test.go — this file never restarts the server.

import "testing"

// TestPort_P0E5AlterAddConstraintRollbackUndoesIndexFields re-attempts the
// SAME `ADD CONSTRAINT ... PRIMARY KEY USING INDEX` after a ROLLBACK: it can
// only succeed a second time if the first attempt's `idx.IsConstraint`/
// `idx.Primary` mutation was actually undone (adoptExistingIndexAsConstraint
// itself refuses to adopt an index that is "already associated with a
// constraint", SQLSTATE 55000 — the exact symptom M0143-0008 described).
func TestPort_P0E5AlterAddConstraintRollbackUndoesIndexFields(t *testing.T) {
	_, r := startP0E4Cluster(t, "p0e5-idxundo")
	p0e4CreateTable(t, r)

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the undo did not fire, idx.IsConstraint is still true and this
	// second attempt fails with 55000 "already associated with a
	// constraint" (adoptExistingIndexAsConstraint, operators_ddl.go).
	res, err = r.PSQL("-c", "ALTER TABLE t ADD CONSTRAINT t_pk PRIMARY KEY USING INDEX t_pk;")
	if err != nil {
		t.Fatalf("launch psql re-ADD CONSTRAINT: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("re-ADD CONSTRAINT after ROLLBACK failed (exit %d, stderr=%s) — "+
			"the ROLLBACKed ALTER's idx.IsConstraint/idx.Primary mutation was not undone "+
			"(M0143-0008 symptom: ROLLBACK reports success but the catalog mutation sticks)",
			res.ExitCode, res.Stderr)
	}
}

// TestPort_P0E5AlterAddConstraintRollbackUndoesNotNullSynthesis exercises the
// PRIMARY KEY NOT-NULL-synthesis half (finishPrimaryKeyConstraint): a column
// that was nullable before the ALTER must accept NULL again after ROLLBACK.
func TestPort_P0E5AlterAddConstraintRollbackUndoesNotNullSynthesis(t *testing.T) {
	_, r := startP0E4Cluster(t, "p0e5-notnullundo")
	for _, stmt := range []string{
		"CREATE TABLE t2(id int, val int)",
		"CREATE UNIQUE INDEX t2_pk ON t2(id)",
	} {
		if err := runSQLSimple(t, r, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	res, err := r.PSQL("-c", "BEGIN; ALTER TABLE t2 ADD CONSTRAINT t2_pk PRIMARY KEY USING INDEX t2_pk; ROLLBACK;")
	if err != nil {
		t.Fatalf("launch psql BEGIN/ALTER/ROLLBACK: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql BEGIN/ALTER/ROLLBACK exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	// If the NOT NULL synthesis undo did not fire, id is still NOT NULL and
	// this INSERT (which omits id, so it defaults to NULL) fails with 23502.
	if err := runSQLSimple(t, r, "INSERT INTO t2(val) VALUES (1)"); err != nil {
		t.Fatalf("INSERT of a NULL id after ROLLBACK failed: %v — "+
			"the ROLLBACKed ALTER's PRIMARY KEY NOT-NULL synthesis (col.NotNull / "+
			"tbl.NotNullConstraints) was not undone (M0143-0008)", err)
	}
}
