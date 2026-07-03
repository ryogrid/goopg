package testport

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// TestPort_PgDumpallRoleMembership ports the pg_dumpall "Role memberships"
// section coverage — the GRANT/REVOKE ROLE membership capability
// TestPort_PgDumpallGlobalsOnly's own ledger row left as "genuinely
// unimplemented" (pg_auth_members registered correctly-empty, no
// `GRANT <role> TO <role>` support at all). M0119-0004-ACLHEAP.
//
// pg_dumpall's dumpRoleMembership (pg_dumpall.c) can only dump a membership
// row whose grantor is either the bootstrap superuser or a role that has
// already been dumped WITH ADMIN OPTION earlier in the same output — any
// other grantor makes it bail out with "could not find a legal dump
// ordering" and exit(1). goopg's default grantor (no `GRANTED BY` clause) is
// always the executing session's effective role, which in this test is the
// bootstrap superuser "postgres" — the always-legal case — so this test does
// not attempt to exercise a non-bootstrap grantor chain.
func TestPort_PgDumpallRoleMembership(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpallrolemembers")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE memadmin"); err != nil {
		t.Fatalf("create role memadmin: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE memalice LOGIN"); err != nil {
		t.Fatalf("create role memalice: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE membob LOGIN"); err != nil {
		t.Fatalf("create role membob: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT memadmin TO memalice WITH ADMIN OPTION"); err != nil {
		t.Fatalf("grant memadmin to memalice: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT memadmin TO membob"); err != nil {
		t.Fatalf("grant memadmin to membob: %v", err)
	}
	// A subsequent REVOKE must remove the row from the dump entirely.
	if err := runSQLSimple(t, c, "CREATE ROLE memcarol LOGIN"); err != nil {
		t.Fatalf("create role memcarol: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT memadmin TO memcarol"); err != nil {
		t.Fatalf("grant memadmin to memcarol: %v", err)
	}
	if err := runSQLSimple(t, c, "REVOKE memadmin FROM memcarol"); err != nil {
		t.Fatalf("revoke memadmin from memcarol: %v", err)
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
		"-- Role memberships",
		`GRANT memadmin TO memalice WITH ADMIN OPTION, INHERIT TRUE GRANTED BY postgres;`,
		`GRANT memadmin TO membob WITH INHERIT TRUE GRANTED BY postgres;`,
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dumpall --globals-only output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "GRANT memadmin TO memcarol") {
		t.Errorf("revoked membership should not appear in the dump\nfull stdout=%s", res.Stdout)
	}
}

// TestPort_PgDumpallPredefinedRoleMembership pins the exact regression
// M0119-0004-ACLHEAP's pg_authid/pg_roles predefined-role rows fixed
// (catalog.go's pgRoles/pgAuthid VirtualRows): before that fix, pg_authid and
// pg_roles projected only user-created roles, so dumpRoleMembership's `LEFT
// JOIN pg_roles ur ON ur.oid = a.roleid` came back NULL for a grant whose
// roleid is one of PG18's 16 built-in "pg_*" predefined roles (e.g.
// pg_read_all_data, OID 6181 — not stored in goopg's user-role registry).
// pg_dumpall.c's dumpRoleMembership treats a NULL `role` column as an
// "orphaned pg_auth_members entry", logs a warning, and — because rows are
// ORDER BY role and NULLs sort last — `break`s out of the loop entirely,
// silently dropping every remaining membership row in the dump, not just the
// one naming the predefined role.
func TestPort_PgDumpallPredefinedRoleMembership(t *testing.T) {
	bin := clientToolBin(t, "pg_dumpall")
	if bin == "" {
		t.Skip("pg_dumpall not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "pgdumpallpredefinedrole")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE ROLE predalice LOGIN"); err != nil {
		t.Fatalf("create role predalice: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT pg_read_all_data TO predalice"); err != nil {
		t.Fatalf("grant pg_read_all_data to predalice: %v", err)
	}
	// A membership row sorting AFTER the predefined-role grant (by role name)
	// that must still be dumped — proves the loop doesn't bail out early.
	if err := runSQLSimple(t, c, "CREATE ROLE predbob LOGIN"); err != nil {
		t.Fatalf("create role predbob: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE ROLE predtrailing"); err != nil {
		t.Fatalf("create role predtrailing: %v", err)
	}
	if err := runSQLSimple(t, c, "GRANT predtrailing TO predbob"); err != nil {
		t.Fatalf("grant predtrailing to predbob: %v", err)
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
		`GRANT pg_read_all_data TO predalice WITH INHERIT TRUE GRANTED BY postgres;`,
		`GRANT predtrailing TO predbob WITH INHERIT TRUE GRANTED BY postgres;`,
	}
	for _, sub := range want {
		if !strings.Contains(res.Stdout, sub) {
			t.Errorf("pg_dumpall --globals-only output missing %q\nfull stdout=%s", sub, res.Stdout)
		}
	}
	if strings.Contains(res.Stderr, "orphaned pg_auth_members") {
		t.Errorf("pg_dumpall reported an orphaned pg_auth_members entry\nstderr=%s", res.Stderr)
	}
}
