package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapPostgresRoleCustomSuperuser verifies that the bootstrap
// superuser row (pg_authid OID 10) honours a caller-supplied name —
// the runtime backing for upstream initdb's -U/--username option.
//
// When the OS user equals the chosen superuser, no duplicate OS-user
// row (OID 16384) is emitted; the seed list collapses to a single
// bootstrap entry plus the 16 predefined roles.
func TestBootstrapPostgresRoleCustomSuperuser(t *testing.T) {
	t.Setenv("USER", "alice")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	entries, err := bootstrapPostgresRole(dir, "alice")
	if err != nil {
		t.Fatalf("bootstrapPostgresRole: %v", err)
	}
	// 1 bootstrap row (alice, since USER==superuser) + 16 predefined = 17.
	const wantEntries = 17
	if len(entries) != wantEntries {
		t.Fatalf("entries=%d, want %d (1 bootstrap + 16 predefined)", len(entries), wantEntries)
	}
	if entries[0].OID != 10 {
		t.Errorf("entries[0].OID=%d, want 10 (BOOTSTRAP_SUPERUSERID)", entries[0].OID)
	}
	if entries[0].Rolname != "alice" {
		t.Errorf("entries[0].Rolname=%q, want %q", entries[0].Rolname, "alice")
	}
	for _, e := range entries {
		if e.OID == 16384 {
			t.Errorf("unexpected OS-user row at OID 16384 when USER==superuser")
		}
	}
}

// TestBootstrapPostgresRoleDistinctOSUser confirms a distinct OS-user
// role (OID 16384) is still seeded when it differs from the superuser.
func TestBootstrapPostgresRoleDistinctOSUser(t *testing.T) {
	t.Setenv("USER", "bob")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o700); err != nil {
		t.Fatalf("mkdir global: %v", err)
	}
	entries, err := bootstrapPostgresRole(dir, "alice")
	if err != nil {
		t.Fatalf("bootstrapPostgresRole: %v", err)
	}
	var foundSuper, foundOS bool
	for _, e := range entries {
		if e.OID == 10 && e.Rolname == "alice" {
			foundSuper = true
		}
		if e.OID == 16384 && e.Rolname == "bob" {
			foundOS = true
		}
	}
	if !foundSuper {
		t.Errorf("missing superuser row (OID 10, %q)", "alice")
	}
	if !foundOS {
		t.Errorf("missing OS-user row (OID 16384, %q)", "bob")
	}
}

// TestInitRejectsReservedSuperuserName mirrors upstream
// initdb.c:3479 — a superuser name beginning with "pg_" is rejected
// before any filesystem layout happens.
func TestInitRejectsReservedSuperuserName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	err := Init(Options{DataDir: dir, SuperuserName: "pg_test"})
	if err == nil {
		t.Fatal("Init accepted reserved superuser name \"pg_test\"; want error")
	}
	if !strings.Contains(err.Error(), "cannot begin with") {
		t.Errorf("error = %q, want it to mention the \"pg_\" prefix restriction", err.Error())
	}
	// The guard runs before ensureEmptyDir/MkdirAll, so no data dir is left.
	if _, statErr := os.Stat(dir); statErr == nil {
		t.Errorf("data dir %q was created despite rejected superuser name", dir)
	}
}

// TestInitThreadsSuperuserToPgAuthid is an end-to-end check that
// Options.SuperuserName flows through Init into the on-disk pg_authid
// heap (global/1260).
func TestInitThreadsSuperuserToPgAuthid(t *testing.T) {
	t.Setenv("USER", "carol")
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, SuperuserName: "carol"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "global", "1260"))
	if err != nil {
		t.Fatalf("read pg_authid (global/1260): %v", err)
	}
	// NameData is a 64-byte NUL-padded field; the superuser cstring must
	// appear verbatim, and the default "postgres" superuser must not.
	if !bytes.Contains(raw, append([]byte("carol"), 0)) {
		t.Errorf("pg_authid does not contain custom superuser %q", "carol")
	}
	if bytes.Contains(raw, append([]byte("postgres"), 0)) {
		t.Errorf("pg_authid unexpectedly contains default superuser \"postgres\"")
	}
}
