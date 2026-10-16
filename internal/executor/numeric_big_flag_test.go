package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/utils/mmgr"
)

// TestIsBigNumericMatchesNumericBigValue pins the M0122-0012 slice 14
// replacement: the numeric arithmetic and comparison fast paths test
// isBigNumeric where they used to test NumericBigValue() against nil, so the
// two must agree on every lane — the int64 lane, a stored big mantissa, a big
// datum whose arena is gone (NumericBigValue then returns a zero big.Int, not
// nil), and non-numeric kinds.
func TestIsBigNumericMatchesNumericBigValue(t *testing.T) {
	huge, _ := new(big.Int).SetString("-123456789012345678901234567890", 10)
	cases := map[string]Datum{
		"int64 lane":      {Kind: KindNumeric, Int: 12345, Scale: 2},
		"int64 lane zero": {Kind: KindNumeric},
		"big lane":        newBigNumericInCtx(mmgr.Perm(), huge, 3),
		"big lane, no arena": {Kind: KindNumeric, Flags: flagBigNumeric,
			Int: 7<<32 | 4},
		"int":  NewIntDatum(9),
		"null": NullDatum,
	}
	for name, d := range cases {
		if got, want := isBigNumeric(d), d.NumericBigValue() != nil; got != want {
			t.Errorf("%s: isBigNumeric=%v, NumericBigValue()!=nil=%v", name, got, want)
		}
	}
	// The fast paths still route a big operand to the exact lane.
	small := Datum{Kind: KindNumeric, Int: 1, Scale: 0}
	sum, err := numericAdd(cases["big lane"], small)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Int).SetString("-123456789012345678901234566890", 10)
	if got := numericMant(sum); got.Cmp(want) != 0 || sum.Scale != 3 {
		t.Fatalf("big + 1 = %s scale %d, want %s scale 3", got, sum.Scale, want)
	}
}
