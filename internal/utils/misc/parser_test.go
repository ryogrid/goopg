package misc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseConfigLines covers the postgresql.conf shapes documented in
// docs/design/0004-configuration-and-guc.md.
func TestParseConfigLines(t *testing.T) {
	src := `
# A test postgresql.conf.
# blank lines and comments are ignored.

listen_addresses = 'localhost,*'   # quoted comma-separated value
port 5433                          # bareword value, '=' is optional
shared_buffers = 128MB
DateStyle = ISO, MDY               # bareword sequence joins with single space
log_line_prefix = '%m [%p] '       # quoted with trailing space
escaped_value = 'it''s here'       # doubled '' is a literal '
`
	got, err := ParseConfigReader(strings.NewReader(src), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"listen_addresses": "localhost,*",
		"port":             "5433",
		"shared_buffers":   "128MB",
		"datestyle":        "ISO, MDY",
		"log_line_prefix":  "%m [%p] ",
		"escaped_value":    "it's here",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (%+v)", len(got), len(want), got)
	}
	for _, e := range got {
		if v, ok := want[e.Name]; !ok || v != e.Value {
			t.Errorf("entry %q = %q, want %q", e.Name, e.Value, v)
		}
	}
}

func TestParseConfigRejectsBadLine(t *testing.T) {
	if _, err := ParseConfigReader(strings.NewReader("port =\n"), ""); err == nil {
		t.Error("expected error for missing value, got nil")
	}
	if _, err := ParseConfigReader(strings.NewReader("port = 'unclosed\n"), ""); err == nil {
		t.Error("expected error for unterminated quote, got nil")
	}
}

func TestParseConfigIncludeAndCycle(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main.conf")
	extra := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(extra, []byte("port = 5555\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := "" +
		"listen_addresses = 'a'\n" +
		"include extra.conf\n" +
		"include_if_exists missing.conf\n"
	if err := os.WriteFile(main, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ParseConfigFile(main)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "port" || got[1].Value != "5555" {
		t.Fatalf("expected listen_addresses + port=5555, got %+v", got)
	}

	// Cycle.
	a := filepath.Join(dir, "a.conf")
	b := filepath.Join(dir, "b.conf")
	_ = os.WriteFile(a, []byte("include b.conf\n"), 0o644)
	_ = os.WriteFile(b, []byte("include a.conf\n"), 0o644)
	if _, err := ParseConfigFile(a); err == nil {
		t.Error("expected cycle error")
	}
}
