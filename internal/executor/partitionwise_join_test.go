package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPartitionwiseJoin pins M0148-0001c3a9 slice 3: an inner equi-join of
// two partitioned tables with equal bounds on their partition keys plans as
// an Append of per-partition joins (try_partitionwise_join), the children
// named `t1_1`, `t2_1`, … after their parents, and returns the same rows as
// the whole-rel join. The setup and plans are PG 18.3's partition_join
// witness (prt1 / prt2); enable_partitionwise_join off keeps the old plan.
func TestPartitionwiseJoin(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"CREATE TABLE prt1 (a int, b int, c varchar) PARTITION BY RANGE(a)",
		"CREATE TABLE prt1_p1 PARTITION OF prt1 FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt1_p3 PARTITION OF prt1 FOR VALUES FROM (500) TO (600)",
		"CREATE TABLE prt1_p2 PARTITION OF prt1 FOR VALUES FROM (250) TO (500)",
		"INSERT INTO prt1 SELECT i, i % 25, to_char(i, 'FM0000') FROM generate_series(0, 599) i WHERE i % 2 = 0",
		"CREATE INDEX iprt1_p1_a on prt1_p1(a)",
		"CREATE INDEX iprt1_p2_a on prt1_p2(a)",
		"CREATE INDEX iprt1_p3_a on prt1_p3(a)",
		"ANALYZE prt1",
		"CREATE TABLE prt2 (a int, b int, c varchar) PARTITION BY RANGE(b)",
		"CREATE TABLE prt2_p1 PARTITION OF prt2 FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt2_p2 PARTITION OF prt2 FOR VALUES FROM (250) TO (500)",
		"CREATE TABLE prt2_p3 PARTITION OF prt2 FOR VALUES FROM (500) TO (600)",
		"INSERT INTO prt2 SELECT i % 25, i, to_char(i, 'FM0000') FROM generate_series(0, 599) i WHERE i % 3 = 0",
		"CREATE INDEX iprt2_p1_b on prt2_p1(b)",
		"CREATE INDEX iprt2_p2_b on prt2_p2(b)",
		"CREATE INDEX iprt2_p3_b on prt2_p3(b)",
		"ANALYZE prt2",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	q := "SELECT count(*) FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b"
	want := "Aggregate\n  ->  Append\n" +
		"        ->  Hash Join\n              Hash Cond: (t1_1.a = t2_1.b)\n              ->  Seq Scan on prt1_p1 t1_1\n              ->  Hash\n                    ->  Seq Scan on prt2_p1 t2_1\n" +
		"        ->  Hash Join\n              Hash Cond: (t1_2.a = t2_2.b)\n              ->  Seq Scan on prt1_p2 t1_2\n              ->  Hash\n                    ->  Seq Scan on prt2_p2 t2_2\n" +
		"        ->  Hash Join\n              Hash Cond: (t1_3.a = t2_3.b)\n              ->  Seq Scan on prt1_p3 t1_3\n              ->  Hash\n                    ->  Seq Scan on prt2_p3 t2_3"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)), "\n"); got != want {
		t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}
	off := ps
	off.EnablePartitionwiseJoin = false
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+q, off)), "\n"); strings.Contains(got, "Append\n        ->  Hash Join") {
		t.Errorf("enable_partitionwise_join off still joined partitionwise:\n%s", got)
	}
	// Same rows either way, including a filtered side and an ORDER BY on
	// the inner relation's columns (the child joins' column order differs
	// from the whole-rel join's).
	for _, rq := range []string{
		"SELECT count(*), sum(t1.a), sum(t2.a) FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b",
		"SELECT t2.c, t1.c FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b AND t1.b = 0 ORDER BY t2.a, t1.c DESC",
	} {
		on := strings.Join(renderRows(runSQLWith(t, ctx, rq, ps)), ";")
		offRows := strings.Join(renderRows(runSQLWith(t, ctx, rq, off)), ";")
		if on != offRows || on == "" {
			t.Errorf("%s: partitionwise rows %q, whole-rel rows %q", rq, on, offRows)
		}
	}
	// Child joins that publish different column subsets (a Nested Loop
	// beside Hash Joins, narrowed differently) must still feed the Append
	// one row shape: the slice-3 build returned `0|0300|0|0300` here (PG
	// partition_join, "Join with pruned partitions from joining relations").
	pq := "SELECT t1.a, t1.c, t2.b, t2.c FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b AND t1.a < 450 AND t2.b > 250 AND t1.b = 0 ORDER BY t1.a, t2.b"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, pq, ps)), ";"); got != "300|0300|300|0300" {
		t.Errorf("%s: rows %q, want 300|0300|300|0300 (PG 18.3)", pq, got)
	}
	// Slice 4: a child rel is the partition's own scan, so it gets the
	// partition's index paths — here the parameterised probe a nested loop
	// binds per partition (PG plans the same probes; PG also prunes the
	// partitions `t1.a < 10` excludes, which goopg does not yet).
	sq := "SELECT t1.a, t2.c FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b AND t1.a < 10"
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+sq, ps)), "\n"); !strings.Contains(got, "Index Scan using iprt2_p1_b on prt2_p1 t2_1\n              Index Cond: (b = t1_1.a)") {
		t.Errorf("%s: no per-partition parameterised index probe:\n%s", sq, got)
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, sq+" ORDER BY 1", ps)), ";"); got != "0|0000;6|0006" {
		t.Errorf("%s: rows %q, want 0|0000;6|0006 (PG 18.3)", sq, got)
	}
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT count(*), sum(t1.a), sum(t2.a) FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b", ps)), ";"); got != "100|29700|1200" {
		t.Errorf("partitionwise aggregate %q, want 100|29700|1200 (PG 18.3)", got)
	}
}

// TestAppendRelChildIndexPaths pins M0148-0001c3a10 slice 1: a partitioned
// table read alone with a selective WHERE plans each partition as its own
// child rel, so the partitions' indexes serve the restriction
// (set_append_rel_pathlist). Plans and values are PG 18.3's.
func TestAppendRelChildIndexPaths(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table pa (a int, b text, c int) partition by range (a)",
		"create table pa_1 partition of pa for values from (0) to (5000)",
		"create table pa_2 partition of pa for values from (5000) to (10000)",
		"insert into pa select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"create index on pa (b)",
		"create index on pa (c, a)",
		"analyze pa",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	bitmap := "->  Bitmap Heap Scan on pa_1\n        Recheck Cond: (b = 'v12'::text)\n        ->  Bitmap Index Scan on pa_1_b_idx\n              Index Cond: (b = 'v12'::text)\n" +
		"  ->  Bitmap Heap Scan on pa_2\n        Recheck Cond: (b = 'v12'::text)\n        ->  Bitmap Index Scan on pa_2_b_idx\n              Index Cond: (b = 'v12'::text)"
	for _, c := range []struct{ q, plan string }{
		{"select * from pa where b = 'v12'", "Append\n  " + bitmap},
		{"select * from pa where b = 'v12' order by a limit 3",
			"Limit\n  ->  Sort\n        Sort Key: pa.a\n        ->  Append\n              " + strings.ReplaceAll(bitmap, "\n", "\n            ")},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select count(*), sum(a) from pa where b = 'v12'", "10|45120"},
		{"select a, b, tableoid::regclass from pa where b = 'v12' order by a limit 3", "12|v12|pa_1;1012|v12|pa_1;2012|v12|pa_1"},
		{"select max(a) from pa where c = 3", "9999"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q (PG 18.3)", c.q, got, c.rows)
		}
	}
}

// TestIndexOnlyEqualityPrefixOrdering pins M0148-0001c3a10 slice 2: an
// index-only probe whose leading column is equated to a constant delivers
// the next column in order, so `WHERE c = 3 ORDER BY a [DESC] LIMIT 1` over
// an (c, a) index is a bare Index Only Scan [Backward] (build_index_paths'
// useful_pathkeys do not depend on the clause kinds). Plans are PG 18.3's.
func TestIndexOnlyEqualityPrefixOrdering(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table pb (a int NOT NULL, b text, c int NOT NULL)",
		"create index pb_c_a_idx on pb (c, a)",
		"insert into pb select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"vacuum pb",
		"analyze pb",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select a from pb where c = 3 order by a limit 1",
			"Limit\n  ->  Index Only Scan using pb_c_a_idx on pb\n        Index Cond: (c = 3)"},
		{"select a from pb where c = 3 order by a desc limit 1",
			"Limit\n  ->  Index Only Scan Backward using pb_c_a_idx on pb\n        Index Cond: (c = 3)"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select a from pb where c = 3 order by a limit 1", "3"},
		{"select a from pb where c = 3 order by a desc limit 1", "9999"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestOrderedAppendRelChildPaths pins M0148-0001c3a10 slice 3: a RANGE
// partitioned table without a default partition, ordered on its partition
// key, plans an Append of per-partition ordered index-only scans in bound
// order — reversed and Backward for DESC — and min/max rides it as PG's
// build_minmax_path does. Plans and values are PG 18.3's.
func TestOrderedAppendRelChildPaths(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table pq (a int NOT NULL, b text, c int NOT NULL) partition by range (a)",
		"create table pq_1 partition of pq for values from (0) to (5000)",
		"create table pq_2 partition of pq for values from (5000) to (10000)",
		"create index on pq (c, a)",
		"insert into pq select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"vacuum pq_1", "vacuum pq_2",
		"analyze pq",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	back := "  ->  Append\n        ->  Index Only Scan Backward using pq_2_c_a_idx on pq_2\n              Index Cond: (c = 3)\n" +
		"        ->  Index Only Scan Backward using pq_1_c_a_idx on pq_1\n              Index Cond: (c = 3)"
	for _, c := range []struct{ q, plan string }{
		{"select a from pq where c = 3 order by a desc limit 1", "Limit\n" + back},
		{"select a from pq where c = 3 order by a limit 2",
			"Limit\n  ->  Append\n        ->  Index Only Scan using pq_1_c_a_idx on pq_1\n              Index Cond: (c = 3)\n" +
				"        ->  Index Only Scan using pq_2_c_a_idx on pq_2\n              Index Cond: (c = 3)"},
		{"select max(a) from pq where c = 3", "Result\n  InitPlan 1\n    ->  Limit\n          ->  Append\n" +
			"                ->  Index Only Scan Backward using pq_2_c_a_idx on pq_2\n                      Index Cond: (c = 3)\n" +
			"                ->  Index Only Scan Backward using pq_1_c_a_idx on pq_1\n                      Index Cond: (c = 3)"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select max(a), min(a) from pq where c = 3", "9999|3"},
		{"select a from pq where c = 3 order by a desc limit 3", "9999;9992;9985"},
		{"select a from pq where c = 3 order by a limit 3", "3;10;17"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestIndexOnlyPrefixNullTest is M0148-0001c3a10 slice 4: build_minmax_path's
// `a IS NOT NULL` over a nullable ordering column behind an equality prefix is
// an index qual of the index-only scan — PG 18.3's `Index Cond: ((c = 3) AND
// (a IS NOT NULL))`, per partition under the ordered Append and on a plain
// table, with the NULL-keyed rows skipped.
func TestIndexOnlyPrefixNullTest(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table pn (a int, b text, c int NOT NULL) partition by range (a)",
		"create table pn_1 partition of pn for values from (0) to (5000)",
		"create table pn_2 partition of pn for values from (5000) to (10000)",
		"create index on pn (c, a)",
		"insert into pn select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"vacuum pn_1", "vacuum pn_2",
		"analyze pn",
		"create table pnb (a int, b text, c int NOT NULL)",
		"create index on pnb (c, a)",
		"insert into pnb select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"insert into pnb values (NULL, 'x', 3), (NULL, 'y', 3), (NULL, 'z', 4)",
		"vacuum pnb",
		"analyze pnb",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select max(a) from pn where c = 3", "Result\n  InitPlan 1\n    ->  Limit\n          ->  Append\n" +
			"                ->  Index Only Scan Backward using pn_2_c_a_idx on pn_2\n                      Index Cond: ((c = 3) AND (a IS NOT NULL))\n" +
			"                ->  Index Only Scan Backward using pn_1_c_a_idx on pn_1\n                      Index Cond: ((c = 3) AND (a IS NOT NULL))"},
		{"select max(a) from pnb where c = 3", "Result\n  InitPlan 1\n    ->  Limit\n" +
			"          ->  Index Only Scan Backward using pnb_c_a_idx on pnb\n                Index Cond: ((c = 3) AND (a IS NOT NULL))"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select max(a) from pn where c = 3", "9999"},
		{"select max(a), min(a) from pnb where c = 3", "9999|3"},
		{"select min(a) from pnb where c = 4", "4"},
		{"select count(*) from pnb where c = 3 and a is not null", "1429"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestMergeAppendRelChildPaths is M0148-0001c3a10 slice 5: when the
// partitions are not ordered on the query's leading key (a default partition,
// LIST partitioning), generate_orderedappend_paths files a Merge Append of the
// children's ordered paths — PG 18.3's plans below, min/max included.
func TestMergeAppendRelChildPaths(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table qd (a int NOT NULL, b text NOT NULL, c int NOT NULL) partition by range (a)",
		"create table qd_1 partition of qd for values from (0) to (5000)",
		"create table qd_2 partition of qd for values from (5000) to (10000)",
		"create table qd_def partition of qd default",
		"create index on qd (c, a)",
		"insert into qd select i, 'v' || (i % 1000), i % 7 from generate_series(0, 11999) i",
		"vacuum qd_1", "vacuum qd_2", "vacuum qd_def",
		"analyze qd",
		"create table ql (a int NOT NULL, b text NOT NULL, c int NOT NULL) partition by list (c)",
		"create table ql_1 partition of ql for values in (0, 1, 2, 3)",
		"create table ql_2 partition of ql for values in (4, 5, 6)",
		"create index on ql (b, a)",
		"insert into ql select i, 'v' || (i % 1000), i % 7 from generate_series(0, 9999) i",
		"vacuum ql_1", "vacuum ql_2",
		"analyze ql",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	qdChildren := func(back string, indent string) string {
		return indent + "->  Index Only Scan" + back + " using qd_1_c_a_idx on qd_1\n" + indent + "      Index Cond: (c = 3)\n" +
			indent + "->  Index Only Scan" + back + " using qd_2_c_a_idx on qd_2\n" + indent + "      Index Cond: (c = 3)\n" +
			indent + "->  Index Only Scan" + back + " using qd_def_c_a_idx on qd_def qd_3\n" + indent + "      Index Cond: (c = 3)"
	}
	for _, c := range []struct{ q, plan string }{
		{"select a from qd where c = 3 order by a limit 2",
			"Limit\n  ->  Merge Append\n        Sort Key: qd.a\n" + qdChildren("", "        ")},
		{"select a from qd where c = 3 order by a desc limit 2",
			"Limit\n  ->  Merge Append\n        Sort Key: qd.a DESC\n" + qdChildren(" Backward", "        ")},
		{"select max(a) from qd where c = 3",
			"Result\n  InitPlan 1\n    ->  Limit\n          ->  Merge Append\n                Sort Key: qd.a DESC\n" + qdChildren(" Backward", "                ")},
		{"select a from ql where b = 'v12' order by a limit 2",
			"Limit\n  ->  Merge Append\n        Sort Key: ql.a\n" +
				"        ->  Index Only Scan using ql_1_b_a_idx on ql_1\n              Index Cond: (b = 'v12'::text)\n" +
				"        ->  Index Only Scan using ql_2_b_a_idx on ql_2\n              Index Cond: (b = 'v12'::text)"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select a from qd where c = 3 order by a limit 2", "3;10"},
		{"select a from qd where c = 3 order by a desc limit 2", "11994;11987"},
		{"select max(a), min(a) from qd where c = 3", "11994|3"},
		{"select a from ql where b = 'v12' order by a limit 2", "12;1012"},
		{"select a from ql where b = 'v12' order by a desc limit 3", "9012;8012;7012"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestUnrestrictedMergeAppendRelChildPaths is M0148-0001c3a10 slice 6: a
// partitioned table with no WHERE but an ORDER BY its partitions are not
// ordered on (here a default partition) gets the Merge Append of the
// children's index paths — PG 18.3's plans below — while an ordering no child
// index delivers keeps the Sort over the Append.
func TestUnrestrictedMergeAppendRelChildPaths(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table ue (a int NOT NULL, b text NOT NULL) partition by range (a)",
		"create table ue_1 partition of ue for values from (0) to (5000)",
		"create table ue_2 partition of ue for values from (5000) to (10000)",
		"create table ue_def partition of ue default",
		"create index on ue (a)",
		"insert into ue select i, 'v' || (i % 1000) from generate_series(0, 11999) i",
		"vacuum ue_1", "vacuum ue_2", "vacuum ue_def",
		"analyze ue",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	back := "->  Index Only Scan Backward using ue_1_a_idx on ue_1\n%s->  Index Only Scan Backward using ue_2_a_idx on ue_2\n" +
		"%s->  Index Only Scan Backward using ue_def_a_idx on ue_def ue_3"
	ind := func(n int) string { return strings.Repeat(" ", n) }
	for _, c := range []struct{ q, plan string }{
		{"select * from ue order by a limit 5",
			"Limit\n  ->  Merge Append\n        Sort Key: ue.a\n" +
				"        ->  Index Scan using ue_1_a_idx on ue_1\n        ->  Index Scan using ue_2_a_idx on ue_2\n" +
				"        ->  Index Scan using ue_def_a_idx on ue_def ue_3"},
		{"select a from ue order by a desc limit 3",
			"Limit\n  ->  Merge Append\n        Sort Key: ue.a DESC\n" + ind(8) + fmt.Sprintf(back, ind(8), ind(8))},
		{"select max(a) from ue",
			"Result\n  InitPlan 1\n    ->  Limit\n          ->  Merge Append\n                Sort Key: ue.a DESC\n" + ind(16) + fmt.Sprintf(back, ind(16), ind(16))},
		{"select * from ue order by b limit 5",
			"Limit\n  ->  Sort\n        Sort Key: ue.b\n        ->  Append\n              ->  Seq Scan on ue_1\n" +
				"              ->  Seq Scan on ue_2\n              ->  Seq Scan on ue_def ue_3"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select * from ue order by a limit 3", "0|v0;1|v1;2|v2"},
		{"select a from ue order by a desc limit 3", "11999;11998;11997"},
		{"select max(a), min(a) from ue", "11999|0"},
		{"select count(*), sum(a) from (select a from ue order by a) s", "12000|71994000"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestPartitionedUpsertSingleArbiterEntry: an INSERT … ON CONFLICT routed to a
// partition files the row once in the leaf's arbiter index. The non-arbiter
// maintenance skipped the PARENT's arbiter OID, so the leaf's own copy of
// that index got a second entry, and an index scan returned the row twice
// (isolation partition-key-update-2/3 under the slice-6 Merge Append).
func TestPartitionedUpsertSingleArbiterEntry(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table uz (a int primary key, b text) partition by list (a)",
		"create table uz1 partition of uz for values in (1)",
		"create table uz2 partition of uz for values in (2)",
		"insert into uz values (1, 'x') on conflict do nothing",
		"insert into uz values (1, 'y') on conflict do nothing",
		"insert into uz values (2, 'u') on conflict (a) do update set b = 'v'",
		"insert into uz values (2, 'u') on conflict (a) do update set b = 'w'",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSeqScan = false
	for _, c := range []struct{ q, rows string }{
		{"select * from uz1 where a = 1", "1|x"},
		{"select * from uz2 where a = 2", "2|w"},
		{"select * from uz order by a", "1|x;2|w"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestStarMergeAppendIndexOnlyChildren is M0148-0001c3a10 slice 7: `select *`
// voids the statement-wide needed-column set, so the children of an
// interleaved LIST table's Merge Append were Index Scans; the child's own
// scan columns now stand in for the star (childNeededColumns) and PG 18.3's
// Index Only Scans plan — while a statement that also reads `ctid` keeps the
// Index Scans, and the merge carries each row's ctid (it returned NULL).
func TestStarMergeAppendIndexOnlyChildren(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table un (a int NOT NULL) partition by list (a)",
		"create table un_1 partition of un for values in (1)",
		"create table un_35 partition of un for values in (3, 5)",
		"create table un_4 partition of un for values in (4)",
		"create index on un (a)",
		"insert into un select 1 + 2 * (i % 3) from generate_series(1, 3000) i",
		"insert into un select 4 from generate_series(1, 1000) i",
		"vacuum un_1", "vacuum un_35", "vacuum un_4",
		"analyze un",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select * from un order by a",
			"Merge Append\n  Sort Key: un.a\n  ->  Index Only Scan using un_1_a_idx on un_1\n" +
				"  ->  Index Only Scan using un_35_a_idx on un_35 un_2\n  ->  Index Only Scan using un_4_a_idx on un_4 un_3"},
		{"select * from un order by a desc limit 3",
			"Limit\n  ->  Merge Append\n        Sort Key: un.a DESC\n        ->  Index Only Scan Backward using un_1_a_idx on un_1\n" +
				"        ->  Index Only Scan Backward using un_35_a_idx on un_35 un_2\n        ->  Index Only Scan Backward using un_4_a_idx on un_4 un_3"},
		{"select *, ctid from un order by a limit 3",
			"Limit\n  ->  Merge Append\n        Sort Key: un.a\n        ->  Index Scan using un_1_a_idx on un_1\n" +
				"        ->  Index Scan using un_35_a_idx on un_35 un_2\n        ->  Index Scan using un_4_a_idx on un_4 un_3"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select * from un order by a desc limit 2", "5;5"},
		{"select count(*), sum(a) from (select * from un order by a) s", "4000|13000"},
		{"select count(*) from (select a, ctid from un order by a limit 1500) s where ctid is not null", "1500"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestListOrderedAppendRelChildPaths is M0148-0001c3a10 slice 8: a LIST
// table whose partitions do not interleave is partitions_are_ordered
// (partbounds.c), so a restricted ORDER BY on its key plans PG 18.3's plain
// Append in partition order (reversed and Backward for DESC), not a Merge
// Append.
func TestListOrderedAppendRelChildPaths(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table lq (a int NOT NULL, b text NOT NULL) partition by list (a)",
		"create table lq_1 partition of lq for values in (1, 2)",
		"create table lq_2 partition of lq for values in (3)",
		"create table lq_56 partition of lq for values in (5, 6)",
		"create index on lq (a)",
		"insert into lq select 1 + (i % 6), 'v' || i from generate_series(1, 6000) i where 1 + (i % 6) <> 4",
		"vacuum lq_1", "vacuum lq_2", "vacuum lq_56",
		"analyze lq",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	child := func(back, name, rel string) string {
		return "        ->  Index Only Scan" + back + " using " + name + "_a_idx on " + rel + "\n              Index Cond: (a > 1)"
	}
	for _, c := range []struct{ q, plan string }{
		{"select a from lq where a > 1 order by a limit 3", "Limit\n  ->  Append\n" +
			child("", "lq_1", "lq_1") + "\n" + child("", "lq_2", "lq_2") + "\n" + child("", "lq_56", "lq_56 lq_3")},
		{"select a from lq where a > 1 order by a desc limit 3", "Limit\n  ->  Append\n" +
			child(" Backward", "lq_56", "lq_56 lq_3") + "\n" + child(" Backward", "lq_2", "lq_2") + "\n" + child(" Backward", "lq_1", "lq_1")},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select a from lq where a > 1 order by a limit 3", "2;2;2"},
		{"select a from lq where a > 1 order by a desc limit 3", "6;6;6"},
		{"select count(*), sum(a) from (select a from lq where a > 1 order by a) s", "4000|16000"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}

// TestUnrestrictedListOrderedAppend is M0148-0001c3a10 slice 9: an
// unrestricted LIST table whose partitions do not interleave gets PG 18.3's
// plain Append in partition order for its ORDER BY. It became reachable once
// the appendrel's own seq-scan Append was priced as cost_append over its
// members (appendRelSeqScanCost) — on the old sub-plan floor the ordered
// Append also beat the HashAggregate PG keeps over an unordered appendrel.
func TestUnrestrictedListOrderedAppend(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table um (a int NOT NULL) partition by list (a)",
		"create table um_1 partition of um for values in (1)",
		"create table um_2 partition of um for values in (2)",
		"create table um_35 partition of um for values in (3, 5)",
		"create index on um (a)",
		"insert into um select 1 + (i % 2) from generate_series(1, 3000) i",
		"insert into um select 3 + 2 * (i % 2) from generate_series(1, 3000) i",
		"vacuum um_1", "vacuum um_2", "vacuum um_35",
		"analyze um",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"select * from um order by a",
			"Append\n  ->  Index Only Scan using um_1_a_idx on um_1\n  ->  Index Only Scan using um_2_a_idx on um_2\n" +
				"  ->  Index Only Scan using um_35_a_idx on um_35 um_3"},
		{"select * from um order by a desc limit 3",
			"Limit\n  ->  Append\n        ->  Index Only Scan Backward using um_35_a_idx on um_35 um_3\n" +
				"        ->  Index Only Scan Backward using um_2_a_idx on um_2\n        ->  Index Only Scan Backward using um_1_a_idx on um_1"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
	for _, c := range []struct{ q, rows string }{
		{"select * from um order by a desc limit 3", "5;5;5"},
		{"select count(*), sum(a) from (select * from um order by a) s", "6000|16500"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, c.q, ps)), ";"); got != c.rows {
			t.Errorf("%s: rows %q, want %q", c.q, got, c.rows)
		}
	}
}
