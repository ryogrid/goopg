package nodes

import (
	"bytes"
	"testing"
)

// TestNumericSpecialSpellingGateIsExact pins M0122-0012's first-byte pre-gate in
// numericPayloadIsSpecialSpelling. Every payload must get the answer of the
// reference rule: bytes.TrimSpace, then a case-insensitive match against a
// spelling. ASCII and Unicode whitespace, NumericData-looking headers and
// near-miss spellings are covered.
func TestNumericSpecialSpellingGateIsExact(t *testing.T) {
	reference := func(p []byte) bool {
		tr := bytes.TrimSpace(p)
		for _, sp := range numericSpecialSpellings {
			if bytes.EqualFold(tr, sp) {
				return true
			}
		}
		return false
	}
	cases := [][]byte{
		nil, {}, []byte("nan"), []byte("NaN"), []byte(" nan"), []byte("\tInfinity\n"), []byte("-inf"),
		[]byte("+INF"), []byte("\vinfinity\f"), []byte("\r-Infinity "), []byte(" nan"), []byte("\u0085inf"),
		[]byte(" nan"), []byte("nanx"), []byte("infinit"), []byte("x"), []byte("1.5"), []byte("  "),
		{0x00, 0x80, 0x01, 0x00}, {0x01, 0x80}, {0xc0, 0x00}, {'n', 0x00}, {'-', 'i', 'n', 'f'},
	}
	for _, c := range cases {
		if got, want := numericPayloadIsSpecialSpelling(c), reference(c); got != want {
			t.Errorf("payload %q: gate %v, reference %v", c, got, want)
		}
	}
	// Exhaustive single- and two-byte payloads: the gate decides on byte 0.
	for a := 0; a < 256; a++ {
		one := []byte{byte(a)}
		if numericPayloadIsSpecialSpelling(one) != reference(one) {
			t.Fatalf("payload %q disagrees", one)
		}
		for _, b := range []byte{'n', 'f', ' ', 0x00, 0xa0} {
			two := []byte{byte(a), b}
			if numericPayloadIsSpecialSpelling(two) != reference(two) {
				t.Fatalf("payload %q disagrees", two)
			}
		}
	}
}
