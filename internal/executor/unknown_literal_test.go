package executor

import (
	"fmt"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// unknownLitFixture is one row per value of each column: col 0 date, 1
// timestamptz, 2 int4, 3 numeric, 4 interval. A column whose declared type
// promises a kind also carries values of OTHER kinds — a string that orders
// differently as text than as a timestamp ("2001-07-13 00:00:00" equals the
// date 2001-07-13 but sorts after the text '2001-07-13') and an int in a
// numeric column — so a memo used without its runtime kind test changes a
// result.
func unknownLitFixture() ([]optimizer.Expr, []Row) {
	day := time.Date(2001, 7, 13, 0, 0, 0, 0, time.UTC)
	cols := []optimizer.Expr{
		&optimizer.ColumnRef{Index: 0, Name: "d", Type: catalog.Type{Name: "date"}},
		&optimizer.ColumnRef{Index: 1, Name: "ts", Type: catalog.Type{Name: "timestamptz"}},
		&optimizer.ColumnRef{Index: 2, Name: "i", Type: catalog.Type{Name: "int4"}},
		&optimizer.ColumnRef{Index: 3, Name: "n", Type: catalog.Type{Name: "numeric"}},
		&optimizer.ColumnRef{Index: 4, Name: "iv", Type: catalog.Type{Name: "interval"}},
	}
	vals := [][]Datum{
		{NewDateDatum(day), NewDateDatum(day.AddDate(0, 0, 1)), NewDateDatum(day.AddDate(0, 0, -1)),
			NewDateInfinity(true), NullDatum, NewStringDatum("2001-07-13 00:00:00"), NewStringDatum("2001-07-14")},
		{NewTimeDatum(day), NewTimeDatum(day.Add(time.Microsecond)), NewTimestampInfinity(false), NullDatum,
			NewStringDatum("2001-07-13 00:00:00+00")},
		{{Kind: KindInt, Int: 5}, {Kind: KindInt, Int: 6}, {Kind: KindInt, Int: -5}, NullDatum, NewStringDatum("05")},
		{{Kind: KindNumeric, Int: 550, Scale: 2}, {Kind: KindNumeric, Int: 5, Scale: 0}, {Kind: KindInt, Int: 5},
			NullDatum, NewStringDatum("5.5")},
		{NewIntervalDatumFull(0, 1, 0), NewIntervalDatumFull(0, 0, 3600e6), NewIntervalDatumFull(1, 0, 0), NullDatum,
			NewStringDatum("1 day")},
	}
	n := 0
	for _, v := range vals {
		n = max(n, len(v))
	}
	rows := make([]Row, n)
	for r := range rows {
		rows[r] = make(Row, len(cols))
		for c := range cols {
			rows[r][c] = NullDatum
			if r < len(vals[c]) {
				rows[r][c] = vals[c][r]
			}
		}
	}
	return cols, rows
}

var unknownLitLiterals = []string{
	"2001-07-13", "2001-7-13", "2001-07-13 00:00:00", "2001-07-14", "infinity", "20010713",
	"5", "05", "5.5", "5.50", "-5", "1 day", "01:00:00", "1 mon", "garbage", "0/16B3748", "",
}

func sameResult(a Datum, aerr error, b Datum, berr error) bool {
	if (aerr == nil) != (berr == nil) {
		return false
	}
	if aerr != nil {
		return aerr.Error() == berr.Error()
	}
	return a.Kind == b.Kind && a.Int == b.Int && a.Format() == b.Format()
}

// TestUnknownLiteralComparisonMatchesPerRowCoercion pins slice 43's compiled
// memo: a comparison of a typed operand with a bare literal, compiled with the
// literal coerced once (unknownLitCoercion), must return exactly what the
// interpreted twin returns by coercing the literal on every row — on the
// per-row and the batch evaluator, for every operator, both operand orders and
// every row, including rows whose runtime kind is not the declared one.
func TestUnknownLiteralComparisonMatchesPerRowCoercion(t *testing.T) {
	cols, rows := unknownLitFixture()
	views := make([]SlotView, len(rows))
	sel := make([]int32, len(rows))
	for r := range rows {
		views[r] = SlotFromRow(nil, rows[r])
		sel[r] = int32(r)
	}
	ops := []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe}
	memoised, checked := 0, 0
	var sc exprBatchScratch
	for _, col := range cols {
		for _, lit := range unknownLitLiterals {
			for _, op := range ops {
				for _, litLeft := range []bool{false, true} {
					e := &optimizer.BinaryOp{Op: op, Left: col, Right: &optimizer.StringConst{Value: lit}}
					if litLeft {
						e = &optimizer.BinaryOp{Op: op, Left: &optimizer.StringConst{Value: lit}, Right: col}
					}
					var slab exprTreeSlab
					idx := slab.buildExpr(e)
					if slab[idx].constVal != nil {
						memoised++
					}
					batch := make([]Datum, len(rows))
					batchErr := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, batch)
					for r := range rows {
						want, wantErr := evalExpr(e, rows[r], nil)
						got, gotErr := evalFastExpr(slab, idx, views[r], nil)
						name := fmt.Sprintf("%s %v %q litLeft=%v row %v", col.(*optimizer.ColumnRef).Name, op, lit, litLeft, rows[r][col.(*optimizer.ColumnRef).Index])
						if !sameResult(got, gotErr, want, wantErr) {
							t.Errorf("%s: compiled %v (%v), interpreted %v (%v)", name, got, gotErr, want, wantErr)
						}
						if batchErr == nil && wantErr == nil && !sameResult(batch[r], nil, want, nil) {
							t.Errorf("%s: batch %v, interpreted %v", name, batch[r], want)
						}
						checked++
					}
				}
			}
		}
	}
	// The fixture must reach the memo, or it proves nothing about it.
	if memoised < 100 {
		t.Fatalf("only %d comparisons memoised over %d checks", memoised, checked)
	}
}

// TestUnknownLiteralInListMatchesPerRowCoercion pins the IN-list memo
// (inListConst.coercedItems): a literal value list evaluated with a Context
// caches its items coerced to the operand's kind; without a Context it
// re-evaluates and re-coerces per row. Both must agree on every row, for IN
// and NOT IN, with NULL and unparseable items mixed in. The literal list is
// TPC-DS Q83's shape: `d_date IN ('2001-07-13', '2001-09-10', ...)`.
func TestUnknownLiteralInListMatchesPerRowCoercion(t *testing.T) {
	cols, rows := unknownLitFixture()
	lists := [][]optimizer.Expr{
		{&optimizer.StringConst{Value: "2001-07-13"}, &optimizer.StringConst{Value: "2001-09-10"}},
		{&optimizer.StringConst{Value: "2001-07-14"}, &optimizer.NullConst{}, &optimizer.StringConst{Value: "garbage"}},
		{&optimizer.StringConst{Value: "5.5"}, &optimizer.StringConst{Value: "1 day"}, &optimizer.StringConst{Value: "infinity"}},
		{&optimizer.StringConst{Value: "05"}, &optimizer.StringConst{Value: "01:00:00"}, &optimizer.IntegerConst{Value: 6}},
		{&optimizer.StringConst{Value: "2001-07-13 00:00:00"}, &optimizer.StringConst{Value: "1 mon"}},
	}
	coercedHits := 0
	for _, col := range cols {
		for li, list := range lists {
			for _, neg := range []bool{false, true} {
				x := &optimizer.InExpr{Operand: col, List: list, Negated: neg}
				ctx := NewContext()
				for r := range rows {
					slot := SlotFromRow(nil, rows[r])
					want, wantErr := evalInExprScalarRow(x, slot, nil)
					got, gotErr := evalInExprScalarRow(x, slot, ctx)
					if !sameResult(got, gotErr, want, wantErr) {
						t.Errorf("%s IN list %d neg=%v row %v: cached %v (%v), per-row %v (%v)",
							col.(*optimizer.ColumnRef).Name, li, neg, rows[r], got, gotErr, want, wantErr)
					}
				}
				if c := ctx.inListConsts[x]; c != nil {
					for _, d := range c.coerced {
						if d.Kind != KindNull {
							coercedHits++
						}
					}
				}
			}
		}
	}
	if coercedHits < 10 {
		t.Fatalf("only %d list items coerced; the fixture never reaches the memo", coercedHits)
	}
}
