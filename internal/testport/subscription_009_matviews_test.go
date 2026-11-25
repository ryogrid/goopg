package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription009Matviews ports postgres/src/test/subscription/t/009_matviews.pl
// to the M0149 contract: logical decoding produces change information for
// a materialized view's heap, but logical replication must ignore it
// (bug #15044) — CREATE MATERIALIZED VIEW on the publisher, with no such
// relation on the subscriber, must not make wait_for_catchup hang. It runs
// in three pairings: all-goopg, a PG 18.3 publisher with a goopg
// subscriber, and a goopg publisher with a PG 18.3 subscriber.
func TestPort_Subscription009Matviews(t *testing.T) {
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
			runSubscriptionMatviews(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionMatviews(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber.Start()

	publisherConnstr := nodePublisher.ConnStr()

	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION mypub FOR ALL TABLES;")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION mysub CONNECTION '"+publisherConnstr+"' PUBLICATION mypub;")

	nodePublisher.SafePSQL("postgres", "CREATE TABLE test1 (a int PRIMARY KEY, b text)")
	nodePublisher.SafePSQL("postgres", "INSERT INTO test1 (a, b) VALUES (1, 'one'), (2, 'two');")

	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE test1 (a int PRIMARY KEY, b text);")

	waitForSubCatchup(t, nodePublisher, "mysub")

	// Materialized views are not supported by logical replication, but
	// logical decoding does produce change information for them, so we
	// need to make sure they are properly ignored. (bug #15044)

	// create a MV with some data
	nodePublisher.SafePSQL("postgres", "CREATE MATERIALIZED VIEW testmv1 AS SELECT * FROM test1;")
	waitForSubCatchup(t, nodePublisher, "mysub")

	// There is no equivalent relation on the subscriber, but MV data is
	// not replicated, so this does not hang.
	// pass "materialized view data not replicated"

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
