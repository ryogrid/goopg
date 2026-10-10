package testport

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_AlterSystemWritesAutoConfAndSurvivesRestart pins M0122-0008 end to
// end through the real binary: ALTER SYSTEM rewrites postgresql.auto.conf
// without changing the running value; pg_reload_conf() applies it; it survives
// a restart (the boot path reads the auto file after postgresql.conf); ALTER
// SYSTEM RESET plus a reload reverts it; and PreventInTransactionBlock and the
// cannot-be-changed check fire as in PostgreSQL. Before, ALTER SYSTEM was a
// silent no-op and pg_reload_conf() did not exist.
func TestPort_AlterSystemWritesAutoConfAndSurvivesRestart(t *testing.T) {
	c, err := cluster.New("alter-system", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	before := queryScalar(t, c, "SHOW work_mem")
	if before == "7MB" {
		t.Fatalf("fixture: work_mem already 7MB")
	}
	if err := runSQLSimple(t, c, "ALTER SYSTEM SET work_mem = '7MB'"); err != nil {
		t.Fatalf("ALTER SYSTEM: %v", err)
	}
	if got := queryScalar(t, c, "SHOW work_mem"); got != before {
		t.Errorf("ALTER SYSTEM changed the running value before a reload: %q", got)
	}
	if got := queryScalar(t, c, "SELECT pg_reload_conf()"); got != "t" {
		t.Fatalf("pg_reload_conf() = %q", got)
	}
	if got := queryScalar(t, c, "SHOW work_mem"); got != "7MB" {
		t.Errorf("after pg_reload_conf(): work_mem = %q, want 7MB", got)
	}

	for stmt, want := range map[string]string{
		"BEGIN; ALTER SYSTEM SET work_mem = '1MB'": "ALTER SYSTEM cannot run inside a transaction block",
		"ALTER SYSTEM SET server_version = '1'":     `parameter "server_version" cannot be changed`,
		"ALTER SYSTEM SET no_such_param = 1":        `unrecognized configuration parameter "no_such_param"`,
	} {
		err := runSQLSimple(t, c, stmt)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v, want %q", stmt, err, want)
		}
	}

	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got := queryScalar(t, c, "SHOW work_mem"); got != "7MB" {
		t.Errorf("after restart: work_mem = %q, want 7MB from postgresql.auto.conf", got)
	}
	if err := runSQLSimple(t, c, "ALTER SYSTEM RESET work_mem"); err != nil {
		t.Fatalf("ALTER SYSTEM RESET: %v", err)
	}
	if got := queryScalar(t, c, "SELECT pg_reload_conf()"); got != "t" {
		t.Fatalf("pg_reload_conf() = %q", got)
	}
	if got := queryScalar(t, c, "SHOW work_mem"); got != before {
		t.Errorf("after RESET + reload: work_mem = %q, want %q", got, before)
	}
}
