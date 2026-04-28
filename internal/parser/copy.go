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

	// Source/target. Either `( select … )` or `relname [(cols)]`.
	if p.acceptSymbol("(") {
		// Query form is only valid for COPY (query) TO …
		sel, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		s, ok := sel.(*SelectStmt)
		if !ok {
			return nil, &SyntaxError{Pos: t.Pos, Message: "COPY (...) only supports SELECT"}
		}
		stmt.Query = s
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

	// Direction.
	switch {
	case p.acceptKeyword(KwFrom):
		stmt.Direction = CopyFrom
	case p.acceptKeyword(KwTo):
		stmt.Direction = CopyTo
	default:
		return nil, p.errAtCur("expected FROM or TO in COPY")
	}
	if stmt.Query != nil && stmt.Direction != CopyTo {
		return nil, &SyntaxError{Pos: t.Pos, Message: "COPY (query) is only valid with TO"}
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
