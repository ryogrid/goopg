package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestInListExprOperandSelectivity pins M0145-0008g: an IN list over a
// non-column operand is scalararraysel — the element operator's own estimate
// per element, merged. PG 18.3 estimates TPC-H Q22's
// `substr(c_phone, 1, 2) IN (7 values)` at 5250 of 150000 customer rows
// (0.035 = 7 × eqsel's no-statistics 1/DEFAULT_NUM_DISTINCT). goopg returned
// the generic 1/3 there, which priced PG's NL anti join out of Q22. Both
// estimator arms (clauseSelectivity and clauseSelectivityWithSource) must
// agree, and NOT IN is the complement.
func TestInListExprOperandSelectivity(t *testing.T) {
	substr := &FuncCall{Name: "substr", Args: []Expr{
		&ColumnRef{Index: 0, Name: "c_phone"}, &IntegerConst{Value: 1}, &IntegerConst{Value: 2}}}
	list := []Expr{}
	for _, v := range []string{"13", "31", "23", "29", "30", "18", "17"} {
		list = append(list, &StringConst{Value: v})
	}
	in := &InExpr{Operand: substr, List: list}
	want := 7 * defaultEqSelectivity
	if got := clauseSelectivity(in, nil); math.Abs(got-want) > 1e-12 {
		t.Errorf("clauseSelectivity = %v, want %v (PG scalararraysel 7 × 1/200)", got, want)
	}
	ws := clauseSelectivityWithSource(in, nil)
	if math.Abs(ws.value-want) > 1e-12 || ws.reliable {
		t.Errorf("clauseSelectivityWithSource = %+v, want %v unreliable", ws, want)
	}
	notIn := &InExpr{Operand: substr, List: list, Negated: true}
	if got := clauseSelectivity(notIn, nil); math.Abs(got-(1-want)) > 1e-12 {
		t.Errorf("NOT IN = %v, want %v", got, 1-want)
	}
	// The per-element estimate is the written-out comparison's.
	eq := clauseSelectivity(&BinaryOp{Op: parser.OpEq, Left: substr, Right: list[0]}, nil)
	one := clauseSelectivity(&InExpr{Operand: substr, List: list[:1]}, nil)
	if eq != one {
		t.Errorf("one-element IN = %v, written-out = %v; must agree", one, eq)
	}
}
