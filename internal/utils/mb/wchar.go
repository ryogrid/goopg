// Package mb implements PostgreSQL's multi-byte encoding conversion layer.
// It mirrors postgres/src/backend/utils/mb/ — encoding validation,
// character-set conversion procs, and the dispatch logic that routes
// a (src_encoding, dest_encoding) pair to the correct conversion function.
//
// First slice: LATIN1 ↔ UTF8 (proc OIDs 4374 / 4375).
// Subsequent slices add more encoding pairs following the same pattern.
package mb

import (
	"fmt"
	"strings"
)

// PG encoding IDs (mirrors postgres/src/include/mb/pg_wchar.h).
const (
	PG_SQL_ASCII = 0
	PG_EUC_JP    = 1
	PG_UTF8      = 6
	PG_LATIN1    = 8
	PG_LATIN2    = 14
)

// MAX_CONVERSION_GROWTH is the worst-case byte expansion for any
// supported conversion, used as a buffer-sizing multiplier.
const MAX_CONVERSION_GROWTH = 4

// isHighBitSet reports whether the byte has the 0x80 bit set.
func isHighBitSet(c byte) bool {
	return c&0x80 != 0
}

// pgUTFMblen returns the length in bytes of the UTF8 character whose
// leading byte is s[0]. Mirrors pg_utf_mblen (wchar.c:556).
func pgUTFMblen(s []byte) int {
	c := s[0]
	if (c & 0x80) == 0 {
		return 1
	} else if (c & 0xe0) == 0xc0 {
		return 2
	} else if (c & 0xf0) == 0xe0 {
		return 3
	} else if (c & 0xf8) == 0xf0 {
		return 4
	}
	return 1
}

// pgUTF8IsLegal validates a UTF8 multi-byte sequence. The length is
// assumed to have been obtained from pgUTFMblen, and the caller must
// have checked that many bytes are present. Mirrors pg_utf8_islegal
// (wchar.c:2011).
func pgUTF8IsLegal(source []byte, length int) bool {
	if length <= 0 || length > 4 {
		return false
	}

	var a byte

	switch length {
	case 4:
		a = source[3]
		if a < 0x80 || a > 0xBF {
			return false
		}
		fallthrough
	case 3:
		a = source[2]
		if a < 0x80 || a > 0xBF {
			return false
		}
		fallthrough
	case 2:
		a = source[1]
		switch source[0] {
		case 0xE0:
			if a < 0xA0 || a > 0xBF {
				return false
			}
		case 0xED:
			if a < 0x80 || a > 0x9F {
				return false
			}
		case 0xF0:
			if a < 0x90 || a > 0xBF {
				return false
			}
		case 0xF4:
			if a < 0x80 || a > 0x8F {
				return false
			}
		default:
			if a < 0x80 || a > 0xBF {
				return false
			}
		}
		fallthrough
	case 1:
		a = source[0]
		if a >= 0x80 && a < 0xC2 {
			return false
		}
		if a > 0xF4 {
			return false
		}
	}
	return true
}

// ErrInvalidEncoding is returned when a source byte sequence is not
// valid in the claimed encoding. SQLSTATE 22021.
//
// Bytes, when non-empty, carries the offending byte sequence so Error()
// can render PG's "0xNN 0xNN ..." trailer (report_invalid_encoding_int,
// postgres/src/backend/utils/mb/mbutils.c:1832) — up to 8 bytes, matching
// PG's fixed-size buffer. Byte/Len are the older single-byte form kept
// for the existing LATIN1/LATIN2 call sites; a caller that has the full
// failing sequence should prefer Bytes.
type ErrInvalidEncoding struct {
	Encoding string
	Byte     byte
	Len      int
	Bytes    []byte
}

func (e *ErrInvalidEncoding) Error() string {
	seq := e.Bytes
	if len(seq) == 0 {
		seq = []byte{e.Byte}
	}
	if len(seq) > 8 {
		seq = seq[:8]
	}
	var b strings.Builder
	b.WriteString("invalid byte sequence for encoding \"")
	b.WriteString(e.Encoding)
	b.WriteString("\": ")
	for i, c := range seq {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "0x%02x", c)
	}
	return b.String()
}

// ErrUntranslatableChar is returned when a character cannot be
// represented in the target encoding. SQLSTATE 22028.
type ErrUntranslatableChar struct {
	SrcEncoding string
	DestEncoding string
}

func (e *ErrUntranslatableChar) Error() string {
	return "character cannot be converted from " + e.SrcEncoding + " to " + e.DestEncoding
}
