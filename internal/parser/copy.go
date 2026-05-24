package parser

// parseCopy parses
//
//	COPY (table [(cols)] | (query))
//	     {FROM|TO} {'filename' | PROGRAM 'cmd' | STDIN | STDOUT}
//	     [ WITH ] [ ( option [, …] ) ]
//
// STDIN/STDOUT/PROGRAM are matched by lower-cased identifier text
// rather than as reserved keywords — that mirrors upstream's "ColLabel"
// classification and keeps the keyword set small. Option names follow
// the same rule. Recognised options (the analyzer is the gate of
// truth) are FORMAT, FREEZE, DELIMITER, NULL, HEADER, QUOTE, ESCAPE,
// FORCE_QUOTE, FORCE_NOT_NULL, FORCE_NULL, ENCODING.
func (p *parser) parseCopy() (Stmt, error) {
	t, err := p.expectKeyword(KwCopy)
	if err != nil {
		return nil, err
	}
	stmt := &CopyStmt{pos: t.Pos}

	// Source/target. Either `( query )` or `relname [(cols)]`.
	// The query form is only valid for COPY (query) TO …; PostgreSQL
	// accepts SELECT/VALUES as well as INSERT/UPDATE/DELETE bodies that
	// carry a RETURNING clause (checked by the planner).
	if p.acceptSymbol("(") {
		inner, err := p.parseCopyInnerQuery()
		if err != nil {
			return nil, err
		}
		// PostgreSQL's grammar accepts the deprecated `SELECT … INTO …`
		// form inside COPY (...), but DoCopy rejects it with a
		// feature-not-supported error. goopg's parseSelect has no SELECT
		// INTO support and stops at the reserved INTO keyword, leaving
		// `INTO <target> FROM …` unconsumed here. Flag it and skip the
		// rest of the inner query (up to the matching ')') so planCopy
		// can emit the PG-compatible message rather than a stray
		// "expected ')'" syntax error. M0097-0024.
		if _, isSelect := inner.(*SelectStmt); isSelect &&
			p.cur().Kind == TokenKeyword && p.cur().Keyword == KwInto {
			stmt.SelectInto = true
			if err := p.skipInnerQueryRemainder(); err != nil {
				return nil, err
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		switch q := inner.(type) {
		case *SelectStmt:
			stmt.Query = q
		case *InsertStmt, *UpdateStmt, *DeleteStmt:
			stmt.QueryDML = inner
		default:
			return nil, &SyntaxError{Pos: t.Pos, Message: "COPY (...) only supports SELECT, INSERT, UPDATE, or DELETE"}
		}
	} else {
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt.Table = name
		if p.acceptSymbol("(") {
			cols, err := p.parseColumnNameList()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
			stmt.Columns = cols
		}
	}

	// Direction. The parenthesised-query form (`COPY (query) TO …`) is
	// TO-only in PostgreSQL's grammar (gram.y has no FROM nor a
	// column-list production for it), so a trailing FROM or `(col…)`
	// must surface as a syntax error anchored at the offending token —
	// e.g. `copy (select …) from stdin` → `syntax error at or near
	// "from"`, and `copy (select …) (t,id) to stdout` → `… near "("`.
	// M0097-0024.
	if stmt.Query != nil || stmt.QueryDML != nil {
		if !p.acceptKeyword(KwTo) {
			return nil, p.errSyntaxAtCur()
		}
		stmt.Direction = CopyTo
	} else {
		switch {
		case p.acceptKeyword(KwFrom):
			stmt.Direction = CopyFrom
		case p.acceptKeyword(KwTo):
			stmt.Direction = CopyTo
		default:
			return nil, p.errAtCur("expected FROM or TO in COPY")
		}
	}

	// Endpoint: STDIN / STDOUT / PROGRAM 'cmd' / 'file'.
	endpoint, filename, err := p.parseCopyEndpoint(stmt.Direction)
	if err != nil {
		return nil, err
	}
	stmt.Endpoint = endpoint
	stmt.Filename = filename

	// Optional [ WITH ] ( option [, …] ).
	withConsumed := p.acceptKeyword(KwWith)
	if p.acceptSymbol("(") {
		opts, err := p.parseCopyOptionList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		stmt.Options = opts
	} else if withConsumed {
		// Bare `WITH` accepts the legacy syntax `WITH BINARY` /
		// `WITH OIDS` etc. — accept the legacy single-word options
		// (BINARY, CSV, HEADER) for pgbench-friendliness. Anything more
		// elaborate must use the parenthesised form.
		opts, err := p.parseCopyLegacyTrail()
		if err != nil {
			return nil, err
		}
		stmt.Options = opts
	}

	return stmt, nil
}

// parseCopyInnerQuery parses the statement inside COPY ( … ) TO. It
// dispatches on the leading keyword: SELECT/VALUES/TABLE/WITH parse as
// a query producing a *SelectStmt; INSERT/UPDATE/DELETE parse as the
// data-modifying form (PostgreSQL requires a RETURNING clause, which
// the planner enforces). Anything else is rejected by the caller.
func (p *parser) parseCopyInnerQuery() (Stmt, error) {
	t := p.cur()
	if t.Kind == TokenKeyword {
		switch t.Keyword {
		case KwInsert:
			return p.parseInsert()
		case KwUpdate:
			return p.parseUpdate()
		case KwDelete:
			return p.parseDelete()
		case KwWith:
			return p.parseStatementWithCTE()
		}
	}
	return p.parseSelect()
}

// skipInnerQueryRemainder consumes the unparsed tail of a COPY (...) inner
// query up to — but not including — the matching close parenthesis, so the
// caller's `)` check still fires. It is used only for the rejected
// `SELECT … INTO …` form, whose `INTO <target> FROM …` tail goopg's
// parseSelect does not understand; the statement is rejected later, so the
// tail's exact shape is irrelevant. Parenthesis depth is tracked to skip
// over nested subqueries / function calls. M0097-0024.
func (p *parser) skipInnerQueryRemainder() error {
	depth := 0
	for {
		t := p.cur()
		if t.Kind == TokenEOF {
			return p.errAtCur("expected ')'")
		}
		if t.Kind == TokenSymbol {
			switch t.Value {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					return nil
				}
				depth--
			}
		}
		p.advance()
	}
}

// parseCopyEndpoint reads the FROM/TO target. STDIN/STDOUT/PROGRAM are
// matched by lower-cased identifier text.
func (p *parser) parseCopyEndpoint(dir CopyDirection) (CopyEndpoint, string, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenKeyword:
		switch t.Value {
		case "stdin":
			if dir != CopyFrom {
				return 0, "", &SyntaxError{Pos: t.Pos, Message: "STDIN is only valid with COPY FROM"}
			}
			p.advance()
			return CopyEndpointStdin, "", nil
		case "stdout":
			if dir != CopyTo {
				return 0, "", &SyntaxError{Pos: t.Pos, Message: "STDOUT is only valid with COPY TO"}
			}
			p.advance()
			return CopyEndpointStdout, "", nil
		case "program":
			p.advance()
			s := p.cur()
			if s.Kind != TokenStringLit {
				return 0, "", p.errAtCur("expected string after PROGRAM")
			}
			p.advance()
			return CopyEndpointProgram, s.Value, nil
		}
	case TokenStringLit:
		p.advance()
		return CopyEndpointFile, t.Value, nil
	}
	return 0, "", p.errAtCur("expected STDIN, STDOUT, PROGRAM, or filename")
}

// parseCopyOptionList parses one or more comma-separated COPY options
// inside the WITH (…) parentheses.
func (p *parser) parseCopyOptionList() ([]CopyOption, error) {
	var out []CopyOption
	first, err := p.parseCopyOption()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		opt, err := p.parseCopyOption()
		if err != nil {
			return nil, err
		}
		out = append(out, opt)
	}
	return out, nil
}

// parseCopyOption parses a single option entry. Forms accepted:
//
//	IDENT                          → bare flag (Bool=true)
//	IDENT value                    → Value form (string/identifier/integer literal)
//	IDENT '*' or IDENT (col, …)    → FORCE_QUOTE-style with column list
func (p *parser) parseCopyOption() (CopyOption, error) {
	t := p.cur()
	if t.Kind != TokenIdent && t.Kind != TokenKeyword {
		return CopyOption{}, p.errAtCur("expected option name")
	}
	p.advance()
	opt := CopyOption{pos: t.Pos, Name: t.Value, Bool: true}

	// Column-list form for FORCE_QUOTE/FORCE_NOT_NULL/FORCE_NULL.
	if p.acceptSymbol("*") {
		opt.Star = true
		opt.Bool = false
		return opt, nil
	}
	if p.acceptSymbol("(") {
		cols, err := p.parseColumnNameList()
		if err != nil {
			return CopyOption{}, err
		}
		if !p.acceptSymbol(")") {
			return CopyOption{}, p.errAtCur("expected ')'")
		}
		opt.Cols = cols
		opt.Bool = false
		return opt, nil
	}

	// Possible value: string literal, integer literal, identifier,
	// keyword used as label.
	v := p.cur()
	switch v.Kind {
	case TokenStringLit, TokenIntLit:
		p.advance()
		opt.Value = v.Value
		opt.Bool = false
	case TokenIdent, TokenQuotedIdent, TokenKeyword:
		// Don't eagerly consume a comma/closing paren: only treat the
		// next token as a value when it isn't a separator.
		p.advance()
		opt.Value = v.Value
		opt.Bool = false
	}
	return opt, nil
}

// parseCopyLegacyTrail covers the historical, parenthesis-free syntax
// `COPY … WITH [BINARY] [HEADER]` etc. We accept BINARY, CSV, HEADER,
// FREEZE, DELIMITER 'd', NULL 'n', QUOTE 'q', ESCAPE 'e' here so
// pgbench-friendly tools survive.
func (p *parser) parseCopyLegacyTrail() ([]CopyOption, error) {
	var out []CopyOption
	for {
		t := p.cur()
		if t.Kind != TokenIdent && t.Kind != TokenKeyword {
			break
		}
		switch t.Value {
		case "binary", "csv", "header", "freeze":
			p.advance()
			out = append(out, CopyOption{pos: t.Pos, Name: t.Value, Bool: true})
		case "delimiter", "null", "quote", "escape", "encoding":
			p.advance()
			s := p.cur()
			if s.Kind != TokenStringLit {
				return nil, p.errAtCur("expected string literal")
			}
			p.advance()
			out = append(out, CopyOption{pos: t.Pos, Name: t.Value, Value: s.Value})
		default:
			return out, nil
		}
	}
	return out, nil
}
