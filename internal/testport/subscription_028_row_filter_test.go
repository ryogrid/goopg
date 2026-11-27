package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription028RowFilter ports
// postgres/src/test/subscription/t/028_row_filter.pl to the M0149 contract:
// publication row filters (FOR TABLE t WHERE (...)) — ignored when the table
// is also published by a FOR ALL TABLES or TABLES IN SCHEMA publication,
// OR-combined across publications and dropped when any publication of the
// table has none, applied to the initial table sync COPY and to the
// streamed changes (with PG's UPDATE transformation: old row matching / new
// row not → DELETE, and the reverse → INSERT), through
// publish_via_partition_root, inheritance children, TOAST and virtual
// generated columns. Three pairings: all-goopg, a PG 18.3 publisher with a
// goopg subscriber, a goopg publisher with a PG 18.3 subscriber.
func TestPort_Subscription028RowFilter(t *testing.T) {
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
			runSubscriptionRowFilter(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionRowFilter(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
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
	pub := func(sql string) { nodePublisher.SafePSQL("postgres", sql) }
	sub := func(sql string) string { return nodeSubscriber.SafePSQL("postgres", sql) }
	both := func(sql string) { pub(sql); sub(sql) }
	publisherConnstr := nodePublisher.ConnStr()
	appname := "tap_sub"
	subscribe := func(pubs string) {
		t.Helper()
		sub("CREATE SUBSCRIPTION tap_sub CONNECTION '" + publisherConnstr + " application_name=" + appname + "' PUBLICATION " + pubs)
		waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, appname)
	}
	catchup := func() { t.Helper(); waitForSubCatchup(t, nodePublisher, appname) }

	// Testcase start: FOR ALL TABLES
	both("CREATE TABLE tab_rf_x (x int primary key)")
	pub("INSERT INTO tab_rf_x (x) VALUES (0), (5), (10), (15), (20)")
	pub("CREATE PUBLICATION tap_pub_x FOR TABLE tab_rf_x WHERE (x > 10)")
	pub("CREATE PUBLICATION tap_pub_forall FOR ALL TABLES")
	subscribe("tap_pub_x, tap_pub_forall")
	is(sub("SELECT count(x) FROM tab_rf_x"), "5", "check initial data copy from table tab_rf_x should not be filtered")
	pub("INSERT INTO tab_rf_x (x) VALUES (-99), (99)")
	catchup()
	is(sub("SELECT count(x) FROM tab_rf_x"), "7", "check table tab_rf_x should not be filtered")
	pub("DROP PUBLICATION tap_pub_forall")
	pub("DROP PUBLICATION tap_pub_x")
	pub("DROP TABLE tab_rf_x")
	sub("DROP SUBSCRIPTION tap_sub")
	sub("DROP TABLE tab_rf_x")

	// Testcase start: TABLES IN SCHEMA
	both("CREATE SCHEMA schema_rf_x")
	both("CREATE TABLE schema_rf_x.tab_rf_x (x int primary key)")
	both("CREATE TABLE schema_rf_x.tab_rf_partitioned (x int primary key) PARTITION BY RANGE(x)")
	both("CREATE TABLE public.tab_rf_partition (LIKE schema_rf_x.tab_rf_partitioned)")
	both("ALTER TABLE schema_rf_x.tab_rf_partitioned ATTACH PARTITION public.tab_rf_partition DEFAULT")
	pub("INSERT INTO schema_rf_x.tab_rf_x (x) VALUES (0), (5), (10), (15), (20)")
	pub("INSERT INTO schema_rf_x.tab_rf_partitioned (x) VALUES (1), (20)")
	pub("CREATE PUBLICATION tap_pub_x FOR TABLE schema_rf_x.tab_rf_x WHERE (x > 10)")
	pub("CREATE PUBLICATION tap_pub_allinschema FOR TABLES IN SCHEMA schema_rf_x, TABLE schema_rf_x.tab_rf_x WHERE (x > 10)")
	pub("ALTER PUBLICATION tap_pub_allinschema ADD TABLE public.tab_rf_partition WHERE (x > 10)")
	subscribe("tap_pub_x, tap_pub_allinschema")
	is(sub("SELECT count(x) FROM schema_rf_x.tab_rf_x"), "5", "check initial data copy from table tab_rf_x should not be filtered")
	pub("INSERT INTO schema_rf_x.tab_rf_x (x) VALUES (-99), (99)")
	pub("INSERT INTO schema_rf_x.tab_rf_partitioned (x) VALUES (5), (25)")
	catchup()
	is(sub("SELECT count(x) FROM schema_rf_x.tab_rf_x"), "7", "check table tab_rf_x should not be filtered")
	is(sub("SELECT * FROM public.tab_rf_partition"), "20\n25", "check table tab_rf_partition should be filtered")
	pub("DROP PUBLICATION tap_pub_allinschema")
	pub("DROP PUBLICATION tap_pub_x")
	pub("DROP TABLE public.tab_rf_partition")
	pub("DROP TABLE schema_rf_x.tab_rf_partitioned")
	pub("DROP TABLE schema_rf_x.tab_rf_x")
	pub("DROP SCHEMA schema_rf_x")
	sub("DROP SUBSCRIPTION tap_sub")
	sub("DROP TABLE public.tab_rf_partition")
	sub("DROP TABLE schema_rf_x.tab_rf_partitioned")
	sub("DROP TABLE schema_rf_x.tab_rf_x")
	sub("DROP SCHEMA schema_rf_x")

	// Testcase start: FOR TABLE with row filter publications
	pub("CREATE TABLE tab_rowfilter_1 (a int primary key, b text)")
	pub("ALTER TABLE tab_rowfilter_1 REPLICA IDENTITY FULL;")
	pub("CREATE TABLE tab_rowfilter_2 (c int primary key)")
	pub("CREATE TABLE tab_rowfilter_3 (a int primary key, b boolean)")
	pub("CREATE TABLE tab_rowfilter_4 (c int primary key)")
	pub("CREATE TABLE tab_rowfilter_partitioned (a int primary key, b integer) PARTITION BY RANGE(a)")
	pub("CREATE TABLE tab_rowfilter_less_10k (LIKE tab_rowfilter_partitioned)")
	pub("ALTER TABLE tab_rowfilter_partitioned ATTACH PARTITION tab_rowfilter_less_10k FOR VALUES FROM (MINVALUE) TO (10000)")
	pub("CREATE TABLE tab_rowfilter_greater_10k (LIKE tab_rowfilter_partitioned)")
	pub("ALTER TABLE tab_rowfilter_partitioned ATTACH PARTITION tab_rowfilter_greater_10k FOR VALUES FROM (10000) TO (MAXVALUE)")
	pub("CREATE TABLE tab_rowfilter_partitioned_2 (a int primary key, b integer) PARTITION BY RANGE(a)")
	pub("CREATE TABLE tab_rowfilter_partition (LIKE tab_rowfilter_partitioned_2)")
	pub("ALTER TABLE tab_rowfilter_partitioned_2 ATTACH PARTITION tab_rowfilter_partition DEFAULT")
	pub("CREATE TABLE tab_rowfilter_toast (a text NOT NULL, b text NOT NULL)")
	pub("ALTER TABLE tab_rowfilter_toast ALTER COLUMN a SET STORAGE EXTERNAL")
	pub("CREATE UNIQUE INDEX tab_rowfilter_toast_ri_index on tab_rowfilter_toast (a, b)")
	pub("ALTER TABLE tab_rowfilter_toast REPLICA IDENTITY USING INDEX tab_rowfilter_toast_ri_index")
	pub("CREATE TABLE tab_rowfilter_inherited (a int)")
	pub("CREATE TABLE tab_rowfilter_child (b text) INHERITS (tab_rowfilter_inherited)")
	pub("CREATE TABLE tab_rowfilter_viaroot_part (a int) PARTITION BY RANGE (a)")
	pub("CREATE TABLE tab_rowfilter_viaroot_part_1 PARTITION OF tab_rowfilter_viaroot_part FOR VALUES FROM (1) TO (20)")
	pub("CREATE TABLE tab_rowfilter_parent_sync (a int) PARTITION BY RANGE (a)")
	pub("CREATE TABLE tab_rowfilter_child_sync PARTITION OF tab_rowfilter_parent_sync FOR VALUES FROM (1) TO (20)")
	pub("CREATE TABLE tab_rowfilter_virtual (id int PRIMARY KEY, x int, y int GENERATED ALWAYS AS (x * 2) VIRTUAL)")
	// setup structure on subscriber
	sub("CREATE TABLE tab_rowfilter_1 (a int primary key, b text)")
	sub("CREATE TABLE tab_rowfilter_2 (c int primary key)")
	sub("CREATE TABLE tab_rowfilter_3 (a int primary key, b boolean)")
	sub("CREATE TABLE tab_rowfilter_4 (c int primary key)")
	sub("CREATE TABLE tab_rowfilter_partitioned (a int primary key, b integer) PARTITION BY RANGE(a)")
	sub("CREATE TABLE tab_rowfilter_less_10k (LIKE tab_rowfilter_partitioned)")
	sub("ALTER TABLE tab_rowfilter_partitioned ATTACH PARTITION tab_rowfilter_less_10k FOR VALUES FROM (MINVALUE) TO (10000)")
	sub("CREATE TABLE tab_rowfilter_greater_10k (LIKE tab_rowfilter_partitioned)")
	sub("ALTER TABLE tab_rowfilter_partitioned ATTACH PARTITION tab_rowfilter_greater_10k FOR VALUES FROM (10000) TO (MAXVALUE)")
	sub("CREATE TABLE tab_rowfilter_partitioned_2 (a int primary key, b integer) PARTITION BY RANGE(a)")
	sub("CREATE TABLE tab_rowfilter_partition (LIKE tab_rowfilter_partitioned_2)")
	sub("ALTER TABLE tab_rowfilter_partitioned_2 ATTACH PARTITION tab_rowfilter_partition DEFAULT")
	sub("CREATE TABLE tab_rowfilter_toast (a text NOT NULL, b text NOT NULL)")
	sub("CREATE UNIQUE INDEX tab_rowfilter_toast_ri_index on tab_rowfilter_toast (a, b)")
	sub("ALTER TABLE tab_rowfilter_toast REPLICA IDENTITY USING INDEX tab_rowfilter_toast_ri_index")
	sub("CREATE TABLE tab_rowfilter_inherited (a int)")
	sub("CREATE TABLE tab_rowfilter_child (b text) INHERITS (tab_rowfilter_inherited)")
	sub("CREATE TABLE tab_rowfilter_viaroot_part (a int)")
	sub("CREATE TABLE tab_rowfilter_viaroot_part_1 (a int)")
	sub("CREATE TABLE tab_rowfilter_parent_sync (a int)")
	sub("CREATE TABLE tab_rowfilter_child_sync (a int)")
	sub("CREATE TABLE tab_rowfilter_virtual (id int PRIMARY KEY, x int, y int GENERATED ALWAYS AS (x * 2) VIRTUAL)")

	// setup logical replication
	pub("CREATE PUBLICATION tap_pub_1 FOR TABLE tab_rowfilter_1 WHERE (a > 1000 AND b <> 'filtered')")
	pub("ALTER PUBLICATION tap_pub_1 ADD TABLE tab_rowfilter_2 WHERE (c % 7 = 0)")
	pub("ALTER PUBLICATION tap_pub_1 SET TABLE tab_rowfilter_1 WHERE (a > 1000 AND b <> 'filtered'), tab_rowfilter_2 WHERE (c % 2 = 0), tab_rowfilter_3")
	pub("CREATE PUBLICATION tap_pub_2 FOR TABLE tab_rowfilter_2 WHERE (c % 3 = 0)")
	pub("CREATE PUBLICATION tap_pub_3 FOR TABLE tab_rowfilter_partitioned")
	pub("ALTER PUBLICATION tap_pub_3 ADD TABLE tab_rowfilter_less_10k WHERE (a < 6000)")
	pub("CREATE PUBLICATION tap_pub_not_used FOR TABLE tab_rowfilter_1 WHERE (a < 0)")
	pub("CREATE PUBLICATION tap_pub_4a FOR TABLE tab_rowfilter_4 WHERE (c % 2 = 0)")
	pub("CREATE PUBLICATION tap_pub_4b FOR TABLE tab_rowfilter_4")
	pub("CREATE PUBLICATION tap_pub_5a FOR TABLE tab_rowfilter_partitioned_2")
	pub("CREATE PUBLICATION tap_pub_5b FOR TABLE tab_rowfilter_partition WHERE (a > 10)")
	pub("CREATE PUBLICATION tap_pub_toast FOR TABLE tab_rowfilter_toast WHERE (a = repeat('1234567890', 200) AND b < '10')")
	pub("CREATE PUBLICATION tap_pub_inherits FOR TABLE tab_rowfilter_inherited WHERE (a > 15)")
	pub("CREATE PUBLICATION tap_pub_viaroot_1 FOR TABLE tab_rowfilter_viaroot_part WHERE (a > 15) WITH (publish_via_partition_root)")
	pub("CREATE PUBLICATION tap_pub_viaroot_2 FOR TABLE tab_rowfilter_viaroot_part_1 WHERE (a < 15) WITH (publish_via_partition_root)")
	pub("CREATE PUBLICATION tap_pub_parent_sync FOR TABLE tab_rowfilter_parent_sync WHERE (a > 15) WITH (publish_via_partition_root)")
	pub("CREATE PUBLICATION tap_pub_child_sync FOR TABLE tab_rowfilter_child_sync WHERE (a < 15)")
	pub("CREATE PUBLICATION tap_pub_virtual FOR TABLE tab_rowfilter_virtual WHERE (y > 10)")

	// initial data
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1, 'not replicated')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1500, 'filtered')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1980, 'not filtered')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) SELECT x, 'test ' || x FROM generate_series(990,1002) x")
	pub("INSERT INTO tab_rowfilter_2 (c) SELECT generate_series(1, 20)")
	pub("INSERT INTO tab_rowfilter_3 (a, b) SELECT x, (x % 3 = 0) FROM generate_series(1, 10) x")
	pub("INSERT INTO tab_rowfilter_4 (c) SELECT generate_series(1, 10)")
	pub("INSERT INTO tab_rowfilter_parent_sync(a) VALUES(14), (16)")
	pub("INSERT INTO tab_rowfilter_partitioned (a, b) VALUES(1, 100),(7000, 101),(15000, 102),(5500, 300)")
	pub("INSERT INTO tab_rowfilter_less_10k (a, b) VALUES(2, 200),(6005, 201)")
	pub("INSERT INTO tab_rowfilter_greater_10k (a, b) VALUES(16000, 103)")
	pub("INSERT INTO tab_rowfilter_partitioned_2 (a, b) VALUES(1, 1),(20, 20)")
	pub("INSERT INTO tab_rowfilter_toast(a, b) VALUES(repeat('1234567890', 200), '1234567890')")
	pub("INSERT INTO tab_rowfilter_inherited(a) VALUES(10),(20)")
	pub("INSERT INTO tab_rowfilter_child(a, b) VALUES(0,'0'),(30,'30'),(40,'40')")
	pub("INSERT INTO tab_rowfilter_virtual (id, x) VALUES (1, 2), (2, 4), (3, 6)")

	subscribe("tap_pub_1, tap_pub_2, tap_pub_3, tap_pub_4a, tap_pub_4b, tap_pub_5a, tap_pub_5b, tap_pub_toast, tap_pub_inherits, tap_pub_viaroot_2, tap_pub_viaroot_1, tap_pub_parent_sync, tap_pub_child_sync, tap_pub_virtual")

	is(sub("SELECT a, b FROM tab_rowfilter_1 ORDER BY 1, 2"), "1001|test 1001\n1002|test 1002\n1980|not filtered", "check initial data copy from table tab_rowfilter_1")
	is(sub("SELECT count(c), min(c), max(c) FROM tab_rowfilter_2"), "13|2|20", "check initial data copy from table tab_rowfilter_2")
	is(sub("SELECT count(c), min(c), max(c) FROM tab_rowfilter_4"), "10|1|10", "check initial data copy from table tab_rowfilter_4")
	is(sub("SELECT count(a) FROM tab_rowfilter_3"), "10", "check initial data copy from table tab_rowfilter_3")
	is(sub("SELECT a, b FROM tab_rowfilter_less_10k ORDER BY 1, 2"), "1|100\n2|200\n5500|300", "check initial data copy from partition tab_rowfilter_less_10k")
	is(sub("SELECT a, b FROM tab_rowfilter_greater_10k ORDER BY 1, 2"), "15000|102\n16000|103", "check initial data copy from partition tab_rowfilter_greater_10k")
	is(sub("SELECT a, b FROM tab_rowfilter_partition ORDER BY 1, 2"), "1|1\n20|20", "check initial data copy from partition tab_rowfilter_partition")
	is(sub("SELECT count(*) FROM tab_rowfilter_toast"), "0", "check initial data copy from table tab_rowfilter_toast")
	is(sub("SELECT a FROM tab_rowfilter_inherited ORDER BY a"), "20\n30\n40", "check initial data copy from table tab_rowfilter_inherited")
	is(sub("SELECT a FROM tab_rowfilter_parent_sync ORDER BY 1"), "16", "check initial data copy from tab_rowfilter_parent_sync")
	is(sub("SELECT a FROM tab_rowfilter_child_sync ORDER BY 1"), "", "check initial data copy from tab_rowfilter_child_sync")
	is(sub("SELECT id, x FROM tab_rowfilter_virtual ORDER BY id"), "3|6", "check initial data copy from table tab_rowfilter_virtual")

	// streamed changes
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (800, 'test 800')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1600, 'test 1600')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1601, 'test 1601')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1602, 'filtered')")
	pub("INSERT INTO tab_rowfilter_1 (a, b) VALUES (1700, 'test 1700')")
	pub("UPDATE tab_rowfilter_1 SET b = NULL WHERE a = 1600")
	pub("UPDATE tab_rowfilter_1 SET b = 'test 1601 updated' WHERE a = 1601")
	pub("UPDATE tab_rowfilter_1 SET b = 'test 1602 updated' WHERE a = 1602")
	pub("DELETE FROM tab_rowfilter_1 WHERE a = 1700")
	pub("INSERT INTO tab_rowfilter_2 (c) VALUES (21), (22), (23), (24), (25)")
	pub("INSERT INTO tab_rowfilter_4 (c) VALUES (0), (11), (12)")
	pub("INSERT INTO tab_rowfilter_inherited (a) VALUES (14), (16)")
	pub("INSERT INTO tab_rowfilter_child (a, b) VALUES (13, '13'), (17, '17')")
	pub("INSERT INTO tab_rowfilter_viaroot_part (a) VALUES (14), (15), (16)")
	pub("INSERT INTO tab_rowfilter_virtual (id, x) VALUES (4, 3), (5, 7)")
	catchup()
	is(sub("SELECT count(c), min(c), max(c) FROM tab_rowfilter_2"), "16|2|24", "check replicated rows to tab_rowfilter_2")
	is(sub("SELECT count(c), min(c), max(c) FROM tab_rowfilter_4"), "13|0|12", "check replicated rows to tab_rowfilter_4")
	is(sub("SELECT a, b FROM tab_rowfilter_1 ORDER BY 1, 2"), "1001|test 1001\n1002|test 1002\n1601|test 1601 updated\n1602|test 1602 updated\n1980|not filtered", "check replicated rows to table tab_rowfilter_1")

	// publish_via_partition_root switch and REFRESH with copy_data
	pub("ALTER PUBLICATION tap_pub_3 SET (publish_via_partition_root = true)")
	pub("ALTER PUBLICATION tap_pub_3 SET TABLE tab_rowfilter_partitioned WHERE (a < 5000), tab_rowfilter_less_10k WHERE (a < 6000)")
	sub("TRUNCATE TABLE tab_rowfilter_partitioned")
	sub("ALTER SUBSCRIPTION tap_sub REFRESH PUBLICATION WITH (copy_data = true)")
	waitForSubscriberSync(t, nodeSubscriber)
	pub("INSERT INTO tab_rowfilter_partitioned (a, b) VALUES(4000, 400),(4001, 401),(4002, 402)")
	pub("INSERT INTO tab_rowfilter_less_10k (a, b) VALUES(4500, 450)")
	pub("INSERT INTO tab_rowfilter_less_10k (a, b) VALUES(5600, 123)")
	pub("INSERT INTO tab_rowfilter_greater_10k (a, b) VALUES(14000, 1950)")
	pub("UPDATE tab_rowfilter_less_10k SET b = 30 WHERE a = 4001")
	pub("DELETE FROM tab_rowfilter_less_10k WHERE a = 4002")
	catchup()
	is(sub("SELECT a, b FROM tab_rowfilter_partitioned ORDER BY 1, 2"), "1|100\n2|200\n4000|400\n4001|30\n4500|450", "check publish_via_partition_root behavior")
	is(sub("SELECT a FROM tab_rowfilter_inherited ORDER BY a"), "16\n17\n20\n30\n40", "check replicated rows to tab_rowfilter_inherited and tab_rowfilter_child")
	is(sub("SELECT id, x FROM tab_rowfilter_virtual ORDER BY id"), "3|6\n5|7", "check replicated rows to tab_rowfilter_virtual")

	// UPDATE the non-toasted column for table tab_rowfilter_toast
	pub("UPDATE tab_rowfilter_toast SET b = '1'")
	catchup()
	is(sub("SELECT a = repeat('1234567890', 200), b FROM tab_rowfilter_toast"), "t|1", "check replicated rows to tab_rowfilter_toast")
	is(sub("SELECT a FROM tab_rowfilter_viaroot_part"), "16", "check replicated rows to tab_rowfilter_viaroot_part")
	is(sub("SELECT a FROM tab_rowfilter_viaroot_part_1"), "", "check replicated rows to tab_rowfilter_viaroot_part_1")

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
