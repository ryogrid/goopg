package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpallGlobalsOnly ports the pg_dumpall half of the
// pg_dump/pg_dumpall TAP suite's globals-dump coverage (M0119-0004-ACLHEAP
// follow-up, source: the "pg_dumpall's cluster-wide dump surface untested"
// residual left open by TestPort_PgDumpRoleConfigSet's ledger row).
//
// pg_dumpall --globals-only queries pg_authid, pg_auth_members and
// pg_parameter_acl directly (not pg_roles/pg_dump's per-database ACL
// surface), none of which goopg's virtual catalog registered before this
// test was written — pg_dumpall failed outright with "relation \"pg_authid\"
// does not exist" (discovered by probing pg_dumpall manually, not assumed
// from the ledger's "pure TAP-porting work" note, which undersold the gap).
// pg_auth_members/pg_parameter_acl are registered correctly-empty: goopg has
// no `GRANT <role> TO <role>` or `GRANT ... ON PARAMETER` support at all, a
// distinct capability from object-level GRANT (deferral ledger).
//
// This test exercises the one live, engine-supported surface:
// pg_dumpall's dumpUserConfig (cluster-wide `ALTER ROLE ... SET`,
// setdatabase = 0), which TestPort_PgDumpRoleConfigSet's own test explicitly
// left untested since it drives pg_dump --create, not pg_dumpall.
func TestPort_PgDumpallGlobalsOnly(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpallglobals")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE cfgrole LOGIN"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	// Cluster-wide (no IN DATABASE) SET — dumped by pg_dumpall's
	// dumpUserConfig, NOT pg_dump --create's dumpDatabaseConfig.
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole SET work_mem = '64MB'"); err != nil {
		t.Fatalf("alter role set work_mem: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole SET search_path TO public, pg_catalog"); err != nil {
		t.Fatalf("alter role set search_path: %v", err)
	}
	// SET followed by RESET of a different config must leave the first
	// entry alone.
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole SET statement_timeout = 5000"); err != nil {
		t.Fatalf("alter role set statement_timeout: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole RESET statement_timeout"); err != nil {
		t.Fatalf("alter role reset statement_timeout: %v", err)
	}
	// An IN-DATABASE override must NOT leak into pg_dumpall's cluster-wide
	// surface — it is setdatabase=<dboid>, not setdatabase=0.
	if err := runSQLSimple(t, c, "ALTER ROLE cfgrole IN DATABASE postgres SET lock_timeout = 1000"); err != nil {
		t.Fatalf("alter role in database set lock_timeout: %v", err)
	}

	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    []string{"--no-sync", "--globals-only"},
		Env:     amcheckEnv(t, c),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_dumpall: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("pg_dumpall --globals-only exited %d\nstdout=%s\nstderr=%s", res.ExitCode, res.Stdout, res.Stderr)
	}

	want := []string{
		"CREATE ROLE cfgrole;",
		`ALTER ROLE cfgrole SET work_mem TO '64MB';`,
		`ALTER ROLE cfgrole SET search_path TO 'public', 'pg_catalog';`,
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dumpall --globals-only output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "SET statement_timeout TO") {
		t.Errorf("pg_dumpall output should not carry the reset statement_timeout override\nfull stdout=%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "SET lock_timeout TO") {
		t.Errorf("pg_dumpall output should not carry the IN-DATABASE-scoped override\nfull stdout=%s", res.Stdout)
	}
}
