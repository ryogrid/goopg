package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestECExpressionMemberDerivesJoinClause pins M0146-0005do against PG 18.3's
// process_equivalence / generate_join_implied_equalities: a non-volatile
// expression over one relation is an equivalence-class member, so
// `a = b - 52 AND a = c` derives `(b - 52) = c` (TPC-DS Q59's
// `(wss_1.d_week_seq - 52) = d.d_week_seq`), and a constant on the class
// reaches the expression member too.
func TestECExpressionMemberDerivesJoinClause(t *testing.T) {
	i4 := catalog.Type{Name: "int4"}
	i8 := catalog.Type{Name: "int8"}
	a := &ColumnRef{Name: "a", Index: 0, Type: i4, SourceTableIdx: 1}
	b := &ColumnRef{Name: "b", Index: 2, Type: i4, SourceTableIdx: 2}
	c := &ColumnRef{Name: "c", Index: 4, Type: i4, SourceTableIdx: 3}
	d8 := &ColumnRef{Name: "d", Index: 6, Type: i8, SourceTableIdx: 4}
	eq := func(l, r Expr) Expr { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
	// The resolver types `int4 - 52` int8 (an integer literal is int8 here).
	minus := &BinaryOp{Op: parser.OpSub, Left: b, Right: &IntegerConst{Value: 52}, ResultType: "int8"}

	has := func(out []Expr, l, r Expr) bool {
		lk, _ := exprIdentityKey(l, scopeVeto)
		rk, _ := exprIdentityKey(r, scopeVeto)
		for _, e := range out {
			bo := e.(*BinaryOp)
			x, _ := exprIdentityKey(bo.Left, scopeVeto)
			y, _ := exprIdentityKey(bo.Right, scopeVeto)
			if (x == lk && y == rk) || (x == rk && y == lk) {
				return true
			}
		}
		return false
	}

	out := inferTransitiveEqualities([]Expr{eq(a, minus), eq(a, c)})
	if !has(out, minus, c) {
		t.Errorf("a = b-52, a = c: (b - 52) = c not derived; got %d clauses", len(out))
	}
	seven := &IntegerConst{Value: 7}
	out = inferTransitiveEqualities([]Expr{eq(a, minus), eq(a, c), eq(a, seven)})
	if !has(out, minus, seven) || !has(out, c, seven) {
		t.Errorf("class constant not propagated to every member")
	}

	// Not members: an expression over two relations, a function call.
	two := &BinaryOp{Op: parser.OpAdd, Left: b, Right: c, ResultType: "int4"}
	if out := inferTransitiveEqualities([]Expr{eq(a, two), eq(a, d8)}); len(out) != 0 {
		t.Errorf("two-relation expression joined a class: %d clauses", len(out))
	}
	fn := &FuncCall{Name: "random"}
	if _, ok := ecMemberIdent(fn); ok {
		t.Error("function call accepted as a class member")
	}
	// A cross-type COLUMN pair stays out (isColumnRefEquality's rule); the
	// integer-family widening is for expression members only.
	if _, _, ok := ecEquality(eq(a, d8)); ok {
		t.Error("int4 = int8 column pair accepted")
	}
	if _, _, ok := ecEquality(eq(a, minus)); !ok {
		t.Error("int4 column = int8 expression member rejected")
	}
}
