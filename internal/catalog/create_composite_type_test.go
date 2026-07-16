package catalog

import "testing"

// TestCreateCompositeTypeCrossDatabaseIsolation mirrors
// TestCreateEnumCrossDatabaseIsolation / TestCreateDomainCrossDatabaseIsolation:
// two distinct databases must each be able to `CREATE TYPE ... AS (...)` a
// same-named composite type without colliding. M0122-0007 4e follow-up
// (composite type isolation — the last unaudited sibling map in this series).
func TestCreateCompositeTypeCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	fields := []CompositeField{{Name: "a", ColType: "int4"}, {Name: "b", ColType: "text"}}
	first := c.RegisterCompositeTypeWithFields("gaddr", fields, DefaultDBOid)
	second := c.RegisterCompositeTypeWithFields("gaddr", fields, otherDB)
	if first.OID == second.OID {
		t.Errorf("cross-database composite types share OID %d, want distinct OIDs", first.OID)
	}

	// Each database's LookupCompositeType(name, dbOid) resolves to its own copy.
	gotDefault := c.LookupCompositeType("gaddr", DefaultDBOid)
	if gotDefault == nil || gotDefault.OID != first.OID {
		t.Errorf("LookupCompositeType(gaddr, DefaultDBOid) = %+v, want OID %d", gotDefault, first.OID)
	}
	gotOther := c.LookupCompositeType("gaddr", otherDB)
	if gotOther == nil || gotOther.OID != second.OID {
		t.Errorf("LookupCompositeType(gaddr, otherDB) = %+v, want OID %d", gotOther, second.OID)
	}
	if !c.HasCompositeType("gaddr", DefaultDBOid) || !c.HasCompositeType("gaddr", otherDB) {
		t.Error("HasCompositeType should report true for both databases' copies")
	}

	// RenameCompositeType/SetCompositeTypeOwner scoped to one database must not
	// touch the other database's same-named composite type.
	if !c.SetCompositeTypeOwner("gaddr", 12345, otherDB) {
		t.Fatal("SetCompositeTypeOwner(gaddr, otherDB) returned false")
	}
	if got := c.LookupCompositeType("gaddr", DefaultDBOid); got.Owner == 12345 {
		t.Error("SetCompositeTypeOwner under otherDB leaked into DefaultDBOid's copy")
	}

	if err := c.RenameCompositeType("gaddr", "gaddr2", otherDB); err != nil {
		t.Fatalf("RenameCompositeType(gaddr->gaddr2) under otherDB: %v", err)
	}
	if c.LookupCompositeType("gaddr", DefaultDBOid) == nil {
		t.Error("RenameCompositeType under otherDB should not affect DefaultDBOid's gaddr")
	}
	if c.LookupCompositeType("gaddr2", otherDB) == nil {
		t.Error("RenameCompositeType under otherDB did not produce gaddr2 in otherDB")
	}

	// Dropping under one database must not remove the other's row.
	if err := c.DropCompositeType("gaddr", DefaultDBOid); err != nil {
		t.Fatalf("DropCompositeType(gaddr) under DefaultDBOid: %v", err)
	}
	if c.LookupCompositeType("gaddr2", otherDB) == nil {
		t.Error("otherDB's gaddr2 vanished after dropping DefaultDBOid's own copy")
	}
	if c.LookupCompositeType("gaddr", DefaultDBOid) != nil {
		t.Error("DefaultDBOid's gaddr still resolvable after DropCompositeType")
	}
}
