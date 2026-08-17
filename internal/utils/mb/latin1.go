package mb

// iso8859_1_to_utf8 converts a LATIN1 (ISO 8859-1) byte slice to UTF8.
// Port of iso8859_1_to_utf8 in
// postgres/src/backend/utils/mb/conversion_procs/utf8_and_iso8859_1/utf8_and_iso8859_1.c:41.
//
// ASCII bytes (0x00–0x7F) pass through unchanged.
// High-bit-set bytes (0x80–0xFF) expand to the 2-byte UTF8 encoding
// [0xC0|(c>>6), 0x80|(c&0x3F)].
// Embedded NUL (0x00) stops conversion when noError is true.
//
// Returns the number of source bytes consumed and the converted output.
func iso8859_1_to_utf8(src []byte, noError bool) (int, []byte, error) {
	// Pre-allocate: ASCII stays 1 byte, high-bit chars become 2 bytes.
	// Worst case: every byte is high-bit → 2× expansion.
	dest := make([]byte, 0, len(src)*2)

	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{
				Encoding: "LATIN1",
				Byte:     c,
				Len:      len(src) - i,
			}
		}
		if !isHighBitSet(c) {
			dest = append(dest, c)
		} else {
			dest = append(dest, (c>>6)|0xc0)
			dest = append(dest, (c&0x3f)|0x80)
		}
	}
	return len(src), dest, nil
}

// utf8_to_iso8859_1 converts a UTF8 byte slice to LATIN1 (ISO 8859-1).
// Port of utf8_to_iso8859_1 in
// postgres/src/backend/utils/mb/conversion_procs/utf8_and_iso8859_1/utf8_and_iso8859_1.c:77.
//
// ASCII bytes pass through unchanged.
// 2-byte UTF8 sequences decode to a codepoint in 0x80–0xFF.
// Sequences longer than 2 bytes are untranslatable (outside LATIN1 range).
// Invalid UTF8 or embedded NUL stops conversion.
//
// Returns the number of source bytes consumed and the converted output.
func utf8_to_iso8859_1(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src))

	for i := 0; i < len(src); {
		c := src[i]
		if c == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{
				Encoding: "UTF8",
				Byte:     c,
				Len:      len(src) - i,
			}
		}
		// Fast path for ASCII-subset characters.
		if !isHighBitSet(c) {
			dest = append(dest, c)
			i++
		} else {
			l := pgUTFMblen(src[i:])
			if l > len(src)-i || !pgUTF8IsLegal(src[i:], l) {
				if noError {
					return i, dest, nil
				}
				return i, dest, &ErrInvalidEncoding{
					Encoding: "UTF8",
					Byte:     c,
					Len:      len(src) - i,
				}
			}
			if l != 2 {
				if noError {
					return i, dest, nil
				}
				return i, dest, &ErrUntranslatableChar{
					SrcEncoding:  "UTF8",
					DestEncoding: "LATIN1",
				}
			}
			c1 := src[i+1] & 0x3f
			cp := (int(c&0x1f) << 6) | int(c1)
			if cp >= 0x80 && cp <= 0xff {
				dest = append(dest, byte(cp))
				i += 2
			} else {
				if noError {
					return i, dest, nil
				}
				return i, dest, &ErrUntranslatableChar{
					SrcEncoding:  "UTF8",
					DestEncoding: "LATIN1",
				}
			}
		}
	}
	return len(src), dest, nil
}
