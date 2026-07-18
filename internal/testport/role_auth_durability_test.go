package testport

// End-to-end coverage of role/auth restart persistence (root-0021).
//
// Previously CREATE ROLE was in-memory only: the PASSWORD was discarded and
// the role vanished on restart. The fix mirrors PostgreSQL's pg_authid model:
// the durable BASE store is the pg_authid heap file (global/1260, rebuilt
// atomically on every role DDL by initdb.SyncPgAuthidFile), and
// RecordKindRoleState/DropRole WAL records are the crash-recovery TAIL
// (replayRoleDDLRecords). `PASSWORD 'x'` is shadowed into an upstream-format
// SCRAM-SHA-256 verifier (auth.NewSCRAMSecret), mirroring PG's
// encrypt_password (postgres/src/backend/libpq/crypt.c).
//
// This e2e proves the full path over the wire: CREATE ROLE ... LOGIN
// PASSWORD survives a clean stop -> start, the restored role still
// authenticates via a real SCRAM-SHA-256 handshake (lib/pq), its pg_roles
// attributes are restored, and DROP ROLE is likewise durable.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestPort_CreateRoleSurvivesRestart(t *testing.T) {
	const (
		roleName     = "wpuser"
		rolePassword = "wp-secret-2026"
	)

	dataDir := filepath.Join(t.TempDir(), "data")
	c, err := cluster.New("role-auth-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      dataDir,
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		SyncInit:     true,
		SyncRuntime:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	// Require SCRAM for the test role so the post-restart login proves the
	// persisted verifier actually authenticates; trust stays the default for
	// everyone else. HBA rules are first-match-wins (PG semantics), so the
	// role-specific rule must PRECEDE the catch-all trust rules — rewrite the
	// whole file rather than appending. goopg start now auto-loads the data
	// directory's pg_hba.conf like PostgreSQL (root-0021).
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

	if err := runSQLSimple(t, c,
		fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s'", roleName, rolePassword)); err != nil {
		t.Fatalf("CREATE ROLE: %v", err)
	}
	// A NOLOGIN role to check attribute persistence, and a dropped role to
	// check drop durability.
	if err := runSQLSimple(t, c, "CREATE ROLE norole NOLOGIN"); err != nil {
		t.Fatalf("CREATE ROLE norole: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE gone LOGIN"); err != nil {
		t.Fatalf("CREATE ROLE gone: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP ROLE gone"); err != nil {
		t.Fatalf("DROP ROLE gone: %v", err)
	}

	scramLogin := func(stage string) {
		t.Helper()
		host, port, err := splitHostPort(c.ListenAddr())
		if err != nil {
			t.Fatal(err)
		}
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
			host, port, roleName, rolePassword)
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			t.Fatalf("%s: sql.Open: %v", stage, err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var one int
		if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
			t.Fatalf("%s: SCRAM login as %s failed: %v", stage, roleName, err)
		}
	}

	// Pre-restart sanity: the credential authenticates and pg_roles reflects
	// the attributes.
	scramLogin("pre-restart")
	if got := queryScalar(t, c,
		"SELECT rolcanlogin FROM pg_roles WHERE rolname = 'wpuser'"); got != "t" {
		t.Fatalf("pre-restart pg_roles.rolcanlogin(wpuser) = %q, want t", got)
	}

	// Clean stop -> restart. Roles are restored from the pg_authid heap file
	// (+ any role WAL-record tail); nothing else carries them.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster: %v", err)
	}

	// (a) The role + its SCRAM verifier survived: a real handshake succeeds.
	scramLogin("post-restart")
	// (b) Attributes survived.
	if got := queryScalar(t, c,
		"SELECT rolcanlogin FROM pg_roles WHERE rolname = 'wpuser'"); got != "t" {
		t.Fatalf("post-restart pg_roles.rolcanlogin(wpuser) = %q, want t", got)
	}
	if got := queryScalar(t, c,
		"SELECT rolcanlogin FROM pg_roles WHERE rolname = 'norole'"); got != "f" {
		t.Fatalf("post-restart pg_roles.rolcanlogin(norole) = %q, want f (NOLOGIN lost)", got)
	}
	// (c) The dropped role stays gone.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_roles WHERE rolname = 'gone'"); got != "0" {
		t.Fatalf("post-restart dropped role count = %q, want 0 (DROP ROLE not durable)", got)
	}
	// (d) ALTER ROLE password change is durable too: change it, restart,
	// old password must fail and the new one succeed.
	if err := runSQLSimple(t, c, "ALTER ROLE wpuser PASSWORD 'rotated-secret'"); err != nil {
		t.Fatalf("ALTER ROLE PASSWORD: %v", err)
	}
	// (e) ALTER ROLE ... RENAME TO is durable too (root-0021 follow-up):
	// rename norole, restart, and confirm the new name carries the old
	// role's attributes forward while the old name is gone.
	if err := runSQLSimple(t, c, "ALTER ROLE norole RENAME TO renamed_role"); err != nil {
		t.Fatalf("ALTER ROLE RENAME TO: %v", err)
	}
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster (2): %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster (2): %v", err)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_roles WHERE rolname = 'norole'"); got != "0" {
		t.Fatalf("post-restart old role name count = %q, want 0 (RENAME TO not durable)", got)
	}
	if got := queryScalar(t, c,
		"SELECT rolcanlogin FROM pg_roles WHERE rolname = 'renamed_role'"); got != "f" {
		t.Fatalf("post-restart pg_roles.rolcanlogin(renamed_role) = %q, want f (renamed role lost attrs)", got)
	}
	host, port, err := splitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatal(err)
	}
	oldDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
		host, port, roleName, rolePassword)
	if db, err := sql.Open("postgres", oldDSN); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var one int
		if qerr := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); qerr == nil {
			t.Fatal("old password still authenticates after ALTER ROLE PASSWORD + restart")
		}
		cancel()
		_ = db.Close()
	}
	newDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=postgres sslmode=disable",
		host, port, roleName, "rotated-secret")
	db, err := sql.Open("postgres", newDSN)
	if err != nil {
		t.Fatalf("sql.Open (rotated): %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("rotated password login failed post-restart: %v", err)
	}
}
