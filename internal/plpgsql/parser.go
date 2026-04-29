package plpgsql

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// SyntaxError is the typed error returned from Parse on malformed
// input. Pos is the 0-based byte offset within the body source.
type SyntaxError struct {
	Pos     int
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("plpgsql: syntax error at byte %d: %s", e.Pos, e.Message)
}

// Parse parses a PL/pgSQL routine body — the bytes that lived
// between the dollar-quote delimiters — into an AST. Stage A 4a
// scope:
//
//	body ::= 'BEGIN' stmt_list 'END' [';']
//	stmt ::= 'RETURN' sql_expr ';'
//
// DECLARE blocks, assignment, IF / ELSIF / ELSE, LOOP / WHILE /
// FOR, EXIT / CONTINUE, PERFORM, SELECT INTO, and embedded SQL
// statements arrive in subsequent slices.
func Parse(src string) (*Block, error) {
	toks, err := parser.Lex(src)
	if err != nil {
		// LexError already pins the byte offset cleanly; wrap it
		// so callers see a plpgsql.SyntaxError typed envelope
		// without losing the location.
		if le, ok := err.(*parser.LexError); ok {
			return nil, &SyntaxError{Pos: le.Pos, Message: le.Message}
		}
		return nil, &SyntaxError{Pos: 0, Message: err.Error()}
	}
	p := &bodyParser{src: src, tokens: toks}
	return p.parseTopBlock()
}

type bodyParser struct {
	src    string
	tokens []parser.Token
	idx    int
}

func (p *bodyParser) cur() parser.Token {
	if p.idx >= len(p.tokens) {
		return parser.Token{Kind: parser.TokenEOF}
	}
	return p.tokens[p.idx]
}

func (p *bodyParser) advance() parser.Token {
	t := p.cur()
	p.idx++
	return t
}

func (p *bodyParser) errAt(pos int, format string, args ...interface{}) error {
	return &SyntaxError{Pos: pos, Message: fmt.Sprintf(format, args...)}
}

func (p *bodyParser) errAtCur(format string, args ...interface{}) error {
	return p.errAt(p.cur().Pos, format, args...)
}

func (p *bodyParser) acceptKeyword(kw parser.Keyword) bool {
	t := p.cur()
	if t.Kind == parser.TokenKeyword && t.Keyword == kw {
		p.advance()
		return true
	}
	return false
}

func (p *bodyParser) expectKeyword(kw parser.Keyword) (parser.Token, error) {
	t := p.cur()
	if t.Kind == parser.TokenKeyword && t.Keyword == kw {
		p.advance()
		return t, nil
	}
	return parser.Token{}, p.errAtCur("expected keyword %q", string(kw))
}

func (p *bodyParser) acceptSymbol(sym string) bool {
	t := p.cur()
	if t.Kind == parser.TokenSymbol && t.Value == sym {
		p.advance()
		return true
	}
	return false
}

// parseTopBlock parses `BEGIN ... END [;]`. Trailing semicolon
// after END is optional — upstream's surface accepts it, and
// PL/pgSQL function bodies frequently include it.
func (p *bodyParser) parseTopBlock() (*Block, error) {
	beginTok, err := p.expectKeyword(parser.KwBegin)
	if err != nil {
		return nil, p.errAtCur("expected BEGIN at start of PL/pgSQL body")
	}
	block := &Block{pos: beginTok.Pos}
	for {
		if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwEnd {
			break
		}
		if p.cur().Kind == parser.TokenEOF {
			return nil, p.errAtCur("expected END to close PL/pgSQL block")
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		block.Statements = append(block.Statements, stmt)
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, err
	}
	// Optional trailing semicolon after END — both `END` and
	// `END;` are upstream-legal.
	p.acceptSymbol(";")
	if p.cur().Kind != parser.TokenEOF {
		return nil, p.errAtCur("unexpected tokens after END")
	}
	return block, nil
}

func (p *bodyParser) parseStmt() (Stmt, error) {
	t := p.cur()
	switch {
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwReturn:
		return p.parseReturn()
	}
	return nil, p.errAtCur("unsupported PL/pgSQL statement (Stage A 4a accepts RETURN only)")
}

// parseReturn parses `RETURN expr;`. The expression is captured by
// scanning ahead for the terminating `;` and re-lexing the slice
// through `parser.ParseExpr` so the resulting AST is the same
// shape as a SELECT-target expression — enables future
// type-checking / planning / execution to reuse the SQL machinery
// without translation.
func (p *bodyParser) parseReturn() (*ReturnStmt, error) {
	retTok, err := p.expectKeyword(parser.KwReturn)
	if err != nil {
		return nil, err
	}
	exprStart := p.cur().Pos
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
		return nil, p.errAtCur("RETURN requires an expression in Stage A (RETURN; without value is not yet supported)")
	}
	// Scan forward to the next top-level `;` to find the end of
	// the expression. Stage A doesn't yet support nested
	// statement-shaped expressions inside RETURN, so this
	// flat-scan is sufficient — and stays accurate when the
	// expression contains `;` inside a string literal or
	// dollar-quote because the lexer already tokenised those into
	// non-`;` tokens.
	exprEnd := exprStart
	for p.cur().Kind != parser.TokenEOF {
		if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
			break
		}
		// `exprEnd` tracks the byte offset just past the last
		// expression token consumed (used to slice the source).
		t := p.cur()
		exprEnd = t.Pos + len(t.Value)
		p.advance()
	}
	if p.cur().Kind != parser.TokenSymbol || p.cur().Value != ";" {
		return nil, p.errAtCur("expected ';' to terminate RETURN")
	}
	p.advance() // consume ';'
	exprSrc := strings.TrimSpace(p.src[exprStart:exprEnd])
	if exprSrc == "" {
		return nil, p.errAt(exprStart, "RETURN requires a non-empty expression")
	}
	expr, err := parser.ParseExpr(exprSrc)
	if err != nil {
		return nil, p.errAt(exprStart, "RETURN expression: %v", err)
	}
	return &ReturnStmt{pos: retTok.Pos, Expr: expr}, nil
}
