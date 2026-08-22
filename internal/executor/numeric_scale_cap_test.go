package executor

import (
	"math/big"
	"testing"
)

// TestNumericMul_HighScaleWithinCap verifies that internal
// arithmetic scale (as opposed to typmod-coercion scale, capped
// separately at NUMERIC_MAX_PRECISION=1000 in roundNumericToScale)
// is allowed up to NUMERIC_DSCALE_MASK=16383
// (postgres/src/backend/utils/adt/numeric.c:236-237). Two operands
// each at scale 800 combine to scale 1600, which exceeds the old
// (incorrect) 1000 ceiling but must succeed since it's well under
// 16383. Mirrors the numeric_big.sql multiply-check case
// (postgres/src/test/regress/sql/numeric_big.sql:562-580).
// (M0134-0050.)
func TestNumericMul_HighScaleWithinCap(t *testing.T) {
	a := Datum{Kind: KindNumeric, Int: 12345, Scale: 800}
	b := Datum{Kind: KindNumeric, Int: 67890, Scale: 800}

	got, err := numericMul(a, b)
	if err != nil {
		t.Fatalf("numericMul with combined scale 1600: unexpected error: %v", err)
	}
	if int(got.Scale) != 1600 {
		t.Errorf("numericMul result scale = %d; want 1600", got.Scale)
	}
	wantMant := big.NewInt(12345 * 67890)
	if got.NumericMantissaValue() != wantMant.Int64() {
		t.Errorf("numericMul result mantissa = %d; want %d", got.NumericMantissaValue(), wantMant.Int64())
	}
}

// TestNumericMul_ScaleExceedsUpperBound verifies the true upper
// ceiling (16383) is still enforced: a combined scale beyond it
// must still error.
func TestNumericMul_ScaleExceedsUpperBound(t *testing.T) {
	a := Datum{Kind: KindNumeric, Int: 1, Scale: 9000}
	b := Datum{Kind: KindNumeric, Int: 1, Scale: 9000}

	_, err := numericMul(a, b)
	if err == nil {
		t.Fatalf("numericMul with combined scale 18000: expected error, got success")
	}
}
