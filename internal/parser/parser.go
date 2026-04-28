package parser

import (
	"fmt"
	"strings"
)

// SyntaxError is the parser's structured error. Message mirrors
// upstream's `syntax error at or near "TOKEN"` shape so psql users
// can reason about it.
type SyntaxError struct {
	Pos     int
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("syntax error at or near %q (byte %d)", e.Message, e.Pos)
}

// Parse splits input on statement boundaries and returns one Stmt per
// non-empty statement. A trailing semicolon is allowed; an empty input
// returns an empty slice and no error.
func Parse(input string) ([]Stmt, error) {
	toks, err := Lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: toks}
	var out []Stmt
	for p.cur().Kind != TokenEOF {
		// Empty statement (just `;`).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			p.advance()
			continue
		}
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		out = append(out, stmt)
		// Optional trailing semicolon between statements; mandatory
		// before another statement starts.
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			p.advance()
			continue
		}
		if p.cur().Kind != TokenEOF {
			return nil, p.errAtCur("expected ';' or end of input")
		}
	}
	return out, nil
}

type parser struct {
	tokens []Token
	idx    int
}

func (p *parser) cur() Token {
	if p.idx >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[p.idx]
}

func (p *parser) peek(off int) Token {
	if p.idx+off >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[p.idx+off]
}

func (p *parser) advance() Token {
	t := p.cur()
	p.idx++
	return t
}

// errAtCur builds a SyntaxError pinned at the current token. The
// message echoes the token text, matching upstream's "at or near".
func (p *parser) errAtCur(msg string) error {
	t := p.cur()
	near := t.Value
	if t.Kind == TokenEOF {
		near = "end of input"
	}
	return &SyntaxError{Pos: t.Pos, Message: msg + " (got " + near + ")"}
}

// expectKeyword consumes the current token if it's the named keyword;
// otherwise it returns a syntax error.
func (p *parser) expectKeyword(kw Keyword) (Token, error) {
	t := p.cur()
	if t.Kind != TokenKeyword || t.Keyword != kw {
		return Token{}, p.errAtCur("expected keyword " + string(kw))
	}
	p.advance()
	return t, nil
}

// acceptKeyword returns true and advances when the current token is kw.
func (p *parser) acceptKeyword(kw Keyword) bool {
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == kw {
		p.advance()
		return true
	}
	return false
}

// acceptSymbol returns true and advances when the current token is the
// given punctuation symbol.
func (p *parser) acceptSymbol(sym string) bool {
	if p.cur().Kind == TokenSymbol && p.cur().Value == sym {
		p.advance()
		return true
	}
	return false
}

// acceptIdentKeyword consumes a TokenIdent matching any of the
// given (case-insensitive) names. Used for SQL words upstream
// treats as unreserved keywords — `FETCH`, `FIRST`, `NEXT`,
// `ROW`, `ROWS`, `ONLY`. Returns false (without advancing)
// when the current token doesn't match.
func (p *parser) acceptIdentKeyword(names ...string) bool {
	t := p.cur()
	if t.Kind != TokenIdent {
		return false
	}
	for _, n := range names {
		if strings.EqualFold(t.Value, n) {
			p.advance()
			return true
		}
	}
	return false
}

// parseStatement dispatches on the leading keyword.
func (p *parser) parseStatement() (Stmt, error) {
	t := p.cur()
	if t.Kind != TokenKeyword {
		return nil, p.errAtCur("expected statement")
	}
	switch t.Keyword {
	case KwBegin:
		return p.parseBegin()
	case KwCommit, KwEnd:
		return p.parseCommit()
	case KwRollback, KwAbort:
		return p.parseRollback()
	case KwVacuum:
		return p.parseVacuum()
	case KwAnalyze, KwAnalyse:
		return p.parseAnalyze()
	case KwShow:
		return p.parseShow()
	case KwSet:
		return p.parseSet()
	case KwReset:
		return p.parseReset()
	case KwSelect:
		return p.parseSelect()
	case KwInsert:
		return p.parseInsert()
	case KwUpdate:
		return p.parseUpdate()
	case KwDelete:
		return p.parseDelete()
	case KwCreate:
		return p.parseCreate()
	case KwDrop:
		return p.parseDrop()
	case KwTruncate:
		return p.parseTruncate()
	case KwAlter:
		return p.parseAlter()
	case KwCopy:
		return p.parseCopy()
	case KwCheckpoint:
		return p.parseCheckpoint()
	}
	return nil, p.errAtCur("unsupported statement")
}

// parseCheckpoint: CHECKPOINT
func (p *parser) parseCheckpoint() (Stmt, error) {
	t := p.advance()
	return &CheckpointStmt{pos: t.Pos}, nil
}

// parseBegin: BEGIN [WORK | TRANSACTION]
func (p *parser) parseBegin() (Stmt, error) {
	t := p.advance() // BEGIN
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &BeginStmt{pos: t.Pos}, nil
}

// parseCommit: COMMIT [WORK | TRANSACTION] | END [WORK | TRANSACTION]
func (p *parser) parseCommit() (Stmt, error) {
	t := p.advance()
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &CommitStmt{pos: t.Pos}, nil
}

// parseRollback: ROLLBACK [WORK | TRANSACTION] | ABORT [WORK | TRANSACTION]
func (p *parser) parseRollback() (Stmt, error) {
	t := p.advance()
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &RollbackStmt{pos: t.Pos}, nil
}

// parseVacuum: VACUUM [VERBOSE] [ANALYZE] [target [, target …]]
func (p *parser) parseVacuum() (Stmt, error) {
	t := p.advance()
	v := &VacuumStmt{pos: t.Pos}
	for {
		switch {
		case p.acceptKeyword(KwVerbose):
			v.Verbose = true
		case p.acceptKeyword(KwAnalyze) || p.acceptKeyword(KwAnalyse):
			v.Analyze = true
		default:
			goto targets
		}
	}
targets:
	if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return v, nil
	}
	tgts, err := p.parseObjectList()
	if err != nil {
		return nil, err
	}
	v.Targets = tgts
	return v, nil
}

// parseAnalyze: ANALYZE [VERBOSE] [target [, target …]]
func (p *parser) parseAnalyze() (Stmt, error) {
	t := p.advance()
	a := &AnalyzeStmt{pos: t.Pos}
	if p.acceptKeyword(KwVerbose) {
		a.Verbose = true
	}
	if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return a, nil
	}
	tgts, err := p.parseObjectList()
	if err != nil {
		return nil, err
	}
	a.Targets = tgts
	return a, nil
}

// parseObjectList parses one or more comma-separated ObjectNames.
func (p *parser) parseObjectList() ([]ObjectName, error) {
	var out []ObjectName
	first, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		o, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// parseObjectName parses [schema.]name where each part is an
// identifier (possibly quoted).
func (p *parser) parseObjectName() (ObjectName, error) {
	first, err := p.parseIdent()
	if err != nil {
		return ObjectName{}, err
	}
	o := ObjectName{pos: first.Pos, Name: identText(first)}
	if p.acceptSymbol(".") {
		second, err := p.parseIdent()
		if err != nil {
			return ObjectName{}, err
		}
		o.Schema = o.Name
		o.Name = identText(second)
	}
	return o, nil
}

func (p *parser) parseIdent() (Token, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenQuotedIdent:
		p.advance()
		return t, nil
	case TokenKeyword:
		// Allow keyword-as-name in v0 — upstream classifies these as
		// "unreserved", and the keyword set we recognise overlaps with
		// table-name candidates (e.g. `analyze`). The analyzer can
		// reject reserved names later when the catalog distinguishes.
		p.advance()
		return t, nil
	}
	return Token{}, p.errAtCur("expected identifier")
}

func identText(t Token) string {
	// TokenIdent and TokenKeyword carry already-lowercased text;
	// TokenQuotedIdent preserves its original case.
	return t.Value
}

// parseShow: SHOW name | SHOW ALL
func (p *parser) parseShow() (Stmt, error) {
	t := p.advance()
	if p.acceptKeyword(KwAll) {
		return &ShowStmt{pos: t.Pos, All: true}, nil
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	return &ShowStmt{pos: t.Pos, Name: name}, nil
}

// parseSet: SET [LOCAL|SESSION] name { = | TO } { value | DEFAULT }
func (p *parser) parseSet() (Stmt, error) {
	t := p.advance()
	s := &SetStmt{pos: t.Pos}
	if p.acceptKeyword(KwLocal) {
		s.Local = true
	} else {
		_ = p.acceptKeyword(KwSession)
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	s.Name = name
	// Either '=' or TO.
	switch {
	case p.cur().Kind == TokenOperator && p.cur().Value == "=":
		p.advance()
	default:
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
	}
	if p.acceptKeyword(KwDefault) {
		s.Default = true
		return s, nil
	}
	val, err := p.parseSetValue()
	if err != nil {
		return nil, err
	}
	s.Value = val
	return s, nil
}

// parseReset: RESET name | RESET ALL
func (p *parser) parseReset() (Stmt, error) {
	t := p.advance()
	if p.acceptKeyword(KwAll) {
		return &ResetStmt{pos: t.Pos, All: true}, nil
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	return &ResetStmt{pos: t.Pos, Name: name}, nil
}

// parseGUCName accepts `name` or `name.subname` (for namespaced GUCs
// like `myext.foo`). Returns the dotted form.
func (p *parser) parseGUCName() (string, error) {
	first, err := p.parseIdent()
	if err != nil {
		return "", err
	}
	name := identText(first)
	for p.acceptSymbol(".") {
		next, err := p.parseIdent()
		if err != nil {
			return "", err
		}
		name = name + "." + identText(next)
	}
	return name, nil
}

// parseSetValue accepts an int literal, string literal, or identifier.
// Multiple comma-separated values are concatenated with commas (rare
// in pgbench but accepted upstream for things like
// `SET search_path TO a, b`).
func (p *parser) parseSetValue() (string, error) {
	parts, err := p.parseSetValueAtoms()
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ", "), nil
}

func (p *parser) parseSetValueAtoms() ([]string, error) {
	var out []string
	for {
		t := p.cur()
		switch t.Kind {
		case TokenIntLit:
			out = append(out, t.Value)
			p.advance()
		case TokenStringLit:
			out = append(out, t.Value)
			p.advance()
		case TokenIdent, TokenQuotedIdent, TokenKeyword:
			out = append(out, identText(t))
			p.advance()
		default:
			if t.Kind == TokenOperator && t.Value == "-" {
				// Allow leading minus on a numeric value (rare).
				p.advance()
				n := p.cur()
				if n.Kind != TokenIntLit {
					return nil, p.errAtCur("expected integer after '-'")
				}
				out = append(out, "-"+n.Value)
				p.advance()
				break
			}
			return nil, p.errAtCur("expected value")
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return out, nil
}
