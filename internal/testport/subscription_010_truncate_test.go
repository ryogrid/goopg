package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription010Truncate ports postgres/src/test/subscription/t/010_truncate.pl
// to the M0149 contract: TRUNCATE replicates (and RESTART IDENTITY restarts
// the subscriber's owned sequence), a publication without publish =
// 'truncate' does not replicate it until ALTER PUBLICATION adds it,
// FK-connected tables truncate together, unpublished tables of a
// multi-table TRUNCATE are left alone, TRUNCATE works under synchronous
// logical replication and with two subscriptions on one table, and no
// deadlock is recorded. It runs in three pairings: all-goopg, a PG 18.3
// publisher with a goopg subscriber, and a goopg publisher with a PG 18.3
// subscriber.
//
// The goopg subscriber restarts owned sequences on a replicated RESTART
// IDENTITY (M0149-0047a); the goopg publisher writes XLOG_HEAP_TRUNCATE at
// wal_level = logical and its walsender sends pgoutput Truncate filtered by
// publish = 'truncate' (M0149-0047b).
func TestPort_Subscription010Truncate(t *testing.T) {
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
			runSubscriptionTruncate(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionTruncate(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber.AppendConf("max_logical_replication_workers = 6")
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

	for _, ddl := range []string{
		"CREATE TABLE tab1 (a int PRIMARY KEY)",
		"CREATE TABLE tab2 (a int PRIMARY KEY)",
		"CREATE TABLE tab3 (a int PRIMARY KEY)",
		"CREATE TABLE tab4 (x int PRIMARY KEY, y int REFERENCES tab3)",
	} {
		nodePublisher.SafePSQL("postgres", ddl)
		nodeSubscriber.SafePSQL("postgres", ddl)
	}

	nodeSubscriber.SafePSQL("postgres", "CREATE SEQUENCE seq1 OWNED BY tab1.a")
	nodeSubscriber.SafePSQL("postgres", "ALTER SEQUENCE seq1 START 101")

	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub1 FOR TABLE tab1")
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub2 FOR TABLE tab2 WITH (publish = insert)")
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub3 FOR TABLE tab3, tab4")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub1 CONNECTION '"+publisherConnstr+"' PUBLICATION pub1")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub2 CONNECTION '"+publisherConnstr+"' PUBLICATION pub2")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub3 CONNECTION '"+publisherConnstr+"' PUBLICATION pub3")

	// Wait for initial sync of all subscriptions
	waitForSubscriberSync(t, nodeSubscriber)

	// insert data to truncate
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab1 VALUES (1), (2), (3)")
	waitForSubCatchup(t, nodePublisher, "sub1")

	// truncate and check
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab1")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab1"), "0||", "truncate replicated")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT nextval('seq1')"), "1", "sequence not restarted")

	// truncate with restart identity
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab1 RESTART IDENTITY")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT nextval('seq1')"), "101", "truncate restarted identities")

	// test publication that does not replicate truncate
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab2 VALUES (1), (2), (3)")
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab2")
	waitForSubCatchup(t, nodePublisher, "sub2")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab2"), "3|1|3", "truncate not replicated")

	nodePublisher.SafePSQL("postgres", "ALTER PUBLICATION pub2 SET (publish = 'insert, truncate')")
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab2")
	waitForSubCatchup(t, nodePublisher, "sub2")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab2"), "0||", "truncate replicated after publication change")

	// test multiple tables connected by foreign keys
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab3 VALUES (1), (2), (3)")
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab4 VALUES (11, 1), (111, 1), (22, 2)")
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab3, tab4")
	waitForSubCatchup(t, nodePublisher, "sub3")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab3"), "0||", "truncate of multiple tables replicated")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(x), max(x) FROM tab4"), "0||", "truncate of multiple tables replicated")

	// test truncate of multiple tables, some of which are not published
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION sub2")
	nodePublisher.SafePSQL("postgres", "DROP PUBLICATION pub2")
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab1 VALUES (1), (2), (3)")
	nodeSubscriber.SafePSQL("postgres", "INSERT INTO tab2 VALUES (1), (2), (3)")
	nodePublisher.SafePSQL("postgres", "TRUNCATE tab1, tab2")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab1"), "0||", "truncate of multiple tables some not published")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab2"), "3|1|3", "truncate of multiple tables some not published")

	// Test that truncate works for synchronous logical replication
	nodePublisher.SafePSQL("postgres", "ALTER SYSTEM SET synchronous_standby_names TO 'sub1'")
	nodePublisher.SafePSQL("postgres", "SELECT pg_reload_conf()")

	// insert data to truncate
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab1 VALUES (1), (2), (3)")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab1"), "3|1|3", "check synchronous logical replication")

	nodePublisher.SafePSQL("postgres", "TRUNCATE tab1")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab1"), "0||", "truncate replicated in synchronous logical replication")

	nodePublisher.SafePSQL("postgres", "ALTER SYSTEM RESET synchronous_standby_names")
	nodePublisher.SafePSQL("postgres", "SELECT pg_reload_conf()")

	// test that truncate works for logical replication when there are
	// multiple subscriptions for a single table
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab5 (a int)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab5 (a int)")
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub5 FOR TABLE tab5")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub5_1 CONNECTION '"+publisherConnstr+"' PUBLICATION pub5")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub5_2 CONNECTION '"+publisherConnstr+"' PUBLICATION pub5")

	// wait for initial data sync
	waitForSubscriberSync(t, nodeSubscriber)

	// insert data to truncate
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab5 VALUES (1), (2), (3)")
	waitForSubCatchup(t, nodePublisher, "sub5_1")
	waitForSubCatchup(t, nodePublisher, "sub5_2")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab5"), "6|1|3", "insert replicated for multiple subscriptions")

	nodePublisher.SafePSQL("postgres", "TRUNCATE tab5")
	waitForSubCatchup(t, nodePublisher, "sub5_1")
	waitForSubCatchup(t, nodePublisher, "sub5_2")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab5"), "0||", "truncate replicated for multiple subscriptions")

	// check deadlocks
	is(nodeSubscriber.SafePSQL("postgres", "SELECT deadlocks FROM pg_stat_database WHERE datname='postgres'"), "0", "no deadlocks detected")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
