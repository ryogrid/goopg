package postmaster

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestPartialAggOverPresortedPartialJoin pins M0146-0240: PG's
// create_partial_grouping_paths runs make_ordered_path over the cheapest
// partial path, which adds a Sort only when the path's pathkeys do not
// already contain the group pathkeys. A partial merge join on fa.k = fb.k is
// ordered by that equivalence class's one canonical pathkey, which is also
// the group key fb.k's, so the Partial GroupAggregate reads the join
// directly. goopg compared the join's `fa.k` key with the group key `fb.k`
// syntactically and sorted again (TPC-DS Q77's sr and ws CTEs).
//
// The plan is PG 18.3's EXPLAIN on the same statements, compared with the
// cost groups removed (EXPLAIN's widths are unnarrowed, M0146-0042).
func TestPartialAggOverPresortedPartialJoin(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	for _, stmt := range []string{
		"CREATE TABLE fa (k int, v int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE fb (k int PRIMARY KEY, w int) WITH (autovacuum_enabled = false)",
		"INSERT INTO fa SELECT g % 50, g FROM generate_series(1, 200000) g",
		"INSERT INTO fb SELECT g, g FROM generate_series(0, 49) g",
		"ANALYZE fa",
		"ANALYZE fb",
		"SET enable_hashjoin = off",
		"SET enable_nestloop = off",
		"SET enable_hashagg = off",
		"SET parallel_setup_cost = 0",
		"SET parallel_tuple_cost = 0",
		"SET min_parallel_table_scan_size = 0",
		"SET max_parallel_workers_per_gather = 4",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const q = "SELECT fb.k, sum(fa.v) FROM fa JOIN fb ON fa.k = fb.k GROUP BY fb.k"
	costRe := regexp.MustCompile(`  \(cost=[^)]*\)`)
	rows, err := conn.QueryContext(ctx, "EXPLAIN "+q)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("EXPLAIN scan: %v", err)
		}
		lines = append(lines, costRe.ReplaceAllString(l, ""))
	}
	rows.Close()
	want := costRe.ReplaceAllString(`Finalize GroupAggregate  (cost=5290.38..6295.45 rows=50 width=12)
  Group Key: fb.k
  ->  Gather Merge  (cost=5290.38..6293.95 rows=200 width=12)
        Workers Planned: 4
        ->  Partial GroupAggregate  (cost=5290.32..6291.07 rows=50 width=12)
              Group Key: fb.k
              ->  Merge Join  (cost=5290.32..6040.57 rows=50000 width=8)
                    Merge Cond: (fa.k = fb.k)
                    ->  Sort  (cost=5287.41..5412.41 rows=50000 width=8)
                          Sort Key: fa.k
                          ->  Parallel Seq Scan on fa  (cost=0.00..1385.00 rows=50000 width=8)
                    ->  Sort  (cost=2.91..3.04 rows=50 width=4)
                          Sort Key: fb.k
                          ->  Seq Scan on fb  (cost=0.00..1.50 rows=50 width=4)`, "")
	if got := strings.Join(lines, "\n"); got != want {
		t.Errorf("EXPLAIN %s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}

	// The presorted partial must still group correctly: 50 groups, each the
	// sum of the 4000 values g with g % 50 = k.
	var groups, bad int
	if err := conn.QueryRowContext(ctx,
		"SELECT count(*), count(*) FILTER (WHERE s <> (SELECT sum(g) FROM generate_series(1, 200000) g WHERE g % 50 = x.k)) FROM ("+q+") AS x(k, s)").
		Scan(&groups, &bad); err != nil {
		t.Fatalf("result check: %v", err)
	}
	if groups != 50 || bad != 0 {
		t.Errorf("grouped result: %d groups, %d wrong sums; want 50 groups, 0 wrong", groups, bad)
	}
}

// TestGroupAggOverECOrderedInputKeepsItsOrder pins M0146-0240 slice 2: a
// sorted GroupAggregate whose input is ordered by an equivalence-class member
// of its group key (a merge join on ga.k = gb.k, grouped by gb.k) still emits
// in group-key order. PG's AggPath carries its subpath's canonical pathkeys,
// so the grouped CTE reaches the outer query presorted and the Merge Left
// Join sorts neither side (TPC-DS Q77's ws and wr). goopg compared the input
// key ga.k with the group key gb.k syntactically, claimed no order for the
// aggregate, and sorted y again above it.
func TestGroupAggOverECOrderedInputKeepsItsOrder(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	for _, stmt := range []string{
		"CREATE TABLE ga (k int, v int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE gb (k int PRIMARY KEY, w int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE gc (k int, v int) WITH (autovacuum_enabled = false)",
		"INSERT INTO ga SELECT g % 50, g FROM generate_series(1, 20000) g",
		"INSERT INTO gb SELECT g, g FROM generate_series(0, 49) g",
		"INSERT INTO gc SELECT g % 30, g FROM generate_series(1, 9000) g",
		"ANALYZE ga",
		"ANALYZE gb",
		"ANALYZE gc",
		"SET enable_hashjoin = off",
		"SET enable_nestloop = off",
		"SET enable_hashagg = off",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	const q = `WITH x AS (SELECT gb.k, sum(ga.v) AS s FROM ga JOIN gb ON ga.k = gb.k GROUP BY gb.k),
	y AS (SELECT gb.k, sum(gc.v) AS t FROM gc JOIN gb ON gc.k = gb.k GROUP BY gb.k)
	SELECT x.k, s, t FROM x LEFT JOIN y ON x.k = y.k`
	costRe := regexp.MustCompile(`  \(cost=[^)]*\)`)
	rows, err := conn.QueryContext(ctx, "EXPLAIN "+q)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("EXPLAIN scan: %v", err)
		}
		lines = append(lines, costRe.ReplaceAllString(l, ""))
	}
	rows.Close()
	want := costRe.ReplaceAllString(`Merge Left Join  (cost=2444.70..3027.35 rows=50 width=20)
  Merge Cond: (gb.k = gb_1.k)
  ->  GroupAggregate  (cost=1720.68..2121.43 rows=50 width=12)
        Group Key: gb.k
        ->  Merge Join  (cost=1720.68..2020.93 rows=20000 width=8)
              Merge Cond: (ga.k = gb.k)
              ->  Sort  (cost=1717.77..1767.77 rows=20000 width=8)
                    Sort Key: ga.k
                    ->  Seq Scan on ga  (cost=0.00..289.00 rows=20000 width=8)
              ->  Sort  (cost=2.91..3.04 rows=50 width=4)
                    Sort Key: gb.k
                    ->  Seq Scan on gb  (cost=0.00..1.50 rows=50 width=4)
  ->  GroupAggregate  (cost=724.02..904.67 rows=50 width=12)
        Group Key: gb_1.k
        ->  Merge Join  (cost=724.02..859.17 rows=9000 width=8)
              Merge Cond: (gc.k = gb_1.k)
              ->  Sort  (cost=721.11..743.61 rows=9000 width=8)
                    Sort Key: gc.k
                    ->  Seq Scan on gc  (cost=0.00..130.00 rows=9000 width=8)
              ->  Sort  (cost=2.91..3.04 rows=50 width=4)
                    Sort Key: gb_1.k
                    ->  Seq Scan on gb gb_1  (cost=0.00..1.50 rows=50 width=4)`, "")
	if got := strings.Join(lines, "\n"); got != want {
		t.Errorf("EXPLAIN %s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
	}

	// 50 groups; y has a row only for k < 30.
	var groups, withY int
	if err := conn.QueryRowContext(ctx, "SELECT count(*), count(t) FROM ("+q+") AS r").Scan(&groups, &withY); err != nil {
		t.Fatalf("result check: %v", err)
	}
	if groups != 50 || withY != 30 {
		t.Errorf("result: %d rows, %d with y; want 50 and 30", groups, withY)
	}
}
