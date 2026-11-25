package optimizer

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestExprTypeTypmod pins which expressions keep a typmod, following PG's
// exprTypmod (nodeFuncs.c): a length-coercing cast has one, and an expression
// that merely computes a value of the type has none.
func TestExprTypeTypmod(t *testing.T) {
	vc := &ColumnRef{Type: catalog.Type{Name: "varchar", Args: []int64{3}}}
	ch := &ColumnRef{Type: catalog.Type{Name: "char", Args: []int64{2}}}
	nu := &ColumnRef{Type: catalog.Type{Name: "numeric", Args: []int64{4, 1}}}
	text := &ColumnRef{Type: catalog.Type{Name: "varchar"}}
	for _, tc := range []struct {
		name string
		e    Expr
		want catalog.Type
	}{
		{"varchar cast", &CastExpr{Operand: text, TargetType: "varchar", Typmod: 4}, catalog.Type{Name: "varchar", Args: []int64{4}}},
		{"numeric(p,s) cast", &CastExpr{Operand: nu, TargetType: "numeric", Typmod: 3<<16 | 1}, catalog.Type{Name: "numeric", Args: []int64{3, 1}}},
		{"numeric(p) cast", &CastExpr{Operand: nu, TargetType: "numeric", Typmod: 3}, catalog.Type{Name: "numeric", Args: []int64{3}}},
		{"interval(p) cast", &CastExpr{Operand: text, TargetType: "interval", Typmod: 2}, catalog.Type{Name: "interval", Args: []int64{0x7fff0002}}},
		{"timestamp(0) cast", &CastExpr{Operand: text, TargetType: "timestamp", Typmod: encodeTypmod("timestamp", []int64{0})}, catalog.Type{Name: "timestamp", Args: []int64{0}}},
		{"timestamp(3) cast", &CastExpr{Operand: text, TargetType: "timestamp", Typmod: encodeTypmod("timestamp", []int64{3})}, catalog.Type{Name: "timestamp", Args: []int64{3}}},
		{"timestamp cast", &CastExpr{Operand: text, TargetType: "timestamp"}, catalog.Type{Name: "timestamp"}},
		{"cast with no typmod", &CastExpr{Operand: vc, TargetType: "varchar"}, catalog.Type{Name: "varchar"}},
		{"int4 cast", &CastExpr{Operand: nu, TargetType: "int4", Typmod: 7}, catalog.Type{Name: "int4"}},
		{"unary minus", &UnaryOp{Op: 0, Operand: nu}, catalog.Type{Name: "numeric"}},
		{"round", &FuncCall{Name: "round", Args: []Expr{nu}}, catalog.Type{Name: "numeric"}},
		{"nullif of char(n) stays bpchar", &FuncCall{Name: "nullif", Args: []Expr{ch, ch}}, catalog.Type{Name: "bpchar"}},
		{"coalesce, same typmod", &FuncCall{Name: "coalesce", Args: []Expr{vc, vc}}, vc.Type},
		{"coalesce, different typmod", &FuncCall{Name: "coalesce", Args: []Expr{vc, text}}, catalog.Type{Name: "varchar"}},
		{"case, same typmod", &CaseExpr{Whens: []CaseWhen{{Then: vc}}, Else: vc}, vc.Type},
		{"case, no else", &CaseExpr{Whens: []CaseWhen{{Then: vc}}}, catalog.Type{Name: "varchar"}},
		{"case, different typmod", &CaseExpr{Whens: []CaseWhen{{Then: vc}}, Else: text}, catalog.Type{Name: "varchar"}},
	} {
		if got := exprType(tc.e); got.Name != tc.want.Name || !reflect.DeepEqual(got.Args, tc.want.Args) {
			t.Errorf("%s: exprType = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
