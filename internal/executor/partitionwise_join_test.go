package executor

import (
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
