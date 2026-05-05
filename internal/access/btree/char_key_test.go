package btree

import (
	"bytes"
	"testing"
)

// TestEncodeCharTrimEquality is the key contract for char(N) encoding:
// 'A' and 'A         ' (any trailing spaces) must produce identical
// bytes, matching PostgreSQL's blank-padded comparison semantics.
func TestEncodeCharTrimEquality(t *testing.T) {
	cases := []struct {
		a, b string
	}{
		{"A", "A         "},        // 1 vs 10 chars
		{"BUILDING", "BUILDING  "}, // no vs 2 trailing spaces
		{"", "          "},         // empty vs all-spaces
		{" A", " A "},              // leading space preserved, trailing trimmed
	}
	for _, tc := range cases {
		ea := EncodeChar([]byte(tc.a))
		eb := EncodeChar([]byte(tc.b))
		if !bytes.Equal(ea, eb) {
			t.Errorf("EncodeChar(%q) != EncodeChar(%q): %v vs %v", tc.a, tc.b, ea, eb)
		}
	}
}

// TestEncodeCharMonotone verifies that bytewise order of char-encoded
// values matches the expected SQL ordering (C locale, trailing-space-
// trimmed).
func TestEncodeCharMonotone(t *testing.T) {
	// All-spaces sorts as empty; 'B' sorts after 'A'; 'AB' sorts after 'A'.
	ordered := []string{
		"          ", // all spaces → empty
		"A",
		"A         ", // same as "A" after trim, but listed for clarity
		"AUTOMOBILE",
		"B",
		"BUILDING",
		"FURNITURE",
		"MACHINERY",
	}
	// Build trimmed-unique list to verify ordering.
	seen := map[string]bool{}
	var deduped [][]byte
	for _, s := range ordered {
		k := string(EncodeChar([]byte(s)))
		if seen[k] {
			continue
		}
		seen[k] = true
		deduped = append(deduped, EncodeChar([]byte(s)))
	}
	for i := 0; i < len(deduped)-1; i++ {
		if bytes.Compare(deduped[i], deduped[i+1]) >= 0 {
			t.Fatalf("monotone violated at index %d: %v >= %v", i, deduped[i], deduped[i+1])
		}
	}
}

// TestEncodeCharAllSpaces verifies that an all-spaces char(N) payload
// encodes to the same single byte as an empty string.
func TestEncodeCharAllSpaces(t *testing.T) {
	enc := EncodeChar([]byte("          "))
	if !bytes.Equal(enc, []byte{0x00}) {
		t.Fatalf("all-spaces: want [0x00] (empty encoding), got %v", enc)
	}
	if !bytes.Equal(enc, EncodeChar([]byte{})) {
		t.Fatalf("all-spaces should equal empty string encoding")
	}
}

// TestEncodeCharLeadingSpacePreserved verifies that only trailing
// spaces are trimmed; leading or embedded spaces survive.
func TestEncodeCharLeadingSpacePreserved(t *testing.T) {
	// " A" and "A" are different (leading space preserved).
	spaceA := EncodeChar([]byte(" A"))
	justA := EncodeChar([]byte("A"))
	if bytes.Equal(spaceA, justA) {
		t.Fatalf("EncodeChar(' A') should differ from EncodeChar('A')")
	}
	// " A" sorts before "A" because space (0x20) < 'A' (0x41).
	if bytes.Compare(spaceA, justA) >= 0 {
		t.Fatalf("EncodeChar(' A') should < EncodeChar('A')")
	}
}

// TestEncodeCharTpchShapes covers the TPC-H char columns that will
// use this encoding in production (c_mktsegment, l_returnflag,
// l_linestatus, l_shipmode, o_orderstatus).
func TestEncodeCharTpchShapes(t *testing.T) {
	// c_mktsegment char(10) values (stored with trailing spaces in
	// some loaders but compared without).
	segs := [][2]string{
		{"AUTOMOBILE", "AUTOMOBILE"},
		{"BUILDING", "BUILDING  "},
		{"FURNITURE", "FURNITURE "},
	}
	for _, tc := range segs {
		a := EncodeChar([]byte(tc[0]))
		b := EncodeChar([]byte(tc[1]))
		if !bytes.Equal(a, b) {
			t.Errorf("mktsegment parity: EncodeChar(%q) != EncodeChar(%q)", tc[0], tc[1])
		}
	}

	// l_returnflag char(1), l_linestatus char(1).
	flags := []string{"A", "N", "R"}
	for i := 0; i < len(flags)-1; i++ {
		a := EncodeChar([]byte(flags[i]))
		b := EncodeChar([]byte(flags[i+1]))
		if bytes.Compare(a, b) >= 0 {
			t.Errorf("returnflag order violated: %q vs %q", flags[i], flags[i+1])
		}
	}
}
