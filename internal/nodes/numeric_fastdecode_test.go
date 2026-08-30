package nodes

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
)

// textPathMantissa reproduces what the executor's numeric arm computed BEFORE
// NumericInt64FromStoredPayload existed: render the payload as numeric_out
// text, then parse that text into a mantissa and scale. It is the oracle the
// fast path must agree with, so these tests compare the new integer decode
// against the behaviour it replaced rather than against a re-derivation of the
// same arithmetic.
func textPathMantissa(t *testing.T, payload []byte) (*big.Int, int, bool) {
	t.Helper()
	text, err := NumericTextFromStoredPayload(payload)
	if err != nil {
		return nil, 0, false
	}
	s := strings.TrimSpace(text)
	neg := false
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	intPart, fracPart := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, fracPart = s[:i], s[i+1:]
	}
	digits := intPart + fracPart
	m, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, 0, false
	}
	if neg {
		m.Neg(m)
	}
	return m, len(fracPart), true
}

// numericCorpus is the set of literals both paths must agree on. It spans the
// short/long header forms, both weight signs, the base-10000 digit boundaries
// (values around powers of 10000 are where a weight/ndigits off-by-one shows
// up), trailing-zero dscale preservation, and the int64 edge.
func numericCorpus() []string {
	base := []string{
		"0", "0.0", "0.00", "-0", "1", "-1", "42", "-42",
		"0.05", "-0.05", "0.04", "0.06", "123.45", "-123.45",
		"24", "100000", "1000000000", "9999", "10000", "10001",
		"99999999", "100000000", "0.0001", "0.00001", "0.000000001",
		"1.5", "2.50", "3.100", "0.1", "0.2", "0.3",
		"12345.6789", "-12345.6789", "999999999999.999999",
		"1e5", "1.5e3", "1e-5", "-1e-5",
		// int64 mantissa boundary: 9223372036854775807 is MaxInt64.
		"9223372036854775807", "-9223372036854775808",
		"922337203685477580.7", "9.223372036854775807",
		// Past int64 — must fall back, never wrap.
		"9223372036854775808", "-9223372036854775809",
		"99999999999999999999999999", "1e30", "0.1e-30",
	}
	// TPC-H lineitem-shaped values: this is the data the fix was written for.
	for i := 0; i < 40; i++ {
		base = append(base, fmt.Sprintf("%d.%02d", 1000+i*337, i*7%100))
	}
	return base
}

func TestNumericInt64FromStoredPayload_AgreesWithTextPath(t *testing.T) {
	for _, lit := range numericCorpus() {
		body, err := NumericBodyFromText(lit)
		if err != nil {
			t.Fatalf("NumericBodyFromText(%q): %v", lit, err)
		}
		mant, scale, ok := NumericInt64FromStoredPayload(body)
		wantM, wantScale, wantOK := textPathMantissa(t, body)
		if !wantOK {
			t.Fatalf("%q: text path failed to produce an oracle", lit)
		}
		if !ok {
			// Declining is allowed ONLY when the value genuinely does not fit
			// the int64 lane. A decline on a representable value would be a
			// silent performance regression, so pin that too.
			if wantM.IsInt64() && wantScale <= math.MaxInt16 {
				t.Errorf("%q: fast path declined a representable value (mantissa=%s scale=%d)",
					lit, wantM, wantScale)
			}
			continue
		}
		if !wantM.IsInt64() || wantM.Int64() != mant {
			t.Errorf("%q: mantissa = %d, text path = %s", lit, mant, wantM)
		}
		if int(scale) != wantScale {
			t.Errorf("%q: scale = %d, text path = %d", lit, scale, wantScale)
		}
		// The pair must actually denote the literal: mantissa × 10^-scale.
		got := new(big.Rat).SetFrac(big.NewInt(mant),
			new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
		want := new(big.Rat)
		if _, ok := want.SetString(lit); !ok {
			continue // scientific forms big.Rat won't take; the oracle above covers them
		}
		if got.Cmp(want) != 0 {
			t.Errorf("%q: decoded value %s != %s", lit, got.RatString(), want.RatString())
		}
	}
}

func TestNumericInt64FromStoredPayload_DeclinesSpecialsAndLegacy(t *testing.T) {
	for _, lit := range []string{"NaN", "Infinity", "-Infinity"} {
		body, err := NumericBodyFromText(lit)
		if err != nil {
			t.Fatalf("NumericBodyFromText(%q): %v", lit, err)
		}
		if _, _, ok := NumericInt64FromStoredPayload(body); ok {
			t.Errorf("%q: fast path must decline NUMERIC_SPECIAL", lit)
		}
		// The text path still has to render it.
		if got, err := NumericTextFromStoredPayload(body); err != nil || got != lit {
			t.Errorf("%q: text path = %q, %v", lit, got, err)
		}
	}
	// Legacy text payloads (the pre-M0119-0006 stored form) must never be read
	// as a NumericData header.
	for _, legacy := range []string{"123.45", "0.05", "-7", "1e5", "NaN", "-Infinity"} {
		if _, _, ok := NumericInt64FromStoredPayload([]byte(legacy)); ok {
			t.Errorf("legacy payload %q: fast path must decline", legacy)
		}
	}
	if _, _, ok := NumericInt64FromStoredPayload(nil); ok {
		t.Error("nil payload must decline")
	}
	if _, _, ok := NumericInt64FromStoredPayload([]byte{0x01}); ok {
		t.Error("1-byte payload must decline")
	}
}

// TestNumericPayloadIsLegacyText_ReorderEquivalence pins the Fix-A claim: the
// reordered implementation answers identically to the original ordering on
// every input class the discrimination has to separate.
func TestNumericPayloadIsLegacyText_ReorderEquivalence(t *testing.T) {
	original := func(payload []byte) bool {
		if len(payload) == 0 {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(string(payload))) {
		case "nan", "infinity", "-infinity", "+infinity", "inf", "-inf", "+inf":
			return true
		}
		for _, b := range payload {
			switch {
			case b >= '0' && b <= '9':
			case b == '+' || b == '-' || b == '.' || b == 'e' || b == 'E':
			default:
				return false
			}
		}
		return true
	}

	var cases [][]byte
	// Legacy text spellings, including case and whitespace variants.
	for _, s := range []string{
		"", "0", "123.45", "-0.05", "1e5", "1E5", "+7", "-7", ".5", "e", "E",
		"NaN", "nan", "NAN", "Infinity", "-Infinity", "+Infinity", "inf",
		"-inf", "+INF", " nan ", "\tNaN\n", "  -Infinity  ", "infinityy",
		"na", "in", "nan ", " nan", "not-a-number", "12 34", "12a34",
	} {
		cases = append(cases, []byte(s))
	}
	// Real NumericData bodies — the payloads the reorder is meant to speed up.
	for _, lit := range numericCorpus() {
		body, err := NumericBodyFromText(lit)
		if err != nil {
			continue
		}
		cases = append(cases, body)
	}
	for _, lit := range []string{"NaN", "Infinity", "-Infinity"} {
		body, _ := NumericBodyFromText(lit)
		cases = append(cases, body)
	}
	// Adversarial binary: every single byte, and every 2-byte header prefix.
	for b := 0; b < 256; b++ {
		cases = append(cases, []byte{byte(b)})
		cases = append(cases, []byte{byte(b), 0x00})
		cases = append(cases, []byte{0x00, byte(b)})
	}

	for _, c := range cases {
		if got, want := numericPayloadIsLegacyText(c), original(c); got != want {
			t.Errorf("payload %q (% x): reordered = %v, original = %v", c, c, got, want)
		}
	}
}

func BenchmarkNumericDecodeStoredPayload(b *testing.B) {
	body, err := NumericBodyFromText("12345.67")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("fast-int64", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, _, ok := NumericInt64FromStoredPayload(body); !ok {
				b.Fatal("declined")
			}
		}
	})
	b.Run("text-path", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := NumericTextFromStoredPayload(body); err != nil {
				b.Fatal(err)
			}
		}
	})
}
