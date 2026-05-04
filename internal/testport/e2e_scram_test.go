package testport

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestE2E_SCRAMAuthDoD is the DoD test for M0049-0003.
//
// DoD: "psql / pgx / JDBC connect against a SCRAM-only HBA rule."
//
// This test:
//  1. Initialises a cluster with a pg_auth file containing a SCRAM-SHA-256
//     verifier for the postgres user.
//  2. Prepends a `host all postgres 127.0.0.1/32 scram-sha-256` rule to
//     pg_hba.conf (before the default trust rule).
//  3. Starts the server and connects via database/sql (lib/pq) using
//     the correct password.
//  4. Executes SELECT 1 and verifies the result.
func TestE2E_SCRAMAuthDoD(t *testing.T) {
	const (
		testUser     = "postgres"
		testPassword = "DoD-SCRAM-password-2026"
	)

	c, err := cluster.New("e2e_scram", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Init the data directory (creates base pg_hba.conf, etc.).
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}

	// Generate a SCRAM-SHA-256 verifier from the cleartext password.
	cred, err := auth.NewSCRAMCredential(testPassword)
	if err != nil {
		t.Fatalf("NewSCRAMCredential: %v", err)
	}

	// Write pg_auth so the server loads the SCRAM verifier on startup.
	if err := c.WritePGAuth(map[string]string{testUser: cred.Secret}); err != nil {
		t.Fatalf("WritePGAuth: %v", err)
	}

	// Prepend a SCRAM rule for postgres; the existing trust rules
	// follow for other users.
	if err := c.AppendPGHBA("host all postgres 127.0.0.1/32 scram-sha-256"); err != nil {
		t.Fatalf("AppendPGHBA: %v", err)
	}

	// Start the server.
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	host, port, err := splitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}

	// Connect via lib/pq with SCRAM credentials.
	// lib/pq negotiates SCRAM-SHA-256 when the server offers it.
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
		host, port, testUser, testPassword)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var result int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&result); err != nil {
		t.Fatalf("SCRAM-authenticated SELECT 1 failed: %v", err)
	}
	if result != 1 {
		t.Errorf("SELECT 1 = %d, want 1", result)
	}
	t.Logf("SCRAM-SHA-256 authentication succeeded; SELECT 1 = %d", result)
}
