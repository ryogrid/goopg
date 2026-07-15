package catalog

import "testing"

// TestCreateRangeTypeCrossDatabaseIsolation mirrors
// TestCreateCompositeTypeCrossDatabaseIsolation/TestCreateEnumCrossDatabaseIsolation/
// TestCreateDomainCrossDatabaseIsolation: two distinct databases must each be
// able to `CREATE TYPE ... AS RANGE (subtype = ...)` a same-named range type
// without colliding. M0122-0007 4e follow-up (range type isolation — resume
// point (4) from the composite-type follow-up row).
func TestCreateRangeTypeCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first, err := c.RegisterRangeType("myrange", "int4", "", "", "", DefaultDBOid)
	if err != nil {
		t.Fatalf("RegisterRangeType(myrange, DefaultDBOid): %v", err)
	}
	second, err := c.RegisterRangeType("myrange", "int4", "", "", "", otherDB)
	if err != nil {
		t.Fatalf("RegisterRangeType(myrange, otherDB): %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database range types share OID %d, want distinct OIDs", first.OID)
	}

	// Each database's LookupRangeType(name, dbOid) resolves to its own copy.
	gotDefault, ok := c.LookupRangeType("myrange", DefaultDBOid)
	if !ok || gotDefault.OID != first.OID {
		t.Errorf("LookupRangeType(myrange, DefaultDBOid) = %+v, want OID %d", gotDefault, first.OID)
	}
	gotOther, ok := c.LookupRangeType("myrange", otherDB)
	if !ok || gotOther.OID != second.OID {
		t.Errorf("LookupRangeType(myrange, otherDB) = %+v, want OID %d", gotOther, second.OID)
	}

	// RenameRangeType/SetRangeTypeOwner scoped to one database must not touch
	// the other database's same-named range type.
	if !c.SetRangeTypeOwner("myrange", 12345, otherDB) {
		t.Fatal("SetRangeTypeOwner(myrange, otherDB) returned false")
	}
	if got, _ := c.LookupRangeType("myrange", DefaultDBOid); got.Owner == 12345 {
		t.Error("SetRangeTypeOwner under otherDB leaked into DefaultDBOid's copy")
	}

	if err := c.RenameRangeType("myrange", "myrange2", otherDB); err != nil {
		t.Fatalf("RenameRangeType(myrange->myrange2) under otherDB: %v", err)
	}
	if _, ok := c.LookupRangeType("myrange", DefaultDBOid); !ok {
		t.Error("RenameRangeType under otherDB should not affect DefaultDBOid's myrange")
	}
	if _, ok := c.LookupRangeType("myrange2", otherDB); !ok {
		t.Error("RenameRangeType under otherDB did not produce myrange2 in otherDB")
	}

	// Dropping under one database must not remove the other's row.
	if err := c.DropRangeType("myrange", DefaultDBOid); err != nil {
		t.Fatalf("DropRangeType(myrange) under DefaultDBOid: %v", err)
	}
	if _, ok := c.LookupRangeType("myrange2", otherDB); !ok {
		t.Error("otherDB's myrange2 vanished after dropping DefaultDBOid's own copy")
	}
	if _, ok := c.LookupRangeType("myrange", DefaultDBOid); ok {
		t.Error("DefaultDBOid's myrange still resolvable after DropRangeType")
	}
}
