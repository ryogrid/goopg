package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestOuterRefCompiledMatchesInterpreted pins M0122-0012 slice 75: an outer
// row's column (OuterColumnRef) and a SubPlan parameter (ExecParamRef) compile
// to their own nodes instead of an ExprAdapter, and a comparison whose
// operands already lie in a materialized row, an outer row, a parameter slot
// or a folded constant compares them in place. Every value and every error
// must be the interpreted twin's: out-of-range levels and columns, unset
// parameters, NULLs, big and int64-lane numerics, strings (which fall through
// to the general path) and a literal the compiled node coerces.
func TestOuterRefCompiledMatchesInterpreted(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })

	int4 := catalog.Type{Name: "int4"}
	numeric := catalog.Type{Name: "numeric"}
	text := catalog.Type{Name: "text"}
	a := &optimizer.ColumnRef{Index: 0, Name: "a", Type: int4}
	n := &optimizer.ColumnRef{Index: 1, Name: "n", Type: numeric}
	s := &optimizer.ColumnRef{Index: 2, Name: "s", Type: text}
	oa := &optimizer.OuterColumnRef{Level: 1, Index: 0, Name: "oa", Type: int4}
	on := &optimizer.OuterColumnRef{Level: 1, Index: 1, Name: "on", Type: numeric}
	os := &optimizer.OuterColumnRef{Level: 1, Index: 2, Name: "os", Type: text}
	oa2 := &optimizer.OuterColumnRef{Level: 2, Index: 0, Name: "oa2", Type: int4}
	badLevel := &optimizer.OuterColumnRef{Level: 3, Index: 0, Name: "bl", Type: int4}
	badIndex := &optimizer.OuterColumnRef{Level: 1, Index: 7, Name: "bi", Type: int4}
	p0 := &optimizer.ExecParamRef{ID: 0, Type: int4}
	unset := &optimizer.ExecParamRef{ID: 1, Type: int4}
	badParam := &optimizer.ExecParamRef{ID: 5, Type: int4}
	cmp := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r, ResultType: "bool"}
	}
	exprs := []optimizer.Expr{
		oa, on, os, oa2, p0, badLevel, badIndex, unset, badParam,
		cmp(parser.OpEq, a, oa),
		cmp(parser.OpLt, a, oa),
		cmp(parser.OpGe, oa, a),
		cmp(parser.OpNe, oa2, a),
		cmp(parser.OpEq, a, p0),
		cmp(parser.OpGt, p0, oa),
		cmp(parser.OpEq, n, on),
		cmp(parser.OpLe, on, n),
		cmp(parser.OpEq, s, os),
		cmp(parser.OpLt, os, s),
		cmp(parser.OpEq, a, badLevel),
		cmp(parser.OpEq, badIndex, a),
		cmp(parser.OpEq, a, unset),
		cmp(parser.OpEq, a, &optimizer.StringConst{Value: "3"}),
		cmp(parser.OpEq, n, &optimizer.NumericConst{Value: "1.50"}),
		&optimizer.BinaryOp{Op: parser.OpAnd, Left: cmp(parser.OpEq, a, oa), Right: cmp(parser.OpLt, n, on), ResultType: "bool"},
		&optimizer.BinaryOp{Op: parser.OpAdd, Left: a, Right: oa, ResultType: "int4"},
	}
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	inner := []Row{
		{NewIntDatum(3), Datum{Kind: KindNumeric, Int: 150, Scale: 2}, NewStringDatum("b")},
		{NewIntDatum(7), NewNumericBigDatum(huge, 2), NewStringDatum("a")},
		{NullDatum, NullDatum, NullDatum},
		{NewIntDatum(-2), Datum{Kind: KindNumeric, Int: -7, Scale: 1}, NewStringDatum("")},
	}
	outers := []Row{
		{NewIntDatum(3), Datum{Kind: KindNumeric, Int: 15, Scale: 1}, NewStringDatum("b")},
		{NewIntDatum(-5), NewNumericBigDatum(huge, 2), NewStringDatum("c")},
		{NullDatum, NullDatum, NullDatum},
	}
	compiled := map[ExprKind]int{}
	for oi, outer := range outers {
		ctx := &Context{
			OuterRows: []Row{{NewIntDatum(11)}, outer},
			ParamExec: []Datum{outer[0], NullDatum},
			ParamSet:  []bool{true, false},
		}
		for ei, e := range exprs {
			var slab exprTreeSlab
			idx := slab.buildExprCtx(e, ctx)
			for _, nd := range slab {
				compiled[nd.Kind]++
			}
			for r := range inner {
				slot := &MaterializedSlot{row: inner[r]}
				want, wantErr := evalExprSlot(e, slot, ctx)
				got, gotErr := evalFastExpr(slab, idx, slot, ctx)
				if !sameResult(got, gotErr, want, wantErr) {
					t.Fatalf("outer %d expr %d row %d: compiled %q (%v), interpreted %q (%v)", oi, ei, r, got.Format(), gotErr, want.Format(), wantErr)
				}
				// The seq scan's early qual slot is read in place too (C58).
				got, gotErr = evalFastExpr(slab, idx, rowSlotView(inner[r]), ctx)
				if !sameResult(got, gotErr, want, wantErr) {
					t.Fatalf("outer %d expr %d row %d (row view): compiled %q (%v), interpreted %q (%v)", oi, ei, r, got.Format(), gotErr, want.Format(), wantErr)
				}
				// A slot kind the in-place read does not know takes the
				// general path and must agree too.
				got, gotErr = evalFastExpr(slab, idx, identityVirtualSlot(slot), ctx)
				if !sameResult(got, gotErr, want, wantErr) {
					t.Fatalf("outer %d expr %d row %d (virtual): compiled %q (%v), interpreted %q (%v)", oi, ei, r, got.Format(), gotErr, want.Format(), wantErr)
				}
			}
		}
	}
	if compiled[ExprOuterColumnRef] == 0 || compiled[ExprExecParamRef] == 0 {
		t.Fatalf("compiled kinds %v: the fixture never reaches the outer-row / parameter nodes", compiled)
	}
}

// TestOperandPtrReadsInPlace pins the in-place operand read itself: a
// materialized row's column, a row view's column (the seq scan's early qual
// slot, C58), an outer row's column, a set parameter and a folded constant
// are returned where they lie; anything it cannot read safely (another slot
// kind, an out-of-range index, an unset parameter, a nil Context, a node that
// must be evaluated) is nil, left to evalFastExpr.
func TestOperandPtrReadsInPlace(t *testing.T) {
	row := Row{NewIntDatum(1), NewIntDatum(2)}
	outer := Row{NewIntDatum(3)}
	ctx := &Context{OuterRows: []Row{outer}, ParamExec: []Datum{NewIntDatum(4), NullDatum}, ParamSet: []bool{true, false}}
	var slab exprTreeSlab
	col := slab.buildExpr(&optimizer.ColumnRef{Index: 1})
	colBad := slab.buildExpr(&optimizer.ColumnRef{Index: 2})
	out := slab.buildExpr(&optimizer.OuterColumnRef{Level: 1, Index: 0})
	param := slab.buildExpr(&optimizer.ExecParamRef{ID: 0})
	unset := slab.buildExpr(&optimizer.ExecParamRef{ID: 1})
	konst := slab.buildExpr(&optimizer.StringConst{Value: "k"})
	expr := slab.buildExpr(&optimizer.UnaryOp{Op: parser.OpSub, Operand: &optimizer.ColumnRef{Index: 0}})
	ms := &MaterializedSlot{row: row}
	if p := fastOperandPtr(slab, col, ms, ctx); p != &row[1] {
		t.Fatalf("materialized column: %p, want %p", p, &row[1])
	}
	if p := fastOperandPtr(slab, col, rowSlotView(row), ctx); p != &row[1] {
		t.Fatalf("row view column: %p, want %p", p, &row[1])
	}
	if p := fastOperandPtr(slab, out, ms, ctx); p != &outer[0] {
		t.Fatalf("outer column: %p, want %p", p, &outer[0])
	}
	if p := fastOperandPtr(slab, param, ms, ctx); p != &ctx.ParamExec[0] {
		t.Fatalf("parameter: %p, want %p", p, &ctx.ParamExec[0])
	}
	if p := fastOperandPtr(slab, konst, ms, ctx); p == nil || p.StringValue() != "k" {
		t.Fatalf("constant: %v", p)
	}
	for name, p := range map[string]*Datum{
		"virtual slot":   fastOperandPtr(slab, col, identityVirtualSlot(ms), ctx),
		"row view range": fastOperandPtr(slab, colBad, rowSlotView(row), ctx),
		"out of range":   fastOperandPtr(slab, colBad, ms, ctx),
		"unset":          fastOperandPtr(slab, unset, ms, ctx),
		"nil context":    fastOperandPtr(slab, out, ms, nil),
		"expression":     fastOperandPtr(slab, expr, ms, ctx),
		"no node":        fastOperandPtr(slab, noExpr, ms, ctx),
	} {
		if p != nil {
			t.Fatalf("%s: %v, want nil", name, *p)
		}
	}
}

// identityVirtualSlot is a *VirtualSlot whose column i is ms's column i: a
// slot kind fastOperandPtr does not read in place, so evalFastExpr takes its
// general path over it.
func identityVirtualSlot(ms *MaterializedSlot) *VirtualSlot {
	cols := make([]virtualCol, len(ms.row))
	for i := range cols {
		cols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
	}
	return NewVirtualSlot(nil, []TupleSlot{ms}, cols)
}
