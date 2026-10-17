package optimizer

import "testing"

// TestUpperInputPathCost pins M0146-0211. The upper producers (grouping,
// partial aggregation, ordered, distinct) seed from their input path at that
// path's own cost, as PG's upper rels price cheapest_input_path. goopg's
// Project wrapper over a stamped search node adds nothing: PG applies the
// target to the path itself (dummypp). Only an input the search did not build
// falls back to the legacy display estimate.
func TestUpperInputPathCost(t *testing.T) {
	d := sizedDistinctFixture(t, 1000, 10)
	scan := d.Child.(*SeqScan)
	targets := make([]Expr, len(scan.Output()))
	for i, c := range scan.Output() {
		targets[i] = &ColumnRef{Index: i, Name: c.Name}
	}
	proj := &Project{Child: scan, Targets: targets, schema: scan.Output()}

	// Unstamped: the legacy estimate of the node itself, Project included.
	if got, want := upperInputPathCost(proj), legacyDisplayCostOf(proj); got != want {
		t.Fatalf("unstamped input: %+v, want the legacy display cost %+v", got, want)
	}
	// Stamped beneath a Project (twice wrapped): the stamped path's cost.
	stamped := PlanCost{StartupCost: 10, TotalCost: 500, PlanRows: 1000}
	scan.setPlanCost(stamped)
	twice := &Project{Child: proj, Targets: targets, schema: proj.Output()}
	got := upperInputPathCost(twice)
	if got.StartupCost != 10 || got.TotalCost != 500 || got.PlanRows != 1000 {
		t.Fatalf("Project over a stamped node: %+v, want the stamped path cost %+v", got, stamped)
	}
	if legacy := legacyDisplayCostOf(twice); !(legacy.TotalCost > got.TotalCost) {
		t.Fatalf("fixture vacuous: the legacy Project estimate %+v does not exceed the stamped cost", legacy)
	}
	// A per-worker stamp is not the input path's whole cost: legacy fallback.
	scan.setPlanCost(PlanCost{StartupCost: 10, TotalCost: 500, PlanRows: 1000, PerWorker: true})
	if got, want := upperInputPathCost(proj), legacyDisplayCostOf(proj); got != want {
		t.Fatalf("per-worker stamp: %+v, want the legacy display cost %+v", got, want)
	}
}
