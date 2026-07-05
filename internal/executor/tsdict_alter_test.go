package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAlterTSDictRenameTo guards the DU-002 ALTER TEXT SEARCH DICTIONARY
// follow-up (M0119-0004): ALTER TEXT SEARCH DICTIONARY name RENAME TO
// newname was entirely unhandled (falling through to the discarded compat
// no-op), mirroring the ALTER TEXT SEARCH CONFIGURATION RENAME TO precedent
// (TestAlterTSConfigRenameTo).
func TestAlterTSDictRenameTo(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_ren_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_ren_dict RENAME TO ts_ren_dict2`); err != nil {
		t.Fatalf("ALTER ... RENAME TO: %v", err)
	}

	found := false
	for _, ud := range im.ListUserTSDicts() {
		if ud.Name == "ts_ren_dict2" {
			found = true
		}
		if ud.Name == "ts_ren_dict" {
			t.Error("old dictionary name still present after RENAME TO")
		}
	}
	if !found {
		t.Fatal("renamed dictionary not found via ListUserTSDicts")
	}

	// Renaming an unknown dictionary raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY nosuchdict RENAME TO whatever`)
	if err == nil {
		t.Fatal("ALTER TEXT SEARCH DICTIONARY RENAME TO on unknown dictionary should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTSDictSetSchema guards the DU-002 ALTER TEXT SEARCH DICTIONARY
// follow-up (M0119-0004): ALTER TEXT SEARCH DICTIONARY name SET SCHEMA
// newschema was entirely unhandled, mirroring
// TestAlterTSConfigSetSchema.
func TestAlterTSDictSetSchema(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE SCHEMA ts_dict_other_schema`); err != nil {
		t.Fatalf("CREATE SCHEMA: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_schema_dict (TEMPLATE = simple)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}

	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_schema_dict SET SCHEMA ts_dict_other_schema`); err != nil {
		t.Fatalf("ALTER ... SET SCHEMA: %v", err)
	}

	wantNsOID := im.SchemaOID("ts_dict_other_schema")
	if wantNsOID == 0 {
		t.Fatal(`SchemaOID("ts_dict_other_schema") = 0, want a real namespace OID`)
	}
	var ud *catalog.UserTSDict
	for _, d := range im.ListUserTSDicts() {
		if d.Name == "ts_schema_dict" {
			ud = d
		}
	}
	if ud == nil {
		t.Fatal("dictionary not found via ListUserTSDicts after SET SCHEMA")
	}
	if ud.NamespaceOID != wantNsOID {
		t.Errorf("NamespaceOID after SET SCHEMA = %d, want %d (ts_dict_other_schema)", ud.NamespaceOID, wantNsOID)
	}

	// Unknown dictionary raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY nosuchdict SET SCHEMA ts_dict_other_schema`)
	if err == nil {
		t.Fatal("ALTER TEXT SEARCH DICTIONARY SET SCHEMA on unknown dictionary should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}

// TestAlterTSDictOptions guards the DU-002 ALTER TEXT SEARCH DICTIONARY
// follow-up (M0119-0004): ALTER TEXT SEARCH DICTIONARY name
// ( key [= value] [, ...] ) was entirely unhandled. Mirrors
// AlterTSDictionary's own remove-then-maybe-add merge semantics
// (tsearchcmds.c): a `key = value` entry replaces (or adds) that key, a
// bare `key` entry removes it, and unrelated existing keys are left alone.
func TestAlterTSDictOptions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_opt_dict (TEMPLATE = simple, STOPWORDS = english)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}
	findDict := func(name string) *catalog.UserTSDict {
		for _, d := range im.ListUserTSDicts() {
			if d.Name == name {
				return d
			}
		}
		return nil
	}
	ud := findDict("ts_opt_dict")
	if ud == nil {
		t.Fatal("dictionary not found via ListUserTSDicts after CREATE")
	}
	if want := `stopwords = 'english'`; ud.InitOption != want {
		t.Fatalf("InitOption after CREATE = %q, want %q", ud.InitOption, want)
	}

	// Adding a second, independent option leaves STOPWORDS untouched.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_opt_dict (ACCEPT = false)`); err != nil {
		t.Fatalf("ALTER ... (ACCEPT = false): %v", err)
	}
	ud = findDict("ts_opt_dict")
	if want := `stopwords = 'english', accept = 'false'`; ud.InitOption != want {
		t.Fatalf("InitOption after adding ACCEPT = %q, want %q", ud.InitOption, want)
	}

	// Replacing an existing key's value.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_opt_dict (STOPWORDS = swedish)`); err != nil {
		t.Fatalf("ALTER ... (STOPWORDS = swedish): %v", err)
	}
	ud = findDict("ts_opt_dict")
	if want := `accept = 'false', stopwords = 'swedish'`; ud.InitOption != want {
		t.Fatalf("InitOption after replacing STOPWORDS = %q, want %q", ud.InitOption, want)
	}

	// A bare key removes it without touching the rest.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_opt_dict (ACCEPT)`); err != nil {
		t.Fatalf("ALTER ... (ACCEPT): %v", err)
	}
	ud = findDict("ts_opt_dict")
	if want := `stopwords = 'swedish'`; ud.InitOption != want {
		t.Fatalf("InitOption after removing ACCEPT = %q, want %q", ud.InitOption, want)
	}

	// Removing the last remaining option yields "" (NULL dictinitoption).
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_opt_dict (STOPWORDS)`); err != nil {
		t.Fatalf("ALTER ... (STOPWORDS): %v", err)
	}
	ud = findDict("ts_opt_dict")
	if ud.InitOption != "" {
		t.Fatalf("InitOption after removing all options = %q, want \"\"", ud.InitOption)
	}

	// Unknown dictionary raises 42704.
	err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY nosuchdict (STOPWORDS = english)`)
	if err == nil {
		t.Fatal("ALTER TEXT SEARCH DICTIONARY options on unknown dictionary should error")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42704" {
		t.Errorf("err = %v, want *ExecError{Code: 42704}", err)
	}
}
