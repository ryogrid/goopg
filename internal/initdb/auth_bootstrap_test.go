package initdb

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/auth"
)

// TestResolveAuthMethods exercises the port of initdb's auth-method option
// handling: default-to-trust (+warn), the ident↔peer cross-map, per-conntype
// validation, and the must-have-password check.
func TestResolveAuthMethods(t *testing.T) {
	cases := []struct {
		name        string
		host, local string
		hasPassword bool
		wantHost    string
		wantLocal   string
		wantWarn    bool
		wantErr     bool
	}{
		{name: "both empty → trust + warn", wantHost: "trust", wantLocal: "trust", wantWarn: true},
		{name: "explicit trust both", host: "trust", local: "trust", wantHost: "trust", wantLocal: "trust"},
		{name: "ident cross-maps local to peer", host: "ident", local: "ident", wantHost: "ident", wantLocal: "peer"},
		{name: "peer cross-maps host to ident", host: "peer", local: "peer", wantHost: "ident", wantLocal: "peer"},
		{name: "scram one side needs no password", host: "scram-sha-256", local: "trust", wantHost: "scram-sha-256", wantLocal: "trust"},
		{name: "both scram without password errors", host: "scram-sha-256", local: "scram-sha-256", wantErr: true},
		{name: "both scram with password ok", host: "scram-sha-256", local: "scram-sha-256", hasPassword: true, wantHost: "scram-sha-256", wantLocal: "scram-sha-256"},
		{name: "md5 both without password errors", host: "md5", local: "md5", wantErr: true},
		{name: "invalid host method", host: "bogus", local: "trust", wantErr: true},
		{name: "ident invalid for local", host: "trust", local: "ident", wantErr: true},
		{name: "peer invalid for host", host: "peer", local: "trust", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHost, gotLocal, gotWarn, err := resolveAuthMethods(tc.host, tc.local, tc.hasPassword)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveAuthMethods(%q,%q,%v): want error, got nil (host=%q local=%q)",
						tc.host, tc.local, tc.hasPassword, gotHost, gotLocal)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveAuthMethods(%q,%q,%v): unexpected error: %v", tc.host, tc.local, tc.hasPassword, err)
			}
			if gotHost != tc.wantHost || gotLocal != tc.wantLocal {
				t.Errorf("methods = (%q,%q), want (%q,%q)", gotHost, gotLocal, tc.wantHost, tc.wantLocal)
			}
			if gotWarn != tc.wantWarn {
				t.Errorf("warn = %v, want %v", gotWarn, tc.wantWarn)
			}
		})
	}
}

// TestBuildPgHBAConf checks the method substitution and that the trust
// default reproduces the historical needles that initdb_test.go pins.
func TestBuildPgHBAConf(t *testing.T) {
	trust := string(buildPgHBAConf("trust", "trust"))
	for _, needle := range []string{
		"local    all       all                    trust",
		"host     all       all    127.0.0.1/32    trust",
		"host     all       all    ::1/128         trust",
		// Upstream's three replication rules (pg_hba.conf.sample): a real
		// PG reading a goopg data dir needs them, because `all` in the
		// DATABASE field does not match a replication connection.
		"local    replication  all                 trust",
		"host     replication  all  127.0.0.1/32   trust",
		"host     replication  all  ::1/128        trust",
		"host     all       all    0.0.0.0/0       reject",
		"host     all       all    ::/0            reject",
	} {
		if !strings.Contains(trust, needle) {
			t.Errorf("trust pg_hba.conf missing %q", needle)
		}
	}
	// defaultPgHBAConf must be byte-identical to the trust build.
	if !bytes.Equal(buildPgHBAConf("trust", "trust"), defaultPgHBAConf()) {
		t.Error("defaultPgHBAConf() != buildPgHBAConf(\"trust\",\"trust\")")
	}

	scram := string(buildPgHBAConf("scram-sha-256", "peer"))
	for _, needle := range []string{
		"local    all       all                    peer",
		"host     all       all    127.0.0.1/32    scram-sha-256",
		"host     all       all    ::1/128         scram-sha-256",
	} {
		if !strings.Contains(scram, needle) {
			t.Errorf("scram/peer pg_hba.conf missing %q", needle)
		}
	}
	// External catch-all stays reject regardless of method.
	if !strings.Contains(scram, "host     all       all    0.0.0.0/0       reject") {
		t.Error("external rule should stay reject")
	}
}

// TestReadSuperuserPasswordFile mirrors get_su_pwd's file branch: first line,
// CRLF strip, empty/missing errors.
func TestReadSuperuserPasswordFile(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	if got, err := readSuperuserPasswordFile(write("plain", "s3cret\n")); err != nil || got != "s3cret" {
		t.Errorf("plain: got %q, err %v; want \"s3cret\", nil", got, err)
	}
	if got, err := readSuperuserPasswordFile(write("firstline", "first\nsecond\n")); err != nil || got != "first" {
		t.Errorf("firstline: got %q, err %v; want \"first\", nil", got, err)
	}
	if got, err := readSuperuserPasswordFile(write("crlf", "withcr\r\n")); err != nil || got != "withcr" {
		t.Errorf("crlf: got %q, err %v; want \"withcr\", nil", got, err)
	}
	if got, err := readSuperuserPasswordFile(write("nonewline", "noeol")); err != nil || got != "noeol" {
		t.Errorf("nonewline: got %q, err %v; want \"noeol\", nil", got, err)
	}
	if _, err := readSuperuserPasswordFile(write("empty", "")); err == nil {
		t.Error("empty file: want error, got nil")
	}
	if _, err := readSuperuserPasswordFile(write("emptyline", "\nignored")); err == nil {
		t.Error("leading-newline (empty first line): want error, got nil")
	}
	if _, err := readSuperuserPasswordFile(filepath.Join(dir, "does-not-exist")); err == nil {
		t.Error("missing file: want error, got nil")
	}
}

// TestEncodeSuperuserPassword checks the scram (default) and md5 verifier
// encodings and the password_encryption GUC choice.
func TestEncodeSuperuserPassword(t *testing.T) {
	// Default (scram-sha-256 both): SCRAM verifier, no GUC override needed.
	verifier, pwEnc, err := encodeSuperuserPassword("hunter2", "scram-sha-256", "scram-sha-256", "postgres")
	if err != nil {
		t.Fatalf("scram encode: %v", err)
	}
	if pwEnc != "" {
		t.Errorf("scram passwordEncryption = %q, want \"\" (template default)", pwEnc)
	}
	if !strings.HasPrefix(verifier, "SCRAM-SHA-256$") {
		t.Fatalf("scram verifier = %q, want SCRAM-SHA-256$ prefix", verifier)
	}
	secret, err := auth.ParseSCRAMSecret(verifier)
	if err != nil {
		t.Fatalf("ParseSCRAMSecret(%q): %v", verifier, err)
	}
	if !secret.VerifySCRAMSecretFromPassword("hunter2") {
		t.Error("SCRAM verifier does not verify the cleartext password")
	}
	if secret.VerifySCRAMSecretFromPassword("wrong") {
		t.Error("SCRAM verifier verifies a wrong password")
	}

	// md5 chosen → md5 verifier + password_encryption=md5.
	md5v, md5Enc, err := encodeSuperuserPassword("hunter2", "md5", "md5", "alice")
	if err != nil {
		t.Fatalf("md5 encode: %v", err)
	}
	if md5Enc != "md5" {
		t.Errorf("md5 passwordEncryption = %q, want \"md5\"", md5Enc)
	}
	if want := auth.MD5Shadow("hunter2", "alice"); md5v != want {
		t.Errorf("md5 verifier = %q, want %q", md5v, want)
	}

	// md5 on one side but scram on the other → scram wins (per initdb rule).
	_, mixedEnc, err := encodeSuperuserPassword("hunter2", "md5", "scram-sha-256", "postgres")
	if err != nil {
		t.Fatalf("mixed encode: %v", err)
	}
	if mixedEnc != "" {
		t.Errorf("md5+scram passwordEncryption = %q, want \"\" (scram default)", mixedEnc)
	}
}

// TestInitAuthScramPwfile drives Init end-to-end with scram auth + a password
// file and checks pg_hba.conf carries scram and pg_authid's OID-10 row stores
// a SCRAM verifier that matches the cleartext.
func TestInitAuthScramPwfile(t *testing.T) {
	t.Setenv("USER", "postgres") // single bootstrap row keeps the page simple
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	pwPath := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwPath, []byte("topsecret\n"), 0o600); err != nil {
		t.Fatalf("write pwfile: %v", err)
	}

	if err := Init(Options{
		DataDir:         dataDir,
		AuthMethodHost:  "scram-sha-256",
		AuthMethodLocal: "scram-sha-256",
		PwFile:          pwPath,
		NoSync:          true,
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}

	hba, err := os.ReadFile(filepath.Join(dataDir, "pg_hba.conf"))
	if err != nil {
		t.Fatalf("read pg_hba.conf: %v", err)
	}
	if !strings.Contains(string(hba), "127.0.0.1/32    scram-sha-256") {
		t.Errorf("pg_hba.conf does not carry scram-sha-256:\n%s", hba)
	}

	// The SCRAM verifier is written verbatim as the rolpassword text value
	// in pg_authid (global/1260). Extract it and verify the cleartext.
	raw, err := os.ReadFile(filepath.Join(dataDir, "global", "1260"))
	if err != nil {
		t.Fatalf("read global/1260: %v", err)
	}
	verifier := extractSCRAMVerifier(t, raw)
	secret, err := auth.ParseSCRAMSecret(verifier)
	if err != nil {
		t.Fatalf("ParseSCRAMSecret(%q): %v", verifier, err)
	}
	if !secret.VerifySCRAMSecretFromPassword("topsecret") {
		t.Errorf("stored rolpassword verifier does not match cleartext (verifier=%q)", verifier)
	}
}

// extractSCRAMVerifier finds the "SCRAM-SHA-256$" marker in a heap page and
// reads the printable verifier string that follows (terminated by the first
// non-printable byte — the rolvaliduntil timestamptz that follows in the row).
func extractSCRAMVerifier(t *testing.T, page []byte) string {
	t.Helper()
	marker := []byte("SCRAM-SHA-256$")
	i := bytes.Index(page, marker)
	if i < 0 {
		t.Fatal("no SCRAM-SHA-256$ marker in pg_authid page")
	}
	j := i
	for j < len(page) && page[j] >= 0x20 && page[j] <= 0x7e {
		j++
	}
	return string(page[i:j])
}

// TestInitAuthBothPasswordNoPwfileFails: scram on both sides without a
// password file aborts before any filesystem layout.
func TestInitAuthBothPasswordNoPwfileFails(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	err := Init(Options{
		DataDir:         dataDir,
		AuthMethodHost:  "scram-sha-256",
		AuthMethodLocal: "scram-sha-256",
		NoSync:          true,
	})
	if err == nil {
		t.Fatal("Init: want error for password method without --pwfile, got nil")
	}
	if !strings.Contains(err.Error(), "must specify a password") {
		t.Errorf("error = %v, want \"must specify a password\"", err)
	}
	// Must have aborted before creating the data directory.
	if _, statErr := os.Stat(dataDir); statErr == nil {
		t.Error("data directory was created despite the pre-flight error")
	}
}

// TestInitAuthInvalidMethodFails: a bogus method aborts before layout.
func TestInitAuthInvalidMethodFails(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	err := Init(Options{DataDir: dataDir, AuthMethodHost: "bogus", NoSync: true})
	if err == nil {
		t.Fatal("Init: want error for invalid auth method, got nil")
	}
	if !strings.Contains(err.Error(), "invalid authentication method") {
		t.Errorf("error = %v, want \"invalid authentication method\"", err)
	}
}
