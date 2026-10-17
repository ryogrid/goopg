package postmaster

import (
	"strings"
	"testing"
)

// TestExplainInlinedCTEAggregateInJoinFilter pins M0146-0181 against PG 18.3.
//
// A join residual over two grouped CTEs' aggregate outputs deparses each
// column through the CTE body, as PG does once setrefs removes the trivial
// Subquery Scans: get_variable resolves the OUTER_VAR / INNER_VAR into the
// Agg's target list and parenthesises the non-Var it finds there. goopg
// rendered the body's expression with the join row still active, so the
// aggregate's argument — an index into the aggregate's own input — was read
// positionally against the join row: TPC-DS Q58's `ws_items.ws_item_rev`
// printed `sum(ss_items.item_id)`, and this fixture printed
// `(sum(a.sa) > (0.9 * sum(a.sa)))`. The parentheses were missing too.
//
// PG 18.3 prints exactly `Join Filter: ((sum(f1.v)) > (0.9 * (sum(f2.w))))`.
func TestExplainInlinedCTEAggregateInJoinFilter(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE f1 (k int, pad text, v numeric)",
		"CREATE TABLE f2 (k int, pad text, w numeric)",
		"INSERT INTO f1 SELECT g % 100, 'x', g FROM generate_series(1, 5000) g",
		"INSERT INTO f2 SELECT g % 100, 'y', g FROM generate_series(1, 5000) g",
		"ANALYZE f1",
		"ANALYZE f2",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, `EXPLAIN (COSTS OFF)
WITH a AS (SELECT k AS ak, sum(v) AS sa FROM f1 GROUP BY k),
     b AS (SELECT k AS bk, sum(w) AS sb FROM f2 GROUP BY k)
SELECT a.ak, a.sa FROM a JOIN b ON a.ak = b.bk WHERE a.sa > 0.9 * b.sb ORDER BY 1 LIMIT 5`)
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	const want = "Join Filter: ((sum(f1.v)) > (0.9 * (sum(f2.w))))"
	if !strings.Contains(plan, want) {
		t.Errorf("missing PG 18.3's %q:\n%s", want, plan)
	}
}
