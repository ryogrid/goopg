package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateTSConfigCopy guards the `COPY = source_config` follow-up to
// M0119-0004 slice 446: `CREATE TEXT SEARCH CONFIGURATION name ( COPY =
// source_config )` previously fell through to a discarded compat no-op
// (tokenized, silently dropped) — the new configuration always started with
// an empty pg_ts_config_map, unlike real PG's DefineTSConfiguration, which
// takes the source's parser and copies its entire mapping list
// (tsearchcmds.c).
func TestCreateTSConfigCopy(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_copy_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_copy_src (PARSER = default)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION (source): %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_copy_src ADD MAPPING FOR asciiword, word WITH ts_copy_dict, simple`); err != nil {
		t.Fatalf("ADD MAPPING: %v", err)
	}

	var userDictOID uint32
	for _, ud := range im.ListUserTSDicts() {
		if ud.Name == "ts_copy_dict" {
			userDictOID = ud.OID
		}
	}
	if userDictOID == 0 {
		t.Fatal("ts_copy_dict not found via ListUserTSDicts")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_copy_dst (COPY = ts_copy_src)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION (COPY): %v", err)
	}

	var srcCfg, dstCfg *catalog.UserTSConfig
	for _, uc := range im.ListUserTSConfigs(catalog.DefaultDBOid) {
		switch uc.Name {
		case "ts_copy_src":
			srcCfg = uc
		case "ts_copy_dst":
			dstCfg = uc
		}
	}
	if srcCfg == nil || dstCfg == nil {
		t.Fatal("expected both ts_copy_src and ts_copy_dst to exist")
	}
	if dstCfg.Parser != srcCfg.Parser {
		t.Errorf("dstCfg.Parser = %d, want %d (copied from source)", dstCfg.Parser, srcCfg.Parser)
	}
	if dstCfg.OID == srcCfg.OID {
		t.Fatal("copy must allocate a distinct OID, not alias the source")
	}

	want := []uint32{userDictOID, catalog.BuiltinTSDictOID["simple"]}
	got := mappingDictOIDs(im, "ts_copy_dst", "asciiword")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("asciiword mapping DictOIDs = %v, want %v (copied)", got, want)
	}
	got = mappingDictOIDs(im, "ts_copy_dst", "word")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("word mapping DictOIDs = %v, want %v (copied)", got, want)
	}

	// Mutating the copy's mapping afterward must not perturb the source
	// (the mapping is copied by value, not shared).
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH CONFIGURATION ts_copy_dst ALTER MAPPING FOR asciiword WITH simple`); err != nil {
		t.Fatalf("ALTER MAPPING on copy: %v", err)
	}
	got = mappingDictOIDs(im, "ts_copy_src", "asciiword")
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("source asciiword mapping perturbed by copy mutation: got %v, want %v", got, want)
	}

	// PARSER and COPY are mutually exclusive (42601, mirroring
	// DefineTSConfiguration's ERRCODE_SYNTAX_ERROR).
	err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_copy_bad (PARSER = default, COPY = ts_copy_src)`)
	if err == nil {
		t.Fatal("specifying both PARSER and COPY should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42601" {
		t.Errorf("err = %v, want *ExecError{Code: 42601}", err)
	}

	// Unknown source configuration raises 42704.
	err = runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION ts_copy_nosrc (COPY = nosuchcfg)`)
	if err == nil {
		t.Fatal("COPY from an unknown configuration should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestCreateTSConfigCopyBuiltinLanguage guards the M0134-0068 fallback: real
// PG's initdb seeds one pg_ts_config row per snowball language (`english`,
// etc. — postgres/src/backend/snowball/snowball_create.sql), which goopg does
// not model, so `COPY = english` must not 42704 like an unknown user
// configuration. drop_if_exists.sql relies on `CREATE TEXT SEARCH
// CONFIGURATION test_tsconfig_exists (COPY=english)` succeeding.
func TestCreateTSConfigCopyBuiltinLanguage(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION x (COPY=english)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH CONFIGURATION (COPY=english): %v", err)
	}

	var xCfg *catalog.UserTSConfig
	for _, uc := range im.ListUserTSConfigs(catalog.DefaultDBOid) {
		if uc.Name == "x" {
			xCfg = uc
		}
	}
	if xCfg == nil {
		t.Fatal("expected configuration x to exist")
	}
	if xCfg.Parser != catalog.BuiltinTSParserOID["default"] {
		t.Errorf("x.Parser = %d, want default parser %d", xCfg.Parser, catalog.BuiltinTSParserOID["default"])
	}
	if len(xCfg.Mappings) != 0 {
		t.Errorf("x.Mappings = %v, want empty (no snowball stemming modeled)", xCfg.Mappings)
	}

	if err := runDDL(t, ctx, `DROP TEXT SEARCH CONFIGURATION x`); err != nil {
		t.Fatalf("DROP TEXT SEARCH CONFIGURATION x: %v", err)
	}

	// A genuinely unknown COPY source (not a user config, not a builtin
	// language name) must still 42704 — the fallback must not swallow real
	// "does not exist" errors.
	err := runDDL(t, ctx, `CREATE TEXT SEARCH CONFIGURATION y (COPY=not_a_real_language)`)
	if err == nil {
		t.Fatal("COPY from a nonexistent name should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
