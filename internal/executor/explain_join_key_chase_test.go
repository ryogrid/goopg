package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestSortKeyExprOverJoinChasesAggregates pins M0146-0005cx against PG
// 18.3 (TPC-DS Q90's shape): an expression Sort key over a join is
// evaluated in the join's targetlist, whose columns are OUTER/INNER_VARs
// into the two aggregate subqueries, so each deparses as the aggregate call
// in parentheses — through a Finalize Aggregate too. PG prints the same key
// for the parallel and the serial plan of this statement.
func TestSortKeyExprOverJoinChasesAggregates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zq1 (a int, b int)")
	runSQL(t, ctx, "INSERT INTO zq1 SELECT i, i % 7 FROM generate_series(1,20000) i")
	runSQL(t, ctx, "ANALYZE zq1")
	const q = "SELECT cast(x.c as numeric(15,4)) / cast(y.d as numeric(15,4)) AS r FROM " +
		"(SELECT count(*) c FROM zq1 WHERE b = 1) x, (SELECT count(*) d FROM zq1 WHERE b = 2) y ORDER BY r"
	const want = "Sort Key: ((((count(*)))::numeric(15,4) / ((count(*)))::numeric(15,4)))"
	for _, workers := range []int{0, 2} {
		ps := optimizer.DefaultPlannerSettings()
		ps.MaxParallelWorkersPerGather = workers
		ps.ParallelSetupCost = 0
		ps.ParallelTupleCost = 0
		ps.MinParallelTableScanSize = 0
		var lines []string
		for _, r := range drainPlanRows(t, ctx, planWithSettings(t, ctx, "EXPLAIN (COSTS OFF) "+q, ps)) {
			if len(r) > 0 && r[0].Kind == KindString {
				lines = append(lines, strings.TrimSpace(r[0].StringValue()))
			}
		}
		found := false
		for _, l := range lines {
			if l == want {
				found = true
			}
		}
		if !found {
			t.Errorf("workers=%d: want %q in:\n%s", workers, want, strings.Join(lines, "\n"))
		}
	}
}
