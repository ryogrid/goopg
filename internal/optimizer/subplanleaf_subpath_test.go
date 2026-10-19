package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
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

// TestHavingIsPricedInsideTheAggregate pins M0146-0214 slice 3: a Filter over
// an Aggregate is its HAVING, which cost_agg prices inside the aggregate — the
// quals' cost per group the Agg emits before filtering, and no cpu_tuple_cost
// (cost_agg charged one per group already). The legacy pass-through charged a
// cpu_tuple_cost per surviving row instead and left the quals free.
func TestHavingIsPricedInsideTheAggregate(t *testing.T) {
	cp := defaultCostParams()
	agg := &Aggregate{}
	agg.setPlanCost(PlanCost{StartupCost: 100, TotalCost: 200, PlanRows: 22})
	// sum > 2 * sum: two operators per group.
	having := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: 1, Type: catalog.Type{Name: "numeric"}},
		Right: &BinaryOp{Op: parser.OpMul, Left: &NumericConst{Value: "2"}, Right: &ColumnRef{Index: 2, Type: catalog.Type{Name: "numeric"}}}}
	f := &Filter{Child: agg, Predicate: having}
	got := DeriveLegacyDisplayCost(f, 7)
	if want := 200 + 2*cp.cpuOperatorCost*22; math.Abs(got.TotalCost-want) > 1e-9 || math.Abs(got.StartupCost-100) > 1e-9 {
		t.Fatalf("HAVING over an aggregate: got %.6f..%.6f, want 100..%.6f (quals per group, no cpu_tuple_cost)",
			got.StartupCost, got.TotalCost, want)
	}
}

// TestInlinedCTELeafPricesCostSubqueryscanOnce pins M0146-0214 slice 3: TPC-DS
// Q64's cs_ui is an inlined CTE whose body is a grouped query with a HAVING —
// CTEScan → Project → Filter → Aggregate. PG plans the reference as a subquery
// (inline_cte) and prices it with cost_subqueryscan over the Agg path, HAVING
// included: one cpu_tuple_cost per row in all. goopg charged one at each of
// the CTEScan, the Project and the Filter as well, so every GEQO tour holding
// cs_ui priced 0.1 above PG's at SF0.25.
func TestInlinedCTELeafPricesCostSubqueryscanOnce(t *testing.T) {
	cp := defaultCostParams()
	agg := &Aggregate{}
	agg.setPlanCost(PlanCost{StartupCost: 100, TotalCost: 200, PlanRows: 22})
	having := &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 0}, Right: &ColumnRef{Index: 1}}
	body := &Project{Child: &Filter{Child: agg, Predicate: having}}
	ce := &plannedCTE{inlineEligible: true, selectOwned: true, refs: 1}
	leaf := &CTEScan{Child: body, cte: ce}
	if !leaf.Inlined() {
		t.Fatal("fixture: the CTE reference should be inlinable")
	}
	got := costSubplanLeaf(cp, leaf, 7)
	want := 200 + 1*cp.cpuOperatorCost*22 + cp.cpuTupleCost*7
	if math.Abs(got.Startup-100) > 1e-9 || math.Abs(got.Total-want) > 1e-9 {
		t.Fatalf("inlined CTE leaf: got %.6f..%.6f, want 100..%.6f", got.Startup, got.Total, want)
	}
	// A kept (shared) CTE is cost_ctescan's, not cost_subqueryscan's: no peel.
	kept := &CTEScan{Child: body, cte: &plannedCTE{inlineEligible: true, selectOwned: true, refs: 2}}
	if kept.Inlined() {
		t.Fatal("fixture: a twice-referenced CTE should not be inlined")
	}
	if sub := subplanLeafSubpathCost(kept); math.Abs(sub.TotalCost-legacyDisplayCostOf(kept).TotalCost) > 1e-9 {
		t.Fatalf("kept CTE: got %.6f, want the CTEScan's own %.6f", sub.TotalCost, legacyDisplayCostOf(kept).TotalCost)
	}
}
