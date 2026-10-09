package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestWindowAggPrintsWindowDefinition pins M0146-0005cg against PG 18.3:
// a WindowAgg prints `Window: name AS (PARTITION BY … ORDER BY … frame)`
// (explain.c show_window_def). The expected lines are PG's own output for
// the same statements:
//   - an unnamed window takes planner.c's made-up `w1`;
//   - rank() rewrites the frame to `ROWS UNBOUNDED PRECEDING`
//     (optimize_window_clauses), with no frame written;
//   - a ROWS offset deparses as the int8 Const it was coerced to, a RANGE
//     offset as the in_range offset type (int8 only over an int8 key);
//   - a WINDOW clause name is kept.
func TestWindowAggPrintsWindowDefinition(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE ww (a int, b int, c int8)")
	runSQL(t, ctx, "INSERT INTO ww SELECT i % 10, i, i FROM generate_series(1,1000) i")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	for _, c := range []struct{ q, want string }{
		{"SELECT a, sum(b) OVER (PARTITION BY a) FROM ww", "Window: w1 AS (PARTITION BY a)"},
		{"SELECT a, rank() OVER (PARTITION BY a ORDER BY b) FROM ww", "Window: w1 AS (PARTITION BY a ORDER BY b ROWS UNBOUNDED PRECEDING)"},
		{"SELECT a, row_number() OVER () FROM ww", "Window: w1 AS (ROWS UNBOUNDED PRECEDING)"},
		{"SELECT a, sum(b) OVER (ORDER BY b ROWS BETWEEN 1 PRECEDING AND 2 FOLLOWING) FROM ww", "Window: w1 AS (ORDER BY b ROWS BETWEEN '1'::bigint PRECEDING AND '2'::bigint FOLLOWING)"},
		{"SELECT a, sum(b) OVER (ORDER BY b ROWS 3 PRECEDING EXCLUDE TIES) FROM ww", "Window: w1 AS (ORDER BY b ROWS '3'::bigint PRECEDING EXCLUDE TIES)"},
		{"SELECT sum(b) OVER (ORDER BY c RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING) FROM ww", "Window: w1 AS (ORDER BY c RANGE BETWEEN '1'::bigint PRECEDING AND '1'::bigint FOLLOWING)"},
		{"SELECT sum(b) OVER (ORDER BY b RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING) FROM ww", "Window: w1 AS (ORDER BY b RANGE BETWEEN 1 PRECEDING AND 1 FOLLOWING)"},
		{"SELECT a, sum(b) OVER (PARTITION BY a ORDER BY b), count(*) OVER w FROM ww WINDOW w AS (PARTITION BY a ORDER BY b)", "Window: w AS (PARTITION BY a ORDER BY b)"},
		{"SELECT a, sum(b) OVER (PARTITION BY a % 2) FROM ww", "Window: w1 AS (PARTITION BY ((a % 2)))"},
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
