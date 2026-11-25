package misc

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// lc_monetary / lc_numeric / lc_time / lc_messages validation
// (M0149-0001k5b). PG's check_locale (pg_locale.c) accepts a value iff
// setlocale() can switch the category to it, so a SET of a locale the host
// does not have fails with 22023 "invalid value for parameter "lc_monetary":
// "de_DE.UTF-8"". goopg accepted any string. This reproduces setlocale's
// availability answer for glibc hosts: the C / POSIX / C.UTF-8 built-ins, the
// empty string (inherit from the environment), and every locale glibc can
// load — a compiled directory under the locale path or an entry in
// locale-archive — with the codeset normalised the way glibc's
// _nl_normalize_codeset does ("UTF-8" → "utf8").

// localeDir is glibc's compiled-locale directory (LOCALEDIR /usr/lib/locale).
var localeDir = "/usr/lib/locale"

var (
	archiveOnce  sync.Once
	archiveNames map[string]bool
)

// normalizeLocaleName lower-cases and strips non-alphanumerics from the
// codeset part, as _nl_normalize_codeset does, and keeps language,
// territory and modifier verbatim.
func normalizeLocaleName(name string) string {
	base, mod, hasMod := strings.Cut(name, "@")
	lang, codeset, hasCodeset := strings.Cut(base, ".")
	if !hasCodeset {
		return name
	}
	var b strings.Builder
	allDigits := true
	for _, r := range codeset {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			allDigits = false
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			allDigits = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	cs := b.String()
	if allDigits {
		cs = "iso" + cs
	}
	out := lang + "." + cs
	if hasMod {
		out += "@" + mod
	}
	return out
}

// localeArchiveNames reads the locale names in glibc's locale-archive
// (struct locarhead: namehash_offset at 8, namehash_size at 16; each
// namehash entry is {hashval, name_offset, locrec_offset}).
func localeArchiveNames() map[string]bool {
	archiveOnce.Do(func() {
		archiveNames = map[string]bool{}
		data, err := os.ReadFile(filepath.Join(localeDir, "locale-archive"))
		if err != nil || len(data) < 32 {
			return
		}
		le := binary.LittleEndian
		hashOff := int(le.Uint32(data[8:12]))
		hashSize := int(le.Uint32(data[16:20]))
		for i := 0; i < hashSize; i++ {
			e := hashOff + i*12
			if e+12 > len(data) {
				break
			}
			nameOff := int(le.Uint32(data[e+4 : e+8]))
			if nameOff == 0 || nameOff >= len(data) {
				continue
			}
			end := nameOff
			for end < len(data) && data[end] != 0 {
				end++
			}
			archiveNames[string(data[nameOff:end])] = true
		}
	})
	return archiveNames
}

// LocaleAvailable reports whether setlocale would accept name for an LC_*
// category on this host.
func LocaleAvailable(name string) bool {
	switch name {
	case "", "C", "POSIX":
		return true
	}
	candidates := []string{name, normalizeLocaleName(name)}
	for _, c := range candidates {
		switch strings.ToLower(c) {
		case "c.utf8", "c.utf-8":
			return true // glibc's built-in C.UTF-8
		}
		if c == "" || strings.ContainsAny(c, "/") {
			continue
		}
		// glibc: with LOCPATH set, setlocale searches those directories
		// instead of the default directory and locale-archive.
		if lp := os.Getenv("LOCPATH"); lp != "" {
			for _, dir := range filepath.SplitList(lp) {
				if fi, err := os.Stat(filepath.Join(dir, c)); err == nil && fi.IsDir() {
					return true
				}
			}
			continue
		}
		if fi, err := os.Stat(filepath.Join(localeDir, c)); err == nil && fi.IsDir() {
			return true
		}
		if localeArchiveNames()[c] {
			return true
		}
	}
	return false
}

// localeCheck is the CheckFn of the lc_* string GUCs (check_locale_monetary /
// _numeric / _time / _messages).
func localeCheck(param string) func(string) error {
	return func(value string) error {
		if LocaleAvailable(value) {
			return nil
		}
		return fmt.Errorf("invalid value for parameter %q: %q", param, value)
	}
}
