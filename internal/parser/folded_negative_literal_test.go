package parser

import "testing"

// TestFoldedNegativeLiteralSQL pins get_const_expr's rendering of a folded
// negative constant (the grammar's doNegate turns `-5` into IntegerConst{-5}):
// the quoted value plus a cast typed from the literal itself (make_const) —
// int4 down to INT_MIN, int8 below, numeric for a decimal. Expected strings are
// pg_dump 18.3's for the same CHECK / DEFAULT expressions.
func TestFoldedNegativeLiteralSQL(t *testing.T) {
	for _, c := range []struct {
		e    Expr
		want string
	}{
		{&IntegerConst{Value: -5}, "'-5'::integer"},
		{&IntegerConst{Value: -2147483648}, "'-2147483648'::integer"},
		{&IntegerConst{Value: -2147483649}, "'-2147483649'::bigint"},
		{&IntegerConst{Value: -9000000000}, "'-9000000000'::bigint"},
		{&NumericConst{Value: "-3.5"}, "'-3.5'::numeric"},
		{&IntegerConst{Value: 5}, ""},
		{&IntegerConst{Value: 0}, ""},
		{&NumericConst{Value: "3.5"}, ""},
		{&StringConst{Value: "-5"}, ""},
	} {
		if got := FoldedNegativeLiteralSQL(c.e); got != c.want {
			t.Errorf("FoldedNegativeLiteralSQL(%#v) = %q, want %q", c.e, got, c.want)
		}
	}
}
