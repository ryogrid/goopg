package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateTSDictOptionValidation guards the DU-002 CREATE TEXT SEARCH
// DICTIONARY option-validation follow-up (M0119-0004): real PG's
// verify_dictoptions (tsearchcmds.c) calls each template's own init function
// — dsimple_init/dsynonym_init/dispell_init/thesaurus_init — which rejects
// any option key it doesn't recognize with ERRCODE_INVALID_PARAMETER_VALUE
// (22023) and a template-specific message. goopg previously accepted any
// option name/value verbatim for any of the four built-in templates.
func TestCreateTSDictOptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		sql     string
		wantErr bool
		wantMsg string
	}{
		{"simple/stopwords ok", `CREATE TEXT SEARCH DICTIONARY ts_val_simple1 (TEMPLATE = simple, STOPWORDS = english)`, false, ""},
		{"simple/accept ok", `CREATE TEXT SEARCH DICTIONARY ts_val_simple2 (TEMPLATE = simple, ACCEPT = false)`, false, ""},
		{"simple/bogus rejected", `CREATE TEXT SEARCH DICTIONARY ts_val_simple3 (TEMPLATE = simple, BOGUS = english)`, true, `unrecognized simple dictionary parameter: "bogus"`},
		{"synonym/synonyms ok", `CREATE TEXT SEARCH DICTIONARY ts_val_syn1 (TEMPLATE = synonym, SYNONYMS = mysyns)`, false, ""},
		{"synonym/casesensitive ok", `CREATE TEXT SEARCH DICTIONARY ts_val_syn2 (TEMPLATE = synonym, CASESENSITIVE = true)`, false, ""},
		{"synonym/bogus rejected", `CREATE TEXT SEARCH DICTIONARY ts_val_syn3 (TEMPLATE = synonym, STOPWORDS = english)`, true, `unrecognized synonym parameter: "stopwords"`},
		{"ispell/dictfile+afffile+stopwords ok", `CREATE TEXT SEARCH DICTIONARY ts_val_ispell1 (TEMPLATE = ispell, DICTFILE = en, AFFFILE = en, STOPWORDS = english)`, false, ""},
		{"ispell/bogus rejected", `CREATE TEXT SEARCH DICTIONARY ts_val_ispell2 (TEMPLATE = ispell, ACCEPT = false)`, true, `unrecognized Ispell parameter: "accept"`},
		{"thesaurus/dictfile+dictionary ok", `CREATE TEXT SEARCH DICTIONARY ts_val_thes1 (TEMPLATE = thesaurus, DICTFILE = thes, DICTIONARY = english_stem)`, false, ""},
		{"thesaurus/bogus rejected", `CREATE TEXT SEARCH DICTIONARY ts_val_thes2 (TEMPLATE = thesaurus, STOPWORDS = english)`, true, `unrecognized Thesaurus parameter: "stopwords"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, cleanup := newDDLFixture(t)
			defer cleanup()

			err := runDDL(t, ctx, tc.sql)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			ee, ok := err.(*ExecError)
			if !ok || ee.Code != "22023" {
				t.Fatalf("err = %v, want *ExecError{Code: 22023}", err)
			}
			if ee.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", ee.Message, tc.wantMsg)
			}
		})
	}
}

// TestAlterTSDictOptionValidation guards the ALTER-side half of the same
// follow-up: AlterTSDictionary (tsearchcmds.c) re-validates the full
// post-merge option list via verify_dictoptions, not just the newly-added
// directives. A bare delete-only directive (no `= value`) for a key that was
// never a real option name is not an error, since it's never added back into
// the merged list — mirroring real PG's `if (defel->arg)` guard exactly.
func TestAlterTSDictOptionValidation(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	if err := runDDL(t, ctx, `CREATE TEXT SEARCH DICTIONARY ts_alter_val (TEMPLATE = simple, STOPWORDS = english)`); err != nil {
		t.Fatalf("CREATE TEXT SEARCH DICTIONARY: %v", err)
	}

	// Adding an option the "simple" template doesn't recognize is rejected,
	// and must NOT mutate the stored options (real PG validates before
	// persisting via verify_dictoptions, prior to the CatalogTupleUpdate).
	err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_alter_val (BOGUS = english)`)
	if err == nil {
		t.Fatal("expected an error for an unrecognized simple dictionary option")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "22023" {
		t.Fatalf("err = %v, want *ExecError{Code: 22023}", err)
	}
	var ud *catalog.UserTSDict
	for _, d := range im.ListUserTSDicts() {
		if d.Name == "ts_alter_val" {
			ud = d
		}
	}
	if ud == nil {
		t.Fatal("dictionary not found via ListUserTSDicts")
	}
	if want := `stopwords = 'english'`; ud.InitOption != want {
		t.Fatalf("InitOption after a rejected ALTER = %q, want unchanged %q", ud.InitOption, want)
	}

	// A bare (delete-only) directive naming a key that was never real is a
	// silent no-op, not a validation error — it never re-enters the merged
	// list that verify_dictoptions/ValidateTSDictOptions checks.
	if err := runDDL(t, ctx, `ALTER TEXT SEARCH DICTIONARY ts_alter_val (BOGUS)`); err != nil {
		t.Fatalf("bare delete-only directive for a never-set key should be a no-op, got: %v", err)
	}
}
