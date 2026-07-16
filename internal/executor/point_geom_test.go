package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestPointStrictlyLeftRightOperators pins the geometric `point << point`
// (strictly left of) and `point >> point` (strictly right of) operators, which
// reuse the `<<` / `>>` spellings. goopg backs `point` with its text form, so
// the comparison parses both operands and compares X coordinates, yielding bool.
// Required by the predicate-gist isolation spec (M0118-0002).
func TestPointStrictlyLeftRightOperators(t *testing.T) {
	cases := []struct {
		name     string
		left     Datum
		right    Datum
		op       parser.OpCode
		wantBool bool
	}{
		{"left-of-true", NewStringDatum("(10,10)"), NewStringDatum("(20,5)"), parser.OpBitShiftLeft, true},
		{"left-of-false", NewStringDatum("(30,10)"), NewStringDatum("(20,5)"), parser.OpBitShiftLeft, false},
		{"left-of-equal-x", NewStringDatum("(20,1)"), NewStringDatum("(20,9)"), parser.OpBitShiftLeft, false},
		{"right-of-true", NewStringDatum("(30,10)"), NewStringDatum("(20,5)"), parser.OpBitShiftRight, true},
		{"right-of-false", NewStringDatum("(10,10)"), NewStringDatum("(20,5)"), parser.OpBitShiftRight, false},
	}
	for _, c := range cases {
		got, err := evalBinary(c.op, c.left, c.right, 0, nil)
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got.Kind != KindBool || got.BoolValue() != c.wantBool {
			t.Errorf("%s: got=%+v want bool %v", c.name, got, c.wantBool)
		}
	}

	// NULL operand → NULL (SQL three-valued logic), not an integer-operand error.
	if got, err := evalBinary(parser.OpBitShiftLeft, NullDatum, NewStringDatum("(1,1)"), 0, nil); err != nil || !got.IsNull() {
		t.Errorf("point << NULL: got=%+v err=%v want NULL", got, err)
	}

	// Integer bit-shift unaffected: 1 << 4 = 16.
	if got, err := evalBinary(parser.OpBitShiftLeft, NewIntDatum(1), NewIntDatum(4), 0, nil); err != nil || got.Kind != KindInt || got.Int != 16 {
		t.Errorf("1 << 4: got=%+v err=%v want int 16", got, err)
	}

	// A non-point string still errors as before (no point hijack).
	if _, err := evalBinary(parser.OpBitShiftLeft, NewStringDatum("abc"), NewStringDatum("def"), 0, nil); err == nil {
		t.Errorf("'abc' << 'def': expected integer-operands error, got nil")
	}
}
