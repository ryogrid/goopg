package initdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GUCSetting is a single name=value GUC override seeded into
// postgresql.conf at init time, mirroring one upstream initdb -c/--set
// pair (src/bin/initdb/initdb.c, case 'c').
type GUCSetting struct {
	Name  string
	Value string
}

// seedPostgresqlConf applies the optional --text-search-config (-T),
// --allow-group-access (-g) log_file_mode, and --set (-c) GUC overrides to
// the freshly written <abs>/postgresql.conf, mirroring upstream initdb's
// setup_config (src/bin/initdb/initdb.c:1283).
//
// Upstream order is preserved: default_text_search_config is written
// first (initdb.c:1343-1346, always as the value 'pg_catalog.<cfg>'), then
// log_file_mode is set to 0640 when group access is enabled
// (initdb.c:1421-1425), then every -c/--set override is applied last
// (initdb.c:1430-1436) so a -c switch can override an earlier assignment —
// including default_text_search_config or log_file_mode. Each replacement
// uses the same replace_guc_value algorithm: an existing (possibly
// commented-out) assignment line is rewritten in place, otherwise a new
// line is appended.
func seedPostgresqlConf(abs, tsConfig, passwordEncryption string, allowGroupAccess bool, localeGUCs, extra []GUCSetting) error {
	if tsConfig == "" && passwordEncryption == "" && !allowGroupAccess && len(localeGUCs) == 0 && len(extra) == 0 {
		return nil
	}
	path := filepath.Join(abs, "postgresql.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read postgresql.conf: %w", err)
	}
	text := string(data)
	hadFinalNewline := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")

	// lc_messages/lc_monetary/lc_numeric/lc_time are written first in
	// upstream setup_config (initdb.c:1351-1366), before
	// default_text_search_config and before the -c/--set loop so an
	// explicit -c override still wins.
	for _, g := range localeGUCs {
		lines = replaceGUCValue(lines, g.Name, g.Value)
	}

	if tsConfig != "" {
		// initdb.c:1343 always prefixes the catalog schema.
		lines = replaceGUCValue(lines, "default_text_search_config", "pg_catalog."+tsConfig)
	}
	if passwordEncryption != "" {
		// initdb.c:1402-1413: md5 is chosen as the encryption when an md5
		// auth method was selected (unless scram-sha-256 was chosen for the
		// other side). Applied before the -c/--set loop so an explicit
		// override still wins.
		lines = replaceGUCValue(lines, "password_encryption", passwordEncryption)
	}
	if allowGroupAccess {
		// initdb.c:1421-1425: when the cluster allows group access, log
		// files should too, else a group-member backup would fail on
		// non-relocated logs. Written unquoted as 0640 (replace_guc_value
		// quote=false).
		lines = replaceGUCValue(lines, "log_file_mode", "0640")
	}
	for _, g := range extra {
		lines = replaceGUCValue(lines, g.Name, g.Value)
	}

	out := strings.Join(lines, "\n")
	if hadFinalNewline {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return fmt.Errorf("write postgresql.conf: %w", err)
	}
	return nil
}

// replaceGUCValue rewrites the first line assigning to name with
// "name = <formatted value>", or appends such a line if none matches,
// mirroring upstream initdb's replace_guc_value (initdb.c:526). lines are
// the logical lines of postgresql.conf with their trailing newlines
// stripped.
//
// A matching line is one that, after skipping leading '#' and whitespace,
// begins with name (case-insensitively) followed by optional whitespace
// and '='. This intentionally matches commented-out template entries
// (e.g. "#default_statistics_target = 100") so the seeded value lands in
// place. The canonical casing shown in the file is preserved, as is any
// inline comment that trails the value.
func replaceGUCValue(lines []string, name, value string) []string {
	for i, line := range lines {
		// Skip leading '#' and whitespace (initdb.c: while *where == '#'
		// || isspace(*where)).
		j := 0
		for j < len(line) && (line[j] == '#' || line[j] == ' ' || line[j] == '\t') {
			j++
		}
		if j+len(name) > len(line) {
			continue
		}
		if !strings.EqualFold(line[j:j+len(name)], name) {
			continue
		}
		k := j + len(name)
		for k < len(line) && (line[k] == ' ' || line[k] == '\t') {
			k++
		}
		if k >= len(line) || line[k] != '=' {
			continue
		}
		// Matched: reuse the canonical casing from the file.
		fileName := line[j : j+len(name)]
		newline := fileName + " = " + formatGUCValue(value)
		// Preserve a trailing inline comment after '=' (initdb.c:
		// strrchr(where, '#') searching from the '='); the upstream
		// tab-column re-indentation is cosmetic and collapsed to a
		// single space here.
		if c := strings.LastIndexByte(line[k:], '#'); c >= 0 {
			newline += " " + line[k+c:]
		}
		lines[i] = newline
		return lines
	}
	// No match: append a new entry (initdb.c: realloc + append).
	return append(lines, name+" = "+formatGUCValue(value))
}

// formatGUCValue renders a GUC value for postgresql.conf, quoting and
// escaping it when required, mirroring upstream initdb's
// guc_value_requires_quotes + escape_quotes (initdb.c:539-545, 644).
func formatGUCValue(v string) string {
	if gucValueRequiresQuotes(v) {
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
	}
	return v
}

const (
	gucLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	gucDigits  = "0123456789"
)

// gucValueRequiresQuotes mirrors initdb.c:644 guc_value_requires_quotes:
// a bare identifier (letter then letters/digits) or a number with an
// optional unit suffix may be written unquoted; everything else must be
// quoted.
func gucValueRequiresQuotes(v string) bool {
	if v == "" {
		return true // empty string must be quoted
	}
	if strings.IndexByte(gucLetters, v[0]) >= 0 {
		// Identifier iff every char is a letter or digit.
		return strings.Trim(v, gucLetters+gucDigits) != ""
	}
	if strings.IndexByte(gucDigits, v[0]) >= 0 {
		// Digits followed by zero or more unit letters is a number.
		rest := strings.TrimLeft(v, gucDigits)
		return strings.Trim(rest, gucLetters) != ""
	}
	return true // all else must be quoted
}
