package initdb

import (
	"strings"
	"testing"
)

// TestResolveLocaleProvider covers the --locale-provider parsing
// (initdb.c:3367) including the unrecognized-value rejection.
func TestResolveLocaleProvider(t *testing.T) {
	cases := []struct {
		in      string
		want    byte
		wantErr bool
	}{
		{"", collProviderLibc, false},
		{"libc", collProviderLibc, false},
		{"builtin", collProviderBuiltin, false},
		{"icu", collProviderICU, false},
		{"xyz", 0, true},
		{"LIBC", 0, true}, // case-sensitive, like initdb
	}
	for _, c := range cases {
		got, err := resolveLocaleProvider(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("resolveLocaleProvider(%q): want error, got provider %q", c.in, string(got))
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveLocaleProvider(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("resolveLocaleProvider(%q) = %q, want %q", c.in, string(got), string(c.want))
		}
	}
}

// TestPgGetEncodingFromLocale checks the codeset-suffix mapping that backs
// check_locale_encoding.
func TestPgGetEncodingFromLocale(t *testing.T) {
	cases := []struct {
		in   string
		want int32
	}{
		{"C", pgEncSQLASCII},
		{"POSIX", pgEncSQLASCII},
		{"", pgEncSQLASCII},
		{"C.UTF-8", pgEncUTF8},
		{"en_US.UTF-8", pgEncUTF8},
		{"en_US.ISO-8859-1", pgEncLATIN1},
		{"de_DE.UTF-8@euro", pgEncUTF8}, // @modifier stripped
		{"xx_YY", -1},                   // no codeset → unknown
		{"xx_YY.bogus", -1},             // unrecognized codeset → unknown
	}
	for _, c := range cases {
		if got := pgGetEncodingFromLocale(c.in); got != c.want {
			t.Errorf("pgGetEncodingFromLocale(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestResolveLocaleLibcDefault verifies the no-option default reproduces a
// fresh libc / "C" cluster (the exact values bootstrapPostgresDatabase wrote
// before this option family existed).
func TestResolveLocaleLibcDefault(t *testing.T) {
	ls, err := resolveLocale(Options{}, pgEncUTF8)
	if err != nil {
		t.Fatalf("resolveLocale default: %v", err)
	}
	if ls.provider != collProviderLibc {
		t.Errorf("provider = %q, want libc", string(ls.provider))
	}
	if ls.collate != "C" || ls.ctype != "C" {
		t.Errorf("collate/ctype = %q/%q, want C/C", ls.collate, ls.ctype)
	}
	if ls.datlocale != "" {
		t.Errorf("datlocale = %q, want empty (NULL) for libc", ls.datlocale)
	}
	if ls.specified {
		t.Errorf("specified = true, want false when no option given")
	}
	if ls.localeGUCSettings() != nil {
		t.Errorf("localeGUCSettings = %v, want nil when no option given", ls.localeGUCSettings())
	}
}

// TestResolveLocaleLibcExplicit checks --locale / --lc-* propagation under the
// libc provider, including datcollate/datctype and the lc_* GUC seeding.
func TestResolveLocaleLibcExplicit(t *testing.T) {
	ls, err := resolveLocale(Options{Locale: "C", LCMessages: "C"}, pgEncUTF8)
	if err != nil {
		t.Fatalf("resolveLocale: %v", err)
	}
	if ls.provider != collProviderLibc {
		t.Errorf("provider = %q, want libc", string(ls.provider))
	}
	if ls.datlocale != "" {
		t.Errorf("datlocale = %q, want empty (libc records no datlocale)", ls.datlocale)
	}
	if !ls.specified {
		t.Errorf("specified = false, want true")
	}
	gucs := ls.localeGUCSettings()
	if len(gucs) != 4 {
		t.Fatalf("localeGUCSettings len = %d, want 4 (%v)", len(gucs), gucs)
	}
}

// TestResolveLocaleBuiltin covers the builtin provider success paths and its
// canonicalization (C.UTF8 → C.UTF-8).
func TestResolveLocaleBuiltin(t *testing.T) {
	// builtin + --locale C → datlocale "C", provider 'b'.
	ls, err := resolveLocale(Options{LocaleProvider: "builtin", Locale: "C"}, pgEncUTF8)
	if err != nil {
		t.Fatalf("builtin --locale C: %v", err)
	}
	if ls.provider != collProviderBuiltin || ls.datlocale != "C" {
		t.Errorf("provider/datlocale = %q/%q, want b/C", string(ls.provider), ls.datlocale)
	}

	// builtin + --builtin-locale C.UTF8 + UTF8 encoding → canonical C.UTF-8.
	ls, err = resolveLocale(Options{LocaleProvider: "builtin", BuiltinLocale: "C.UTF8", LCCollate: "C", LCCtype: "C"}, pgEncUTF8)
	if err != nil {
		t.Fatalf("builtin C.UTF8: %v", err)
	}
	if ls.datlocale != "C.UTF-8" {
		t.Errorf("datlocale = %q, want canonical C.UTF-8", ls.datlocale)
	}
}

// TestResolveLocaleErrors covers every rejection path: bad provider, missing
// locale for non-libc, option-combination mismatches, ICU not built, invalid
// builtin locale, and the builtin C.UTF-8/encoding requirement.
func TestResolveLocaleErrors(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		enc  int32
		want string
	}{
		{"bad provider", Options{LocaleProvider: "xyz"}, pgEncUTF8, "unrecognized locale provider"},
		{"builtin needs locale", Options{LocaleProvider: "builtin"}, pgEncUTF8, "locale must be specified if provider is builtin"},
		{"icu not built", Options{LocaleProvider: "icu", ICULocale: "en"}, pgEncUTF8, "ICU is not supported in this build"},
		{"icu needs locale", Options{LocaleProvider: "icu"}, pgEncUTF8, "locale must be specified if provider is icu"},
		{"builtin-locale wrong provider", Options{BuiltinLocale: "C"}, pgEncUTF8, "--builtin-locale cannot be specified"},
		{"icu-locale wrong provider", Options{LocaleProvider: "libc", ICULocale: "en"}, pgEncUTF8, "--icu-locale cannot be specified"},
		{"icu-rules wrong provider", Options{ICURules: "x"}, pgEncUTF8, "--icu-rules cannot be specified"},
		{"invalid builtin locale", Options{LocaleProvider: "builtin", BuiltinLocale: "fr_FR"}, pgEncUTF8, "invalid locale name"},
		{"builtin C.UTF-8 needs UTF8", Options{LocaleProvider: "builtin", BuiltinLocale: "C.UTF-8", LCCollate: "C", LCCtype: "C"}, pgEncSQLASCII, "requires encoding \"UTF-8\""},
	}
	for _, c := range cases {
		_, err := resolveLocale(c.opts, c.enc)
		if err == nil {
			t.Errorf("%s: want error containing %q, got nil", c.name, c.want)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %q, want containing %q", c.name, err.Error(), c.want)
		}
	}
}
