package datetime

import (
	"embed"
	"fmt"
	"path"
	"strconv"
	"strings"
	"sync"
)

// M0149-0001h1a82: the timezone_abbreviations setting.
//
// PG loads the abbreviation set the setting names from
// share/timezonesets (load_tzoffsets, tzparser.c). The three loadable files
// PG ships — Default, Australia and India — are embedded here; the *.txt
// files are reference material that no shipped set includes. A file is a
// list of `ABBREV offset [D]` or `ABBREV zone` lines, `@INCLUDE name` lines
// that splice another file in, and an `@OVERRIDE` line that lets this file's
// entries replace included ones; without it a conflicting entry is an error
// (addToArray).

//go:embed tznames/Default tznames/Australia tznames/India
var tznamesFS embed.FS

var (
	abbrevSetMu    sync.Mutex
	abbrevSetCache = map[string]map[string]zoneAbbrev{}
)

// ValidAbbrevSet reports whether name is a loadable abbreviation set, the
// check_timezone_abbreviations GUC hook.
func ValidAbbrevSet(name string) error {
	_, err := abbrevSet(name)
	return err
}

// abbrevSet returns the abbreviations of the named set, loading and caching
// it on first use. "" and "Default" are the Default set.
func abbrevSet(name string) (map[string]zoneAbbrev, error) {
	if name == "" || name == "Default" {
		return defaultZoneAbbrevs, nil
	}
	abbrevSetMu.Lock()
	defer abbrevSetMu.Unlock()
	if m, ok := abbrevSetCache[name]; ok {
		return m, nil
	}
	m := map[string]zoneAbbrev{}
	if err := parseTzFile(name, 0, m); err != nil {
		return nil, err
	}
	abbrevSetCache[name] = m
	return m, nil
}

// parseTzFile is tzparser.c's ParseTzFile: it adds the file's entries to m.
func parseTzFile(name string, depth int, m map[string]zoneAbbrev) error {
	// Names are restricted to letters, digits and '_' (no path or '.'), and
	// @INCLUDE nests at most three levels deep.
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("invalid time zone file name \"%s\"", name)
		}
	}
	if depth > 3 {
		return fmt.Errorf("time zone file recursion limit exceeded in file \"%s\"", name)
	}
	data, err := tznamesFS.ReadFile(path.Join("tznames", name))
	if err != nil {
		return fmt.Errorf("time zone file \"%s\" does not exist", name)
	}
	override := false
	for lineno, raw := range strings.Split(string(data), "\n") {
		line := raw
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case strings.EqualFold(fields[0], "@INCLUDE"):
			if len(fields) < 2 {
				return fmt.Errorf("@INCLUDE without file name in time zone file \"%s\", line %d", name, lineno+1)
			}
			if err := parseTzFile(fields[1], depth+1, m); err != nil {
				return err
			}
			continue
		case strings.EqualFold(fields[0], "@OVERRIDE"):
			override = true
			continue
		}
		if len(fields) < 2 {
			return fmt.Errorf("missing time zone offset in time zone file \"%s\", line %d", name, lineno+1)
		}
		abbr := strings.ToLower(fields[0])
		var e zoneAbbrev
		if c := fields[1][0]; c >= '0' && c <= '9' || c == '+' || c == '-' {
			off, err := strconv.Atoi(fields[1])
			if err != nil {
				return fmt.Errorf("invalid number for time zone offset in time zone file \"%s\", line %d", name, lineno+1)
			}
			e = zoneAbbrev{kind: tokTZ, offset: off}
			if len(fields) > 2 && strings.EqualFold(fields[2], "D") {
				e.kind = tokDTZ
			}
		} else {
			e = zoneAbbrev{kind: tokDynTZ, zone: fields[1]}
		}
		if old, ok := m[abbr]; ok && old != e && !override {
			return fmt.Errorf("time zone abbreviation \"%s\" is multiply defined", abbr)
		}
		m[abbr] = e
	}
	return nil
}
