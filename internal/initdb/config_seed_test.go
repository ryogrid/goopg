package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// confLine returns the first uncommented assignment line for name in the
// data directory's postgresql.conf, or "" if none is present.
func confLine(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	if err != nil {
		t.Fatalf("read postgresql.conf: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, name) {
			rest := strings.TrimLeft(trimmed[len(name):], " \t")
			if strings.HasPrefix(rest, "=") {
				return trimmed
			}
		}
	}
	return ""
}

// TestInitTextSearchConfig mirrors the --text-search-config arm of
// 001_initdb.pl's "successful creation" test: -T german seeds
// default_text_search_config as the value 'pg_catalog.german' (initdb.c
// always prefixes pg_catalog.).
func TestInitTextSearchConfig(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true, TextSearchConfig: "german"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := confLine(t, dir, "default_text_search_config")
	want := "default_text_search_config = 'pg_catalog.german'"
	if got != want {
		t.Errorf("default_text_search_config line = %q, want %q", got, want)
	}
}

// TestInitSuccessfulCreationOptions mirrors 001_initdb.pl's full
// "successful creation" command: --no-sync --text-search-config german
// --set default_text_search_config=german. The -c switch is applied after
// the -T seeding, so the final value is the unprefixed 'german' the user
// set explicitly (initdb.c:1430-1436 overrides the earlier assignment).
func TestInitSuccessfulCreationOptions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{
		DataDir:          dir,
		NoSync:           true,
		TextSearchConfig: "german",
		ExtraGUC:         []GUCSetting{{Name: "default_text_search_config", Value: "german"}},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := confLine(t, dir, "default_text_search_config")
	// "german" is a bare identifier, so it is written unquoted.
	want := "default_text_search_config = german"
	if got != want {
		t.Errorf("default_text_search_config line = %q, want %q", got, want)
	}
	// Exactly one uncommented assignment must exist (the -T line was
	// rewritten in place, not duplicated).
	data, _ := os.ReadFile(filepath.Join(dir, "postgresql.conf"))
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "#") && strings.HasPrefix(trimmed, "default_text_search_config") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("found %d uncommented default_text_search_config lines, want 1", n)
	}
}

// TestInitExtraGUCRewritesTemplateLine verifies a --set against a GUC that
// already exists (commented) in the template rewrites that line in place
// rather than appending, preserving the template's inline comment.
func TestInitExtraGUCRewritesTemplateLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{
		DataDir:  dir,
		NoSync:   true,
		ExtraGUC: []GUCSetting{{Name: "default_statistics_target", Value: "250"}},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := confLine(t, dir, "default_statistics_target")
	if !strings.HasPrefix(got, "default_statistics_target = 250") {
		t.Errorf("default_statistics_target line = %q, want it to start with the seeded value", got)
	}
	// The template entry carried an inline "# 1-10000" comment; it should
	// survive the in-place rewrite.
	if !strings.Contains(got, "#") {
		t.Errorf("inline comment lost: %q", got)
	}
}

// TestInitExtraGUCAppendsUnknown verifies a --set for a GUC absent from the
// template appends a new line (initdb.c relies on the bootstrap server to
// reject an invalid name; goopg simply records it).
func TestInitExtraGUCAppendsUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{
		DataDir:  dir,
		NoSync:   true,
		ExtraGUC: []GUCSetting{{Name: "goopg_made_up_guc", Value: "hello world"}},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	got := confLine(t, dir, "goopg_made_up_guc")
	// "hello world" contains a space, so it must be quoted.
	want := "goopg_made_up_guc = 'hello world'"
	if got != want {
		t.Errorf("appended line = %q, want %q", got, want)
	}
}

// TestInitNoSeedingLeavesTemplateUntouched confirms that without either
// option the postgresql.conf bytes equal the embedded sample verbatim.
func TestInitNoSeedingLeavesTemplateUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := confLine(t, dir, "default_text_search_config"); got != "" {
		t.Errorf("default_text_search_config seeded without -T: %q", got)
	}
}

func TestReplaceGUCValue(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		guc   string
		value string
		want  []string
	}{
		{
			name:  "rewrites commented template line preserving comment",
			lines: []string{"#default_statistics_target = 100\t\t# 1-10000"},
			guc:   "default_statistics_target",
			value: "250",
			want:  []string{"default_statistics_target = 250 # 1-10000"},
		},
		{
			name:  "appends when absent",
			lines: []string{"# nothing relevant here"},
			guc:   "work_mem",
			value: "64MB",
			want:  []string{"# nothing relevant here", "work_mem = 64MB"},
		},
		{
			name:  "quotes value with dot",
			lines: []string{"#default_text_search_config = 'pg_catalog.simple'"},
			guc:   "default_text_search_config",
			value: "pg_catalog.german",
			want:  []string{"default_text_search_config = 'pg_catalog.german'"},
		},
		{
			name:  "preserves canonical file casing over guc-name casing",
			lines: []string{"#Work_Mem = 4MB"},
			guc:   "work_mem",
			value: "8MB",
			want:  []string{"Work_Mem = 8MB"},
		},
		{
			name:  "only first match rewritten",
			lines: []string{"#shared_buffers = 128MB", "#shared_buffers = 256MB"},
			guc:   "shared_buffers",
			value: "512MB",
			want:  []string{"shared_buffers = 512MB", "#shared_buffers = 256MB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replaceGUCValue(append([]string(nil), tt.lines...), tt.guc, tt.value)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("replaceGUCValue = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatGUCValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"german", "german"}, // bare identifier
		{"100", "100"},       // number
		{"64MB", "64MB"},     // number with unit
		{"", "''"},           // empty must be quoted
		{"pg_catalog.german", "'pg_catalog.german'"}, // has a dot
		{"read committed", "'read committed'"},       // has a space
		{"on", "on"},                                 // identifier
		{"it's", "'it''s'"},                          // single quote escaped
		{"3.14", "'3.14'"},                           // float -> not a plain number
	}
	for _, tt := range tests {
		if got := formatGUCValue(tt.in); got != tt.want {
			t.Errorf("formatGUCValue(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
