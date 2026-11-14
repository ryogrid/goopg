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
		"ANALYZE prt1",
		"CREATE TABLE prt2 (a int, b int, c varchar) PARTITION BY RANGE(b)",
		"CREATE TABLE prt2_p1 PARTITION OF prt2 FOR VALUES FROM (0) TO (250)",
		"CREATE TABLE prt2_p2 PARTITION OF prt2 FOR VALUES FROM (250) TO (500)",
		"CREATE TABLE prt2_p3 PARTITION OF prt2 FOR VALUES FROM (500) TO (600)",
		"INSERT INTO prt2 SELECT i % 25, i, to_char(i, 'FM0000') FROM generate_series(0, 599) i WHERE i % 3 = 0",
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
	if got := strings.Join(renderRows(runSQLWith(t, ctx, "SELECT count(*), sum(t1.a), sum(t2.a) FROM prt1 t1, prt2 t2 WHERE t1.a = t2.b", ps)), ";"); got != "100|29700|1200" {
		t.Errorf("partitionwise aggregate %q, want 100|29700|1200 (PG 18.3)", got)
	}
}
