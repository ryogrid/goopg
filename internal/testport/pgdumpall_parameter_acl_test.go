package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpallParameterACL ports pg_dumpall's "Role privileges on
// configuration parameters" section (dumpRoleGUCPrivs, pg_dumpall.c), which
// queries pg_parameter_acl directly. This closes the "GRANT ... ON PARAMETER"
// item left open by the GRANT/REVOKE role-membership deferral-ledger row
// (M0119-0004-ACLHEAP): pg_parameter_acl was registered correctly-empty (no
// GRANT ON PARAMETER support at all); it is now populated by
// execParameterACLChange on the first GRANT for a given GUC.
func TestPort_PgDumpallParameterACL(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpallparamacl")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE paramgrantee LOGIN"); err != nil {
		t.Fatalf("create role paramgrantee: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT SET ON PARAMETER work_mem TO paramgrantee"); err != nil {
		t.Fatalf("grant set on parameter work_mem: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT ALTER SYSTEM ON PARAMETER statement_timeout TO paramgrantee"); err != nil {
		t.Fatalf("grant alter system on parameter statement_timeout: %v", err)
	}
	// A subsequent REVOKE must remove the aclitem entirely so it does not
	// appear in the re-emitted GRANT list.
	if err := runSQLSimple(t, c, "GRANT SET ON PARAMETER log_min_duration_statement TO paramgrantee"); err != nil {
		t.Fatalf("grant set on parameter log_min_duration_statement: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE SET ON PARAMETER log_min_duration_statement FROM paramgrantee"); err != nil {
		t.Fatalf("revoke set on parameter log_min_duration_statement: %v", err)
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
		"-- Role privileges on configuration parameters",
		"GRANT SET ON PARAMETER work_mem TO paramgrantee;",
		"GRANT ALTER SYSTEM ON PARAMETER statement_timeout TO paramgrantee;",
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dumpall --globals-only output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "log_min_duration_statement") {
		t.Errorf("fully-revoked parameter ACL should not appear in the dump\nfull stdout=%s", res.Stdout)
	}
}
