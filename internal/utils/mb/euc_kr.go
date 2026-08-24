package mb

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/korean"
)

// eucKRVerifyChar checks whether s begins with a structurally valid
// EUC_KR character and returns its length, or -1 if invalid. Port of
// pg_euckr_verifychar (postgres/src/common/wchar.c:1188-1213). Unlike
// EUC_JP, EUC_KR has no SS2/SS3 shift bytes: a high-bit lead byte is
// always a 2-byte KS X 1001 character, both bytes constrained to the
// EUC range (isEucRangeValid, already shared with euc_jp.go).
func eucKRVerifyChar(s []byte) int {
	if len(s) == 0 {
		return -1
	}
	c1 := s[0]
	if !isHighBitSet(c1) {
		return 1 // ASCII
	}
	if len(s) < 2 {
		return -1
	}
	if !isEucRangeValid(c1) || !isEucRangeValid(s[1]) {
		return -1
	}
	return 2
}

// euc_kr_to_utf8 converts an EUC_KR byte slice to UTF8.
//
// Structural validation follows pg_euckr_verifychar exactly (same
// SQLSTATE/byte-count as PG for a malformed lead/trail byte). Unlike
// euc_jp_to_utf8 (M0134-0107, deferred for lack of a JIS X 0208/0212
// table), each structurally-valid 2-byte character is translated to its
// real Unicode codepoint via golang.org/x/text/encoding/korean.EUCKR —
// already an indirect module dependency (go.mod) — rather than hand
// porting the ~2500-entry euc_kr_to_utf8.map radix table
// (postgres/src/backend/utils/mb/Unicode/euc_kr_to_utf8.map). x/text's
// EUCKR table is the CP949 superset of KS X 1001; every byte pair PG
// accepts as EUC_KR is also a valid CP949 pair mapping to the identical
// codepoint, so this is a faithful (not approximate) translation for
// every input pg_euckr_verifychar accepts. M0134-0121.
func euc_kr_to_utf8(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src)*3)
	dec := korean.EUCKR.NewDecoder()
	for i := 0; i < len(src); {
		c1 := src[i]
		if c1 == 0 {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrInvalidEncoding{Encoding: "EUC_KR", Bytes: []byte{c1}}
		}
		l := eucKRVerifyChar(src[i:])
		if l < 0 {
			if noError {
				return i, dest, nil
			}
			end := min(i+2, len(src))
			return i, dest, &ErrInvalidEncoding{Encoding: "EUC_KR", Bytes: append([]byte(nil), src[i:end]...)}
		}
		if l == 1 {
			dest = append(dest, c1)
			i++
			continue
		}
		out, err := dec.Bytes(src[i : i+2])
		// x/text's decoder never returns an error for an in-range but
		// unmapped byte pair — it substitutes U+FFFD instead. Treat that
		// substitution as PG's untranslatable-character case (22P05).
		if err != nil || string(out) == string(utf8.RuneError) {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrUntranslatableChar{SrcEncoding: "EUC_KR", DestEncoding: "UTF8"}
		}
		dest = append(dest, out...)
		i += 2
	}
	return len(src), dest, nil
}

// utf8_to_euc_kr converts a UTF8 byte slice to EUC_KR. Same table source
// as euc_kr_to_utf8 (golang.org/x/text/encoding/korean.EUCKR), reversed.
func utf8_to_euc_kr(src []byte, noError bool) (int, []byte, error) {
	dest := make([]byte, 0, len(src))
	enc := korean.EUCKR.NewEncoder()
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
		out, err := enc.Bytes(src[i : i+l])
		if err != nil {
			if noError {
				return i, dest, nil
			}
			return i, dest, &ErrUntranslatableChar{SrcEncoding: "UTF8", DestEncoding: "EUC_KR"}
		}
		dest = append(dest, out...)
		i += l
	}
	return len(src), dest, nil
}

func init() {
	// Proc OIDs 4364/4365 match pg_proc_seed_data.go's euc_kr_to_utf8/
	// utf8_to_euc_kr rows (real PG regproc OIDs).
	BuiltinConversions[4364] = euc_kr_to_utf8
	BuiltinConversions[4365] = utf8_to_euc_kr
	builtinPair[[2]int32{PG_EUC_KR, PG_UTF8}] = 4364
	builtinPair[[2]int32{PG_UTF8, PG_EUC_KR}] = 4365
}
