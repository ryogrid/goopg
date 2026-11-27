package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription030Origin ports
// postgres/src/test/subscription/t/030_origin.pl to the M0149 contract: the
// CREATE SUBSCRIPTION 'origin' parameter and its interaction with
// 'copy_data'. Three nodes A, B, C, each both publisher and subscriber. A
// bidirectional A <-> B setup with origin = none must not recurse
// infinitely; data B received from C must not be forwarded to A; with
// track_commit_timestamp on, B logs the update_origin_differs /
// delete_origin_differs conflicts when C changes a row A wrote. CREATE
// SUBSCRIPTION ... WITH (origin = none, copy_data = on) and ALTER
// SUBSCRIPTION ... REFRESH PUBLICATION warn ("might copy data that had a
// different origin") when the publisher's table — or a partition or an
// ancestor of it — is itself subscribed from another node. Three pairings:
// all-goopg, a goopg node B with PG 18.3 nodes A and C, a PG 18.3 node B
// with goopg nodes A and C.
//
// Three pairings 3/3 each (M0149-0062).
func TestPort_Subscription030Origin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subscription TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)
	goopg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewGoopg(t, repo, name) }
	pg := func(t *testing.T, name string) *tapnode.Node { return tapnode.NewPG(t, repo, name) }
	cases := []struct {
		name          string
		nodeB, nodeAC func(t *testing.T, name string) *tapnode.Node
	}{
		{"all-goopg", goopg, goopg},
		{"goopg-B-pg18-A-C", goopg, pg},
		{"pg18-B-goopg-A-C", pg, goopg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runSubscriptionOrigin(t, tc.nodeB, tc.nodeAC)
		})
	}
}

// o030WaitForSubscriptionSync is Cluster.pm's
// $subscriber->wait_for_subscription_sync($publisher, $subname): every
// pg_subscription_rel row is ready or synchronized, then the publisher's
// walsender for subname has caught up.
func o030WaitForSubscriptionSync(t *testing.T, subscriber, publisher *tapnode.Node, subname string) {
	t.Helper()
	waitForSubscriberSync(t, subscriber)
	waitForSubCatchup(t, publisher, subname)
}

func runSubscriptionOrigin(t *testing.T, newNodeB, newNodeAC func(t *testing.T, name string) *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	like := func(got string, re *regexp.Regexp, name string) {
		t.Helper()
		if !re.MatchString(got) {
			t.Errorf("not ok: %s: %q does not match %q", name, got, re)
		}
	}
	// init(allows_streaming => 'logical')
	logical := func(n *tapnode.Node) {
		for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
			n.AppendConf(l)
		}
	}
	// track registers the failure-log dump for n right after its creation, so
	// the dump runs before the node's own cleanup removes its directory.
	track := func(n *tapnode.Node) {
		t.Cleanup(func() {
			if !t.Failed() {
				return
			}
			for _, l := range strings.Split(fileTail(n.LogPath(), 2000), "\n") {
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		})
	}

	const subnameAB = "tap_sub_A_B"
	const subnameAB2 = "tap_sub_A_B_2"
	const subnameBA = "tap_sub_B_A"
	const subnameBC = "tap_sub_B_C"

	var result, stderr string

	///////////////////////////////////////////////////////////////////////////
	// Setup a bidirectional logical replication between node_A & node_B
	///////////////////////////////////////////////////////////////////////////

	// Initialize nodes
	// node_A
	nodeA := newNodeAC(t, "node_A")
	track(nodeA)
	logical(nodeA)
	nodeA.Start()

	// node_B
	nodeB := newNodeB(t, "node_B")
	track(nodeB)
	logical(nodeB)

	// Enable the track_commit_timestamp to detect the conflict when attempting to
	// update a row that was previously modified by a different origin.
	nodeB.AppendConf("track_commit_timestamp = on")
	nodeB.Start()

	a := func(sql string) string { return nodeA.SafePSQL("postgres", sql) }
	b := func(sql string) string { return nodeB.SafePSQL("postgres", sql) }

	// Create table on node_A
	a("CREATE TABLE tab (a int PRIMARY KEY)")

	// Create the same table on node_B
	b("CREATE TABLE tab (a int PRIMARY KEY)")

	// Setup logical replication
	// node_A (pub) -> node_B (sub)
	// (ConnStr already carries dbname=postgres, as connstr . ' dbname=postgres')
	nodeAConnstr := nodeA.ConnStr()
	a("CREATE PUBLICATION tap_pub_A FOR TABLE tab")
	b(`
	CREATE SUBSCRIPTION ` + subnameBA + `
	CONNECTION '` + nodeAConnstr + ` application_name=` + subnameBA + `'
	PUBLICATION tap_pub_A
	WITH (origin = none)`)

	// node_B (pub) -> node_A (sub)
	nodeBConnstr := nodeB.ConnStr()
	b("CREATE PUBLICATION tap_pub_B FOR TABLE tab")
	a(`
	CREATE SUBSCRIPTION ` + subnameAB + `
	CONNECTION '` + nodeBConnstr + ` application_name=` + subnameAB + `'
	PUBLICATION tap_pub_B
	WITH (origin = none, copy_data = off)`)

	// Wait for initial table sync to finish
	o030WaitForSubscriptionSync(t, nodeA, nodeB, subnameAB)
	o030WaitForSubscriptionSync(t, nodeB, nodeA, subnameBA)

	is("1", "1", "Bidirectional replication setup is complete")

	///////////////////////////////////////////////////////////////////////////
	// Check that bidirectional logical replication setup does not cause infinite
	// recursive insertion.
	///////////////////////////////////////////////////////////////////////////

	// insert a record
	a("INSERT INTO tab VALUES (11);")
	b("INSERT INTO tab VALUES (21);")

	waitForSubCatchup(t, nodeA, subnameBA)
	waitForSubCatchup(t, nodeB, subnameAB)

	// check that transaction was committed on subscriber(s)
	result = a("SELECT * FROM tab ORDER BY 1;")
	is(result, "11\n21",
		"Inserted successfully without leading to infinite recursion in bidirectional replication setup")
	result = b("SELECT * FROM tab ORDER BY 1;")
	is(result, "11\n21",
		"Inserted successfully without leading to infinite recursion in bidirectional replication setup")

	a("DELETE FROM tab;")

	waitForSubCatchup(t, nodeA, subnameBA)
	waitForSubCatchup(t, nodeB, subnameAB)

	///////////////////////////////////////////////////////////////////////////
	// Check that remote data of node_B (that originated from node_C) is not
	// published to node_A.
	///////////////////////////////////////////////////////////////////////////
	result = a("SELECT * FROM tab ORDER BY 1;")
	is(result, "", "Check existing data")

	result = b("SELECT * FROM tab ORDER BY 1;")
	is(result, "", "Check existing data")

	// Initialize node node_C
	nodeC := newNodeAC(t, "node_C")
	track(nodeC)
	logical(nodeC)
	nodeC.Start()
	c := func(sql string) string { return nodeC.SafePSQL("postgres", sql) }

	c("CREATE TABLE tab (a int PRIMARY KEY)")

	// Setup logical replication
	// node_C (pub) -> node_B (sub)
	nodeCConnstr := nodeC.ConnStr()
	c("CREATE PUBLICATION tap_pub_C FOR TABLE tab")
	b(`
	CREATE SUBSCRIPTION ` + subnameBC + `
	CONNECTION '` + nodeCConnstr + ` application_name=` + subnameBC + `'
	PUBLICATION tap_pub_C
	WITH (origin = none)`)
	o030WaitForSubscriptionSync(t, nodeB, nodeC, subnameBC)

	// insert a record
	c("INSERT INTO tab VALUES (32);")

	waitForSubCatchup(t, nodeC, subnameBC)
	waitForSubCatchup(t, nodeB, subnameAB)
	waitForSubCatchup(t, nodeA, subnameBA)

	result = b("SELECT * FROM tab ORDER BY 1;")
	is(result, "32", "The node_C data replicated to node_B")

	// check that the data published from node_C to node_B is not sent to node_A
	result = a("SELECT * FROM tab ORDER BY 1;")
	is(result, "",
		"Remote data originating from another node (not the publisher) is not replicated when origin parameter is none")

	///////////////////////////////////////////////////////////////////////////
	// Check that the conflict can be detected when attempting to update or
	// delete a row that was previously modified by a different source.
	///////////////////////////////////////////////////////////////////////////

	b("DELETE FROM tab;")

	a("INSERT INTO tab VALUES (32);")

	waitForSubCatchup(t, nodeA, subnameBA)
	waitForSubCatchup(t, nodeB, subnameAB)

	result = b("SELECT * FROM tab ORDER BY 1;")
	is(result, "32", "The node_A data replicated to node_B")

	// The update should update the row on node B that was inserted by node A.
	c("UPDATE tab SET a = 33 WHERE a = 32;")

	waitForLog(t, nodeB, regexp.MustCompile(`conflict detected on relation "public.tab": conflict=update_origin_differs.*\n.*DETAIL:.* Updating the row that was modified by a different origin ".*" in transaction [0-9]+ at .*\n.*Existing local row \(32\); remote row \(33\); replica identity \(a\)=\(32\)`), 0)

	b("DELETE FROM tab;")
	a("INSERT INTO tab VALUES (33);")

	waitForSubCatchup(t, nodeA, subnameBA)
	waitForSubCatchup(t, nodeB, subnameAB)

	result = b("SELECT * FROM tab ORDER BY 1;")
	is(result, "33", "The node_A data replicated to node_B")

	// The delete should remove the row on node B that was inserted by node A.
	c("DELETE FROM tab WHERE a = 33;")

	waitForLog(t, nodeB, regexp.MustCompile(`conflict detected on relation "public.tab": conflict=delete_origin_differs.*\n.*DETAIL:.* Deleting the row that was modified by a different origin ".*" in transaction [0-9]+ at .*\n.*Existing local row \(33\); replica identity \(a\)=\(33\)`), 0)

	// The remaining tests no longer test conflict detection.
	nodeB.AppendConf("track_commit_timestamp = off")
	nodeB.Restart()

	///////////////////////////////////////////////////////////////////////////
	// Specifying origin = NONE indicates that the publisher should only replicate the
	// changes that are generated locally from node_B, but in this case since the
	// node_B is also subscribing data from node_A, node_B can have remotely
	// originated data from node_A. We log a warning, in this case, to draw
	// attention to there being possible remote data.
	///////////////////////////////////////////////////////////////////////////
	_, stderr, _ = nodeA.PSQLExit("postgres", `
        CREATE SUBSCRIPTION `+subnameAB2+`
        CONNECTION '`+nodeBConnstr+` application_name=`+subnameAB2+`'
        PUBLICATION tap_pub_B
        WITH (origin = none, copy_data = on)`)
	like(stderr,
		regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? subscription "tap_sub_a_b_2" requested copy_data with origin = NONE but might copy data that had a different origin`),
		"Create subscription with origin = none and copy_data when the publisher has subscribed same table")

	o030WaitForSubscriptionSync(t, nodeA, nodeB, subnameAB2)

	// Alter subscription ... refresh publication should be successful when no new
	// table is added
	a(`
        ALTER SUBSCRIPTION ` + subnameAB2 + ` REFRESH PUBLICATION`)

	// Check Alter subscription ... refresh publication when there is a new
	// table that is subscribing data from a different publication
	a("CREATE TABLE tab_new (a int PRIMARY KEY)")
	b("CREATE TABLE tab_new (a int PRIMARY KEY)")

	// add a new table to the publication
	a("ALTER PUBLICATION tap_pub_A ADD TABLE tab_new")
	b(`
        ALTER SUBSCRIPTION ` + subnameBA + ` REFRESH PUBLICATION`)

	o030WaitForSubscriptionSync(t, nodeB, nodeA, subnameBA)

	// add a new table to the publication
	b("ALTER PUBLICATION tap_pub_B ADD TABLE tab_new")

	// Alter subscription ... refresh publication should log a warning when a new
	// table on the publisher is subscribing data from a different publication
	_, stderr, _ = nodeA.PSQLExit("postgres", `
        ALTER SUBSCRIPTION `+subnameAB2+` REFRESH PUBLICATION`)
	like(stderr,
		regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? subscription "tap_sub_a_b_2" requested copy_data with origin = NONE but might copy data that had a different origin`),
		"Refresh publication when the publisher has subscribed for the new table, but the subscriber-side wants origin = none")

	// Ensure that relation has reached 'ready' state before we try to drop it
	const syncedQuery = "SELECT count(1) = 0 FROM pg_subscription_rel WHERE srsubstate NOT IN ('r');"
	if !nodeA.PollQueryUntil("postgres", syncedQuery, "t") {
		t.Fatalf("Timed out while waiting for subscriber to synchronize data")
	}

	waitForSubCatchup(t, nodeB, subnameAB2)

	// clear the operations done by this test
	a(`
DROP TABLE tab_new;
DROP SUBSCRIPTION ` + subnameAB2 + `;
DROP SUBSCRIPTION ` + subnameAB + `;
DROP PUBLICATION tap_pub_A;
`)
	b(`
DROP TABLE tab_new;
DROP SUBSCRIPTION ` + subnameBA + `;
DROP PUBLICATION tap_pub_B;
`)

	///////////////////////////////////////////////////////////////////////////
	// Specifying origin = NONE and copy_data = on must raise WARNING if we subscribe
	// to a partitioned table and this table contains any remotely originated data.
	//
	//           node_B
	//  __________________________
	// |       tab_main           | --------------> node_C (tab_main)
	// |__________________________|
	// | tab_part1  | tab_part2   | <-------------- node_A (tab_part2)
	// |____________|_____________|
	//              | tab_part2_1 |
	//              |_____________|
	//
	//           node_B
	//  __________________________
	// |       tab_main           |
	// |__________________________|
	// | tab_part1  | tab_part2   | <-------------- node_A (tab_part2)
	// |____________|_____________|
	//              | tab_part2_1 | --------------> node_C (tab_part2_1)
	//              |_____________|
	///////////////////////////////////////////////////////////////////////////

	// create a table on node A which will act as a source for a partition on node B
	a(`
CREATE TABLE tab_part2(a int);
CREATE PUBLICATION tap_pub_A FOR TABLE tab_part2;
`)

	// create a partition table on node B
	b(`
CREATE TABLE tab_main(a int) PARTITION BY RANGE(a);
CREATE TABLE tab_part1 PARTITION OF tab_main FOR VALUES FROM (0) TO (5);
CREATE TABLE tab_part2(a int) PARTITION BY RANGE(a);
CREATE TABLE tab_part2_1 PARTITION OF tab_part2 FOR VALUES FROM (5) TO (10);
ALTER TABLE tab_main ATTACH PARTITION tab_part2 FOR VALUES FROM (5) to (10);
CREATE SUBSCRIPTION tap_sub_A_B CONNECTION '` + nodeAConnstr + `' PUBLICATION tap_pub_A;
`)

	// create a table on node C
	c(`
CREATE TABLE tab_main(a int);
CREATE TABLE tab_part2_1(a int);
`)

	// create a logical replication setup between node B and node C with
	// subscription on node C having origin = NONE and copy_data = on
	b(`
CREATE PUBLICATION tap_pub_B FOR TABLE tab_main WITH (publish_via_partition_root);
CREATE PUBLICATION tap_pub_B_2 FOR TABLE tab_part2_1;
`)

	_, stderr, _ = nodeC.PSQLExit("postgres", `
	CREATE SUBSCRIPTION tap_sub_B_C CONNECTION '`+nodeBConnstr+`' PUBLICATION tap_pub_B WITH (origin = none, copy_data = on);
`)

	// A warning must be logged as a partition 'tab_part2' in node B is subscribed to
	// node A so partition 'tab_part2' can have remotely originated data
	like(stderr,
		regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? subscription "tap_sub_b_c" requested copy_data with origin = NONE but might copy data that had a different origin`),
		"Create subscription with origin = none and copy_data when the publisher's partition is subscribing from different origin")
	c("DROP SUBSCRIPTION tap_sub_B_C")

	_, stderr, _ = nodeC.PSQLExit("postgres", `
	CREATE SUBSCRIPTION tap_sub_B_C CONNECTION '`+nodeBConnstr+`' PUBLICATION tap_pub_B_2 WITH (origin = none, copy_data = on);
`)

	// A warning must be logged as ancestor of table 'tab_part2_1' in node B is
	// subscribed to node A so table 'tab_part2_1' can have remotely originated
	// data
	like(stderr,
		regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? subscription "tap_sub_b_c" requested copy_data with origin = NONE but might copy data that had a different origin`),
		"Create subscription with origin = none and copy_data when the publisher's ancestor is subscribing from different origin")

	// clear the operations done by this test
	c(`
DROP SUBSCRIPTION tap_sub_B_C;
DROP TABLE tab_main;
DROP TABLE tab_part2_1;
`)
	b(`
DROP SUBSCRIPTION tap_sub_A_B;
DROP PUBLICATION tap_pub_B;
DROP PUBLICATION tap_pub_B_2;
DROP TABLE tab_main;
`)
	a(`
DROP PUBLICATION tap_pub_A;
DROP TABLE tab_part2;
`)

	// shutdown
	nodeB.Stop(tapnode.StopFast)
	nodeA.Stop(tapnode.StopFast)
	nodeC.Stop(tapnode.StopFast)
}
