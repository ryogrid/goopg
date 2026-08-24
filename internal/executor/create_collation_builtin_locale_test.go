package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreateCollationBuiltinLocaleValidation guards the M0134-0102 fix:
// CREATE COLLATION ... (provider = builtin, locale = '...') previously
// accepted any locale spelling, so an invalid name (e.g. "unicode", the
// PostgreSQL enum-value-shaped name people write instead of the actual
// locale spelling "PG_UNICODE_FAST") silently registered a bogus collation
// instead of raising 22023 "invalid locale name ... for builtin provider"
// (pg_locale.c builtin_validate_locale, called from DefineCollation in
// collationcmds.c before the pg_collation insert). collate.utf8.sql relies
// on the reject: a follow-up CREATE COLLATION with the correct locale name
// re-uses the same collation name and expects "already exists" only because
// the first (invalid) attempt failed — goopg previously let the first one
// through, so the second one's "already exists" collided on the WRONG
// definition.
func TestCreateCollationBuiltinLocaleValidation(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatal("catalog is not *InMemory")
	}

	t.Run("invalid locale name rejected", func(t *testing.T) {
		err := runDDL(t, ctx, `CREATE COLLATION regress_bad (provider = builtin, locale = 'unicode')`)
		if err == nil {
			t.Fatal("expected error for invalid builtin locale name \"unicode\", got nil")
		}
		ee, ok := err.(*ExecError)
		if !ok {
			t.Fatalf("error is %T, want *ExecError", err)
		}
		if ee.Code != "22023" {
			t.Errorf("Code = %q, want 22023", ee.Code)
		}
		if !strings.Contains(ee.Message, `invalid locale name "unicode" for builtin provider`) {
			t.Errorf("Message = %q, want PG's builtin_validate_locale wording", ee.Message)
		}
		if _, found := im.CollationAttrsByName("regress_bad"); found {
			t.Error("collation registered despite rejection — must not leave a catalog entry behind")
		}
	})

	t.Run("PG_C_UTF8-style underscore spelling rejected", func(t *testing.T) {
		err := runDDL(t, ctx, `CREATE COLLATION regress_bad2 (provider = builtin, locale = 'C_UTF8')`)
		if err == nil {
			t.Fatal("expected error for invalid builtin locale name \"C_UTF8\", got nil")
		}
	})

	t.Run("canonical spellings accepted", func(t *testing.T) {
		for _, tc := range []struct {
			name, locale, wantLocale string
		}{
			{"regress_ok_c", "C", "C"},
			{"regress_ok_dot", "C.UTF-8", "C.UTF-8"},
			{"regress_ok_nodot", "C.UTF8", "C.UTF-8"}, // canonicalized
			{"regress_ok_fast", "PG_UNICODE_FAST", "PG_UNICODE_FAST"},
		} {
			if err := runDDL(t, ctx, `CREATE COLLATION `+tc.name+` (provider = builtin, locale = '`+tc.locale+`')`); err != nil {
				t.Fatalf("CREATE COLLATION %s locale=%s: unexpected error: %v", tc.name, tc.locale, err)
			}
			uc, found := im.CollationAttrsByName(tc.name)
			if !found {
				t.Fatalf("collation %s not registered after successful CREATE", tc.name)
			}
			if uc.Locale != tc.wantLocale {
				t.Errorf("%s: Locale = %q, want %q", tc.name, uc.Locale, tc.wantLocale)
			}
		}
	})
}
