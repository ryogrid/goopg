package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSubqueryScanExprKeyQualified pins M0146-0005dd against PG 18.3: a
// Subquery Scan with a qual is kept (setrefs.c trivial_subqueryscan), and a
// Sort key over it deparses through the scan's own columns, so an
// expression key qualifies its columns by the alias exactly as a bare
// column key does — TPC-DS Q89's `((tmp1.sum_sales -
// tmp1.avg_monthly_sales)), tmp1.s_store_name`. Want lines are PG's output.
func TestSubqueryScanExprKeyQualified(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zsq (a int, b numeric)")
	ps := optimizer.DefaultPlannerSettings()
	ps.MaxParallelWorkersPerGather = 0
	inner := "SELECT * FROM (SELECT a, b, avg(b) OVER (PARTITION BY a) av FROM zsq) x WHERE av > 1 ORDER BY "
	for _, c := range []struct{ q, want string }{
		{inner + "b - av, a", "Sort Key: ((x.b - x.av)), x.a"},
		{inner + "abs(b - av) DESC, a", "Sort Key: (abs((x.b - x.av))) DESC, x.a"},
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
