package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestFastNumericArithMatchesEvalBinary pins M0122-0012 C53: the compiled
// binary arm computes NUMERIC (and int-with-NUMERIC) +, - and * through
// fastNumericArith instead of evalBinary, so for every operand pair
// fastNumericArithOperands admits the two must return the same value, kind
// and scale — and the same error. The matrix mixes scales, signs, an int on
// either side, a value past the int64 mantissa lane and zero.
func TestFastNumericArithMatchesEvalBinary(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890", 10)
	vals := []Datum{
		newNumeric(big.NewInt(550), 2),
		newNumeric(big.NewInt(-7), 1),
		newNumeric(big.NewInt(0), 0),
		newNumeric(big.NewInt(1), 4),
		newNumeric(huge, 3),
		NewIntDatum(3),
		NewIntDatum(-2),
		NewIntDatum(0),
	}
	admitted := 0
	for _, op := range []parser.OpCode{parser.OpAdd, parser.OpSub, parser.OpMul} {
		for _, l := range vals {
			for _, r := range vals {
				if !fastNumericArithOperands(&l, &r) {
					if l.Kind == KindNumeric || r.Kind == KindNumeric {
						t.Fatalf("%v %s %v: a numeric pair was not admitted", l, op, r)
					}
					continue
				}
				admitted++
				want, wantErr := evalBinary(op, l, r, 0, nil)
				got, gotErr := fastNumericArith(op, l, r)
				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("%v %s %v: fast err %v, evalBinary err %v", l, op, r, gotErr, wantErr)
				}
				if got.Kind != want.Kind || got.Scale != want.Scale || got.Format() != want.Format() {
					t.Fatalf("%v %s %v: fast %s (kind %d scale %d), evalBinary %s (kind %d scale %d)",
						l, op, r, got.Format(), got.Kind, got.Scale, want.Format(), want.Kind, want.Scale)
				}
			}
		}
	}
	if admitted != 3*(8*8-3*3) {
		t.Fatalf("admitted %d pairs, want %d", admitted, 3*(8*8-3*3))
	}
	// NULL and non-numeric operands stay with evalBinary.
	for _, pair := range [][2]Datum{{NullDatum, NewIntDatum(1)}, {newNumeric(big.NewInt(1), 0), NullDatum},
		{NewStringDatum("1.5"), newNumeric(big.NewInt(1), 0)}, {NewIntDatum(1), NewIntDatum(2)}} {
		if fastNumericArithOperands(&pair[0], &pair[1]) {
			t.Fatalf("%v, %v admitted", pair[0], pair[1])
		}
	}
}
