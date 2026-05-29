package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestDecodeRowIntoMctxPGTupleUsesMissingValueForFastDefault pins the
// M0097-0077 "fast default" backfill: when storedNatts < len(cols) (i.e.
// the row was written before `ALTER TABLE ADD COLUMN <name> <type>
// DEFAULT <const>` landed) and the new column carries a MissingValue
// Datum, the decoder surfaces the default instead of NULL.
//
// Without the fix, the decoder always emitted NullDatum for trailing
// columns, which made `SELECT * FROM foo` after an ADD COLUMN ... DEFAULT
// show blanks instead of the default — see the `returning` regress test
// (foo.f4 column).
func TestDecodeRowIntoMctxPGTupleUsesMissingValueForFastDefault(t *testing.T) {
	cols := []catalog.Column{
		{Name: "f1", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "f4", Type: catalog.Type{Name: "int8"}, Ordinal: 1, MissingValue: Datum{Kind: KindInt, Int: 99}},
	}
	// Encode a single-column row (mimicking a pre-ALTER tuple): natts=1.
	enc, err := encodeValuePG(cols[0].Type, NewIntDatum(7))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	row := make(Row, len(cols))
	if err := DecodeRowIntoMctxPGTuple(row, cols, enc, nil, 1, nil); err != nil {
		t.Fatalf("DecodeRowIntoMctxPGTuple: %v", err)
	}
	if row[0].Kind != KindInt || row[0].Int != 7 {
		t.Fatalf("col 0 got %+v, want KindInt 7", row[0])
	}
	if row[1].Kind != KindInt || row[1].Int != 99 {
		t.Fatalf("col 1 (missing) got %+v, want KindInt 99 (fast default)", row[1])
	}
}

// TestDecodeRowIntoMctxPGTupleNoMissingValueDecodesNullForTrailing pins
// the back-compat branch: when the new column has no MissingValue (added
// via plain `ALTER TABLE ADD COLUMN col TYPE` with no DEFAULT, or with a
// non-constant DEFAULT that constDefaultDatum couldn't evaluate), the
// decoder must continue to emit NullDatum for trailing missing columns.
func TestDecodeRowIntoMctxPGTupleNoMissingValueDecodesNullForTrailing(t *testing.T) {
	cols := []catalog.Column{
		{Name: "f1", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "f4", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
	}
	enc, err := encodeValuePG(cols[0].Type, NewIntDatum(7))
	if err != nil {
		t.Fatalf("encodeValuePG: %v", err)
	}
	row := make(Row, len(cols))
	if err := DecodeRowIntoMctxPGTuple(row, cols, enc, nil, 1, nil); err != nil {
		t.Fatalf("DecodeRowIntoMctxPGTuple: %v", err)
	}
	if !row[1].IsNull() {
		t.Fatalf("col 1 (missing, no fast default) got %+v, want NULL", row[1])
	}
}

// TestConstDefaultDatumLiteralCases pins the constant-default evaluator
// across the literal shapes regress / TPC-H actually exercise.
func TestConstDefaultDatumLiteralCases(t *testing.T) {
	int8Type := catalog.Type{Name: "int8"}
	textType := catalog.Type{Name: "text"}
	numericType := catalog.Type{Name: "numeric"}
	boolType := catalog.Type{Name: "bool"}

	cases := []struct {
		name   string
		expr   parser.Expr
		typ    catalog.Type
		wantOK bool
		check  func(d Datum) bool
	}{
		{"int literal -> int8", &parser.IntegerConst{Value: 99}, int8Type, true,
			func(d Datum) bool { return d.Kind == KindInt && d.Int == 99 }},
		{"negative int -> int8", &parser.UnaryOp{Op: parser.OpUnaryNeg, Operand: &parser.IntegerConst{Value: 5}}, int8Type, true,
			func(d Datum) bool { return d.Kind == KindInt && d.Int == -5 }},
		{"string literal -> text", &parser.StringConst{Value: "hi"}, textType, true,
			func(d Datum) bool { return d.Kind == KindString && d.StringValue() == "hi" }},
		{"bool true", &parser.BooleanConst{Value: true}, boolType, true,
			func(d Datum) bool { return d.Kind == KindBool && d.BoolValue() }},
		{"null", &parser.NullConst{}, int8Type, true, func(d Datum) bool { return d.IsNull() }},
		{"decimal numeric", &parser.NumericConst{Value: "12.34"}, numericType, true,
			func(d Datum) bool { return d.Kind == KindNumeric && d.Int == 1234 && d.Scale == 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := constDefaultDatum(tc.expr, tc.typ)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !tc.check(got) {
				t.Fatalf("unexpected datum %+v", got)
			}
		})
	}
}
