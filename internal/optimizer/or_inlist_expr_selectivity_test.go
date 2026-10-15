package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestOrInListExprSelectivity pins M0146-0208. TPC-DS Q45's Join Filter is
// `substr(ca_zip, 1, 5) IN (…9 constants…) OR i_item_id IN (SELECT …)`. PG
// prices the first arm as a restriction: scalararraysel → eqsel →
// var_eq_const, with no statistics for the expression, gives 1/200 per element,
// so the disjoint sum is 9 × 0.005. The SubPlan arm gets boolvarsel's 0.5
// (clausesel.c:956), so the OR is 0.045 + 0.5 − 0.0225. goopg declined the
// expression operand to the whole-OR 0.5 and computed 0.75.
func TestOrInListExprSelectivity(t *testing.T) {
	s, _, _ := jsNationCtx(t)
	col := func(src int16) Expr {
		return &ColumnRef{Index: 0, Name: "n_name", Type: catalog.Type{Name: "bpchar"}, SourceTableIdx: src}
	}
	substr := &FuncCall{Name: "substr", Args: []Expr{col(1), &IntegerConst{Value: 1}, &IntegerConst{Value: 5}}}
	var list []Expr
	for _, v := range []string{"85669", "86197", "88274", "83405", "86475", "85392", "85460", "80348", "81792"} {
		list = append(list, &StringConst{Value: v})
	}
	in := &InExpr{Operand: substr, List: list}

	sel, isDefault := s.orConjunctSelectivity(in)
	if want := 9 * defaultEqSelectivity; math.Abs(sel-want) > 1e-12 {
		t.Fatalf("expression IN-list: sel=%v, want 9 × 1/200 = %v", sel, want)
	}
	if !isDefault {
		t.Fatal("var_eq_const's no-statistics branch is a guess; the arm must say so")
	}
	// The restriction path prices the same clause identically (sibling paths).
	if got := inListExprSelectivity(in, nil); math.Abs(got-sel) > 1e-15 {
		t.Fatalf("restriction sibling %v != OR arm %v", got, sel)
	}

	// The whole OR with the hashed-SubPlan arm: s1 + 0.5 − 0.5·s1.
	sub := &InExpr{Operand: col(2), List: nil, Plan: &SeqScan{}}
	or := &BinaryOp{Op: parser.OpOr, Left: in, Right: sub}
	got, _ := s.orJoinSelectivity(or)
	if want := 0.045 + 0.5 - 0.045*0.5; math.Abs(got-want) > 1e-12 {
		t.Fatalf("Q45 OR: sel=%v, want %v (PG 0.524 at SF1)", got, want)
	}

	// An operand over two relations is a join expression: still declined.
	cross := &InExpr{Operand: &BinaryOp{Op: parser.OpConcat, Left: col(1), Right: col(2)}, List: list}
	if sel, def := s.orConjunctSelectivity(cross); sel != defaultUnhandledClauseSel || !def {
		t.Fatalf("two-relation operand: sel=%v default=%v, want the unchanged 0.5 guess", sel, def)
	}
	// So is a non-equality ANY over an expression.
	rng := &InExpr{Operand: substr, List: list, AnyOp: parser.OpLt}
	if sel, _ := s.orConjunctSelectivity(rng); sel != defaultUnhandledClauseSel {
		t.Fatalf("range ANY over an expression: sel=%v, want the unchanged 0.5", sel)
	}
}
