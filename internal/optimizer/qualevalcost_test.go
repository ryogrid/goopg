package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestQualEvalOpsMatchesCostQualEval pins M0146-0005ba against PG 18.3's
// cost_qual_eval. TPC-DS Q47's filter on the `v1` CTE scan —
// `d_year = 2000 AND avg > 0 AND CASE WHEN avg > 0 THEN abs(sum - avg) / avg
// ELSE NULL END > 0.1` — is seven operators and functions (PG prices the scan
// at 3850 × (2 × 0.01 + 7 × 0.0025) = 144.38); AND and CASE are free. An IN
// list of fewer than nine constants costs half its length per row; nine or
// more are hashed — two per row and the list's length once at startup.
func TestQualEvalOpsMatchesCostQualEval(t *testing.T) {
	col := func(i int) Expr { return &ColumnRef{Index: i} }
	num := func(v int64) Expr { return &IntegerConst{Value: v} }
	op := func(o parser.OpCode, l, r Expr) Expr { return &BinaryOp{Op: o, Left: l, Right: r} }

	caseExpr := &CaseExpr{
		Whens: []CaseWhen{{
			When: op(parser.OpGt, col(1), num(0)),
			Then: op(parser.OpDiv, &FuncCall{Name: "abs", Args: []Expr{op(parser.OpSub, col(2), col(1))}}, col(1)),
		}},
		Else: &NullConst{},
	}
	q47 := op(parser.OpAnd,
		op(parser.OpAnd, op(parser.OpEq, col(0), num(2000)), op(parser.OpGt, col(1), num(0))),
		op(parser.OpGt, caseExpr, num(0)))
	if s, p := qualEvalOps(q47); s != 0 || p != 7 {
		t.Fatalf("Q47 filter = (%v, %v) operators, want (0, 7)", s, p)
	}

	list := func(n int) Expr {
		xs := make([]Expr, n)
		for i := range xs {
			xs[i] = num(int64(i))
		}
		return &InExpr{Operand: col(0), AnyOp: parser.OpEq, List: xs}
	}
	if s, p := qualEvalOps(list(8)); s != 0 || p != 4 {
		t.Fatalf("8-element IN list = (%v, %v), want linear (0, 4)", s, p)
	}
	if s, p := qualEvalOps(list(9)); s != 9 || p != 2 {
		t.Fatalf("9-element IN list = (%v, %v), want hashed (9, 2)", s, p)
	}
}
