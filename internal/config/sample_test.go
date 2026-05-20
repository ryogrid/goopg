package config

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// sampleEntryRE matches a `name = value` line in postgresql.conf.sample,
// optionally prefixed by a single `#` (every shipped entry is commented
// out). The name uses `\w` rather than `[a-z_]` because the registry
// preserves PG's capitalised names (`DateStyle`, `TimeZone`,
// `IntervalStyle`).
//
// The trailing `(?:#.*)?` discards an inline hint comment. Values
// containing a `#` inside single quotes are not currently present in
// the template; if one is ever added, this regex will need an explicit
// `'...'` branch.
var sampleEntryRE = regexp.MustCompile(`^#?\s*(\w+)\s*=\s*(.+?)\s*(?:#.*)?$`)

// parseSampleEntries walks the embedded postgresql.conf.sample bytes and
// returns lowercased-GUC-name -> raw-value-string. Enclosing single
// quotes are stripped so the value can be compared against the registry
// BootVal (string GUCs are stored unquoted in the registry).
func parseSampleEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for i, line := range bytes.Split(b, []byte{'\n'}) {
		m := sampleEntryRE.FindSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.ToLower(string(m[1]))
		val := string(m[2])
		if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
			val = val[1 : len(val)-1]
		}
		if prev, dup := out[name]; dup {
			t.Errorf("postgresql.conf.sample:%d: duplicate entry for %q (was %q, now %q)", i+1, name, prev, val)
		}
		out[name] = val
	}
	return out
}

// TestSampleConfigCoversRegistry enforces the M0108 invariant: every
// file-settable GUC in BuildDefaultRegistry has a commented-out entry
// in postgresql.conf.sample, no template entry references an unknown
// GUC, and the default in the template equals the registry BootVal so
// uncommenting the file as-is is functionally a no-op.
//
// Authors adding a GUC to defaults.go MUST add the matching entry to
// internal/config/postgresql.conf.sample in the same commit (see the
// "GUC sample-file discipline" section of .ralph/AGENT.md).
func TestSampleConfigCoversRegistry(t *testing.T) {
	reg := BuildDefaultRegistry()
	sample := SampleConfig()
	sampleEntries := parseSampleEntries(t, sample)

	for _, v := range reg.All() {
		if v.Flags&FlagDisallowInFile != 0 {
			continue
		}
		if _, ok := sampleEntries[strings.ToLower(v.Name)]; !ok {
			t.Errorf("postgresql.conf.sample missing GUC %q (registered in defaults.go)", v.Name)
		}
	}

	for name := range sampleEntries {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("postgresql.conf.sample references unknown GUC %q", name)
		}
	}

	for name, val := range sampleEntries {
		v, ok := reg.Get(name)
		if !ok {
			continue
		}
		if val != v.BootVal {
			t.Errorf("postgresql.conf.sample default for %q is %q; registry BootVal is %q", v.Name, val, v.BootVal)
		}
	}
}
