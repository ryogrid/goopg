package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSortKeyOverWindowAggDeparsesWindowFunc pins M0146-0005cj against PG
// 18.3: a Sort key read from a WindowAgg's targetlist prints its window
// function in place (`sum((sum(v))) OVER w1`), an input column as an
// OUTER_VAR parenthesised over its non-Var referent (`(sum(v))`), and the
// whole key in get_special_variable's outer pair. Each want line is PG's
// own output for the statement (TPC-DS Q12/Q20/Q98's shape).
func TestSortKeyOverWindowAggDeparsesWindowFunc(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE wk (a int, b int, v numeric)")
	runSQL(t, ctx, "INSERT INTO wk SELECT i % 10, i % 7, i FROM generate_series(1,1000) i")
	runSQL(t, ctx, "ANALYZE wk")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ q, want string }{
		{"SELECT a, b, sum(v) * 100 / sum(sum(v)) OVER (PARTITION BY a) AS r FROM wk GROUP BY a, b ORDER BY a, r",
			"Sort Key: a, ((((sum(v)) * '100'::numeric) / sum((sum(v))) OVER w1))"},
		{"SELECT a, rank() OVER (ORDER BY b) AS rk FROM wk ORDER BY rk, a",
			"Sort Key: (rank() OVER w1), a"},
		{"SELECT a, b, avg(sum(v)) OVER (PARTITION BY a) AS av FROM wk GROUP BY a, b ORDER BY sum(v) - avg(sum(v)) OVER (PARTITION BY a), a",
			"Sort Key: (((sum(v)) - avg((sum(v))) OVER w1)), a"},
	} {
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		found := false
		for _, l := range lines {
			if l == c.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s\nwant line %q in:\n%s", c.q, c.want, strings.Join(lines, "\n"))
		}
	}
}
