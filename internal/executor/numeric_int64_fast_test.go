package executor

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// TestMulInt64Pow10Basic pins 10^0 .. 10^18 round-trip
// and overflow detection at the boundary.
func TestMulInt64Pow10Basic(t *testing.T) {
	cases := []struct {
		v       int64
		exp     int
		want    int64
		wantOK  bool
	}{
		{0, 0, 0, true},
		{1, 0, 1, true},
		{1, 18, 1000000000000000000, true},
		{1, 19, 0, false},                    // 10^19 overflows
		{0, 19, 0, true},                     // zero never overflows
		{0, 20, 0, true},                     // zero never overflows
		{9, 18, 9000000000000000000, true},   // 9*10^18 fits in MaxInt64=9.22e18
		{10, 18, 0, false},                   // 10*10^18 overflows
		{1, 17, 100000000000000000, true},
		{-1, 18, -1000000000000000000, true},
		{math.MaxInt64 / 10, 1, 9223372036854775800, true},
		{math.MaxInt64/10 + 1, 1, 0, false},
		{math.MinInt64 / 10, 1, -9223372036854775800, true},
		{-100, 17, 0, false}, // -100 * 10^17 overflows MinInt64 boundary
	}
	for _, tc := range cases {
		got, ok := mulInt64Pow10(tc.v, tc.exp)
		if ok != tc.wantOK {
			t.Errorf("mulInt64Pow10(%d, %d) ok=%v want %v", tc.v, tc.exp, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("mulInt64Pow10(%d, %d) = %d want %d", tc.v, tc.exp, got, tc.want)
		}
	}
}

// TestAlignNumericInt64Equal pins same-scale path.
func TestAlignNumericInt64Equal(t *testing.T) {
	am, bm, ok := alignNumericInt64(123, 2, 456, 2)
	if !ok || am != 123 || bm != 456 {
		t.Errorf("alignNumericInt64(123/2, 456/2) = (%d, %d, %v) want (123, 456, true)", am, bm, ok)
	}
}

// TestAlignNumericInt64ScaleUp pins scale-up paths in
// both directions.
func TestAlignNumericInt64ScaleUp(t *testing.T) {
	// a=12.3 (scale=1), b=4.56 (scale=2) → both at scale=2 → (123, 456)
	am, bm, ok := alignNumericInt64(123, 1, 456, 2)
	if !ok || am != 1230 || bm != 456 {
		t.Errorf("alignNumericInt64(123/1, 456/2) = (%d, %d, %v) want (1230, 456, true)", am, bm, ok)
	}
	// a=4.56 (scale=2), b=12.3 (scale=1) → both at scale=2 → (456, 1230)
	am, bm, ok = alignNumericInt64(456, 2, 123, 1)
	if !ok || am != 456 || bm != 1230 {
		t.Errorf("alignNumericInt64(456/2, 123/1) = (%d, %d, %v) want (456, 1230, true)", am, bm, ok)
	}
}

// TestAlignNumericInt64Overflow pins fallback when scale
// alignment would overflow int64.
func TestAlignNumericInt64Overflow(t *testing.T) {
	// am=MaxInt64, scale=0; b=1, scale=1 → would scale am
	// up by 10× → overflow.
	_, _, ok := alignNumericInt64(math.MaxInt64, 0, 1, 1)
	if ok {
		t.Errorf("alignNumericInt64 overflow case returned ok=true")
	}
}

// TestNumericCmpInt64FastBasic pins comparison at common
// TPC-H scales (0, 2, 6, 15) for equal/less/greater.
func TestNumericCmpInt64FastBasic(t *testing.T) {
	cases := []struct {
		a, b Datum
		want int
	}{
		// scale=0, both ints
		{Datum{Kind: KindNumeric, Int: 5, Scale: 0}, Datum{Kind: KindNumeric, Int: 5, Scale: 0}, 0},
		{Datum{Kind: KindNumeric, Int: 5, Scale: 0}, Datum{Kind: KindNumeric, Int: 7, Scale: 0}, -1},
		{Datum{Kind: KindNumeric, Int: 7, Scale: 0}, Datum{Kind: KindNumeric, Int: 5, Scale: 0}, 1},
		// scale=2 (currency)
		{Datum{Kind: KindNumeric, Int: 12345, Scale: 2}, Datum{Kind: KindNumeric, Int: 12345, Scale: 2}, 0},
		{Datum{Kind: KindNumeric, Int: 12345, Scale: 2}, Datum{Kind: KindNumeric, Int: 12346, Scale: 2}, -1},
		// cross-scale: 1.0 = 1.00 = 1.000
		{Datum{Kind: KindNumeric, Int: 1, Scale: 0}, Datum{Kind: KindNumeric, Int: 100, Scale: 2}, 0},
		{Datum{Kind: KindNumeric, Int: 100, Scale: 2}, Datum{Kind: KindNumeric, Int: 1, Scale: 0}, 0},
		// negative
		{Datum{Kind: KindNumeric, Int: -5, Scale: 0}, Datum{Kind: KindNumeric, Int: 5, Scale: 0}, -1},
		{Datum{Kind: KindNumeric, Int: -5, Scale: 0}, Datum{Kind: KindNumeric, Int: -3, Scale: 0}, -1},
	}
	for _, tc := range cases {
		got, err := numericCmp(tc.a, tc.b)
		if err != nil {
			t.Errorf("numericCmp(%v, %v) error: %v", tc.a, tc.b, err)
			continue
		}
		if got != tc.want {
			t.Errorf("numericCmp(%d/scale=%d, %d/scale=%d) = %d want %d",
				tc.a.Int, tc.a.Scale, tc.b.Int, tc.b.Scale, got, tc.want)
		}
	}
}

// TestNumericCmpInt64FastFallsBackToBig pins that
// overflow during alignment falls through to the big.Int
// path and produces correct results.
func TestNumericCmpInt64FastFallsBackToBig(t *testing.T) {
	// a = 1 at scale=0; b = 100 at scale=2 — both fit,
	// equal. (Sanity.)
	cmp, err := numericCmp(
		Datum{Kind: KindNumeric, Int: 1, Scale: 0},
		Datum{Kind: KindNumeric, Int: 100, Scale: 2},
	)
	if err != nil || cmp != 0 {
		t.Errorf("equal cross-scale int64 cmp wrong: %d, %v", cmp, err)
	}
	// a = MaxInt64 at scale=0 vs b = 1 at scale=18.
	// Scaling a up by 10^18 overflows. Big path
	// handles correctly: a >>> b.
	cmp, err = numericCmp(
		Datum{Kind: KindNumeric, Int: math.MaxInt64, Scale: 0},
		Datum{Kind: KindNumeric, Int: 1, Scale: 18},
	)
	if err != nil {
		t.Fatalf("big-fallback cmp error: %v", err)
	}
	if cmp != 1 {
		t.Errorf("big-fallback cmp = %d want 1 (a > b)", cmp)
	}
}

// TestNumericCmpInt64FastVsBigPath verifies the int64
// fast path produces the same result as the big.Int path
// across 1000 random pairs that fit in int64 at common
// TPC-H scales (0, 2, 6).
func TestNumericCmpInt64FastVsBigPath(t *testing.T) {
	rng := rand.New(rand.NewSource(0xdeadbeef))
	for i := 0; i < 1000; i++ {
		am := rng.Int63n(1_000_000_000) - 500_000_000
		bm := rng.Int63n(1_000_000_000) - 500_000_000
		ascale := int16([]int{0, 2, 6}[rng.Intn(3)])
		bscale := int16([]int{0, 2, 6}[rng.Intn(3)])
		a := Datum{Kind: KindNumeric, Int: am, Scale: ascale}
		b := Datum{Kind: KindNumeric, Int: bm, Scale: bscale}
		// Fast path
		fast, err := numericCmp(a, b)
		if err != nil {
			t.Fatalf("fast path error: %v", err)
		}
		// Forced big path: convert to *big.Int operands first.
		aBig := Datum{Kind: KindNumeric, Big: big.NewInt(am), Scale: ascale}
		bBig := Datum{Kind: KindNumeric, Big: big.NewInt(bm), Scale: bscale}
		slow, err := numericCmp(aBig, bBig)
		if err != nil {
			t.Fatalf("slow path error: %v", err)
		}
		if fast != slow {
			t.Errorf("cmp differs for am=%d ascale=%d bm=%d bscale=%d: fast=%d slow=%d",
				am, ascale, bm, bscale, fast, slow)
		}
	}
}

// TestAddSubInt64Overflow pins the overflow detector.
func TestAddSubInt64Overflow(t *testing.T) {
	// add overflow
	if _, ok := addInt64Overflow(math.MaxInt64, 1); ok {
		t.Errorf("add overflow not detected")
	}
	if _, ok := addInt64Overflow(math.MinInt64, -1); ok {
		t.Errorf("add underflow not detected")
	}
	// sub overflow
	if _, ok := subInt64Overflow(math.MinInt64, 1); ok {
		t.Errorf("sub overflow not detected")
	}
	if _, ok := subInt64Overflow(math.MaxInt64, -1); ok {
		t.Errorf("sub overflow not detected")
	}
	// non-overflow
	if r, ok := addInt64Overflow(100, 200); !ok || r != 300 {
		t.Errorf("add 100+200 = %d ok=%v", r, ok)
	}
	if r, ok := subInt64Overflow(100, 200); !ok || r != -100 {
		t.Errorf("sub 100-200 = %d ok=%v", r, ok)
	}
}

// TestMulInt64Overflow pins the multiply overflow detector.
func TestMulInt64Overflow(t *testing.T) {
	// MinInt64 * -1 overflows
	if _, ok := mulInt64Overflow(math.MinInt64, -1); ok {
		t.Errorf("MinInt64 * -1 should overflow")
	}
	if _, ok := mulInt64Overflow(-1, math.MinInt64); ok {
		t.Errorf("-1 * MinInt64 should overflow")
	}
	// MaxInt64 / 2 * 3 overflows
	if _, ok := mulInt64Overflow(math.MaxInt64/2+1, 3); ok {
		t.Errorf("MaxInt64/2+1 * 3 should overflow")
	}
	// 0 case
	if r, ok := mulInt64Overflow(math.MaxInt64, 0); !ok || r != 0 {
		t.Errorf("0 * anything = 0; got %d ok=%v", r, ok)
	}
	if r, ok := mulInt64Overflow(0, math.MaxInt64); !ok || r != 0 {
		t.Errorf("anything * 0 = 0; got %d ok=%v", r, ok)
	}
	// non-overflow
	if r, ok := mulInt64Overflow(1234567, 8901234); !ok || r != 1234567*8901234 {
		t.Errorf("1234567 * 8901234 = %d ok=%v", r, ok)
	}
}

// TestNumericArithInt64FastVsBigPath fuzz-tests
// numericAdd / numericSub / numericMul fast path against
// big.Int slow path across 1000 random pairs at common
// TPC-H scales.
func TestNumericArithInt64FastVsBigPath(t *testing.T) {
	rng := rand.New(rand.NewSource(0xfeedface))
	for i := 0; i < 1000; i++ {
		// Bound mantissas so all fast-path ops stay well
		// within int64. Larger ranges land us on the big
		// path which is still tested via the
		// slow-path-equivalence assert.
		am := rng.Int63n(100_000_000) - 50_000_000
		bm := rng.Int63n(100_000_000) - 50_000_000
		ascale := int16([]int{0, 2, 4, 6}[rng.Intn(4)])
		bscale := int16([]int{0, 2, 4, 6}[rng.Intn(4)])
		a := Datum{Kind: KindNumeric, Int: am, Scale: ascale}
		b := Datum{Kind: KindNumeric, Int: bm, Scale: bscale}
		aBig := Datum{Kind: KindNumeric, Big: big.NewInt(am), Scale: ascale}
		bBig := Datum{Kind: KindNumeric, Big: big.NewInt(bm), Scale: bscale}

		// Add
		fastAdd, err := numericAdd(a, b)
		if err != nil {
			t.Fatalf("fastAdd error: %v", err)
		}
		slowAdd, err := numericAdd(aBig, bBig)
		if err != nil {
			t.Fatalf("slowAdd error: %v", err)
		}
		if cmp, _ := numericCmp(fastAdd, slowAdd); cmp != 0 {
			t.Errorf("Add diverges am=%d/%d bm=%d/%d: fast=%v slow=%v",
				am, ascale, bm, bscale, fastAdd, slowAdd)
		}

		// Sub
		fastSub, err := numericSub(a, b)
		if err != nil {
			t.Fatalf("fastSub error: %v", err)
		}
		slowSub, err := numericSub(aBig, bBig)
		if err != nil {
			t.Fatalf("slowSub error: %v", err)
		}
		if cmp, _ := numericCmp(fastSub, slowSub); cmp != 0 {
			t.Errorf("Sub diverges am=%d/%d bm=%d/%d: fast=%v slow=%v",
				am, ascale, bm, bscale, fastSub, slowSub)
		}

		// Mul
		fastMul, err := numericMul(a, b)
		if err != nil {
			t.Fatalf("fastMul error: %v", err)
		}
		slowMul, err := numericMul(aBig, bBig)
		if err != nil {
			t.Fatalf("slowMul error: %v", err)
		}
		if cmp, _ := numericCmp(fastMul, slowMul); cmp != 0 {
			t.Errorf("Mul diverges am=%d/%d bm=%d/%d: fast=%v slow=%v",
				am, ascale, bm, bscale, fastMul, slowMul)
		}
	}
}

// TestNumericArithBigOperandStillCorrect pins that the
// fast-path skip on Big != nil correctly falls through.
func TestNumericArithBigOperandStillCorrect(t *testing.T) {
	// 5 + (big.Int 7) at scale 0 = 12
	a := Datum{Kind: KindNumeric, Int: 5, Scale: 0}
	b := Datum{Kind: KindNumeric, Big: big.NewInt(7), Scale: 0}
	r, err := numericAdd(a, b)
	if err != nil {
		t.Fatalf("add error: %v", err)
	}
	want := Datum{Kind: KindNumeric, Int: 12, Scale: 0}
	if cmp, _ := numericCmp(r, want); cmp != 0 {
		t.Errorf("5 + 7 = %v want %v", r, want)
	}
}

// TestNumericMulOverflowFallsBackToBig pins that mul
// overflow falls through to big.Int and produces the
// correct large product.
func TestNumericMulOverflowFallsBackToBig(t *testing.T) {
	a := Datum{Kind: KindNumeric, Int: math.MaxInt64 / 2, Scale: 0}
	b := Datum{Kind: KindNumeric, Int: 4, Scale: 0}
	r, err := numericMul(a, b)
	if err != nil {
		t.Fatalf("mul error: %v", err)
	}
	expected := new(big.Int).Mul(
		big.NewInt(math.MaxInt64/2),
		big.NewInt(4),
	)
	bigR := r.NumericBigValue()
	if bigR == nil {
		// Result might still fit on the int64 fast path
		// after newNumeric's bounds check. Use accessor.
		bigR = big.NewInt(r.NumericMantissaValue())
	}
	if bigR.Cmp(expected) != 0 {
		t.Errorf("MaxInt64/2 * 4 = %v want %v", bigR, expected)
	}
}
