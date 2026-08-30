package executor

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// foldCorpus builds the predicate shapes constant folding has to get right,
// including the ones the PS6.2 parity corpus found diffs on (short-circuit with
// a non-boolean left operand, NULL propagation).
func foldCorpus() []struct {
	name string
	expr optimizer.Expr
} {
	col := func(i int, tn string) optimizer.Expr {
		return &optimizer.ColumnRef{Index: i, Name: "c", Type: catalog.Type{Name: tn}}
	}
	num := func(v string) optimizer.Expr { return &optimizer.NumericConst{Value: v} }
	iconst := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	bin := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	date := func(v string) optimizer.Expr {
		return &optimizer.TypedStringLit{Value: v, Type: "date"}
	}
	return []struct {
		name string
		expr optimizer.Expr
	}{
		{"all constant, true", bin(parser.OpLt, num("1"), num("2"))},
		{"all constant, false", bin(parser.OpGt, num("1"), num("2"))},
		{"column vs constant", bin(parser.OpLt, col(0, "numeric"), num("24"))},
		{"constant arithmetic on the right", bin(parser.OpLt, col(0, "numeric"),
			bin(parser.OpAdd, iconst(20), iconst(4)))},
		{"and chain", bin(parser.OpAnd,
			bin(parser.OpGe, col(0, "numeric"), num("0.04")),
			bin(parser.OpLe, col(0, "numeric"), num("0.06")))},
		{"or chain", bin(parser.OpOr,
			bin(parser.OpLt, col(0, "numeric"), num("0")),
			bin(parser.OpGt, col(0, "numeric"), num("1")))},
		{"date literal compare", bin(parser.OpGe, col(1, "date"), date("1994-01-01"))},
		{"null constant compare", bin(parser.OpEq, col(0, "numeric"), &optimizer.NullConst{})},
		{"is null on a column", &optimizer.IsNullExpr{Operand: col(0, "numeric")}},
		{"unary minus on a constant", bin(parser.OpLt, col(0, "numeric"),
			&optimizer.UnaryOp{Op: parser.OpSub, Operand: num("5")})},
		// Short-circuit with a NON-boolean left operand: the PS6.2 root cause.
		{"non-bool left of AND", bin(parser.OpAnd, col(2, "text"),
			bin(parser.OpLt, col(0, "numeric"), num("1")))},
		{"bool column and constant", bin(parser.OpAnd, col(3, "bool"),
			bin(parser.OpLt, col(0, "numeric"), num("100")))},
	}
}

func foldTestRows() []Row {
	return []Row{
		{Datum{Kind: KindNumeric, Int: 4, Scale: 2}, NewDateDatum(time.Date(1994, 6, 1, 0, 0, 0, 0, time.UTC)), NewStringDatum("x"), NewBoolDatum(true)},
		{Datum{Kind: KindNumeric, Int: 6, Scale: 2}, NewDateDatum(time.Date(1993, 6, 1, 0, 0, 0, 0, time.UTC)), NewStringDatum(""), NewBoolDatum(false)},
		{Datum{Kind: KindNumeric, Int: 24, Scale: 0}, NewDateDatum(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)), NewStringDatum("y"), NewBoolDatum(true)},
		{NullDatum, NullDatum, NullDatum, NullDatum},
		{Datum{Kind: KindNumeric, Int: -5, Scale: 0}, NewDateDatum(time.Date(1994, 1, 1, 0, 0, 0, 0, time.UTC)), NewStringDatum("z"), NewBoolDatum(false)},
	}
}

// TestCompiledFoldedExprMatchesInterpreter is the parity gate for take 7. The
// compiled evaluator with constant folding must agree with evalExprSlot on
// every (expression, row) pair — same value, same NULL-ness, same error.
//
// A disagreement here is a wrong answer in a WHERE clause, which is why the
// corpus includes the two shapes that produced the 619 diffs in the M0127-PS6.2
// audit: a non-boolean left operand of AND, and NULL propagation.
func TestCompiledFoldedExprMatchesInterpreter(t *testing.T) {
	ctx := &Context{}
	rows := foldTestRows()
	for _, tc := range foldCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			var slab exprTreeSlab
			idx := slab.buildExprCtx(tc.expr, ctx)
			if idx == noExpr {
				t.Fatalf("buildExprCtx returned noExpr")
			}
			for ri, row := range rows {
				slot := rowSlotView(row)
				want, wantErr := evalExprSlot(tc.expr, slot, ctx)
				got, gotErr := evalFastExpr(slab, idx, slot, ctx)

				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("row %d: error mismatch: interpreted=%v compiled=%v", ri, wantErr, gotErr)
				}
				if wantErr != nil {
					if wantErr.Error() != gotErr.Error() {
						t.Errorf("row %d: error text differs:\n  interpreted: %v\n  compiled:    %v", ri, wantErr, gotErr)
					}
					continue
				}
				if want.IsNull() != got.IsNull() {
					t.Errorf("row %d: NULL-ness differs: interpreted null=%v compiled null=%v", ri, want.IsNull(), got.IsNull())
					continue
				}
				if want.IsNull() {
					continue
				}
				if want.Kind != got.Kind || want.Format() != got.Format() {
					t.Errorf("row %d: value differs: interpreted={%v %q} compiled={%v %q}",
						ri, want.Kind, want.Format(), got.Kind, got.Format())
				}
			}
		})
	}
}

// TestFoldingDeclinesOnError pins design §3.1 rule 3. A constant subtree that
// FAILS must not be folded: folding it would move the error from row time to
// plan-build time, changing when the statement fails and whether earlier rows
// were already returned.
func TestFoldingDeclinesOnError(t *testing.T) {
	ctx := &Context{}
	// 1/0 is constant and raises division by zero.
	bad := &optimizer.BinaryOp{Op: parser.OpDiv,
		Left:  &optimizer.IntegerConst{Value: 1},
		Right: &optimizer.IntegerConst{Value: 0}}

	if _, err := evalExprSlot(bad, nil, ctx); err == nil {
		t.Skip("1/0 does not error in this build; nothing to pin")
	}
	var slab exprTreeSlab
	idx := slab.buildExprCtx(bad, ctx)
	if idx == noExpr {
		t.Fatal("buildExprCtx returned noExpr")
	}
	if slab[idx].Kind == ExprConstVal {
		t.Fatal("a failing constant subtree was folded; the error would now be " +
			"raised at build time instead of per row")
	}
	// And it must still raise at evaluation time, like the interpreter.
	if _, err := evalFastExpr(slab, idx, nil, ctx); err == nil {
		t.Error("compiled form of a failing constant did not raise at eval time")
	}
}

// TestFoldingActuallyFolds is the positive control: without it the two tests
// above would pass just as happily if folding never fired at all.
func TestFoldingActuallyFolds(t *testing.T) {
	ctx := &Context{}
	// A wholly constant subtree on the right of a column comparison.
	e := &optimizer.BinaryOp{Op: parser.OpLt,
		Left: &optimizer.ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "numeric"}},
		Right: &optimizer.BinaryOp{Op: parser.OpAdd,
			Left:  &optimizer.IntegerConst{Value: 20},
			Right: &optimizer.IntegerConst{Value: 4}}}

	var folded exprTreeSlab
	fi := folded.buildExprCtx(e, ctx)
	var plain exprTreeSlab
	pi := plain.buildExprCtx(e, nil)

	countConst := func(s exprTreeSlab) int {
		n := 0
		for i := range s {
			if s[i].Kind == ExprConstVal {
				n++
			}
		}
		return n
	}
	if countConst(folded) == 0 {
		t.Fatal("folding produced no ExprConstVal node — constant folding is not firing")
	}
	if countConst(plain) != 0 {
		t.Error("ctx==nil must not fold; existing buildExpr callers would change behaviour")
	}
	if len(folded) >= len(plain) {
		t.Errorf("folded slab (%d nodes) should be smaller than unfolded (%d)", len(folded), len(plain))
	}
	// And both must still agree with the interpreter.
	row := Row{Datum{Kind: KindNumeric, Int: 5, Scale: 0}}
	slot := rowSlotView(row)
	want, _ := evalExprSlot(e, slot, ctx)
	gotF, _ := evalFastExpr(folded, fi, slot, ctx)
	gotP, _ := evalFastExpr(plain, pi, slot, ctx)
	if want.Format() != gotF.Format() || want.Format() != gotP.Format() {
		t.Errorf("value mismatch: interpreted=%q folded=%q unfolded=%q",
			want.Format(), gotF.Format(), gotP.Format())
	}
}

// TestFoldingDeclinesSessionDependentValues pins the parallel-only hazard that
// the take-7 design review caught (C2).
//
// gatherOp Opens the leader's child with the session Context but each worker
// with a NewWorkerContext whose GetSetting is nil. If a session-sensitive
// constant were folded, the leader and the workers would freeze DIFFERENT
// values into two plans running the same query — a wrong answer that appears
// only under parallelism and only for some session settings.
//
// The literal here is a zone-less TIMESTAMPTZ, which reads the session
// TimeZone GUC (expr.go, M0134-0026).
func TestFoldingDeclinesSessionDependentValues(t *testing.T) {
	lit := &optimizer.TypedStringLit{Value: "2026-08-30 12:00:00", Type: "timestamptz"}

	sessionCtx := &Context{GetSetting: func(name string) (string, bool) {
		if strings.EqualFold(name, "timezone") {
			return "Asia/Tokyo", true
		}
		return "", false
	}}
	workerCtx := &Context{} // GetSetting nil, exactly as NewWorkerContext leaves it

	withSession, err1 := evalExprSlot(lit, nil, sessionCtx)
	withoutSession, err2 := evalExprSlot(lit, nil, workerCtx)
	if err1 != nil || err2 != nil {
		t.Skipf("literal did not evaluate in this build (%v / %v)", err1, err2)
	}
	if foldedValuesIdentical(withSession, withoutSession) {
		t.Skip("this literal is not session-sensitive in this build; nothing to pin")
	}

	// The values DIFFER by session, so folding must decline.
	if _, ok := foldConstant(lit, sessionCtx); ok {
		t.Fatalf("a session-dependent literal was folded (session=%q worker=%q). "+
			"Under Gather the leader and the workers would freeze different "+
			"constants into the same query.", withSession.Format(), withoutSession.Format())
	}

	var slab exprTreeSlab
	idx := slab.buildExprCtx(lit, sessionCtx)
	if idx != noExpr && slab[idx].Kind == ExprConstVal {
		t.Error("buildExprCtx folded a session-dependent literal")
	}
}
