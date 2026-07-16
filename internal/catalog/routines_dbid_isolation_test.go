package catalog

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRoutinesCrossDatabaseIsolation mirrors TestCreateDomainCrossDatabaseIsolation
// et al (M0122-0007 4e series) for the routine/function registry: a same-named
// routine in two distinct databases must not collide, and Lookup/LookupByName/
// Drop/DropByName must each resolve only their own database's copy. M0119-0004
// DU-002 follow-up.
func TestRoutinesCrossDatabaseIsolation(t *testing.T) {
	rs := NewRoutines()
	const otherDB = uint32(99999)

	first, err := rs.Create(&Routine{
		DBOid:    DefaultDBOid,
		Schema:   "public",
		Name:     "add_calcdef",
		ArgTypes: []Type{{Name: "int4"}},
	}, false)
	if err != nil {
		t.Fatalf("Create under DefaultDBOid: %v", err)
	}

	// The exact same schema+name+signature under a distinct database must
	// NOT collide, unlike a genuine same-database duplicate.
	second, err := rs.Create(&Routine{
		DBOid:    otherDB,
		Schema:   "public",
		Name:     "add_calcdef",
		ArgTypes: []Type{{Name: "int4"}},
	}, false)
	if err != nil {
		t.Fatalf("Create under a distinct dbOid falsely collided: %v", err)
	}
	if first.OID == second.OID {
		t.Errorf("cross-database routines share OID %d, want distinct OIDs", first.OID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := rs.Create(&Routine{
		DBOid:    DefaultDBOid,
		Schema:   "public",
		Name:     "add_calcdef",
		ArgTypes: []Type{{Name: "int4"}},
	}, false); err == nil {
		t.Error("same-database duplicate Create should still error")
	}

	objName := parser.ObjectName{Schema: "public", Name: "add_calcdef"}
	argTypes := []Type{{Name: "int4"}}

	// Each database's Lookup(name, argTypes, dbOid) resolves to its own copy.
	gotDefault, ok := rs.Lookup(objName, argTypes, DefaultDBOid)
	if !ok || gotDefault.OID != first.OID {
		t.Errorf("Lookup(add_calcdef, DefaultDBOid) = %+v, ok=%v, want OID %d", gotDefault, ok, first.OID)
	}
	gotOther, ok := rs.Lookup(objName, argTypes, otherDB)
	if !ok || gotOther.OID != second.OID {
		t.Errorf("Lookup(add_calcdef, otherDB) = %+v, ok=%v, want OID %d", gotOther, ok, second.OID)
	}

	// LookupByName is likewise scoped per database.
	if got := rs.LookupByName(objName, DefaultDBOid); len(got) != 1 || got[0].OID != first.OID {
		t.Errorf("LookupByName(add_calcdef, DefaultDBOid) = %+v, want single routine OID %d", got, first.OID)
	}
	if got := rs.LookupByName(objName, otherDB); len(got) != 1 || got[0].OID != second.OID {
		t.Errorf("LookupByName(add_calcdef, otherDB) = %+v, want single routine OID %d", got, second.OID)
	}

	// Dropping under one database must not remove the other's row.
	if err := rs.Drop(objName, argTypes, DefaultDBOid); err != nil {
		t.Fatalf("Drop(add_calcdef) under DefaultDBOid: %v", err)
	}
	if _, ok := rs.Lookup(objName, argTypes, otherDB); !ok {
		t.Error("otherDB's add_calcdef vanished after dropping DefaultDBOid's own copy")
	}
	if _, ok := rs.Lookup(objName, argTypes, DefaultDBOid); ok {
		t.Error("DefaultDBOid's add_calcdef still resolves after Drop")
	}

	// DropByName is likewise scoped per database.
	if err := rs.DropByName(objName, otherDB); err != nil {
		t.Fatalf("DropByName(add_calcdef) under otherDB: %v", err)
	}
	if _, ok := rs.Lookup(objName, argTypes, otherDB); ok {
		t.Error("otherDB's add_calcdef still resolves after DropByName")
	}
}
