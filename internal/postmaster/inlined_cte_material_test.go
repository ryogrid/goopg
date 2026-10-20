package postmaster

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestInlinedCTEInnerIsMaterialized pins M0146-0232: a single-reference CTE
// is inlined into an ordinary subquery (inline_cte), so as a nested loop's
// inner it is PG's T_SubqueryScan — cost_rescan's default arm re-executes it
// per outer row and ExecMaterializesOutput does not list it — and
// match_unsorted_outer's Material over it wins. goopg classed the inlined
// reference as T_CteScan (a tuplestore): cheap rescans and no Material.
//
// The Material is a MaterialPath, whose create_material_plan plans its child
// with CP_SMALL_TLIST, so a subquery scan consuming a subset of its output
// keeps `Subquery Scan on cb`. Every plan below is PG 18.3's EXPLAIN on the
// same statements (TPC-DS Q77's `cr` branch has this shape), compared with
// the cost groups removed: EXPLAIN's widths are unnarrowed (M0146-0042) and a
// wrapper's display cost is not PG's yet, neither of which this pins.
func TestInlinedCTEInnerIsMaterialized(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	for _, stmt := range []string{
		"CREATE TABLE ma (k int, v int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE mb (k int, v int) WITH (autovacuum_enabled = false)",
		"INSERT INTO ma SELECT g % 5, g FROM generate_series(1, 10000) g",
		"INSERT INTO mb SELECT g % 4, g FROM generate_series(1, 8000) g",
		"ANALYZE ma",
		"ANALYZE mb",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	costRe := regexp.MustCompile(`  \(cost=[^)]*\)`)
	explain := func(q string) string {
		rows, err := db.QueryContext(ctx, "EXPLAIN "+q)
		if err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				t.Fatalf("EXPLAIN %s: scan: %v", q, err)
			}
			lines = append(lines, costRe.ReplaceAllString(l, ""))
		}
		return strings.Join(lines, "\n")
	}
	const ctes = "WITH ca AS (SELECT k, sum(v) AS s FROM ma GROUP BY k), cb AS (SELECT k, sum(v) AS t FROM mb GROUP BY k) "
	for q, want := range map[string]string{
		ctes + "SELECT s, t FROM ca, cb": `Nested Loop  (cost=351.00..351.44 rows=20 width=16)
  ->  HashAggregate  (cost=195.00..195.05 rows=5 width=12)
        Group Key: ma.k
        ->  Seq Scan on ma  (cost=0.00..145.00 rows=10000 width=8)
  ->  Materialize  (cost=156.00..156.10 rows=4 width=8)
        ->  Subquery Scan on cb  (cost=156.00..156.08 rows=4 width=8)
              ->  HashAggregate  (cost=156.00..156.04 rows=4 width=12)
                    Group Key: mb.k
                    ->  Seq Scan on mb  (cost=0.00..116.00 rows=8000 width=8)`,
		// The same subquery written in FROM — already an RTE_SUBQUERY in goopg.
		"SELECT s, t FROM (SELECT k, sum(v) AS s FROM ma GROUP BY k) ca, (SELECT k, sum(v) AS t FROM mb GROUP BY k) cb": `Nested Loop  (cost=351.00..351.44 rows=20 width=16)
  ->  HashAggregate  (cost=195.00..195.05 rows=5 width=12)
        Group Key: ma.k
        ->  Seq Scan on ma  (cost=0.00..145.00 rows=10000 width=8)
  ->  Materialize  (cost=156.00..156.10 rows=4 width=8)
        ->  Subquery Scan on cb  (cost=156.00..156.08 rows=4 width=8)
              ->  HashAggregate  (cost=156.00..156.04 rows=4 width=12)
                    Group Key: mb.k
                    ->  Seq Scan on mb  (cost=0.00..116.00 rows=8000 width=8)`,
	} {
		if got, want := explain(q), costRe.ReplaceAllString(want, ""); got != want {
			t.Errorf("EXPLAIN %s:\n got:\n%s\nwant (PG 18.3):\n%s", q, got, want)
		}
	}
}
