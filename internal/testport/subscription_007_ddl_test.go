package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription007DDL ports postgres/src/test/subscription/t/007_ddl.pl
// to the M0149 contract: ALTER SUBSCRIPTION … DISABLE / SET (slot_name =
// NONE) / DROP SUBSCRIPTION in one transaction does not hang; CREATE and
// ALTER SUBSCRIPTION warn about publications missing on the publisher; and
// renaming publications on the publisher changes what a running
// subscription receives, for FOR TABLE and FOR ALL TABLES publications. It
// runs in three pairings: all-goopg, a PG 18.3 publisher with a goopg
// subscriber, and a goopg publisher with a PG 18.3 subscriber.
func TestPort_Subscription007DDL(t *testing.T) {
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
			runSubscriptionDDL(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionDDL(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name, detail string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s: %s", name, detail)
		}
	}
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber.Start()

	ddl := "CREATE TABLE test1 (a int, b text);"
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)

	publisherConnstr := nodePublisher.ConnStr()

	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION mypub FOR ALL TABLES;")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION mysub CONNECTION '"+publisherConnstr+"' PUBLICATION mypub;")

	waitForSubCatchup(t, nodePublisher, "mysub")

	nodeSubscriber.SafePSQL("postgres", `
BEGIN;
ALTER SUBSCRIPTION mysub DISABLE;
ALTER SUBSCRIPTION mysub SET (slot_name = NONE);
DROP SUBSCRIPTION mysub;
COMMIT;
`)
	// pass "subscription disable and drop in same transaction did not hang"

	// One of the specified publications exists.
	_, stderr, _ := nodeSubscriber.PSQLExit("postgres", "CREATE SUBSCRIPTION mysub1 CONNECTION '"+publisherConnstr+"' PUBLICATION mypub, non_existent_pub")
	ok(strings.Contains(stderr, `WARNING:  publication "non_existent_pub" does not exist on the publisher`),
		"Create subscription throws warning for non-existent publication", "stderr "+stderr)

	// Wait for initial table sync to finish.
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "mysub1")

	// Specifying non-existent publication along with add publication.
	_, stderr, _ = nodeSubscriber.PSQLExit("postgres", "ALTER SUBSCRIPTION mysub1 ADD PUBLICATION non_existent_pub1, non_existent_pub2")
	ok(strings.Contains(stderr, `WARNING:  publications "non_existent_pub1", "non_existent_pub2" do not exist on the publisher`),
		"Alter subscription add publication throws warning for non-existent publications", "stderr "+stderr)

	// Specifying non-existent publication along with set publication.
	_, stderr, _ = nodeSubscriber.PSQLExit("postgres", "ALTER SUBSCRIPTION mysub1 SET PUBLICATION non_existent_pub")
	ok(strings.Contains(stderr, `WARNING:  publication "non_existent_pub" does not exist on the publisher`),
		"Alter subscription set publication throws warning for non-existent publication", "stderr "+stderr)

	// Cleanup
	nodePublisher.SafePSQL("postgres", `
	DROP PUBLICATION mypub;
	SELECT pg_drop_replication_slot('mysub');
`)
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION mysub1")

	// Test ALTER PUBLICATION RENAME command during the replication.
	testSwap := func(tableName, pubname, appname string) {
		t.Helper()
		// Confirms tuples can be replicated
		nodePublisher.SafePSQL("postgres", "INSERT INTO "+tableName+" VALUES (1);")
		waitForSubCatchup(t, nodePublisher, appname)
		is(nodeSubscriber.SafePSQL("postgres", "SELECT a FROM "+tableName), "1",
			"check replication worked well before renaming a publication")

		// Swap the name of publications; $pubname <-> pub_empty
		swap := `
		ALTER PUBLICATION ` + pubname + ` RENAME TO tap_pub_tmp;
		ALTER PUBLICATION pub_empty RENAME TO ` + pubname + `;
		ALTER PUBLICATION tap_pub_tmp RENAME TO pub_empty;
	`
		nodePublisher.SafePSQL("postgres", swap)

		// Insert the data again
		nodePublisher.SafePSQL("postgres", "INSERT INTO "+tableName+" VALUES (2);")
		waitForSubCatchup(t, nodePublisher, appname)

		// Confirms the second tuple won't be replicated because $pubname does
		// not contains relations anymore.
		is(nodeSubscriber.SafePSQL("postgres", "SELECT a FROM "+tableName+" ORDER BY a"), "1",
			"check the tuple inserted after the RENAME was not replicated")

		// Restore the name of publications because it can be called several
		// times
		nodePublisher.SafePSQL("postgres", swap)
	}

	// Create another table
	ddl = "CREATE TABLE test2 (a int, b text);"
	nodePublisher.SafePSQL("postgres", ddl)
	nodeSubscriber.SafePSQL("postgres", ddl)

	// Create publications and a subscription
	nodePublisher.SafePSQL("postgres", `
	CREATE PUBLICATION pub_empty;
	CREATE PUBLICATION pub_for_tab FOR TABLE test1;
	CREATE PUBLICATION pub_for_all_tables FOR ALL TABLES;
`)
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION tap_sub CONNECTION '"+publisherConnstr+"' PUBLICATION pub_for_tab")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	// Confirms RENAME command works well for a publication
	testSwap("test1", "pub_for_tab", "tap_sub")

	// Switches a publication which includes all tables
	nodeSubscriber.SafePSQL("postgres", "ALTER SUBSCRIPTION tap_sub SET PUBLICATION pub_for_all_tables;")

	// Wait for initial table sync to finish
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "tap_sub")

	// Confirms RENAME command works well for ALL TABLES publication
	testSwap("test2", "pub_for_all_tables", "tap_sub")

	// Cleanup
	nodePublisher.SafePSQL("postgres", `
	DROP PUBLICATION pub_empty, pub_for_tab, pub_for_all_tables;
	DROP TABLE test1, test2;
`)
	nodeSubscriber.SafePSQL("postgres", `
	DROP SUBSCRIPTION tap_sub;
	DROP TABLE test1, test2;
`)

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
