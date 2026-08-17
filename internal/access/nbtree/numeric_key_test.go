package nbtree

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

// TestDecodeNumericKeyRoundTrip pins DecodeNumericKey as the inverse of
// EncodeNumericKey (M0119-0006: the amcheck operator-class comparator must
// hand the user's FUNCTION 1 routine the *value*, not the key bytes).
//
// The round trip is value-preserving, not byte-preserving: EncodeNumericKey
// strips trailing mantissa zeros, so (150,2) comes back as (15,1). Both denote
// 1.5, which is what every caller compares on.
func TestDecodeNumericKeyRoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		mant      int64
		scale     int16
		wantMant  int64
		wantScale int16
	}{
		{"zero", 0, 0, 0, 0},
		{"zero-scaled", 0, 4, 0, 0},
		{"one", 1, 0, 1, 0},
		{"one-point-five", 15, 1, 15, 1},
		{"trailing-zeros-normalise", 150, 2, 15, 1},
		{"hundred", 100, 0, 100, 0},
		{"negative", -12345, 3, -12345, 3},
		{"negative-int", -42, 0, -42, 0},
		{"tiny", 1, 9, 1, 9},
		{"negative-tiny", -7, 9, -7, 9},
		{"max-int64", math.MaxInt64, 0, math.MaxInt64, 0},
		{"min-int64", math.MinInt64, 0, math.MinInt64, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc := encodeNumericInt64(c.mant, c.scale)
			m, s, n, err := DecodeNumericKey(enc)
			if err != nil {
				t.Fatalf("DecodeNumericKey(%d,%d): %v", c.mant, c.scale, err)
			}
			if n != len(enc) {
				t.Fatalf("consumed %d bytes, key is %d", n, len(enc))
			}
			if m.Cmp(big.NewInt(c.wantMant)) != 0 || s != c.wantScale {
				t.Fatalf("got (%s,%d), want (%d,%d)", m.String(), s, c.wantMant, c.wantScale)
			}
			// Re-encoding the decoded pair must reproduce the same key bytes.
			if got := EncodeNumericKey(m, s); !bytes.Equal(got, enc) {
				t.Fatalf("re-encode mismatch: %x vs %x", got, enc)
			}
		})
	}
}

// TestDecodeNumericKeyBigMantissa covers the big.Int lane (M0041-0004 widened
// the encoder past int64; the decoder must not narrow it back).
func TestDecodeNumericKeyBigMantissa(t *testing.T) {
	big1, _ := new(big.Int).SetString("123456789012345678901234567891", 10)
	for _, mant := range []*big.Int{big1, new(big.Int).Neg(big1)} {
		enc := EncodeNumericKey(mant, 5)
		m, s, n, err := DecodeNumericKey(enc)
		if err != nil {
			t.Fatalf("DecodeNumericKey: %v", err)
		}
		if n != len(enc) || s != 5 || m.Cmp(mant) != 0 {
			t.Fatalf("got (%s,%d,%d), want (%s,5,%d)", m, s, n, mant, len(enc))
		}
	}
}

// TestDecodeNumericKeyComposite pins the self-delimiting property: a decoder
// handed a composite key that continues past this column must consume exactly
// this column's bytes. This is the contract the amcheck column-by-column walk
// (btIndexOpClassComparator) depends on.
func TestDecodeNumericKeyComposite(t *testing.T) {
	first := encodeNumericInt64(-2500, 2)
	second := encodeNumericInt64(31415, 4)
	composite := append(append([]byte{}, first...), second...)

	m, s, n, err := DecodeNumericKey(composite)
	if err != nil {
		t.Fatalf("first column: %v", err)
	}
	if n != len(first) || m.Cmp(big.NewInt(-25)) != 0 || s != 0 {
		t.Fatalf("first column got (%s,%d,%d), want (-25,0,%d)", m, s, n, len(first))
	}
	m2, s2, n2, err := DecodeNumericKey(composite[n:])
	if err != nil {
		t.Fatalf("second column: %v", err)
	}
	if n2 != len(second) || m2.Cmp(big.NewInt(31415)) != 0 || s2 != 4 {
		t.Fatalf("second column got (%s,%d,%d), want (31415,4,%d)", m2, s2, n2, len(second))
	}
}

// TestDecodeNumericKeyRejectsGarbage: a key slice that is not a numeric key
// must error rather than manufacture a value — the comparator falls back to
// byte order on error, which is only safe if errors are actually reported.
func TestDecodeNumericKeyRejectsGarbage(t *testing.T) {
	for _, bad := range [][]byte{
		{},
		{0x7F},                                   // bad sign byte
		{0x02, 0x80, 0x00, 0x00},                 // truncated exponent
		{0x02, 0x80, 0x00, 0x00, 0x00, '1', '2'}, // missing terminator
		{0x02, 0x80, 0x00, 0x00, 0x00, 'x', 0x00}, // non-digit
		{0x02, 0x80, 0x00, 0x00, 0x00, 0x00},      // no digits
	} {
		if _, _, _, err := DecodeNumericKey(bad); err == nil {
			t.Fatalf("DecodeNumericKey(%x) = nil error, want error", bad)
		}
	}
}
