package testport

// M0143-0002 — `ALTER TABLE ... DROP CONSTRAINT` on a FOREIGN KEY reported
// success and did nothing when the table lived in a non-default database.
//
// catalog.InMemory.DropForeignKeyConstraint(tableOID, name) re-resolved the
// table by OID via tableByOID(tableOID, DefaultDBOid) — hardcoded to the
// default database's namespace — instead of using the *catalog.Table pointer
// the caller (execAlterTableDropConstraint, internal/executor/operators_ddl.go)
// already had in hand. For a table in any other database that lookup failed
// (ok=false), so the method returned immediately without touching the real
// tbl.ForeignKeys slice, and the caller discarded the bool result — so a
// plain COMMITted DROP CONSTRAINT on such an FK left it permanently enforced,
// independent of ROLLBACK. Live-confirmed while writing
// p0e5b_alter_drop_constraint_rollback_undo_test.go's FK sub-test (M0143-0008b),
// which routed around the bug by running against the cluster's default
// database instead — see that file's comment for the original repro.
//
// Fixed by changing DropForeignKeyConstraint to take the resolved *Table
// directly (catalog.go), so it mutates the same live pointer the caller
// already validated the constraint against — no OID/dbOid relookup at all.

import "testing"

// TestPort_M0143_0002_DropForeignKeyConstraintNonDefaultDB verifies a
// COMMITted `DROP CONSTRAINT` on a named FOREIGN KEY in a non-default
// database actually disables enforcement: an INSERT referencing a
// non-existent parent row must succeed once the constraint is dropped.
func TestPort_M0143_0002_DropForeignKeyConstraintNonDefaultDB(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0002-fk-nondefault-db")
	for _, stmt := range []string{
		"CREATE TABLE fk_parent(id int PRIMARY KEY)",
		"CREATE TABLE fk_child(id int, pid int, CONSTRAINT child_fk FOREIGN KEY (pid) REFERENCES fk_parent(id))",
	} {
		if err := runSQLSimple(t, r, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Sanity: the FK is enforced before the drop.
	if err := runSQLSimple(t, r, "INSERT INTO fk_child VALUES (1, 999)"); err == nil {
		t.Fatalf("INSERT with a dangling FK reference succeeded before DROP CONSTRAINT — fixture is not exercising FK enforcement")
	}

	if err := runSQLSimple(t, r, "ALTER TABLE fk_child DROP CONSTRAINT child_fk"); err != nil {
		t.Fatalf("ALTER TABLE ... DROP CONSTRAINT child_fk: %v", err)
	}

	// If M0143-0002's bug is still present, child_fk silently survives the
	// drop on this non-default database and this INSERT still fails 23503.
	if err := runSQLSimple(t, r, "INSERT INTO fk_child VALUES (1, 999)"); err != nil {
		t.Fatalf("INSERT with a dangling FK reference failed after DROP CONSTRAINT on a "+
			"non-default database — the FK was not actually removed (M0143-0002): %v", err)
	}

	// Dropping again must report undefined_object, not silently no-op a
	// second time.
	err := runSQLSimple(t, r, "ALTER TABLE fk_child DROP CONSTRAINT child_fk")
	if err == nil {
		t.Fatalf("re-dropping an already-dropped constraint should fail 42704, got no error")
	}
}
