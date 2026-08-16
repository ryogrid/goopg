package mb

// iso8859_2_to_utf8_table maps LATIN2 (ISO 8859-2) bytes 0x80–0xFF to their
// UTF8 encoding. Index 0 = byte 0x80, index 127 = byte 0xFF. Each entry is a
// big-endian uint16 of the 2-byte UTF8 sequence. Generated from
// postgres/src/backend/utils/mb/Unicode/iso8859_2_to_utf8.map.
var iso8859_2_to_utf8_table = [128]uint16{
	0xc280, 0xc281, 0xc282, 0xc283, 0xc284, 0xc285, 0xc286, 0xc287,
	0xc288, 0xc289, 0xc28a, 0xc28b, 0xc28c, 0xc28d, 0xc28e, 0xc28f,
	0xc290, 0xc291, 0xc292, 0xc293, 0xc294, 0xc295, 0xc296, 0xc297,
	0xc298, 0xc299, 0xc29a, 0xc29b, 0xc29c, 0xc29d, 0xc29e, 0xc29f,
	0xc2a0, 0xc484, 0xcb98, 0xc581, 0xc2a4, 0xc4bd, 0xc59a, 0xc2a7,
	0xc2a8, 0xc5a0, 0xc59e, 0xc5a4, 0xc5b9, 0xc2ad, 0xc5bd, 0xc5bb,
	0xc2b0, 0xc485, 0xcb9b, 0xc582, 0xc2b4, 0xc4be, 0xc59b, 0xcb87,
	0xc2b8, 0xc5a1, 0xc59f, 0xc5a5, 0xc5ba, 0xcb9d, 0xc5be, 0xc5bc,
	0xc594, 0xc381, 0xc382, 0xc482, 0xc384, 0xc4b9, 0xc486, 0xc387,
	0xc48c, 0xc389, 0xc498, 0xc38b, 0xc49a, 0xc38d, 0xc38e, 0xc48e,
	0xc490, 0xc583, 0xc587, 0xc393, 0xc394, 0xc590, 0xc396, 0xc397,
	0xc598, 0xc5ae, 0xc39a, 0xc5b0, 0xc39c, 0xc39d, 0xc5a2, 0xc39f,
	0xc595, 0xc3a1, 0xc3a2, 0xc483, 0xc3a4, 0xc4ba, 0xc487, 0xc3a7,
	0xc48d, 0xc3a9, 0xc499, 0xc3ab, 0xc49b, 0xc3ad, 0xc3ae, 0xc48f,
	0xc491, 0xc584, 0xc588, 0xc3b3, 0xc3b4, 0xc591, 0xc3b6, 0xc3b7,
	0xc599, 0xc5af, 0xc3ba, 0xc5b1, 0xc3bc, 0xc3bd, 0xc5a3, 0xcb99,
}

// iso8859_2_from_utf8_map is the reverse mapping: big-endian uint16 of a
// 2-byte UTF8 sequence → LATIN2 byte. Built in init() from the forward table.
var iso8859_2_from_utf8_map map[uint16]byte

func init() {
	iso8859_2_from_utf8_map = make(map[uint16]byte, 128)
	for i, v := range iso8859_2_to_utf8_table {
		iso8859_2_from_utf8_map[v] = byte(i + 0x80)
	}
}

// iso8859_2_to_utf8 converts a LATIN2 (ISO 8859-2) byte slice to UTF8.
// Port of iso8859_to_utf8 (the PG dispatcher for all ISO 8859 variants) in
// postgres/src/backend/utils/mb/conversion_procs/utf8_and_iso8859/utf8_and_iso8859.c:102.
//
// ASCII bytes (0x00–0x7F) pass through unchanged.
// High-bit-set bytes (0x80–0xFF) are looked up in the mapping table.
// Embedded NUL (0x00) stops conversion when noError is true.
//
// Returns the number of source bytes consumed and the converted output.
func iso8859_2_to_utf8(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src)*2)

	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{
				Encoding: "LATIN2",
				Byte:     c,
				Len:      len(src) - i,
			}
		}
		if !isHighBitSet(c) {
			dest = append(dest, c)
		} else {
			v := iso8859_2_to_utf8_table[c-0x80]
			dest = append(dest, byte(v>>8), byte(v&0xFF))
		}
	}
	return len(src), dest, nil
}

// utf8_to_iso8859_2 converts a UTF8 byte slice to LATIN2 (ISO 8859-2).
// Port of utf8_to_iso8859 (the PG dispatcher for all ISO 8859 variants) in
// postgres/src/backend/utils/mb/conversion_procs/utf8_and_iso8859/utf8_and_iso8859.c:138.
//
// ASCII bytes pass through unchanged.
// 2-byte UTF8 sequences are decoded and looked up in the reverse map.
// Sequences longer than 2 bytes are untranslatable (outside LATIN2 range).
// Invalid UTF8 or embedded NUL stops conversion.
//
// Returns the number of source bytes consumed and the converted output.
func utf8_to_iso8859_2(src []byte, noError bool) (int, []byte, error) {
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
					DestEncoding: "LATIN2",
				}
			}
			key := uint16(src[i])<<8 | uint16(src[i+1])
			if b, ok := iso8859_2_from_utf8_map[key]; ok {
				dest = append(dest, b)
				i += 2
			} else {
				if noError {
					return i, dest, nil
				}
				return i, dest, &ErrUntranslatableChar{
					SrcEncoding:  "UTF8",
					DestEncoding: "LATIN2",
				}
			}
		}
	}
	return len(src), dest, nil
}
