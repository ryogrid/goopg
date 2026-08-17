package executor

import (
	"fmt"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/utils/adt/array"
)

// PG-faithful bytea input/output primitives (M0125-0021).
//
// Before this file goopg had no bytea *value* at all above the storage layer:
// `'\xaabb'::bytea` fell through evalCast's default arm and stayed the
// six-character KindString `\xaabb`, so `length()` counted escape characters
// (6, not 2), `substring()` sliced the escape text, and the bytea column
// encoder stored those six characters verbatim. `encode()` was a stub that
// returned the empty string for every input, which turned a hex dump into a
// SILENT wrong answer rather than an error.
//
// Everything here mirrors upstream byte-for-byte:
//   - byteaIn        → byteain            (postgres/src/backend/utils/adt/varlena.c)
//   - hexDecodePG    → hex_decode_safe    (postgres/src/backend/utils/adt/encode.c)
//   - escDecodePG    → esc_decode         (encode.c)
//   - hexEncodePG    → hex_encode         (encode.c)
//   - escEncodePG    → esc_encode         (encode.c)
//   - byteaOutHex    → byteaout, hex mode (varlena.c)
//
// The two error families are upstream's and they differ, which is why they are
// not collapsed into one helper: the escape-format parser inside byteain raises
// 22P02 `invalid input syntax for type bytea`, while every hex diagnostic comes
// from encode.c and raises 22023. `decode(…, 'hex')` and `'\x…'::bytea` share
// hexDecodePG precisely so the two entry points cannot drift (Hard-won Rule #2).

const hexLowerDigits = "0123456789abcdef"

// hexVal decodes one hex digit, mirroring encode.c's get_hex/hexlookup.
func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// hexDecodePG is hex_decode_safe (encode.c): whitespace between digits is
// skipped, an odd digit count and any non-hex character are hard errors with
// ERRCODE_INVALID_PARAMETER_VALUE (22023). Note that upstream does NOT accept a
// leading `\x` here — `decode('\xaabb','hex')` errors in PG with
// `invalid hexadecimal digit: "\"`, so this helper must not strip it either.
func hexDecodePG(src string, pos int) ([]byte, error) {
	out := make([]byte, 0, len(src)/2)
	for i := 0; i < len(src); {
		c := src[i]
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			i++
			continue
		}
		v1, ok := hexVal(c)
		if !ok {
			return nil, badHexDigit(src[i:], pos)
		}
		i++
		// Upstream checks bounds before consuming the second digit, so a
		// trailing space after an odd digit still reports "odd number of
		// digits" only once the source is exhausted.
		if i >= len(src) {
			return nil, &ExecError{Code: "22023", Pos: pos,
				Message: "invalid hexadecimal data: odd number of digits"}
		}
		v2, ok := hexVal(src[i])
		if !ok {
			return nil, badHexDigit(src[i:], pos)
		}
		i++
		out = append(out, v1<<4|v2)
	}
	return out, nil
}

// badHexDigit builds encode.c's `invalid hexadecimal digit: "%.*s"` error. The
// width is pg_mblen_range — the WHOLE offending multibyte character, not its
// first byte — so a non-ASCII character is reported intact. Note the message
// quotes the character raw (upstream `%.*s`, not `%q`): a stray backslash
// prints as `"\"`, which Go's %q would have escaped to `"\\"`.
func badHexDigit(rest string, pos int) error {
	_, n := utf8.DecodeRuneInString(rest)
	if n == 0 {
		n = 1
	}
	return &ExecError{Code: "22023", Pos: pos,
		Message: fmt.Sprintf(`invalid hexadecimal digit: "%s"`, rest[:n])}
}

// byteaIn is byteain (varlena.c): a `\x` prefix selects hex format, everything
// else is the traditional escape format where only `\\` and `\` + three octal
// digits (first in 0..3) are legal backslash sequences. A lone backslash is
// 22P02, matching upstream's two-pass validate-then-decode structure.
func byteaIn(s string, pos int) ([]byte, error) {
	if len(s) >= 2 && s[0] == '\\' && s[1] == 'x' {
		return hexDecodePG(s[2:], pos)
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			out = append(out, s[i])
			i++
			continue
		}
		if i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '3' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			out = append(out, (s[i+1]-'0')<<6|(s[i+2]-'0')<<3|(s[i+3]-'0'))
			i += 4
			continue
		}
		if i+1 < len(s) && s[i+1] == '\\' {
			out = append(out, '\\')
			i += 2
			continue
		}
		return nil, &ExecError{Code: "22P02", Pos: pos,
			Message: "invalid input syntax for type bytea"}
	}
	return out, nil
}

// escDecodePG is esc_decode (encode.c), the decoder behind
// `decode(text, 'escape')`. It differs from byteaIn's escape pass only in that
// upstream's bounds test is `src + 3 < end` (an octal escape needs a byte to
// follow it inside the buffer); reproduced verbatim so the two stay comparable.
func escDecodePG(src string, pos int) ([]byte, error) {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		if src[i] != '\\' {
			out = append(out, src[i])
			i++
			continue
		}
		if i+3 < len(src) &&
			src[i+1] >= '0' && src[i+1] <= '3' &&
			src[i+2] >= '0' && src[i+2] <= '7' &&
			src[i+3] >= '0' && src[i+3] <= '7' {
			out = append(out, (src[i+1]-'0')<<6|(src[i+2]-'0')<<3|(src[i+3]-'0'))
			i += 4
			continue
		}
		if i+1 < len(src) && src[i+1] == '\\' {
			out = append(out, '\\')
			i += 2
			continue
		}
		return nil, &ExecError{Code: "22P02", Pos: pos,
			Message: "invalid input syntax for type bytea"}
	}
	return out, nil
}

// hexEncodePG is hex_encode (encode.c): lowercase, no `\x` prefix.
func hexEncodePG(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexLowerDigits[c>>4], hexLowerDigits[c&0x0f])
	}
	return string(out)
}

// escEncodePG is esc_encode (encode.c), the `escape` format of encode().
// Only NUL, high-bit-set bytes and the backslash itself are escaped — a
// newline or a tab passes through raw. This is deliberately NOT byteaout's
// escape mode (which also escapes the other non-printables); the two are
// separate upstream functions and `encode(E'\\x0a'::bytea,'escape')` really
// does return a bare newline.
func escEncodePG(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		switch {
		case c == 0 || c&0x80 != 0:
			out = append(out, '\\', '0'+(c>>6), '0'+((c>>3)&7), '0'+(c&7))
		case c == '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// b64EncodePG is pg_base64_encode (encode.c). It is NOT
// base64.StdEncoding.EncodeToString: upstream breaks the output with a newline
// every 76 characters, and because the wrap test runs only after a complete
// four-character group, a payload of exactly 57 bytes (76 output characters)
// ends WITH a trailing newline. The loop below is a transliteration so those
// edges cannot drift.
func b64EncodePG(src []byte) string {
	out := make([]byte, 0, len(src)*4/3+len(src)/57+4)
	lend := 76
	pos := 2
	buf := uint32(0)
	for _, c := range src {
		buf |= uint32(c) << (pos << 3)
		pos--
		if pos < 0 {
			out = append(out,
				b64Alphabet[(buf>>18)&0x3f],
				b64Alphabet[(buf>>12)&0x3f],
				b64Alphabet[(buf>>6)&0x3f],
				b64Alphabet[buf&0x3f])
			pos = 2
			buf = 0
		}
		if len(out) >= lend {
			out = append(out, '\n')
			lend = len(out) + 76
		}
	}
	if pos != 2 {
		last := byte('=')
		if pos == 0 {
			last = b64Alphabet[(buf>>6)&0x3f]
		}
		out = append(out,
			b64Alphabet[(buf>>18)&0x3f],
			b64Alphabet[(buf>>12)&0x3f],
			last, '=')
	}
	return string(out)
}

// b64DecodePG is pg_base64_decode (encode.c). Whitespace (including the
// newlines b64EncodePG emits) is skipped, and both error families are
// upstream's ERRCODE_INVALID_PARAMETER_VALUE (22023) — Go's
// base64.StdEncoding rejects the embedded newlines outright, so a round-trip
// through encode()/decode() needs this decoder.
func b64DecodePG(src string, pos int) ([]byte, error) {
	out := make([]byte, 0, len(src)*3/4+3)
	buf := uint32(0)
	npos, end := 0, 0
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		var b int
		if c == '=' {
			if end == 0 {
				switch npos {
				case 2:
					end = 1
				case 3:
					end = 2
				default:
					return nil, &ExecError{Code: "22023", Pos: pos,
						Message: `unexpected "=" while decoding base64 sequence`}
				}
			}
			b = 0
		} else {
			b = b64Value(c)
			if b < 0 {
				_, n := utf8.DecodeRuneInString(src[i:])
				if n == 0 {
					n = 1
				}
				return nil, &ExecError{Code: "22023", Pos: pos,
					Message: fmt.Sprintf(`invalid symbol "%s" found while decoding base64 sequence`, src[i:i+n])}
			}
		}
		buf = buf<<6 + uint32(b)
		npos++
		if npos == 4 {
			out = append(out, byte(buf>>16))
			if end == 0 || end > 1 {
				out = append(out, byte(buf>>8))
			}
			if end == 0 || end > 2 {
				out = append(out, byte(buf))
			}
			buf = 0
			npos = 0
		}
	}
	if npos != 0 {
		return nil, &ExecError{Code: "22023", Pos: pos,
			Message: "invalid base64 end sequence",
			Hint:    "Input data is missing padding, is truncated, or is otherwise corrupted."}
	}
	return out, nil
}

// b64Value is encode.c's b64lookup table: the alphabet index of c, or -1.
func b64Value(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 26
	case c >= '0' && c <= '9':
		return int(c-'0') + 52
	case c == '+':
		return 62
	case c == '/':
		return 63
	}
	return -1
}

// byteaOperand resolves a datum that participates in a bytea operator to its
// bytes: KindBytes verbatim, KindString through byteain (PG's unknown-literal
// coercion). Reports false when the value is neither, or when the string is not
// valid bytea input — callers then keep their pre-M0125-0021 behaviour instead
// of turning a working query into an error.
func byteaOperand(d Datum) ([]byte, bool) {
	switch d.Kind {
	case KindBytes:
		return d.BytesValue(), true
	case KindString:
		b, err := byteaIn(d.StringValue(), 0)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	return nil, false
}

// byteaOutMode renders b per the `bytea_output` mode string ("hex", "escape",
// or absent/unrecognised → hex). This is the ONLY correct entry point for a
// bytea text-output site to call — every one of them (scalar cast-to-text,
// the wire renderer, COPY TO, string_agg's finish step, and the array-element
// renderer via array.ByteaOutStyled) resolves through this single dispatch,
// so hex stays the default everywhere and an `escape` GUC changes all of them
// together (Hard-won Rule #2). Round 2 of M0134-0001 S12 deleted the
// standalone byteaOutHex/byteaOutEscape wrappers that used to sit here:
// after the sibling sweep landed, byteaOutHex had zero non-test callers left
// and a future call site could have grabbed it directly, silently
// reintroducing the GUC-blind path this slice exists to close. Call
// byteaOutMode(b, "hex") / byteaOutMode(b, "escape") directly if a fixed
// mode is genuinely needed (e.g. a test oracle); every real render site
// should be resolving the mode from the session via byteaOutputModeFromCtx
// or the equivalent getSetting lookup, never hardcoding one.
func byteaOutMode(b []byte, mode string) string { return array.ByteaOutStyled(b, mode) }

// byteaOutputModeFromCtx resolves the session's `bytea_output` GUC via
// ctx.GetSetting, defaulting to "hex" (PostgreSQL's boot default) when ctx is
// nil, has no GetSetting wired, or the GUC is unset — mirroring
// dateStyleFromCtx/timeZoneFromCtx (internal/executor/expr.go). PG validates
// the enum at SET time, so any other stored value is unreachable in practice;
// byteaOutMode still normalises it to hex out of caution. M0134-0001 S12.
func byteaOutputModeFromCtx(ctx *Context) string {
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("bytea_output"); ok {
			return v
		}
	}
	return "hex"
}
