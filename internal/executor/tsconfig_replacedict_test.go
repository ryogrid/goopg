package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// mappingDictOIDs returns the DictOIDs slice for tokenType on the named
// configuration, or nil if the configuration/mapping is not found.
func mappingDictOIDs(im *catalog.InMemory, cfgName, tokenType string) []uint32 {
	for _, uc := range im.ListUserTSConfigs() {
		if uc.Name != cfgName {
			continue
		}
		for _, m := range uc.Mappings {
			if m.TokenType == tokenType {
				return m.DictOIDs
			}
		}
	}
	return nil
}

// TestAlterTSConfigReplaceDictForToken guards the ALTER MAPPING REPLACE
// follow-up to M0119-0004 slice 446: `ALTER TEXT SEARCH CONFIGURATION name
// ALTER MAPPING FOR tok [, ...] REPLACE olddict WITH newdict` was entirely
// unhandled (falling through to the discarded compat no-op). The
// token-type-scoped form only substitutes the dictionary within the named
// token types, mirroring MakeConfigurationMapping's replace path in
// tsearchcmds.c (ALTER_TSCONFIG_REPLACE_DICT_FOR_TOKEN).
func TestAlterTSConfigReplaceDictForToken(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_repl_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_repl_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_cfg ADD MAPPING FOR asciiword, word WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_cfg ALTER MAPPING FOR asciiword REPLACE simple WITH ts_repl_dict`); err != nil {
		t.Fatalf("ALTER MAPPING FOR ... REPLACE: %v", err)
	}

	var userDictOID uint32
	for _, ud := range im.ListUserTSDicts() {
		if ud.Name == "ts_repl_dict" {
			userDictOID = ud.OID
		}
	}
	if userDictOID == 0 {
		t.Fatal("ts_repl_dict not found via ListUserTSDicts")
	}

	got := mappingDictOIDs(im, "ts_repl_cfg", "asciiword")
	if len(got) != 1 || got[0] != userDictOID {
		t.Errorf("asciiword mapping DictOIDs = %v, want [%d] (replaced)", got, userDictOID)
	}
	// The unscoped "word" mapping must be untouched — REPLACE only applies
	// to the named token types when FOR is present.
	got = mappingDictOIDs(im, "ts_repl_cfg", "word")
	if len(got) != 1 || got[0] != catalog.BuiltinTSDictOID["simple"] {
		t.Errorf("word mapping DictOIDs = %v, want [%d] (untouched)", got, catalog.BuiltinTSDictOID["simple"])
	}

	// Unknown configuration raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION nosuchcfg ALTER MAPPING FOR asciiword REPLACE simple WITH ts_repl_dict`)
	if err == nil {
		t.Fatal("ALTER MAPPING REPLACE on unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}

	// Unknown dictionary name raises 42704.
	err = runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_cfg ALTER MAPPING FOR asciiword REPLACE nosuchdict WITH ts_repl_dict`)
	if err == nil {
		t.Fatal("ALTER MAPPING REPLACE with an unknown old dictionary should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTSConfigReplaceDictBare guards the bare (no FOR clause) ALTER
// MAPPING REPLACE form — ALTER_TSCONFIG_REPLACE_DICT in gram.y — which
// substitutes the dictionary across every token type the configuration
// maps, not just a named subset. Also verifies real PG's behavior of
// silently matching zero rows rather than erroring when the old dictionary
// is valid but not actually used by any mapping.
func TestAlterTSConfigReplaceDictBare(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_repl_bare_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_repl_bare_cfg (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_bare_cfg ADD MAPPING FOR asciiword, word WITH simple`); err != nil {
		t.Fatalf("ADD MAPPING: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_bare_cfg ALTER MAPPING REPLACE simple WITH ts_repl_bare_dict`); err != nil {
		t.Fatalf("bare ALTER MAPPING REPLACE: %v", err)
	}

	var userDictOID uint32
	for _, ud := range im.ListUserTSDicts() {
		if ud.Name == "ts_repl_bare_dict" {
			userDictOID = ud.OID
		}
	}
	if userDictOID == 0 {
		t.Fatal("ts_repl_bare_dict not found via ListUserTSDicts")
	}
	for _, tt := range []string{"asciiword", "word"} {
		got := mappingDictOIDs(im, "ts_repl_bare_cfg", tt)
		if len(got) != 1 || got[0] != userDictOID {
			t.Errorf("%s mapping DictOIDs = %v, want [%d] (bare REPLACE touches every mapped token type)", tt, got, userDictOID)
		}
	}

	// Real PG raises no error when the replace matches zero rows (an
	// unconditional scan, not a lookup) — "simple" is no longer mapped to
	// anything after the prior statement, so this is a silent no-op.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_repl_bare_cfg ALTER MAPPING REPLACE simple WITH ts_repl_bare_dict`); err != nil {
		t.Fatalf("REPLACE matching zero rows should be a silent no-op, got: %v", err)
	}
}
