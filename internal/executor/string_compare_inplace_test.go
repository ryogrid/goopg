package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestStringCompareInPlaceMatchesInterpreted pins M0122-0012 slice 89: a
// character-string comparison of two operands that live in place (a row's
// column, an outer row's column, a parameter, a constant) compares them
// there when both are plain strings — evalFastBinaryApply's plain-string
// branch without the operand copies. Every result must be the interpreted
// twin's: bpchar against a literal (trailing blanks dropped on both sides),
// text against a literal (kept), bpchar against text, an outer bpchar column,
// and the strings the plain test turns away (a '/' — pg_lsn shape — a leading
// '(' or '{', a uuid's length), NULLs and empty strings, for every
// comparison operator.
func TestStringCompareInPlaceMatchesInterpreted(t *testing.T) {
	old := seqScanDeformPoison
	seqScanDeformPoison = false
	t.Cleanup(func() { seqScanDeformPoison = old })

	bp := catalog.Type{Name: "bpchar", Args: []int64{9}}
	text := catalog.Type{Name: "text"}
	b := &optimizer.ColumnRef{Index: 0, Name: "b", Type: bp}
	x := &optimizer.ColumnRef{Index: 1, Name: "x", Type: text}
	ob := &optimizer.OuterColumnRef{Level: 1, Index: 0, Name: "ob", Type: bp}
	lit := func(s string) optimizer.Expr { return &optimizer.StringConst{Value: s} }
	var exprs []optimizer.Expr
	for _, op := range []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe} {
		for _, pair := range [][2]optimizer.Expr{
			{b, lit("Sunday")}, {lit("Sunday  "), b}, {x, lit("Sunday ")}, {x, lit("Sunday")},
			{b, x}, {x, b}, {b, ob}, {ob, x}, {b, lit("")}, {x, lit("1/2")}, {b, lit("(a")},
		} {
			exprs = append(exprs, &optimizer.BinaryOp{Op: op, Left: pair[0], Right: pair[1], ResultType: "bool"})
		}
	}
	rows := []Row{
		{NewStringDatum("Sunday   "), NewStringDatum("Sunday")},
		{NewStringDatum("Sunday"), NewStringDatum("Sunday ")},
		{NewStringDatum("Monday   "), NewStringDatum("monday")},
		{NewStringDatum("         "), NewStringDatum("")},
		{NewStringDatum("1/2"), NewStringDatum("1/2")},
		{NewStringDatum("(a"), NewStringDatum("{b}")},
		{NewStringDatum("0e5c3b8e-6b4b-4c8a-9f43-1f5b2a7c9d10"), NewStringDatum("0e5c3b8e6b4b4c8a9f431f5b2a7c9d10")},
		{NullDatum, NewStringDatum("x")},
		{NewStringDatum("Tue"), NullDatum},
	}
	outers := []Row{{NewStringDatum("Sunday   ")}, {NewStringDatum("Tue")}, {NullDatum}}
	fired := false
	for oi, outer := range outers {
		ctx := &Context{OuterRows: []Row{outer}}
		for ei, e := range exprs {
			var slab exprTreeSlab
			idx := slab.buildExprCtx(e, ctx)
			if slab[idx].payload[16]&4 != 0 {
				fired = true
			}
			for r := range rows {
				slot := &MaterializedSlot{row: rows[r]}
				want, wantErr := evalExprSlot(e, slot, ctx)
				got, gotErr := evalFastExpr(slab, idx, slot, ctx)
				if !sameResult(got, gotErr, want, wantErr) {
					t.Fatalf("outer %d expr %d (%v) row %d: compiled %q (%v), interpreted %q (%v)", oi, ei, e, r, got.Format(), gotErr, want.Format(), wantErr)
				}
			}
		}
	}
	if !fired {
		t.Fatal("no comparison compiled as a character-string comparison; the in-place lane is never reached")
	}
}
