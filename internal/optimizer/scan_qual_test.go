package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanScanQualMaxColsSurvivesIneligibility is the regression pin for the
// mechanism error the E-17 design review caught: expressing early-ineligibility
// as a `false` return from exprVisitor.Visit PRUNES the node's children without
// aborting (exprwalk.go:333-335), so a ColumnRef beneath an ineligible node
// would never be visited and MaxCols would come back too small — while Early
// stayed true. The scan would then judge the qual against a prefix that does
// not cover the column it reads, i.e. against the PREVIOUS tuple's datum.
//
// The invariant: whenever Early is true, MaxCols must cover EVERY ColumnRef in
// the qual, including ones buried under a node that is itself ineligible.
func TestPlanScanQualMaxColsSurvivesIneligibility(t *testing.T) {
	col := func(idx int) Expr {
		return &ColumnRef{Index: idx, Name: "c", Type: catalog.Type{Name: "int4"}}
	}
	and := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpAnd, Left: l, Right: r} }
	lt := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpLt, Left: l, Right: r} }
	lit := func() Expr { return &IntegerConst{Value: 1} }

	cases := []struct {
		name string
		qual Expr
	}{
		// Each buries a HIGH column index under an ineligible node. If the
		// walk pruned there, maxIdx would be 1 and Early could stay true.
		{"funccall wrapping a high column",
			and(lt(col(1), lit()), lt(&FuncCall{Name: "abs", Args: []Expr{col(9)}}, lit()))},
		{"subquery arg carrying a high column",
			and(lt(col(1), lit()), lt(&SubqueryExpr{Args: []Expr{col(9)}}, lit()))},
		{"ctid ref beside a high column",
			and(lt(col(9), lit()), lt(&CTIDExpr{}, lit()))},
		{"param ref beside a high column",
			and(lt(col(9), lit()), lt(&ParamRef{}, lit()))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanScanQual(tc.qual, 16)
			if got.Early {
				// If a future change makes one of these eligible, the bound
				// must still cover column 9 — that is the real invariant.
				if got.MaxCols <= 9 {
					t.Fatalf("Early with MaxCols=%d does not cover column 9 — "+
						"the walk pruned instead of descending", got.MaxCols)
				}
			}
		})
	}
}

// TestPlanScanQualFailsClosed pins the DIRECTION of every decline: an
// expression the analysis cannot judge from a bare prefix must report
// Early=false, which routes the qual to the scan's LATE position. A false
// negative costs performance; a false positive is a wrong answer.
func TestPlanScanQualFailsClosed(t *testing.T) {
	col := func(idx int) Expr {
		return &ColumnRef{Index: idx, Name: "c", Type: catalog.Type{Name: "int4"}}
	}
	lt := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpLt, Left: l, Right: r} }
	lit := func() Expr { return &IntegerConst{Value: 1} }

	ineligible := []struct {
		name string
		qual Expr
	}{
		{"nil qual", nil},
		{"constant only, reads no column", lt(lit(), lit())},
		{"reads the last column, nothing saved", lt(col(15), lit())},
		{"reads past the row", lt(col(99), lit())},
		{"function call", lt(&FuncCall{Name: "random"}, col(1))},
		// A bare subquery node has a nil Plan, so exprChildSlots reports NO
		// slotInnerPlan slot and the OnScope hook never fires. Relying on
		// OnScope alone was fail-OPEN here; the named arms are what catch it.
		{"subquery with nil plan", lt(col(1), &SubqueryExpr{})},
		{"exists with nil plan", lt(col(1), &ExistsExpr{})},
		{"array subquery with nil plan", lt(col(1), &ArraySubqueryExpr{})},
		{"outer column ref", lt(col(1), &OuterColumnRef{Level: 1})},
		{"param ref", lt(col(1), &ParamRef{})},
		{"exec param ref", lt(col(1), &ExecParamRef{})},
		{"ctid", lt(col(1), &CTIDExpr{})},
		{"table oid", lt(col(1), &TableOidExpr{})},
	}
	for _, tc := range ineligible {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlanScanQual(tc.qual, 16); got.Early {
				t.Fatalf("Early=true (MaxCols=%d), want false — an unjudgeable "+
					"qual must fall to the scan's LATE position", got.MaxCols)
			}
		})
	}

	eligible := []struct {
		name string
		qual Expr
		want int
	}{
		{"single column compare", lt(col(2), lit()), 3},
		{"two columns, highest wins",
			&BinaryOp{Op: parser.OpAnd, Left: lt(col(5), lit()), Right: lt(col(0), lit())}, 6},
		{"cast wrapping a column",
			lt(&CastExpr{Operand: col(4), TargetType: "int8"}, lit()), 5},
		{"is null", &IsNullExpr{Operand: col(2)}, 3},
	}
	for _, tc := range eligible {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanScanQual(tc.qual, 16)
			if !got.Early || got.MaxCols != tc.want {
				t.Fatalf("got Early=%v MaxCols=%d, want Early=true MaxCols=%d",
					got.Early, got.MaxCols, tc.want)
			}
		})
	}
}
