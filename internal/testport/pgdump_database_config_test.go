package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpDatabaseConfigSet verifies the pg_db_role_setting.setconfig
// round-trip for `ALTER DATABASE ... SET/RESET` (M0119-0004-ACLHEAP, ALTER
// DATABASE ... SET follow-up): goopg's parser has no ALTER DATABASE grammar
// at all (requires the literal TABLE keyword after ALTER), so SET/RESET are
// intercepted at the wire-protocol dispatch layer (parseAlterDatabaseConfig,
// internal/server/database_ddl.go), written into a new per-database GUC
// override registry, and projected through the (previously permanently
// empty) pg_db_role_setting virtual catalog.
//
// Like TestPort_PgDumpDatabaseGrantACL (the datacl half's own test), the
// `ALTER DATABASE ... SET ...` lines only appear in dumpDatabaseConfig,
// which dumpDatabase only calls under -C/--create (pg_dump.c: `if
// (dopt.outputCreateDB) dumpDatabase(fout);`), so this test invokes pg_dump
// with --create explicitly. The exact `ALTER DATABASE name SET config TO
// 'value';` text is reconstructed CLIENT-SIDE by the real pg_dump binary
// (makeAlterConfigCommand, dumputils.c) from the raw setconfig array text
// goopg's SELECT path returns — goopg only needs to store the correct raw
// "name=value" entries, not replicate pg_dump's quoting logic.
func TestPort_PgDumpDatabaseConfigSet(t *testing.T) {
	bin := clientToolBin(t, "pg_dump")
	if bin == "" {
		t.Skip("pg_dump not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpdbcfg")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "ALTER DATABASE postgres SET work_mem = '64MB'"); err != nil {
		t.Fatalf("alter database set work_mem: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER DATABASE postgres SET search_path TO public, pg_catalog"); err != nil {
		t.Fatalf("alter database set search_path: %v", err)
	}
	// SET followed by RESET of a DIFFERENT config must leave the first
	// entry alone (verifies RESET only removes the named entry, not the
	// whole override list).
	if err := runSQLSimple(t, c, "ALTER DATABASE postgres SET statement_timeout = 5000"); err != nil {
		t.Fatalf("alter database set statement_timeout: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER DATABASE postgres RESET statement_timeout"); err != nil {
		t.Fatalf("alter database reset statement_timeout: %v", err)
	}
	// Re-SET an already-overridden config: must replace in place, not
	// append a second entry.
	if err := runSQLSimple(t, c, "ALTER DATABASE postgres SET work_mem = '128MB'"); err != nil {
		t.Fatalf("alter database re-set work_mem: %v", err)
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
		"ALTER DATABASE postgres SET work_mem TO '128MB';",
		"ALTER DATABASE postgres SET search_path TO 'public', 'pg_catalog';",
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dump --create output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "ALTER DATABASE postgres SET statement_timeout") {
		t.Errorf("pg_dump --create output should not carry the reset statement_timeout override\nfull stdout=%s", res.Stdout)
	}
	if strings.Count(res.Stdout, "SET work_mem") != 1 {
		t.Errorf("expected exactly one SET work_mem line (re-SET must replace in place), got:\n%s", res.Stdout)
	}
}
