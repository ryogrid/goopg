package optimizer

import (
	"math"
	"testing"
)

// TestSubplanLeafPricesCostSubqueryscanOnce pins M0146-0224 slice 2: a derived
// leaf (SubqueryScan → Project → Aggregate) enters the search at its
// aggregate's cost plus ONE cpu_tuple_cost per row — cost_subqueryscan over a
// subpath whose projection is folded into the projection-capable aggregate
// (create_projection_path's dummypp arm). The legacy display cost charged a
// cpu_tuple_cost at the SubqueryScan and at the Project as well, so TPC-DS
// Q78's grouped CTE legs entered at three times PG's overhead.
func TestSubplanLeafPricesCostSubqueryscanOnce(t *testing.T) {
	cp := defaultCostParams()
	agg := &Aggregate{}
	agg.setPlanCost(PlanCost{StartupCost: 100, TotalCost: 200, PlanRows: 10})
	leaf := &SubqueryScan{Child: &Project{Child: agg}}
	got := costSubplanLeaf(cp, leaf, 10)
	want := Cost{Startup: 100, Total: 200 + cp.cpuTupleCost*10}
	if math.Abs(got.Startup-want.Startup) > 1e-9 || math.Abs(got.Total-want.Total) > 1e-9 {
		t.Fatalf("derived leaf over an aggregate: got %.4f..%.4f, want %.4f..%.4f",
			got.Startup, got.Total, want.Startup, want.Total)
	}
	// A projection over a node PG cannot project from (Material) needs its own
	// Result node, which keeps its cpu_tuple_cost.
	proj := &Project{Child: &Materialize{Child: agg}}
	sub := subplanLeafSubpathCost(&SubqueryScan{Child: proj})
	if keep := legacyDisplayCostOf(proj); math.Abs(sub.TotalCost-keep.TotalCost) > 1e-9 {
		t.Fatalf("projection over Material: got %.4f, want the Project's own %.4f", sub.TotalCost, keep.TotalCost)
	}
}
