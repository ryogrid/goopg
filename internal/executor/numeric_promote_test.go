package executor

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// TestPromoteNumericOperandsMatchesPromoteToNumeric pins evalBinary's
// pass-through promotion (M0122-0012) to promoteToNumeric for every operand
// pairing evalBinary's numeric arms can see: numeric, int, numeric-looking
// string, and an operand toNumeric rejects (same error).
func TestPromoteNumericOperandsMatchesPromoteToNumeric(t *testing.T) {
	ops := []Datum{
		{Kind: KindNumeric, Int: 12345, Scale: 2},
		{Kind: KindNumeric, Int: -7, Scale: 0},
		NewIntDatum(1),
		NewIntDatum(-42),
		NewStringDatum("3.25"),
		NewBoolDatum(true), // rejected by toNumeric
	}
	for _, a := range ops {
		for _, b := range ops {
			wa, wb, werr := promoteToNumeric(a, b, parser.OpAdd, 7)
			ga, gb, gerr := promoteNumericOperands(a, b, parser.OpAdd, 7)
			if (werr == nil) != (gerr == nil) || (werr != nil && werr.Error() != gerr.Error()) {
				t.Fatalf("(%v, %v): err %v, want %v", a, b, gerr, werr)
			}
			if werr != nil {
				continue
			}
			if !reflect.DeepEqual(ga, wa) || !reflect.DeepEqual(gb, wb) {
				t.Fatalf("(%#v, %#v): got (%#v, %#v), want (%#v, %#v)", a, b, ga, gb, wa, wb)
			}
		}
	}
}

// TestEvalBinaryNumericArmsMatchPromoteToNumeric pins evalBinary's in-place
// int promotion (M0122-0012 slice 15, promoteIntOperand) against the path it
// replaced: promoteToNumeric, then the numeric operator. It covers every
// arithmetic operator and every pairing that reaches the numeric arms (at
// least one numeric operand): numeric, big numeric, int, numeric-looking
// string and a rejected kind. The results, or the errors, must be identical.
func TestEvalBinaryNumericArmsMatchPromoteToNumeric(t *testing.T) {
	huge, _ := new(big.Int).SetString("98765432109876543210987", 10)
	numerics := []Datum{
		{Kind: KindNumeric, Int: 12345, Scale: 2},
		{Kind: KindNumeric, Int: -7, Scale: 0},
		{Kind: KindNumeric, Int: 0, Scale: 1},
		newBigNumericInCtx(mmgr.Perm(), huge, 4),
	}
	others := []Datum{
		NewIntDatum(1),
		NewIntDatum(-42),
		NewIntDatum(0),
		NewStringDatum("3.25"),
		NewBoolDatum(true), // rejected by toNumeric
	}
	ref := func(op parser.OpCode, l, r Datum) (Datum, error) {
		for _, d := range []*Datum{&l, &r} {
			if d.Kind == KindString {
				if m, s, err := parseNumeric(d.StringValue()); err == nil {
					*d = newNumeric(m, int(s))
				}
			}
		}
		a, b, err := promoteToNumeric(l, r, op, 9)
		if err != nil {
			return Datum{}, err
		}
		switch op {
		case parser.OpAdd:
			return numericAdd(a, b)
		case parser.OpSub:
			return numericSub(a, b)
		case parser.OpMul:
			return numericMul(a, b)
		case parser.OpDiv:
			return numericDiv(a, b, 9)
		}
		return numericMod(a, b, 9)
	}
	var pairs [][2]Datum
	for _, n := range numerics {
		for _, m := range numerics {
			pairs = append(pairs, [2]Datum{n, m})
		}
		for _, o := range others {
			pairs = append(pairs, [2]Datum{n, o}, [2]Datum{o, n})
		}
	}
	render := func(d Datum) string {
		if d.Kind != KindNumeric {
			return fmt.Sprintf("%#v", d)
		}
		return fmt.Sprintf("%s/%d", numericMant(d), d.Scale)
	}
	for _, op := range []parser.OpCode{parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod} {
		for _, p := range pairs {
			want, werr := ref(op, p[0], p[1])
			got, gerr := evalBinary(op, p[0], p[1], 9, nil)
			if (werr == nil) != (gerr == nil) || (werr != nil && werr.Error() != gerr.Error()) {
				t.Fatalf("%s (%s, %s): err %v, want %v", op, render(p[0]), render(p[1]), gerr, werr)
			}
			if werr == nil && render(got) != render(want) {
				t.Fatalf("%s (%s, %s) = %s, want %s", op, render(p[0]), render(p[1]), render(got), render(want))
			}
		}
	}
}
