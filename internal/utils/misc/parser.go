// Package config implements goopg's postgresql.conf parser and the GUC
// registry. Scope and growth path are documented in
// docs/design/0004-configuration-and-guc.md.
package misc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigEntry is one parsed `name = value` line. Order is preserved
// across the entire (possibly include-expanded) file set so that later
// values override earlier ones, matching upstream guc-file.l semantics.
type ConfigEntry struct {
	Name       string
	Value      string
	SourceFile string
	SourceLine int
}

// ParseError annotates failures with file/line context.
type ParseError struct {
	File string
	Line int
	Err  error
}

func (e ParseError) Error() string {
	if e.File == "" {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("%s:%d: %v", e.File, e.Line, e.Err)
}

func (e ParseError) Unwrap() error { return e.Err }

// ParseConfigFile reads a postgresql.conf-style file and returns the
// resolved entries in source order. include / include_if_exists /
// include_dir directives expand recursively; cycles error out.
func ParseConfigFile(path string) ([]ConfigEntry, error) {
	visited := map[string]bool{}
	var out []ConfigEntry
	if err := parseFileInto(path, false, &out, visited); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseConfigReader is the io.Reader form of ParseConfigFile, intended
// for tests. The label appears in SourceFile diagnostics.
func ParseConfigReader(r io.Reader, label string) ([]ConfigEntry, error) {
	visited := map[string]bool{}
	var out []ConfigEntry
	if err := parseStream(r, label, &out, visited); err != nil {
		return nil, err
	}
	return out, nil
}

func parseFileInto(path string, optional bool, out *[]ConfigEntry, visited map[string]bool) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if visited[abs] {
		return fmt.Errorf("include cycle detected at %s", abs)
	}
	visited[abs] = true
	defer delete(visited, abs)
	f, err := os.Open(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return parseStream(f, abs, out, visited)
}

func parseStream(r io.Reader, label string, out *[]ConfigEntry, visited map[string]bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		name, value, ok, err := parseConfigLine(raw)
		if err != nil {
			return ParseError{File: label, Line: lineNo, Err: err}
		}
		if !ok {
			continue
		}
		switch name {
		case "include", "include_if_exists":
			optional := name == "include_if_exists"
			resolved := resolveIncludePath(label, value)
			if err := parseFileInto(resolved, optional, out, visited); err != nil {
				return ParseError{File: label, Line: lineNo, Err: err}
			}
		case "include_dir":
			resolved := resolveIncludePath(label, value)
			if err := parseDirInto(resolved, out, visited); err != nil {
				return ParseError{File: label, Line: lineNo, Err: err}
			}
		default:
			*out = append(*out, ConfigEntry{
				Name:       strings.ToLower(name),
				Value:      value,
				SourceFile: label,
				SourceLine: lineNo,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	return nil
}

func parseDirInto(dir string, out *[]ConfigEntry, visited map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("include_dir %s: %w", dir, err)
	}
	var conf []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		conf = append(conf, filepath.Join(dir, e.Name()))
	}
	sort.Strings(conf)
	for _, p := range conf {
		if err := parseFileInto(p, false, out, visited); err != nil {
			return err
		}
	}
	return nil
}

func resolveIncludePath(parent, arg string) string {
	if filepath.IsAbs(arg) {
		return arg
	}
	if parent == "" {
		return arg
	}
	return filepath.Join(filepath.Dir(parent), arg)
}

// parseConfigLine extracts (name, value) from a postgresql.conf line.
// Returns ok=false for blank/comment-only lines.
//
// Syntax (matches upstream guc-file.l):
//
//	name = value           # the '=' is optional
//	name 'quoted value'    # single quotes; '' inside is a literal '
//	name "quoted value"    # double quotes (rare; recorded as-is)
//	# comment              # '#' starts a comment to end of line, except inside quotes
func parseConfigLine(line string) (name, value string, ok bool, err error) {
	i := skipSpace(line, 0)
	if i >= len(line) || line[i] == '#' {
		return "", "", false, nil
	}
	// Read name (alphanumerics, '_' and '.').
	start := i
	for i < len(line) && (isNameChar(line[i])) {
		i++
	}
	if i == start {
		return "", "", false, fmt.Errorf("invalid character %q at start of name", line[i])
	}
	name = line[start:i]

	// Skip whitespace and an optional '='.
	i = skipSpace(line, i)
	if i < len(line) && line[i] == '=' {
		i++
		i = skipSpace(line, i)
	}
	if i >= len(line) || line[i] == '#' {
		return "", "", false, fmt.Errorf("missing value for %q", name)
	}

	// Read value: a quoted string or a bareword run terminated by
	// whitespace or '#'.
	if line[i] == '\'' {
		v, n, err := readSingleQuoted(line, i)
		if err != nil {
			return "", "", false, err
		}
		// Trailing whitespace + optional comment.
		j := skipSpace(line, n)
		if j < len(line) && line[j] != '#' {
			return "", "", false, fmt.Errorf("unexpected trailing text after quoted value: %q", line[j:])
		}
		return name, v, true, nil
	}
	if line[i] == '"' {
		v, n, err := readDoubleQuoted(line, i)
		if err != nil {
			return "", "", false, err
		}
		j := skipSpace(line, n)
		if j < len(line) && line[j] != '#' {
			return "", "", false, fmt.Errorf("unexpected trailing text after quoted value: %q", line[j:])
		}
		return name, v, true, nil
	}

	// Bareword value.
	vStart := i
	for i < len(line) && line[i] != '#' && !isSpace(line[i]) {
		i++
	}
	rawVal := line[vStart:i]
	j := skipSpace(line, i)
	if j < len(line) && line[j] != '#' {
		// Postgres allows multiple bareword tokens to be re-joined with
		// a single space (e.g. `DateStyle = ISO, MDY` parses as a
		// single value). Accept that shape: keep consuming until we
		// hit '#' or end of line, preserving exactly one space.
		var b strings.Builder
		b.WriteString(rawVal)
		for j < len(line) && line[j] != '#' {
			b.WriteByte(' ')
			tStart := j
			for j < len(line) && line[j] != '#' && !isSpace(line[j]) {
				j++
			}
			if j == tStart {
				break
			}
			b.WriteString(line[tStart:j])
			j = skipSpace(line, j)
		}
		rawVal = b.String()
	}
	return name, rawVal, true, nil
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' }

func isNameChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= 'A' && b <= 'Z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_' || b == '.':
		return true
	}
	return false
}

// readSingleQuoted parses a single-quoted string starting at s[i]
// (which must be '\”). Returns the unquoted body, the index just
// past the closing quote, and an error.
//
// Inside single quotes, ” is an escaped literal single quote;
// upstream also honours \\ and \n, but we keep the surface tight: a
// backslash is literal, just like inside upstream's GUC string values
// when GUC_LIST_QUOTE is not set.
func readSingleQuoted(s string, i int) (string, int, error) {
	if i >= len(s) || s[i] != '\'' {
		return "", i, errors.New("expected single quote")
	}
	i++
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		if c == '\'' {
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return "", i, errors.New("unterminated single-quoted value")
}

func readDoubleQuoted(s string, i int) (string, int, error) {
	if i >= len(s) || s[i] != '"' {
		return "", i, errors.New("expected double quote")
	}
	i++
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		if c == '"' {
			return b.String(), i + 1, nil
		}
		b.WriteByte(c)
		i++
	}
	return "", i, errors.New("unterminated double-quoted value")
}
