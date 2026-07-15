package catalog

import "testing"

// TestCreateConversionCrossDatabaseIsolation verifies that CREATE CONVERSION
// under one database does not collide with a same-named, same-schema
// conversion already created under a different database — the specific gap
// the DU-002 dump+restore round-trip probe (TestPort_PgDumpConnectionSetup)
// hit next after the operator-family/class ordering series was exhausted:
// restoring a dump into a fresh database re-issues `CREATE DEFAULT
// CONVERSION public.aliasconv ...`, which previously errored "conversion
// \"aliasconv\" already exists" because every UserConversion shared one flat,
// dbOid-less registry. Mirrors the UserCollation/AccessMethod/
// UserOperatorFamily+Class cross-database isolation precedent. M0122-0007 4e
// follow-up (DU-002 round-trip probe unblock).
func TestCreateConversionCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first := &UserConversion{
		Name:        "aliasconv",
		Owner:       10,
		ForEncoding: EncodingNameToID("UTF8"),
		ToEncoding:  EncodingNameToID("LATIN1"),
		ProcSchema:  "public",
		ProcName:    "aliasconv_func",
	}
	firstOID, err := c.CreateConversion(first, "public", DefaultDBOid)
	if err != nil {
		t.Fatalf("CreateConversion under DefaultDBOid: %v", err)
	}

	// The exact same (schema, name) under a distinct database must NOT
	// collide, unlike a genuine same-database duplicate.
	second := &UserConversion{
		Name:        "aliasconv",
		Owner:       10,
		ForEncoding: EncodingNameToID("UTF8"),
		ToEncoding:  EncodingNameToID("LATIN1"),
		ProcSchema:  "public",
		ProcName:    "aliasconv_func",
	}
	secondOID, err := c.CreateConversion(second, "public", otherDB)
	if err != nil {
		t.Fatalf("CreateConversion under a distinct dbOid falsely collided: %v", err)
	}
	if firstOID == secondOID {
		t.Errorf("cross-database conversions share OID %d, want distinct OIDs", firstOID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.CreateConversion(&UserConversion{Name: "aliasconv", ProcName: "x"}, "public", DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreateConversion should still error")
	}

	// Each database's pg_conversion view sees only its own row.
	rowsDefault := c.PGConversionRowsForDBOid(DefaultDBOid)
	rowsOther := c.PGConversionRowsForDBOid(otherDB)
	if len(rowsDefault) != 1 {
		t.Errorf("PGConversionRowsForDBOid(DefaultDBOid) rows = %d, want 1", len(rowsDefault))
	}
	if len(rowsOther) != 1 {
		t.Errorf("PGConversionRowsForDBOid(otherDB) rows = %d, want 1", len(rowsOther))
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropConversion("aliasconv", "public", DefaultDBOid) {
		t.Fatal("DropConversion(aliasconv) under DefaultDBOid = false, want true")
	}
	if rows := c.PGConversionRowsForDBOid(otherDB); len(rows) != 1 {
		t.Error("otherDB's aliasconv vanished after dropping DefaultDBOid's own copy")
	}
	if rows := c.PGConversionRowsForDBOid(DefaultDBOid); len(rows) != 0 {
		t.Error("DefaultDBOid's aliasconv still resolvable after DropConversion")
	}
}
