package parser

import (
	"fmt"
	"strings"
	"unicode"
)

// LexError reports a lexing failure with byte position.
type LexError struct {
	Pos     int
	Message string
}

func (e *LexError) Error() string {
	return fmt.Sprintf("lex error at byte %d: %s", e.Pos, e.Message)
}

// Lex breaks input into tokens, returning a slice that ends with a
// TokenEOF sentinel. Errors stop scanning.
func Lex(input string) ([]Token, error) {
	return lexInto(nil, input)
}

// lexInto appends tokens for input into dst and returns the result.
// If dst is non-nil its backing array is reused (pool-friendly). M0098-0006.
func lexInto(dst []Token, input string) ([]Token, error) {
	l := &lexer{src: input}
	out := dst
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
		if tok.Kind == TokenEOF {
			return out, nil
		}
	}
}

type lexer struct {
	src string
	pos int
}

func (l *lexer) errf(pos int, format string, args ...interface{}) error {
	return &LexError{Pos: pos, Message: fmt.Sprintf(format, args...)}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *lexer) peekAt(off int) byte {
	if l.pos+off >= len(l.src) {
		return 0
	}
	return l.src[l.pos+off]
}

func (l *lexer) skipWhitespaceAndComments() error {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			l.pos++
		case c == '-' && l.peekAt(1) == '-':
			// Line comment.
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && l.peekAt(1) == '*':
			// Block comment, nestable per upstream.
			start := l.pos
			depth := 0
			for l.pos < len(l.src) {
				if l.src[l.pos] == '/' && l.peekAt(1) == '*' {
					depth++
					l.pos += 2
				} else if l.src[l.pos] == '*' && l.peekAt(1) == '/' {
					depth--
					l.pos += 2
					if depth == 0 {
						break
					}
				} else {
					l.pos++
				}
			}
			if depth != 0 {
				return l.errf(start, "unterminated block comment")
			}
		default:
			return nil
		}
	}
	return nil
}

func (l *lexer) next() (Token, error) {
	if err := l.skipWhitespaceAndComments(); err != nil {
		return Token{}, err
	}
	if l.pos >= len(l.src) {
		return Token{Kind: TokenEOF, Pos: l.pos}, nil
	}
	start := l.pos
	c := l.src[l.pos]

	switch {
	case isIdentStart(c):
		for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
			l.pos++
		}
		text := strings.ToLower(l.src[start:l.pos])
		// E'...' escape-string literal: single character 'e'/'E' immediately
		// followed by a single-quote with no whitespace.
		if (text == "e") && l.pos < len(l.src) && l.src[l.pos] == '\'' {
			return l.lexEscapeString(start)
		}
		if kw, ok := keywords[text]; ok {
			return Token{Kind: TokenKeyword, Keyword: kw, Value: text, Pos: start}, nil
		}
		return Token{Kind: TokenIdent, Value: text, Pos: start}, nil

	case c == '"':
		// Quoted identifier; "" doubles to ".
		l.pos++
		var b strings.Builder
		for l.pos < len(l.src) {
			if l.src[l.pos] == '"' {
				if l.peekAt(1) == '"' {
					b.WriteByte('"')
					l.pos += 2
					continue
				}
				l.pos++
				return Token{Kind: TokenQuotedIdent, Value: b.String(), Pos: start}, nil
			}
			b.WriteByte(l.src[l.pos])
			l.pos++
		}
		return Token{}, l.errf(start, "unterminated quoted identifier")

	case c == '\'':
		l.pos++
		var b strings.Builder
		for l.pos < len(l.src) {
			if l.src[l.pos] == '\'' {
				if l.peekAt(1) == '\'' {
					b.WriteByte('\'')
					l.pos += 2
					continue
				}
				l.pos++
				return Token{Kind: TokenStringLit, Value: b.String(), Pos: start}, nil
			}
			b.WriteByte(l.src[l.pos])
			l.pos++
		}
		return Token{}, l.errf(start, "unterminated string literal")

	case isDigit(c):
		// l.pos currently points to c (not yet advanced past it).
		// Advance past c so subsequent checks look at the NEXT character.
		l.pos++
		// Detect non-decimal integer prefixes: 0b (binary), 0o (octal), 0x/0X (hex).
		// PostgreSQL 16+ numeric literal syntax. M0097-0003.
		if c == '0' && l.pos < len(l.src) {
			next := l.src[l.pos]
			switch {
			case next == 'b' || next == 'B':
				// Binary literal: 0b[01]+ with optional _ separators.
				l.pos++ // consume 'b'
				digStart := l.pos
				for l.pos < len(l.src) && (l.src[l.pos] == '0' || l.src[l.pos] == '1' || l.src[l.pos] == '_') {
					l.pos++
				}
				if l.pos == digStart {
					// No valid binary digits → PG-compatible "invalid binary integer" error.
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "invalid binary integer at or near %q", l.src[start:l.pos])
				}
				if l.pos < len(l.src) && isIdentStart(l.src[l.pos]) {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
				}
				return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
			case next == 'o' || next == 'O':
				// Octal literal: 0o[0-7]+ with optional _ separators.
				l.pos++
				digStart := l.pos
				for l.pos < len(l.src) && ((l.src[l.pos] >= '0' && l.src[l.pos] <= '7') || l.src[l.pos] == '_') {
					l.pos++
				}
				if l.pos == digStart {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "invalid octal integer at or near %q", l.src[start:l.pos])
				}
				if l.pos < len(l.src) && isIdentStart(l.src[l.pos]) {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
				}
				return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
			case next == 'x' || next == 'X':
				// Hexadecimal literal: 0x[0-9a-fA-F]+ with optional _ separators.
				l.pos++
				digStart := l.pos
				for l.pos < len(l.src) && (isHexDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
					l.pos++
				}
				if l.pos == digStart {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "invalid hexadecimal integer at or near %q", l.src[start:l.pos])
				}
				if l.pos < len(l.src) && isIdentStart(l.src[l.pos]) {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
				}
				return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
			}
		}
		// Decimal integer (possibly with _ separators): consume remaining digits.
		// checkUnderscoreJunk returns true if the consumed range [rStart, rEnd)
		// contains an invalid underscore pattern: trailing underscore or double underscore.
		checkUnderscoreJunk := func(rStart, rEnd int) bool {
			s := l.src[rStart:rEnd]
			if len(s) == 0 {
				return false
			}
			if s[len(s)-1] == '_' {
				return true
			}
			for i := 1; i < len(s); i++ {
				if s[i] == '_' && s[i-1] == '_' {
					return true
				}
			}
			return false
		}
		intPartStart := l.pos - 1
		for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
		// Trailing underscore or double underscore in integer part → trailing junk.
		// e.g. "100_", "100__000" → PostgreSQL errors. M0097-0003.
		if checkUnderscoreJunk(intPartStart, l.pos) {
			for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
			return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
		}
		// Optional fractional part. We commit to a decimal literal when we see a dot
		// that is NOT immediately followed by an identifier (qualified-name form `a.b`).
		// A trailing `.` (e.g. `1000.` or `1_000.`) without following digits or exponent
		// is a valid float literal in PostgreSQL. M0097-0003.
		isNumeric := false
		if l.pos < len(l.src) && l.src[l.pos] == '.' {
			nextAfterDot := byte(0)
			if l.pos+1 < len(l.src) {
				nextAfterDot = l.src[l.pos+1]
			}
			// Commit to numeric if: next char after '.' is a digit, whitespace, semicolon,
			// comma, closing paren, end of input, or other non-ident non-dot char.
			// Also commit if next is 'e'/'E' followed by '+', '-', or a digit (scientific notation
			// like "4664.E+5" or "1.E-10"). Do NOT commit if followed by a plain identifier.
			isExponentStart := (nextAfterDot == 'e' || nextAfterDot == 'E') &&
				l.pos+2 < len(l.src) && (l.src[l.pos+2] == '+' || l.src[l.pos+2] == '-' || isDigit(l.src[l.pos+2]))
			if isExponentStart || (!isIdentStart(nextAfterDot) && nextAfterDot != '.') {
				l.pos++ // consume '.'
				fracStart := l.pos
				for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
					l.pos++
				}
				// Trailing/double underscore in fractional part → error.
				if checkUnderscoreJunk(fracStart, l.pos) {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
				}
				isNumeric = true
			}
		}
		// Optional exponent: e[+-]?digits
		if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
			save := l.pos
			l.pos++
			if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
				l.pos++
			}
			expStart := l.pos
			// Leading underscore in exponent is invalid.
			if l.pos < len(l.src) && l.src[l.pos] == '_' {
				// Consume junk identifier chars for better error reporting.
				for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
				return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
			}
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
			if l.pos == expStart {
				// Not a real exponent — back off so a token like
				// `1e` followed by something else doesn't get
				// silently consumed.
				l.pos = save
			} else {
				// Trailing/double underscore in exponent → error.
				if checkUnderscoreJunk(expStart, l.pos) {
					for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
					return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
				}
				isNumeric = true
			}
		}
		// PostgreSQL error: "trailing junk after numeric literal" when the
		// literal is immediately followed by an identifier character (e.g.
		// "123abc", "0.0e1a"). Return a lex error so the parser reports
		// the correct error message. M0097-0003.
		if l.pos < len(l.src) && isIdentStart(l.src[l.pos]) {
			// Consume the junk to include it in the error token value.
			for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
				l.pos++
			}
			return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
		}
		if isNumeric {
			return Token{Kind: TokenNumericLit, Value: l.src[start:l.pos], Pos: start}, nil
		}
		return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil

	case c == '$':
		l.pos++
		// Dollar-quoted string literal (M0015): `$$body$$` or
		// `$tag$body$tag$`. Used by PL/pgSQL function bodies and
		// any payload where embedded single-quotes would be
		// awkward. The tag may be empty or a SQL identifier
		// (letter/underscore followed by letter/digit/underscore).
		// Closer must match the opening tag exactly.
		if l.pos < len(l.src) && (l.src[l.pos] == '$' || isIdentStart(l.src[l.pos])) {
			tagStart := l.pos
			// Tag chars follow upstream's rule: an unquoted
			// identifier *without* `$` (PG manual: "the tag ...
			// cannot contain a dollar sign"). Empty tag (`$$`)
			// falls out of the loop after consuming zero bytes.
			for l.pos < len(l.src) && isDollarTagCont(l.src[l.pos]) {
				l.pos++
			}
			if l.pos < len(l.src) && l.src[l.pos] == '$' {
				tag := l.src[tagStart:l.pos]
				l.pos++ // consume closing `$` of opener
				bodyStart := l.pos
				closer := "$" + tag + "$"
				for l.pos+len(closer) <= len(l.src) {
					if l.src[l.pos] == '$' && l.src[l.pos:l.pos+len(closer)] == closer {
						body := l.src[bodyStart:l.pos]
						l.pos += len(closer)
						return Token{Kind: TokenStringLit, Value: body, Pos: start}, nil
					}
					l.pos++
				}
				return Token{}, l.errf(start, "unterminated dollar-quoted string (looking for %q)", closer)
			}
			// Not a dollar-quote opener — rewind so the
			// positional-parameter path can re-scan from `$`.
			l.pos = tagStart
		}
		dStart := l.pos
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		if l.pos == dStart {
			return Token{}, l.errf(start, "expected digit after '$'")
		}
		// Trailing junk after parameter number: "$1a", "$0_1" etc. M0097-0003.
		if l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
			// Consume the junk identifier chars.
			for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) {
				l.pos++
			}
			return Token{}, l.errf(start, "trailing junk after parameter at or near %q",
				"$"+l.src[dStart:l.pos])
		}
		// Parameter number overflow check: PostgreSQL supports up to INT_MAX (2147483647).
		paramStr := l.src[dStart:l.pos]
		if len(paramStr) > 10 || (len(paramStr) == 10 && paramStr > "2147483647") {
			return Token{}, l.errf(start, "parameter number too large at or near %q", "$"+paramStr)
		}
		return Token{Kind: TokenParam, Value: paramStr, Pos: start}, nil

	case c == ',' || c == ';' || c == '(' || c == ')' || c == '.' || c == '*' || c == '[' || c == ']':
		if c == '.' && l.peekAt(1) == '.' {
			l.pos += 2
			return Token{Kind: TokenOperator, Value: "..", Pos: start}, nil
		}
		// Decimal literal starting with '.': e.g. ".5" or ".000_005".
		// If '.' is followed by a digit, lex as a numeric literal. M0097-0003.
		if c == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
			l.pos++ // consume '.'
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
			// Optional exponent.
			if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
				save := l.pos
				l.pos++
				if l.pos < len(l.src) && (l.src[l.pos] == '+' || l.src[l.pos] == '-') {
					l.pos++
				}
				expStart := l.pos
				for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
					l.pos++
				}
				if l.pos == expStart {
					l.pos = save
				}
			}
			// Trailing junk check.
			if l.pos < len(l.src) && isIdentStart(l.src[l.pos]) {
				for l.pos < len(l.src) && isIdentCont(l.src[l.pos]) { l.pos++ }
				return Token{}, l.errf(start, "trailing junk after numeric literal at or near %q", l.src[start:l.pos])
			}
			return Token{Kind: TokenNumericLit, Value: l.src[start:l.pos], Pos: start}, nil
		}
		l.pos++
		return Token{Kind: TokenSymbol, Value: string(c), Pos: start}, nil

	case c == ':':
		// `::` is upstream's typecast operator; a bare `:` is not
		// otherwise meaningful in goopg's SQL surface and surfaces
		// here as a lex error so the wire layer can return SQLSTATE
		// 42601. `:=` is the PL/pgSQL assignment operator
		// (M0015 Stage A step 4b) — recognised by the shared lexer
		// because PL/pgSQL bodies tokenise through the same `Lex`
		// entry point as SQL.
		if l.peekAt(1) == ':' {
			l.pos += 2
			return Token{Kind: TokenOperator, Value: "::", Pos: start}, nil
		}
		if l.peekAt(1) == '=' {
			l.pos += 2
			return Token{Kind: TokenOperator, Value: ":=", Pos: start}, nil
		}
		return Token{}, l.errf(start, "unexpected character %q", c)

	case c == '<' || c == '>' || c == '=' || c == '!' || c == '+' || c == '-' || c == '/' || c == '%' || c == '|' || c == '&' || c == '#' || c == '~' || c == '@' || c == '^':
		// Greedy multi-char operator match. M0097-0003: added <<, >>, &, #, ~.
		two := ""
		if l.pos+1 < len(l.src) {
			two = l.src[l.pos : l.pos+2]
		}
		switch two {
		case "<=", ">=", "<>", "!=", "||", "<<", ">>", "~*", "!~", "=>", "<@", "@>", "&&", "->":
			l.pos += 2
			// Check for 3-char operators (e.g., !~*).
			if l.pos < len(l.src) && l.src[l.pos] == '*' && (two == "!~") {
				l.pos++
				return Token{Kind: TokenOperator, Value: "!~*", Pos: start}, nil
			}
			// JSON text-extraction operator ->> (3-char). M0118-0009.
			if l.pos < len(l.src) && l.src[l.pos] == '>' && two == "->" {
				l.pos++
				return Token{Kind: TokenOperator, Value: "->>", Pos: start}, nil
			}
			return Token{Kind: TokenOperator, Value: two, Pos: start}, nil
		}
		l.pos++
		return Token{Kind: TokenOperator, Value: string(c), Pos: start}, nil
	}

	return Token{}, l.errf(start, "unexpected character %q", c)
}

// lexEscapeString handles E'...' escape-string literals. The E/e prefix has
// already been consumed and l.pos points at the opening single-quote.
// Supported escape sequences (PostgreSQL-compatible):
//
//	\n \t \r \b \f \v  — C-style single-char escapes
//	\ooo               — octal (1-3 digits)
//	\xhh               — hex (1-2 digits)
//	\uXXXX             — Unicode 4-hex-digit codepoint
//	\UXXXXXXXX         — Unicode 8-hex-digit codepoint
//	\'                 — literal single-quote
//	\\                 — literal backslash
//	''                 — literal single-quote (standard SQL doubling)
func (l *lexer) lexEscapeString(start int) (Token, error) {
	l.pos++ // consume opening '\''
	var b strings.Builder
	for l.pos < len(l.src) {
		ch := l.src[l.pos]
		if ch == '\'' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '\'' {
				b.WriteByte('\'')
				l.pos += 2
				continue
			}
			l.pos++
			return Token{Kind: TokenStringLit, Value: b.String(), Pos: start}, nil
		}
		if ch == '\\' {
			l.pos++ // consume backslash
			if l.pos >= len(l.src) {
				break
			}
			esc := l.src[l.pos]
			l.pos++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '\'':
				b.WriteByte('\'')
			case '\\':
				b.WriteByte('\\')
			case 'x', 'X':
				// Hex: 1-2 digits.
				val := byte(0)
				count := 0
				for count < 2 && l.pos < len(l.src) && isHexDigit(l.src[l.pos]) {
					val = val*16 + hexVal(l.src[l.pos])
					l.pos++
					count++
				}
				b.WriteByte(val)
			case 'u':
				// Unicode 4-hex-digit.
				val := rune(0)
				for i := 0; i < 4 && l.pos < len(l.src) && isHexDigit(l.src[l.pos]); i++ {
					val = val*16 + rune(hexVal(l.src[l.pos]))
					l.pos++
				}
				b.WriteRune(val)
			case 'U':
				// Unicode 8-hex-digit.
				val := rune(0)
				for i := 0; i < 8 && l.pos < len(l.src) && isHexDigit(l.src[l.pos]); i++ {
					val = val*16 + rune(hexVal(l.src[l.pos]))
					l.pos++
				}
				b.WriteRune(val)
			default:
				if esc >= '0' && esc <= '7' {
					// Octal: 1-3 digits.
					val := int(esc - '0')
					for i := 0; i < 2 && l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '7'; i++ {
						val = val*8 + int(l.src[l.pos]-'0')
						l.pos++
					}
					b.WriteByte(byte(val))
				} else {
					// Unknown escape: pass through literally (PG compatibility).
					b.WriteByte('\\')
					b.WriteByte(esc)
				}
			}
			continue
		}
		b.WriteByte(ch)
		l.pos++
	}
	return Token{}, l.errf(start, "unterminated escape string literal")
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		// allow extended-ASCII letters; upstream is more permissive but
		// pgbench identifiers are ASCII.
		c >= 0x80 && unicode.IsLetter(rune(c))
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || isDigit(c) || c == '$'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isDollarTagCont is the dollar-quote tag character class:
// identifier chars except `$`. The `$` exclusion mirrors upstream's
// PostgreSQL rule (per the manual: "the tag ... cannot contain a
// dollar sign") — it's what lets the lexer detect the end of the
// opening tag at the first subsequent `$`.
func isDollarTagCont(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}
