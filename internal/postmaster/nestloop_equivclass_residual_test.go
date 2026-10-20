package postmaster

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestNestLoopKeepsOneEquivClassClausePerJoin pins M0146-0242: a join's
// restrictlist carries ONE equality per equivalence class between its sides
// (generate_join_implied_equalities_normal), and create_nestloop_path drops it
// only when the parameterised inner enforces it.
//
// The class is {ea.k, ef.k, ed.k} and ed is probed last, keyed on ea.k (which
// loop_count makes the cheaper probe). In the first statement the class's
// equality to the outer is `ed.k = ea.k`, which the probe enforces, so the
// loop carries no Join Filter; in the second it is `ed.k = ef.k`, which the
// probe cannot enforce, so PG keeps it as the Join Filter. This is the
// end-to-end shape guard for both directions of the rule; the case where
// goopg's old every-member-pair list changed the elected probe (TPC-DS Q72's
// near-tie) is pinned directly by TestNLIResidualKeepsOneClausePerEquivClass.
// Plans are PG 18.3's EXPLAIN on the same statements, costs removed
// (EXPLAIN's widths are unnarrowed, M0146-0042).
func TestNestLoopKeepsOneEquivClassClausePerJoin(t *testing.T) {
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
		"CREATE TABLE ea (k int, v int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE ef (k int, w int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE ed (k int, u int) WITH (autovacuum_enabled = false)",
		"INSERT INTO ea SELECT g, g FROM generate_series(1, 1000) g",
		"INSERT INTO ef SELECT g % 1000 + 1, g FROM generate_series(1, 5000) g",
		"INSERT INTO ed SELECT g % 100000 + 1, g FROM generate_series(1, 300000) g",
		"CREATE INDEX ed_k ON ed (k)",
		"ANALYZE ea",
		"ANALYZE ef",
		"ANALYZE ed",
		"SET max_parallel_workers_per_gather = 0",
		"SET enable_mergejoin = off",
		"SET enable_hashjoin = off",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	costRe := regexp.MustCompile(`  \(cost=[^)]*\)`)
	explain := func(q string) string {
		rows, err := conn.QueryContext(ctx, "EXPLAIN "+q)
		if err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatalf("EXPLAIN scan: %v", err)
			}
			lines = append(lines, costRe.ReplaceAllString(l, ""))
		}
		return strings.Join(lines, "\n")
	}
	for q, want := range map[string]string{
		"SELECT ea.v, ef.w, ed.u FROM ea JOIN ef ON ea.k = ef.k JOIN ed ON ed.k = ea.k WHERE ef.w < 40": `Nested Loop  (cost=0.42..987.66 rows=116 width=12)
  ->  Nested Loop  (cost=0.00..685.60 rows=39 width=16)
        Join Filter: (ea.k = ef.k)
        ->  Seq Scan on ea  (cost=0.00..15.00 rows=1000 width=8)
        ->  Materialize  (cost=0.00..85.69 rows=39 width=8)
              ->  Seq Scan on ef  (cost=0.00..85.50 rows=39 width=8)
                    Filter: (w < 40)
  ->  Index Scan using ed_k on ed  (cost=0.42..7.72 rows=3 width=8)
        Index Cond: (k = ea.k)`,
		"SELECT ea.v, ef.w, ed.u FROM ef JOIN ea ON ef.k = ea.k JOIN ed ON ed.k = ef.k WHERE ef.w < 40": `Nested Loop  (cost=0.42..987.95 rows=116 width=12)
  Join Filter: (ed.k = ef.k)
  ->  Nested Loop  (cost=0.00..685.60 rows=39 width=16)
        Join Filter: (ef.k = ea.k)
        ->  Seq Scan on ea  (cost=0.00..15.00 rows=1000 width=8)
        ->  Materialize  (cost=0.00..85.69 rows=39 width=8)
              ->  Seq Scan on ef  (cost=0.00..85.50 rows=39 width=8)
                    Filter: (w < 40)
  ->  Index Scan using ed_k on ed  (cost=0.42..7.72 rows=3 width=8)
        Index Cond: (k = ea.k)`,
	} {
		if got, want := explain(q), costRe.ReplaceAllString(want, ""); got != want {
			t.Errorf("EXPLAIN %s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM ea JOIN ef ON ea.k = ef.k JOIN ed ON ed.k = ea.k WHERE ef.w < 40").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 117 {
		t.Errorf("row count %d, want 117", n)
	}
}
