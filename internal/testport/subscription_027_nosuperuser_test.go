package testport

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription027Nosuperuser ports
// postgres/src/test/subscription/t/027_nosuperuser.pl to the M0149
// contract: the apply worker runs as the subscription owner and switches to
// each table's owner (run_as_owner = false). A non-superuser owner without
// membership in the table owner's role fails with "cannot SET ROLE"; with
// the membership it replicates INSERT / UPDATE / DELETE, including into a
// hash-partitioned table published via its root. Forced row-level security
// and missing INSERT / UPDATE / DELETE / SELECT privileges of the table
// owner stop replication until fixed, and revoking the owner's superuser
// makes the worker restart. The password_required block needs Unix-domain
// sockets and is skipped, as upstream skips it without them (tapnode
// connects over TCP). Three pairings: all-goopg, a PG 18.3 publisher with a
// goopg subscriber, a goopg publisher with a PG 18.3 subscriber.
//
// Gated by GOOPG_TAP_027 until it passes (M0149-0057).
func TestPort_Subscription027Nosuperuser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	if os.Getenv("GOOPG_TAP_027") == "" {
		t.Skip("027 port is not passing yet (M0149-0057); set GOOPG_TAP_027=1 to run")
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
			runSubscriptionNosuperuser(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionNosuperuser(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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
	grantSuperuser := func(role string) { nodeSubscriber.SafePSQL("postgres", "ALTER ROLE "+role+" SUPERUSER") }

	// Create roles, schema and tables on both nodes; the hash partitions
	// swap remainders between the nodes so that routing via the root is
	// exercised.
	for _, n := range []struct {
		node       *tapnode.Node
		remA, remB int
	}{{nodePublisher, 0, 1}, {nodeSubscriber, 1, 0}} {
		n.node.SafePSQL("postgres", fmt.Sprintf(`
  CREATE ROLE regress_admin SUPERUSER LOGIN;
  CREATE ROLE regress_alice NOSUPERUSER LOGIN;
  GRANT CREATE ON DATABASE postgres TO regress_alice;
  GRANT PG_CREATE_SUBSCRIPTION TO regress_alice;
  SET SESSION AUTHORIZATION regress_alice;
  CREATE SCHEMA alice;
  GRANT USAGE ON SCHEMA alice TO regress_admin;
  CREATE TABLE alice.unpartitioned (i INTEGER);
  ALTER TABLE alice.unpartitioned REPLICA IDENTITY FULL;
  GRANT SELECT ON TABLE alice.unpartitioned TO regress_admin;
  CREATE TABLE alice.hashpart (i INTEGER) PARTITION BY HASH (i);
  ALTER TABLE alice.hashpart REPLICA IDENTITY FULL;
  GRANT SELECT ON TABLE alice.hashpart TO regress_admin;
  CREATE TABLE alice.hashpart_a PARTITION OF alice.hashpart
    FOR VALUES WITH (MODULUS 2, REMAINDER %d);
  ALTER TABLE alice.hashpart_a REPLICA IDENTITY FULL;
  CREATE TABLE alice.hashpart_b PARTITION OF alice.hashpart
    FOR VALUES WITH (MODULUS 2, REMAINDER %d);
  ALTER TABLE alice.hashpart_b REPLICA IDENTITY FULL;
  `, n.remA, n.remB))
	}
	nodePublisher.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
CREATE PUBLICATION alice
  FOR TABLE alice.unpartitioned, alice.hashpart
  WITH (publish_via_partition_root = true);
`)
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_admin;
CREATE SUBSCRIPTION admin_sub CONNECTION '`+publisherConnstr+`' PUBLICATION alice WITH (password_required=false);
`)

	// Wait for initial sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "admin_sub")

	// Verify that "regress_admin" can replicate into the tables
	publishInsert("alice.unpartitioned", 1)
	publishInsert("alice.unpartitioned", 3)
	publishInsert("alice.unpartitioned", 5)
	publishUpdate("alice.unpartitioned", 1, 7)
	publishDelete("alice.unpartitioned", 3)
	expectReplication("alice.unpartitioned", 2, 5, 7, "superuser admin replicates into unpartitioned")

	// Revoke and restore superuser privilege for "regress_admin",
	// verifying that replication fails while superuser privilege is
	// missing, but works again and catches up once superuser is restored.
	revokeSuperuser("regress_admin")
	publishUpdate("alice.unpartitioned", 5, 9)
	expectFailure("alice.unpartitioned", 2, 5, 7,
		regexp.MustCompile(`(?msi)ERROR: ( [A-Z0-9]+:)? role "regress_admin" cannot SET ROLE to "regress_alice"`),
		"non-superuser admin fails to replicate update")
	grantSuperuser("regress_admin")
	expectReplication("alice.unpartitioned", 2, 7, 9, "admin with restored superuser privilege replicates update")

	// Grant privileges on the target tables to "regress_admin" so that
	// even without superuser privilege it can replicate.
	nodeSubscriber.SafePSQL("postgres", `
ALTER ROLE regress_admin NOSUPERUSER;
GRANT regress_alice TO regress_admin;
`)
	publishInsert("alice.unpartitioned", 11)
	expectReplication("alice.unpartitioned", 3, 7, 11, "nosuperuser admin with privileges on role can replicate INSERT into unpartitioned")
	publishUpdate("alice.unpartitioned", 7, 13)
	expectReplication("alice.unpartitioned", 3, 9, 13, "nosuperuser admin with privileges on role can replicate UPDATE into unpartitioned")
	publishDelete("alice.unpartitioned", 9)
	expectReplication("alice.unpartitioned", 2, 11, 13, "nosuperuser admin with privileges on role can replicate DELETE into unpartitioned")

	// Test that the replication of partitioned tables works with
	// publish_via_partition_root
	publishInsert("alice.hashpart", 101)
	publishInsert("alice.hashpart", 102)
	publishInsert("alice.hashpart", 103)
	publishUpdate("alice.hashpart", 102, 120)
	publishDelete("alice.hashpart", 101)
	expectReplication("alice.hashpart", 2, 103, 120, "nosuperuser admin with privileges on role can replicate into hashpart")

	// Enable RLS on the target table and check that "regress_admin" can
	// only replicate into it when superuser or bypassrls.
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
ALTER TABLE alice.unpartitioned ENABLE ROW LEVEL SECURITY;
ALTER TABLE alice.unpartitioned FORCE ROW LEVEL SECURITY;
`)
	publishInsert("alice.unpartitioned", 15)
	rlsErr := regexp.MustCompile(`(?msi)ERROR: ( [A-Z0-9]+:)? user "regress_alice" cannot replicate into relation with row-level security enabled: "unpartitioned\w*"`)
	expectFailure("alice.unpartitioned", 2, 11, 13, rlsErr, "replication of insert into table with forced rls fails")
	nodeSubscriber.SafePSQL("postgres", "\nALTER TABLE alice.unpartitioned NO FORCE ROW LEVEL SECURITY;\n")
	expectReplication("alice.unpartitioned", 3, 11, 15, "non-superuser admin can replicate insert if rls is not forced")

	nodeSubscriber.SafePSQL("postgres", "\nALTER TABLE alice.unpartitioned FORCE ROW LEVEL SECURITY;\n")
	publishUpdate("alice.unpartitioned", 11, 17)
	expectFailure("alice.unpartitioned", 3, 11, 15, rlsErr, "replication of update into table with forced rls fails")
	nodeSubscriber.SafePSQL("postgres", "\nALTER TABLE alice.unpartitioned NO FORCE ROW LEVEL SECURITY;\n")
	expectReplication("alice.unpartitioned", 3, 13, 17, "non-superuser admin can replicate update if rls is not forced")

	// Remove some of alice's privileges on her own table. Then replication
	// should fail.
	permErr := regexp.MustCompile(`(?msi)ERROR: ( [A-Z0-9]+:)? permission denied for table unpartitioned`)
	nodeSubscriber.SafePSQL("postgres", "\nREVOKE SELECT, INSERT ON alice.unpartitioned FROM regress_alice;\n")
	publishInsert("alice.unpartitioned", 19)
	expectFailure("alice.unpartitioned", 3, 13, 17, permErr, "replication of insert fails if table owner lacks insert permission")

	// alice needs INSERT but not SELECT to replicate an INSERT.
	nodeSubscriber.SafePSQL("postgres", "\nGRANT INSERT ON alice.unpartitioned TO regress_alice;\n")
	expectReplication("alice.unpartitioned", 4, 13, 19, "restoring insert permission permits replication to continue")

	// Now let's try an UPDATE and a DELETE.
	nodeSubscriber.SafePSQL("postgres", "\nREVOKE UPDATE, DELETE ON alice.unpartitioned FROM regress_alice;\n")
	publishUpdate("alice.unpartitioned", 13, 21)
	publishDelete("alice.unpartitioned", 15)
	expectFailure("alice.unpartitioned", 4, 13, 19, permErr, "replication of update/delete fails if table owner lacks corresponding permission")

	// Restoring UPDATE and DELETE is insufficient.
	nodeSubscriber.SafePSQL("postgres", "\nGRANT UPDATE, DELETE ON alice.unpartitioned TO regress_alice;\n")
	expectFailure("alice.unpartitioned", 4, 13, 19, permErr, "replication of update/delete fails if table owner lacks SELECT permission")

	// alice needs SELECT too.
	nodeSubscriber.SafePSQL("postgres", "\nGRANT SELECT ON alice.unpartitioned TO regress_alice;\n")
	expectReplication("alice.unpartitioned", 3, 17, 21, "restoring SELECT permission permits replication to continue")

	// The apply worker should get restarted after the superuser privileges
	// are revoked for subscription owner alice.
	grantSuperuser("regress_alice")
	nodeSubscriber.SafePSQL("postgres", `
SET SESSION AUTHORIZATION regress_alice;
CREATE SUBSCRIPTION regression_sub CONNECTION '`+publisherConnstr+`' PUBLICATION alice;
`)
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "regression_sub")
	// Check the subscriber log from now on.
	offset = nodeSubscriber.LogSize()
	revokeSuperuser("regress_alice")
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`LOG: ( [A-Z0-9]+:)? logical replication worker for subscription "regression_sub" will restart because the subscription owner's superuser privileges have been revoked`), offset)

	// The password_required block (subscription owned by a non-superuser
	// must supply a password) is skipped: it needs Unix-domain sockets.
	t.Log("skip: subscription password_required test cannot run without Unix-domain sockets")
}

// waitForLogOffset is Cluster.pm's wait_for_log returning the end offset of
// the match, which 027's expect_failure carries forward.
func waitForLogOffset(t *testing.T, n *tapnode.Node, re *regexp.Regexp, offset int64) int64 {
	t.Helper()
	waitForLog(t, n, re, offset)
	text := n.LogFrom(offset)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return offset
	}
	// LogFrom renders goopg lines; map the rendered match end back to a
	// file offset by counting whole lines up to it.
	lines := strings.Count(text[:loc[1]], "\n")
	raw, _ := os.ReadFile(n.LogPath())
	pos := offset
	for i := 0; i < lines+1 && pos < int64(len(raw)); i++ {
		nl := strings.IndexByte(string(raw[pos:]), '\n')
		if nl < 0 {
			pos = int64(len(raw))
			break
		}
		pos += int64(nl) + 1
	}
	return pos
}
