package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestPlanScanPrefilterWhitelist pins the DIRECTION of the whitelist: an
// expression the walker does not recognise must DISABLE the prefilter, never
// arm it with a wrong column bound. A false negative costs speed; a false
// positive would let a partially-deformed row reach an expression that reads
// past MaxCols, which is a wrong answer.
func TestPlanScanPrefilterWhitelist(t *testing.T) {
	col := func(idx int) optimizer.Expr {
		return &optimizer.ColumnRef{Index: idx, Name: "c", Type: catalog.Type{Name: "numeric"}}
	}
	lit := func(v string) optimizer.Expr { return &optimizer.NumericConst{Value: v} }
	bin := func(op parser.OpCode, l, r optimizer.Expr) optimizer.Expr {
		return &optimizer.BinaryOp{Op: op, Left: l, Right: r}
	}

	cases := []struct {
		name    string
		pred    optimizer.Expr
		ncols   int
		wantOK  bool
		wantMax int
	}{
		{"single column compare", bin(parser.OpLt, col(2), lit("24")), 16, true, 3},
		{"two columns, highest wins", bin(parser.OpAnd,
			bin(parser.OpLt, col(5), lit("24")),
			bin(parser.OpGe, col(0), lit("1"))), 16, true, 6},
		{"nested and/or", bin(parser.OpOr,
			bin(parser.OpAnd, bin(parser.OpLt, col(1), lit("2")), bin(parser.OpGt, col(3), lit("4"))),
			bin(parser.OpEq, col(2), lit("9"))), 16, true, 4},
		{"cast wrapping a column", bin(parser.OpLt,
			&optimizer.CastExpr{Operand: col(4), TargetType: "numeric"}, lit("1")), 16, true, 5},
		{"is null", &optimizer.IsNullExpr{Operand: col(2)}, 16, true, 3},
		{"unary minus", bin(parser.OpLt,
			&optimizer.UnaryOp{Op: parser.OpSub, Operand: col(1)}, lit("0")), 16, true, 2},
		{"constant only — no column, declined", bin(parser.OpLt, lit("1"), lit("2")), 16, false, 0},
		{"reads last column — nothing saved, declined", bin(parser.OpLt, col(15), lit("1")), 16, false, 0},
		{"index past ncols — declined", bin(parser.OpLt, col(99), lit("1")), 16, false, 0},
		{"nil predicate — declined", nil, 16, false, 0},
		{"zero columns — declined", bin(parser.OpLt, col(0), lit("1")), 0, false, 0},
		{"function call — not whitelisted, declined",
			bin(parser.OpLt, &optimizer.FuncCall{Name: "random"}, col(1)), 16, false, 0},
		{"function call buried in a subtree — declined", bin(parser.OpAnd,
			bin(parser.OpLt, col(1), lit("2")),
			bin(parser.OpGt, &optimizer.FuncCall{Name: "random"}, lit("0"))), 16, false, 0},
		{"subquery — declined",
			bin(parser.OpLt, col(1), &optimizer.SubqueryExpr{}), 16, false, 0},
		{"outer column ref — declined",
			bin(parser.OpLt, col(1), &optimizer.OuterColumnRef{Level: 1, Index: 0}), 16, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pf, ok := planScanPrefilter(tc.pred, tc.ncols)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && pf.MaxCols != tc.wantMax {
				t.Errorf("MaxCols = %d, want %d", pf.MaxCols, tc.wantMax)
			}
		})
	}
}

// TestDecodeRowRangeResumeEqualsFullDecode is the other half of the safety
// argument. A physical tuple carries no per-column offset array, so the second
// pass can only land correctly if the offset returned by the first is threaded
// back unchanged; a mistake decodes garbage rather than failing loudly. Every
// split point must reproduce the single-pass decode exactly.
func TestDecodeRowRangeResumeEqualsFullDecode(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "numeric"}, Ordinal: 1},
		{Name: "c", Type: catalog.Type{Name: "text"}, Ordinal: 2},
		{Name: "d", Type: catalog.Type{Name: "int8"}, Ordinal: 3},
		{Name: "e", Type: catalog.Type{Name: "numeric"}, Ordinal: 4},
		{Name: "f", Type: catalog.Type{Name: "text"}, Ordinal: 5},
		{Name: "g", Type: catalog.Type{Name: "bool"}, Ordinal: 6},
	}
	src := Row{
		NewIntDatum(42),
		Datum{Kind: KindNumeric, Int: 12345, Scale: 2},
		NewStringDatum("hello world"),
		NewIntDatum(-9000000000),
		Datum{Kind: KindNumeric, Int: -7, Scale: 3},
		NewStringDatum("x"),
		NewBoolDatum(true),
	}
	data, err := EncodeRowPG(cols, src)
	if err != nil {
		t.Fatalf("EncodeRowPG: %v", err)
	}
	natts := len(cols)
	st := array.DefaultOutputStyle()

	full := make(Row, len(cols))
	if _, err := DecodeRowRangeIntoMctxPGTupleStyled(full, cols, data, nil, natts, nil, st, 0, len(cols), 0); err != nil {
		t.Fatalf("full decode: %v", err)
	}

	for cut := 0; cut <= len(cols); cut++ {
		got := make(Row, len(cols))
		off, err := DecodeRowRangeIntoMctxPGTupleStyled(got, cols, data, nil, natts, nil, st, 0, cut, 0)
		if err != nil {
			t.Fatalf("cut=%d first half: %v", cut, err)
		}
		if _, err := DecodeRowRangeIntoMctxPGTupleStyled(got, cols, data, nil, natts, nil, st, cut, len(cols), off); err != nil {
			t.Fatalf("cut=%d second half: %v", cut, err)
		}
		for i := range cols {
			if got[i].Kind != full[i].Kind || got[i].Int != full[i].Int ||
				got[i].Scale != full[i].Scale || got[i].StringValue() != full[i].StringValue() {
				t.Errorf("cut=%d col %d (%s): split = %+v, full = %+v",
					cut, i, cols[i].Name, got[i], full[i])
			}
		}
	}
}

// TestNumericConstFastPathsMatchBigIntPath pins the sibling-path agreement the
// evalExpr NumericConst arm now depends on: the int64 fast paths must produce
// exactly what the math/big path produced, or a WHERE-clause literal changes
// meaning. Anything the fast paths decline still reaches parseNumeric.
func TestNumericConstFastPathsMatchBigIntPath(t *testing.T) {
	lits := []string{
		"0", "1", "-1", "24", "-24", "+7", "42",
		"0.04", "0.06", "-0.05", "123.45", "-123.45", "1.50", "0.000001",
		// PG constant-folds `0.05 + 0.01` to this; Q6 evaluates it per row.
		"0.060000000000000005", "-0.060000000000000005",
		"0.000000000000000001", "0000123.45", "0.0", "0.00", "-0.000",
		"999999999999999999", "-999999999999999999",
		// Declined by the fast paths — must still work via big.Int.
		"1e5", "1.5e3", "1e-5", "9223372036854775808", "1_000",
		"12345678901234567890123", "0.1e-30",
	}
	for _, lit := range lits {
		wantM, wantS, wantErr := parseNumeric(lit)
		var got Datum
		var gotFast bool
		if v, s, ok := parseNumericFastInt(lit); ok {
			got, gotFast = Datum{Kind: KindNumeric, Int: v, Scale: s}, true
		} else if v, s, ok := parseNumericFastScale(lit, -1); ok {
			got, gotFast = Datum{Kind: KindNumeric, Int: v, Scale: s}, true
		}
		if !gotFast {
			continue // falls through to parseNumeric, unchanged behaviour
		}
		if wantErr != nil {
			t.Errorf("%q: fast path accepted a literal parseNumeric rejects (%v)", lit, wantErr)
			continue
		}
		want := newNumeric(wantM, int(wantS))
		if got.Kind != want.Kind || got.Int != want.Int || got.Scale != want.Scale {
			t.Errorf("%q: fast path = {Int:%d Scale:%d}, big.Int path = {Int:%d Scale:%d}",
				lit, got.Int, got.Scale, want.Int, want.Scale)
		}
		// And the pair must denote the literal itself.
		gotRat := new(big.Rat).SetFrac(big.NewInt(got.Int),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(got.Scale)), nil))
		wantRat, ok := new(big.Rat).SetString(lit)
		if ok && gotRat.Cmp(wantRat) != 0 {
			t.Errorf("%q: decoded %s, want %s", lit, gotRat.RatString(), wantRat.RatString())
		}
	}
}
