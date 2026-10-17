package optimizer

import (
	"math"
	"testing"
)

// TestEstimateRowsGatherReadsItsPathRows pins M0146-0210. A Gather the path
// search built carries its path's rows, which are compute_gather_rows: the
// per-worker rows times the parallel divisor, clamped (costsize.c:6625).
// create_gather_plan copies them into plan_rows. TPC-DS Q54's my_customers
// Gather has 1 row per worker under 1 planned worker, so 1 × 1.7 = 2, and
// PG's DISTINCT above it keeps 2 rows. goopg re-estimated the child's
// per-worker rows (1), so the DISTINCT came out at 1 and every join above
// it shrank.
func TestEstimateRowsGatherReadsItsPathRows(t *testing.T) {
	d := sizedDistinctFixture(t, 1000, 10)
	scan := d.Child
	g := &Gather{Child: scan}
	if got, want := EstimateRows(g), EstimateRows(scan); got != want {
		t.Fatalf("unstamped Gather: %d rows, want its child's %d", got, want)
	}
	g.setPlanCost(PlanCost{StartupCost: 1000, TotalCost: 1100, PlanRows: 2})
	if got := EstimateRows(g); got != 2 {
		t.Fatalf("stamped Gather: %d rows, want its path's 2", got)
	}
	gm := &GatherMerge{Child: scan}
	gm.setPlanCost(PlanCost{StartupCost: 1000, TotalCost: 1100, PlanRows: 2})
	if got := EstimateRows(gm); got != 2 {
		t.Fatalf("stamped Gather Merge: %d rows, want its path's 2", got)
	}
	// A per-worker stamp is not a whole-node count.
	pw := &Gather{Child: scan}
	pw.setPlanCost(PlanCost{StartupCost: 1000, TotalCost: 1100, PlanRows: 2, PerWorker: true})
	if got, want := EstimateRows(pw), EstimateRows(scan); got != want {
		t.Fatalf("per-worker stamp: %d rows, want the child's %d", got, want)
	}
}

// TestPartialDistinctGatheredRowsAreClamped: every Gather Merge the
// partial-DISTINCT arms file carries compute_gather_rows, which clamps. That is
// what create_gather_merge_path receives from generate_useful_gather_paths
// (planner.c:5017). Unclamped, Q54's 1 × 1.7 priced the merge under PG's and
// tipped the DISTINCT onto the partial arm.
func TestPartialDistinctGatheredRowsAreClamped(t *testing.T) {
	prev := parallelOn.Load()
	parallelOn.Store(true)
	defer parallelOn.Store(prev)

	// 400000 rows plan one worker, so the divisor is 1.7 and the gathered
	// rows are fractional before the clamp (45.9).
	d := sizedDistinctFixture(t, 400_000, 3)
	dr, final := addPartialDistinctFor(t, d, sortedArmSettings())
	if final == nil {
		t.Fatal("producer filed no candidate")
	}
	n := 0
	for _, p := range dr.Pathlist {
		if p == nil || len(p.Children) != 1 || p.Children[0].Kind != PathGatherMerge {
			continue
		}
		gm := p.Children[0]
		if gm.Rows != math.RoundToEven(gm.Rows) || gm.Rows < 1 {
			t.Errorf("Gather Merge rows %v: not clamp_row_est'd", gm.Rows)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no Gather Merge arm inspected")
	}
}

// TestDistinctSeedLooksThroughProject: create_distinct_paths reads
// cheapest_input_path at its own cost. goopg's Project above a stamped search
// node is not a PG node (the target is applied to the scan/join path itself), so
// the DISTINCT arms must be priced the same over Project(n) as over n.
func TestDistinctSeedLooksThroughProject(t *testing.T) {
	d := sizedDistinctFixture(t, 1000, 10)
	scan := d.Child.(*SeqScan)
	scan.setPlanCost(PlanCost{StartupCost: 0, TotalCost: 500, PlanRows: 1000})
	cost := func(child Node) Cost {
		u := newUpperRels()
		spec := &Distinct{Child: child, schema: child.Output()}
		if _, err := createDistinctPaths(u, spec, nil, DefaultPlannerSettings(), 0); err != nil {
			t.Fatal(err)
		}
		rel := fetchUpperRel(u, UpperDistinct, 0, 0)
		if rel.CheapestTotal == nil {
			t.Fatal("no cheapest DISTINCT path")
		}
		return rel.CheapestTotal.Cost
	}
	targets := make([]Expr, len(scan.Output()))
	for i, c := range scan.Output() {
		targets[i] = &ColumnRef{Index: i, Name: c.Name}
	}
	bare := cost(scan)
	wrapped := cost(&Project{Child: scan, Targets: targets, schema: scan.Output()})
	if math.Abs(bare.Total-wrapped.Total) > 1e-9 || math.Abs(bare.Startup-wrapped.Startup) > 1e-9 {
		t.Fatalf("DISTINCT over Project %+v != over the stamped node %+v", wrapped, bare)
	}
}
