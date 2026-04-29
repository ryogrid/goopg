package executor

import (
	"strings"
	"testing"
)

// TestNumericDivLargeIntermediateScaleFitsInt64 reproduces the Q14
// failure mode: scaling numerator by 10^6 overflows int64, but the
// final quotient still fits and must succeed.
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
	if got.NumericScale != 6 {
		t.Fatalf("numericDiv scale=%d, want 6", got.NumericScale)
	}
	if got.NumericMantissa != 500000 {
		t.Fatalf("numericDiv mantissa=%d, want 500000", got.NumericMantissa)
	}
}

// TestNumericDivResultOutOfRangeStillErrors ensures we keep the
// int64-bounded contract even though intermediates use big.Int.
func TestNumericDivResultOutOfRangeStillErrors(t *testing.T) {
	a := Datum{Kind: KindNumeric, NumericMantissa: 9223372036854775807, NumericScale: 0}
	b := Datum{Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0}
	_, err := numericDiv(a, b, 0)
	if err == nil {
		t.Fatal("numericDiv unexpectedly succeeded; want overflow error")
	}
	if !strings.Contains(err.Error(), "numeric overflow in divide") {
		t.Fatalf("numericDiv error=%q, want contains %q", err.Error(), "numeric overflow in divide")
	}
}
