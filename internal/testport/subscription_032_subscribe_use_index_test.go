package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription032SubscribeUseIndex ports
// postgres/src/test/subscription/t/032_subscribe_use_index.pl to the M0149
// contract: under REPLICA IDENTITY FULL the subscriber's apply worker finds
// the row to UPDATE / DELETE through a usable index
// (FindUsableIndexForReplicaIdentityFull: btree or hash, leftmost key a
// plain column, not partial) and counts the scans in
// pg_stat_all_indexes.idx_scan; an index on expressions only, or a partial
// one, is not used (sequential scan). Covers partitioned tables, an index
// with expressions after plain columns, a dropped index, a subscriber
// column missing on the publisher (NULL), a unique index, and a hash index.
// Three pairings: all-goopg, a PG 18.3 publisher with a goopg subscriber,
// a goopg publisher with a PG 18.3 subscriber.
//
// Ported in M0149-0054.
func TestPort_Subscription032SubscribeUseIndex(t *testing.T) {
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
			runSubscriptionUseIndex(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionUseIndex(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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
	appname := "tap_sub"
	pub := func(sql string) { nodePublisher.SafePSQL("postgres", sql) }
	sub := func(sql string) string { return nodeSubscriber.SafePSQL("postgres", sql) }
	pollSub := func(q, what string) {
		t.Helper()
		if !nodeSubscriber.PollQueryUntil("postgres", q, "t") {
			st, _, _ := nodeSubscriber.PSQLExit("postgres", "SELECT indexrelname, idx_scan FROM pg_stat_all_indexes WHERE schemaname = 'public'")
			t.Fatalf("Timed out while waiting for %s; pg_stat_all_indexes: %q", what, st)
		}
	}
	subscribe := func() {
		t.Helper()
		sub("CREATE SUBSCRIPTION tap_sub_rep_full CONNECTION '" + publisherConnstr + " application_name=" + appname + "' PUBLICATION tap_pub_rep_full")
		waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, appname)
	}
	teardown := func(table string) {
		pub("DROP PUBLICATION tap_pub_rep_full")
		pub("DROP TABLE " + table)
		sub("DROP SUBSCRIPTION tap_sub_rep_full")
		sub("DROP TABLE " + table)
	}

	// Testcase start: SUBSCRIPTION USES INDEX
	pub("CREATE TABLE test_replica_id_full (x int, y text)")
	pub("ALTER TABLE test_replica_id_full REPLICA IDENTITY FULL")
	sub("CREATE TABLE test_replica_id_full (x int, y text)")
	sub("CREATE INDEX test_replica_id_full_idx ON test_replica_id_full(x,y)")
	pub("INSERT INTO test_replica_id_full SELECT (i%10), (i%10)::text FROM generate_series(0,10) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE test_replica_id_full")
	subscribe()
	pub("DELETE FROM test_replica_id_full WHERE x IN (5, 6)")
	pub("UPDATE test_replica_id_full SET x = 100, y = '200' WHERE x IN (1, 2)")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select (idx_scan = 4) from pg_stat_all_indexes where indexrelname = 'test_replica_id_full_idx';",
		"check subscriber tap_sub_rep_full updates 4 rows via index")
	is(sub("select count(*) from test_replica_id_full WHERE (x = 100 and y = '200')"), "2", "ensure subscriber has the correct data at the end of the test")
	is(sub("select count(*) from test_replica_id_full where x in (5, 6)"), "0", "ensure subscriber has the correct data at the end of the test")
	teardown("test_replica_id_full")

	// Testcase start: SUBSCRIPTION USES INDEX ON PARTITIONED TABLES
	pub("CREATE TABLE users_table_part(user_id bigint, value_1 int, value_2 int) PARTITION BY RANGE (value_1)")
	pub("CREATE TABLE users_table_part_0 PARTITION OF users_table_part FOR VALUES FROM (0) TO (10)")
	pub("CREATE TABLE users_table_part_1 PARTITION OF users_table_part FOR VALUES FROM (10) TO (20)")
	pub("ALTER TABLE users_table_part REPLICA IDENTITY FULL")
	pub("ALTER TABLE users_table_part_0 REPLICA IDENTITY FULL")
	pub("ALTER TABLE users_table_part_1 REPLICA IDENTITY FULL")
	sub("CREATE TABLE users_table_part(user_id bigint, value_1 int, value_2 int) PARTITION BY RANGE (value_1)")
	sub("CREATE TABLE users_table_part_0 PARTITION OF users_table_part FOR VALUES FROM (0) TO (10)")
	sub("CREATE TABLE users_table_part_1 PARTITION OF users_table_part FOR VALUES FROM (10) TO (20)")
	sub("CREATE INDEX users_table_part_idx ON users_table_part(user_id, value_1)")
	pub("INSERT INTO users_table_part SELECT (i%100), (i%20), i FROM generate_series(0,100) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE users_table_part")
	subscribe()
	pub("UPDATE users_table_part SET value_1 = 0 WHERE user_id = 4")
	pub("DELETE FROM users_table_part WHERE user_id = 1 and value_1 = 1")
	pub("DELETE FROM users_table_part WHERE user_id = 12 and value_1 = 12")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select sum(idx_scan)=3 from pg_stat_all_indexes where indexrelname ilike 'users_table_part_%';",
		"check subscriber tap_sub_rep_full updates partitioned table")
	is(sub("select sum(user_id+value_1+value_2) from users_table_part"), "10907", "ensure subscriber has the correct data at the end of the test")
	is(sub("select count(DISTINCT(user_id,value_1, value_2)) from users_table_part"), "99", "ensure subscriber has the correct data at the end of the test")
	teardown("users_table_part")

	// Testcase start: SUBSCRIPTION DOES NOT USE INDEXES WITH ONLY EXPRESSIONS
	// OR PARTIAL INDEX
	pub("CREATE TABLE people (firstname text, lastname text)")
	pub("ALTER TABLE people REPLICA IDENTITY FULL")
	sub("CREATE TABLE people (firstname text, lastname text)")
	sub("CREATE INDEX people_names_expr_only ON people ((firstname || ' ' || lastname))")
	sub("CREATE INDEX people_names_partial ON people(firstname) WHERE (firstname = 'first_name_1')")
	pub("INSERT INTO people SELECT 'first_name_' || i::text, 'last_name_' || i::text FROM generate_series(0,200) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE people")
	subscribe()
	pub("UPDATE people SET firstname = 'no-name' WHERE firstname = 'first_name_1'")
	pub("UPDATE people SET firstname = 'no-name' WHERE firstname = 'first_name_2' AND lastname = 'last_name_2'")
	waitForSubCatchup(t, nodePublisher, appname)
	is(sub("select sum(idx_scan) from pg_stat_all_indexes where indexrelname IN ('people_names_expr_only', 'people_names_partial')"), "0",
		"ensure subscriber tap_sub_rep_full updates two rows via seq. scan with index on expressions")
	pub("DELETE FROM people WHERE firstname = 'first_name_3'")
	pub("DELETE FROM people WHERE firstname = 'first_name_4' AND lastname = 'last_name_4'")
	waitForSubCatchup(t, nodePublisher, appname)
	is(sub("select sum(idx_scan) from pg_stat_all_indexes where indexrelname IN ('people_names_expr_only', 'people_names_partial')"), "0",
		"ensure subscriber tap_sub_rep_full updates two rows via seq. scan with index on expressions")
	is(sub("SELECT count(*) FROM people"), "199", "ensure subscriber has the correct data at the end of the test")
	teardown("people")

	// Testcase start: SUBSCRIPTION USES INDEX WITH MULTIPLE COLUMNS AND
	// EXPRESSIONS
	pub("CREATE TABLE people (firstname text, lastname text)")
	pub("ALTER TABLE people REPLICA IDENTITY FULL")
	sub("CREATE TABLE people (firstname text, lastname text)")
	sub("CREATE INDEX people_names ON people (firstname, lastname, (firstname || ' ' || lastname))")
	pub("INSERT INTO people SELECT 'first_name_' || i::text, 'last_name_' || i::text FROM generate_series(0, 20) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE people")
	subscribe()
	pub("UPDATE people SET firstname = 'no-name' WHERE firstname = 'first_name_1'")
	pub("DELETE FROM people WHERE firstname = 'no-name'")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select idx_scan=2 from pg_stat_all_indexes where indexrelname = 'people_names';",
		"check subscriber tap_sub_rep_full deletes two rows via index scan with index on expressions and columns")
	is(sub("SELECT count(*) FROM people"), "20", "ensure subscriber has the correct data at the end of the test")
	is(sub("SELECT count(*) FROM people WHERE firstname = 'no-name'"), "0", "ensure subscriber has the correct data at the end of the test")
	// now, drop the index with the expression, we'll use sequential scan
	sub("DROP INDEX people_names")
	pub("DELETE FROM people WHERE lastname = 'last_name_18'")
	waitForSubCatchup(t, nodePublisher, appname)
	is(sub("SELECT count(*) FROM people WHERE lastname = 'last_name_18'"), "0", "ensure subscriber has the correct data at the end of the test")
	teardown("people")

	// Testcase start: Null values and missing column
	pub("CREATE TABLE test_replica_id_full (x int)")
	pub("ALTER TABLE test_replica_id_full REPLICA IDENTITY FULL")
	sub("CREATE TABLE test_replica_id_full (x int, y int)")
	sub("CREATE INDEX test_replica_id_full_idx ON test_replica_id_full(x,y)")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE test_replica_id_full")
	subscribe()
	pub("INSERT INTO test_replica_id_full VALUES (1), (2), (3)")
	pub("UPDATE test_replica_id_full SET x = x + 1 WHERE x = 1")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select idx_scan=1 from pg_stat_all_indexes where indexrelname = 'test_replica_id_full_idx';",
		"check subscriber tap_sub_rep_full updates test_replica_id_full table")
	is(sub("select sum(x) from test_replica_id_full WHERE y IS NULL"), "7", "ensure subscriber has the correct data at the end of the test")
	is(sub("select count(*) from test_replica_id_full WHERE y IS NULL"), "3", "ensure subscriber has the correct data at the end of the test")
	teardown("test_replica_id_full")

	// Testcase start: Subscription can use index with multiple rows and
	// columns (unique index)
	pub("CREATE TABLE test_replica_id_full (x int, y int)")
	pub("ALTER TABLE test_replica_id_full REPLICA IDENTITY FULL")
	sub("CREATE TABLE test_replica_id_full (x int, y int)")
	sub("CREATE UNIQUE INDEX test_replica_id_full_idxy ON test_replica_id_full(x,y)")
	pub("INSERT INTO test_replica_id_full SELECT i, i FROM generate_series(0,21) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE test_replica_id_full")
	subscribe()
	// insert some initial data
	sub("INSERT INTO test_replica_id_full SELECT i+100, i FROM generate_series(0,21) i")
	pub("UPDATE test_replica_id_full SET x = 2000 WHERE y = 15")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select (idx_scan = 1) from pg_stat_all_indexes where indexrelname = 'test_replica_id_full_idxy';",
		"check subscriber tap_sub_rep_full updates one row via index")
	is(sub("SELECT count(*) FROM test_replica_id_full WHERE x = 2000"), "1", "ensure subscriber has the correct data at the end of the test")
	teardown("test_replica_id_full")

	// Testcase start: Subscription uses hash index
	pub("CREATE TABLE test_replica_id_full (x int, y text)")
	pub("ALTER TABLE test_replica_id_full REPLICA IDENTITY FULL")
	sub("CREATE TABLE test_replica_id_full (x int, y text)")
	sub("CREATE INDEX test_replica_id_full_idx ON test_replica_id_full USING HASH (x)")
	pub("INSERT INTO test_replica_id_full SELECT i, (i%10)::text FROM generate_series(0,10) i")
	pub("CREATE PUBLICATION tap_pub_rep_full FOR TABLE test_replica_id_full")
	subscribe()
	pub("DELETE FROM test_replica_id_full WHERE x IN (5, 6)")
	pub("UPDATE test_replica_id_full SET x = 100, y = '200' WHERE x IN (1, 2)")
	waitForSubCatchup(t, nodePublisher, appname)
	pollSub("select (idx_scan = 4) from pg_stat_all_indexes where indexrelname = 'test_replica_id_full_idx';",
		"check subscriber tap_sub_rep_full deletes 2 rows and updates 2 rows via index")
	is(sub("select count(*) from test_replica_id_full WHERE (x = 100 and y = '200')"), "2", "ensure subscriber has the correct data at the end of the test")
	is(sub("select count(*) from test_replica_id_full where x in (5, 6)"), "0", "ensure subscriber has the correct data at the end of the test")
	teardown("test_replica_id_full")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
