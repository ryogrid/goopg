package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestProvePartialIndexPredicateNullTest pins predicate_implied_by_simple_
// clause's NullTest rules (M0148-0001c3): a NullTest implies itself, and
// `x IS NOT NULL` is implied by a clause strict for x — never by a
// non-strict one, and `x IS NULL` by nothing but itself.
func TestProvePartialIndexPredicateNullTest(t *testing.T) {
	x := func() Expr { return &ColumnRef{Name: "x"} }
	y := func() Expr { return &ColumnRef{Name: "y", Index: 1} }
	notNull := func(e Expr) Expr { return &IsNullExpr{Operand: e, Negated: true} }
	isNull := func(e Expr) Expr { return &IsNullExpr{Operand: e} }
	bin := func(op parser.OpCode, l, r Expr) Expr { return &BinaryOp{Op: op, Left: l, Right: r} }
	one := func() Expr { return &IntegerConst{Value: 1} }
	for _, tc := range []struct {
		name        string
		pred, claus Expr
		want        bool
	}{
		{"IS NOT NULL implies itself", notNull(x()), notNull(x()), true},
		{"IS NULL implies itself", isNull(x()), isNull(x()), true},
		{"comparison is strict", notNull(x()), bin(parser.OpGt, x(), one()), true},
		{"arithmetic under a comparison", notNull(x()), bin(parser.OpEq, bin(parser.OpAdd, x(), one()), one()), true},
		{"cast is strict", notNull(x()), bin(parser.OpLt, &CastExpr{Operand: x(), TargetType: "int8"}, one()), true},
		{"other column", notNull(x()), bin(parser.OpGt, y(), one()), false},
		{"IS NULL clause does not imply IS NOT NULL", notNull(x()), isNull(x()), false},
		{"IS NULL predicate is not implied by a strict clause", isNull(x()), bin(parser.OpGt, x(), one()), false},
		{"OR is not strict", notNull(x()), bin(parser.OpOr, bin(parser.OpGt, x(), one()), bin(parser.OpGt, y(), one())), false},
		{"concat is not proven strict", notNull(x()), bin(parser.OpEq, bin(parser.OpConcat, x(), one()), one()), false},
		{"negation mismatch", isNull(x()), notNull(x()), false},
	} {
		if got := provePartialIndexPredicate(tc.pred, tc.claus); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
