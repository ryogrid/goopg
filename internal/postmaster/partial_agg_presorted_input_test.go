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
