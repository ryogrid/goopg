package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestAggArgSharedAcrossGroupingSets pins M0122-0012 C27: a grouping-sets
// aggregate evaluates each aggregate's argument once per input row (PG's
// ExecBuildAggTrans calls the transition for every grouping set after one
// argument evaluation) and its results are the ones the per-set evaluation
// gave. The queries mix inline-scalar arguments (int, numeric, float, date
// arithmetic, CASE, coalesce) — which are shared — with text and FILTER
// arguments, which are evaluated per set, under both the hash and the
// sorted strategies.
func TestAggArgSharedAcrossGroupingSets(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE aas (a int, b text, c numeric(7,2), d float8, e int)")
	runSQL(t, ctx, "INSERT INTO aas SELECT x % 4, 'b' || (x % 3), (x % 17) * 1.25, x / 7.0, CASE WHEN x % 5 = 0 THEN NULL ELSE x END FROM generate_series(1, 400) x")
	queries := []string{
		"SELECT a, b, sum(c * e), avg(coalesce(e, 0) + 1), count(e), sum(d * 2) FROM aas GROUP BY ROLLUP(a, b)",
		"SELECT a, b, min(b || 'x'), max(c), sum(CASE WHEN e > 100 THEN c ELSE 0 END) FROM aas GROUP BY GROUPING SETS ((a), (b), ())",
		"SELECT a, sum(e) FILTER (WHERE e % 2 = 0), sum(e), count(*) FROM aas GROUP BY CUBE(a, b)",
	}
	for _, sorted := range []bool{false, true} {
		ps := optimizer.DefaultPlannerSettings()
		ps.EnableHashAgg = !sorted
		ps.MaxParallelWorkersPerGather = 0
		for _, q := range queries {
			got := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
			aggArgShareDisabled = true
			want := renderRows(drainPlanRows(t, ctx, planWithSettings(t, ctx, q, ps)))
			aggArgShareDisabled = false
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("sorted=%v %s: shared arguments differ from per-set evaluation\nshared  %v\nper-set %v", sorted, q, got, want)
			}
			if len(got) == 0 {
				t.Fatalf("sorted=%v %s: no rows", sorted, q)
			}
		}
	}
}
