package btree

import (
	"bytes"
	"math"
	"math/big"
	"sort"
	"testing"
)

// numCase is a NUMERIC value carrier for ordering tests:
//
//	value = mantissa × 10^(-scale)
type numCase struct {
	name     string
	mantissa int64
	scale    int16
}

func encodeNumericInt64(m int64, s int16) []byte {
	return EncodeNumericKey(big.NewInt(m), s)
}

// TestEncodeNumericKeyZeroIsSingleByte pins the zero sentinel.
// The B-tree's int4 encoding is fixed-width 4 bytes, but for NUMERIC
// the encoding is variable; zero gets its own short sentinel that
// orders strictly between all negatives and all positives.
func TestEncodeNumericKeyZeroIsSingleByte(t *testing.T) {
	got := encodeNumericInt64(0, 0)
	if len(got) != 1 || got[0] != 0x01 {
		t.Fatalf("encode(0,0) = %x, want 01", got)
	}
	// Different (m=0, s=arbitrary) inputs must all hit the same
	// sentinel — the carrier doesn't represent "negative zero".
	if !bytes.Equal(encodeNumericInt64(0, 0), encodeNumericInt64(0, 100)) {
		t.Fatalf("zero sentinel must be scale-independent")
	}
}

// TestEncodeNumericKeyScaleInvariance pins the UNIQUE/PRIMARY-KEY
// equality contract: numerically-equal values produce identical bytes
// regardless of how the literal was written.
func TestEncodeNumericKeyScaleInvariance(t *testing.T) {
	groups := [][]numCase{
		// 1 == 1.0 == 1.00 == 1.000
		{{"1", 1, 0}, {"1.0", 10, 1}, {"1.00", 100, 2}, {"1.000", 1000, 3}},
		// 10 == 10.0 == 10.00
		{{"10", 10, 0}, {"10.0", 100, 1}, {"10.00", 1000, 2}},
		// -3.14 == -3.140 == -3.1400
		{{"-3.14", -314, 2}, {"-3.140", -3140, 3}, {"-3.1400", -31400, 4}},
		// 0.5 == 0.50
		{{"0.5", 5, 1}, {"0.50", 50, 2}},
	}
	for _, g := range groups {
		base := encodeNumericInt64(g[0].mantissa, g[0].scale)
		for _, c := range g[1:] {
			got := encodeNumericInt64(c.mantissa, c.scale)
			if !bytes.Equal(base, got) {
				t.Errorf("%s vs %s: encodings diverge\n  %s -> %x\n  %s -> %x",
					g[0].name, c.name, g[0].name, base, c.name, got)
			}
		}
	}
}

// TestEncodeNumericKeySignOrder pins the cross-sign ordering: every
// negative encoding sorts before zero, which sorts before every
// positive encoding. The single-byte sign prefix alone settles this.
func TestEncodeNumericKeySignOrder(t *testing.T) {
	zero := encodeNumericInt64(0, 0)
	negs := []numCase{{"-1", -1, 0}, {"-1.5", -15, 1}, {"-100", -100, 0}, {"min", math.MinInt64, 0}}
	pos := []numCase{{"1", 1, 0}, {"0.001", 1, 3}, {"100", 100, 0}, {"max", math.MaxInt64, 0}}
	for _, n := range negs {
		b := encodeNumericInt64(n.mantissa, n.scale)
		if bytes.Compare(b, zero) >= 0 {
			t.Errorf("encode(%s) >= encode(0): %x >= %x", n.name, b, zero)
		}
	}
	for _, p := range pos {
		b := encodeNumericInt64(p.mantissa, p.scale)
		if bytes.Compare(b, zero) <= 0 {
			t.Errorf("encode(%s) <= encode(0): %x <= %x", p.name, b, zero)
		}
	}
}

// TestEncodeNumericKeyMonotone is the headline contract test: take a
// representative slice of NUMERIC values across signs, magnitudes, and
// scales, sort them numerically, then assert their encodings sort the
// same way under bytewise comparison (CompareKeys). If this passes,
// the B-tree's existing CompareKeys / RangeScan logic transparently
// supports NUMERIC keys.
func TestEncodeNumericKeyMonotone(t *testing.T) {
	cases := []numCase{
		{"-1e9", -1_000_000_000, 0},
		{"-100", -100, 0},
		{"-99.99", -9999, 2},
		{"-10", -10, 0},
		{"-1.99", -199, 2},
		{"-1.9", -19, 1},
		{"-1.5", -15, 1},
		{"-1", -1, 0},
		{"-0.5", -5, 1},
		{"-0.001", -1, 3},
		{"-0.0001", -1, 4},
		{"0", 0, 0},
		{"0.0001", 1, 4},
		{"0.001", 1, 3},
		{"0.5", 5, 1},
		{"1", 1, 0},
		{"1.5", 15, 1},
		{"1.9", 19, 1},
		{"1.99", 199, 2},
		{"10", 10, 0},
		{"99.99", 9999, 2},
		{"100", 100, 0},
		{"1e9", 1_000_000_000, 0},
	}
	// Numeric sort by aligning scales then comparing mantissas, so
	// the test's "expected order" is independent of the encoding
	// under test.
	sort.Slice(cases, func(i, j int) bool { return numericLess(cases[i], cases[j]) })
	for i := 1; i < len(cases); i++ {
		a := encodeNumericInt64(cases[i-1].mantissa, cases[i-1].scale)
		b := encodeNumericInt64(cases[i].mantissa, cases[i].scale)
		cmp := CompareKeys(a, b)
		if cmp >= 0 {
			t.Errorf("CompareKeys(%s, %s) = %d, want < 0\n  %s -> %x\n  %s -> %x",
				cases[i-1].name, cases[i].name, cmp,
				cases[i-1].name, a, cases[i].name, b)
		}
	}
}

// TestEncodeNumericKeySamePrefixDifferentLengths pins the digit-length
// terminator behaviour: with the same exponent, longer mantissa = more
// digits = larger absolute value (e.g. 1.9 vs 1.99 in the positive
// case, and the mirror for negatives). The terminators (0x00 / 0xFF)
// make bytewise compare match.
func TestEncodeNumericKeySamePrefixDifferentLengths(t *testing.T) {
	pairs := []struct{ a, b numCase }{
		{numCase{"1.9", 19, 1}, numCase{"1.99", 199, 2}},     // 1.9 < 1.99
		{numCase{"1", 1, 0}, numCase{"1.5", 15, 1}},          // 1 < 1.5
		{numCase{"-1.99", -199, 2}, numCase{"-1.9", -19, 1}}, // -1.99 < -1.9
		{numCase{"-1.5", -15, 1}, numCase{"-1", -1, 0}},      // -1.5 < -1
	}
	for _, p := range pairs {
		ea := encodeNumericInt64(p.a.mantissa, p.a.scale)
		eb := encodeNumericInt64(p.b.mantissa, p.b.scale)
		if CompareKeys(ea, eb) >= 0 {
			t.Errorf("encode(%s) < encode(%s) expected, got %x !< %x", p.a.name, p.b.name, ea, eb)
		}
	}
}

// TestEncodeNumericKeyMinInt64 pins the int64-min edge case: -2^63
// cannot be negated as int64, so the encoder must use unsigned
// arithmetic to compute the absolute value. Without the special
// case, |MinInt64| wraps and the encoding collides with positive
// zero or worse.
func TestEncodeNumericKeyMinInt64(t *testing.T) {
	got := encodeNumericInt64(math.MinInt64, 0)
	if len(got) == 0 || got[0] != 0x00 {
		t.Fatalf("MinInt64 sign byte = %x, want 0x00 (negative)", got)
	}
	// Must compare strictly less than -1, less than -100, less than
	// any in-range negative encountered.
	for _, c := range []numCase{{"-1", -1, 0}, {"-100", -100, 0}, {"-1e18", -1_000_000_000_000_000_000, 0}} {
		ref := encodeNumericInt64(c.mantissa, c.scale)
		if CompareKeys(got, ref) >= 0 {
			t.Errorf("MinInt64 encoding not less than %s: %x !< %x", c.name, got, ref)
		}
	}
}

// numericLess is the test oracle: aligns scales so a × 10^x and
// b × 10^y compare on common-scale mantissas. Independent of
// EncodeNumericKey under test.
func numericLess(a, b numCase) bool {
	am, bm, _ := alignForTest(a.mantissa, a.scale, b.mantissa, b.scale)
	return am < bm
}

func alignForTest(am int64, as int16, bm int64, bs int16) (int64, int64, int16) {
	common := as
	if bs > common {
		common = bs
	}
	for as < common {
		am *= 10
		as++
	}
	for bs < common {
		bm *= 10
		bs++
	}
	return am, bm, common
}
