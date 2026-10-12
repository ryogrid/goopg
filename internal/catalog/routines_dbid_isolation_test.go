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

// TestRoutinesInDBHandle pins M0146-0156: a handle from InDB resolves calls
// that pass no dbOid against its own database, shares the registry with every
// other handle, normalises the connection's postgres OID to the default
// namespace, and DropAllInDB removes exactly one database's routines.
func TestRoutinesInDBHandle(t *testing.T) {
	rs := NewRoutines()
	const otherDB = uint32(99999)
	name := parser.ObjectName{Name: "f"}
	args := []Type{{Name: "int4"}}

	other := rs.InDB(otherDB)
	created, err := other.Create(&Routine{Schema: "public", Name: "f", ArgTypes: args}, false)
	if err != nil {
		t.Fatal(err)
	}
	if created.DBOid != otherDB {
		t.Fatalf("Create through the handle stamped DBOid %d, want %d", created.DBOid, otherDB)
	}
	if got := other.LookupByName(name); len(got) != 1 || got[0].OID != created.OID {
		t.Fatalf("handle LookupByName = %v, want the routine", got)
	}
	if got := rs.LookupByName(name); len(got) != 0 {
		t.Fatalf("default handle sees the other database's routine: %v", got)
	}
	if got := rs.InDB(PostgresDBOid).LookupByName(name); len(got) != 0 {
		t.Fatalf("postgres-OID handle sees the other database's routine: %v", got)
	}
	if _, ok := rs.Lookup(name, args, otherDB); !ok {
		t.Fatal("an explicit dbOid still resolves against that database")
	}
	if rs.LookupByOID(created.OID) == nil {
		t.Fatal("OID lookups span databases")
	}

	// Recovery keeps the database the reload stamped.
	re := rs.CreateDuringRecovery(&Routine{OID: created.OID + 10, DBOid: otherDB, Schema: "public", Name: "g", ArgTypes: args})
	if re.DBOid != otherDB {
		t.Fatalf("CreateDuringRecovery DBOid = %d, want %d", re.DBOid, otherDB)
	}

	if _, err := rs.Create(&Routine{Schema: "public", Name: "f", ArgTypes: args}, false); err != nil {
		t.Fatal(err)
	}
	rs.DropAllInDB(otherDB)
	if got := other.LookupByName(name); len(got) != 0 {
		t.Fatalf("after DropAllInDB the handle still sees %v", got)
	}
	if rs.LookupByOID(created.OID) != nil {
		t.Fatal("after DropAllInDB the routine is still reachable by OID")
	}
	if got := rs.LookupByName(name); len(got) != 1 {
		t.Fatalf("DropAllInDB removed the default database's routine too: %v", got)
	}
}
