package executor

import (
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/parser"
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
