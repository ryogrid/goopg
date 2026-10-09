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
// The HashAggregate's own cost (and the scan's unnarrowed width) differ from
// PG's for reasons outside this fix, so the aggregate cases pin the Limit
// relative to its input: LIMIT 100 over 22 rows is the input's own cost line;
// LIMIT 10 OFFSET 5 over (43.90..46.62, 22 rows) is
// 43.90 + 2.72*5/22 = 44.52 .. 44.52 + 2.72*10/22 = 45.75.
func TestExplainLimitClampsToInputRows(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT a, count(*) FROM t GROUP BY a HAVING count(*) >= 15 AND count(*) <= 20 LIMIT 100`,
			"Limit  (cost=43.90..46.62 rows=22 "},
		{`SELECT a, count(*) FROM t GROUP BY a HAVING count(*) >= 15 AND count(*) <= 20 LIMIT 10 OFFSET 5`,
			"Limit  (cost=44.52..45.75 rows=10 "},
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
