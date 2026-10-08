package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0108: a NestLoop param over an inlined single-reference CTE deparses
// into the CTE's body, as PG's flattened subquery does (TPC-DS Q64's
// `(sr_item_sk = catalog_sales.cs_item_sk)` inside the `cross_sales` CTE,
// where goopg printed the inlined scan's name `cs_ui`).
//
// PG 18.3, enable_hashjoin = enable_mergejoin = off:
//
//	CTE x
//	  ->  Nested Loop
//	        ->  HashAggregate …
//	        ->  Index Scan using m108b_pkey on m108b b
//	              Index Cond: (k = m108a.k)
func TestNestLoopParamOverInlinedCTE(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE m108a (k int, v int)",
		"CREATE TABLE m108b (k int PRIMARY KEY, w int)",
		"INSERT INTO m108a SELECT g % 50, g FROM generate_series(1, 200) g",
		"INSERT INTO m108b SELECT g, g FROM generate_series(1, 5000) g",
		"ANALYZE m108a", "ANALYZE m108b",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableMergeJoin = false, false
	ps.MaxParallelWorkersPerGather = 0
	const q = "EXPLAIN (COSTS OFF) WITH u AS (SELECT k, sum(v) s FROM m108a GROUP BY k HAVING sum(v) > 10), " +
		"x AS (SELECT b.w, u.k FROM m108b b JOIN u ON b.k = u.k) SELECT * FROM x x1 JOIN x x2 ON x1.k = x2.k"
	plan := strings.Join(renderRows(runSQLWith(t, ctx, q, ps)), "\n")
	if !strings.Contains(plan, "Index Scan using m108b_pkey on m108b b") {
		t.Fatalf("want PG's index probe of m108b:\n%s", plan)
	}
	if !strings.Contains(plan, "Index Cond: (k = m108a.k)") {
		t.Errorf("want the param deparsed into the inlined CTE's body:\n%s", plan)
	}
}
