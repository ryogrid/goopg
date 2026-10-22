package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestNullTestCompiledMatchesInterpreted pins M0122-0012 slice 59: IS [NOT]
// NULL compiles to ExprNullTest instead of an ExprAdapter that ran
// evalExprSlot per row. On the per-row and the batch evaluator, alone, under
// AND, inside a searched CASE (TPC-DS Q97's `a IS NOT NULL AND b IS NULL`
// arms) and over a computed operand, it must return exactly what the
// interpreted evalExpr returns for every row — never NULL — and a row-valued
// operand must stay on the adapter (its field-wise rule is not the scalar
// test).
func TestNullTestCompiledMatchesInterpreted(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	a := &optimizer.ColumnRef{Index: 0, Name: "a", Type: int4}
	b := &optimizer.ColumnRef{Index: 1, Name: "b", Type: int4}
	s := &optimizer.ColumnRef{Index: 2, Name: "s", Type: catalog.Type{Name: "text"}}
	rows := []Row{
		{NewIntDatum(1), NullDatum, NewStringDatum("x")},
		{NullDatum, NewIntDatum(2), NullDatum},
		{NullDatum, NullDatum, NewStringDatum("")},
		{NewIntDatum(0), NewIntDatum(-3), NullDatum},
	}
	isNull := func(e optimizer.Expr, neg bool) optimizer.Expr { return &optimizer.IsNullExpr{Operand: e, Negated: neg} }
	and := func(l, r optimizer.Expr) optimizer.Expr { return &optimizer.BinaryOp{Op: parser.OpAnd, Left: l, Right: r} }
	q97Case := &optimizer.CaseExpr{
		Whens: []optimizer.CaseWhen{{When: and(isNull(a, true), isNull(b, false)), Then: &optimizer.IntegerConst{Value: 1}}},
		Else:  &optimizer.IntegerConst{Value: 0},
	}
	exprs := []optimizer.Expr{
		isNull(a, false), isNull(a, true), isNull(s, false), isNull(s, true),
		and(isNull(a, true), isNull(b, false)),
		q97Case,
		isNull(&optimizer.BinaryOp{Op: parser.OpAdd, Left: a, Right: b}, false),
	}
	views := make([]SlotView, len(rows))
	sel := make([]int32, len(rows))
	for r := range rows {
		views[r] = SlotFromRow(nil, rows[r])
		sel[r] = int32(r)
	}
	var sc exprBatchScratch
	compiled := 0
	for ei, e := range exprs {
		var slab exprTreeSlab
		idx := slab.buildExpr(e)
		for _, n := range slab {
			if n.Kind == ExprNullTest {
				compiled++
			}
		}
		batch := make([]Datum, len(rows))
		batchErr := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, batch)
		if batchErr != nil {
			t.Fatalf("expr %d: batch: %v", ei, batchErr)
		}
		for r := range rows {
			want, wantErr := evalExpr(e, rows[r], nil)
			got, gotErr := evalFastExpr(slab, idx, views[r], nil)
			if !sameResult(got, gotErr, want, wantErr) {
				t.Fatalf("expr %d row %d: compiled %v (%v), interpreted %v (%v)", ei, r, got.Format(), gotErr, want.Format(), wantErr)
			}
			if !sameResult(batch[r], nil, want, nil) {
				t.Fatalf("expr %d row %d: batch %v, interpreted %v", ei, r, batch[r].Format(), want.Format())
			}
		}
	}
	if compiled < 9 {
		t.Fatalf("only %d IS NULL nodes compiled — the fixture never reaches ExprNullTest", compiled)
	}

	row := &optimizer.RowExpr{Elems: []optimizer.Expr{a, b}, Types: []catalog.Type{int4, int4}}
	var slab exprTreeSlab
	idx := slab.buildExpr(isNull(row, false))
	if slab[idx].Kind != ExprAdapter {
		t.Fatalf("row-valued IS NULL compiled to kind %d, want the adapter", slab[idx].Kind)
	}
	for r := range rows {
		want, wantErr := evalExpr(isNull(row, false), rows[r], nil)
		got, gotErr := evalFastExpr(slab, idx, views[r], nil)
		if !sameResult(got, gotErr, want, wantErr) {
			t.Fatalf("row IS NULL row %d: compiled %v, interpreted %v", r, got.Format(), want.Format())
		}
	}
}
