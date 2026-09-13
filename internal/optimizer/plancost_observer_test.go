package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestDeriveLegacyDisplayCostObservedCapturesChildProvenance(t *testing.T) {
	stamped := &pricedNode{sch: Schema{{Name: "s", Type: catalog.Type{Name: "int4"}}}}
	stamped.setPlanCost(PlanCost{StartupCost: 3, TotalCost: 7, PlanRows: 9, PlanWidth: 4})
	legacy := &Values{Rows: [][]Expr{{&IntegerConst{Value: 1}}, {&IntegerConst{Value: 2}}}, schema: Schema{{Name: "v", Type: catalog.Type{Name: "int4"}}}}
	j := &Join{Type: JoinTypeInner, Left: stamped, Right: legacy}

	plain := DeriveLegacyDisplayCost(j, EstimateRows(j))
	var got []LegacyDisplayCostObservation
	seen := DeriveLegacyDisplayCostObserved(j, EstimateRows(j), func(v LegacyDisplayCostObservation) { got = append(got, v) })
	if plain != seen {
		t.Fatalf("observed cost %+v, want normal %+v", seen, plain)
	}
	if len(got) != 2 { // Values fallback, then its Join parent.
		t.Fatalf("observer callbacks = %d, want 2", len(got))
	}
	top := got[1]
	if top.Node != j || len(top.Children) != 2 {
		t.Fatalf("top observation = %+v, want join with two children", top)
	}
	if top.Children[0].Node != stamped || top.Children[0].Source != LegacyDisplayCostSourceStamped || top.Children[0].Cost.TotalCost != 7 {
		t.Fatalf("stamped child = %+v", top.Children[0])
	}
	if top.Children[1].Node != legacy || top.Children[1].Source != LegacyDisplayCostSourceLegacy || top.Children[1].Cost.PlanRows != 2 {
		t.Fatalf("legacy child = %+v", top.Children[1])
	}
	if !approx(top.ChildTotal, 7.02) || !approx(top.PerRow, 0) || !approx(top.Cost.TotalCost, plain.TotalCost) {
		t.Fatalf("ledger %+v does not preserve normal arithmetic", top)
	}
}

func TestLegacyDisplayCostObservedNilChild(t *testing.T) {
	j := &Join{Type: JoinTypeInner, Left: nil, Right: nil}
	var got LegacyDisplayCostObservation
	DeriveLegacyDisplayCostObserved(j, 0, func(v LegacyDisplayCostObservation) { got = v })
	if len(got.Children) != 2 || got.Children[0].Source != LegacyDisplayCostSourceNil || got.Children[1].Source != LegacyDisplayCostSourceNil {
		t.Fatalf("nil children = %+v", got.Children)
	}
}

func TestLegacyDisplayCostSourceNamesAreClosed(t *testing.T) {
	for source, want := range map[LegacyDisplayCostSource]string{
		LegacyDisplayCostSourceInvalid: "invalid",
		LegacyDisplayCostSourceNil:     "nil",
		LegacyDisplayCostSourceStamped: "stamped",
		LegacyDisplayCostSourceLegacy:  "legacy",
	} {
		if got := source.String(); got != want {
			t.Fatalf("source %d string = %q, want %q", source, got, want)
		}
	}
	if got := LegacyDisplayCostSource(99).String(); got != "invalid" {
		t.Fatalf("unknown source = %q, want invalid", got)
	}
}

func TestDeriveLegacyDisplayCostNilObserverDoesNotAllocate(t *testing.T) {
	j := &Join{Type: JoinTypeInner}
	before := DeriveLegacyDisplayCost(j, 0)
	// A Join's pre-existing legacy child walker allocates its two-element
	// slice. The regression is that the nil observer adds nothing beyond it.
	baseline := testing.AllocsPerRun(1000, func() { _ = legacyDisplayChildren(j) })
	allocs := testing.AllocsPerRun(1000, func() {
		if got := DeriveLegacyDisplayCost(j, 0); got != before {
			t.Fatalf("normal cost changed: got %+v want %+v", got, before)
		}
	})
	if allocs != baseline {
		t.Fatalf("nil observer path allocates %.2f objects/run, baseline %.2f", allocs, baseline)
	}
}

func TestLegacyDisplayCostObserverSnapshotsScalars(t *testing.T) {
	child := &pricedNode{sch: Schema{{Name: "s", Type: catalog.Type{Name: "int4"}}}}
	child.setPlanCost(PlanCost{StartupCost: 11, TotalCost: 13, PlanRows: 17, PlanWidth: 19})
	j := &Join{Type: JoinTypeInner, Left: child}
	var snapshot struct{ total, rows float64 }
	DeriveLegacyDisplayCostObserved(j, 23, func(v LegacyDisplayCostObservation) {
		if v.Node == j {
			snapshot.total = v.Children[0].Cost.TotalCost
			snapshot.rows = v.Children[0].Cost.PlanRows
		}
	})
	child.setPlanCost(PlanCost{TotalCost: 101, PlanRows: 103})
	if snapshot.total != 13 || snapshot.rows != 17 {
		t.Fatalf("callback scalar snapshot changed after node mutation: %+v", snapshot)
	}
}

func TestDeriveLegacyDisplayCostObservedExtremeLedger(t *testing.T) {
	left := &pricedNode{sch: Schema{{Name: "l", Type: catalog.Type{Name: "numeric"}}}}
	right := &pricedNode{sch: Schema{{Name: "r", Type: catalog.Type{Name: "text"}}}}
	left.setPlanCost(PlanCost{StartupCost: math.MaxFloat64, TotalCost: math.MaxFloat64, PlanRows: math.MaxFloat64, PlanWidth: math.MaxInt})
	right.setPlanCost(PlanCost{StartupCost: math.MaxFloat64, TotalCost: math.MaxFloat64, PlanRows: math.MaxFloat64, PlanWidth: math.MaxInt})
	j := &Join{Type: JoinTypeInner, Left: left, Right: right}
	plain := DeriveLegacyDisplayCost(j, math.MaxInt64)
	var ledger LegacyDisplayCostObservation
	seen := DeriveLegacyDisplayCostObserved(j, math.MaxInt64, func(v LegacyDisplayCostObservation) {
		if v.Node == j {
			ledger = v
		}
	})
	if plain != seen || !math.IsInf(ledger.ChildTotal, 1) || ledger.ChildStartup != math.MaxFloat64 || !math.IsInf(ledger.Cost.TotalCost, 1) || math.IsInf(ledger.PerRow, 0) || math.IsNaN(seen.TotalCost) || math.IsNaN(seen.StartupCost) {
		t.Fatalf("extreme observed ledger %+v, want normal non-NaN %+v", seen, plain)
	}
}
