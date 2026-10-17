package executor

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestEvalFastExprBatchMatchesPerRow is evalFastExprBatch's sibling check
// (M0122-0012). For every expression and every random selection it must give
// exactly the per-row evaluator's Datum on each selected row. When the per-row
// pass raises on any selected row, the batch must also raise: callers then
// re-run the batch per row, which is what makes its node-at-a-time error order
// acceptable. The batch must NOT raise when no selected row raises. The AND/OR
// cases are built so that a right side evaluated on a short-circuited row would
// divide by zero; that pins the selection-vector short circuit.
func TestEvalFastExprBatchMatchesPerRow(t *testing.T) {
	col := func(i int) optimizer.Expr { return &optimizer.ColumnRef{Index: i} }
	ic := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	bin := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	// Columns: 0 int (may be 0 or NULL), 1 numeric, 2 string, 3 bool (or
	// NULL), 4 int near the int4 limit.
	exprs := map[string]optimizer.Expr{
		"eq":          bin(parser.OpEq, col(0), ic(3)),
		"lt-numeric":  bin(parser.OpLt, col(1), ic(10)),
		"ge-numconst": bin(parser.OpGe, col(1), &optimizer.NumericConst{Value: "5.25"}),
		"between":     bin(parser.OpAnd, bin(parser.OpGe, col(0), ic(1)), bin(parser.OpLe, col(0), ic(4))),
		"and-guard": bin(parser.OpAnd, bin(parser.OpNe, col(0), ic(0)),
			bin(parser.OpGt, bin(parser.OpDiv, ic(12), col(0)), ic(2))),
		"or-guard": bin(parser.OpOr, bin(parser.OpEq, col(0), ic(0)),
			bin(parser.OpGt, bin(parser.OpDiv, ic(12), col(0)), ic(2))),
		"unguarded-div": bin(parser.OpGt, bin(parser.OpDiv, ic(12), col(0)), ic(2)),
		"and-chain": bin(parser.OpAnd, bin(parser.OpAnd, bin(parser.OpGe, col(0), ic(1)), bin(parser.OpLt, col(1), ic(50))),
			bin(parser.OpNe, col(2), &optimizer.StringConst{Value: "b"})),
		"bool-col-and":  bin(parser.OpAnd, col(3), bin(parser.OpGt, col(0), ic(2))),
		"bool-col-or":   bin(parser.OpOr, col(3), bin(parser.OpGt, col(0), ic(2))),
		"null-and":      bin(parser.OpAnd, &optimizer.NullConst{}, bin(parser.OpGt, col(0), ic(2))),
		"string-lt":     bin(parser.OpLt, col(2), &optimizer.StringConst{Value: "c"}),
		"arith-cmp":     bin(parser.OpGt, bin(parser.OpAdd, col(0), col(1)), ic(7)),
		"int4-overflow": bin(parser.OpGt, bin(parser.OpMul, col(4), ic(4)), ic(0)),
		"unary-neg":     bin(parser.OpLt, &optimizer.UnaryOp{Op: parser.OpUnaryNeg, Operand: col(0)}, ic(-2)),
		"not":           &optimizer.UnaryOp{Op: parser.OpNot, Operand: bin(parser.OpGt, col(0), ic(2))},
		"bare-col":      col(1),
		"const-only":    bin(parser.OpLt, ic(1), ic(2)),
	}
	rng := rand.New(rand.NewSource(122012))
	strs := []string{"a", "b", "c", "d"}
	mkRows := func(n int, withZero bool) []Row {
		rows := make([]Row, n)
		for i := range rows {
			v0 := NewIntDatum(int64(rng.Intn(6)))
			if !withZero && v0.Int == 0 {
				v0 = NewIntDatum(1)
			}
			if rng.Intn(9) == 0 {
				v0 = NullDatum
			}
			v3 := NewBoolDatum(rng.Intn(2) == 0)
			if rng.Intn(7) == 0 {
				v3 = NullDatum
			}
			v4 := NewIntDatum(int64(rng.Intn(1000)))
			if rng.Intn(25) == 0 {
				v4 = NewIntDatum(2147483000)
			}
			rows[i] = Row{v0, Datum{Kind: KindNumeric, Int: int64(rng.Intn(10000)), Scale: 2}, NewStringDatum(strs[rng.Intn(len(strs))]), v3, v4}
		}
		return rows
	}
	var sc exprBatchScratch
	for name, e := range exprs {
		var slab exprTreeSlab
		idx := slab.buildExpr(e)
		for trial := 0; trial < 40; trial++ {
			rows := mkRows(1+rng.Intn(80), trial%3 != 0)
			views := make([]SlotView, len(rows))
			for i := range rows {
				views[i] = rowSlotView(rows[i])
			}
			var sel []int32
			for i := range rows {
				if trial%2 == 0 || rng.Intn(3) != 0 {
					sel = append(sel, int32(i))
				}
			}
			want := make([]Datum, len(sel))
			var wantErr error
			for k, r := range sel {
				v, err := evalFastExpr(slab, idx, views[r], nil)
				if err != nil {
					wantErr = err
					break
				}
				want[k] = v
			}
			got := make([]Datum, len(sel))
			gotErr := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, got)
			if sc.dTop != 0 || sc.sTop != 0 {
				t.Fatalf("%s: scratch stack not balanced: datums=%d sels=%d", name, sc.dTop, sc.sTop)
			}
			if (wantErr != nil) != (gotErr != nil) {
				t.Fatalf("%s trial %d: per-row err=%v, batch err=%v", name, trial, wantErr, gotErr)
			}
			if wantErr != nil {
				continue
			}
			for k := range sel {
				if !reflect.DeepEqual(got[k], want[k]) {
					t.Fatalf("%s trial %d row %d: batch %#v, per-row %#v (row %v)", name, trial, sel[k], got[k], want[k], rows[sel[k]])
				}
			}
		}
	}
}

// benchStoreSalesQual is the filtered TPC-DS store_sales qual the M0122-0012
// slices were measured on: an int column BETWEEN two constants, and two numeric
// columns against integer constants (the cross-kind comparison).
func benchStoreSalesQual() (exprTreeSlab, int32, []Row, []SlotView) {
	col := func(i int) optimizer.Expr { return &optimizer.ColumnRef{Index: i} }
	ic := func(v int64) optimizer.Expr { return &optimizer.IntegerConst{Value: v} }
	bin := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}
	e := bin(parser.OpAnd,
		bin(parser.OpAnd,
			bin(parser.OpAnd, bin(parser.OpGe, col(0), ic(1)), bin(parser.OpLe, col(0), ic(20))),
			bin(parser.OpGt, col(1), ic(10))),
		bin(parser.OpLt, col(2), ic(500)))
	var slab exprTreeSlab
	idx := slab.buildExpr(e)
	rng := rand.New(rand.NewSource(1))
	rows := make([]Row, filterBatchSize)
	views := make([]SlotView, len(rows))
	for i := range rows {
		rows[i] = Row{NewIntDatum(int64(1 + rng.Intn(100))),
			Datum{Kind: KindNumeric, Int: int64(rng.Intn(20000)), Scale: 2},
			Datum{Kind: KindNumeric, Int: int64(rng.Intn(100000)), Scale: 2}}
		views[i] = rowSlotView(rows[i])
	}
	return slab, idx, rows, views
}

func BenchmarkStoreSalesQualPerRow(b *testing.B) {
	slab, idx, _, views := benchStoreSalesQual()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range views {
			if _, err := evalFastExpr(slab, idx, v, nil); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkStoreSalesQualBatch(b *testing.B) {
	slab, idx, rows, views := benchStoreSalesQual()
	sel := make([]int32, len(rows))
	for i := range sel {
		sel[i] = int32(i)
	}
	out := make([]Datum, len(rows))
	var sc exprBatchScratch
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, out); err != nil {
			b.Fatal(err)
		}
	}
}
