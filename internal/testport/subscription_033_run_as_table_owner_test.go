package testport

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription033RunAsTableOwner ports
// postgres/src/test/subscription/t/033_run_as_table_owner.pl to the M0149
// contract: with run_as_owner = true the apply worker acts as the
// subscription owner, so replication needs that role's own privileges (or
// the table owner's through an INHERIT membership) — a SET-only membership
// is not enough — and SELECT for UPDATE / DELETE; switching to
// run_as_owner = false lets a SET ROLE membership replicate as the table
// owner, which also performs the initial data copy. Three pairings:
// all-goopg, a PG 18.3 publisher with a goopg subscriber, a goopg publisher
// with a PG 18.3 subscriber.
//
// Ported in M0149-0058.
func TestPort_Subscription033RunAsTableOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	cases := []struct {
		name                  string
		publisher, subscriber func(t *testing.T, name string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopg},
		{"pg18-publisher-goopg-subscriber", pg, goopg},
		{"goopg-publisher-pg18-subscriber", goopg, pg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubscriptionRunAsTableOwner(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionRunAsTableOwner(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	nodePublisher.AppendConf("wal_level = logical")
	nodePublisher.AppendConf("max_wal_senders = 10")
	nodePublisher.AppendConf("max_replication_slots = 10")
	nodePublisher.Start()
	nodeSubscriber.Start()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, n := range []*tapnode.Node{nodeSubscriber, nodePublisher} {
			for _, l := range strings.Split(fileTail(n.LogPath(), 2000), "\n") {
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		}
	})
	publisherConnstr := nodePublisher.ConnStr()
	var offset int64

	publishInsert := func(tbl string, newI int) {
		nodePublisher.SafePSQL("postgres", fmt.Sprintf(`
  SET SESSION AUTHORIZATION regress_alice;
  INSERT INTO %s (i) VALUES (%d);
  `, tbl, newI))
	}
	publishUpdate := func(tbl string, oldI, newI int) {
		nodePublisher.SafePSQL("postgres", fmt.Sprintf(`
  SET SESSION AUTHORIZATION regress_alice;
  UPDATE %s SET i = %d WHERE i = %d;
  `, tbl, newI, oldI))
	}
	publishDelete := func(tbl string, oldI int) {
		nodePublisher.SafePSQL("postgres", fmt.Sprintf(`
  SET SESSION AUTHORIZATION regress_alice;
  DELETE FROM %s WHERE i = %d;
  `, tbl, oldI))
	}
	expectReplication := func(tbl string, cnt, min, max int, testname string) {
		t.Helper()
		waitForSubCatchup(t, nodePublisher, "admin_sub")
		is(nodeSubscriber.SafePSQL("postgres", "SELECT COUNT(i), MIN(i), MAX(i) FROM "+tbl), fmt.Sprintf("%d|%d|%d", cnt, min, max), testname)
	}
	expectFailure := func(tbl string, cnt, min, max int, re *regexp.Regexp, testname string) {
		t.Helper()
		offset = waitForLogOffset(t, nodeSubscriber, re, offset)
		is(nodeSubscriber.SafePSQL("postgres", "SELECT COUNT(i), MIN(i), MAX(i) FROM "+tbl), fmt.Sprintf("%d|%d|%d", cnt, min, max), testname)
	}
	revokeSuperuser := func(role string) { nodeSubscriber.SafePSQL("postgres", "ALTER ROLE "+role+" NOSUPERUSER") }
	permErr := regexp.MustCompile(`(?msi)ERROR: ( [A-Z0-9]+:)? permission denied for table unpartitioned`)

	// Create the same roles, schema and table on both nodes.
	for _, n := range []*tapnode.Node{nodePublisher, nodeSubscriber} {
		n.SafePSQL("postgres", `
  CREATE ROLE regress_admin SUPERUSER LOGIN;
  CREATE ROLE regress_admin2 SUPERUSER LOGIN;
  CREATE ROLE regress_alice NOSUPERUSER LOGIN;
  GRANT CREATE ON DATABASE postgres TO regress_alice;
  SET SESSION AUTHORIZATION regress_alice;
  CREATE SCHEMA alice;
  GRANT USAGE ON SCHEMA alice TO regress_admin;
  CREATE TABLE alice.unpartitioned (i INTEGER);
  ALTER TABLE alice.unpartitioned REPLICA IDENTITY FULL;
  GRANT SELECT ON TABLE alice.unpartitioned TO regress_admin;
  `)
	}

	// Create a subscription as the superuser regress_admin.
	nodePublisher.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
CREATE PUBLICATION alice FOR TABLE alice.unpartitioned
  WITH (publish_via_partition_root = true);
`)
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_admin;
CREATE SUBSCRIPTION admin_sub CONNECTION '`+publisherConnstr+`' PUBLICATION alice
	WITH (run_as_owner = true, password_required = false);
`)

	// Wait for initial sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "admin_sub")

	// Verify that "regress_admin" can replicate into the tables
	publishInsert("alice.unpartitioned", 1)
	publishInsert("alice.unpartitioned", 3)
	publishInsert("alice.unpartitioned", 5)
	publishUpdate("alice.unpartitioned", 1, 7)
	publishDelete("alice.unpartitioned", 3)
	expectReplication("alice.unpartitioned", 2, 5, 7, "superuser can replicate")

	// Revoke superuser privilege for "regress_admin", and verify that we now
	// fail to replicate an insert.
	revokeSuperuser("regress_admin")
	publishInsert("alice.unpartitioned", 9)
	expectFailure("alice.unpartitioned", 2, 5, 7, permErr, "with no privileges cannot replicate")

	// Now grant DML privileges and verify that we can replicate an INSERT.
	nodeSubscriber.SafePSQL("postgres", `
ALTER ROLE regress_admin NOSUPERUSER;
SET SESSION AUTHORIZATION regress_alice;
GRANT INSERT,UPDATE,DELETE ON alice.unpartitioned TO regress_admin;
REVOKE SELECT ON alice.unpartitioned FROM regress_admin;
`)
	expectReplication("alice.unpartitioned", 3, 5, 9, "with INSERT privilege can replicate INSERT")

	// We can't yet replicate an UPDATE because we don't have SELECT.
	publishUpdate("alice.unpartitioned", 5, 11)
	publishDelete("alice.unpartitioned", 9)
	expectFailure("alice.unpartitioned", 3, 5, 9, permErr, "without SELECT privilege cannot replicate UPDATE or DELETE")

	// After granting SELECT, replication resumes.
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
GRANT SELECT ON alice.unpartitioned TO regress_admin;
`)
	expectReplication("alice.unpartitioned", 2, 7, 11, "with all privileges can replicate")

	// Remove all privileges again. Instead, give the ability to SET ROLE to
	// regress_alice.
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
REVOKE ALL PRIVILEGES ON alice.unpartitioned FROM regress_admin;
RESET SESSION AUTHORIZATION;
GRANT regress_alice TO regress_admin WITH INHERIT FALSE, SET TRUE;
`)

	// Because replication is running as the subscription owner in this test,
	// the above grant doesn't help.
	publishInsert("alice.unpartitioned", 13)
	expectFailure("alice.unpartitioned", 2, 7, 11, permErr, "with SET ROLE but not INHERIT cannot replicate")

	// Allow the replication to run as table owner and check that things
	// start working.
	nodeSubscriber.SafePSQL("postgres", "\nGRANT regress_alice TO regress_admin WITH INHERIT TRUE, SET FALSE;\n")
	expectReplication("alice.unpartitioned", 3, 7, 13, "with INHERIT but not SET ROLE can replicate")

	// Similar to the previous test, remove all privileges again and enable
	// replication to run as table owner.
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
REVOKE ALL PRIVILEGES ON alice.unpartitioned FROM regress_admin;
RESET SESSION AUTHORIZATION;
GRANT regress_alice TO regress_admin WITH INHERIT FALSE, SET TRUE;
`)

	// Because replication is running as the subscription owner in this test,
	// the above grant doesn't help.
	publishInsert("alice.unpartitioned", 14)
	expectFailure("alice.unpartitioned", 3, 7, 13, permErr, "with no privileges cannot replicate")

	// Allow the replication to run as table owner and check that things
	// start working.
	nodeSubscriber.SafePSQL("postgres", "\nALTER SUBSCRIPTION admin_sub SET (run_as_owner = false);\n")
	expectReplication("alice.unpartitioned", 4, 7, 14, "can replicate after setting run_as_owner to false")

	// Remove the subscription and truncate the table for the initial data
	// sync tests.
	nodeSubscriber.SafePSQL("postgres", `
DROP SUBSCRIPTION admin_sub;
TRUNCATE alice.unpartitioned;
`)

	// Create a new subscription "admin_sub" owned by regress_admin2. It's
	// disabled so that we revoke superuser privilege after creation.
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_admin2;
CREATE SUBSCRIPTION admin_sub CONNECTION '`+publisherConnstr+`' PUBLICATION alice
WITH (run_as_owner = false, password_required = false, copy_data = true, enabled = false);
`)

	// Revoke superuser privilege for "regress_admin2", and give it the
	// ability to SET ROLE. Then enable the subscription "admin_sub".
	revokeSuperuser("regress_admin2")
	nodeSubscriber.SafePSQL("postgres", `
GRANT regress_alice TO regress_admin2 WITH INHERIT FALSE, SET TRUE;
ALTER SUBSCRIPTION admin_sub ENABLE;
`)

	// Because the initial data sync is working as the table owner, all data
	// should be copied.
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "admin_sub")
	expectReplication("alice.unpartitioned", 4, 7, 14, "table owner can do the initial data copy")
}
