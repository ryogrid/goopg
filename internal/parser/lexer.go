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
	l := &lexer{src: input}
	var out []Token
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
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		return Token{Kind: TokenIntLit, Value: l.src[start:l.pos], Pos: start}, nil

	case c == '$':
		l.pos++
		dStart := l.pos
		for l.pos < len(l.src) && isDigit(l.src[l.pos]) {
			l.pos++
		}
		if l.pos == dStart {
			return Token{}, l.errf(start, "expected digit after '$'")
		}
		return Token{Kind: TokenParam, Value: l.src[dStart:l.pos], Pos: start}, nil

	case c == ',' || c == ';' || c == '(' || c == ')' || c == '.' || c == '*':
		l.pos++
		return Token{Kind: TokenSymbol, Value: string(c), Pos: start}, nil

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
