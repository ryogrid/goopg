package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription011Generated ports postgres/src/test/subscription/t/011_generated.pl
// to the M0149 contract: a subscriber computes its own STORED / VIRTUAL
// generated columns in initial sync and apply (also with a BEFORE trigger
// enabled as REPLICA); publish_generated_columns = none / stored decides
// whether a publisher's generated column reaches a subscriber's regular
// column; a publication column list takes precedence over that option;
// and replicating into a subscriber-side generated column fails with
// "has incompatible generated columns". It runs in three pairings:
// all-goopg, a PG 18.3 publisher with a goopg subscriber, and a goopg
// publisher with a PG 18.3 subscriber.
//
// Landed by M0149-0048a-f: the publisher's generated-column and
// column-list rule, PG 18's pg_get_publication_tables, the subscriber's own
// generated columns and incompatible-column error, PG 18's tablesync column
// set and a retried failed table sync.
func TestPort_Subscription011Generated(t *testing.T) {
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
			runSubscriptionGenerated(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

// waitForSubscriberSyncDB is wait_for_subscription_sync on database db.
func waitForSubscriberSyncDB(t *testing.T, subscriber *tapnode.Node, db string) {
	t.Helper()
	const q = "SELECT count(1) = 0 FROM pg_subscription_rel WHERE srsubstate NOT IN ('r', 's');"
	if !subscriber.PollQueryUntil(db, q, "t") {
		st, _, _ := subscriber.PSQLExit(db, "SELECT srrelid::regclass, srsubstate FROM pg_subscription_rel")
		t.Fatalf("timed out waiting for subscriber to synchronize data in %s; pg_subscription_rel: %q", db, st)
	}
}

func runSubscriptionGenerated(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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

	publisherConnstr := nodePublisher.ConnStr()

	nodePublisher.SafePSQL("postgres",
		"CREATE TABLE tab1 (a int PRIMARY KEY, b int GENERATED ALWAYS AS (a * 2) STORED, c int GENERATED ALWAYS AS (a * 3) VIRTUAL)")
	nodeSubscriber.SafePSQL("postgres",
		"CREATE TABLE tab1 (a int PRIMARY KEY, b int GENERATED ALWAYS AS (a * 22) STORED, c int GENERATED ALWAYS AS (a * 33) VIRTUAL, d int)")

	// data for initial sync
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab1 (a) VALUES (1), (2), (3)")
	nodePublisher.SafePSQL("postgres", "CREATE PUBLICATION pub1 FOR ALL TABLES")
	nodeSubscriber.SafePSQL("postgres", "CREATE SUBSCRIPTION sub1 CONNECTION '"+publisherConnstr+"' PUBLICATION pub1")

	// Wait for initial sync of all subscriptions
	waitForSubscriberSync(t, nodeSubscriber)
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b, c FROM tab1"), "1|22|33\n2|44|66\n3|66|99", "generated columns initial sync")

	// data to replicate
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab1 VALUES (4), (5)")
	nodePublisher.SafePSQL("postgres", "UPDATE tab1 SET a = 6 WHERE a = 5")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab1"), "1|22|33|\n2|44|66|\n3|66|99|\n4|88|132|\n6|132|198|", "generated columns replicated")

	// try it with a subscriber-side trigger
	nodeSubscriber.SafePSQL("postgres", `
CREATE FUNCTION tab1_trigger_func() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.d := NEW.a + 10;
  RETURN NEW;
END $$;

CREATE TRIGGER test1 BEFORE INSERT OR UPDATE ON tab1
  FOR EACH ROW
  EXECUTE PROCEDURE tab1_trigger_func();

ALTER TABLE tab1 ENABLE REPLICA TRIGGER test1;
`)
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab1 VALUES (7), (8)")
	nodePublisher.SafePSQL("postgres", "UPDATE tab1 SET a = 9 WHERE a = 7")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab1 ORDER BY 1"),
		"1|22|33|\n2|44|66|\n3|66|99|\n4|88|132|\n6|132|198|\n8|176|264|18\n9|198|297|19", "generated columns replicated with trigger")

	// cleanup
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION sub1")
	nodePublisher.SafePSQL("postgres", "DROP PUBLICATION pub1")

	// Generated to regular column replication, with publish_generated_columns
	// = none (sub1, db postgres) and = stored (sub2, db test_pgc_true).
	nodeSubscriber.SafePSQL("postgres", "CREATE DATABASE test_pgc_true")
	nodePublisher.SafePSQL("postgres", `
	CREATE TABLE tab_gen_to_nogen (a int, b int GENERATED ALWAYS AS (a * 2) STORED);
	INSERT INTO tab_gen_to_nogen (a) VALUES (1), (2), (3);
	CREATE PUBLICATION regress_pub1_gen_to_nogen FOR TABLE tab_gen_to_nogen WITH (publish_generated_columns = none);
	CREATE PUBLICATION regress_pub2_gen_to_nogen FOR TABLE tab_gen_to_nogen WITH (publish_generated_columns = stored);
`)
	nodeSubscriber.SafePSQL("postgres", `
	CREATE TABLE tab_gen_to_nogen (a int, b int);
	CREATE SUBSCRIPTION regress_sub1_gen_to_nogen CONNECTION '`+publisherConnstr+`' PUBLICATION regress_pub1_gen_to_nogen WITH (copy_data = true);
`)
	nodeSubscriber.SafePSQL("test_pgc_true", `
	CREATE TABLE tab_gen_to_nogen (a int, b int);
	CREATE SUBSCRIPTION regress_sub2_gen_to_nogen CONNECTION '`+publisherConnstr+`' PUBLICATION regress_pub2_gen_to_nogen WITH (copy_data = true);
`)
	waitForSubscriberSyncDB(t, nodeSubscriber, "postgres")
	waitForSubscriberSyncDB(t, nodeSubscriber, "test_pgc_true")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b FROM tab_gen_to_nogen ORDER BY a"), "1|\n2|\n3|",
		"tab_gen_to_nogen initial sync, when publish_generated_columns=none")
	is(nodeSubscriber.SafePSQL("test_pgc_true", "SELECT a, b FROM tab_gen_to_nogen ORDER BY a"), "1|2\n2|4\n3|6",
		"tab_gen_to_nogen initial sync, when publish_generated_columns=stored")

	nodePublisher.SafePSQL("postgres", "INSERT INTO tab_gen_to_nogen VALUES (4), (5)")
	waitForSubCatchup(t, nodePublisher, "regress_sub1_gen_to_nogen")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT a, b FROM tab_gen_to_nogen ORDER BY a"), "1|\n2|\n3|\n4|\n5|",
		"tab_gen_to_nogen incremental replication, when publish_generated_columns=none")
	waitForSubCatchup(t, nodePublisher, "regress_sub2_gen_to_nogen")
	is(nodeSubscriber.SafePSQL("test_pgc_true", "SELECT a, b FROM tab_gen_to_nogen ORDER BY a"), "1|2\n2|4\n3|6\n4|8\n5|10",
		"tab_gen_to_nogen incremental replication, when publish_generated_columns=stored")

	// cleanup
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION regress_sub1_gen_to_nogen")
	nodeSubscriber.SafePSQL("test_pgc_true", "DROP SUBSCRIPTION regress_sub2_gen_to_nogen")
	nodePublisher.SafePSQL("postgres", `
	DROP PUBLICATION regress_pub1_gen_to_nogen;
	DROP PUBLICATION regress_pub2_gen_to_nogen;
`)
	nodeSubscriber.SafePSQL("test_pgc_true", "DROP table tab_gen_to_nogen")
	nodeSubscriber.SafePSQL("postgres", "DROP DATABASE test_pgc_true")

	// A column list takes precedence: gen1 is replicated even with
	// publish_generated_columns = none.
	nodePublisher.SafePSQL("postgres", `
	CREATE TABLE tab2 (a int, gen1 int GENERATED ALWAYS AS (a * 2) STORED);
	INSERT INTO tab2 (a) VALUES (1), (2);
	CREATE PUBLICATION pub1 FOR table tab2(gen1) WITH (publish_generated_columns=none);
`)
	nodeSubscriber.SafePSQL("postgres", `
	CREATE TABLE tab2 (a int, gen1 int);
	CREATE SUBSCRIPTION sub1 CONNECTION '`+publisherConnstr+`' PUBLICATION pub1 WITH (copy_data = true);
`)
	waitForSubscriberSync(t, nodeSubscriber)
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab2 ORDER BY gen1"), "|2\n|4",
		"tab2 initial sync, when publish_generated_columns=none")
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab2 VALUES (3), (4)")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab2 ORDER BY gen1"), "|2\n|4\n|6\n|8",
		"tab2 incremental replication, when publish_generated_columns=none")
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION sub1")
	nodePublisher.SafePSQL("postgres", "DROP PUBLICATION pub1")

	// With a column list only the listed generated column is replicated,
	// even with publish_generated_columns = stored.
	nodePublisher.SafePSQL("postgres", `
	CREATE TABLE tab3 (a int, gen1 int GENERATED ALWAYS AS (a * 2) STORED, gen2 int GENERATED ALWAYS AS (a * 2) STORED);
	INSERT INTO tab3 (a) VALUES (1), (2);
	CREATE PUBLICATION pub1 FOR table tab3(gen1) WITH (publish_generated_columns=stored);
`)
	nodeSubscriber.SafePSQL("postgres", `
	CREATE TABLE tab3 (a int, gen1 int, gen2 int);
	CREATE SUBSCRIPTION sub1 CONNECTION '`+publisherConnstr+`' PUBLICATION pub1 WITH (copy_data = true);
`)
	waitForSubscriberSync(t, nodeSubscriber)
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab3 ORDER BY gen1"), "|2|\n|4|",
		"tab3 initial sync, when publish_generated_columns=stored")
	nodePublisher.SafePSQL("postgres", "INSERT INTO tab3 VALUES (3), (4)")
	waitForSubCatchup(t, nodePublisher, "sub1")
	is(nodeSubscriber.SafePSQL("postgres", "SELECT * FROM tab3 ORDER BY gen1"), "|2|\n|4|\n|6|\n|8|",
		"tab3 incremental replication, when publish_generated_columns=stored")
	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION sub1")
	nodePublisher.SafePSQL("postgres", "DROP PUBLICATION pub1")

	// regular -> generated and generated -> generated fail.
	nodePublisher.SafePSQL("postgres", `
	CREATE TABLE t1(c1 int, c2 int, c3 int GENERATED ALWAYS AS (c1 * 2) STORED);
	CREATE PUBLICATION pub1 for table t1(c1, c2, c3);
	INSERT INTO t1 VALUES (1);
`)
	nodeSubscriber.SafePSQL("postgres", `
	CREATE TABLE t1(c1 int, c2 int GENERATED ALWAYS AS (c1 + 2) STORED, c3 int GENERATED ALWAYS AS (c1 + 2) STORED);
	CREATE SUBSCRIPTION sub1 CONNECTION '`+publisherConnstr+`' PUBLICATION pub1;
`)
	offset := nodeSubscriber.LogSize()
	waitForLog(t, nodeSubscriber, regexp.MustCompile(`ERROR: ( [A-Z0-9]+:)? logical replication target relation "public.t1" has incompatible generated columns: "c2", "c3"`), offset)

	nodeSubscriber.SafePSQL("postgres", "DROP SUBSCRIPTION sub1")
	nodePublisher.SafePSQL("postgres", "DROP PUBLICATION pub1")
}
