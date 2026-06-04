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
	// parseStmtList terminates at END or at "exception" identifier.
	stmts, excBlock, err := p.parseStmtListWithException(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	if excBlock != nil {
		block.Statements = append(stmts, excBlock)
	} else {
		block.Statements = stmts
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

// parseStmtListWithException parses statements until END or EXCEPTION.
// If EXCEPTION is found, parses the handler blocks and returns them
// as an *ExceptionBlock alongside the main statements. M0097-0012.
func (p *bodyParser) parseStmtListWithException(terminators ...parser.Keyword) ([]Stmt, *ExceptionBlock, error) {
	var stmts []Stmt
Loop:
	for {
		t := p.cur()
		if t.Kind == parser.TokenEOF {
			return nil, nil, p.errAtCur("unexpected EOF (expected END)")
		}
		if t.Kind == parser.TokenKeyword {
			for _, term := range terminators {
				if t.Keyword == term {
					break Loop
				}
			}
		}
		// Check for EXCEPTION (identifier).
		if t.Kind == parser.TokenIdent && strings.EqualFold(t.Value, "exception") {
			excBlock, err := p.parseExceptionBlock()
			if err != nil {
				return stmts, nil, err
			}
			return stmts, excBlock, nil
		}
		stmt, err := p.parseStmt()
		if err != nil {
			return nil, nil, err
		}
		stmts = append(stmts, stmt)
	}
	return stmts, nil, nil
}

// parseExceptionBlock parses EXCEPTION WHEN ... THEN ... (END handled by caller).
// M0097-0012.
func (p *bodyParser) parseExceptionBlock() (*ExceptionBlock, error) {
	pos := p.cur().Pos
	p.advance() // consume "exception"
	excBlk := &ExceptionBlock{pos: pos}
	for {
		t := p.cur()
		// Stop at END (the caller will consume it).
		if t.Kind == parser.TokenKeyword && t.Keyword == parser.KwEnd {
			break
		}
		if t.Kind == parser.TokenEOF {
			break
		}
		// Parse WHEN conditions THEN stmts.
		if !(t.Kind == parser.TokenIdent && strings.EqualFold(t.Value, "when")) &&
			!(t.Kind == parser.TokenKeyword && t.Keyword == parser.KwWhen) {
			break
		}
		p.advance() // consume WHEN
		// Collect condition names until THEN.
		var conditions []string
		for {
			ct := p.cur()
			if ct.Kind == parser.TokenKeyword && ct.Keyword == parser.KwThen {
				p.advance()
				break
			}
			if ct.Kind == parser.TokenEOF {
				break
			}
			if ct.Kind == parser.TokenIdent || ct.Kind == parser.TokenKeyword {
				if !strings.EqualFold(ct.Value, "or") {
					conditions = append(conditions, ct.Value)
				}
			}
			p.advance()
		}
		// Parse handler body until next WHEN or END.
		handlerStmts, _, err := p.parseStmtListWithException(parser.KwEnd)
		if err != nil {
			return nil, err
		}
		// Check if stopped at WHEN (not END).
		if ht := p.cur(); ht.Kind == parser.TokenKeyword && ht.Keyword == parser.KwEnd {
			excBlk.Handlers = append(excBlk.Handlers, &ExceptionHandler{
				Conditions: conditions, Body: handlerStmts,
			})
			break
		}
		excBlk.Handlers = append(excBlk.Handlers, &ExceptionHandler{
			Conditions: conditions, Body: handlerStmts,
		})
	}
	return excBlk, nil
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
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwBegin:
		return p.parseNestedBlock()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwPerform:
		return p.parsePerform()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwExit:
		return p.parseExit()
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwContinue:
		return p.parseContinue()
	// SQL DML/DDL statements embedded in PL/pgSQL body. M0096-0012.
	case t.Kind == parser.TokenKeyword && (t.Keyword == parser.KwInsert ||
		t.Keyword == parser.KwUpdate || t.Keyword == parser.KwDelete ||
		t.Keyword == parser.KwSelect ||
		t.Keyword == parser.KwCreate ||
		t.Keyword == parser.KwDrop ||
		t.Keyword == parser.KwAlter):
		return p.parseSQLStmt()
	// EXECUTE expr [INTO var] [USING expr, ...]. M0100-0005.
	case t.Kind == parser.TokenKeyword && t.Keyword == parser.KwExecute:
		return p.parseExecute()
	// RAISE [NOTICE|WARNING|ERROR|EXCEPTION] 'msg'. M0096-0012.
	case t.Kind == parser.TokenIdent && strings.EqualFold(t.Value, "raise"):
		return p.parseRaise()
	case t.Kind == parser.TokenIdent:
		// Stage A 4b: bare identifier at statement start.
		// Handle: ident := value (assignment)
		// Handle: ident.field = value (record field, treated as no-op expr for trigger OLD)
		// Handle: ident.field.* (expansion — treated as SQL stmt)
		return p.parseAssign()
	}
	return nil, p.errAtCur("unsupported PL/pgSQL statement")
}

// parseNestedBlock parses a nested `BEGIN [stmts] [EXCEPTION ...] END ;`
// sub-block statement. Returns a *Block. M0097-0003.
func (p *bodyParser) parseNestedBlock() (*Block, error) {
	beginPos := p.advance().Pos // consume BEGIN
	stmts, excBlock, err := p.parseStmtListWithException(parser.KwEnd)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(parser.KwEnd); err != nil {
		return nil, p.errAtCur("expected END to close nested BEGIN block")
	}
	p.acceptSymbol(";")
	block := &Block{pos: beginPos, Statements: stmts}
	if excBlock != nil {
		block.Statements = append(stmts, excBlock)
	}
	return block, nil
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

// parseFor parses FOR loops: integer-range (`FOR var IN low..high`)
// or query-based (`FOR rec IN SELECT ...`). M0097-0012 extended.
func (p *bodyParser) parseFor() (Stmt, error) {
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

	// Determine if this is a query-based FOR (starts with SELECT, EXECUTE,
	// or a subquery parenthesis) or an integer-range FOR (contains ..).
	// Peek ahead: if the first real token is SELECT/EXECUTE/( it's a query FOR.
	isQueryFor := false
	if !isReverse {
		t := p.cur()
		if t.Kind == parser.TokenKeyword && (t.Keyword == parser.KwSelect ||
			t.Keyword == parser.KwInsert || t.Keyword == parser.KwUpdate ||
			t.Keyword == parser.KwDelete || t.Keyword == parser.KwWith ||
			t.Keyword == parser.KwExecute) {
			isQueryFor = true
		} else if t.Kind == parser.TokenSymbol && t.Value == "(" {
			isQueryFor = true
		}
	}

	if isQueryFor {
		// FOR rec IN query LOOP stmts END LOOP;  M0097-0012.
		sqlStart := p.cur().Pos
		depth := 0
		for p.cur().Kind != parser.TokenEOF {
			t := p.cur()
			if t.Kind == parser.TokenSymbol && t.Value == "(" {
				depth++
			} else if t.Kind == parser.TokenSymbol && t.Value == ")" {
				depth--
			} else if t.Kind == parser.TokenKeyword && t.Keyword == parser.KwLoop && depth == 0 {
				break
			}
			p.advance()
		}
		sqlEnd := p.cur().Pos
		sqlText := strings.TrimSpace(p.src[sqlStart:sqlEnd])
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
		return &ForSelectStmt{
			pos:  forTok.Pos,
			Var:  varName,
			SQL:  sqlText,
			Body: body,
		}, nil
	}

	// Integer-range FOR: FOR var IN [REVERSE] lower..upper [BY step] LOOP.
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

// parseAssign parses `target := value;` or `target = value;`.
// Also handles `ident.field = expr;` — for `NEW.field` this is a real
// assignment to the trigger's new-row column (BEFORE triggers in PG can
// rewrite NEW.*); for `OLD.field` it is a no-op (OLD is immutable).
// M0096-0012; NEW-write path added M0100-0005p.
func (p *bodyParser) parseAssign() (Stmt, error) {
	nameTok := p.advance()
	// Handle dotted target: `ident.field ...`.
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "." {
		return p.parseDottedExprStmt(nameTok)
	}
	// Handle array subscript assignment: `x[idx] := value;`.
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "[" {
		return p.parseArraySubscriptAssign(nameTok)
	}
	isAssign := false
	if p.cur().Kind == parser.TokenOperator && p.cur().Value == ":=" {
		isAssign = true
	} else if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "=" {
		isAssign = true
	}
	if !isAssign {
		return nil, p.errAtCur("expected ':=' or '=' after %q", nameTok.Value)
	}
	p.advance() // := or =
	expr, err := p.scanExprToSemicolon("assignment value")
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate assignment")
	}
	return &AssignStmt{pos: nameTok.Pos, Target: nameTok.Value, Value: expr}, nil
}

// parseDottedExprStmt parses `ident.field [= expr] ;`.
// For `NEW.field := expr` in trigger context this produces a real
// assignment that targets the injected `_new_<field>` frame variable
// (BEFORE triggers in PG can rewrite NEW.*). For `OLD.field = ...` and
// any other prefix it is a no-op (OLD is immutable; record types are not
// supported in v0). M0096-0012; NEW-write path added M0100-0005p.
func (p *bodyParser) parseDottedExprStmt(nameTok parser.Token) (*AssignStmt, error) {
	pos := nameTok.Pos
	prefix := strings.ToLower(nameTok.Value)
	// Consume the '.' then the field identifier.
	p.advance() // '.'
	if p.cur().Kind != parser.TokenIdent {
		// Malformed — consume to ';' and return a no-op so the rest of
		// the body parses.
		for p.cur().Kind != parser.TokenEOF {
			if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
				p.advance()
				break
			}
			p.advance()
		}
		return &AssignStmt{pos: pos, Target: "_plpgsql_noop", Value: &parser.IntegerConst{}}, nil
	}
	field := p.advance().Value
	// `NEW.<field> := <expr>` / `OLD.<field> := <expr>` (or `=` form) —
	// capture the RHS expression so the assignment actually fires at
	// runtime. NEW.<col> mutation feeds INSERT/UPDATE row routing (see
	// M0100-0005p); OLD.<col> mutation feeds BEFORE DELETE trigger bodies
	// that subsequently read OLD.* in embedded SQL (M0100-0005aa,
	// partition-key-update-4.spec).  Both `:=` and `=` are tokenised as
	// TokenOperator by the SQL lexer (see internal/parser/lexer.go).
	isAssign := false
	if p.cur().Kind == parser.TokenOperator && (p.cur().Value == ":=" || p.cur().Value == "=") {
		isAssign = true
	}
	if isAssign && (prefix == "new" || prefix == "old") {
		p.advance() // := or =
		expr, err := p.scanExprToSemicolon("assignment value")
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(";") {
			return nil, p.errAtCur("expected ';' to terminate assignment")
		}
		return &AssignStmt{pos: pos, Target: "_" + prefix + "_" + strings.ToLower(field), Value: expr}, nil
	}
	// Composite type field assignment: varName.fieldName := expr
	// Emit as AssignStmt with target "varname\x00fieldname" so the runtime
	// can distinguish it from a plain variable assignment. M0097-composite.
	if isAssign && prefix != "new" && prefix != "old" {
		p.advance() // consume ':=' or '='
		expr, err := p.scanExprToSemicolon("assignment value")
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(";") {
			return nil, p.errAtCur("expected ';' to terminate assignment")
		}
		return &AssignStmt{pos: pos, Target: prefix + "\x00" + strings.ToLower(field), Value: expr}, nil
	}
	// Unrelated dotted refs / non-assign expressions: swallow to ';' and
	// emit the noop sentinel — preserves the M0096-0012 behaviour.
	for p.cur().Kind != parser.TokenEOF {
		if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
			p.advance()
			break
		}
		p.advance()
	}
	return &AssignStmt{pos: pos, Target: "_plpgsql_noop", Value: &parser.IntegerConst{}}, nil
}

// parseArraySubscriptAssign parses `x[subscript] := value;` in PL/pgSQL.
func (p *bodyParser) parseArraySubscriptAssign(nameTok parser.Token) (*ArraySubscriptAssignStmt, error) {
	pos := nameTok.Pos
	p.advance() // consume '['
	subscriptToks, err := p.scanToMatchingBracket()
	if err != nil {
		return nil, err
	}
	p.advance() // consume ']'
	isAssign := false
	if p.cur().Kind == parser.TokenOperator && p.cur().Value == ":=" {
		isAssign = true
	} else if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "=" {
		isAssign = true
	}
	if !isAssign {
		return nil, p.errAtCur("expected ':=' or '=' after array subscript")
	}
	p.advance() // consume ':=' or '='
	valueExpr, err := p.scanExprToSemicolon("array subscript assignment value")
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(";") {
		return nil, p.errAtCur("expected ';' to terminate array subscript assignment")
	}
	subscriptExpr, perr := parseExprFromTokens(subscriptToks)
	if perr != nil {
		return nil, p.errAt(pos, "invalid array subscript: %v", perr)
	}
	return &ArraySubscriptAssignStmt{
		pos:       pos,
		VarName:   strings.ToLower(nameTok.Value),
		Subscript: subscriptExpr,
		Value:     valueExpr,
	}, nil
}

// scanToMatchingBracket scans tokens until the matching ']' (handling nesting).
// Returns tokens between '[' and ']' exclusive; caller has already consumed '['.
func (p *bodyParser) scanToMatchingBracket() ([]parser.Token, error) {
	var toks []parser.Token
	depth := 1
	for {
		t := p.cur()
		if t.Kind == parser.TokenEOF {
			return nil, p.errAtCur("unexpected EOF inside array subscript")
		}
		if t.Kind == parser.TokenSymbol {
			if t.Value == "[" {
				depth++
			} else if t.Value == "]" {
				depth--
				if depth == 0 {
					return toks, nil
				}
			}
		}
		toks = append(toks, t)
		p.advance()
	}
}

// parseExprFromTokens parses a single SQL expression from a token slice.
// Reconstructs the source text from tokens and delegates to parser.ParseExpr.
func parseExprFromTokens(toks []parser.Token) (parser.Expr, error) {
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty subscript")
	}
	// Reconstruct the source from token values and parse as a SQL expression.
	src := ""
	for _, t := range toks {
		src += t.Value + " "
	}
	src = strings.TrimSpace(src)
	return parser.ParseExpr(src)
}

// parseSQLStmt captures an embedded SQL statement (INSERT / UPDATE / DELETE /
// SELECT) up to the trailing `;` and stores it verbatim. M0096-0012.
func (p *bodyParser) parseSQLStmt() (*SQLStmt, error) {
	startPos := p.cur().Pos
	depth := 0
	for p.cur().Kind != parser.TokenEOF {
		t := p.cur()
		if t.Kind == parser.TokenSymbol && t.Value == "(" {
			depth++
		} else if t.Kind == parser.TokenSymbol && t.Value == ")" {
			depth--
		} else if t.Kind == parser.TokenSymbol && t.Value == ";" && depth == 0 {
			endPos := t.Pos
			p.advance() // consume `;`
			sql := strings.TrimSpace(p.src[startPos:endPos])
			return &SQLStmt{pos: startPos, SQL: sql}, nil
		}
		p.advance()
	}
	return nil, p.errAtCur("unterminated embedded SQL statement")
}

// parseExecute parses `EXECUTE expr [INTO varname] [USING expr, ...] ;`.
// M0100-0005: PL/pgSQL dynamic SQL.
func (p *bodyParser) parseExecute() (*ExecuteStmt, error) {
	pos := p.cur().Pos
	p.advance() // consume EXECUTE keyword

	// Parse the SQL expression (everything up to INTO, USING, or ;)
	queryExpr, err := p.scanExprTo("EXECUTE query", func(t parser.Token) bool {
		if t.Kind == parser.TokenSymbol && t.Value == ";" {
			return true
		}
		if t.Kind == parser.TokenKeyword && (t.Keyword == parser.KwInto || t.Keyword == parser.KwUsing) {
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}

	stmt := &ExecuteStmt{pos: pos, Query: queryExpr}

	// Optional INTO clause
	if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwInto {
		p.advance() // consume INTO
		varTok := p.advance()
		if varTok.Kind != parser.TokenIdent {
			return nil, p.errAt(varTok.Pos, "expected variable name after INTO")
		}
		stmt.IntoVar = varTok.Value
	}

	// Optional USING clause
	if p.cur().Kind == parser.TokenKeyword && p.cur().Keyword == parser.KwUsing {
		p.advance() // consume USING
		for {
			argExpr, err := p.scanExprTo("USING argument", func(t parser.Token) bool {
				if t.Kind == parser.TokenSymbol && (t.Value == "," || t.Value == ";") {
					return true
				}
				return false
			})
			if err != nil {
				return nil, err
			}
			stmt.Using = append(stmt.Using, argExpr)
			if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "," {
				p.advance() // consume ","
				continue
			}
			break
		}
	}

	// Consume the trailing semicolon
	if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
		p.advance()
	}
	return stmt, nil
}

// parseRaise parses `RAISE [level] 'message' [, args...];`. M0096-0012.
// Level defaults to "exception" when omitted.
func (p *bodyParser) parseRaise() (*RaiseStmt, error) {
	pos := p.cur().Pos
	p.advance() // consume "raise" ident
	level := "exception"
	conditionName := ""
	// Optional level keyword or condition name.
	if p.cur().Kind == parser.TokenIdent {
		val := strings.ToLower(p.cur().Value)
		isLevel := false
		for _, lvl := range []string{"notice", "warning", "info", "log", "debug", "error", "exception"} {
			if val == lvl {
				isLevel = true
				level = val
				p.advance()
				break
			}
		}
		if !isLevel && val != "" {
			// Condition name: RAISE condition_name [USING MESSAGE = 'text']
			conditionName = val
			p.advance()
		}
	}
	// Consume everything to `;` (captures format string + USING clause).
	msgStart := p.cur().Pos
	for p.cur().Kind != parser.TokenEOF {
		if p.cur().Kind == parser.TokenSymbol && p.cur().Value == ";" {
			msg := strings.TrimSpace(p.src[msgStart:p.cur().Pos])
			p.advance()
			// If USING MESSAGE = 'text', extract the message.
			if conditionName != "" {
				msg = extractRaiseUsingMessage(msg)
			}
			return &RaiseStmt{pos: pos, Level: level, Msg: msg, ConditionName: conditionName}, nil
		}
		p.advance()
	}
	return nil, p.errAtCur("unterminated RAISE statement")
}

// extractRaiseUsingMessage extracts the message text from `USING MESSAGE = 'text'`
// or similar USING clauses in RAISE statements. M0097-0003.
func extractRaiseUsingMessage(s string) string {
	s = strings.TrimSpace(s)
	// Pattern: USING MESSAGE = 'text'
	lower := strings.ToLower(s)
	if idx := strings.Index(lower, "using"); idx >= 0 {
		rest := strings.TrimSpace(s[idx+5:])
		lowerRest := strings.ToLower(rest)
		if strings.HasPrefix(lowerRest, "message") {
			rest = strings.TrimSpace(rest[7:])
			if strings.HasPrefix(rest, "=") {
				rest = strings.TrimSpace(rest[1:])
				if len(rest) >= 2 && rest[0] == '\'' {
					// Strip surrounding single quotes.
					end := strings.LastIndexByte(rest, '\'')
					if end > 0 {
						return strings.ReplaceAll(rest[1:end], "''", "'")
					}
				}
				return rest
			}
		}
	}
	return s
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
	// Handle `varname%TYPE` — consume and use text as stand-in type.
	// % is tokenized as TokenOperator by the SQL lexer.
	if p.cur().Kind == parser.TokenOperator && p.cur().Value == "%" {
		p.advance() // consume '%'
		if p.cur().Kind == parser.TokenIdent && strings.EqualFold(p.cur().Value, "type") {
			p.advance() // consume 'TYPE'
		}
		return parser.ColumnType{Name: "text"}, nil
	}
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
	// Save endPos before consuming array suffix — the SQL parser doesn't
	// support `text[]` notation, so we parse the base type only. M0097-0003.
	baseEndPos := endPos
	// Array suffix: text[] — consume '[]' pairs from token stream but exclude
	// from the source fed to the SQL type parser.
	for p.cur().Kind == parser.TokenSymbol && p.cur().Value == "[" {
		p.advance() // '['
		if p.cur().Kind == parser.TokenSymbol && p.cur().Value == "]" {
			p.advance() // ']'
		}
	}
	src := p.src[startPos:baseEndPos]
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

// parseReturn parses `RETURN [NEXT] expr;`. RETURN NEXT emits one row
// from a SETOF function and continues; plain RETURN exits the function.
func (p *bodyParser) parseReturn() (Stmt, error) {
	retTok, err := p.expectKeyword(parser.KwReturn)
	if err != nil {
		return nil, err
	}
	// RETURN NEXT expr; — PL/pgSQL SETOF row emitter. M0097-0073.
	if p.cur().Kind == parser.TokenIdent && strings.EqualFold(p.cur().Value, "next") {
		p.advance() // consume NEXT
		expr, err := p.scanExprToSemicolon("RETURN NEXT expression")
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(";") {
			return nil, p.errAtCur("expected ';' to terminate RETURN NEXT")
		}
		return &ReturnNextStmt{pos: retTok.Pos, Expr: expr}, nil
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
