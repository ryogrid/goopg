package testport

// End-to-end proof that goopg's server-side SASLprep normalization
// (internal/auth's pgSASLPrep, wired into NewSCRAMSecretWithIterations
// and VerifySCRAMSecretFromPassword) is byte-compatible with real
// PostgreSQL's pg_saslprep (postgres/src/common/saslprep.c).
//
// A unit-test differential against known RFC 4013 vectors already lives in
// internal/auth/saslprep_test.go, but that only proves goopg's own
// derive/verify pair agrees with itself. This test instead authenticates
// with a REAL libpq client (`psql`, linked against the same saslprep.c
// upstream ships) rather than lib/pq's Go reimplementation, which does no
// SASLprep at all (confirmed by reading github.com/lib/pq/scram) and so
// cannot exercise this code path meaningfully.
//
// CREATE ROLE stores a verifier derived from a password containing
// U+2168 ROMAN NUMERAL NINE, which SASLprep's NFKC normalization step
// compatibility-decomposes to the ASCII string "IX". A real libpq client
// authenticating with the plain ASCII password "IX" must therefore
// succeed over a scram-sha-256 HBA rule: this only holds if goopg's
// CREATE-ROLE-time SASLprep normalizes U+2168 exactly the way libpq's own
// SASLprep (a no-op on pure-ASCII input) expects the stored secret to
// have been derived.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestE2E_SASLPrepMatchesRealLibpqClient(t *testing.T) {
	const (
		roleName       = "saslprepuser"
		decomposedForm = "Ⅸ"  // U+2168 ROMAN NUMERAL NINE
		canonicalForm  = "IX" // its NFKC-normalized, SASLprep-mapped form
	)

	psql := psqlPath(t)
	if psql == "" {
		t.Skip("psql not found (in-tree local_install or PATH)")
	}

	dataDir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("e2e-saslprep", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dataDir,
		PSQLPath:     psql,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	// scram-sha-256 for the test role only, ahead of the default trust
	// rules (first-match-wins, same shape as role_auth_durability_test.go).
	hba := "host all " + roleName + " 127.0.0.1/32 scram-sha-256\n" +
		"local all all trust\n" +
		"host all all 127.0.0.1/32 trust\n" +
		"host all all ::1/128 trust\n"
	if err := os.WriteFile(filepath.Join(dataDir, "pg_hba.conf"), []byte(hba), 0o600); err != nil {
		t.Fatalf("write pg_hba.conf: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// CREATE ROLE over a trust connection, exercising
	// auth.NewSCRAMSecretWithIterations's SASLprep on the decomposed form.
	createSQL := "CREATE ROLE " + roleName + " LOGIN PASSWORD '" + decomposedForm + "'"
	if err := runSQLSimple(t, c, createSQL); err != nil {
		t.Fatalf("CREATE ROLE: %v", err)
	}

	// A real libpq client (psql) authenticating with the plain ASCII
	// canonical form must succeed: libpq's own SASLprep is a no-op on
	// "IX" (pure ASCII), so this only passes if goopg's stored verifier
	// was itself derived from "IX" after normalizing the decomposed form
	// the same way.
	res, err := c.PSQLWithPassword(canonicalForm, "-U", roleName, "-c", "SELECT 1")
	if err != nil {
		t.Fatalf("psql (canonical form) exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("psql (canonical form \"IX\") failed: exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "1") {
		t.Fatalf("psql (canonical form) SELECT 1 unexpected stdout: %s", res.Stdout)
	}

	// Sanity: the ORIGINAL decomposed form must also still authenticate
	// (SASLprep on the client side reduces it to the same canonical form
	// again — this exercises libpq's own saslprep.c, proving both ends
	// converge, not just that we happened to store "IX" outright).
	res2, err := c.PSQLWithPassword(decomposedForm, "-U", roleName, "-c", "SELECT 1")
	if err != nil {
		t.Fatalf("psql (decomposed form) exec: %v", err)
	}
	if res2.ExitCode != 0 {
		t.Fatalf("psql (original decomposed form) failed: exit=%d stderr=%s", res2.ExitCode, res2.Stderr)
	}

	// A password that is neither the decomposed nor the canonical form
	// must be rejected, ruling out an accidental "always trust" bug.
	res3, err := c.PSQLWithPassword("wrong-password", "-U", roleName, "-c", "SELECT 1")
	if err == nil && res3.ExitCode == 0 {
		t.Fatalf("psql with an unrelated password unexpectedly succeeded")
	}
}
