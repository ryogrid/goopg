package postmaster

import (
	"strings"
	"testing"
)

// TestExplainRangeWithInitPlanBounds pins M0146-0184 against PG 18.3.
//
// clauselist_selectivity (clausesel.c) pairs `x > a AND x < b` into one range
// whenever a and b are pseudo-constant — an InitPlan output qualifies, not only
// a literal. scalarineqsel cannot estimate a non-Const bound, so both sides are
// DEFAULT_INEQ_SEL and the pair collapses to DEFAULT_RANGE_INEQ_SEL (0.005).
// goopg paired literals only and multiplied two DEFAULT_INEQ_SEL factors
// (0.111): TPC-DS Q54's `d_month_seq` band was estimated 22x too wide, which
// sized its store_sales join at 76335 rows against PG's 3441.
//
// PG 18.3 prints rows=100 (20000 x 0.005) for both bands, operator over an
// InitPlan included, and rows=6667 (DEFAULT_INEQ_SEL) for a single bound.
func TestExplainRangeWithInitPlanBounds(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE rq (x int, pad text)",
		"INSERT INTO rq SELECT g, 'p' FROM generate_series(1, 20000) g",
		"ANALYZE rq",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	for _, tc := range []struct{ where, rows string }{
		{"x >= (SELECT 100) AND x <= (SELECT 200)", "rows=100 "},
		{"x > (SELECT 100) + 1 AND x < (SELECT 200)", "rows=100 "},
		{"x >= (SELECT 100)", "rows=6667 "},
	} {
		got, e := allRowsQuery(t, conn, "EXPLAIN SELECT x FROM rq WHERE "+tc.where)
		if e != "" {
			t.Fatal(e)
		}
		plan := strings.Join(got, "\n")
		if !strings.Contains(plan, "Seq Scan on rq  (cost=") || !strings.Contains(plan, tc.rows) {
			t.Errorf("WHERE %s: PG 18.3 estimates %q:\n%s", tc.where, tc.rows, plan)
		}
	}
}
