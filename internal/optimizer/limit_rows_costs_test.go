package optimizer

import (
	"math"
	"testing"
)

// TestLimitReadsInputPathThroughProject pins M0146-0005bm: a Limit is sized
// by adjust_limit_rows_costs over the path it reads. goopg puts a Project
// between them (PG projects on the input path's tlist), so the Limit looks
// through it. TPC-DS Q84: `LIMIT 100` over a Gather Merge of 11 rows is PG's
// Limit of the input's rows at the input's cost, where goopg printed rows=100.
func TestLimitReadsInputPathThroughProject(t *testing.T) {
	gm := &Sort{}
	gm.setPlanCost(PlanCost{StartupCost: 8287.81, TotalCost: 8289.06, PlanRows: 11, PlanWidth: 356})
	lim := &Limit{
		Child: &Project{Child: gm},
		Limit: &IntegerConst{Value: 100},
	}
	if got := limitInputRows(lim.Child); got != 11 {
		t.Fatalf("limitInputRows = %d, want the stamped input's 11", got)
	}
	in, ok := limitInputPath(lim.Child)
	if !ok {
		t.Fatal("limitInputPath did not reach the stamped node under the Project")
	}
	rows, startup, total := adjustLimitRowsCosts(in.PlanRows, in.StartupCost, in.TotalCost, limitEstimatesOf(lim))
	if rows != 11 || startup != 8287.81 || total != 8289.06 {
		t.Fatalf("LIMIT 100 over 11 rows = (%v, %.2f..%.2f), want (11, 8287.81..8289.06)", rows, startup, total)
	}

	// OFFSET skips input first; LIMIT counts what is left (pathnode.c).
	rows, startup, total = adjustLimitRowsCosts(22, 43.90, 46.62, limitEstimates{count: 10, offset: 5})
	if rows != 10 || !near3(startup, 44.518) || !near3(total, 45.755) {
		t.Fatalf("LIMIT 10 OFFSET 5 over 22 = (%v, %.3f..%.3f)", rows, startup, total)
	}
	// An unestimatable count is 10% of the input.
	if rows, _, _ = adjustLimitRowsCosts(2260, 0, 32.6, limitEstimates{count: -1}); rows != 226 {
		t.Fatalf("LIMIT $1 over 2260 = %v rows, want 226", rows)
	}
}

func near3(a, b float64) bool { return math.Abs(a-b) < 0.001 }
