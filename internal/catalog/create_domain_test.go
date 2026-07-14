package catalog

import "testing"

// TestCreateDomainCrossDatabaseIsolation verifies that CREATE DOMAIN under one
// database does not collide with a same-named domain already created under a
// different database — the specific gap the DU-002 dump+restore round-trip
// probe (TestPort_PgDumpConnectionSetup) hit next after the collation fix:
// restoring a dump into a fresh database re-issues `CREATE DOMAIN
// public.b_in AS bigint ...`, which previously errored "already exists"
// because every Domain shared one flat, dbOid-less registry. Mirrors the
// UserCollation cross-database isolation precedent (M0122-0007 4e follow-up).
func TestCreateDomainCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first, err := c.RegisterDomain("b_in", Type{Name: "bigint"}, false, DefaultDBOid)
	if err != nil {
		t.Fatalf("RegisterDomain under DefaultDBOid: %v", err)
	}

	// The exact same name under a distinct database must NOT collide, unlike
	// a genuine same-database duplicate.
	second, err := c.RegisterDomain("b_in", Type{Name: "bigint"}, false, otherDB)
	if err != nil {
		t.Fatalf("RegisterDomain under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database domains share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.RegisterDomain("b_in", Type{Name: "bigint"}, false, DefaultDBOid); err == nil {
		t.Error("same-database duplicate RegisterDomain should still error")
	}

	// Each database's LookupDomain(name, dbOid) resolves to its own copy.
	gotDefault, ok := c.LookupDomain("b_in", DefaultDBOid)
	if !ok || gotDefault.OID != first.OID {
		t.Errorf("LookupDomain(b_in, DefaultDBOid) = %+v, ok=%v, want OID %d", gotDefault, ok, first.OID)
	}
	gotOther, ok := c.LookupDomain("b_in", otherDB)
	if !ok || gotOther.OID != second.OID {
		t.Errorf("LookupDomain(b_in, otherDB) = %+v, ok=%v, want OID %d", gotOther, ok, second.OID)
	}

	// Dropping under one database must not remove the other's row.
	if _, err := c.DropDomain("b_in", false, false, DefaultDBOid); err != nil {
		t.Fatalf("DropDomain(b_in) under DefaultDBOid: %v", err)
	}
	if _, ok := c.LookupDomain("b_in", otherDB); !ok {
		t.Error("otherDB's b_in vanished after dropping DefaultDBOid's own copy")
	}
	if _, ok := c.LookupDomain("b_in", DefaultDBOid); ok {
		t.Error("DefaultDBOid's b_in still resolvable after DropDomain")
	}
}
