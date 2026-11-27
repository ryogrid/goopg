package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription013Partition ports
// postgres/src/test/subscription/t/013_partition.pl to the M0149 contract:
// logical replication with partitioned tables — one publisher, two
// subscribers. subscriber1 is partitioned differently from the publisher
// (tuple routing on the subscriber, a sub-partitioned leaf, a BRIN index,
// replica triggers on part of the tree); subscriber2 does not partition at
// all (leaf tables match the publisher's leaves). The first half replicates
// using the leaf partitions' identity and schema; the second half with
// publish_via_partition_root (root identity and schema, publication-list
// order independence, a lower-level root). Also covered: UPDATE replicated as
// UPDATE or as DELETE+INSERT, TRUNCATE of a leaf vs. the root, the
// update_missing / delete_missing / update_origin_differs conflict logs, the
// leaf-to-root map rebuilt after ALTER TABLE, and a subscriber-side partition
// whose column order changes. Three pairings: all-goopg, a PG 18.3 publisher
// with goopg subscribers, a goopg publisher with PG 18.3 subscribers.
//
// Three pairings 3/3 each (M0149-0061).
func TestPort_Subscription013Partition(t *testing.T) {
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
			runSubscriptionPartition(t,
				tc.publisher(t, "publisher"),
				tc.subscriber(t, "subscriber1"),
				tc.subscriber(t, "subscriber2"))
		})
	}
}

func runSubscriptionPartition(t *testing.T, nodePublisher, nodeSubscriber1, nodeSubscriber2 *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	// setup: publisher (allows_streaming => 'logical'), two plain subscribers
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	nodeSubscriber1.Start()
	nodeSubscriber2.Start()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, n := range []*tapnode.Node{nodeSubscriber1, nodeSubscriber2, nodePublisher} {
			for _, l := range strings.Split(fileTail(n.LogPath(), 2000), "\n") {
				if strings.Contains(l, "ERROR") || strings.Contains(l, "FATAL") || strings.Contains(l, "level=ERROR") || strings.Contains(l, "level=WARN") || strings.Contains(l, "decoder run failed") {
					t.Log(n.Name() + " log| " + l)
				}
			}
		}
	})
	pub := func(sql string) string { return nodePublisher.SafePSQL("postgres", sql) }
	sub1 := func(sql string) string { return nodeSubscriber1.SafePSQL("postgres", sql) }
	sub2 := func(sql string) string { return nodeSubscriber2.SafePSQL("postgres", sql) }
	publisherConnstr := nodePublisher.ConnStr()
	catchup := func(appname string) { t.Helper(); waitForSubCatchup(t, nodePublisher, appname) }
	var result string

	// publisher
	pub("CREATE PUBLICATION pub1")
	pub("CREATE PUBLICATION pub_all FOR ALL TABLES")
	pub("CREATE TABLE tab1 (a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	pub("CREATE TABLE tab1_1 (b text, a int NOT NULL)")
	pub("ALTER TABLE tab1 ATTACH PARTITION tab1_1 FOR VALUES IN (1, 2, 3)")
	pub("CREATE TABLE tab1_2 PARTITION OF tab1 FOR VALUES IN (4, 5, 6)")
	pub("CREATE TABLE tab1_def PARTITION OF tab1 DEFAULT")
	pub("ALTER PUBLICATION pub1 ADD TABLE tab1, tab1_1")

	// subscriber1: partitioned differently from the publisher; tab1_2 is
	// subpartitioned (tests the tuple routing code on the subscriber)
	sub1("CREATE TABLE tab1 (c text, a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	// make a BRIN index to test aminsertcleanup logic in subscriber
	sub1("CREATE INDEX tab1_c_brin_idx ON tab1 USING brin (c)")
	sub1("CREATE TABLE tab1_1 (b text, c text DEFAULT 'sub1_tab1', a int NOT NULL)")
	sub1("ALTER TABLE tab1 ATTACH PARTITION tab1_1 FOR VALUES IN (1, 2, 3)")
	sub1("CREATE TABLE tab1_2 PARTITION OF tab1 (c DEFAULT 'sub1_tab1') FOR VALUES IN (4, 5, 6) PARTITION BY LIST (a)")
	sub1("CREATE TABLE tab1_2_1 (c text, b text, a int NOT NULL)")
	sub1("ALTER TABLE tab1_2 ATTACH PARTITION tab1_2_1 FOR VALUES IN (5)")
	sub1("CREATE TABLE tab1_2_2 PARTITION OF tab1_2 FOR VALUES IN (4, 6)")
	sub1("CREATE TABLE tab1_def PARTITION OF tab1 (c DEFAULT 'sub1_tab1') DEFAULT")
	sub1("CREATE SUBSCRIPTION sub1 CONNECTION '" + publisherConnstr + "' PUBLICATION pub1")

	// AFTER replica triggers on a subset of subscriber1's partition tree,
	// recording all trigger activity
	sub1(`
CREATE TABLE sub1_trigger_activity (tgtab text, tgop text,
  tgwhen text, tglevel text, olda int, newa int);
CREATE FUNCTION sub1_trigger_activity_func() RETURNS TRIGGER AS $$
BEGIN
  IF (TG_OP = 'INSERT') THEN
    INSERT INTO public.sub1_trigger_activity
      SELECT TG_RELNAME, TG_OP, TG_WHEN, TG_LEVEL, NULL, NEW.a;
  ELSIF (TG_OP = 'UPDATE') THEN
    INSERT INTO public.sub1_trigger_activity
      SELECT TG_RELNAME, TG_OP, TG_WHEN, TG_LEVEL, OLD.a, NEW.a;
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER sub1_tab1_log_op_trigger
  AFTER INSERT OR UPDATE ON tab1
  FOR EACH ROW EXECUTE PROCEDURE sub1_trigger_activity_func();
ALTER TABLE ONLY tab1 ENABLE REPLICA TRIGGER sub1_tab1_log_op_trigger;
CREATE TRIGGER sub1_tab1_2_log_op_trigger
  AFTER INSERT OR UPDATE ON tab1_2
  FOR EACH ROW EXECUTE PROCEDURE sub1_trigger_activity_func();
ALTER TABLE ONLY tab1_2 ENABLE REPLICA TRIGGER sub1_tab1_2_log_op_trigger;
CREATE TRIGGER sub1_tab1_2_2_log_op_trigger
  AFTER INSERT OR UPDATE ON tab1_2_2
  FOR EACH ROW EXECUTE PROCEDURE sub1_trigger_activity_func();
ALTER TABLE ONLY tab1_2_2 ENABLE REPLICA TRIGGER sub1_tab1_2_2_log_op_trigger;
`)

	// subscriber 2: no partitioning; the tables match the publisher's leaves
	sub2("CREATE TABLE tab1 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab1', b text)")
	sub2("CREATE TABLE tab1_1 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab1_1', b text)")
	sub2("CREATE TABLE tab1_2 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab1_2', b text)")
	sub2("CREATE TABLE tab1_def (a int PRIMARY KEY, b text, c text DEFAULT 'sub2_tab1_def')")
	sub2("CREATE SUBSCRIPTION sub2 CONNECTION '" + publisherConnstr + "' PUBLICATION pub_all")

	// AFTER replica triggers on subscriber2, same method as subscriber1
	sub2(`
CREATE TABLE sub2_trigger_activity (tgtab text,
  tgop text, tgwhen text, tglevel text, olda int, newa int);
CREATE FUNCTION sub2_trigger_activity_func() RETURNS TRIGGER AS $$
BEGIN
  IF (TG_OP = 'INSERT') THEN
    INSERT INTO public.sub2_trigger_activity
      SELECT TG_RELNAME, TG_OP, TG_WHEN, TG_LEVEL, NULL, NEW.a;
  ELSIF (TG_OP = 'UPDATE') THEN
    INSERT INTO public.sub2_trigger_activity
      SELECT TG_RELNAME, TG_OP, TG_WHEN, TG_LEVEL, OLD.a, NEW.a;
  END IF;
  RETURN NULL;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER sub2_tab1_log_op_trigger
  AFTER INSERT OR UPDATE ON tab1
  FOR EACH ROW EXECUTE PROCEDURE sub2_trigger_activity_func();
ALTER TABLE ONLY tab1 ENABLE REPLICA TRIGGER sub2_tab1_log_op_trigger;
CREATE TRIGGER sub2_tab1_2_log_op_trigger
  AFTER INSERT OR UPDATE ON tab1_2
  FOR EACH ROW EXECUTE PROCEDURE sub2_trigger_activity_func();
ALTER TABLE ONLY tab1_2 ENABLE REPLICA TRIGGER sub2_tab1_2_log_op_trigger;
`)

	// Wait for initial sync of all subscriptions
	waitForSubscriberSync(t, nodeSubscriber1)
	waitForSubscriberSync(t, nodeSubscriber2)

	// TEST: replication using leaf partition identity and schema — insert
	pub("INSERT INTO tab1 VALUES (1)")
	pub("INSERT INTO tab1_1 (a) VALUES (3)")
	pub("INSERT INTO tab1_2 VALUES (5)")
	pub("INSERT INTO tab1 VALUES (0)")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub1_tab1|0\nsub1_tab1|1\nsub1_tab1|3\nsub1_tab1|5", "inserts into tab1 and its partitions replicated")

	result = sub1("SELECT a FROM tab1_2_1 ORDER BY 1")
	is(result, "5", "inserts into tab1_2 replicated into tab1_2_1 correctly")

	result = sub1("SELECT a FROM tab1_2_2 ORDER BY 1")
	is(result, "", "inserts into tab1_2 replicated into tab1_2_2 correctly")

	result = sub2("SELECT c, a FROM tab1_1 ORDER BY 1, 2")
	is(result, "sub2_tab1_1|1\nsub2_tab1_1|3", "inserts into tab1_1 replicated")

	result = sub2("SELECT c, a FROM tab1_2 ORDER BY 1, 2")
	is(result, "sub2_tab1_2|5", "inserts into tab1_2 replicated")

	// The AFTER trigger of tab1_2 should have recorded one INSERT.
	result = sub2("SELECT * FROM sub2_trigger_activity ORDER BY tgtab, tgop, tgwhen, olda, newa;")
	is(result, "tab1_2|INSERT|AFTER|ROW||5", "check replica insert after trigger applied on subscriber")

	result = sub2("SELECT c, a FROM tab1_def ORDER BY 1, 2")
	is(result, "sub2_tab1_def|0", "inserts into tab1_def replicated")

	// TEST: update (replicated as update)
	pub("UPDATE tab1 SET a = 2 WHERE a = 1")
	// All of the following cause an update to be applied to a partitioned
	// table on subscriber1: tab1_2 is leaf partition on publisher, whereas
	// it's sub-partitioned on subscriber1.
	pub("UPDATE tab1 SET a = 6 WHERE a = 5")
	pub("UPDATE tab1 SET a = 4 WHERE a = 6")
	pub("UPDATE tab1 SET a = 6 WHERE a = 4")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub1_tab1|0\nsub1_tab1|2\nsub1_tab1|3\nsub1_tab1|6", "update of tab1_1, tab1_2 replicated")

	result = sub1("SELECT a FROM tab1_2_1 ORDER BY 1")
	is(result, "", "updates of tab1_2 replicated into tab1_2_1 correctly")

	result = sub1("SELECT a FROM tab1_2_2 ORDER BY 1")
	is(result, "6", "updates of tab1_2 replicated into tab1_2_2 correctly")

	// The AFTER trigger should have recorded the UPDATEs of tab1_2_2.
	result = sub1("SELECT * FROM sub1_trigger_activity ORDER BY tgtab, tgop, tgwhen, olda, newa;")
	is(result, "tab1_2_2|INSERT|AFTER|ROW||6\ntab1_2_2|UPDATE|AFTER|ROW|4|6\ntab1_2_2|UPDATE|AFTER|ROW|6|4", "check replica update after trigger applied on subscriber")

	result = sub2("SELECT c, a FROM tab1_1 ORDER BY 1, 2")
	is(result, "sub2_tab1_1|2\nsub2_tab1_1|3", "update of tab1_1 replicated")

	result = sub2("SELECT c, a FROM tab1_2 ORDER BY 1, 2")
	is(result, "sub2_tab1_2|6", "tab1_2 updated")

	// The AFTER trigger should have recorded the updates of tab1_2.
	result = sub2("SELECT * FROM sub2_trigger_activity ORDER BY tgtab, tgop, tgwhen, olda, newa;")
	is(result, "tab1_2|INSERT|AFTER|ROW||5\ntab1_2|UPDATE|AFTER|ROW|4|6\ntab1_2|UPDATE|AFTER|ROW|5|6\ntab1_2|UPDATE|AFTER|ROW|6|4", "check replica update after trigger applied on subscriber")

	result = sub2("SELECT c, a FROM tab1_def ORDER BY 1")
	is(result, "sub2_tab1_def|0", "tab1_def unchanged")

	// TEST: update (replicated as delete+insert)
	pub("UPDATE tab1 SET a = 1 WHERE a = 0")
	pub("UPDATE tab1 SET a = 4 WHERE a = 1")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub1_tab1|2\nsub1_tab1|3\nsub1_tab1|4\nsub1_tab1|6", "update of tab1 (delete from tab1_def + insert into tab1_1) replicated")

	result = sub1("SELECT a FROM tab1_2_2 ORDER BY 1")
	is(result, "4\n6", "updates of tab1 (delete + insert) replicated into tab1_2_2 correctly")

	result = sub2("SELECT c, a FROM tab1_1 ORDER BY 1, 2")
	is(result, "sub2_tab1_1|2\nsub2_tab1_1|3", "tab1_1 unchanged")

	result = sub2("SELECT c, a FROM tab1_2 ORDER BY 1, 2")
	is(result, "sub2_tab1_2|4\nsub2_tab1_2|6", "insert into tab1_2 replicated")

	result = sub2("SELECT a FROM tab1_def ORDER BY 1")
	is(result, "", "delete from tab1_def replicated")

	// TEST: delete
	pub("DELETE FROM tab1 WHERE a IN (2, 3, 5)")
	pub("DELETE FROM tab1_2")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT a FROM tab1")
	is(result, "", "delete from tab1_1, tab1_2 replicated")

	result = sub2("SELECT a FROM tab1_1")
	is(result, "", "delete from tab1_1 replicated")

	result = sub2("SELECT a FROM tab1_2")
	is(result, "", "delete from tab1_2 replicated")

	// TEST: truncate of a leaf (tab1_2), then of the root (tab1)
	sub1("INSERT INTO tab1 (a) VALUES (1), (2), (5)")
	sub2("INSERT INTO tab1_2 (a) VALUES (2)")
	pub("TRUNCATE tab1_2")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT a FROM tab1 ORDER BY 1")
	is(result, "1\n2", "truncate of tab1_2 replicated")

	result = sub2("SELECT a FROM tab1_2 ORDER BY 1")
	is(result, "", "truncate of tab1_2 replicated")

	pub("TRUNCATE tab1")

	catchup("sub1")
	catchup("sub2")

	result = sub1("SELECT a FROM tab1 ORDER BY 1")
	is(result, "", "truncate of tab1_1 replicated")
	result = sub2("SELECT a FROM tab1 ORDER BY 1")
	is(result, "", "truncate of tab1 replicated")

	// TEST: update_missing / delete_missing conflicts logged per leaf partition
	pub("INSERT INTO tab1 VALUES (1, 'foo'), (4, 'bar'), (10, 'baz')")

	catchup("sub1")
	catchup("sub2")

	sub1("DELETE FROM tab1")

	// Note that the current location of the log file is not grabbed immediately
	// after reloading the configuration, but after sending one SQL command to
	// the node so as we are sure that the reloading has taken effect.
	logLocation := nodeSubscriber1.LogSize()

	pub("UPDATE tab1 SET b = 'quux' WHERE a = 4")
	pub("DELETE FROM tab1")

	catchup("sub1")
	catchup("sub2")

	logfile := nodeSubscriber1.LogFrom(logLocation)
	ok(regexp.MustCompile(`conflict detected on relation "public.tab1_2_2": conflict=update_missing.*\n.*DETAIL:.* Could not find the row to be updated.*\n.*Remote row \(null, 4, quux\); replica identity \(a\)=\(4\)`).MatchString(logfile),
		"update target row is missing in tab1_2_2")
	ok(regexp.MustCompile(`conflict detected on relation "public.tab1_1": conflict=delete_missing.*\n.*DETAIL:.* Could not find the row to be deleted.*\n.*Replica identity \(a\)=\(1\)`).MatchString(logfile),
		"delete target row is missing in tab1_1")
	ok(regexp.MustCompile(`conflict detected on relation "public.tab1_2_2": conflict=delete_missing.*\n.*DETAIL:.* Could not find the row to be deleted.*\n.*Replica identity \(a\)=\(4\)`).MatchString(logfile),
		"delete target row is missing in tab1_2_2")
	ok(regexp.MustCompile(`conflict detected on relation "public.tab1_def": conflict=delete_missing.*\n.*DETAIL:.* Could not find the row to be deleted.*\n.*Replica identity \(a\)=\(10\)`).MatchString(logfile),
		"delete target row is missing in tab1_def")

	// TEST: replication using root table identity and schema — setup

	// publisher
	pub("DROP PUBLICATION pub1")
	pub("CREATE TABLE tab2 (a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	pub("CREATE TABLE tab2_1 (b text, a int NOT NULL)")
	pub("ALTER TABLE tab2 ATTACH PARTITION tab2_1 FOR VALUES IN (0, 1, 2, 3)")
	pub("CREATE TABLE tab2_2 PARTITION OF tab2 FOR VALUES IN (5, 6)")

	pub("CREATE TABLE tab3 (a int PRIMARY KEY, b text) PARTITION BY LIST (a)")
	pub("CREATE TABLE tab3_1 PARTITION OF tab3 FOR VALUES IN (0, 1, 2, 3, 5, 6)")

	pub("CREATE TABLE tab4 (a int PRIMARY KEY) PARTITION BY LIST (a)")
	pub("CREATE TABLE tab4_1 PARTITION OF tab4 FOR VALUES IN (-1, 0, 1) PARTITION BY LIST (a)")
	pub("CREATE TABLE tab4_1_1 PARTITION OF tab4_1 FOR VALUES IN (-1, 0, 1)")

	pub("ALTER PUBLICATION pub_all SET (publish_via_partition_root = true)")
	// Note: tab3_1's parent is not in the publication, in which case its
	// changes are published using own identity. For tab2, even though both parent
	// and child tables are present but changes will be replicated via the parent's
	// identity and only once.
	pub("CREATE PUBLICATION pub_viaroot FOR TABLE tab2, tab2_1, tab3_1 WITH (publish_via_partition_root = true)")

	pub("CREATE PUBLICATION pub_lower_level FOR TABLE tab4_1 WITH (publish_via_partition_root = true)")

	// prepare data for the initial sync
	pub("INSERT INTO tab2 VALUES (1)")
	pub("INSERT INTO tab4 VALUES (-1)")

	// subscriber 1
	sub1("DROP SUBSCRIPTION sub1")
	sub1("CREATE TABLE tab2 (a int PRIMARY KEY, c text DEFAULT 'sub1_tab2', b text) PARTITION BY RANGE (a)")
	sub1("CREATE TABLE tab2_1 (c text DEFAULT 'sub1_tab2', b text, a int NOT NULL)")
	sub1("ALTER TABLE tab2 ATTACH PARTITION tab2_1 FOR VALUES FROM (0) TO (10)")
	sub1("CREATE TABLE tab3_1 (c text DEFAULT 'sub1_tab3_1', b text, a int NOT NULL PRIMARY KEY)")
	sub1("CREATE SUBSCRIPTION sub_viaroot CONNECTION '" + publisherConnstr + "' PUBLICATION pub_viaroot")

	// subscriber 2
	sub2("DROP TABLE tab1")
	sub2("CREATE TABLE tab1 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab1', b text) PARTITION BY HASH (a)")
	// Note: tab1's partitions are named tab1_1 and tab1_2 on the publisher.
	sub2("CREATE TABLE tab1_part1 (b text, c text, a int NOT NULL)")
	sub2("ALTER TABLE tab1 ATTACH PARTITION tab1_part1 FOR VALUES WITH (MODULUS 2, REMAINDER 0)")
	sub2("CREATE TABLE tab1_part2 PARTITION OF tab1 FOR VALUES WITH (MODULUS 2, REMAINDER 1)")
	sub2("CREATE TABLE tab2 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab2', b text)")
	sub2("CREATE TABLE tab3 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab3', b text)")
	sub2("CREATE TABLE tab3_1 (a int PRIMARY KEY, c text DEFAULT 'sub2_tab3_1', b text)")

	// Note: We create two separate tables, not a partitioned one, so that we can
	// easily identity through which relation were the changes replicated.
	sub2("CREATE TABLE tab4 (a int PRIMARY KEY)")
	sub2("CREATE TABLE tab4_1 (a int PRIMARY KEY)")
	// Since we specified publish_via_partition_root in pub_all and
	// pub_lower_level, all partition tables use their root tables' identity and
	// schema. We set the list of publications so that the FOR ALL TABLES
	// publication is second (the list order matters).
	sub2("ALTER SUBSCRIPTION sub2 SET PUBLICATION pub_lower_level, pub_all")

	// Wait for initial sync of all subscriptions
	waitForSubscriberSync(t, nodeSubscriber1)
	waitForSubscriberSync(t, nodeSubscriber2)

	// TEST: initial data synced via root identity
	result = sub1("SELECT c, a FROM tab2")
	is(result, "sub1_tab2|1", "initial data synced for pub_viaroot")
	result = sub2("SELECT a FROM tab4 ORDER BY 1")
	is(result, "-1", "initial data synced for pub_lower_level and pub_all")
	result = sub2("SELECT a FROM tab4_1 ORDER BY 1")
	is(result, "", "initial data synced for pub_lower_level and pub_all")

	// TEST: insert via root identity
	pub("INSERT INTO tab1 VALUES (1), (0)")
	pub("INSERT INTO tab1_1 (a) VALUES (3)")
	pub("INSERT INTO tab1_2 VALUES (5)")
	pub("INSERT INTO tab2 VALUES (0), (3), (5)")
	pub("INSERT INTO tab3 VALUES (1), (0), (3), (5)")

	// Insert a row into the leaf partition, should be replicated through the
	// partition root (thanks to the FOR ALL TABLES partition).
	pub("INSERT INTO tab4 VALUES (0)")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub1_tab2|0\nsub1_tab2|1\nsub1_tab2|3\nsub1_tab2|5", "inserts into tab2 replicated")

	result = sub1("SELECT c, a FROM tab3_1 ORDER BY 1, 2")
	is(result, "sub1_tab3_1|0\nsub1_tab3_1|1\nsub1_tab3_1|3\nsub1_tab3_1|5", "inserts into tab3_1 replicated")

	result = sub2("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub2_tab1|0\nsub2_tab1|1\nsub2_tab1|3\nsub2_tab1|5", "inserts into tab1 replicated")

	result = sub2("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub2_tab2|0\nsub2_tab2|1\nsub2_tab2|3\nsub2_tab2|5", "inserts into tab2 replicated")

	result = sub2("SELECT c, a FROM tab3 ORDER BY 1, 2")
	is(result, "sub2_tab3|0\nsub2_tab3|1\nsub2_tab3|3\nsub2_tab3|5", "inserts into tab3 replicated")

	// tab4 change should be replicated through the root partition, which
	// maps to the tab4 relation on subscriber.
	result = sub2("SELECT a FROM tab4 ORDER BY 1")
	is(result, "-1\n0", "inserts into tab4 replicated")

	result = sub2("SELECT a FROM tab4_1 ORDER BY 1")
	is(result, "", "inserts into tab4_1 replicated")

	// TEST: switch the order of publications in the list; same result
	// (no dependence on order of publications)
	sub2("ALTER SUBSCRIPTION sub2 SET PUBLICATION pub_all, pub_lower_level")

	// make sure the subscription on the second subscriber is synced, before
	// continuing
	waitForSubscriberSync(t, nodeSubscriber2)

	// Insert a change into the leaf partition, should be replicated through
	// the partition root (thanks to the FOR ALL TABLES partition).
	pub("INSERT INTO tab4 VALUES (1)")

	catchup("sub2")

	// tab4 change should be replicated through the root partition, which
	// maps to the tab4 relation on subscriber.
	result = sub2("SELECT a FROM tab4 ORDER BY 1")
	is(result, "-1\n0\n1", "inserts into tab4 replicated")

	result = sub2("SELECT a FROM tab4_1 ORDER BY 1")
	is(result, "", "inserts into tab4_1 replicated")

	// TEST: update (replicated as update) via root identity
	pub("UPDATE tab1 SET a = 6 WHERE a = 5")
	pub("UPDATE tab2 SET a = 6 WHERE a = 5")
	pub("UPDATE tab3 SET a = 6 WHERE a = 5")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub1_tab2|0\nsub1_tab2|1\nsub1_tab2|3\nsub1_tab2|6", "update of tab2 replicated")

	result = sub1("SELECT c, a FROM tab3_1 ORDER BY 1, 2")
	is(result, "sub1_tab3_1|0\nsub1_tab3_1|1\nsub1_tab3_1|3\nsub1_tab3_1|6", "update of tab3_1 replicated")

	result = sub2("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub2_tab1|0\nsub2_tab1|1\nsub2_tab1|3\nsub2_tab1|6", "inserts into tab1 replicated")

	result = sub2("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub2_tab2|0\nsub2_tab2|1\nsub2_tab2|3\nsub2_tab2|6", "inserts into tab2 replicated")

	result = sub2("SELECT c, a FROM tab3 ORDER BY 1, 2")
	is(result, "sub2_tab3|0\nsub2_tab3|1\nsub2_tab3|3\nsub2_tab3|6", "inserts into tab3 replicated")

	// TEST: update (replicated as delete+insert) via root identity
	pub("UPDATE tab1 SET a = 2 WHERE a = 6")
	pub("UPDATE tab2 SET a = 2 WHERE a = 6")
	pub("UPDATE tab3 SET a = 2 WHERE a = 6")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub1_tab2|0\nsub1_tab2|1\nsub1_tab2|2\nsub1_tab2|3", "update of tab2 replicated")

	result = sub1("SELECT c, a FROM tab3_1 ORDER BY 1, 2")
	is(result, "sub1_tab3_1|0\nsub1_tab3_1|1\nsub1_tab3_1|2\nsub1_tab3_1|3", "update of tab3_1 replicated")

	result = sub2("SELECT c, a FROM tab1 ORDER BY 1, 2")
	is(result, "sub2_tab1|0\nsub2_tab1|1\nsub2_tab1|2\nsub2_tab1|3", "update of tab1 replicated")

	result = sub2("SELECT c, a FROM tab2 ORDER BY 1, 2")
	is(result, "sub2_tab2|0\nsub2_tab2|1\nsub2_tab2|2\nsub2_tab2|3", "update of tab2 replicated")

	result = sub2("SELECT c, a FROM tab3 ORDER BY 1, 2")
	is(result, "sub2_tab3|0\nsub2_tab3|1\nsub2_tab3|2\nsub2_tab3|3", "update of tab3 replicated")

	// TEST: delete via root identity
	pub("DELETE FROM tab1")
	pub("DELETE FROM tab2")
	pub("DELETE FROM tab3")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT a FROM tab2")
	is(result, "", "delete tab2 replicated")

	result = sub2("SELECT a FROM tab1")
	is(result, "", "delete from tab1 replicated")

	result = sub2("SELECT a FROM tab2")
	is(result, "", "delete from tab2 replicated")

	result = sub2("SELECT a FROM tab3")
	is(result, "", "delete from tab3 replicated")

	// TEST: truncate via root identity — leaf truncates NOT replicated, root ones are
	pub("INSERT INTO tab1 VALUES (1), (2), (5)")
	pub("INSERT INTO tab2 VALUES (1), (2), (5)")
	// these will NOT be replicated
	pub("TRUNCATE tab1_2, tab2_1, tab3_1")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT a FROM tab2 ORDER BY 1")
	is(result, "1\n2\n5", "truncate of tab2_1 NOT replicated")

	result = sub2("SELECT a FROM tab1 ORDER BY 1")
	is(result, "1\n2\n5", "truncate of tab1_2 NOT replicated")

	result = sub2("SELECT a FROM tab2 ORDER BY 1")
	is(result, "1\n2\n5", "truncate of tab2_1 NOT replicated")

	pub("TRUNCATE tab1, tab2, tab3")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT a FROM tab2")
	is(result, "", "truncate of tab2 replicated")

	result = sub2("SELECT a FROM tab1")
	is(result, "", "truncate of tab1 replicated")

	result = sub2("SELECT a FROM tab2")
	is(result, "", "truncate of tab2 replicated")

	result = sub2("SELECT a FROM tab3")
	is(result, "", "truncate of tab3 replicated")

	result = sub2("SELECT a FROM tab3_1")
	is(result, "", "truncate of tab3_1 replicated")

	// TEST: the map to convert tuples from leaf partition to the root
	// table is correctly rebuilt when a new column is added
	pub("ALTER TABLE tab2 DROP b, ADD COLUMN c text DEFAULT 'pub_tab2', ADD b text")
	pub("INSERT INTO tab2 (a, b) VALUES (1, 'xxx'), (3, 'yyy'), (5, 'zzz')")
	pub("INSERT INTO tab2 (a, b, c) VALUES (6, 'aaa', 'xxx_c')")

	catchup("sub_viaroot")
	catchup("sub2")

	result = sub1("SELECT c, a, b FROM tab2 ORDER BY 1, 2")
	is(result, "pub_tab2|1|xxx\npub_tab2|3|yyy\npub_tab2|5|zzz\nxxx_c|6|aaa", "inserts into tab2 replicated")

	result = sub2("SELECT c, a, b FROM tab2 ORDER BY 1, 2")
	is(result, "pub_tab2|1|xxx\npub_tab2|3|yyy\npub_tab2|5|zzz\nxxx_c|6|aaa", "inserts into tab2 replicated")

	// TEST: update_missing / delete_missing conflicts via root identity (tab2_1)
	sub1("DELETE FROM tab2")

	// Note that the current location of the log file is not grabbed immediately
	// after reloading the configuration, but after sending one SQL command to
	// the node so as we are sure that the reloading has taken effect.
	logLocation = nodeSubscriber1.LogSize()

	pub("UPDATE tab2 SET b = 'quux' WHERE a = 5")
	pub("DELETE FROM tab2 WHERE a = 1")

	catchup("sub_viaroot")
	catchup("sub2")

	logfile = nodeSubscriber1.LogFrom(logLocation)
	ok(regexp.MustCompile(`conflict detected on relation "public.tab2_1": conflict=update_missing.*\n.*DETAIL:.* Could not find the row to be updated.*\n.*Remote row \(pub_tab2, quux, 5\); replica identity \(a\)=\(5\)`).MatchString(logfile),
		"update target row is missing in tab2_1")
	ok(regexp.MustCompile(`conflict detected on relation "public.tab2_1": conflict=delete_missing.*\n.*DETAIL:.* Could not find the row to be deleted.*\n.*Replica identity \(a\)=\(1\)`).MatchString(logfile),
		"delete target row is missing in tab2_1")

	// TEST: update_origin_differs conflict with track_commit_timestamp on
	// Enable the track_commit_timestamp to detect the conflict when attempting
	// to update a row that was previously modified by a different origin.
	nodeSubscriber1.AppendConf("track_commit_timestamp = on")
	nodeSubscriber1.Restart()

	sub1("INSERT INTO tab2 VALUES (3, 'yyy')")
	pub("UPDATE tab2 SET b = 'quux' WHERE a = 3")

	catchup("sub_viaroot")

	logfile = nodeSubscriber1.LogFrom(logLocation)
	ok(regexp.MustCompile(`conflict detected on relation "public.tab2_1": conflict=update_origin_differs.*\n.*DETAIL:.* Updating the row that was modified locally in transaction [0-9]+ at .*\n.*Existing local row \(yyy, null, 3\); remote row \(pub_tab2, quux, 3\); replica identity \(a\)=\(3\)`).MatchString(logfile),
		"updating a row that was modified by a different origin")

	// The remaining tests no longer test conflict detection.
	nodeSubscriber1.AppendConf("track_commit_timestamp = off")
	nodeSubscriber1.Restart()

	// TEST: replication continues to work correctly after altering the
	// partition of a partitioned target table.

	pub(`
	CREATE TABLE tab5 (a int NOT NULL, b int);
	CREATE UNIQUE INDEX tab5_a_idx ON tab5 (a);
	ALTER TABLE tab5 REPLICA IDENTITY USING INDEX tab5_a_idx;`)

	sub2(`
	CREATE TABLE tab5 (a int NOT NULL, b int, c int) PARTITION BY LIST (a);
	CREATE TABLE tab5_1 PARTITION OF tab5 DEFAULT;
	CREATE UNIQUE INDEX tab5_a_idx ON tab5 (a);
	ALTER TABLE tab5 REPLICA IDENTITY USING INDEX tab5_a_idx;
	ALTER TABLE tab5_1 REPLICA IDENTITY USING INDEX tab5_1_a_idx;`)

	sub2("ALTER SUBSCRIPTION sub2 REFRESH PUBLICATION")

	waitForSubscriberSync(t, nodeSubscriber2)

	// Make partition map cache
	pub("INSERT INTO tab5 VALUES (1, 1)")
	pub("UPDATE tab5 SET a = 2 WHERE a = 1")

	catchup("sub2")

	result = sub2("SELECT a, b FROM tab5 ORDER BY 1")
	is(result, "2|1", "updates of tab5 replicated correctly")

	// TEST: change the column order of partition on subscriber
	sub2(`
	ALTER TABLE tab5 DETACH PARTITION tab5_1;
	ALTER TABLE tab5_1 DROP COLUMN b;
	ALTER TABLE tab5_1 ADD COLUMN b int;
	ALTER TABLE tab5 ATTACH PARTITION tab5_1 DEFAULT`)

	pub("UPDATE tab5 SET a = 3 WHERE a = 2")

	catchup("sub2")

	result = sub2("SELECT a, b, c FROM tab5 ORDER BY 1")
	is(result, "3|1|", "updates of tab5 replicated correctly after altering table on subscriber")

	// TEST: replication into the partitioned target table continues to
	// work correctly when the published table is altered.
	pub(`
	ALTER TABLE tab5 DROP COLUMN b, ADD COLUMN c INT;
	ALTER TABLE tab5 ADD COLUMN b INT;`)

	pub("UPDATE tab5 SET c = 1 WHERE a = 3")

	catchup("sub2")

	result = sub2("SELECT a, b, c FROM tab5 ORDER BY 1")
	is(result, "3||1", "updates of tab5 replicated correctly after altering table on publisher")

	// TEST: replication works as long as the leaf partition has the necessary
	// REPLICA IDENTITY, even though the actual target partitioned table does not.
	sub2("ALTER TABLE tab5 REPLICA IDENTITY NOTHING")

	pub("UPDATE tab5 SET a = 4 WHERE a = 3")

	catchup("sub2")

	result = sub2("SELECT a, b, c FROM tab5_1 ORDER BY 1")
	is(result, "4||1", "updates of tab5 replicated correctly")

	nodeSubscriber2.Stop(tapnode.StopFast)
	nodeSubscriber1.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
