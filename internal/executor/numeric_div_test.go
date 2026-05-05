package executor

import (
	"math/big"
	"testing"
)

// TestNumericDivLargeIntermediateScaleFitsInt64 reproduces the
// pre-M0041-0004 Q14 shape (numerator scales up by 10^6+ and used
// to overflow int64). Post-M0041-0004 the rule changed: rscale is
// chosen by upstream's select_div_scale formula instead of being
// hardcoded to max(left, right, 6), so the result has more
// fractional digits but still represents the same quotient.
func TestNumericDivLargeIntermediateScaleFitsInt64(t *testing.T) {
	a := Datum{Kind: KindNumeric, NumericMantissa: 9223372036855, NumericScale: 4}
	b := Datum{Kind: KindNumeric, NumericMantissa: 18446744073710, NumericScale: 4}
	got, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("numericDiv returned error: %v", err)
	}
	if got.Kind != KindNumeric {
		t.Fatalf("numericDiv kind=%v, want KindNumeric", got.Kind)
	}
	// Quotient is 0.5 exactly; the upstream rule produces
	// rscale = NUMERIC_MIN_SIG_DIGITS - qweight (with qweight = -1
	// because firstdigit1=9, firstdigit2=1 and 9>1 so no
	// decrement, and weight1 = weight2 = 8 → qweight = 0 → -1
	// after the first-digit comparison) ≈ 17 fractional digits.
	// Format the result and check it equals 0.5 numerically.
	gotText := numericText(got)
	if gotText != formatExpected(t, big.NewRat(1, 2), int(got.NumericScale)) {
		t.Fatalf("numericDiv = %q, want exact representation of 0.5 at scale %d", gotText, got.NumericScale)
	}
}

func formatExpected(t *testing.T, r *big.Rat, scale int) string {
	t.Helper()
	return r.FloatString(scale)
}

// TestNumericDivQ1Avg pins Q1's avg pattern: 70 / 6 →
// "11.6666666666666667" (16 fractional digits, half-rounded).
func TestNumericDivQ1Avg(t *testing.T) {
	a := Datum{Kind: KindNumeric, NumericMantissa: 70, NumericScale: 0}
	b := numericFromInt(6)
	got, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("numericDiv: %v", err)
	}
	want := "11.6666666666666667"
	if numericText(got) != want {
		t.Fatalf("numericDiv 70/6 = %q, want %q", numericText(got), want)
	}
}

// TestNumericDivQ8MktShare exercises the big-mantissa lane: when
// rscale ≥ 19 the mantissa exceeds int64 (Q8's
// "1.00000000000000000000" is mantissa = 10^20). The result must
// land on the *big.Int carrier and format identically.
func TestNumericDivQ8MktShare(t *testing.T) {
	// Construct sums that yield exactly 1.0 at rscale = 20:
	// 100 / 100 with both at scale 20 → numerator weight = -19,
	// denominator weight = -19, qweight = 0, firstdigit1 =
	// firstdigit2 = 1 → qweight = -1, rscale = 17 ... not 20.
	// Use scale-20 inputs to drive rscale = 20 via the dscale
	// floor.
	scale20 := int16(20)
	mant := new(big.Int).Exp(big.NewInt(10), big.NewInt(20), nil) // 10^20
	a := newNumeric(mant, int(scale20))
	b := newNumeric(mant, int(scale20))
	got, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("numericDiv: %v", err)
	}
	want := "1.00000000000000000000"
	if numericText(got) != want {
		t.Fatalf("numericDiv 1.0/1.0 (scale 20) = %q, want %q", numericText(got), want)
	}
}

// TestNumericDivRoundHalfAwayFromZero pins the rounding mode used
// by upstream's div_var (rounding=true): half-away-from-zero, not
// truncation. Q14's expected output requires this rounding.
func TestNumericDivRoundHalfAwayFromZero(t *testing.T) {
	// 5 / 2 = 2.5 — at any rscale ≥ 1 this is exact.
	a := numericFromInt(5)
	b := numericFromInt(2)
	got, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("numericDiv: %v", err)
	}
	// rscale = 16 - qweight, qweight = (0 - 0) - 1 (because
	// firstdigit1=5, firstdigit2=2; 5>2 so NO decrement → qweight=0,
	// rscale=16). Result is "2.5000…0".
	want := "2.5000000000000000"
	if numericText(got) != want {
		t.Fatalf("numericDiv 5/2 = %q, want %q", numericText(got), want)
	}
}
