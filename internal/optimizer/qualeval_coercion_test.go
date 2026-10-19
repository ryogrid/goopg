package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestQualEvalPricesNumericCoercions pins M0146-0228 slice 2: cost_qual_eval
// prices a coercion by the function that performs it, and every numeric-family
// cast (int4_numeric, i4tod, numeric_float8, int48, ...) is a function cast of
// procost 1. TPC-DS Q23's sum(ss_quantity*ss_sales_price) multiplies an int4
// by a numeric: PG evaluates int4_numeric(ss_quantity) then numeric_mul, two
// operators per row, where goopg counted one. Constant operands are folded by
// eval_const_expressions and cost nothing.
func TestQualEvalPricesNumericCoercions(t *testing.T) {
	col := func(i int, typ string) Expr { return &ColumnRef{Index: i, Name: "c", Type: catalog.Type{Name: typ}} }
	mul := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpMul, Left: l, Right: r} }
	cases := []struct {
		name string
		e    Expr
		want float64
	}{
		{"int4 * numeric coerces the int4", mul(col(0, "int4"), col(1, "numeric")), 2},
		{"numeric * int4 coerces the int4", mul(col(1, "numeric"), col(0, "int4")), 2},
		{"int4 * float8 coerces the int4", mul(col(0, "int4"), col(1, "float8")), 2},
		{"numeric * float8 coerces the numeric", mul(col(0, "numeric"), col(1, "float8")), 2},
		{"int4 * int8 has a cross-type operator", mul(col(0, "int4"), col(1, "int8")), 1},
		{"float4 * float8 has a cross-type operator", mul(col(0, "float4"), col(1, "float8")), 1},
		{"numeric * integer constant folds the constant", mul(col(0, "numeric"), &IntegerConst{Value: 2}), 1},
		{"explicit int4 -> numeric cast of a column", &CastExpr{Operand: col(0, "int4"), SourceType: "int4", TargetType: "numeric"}, 1},
		{"cast of a constant is folded", &CastExpr{Operand: &IntegerConst{Value: 7}, SourceType: "int4", TargetType: "numeric"}, 0},
		{"text-family cast stays free", &CastExpr{Operand: col(0, "text"), SourceType: "text", TargetType: "varchar"}, 0},
	}
	for _, c := range cases {
		if _, got := qualEvalOps(c.e); got != c.want {
			t.Errorf("%s: %v ops, want %v", c.name, got, c.want)
		}
	}
}
