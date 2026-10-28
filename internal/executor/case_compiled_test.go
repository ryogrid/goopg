package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCaseCompiledMatchesInterpreted pins M0122-0012 slice 47: CASE compiles
// to an ExprCase node (evalFastCase), the twin of evalCaseExpr, and the two
// must agree on every row. The matrix covers the searched form, the simple
// form over a char(9) operand (padded values against bare literals — the
// bpchar arm), an int and a numeric operand against mixed-kind WHENs, NULL
// operands and WHENs, a missing ELSE, and a CASE nested in a THEN. TPC-DS
// Q59's `sum(CASE WHEN d_day_name = 'Sunday' THEN ss_sales_price ELSE NULL
// END)` is the searched shape.
func TestCaseCompiledMatchesInterpreted(t *testing.T) {
	day := &optimizer.ColumnRef{Index: 0, Name: "d_day_name", Type: catalog.Type{Name: "bpchar", Args: []int64{9}}}
	i := &optimizer.ColumnRef{Index: 1, Name: "i", Type: catalog.Type{Name: "int4"}}
	n := &optimizer.ColumnRef{Index: 2, Name: "n", Type: catalog.Type{Name: "numeric"}}
	lit := func(s string) optimizer.Expr { return &optimizer.StringConst{Value: s} }
	num := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	eq := func(l, r optimizer.Expr) optimizer.Expr { return &optimizer.BinaryOp{Op: parser.OpEq, Left: l, Right: r} }
	gt := func(l, r optimizer.Expr) optimizer.Expr { return &optimizer.BinaryOp{Op: parser.OpGt, Left: l, Right: r} }
	w := func(when, then optimizer.Expr) optimizer.CaseWhen { return optimizer.CaseWhen{When: when, Then: then} }

	rows := []Row{
		{NewStringDatum("Sunday   "), NewIntDatum(5), {Kind: KindNumeric, Int: 550, Scale: 2}},
		{NewStringDatum("Monday   "), NewIntDatum(6), {Kind: KindNumeric, Int: 5, Scale: 0}},
		{NewStringDatum("Sunday"), NullDatum, NullDatum},
		{NullDatum, NewIntDatum(-1), {Kind: KindNumeric, Int: 7, Scale: 1}},
		{NewStringDatum("sunday   "), NewIntDatum(0), {Kind: KindNumeric, Int: 0, Scale: 0}},
	}
	cases := []*optimizer.CaseExpr{
		// searched, Q59's shape
		{Whens: []optimizer.CaseWhen{w(eq(day, lit("Sunday")), n)}, Else: &optimizer.NullConst{}},
		// searched, several arms, no ELSE
		{Whens: []optimizer.CaseWhen{w(gt(i, num(5)), lit("big")), w(eq(i, num(5)), lit("five")), w(eq(day, lit("Monday")), i)}},
		// simple over the char(9) operand: bare literals and a NULL WHEN
		{Operand: day, Whens: []optimizer.CaseWhen{w(&optimizer.NullConst{}, num(0)), w(lit("Monday"), num(2)), w(lit("Sunday"), num(1))}, Else: num(-1)},
		// simple over an int operand: an int WHEN, a string literal WHEN
		{Operand: i, Whens: []optimizer.CaseWhen{w(num(6), lit("six")), w(lit("5"), lit("five"))}, Else: lit("other")},
		// simple over a numeric operand
		{Operand: n, Whens: []optimizer.CaseWhen{w(num(5), lit("five")), w(lit("0.7"), lit("seven-tenths"))}},
		// M0122-0012 C50: searched WHENs consumed as bools through
		// fastComparisonBool — an int comparison (the non-string branch),
		// <> and < on the char(9) column, NULL operands falling back — with
		// constant THEN / ELSE arms returned without the evaluator.
		{Whens: []optimizer.CaseWhen{w(gt(i, num(5)), lit("gt5")), w(&optimizer.BinaryOp{Op: parser.OpNe, Left: day, Right: lit("Sunday")}, num(7))}, Else: lit("else")},
		{Whens: []optimizer.CaseWhen{w(&optimizer.BinaryOp{Op: parser.OpLt, Left: day, Right: lit("Sunday")}, n), w(eq(i, num(0)), &optimizer.NullConst{})}, Else: num(42)},
		// nested CASE in a THEN, a non-bool WHEN in the searched form
		{Whens: []optimizer.CaseWhen{
			w(i, lit("non-bool when")),
			w(gt(n, num(1)), &optimizer.CaseExpr{Operand: day, Whens: []optimizer.CaseWhen{w(lit("Sunday"), n)}, Else: i}),
		}, Else: day},
	}
	checked := map[string]int{}
	for ci, c := range cases {
		var slab exprTreeSlab
		idx := slab.buildExpr(c)
		if slab[idx].Kind != ExprCase {
			t.Fatalf("case %d compiled to kind %d, want ExprCase", ci, slab[idx].Kind)
		}
		for ri, r := range rows {
			want, wantErr := evalExpr(c, r, nil)
			got, gotErr := evalFastExpr(slab, idx, SlotFromRow(nil, r), nil)
			if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
				t.Errorf("case %d row %d: compiled err %v, interpreted err %v", ci, ri, gotErr, wantErr)
				continue
			}
			if got.Kind != want.Kind || got.Format() != want.Format() {
				t.Errorf("case %d row %d: compiled %v (%s), interpreted %v (%s)", ci, ri, got, got.Format(), want, want.Format())
			}
			checked[want.Format()]++
		}
	}
	// The matrix must reach several arms, not one value everywhere.
	if len(checked) < 8 {
		t.Fatalf("only %d distinct results: %v", len(checked), checked)
	}
}
