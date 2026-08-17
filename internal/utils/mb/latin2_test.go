package mb

import "testing"

// TestIso88592ToUTF8RoundTrip verifies that every LATIN2 byte (0x00–0xFF)
// round-trips through iso8859_2_to_utf8 → utf8_to_iso8859_2.
func TestIso88592ToUTF8RoundTrip(t *testing.T) {
	for b := 0; b < 256; b++ {
		src := []byte{byte(b)}
		consumed, utf8Bytes, err := iso8859_2_to_utf8(src, false)
		if b == 0 {
			// Embedded NUL is an error in strict mode.
			if err == nil {
				t.Errorf("NUL byte: expected error, got nil")
			}
			continue
		}
		if err != nil {
			t.Errorf("0x%02x: iso8859_2_to_utf8 error: %v", b, err)
			continue
		}
		if consumed != 1 {
			t.Errorf("0x%02x: consumed=%d, want 1", b, consumed)
		}

		// Round-trip back.
		_, latin2Bytes, err := utf8_to_iso8859_2(utf8Bytes, false)
		if err != nil {
			t.Errorf("0x%02x: utf8_to_iso8859_2 error: %v", b, err)
			continue
		}
		if len(latin2Bytes) != 1 || latin2Bytes[0] != byte(b) {
			t.Errorf("0x%02x: round-trip got 0x%02x", b, latin2Bytes[0])
		}
	}
}

// TestIso88592ToUTF8Expansion verifies the expected byte expansion for
// representative LATIN2 code points.
func TestIso88592ToUTF8Expansion(t *testing.T) {
	tests := []struct {
		name      string
		input     byte
		wantASCII bool
		wantBytes []byte
	}{
		{"space", 0x20, true, []byte{0x20}},
		{"A", 'A', true, []byte{'A'}},
		{"0x80", 0x80, false, []byte{0xC2, 0x80}},
		{"0xA1 (Aogonek)", 0xA1, false, []byte{0xC4, 0x84}}, // Ą
		{"0xA2 (breve)", 0xA2, false, []byte{0xCB, 0x98}},   // ˘
		{"0xA3 (Lstroke)", 0xA3, false, []byte{0xC5, 0x81}}, // Ł
		{"0xB1 (aogonek)", 0xB1, false, []byte{0xC4, 0x85}}, // ą
		{"0xFF (dot above)", 0xFF, false, []byte{0xCB, 0x99}}, // ˙
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, utf8Bytes, err := iso8859_2_to_utf8([]byte{tt.input}, false)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantASCII {
				if len(utf8Bytes) != 1 || utf8Bytes[0] != tt.input {
					t.Errorf("ASCII: got %v, want [%d]", utf8Bytes, tt.input)
				}
			} else {
				if len(utf8Bytes) != 2 || utf8Bytes[0] != tt.wantBytes[0] || utf8Bytes[1] != tt.wantBytes[1] {
					t.Errorf("got %v, want %v", utf8Bytes, tt.wantBytes)
				}
			}
		})
	}
}

// TestUtf8ToIso88592Untranslatable verifies that 3-byte UTF8 (outside LATIN2)
// is rejected as untranslatable.
func TestUtf8ToIso88592Untranslatable(t *testing.T) {
	// U+0400 (Cyrillic) = 0xD0 0x80 — 2 bytes, codepoint 0x400, outside LATIN2.
	src := []byte{0xD0, 0x80}
	_, _, err := utf8_to_iso8859_2(src, false)
	if err == nil {
		t.Error("expected untranslatable error for Cyrillic U+0400 → LATIN2")
	}
	if _, ok := err.(*ErrUntranslatableChar); !ok {
		t.Errorf("expected ErrUntranslatableChar, got %T: %v", err, err)
	}

	// Euro sign U+20AC = 0xE2 0x82 0xAC — 3 bytes, outside LATIN2.
	src = []byte{0xE2, 0x82, 0xAC}
	_, _, err = utf8_to_iso8859_2(src, false)
	if err == nil {
		t.Error("expected untranslatable error for Euro sign → LATIN2")
	}
	if _, ok := err.(*ErrUntranslatableChar); !ok {
		t.Errorf("expected ErrUntranslatableChar, got %T: %v", err, err)
	}
}

// TestNulByteNoErrorLatin2 verifies that noError=true stops at embedded NUL.
func TestNulByteNoErrorLatin2(t *testing.T) {
	src := []byte{'A', 'B', 0, 'C', 'D'}

	// LATIN2 → UTF8
	consumed, utf8Bytes, err := iso8859_2_to_utf8(src, true)
	if err != nil {
		t.Fatalf("unexpected error with noError=true: %v", err)
	}
	if consumed != 2 {
		t.Errorf("consumed=%d, want 2 (stopped at NUL)", consumed)
	}
	if string(utf8Bytes) != "AB" {
		t.Errorf("got %q, want %q", utf8Bytes, "AB")
	}
}

// TestDoEncodingConversionLATIN2ToUTF8 verifies actual conversion through dispatch.
func TestDoEncodingConversionLATIN2ToUTF8(t *testing.T) {
	// LATIN2 0xA1 (Ą) → UTF8 0xC4 0x84
	src := []byte{0xA1}
	result, err := DoEncodingConversion(src, PG_LATIN2, PG_UTF8, BuiltinLookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 || result[0] != 0xC4 || result[1] != 0x84 {
		t.Errorf("LATIN2 0xA1 → UTF8: got %v, want [0xC4, 0x84]", result)
	}

	// Round-trip back.
	result, err = DoEncodingConversion(result, PG_UTF8, PG_LATIN2, BuiltinLookup)
	if err != nil {
		t.Fatalf("round-trip error: %v", err)
	}
	if len(result) != 1 || result[0] != 0xA1 {
		t.Errorf("UTF8 → LATIN2: got %v, want [0xA1]", result)
	}
}

// TestDoEncodingConversionLATIN2NotFound verifies error for unsupported pair
// when using LATIN2 with a non-UTF8 encoding.
func TestDoEncodingConversionLATIN2NotFound(t *testing.T) {
	// LATIN2 → LATIN1 is not a built-in pair.
	_, err := DoEncodingConversion([]byte("test"), PG_LATIN2, PG_LATIN1, BuiltinLookup)
	if err == nil {
		t.Error("expected ErrConversionNotFound for LATIN2→LATIN1")
	}
}

// TestLatin2ForwardTableIntegrity verifies the forward table has exactly
// 128 distinct entries (bijection between LATIN2 bytes and UTF8 sequences).
func TestLatin2ForwardTableIntegrity(t *testing.T) {
	seen := make(map[uint16]bool, 128)
	for i, v := range iso8859_2_to_utf8_table {
		if seen[v] {
			t.Errorf("duplicate UTF8 entry 0x%04X at index %d (byte 0x%02X)", v, i, i+0x80)
		}
		seen[v] = true
	}
}

// TestLatin2ReverseMapMatchesForward verifies that the reverse map
// is a true inverse of the forward table.
func TestLatin2ReverseMapMatchesForward(t *testing.T) {
	if len(iso8859_2_from_utf8_map) != 128 {
		t.Errorf("reverse map has %d entries, want 128", len(iso8859_2_from_utf8_map))
	}
	for i, v := range iso8859_2_to_utf8_table {
		lat2Byte := byte(i + 0x80)
		if b, ok := iso8859_2_from_utf8_map[v]; !ok {
			t.Errorf("UTF8 0x%04X (from byte 0x%02X) not in reverse map", v, lat2Byte)
		} else if b != lat2Byte {
			t.Errorf("reverse map[0x%04X] = 0x%02X, want 0x%02X", v, b, lat2Byte)
		}
	}
}
