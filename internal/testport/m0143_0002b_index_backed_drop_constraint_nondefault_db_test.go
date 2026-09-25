package testport

// M0143-0002b — `ALTER TABLE ... DROP CONSTRAINT` on a PRIMARY KEY, UNIQUE, or
// EXCLUDE constraint reported success and did nothing when the table lived in
// a non-default database.
//
// catalog.InMemory.dropIndexByName(tableOID, name) — shared by
// DropPrimaryKeyConstraint/DropUniqueConstraint/DropExclusionConstraint —
// hardcoded c.ns(DefaultDBOid).byTable[tableOID] instead of the table's own
// namespace. For a table in any other database the lookup found nothing
// (the index actually lives under c.ns(tbl.DBOid)), so the method returned
// false without touching the real registry, and
// execAlterTableDropConstraint's PK/UNIQUE/EXCLUDE branches (internal/
// executor/operators_ddl.go) discard that bool — so a plain COMMITted DROP
// CONSTRAINT on such a constraint left it permanently enforced. Exact same
// hardcode shape as M0143-0002's DropForeignKeyConstraint bug (filed as its
// child task M0143-0002b once that fix confirmed the FK-specific instance
// live and left these three index-backed siblings unconfirmed).
//
// Fixed by changing dropIndexByName (and the three public wrappers) to take
// the resolved *Table directly and key c.ns() off tbl.DBOid, mirroring
// DropForeignKeyConstraint's M0143-0002 fix.

import "testing"

// TestPort_M0143_0002b_DropPrimaryKeyConstraintNonDefaultDB verifies a
// COMMITted `DROP CONSTRAINT` on a named PRIMARY KEY in a non-default
// database actually disables enforcement.
func TestPort_M0143_0002b_DropPrimaryKeyConstraintNonDefaultDB(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0002b-pk-nondefault-db")
	if err := runSQLSimple(t, r, "CREATE TABLE pk_t(id int, CONSTRAINT pk_t_pkey PRIMARY KEY (id))"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runSQLSimple(t, r, "INSERT INTO pk_t VALUES (1)"); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	// Sanity: the PK is enforced before the drop.
	if err := runSQLSimple(t, r, "INSERT INTO pk_t VALUES (1)"); err == nil {
		t.Fatalf("duplicate-key INSERT succeeded before DROP CONSTRAINT — fixture is not exercising PK enforcement")
	}

	if err := runSQLSimple(t, r, "ALTER TABLE pk_t DROP CONSTRAINT pk_t_pkey"); err != nil {
		t.Fatalf("ALTER TABLE ... DROP CONSTRAINT pk_t_pkey: %v", err)
	}

	// If M0143-0002b's bug is still present, pk_t_pkey silently survives the
	// drop on this non-default database and this INSERT still fails 23505.
	if err := runSQLSimple(t, r, "INSERT INTO pk_t VALUES (1)"); err != nil {
		t.Fatalf("duplicate-key INSERT failed after DROP CONSTRAINT on a "+
			"non-default database — the PK was not actually removed (M0143-0002b): %v", err)
	}

	// Dropping again must report undefined_object, not silently no-op a
	// second time.
	if err := runSQLSimple(t, r, "ALTER TABLE pk_t DROP CONSTRAINT pk_t_pkey"); err == nil {
		t.Fatalf("re-dropping an already-dropped constraint should fail 42704, got no error")
	}
}

// TestPort_M0143_0002b_DropUniqueConstraintNonDefaultDB verifies a
// COMMITted `DROP CONSTRAINT` on a named UNIQUE constraint in a non-default
// database actually disables enforcement.
func TestPort_M0143_0002b_DropUniqueConstraintNonDefaultDB(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0002b-uq-nondefault-db")
	if err := runSQLSimple(t, r, "CREATE TABLE uq_t(id int, CONSTRAINT uq_t_id_key UNIQUE (id))"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runSQLSimple(t, r, "INSERT INTO uq_t VALUES (1)"); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	if err := runSQLSimple(t, r, "INSERT INTO uq_t VALUES (1)"); err == nil {
		t.Fatalf("duplicate-value INSERT succeeded before DROP CONSTRAINT — fixture is not exercising UNIQUE enforcement")
	}

	if err := runSQLSimple(t, r, "ALTER TABLE uq_t DROP CONSTRAINT uq_t_id_key"); err != nil {
		t.Fatalf("ALTER TABLE ... DROP CONSTRAINT uq_t_id_key: %v", err)
	}

	if err := runSQLSimple(t, r, "INSERT INTO uq_t VALUES (1)"); err != nil {
		t.Fatalf("duplicate-value INSERT failed after DROP CONSTRAINT on a "+
			"non-default database — the UNIQUE constraint was not actually removed (M0143-0002b): %v", err)
	}

	if err := runSQLSimple(t, r, "ALTER TABLE uq_t DROP CONSTRAINT uq_t_id_key"); err == nil {
		t.Fatalf("re-dropping an already-dropped constraint should fail 42704, got no error")
	}
}

// TestPort_M0143_0002b_DropExclusionConstraintNonDefaultDB verifies a
// COMMITted `DROP CONSTRAINT` on a named EXCLUDE constraint in a
// non-default database actually disables enforcement.
func TestPort_M0143_0002b_DropExclusionConstraintNonDefaultDB(t *testing.T) {
	_, r := startP0E4Cluster(t, "m0143-0002b-excl-nondefault-db")
	if err := runSQLSimple(t, r, "CREATE TABLE excl_t(a int, CONSTRAINT excl_t_a_excl EXCLUDE USING btree (a WITH =))"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := runSQLSimple(t, r, "INSERT INTO excl_t VALUES (1)"); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	if err := runSQLSimple(t, r, "INSERT INTO excl_t VALUES (1)"); err == nil {
		t.Fatalf("conflicting INSERT succeeded before DROP CONSTRAINT — fixture is not exercising EXCLUDE enforcement")
	}

	if err := runSQLSimple(t, r, "ALTER TABLE excl_t DROP CONSTRAINT excl_t_a_excl"); err != nil {
		t.Fatalf("ALTER TABLE ... DROP CONSTRAINT excl_t_a_excl: %v", err)
	}

	if err := runSQLSimple(t, r, "INSERT INTO excl_t VALUES (1)"); err != nil {
		t.Fatalf("conflicting INSERT failed after DROP CONSTRAINT on a "+
			"non-default database — the EXCLUDE constraint was not actually removed (M0143-0002b): %v", err)
	}

	if err := runSQLSimple(t, r, "ALTER TABLE excl_t DROP CONSTRAINT excl_t_a_excl"); err == nil {
		t.Fatalf("re-dropping an already-dropped constraint should fail 42704, got no error")
	}
}
