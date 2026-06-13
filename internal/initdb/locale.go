package initdb

import (
	"fmt"
	"strings"
)

// Locale + locale-provider handling for `goopg init`, ported from the
// validation logic in PostgreSQL's src/bin/initdb/initdb.c (setlocales,
// setup_locale_encoding, check_locale_encoding, and the main() option-
// combination checks).
//
// initdb records, per the chosen collation provider, the lc_* settings and
// the per-database collation into pg_database (datlocprovider, datcollate,
// datctype, datlocale). goopg's server runs with a fixed C / UTF8 locale, so
// these options affect only the on-disk catalog (PG-compat surface, like the
// -E/--encoding work in encoding.go and the --pwfile verifier) — the running
// engine does not change collation behavior. What this file faithfully
// reproduces is initdb's acceptance/rejection of the option combinations and
// the values written into pg_database.
//
// Scope: the libc and builtin providers. The icu provider is recognized for
// validation but rejected ("ICU is not supported in this build"), mirroring an
// initdb compiled without USE_ICU. The locale-derived default encoding
// (pg_get_encoding_from_locale on an unset encoding) is still out of scope —
// goopg's fixed C locale makes the default UTF8 (see encoding.go).

// collprovider byte codes match Form_pg_database.datlocprovider
// (postgres/src/include/catalog/pg_collation.h: COLLPROVIDER_LIBC='c',
// COLLPROVIDER_BUILTIN='b', COLLPROVIDER_ICU='i').
const (
	collProviderLibc    byte = 'c'
	collProviderBuiltin byte = 'b'
	collProviderICU     byte = 'i'
)

// localeSettings is the resolved per-cluster locale configuration produced by
// resolveLocale and consumed by bootstrapPostgresDatabase (pg_database row)
// and Init (lc_* GUC seeding).
type localeSettings struct {
	provider  byte   // datlocprovider: 'c' libc / 'b' builtin / 'i' icu
	collate   string // datcollate (text, NOT NULL)
	ctype     string // datctype   (text, NOT NULL)
	datlocale string // datlocale; "" → NULL (libc records no datlocale)
	messages  string // lc_messages GUC
	monetary  string // lc_monetary GUC
	numeric   string // lc_numeric GUC
	time      string // lc_time GUC
	// specified is true when the user passed any locale option, so Init
	// knows to seed the lc_* GUCs into postgresql.conf (when false the
	// template's commented defaults are left untouched, matching the
	// established -T/--set "no-op when unset" pattern).
	specified bool
}

// collproviderName mirrors initdb.c collprovider_name (the spelling used in
// user-facing diagnostics).
func collproviderName(p byte) string {
	switch p {
	case collProviderBuiltin:
		return "builtin"
	case collProviderICU:
		return "icu"
	default:
		return "libc"
	}
}

// resolveLocaleProvider ports the --locale-provider parsing in initdb.c:3367
// (case OPT_LOCALE_PROVIDER). An empty value defaults to libc.
func resolveLocaleProvider(name string) (byte, error) {
	switch name {
	case "", "libc":
		return collProviderLibc, nil
	case "builtin":
		return collProviderBuiltin, nil
	case "icu":
		return collProviderICU, nil
	default:
		return 0, fmt.Errorf("goopg init: unrecognized locale provider: %s", name)
	}
}

// pgGetEncodingFromLocale is a faithful-enough port of
// pg_get_encoding_from_locale for goopg, which (unlike libpq) cannot call
// setlocale to interrogate the host. "C"/"POSIX"/empty map to SQL_ASCII (the
// codeset-less locales); any other name's encoding is taken from its `.CODESET`
// suffix (with an optional `@modifier` stripped). An unrecognized or absent
// codeset yields -1 ("unknown"), which check_locale_encoding treats as
// compatible — matching upstream's behavior for locales whose codeset PG does
// not map to a server encoding.
func pgGetEncodingFromLocale(locale string) int32 {
	switch locale {
	case "", "C", "POSIX":
		return pgEncSQLASCII
	}
	if dot := strings.LastIndexByte(locale, '.'); dot >= 0 {
		codeset := locale[dot+1:]
		if at := strings.IndexByte(codeset, '@'); at >= 0 {
			codeset = codeset[:at]
		}
		if enc := pgValidServerEncoding(codeset); enc >= 0 {
			return enc
		}
	}
	return -1
}

// checkLocaleEncoding ports initdb.c check_locale_encoding (initdb.c:2265): the
// chosen encoding must agree with the encoding implied by the lc_collate /
// lc_ctype locale, except that SQL_ASCII (on either side) and an unknown locale
// encoding are always accepted. Returns true when the combination is allowed.
func checkLocaleEncoding(locale string, userEnc int32) bool {
	localeEnc := pgGetEncodingFromLocale(locale)
	return localeEnc == userEnc ||
		localeEnc == pgEncSQLASCII ||
		localeEnc == -1 ||
		userEnc == pgEncSQLASCII
}

// resolveLocale ports the locale resolution + validation initdb performs after
// option parsing: the option-combination checks (initdb.c:3424-3434),
// setlocales (initdb.c:2424) including the "locale must be specified" and
// builtin-locale canonicalization rules, and setup_encoding's
// check_locale_encoding + builtin-UTF8 requirement (initdb.c:2772-2783).
//
// encodingID is the already-resolved default encoding (resolveEncoding); it is
// needed for the encoding-compatibility checks. All errors abort before any
// filesystem layout, matching initdb's pg_fatal during option processing.
func resolveLocale(opts Options, encodingID int32) (localeSettings, error) {
	provider, err := resolveLocaleProvider(opts.LocaleProvider)
	if err != nil {
		return localeSettings{}, err
	}

	// Option-combination checks (initdb.c:3424-3434): the provider-specific
	// locale switches are only legal with their matching provider.
	if opts.BuiltinLocale != "" && provider != collProviderBuiltin {
		return localeSettings{}, fmt.Errorf("goopg init: %s cannot be specified unless locale provider %q is chosen", "--builtin-locale", "builtin")
	}
	if opts.ICULocale != "" && provider != collProviderICU {
		return localeSettings{}, fmt.Errorf("goopg init: %s cannot be specified unless locale provider %q is chosen", "--icu-locale", "icu")
	}
	if opts.ICURules != "" && provider != collProviderICU {
		return localeSettings{}, fmt.Errorf("goopg init: %s cannot be specified unless locale provider %q is chosen", "--icu-rules", "icu")
	}

	// setlocales(): each unset lc_* falls back to --locale, then to "C"
	// (goopg's fixed locale; upstream would canonicalize from the
	// environment via setlocale, which goopg cannot do).
	pick := func(specific string) string {
		if specific != "" {
			return specific
		}
		if opts.Locale != "" {
			return opts.Locale
		}
		return "C"
	}
	ls := localeSettings{
		provider: provider,
		collate:  pick(opts.LCCollate),
		ctype:    pick(opts.LCCtype),
		messages: pick(opts.LCMessages),
		monetary: pick(opts.LCMonetary),
		numeric:  pick(opts.LCNumeric),
		time:     pick(opts.LCTime),
	}

	// datlocale source (initdb.c:2444 + the OPT_*_LOCALE cases): for a
	// non-libc provider it comes from --builtin-locale / --icu-locale, or
	// from --locale when neither was given.
	datlocale := ""
	switch provider {
	case collProviderBuiltin:
		datlocale = opts.BuiltinLocale
	case collProviderICU:
		datlocale = opts.ICULocale
	}
	if datlocale == "" && opts.Locale != "" && provider != collProviderLibc {
		datlocale = opts.Locale
	}

	// "locale must be specified if provider is <name>" (initdb.c:2471).
	if provider != collProviderLibc && datlocale == "" {
		return localeSettings{}, fmt.Errorf("goopg init: locale must be specified if provider is %s", collproviderName(provider))
	}

	switch provider {
	case collProviderBuiltin:
		// Builtin canonicalization (initdb.c:2477-2488).
		switch datlocale {
		case "C":
			datlocale = "C"
		case "C.UTF-8", "C.UTF8":
			datlocale = "C.UTF-8"
		case "PG_UNICODE_FAST":
			datlocale = "PG_UNICODE_FAST"
		default:
			return localeSettings{}, fmt.Errorf("goopg init: invalid locale name %q for builtin provider", datlocale)
		}
	case collProviderICU:
		// goopg is built without ICU (USE_ICU undefined), so the provider
		// is rejected exactly where initdb.c:2503 does (#ifndef USE_ICU).
		return localeSettings{}, fmt.Errorf("goopg init: ICU is not supported in this build")
	}
	ls.datlocale = datlocale

	// setup_encoding (initdb.c:2772): the encoding must be compatible with
	// the lc_ctype and lc_collate locales.
	if !checkLocaleEncoding(ls.ctype, encodingID) || !checkLocaleEncoding(ls.collate, encodingID) {
		return localeSettings{}, fmt.Errorf("goopg init: encoding mismatch: the selected encoding (%s) and the encoding of the selected locale do not match",
			pgEncodingToChar(encodingID))
	}
	// Builtin C.UTF-8 / PG_UNICODE_FAST require UTF8 (initdb.c:2778-2783).
	if provider == collProviderBuiltin &&
		(datlocale == "C.UTF-8" || datlocale == "PG_UNICODE_FAST") &&
		encodingID != pgEncUTF8 {
		return localeSettings{}, fmt.Errorf("goopg init: builtin provider locale %q requires encoding %q", datlocale, "UTF-8")
	}

	ls.specified = opts.LocaleProvider != "" || opts.Locale != "" ||
		opts.LCCollate != "" || opts.LCCtype != "" || opts.LCMessages != "" ||
		opts.LCMonetary != "" || opts.LCNumeric != "" || opts.LCTime != "" ||
		opts.BuiltinLocale != ""
	return ls, nil
}

// localeGUCSettings returns the lc_* GUC overrides to seed into
// postgresql.conf, mirroring the lc_messages/lc_monetary/lc_numeric/lc_time
// assignments in initdb.c setup_config (initdb.c:1351-1366). Returns nil when
// no locale option was specified, leaving the template's commented defaults
// untouched (lc_collate/lc_ctype are per-database in modern PG, recorded in
// pg_database rather than postgresql.conf, so they are intentionally absent).
func (ls localeSettings) localeGUCSettings() []GUCSetting {
	if !ls.specified {
		return nil
	}
	return []GUCSetting{
		{Name: "lc_messages", Value: ls.messages},
		{Name: "lc_monetary", Value: ls.monetary},
		{Name: "lc_numeric", Value: ls.numeric},
		{Name: "lc_time", Value: ls.time},
	}
}
