package testport

import (
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Subscription031ColumnList ports
// postgres/src/test/subscription/t/031_column_list.pl to the M0149 contract:
// publication column lists (FOR TABLE t (a, b)) — pg_publication_rel.prattrs,
// applied to the initial table sync COPY and to streamed INSERT / UPDATE /
// DELETE, through replica identity changes, partitions published with and
// without publish_via_partition_root (the top-most published ancestor's list
// wins), FOR ALL TABLES / TABLES IN SCHEMA overriding a list, dropped and
// generated columns, the old-tuple column count in pgoutput, and the
// "different column lists" error at CREATE SUBSCRIPTION and in the
// walsender. Three pairings: all-goopg, a PG 18.3 publisher with a goopg
// subscriber, a goopg publisher with a PG 18.3 subscriber.
func TestPort_Subscription031ColumnList(t *testing.T) {
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
			runSubscriptionColumnList(t, tc.publisher(t, "publisher"), tc.subscriber(t, "subscriber"))
		})
	}
}

func runSubscriptionColumnList(t *testing.T, nodePublisher, nodeSubscriber *tapnode.Node) {
	is := func(got, want, name string) {
		t.Helper()
		if got != want {
			t.Errorf("not ok: %s: got %q, want %q", name, got, want)
		}
	}
	// create publisher node (allows_streaming => 'logical')
	for _, l := range []string{"wal_level = logical", "max_wal_senders = 10", "max_replication_slots = 10"} {
		nodePublisher.AppendConf(l)
	}
	nodePublisher.Start()
	// create subscriber node
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
	pub := func(sql string) string { return nodePublisher.SafePSQL("postgres", sql) }
	sub := func(sql string) string { return nodeSubscriber.SafePSQL("postgres", sql) }
	publisherConnstr := nodePublisher.ConnStr()
	var offset int64
	syncWait := func() { t.Helper(); waitForSubscriberSync(t, nodeSubscriber) }
	catchup := func() { t.Helper(); waitForSubCatchup(t, nodePublisher, "sub1") }

	// setup tables on both nodes

	// tab1: simple 1:1 replication
	pub(`
	CREATE TABLE tab1 (a int PRIMARY KEY, "B" int, c int)
`)
	sub(`
	CREATE TABLE tab1 (a int PRIMARY KEY, "B" int, c int)
`)

	// tab2: replication from regular to table with fewer columns
	pub(`
	CREATE TABLE tab2 (a int PRIMARY KEY, b varchar, c int);
`)
	sub(`
	CREATE TABLE tab2 (a int PRIMARY KEY, b varchar)
`)

	// tab3: simple 1:1 replication with weird column names
	pub(`
	CREATE TABLE tab3 ("a'" int PRIMARY KEY, "B" varchar, "c'" int)
`)
	sub(`
	CREATE TABLE tab3 ("a'" int PRIMARY KEY, "c'" int)
`)

	// test_part: partitioned tables, multi-level, fewer columns on the subscriber
	pub(`
	CREATE TABLE test_part (a int PRIMARY KEY, b text, c timestamptz) PARTITION BY LIST (a);
	CREATE TABLE test_part_1_1 PARTITION OF test_part FOR VALUES IN (1,2,3,4,5,6);
	CREATE TABLE test_part_2_1 PARTITION OF test_part FOR VALUES IN (7,8,9,10,11,12) PARTITION BY LIST (a);
	CREATE TABLE test_part_2_2 PARTITION OF test_part_2_1 FOR VALUES IN (7,8,9,10);
`)
	sub(`
	CREATE TABLE test_part (a int PRIMARY KEY, b text) PARTITION BY LIST (a);
	CREATE TABLE test_part_1_1 PARTITION OF test_part FOR VALUES IN (1,2,3,4,5,6);
	CREATE TABLE test_part_2_1 PARTITION OF test_part FOR VALUES IN (7,8,9,10,11,12) PARTITION BY LIST (a);
	CREATE TABLE test_part_2_2 PARTITION OF test_part_2_1 FOR VALUES IN (7,8,9,10);
`)

	// tab4: table with user-defined enum types
	pub(`
	CREATE TYPE test_typ AS ENUM ('blue', 'red');
	CREATE TABLE tab4 (a INT PRIMARY KEY, b test_typ, c int, d text);
`)
	sub(`
	CREATE TYPE test_typ AS ENUM ('blue', 'red');
	CREATE TABLE tab4 (a INT PRIMARY KEY, b test_typ, d text);
`)

	// TEST: create publication and subscription for some of the tables with column lists
	pub(`
	CREATE PUBLICATION pub1
	   FOR TABLE tab1 (a, "B"), tab3 ("a'", "c'"), test_part (a, b), tab4 (a, b, d)
	  WITH (publish_via_partition_root = 'true');
`)

	// check prattrs in pg_publication_rel (ordered by relname)
	result := pub(`
	SELECT relname, prattrs
	FROM pg_publication_rel pb JOIN pg_class pc ON(pb.prrelid = pc.oid)
	ORDER BY relname
`)
	is(result, "tab1|1 2\ntab3|1 3\ntab4|1 2 4\ntest_part|1 2", "publication relation updated")

	// TEST: insert data, create subscription and see if sync replicates the right columns
	pub(`
	INSERT INTO tab1 VALUES (1, 2, 3);
	INSERT INTO tab1 VALUES (4, 5, 6);
`)
	pub(`
	INSERT INTO tab3 VALUES (1, 2, 3);
	INSERT INTO tab3 VALUES (4, 5, 6);
`)
	pub(`
	INSERT INTO tab4 VALUES (1, 'red', 3, 'oh my');
	INSERT INTO tab4 VALUES (2, 'blue', 4, 'hello');
`)
	// replication of partitioned table
	pub(`
	INSERT INTO test_part VALUES (1, 'abc', '2021-07-04 12:00:00');
	INSERT INTO test_part VALUES (2, 'bcd', '2021-07-03 11:12:13');
	INSERT INTO test_part VALUES (7, 'abc', '2021-07-04 12:00:00');
	INSERT INTO test_part VALUES (8, 'bcd', '2021-07-03 11:12:13');
`)

	// create subscription, wait for sync, check the sync results
	sub(`
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub1
`)
	syncWait()

	// tab1: only (a,b) is replicated
	result = sub("SELECT * FROM tab1 ORDER BY a")
	is(result, "1|2|\n4|5|", "insert on column tab1.c is not replicated")
	// tab3: only (a,c) is replicated
	result = sub(`SELECT * FROM tab3 ORDER BY "a'"`)
	is(result, "1|3\n4|6", "insert on column tab3.b is not replicated")
	// tab4: only (a,b,d) is replicated
	result = sub("SELECT * FROM tab4 ORDER BY a")
	is(result, "1|red|oh my\n2|blue|hello", "insert on column tab4.c is not replicated")
	// test_part: (a,b) is replicated
	result = sub("SELECT * FROM test_part ORDER BY a")
	is(result, "1|abc\n2|bcd\n7|abc\n8|bcd", "insert on column test_part.c columns is not replicated")

	// TEST: insert more data, replicated by regular decoding (not tablesync)
	pub(`
	INSERT INTO tab1 VALUES (2, 3, 4);
	INSERT INTO tab1 VALUES (5, 6, 7);
`)
	pub(`
	INSERT INTO tab3 VALUES (2, 3, 4);
	INSERT INTO tab3 VALUES (5, 6, 7);
`)
	pub(`
	INSERT INTO tab4 VALUES (3, 'red', 5, 'foo');
	INSERT INTO tab4 VALUES (4, 'blue', 6, 'bar');
`)
	// replication of partitioned table
	pub(`
	INSERT INTO test_part VALUES (3, 'xxx', '2022-02-01 10:00:00');
	INSERT INTO test_part VALUES (4, 'yyy', '2022-03-02 15:12:13');
	INSERT INTO test_part VALUES (9, 'zzz', '2022-04-03 21:00:00');
	INSERT INTO test_part VALUES (10, 'qqq', '2022-05-04 22:12:13');
`)

	// wait for catchup before checking the subscriber
	catchup()

	result = sub("SELECT * FROM tab1 ORDER BY a")
	is(result, "1|2|\n2|3|\n4|5|\n5|6|", "insert on column tab1.c is not replicated")
	result = sub(`SELECT * FROM tab3 ORDER BY "a'"`)
	is(result, "1|3\n2|4\n4|6\n5|7", "insert on column tab3.b is not replicated")
	result = sub("SELECT * FROM tab4 ORDER BY a")
	is(result, "1|red|oh my\n2|blue|hello\n3|red|foo\n4|blue|bar", "insert on column tab4.c is not replicated")
	result = sub("SELECT * FROM test_part ORDER BY a")
	is(result, "1|abc\n2|bcd\n3|xxx\n4|yyy\n7|abc\n8|bcd\n9|zzz\n10|qqq", "insert on column test_part.c columns is not replicated")

	// TEST: updates on columns both inside and outside the column list
	// tab1: update of replicated column
	pub(`UPDATE tab1 SET "B" = 2 * "B" where a = 1`)
	// tab1: update of non-replicated column
	pub(`UPDATE tab1 SET c = 2*c where a = 4`)
	// tab3: update of non-replicated
	pub(`UPDATE tab3 SET "B" = "B" || ' updated' where "a'" = 4`)
	// tab3: update of replicated column
	pub(`UPDATE tab3 SET "c'" = 2 * "c'" where "a'" = 1`)
	// tab4
	pub(`UPDATE tab4 SET b = 'blue', c = c * 2, d = d || ' updated' where a = 1`)
	// tab4
	pub(`UPDATE tab4 SET b = 'red', c = c * 2, d = d || ' updated' where a = 2`)

	// wait for catchup, check the UPDATE results with the right column list
	catchup()

	result = sub(`SELECT * FROM tab1 ORDER BY a`)
	is(result, "1|4|\n2|3|\n4|5|\n5|6|", "only update on column tab1.b is replicated")
	result = sub(`SELECT * FROM tab3 ORDER BY "a'"`)
	is(result, "1|6\n2|4\n4|6\n5|7", "only update on column tab3.c is replicated")
	result = sub(`SELECT * FROM tab4 ORDER BY a`)
	is(result, "1|blue|oh my updated\n2|red|hello updated\n3|red|foo\n4|blue|bar", "update on column tab4.c is not replicated")

	// TEST: add table with a column list, insert data, replicate
	// insert some data before adding it to the publication
	pub(`
	INSERT INTO tab2 VALUES (1, 'abc', 3);
`)
	pub("ALTER PUBLICATION pub1 ADD TABLE tab2 (a, b)")
	sub("ALTER SUBSCRIPTION sub1 REFRESH PUBLICATION")

	// wait for the tablesync, add a bit more data, check the results
	syncWait()

	pub(`
	INSERT INTO tab2 VALUES (2, 'def', 6);
`)
	catchup()

	result = sub("SELECT * FROM tab2 ORDER BY a")
	is(result, "1|abc\n2|def", "insert on column tab2.c is not replicated")

	// do a couple updates, check the correct stuff gets replicated
	pub(`
	UPDATE tab2 SET c = 5 where a = 1;
	UPDATE tab2 SET b = 'xyz' where a = 2;
`)
	catchup()

	result = sub("SELECT * FROM tab2 ORDER BY a")
	is(result, "1|abc\n2|xyz", "update on column tab2.c is not replicated")

	// TEST: table in two publications with same column lists, one subscription for both
	pub(`
	CREATE TABLE tab5 (a int PRIMARY KEY, b int, c int, d int);
	CREATE PUBLICATION pub2 FOR TABLE tab5 (a, b);
	CREATE PUBLICATION pub3 FOR TABLE tab5 (a, b);

	-- insert a couple initial records
	INSERT INTO tab5 VALUES (1, 11, 111, 1111);
	INSERT INTO tab5 VALUES (2, 22, 222, 2222);
`)
	sub(`
	CREATE TABLE tab5 (a int PRIMARY KEY, b int, d int);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub2, pub3
`)
	waitForSubscriptionSync(t, nodeSubscriber, nodePublisher, "sub1")

	// insert data and make sure the columns in column list get fully replicated
	pub(`
	INSERT INTO tab5 VALUES (3, 33, 333, 3333);
	INSERT INTO tab5 VALUES (4, 44, 444, 4444);
`)
	catchup()

	is(sub("SELECT * FROM tab5 ORDER BY a"), "1|11|\n2|22|\n3|33|\n4|44|", "overlapping publications with overlapping column lists")

	// TEST: change the replica identity by replacing the primary key (column still in the list)
	pub(`
	CREATE TABLE tab6 (a int PRIMARY KEY, b int, c int, d int);
	CREATE PUBLICATION pub4 FOR TABLE tab6 (a, b);

	-- initial data
	INSERT INTO tab6 VALUES (1, 22, 333, 4444);
`)
	sub(`
	CREATE TABLE tab6 (a int PRIMARY KEY, b int, c int, d int);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub4
`)
	syncWait()

	pub(`
	INSERT INTO tab6 VALUES (2, 33, 444, 5555);
	UPDATE tab6 SET b = b * 2, c = c * 3, d = d * 4;
`)
	catchup()

	is(sub("SELECT * FROM tab6 ORDER BY a"), "1|44||\n2|66||", "replication with the original primary key")

	// now move the primary key to a different column (still covered by the column list)
	pub(`
	ALTER TABLE tab6 DROP CONSTRAINT tab6_pkey;
	ALTER TABLE tab6 ADD PRIMARY KEY (b);
`)
	// we need to do the same thing on the subscriber
	sub(`
	ALTER TABLE tab6 DROP CONSTRAINT tab6_pkey;
	ALTER TABLE tab6 ADD PRIMARY KEY (b);
`)
	sub(`
	ALTER SUBSCRIPTION sub1 REFRESH PUBLICATION
`)
	syncWait()

	pub(`
	INSERT INTO tab6 VALUES (3, 55, 666, 8888);
	UPDATE tab6 SET b = b * 2, c = c * 3, d = d * 4;
`)
	catchup()

	is(sub("SELECT * FROM tab6 ORDER BY a"), "1|88||\n2|132||\n3|110||", "replication with the modified primary key")

	// TEST: replace the primary key with a multi-column key (all covered by the column list)
	pub(`
	CREATE TABLE tab7 (a int PRIMARY KEY, b int, c int, d int);
	CREATE PUBLICATION pub5 FOR TABLE tab7 (a, b);

	-- some initial data
	INSERT INTO tab7 VALUES (1, 22, 333, 4444);
`)
	sub(`
	CREATE TABLE tab7 (a int PRIMARY KEY, b int, c int, d int);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub5
`)
	syncWait()

	pub(`
	INSERT INTO tab7 VALUES (2, 33, 444, 5555);
	UPDATE tab7 SET b = b * 2, c = c * 3, d = d * 4;
`)
	catchup()

	is(sub("SELECT * FROM tab7 ORDER BY a"), "1|44||\n2|66||", "replication with the original primary key")

	// now move the primary key to (a, b)
	pub(`
	ALTER TABLE tab7 DROP CONSTRAINT tab7_pkey;
	ALTER TABLE tab7 ADD PRIMARY KEY (a, b);
`)
	pub(`
	INSERT INTO tab7 VALUES (3, 55, 666, 7777);
	UPDATE tab7 SET b = b * 2, c = c * 3, d = d * 4;
`)
	catchup()

	is(sub("SELECT * FROM tab7 ORDER BY a"), "1|88||\n2|132||\n3|110||", "replication with the modified primary key")

	// switch the primary key again, with writes between the drop and the new constraint
	pub(`
	ALTER TABLE tab7 DROP CONSTRAINT tab7_pkey;
	INSERT INTO tab7 VALUES (4, 77, 888, 9999);
	-- update/delete is not allowed for tables without RI
	ALTER TABLE tab7 ADD PRIMARY KEY (b, a);
	UPDATE tab7 SET b = b * 2, c = c * 3, d = d * 4;
	DELETE FROM tab7 WHERE a = 1;
`)
	pub(`
`)
	catchup()

	is(sub("SELECT * FROM tab7 ORDER BY a"), "2|264||\n3|220||\n4|154||", "replication with the modified primary key")

	// TEST: partitioned tables (publish_via_partition_root = false) and per-partition replica identity
	// partitions with different RI, column list not covering all of them
	pub(`
	CREATE TABLE test_part_a (a int, b int, c int) PARTITION BY LIST (a);

	CREATE TABLE test_part_a_1 PARTITION OF test_part_a FOR VALUES IN (1,2,3,4,5);
	ALTER TABLE test_part_a_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_a_1 REPLICA IDENTITY USING INDEX test_part_a_1_pkey;

	CREATE TABLE test_part_a_2 PARTITION OF test_part_a FOR VALUES IN (6,7,8,9,10);
	ALTER TABLE test_part_a_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_a_2 REPLICA IDENTITY USING INDEX test_part_a_2_pkey;

	-- initial data, one row in each partition
	INSERT INTO test_part_a VALUES (1, 3);
	INSERT INTO test_part_a VALUES (6, 4);
`)
	// do the same thing on the subscriber (with the opposite column order)
	sub(`
	CREATE TABLE test_part_a (b int, a int) PARTITION BY LIST (a);

	CREATE TABLE test_part_a_1 PARTITION OF test_part_a FOR VALUES IN (1,2,3,4,5);
	ALTER TABLE test_part_a_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_a_1 REPLICA IDENTITY USING INDEX test_part_a_1_pkey;

	CREATE TABLE test_part_a_2 PARTITION OF test_part_a FOR VALUES IN (6,7,8,9,10);
	ALTER TABLE test_part_a_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_a_2 REPLICA IDENTITY USING INDEX test_part_a_2_pkey;
`)
	// a publication replicating just column "a", not enough for the second partition
	pub(`
	CREATE PUBLICATION pub6 FOR TABLE test_part_a (b, a) WITH (publish_via_partition_root = true);
	ALTER PUBLICATION pub6 ADD TABLE test_part_a_1 (a);
	ALTER PUBLICATION pub6 ADD TABLE test_part_a_2 (b);
`)
	// create the subscription, wait for sync
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub6
`)
	syncWait()

	pub(`
	INSERT INTO test_part_a VALUES (2, 5);
	INSERT INTO test_part_a VALUES (7, 6);
`)
	catchup()

	is(sub("SELECT a, b FROM test_part_a ORDER BY a, b"), "1|3\n2|5\n6|4\n7|6", "partitions with different replica identities not replicated correctly")

	// column list covering RI for all partitions
	pub(`
	CREATE TABLE test_part_b (a int, b int) PARTITION BY LIST (a);

	CREATE TABLE test_part_b_1 PARTITION OF test_part_b FOR VALUES IN (1,2,3,4,5);
	ALTER TABLE test_part_b_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_b_1 REPLICA IDENTITY USING INDEX test_part_b_1_pkey;

	CREATE TABLE test_part_b_2 PARTITION OF test_part_b FOR VALUES IN (6,7,8,9,10);
	ALTER TABLE test_part_b_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_b_2 REPLICA IDENTITY USING INDEX test_part_b_2_pkey;

	-- initial data, one row in each partitions
	INSERT INTO test_part_b VALUES (1, 1);
	INSERT INTO test_part_b VALUES (6, 2);
`)
	// do the same thing on the subscriber
	sub(`
	CREATE TABLE test_part_b (a int, b int) PARTITION BY LIST (a);

	CREATE TABLE test_part_b_1 PARTITION OF test_part_b FOR VALUES IN (1,2,3,4,5);
	ALTER TABLE test_part_b_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_b_1 REPLICA IDENTITY USING INDEX test_part_b_1_pkey;

	CREATE TABLE test_part_b_2 PARTITION OF test_part_b FOR VALUES IN (6,7,8,9,10);
	ALTER TABLE test_part_b_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_b_2 REPLICA IDENTITY USING INDEX test_part_b_2_pkey;
`)
	// a publication replicating both columns, sufficient for both partitions
	pub(`
	CREATE PUBLICATION pub7 FOR TABLE test_part_b (a, b) WITH (publish_via_partition_root = true);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub7
`)
	syncWait()

	pub(`
	INSERT INTO test_part_b VALUES (2, 3);
	INSERT INTO test_part_b VALUES (7, 4);
`)
	catchup()

	is(sub("SELECT * FROM test_part_b ORDER BY a, b"), "1|1\n2|3\n6|2\n7|4", "partitions with different replica identities not replicated correctly")

	// TEST: column list covering RI for all partitions, then a partition's RI not covered anymore
	pub(`
	CREATE TABLE test_part_c (a int, b int, c int) PARTITION BY LIST (a);

	CREATE TABLE test_part_c_1 PARTITION OF test_part_c FOR VALUES IN (1,3);
	ALTER TABLE test_part_c_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_c_1 REPLICA IDENTITY USING INDEX test_part_c_1_pkey;

	CREATE TABLE test_part_c_2 PARTITION OF test_part_c FOR VALUES IN (2,4);
	ALTER TABLE test_part_c_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_c_2 REPLICA IDENTITY USING INDEX test_part_c_2_pkey;

	-- initial data, one row for each partition
	INSERT INTO test_part_c VALUES (1, 3, 5);
	INSERT INTO test_part_c VALUES (2, 4, 6);
`)
	// do the same thing on the subscriber
	sub(`
	CREATE TABLE test_part_c (a int, b int, c int) PARTITION BY LIST (a);

	CREATE TABLE test_part_c_1 PARTITION OF test_part_c FOR VALUES IN (1,3);
	ALTER TABLE test_part_c_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_c_1 REPLICA IDENTITY USING INDEX test_part_c_1_pkey;

	CREATE TABLE test_part_c_2 PARTITION OF test_part_c FOR VALUES IN (2,4);
	ALTER TABLE test_part_c_2 ADD PRIMARY KEY (b);
	ALTER TABLE test_part_c_2 REPLICA IDENTITY USING INDEX test_part_c_2_pkey;
`)
	// publish the root without via-root, partitions added with separate column lists
	pub(`
	CREATE PUBLICATION pub8 FOR TABLE test_part_c WITH (publish_via_partition_root = false);
	ALTER PUBLICATION pub8 ADD TABLE test_part_c_1 (a,c);
	ALTER PUBLICATION pub8 ADD TABLE test_part_c_2 (a,b);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub8;
`)
	syncWait()

	pub(`
	INSERT INTO test_part_c VALUES (3, 7, 8);
	INSERT INTO test_part_c VALUES (4, 9, 10);
`)
	catchup()

	is(sub("SELECT * FROM test_part_c ORDER BY a, b"), "1||5\n2|4|\n3||8\n4|9|", "partitions with different replica identities not replicated correctly")

	// recreate pub8 without a root column list, partitions with separate column lists
	pub(`
	DROP PUBLICATION pub8;
	CREATE PUBLICATION pub8 FOR TABLE test_part_c WITH (publish_via_partition_root = false);
	ALTER PUBLICATION pub8 ADD TABLE test_part_c_1 (a);
	ALTER PUBLICATION pub8 ADD TABLE test_part_c_2 (a,b);
`)
	// add the publication to our subscription, wait for sync to complete
	sub(`
	ALTER SUBSCRIPTION sub1 REFRESH PUBLICATION;
	TRUNCATE test_part_c;
`)
	syncWait()

	pub(`
	TRUNCATE test_part_c;
	INSERT INTO test_part_c VALUES (1, 3, 5);
	INSERT INTO test_part_c VALUES (2, 4, 6);
`)
	catchup()

	is(sub("SELECT * FROM test_part_c ORDER BY a, b"), "1||\n2|4|", "partitions with different replica identities not replicated correctly")

	// TEST: single partition with compatible RI, then attach a partition with incompatible RI
	pub(`
	CREATE TABLE test_part_d (a int, b int) PARTITION BY LIST (a);

	CREATE TABLE test_part_d_1 PARTITION OF test_part_d FOR VALUES IN (1,3);
	ALTER TABLE test_part_d_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_d_1 REPLICA IDENTITY USING INDEX test_part_d_1_pkey;

	INSERT INTO test_part_d VALUES (1, 2);
`)
	// same on the subscriber (both partitions created right away)
	sub(`
	CREATE TABLE test_part_d (a int, b int) PARTITION BY LIST (a);

	CREATE TABLE test_part_d_1 PARTITION OF test_part_d FOR VALUES IN (1,3);
	ALTER TABLE test_part_d_1 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_d_1 REPLICA IDENTITY USING INDEX test_part_d_1_pkey;

	CREATE TABLE test_part_d_2 PARTITION OF test_part_d FOR VALUES IN (2,4);
	ALTER TABLE test_part_d_2 ADD PRIMARY KEY (a);
	ALTER TABLE test_part_d_2 REPLICA IDENTITY USING INDEX test_part_d_2_pkey;
`)
	pub(`
	CREATE PUBLICATION pub9 FOR TABLE test_part_d (a) WITH (publish_via_partition_root = true);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub9
`)
	syncWait()

	pub(`
	INSERT INTO test_part_d VALUES (3, 4);
`)
	catchup()

	is(sub("SELECT * FROM test_part_d ORDER BY a, b"), "1|\n3|", "partitions with different replica identities not replicated correctly")

	// TEST: a table also in a FOR ALL TABLES publication replicates all columns
	// drop unnecessary tables, so as not to interfere with the FOR ALL TABLES
	pub(`
	DROP TABLE tab1, tab2, tab3, tab4, tab5, tab6, tab7,
			   test_part, test_part_a, test_part_b, test_part_c, test_part_d;
`)
	pub(`
	CREATE TABLE test_mix_2 (a int PRIMARY KEY, b int, c int);
	CREATE PUBLICATION pub_mix_3 FOR TABLE test_mix_2 (a, b, c);
	CREATE PUBLICATION pub_mix_4 FOR ALL TABLES;

	-- initial data
	INSERT INTO test_mix_2 VALUES (1, 2, 3);
`)
	sub(`
	CREATE TABLE test_mix_2 (a int PRIMARY KEY, b int, c int);
	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub_mix_3, pub_mix_4;
`)
	syncWait()

	pub(`
	INSERT INTO test_mix_2 VALUES (4, 5, 6);
`)
	catchup()

	is(sub("SELECT * FROM test_mix_2"), "1|2|3\n4|5|6", "all columns should be replicated")

	// TEST: a table also in a FOR TABLES IN SCHEMA publication replicates all columns
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE TABLE test_mix_3 (a int PRIMARY KEY, b int, c int);
`)
	pub(`
	DROP TABLE test_mix_2;
	CREATE TABLE test_mix_3 (a int PRIMARY KEY, b int, c int);
	CREATE PUBLICATION pub_mix_5 FOR TABLE test_mix_3 (a, b, c);
	CREATE PUBLICATION pub_mix_6 FOR TABLES IN SCHEMA public;

	-- initial data
	INSERT INTO test_mix_3 VALUES (1, 2, 3);
`)
	sub(`
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub_mix_5, pub_mix_6;
`)
	syncWait()

	pub(`
	INSERT INTO test_mix_3 VALUES (4, 5, 6);
`)
	catchup()

	is(sub("SELECT * FROM test_mix_3"), "1|2|3\n4|5|6", "all columns should be replicated")

	// TEST: publish_via_partition_root applies only the root's column list (sync and changes)
	sub(`
	DROP SUBSCRIPTION sub1;

	CREATE TABLE test_root (a int PRIMARY KEY, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE test_root_1 PARTITION OF test_root FOR VALUES FROM (1) TO (10);
	CREATE TABLE test_root_2 PARTITION OF test_root FOR VALUES FROM (10) TO (20);
`)
	pub(`
	CREATE TABLE test_root (a int PRIMARY KEY, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE test_root_1 PARTITION OF test_root FOR VALUES FROM (1) TO (10);
	CREATE TABLE test_root_2 PARTITION OF test_root FOR VALUES FROM (10) TO (20);

	CREATE PUBLICATION pub_test_root FOR TABLE test_root (a) WITH (publish_via_partition_root = true);
	CREATE PUBLICATION pub_test_root_1 FOR TABLE test_root_1 (a, b);

	-- initial data
	INSERT INTO test_root VALUES (1, 2, 3);
	INSERT INTO test_root VALUES (10, 20, 30);
`)
	// subscribe to both: only the parent's column list is used for sync and replication
	sub(`
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub_test_root, pub_test_root_1;
`)
	syncWait()

	pub(`
	INSERT INTO test_root VALUES (2, 3, 4);
	INSERT INTO test_root VALUES (11, 21, 31);
`)
	catchup()

	is(sub("SELECT * FROM test_root ORDER BY a, b, c"), "1||\n2||\n10||\n11||", "publication via partition root applies column list")

	// TEST: partition published via a schema (no list) and directly (all columns) — no column list
	pub(`
	DROP PUBLICATION pub1, pub2, pub3, pub4, pub5, pub6, pub7, pub8;

	CREATE SCHEMA s1;
	CREATE TABLE s1.t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF s1.t FOR VALUES FROM (1) TO (10);

	CREATE PUBLICATION pub1 FOR TABLES IN SCHEMA s1;
	CREATE PUBLICATION pub2 FOR TABLE t_1(a, b, c);

	-- initial data
	INSERT INTO s1.t VALUES (1, 2, 3);
`)
	sub(`
	CREATE SCHEMA s1;
	CREATE TABLE s1.t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF s1.t FOR VALUES FROM (1) TO (10);

	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub1, pub2;
`)
	syncWait()

	pub(`
	INSERT INTO s1.t VALUES (4, 5, 6);
`)
	catchup()

	is(sub("SELECT * FROM s1.t ORDER BY a"), "1|2|3\n4|5|6", "two publications, publishing the same relation")

	// resync with the publications in the opposite order; same result
	sub(`
	TRUNCATE s1.t;

	ALTER SUBSCRIPTION sub1 SET PUBLICATION pub2, pub1;
`)
	syncWait()

	pub(`
	INSERT INTO s1.t VALUES (7, 8, 9);
`)
	catchup()

	is(sub("SELECT * FROM s1.t ORDER BY a"), "7|8|9", "two publications, publishing the same relation")

	// TEST: one publication with parent and child — the top-most ancestor's list "a" wins
	pub(`
	DROP SCHEMA s1 CASCADE;
	CREATE TABLE t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF t FOR VALUES FROM (1) TO (10)
		   PARTITION BY RANGE (a);
	CREATE TABLE t_2 PARTITION OF t_1 FOR VALUES FROM (1) TO (10);

	CREATE PUBLICATION pub3 FOR TABLE t_1 (a), t_2
	  WITH (PUBLISH_VIA_PARTITION_ROOT);

	-- initial data
	INSERT INTO t VALUES (1, 2, 3);
`)
	sub(`
	DROP SCHEMA s1 CASCADE;
	CREATE TABLE t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF t FOR VALUES FROM (1) TO (10)
		   PARTITION BY RANGE (a);
	CREATE TABLE t_2 PARTITION OF t_1 FOR VALUES FROM (1) TO (10);

	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub3;
`)
	syncWait()

	pub(`
	INSERT INTO t VALUES (4, 5, 6);
`)
	catchup()

	is(sub("SELECT * FROM t ORDER BY a, b, c"), "1||\n4||", "publication containing both parent and child relation")

	// TEST: same, but both relations have a column list defined
	pub(`
	DROP TABLE t;
	CREATE TABLE t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF t FOR VALUES FROM (1) TO (10)
		   PARTITION BY RANGE (a);
	CREATE TABLE t_2 PARTITION OF t_1 FOR VALUES FROM (1) TO (10);

	CREATE PUBLICATION pub4 FOR TABLE t_1 (a), t_2 (b)
	  WITH (PUBLISH_VIA_PARTITION_ROOT);

	-- initial data
	INSERT INTO t VALUES (1, 2, 3);
`)
	sub(`
	DROP TABLE t;
	CREATE TABLE t (a int, b int, c int) PARTITION BY RANGE (a);
	CREATE TABLE t_1 PARTITION OF t FOR VALUES FROM (1) TO (10)
		   PARTITION BY RANGE (a);
	CREATE TABLE t_2 PARTITION OF t_1 FOR VALUES FROM (1) TO (10);

	DROP SUBSCRIPTION sub1;
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub4;
`)
	syncWait()

	pub(`
	INSERT INTO t VALUES (4, 5, 6);
`)
	catchup()

	is(sub("SELECT * FROM t ORDER BY a, b, c"), "1||\n4||", "publication containing both parent and child relation")

	// TEST: only column-list columns exist in the old tuple of UPDATE and DELETE
	pub(`
	CREATE TABLE test_oldtuple_col (a int PRIMARY KEY, b int, c int);
	CREATE PUBLICATION pub_check_oldtuple FOR TABLE test_oldtuple_col (a, b);
	INSERT INTO test_oldtuple_col VALUES(1, 2, 3);
	SELECT * FROM pg_create_logical_replication_slot('test_slot', 'pgoutput');
	UPDATE test_oldtuple_col SET a = 2;
	DELETE FROM test_oldtuple_col;
`)

	// 7th byte of binary data = number of columns in the old tuple (U=85, D=68)
	result = pub(`
		SELECT substr(data, 7, 2) = int2send(2::smallint)
		FROM pg_logical_slot_peek_binary_changes('test_slot', NULL, NULL,
			'proto_version', '1',
			'publication_names', 'pub_check_oldtuple')
		WHERE get_byte(data, 0) = 85 OR get_byte(data, 0) = 68
`)
	is(result, "t\nt", "check the number of columns in the old tuple")

	// TEST: dropped and (unlisted) generated columns don't count — both publications have the same list
	pub(`
	CREATE TABLE test_mix_4 (a int PRIMARY KEY, b int, c int, d int GENERATED ALWAYS AS (a + 1) STORED, e int GENERATED ALWAYS AS (a + 2) VIRTUAL);
	ALTER TABLE test_mix_4 DROP COLUMN c;

	CREATE PUBLICATION pub_mix_7 FOR TABLE test_mix_4 (a, b);
	CREATE PUBLICATION pub_mix_8 FOR TABLE test_mix_4;

	-- initial data
	INSERT INTO test_mix_4 VALUES (1, 2);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE TABLE test_mix_4 (a int PRIMARY KEY, b int, c int, d int);
`)
	sub(`
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub_mix_7, pub_mix_8;
`)
	syncWait()

	is(sub("SELECT * FROM test_mix_4 ORDER BY a"), "1|2||", "initial synchronization with multiple publications with the same column list")

	pub(`
	INSERT INTO test_mix_4 VALUES (3, 4);
`)
	catchup()

	is(sub("SELECT * FROM test_mix_4 ORDER BY a"), "1|2||\n3|4||", "replication with multiple publications with the same column list")

	// TEST: different column lists in multiple publications error at CREATE SUBSCRIPTION
	pub(`
	CREATE TABLE test_mix_1 (a int PRIMARY KEY, b int, c int);
	CREATE PUBLICATION pub_mix_1 FOR TABLE test_mix_1 (a, b);
	CREATE PUBLICATION pub_mix_2 FOR TABLE test_mix_1 (a, c);
`)
	sub(`
	DROP SUBSCRIPTION sub1;
	CREATE TABLE test_mix_1 (a int PRIMARY KEY, b int, c int);
`)

	_, stderr, _ := nodeSubscriber.PSQLExit("postgres", `
	CREATE SUBSCRIPTION sub1 CONNECTION '`+publisherConnstr+`' PUBLICATION pub_mix_1, pub_mix_2;
`)
	differentLists := regexp.MustCompile(`cannot use different column lists for table "public.test_mix_1" in different publications`)
	if !differentLists.MatchString(stderr) {
		t.Errorf("not ok: different column lists detected: stderr %q", stderr)
	}

	// TEST: column list changed after CREATE SUBSCRIPTION — the walsender reports the error
	pub(`
	ALTER PUBLICATION pub_mix_1 SET TABLE test_mix_1 (a, c);
`)
	sub(`
	CREATE SUBSCRIPTION sub1 CONNECTION '` + publisherConnstr + `' PUBLICATION pub_mix_1, pub_mix_2;
`)
	catchup()

	pub(`
	ALTER PUBLICATION pub_mix_1 SET TABLE test_mix_1 (a, b);
	INSERT INTO test_mix_1 VALUES(1, 1, 1);
`)

	offset = waitForLogOffset(t, nodePublisher, differentLists, offset)
	_ = offset

	nodeSubscriber.Stop(tapnode.StopFast)
	nodePublisher.Stop(tapnode.StopFast)
}
