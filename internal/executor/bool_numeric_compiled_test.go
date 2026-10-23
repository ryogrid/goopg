package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCompiledBoolAndNumericIntMatchInterpreter pins M0122-0012 slice 84: the
// compiled evaluator applies Kleene AND / OR to two bool-or-NULL operands
// itself, and compares a numeric with an int in place (fastCompareDatum's
// numeric / int arm). Both must answer exactly what the interpreted twin
// answers — every comparison operator in both operand orders, scales 0–4,
// negative values, a big-mantissa numeric (which takes the general path) and
// NULLs, and AND / OR over every pairing of true, false and NULL.
func TestCompiledBoolAndNumericIntMatchInterpreter(t *testing.T) {
	num := &optimizer.ColumnRef{Index: 0, Name: "n", Type: catalog.Type{Name: "numeric"}}
	itg := &optimizer.ColumnRef{Index: 1, Name: "i", Type: catalog.Type{Name: "int4"}}
	b1 := &optimizer.ColumnRef{Index: 2, Name: "b1", Type: catalog.Type{Name: "bool"}}
	b2 := &optimizer.ColumnRef{Index: 3, Name: "b2", Type: catalog.Type{Name: "bool"}}
	huge, _ := new(big.Int).SetString("98765432109876543210987654321", 10)
	nums := []Datum{
		{Kind: KindNumeric, Int: 10700, Scale: 2}, {Kind: KindNumeric, Int: 107, Scale: 0},
		{Kind: KindNumeric, Int: -1070001, Scale: 4}, {Kind: KindNumeric, Int: 1069999, Scale: 4},
		NewNumericBigDatum(huge, 3), NullDatum,
	}
	ints := []Datum{NewIntDatum(107), NewIntDatum(-107), NewIntDatum(0), NullDatum}
	bools := []Datum{NewBoolDatum(true), NewBoolDatum(false), NullDatum}
	var exprs []optimizer.Expr
	for _, op := range []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe} {
		exprs = append(exprs,
			&optimizer.BinaryOp{Op: op, Left: num, Right: itg, ResultType: "bool"},
			&optimizer.BinaryOp{Op: op, Left: itg, Right: num, ResultType: "bool"},
			&optimizer.BinaryOp{Op: op, Left: num, Right: &optimizer.IntegerConst{Value: 107}, ResultType: "bool"})
	}
	for _, op := range []parser.OpCode{parser.OpAnd, parser.OpOr} {
		exprs = append(exprs,
			&optimizer.BinaryOp{Op: op, Left: b1, Right: b2, ResultType: "bool"},
			&optimizer.BinaryOp{Op: op, ResultType: "bool",
				Left:  &optimizer.BinaryOp{Op: parser.OpGe, Left: num, Right: itg, ResultType: "bool"},
				Right: &optimizer.BinaryOp{Op: parser.OpLe, Left: num, Right: &optimizer.IntegerConst{Value: 200}, ResultType: "bool"}})
	}
	checked := 0
	for ei, e := range exprs {
		var slab exprTreeSlab
		idx := slab.buildExpr(e)
		for _, n := range nums {
			for _, i := range ints {
				for _, x := range bools {
					for _, y := range bools {
						row := Row{n, i, x, y}
						slot := &MaterializedSlot{row: row}
						want, werr := evalExprSlot(e, slot, nil)
						got, gerr := evalFastExpr(slab, idx, slot, nil)
						if !sameResult(got, gerr, want, werr) {
							t.Fatalf("expr %d row %v: compiled %v (%v), interpreted %v (%v)", ei, row, got, gerr, want, werr)
						}
						checked++
					}
				}
			}
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d evaluations", checked)
	}
}
