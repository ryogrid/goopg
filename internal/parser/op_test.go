package parser

import "testing"

// TestM0073OpCodeRoundTrip pins the Parse* + String()
// reverse-mapping for every defined OpCode. The unknown
// sentinel must round-trip via the special "<unknown>"
// string but ParseBinaryOp("<unknown>") must NOT find a
// match (returns OpUnknown).
func TestM0073OpCodeRoundTrip(t *testing.T) {
	type opcase struct {
		op       OpCode
		text     string
		isUnary  bool
		isBinary bool
	}
	cases := []opcase{
		// Unary
		{OpUnaryNeg, "-", true, false},
		{OpUnaryPos, "+", true, false},
		{OpNot, "NOT", true, false},
		// Binary arithmetic
		{OpAdd, "+", false, true},
		{OpSub, "-", false, true},
		{OpMul, "*", false, true},
		{OpDiv, "/", false, true},
		{OpMod, "%", false, true},
		{OpPow, "^", false, true},
		// Binary text
		{OpConcat, "||", false, true},
		// Binary comparison
		{OpEq, "=", false, true},
		{OpLt, "<", false, true},
		{OpGt, ">", false, true},
		{OpLe, "<=", false, true},
		{OpGe, ">=", false, true},
		{OpNe, "<>", false, true},
		// Binary boolean
		{OpAnd, "AND", false, true},
		{OpOr, "OR", false, true},
		// Binary pattern
		{OpLike, "LIKE", false, true},
		{OpNotLike, "NOT LIKE", false, true},
	}

	for _, tc := range cases {
		if got := tc.op.String(); got != tc.text {
			t.Errorf("OpCode(%d).String() = %q, want %q",
				tc.op, got, tc.text)
		}
		if tc.isUnary {
			if got := ParseUnaryOp(tc.text); got != tc.op {
				t.Errorf("ParseUnaryOp(%q) = %d, want %d",
					tc.text, got, tc.op)
			}
		}
		if tc.isBinary {
			if got := ParseBinaryOp(tc.text); got != tc.op {
				t.Errorf("ParseBinaryOp(%q) = %d, want %d",
					tc.text, got, tc.op)
			}
		}
	}
}

// TestM0073OpCodeNeAlias pins the != / <> alias contract:
// both spellings parse to the same OpNe; String() returns
// the canonical "<>" form regardless of which input was
// used.
func TestM0073OpCodeNeAlias(t *testing.T) {
	if got := ParseBinaryOp("<>"); got != OpNe {
		t.Errorf("ParseBinaryOp(\"<>\") = %d, want OpNe", got)
	}
	if got := ParseBinaryOp("!="); got != OpNe {
		t.Errorf("ParseBinaryOp(\"!=\") = %d, want OpNe", got)
	}
	if got := OpNe.String(); got != "<>" {
		t.Errorf("OpNe.String() = %q, want \"<>\"", got)
	}
}

// TestM0073OpCodeUnknown pins the parse-error sentinel:
// unrecognised tokens return OpUnknown; OpUnknown.String()
// returns "<unknown>".
func TestM0073OpCodeUnknown(t *testing.T) {
	if got := ParseUnaryOp("???"); got != OpUnknown {
		t.Errorf("ParseUnaryOp(\"???\") = %d, want OpUnknown", got)
	}
	if got := ParseBinaryOp("FOO"); got != OpUnknown {
		t.Errorf("ParseBinaryOp(\"FOO\") = %d, want OpUnknown", got)
	}
	if got := OpUnknown.String(); got != "<unknown>" {
		t.Errorf("OpUnknown.String() = %q, want \"<unknown>\"", got)
	}
}

// TestM0073OpCodeIsBoolean pins IsBoolean() coverage:
// only OpAnd / OpOr return true.
func TestM0073OpCodeIsBoolean(t *testing.T) {
	for _, op := range []OpCode{
		OpAnd, OpOr,
	} {
		if !op.IsBoolean() {
			t.Errorf("(%s).IsBoolean() = false, want true", op)
		}
	}
	for _, op := range []OpCode{
		OpUnknown, OpAdd, OpSub, OpMul, OpDiv, OpMod,
		OpConcat, OpEq, OpLt, OpGt, OpLe, OpGe, OpNe,
		OpLike, OpNotLike, OpUnaryNeg, OpUnaryPos, OpNot,
	} {
		if op.IsBoolean() {
			t.Errorf("(%s).IsBoolean() = true, want false", op)
		}
	}
}

// TestM0073OpCodeIsComparison pins the comparison-set
// helper used by predicate-shape walkers.
func TestM0073OpCodeIsComparison(t *testing.T) {
	for _, op := range []OpCode{OpEq, OpLt, OpGt, OpLe, OpGe, OpNe} {
		if !op.IsComparison() {
			t.Errorf("(%s).IsComparison() = false, want true", op)
		}
	}
	for _, op := range []OpCode{OpAdd, OpAnd, OpOr, OpLike, OpUnknown} {
		if op.IsComparison() {
			t.Errorf("(%s).IsComparison() = true, want false", op)
		}
	}
}
