package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCoalesceCompiledMatchesInterpreted pins M0122-0012 slice 70: COALESCE
// compiles to an ExprCoalesce chain instead of an ExprAdapter. On the per-row
// and the batch evaluator, with one, two and three arguments, inside
// arithmetic (TPC-DS Q75's `q - COALESCE(r, 0)`) and with an argument that
// raises, it must answer exactly what evalFuncCall's "coalesce" arm answers:
// the first non-NULL argument as is, an earlier argument's error raised, a
// later argument never evaluated once one is non-NULL.
func TestCoalesceCompiledMatchesInterpreted(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	a := &optimizer.ColumnRef{Index: 0, Name: "a", Type: int4}
	b := &optimizer.ColumnRef{Index: 1, Name: "b", Type: int4}
	c := &optimizer.ColumnRef{Index: 2, Name: "c", Type: catalog.Type{Name: "numeric"}}
	zero := &optimizer.IntegerConst{Value: 0}
	coalesce := func(args ...optimizer.Expr) optimizer.Expr {
		return &optimizer.FuncCall{Name: "COALESCE", Args: args}
	}
	div0 := &optimizer.BinaryOp{Op: parser.OpDiv, Left: a, Right: zero, ResultType: "int4"}
	rows := []Row{
		{NewIntDatum(1), NullDatum, NullDatum},
		{NullDatum, NewIntDatum(2), Datum{Kind: KindNumeric, Int: 150, Scale: 2}},
		{NullDatum, NullDatum, Datum{Kind: KindNumeric, Int: -7, Scale: 1}},
		{NullDatum, NullDatum, NullDatum},
		{NewIntDatum(9), NewIntDatum(4), NullDatum},
	}
	exprs := []optimizer.Expr{
		coalesce(a),
		coalesce(a, zero),
		coalesce(a, b, zero),
		coalesce(a, c),
		coalesce(c, a, b),
		&optimizer.BinaryOp{Op: parser.OpSub, Left: b, Right: coalesce(a, zero), ResultType: "int4"},
		coalesce(div0, b), // a's error is raised when a is non-NULL; NULL/0 is NULL
		coalesce(b, div0), // div0 is reached only where b is NULL
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
			if n.Kind == ExprCoalesce {
				compiled++
			}
			if n.Kind == ExprAdapter {
				t.Fatalf("expr %d: an ExprAdapter is left in the compiled tree", ei)
			}
		}
		batch := make([]Datum, len(rows))
		batchErr := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, batch)
		for r := range rows {
			want, wantErr := evalExpr(e, rows[r], nil)
			got, gotErr := evalFastExpr(slab, idx, views[r], nil)
			if !sameResult(got, gotErr, want, wantErr) {
				t.Fatalf("expr %d row %d: compiled %q (%v), interpreted %q (%v)", ei, r, got.Format(), gotErr, want.Format(), wantErr)
			}
			if batchErr == nil && !sameResult(batch[r], nil, want, wantErr) {
				t.Fatalf("expr %d row %d: batch %q, interpreted %q (%v)", ei, r, batch[r].Format(), want.Format(), wantErr)
			}
		}
	}
	if compiled < 7 {
		t.Fatalf("only %d ExprCoalesce nodes compiled — the fixture never reaches the compiled arm", compiled)
	}
}

// TestProjectTargetsRunCompiled pins slice 70's projectOp change: targets are
// compiled once (fast / fastIdx) and evaluated with evalFastExpr — COALESCE
// arithmetic as compiled nodes, an unknown function as an ExprAdapter — with
// exactly evalExprSlot's values, and the compiled targets survive a re-Open.
func TestProjectTargetsRunCompiled(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	a := &optimizer.ColumnRef{Index: 0, Name: "a", Type: int4}
	b := &optimizer.ColumnRef{Index: 1, Name: "b", Type: int4}
	s := &optimizer.ColumnRef{Index: 2, Name: "s", Type: catalog.Type{Name: "text"}}
	targets := []optimizer.Expr{
		a,
		&optimizer.BinaryOp{Op: parser.OpSub, Left: a, Right: &optimizer.FuncCall{Name: "coalesce", Args: []optimizer.Expr{b, &optimizer.IntegerConst{Value: 0}}}, ResultType: "int4"},
		&optimizer.FuncCall{Name: "upper", Args: []optimizer.Expr{s}},
	}
	rows := []Row{
		{NewIntDatum(10), NewIntDatum(3), NewStringDatum("ab")},
		{NewIntDatum(10), NullDatum, NewStringDatum("x")},
		{NullDatum, NewIntDatum(1), NullDatum},
	}
	p := &projectOp{child: &rowsOp{rows: rows}, targets: targets}
	for pass := range 2 {
		if err := p.Open(&Context{}); err != nil {
			t.Fatal(err)
		}
		for r := range rows {
			slot, err := p.Next()
			if err != nil {
				t.Fatal(err)
			}
			for i, e := range targets {
				want, werr := evalExpr(e, rows[r], &Context{})
				if werr != nil || !sameResult(slot.Row()[i], nil, want, nil) {
					t.Fatalf("pass %d row %d target %d: %q, want %q (%v)", pass, r, i, slot.Row()[i].Format(), want.Format(), werr)
				}
			}
		}
		if _, err := p.Next(); err != EOF {
			t.Fatalf("pass %d: want EOF, got %v", pass, err)
		}
	}
	kinds := map[ExprKind]int{}
	for _, n := range p.fast {
		kinds[n.Kind]++
	}
	if kinds[ExprCoalesce] != 1 || kinds[ExprBinaryOp] != 1 || kinds[ExprAdapter] != 1 {
		t.Fatalf("compiled node kinds %v, want one ExprCoalesce, one ExprBinaryOp and one ExprAdapter (upper)", kinds)
	}
}
