package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpDatabaseGrantACL verifies the pg_database.datacl GRANT
// round-trip (M0119-0004-ACLHEAP, datacl half): a `GRANT … ON DATABASE …`
// must update the OID-keyed ACL store AND re-sync the heap-backed
// pg_database row (pg_database is a SHARED cluster-wide catalog, unlike the
// per-database pg_type/pg_attribute rows the sibling typacl/attacl slices
// resync) so pg_dump reads the granted ACL back and re-emits the GRANT.
//
// Unlike TestPort_PgDumpConnectionSetup (which runs pg_dump with the default
// --no-create posture), dumpDatabase's ACL section is only emitted when
// dopt.outputCreateDB is set (pg_dump.c: `if (dopt.outputCreateDB)
// dumpDatabase(fout);`) — i.e. only under -C/--create — so this test invokes
// pg_dump with --create explicitly.
func TestPort_PgDumpDatabaseGrantACL(t *testing.T) {
	bin := clientToolBin(t, "pg_dump")
	if bin == "" {
		t.Skip("pg_dump not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpdbacl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE grantee_db NOLOGIN"); err != nil {
		t.Fatalf("create role: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT CREATE ON DATABASE postgres TO PUBLIC"); err != nil {
		t.Fatalf("grant create on database to public: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT TEMPORARY ON DATABASE postgres TO grantee_db"); err != nil {
		t.Fatalf("grant temporary on database to grantee_db: %v", err)
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

	// PUBLIC already implicitly holds TEMPORARY+CONNECT via acldefault('d', …)
	// (the world_default). PostgreSQL materializes an object's FULL default ACL
	// (owner set + world set) into the array on its first GRANT/REVOKE, then
	// applies the delta — so granting CREATE to PUBLIC makes PUBLIC's effective
	// set {CREATE, TEMPORARY, CONNECT}, the complete ACL_ALL_RIGHTS_DATABASE
	// set, which pg_dump's buildACLCommands collapses to GRANT ALL (preceded by
	// a REVOKE of the implicit world_default so a restore starts PUBLIC at
	// zero). Confirmed against real pg_dump 18.3 output, not assumed.
	want := []string{
		"CREATE DATABASE postgres",
		"REVOKE CONNECT,TEMPORARY ON DATABASE postgres FROM PUBLIC;",
		"GRANT ALL ON DATABASE postgres TO PUBLIC;",
		"GRANT TEMPORARY ON DATABASE postgres TO grantee_db;",
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dump --create output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
}
