package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpRoleConfigSet verifies the pg_db_role_setting.setconfig
// round-trip for `ALTER ROLE ... [IN DATABASE ...] SET/RESET`
// (M0119-0004-ACLHEAP, ALTER ROLE ... SET follow-up): goopg's parser has no
// ALTER ROLE ... SET grammar at all, so SET/RESET are intercepted at the
// wire-protocol dispatch layer (parseAlterRoleConfig,
// internal/server/role_ddl.go), written into a new per-role GUC override
// registry (the setrole != 0 complement of TestPort_PgDumpDatabaseConfigSet's
// setrole=0 registry), and projected through the same pg_db_role_setting
// virtual catalog.
//
// Like TestPort_PgDumpDatabaseConfigSet, the `ALTER ROLE ... IN DATABASE ...
// SET ...` lines only appear in dumpDatabaseConfig's second query (pg_dump.c
// "role-and-database-specific options"), which dumpDatabase only calls under
// -C/--create, so this test invokes pg_dump with --create explicitly. A
// plain cluster-wide `ALTER ROLE ... SET` (no IN DATABASE, setdatabase=0) is
// dumped by pg_dumpall's dumpUserConfig instead — a separate binary outside
// this milestone's pg_dump-only scope — so this test only exercises the IN
// DATABASE form, though the engine-level SET/RESET/RESET ALL handling is
// shared by both forms (see TestTryHandleRoleDDLAlterRoleConfig for the
// cluster-wide-scope unit coverage).
func TestPort_PgDumpRoleConfigSet(t *testing.T) {
	bin := clientToolBin(t, "pg_dump")
	if bin == "" {
		t.Skip("pg_dump not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumprolecfg")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE cfgrole LOGIN"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres SET work_mem = '64MB'"); err != nil {
		t.Fatalf("alter role in database set work_mem: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres SET search_path TO public, pg_catalog"); err != nil {
		t.Fatalf("alter role in database set search_path: %v", err)
	}
	// SET followed by RESET of a DIFFERENT config must leave the first
	// entry alone (verifies RESET only removes the named entry, not the
	// whole override list).
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres SET statement_timeout = 5000"); err != nil {
		t.Fatalf("alter role in database set statement_timeout: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres RESET statement_timeout"); err != nil {
		t.Fatalf("alter role in database reset statement_timeout: %v", err)
	}
	// Re-SET an already-overridden config: must replace in place, not
	// append a second entry.
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres SET work_mem = '128MB'"); err != nil {
		t.Fatalf("alter role in database re-set work_mem: %v", err)
	}
	// A cluster-wide SET (no IN DATABASE) must NOT leak into the IN-DATABASE
	// dump surface — it is setdatabase=0, not setdatabase=<dboid>.
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole SET lock_timeout = 1000"); err != nil {
		t.Fatalf("alter role cluster-wide set lock_timeout: %v", err)
	}

	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "--create", "--schema-only", "postgres"},
		Env:     amcheckEnv(t, c),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dump: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pg_dump --create exited %d\nstderr=%s", res.ExitCode, res.Stderr)
	}

	want := []string{
		`ALTER ROLE cfgrole IN DATABASE postgres SET work_mem TO '128MB';`,
		`ALTER ROLE cfgrole IN DATABASE postgres SET search_path TO 'public', 'pg_catalog';`,
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dump --create output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	// (The dump preamble always carries boilerplate `SET statement_timeout =
	// 0;`/`SET lock_timeout = 0;` lines unrelated to these GUC overrides, so
	// the negative checks must anchor on the ALTER ROLE form specifically.)
	if strings.Contains(res.Stdout, "ALTER ROLE cfgrole IN DATABASE postgres SET statement_timeout") {
		t.Errorf("pg_dump --create output should not carry the reset statement_timeout override\nfull stdout=%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "SET lock_timeout TO") {
		t.Errorf("pg_dump --create output should not carry the cluster-wide (non-IN-DATABASE) override\nfull stdout=%s", res.Stdout)
	}
	if strings.Count(res.Stdout, "SET work_mem") != 1 {
		t.Errorf("expected exactly one SET work_mem line (re-SET must replace in place), got:\n%s", res.Stdout)
	}
}
