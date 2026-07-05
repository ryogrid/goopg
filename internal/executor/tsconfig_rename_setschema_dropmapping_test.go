package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTSConfigRenameTo guards the M0119-0004 slice 446 follow-up: ALTER
// TEXT SEARCH CONFIGURATION name RENAME TO newname was entirely unhandled
// (falling through to the discarded compat no-op), even though the
// configuration itself already round-trips through pg_dump under its
// original name.
func TestAlterTSConfigRenameTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_ren_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_ren_cfg RENAME TO ts_ren_cfg2`); err != nil {
		t.Fatalf("ALTER ... RENAME TO: %v", err)
	}

	found := false
	for _, uc := range im.ListUserTSConfigs() {
		if uc.Name == "ts_ren_cfg2" {
			found = true
		}
		if uc.Name == "ts_ren_cfg" {
			t.Error("old configuration name still present after RENAME TO")
		}
	}
	if !found {
		t.Fatal("renamed configuration not found via ListUserTSConfigs")
	}

	// Renaming an unknown configuration raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION nosuchcfg RENAME TO whatever`)
	if err == nil {
		t.Fatal("ALTER TEXT SEARCH CONFIGURATION RENAME TO on unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTSConfigSetSchema guards the M0119-0004 slice 446 follow-up:
// ALTER TEXT SEARCH CONFIGURATION name SET SCHEMA newschema was entirely
// unhandled, mirroring how ALTER COLLATION SET SCHEMA landed in slice 442
// (TestAlterCollationSetSchema).
func TestAlterTSConfigSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA ts_other_schema`); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_schema_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_schema_cfg SET SCHEMA ts_other_schema`); err != nil {
		t.Fatalf("ALTER ... SET SCHEMA: %v", err)
	}

	wantNsOID := im.SchemaOID("ts_other_schema")
	if wantNsOID == 0 {
		t.Fatal("SchemaOID(\"ts_other_schema\") = 0, want a real namespace OID")
	}
	var uc *catalog.UserTSConfig
	for _, c := range im.ListUserTSConfigs() {
		if c.Name == "ts_schema_cfg" {
			uc = c
		}
	}
	if uc == nil {
		t.Fatal("configuration not found via ListUserTSConfigs after SET SCHEMA")
	}
	if uc.NamespaceOID != wantNsOID {
		t.Errorf("NamespaceOID after SET SCHEMA = %d, want %d (ts_other_schema)", uc.NamespaceOID, wantNsOID)
	}

	// Unknown configuration raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION nosuchcfg SET SCHEMA ts_other_schema`)
	if err == nil {
		t.Fatal("ALTER TEXT SEARCH CONFIGURATION SET SCHEMA on unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTSConfigDropMapping guards the M0119-0004 slice 446 follow-up:
// ALTER TEXT SEARCH CONFIGURATION name DROP MAPPING [IF EXISTS] FOR
// tokentype [, ...] was entirely unhandled. Mirrors
// DropConfigurationMapping in tsearchcmds.c: dropping an unmapped token type
// raises 42704 "mapping for token type ... does not exist" unless IF EXISTS
// is given, in which case it is a NOTICE, not an error.
func TestAlterTSConfigDropMapping(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_drop_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_drop_cfg ADD MAPPING FOR word WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_drop_cfg DROP MAPPING FOR word`); err != nil {
		t.Fatalf("DROP MAPPING: %v", err)
	}
	for _, c := range im.ListUserTSConfigs() {
		if c.Name != "ts_drop_cfg" {
			continue
		}
		for _, m := range c.Mappings {
			if m.TokenType == "word" {
				t.Error("mapping for \"word\" still present after DROP MAPPING")
			}
		}
	}

	// Dropping an already-removed mapping without IF EXISTS raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_drop_cfg DROP MAPPING FOR word`)
	if err == nil {
		t.Fatal("DROP MAPPING for an unmapped token type without IF EXISTS should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// With IF EXISTS, the same case is a no-op notice, not an error.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_drop_cfg DROP MAPPING IF EXISTS FOR word`); err != nil {
		t.Fatalf("DROP MAPPING IF EXISTS for an unmapped token type should be a no-op, got: %v", err)
	}

	// Unknown configuration raises 42704 regardless of IF EXISTS on the
	// mapping clause (the configuration lookup itself is unconditional).
	err = runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION nosuchcfg DROP MAPPING FOR word`)
	if err == nil {
		t.Fatal("DROP MAPPING on an unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
