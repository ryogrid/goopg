package config

import (
	"strings"
	"testing"
)

// pgInitdbConfLines is the set of assignments a stock PostgreSQL 18.3
// `initdb` writes into postgresql.conf that goopg had never registered,
// captured verbatim (values and quoting included) from the reference
// cluster at bench/tpch/runtime/pgdata/postgresql.conf. The line numbers
// in the comments are that file's.
//
// Before M0131-S1 every one of these aborted Registry.ApplyConfigEntries
// with "unrecognized configuration parameter", and cmd/goopg/main.go
// turns that into exit 1 — so `goopg start -D <a PG data directory>`
// failed before opening a buffer pool, let alone reading base/5/1259.
// That is the first obstacle on M0131's reverse cold-start path, and it
// is not a catalog-format problem at all.
const pgInitdbConfLines = `
dynamic_shared_memory_type = posix	# the default is the first option
log_timezone = 'Asia/Tokyo'
autovacuum_worker_slots = 16		# autovacuum worker slots to allocate
lc_messages = C				# locale for system error message
lc_monetary = C				# locale for monetary formatting
lc_numeric = C				# locale for number formatting
lc_time = C				# locale for time formatting
default_text_search_config = 'pg_catalog.english'
`

// goopgInitdbConfLines is the sibling failure: names goopg's OWN
// `goopg init` appends to postgresql.conf when a locale / text-search /
// --pwfile / --allow-group-access flag is given
// (internal/initdb/config_seed.go:32-83, internal/initdb/locale.go:235-245).
// Because none of them was in the shipped template, replaceGUCValue
// appended each as a fresh line — so goopg could produce a directory that
// goopg itself then refused to start on.
const goopgInitdbConfLines = `
lc_messages = 'C'
lc_monetary = 'C'
lc_numeric = 'C'
lc_time = 'C'
default_text_search_config = 'pg_catalog.english'
password_encryption = 'md5'
log_file_mode = 0640
`

func applyConfText(t *testing.T, text string) error {
	t.Helper()
	entries, err := ParseConfigReader(strings.NewReader(text), "test.conf")
	if err != nil {
		t.Fatalf("ParseConfigReader: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no entries parsed from %q", text)
	}
	return BuildDefaultRegistry().ApplyConfigEntries(entries)
}

func TestApplyPGInitdbConfEntries(t *testing.T) {
	if err := applyConfText(t, pgInitdbConfLines); err != nil {
		t.Fatalf("PG 18.3 initdb conf rejected: %v", err)
	}
}

func TestApplyGoopgInitdbConfEntries(t *testing.T) {
	if err := applyConfText(t, goopgInitdbConfLines); err != nil {
		t.Fatalf("goopg init conf rejected: %v", err)
	}
}

// TestInitdbGUCsAreStoredNotDropped proves the registrations are
// "parsed and stored" rather than "silently swallowed": the value a conf
// file sets is what Registry.Get reports back, which is what SHOW reads.
func TestInitdbGUCsAreStoredNotDropped(t *testing.T) {
	entries, err := ParseConfigReader(strings.NewReader(pgInitdbConfLines), "test.conf")
	if err != nil {
		t.Fatalf("ParseConfigReader: %v", err)
	}
	r := BuildDefaultRegistry()
	if err := r.ApplyConfigEntries(entries); err != nil {
		t.Fatalf("ApplyConfigEntries: %v", err)
	}
	want := map[string]string{
		"dynamic_shared_memory_type": "posix",
		"log_timezone":               "Asia/Tokyo",
		"autovacuum_worker_slots":    "16",
		"lc_messages":                "C",
		"lc_monetary":                "C",
		"lc_numeric":                 "C",
		"lc_time":                    "C",
		"default_text_search_config": "pg_catalog.english",
	}
	for name, val := range want {
		v, ok := r.Get(name)
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		if v.Value != val {
			t.Errorf("%s = %q, want %q", name, v.Value, val)
		}
	}
}

// TestInitdbGUCsRejectBadValues keeps the stubs from degrading into
// accept-anything strings: each still enforces the upstream enum or
// range, so goopg's accept/reject set matches PG's.
func TestInitdbGUCsRejectBadValues(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"dynamic_shared_memory_type", "dynamic_shared_memory_type = nonesuch"},
		{"autovacuum_worker_slots", "autovacuum_worker_slots = 0"},
		{"password_encryption", "password_encryption = plaintext"},
		{"log_file_mode octal range", "log_file_mode = 07777"},
		{"log_file_mode non-numeric", "log_file_mode = rwx"},
	} {
		if err := applyConfText(t, tc.line+"\n"); err == nil {
			t.Errorf("%s: %q accepted, want rejection", tc.name, tc.line)
		}
	}
}

// TestLogFileModeKeepsOctalSpelling documents the one deliberate
// divergence: upstream stores log_file_mode as an int and re-renders it
// through show_log_file_mode (guc_tables.c:2556), so PG normalises any
// spelling to octal. goopg validates with the same base-0 parse and range
// but echoes the literal text, which agrees with PG for every
// octal-spelled value — the only spelling initdb or the shipped template
// ever produces.
func TestLogFileModeKeepsOctalSpelling(t *testing.T) {
	entries, err := ParseConfigReader(strings.NewReader("log_file_mode = 0640\n"), "test.conf")
	if err != nil {
		t.Fatalf("ParseConfigReader: %v", err)
	}
	r := BuildDefaultRegistry()
	if err := r.ApplyConfigEntries(entries); err != nil {
		t.Fatalf("ApplyConfigEntries: %v", err)
	}
	v, _ := r.Get("log_file_mode")
	if v.Value != "0640" {
		t.Fatalf("log_file_mode = %q, want %q", v.Value, "0640")
	}
}
