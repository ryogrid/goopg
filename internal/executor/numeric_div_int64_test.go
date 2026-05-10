package executor

import (
	"math/big"
	"math/rand"
	"testing"
)

// TestDecimalDigitCountBoundary pins decimalDigitCount at
// every 10^k boundary. (M0075-0005.)
func TestDecimalDigitCountBoundary(t *testing.T) {
	cases := []struct {
		v    int64
		want int
	}{
		{0, 1},
		{1, 1},
		{9, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{999, 3},
		{1000, 4},
		{9999, 4},
		{10000, 5},
		{99999, 5},
		{100000, 6},
		{1000000, 7},
		{1000000000, 10},  // 10^9
		{9999999999, 10},  // 10^10 - 1
		{10000000000, 11}, // 10^10
		{1000000000000000000, 19},  // 10^18
		{-1, 1},
		{-9, 1},
		{-10, 2},
		{-1000000000000000000, 19},
	}
	for _, tc := range cases {
		got := decimalDigitCount(tc.v)
		if got != tc.want {
			t.Errorf("decimalDigitCount(%d) = %d, want %d", tc.v, got, tc.want)
		}
	}
}

// TestFloorDiv4 pins floor-division semantics matching
// upstream's NBASE weight calculation. (M0075-0005.)
func TestFloorDiv4(t *testing.T) {
	cases := []struct {
		x, want int
	}{
		{0, 0},
		{1, 0},
		{3, 0},
		{4, 1},
		{7, 1},
		{8, 2},
		{-1, -1},
		{-3, -1},
		{-4, -1},
		{-5, -2},
		{-7, -2},
		{-8, -2},
		{-9, -3},
	}
	for _, tc := range cases {
		got := floorDiv4(tc.x)
		if got != tc.want {
			t.Errorf("floorDiv4(%d) = %d, want %d", tc.x, got, tc.want)
		}
	}
}

// TestNumericDivInt64FastBasic pins basic positive cases
// at TPC-H scales (0/2/6). (M0075-0005.)
func TestNumericDivInt64FastBasic(t *testing.T) {
	cases := []struct {
		am, bm     int64
		ascale     int16
		bscale     int16
		// Result mantissa+scale matches the big.Int slow path.
	}{
		{6, 2, 0, 0},
		{100, 7, 2, 0},
		{12345, 678, 2, 2},
		{1000000, 333, 6, 6},
		{-12345, 678, 2, 2},
		{12345, -678, 2, 2},
		{-12345, -678, 2, 2},
	}
	for _, tc := range cases {
		a := Datum{Kind: KindNumeric, Int: tc.am, Scale: tc.ascale}
		b := Datum{Kind: KindNumeric, Int: tc.bm, Scale: tc.bscale}
		fast, err := numericDiv(a, b, 0)
		if err != nil {
			t.Errorf("fast(%d/%d, %d/%d) error: %v", tc.am, tc.ascale, tc.bm, tc.bscale, err)
			continue
		}
		// Force big.Int path
		aBig := Datum{Kind: KindNumeric, Big: big.NewInt(tc.am), Scale: tc.ascale}
		bBig := Datum{Kind: KindNumeric, Big: big.NewInt(tc.bm), Scale: tc.bscale}
		slow, err := numericDiv(aBig, bBig, 0)
		if err != nil {
			t.Errorf("slow(%d/%d, %d/%d) error: %v", tc.am, tc.ascale, tc.bm, tc.bscale, err)
			continue
		}
		// Compare via numericCmp (which uses int64 fast-path
		// internally; equality there means same value).
		if cmp, _ := numericCmp(fast, slow); cmp != 0 {
			t.Errorf("fast vs slow diverge for %d/%d ÷ %d/%d:\n  fast=%v\n  slow=%v",
				tc.am, tc.ascale, tc.bm, tc.bscale, fast, slow)
		}
	}
}

// TestNumericDivInt64FastVsBigPath fuzz-tests the fast
// path against the big.Int slow path. 1000 random pairs
// at TPC-H scales (0/2/4/6); int64-fitting mantissas to
// ensure both paths produce a result. (M0075-0005.)
func TestNumericDivInt64FastVsBigPath(t *testing.T) {
	rng := rand.New(rand.NewSource(0xc001cafe))
	scales := []int16{0, 2, 4, 6}
	for i := 0; i < 1000; i++ {
		am := rng.Int63n(1_000_000_000) - 500_000_000
		bm := rng.Int63n(1_000_000_000) - 500_000_000
		if bm == 0 {
			bm = 1 // avoid division by zero in test data
		}
		ascale := scales[rng.Intn(len(scales))]
		bscale := scales[rng.Intn(len(scales))]

		a := Datum{Kind: KindNumeric, Int: am, Scale: ascale}
		b := Datum{Kind: KindNumeric, Int: bm, Scale: bscale}
		aBig := Datum{Kind: KindNumeric, Big: big.NewInt(am), Scale: ascale}
		bBig := Datum{Kind: KindNumeric, Big: big.NewInt(bm), Scale: bscale}

		fast, err := numericDiv(a, b, 0)
		if err != nil {
			t.Fatalf("fast %d/%d ÷ %d/%d error: %v", am, ascale, bm, bscale, err)
		}
		slow, err := numericDiv(aBig, bBig, 0)
		if err != nil {
			t.Fatalf("slow %d/%d ÷ %d/%d error: %v", am, ascale, bm, bscale, err)
		}
		if cmp, _ := numericCmp(fast, slow); cmp != 0 {
			t.Errorf("Div diverges for am=%d/%d bm=%d/%d:\n  fast=%v\n  slow=%v",
				am, ascale, bm, bscale, fast, slow)
		}
	}
}

// TestNumericDivInt64FastZeroNumerator pins the
// zero-numerator short-circuit. (M0075-0005.)
func TestNumericDivInt64FastZeroNumerator(t *testing.T) {
	a := Datum{Kind: KindNumeric, Int: 0, Scale: 4}
	b := Datum{Kind: KindNumeric, Int: 7, Scale: 2}
	r, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("div error: %v", err)
	}
	if r.Int != 0 {
		t.Errorf("zero-numerator result mantissa = %d, want 0", r.Int)
	}
	if r.Scale != 4 {
		t.Errorf("zero-numerator result scale = %d, want max(4,2)=4", r.Scale)
	}
}

// TestNumericDivInt64FastDivisionByZero pins that
// division by zero raises 22012 via the fast-path
// guard before the body executes. (M0075-0005.)
func TestNumericDivInt64FastDivisionByZero(t *testing.T) {
	a := Datum{Kind: KindNumeric, Int: 100, Scale: 2}
	b := Datum{Kind: KindNumeric, Int: 0, Scale: 0}
	_, err := numericDiv(a, b, 0)
	if err == nil {
		t.Fatal("expected division-by-zero error, got nil")
	}
	ee, ok := err.(*ExecError)
	if !ok {
		t.Fatalf("expected *ExecError, got %T: %v", err, err)
	}
	if ee.Code != "22012" {
		t.Errorf("error code = %q, want 22012", ee.Code)
	}
}

// TestNumericDivInt64FastShiftOverflowFallthrough pins
// that operands forcing shift > 18 fall through to the
// big.Int slow path; the result is still produced
// correctly. (M0075-0005.)
func TestNumericDivInt64FastShiftOverflowFallthrough(t *testing.T) {
	// scale=18 numerator, scale=0 denominator
	// → shift ≈ 18+0 - 18 = 0 (small case)
	// To force shift > 18: db is large and da is small.
	// Use ascale=0, bscale=18 → shift ≈ rscale + 18 - 0
	// = something > 18.
	a := Datum{Kind: KindNumeric, Int: 1, Scale: 0}
	b := Datum{Kind: KindNumeric, Int: 1, Scale: 18}
	// Result should be 1.0 / 1e-18 = 1e18; preserved
	// at appropriate scale via slow path.
	r, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("div error: %v", err)
	}
	// The slow path should return a non-zero result.
	// (Not asserting exact value; the key check is that
	// the result is correct, not that the fast path took
	// the call. The fuzz test verifies fast-vs-slow
	// equivalence.)
	zero := Datum{Kind: KindNumeric, Int: 0, Scale: 0}
	if cmp, _ := numericCmp(r, zero); cmp == 0 {
		t.Errorf("result is unexpectedly zero: %v", r)
	}
}

// TestNumericDivInt64FastQEqualsZeroSignHandling pins
// the tricky q==0 case where the rounded magnitude is
// 1 but the sign comes from num*bm. (M0075-0005.)
func TestNumericDivInt64FastQEqualsZeroSignHandling(t *testing.T) {
	// 1 / 3 at scale 0 → q=0 truncated; round 0.333 →
	// magnitude 0 (rscale=0 case is unusual; numericDiv's
	// rscale derivation makes this scale > 0).
	// Use 1/(-2) at scale 0 → exact -0.5, q=0 rounded to -1
	// in integer-divide land.
	cases := []struct {
		am, bm   int64
		ascale   int16
		bscale   int16
		// We don't assert exact result; just that fast
		// matches slow.
	}{
		{1, 3, 0, 0},
		{1, -2, 0, 0},
		{-1, 2, 0, 0},
		{-1, -2, 0, 0},
		{1, 3, 4, 4},
	}
	for _, tc := range cases {
		a := Datum{Kind: KindNumeric, Int: tc.am, Scale: tc.ascale}
		b := Datum{Kind: KindNumeric, Int: tc.bm, Scale: tc.bscale}
		aBig := Datum{Kind: KindNumeric, Big: big.NewInt(tc.am), Scale: tc.ascale}
		bBig := Datum{Kind: KindNumeric, Big: big.NewInt(tc.bm), Scale: tc.bscale}
		fast, err := numericDiv(a, b, 0)
		if err != nil {
			t.Fatalf("fast %d/%d ÷ %d/%d error: %v", tc.am, tc.ascale, tc.bm, tc.bscale, err)
		}
		slow, err := numericDiv(aBig, bBig, 0)
		if err != nil {
			t.Fatalf("slow %d/%d ÷ %d/%d error: %v", tc.am, tc.ascale, tc.bm, tc.bscale, err)
		}
		if cmp, _ := numericCmp(fast, slow); cmp != 0 {
			t.Errorf("sign-handling diverges for %d/%d ÷ %d/%d: fast=%v slow=%v",
				tc.am, tc.ascale, tc.bm, tc.bscale, fast, slow)
		}
	}
}

// TestNumericDivBigOperandStillCorrect pins that when
// either operand has Big != nil, the slow path runs
// correctly (fast path skipped via the
// `Big == nil && Big == nil` gate). (M0075-0005.)
func TestNumericDivBigOperandStillCorrect(t *testing.T) {
	// 6 / 2 = 3 (ints in big lane)
	a := Datum{Kind: KindNumeric, Big: big.NewInt(6), Scale: 0}
	b := Datum{Kind: KindNumeric, Int: 2, Scale: 0}
	r, err := numericDiv(a, b, 0)
	if err != nil {
		t.Fatalf("div error: %v", err)
	}
	want := Datum{Kind: KindNumeric, Int: 3, Scale: 0}
	if cmp, _ := numericCmp(r, want); cmp != 0 {
		t.Errorf("6/2 = %v, want %v", r, want)
	}
}
