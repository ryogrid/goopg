package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTSConfigAlterMapping guards the ALTER MAPPING FOR tok WITH dict
// override follow-up to M0119-0004 slice 446:
// `ALTER TEXT SEARCH CONFIGURATION name ALTER MAPPING FOR tok [, ...] WITH
// dict [, ...]` (ALTER_TSCONFIG_ALTER_MAPPING_FOR_TOKEN, override=true in
// tsearchcmds.c's MakeConfigurationMapping) was entirely unhandled (falling
// through to the discarded compat no-op). Unlike ADD MAPPING it must not
// 23505 on an already-mapped token type — it wholesale replaces that token
// type's dictionary list — and unlike ALTER MAPPING REPLACE it substitutes
// the whole list, not just a single matched OID.
func TestAlterTSConfigAlterMapping(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_alter_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_alter_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_alter_cfg ADD MAPPING FOR asciiword, word WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING: %v", err)
	}

	var userDictOID uint32
	for _, ud := range im.ListUserTSDicts() {
		if ud.Name == "ts_alter_dict" {
			userDictOID = ud.OID
		}
	}
	if userDictOID == 0 {
		t.Fatal("ts_alter_dict not found via ListUserTSDicts")
	}

	// Overriding an already-mapped token type must NOT 23505 — that is the
	// entire point of this form, unlike ADD MAPPING.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_alter_cfg ALTER MAPPING FOR asciiword WITH ts_alter_dict, simple`); err != nil {
		t.Fatalf("ALTER MAPPING FOR ... WITH: %v", err)
	}

	got := mappingDictOIDs(im, "ts_alter_cfg", "asciiword")
	want := []uint32{userDictOID, catalog.BuiltinTSDictOID["simple"]}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("asciiword mapping DictOIDs = %v, want %v (wholesale override)", got, want)
	}
	// The untouched "word" mapping must keep its original single-entry list.
	got = mappingDictOIDs(im, "ts_alter_cfg", "word")
	if len(got) != 1 || got[0] != catalog.BuiltinTSDictOID["simple"] {
		t.Errorf("word mapping DictOIDs = %v, want [%d] (untouched)", got, catalog.BuiltinTSDictOID["simple"])
	}

	// A token type with no prior mapping is simply created (same effect as
	// ADD MAPPING would have had).
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_alter_cfg ALTER MAPPING FOR numword WITH simple`); err != nil {
		t.Fatalf("ALTER MAPPING FOR (new token type): %v", err)
	}
	got = mappingDictOIDs(im, "ts_alter_cfg", "numword")
	if len(got) != 1 || got[0] != catalog.BuiltinTSDictOID["simple"] {
		t.Errorf("numword mapping DictOIDs = %v, want [%d] (newly created)", got, catalog.BuiltinTSDictOID["simple"])
	}

	// Unknown configuration raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION nosuchcfg ALTER MAPPING FOR asciiword WITH simple`)
	if err == nil {
		t.Fatal("ALTER MAPPING on unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// Unknown dictionary name raises 42704.
	err = runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_alter_cfg ALTER MAPPING FOR asciiword WITH nosuchdict`)
	if err == nil {
		t.Fatal("ALTER MAPPING with an unknown dictionary should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
