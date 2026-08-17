package nbtree

import (
	"bytes"
	"testing"
)

// TestEncodeVarcharTerminator verifies that the encoding always ends
// with a 0x00 terminator and that an empty string encodes to exactly
// one byte.
func TestEncodeVarcharTerminator(t *testing.T) {
	enc := EncodeVarchar([]byte{})
	if len(enc) != 1 || enc[0] != 0x00 {
		t.Fatalf("empty string: want [0x00], got %v", enc)
	}
	enc = EncodeVarchar([]byte("A"))
	if enc[len(enc)-1] != 0x00 {
		t.Fatalf("last byte must be terminator 0x00, got 0x%02x", enc[len(enc)-1])
	}
}

// TestEncodeVarcharMonotone verifies that the bytewise order of
// encoded strings matches the expected C-locale string order for a
// representative set of ASCII inputs.
func TestEncodeVarcharMonotone(t *testing.T) {
	ordered := []string{
		"",
		"A",
		"FURNITURE",
		"HOUSEHOLD",
		"MACHINERY",
		"a",
		"abc",
		"abcd",
		"b",
	}
	for i := 0; i < len(ordered)-1; i++ {
		a := EncodeVarchar([]byte(ordered[i]))
		b := EncodeVarchar([]byte(ordered[i+1]))
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("ordering violated: encode(%q) >= encode(%q)\n  %v\n  %v",
				ordered[i], ordered[i+1], a, b)
		}
	}
}

// TestEncodeVarcharEscaping verifies the escape rules for embedded
// 0x00 and 0x01 bytes.
func TestEncodeVarcharEscaping(t *testing.T) {
	// "\x00" → [0x01, 0x01, 0x00] (NUL escaped via 0x01 introducer).
	enc := EncodeVarchar([]byte{0x00})
	if !bytes.Equal(enc, []byte{0x01, 0x01, 0x00}) {
		t.Fatalf("NUL encoding: want [0x01, 0x01, 0x00], got %v", enc)
	}

	// "\x01" → [0x01, 0x02, 0x00] (SOH escaped via 0x01 introducer).
	enc = EncodeVarchar([]byte{0x01})
	if !bytes.Equal(enc, []byte{0x01, 0x02, 0x00}) {
		t.Fatalf("SOH encoding: want [0x01, 0x02, 0x00], got %v", enc)
	}

	// Order: "" < "\x00" < "\x01" < "\x02" < "A".
	encEmpty := EncodeVarchar([]byte{})
	encNul := EncodeVarchar([]byte{0x00})
	encSoh := EncodeVarchar([]byte{0x01})
	encStx := EncodeVarchar([]byte{0x02})
	encA := EncodeVarchar([]byte("A"))
	ordered := [][]byte{encEmpty, encNul, encSoh, encStx, encA}
	for i := 0; i < len(ordered)-1; i++ {
		if bytes.Compare(ordered[i], ordered[i+1]) >= 0 {
			t.Fatalf("escape ordering violated at index %d: %v >= %v", i, ordered[i], ordered[i+1])
		}
	}

	// "A" < "A\x00B" (embedded NUL extends the key).
	encANulB := EncodeVarchar([]byte{'A', 0x00, 'B'})
	if bytes.Compare(encA, encANulB) >= 0 {
		t.Fatalf("want encode('A') < encode('A\\x00B'), got %v >= %v", encA, encANulB)
	}
}

// TestEncodeVarcharCompositeKey verifies that two concatenated varchar
// keys compare as expected for composite-index key ordering.
// This is the fundamental property that makes EncodeVarchar safe for
// use inside encodeCompositeBTreeKey.
func TestEncodeVarcharCompositeKey(t *testing.T) {
	// Composite ("A", "X") vs ("A", "Y") — second column determines order.
	k1 := append(EncodeVarchar([]byte("A")), EncodeVarchar([]byte("X"))...)
	k2 := append(EncodeVarchar([]byte("A")), EncodeVarchar([]byte("Y"))...)
	if bytes.Compare(k1, k2) >= 0 {
		t.Fatalf("composite: ('A','X') should < ('A','Y')")
	}

	// Composite ("A", "X") vs ("B", "A") — first column determines order.
	k3 := append(EncodeVarchar([]byte("B")), EncodeVarchar([]byte("A"))...)
	if bytes.Compare(k1, k3) >= 0 {
		t.Fatalf("composite: ('A','X') should < ('B','A')")
	}

	// Composite ("A", "") vs ("A", "Z") — empty second col sorts first.
	k4 := append(EncodeVarchar([]byte("A")), EncodeVarchar([]byte{})...)
	k5 := append(EncodeVarchar([]byte("A")), EncodeVarchar([]byte("Z"))...)
	if bytes.Compare(k4, k5) >= 0 {
		t.Fatalf("composite: ('A','') should < ('A','Z')")
	}
}

// TestEncodeVarcharTpchShapes spot-checks representative TPC-H
// varchar values from the part and customer tables.
func TestEncodeVarcharTpchShapes(t *testing.T) {
	// p_type values from PART table (varchar 25).
	ptypes := []string{
		"ECONOMY ANODIZED BRASS",
		"ECONOMY ANODIZED COPPER",
		"ECONOMY ANODIZED NICKEL",
		"PROMO BRUSHED STEEL",
		"STANDARD ANODIZED TIN",
	}
	for i := 0; i < len(ptypes)-1; i++ {
		a := EncodeVarchar([]byte(ptypes[i]))
		b := EncodeVarchar([]byte(ptypes[i+1]))
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("TPC-H p_type ordering violated: %q vs %q", ptypes[i], ptypes[i+1])
		}
	}

	// c_mktsegment values from CUSTOMER table (char 10 padded, but
	// stored as varchar in goopg).
	segs := []string{
		"AUTOMOBILE",
		"BUILDING",
		"FURNITURE",
		"HOUSEHOLD",
		"MACHINERY",
	}
	for i := 0; i < len(segs)-1; i++ {
		a := EncodeVarchar([]byte(segs[i]))
		b := EncodeVarchar([]byte(segs[i+1]))
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("TPC-H c_mktsegment ordering violated: %q vs %q", segs[i], segs[i+1])
		}
	}
}
