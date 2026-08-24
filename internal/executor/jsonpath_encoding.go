package executor

import "strings"

// jsonpath string-escape lexing (M0134-0134).
//
// goopg has no jsonpath grammar parser yet — path navigation (`$.a`),
// filters (`?(...)`), operators, and numeric literals all pass through
// `::jsonpath` unvalidated (M0134 deferral, tracked alongside the GIN/GiST
// physical-index-integration and pg_shdepend-shaped gaps as a candidate for
// its own milestone). This file closes ONE narrow slice of that gap: the
// double-quoted-string escape rules PG's jsonpath scanner enforces
// (postgres/src/backend/utils/adt/jsonpath_scan.l — states xq/xnq/xvq,
// functions parseUnicode/addUnicode/addUnicodeChar/hexval), which is all
// jsonpath_encoding.sql exercises. PG's scanner uses the SAME string-escape
// state for a quoted path value (`"foo"`) and a quoted key after `.`
// (`$."foo"`), so one quote-scanning pass over the whole input handles both
// without needing the surrounding grammar.
//
// Unlike `::json` (M0134-0133), which preserves the input's exact spelling
// because PG's json type has no re-serializing printer, `::jsonpath` DOES
// round-trip through PG's own printer (jsonPathToCstring/printJsonPathItem,
// jsonpath.c) — so a decoded escape like `$` prints back as the literal
// `$` character, while a literal backslash from `\\u0024` prints back
// RE-escaped as `\\u0024`. rewriteJSONPathText mirrors that decode+re-print
// round trip using the same escape_json_char rule set jsonb canonicalization
// already implements (appendJSONBEscaped, jsonb_canonical.go).
//
// This is the hand-ported JSON-string-escape lexer M0134-0133's deferral
// ledger row anticipated reusing across json_encoding.sql and
// jsonpath_encoding.sql — it landed here first because jsonpath's cast had
// ZERO escape handling (pure pass-through) where json/jsonb's shared
// validators already existed; retrofitting json_encoding.sql's still-open
// surrogate-pair gap onto this lexer is a separate follow-on (ledger).

// rewriteJSONPathText re-lexes every double-quoted string token in a
// ::jsonpath cast's input and re-prints it through appendJSONBEscaped,
// leaving everything outside a quoted string byte-for-byte unchanged.
func rewriteJSONPathText(s string) (string, error) {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '"' {
			decoded, next, err := scanJSONPathQuotedString(s, i)
			if err != nil {
				return "", err
			}
			appendJSONBEscaped(&out, decoded)
			i = next
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String(), nil
}

// jsonpathSyntaxError matches jsonpath_yyerror's "at or near" wording
// (jsonpath_scan.l) for a lexing error whose offending token is still
// available. near is written WITHOUT Go's %q escaping — PG's message embeds
// the raw matched text (a literal "\u", not an escaped "\\u").
func jsonpathSyntaxError(msg, near string) error {
	return &ExecError{Code: "42601", Message: msg + ` at or near "` + near + `" of jsonpath input`}
}

// jsonpathSyntaxErrorAtEnd matches jsonpath_yyerror's end-of-input wording
// (yytext == YY_END_OF_BUFFER_CHAR branch).
func jsonpathSyntaxErrorAtEnd(msg string) error {
	return &ExecError{Code: "42601", Message: msg + " at end of jsonpath input"}
}

// jsonpathSurrogateError matches addUnicode's surrogate-pairing failures
// (ERRCODE_INVALID_TEXT_REPRESENTATION, "invalid input syntax for type
// jsonpath" + DETAIL).
func jsonpathSurrogateError(detail string) error {
	return &ExecError{Code: "22P02", Message: "invalid input syntax for type jsonpath", Detail: detail}
}

// jsonpathNulEscapeError matches addUnicodeChar's zero-codepoint rejection
// (ERRCODE_UNTRANSLATABLE_CHARACTER — goopg's TEXT storage can't hold NUL,
// same reason json_lex_string rejects it, M0134-0133's ledgered twin).
func jsonpathNulEscapeError() error {
	nulEscape := "\\" + "u0000"
	return &ExecError{Code: "22P05", Message: "unsupported Unicode escape sequence", Detail: nulEscape + " cannot be converted to text."}
}

// scanJSONPathQuotedString decodes one double-quoted jsonpath string token
// starting at s[start] == '"', returning the decoded text and the index just
// past the closing quote.
func scanJSONPathQuotedString(s string, start int) (string, int, error) {
	i := start + 1
	var out strings.Builder
	hiSurrogate := -1
	flushSurrogate := func() error {
		if hiSurrogate != -1 {
			hiSurrogate = -1
			return jsonpathSurrogateError("Unicode low surrogate must follow a high surrogate.")
		}
		return nil
	}
	for i < len(s) {
		c := s[i]
		if c == '"' {
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			return out.String(), i + 1, nil
		}
		if c != '\\' {
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte(c)
			i++
			continue
		}
		// c == '\\'
		if i+1 >= len(s) {
			return "", 0, jsonpathSyntaxError("unexpected end after backslash", "\\")
		}
		switch s[i+1] {
		case 'b':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\b')
			i += 2
		case 'f':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\f')
			i += 2
		case 'n':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\n')
			i += 2
		case 'r':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\r')
			i += 2
		case 't':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\t')
			i += 2
		case 'v':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte('\v')
			i += 2
		case 'x':
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			ch, next, err := scanJSONPathHexEscape(s, i)
			if err != nil {
				return "", 0, err
			}
			if ch == 0 {
				return "", 0, jsonpathNulEscapeError()
			}
			out.WriteRune(rune(ch))
			i = next
		case 'u':
			runStart := i
			for i < len(s) && s[i] == '\\' && i+1 < len(s) && s[i+1] == 'u' {
				ch, next, err := scanJSONPathUnicodeToken(s, i, runStart)
				if err != nil {
					return "", 0, err
				}
				i = next
				switch {
				case isUTF16HighSurrogate(ch):
					if hiSurrogate != -1 {
						return "", 0, jsonpathSurrogateError("Unicode high surrogate must not follow a high surrogate.")
					}
					hiSurrogate = ch
					continue
				case isUTF16LowSurrogate(ch):
					if hiSurrogate == -1 {
						return "", 0, jsonpathSurrogateError("Unicode low surrogate must follow a high surrogate.")
					}
					ch = surrogatePairToCodepoint(hiSurrogate, ch)
					hiSurrogate = -1
				default:
					if hiSurrogate != -1 {
						return "", 0, jsonpathSurrogateError("Unicode low surrogate must follow a high surrogate.")
					}
				}
				if ch == 0 {
					return "", 0, jsonpathNulEscapeError()
				}
				out.WriteRune(rune(ch))
			}
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
		default:
			if err := flushSurrogate(); err != nil {
				return "", 0, err
			}
			out.WriteByte(s[i+1])
			i += 2
		}
	}
	return "", 0, jsonpathSyntaxErrorAtEnd("unterminated quoted string")
}

// scanJSONPathUnicodeToken decodes one `\uXXXX` or `\u{XXXXXX}` token
// starting at s[i] == '\\' (s[i+1] == 'u'). runStart is the start of the
// current maximal run of \u tokens (jsonpath_encoding.sql's "invalid Unicode
// escape sequence" error reports the whole run's matched text, mirroring
// flex's `{unicode}*{unicodefail}` rule).
func scanJSONPathUnicodeToken(s string, i, runStart int) (int, int, error) {
	j := i + 2
	if j < len(s) && s[j] == '{' {
		j++
		ch := 0
		digits := 0
		for j < len(s) && s[j] != '}' && digits <= 6 {
			v, ok := hexDigitVal(s[j])
			if !ok {
				return 0, 0, jsonpathSyntaxError("invalid Unicode escape sequence", s[runStart:j])
			}
			ch = ch<<4 | v
			j++
			digits++
		}
		if j >= len(s) || s[j] != '}' || digits == 0 || digits > 6 {
			return 0, 0, jsonpathSyntaxError("invalid Unicode escape sequence", s[runStart:j])
		}
		return ch, j + 1, nil
	}
	ch := 0
	got := 0
	for got < 4 && j < len(s) {
		v, ok := hexDigitVal(s[j])
		if !ok {
			break
		}
		ch = ch<<4 | v
		j++
		got++
	}
	if got < 4 {
		return 0, 0, jsonpathSyntaxError("invalid Unicode escape sequence", s[runStart:j])
	}
	return ch, j, nil
}

// scanJSONPathHexEscape decodes a `\xXX` token (jsonpath_scan.l's hex_char /
// parseHexChar) starting at s[i] == '\\' (s[i+1] == 'x'). Not exercised by
// jsonpath_encoding.sql, but implemented alongside \u for completeness since
// it shares hexDigitVal/addUnicodeChar's escape-decode rule.
func scanJSONPathHexEscape(s string, i int) (int, int, error) {
	j := i + 2
	near := "\\x"
	if j < len(s) {
		near += string(s[j])
	}
	v1, ok1 := byteHexDigitVal(s, j)
	if !ok1 {
		return 0, 0, jsonpathSyntaxError("invalid hexadecimal character sequence", near)
	}
	j++
	if j < len(s) {
		near += string(s[j])
	}
	v2, ok2 := byteHexDigitVal(s, j)
	if !ok2 {
		return 0, 0, jsonpathSyntaxError("invalid hexadecimal character sequence", near)
	}
	j++
	return v1<<4 | v2, j, nil
}

func byteHexDigitVal(s string, j int) (int, bool) {
	if j >= len(s) {
		return 0, false
	}
	return hexDigitVal(s[j])
}

func hexDigitVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 0xA, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 0xA, true
	}
	return 0, false
}

func isUTF16HighSurrogate(ch int) bool { return ch >= 0xD800 && ch <= 0xDBFF }
func isUTF16LowSurrogate(ch int) bool  { return ch >= 0xDC00 && ch <= 0xDFFF }

func surrogatePairToCodepoint(hi, lo int) int {
	return 0x10000 + (hi-0xD800)*0x400 + (lo - 0xDC00)
}
