package catalog

import "testing"

// TestCreateTSConfigCrossDatabaseIsolation verifies that CREATE TEXT SEARCH
// CONFIGURATION under one database does not collide with a same-named,
// same-schema configuration already created under a different database — the
// specific gap the DU-002 dump+restore round-trip probe
// (TestPort_PgDumpConnectionSetup) hit: restoring a dump into a fresh database
// re-issues `CREATE TEXT SEARCH CONFIGURATION public.my_cfg ...`, which
// previously leaked across databases because every UserTSConfig shared one
// flat, dbOid-less registry. Mirrors TestCreateTSDictCrossDatabaseIsolation.
// M0122-0007 4e follow-up (DU-002 round-trip probe unblock).
func TestCreateTSConfigCrossDatabaseIsolation(t *testing.T) {
	c := NewInMemory()
	const otherDB = uint32(99999)

	first := &UserTSConfig{
		Name:   "my_cfg",
		Owner:  10,
		Parser: BuiltinTSParserOID["default"],
	}
	firstOID, err := c.CreateTSConfig(first, "public", DefaultDBOid)
	if err != nil {
		t.Fatalf("CreateTSConfig under DefaultDBOid: %v", err)
	}

	// The exact same (schema, name) under a distinct database must NOT
	// collide, unlike a genuine same-database duplicate.
	second := &UserTSConfig{
		Name:   "my_cfg",
		Owner:  10,
		Parser: BuiltinTSParserOID["default"],
	}
	secondOID, err := c.CreateTSConfig(second, "public", otherDB)
	if err != nil {
		t.Fatalf("CreateTSConfig under a distinct dbOid falsely collided: %v", err)
	}
	if firstOID == secondOID {
		t.Errorf("cross-database configurations share OID %d, want distinct OIDs", firstOID)
	}

	// A genuine same-database duplicate still errors (regression guard for
	// the existing single-database behavior).
	if _, err := c.CreateTSConfig(&UserTSConfig{Name: "my_cfg", Parser: BuiltinTSParserOID["default"]}, "public", DefaultDBOid); err == nil {
		t.Error("same-database duplicate CreateTSConfig should still error")
	}

	// Each database's pg_ts_config view sees only its own rows.
	rowsDefault := c.PGTSConfigRowsForDBOid(DefaultDBOid)
	rowsOther := c.PGTSConfigRowsForDBOid(otherDB)
	if len(rowsDefault) != 1 {
		t.Errorf("PGTSConfigRowsForDBOid(DefaultDBOid) rows = %d, want 1", len(rowsDefault))
	}
	if len(rowsOther) != 1 {
		t.Errorf("PGTSConfigRowsForDBOid(otherDB) rows = %d, want 1", len(rowsOther))
	}

	// FindTSConfig is likewise dbOid-scoped.
	if cfg := c.FindTSConfig("my_cfg", "public", DefaultDBOid); cfg == nil || cfg.OID != firstOID {
		t.Errorf("FindTSConfig under DefaultDBOid = %v, want OID %d", cfg, firstOID)
	}
	if cfg := c.FindTSConfig("my_cfg", "public", otherDB); cfg == nil || cfg.OID != secondOID {
		t.Errorf("FindTSConfig under otherDB = %v, want OID %d", cfg, secondOID)
	}

	// ListUserTSConfigs is likewise dbOid-scoped.
	if cfgs := c.ListUserTSConfigs(DefaultDBOid); len(cfgs) != 1 {
		t.Errorf("ListUserTSConfigs(DefaultDBOid) = %d configs, want 1", len(cfgs))
	}
	if cfgs := c.ListUserTSConfigs(otherDB); len(cfgs) != 1 {
		t.Errorf("ListUserTSConfigs(otherDB) = %d configs, want 1", len(cfgs))
	}

	// Dropping under one database must not remove the other's row.
	if !c.DropTSConfig("my_cfg", "public", DefaultDBOid) {
		t.Fatal("DropTSConfig(my_cfg) under DefaultDBOid = false, want true")
	}
	if rows := c.PGTSConfigRowsForDBOid(otherDB); len(rows) != 1 {
		t.Error("otherDB's my_cfg vanished after dropping DefaultDBOid's own copy")
	}
	if rows := c.PGTSConfigRowsForDBOid(DefaultDBOid); len(rows) != 0 {
		t.Error("DefaultDBOid's my_cfg still resolvable after DropTSConfig")
	}
}
