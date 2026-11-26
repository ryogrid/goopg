package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription024AddDropPub ports postgres/src/test/subscription/t/024_add_drop_pub.pl
// to the M0149 contract: ALTER SUBSCRIPTION ... DROP / ADD PUBLICATION
// refresh the subscription's table list (copying the data of newly added
// tables), and ALTER SUBSCRIPTION ... SET PUBLICATION naming a missing
// publication logs "skipped loading publication" on the publisher without
// breaking replication, which resumes once the publication is created and
// the subscription refreshed. Three pairings: all-goopg, a PG 18.3
// publisher with a goopg subscriber, a goopg publisher with a PG 18.3
// subscriber.
//
// Ported in M0149-0052.
func TestPort_Subscription024AddDropPub(t *testing.T) {
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
			runSubscriptionAddDropPub(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionAddDropPub(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "WARNING") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		}
	})

	// Create table on publisher
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_1 (a int)")
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_1 SELECT generate_series(1,10)")

	// Create table on subscriber
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_1 (a int)")

	// Setup logical replication
	publisherConnstr := nodePublisher.ConnStr()
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub_1 FOR TABLE tab_1")
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub_2")
	nodeSubscriber.SafePSQL("postgres",
		"CREATE SUBSCRIPTION tap_sub CONNECTION '"+publisherConnstr+"' PUBLICATION tap_pub_1, tap_pub_2")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab_1"),
		"10|1|10", "check initial data is copied to subscriber")

	// Create a new table on publisher
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_2 (a int)")
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_2 SELECT generate_series(1,10)")

	// Create a new table on subscriber
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_2 (a int)")

	// Add the table to publication
	nodePublisher.SafePSQL("postgres", "ALTER PUBLICATION tap_pub_2 ADD TABLE tab_2")

	// Dropping tap_pub_1 will refresh the entire publication list
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub DROP PUBLICATION tap_pub_1")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab_2"),
		"10|1|10", "check initial data is copied to subscriber")

	// Re-adding tap_pub_1 will refresh the entire publication list
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub ADD PUBLICATION tap_pub_1")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	is(nodeSubscriber.SafePSQL("postgres", "SELECT count(*), min(a), max(a) FROM tab_1"),
		"20|1|10", "check initial data is copied to subscriber")

	// Setting a missing publication must not disrupt replication: the
	// publisher logs a warning, and replication resumes once the
	// publication exists and the subscription is refreshed.
	nodePublisher.SafePSQL("postgres", "CREATE TABLE tab_3 (a int)")
	nodeSubscriber.SafePSQL("postgres", "CREATE TABLE tab_3 (a int)")

	oldpid := nodePublisher.SafePSQL("postgres",
		"SELECT pid FROM pg_stat_replication WHERE application_name = 'tap_sub' AND state = 'streaming';")

	// Set the subscription with a missing publication
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub SET PUBLICATION tap_pub_3")

	// Wait for the walsender to restart after altering the subscription
	if !nodePublisher.PollQueryUntil("postgres",
		"SELECT pid != "+oldpid+" FROM pg_stat_replication WHERE application_name = 'tap_sub' AND state = 'streaming';", "t") {
		st, _, _ := nodePublisher.PSQLExit("postgres", "SELECT pid, application_name, state FROM pg_stat_replication")
		t.Fatalf("Timed out while waiting for apply worker to restart after altering the subscription (old pid %q; pg_stat_replication %q)", oldpid, st)
	}

	offset := nodePublisher.LogSize()

	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_3 values(1)")

	// Verify that a warning is logged.
	waitForLog(t, nodePublisher, regexp.MustCompile(`WARNING: ( [A-Z0-9]+:)? skipped loading publication "tap_pub_3"`), offset)

	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION tap_pub_3 FOR TABLE tab_3")

	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub REFRESH  PUBLICATION")

	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_3 values(2)")

	waitForSubCatchup(t, nodePublisher, "tap_sub")

	// Verify that the insert operation gets replicated to subscriber after
	// publication is created.
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab_3"), "1\n2",
		"check that the incremental data is replicated after the publication is created")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
