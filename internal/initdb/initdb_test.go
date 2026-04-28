package initdb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitLaysOutDirectoryStructure pins the directory layout
// promised by .ralph/specs/GOAL_AND_REQUIREMENTS.md §6.1: every
// load-bearing subdirectory exists with the expected mode, plus
// PG_VERSION, postgresql.conf, and pg_hba.conf written at the
// root.
func TestInitLaysOutDirectoryStructure(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	for _, sub := range Subdirs {
		st, err := os.Stat(filepath.Join(dir, sub))
		if err != nil {
			t.Errorf("missing subdir %q: %v", sub, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("%q is not a directory", sub)
		}
		if st.Mode().Perm() != 0o700 {
			t.Errorf("%q mode=%o want 0700", sub, st.Mode().Perm())
		}
	}
	// base/<DBOid> for the default database.
	if _, err := os.Stat(filepath.Join(dir, "base", "1")); err != nil {
		t.Errorf("missing base/1: %v", err)
	}
	for _, want := range []string{"PG_VERSION", "postgresql.conf", "pg_hba.conf"} {
		path := filepath.Join(dir, want)
		st, err := os.Stat(path)
		if err != nil {
			t.Errorf("missing %q: %v", want, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("%q is empty", want)
		}
	}
	pv, err := os.ReadFile(filepath.Join(dir, "PG_VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pv)) != CatalogVersion {
		t.Errorf("PG_VERSION=%q want %q", strings.TrimSpace(string(pv)), CatalogVersion)
	}
}

// TestInitRefusesNonEmptyDir matches upstream initdb's "directory
// not empty" guard so users can't accidentally clobber a real
// PG installation.
func TestInitRefusesNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stale"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(Options{DataDir: dir})
	if err == nil {
		t.Fatal("expected error for non-empty dir")
	}
	if !strings.Contains(err.Error(), "not empty") {
		t.Errorf("err=%q, want a 'not empty' message", err.Error())
	}
}

// TestInitAcceptsExistingEmptyDir: an existing-but-empty target
// directory is fine — operators commonly pre-create the mountpoint
// with the right permissions before running goopg init.
func TestInitAcceptsExistingEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatalf("init on empty existing dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err != nil {
		t.Errorf("PG_VERSION missing after init: %v", err)
	}
}

// TestInitRejectsEmptyOption surfaces a clean error rather than
// silently writing to "" / current working directory.
func TestInitRejectsEmptyOption(t *testing.T) {
	if err := Init(Options{}); err == nil {
		t.Fatal("expected error for empty DataDir")
	}
}

// TestPGHBADefaultRules: the sample pg_hba.conf trusts loopback
// and rejects everything else, matching auth.DefaultPolicy() so
// goopg init's defaults align with goopg start's defaults.
func TestPGHBADefaultRules(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "pg_hba.conf"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, needle := range []string{"127.0.0.1/32    trust", "::1/128         trust", "0.0.0.0/0       reject"} {
		if !strings.Contains(got, needle) {
			t.Errorf("pg_hba.conf missing %q", needle)
		}
	}
}
