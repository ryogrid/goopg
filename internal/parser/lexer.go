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
					// No binary digits — treat as `0` followed by `b` ident.
					l.pos = start + 1
					return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
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
					l.pos = start + 1
					return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
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
					l.pos = start + 1
					return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
				}
				return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil
			}
		}
		// Decimal integer (possibly with _ separators): consume remaining digits.
		for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
			l.pos++
		}
		// Optional fractional part. We commit to a decimal literal
		// only when we see digit(s) after the dot; a trailing `.`
		// followed by an identifier is upstream's qualified-name
		// form and stays an integer.
		isNumeric := false
		if l.pos < len(l.src) && l.src[l.pos] == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
			l.pos++ // consume '.'
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '_') {
				l.pos++
			}
			isNumeric = true
		}
		// Optional exponent: e[+-]?digits
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
				// Not a real exponent — back off so a token like
				// `1e` followed by something else doesn't get
				// silently consumed.
				l.pos = save
			} else {
				isNumeric = true
			}
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
		return Token{Kind: TokenParam, Value: l.src[dStart:l.pos], Pos: start}, nil

	case c == ',' || c == ';' || c == '(' || c == ')' || c == '.' || c == '*' || c == '[' || c == ']':
		if c == '.' && l.peekAt(1) == '.' {
			l.pos += 2
			return Token{Kind: TokenOperator, Value: "..", Pos: start}, nil
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

	case c == '<' || c == '>' || c == '=' || c == '!' || c == '+' || c == '-' || c == '/' || c == '%' || c == '|':
		// Greedy multi-char operator match.
		two := ""
		if l.pos+1 < len(l.src) {
			two = l.src[l.pos : l.pos+2]
		}
		switch two {
		case "<=", ">=", "<>", "!=", "||":
			l.pos += 2
			return Token{Kind: TokenOperator, Value: two, Pos: start}, nil
		}
		l.pos++
		return Token{Kind: TokenOperator, Value: string(c), Pos: start}, nil
	}

	return Token{}, l.errf(start, "unexpected character %q", c)
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
