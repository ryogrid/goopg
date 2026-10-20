package executor

import (
	"strings"
	"testing"
)

// TestExplainLimitClampsToInputRows pins M0146-0005bm against PG 18.3's
// adjust_limit_rows_costs (pathnode.c): a Limit emits at most its input's
// rows, skips its OFFSET rows first, and interpolates the input's costs over
// the fraction it reads. PG prints, for an empty never-analyzed t(a int, b int):
//
//	Limit  (cost=43.90..46.90 rows=22 width=12)             -- LIMIT 100
//	Limit  (cost=44.58..45.95 rows=10 width=12)             -- LIMIT 10 OFFSET 5
//	Limit  (cost=0.10..0.17 rows=5 width=4)                 -- over Seq Scan, LIMIT 5 OFFSET 7
//
// goopg printed `rows=<count>` with the input's total plus a per-row charge
// (TPC-DS Q84: rows=100 over a Gather Merge of 11).
//
// PG's aggregate total is 43.90 + 200 groups x (cpu_tuple_cost 0.01 + the
// HAVING quals' 2 x cpu_operator_cost) = 46.90; count(*) has no finalfn. goopg
// reaches it exactly since M0146-0214 slice 3 priced the HAVING inside the
// aggregate (cost_agg's quals arm; 46.12 before, with the 0.78 of HAVING quals
// missing), so all three cases pin PG's own Limit lines. LIMIT 10 OFFSET 5 over
// (43.90..46.90, 22 rows) is 43.90 + 3.00*5/22 = 44.58 .. 44.58 + 3.00*10/22 =
// 45.95.
func TestExplainLimitClampsToInputRows(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT a, count(*) FROM t GROUP BY a HAVING count(*) >= 15 AND count(*) <= 20 LIMIT 100`,
			"Limit  (cost=43.90..46.90 rows=22 "},
		{`SELECT a, count(*) FROM t GROUP BY a HAVING count(*) >= 15 AND count(*) <= 20 LIMIT 10 OFFSET 5`,
			"Limit  (cost=44.58..45.95 rows=10 "},
		{`SELECT a FROM t LIMIT 5 OFFSET 7`,
			"Limit  (cost=0.10..0.17 rows=5 "},
	}
	for _, c := range cases {
		lines := cteExplainLines(t, c.sql)
		if !strings.HasPrefix(lines[0], c.want) {
			t.Errorf("%s\nwant prefix %q, got plan:\n%s", c.sql, c.want, strings.Join(lines, "\n"))
		}
		if c.want[len("Limit  (cost=")] == '4' && !strings.Contains(lines[1], "rows=22 ") {
			t.Errorf("%s: input is not the 22-row aggregate:\n%s", c.sql, strings.Join(lines, "\n"))
		}
	}
}
