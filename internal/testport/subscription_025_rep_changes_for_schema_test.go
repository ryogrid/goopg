package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription025RepChangesForSchema ports
// postgres/src/test/subscription/t/025_rep_changes_for_schema.pl to the
// M0149 contract: a FOR TABLES IN SCHEMA publication replicates every table
// of the schema (including a partitioned table whose partitions live in
// another schema), a table created in the schema later joins on REFRESH
// PUBLICATION, a table moved out of the schema (ALTER TABLE ... SET
// SCHEMA) or dropped stops replicating and leaves pg_subscription_rel on
// the next refresh, and ALTER PUBLICATION ... DROP TABLES IN SCHEMA stops
// the stream mid-transaction. Three pairings: all-goopg, a PG 18.3
// publisher with a goopg subscriber, a goopg publisher with a PG 18.3
// subscriber.
//
// Ported in M0149-0053.
func TestPort_Subscription025RepChangesForSchema(t *testing.T) {
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
			runSubscriptionRepChangesForSchema(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionRepChangesForSchema(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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
	subRelCount := "SELECT count(*) FROM pg_subscription_rel WHERE srsubid IN (SELECT oid FROM pg_subscription WHERE subname = 'tap_sub_schema')"

	// Test replication with publications created using FOR TABLES IN SCHEMA
	// option.
	// Create schemas and tables on publisher
	nodePublisher.SafePSQL("postgres", "CREATE SCHEMA sch1")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE sch1.tab1 AS SELECT generate_series(1,10) AS a")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE sch1.tab2 AS SELECT generate_series(1,10) AS a")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE sch1.tab1_parent (a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE public.tab1_child1 PARTITION OF sch1.tab1_parent FOR VALUES IN (1, 2, 3)")
	nodePublisher.SafePSQL("postgres", "CREATE TABLE public.tab1_child2 PARTITION OF sch1.tab1_parent FOR VALUES IN (4, 5, 6)")
	nodePublisher.SafePSQL("postgres", "INSERT INTO sch1.tab1_parent values (1),(4)")

	// Create schemas and tables on subscriber
	nodeSubscriber.SafePSQL("postgres", "CREATE SCHEMA sch1")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE sch1.tab1 (a int)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE sch1.tab2 (a int)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE sch1.tab1_parent (a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE public.tab1_child1 PARTITION OF sch1.tab1_parent FOR VALUES IN (1, 2, 3)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE public.tab1_child2 PARTITION OF sch1.tab1_parent FOR VALUES IN (4, 5, 6)")

	// Setup logical replication
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub_schema FOR TABLES IN SCHEMA sch1")
	nodeSubscriber.SafePSQL("postgres",
		"CREATE SUBSCRIPTION tap_sub_schema CONNECTION '"+publisherConnstr+"' PUBLICATION tap_pub_schema")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub_schema")

	// Check the schema table data is synced up
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab1"), "10|1|10", "check rows on subscriber catchup")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab2"), "10|1|10", "check rows on subscriber catchup")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM sch1.tab1_parent order by 1"), "1|\n4|", "check rows on subscriber catchup")

	// Insert some data into few tables and verify that inserted data is
	// replicated
	nodePublisher.SafePSQL("postgres", "INSERT INTO sch1.tab1 VALUES(generate_series(11,20))")
	nodePublisher.SafePSQL("postgres", "INSERT INTO sch1.tab1_parent values (2),(5)")
	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab1"), "20|1|20", "check replicated inserts on subscriber")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM sch1.tab1_parent order by 1"), "1|\n2|\n4|\n5|", "check replicated inserts on subscriber")

	// Create new table in the publication schema, verify that subscriber
	// does not get the new table data before refresh.
	nodePublisher.SafePSQL("postgres", "CREATE TABLE sch1.tab3 AS SELECT generate_series(1,10) AS a")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE sch1.tab3(a int)")
	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*) FROM sch1.tab3"), "0", "check replicated inserts on subscriber")

	// Table data should be reflected after refreshing the publication in
	// subscriber.
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub_schema REFRESH PUBLICATION")

	// Wait for sync to finish
	waitForSubscriberSync(t, nodeSubscriber)

	nodePublisher.SafePSQL("postgres", "INSERT INTO sch1.tab3 VALUES(11)")
	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab3"), "11|1|11", "check rows on subscriber catchup")

	// Set the schema of a publication schema table to a non publication
	// schema and verify that inserted data is not reflected by the
	// subscriber.
	nodePublisher.SafePSQL("postgres", "ALTER TABLE sch1.tab3 SET SCHEMA public")
	nodePublisher.SafePSQL("postgres", "INSERT INTO public.tab3 VALUES(12)")
	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab3"), "11|1|11", "check replicated inserts on subscriber")

	// Verify that the subscription relation list is updated after refresh
	is(nodeSubscriber.SafePSQL("postgres", subRelCount), "5", "check subscription relation status is not yet dropped on subscriber")

	// Ask for data sync
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub_schema REFRESH PUBLICATION")

	// Wait for sync to finish
	waitForSubscriberSync(t, nodeSubscriber)

	is(nodeSubscriber.SafePSQL("postgres", subRelCount), "4", "check subscription relation status was dropped on subscriber")

	// Drop table from the publication schema, verify that subscriber removes
	// the table entry after refresh.
	nodePublisher.SafePSQL("postgres", "DROP TABLE sch1.tab2")
	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")

	is(nodeSubscriber.SafePSQL("postgres", subRelCount), "4", "check subscription relation status is not yet dropped on subscriber")

	// Table should be removed from pg_subscription_rel after refresh
	// publication.
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub_schema REFRESH PUBLICATION")

	// Wait for sync to finish
	waitForSubscriberSync(t, nodeSubscriber)

	is(nodeSubscriber.SafePSQL("postgres", subRelCount), "3", "check subscription relation status was dropped on subscriber")

	// Drop schema from publication, verify that the inserts are not
	// published after dropping the schema from publication. Here 2nd insert
	// should not be published.
	nodePublisher.SafePSQL("postgres", `
	INSERT INTO sch1.tab1 VALUES(21);
	ALTER PUBLICATION tap_pub_schema DROP TABLES IN SCHEMA sch1;
	INSERT INTO sch1.tab1 values(22);`)

	waitForSubCatchup(t, nodePublisher, "tap_sub_schema")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM sch1.tab1"), "21|1|21", "check replicated inserts on subscriber")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
