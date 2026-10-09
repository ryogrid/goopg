package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestDerivedLeafChargesItsInitPlans pins M0146-0005dw against PG 18.3: a
// subquery in FROM is a query level of its own, and SS_charge_for_initplans
// adds its initPlans to that level's paths, so a join over it pays for them.
// Two ranked derived tables, each filtered by an uncorrelated InitPlan,
// merge-joined on the rank: PG's Merge Join starts at 3516.50, the two Sorts
// over Subquery Scans that each include the InitPlan's 339.01. goopg filed
// those initPlans under the statement's top (no Subquery Scan marks the level
// in its plan), so the search priced each derived leaf without them and the
// merge started below its own inputs — TPC-DS Q44's ranked derived tables,
// ~16k short each.
//
// The test plans the query twice: with `(SELECT avg(y) FROM dli)` (an
// InitPlan costing 339.01) and with `(SELECT 500::numeric)` (≈0). Both are
// parameters the estimator cannot read, so the plans are sized alike and the
// join's startup differs only by what the derived leaves charge: two
// InitPlans. Before the change it differed by nothing.
func TestDerivedLeafChargesItsInitPlans(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE dli (p int, y int)",
		"INSERT INTO dli SELECT g % 7, (g * 37) % 1000 FROM generate_series(1, 20000) g",
		"ANALYZE dli",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin, ps.EnableNestLoop = false, false
	joinStartup := func(sub string) float64 {
		t.Helper()
		stmts, err := parser.Parse("SELECT count(*) FROM (SELECT y, rank() OVER (ORDER BY y) r FROM dli WHERE y > " + sub + ") a " +
			"JOIN (SELECT y, rank() OVER (ORDER BY y DESC) r FROM dli WHERE y > " + sub + ") b ON a.r = b.r")
		if err != nil {
			t.Fatal(err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
		if err != nil {
			t.Fatal(err)
		}
		var join *optimizer.Join
		var walk func(n optimizer.Node)
		walk = func(n optimizer.Node) {
			if n == nil || join != nil {
				return
			}
			if j, ok := n.(*optimizer.Join); ok {
				join = j
				return
			}
			for _, c := range optimizer.ParallelChildrenForTest(n) {
				walk(c)
			}
		}
		walk(plan)
		if join == nil {
			t.Fatalf("no join in the plan for %s", sub)
		}
		pc, set := join.PlanCostInfo()
		if !set {
			t.Fatalf("join carries no path cost for %s", sub)
		}
		return pc.StartupCost
	}
	costly := joinStartup("(SELECT avg(y) FROM dli)")
	cheap := joinStartup("(SELECT 500::numeric)")
	if d := costly - cheap; d < 2*339.0 {
		t.Errorf("join startup with a 339.01 InitPlan in each derived table is %.2f, with a free one %.2f: "+
			"difference %.2f, want both InitPlans (≥ 678.02) as PG charges them to the subquery level", costly, cheap, d)
	}
}
