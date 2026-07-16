package catalog

import "testing"

// TestCreateEnumCrossDatabaseIsolation mirrors
// TestCreateDomainCrossDatabaseIsolation (create_domain_test.go): two
// distinct databases must each be able to `CREATE TYPE ... AS ENUM` a
// same-named enum without colliding. M0122-0007 4e follow-up (enum
// isolation, DU-002 round-trip probe unblock — the "gtype" collision).
func TestCreateEnumCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first, err := c.RegisterEnum("gtype", []string{"lo", "hi"}, DefaultDBOid)
	if err != nil {
		t.Fatalf("RegisterEnum under DefaultDBOid: %v", err)
	}

	// The exact same name under a distinct database must NOT collide, unlike
	// a genuine same-database duplicate.
	second, err := c.RegisterEnum("gtype", []string{"lo", "hi"}, otherDB)
	if err != nil {
		t.Fatalf("RegisterEnum under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database enums share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.RegisterEnum("gtype", []string{"lo", "hi"}, DefaultDBOid); err == nil {
		t.Error("same-database duplicate RegisterEnum should still error")
	}

	// Each database's LookupEnum(name, dbOid) resolves to its own copy.
	gotDefault, ok := c.LookupEnum("gtype", DefaultDBOid)
	if !ok || gotDefault.OID != first.OID {
		t.Errorf("LookupEnum(gtype, DefaultDBOid) = %+v, ok=%v, want OID %d", gotDefault, ok, first.OID)
	}
	gotOther, ok := c.LookupEnum("gtype", otherDB)
	if !ok || gotOther.OID != second.OID {
		t.Errorf("LookupEnum(gtype, otherDB) = %+v, ok=%v, want OID %d", gotOther, ok, second.OID)
	}

	// RenameEnum/SetEnumOwner/AddEnumValueResult scoped to one database must
	// not touch the other database's same-named enum.
	if err := c.SetEnumOwner("gtype", 12345, otherDB); !err {
		t.Fatal("SetEnumOwner(gtype, otherDB) returned false")
	}
	if gotDefault, _ := c.LookupEnum("gtype", DefaultDBOid); gotDefault.Owner == 12345 {
		t.Error("SetEnumOwner under otherDB leaked into DefaultDBOid's copy")
	}

	if _, err := c.AddEnumValueResult("gtype", "mid", false, "", "", otherDB); err != nil {
		t.Fatalf("AddEnumValueResult under otherDB: %v", err)
	}
	if gotDefault, _ := c.LookupEnum("gtype", DefaultDBOid); len(gotDefault.Values) != 2 {
		t.Errorf("AddEnumValueResult under otherDB leaked into DefaultDBOid's copy: %d values, want 2", len(gotDefault.Values))
	}

	// Dropping under one database must not remove the other's row.
	if err := c.DropEnum("gtype", false, DefaultDBOid); err != nil {
		t.Fatalf("DropEnum(gtype) under DefaultDBOid: %v", err)
	}
	if _, ok := c.LookupEnum("gtype", otherDB); !ok {
		t.Error("otherDB's gtype vanished after dropping DefaultDBOid's own copy")
	}
	if _, ok := c.LookupEnum("gtype", DefaultDBOid); ok {
		t.Error("DefaultDBOid's gtype still resolvable after DropEnum")
	}
}
