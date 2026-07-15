package catalog

import "testing"

// TestAccessMethodCrossDatabaseIsolation verifies that CREATE ACCESS METHOD
// under one database does not collide with a same-named access method
// already created under a different database — the specific gap the DU-002
// dump+restore round-trip probe (TestPort_PgDumpConnectionSetup) hit next
// after the function/routine signature-matching sub-series was exhausted:
// restoring a dump into a fresh database re-issues `CREATE ACCESS METHOD
// goopg_am TYPE INDEX HANDLER goopg_am_handler`, which previously errored
// "access method \"goopg_am\" already exists" because every AccessMethod
// shared one flat, dbOid-less registry. Mirrors the UserCollation/Domain/
// Routines cross-database isolation precedent (M0122-0007 4e follow-up).
func TestAccessMethodCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first, err := c.RegisterAccessMethod("goopg_am", "i", 12345, DefaultDBOid)
	if err != nil {
		t.Fatalf("RegisterAccessMethod under DefaultDBOid: %v", err)
	}

	// The exact same name under a distinct database must NOT collide, unlike
	// a genuine same-database duplicate.
	second, err := c.RegisterAccessMethod("goopg_am", "i", 12345, otherDB)
	if err != nil {
		t.Fatalf("RegisterAccessMethod under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database access methods share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.RegisterAccessMethod("goopg_am", "i", 12345, DefaultDBOid); err == nil {
		t.Error("same-database duplicate RegisterAccessMethod should still error")
	}

	// Each database's UserAccessMethodOID(name, dbOid) resolves to its own copy.
	if oid := c.UserAccessMethodOID("goopg_am", DefaultDBOid); oid != first.OID {
		t.Errorf("UserAccessMethodOID(goopg_am, DefaultDBOid) = %d, want %d", oid, first.OID)
	}
	if oid := c.UserAccessMethodOID("goopg_am", otherDB); oid != second.OID {
		t.Errorf("UserAccessMethodOID(goopg_am, otherDB) = %d, want %d", oid, second.OID)
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropAccessMethod("goopg_am", DefaultDBOid) {
		t.Fatal("DropAccessMethod(goopg_am) under DefaultDBOid: not found")
	}
	if oid := c.UserAccessMethodOID("goopg_am", otherDB); oid != second.OID {
		t.Error("otherDB's goopg_am vanished after dropping DefaultDBOid's own copy")
	}
	if oid := c.UserAccessMethodOID("goopg_am", DefaultDBOid); oid != 0 {
		t.Error("DefaultDBOid's goopg_am still resolvable after DropAccessMethod")
	}
}
