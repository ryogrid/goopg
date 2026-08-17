package nodes

import (
	"bytes"
	"strings"
	"testing"
)

// The heap-facing entry points added by M0119-0006. The pg_node_tree side of
// this port is already covered; what is new is (a) the payload framing (no
// varlena header) and (b) the dual-form read that lets a pre-flip cluster keep
// decoding.

func TestNumericBodyTextRoundTrip(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"0", "0"},
		{"1", "1"},
		{"-1", "-1"},
		{"1.00", "1.00"}, // dscale is preserved, so a re-encode is stable
		{"1234.5", "1234.5"},
		{"-1234.5", "-1234.5"},
		{"  42  ", "42"}, // numeric_in trims
		{"1e3", "1000"},
		{"-0.000000000001", "-0.000000000001"},
		{"NaN", "NaN"},
		{"Infinity", "Infinity"},
		{"-Infinity", "-Infinity"},
	} {
		body, err := NumericBodyFromText(tc.in)
		if err != nil {
			t.Fatalf("NumericBodyFromText(%q): %v", tc.in, err)
		}
		got, err := NumericTextFromBody(body)
		if err != nil {
			t.Fatalf("NumericTextFromBody(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The body carries no varlena header — the heap adds its own framing, and a
// stray 4 bytes here would shift every following column of the tuple.
func TestNumericBodyHasNoVarlenaHeader(t *testing.T) {
	body, err := NumericBodyFromText("1234.5")
	if err != nil {
		t.Fatalf("NumericBodyFromText: %v", err)
	}
	// short header 0x8080 (NUMERIC_SHORT | dscale 1 << 7 | weight 0), digits
	// 1234 and 5000, little-endian.
	want := []byte{0x80, 0x80, 0xd2, 0x04, 0x88, 0x13}
	if !bytes.Equal(body, want) {
		t.Fatalf("body = % x, want % x", body, want)
	}
}

func TestNumericBodyRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "abc", "1.2.3", "--1", "1e", "0x10"} {
		if _, err := NumericBodyFromText(in); err == nil {
			t.Errorf("NumericBodyFromText(%q) accepted garbage", in)
		}
	}
}

// The dual read: a legacy (decimal-string) payload and a NumericData payload
// both render the same text, and neither is ever taken for the other.
func TestNumericTextFromStoredPayloadAcceptsBothForms(t *testing.T) {
	for _, text := range []string{"0", "1", "-1", "1.00", "1234.5", "-1234.5", "1e100", "NaN", "-Infinity"} {
		body, err := NumericBodyFromText(text)
		if err != nil {
			t.Fatalf("NumericBodyFromText(%q): %v", text, err)
		}
		canonical, err := NumericTextFromBody(body)
		if err != nil {
			t.Fatalf("NumericTextFromBody(%q): %v", text, err)
		}
		gotNew, err := NumericTextFromStoredPayload(body)
		if err != nil {
			t.Fatalf("stored(new-format %q): %v", text, err)
		}
		if gotNew != canonical {
			t.Errorf("new-format %q rendered %q, want %q", text, gotNew, canonical)
		}
		gotLegacy, err := NumericTextFromStoredPayload([]byte(canonical))
		if err != nil {
			t.Fatalf("stored(legacy %q): %v", text, err)
		}
		if gotLegacy != canonical {
			t.Errorf("legacy %q rendered %q", canonical, gotLegacy)
		}
	}
}

// The disjointness the dual read rests on, swept rather than argued: no
// NumericData body over a wide value set is spellable from the decimal-literal
// charset, so no new-format payload can be mistaken for legacy text.
func TestNumericStoredFormsCannotCollide(t *testing.T) {
	texts := []string{"0", "NaN", "Infinity", "-Infinity"}
	for _, mant := range []string{"1", "9", "12", "1234", "999999", "12345678901234567890"} {
		for _, scale := range []string{"", ".5", ".00", ".000000000000000000000000000000000000000000000000000000000000000000001"} {
			texts = append(texts, mant+scale, "-"+mant+scale)
		}
	}
	for _, text := range texts {
		body, err := NumericBodyFromText(text)
		if err != nil {
			t.Fatalf("NumericBodyFromText(%q): %v", text, err)
		}
		if numericPayloadIsLegacyText(body) {
			t.Errorf("NumericData body for %q (% x) reads as legacy text", text, body)
		}
	}
	// ...and in the other direction, every legacy spelling is recognised.
	for _, text := range append([]string{"1e100", "+1", ".5"}, texts...) {
		if !numericPayloadIsLegacyText([]byte(text)) {
			t.Errorf("legacy payload %q not recognised as text", text)
		}
	}
	// A payload that is neither must surface as an error rather than silently
	// rendering. {0x00, 0x00} is a NUMERIC_POS long header with no room for
	// the int16 weight that form mandates.
	if _, err := NumericTextFromStoredPayload([]byte{0x00, 0x00}); err == nil ||
		!strings.Contains(err.Error(), "numeric") {
		t.Errorf("corrupt payload: err = %v, want a numeric decode error", err)
	}
}
