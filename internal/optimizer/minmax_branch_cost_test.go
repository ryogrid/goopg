package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0148-0001c2 arm (3): a gating Result — a One-Time Filter over a child —
// costs exactly its child (createplan.c create_gating_plan →
// copy_plan_costsize), while a projecting Result still charges
// cpu_tuple_cost per row. PG prints the `max(100)` InitPlan's
// `Result  (cost=0.00..155.00 rows=10000)` over `Seq Scan  (cost=0.00..155.00)`.
func TestGatingResultCopiesChildCost(t *testing.T) {
	seq := &SeqScan{Table: &catalog.Table{Name: "tk"}}
	seq.setPlanCost(PlanCost{StartupCost: 0, TotalCost: 155, PlanRows: 10000, PlanWidth: 4})
	gating := &Result{Child: seq, OneTimeFilter: &BooleanConst{Value: true}, Targets: []Expr{&IntegerConst{Value: 100}}}
	pc := DeriveLegacyDisplayCost(gating, 10000)
	if pc.StartupCost != 0 || math.Abs(pc.TotalCost-155) > 1e-9 {
		t.Fatalf("gating Result cost %.2f..%.2f, want the child's 0.00..155.00", pc.StartupCost, pc.TotalCost)
	}
	projecting := &Result{Child: seq, Targets: []Expr{&IntegerConst{Value: 100}}}
	if pc := DeriveLegacyDisplayCost(projecting, 10000); math.Abs(pc.TotalCost-(155+0.01*10000)) > 1e-9 {
		t.Fatalf("projecting Result total %.2f, want child + cpu_tuple_cost per row (255.00)", pc.TotalCost)
	}
}

// stampMinMaxSeqScanCost is cost_seqscan: pages at seq_page_cost, tuples at
// cpu_tuple_cost plus the quals' operator cost (a NullTest is free), rows the
// restricted relation's, clamped to 1.
func TestStampMinMaxSeqScanCost(t *testing.T) {
	tbl := &catalog.Table{Name: "i4", Columns: []catalog.Column{{Name: "f1", Type: catalog.Type{Name: "int4"}}},
		Stats: &catalog.TableStats{RowCount: 5, Pages: 1, Analyzed: true}}
	f := &Filter{}
	pred := andExpr(0, &IsNullExpr{Operand: &ColumnRef{Name: "f1", Type: catalog.Type{Name: "int4"}}, Negated: true},
		&BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Name: "f1", Type: catalog.Type{Name: "int4"}}, Right: &IntegerConst{Value: 5}})
	stampMinMaxSeqScanCost(f, nil, defaultCostParams(), tbl, pred, 0.2, 4)
	pc, ok := f.PlanCostInfo()
	if !ok {
		t.Fatal("no cost stamped")
	}
	// 1 page + 5 tuples * (0.01 + 1 operator * 0.0025) = 1.0625; rows 5 * 0.2 = 1.
	if math.Abs(pc.TotalCost-1.0625) > 1e-9 || pc.StartupCost != 0 || pc.PlanRows != 1 || pc.PlanWidth != 4 {
		t.Fatalf("stamped %+v, want 0.00..1.0625 rows=1 width=4 (PG 18.3's 0.00..1.06)", pc)
	}
}
