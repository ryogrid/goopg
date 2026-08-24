package mb

// eucJPVerifyChar checks whether src begins with a structurally valid
// EUC_JP character and returns its length, or -1 if invalid. Port of
// pg_eucjp_verifychar (postgres/src/common/wchar.c:1102-1150).
//
// This is a *grammar* check only — it does not consult a Unicode mapping
// table, so it accepts any byte sequence with a legal EUC_JP shape even
// if the codepoint has no JIS X 0208/0212 assignment. That matches PG's
// own layering: pg_encoding_verifymbchar runs before the conversion
// proc's table lookup (LocalToUtf, postgres/src/backend/utils/mb/conv.c:754),
// so the "invalid byte sequence" error is raised independently of
// whether a full Unicode table is available.
const (
	eucSS2 = 0x8e
	eucSS3 = 0x8f
)

func isEucRangeValid(c byte) bool { return c >= 0xa1 && c <= 0xfe }

func eucJPVerifyChar(s []byte) int {
	if len(s) == 0 {
		return -1
	}
	c1 := s[0]
	switch c1 {
	case eucSS2: // JIS X 0201 (half-width kana)
		if len(s) < 2 {
			return -1
		}
		c2 := s[1]
		if c2 < 0xa1 || c2 > 0xdf {
			return -1
		}
		return 2
	case eucSS3: // JIS X 0212
		if len(s) < 3 {
			return -1
		}
		if !isEucRangeValid(s[1]) || !isEucRangeValid(s[2]) {
			return -1
		}
		return 3
	default:
		if isHighBitSet(c1) { // JIS X 0208
			if len(s) < 2 {
				return -1
			}
			if !isEucRangeValid(c1) || !isEucRangeValid(s[1]) {
				return -1
			}
			return 2
		}
		return 1 // ASCII
	}
}

// eucJPMblenGuess is the "expected" length from the lead byte alone,
// used only to size the error-message byte trailer — it mirrors PG's
// pg_eucjp_mblen (a cheap length guess, not full validation) so
// report_invalid_encoding_int prints the same byte count PG does.
func eucJPMblenGuess(c byte) int {
	switch c {
	case eucSS3:
		return 3
	case eucSS2:
		return 2
	default:
		if isHighBitSet(c) {
			return 2
		}
		return 1
	}
}

// euc_jp_to_utf8 converts an EUC_JP byte slice to UTF8.
// Structural validation follows pg_eucjp_verifychar exactly, so an
// invalid byte sequence is reported with the same SQLSTATE/byte-count
// PG produces. ASCII bytes pass straight through.
//
// Non-ASCII characters that pass structural validation are not yet
// translated to their real Unicode codepoint — that needs the ~4000-
// entry euc_jp_to_utf8.map JIS X 0208/0212 table
// (postgres/src/backend/utils/mb/Unicode/euc_jp_to_utf8.map), which is
// out of scope here (see .ralph/deferral_ledger.md, M0134-0107). Rather
// than silently emit non-UTF8 bytes into a UTF8-declared column, a
// structurally-valid non-ASCII character is reported as untranslatable.
func euc_jp_to_utf8(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		c := src[i]
		if c == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{Encoding: "EUC_JP", Bytes: []byte{c}}
		}
		if !isHighBitSet(c) {
			dest = append(dest, c)
			i++
			continue
		}
		l := eucJPVerifyChar(src[i:])
		if l < 0 {
			if noError {
				return i, dest, nil
			}
			end := min(i+eucJPMblenGuess(c), len(src))
			return i, dest, &ErrInvalidEncoding{Encoding: "EUC_JP", Bytes: append([]byte(nil), src[i:end]...)}
		}
		if noError {
			return i, dest, nil
		}
		return i, dest, &ErrUntranslatableChar{SrcEncoding: "EUC_JP", DestEncoding: "UTF8"}
	}
	return len(src), dest, nil
}

// utf8_to_euc_jp converts a UTF8 byte slice to EUC_JP. Same scope note
// as euc_jp_to_utf8: ASCII round-trips; a structurally-valid non-ASCII
// UTF8 character has no table to translate through yet, so it is
// reported untranslatable instead of emitting wrong bytes.
func utf8_to_euc_jp(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		c := src[i]
		if c == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{Encoding: "UTF8", Bytes: []byte{c}}
		}
		if !isHighBitSet(c) {
			dest = append(dest, c)
			i++
			continue
		}
		l := pgUTFMblen(src[i:])
		if l > len(src)-i || !pgUTF8IsLegal(src[i:], l) {
			if noError {
				return i, dest, nil
			}
			end := min(i+l, len(src))
			return i, dest, &ErrInvalidEncoding{Encoding: "UTF8", Bytes: append([]byte(nil), src[i:end]...)}
		}
		if noError {
			return i, dest, nil
		}
		return i, dest, &ErrUntranslatableChar{SrcEncoding: "UTF8", DestEncoding: "EUC_JP"}
	}
	return len(src), dest, nil
}

func init() {
	BuiltinConversions[4362] = euc_jp_to_utf8
	BuiltinConversions[4363] = utf8_to_euc_jp
	builtinPair[[2]int32{PG_EUC_JP, PG_UTF8}] = 4362
	builtinPair[[2]int32{PG_UTF8, PG_EUC_JP}] = 4363
}
