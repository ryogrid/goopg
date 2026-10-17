package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestFoldConstantsConditionalAndNullTests pins M0146-0124: the
// eval_const_expressions arms for CoalesceExpr, NullIfExpr, MinMaxExpr,
// NullTest and BooleanTest over constants (clauses.c). goopg spells the first
// three as FuncCalls; every arm folds only when the non-null arguments share
// one exact type, so the kept argument already has the expression's type.
func TestFoldConstantsConditionalAndNullTests(t *testing.T) {
	i := func(v int64) Expr { return &IntegerConst{Value: v} }
	col := &ColumnRef{Name: "b", Type: catalog.Type{Name: "int4"}}
	null := &NullConst{}
	call := func(name string, args ...Expr) *FuncCall { return &FuncCall{Name: name, Args: args} }
	intOf := func(e Expr) (int64, bool) {
		c, ok := e.(*IntegerConst)
		if !ok {
			return 0, false
		}
		return c.Value, true
	}

	if v, ok := intOf(FoldConstants(call("coalesce", null, i(5)))); !ok || v != 5 {
		t.Errorf("COALESCE(NULL, 5) = %#v, want 5", FoldConstants(call("coalesce", null, i(5))))
	}
	if v, ok := intOf(FoldConstants(call("coalesce", i(4), col))); !ok || v != 4 {
		t.Errorf("COALESCE(4, b) did not fold to 4")
	}
	got, ok := FoldConstants(call("coalesce", null, col, null, i(7), i(8))).(*FuncCall)
	if !ok || len(got.Args) != 2 || got.Args[0] != Expr(col) {
		t.Fatalf("COALESCE(NULL, b, NULL, 7, 8) = %#v, want COALESCE(b, 7)", got)
	}
	if v, ok := intOf(got.Args[1]); !ok || v != 7 {
		t.Errorf("COALESCE kept %#v after b, want 7", got.Args[1])
	}
	if v, ok := intOf(FoldConstants(call("greatest", i(1), i(3), i(2)))); !ok || v != 3 {
		t.Errorf("GREATEST(1, 3, 2) did not fold to 3")
	}
	if v, ok := intOf(FoldConstants(call("least", i(5), null, i(3)))); !ok || v != 3 {
		t.Errorf("LEAST(5, NULL, 3) did not fold to 3")
	}
	if v, ok := intOf(FoldConstants(call("nullif", i(1), i(2)))); !ok || v != 1 {
		t.Errorf("NULLIF(1, 2) did not fold to 1")
	}
	// regress case.sql: NULLIF(1, NULL) is 1 (a NULL argument cannot be
	// equal), NULLIF(1, 1) a NULL of type int4, NULLIF(b, NULL) is b.
	if v, ok := intOf(FoldConstants(call("nullif", i(1), null))); !ok || v != 1 {
		t.Errorf("NULLIF(1, NULL) did not fold to 1")
	}
	if c, ok := FoldConstants(call("nullif", i(1), i(1))).(*CastExpr); !ok || !isNullConstExpr(c) || c.TargetType != "int4" {
		t.Errorf("NULLIF(1, 1) = %#v, want a NULL typed int4", FoldConstants(call("nullif", i(1), i(1))))
	}
	if FoldConstants(call("nullif", col, null)) != Expr(col) {
		t.Errorf("NULLIF(b, NULL) did not fold to b")
	}
	if b, ok := FoldConstants(&IsNullExpr{Operand: call("nullif", i(1), i(1)), Negated: true}).(*BooleanConst); !ok || b.Value {
		t.Errorf("NULLIF(1, 1) IS NOT NULL did not fold to false")
	}
	// Kept: all-NULL COALESCE needs a typed NULL of the common type; mixed
	// types need parse-time coercion goopg does not record.
	for name, e := range map[string]Expr{
		"NULLIF(NULL, 1)":    call("nullif", null, i(1)),
		"COALESCE(NULL)":     call("coalesce", null),
		"COALESCE(1, 2.5)":   call("coalesce", i(1), &NumericConst{Value: "2.5"}),
		"COALESCE('x', 'y')": call("coalesce", &StringConst{Value: "x"}, &StringConst{Value: "y"}),
		"GREATEST(b, 1)":     call("greatest", col, i(1)),
	} {
		if _, still := FoldConstants(e).(*FuncCall); !still {
			t.Errorf("%s was folded; it must stay a call", name)
		}
	}

	for _, c := range []struct {
		name string
		e    Expr
		want bool
	}{
		{"1 IS NULL", &IsNullExpr{Operand: i(1)}, false},
		{"NULL IS NULL", &IsNullExpr{Operand: null}, true},
		{"NULL::int IS NOT NULL", &IsNullExpr{Operand: &CastExpr{Operand: null, TargetType: "int4"}, Negated: true}, false},
		{"true IS TRUE", &IsBoolExpr{Operand: &BooleanConst{Value: true}, TestTrue: true}, true},
		{"true IS NOT FALSE", &IsBoolExpr{Operand: &BooleanConst{Value: true}, TestFalse: true, Negated: true}, true},
		{"NULL IS UNKNOWN", &IsBoolExpr{Operand: null}, true},
		{"NULL IS NOT TRUE", &IsBoolExpr{Operand: null, TestTrue: true, Negated: true}, true},
	} {
		b, ok := FoldConstants(c.e).(*BooleanConst)
		if !ok || b.Value != c.want {
			t.Errorf("%s = %#v, want %v", c.name, FoldConstants(c.e), c.want)
		}
	}
	if _, still := FoldConstants(&IsNullExpr{Operand: col}).(*IsNullExpr); !still {
		t.Error("b IS NULL was folded")
	}
}
