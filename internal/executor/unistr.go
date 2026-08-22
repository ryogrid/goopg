package executor

import (
	"fmt"
	"unicode/utf8"
)

// unistrDecode implements unistr(text) — PG oracle:
// postgres/src/backend/utils/adt/varlena.c:6762-6925 (unistr), M0134-0070.
//
// It scans the input for Unicode escape sequences (\\, \XXXX, \uXXXX,
// \UXXXXXXXX, \+XXXXXX), decodes each to a codepoint, reassembles UTF-16
// surrogate pairs, and appends the UTF-8 encoding of every codepoint (plus
// any literal bytes outside an escape) to the result. goopg is UTF-8
// internally, so this skips PG's separate pg_unicode_to_server "server
// encoding" conversion step — utf8.AppendRune is the whole job.
func unistrDecode(s string, pos int) (string, *ExecError) {
	instr := s
	var out []byte
	var pairFirst int64 // 0 means "no pending high surrogate"

	invalidPair := func() (string, *ExecError) {
		return "", &ExecError{Code: "42601",
			Message: "invalid Unicode surrogate pair"}
	}

	for len(instr) > 0 {
		if instr[0] == '\\' {
			switch {
			case len(instr) >= 2 && instr[1] == '\\':
				if pairFirst != 0 {
					return invalidPair()
				}
				out = append(out, '\\')
				instr = instr[2:]

			case len(instr) >= 5 && isHexDigitsN(instr[1:], 4):
				// \XXXX — bare 4 hex digits.
				cp := int64(hexValN(instr[1:], 4))
				var uerr *ExecError
				pairFirst, uerr = unistrApplyCodepoint(&out, pairFirst, cp, pos)
				if uerr != nil {
					return "", uerr
				}
				instr = instr[5:]

			case len(instr) >= 6 && instr[1] == 'u' && isHexDigitsN(instr[2:], 4):
				// \uXXXX — explicit 4 hex digits.
				cp := int64(hexValN(instr[2:], 4))
				var uerr *ExecError
				pairFirst, uerr = unistrApplyCodepoint(&out, pairFirst, cp, pos)
				if uerr != nil {
					return "", uerr
				}
				instr = instr[6:]

			case len(instr) >= 8 && instr[1] == '+' && isHexDigitsN(instr[2:], 6):
				// \+XXXXXX — 6 hex digits.
				cp := int64(hexValN(instr[2:], 6))
				var uerr *ExecError
				pairFirst, uerr = unistrApplyCodepoint(&out, pairFirst, cp, pos)
				if uerr != nil {
					return "", uerr
				}
				instr = instr[8:]

			case len(instr) >= 10 && instr[1] == 'U' && isHexDigitsN(instr[2:], 8):
				// \UXXXXXXXX — 8 hex digits.
				cp := int64(hexValN(instr[2:], 8))
				var uerr *ExecError
				pairFirst, uerr = unistrApplyCodepoint(&out, pairFirst, cp, pos)
				if uerr != nil {
					return "", uerr
				}
				instr = instr[10:]

			default:
				return "", &ExecError{Code: "42601",
					Message: "invalid Unicode escape",
					Hint:    `Unicode escapes must be \XXXX, \+XXXXXX, \uXXXX, or \UXXXXXXXX.`}
			}
		} else {
			if pairFirst != 0 {
				return invalidPair()
			}
			out = append(out, instr[0])
			instr = instr[1:]
		}
	}

	if pairFirst != 0 {
		return invalidPair()
	}

	return string(out), nil
}

// unistrApplyCodepoint validates a decoded escape codepoint, resolves any
// pending high-surrogate pairing, and appends the resulting UTF-8 bytes (or
// stashes a new pending high surrogate) — mirroring the shared body repeated
// after each of the four escape-form branches in PG's unistr().
func unistrApplyCodepoint(out *[]byte, pairFirst int64, cp int64, pos int) (int64, *ExecError) {
	// is_valid_unicode_codepoint: c > 0 && c <= 0x10FFFF.
	if cp <= 0 || cp > 0x10FFFF {
		return 0, &ExecError{Code: "22023",
			Message: fmt.Sprintf("invalid Unicode code point: %04X", uint32(cp))}
	}

	isHighSurrogate := cp >= 0xD800 && cp <= 0xDBFF
	isLowSurrogate := cp >= 0xDC00 && cp <= 0xDFFF

	if pairFirst != 0 {
		if isLowSurrogate {
			cp = 0x10000 + (pairFirst-0xD800)*0x400 + (cp - 0xDC00)
			pairFirst = 0
		} else {
			return 0, &ExecError{Code: "42601",
				Message: "invalid Unicode surrogate pair"}
		}
	} else if isLowSurrogate {
		return 0, &ExecError{Code: "42601",
			Message: "invalid Unicode surrogate pair"}
	}

	if isHighSurrogate && pairFirst == 0 {
		// (pairFirst == 0 here also covers the just-resolved-pair case, where
		// cp is now a supplementary codepoint, never in the surrogate range.)
		return cp, nil
	}

	*out = utf8.AppendRune(*out, rune(cp))
	return 0, nil
}

// isHexDigitsN reports whether the first n bytes of s are ASCII hex digits
// (PG's isxdigits_n).
func isHexDigitsN(s string, n int) bool {
	if len(s) < n {
		return false
	}
	for i := 0; i < n; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// hexValN parses the first n bytes of s as hex (PG's hexval_n).
func hexValN(s string, n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		c := s[i]
		var d uint32
		switch {
		case c >= '0' && c <= '9':
			d = uint32(c - '0')
		case c >= 'a' && c <= 'f':
			d = uint32(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = uint32(c-'A') + 10
		}
		v = v<<4 | d
	}
	return v
}
