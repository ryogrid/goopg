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
// scope: BEGIN/END + RETURN. Stage A 4b extends the body to
// optional DECLARE prefix + assignment:
//
//	body         ::= [decl_section] 'BEGIN' stmt_list 'END' [';']
//	decl_section ::= 'DECLARE' decl+
//	decl         ::= ident type [ ('DEFAULT'|':=') sql_expr ] ';'
//	stmt         ::= 'RETURN' sql_expr ';'
//	              |  ident ':=' sql_expr ';'
//
// IF / ELSIF / ELSE, LOOP / WHILE / FOR, EXIT / CONTINUE, PERFORM,
// SELECT INTO, and embedded SQL arrive in subsequent slices.
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

// parseTopBlock parses `[DECLARE decls] BEGIN ... END [;]`.
// Trailing semicolon after END is optional — upstream's surface
// accepts it, and PL/pgSQL function bodies frequently include it.
func (p *bodyParser) parseTopBlock() (*Block, error) {
	startPos := p.cur().Pos
	var decls []*Declaration
	if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwDeclare {
		p.advance()
		var err error
		decls, err = p.parseDeclSection()
		if err != nil {
			return nil, err
		}
	}
	beginTok, err := p.expectKeyword(parser.KwBegin)
	if err != nil {
		return nil, p.errAtCur("expected BEGIN at start of PL/pgSQL body")
	}
	if len(decls) == 0 {
		startPos = beginTok.Pos
	}
	block := &Block{pos: startPos, Declarations: decls}
	stmts, err := p.parseStmtList(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	block.Statements = stmts
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

// parseStmtList parses zero-or-more statements terminated by one
// of the provided keywords. Does NOT consume the terminator.
func (p *bodyParser) parseStmtList(terminators ...parser.Keyword) ([]Stmt, error) {
	var stmts []Stmt
Loop:
	for {
		t := p.cur()
		if t.Kind == parser.TokenEOF {
			return nil, p.errAtCur("unexpected EOF (expected one of %v)", terminators)
		}
		if t.Kind == parser.TokenKeyword {
			for _, term := range terminators {
				if t.Keyword == term {
					break Loop
				}
			}
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, stmt)
	}
	return stmts, nil
}

func (p *bodyParser) parseStmt() (Stmt, error) {
	t := p.cur()
	switch {
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwReturn:
		return p.parseReturn()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwIf:
		return p.parseIf()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwLoop:
		return p.parseLoop()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwWhile:
		return p.parseWhile()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwFor:
		return p.parseFor()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwPerform:
		return p.parsePerform()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwExit:
		return p.parseExit()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwContinue:
		return p.parseContinue()
	case t.Kind == parser.TokenIdent:
		// Stage A 4b: bare identifier at statement start is the
		// assignment shape `target := value;`. Other identifier-
		// led statements (PERFORM, label-prefixed loops) arrive
		// in later slices and surface a specific Stage-A
		// diagnostic from the assignment-parser when the `:=`
		// isn't there.
		return p.parseAssign()
	}
	return nil, p.errAtCur("unsupported PL/pgSQL statement (Stage A 4d accepts RETURN, assignment, IF, LOOP, and EXIT only)")
}

// parseLoop parses `LOOP stmts END LOOP ;`.
func (p *bodyParser) parseLoop() (*LoopStmt, error) {
	loopTok, err := p.expectKeyword(parser.KwLoop)
	if err != nil {
		return nil, err
	}
	body, err := p.parseStmtList(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwLoop); err != nil {
		return nil, p.errAtCur("expected END LOOP to close LOOP statement")
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate LOOP statement")
	}
	return &LoopStmt{pos: loopTok.Pos, Body: body}, nil
}

// parseWhile parses `WHILE cond LOOP stmts END LOOP ;`.
func (p *bodyParser) parseWhile() (*WhileStmt, error) {
	whileTok, err := p.expectKeyword(parser.KwWhile)
	if err != nil {
		return nil, err
	}
	cond, err := p.scanExprToKeyword("WHILE condition", parser.KwLoop)
	if err != nil {
		return nil, err
	}
	p.advance() // LOOP
	body, err := p.parseStmtList(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwLoop); err != nil {
		return nil, p.errAtCur("expected END LOOP to close WHILE statement")
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate WHILE statement")
	}
	return &WhileStmt{pos: whileTok.Pos, Cond: cond, Body: body}, nil
}

// parseFor parses `FOR var IN [REVERSE] lower..upper [BY step] LOOP stmts END LOOP ;`.
func (p *bodyParser) parseFor() (*ForStmt, error) {
	forTok, err := p.expectKeyword(parser.KwFor)
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != parser.TokenIdent {
		return nil, p.errAtCur("expected loop variable name")
	}
	varName := p.advance().Value
	if _, err := p.expectKeyword(parser.KwIn); err != nil {
		return nil, err
	}
	isReverse := p.acceptKeyword(parser.KwReverse)
	lower, err := p.scanExprTo("lower bound", func(t parser.Token) bool {
		return t.Kind == parser.TokenOperator && t.Value == ".."
	})
	if err != nil {
		return nil, err
	}
	p.advance() // ..
	upper, err := p.scanExprTo("upper bound", func(t parser.Token) bool {
		return t.Kind == parser.TokenKeyword && (t.Keyword == parser.KwLoop || t.Keyword == parser.KwBy)
	})
	if err != nil {
		return nil, err
	}
	var step parser.Expr
	if p.acceptKeyword(parser.KwBy) {
		step, err = p.scanExprToKeyword("BY step", parser.KwLoop)
		if err != nil {
			return nil, err
		}
	}
	p.advance() // LOOP
	body, err := p.parseStmtList(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwLoop); err != nil {
		return nil, p.errAtCur("expected END LOOP to close FOR statement")
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate FOR statement")
	}
	return &ForStmt{
		pos:     forTok.Pos,
		Var:     varName,
		Reverse: isReverse,
		Lower:   lower,
		Upper:   upper,
		Step:    step,
		Body:    body,
	}, nil
}

// parsePerform parses `PERFORM expr ;`.
func (p *bodyParser) parsePerform() (*PerformStmt, error) {
	perfTok, err := p.expectKeyword(parser.KwPerform)
	if err != nil {
		return nil, err
	}
	expr, err := p.scanExprToSemicolon("PERFORM expression")
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate PERFORM statement")
	}
	return &PerformStmt{pos: perfTok.Pos, Expr: expr}, nil
}

// parseExit parses `EXIT [ WHEN cond ] ;`.
func (p *bodyParser) parseExit() (*ExitStmt, error) {
	exitTok, err := p.expectKeyword(parser.KwExit)
	if err != nil {
		return nil, err
	}
	e := &ExitStmt{pos: exitTok.Pos}
	if p.acceptKeyword(parser.KwWhen) {
		cond, err := p.scanExprToSemicolon("EXIT WHEN condition")
		if err != nil {
			return nil, err
		}
		e.Cond = cond
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate EXIT statement")
	}
	return e, nil
}

// parseContinue parses `CONTINUE [ WHEN cond ] ;`.
func (p *bodyParser) parseContinue() (*ContinueStmt, error) {
	contTok, err := p.expectKeyword(parser.KwContinue)
	if err != nil {
		return nil, err
	}
	c := &ContinueStmt{pos: contTok.Pos}
	if p.acceptKeyword(parser.KwWhen) {
		cond, err := p.scanExprToSemicolon("CONTINUE WHEN condition")
		if err != nil {
			return nil, err
		}
		c.Cond = cond
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate CONTINUE statement")
	}
	return c, nil
}

// parseIf parses `IF cond THEN stmts [ ELSIF cond THEN stmts ]* [ ELSE stmts ] END IF ;`.
func (p *bodyParser) parseIf() (*IfStmt, error) {
	ifTok, err := p.expectKeyword(parser.KwIf)
	if err != nil {
		return nil, err
	}
	cond, err := p.scanExprToKeyword("IF condition", parser.KwThen)
	if err != nil {
		return nil, err
	}
	p.advance() // THEN
	thenStmts, err := p.parseStmtList(parser.KwElsif, parser.KwElseif, parser.KwElse, parser.KwEnd)
	if err != nil {
		return nil, err
	}
	ifStmt := &IfStmt{pos: ifTok.Pos, Cond: cond, Then: thenStmts}
	for {
		t := p.cur()
		if t.Kind != parser.TokenKeyword || (t.Keyword != parser.KwElsif && t.Keyword != parser.KwElseif) {
			break
		}
		elsifPos := p.advance().Pos
		elsifCond, err := p.scanExprToKeyword("ELSIF condition", parser.KwThen)
		if err != nil {
			return nil, err
		}
		p.advance() // THEN
		elsifThen, err := p.parseStmtList(parser.KwElsif, parser.KwElseif, parser.KwElse, parser.KwEnd)
		if err != nil {
			return nil, err
		}
		ifStmt.Elsifs = append(ifStmt.Elsifs, &Elsif{pos: elsifPos, Cond: elsifCond, Then: elsifThen})
	}
	if p.acceptKeyword(parser.KwElse) {
		elseStmts, err := p.parseStmtList(parser.KwEnd)
		if err != nil {
			return nil, err
		}
		ifStmt.Else = elseStmts
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, p.errAtCur("expected END IF to close IF statement")
	}
	if _, err := p.expectKeyword(parser.KwIf); err != nil {
		return nil, p.errAtCur("expected END IF (missing IF keyword)")
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate IF statement")
	}
	return ifStmt, nil
}

// scanExprToKeyword scans tokens up to (but not including) the
// provided terminator keyword, slices the original source, and
// feeds it through `parser.ParseExpr`.
func (p *bodyParser) scanExprToKeyword(ctx string, term parser.Keyword) (parser.Expr, error) {
	return p.scanExprTo(ctx, func(t parser.Token) bool {
		return t.Kind == parser.TokenKeyword && t.Keyword == term
	})
}

// scanExprTo scans tokens until the predicate matches, slices the
// original source, and feeds it through `parser.ParseExpr`.
func (p *bodyParser) scanExprTo(ctx string, stop func(parser.Token) bool) (parser.Expr, error) {
	exprStart := p.cur().Pos
	for p.cur().Kind != parser.TokenEOF {
		if stop(p.cur()) {
			break
		}
		p.advance()
	}
	exprEnd := p.cur().Pos
	if !stop(p.cur()) {
		return nil, p.errAtCur("unexpected end of %s", ctx)
	}
	exprSrc := strings.TrimSpace(p.src[exprStart:exprEnd])
	if exprSrc == "" {
		return nil, p.errAt(exprStart, "%s requires a non-empty expression", ctx)
	}
	expr, err := parser.ParseExpr(exprSrc)
	if err != nil {
		return nil, p.errAt(exprStart, "%s: %v", ctx, err)
	}
	return expr, nil
}

// parseDeclSection parses one-or-more declarations terminated by
// the `BEGIN` keyword. Empty DECLARE sections (the bare `DECLARE
// BEGIN END` form) are upstream-legal but useless; we accept them
// since the alternative is a special-case lookahead the future
// label-prefix parser would have to undo anyway.
func (p *bodyParser) parseDeclSection() ([]*Declaration, error) {
	var decls []*Declaration
	for {
		if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwBegin {
			return decls, nil
		}
		if p.cur().Kind == parser.TokenEOF {
			return nil, p.errAtCur("expected BEGIN after DECLARE section")
		}
		d, err := p.parseDeclaration()
		if err != nil {
			return nil, err
		}
		decls = append(decls, d)
	}
}

// parseDeclaration parses a single `varname type [ DEFAULT expr |
// := expr ] ;`. CONSTANT and NOT NULL surface Stage-A-4b
// diagnostics so handwritten PL/pgSQL using them gets a specific
// message.
func (p *bodyParser) parseDeclaration() (*Declaration, error) {
	if p.cur().Kind != parser.TokenIdent {
		return nil, p.errAtCur("expected variable name in DECLARE section")
	}
	nameTok := p.advance()
	if p.cur().Kind == parser.TokenKeyword {
		switch p.cur().Value {
		case "constant":
			return nil, p.errAtCur("CONSTANT declarations are not supported in v0 (Stage A 4b)")
		}
	}
	typeStart := p.cur().Pos
	colType, err := p.parseTypeRef()
	if err != nil {
		return nil, err
	}
	d := &Declaration{pos: nameTok.Pos, Name: nameTok.Value, Type: colType}
	if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwNot {
		return nil, p.errAt(p.cur().Pos, "NOT NULL declarations are not supported in v0 (Stage A 4b)")
	}
	// Optional initializer: `DEFAULT expr` or `:= expr`. Both end
	// at the next `;` (top-level — string-literal/dollar-quote
	// `;` already absorbed by the lexer).
	hasInit := false
	if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwDefault {
		p.advance()
		hasInit = true
	} else if p.cur().Kind == parser.TokenOperator && p.cur().Value == ":=" {
		p.advance()
		hasInit = true
	}
	_ = typeStart
	if hasInit {
		expr, err := p.scanExprToSemicolon("declaration initializer")
		if err != nil {
			return nil, err
		}
		d.Default = expr
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate declaration")
	}
	return d, nil
}

// parseAssign parses `target := value ;`. The target is a bare
// identifier (Stage A 4b scope — no record-field or array-element
// assignment yet).
func (p *bodyParser) parseAssign() (*AssignStmt, error) {
	nameTok := p.advance()
	if p.cur().Kind != parser.TokenOperator || p.cur().Value != ":=" {
		return nil, p.errAtCur("expected ':=' after %q (Stage A 4b only supports RETURN and assignment)", nameTok.Value)
	}
	p.advance() // :=
	expr, err := p.scanExprToSemicolon("assignment value")
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate assignment")
	}
	return &AssignStmt{pos: nameTok.Pos, Target: nameTok.Value, Value: expr}, nil
}

// parseTypeRef captures a SQL type spec — `name` or `schema.name`,
// optionally followed by `(N [, N ...])`. We re-use the SQL
// parser's type machinery by serialising the matched tokens into
// a string and feeding them through `parser.Parse("CREATE TABLE
// _ (_ <type>)")` — keeping a single source of truth for type
// parsing without exposing `parseColumnType` publicly.
func (p *bodyParser) parseTypeRef() (parser.ColumnType, error) {
	if p.cur().Kind != parser.TokenIdent {
		return parser.ColumnType{}, p.errAtCur("expected type name in declaration")
	}
	startPos := p.cur().Pos
	endPos := startPos
	consume := func() {
		t := p.advance()
		endPos = t.Pos + len(t.Value)
	}
	consume() // first ident
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "." {
		consume()
		if p.cur().Kind != parser.TokenIdent {
			return parser.ColumnType{}, p.errAtCur("expected type name after '.'")
		}
		consume()
	}
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "(" {
		// Walk to the matching ')' — type arg lists never nest in
		// Stage A. Capture verbatim source so the SQL type parser
		// reads the same bytes.
		depth := 1
		consume() // '('
		for depth > 0 {
			t := p.cur()
			if t.Kind == parser.TokenEOF {
				return parser.ColumnType{}, p.errAtCur("unterminated '(' in type spec")
			}
			if t.Kind == parser.TokenSymbol && t.Value == "(" {
				depth++
			} else if t.Kind == parser.TokenSymbol && t.Value == ")" {
				depth--
			}
			consume()
		}
	}
	src := p.src[startPos:endPos]
	stmts, err := parser.Parse("CREATE TABLE _t (_c " + src + ")")
	if err != nil {
		return parser.ColumnType{}, p.errAt(startPos, "type %q: %v", src, err)
	}
	if len(stmts) != 1 {
		return parser.ColumnType{}, p.errAt(startPos, "type %q: expected 1 stmt", src)
	}
	ct, ok := stmts[0].(*parser.CreateTableStmt)
	if !ok || len(ct.Columns) != 1 {
		return parser.ColumnType{}, p.errAt(startPos, "type %q: parser produced unexpected shape", src)
	}
	return ct.Columns[0].Type, nil
}

// scanExprToSemicolon scans tokens up to (but not including) the
// next top-level `;`, slices the original source over those
// bytes, and feeds the slice through `parser.ParseExpr` so the
// AST shape matches a SELECT target-list expression. Used by
// RETURN, assignment, and declaration initializers.
func (p *bodyParser) scanExprToSemicolon(ctx string) (parser.Expr, error) {
	exprStart := p.cur().Pos
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
		return nil, p.errAt(exprStart, "%s requires a non-empty expression", ctx)
	}
	for p.cur().Kind != parser.TokenEOF {
		if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
			break
		}
		p.advance()
	}
	exprEnd := p.cur().Pos
	if p.cur().Kind != parser.TokenSymbol || p.cur().Value != ";" {
		return nil, p.errAtCur("expected ';' to terminate %s", ctx)
	}
	exprSrc := strings.TrimSpace(p.src[exprStart:exprEnd])
	if exprSrc == "" {
		return nil, p.errAt(exprStart, "%s requires a non-empty expression", ctx)
	}
	expr, err := parser.ParseExpr(exprSrc)
	if err != nil {
		return nil, p.errAt(exprStart, "%s: %v", ctx, err)
	}
	return expr, nil
}

// parseReturn parses `RETURN expr;`. Expression capture goes
// through scanExprToSemicolon — same path as assignment and
// declaration initializers — so the AST shape matches a SELECT
// target-list expression and the future interpreter can reuse the
// SQL evaluator without translation.
func (p *bodyParser) parseReturn() (*ReturnStmt, error) {
	retTok, err := p.expectKeyword(parser.KwReturn)
	if err != nil {
		return nil, err
	}
	expr, err := p.scanExprToSemicolon("RETURN expression")
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate RETURN")
	}
	return &ReturnStmt{pos: retTok.Pos, Expr: expr}, nil
}
