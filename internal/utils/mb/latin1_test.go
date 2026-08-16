package mb

import "testing"

// TestIso88591ToUTF8RoundTrip verifies that every LATIN1 byte (0x00–0xFF)
// round-trips through iso8859_1_to_utf8 → utf8_to_iso8859_1.
func TestIso88591ToUTF8RoundTrip(t *testing.T) {
	for b := 0; b < 256; b++ {
		src := []byte{byte(b)}
		consumed, utf8Bytes, err := iso8859_1_to_utf8(src, false)
		if b == 0 {
			// Embedded NUL is an error in strict mode.
			if err == nil {
				t.Errorf("NUL byte: expected error, got nil")
			}
			continue
		}
		if err != nil {
			t.Errorf("0x%02x: iso8859_1_to_utf8 error: %v", b, err)
			continue
		}
		if consumed != 1 {
			t.Errorf("0x%02x: consumed=%d, want 1", b, consumed)
		}

		// Round-trip back.
		_, latin1Bytes, err := utf8_to_iso8859_1(utf8Bytes, false)
		if err != nil {
			t.Errorf("0x%02x: utf8_to_iso8859_1 error: %v", b, err)
			continue
		}
		if len(latin1Bytes) != 1 || latin1Bytes[0] != byte(b) {
			t.Errorf("0x%02x: round-trip got 0x%02x", b, latin1Bytes[0])
		}
	}
}

// TestIso88591ToUTF8Expansion verifies the expected byte expansion.
func TestIso88591ToUTF8Expansion(t *testing.T) {
	// ASCII bytes stay single-byte.
	tests := []struct {
		name      string
		input     byte
		wantASCII bool
		wantBytes []byte
	}{
		{"space", 0x20, true, []byte{0x20}},
		{"A", 'A', true, []byte{'A'}},
		{"tilde", 0x7E, true, []byte{0x7E}},
		{"high del", 0x7F, true, []byte{0x7F}}, // DEL is not high-bit-set
		{"0x80", 0x80, false, []byte{0xC2, 0x80}},
		{"0xA0 (nbsp)", 0xA0, false, []byte{0xC2, 0xA0}},
		{"0xFF (y diaeresis)", 0xFF, false, []byte{0xC3, 0xBF}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, utf8Bytes, err := iso8859_1_to_utf8([]byte{tt.input}, false)
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

// TestNulByteNoError verifies that noError=true stops at embedded NUL.
func TestNulByteNoError(t *testing.T) {
	src := []byte{'A', 'B', 0, 'C', 'D'}

	// LATIN1 → UTF8
	consumed, utf8Bytes, err := iso8859_1_to_utf8(src, true)
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

// TestUtf8ToIso88591InvalidRejection verifies invalid UTF8 is rejected.
func TestUtf8ToIso88591InvalidRejection(t *testing.T) {
	// Invalid UTF8 byte 0xFE.
	src := []byte{0xFE}
	_, _, err := utf8_to_iso8859_1(src, false)
	if err == nil {
		t.Error("expected error for invalid UTF8 byte 0xFE")
	}

	// Overlong encoding of '/'.
	src = []byte{0xC0, 0xAF}
	_, _, err = utf8_to_iso8859_1(src, false)
	if err == nil {
		t.Error("expected error for overlong encoding 0xC0 0xAF")
	}
}

// TestUtf8ToIso88591Untranslatable verifies 3-byte UTF8 is rejected
// for LATIN1 conversion (outside 0x80–0xFF codepoint range).
func TestUtf8ToIso88591Untranslatable(t *testing.T) {
	// U+0400 (Cyrillic) = 0xD0 0x80 — 2 bytes, codepoint 0x400, outside LATIN1.
	src := []byte{0xD0, 0x80}
	_, _, err := utf8_to_iso8859_1(src, false)
	if err == nil {
		t.Error("expected untranslatable error for Cyrillic U+0400 → LATIN1")
	}
	if _, ok := err.(*ErrUntranslatableChar); !ok {
		t.Errorf("expected ErrUntranslatableChar, got %T: %v", err, err)
	}
}

// TestDoEncodingConversionFastPaths verifies the fast-path returns.
func TestDoEncodingConversionFastPaths(t *testing.T) {
	src := []byte("hello")

	// Empty input.
	result, err := DoEncodingConversion(nil, PG_UTF8, PG_LATIN1, nil)
	if err != nil {
		t.Errorf("empty: unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("empty: expected empty result, got %v", result)
	}

	// Same encoding.
	result, err = DoEncodingConversion(src, PG_UTF8, PG_UTF8, nil)
	if err != nil {
		t.Errorf("same enc: unexpected error: %v", err)
	}
	if string(result) != "hello" {
		t.Errorf("same enc: got %q, want %q", result, "hello")
	}

	// Dest SQL_ASCII.
	result, err = DoEncodingConversion(src, PG_UTF8, PG_SQL_ASCII, nil)
	if err != nil {
		t.Errorf("dest SQL_ASCII: unexpected error: %v", err)
	}
	if string(result) != "hello" {
		t.Errorf("dest SQL_ASCII: got %q, want %q", result, "hello")
	}
}

// TestDoEncodingConversionLATIN1ToUTF8 verifies actual conversion.
func TestDoEncodingConversionLATIN1ToUTF8(t *testing.T) {
	// LATIN1 'é' (0xE9) → UTF8 0xC3 0xA9
	src := []byte{0xE9}
	result, err := DoEncodingConversion(src, PG_LATIN1, PG_UTF8, BuiltinLookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 || result[0] != 0xC3 || result[1] != 0xA9 {
		t.Errorf("LATIN1 0xE9 → UTF8: got %v, want [0xC3, 0xA9]", result)
	}

	// Round-trip back.
	result, err = DoEncodingConversion(result, PG_UTF8, PG_LATIN1, BuiltinLookup)
	if err != nil {
		t.Fatalf("round-trip error: %v", err)
	}
	if len(result) != 1 || result[0] != 0xE9 {
		t.Errorf("UTF8 → LATIN1: got %v, want [0xE9]", result)
	}
}

// TestDoEncodingConversionNotFound verifies error for unsupported pair.
func TestDoEncodingConversionNotFound(t *testing.T) {
	_, err := DoEncodingConversion([]byte("test"), PG_UTF8, 123, BuiltinLookup)
	if err == nil {
		t.Error("expected ErrConversionNotFound for unsupported encoding pair")
	}
}
