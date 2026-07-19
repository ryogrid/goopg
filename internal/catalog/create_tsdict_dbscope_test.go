package catalog

import "testing"

// TestCreateTSDictCrossDatabaseIsolation verifies that CREATE TEXT SEARCH
// DICTIONARY under one database does not collide with a same-named,
// same-schema dictionary already created under a different database — the
// specific gap the DU-002 dump+restore round-trip probe
// (TestPort_PgDumpConnectionSetup) hit next after the ALTER CONVERSION series:
// restoring a dump into a fresh database re-issues `CREATE TEXT SEARCH
// DICTIONARY public.simple_dict ...`, which previously errored `text search
// dictionary "simple_dict" already exists` because every UserTSDict shared one
// flat, dbOid-less registry. Mirrors the UserConversion cross-database
// isolation precedent (create_conversion_dbscope_test.go). M0122-0007 4e
// follow-up (DU-002 round-trip probe unblock).
func TestCreateTSDictCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	// The BKI-pinned "simple" builtin is always row 0 of every database's
	// pg_ts_dict view; user dictionaries follow it.
	const builtinRows = 1

	first := &UserTSDict{Name: "simple_dict", Owner: 10, Template: BuiltinTSTemplateOID["simple"]}
	firstOID, err := c.CreateTSDict(first, "public", DefaultDBOid)
	if err != nil {
		t.Fatalf("CreateTSDict under DefaultDBOid: %v", err)
	}

	// The exact same (schema, name) under a distinct database must NOT
	// collide, unlike a genuine same-database duplicate.
	second := &UserTSDict{Name: "simple_dict", Owner: 10, Template: BuiltinTSTemplateOID["simple"]}
	secondOID, err := c.CreateTSDict(second, "public", otherDB)
	if err != nil {
		t.Fatalf("CreateTSDict under a distinct dbOid falsely collided: %v", err)
	}
	if firstOID == secondOID {
		t.Errorf("cross-database dictionaries share OID %d, want distinct OIDs", firstOID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.CreateTSDict(&UserTSDict{Name: "simple_dict"}, "public", DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreateTSDict should still error")
	}

	// Each database's pg_ts_dict view sees only its own user row (plus the
	// shared builtin "simple").
	rowsDefault := c.PGTSDictRowsForDBOid(DefaultDBOid)
	rowsOther := c.PGTSDictRowsForDBOid(otherDB)
	if len(rowsDefault) != builtinRows+1 {
		t.Errorf("PGTSDictRowsForDBOid(DefaultDBOid) rows = %d, want %d", len(rowsDefault), builtinRows+1)
	}
	if len(rowsOther) != builtinRows+1 {
		t.Errorf("PGTSDictRowsForDBOid(otherDB) rows = %d, want %d", len(rowsOther), builtinRows+1)
	}

	// FindTSDict is likewise dbOid-scoped.
	if ud := c.FindTSDict("simple_dict", "public", DefaultDBOid); ud == nil || ud.OID != firstOID {
		t.Errorf("FindTSDict under DefaultDBOid = %v, want OID %d", ud, firstOID)
	}
	if ud := c.FindTSDict("simple_dict", "public", otherDB); ud == nil || ud.OID != secondOID {
		t.Errorf("FindTSDict under otherDB = %v, want OID %d", ud, secondOID)
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropTSDict("simple_dict", "public", DefaultDBOid) {
		t.Fatal("DropTSDict(simple_dict) under DefaultDBOid = false, want true")
	}
	if rows := c.PGTSDictRowsForDBOid(otherDB); len(rows) != builtinRows+1 {
		t.Error("otherDB's simple_dict vanished after dropping DefaultDBOid's own copy")
	}
	if rows := c.PGTSDictRowsForDBOid(DefaultDBOid); len(rows) != builtinRows {
		t.Error("DefaultDBOid's simple_dict still resolvable after DropTSDict")
	}
}
