package parser

import (
	"strconv"
	"strings"
)

// parseCreate dispatches on the next keyword after CREATE.
func (p *parser) parseCreate() (Stmt, error) {
	t, err := p.expectKeyword(KwCreate)
	if err != nil {
		return nil, err
	}
	unlogged := p.acceptKeyword(KwUnlogged)
	// `OR REPLACE` is recognised here so that `CREATE OR REPLACE VIEW`
	// dispatches into parseCreateViewTail with the flag set. Other
	// CREATE-something forms reject OR REPLACE.
	orReplace := false
	if p.acceptKeyword(KwOr) {
		if _, err := p.expectKeyword(KwReplace); err != nil {
			return nil, err
		}
		orReplace = true
	}
	// CREATE [GLOBAL|LOCAL] TEMP[ORARY] <object> → dispatch on the object
	// kind that follows TEMP/TEMPORARY. VIEW, MATERIALIZED VIEW and SEQUENCE
	// must NOT fall through to the table parser, otherwise `CREATE TEMP VIEW`
	// is silently mis-parsed as a table (M0097-0036). The TABLE/default arm
	// preserves the shadow-on-conflict Temporary flag wired in M0097-0003.
	// GLOBAL is an unreserved ident; LOCAL is a reserved keyword token.
	_ = p.acceptIdentKeyword("global") || p.acceptIdentKeyword("local") || p.acceptKeyword(KwLocal)
	if p.acceptIdentKeyword("temp") || p.acceptIdentKeyword("temporary") {
		switch {
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwView:
			p.advance()
			s, err := p.parseCreateViewTail(t.Pos, orReplace)
			if err != nil {
				return nil, err
			}
			if cv, ok := s.(*CreateViewStmt); ok {
				cv.Temporary = true
			}
			return s, nil
		case p.acceptIdentKeyword("materialized"):
			_ = p.acceptKeyword(KwView) || p.acceptIdentKeyword("view")
			return p.parseCreateMatViewTail(t.Pos)
		case p.acceptIdentKeyword("sequence"):
			return p.parseCreateSequenceTail(t.Pos, true)
		default:
			// Consume optional TABLE keyword that follows TEMP/TEMPORARY.
			p.acceptKeyword(KwTable)
			s, err := p.parseCreateTableTail(t.Pos, unlogged)
			if err != nil {
				return nil, err
			}
			if ct, ok := s.(*CreateTableStmt); ok {
				ct.Temporary = true
			}
			return s, nil
		}
	}
	// CREATE RECURSIVE VIEW name(cols) AS query (M0097-0085).
	// Equivalent to: CREATE VIEW name AS (WITH RECURSIVE name(cols) AS (query) SELECT * FROM name).
	if p.acceptKeyword(KwRecursive) {
		if _, err := p.expectKeyword(KwView); err != nil {
			return nil, err
		}
		return p.parseCreateRecursiveViewTail(t.Pos, orReplace)
	}
	switch {
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTable:
		if orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "OR REPLACE is not valid for CREATE TABLE"}
		}
		p.advance()
		return p.parseCreateTableTail(t.Pos, unlogged)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwView:
		if unlogged {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED is not valid for CREATE VIEW"}
		}
		p.advance()
		return p.parseCreateViewTail(t.Pos, orReplace)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
		// CREATE UNIQUE INDEX …
		if unlogged {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED is not valid for CREATE INDEX"}
		}
		if orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "OR REPLACE is not valid for CREATE INDEX"}
		}
		p.advance()
		if _, err := p.expectKeyword(KwIndex); err != nil {
			return nil, err
		}
		return p.parseCreateIndexTail(t.Pos, true)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwIndex:
		if unlogged {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED is not valid for CREATE INDEX"}
		}
		if orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "OR REPLACE is not valid for CREATE INDEX"}
		}
		p.advance()
		return p.parseCreateIndexTail(t.Pos, false)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPublication:
		if unlogged || orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED / OR REPLACE not valid for CREATE PUBLICATION"}
		}
		p.advance()
		return p.parseCreatePublicationTail(t.Pos)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSubscription:
		if unlogged || orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED / OR REPLACE not valid for CREATE SUBSCRIPTION"}
		}
		p.advance()
		return p.parseCreateSubscriptionTail(t.Pos)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFunction:
		if unlogged {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED is not valid for CREATE FUNCTION"}
		}
		p.advance()
		return p.parseCreateFunctionTail(t.Pos, orReplace)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwProcedure:
		if unlogged {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED is not valid for CREATE PROCEDURE"}
		}
		p.advance()
		return p.parseCreateProcedureTail(t.Pos, orReplace)
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTrigger:
		p.advance()
		return p.parseCreateTriggerTail(t.Pos)
	// CREATE SEQUENCE [IF NOT EXISTS] name [options…] (M0097-0009)
	case p.acceptIdentKeyword("sequence"):
		return p.parseCreateSequenceTail(t.Pos, unlogged)
	// CREATE MATERIALIZED VIEW [IF NOT EXISTS] name AS query [WITH NO DATA] (M0097-0013)
	case p.acceptIdentKeyword("materialized"):
		_ = p.acceptKeyword(KwView) || p.acceptIdentKeyword("view")
		return p.parseCreateMatViewTail(t.Pos)
	// CREATE TYPE name AS ENUM (val1, val2, …) — M0097-0017.
	case p.acceptIdentKeyword("type"):
		return p.parseCreateType(t.Pos)
	// CREATE DOMAIN name [AS] base_type [constraints] — M0097-0017.
	case p.acceptIdentKeyword("domain"):
		return p.parseCreateDomain(t.Pos)
	// Accept CREATE CONSTRAINT TRIGGER (skip CONSTRAINT keyword then TRIGGER)
	case p.acceptIdentKeyword("constraint"):
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTrigger {
			p.advance()
			return p.parseCreateTriggerTail(t.Pos)
		}
		return nil, p.errAtCur("expected TRIGGER after CREATE CONSTRAINT")
	// CREATE AGGREGATE name (sfunc=F, basetype=T, stype=S [, ...]) — validate basetype.
	// M0097-regress.
	case p.acceptIdentKeyword("aggregate"):
		return p.parseCreateAggregateTail(t.Pos)
	// CREATE OPERATOR CLASS name FOR TYPE t USING hash AS … — register hash func. M0097-0027.
	case p.acceptIdentKeyword("operator"):
		if p.acceptIdentKeyword("class") {
			return p.parseCreateOpClassTail(t.Pos)
		}
		// CREATE OPERATOR name (leftarg=T, rightarg=T, ...) — parse name + arg types for compat
		// registry so DROP OPERATOR can find it later. M0097-regress.
		opName, _ := p.parseOperatorName()
		// Extract leftarg and rightarg from the parenthesised option list.
		var leftArg, rightArg string
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			depth := 1
			for depth > 0 {
				tok := p.cur()
				if tok.Kind == TokenEOF {
					break
				}
				if tok.Kind == TokenSymbol {
					if tok.Value == "(" {
						depth++
						p.advance()
						continue
					} else if tok.Value == ")" {
						depth--
						p.advance()
						continue
					} else if tok.Value == "," || tok.Value == ";" {
						p.advance()
						continue
					}
				}
				// Look for "leftarg = type" or "rightarg = type" key-value pairs.
				if depth == 1 && (tok.Kind == TokenIdent || tok.Kind == TokenKeyword) {
					key := strings.ToLower(tok.Value)
					p.advance()
					if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
						p.advance()
						// Collect type name (may be multi-word like "double precision").
						var typeParts []string
						for p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
							typeParts = append(typeParts, p.cur().Value)
							p.advance()
						}
						typeName := strings.Join(typeParts, " ")
						if key == "leftarg" {
							leftArg = typeName
						} else if key == "rightarg" {
							rightArg = typeName
						}
					}
					continue
				}
				p.advance()
			}
		}
		stmt, err := p.parseSkipToSemicolon(t.Pos)
		if err != nil {
			return nil, err
		}
		if ns, ok := stmt.(*CompatNoopStmt); ok {
			ns.ObjType = "operator"
			ns.ObjName = ObjectName{Name: opName.Name, Schema: opName.Schema}
			ns.ArgTypes = []string{leftArg, rightArg}
		}
		return stmt, nil
	// CREATE CONVERSION name ... — parse name for compat registry, then skip. M0097-0071.
	case p.acceptIdentKeyword("conversion"):
		convName, _ := p.parseObjectName()
		stmt, err := p.parseSkipToSemicolon(t.Pos)
		if err != nil {
			return nil, err
		}
		if ns, ok := stmt.(*CompatNoopStmt); ok {
			ns.ObjType = "conversion"
			ns.ObjName = convName
		}
		return stmt, nil
	// CREATE TEXT SEARCH DICTIONARY|CONFIGURATION|PARSER|TEMPLATE name — parse name for compat registry. M0097-0071.
	case p.acceptIdentKeyword("text"):
		_ = p.acceptIdentKeyword("search") // consume "search"
		var tsType string
		switch {
		case p.acceptIdentKeyword("dictionary"):
			tsType = "text search dictionary"
		case p.acceptIdentKeyword("configuration"):
			tsType = "text search configuration"
		case p.acceptIdentKeyword("parser"):
			tsType = "text search parser"
		case p.acceptIdentKeyword("template"):
			tsType = "text search template"
		}
		var tsName ObjectName
		if tsType != "" {
			tsName, _ = p.parseObjectName()
		}
		stmt, err := p.parseSkipToSemicolon(t.Pos)
		if err != nil {
			return nil, err
		}
		if ns, ok := stmt.(*CompatNoopStmt); ok && tsType != "" {
			ns.ObjType = tsType
			ns.ObjName = tsName
		}
		return stmt, nil
	// CREATE SERVER name ... FOREIGN DATA WRAPPER fdwname [OPTIONS (...)] — register as compat object. M0097-0071.
	case p.acceptIdentKeyword("server"):
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Look for FOREIGN DATA WRAPPER fdwname to record the FDW association.
		var fdwName ObjectName
		for {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				break
			}
			// Detect "FOREIGN DATA WRAPPER fdwname".
			if tok.Kind == TokenKeyword && tok.Keyword == KwForeign {
				p.advance()
				if p.acceptIdentKeyword("data") && p.acceptIdentKeyword("wrapper") {
					fdwName, _ = p.parseObjectName()
					continue
				}
				continue
			}
			p.advance()
		}
		ns := &CompatNoopStmt{pos: t.Pos, Tag: "CREATE", ObjType: "server", ObjName: name}
		if fdwName.Name != "" {
			ns.TableName = fdwName // reuse TableName field to store FDW association
		}
		return ns, nil
	// CREATE FOREIGN TABLE / CREATE FOREIGN DATA WRAPPER. M0097-0071.
	// FOREIGN is a reserved keyword so acceptKeyword is required (not acceptIdentKeyword).
	case p.acceptKeyword(KwForeign):
		if p.acceptKeyword(KwTable) {
			// CREATE FOREIGN TABLE name (cols) SERVER server [OPTIONS (...)]
			return p.parseCreateForeignTableTail(t.Pos)
		}
		if p.acceptIdentKeyword("data") {
			_ = p.acceptIdentKeyword("wrapper")
			// CREATE FOREIGN DATA WRAPPER name [OPTIONS (...)] — register as compat object.
			name, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			for {
				tok := p.cur()
				if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "CREATE", ObjType: "foreign-data wrapper", ObjName: name}, nil
		}
		// Other CREATE FOREIGN ... → skip.
		return p.parseSkipToSemicolon(t.Pos)
	// CREATE STATISTICS [IF NOT EXISTS] name [(types)] ON ... FROM table. M0097-0023.
	case p.acceptIdentKeyword("statistics"):
		return p.parseCreateStatisticsTail(t.Pos)
	// CREATE RULE name AS ON event TO table [WHERE cond] DO ... — accept as no-op.
	// Rules are not implemented in goopg v0; CREATE RULE succeeds silently so that
	// DROP RULE can track rule existence.
	case p.acceptIdentKeyword("rule"):
		return p.parseCreateRuleTail(t.Pos)
	// CREATE SCHEMA [name] [AUTHORIZATION role] — register schema in catalog.
	// Standalone CREATE SCHEMA is intercepted by dispatch.go before parsing; this
	// case handles multi-statement batches where the SQL parser runs first.
	case p.acceptIdentKeyword("schema"):
		var schemaName string
		if tok := p.cur(); (tok.Kind == TokenIdent || tok.Kind == TokenKeyword) &&
			!strings.EqualFold(tok.Value, "authorization") {
			schemaName = tok.Value
			p.advance()
		}
		for {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "CREATE SCHEMA", ObjType: "schema", ObjName: ObjectName{Name: schemaName}}, nil
	// CREATE EXTENSION name [WITH] [SCHEMA s] [VERSION v] [CASCADE] — inserts a
	// pg_extension row (e.g. amcheck). M0110-0003.
	case p.acceptIdentKeyword("extension"):
		return p.parseCreateExtensionTail(t.Pos)
	// CREATE TABLESPACE name [OWNER role] LOCATION 'dir' [WITH (opts)] — in-place
	// developer/regression tablespace support. M0095-0003.
	case p.acceptKeyword(KwTablespace):
		return p.parseCreateTablespaceTail(t.Pos)
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, FUNCTION, PROCEDURE, TRIGGER, EXTENSION, or TABLESPACE after CREATE")
}

// parseCreateExtensionTail parses the tail of
//
//	CREATE EXTENSION [IF NOT EXISTS] name [WITH] [SCHEMA s] [VERSION v] [CASCADE]
//
// after the EXTENSION keyword. Mirrors gram.y's CreateExtensionStmt option
// list (the options may appear in any order). M0110-0003.
func (p *parser) parseCreateExtensionTail(pos int) (Stmt, error) {
	stmt := &CreateExtensionStmt{pos: pos}
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwNot); err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfNotExists = true
	}
	// Extension name (identifier, keyword-as-ident, or quoted string).
	tok := p.cur()
	if tok.Kind != TokenIdent && tok.Kind != TokenKeyword && tok.Kind != TokenStringLit {
		return nil, p.errAtCur("expected extension name")
	}
	stmt.Name = tok.Value
	p.advance()
	// Optional WITH before the option list.
	_ = p.acceptKeyword(KwWith)
	// Option list: SCHEMA name | VERSION version | CASCADE, any order.
	for {
		switch {
		case p.acceptIdentKeyword("schema"):
			nt := p.cur()
			if nt.Kind != TokenIdent && nt.Kind != TokenKeyword && nt.Kind != TokenStringLit {
				return nil, p.errAtCur("expected schema name after SCHEMA")
			}
			stmt.Schema = nt.Value
			p.advance()
		case p.acceptIdentKeyword("version"):
			nt := p.cur()
			if nt.Kind != TokenIdent && nt.Kind != TokenKeyword && nt.Kind != TokenStringLit {
				return nil, p.errAtCur("expected version after VERSION")
			}
			stmt.Version = nt.Value
			p.advance()
		case p.acceptKeyword(KwCascade):
			stmt.Cascade = true
		default:
			return stmt, nil
		}
	}
}

// parseCreateTablespaceTail parses the tail of
//
//	CREATE TABLESPACE name [OWNER role] LOCATION 'dir' [WITH (opt = val, …)]
//
// after the TABLESPACE keyword. Mirrors gram.y's CreateTableSpaceStmt. The
// LOCATION string is mandatory in the grammar; an empty location requests an
// in-place tablespace (validated against allow_in_place_tablespaces by the
// executor). M0095-0003.
func (p *parser) parseCreateTablespaceTail(pos int) (Stmt, error) {
	stmt := &CreateTablespaceStmt{pos: pos}
	// Tablespace name.
	tok := p.cur()
	if tok.Kind != TokenIdent && tok.Kind != TokenKeyword && tok.Kind != TokenQuotedIdent {
		return nil, p.errAtCur("expected tablespace name")
	}
	stmt.Name = tok.Value
	p.advance()
	// Optional OWNER role.
	if p.acceptIdentKeyword("owner") {
		// PG allows OWNER [=] role; accept an optional '=' ("=" is TokenOperator).
		if c := p.cur(); (c.Kind == TokenOperator || c.Kind == TokenSymbol) && c.Value == "=" {
			p.advance()
		}
		ot := p.cur()
		if ot.Kind != TokenIdent && ot.Kind != TokenKeyword && ot.Kind != TokenQuotedIdent && ot.Kind != TokenStringLit {
			return nil, p.errAtCur("expected owner role after OWNER")
		}
		stmt.Owner = ot.Value
		p.advance()
	}
	// LOCATION 'dir' (mandatory).
	if !p.acceptIdentKeyword("location") {
		return nil, p.errAtCur("expected LOCATION in CREATE TABLESPACE")
	}
	lt := p.cur()
	if lt.Kind != TokenStringLit {
		return nil, p.errAtCur("expected location string after LOCATION")
	}
	stmt.Location = lt.Value
	p.advance()
	// Optional WITH ( option = value, … ) — parsed but currently ignored.
	if p.acceptKeyword(KwWith) {
		if c := p.cur(); c.Kind != TokenSymbol || c.Value != "(" {
			return nil, p.errAtCur("expected '(' after WITH")
		}
		p.advance() // consume '('
		for {
			c := p.cur()
			if c.Kind == TokenEOF {
				return nil, p.errAtCur("unterminated WITH option list")
			}
			if c.Kind == TokenSymbol && c.Value == ")" {
				p.advance()
				break
			}
			stmt.Options = append(stmt.Options, c.Value)
			p.advance()
		}
	}
	return stmt, nil
}

// parseCreateAggregateTail picks up after "CREATE AGGREGATE".  It parses just
// enough of the aggregate definition to determine whether "basetype" is present.
// Returns a CreateAggregateStmt that the executor validates. M0097-regress.
func (p *parser) parseCreateAggregateTail(pos int) (Stmt, error) {
	stmt := &CreateAggregateStmt{pos: pos}
	// Name.
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name

	// Support two forms:
	//   New style: aggregate_name(type1, type2, ...) (sfunc=F, stype=S, ...)
	//   Old style: aggregate_name (sfunc=F, basetype=T, stype=S, ...)
	//
	// Distinguish by peeking ahead in the first '(' block: if none of the
	// tokens before the closing ')' has '=' right after it, it's an arg-type
	// list (new style). Otherwise it's an old-style option list.
	if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
		return nil, p.errSyntaxAtCur()
	}

	// Peek ahead to detect which style.
	isNewStyle := p.aggregateIsNewStyle()

	if isNewStyle {
		// New style: parse "(type1, type2, ...)" as arg types.
		p.advance() // consume "("
		var argTypes []string
		isVariadic := false
		for p.cur().Kind != TokenSymbol || p.cur().Value != ")" {
			if p.cur().Kind == TokenEOF {
				break
			}
			// Detect VARIADIC keyword in arg list.
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwVariadic {
				isVariadic = true
				p.advance()
				continue // skip parameter name following VARIADIC
			}
			// Skip parameter name (identifier before type).
			// Pattern: "VARIADIC name type" — skip ident if followed by another ident/keyword.
			if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
				next := p.peek(1)
				if (next.Kind == TokenIdent || next.Kind == TokenKeyword) && next.Value != ")" && next.Value != "," {
					p.advance()
					continue
				}
			}
			// Collect type tokens until ',' or ')'.
			tok := p.cur()
			p.advance()
			argTypes = append(argTypes, strings.ToLower(tok.Value))
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				p.advance()
			}
		}
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			p.advance() // consume ")"
		}
		if len(argTypes) > 0 {
			stmt.HasBaseType = true
			stmt.BaseType = argTypes[0]
			stmt.Variadic = isVariadic
		}
		// For zero-arg new-style: CREATE AGGREGATE name (*) (...)
		// The arg list was just "*".
		if len(argTypes) == 1 && argTypes[0] == "*" {
			stmt.HasBaseType = true
			stmt.BaseType = "*"
		}
		// Now parse the options block "(sfunc=F, stype=S, ...)".
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			if err := p.parseAggregateOptions(stmt); err != nil {
				return nil, err
			}
		}
	} else {
		// Old style: parse "(basetype=T, sfunc=F, stype=S, ...)".
		if err := p.parseAggregateOptions(stmt); err != nil {
			return nil, err
		}
	}
	return stmt, nil
}

// aggregateIsNewStyle peeks ahead to determine if the current '(' block is
// a new-style arg-type list (no '=' tokens) or an old-style option list.
// Does NOT consume any tokens.
func (p *parser) aggregateIsNewStyle() bool {
	// Scan forward from current position looking for '='.
	// If we find '=' before the matching ')', it's old style.
	depth := 0
	for i := p.idx; i < len(p.tokens); i++ {
		tok := p.tokens[i]
		if tok.Kind == TokenSymbol && tok.Value == "(" {
			depth++
		} else if tok.Kind == TokenSymbol && tok.Value == ")" {
			depth--
			if depth == 0 {
				break
			}
		} else if depth == 1 && tok.Kind == TokenOperator && tok.Value == "=" {
			return false // found '=' inside first-level parens → old style
		}
	}
	return true // no '=' found → new style
}

// parseAggregateOptions parses the "(key=value, ...)" option list for a
// CREATE AGGREGATE statement, consuming the enclosing parentheses.
func (p *parser) parseAggregateOptions(stmt *CreateAggregateStmt) error {
	if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
		return p.errSyntaxAtCur()
	}
	p.advance() // consume "("
	for p.cur().Kind != TokenSymbol || p.cur().Value != ")" {
		if p.cur().Kind == TokenEOF {
			break
		}
		// Parse key = value pair.
		keyTok := p.cur()
		p.advance()
		// "=" is TokenOperator in goopg's lexer (not TokenSymbol).
		if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
			p.advance() // consume "="
			valTok := p.cur()
			if valTok.Kind != TokenEOF {
				p.advance()
			}
			key := strings.ToLower(keyTok.Value)
			// valStr is the raw value; for string literals the lexer stores
			// the literal without surrounding quotes already.
			valStr := valTok.Value
			switch key {
			case "basetype":
				stmt.HasBaseType = true
				stmt.BaseType = strings.ToLower(valStr)
			case "stype", "stype1":
				stmt.SType = strings.ToLower(valStr)
			case "sfunc", "sfunc1":
				stmt.SFunc = strings.ToLower(valStr)
			case "finalfunc":
				stmt.FinalFunc = strings.ToLower(valStr)
			case "initcond", "initcond1":
				stmt.InitCond = valStr
			case "combinefunc":
				stmt.CombineFunc = strings.ToLower(valStr)
			// Accepted but ignored options.
			case "parallel", "sspace", "serialfunc", "deserialfunc",
				"mstype", "msfunc", "minvfunc", "mfinalfunc", "minitcond",
				"sortop", "hypothetical", "mspace":
				// ignore
			}
			// If the value token is followed by '(', skip the arg list
			// (e.g. COMBINEFUNC = balkifnull(int8, int8)). M0097-0035.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				depth := 1
				p.advance() // consume "("
				for depth > 0 && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
						depth++
					} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
						depth--
					}
					if depth > 0 {
						p.advance()
					}
				}
				if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
					p.advance() // consume final ")"
				}
			}
		}
		// Skip optional comma.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
			p.advance()
		}
	}
	if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
		p.advance() // consume ")"
	}
	return nil
}

// parseCreateOpClassTail picks up after "CREATE OPERATOR CLASS".
// Captures just enough to register the FUNCTION 2 (hash extended support func)
// for use in satisfies_hash_partition. Everything else is accepted and ignored.
// M0097-0027.
func (p *parser) parseCreateOpClassTail(pos int) (Stmt, error) {
	stmt := &CreateOpClassStmt{pos: pos}
	// Name.
	nameTok := p.cur()
	if nameTok.Kind != TokenIdent && nameTok.Kind != TokenQuotedIdent {
		return nil, p.errAtCur("expected operator class name")
	}
	stmt.Name = nameTok.Value
	p.advance()
	// Skip optional DEFAULT (reserved keyword).
	p.acceptKeyword(KwDefault)
	// FOR TYPE typename.
	if !p.acceptKeyword(KwFor) {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	// "type" is not in goopg's keyword map — arrives as TokenIdent.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "type") {
		p.advance()
	} else {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	typeObj, _ := p.parseTypeNameAfterCast()
	stmt.ForType = strings.ToLower(typeObj.String())
	// USING hash (USING is a reserved keyword).
	if !p.acceptKeyword(KwUsing) {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	p.advance() // access method name (e.g. "hash") — consume it
	// AS list of entries (AS is a reserved keyword).
	if !p.acceptKeyword(KwAs) {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	// Scan entries: OPERATOR n op [, FUNCTION n name(args) [, ...]]
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		// "operator" is not in goopg's keyword map → TokenIdent.
		isOperator := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "operator")
		// "function" IS in the keyword map as KwCatUnreserved → TokenKeyword.
		isFunction := tok.Kind == TokenKeyword && tok.Keyword == KwFunction
		if isOperator {
			p.advance() // consume "operator"
			// Skip strategy number.
			if p.cur().Kind == TokenIntLit {
				p.advance()
			}
			// Skip operator name (may be =, <>, OPERATOR(...), or bare ident).
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.skipBalancedParens() // OPERATOR(schema.op) qualified form
			} else if p.cur().Kind == TokenOperator {
				p.advance() // simple operator like =, <, >, <=
			} else if p.cur().Kind == TokenIdent {
				p.advance() // bare identifier operator
			}
			// Skip optional operand type list: (type, type).
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.skipBalancedParens()
			}
		} else if isFunction {
			p.advance() // consume "function"
			numTok := p.cur()
			if numTok.Kind == TokenIntLit {
				p.advance()
			}
			funcName, err := p.parseObjectName()
			if err != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			// Skip argument list.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.skipBalancedParens()
			}
			// FUNCTION 2 is the hash extended support function.
			if numTok.Value == "2" {
				stmt.HashFuncName = strings.ToLower(funcName.String())
			}
		} else {
			break
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return stmt, nil
}

func parseSkipToSemicolonHelper(p *parser, stmt Stmt) (Stmt, error) {
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		if tok.Kind == TokenSymbol && tok.Value == ";" {
			break
		}
		p.advance()
	}
	return stmt, nil
}

// parseSkipToSemicolon advances past all tokens until ';' or EOF and returns a
// CompatNoopStmt so the caller has a valid non-nil Stmt that succeeds silently.
// Used for DDL statements that are syntactically accepted but semantically
// ignored (e.g. CREATE OPERATOR, CREATE CAST). M0097-0027. Previously returned
// DoStmt which failed with "DO block language not supported" and aborted
// any enclosing transaction; CompatNoopStmt avoids that. M0097-0065.
// parseCreateRuleTail parses a CREATE RULE statement as a no-op.
// Rules are not implemented in goopg v0 but CREATE RULE must parse
// successfully so that DROP RULE can track the existence of created rules.
// The CREATE RULE syntax can have nested parens in the DO clause, so we
// use depth tracking rather than a simple scan to semicolon.
func (p *parser) parseCreateRuleTail(pos int) (Stmt, error) {
	// Extract rule name (first token after RULE keyword).
	ns := &CompatNoopStmt{pos: pos, Tag: "CREATE RULE", ObjType: "rule"}
	if tok := p.cur(); tok.Kind == TokenIdent || tok.Kind == TokenKeyword {
		ns.ObjName = ObjectName{Name: tok.Value}
		p.advance()
	}
	// Scan for "TO <tablename>" and detect the rule kind (DO ALSO, DO INSTEAD NOTHING,
	// multi-statement, conditional, or utility action). M0097-0140.
	depth := 0
	seenDo := false      // passed DO keyword at depth 0
	seenInstead := false // passed INSTEAD keyword at depth 0 after DO
	seenAlso := false    // DO ALSO detected
	hasWhere := false    // WHERE clause present before DO
	gotKind := false     // rule kind already determined

	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		if tok.Kind == TokenSymbol && tok.Value == ";" && depth == 0 {
			break
		}
		if tok.Kind == TokenSymbol {
			switch tok.Value {
			case "(", "[":
				if depth == 0 && seenInstead && !gotKind {
					ns.RuleKind = "multi-statement DO INSTEAD"
					gotKind = true
				}
				depth++
				p.advance()
				continue
			case ")", "]":
				if depth > 0 {
					depth--
				}
			}
		}
		if depth == 0 {
			kw := strings.ToUpper(tok.Value)
			switch {
			case kw == "TO" && ns.TableName.Name == "" && !seenDo:
				p.advance()
				tname, _ := p.parseObjectName()
				ns.TableName = tname
				continue
			case kw == "WHERE" && !seenDo:
				hasWhere = true
			case kw == "DO" && !seenDo:
				seenDo = true
			case seenDo && !seenInstead && !seenAlso && kw == "INSTEAD":
				seenInstead = true
			case seenDo && !seenInstead && !seenAlso && kw == "ALSO":
				ns.RuleKind = "DO ALSO"
				seenAlso = true
				gotKind = true
			case seenInstead && !gotKind && kw == "NOTHING":
				ns.RuleKind = "DO INSTEAD NOTHING"
				gotKind = true
			case seenInstead && !gotKind && kw == "NOTIFY":
				ns.RuleKind = "utility"
				gotKind = true
			case seenInstead && !gotKind:
				// First meaningful token after INSTEAD: not NOTHING, not (, not NOTIFY.
				if hasWhere {
					ns.RuleKind = "conditional DO INSTEAD"
				} else {
					ns.RuleKind = "DO INSTEAD"
				}
				gotKind = true
			}
		}
		p.advance()
	}
	return ns, nil
}

// parseCreateStatisticsTail parses CREATE STATISTICS after the STATISTICS keyword.
// Grammar: [IF NOT EXISTS] name [(types)] ON expr_list FROM table_name
// Only the name and FROM table are extracted; the ON clause is skipped. M0097-0023.
func (p *parser) parseCreateStatisticsTail(pos int) (Stmt, error) {
	stmt := &CreateStatisticsStmt{pos: pos}
	// IF NOT EXISTS
	if p.acceptIdentKeyword("if") {
		if !p.acceptIdentKeyword("not") {
			return p.parseSkipToSemicolon(pos)
		}
		if !p.acceptIdentKeyword("exists") {
			return p.parseSkipToSemicolon(pos)
		}
		stmt.IfNotExists = true
	}
	name, err := p.parseObjectName()
	if err != nil {
		return p.parseSkipToSemicolon(pos)
	}
	stmt.Name = name
	// Skip optional (statistics_kind, ...) type list.
	if p.acceptSymbol("(") {
		depth := 1
		for depth > 0 {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				break
			}
			if tok.Kind == TokenSymbol && tok.Value == "(" {
				depth++
			} else if tok.Kind == TokenSymbol && tok.Value == ")" {
				depth--
			}
			p.advance()
		}
	}
	// Skip tokens until FROM keyword.
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
			return stmt, nil
		}
		if tok.Kind == TokenKeyword && tok.Keyword == KwFrom {
			p.advance()
			break
		}
		p.advance()
	}
	fromTable, err := p.parseObjectName()
	if err != nil {
		return stmt, nil // best-effort: return what we have
	}
	stmt.FromTable = fromTable
	return stmt, nil
}

func (p *parser) parseSkipToSemicolon(pos int) (Stmt, error) {
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		if tok.Kind == TokenSymbol && tok.Value == ";" {
			break
		}
		p.advance()
	}
	return &CompatNoopStmt{pos: pos, Tag: "CREATE"}, nil
}

// parseCreateForeignTableTail parses the tail of CREATE FOREIGN TABLE after the TABLE keyword.
// Grammar: name [(colDefs)] SERVER servername [OPTIONS (...)]
// Returns a CreateTableStmt; the SERVER/OPTIONS suffix is consumed and discarded.
// Foreign tables are treated as regular tables for storage purposes in goopg v0.
func (p *parser) parseCreateForeignTableTail(pos int) (Stmt, error) {
	// Reuse the regular CREATE TABLE parser for name + column list.
	stmt, err := p.parseCreateTableTail(pos, false)
	if err != nil {
		return nil, err
	}
	// Consume the optional SERVER name and OPTIONS (...) that follow the column list.
	// Skip everything up to ';' or EOF.
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
			break
		}
		p.advance()
	}
	return stmt, nil
}

// parseCreatePublicationTail picks up after CREATE PUBLICATION.
// Grammar: `name [FOR ALL TABLES | FOR TABLE t1 [, t2 ...]]
//
//	[WITH (option = value [, ...])]`.
//
// See docs/design/0008-0003-publication-subscription-ddl.md.
func (p *parser) parseCreatePublicationTail(pos int) (Stmt, error) {
	stmt := &CreatePublicationStmt{pos: pos}
	if p.cur().Kind != TokenIdent {
		return nil, p.errAtCur("expected publication name after CREATE PUBLICATION")
	}
	stmt.Name = p.cur().Value
	p.advance()
	if p.acceptKeyword(KwFor) {
		if p.acceptKeyword(KwAll) {
			if !p.acceptKeyword(KwTables) {
				return nil, p.errAtCur("expected TABLES after FOR ALL")
			}
			stmt.AllTables = true
		} else if p.acceptKeyword(KwTable) {
			tables, err := p.parseObjectList()
			if err != nil {
				return nil, err
			}
			stmt.Tables = tables
		} else {
			return nil, p.errAtCur("expected ALL TABLES or TABLE after FOR")
		}
	}
	if p.acceptKeyword(KwWith) {
		opts, err := p.parsePubSubWithList()
		if err != nil {
			return nil, err
		}
		stmt.With = opts
	}
	return stmt, nil
}

// parseCreateSubscriptionTail picks up after CREATE SUBSCRIPTION.
// Grammar: `name CONNECTION 'conninfo' PUBLICATION p [, p2 ...]
//
//	[WITH (option = value [, ...])]`.
func (p *parser) parseCreateSubscriptionTail(pos int) (Stmt, error) {
	stmt := &CreateSubscriptionStmt{pos: pos}
	if p.cur().Kind != TokenIdent {
		return nil, p.errAtCur("expected subscription name after CREATE SUBSCRIPTION")
	}
	stmt.Name = p.cur().Value
	p.advance()
	if _, err := p.expectKeyword(KwConnection); err != nil {
		return nil, err
	}
	if p.cur().Kind != TokenStringLit {
		return nil, p.errAtCur("expected string literal after CONNECTION")
	}
	stmt.Conninfo = p.cur().Value
	p.advance()
	if _, err := p.expectKeyword(KwPublication); err != nil {
		return nil, err
	}
	if p.cur().Kind != TokenIdent {
		return nil, p.errAtCur("expected publication name after PUBLICATION")
	}
	stmt.Publications = append(stmt.Publications, p.cur().Value)
	p.advance()
	for p.acceptSymbol(",") {
		if p.cur().Kind != TokenIdent {
			return nil, p.errAtCur("expected publication name after ','")
		}
		stmt.Publications = append(stmt.Publications, p.cur().Value)
		p.advance()
	}
	if p.acceptKeyword(KwWith) {
		opts, err := p.parsePubSubWithList()
		if err != nil {
			return nil, err
		}
		stmt.With = opts
	}
	return stmt, nil
}

// parsePubSubWithList parses `(key = value [, key = value …])`. Values
// may be string literals, identifiers, or boolean keywords.
func (p *parser) parsePubSubWithList() (map[string]string, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after WITH")
	}
	out := map[string]string{}
	for {
		if p.cur().Kind != TokenIdent && p.cur().Kind != TokenKeyword {
			return nil, p.errAtCur("expected option name in WITH list")
		}
		key := p.cur().Value
		p.advance()
		if cur := p.cur(); !(cur.Kind == TokenOperator && cur.Value == "=") {
			return nil, p.errAtCur("expected '=' after option name")
		}
		p.advance()
		var value string
		switch p.cur().Kind {
		case TokenStringLit, TokenIdent, TokenIntLit:
			value = p.cur().Value
			p.advance()
		case TokenKeyword:
			value = string(p.cur().Keyword)
			p.advance()
		default:
			return nil, p.errAtCur("expected option value")
		}
		out[key] = value
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close WITH list")
	}
	return out, nil
}

// parseCreateViewTail picks up after CREATE [OR REPLACE] VIEW.
// Grammar: `name [(col_list)] AS <select>`.
func (p *parser) parseCreateViewTail(pos int, orReplace bool) (Stmt, error) {
	stmt := &CreateViewStmt{pos: pos, OrReplace: orReplace}
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Optional explicit column list.
	if p.acceptSymbol("(") {
		cols, err := p.parseColumnNameList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after view column list")
		}
		stmt.Columns = cols
	}
	// Optional WITH (view_option_name [= view_option_value] [, ...]) before AS.
	// PostgreSQL supports security_invoker, security_barrier, check_option.
	// goopg v0 accepts and ignores all view options.
	if p.acceptKeyword(KwWith) {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after WITH in CREATE VIEW")
		}
		for !p.acceptSymbol(")") {
			// option name (identifier)
			if _, err := p.parseIdent(); err != nil {
				return nil, err
			}
			// optional = value
			if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
				p.advance()
				if _, err := p.parseIdent(); err != nil {
					return nil, err
				}
			}
			p.acceptSymbol(",")
		}
	}
	if _, err := p.expectKeyword(KwAs); err != nil {
		return nil, err
	}
	// Allow SELECT, VALUES, or WITH as the view body.
	cur := p.cur()
	if !(cur.Kind == TokenKeyword && (cur.Keyword == KwSelect || cur.Keyword == KwValues || cur.Keyword == KwWith)) {
		return nil, p.errAtCur("expected SELECT after AS")
	}
	// SELECT … INTO is not permitted in a view body (M0097-0020).
	old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
	p.selectIntoErrMsg = "views must not contain SELECT INTO"
	p.selectIntoNoPos = true
	inner, err := p.parseSelect()
	p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
	if err != nil {
		return nil, err
	}
	sel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, &SyntaxError{Pos: pos, Message: "view body did not produce SELECT"}
	}
	stmt.Query = sel
	// Capture the raw source span of the view body (from the SELECT/VALUES/WITH
	// keyword up to the next unconsumed token) so pg_get_viewdef can echo it
	// verbatim for pg_dump. p.cur() now points just past the body.
	stmt.RawDef = p.captureSrcSpan(cur.Pos, p.cur())
	// Optional trailing WITH [CASCADED|LOCAL] CHECK OPTION clause.
	// goopg accepts and ignores the clause (check enforcement not yet implemented).
	if p.acceptKeyword(KwWith) {
		_ = p.acceptIdentKeyword("cascaded") || p.acceptKeyword(KwLocal) || p.acceptIdentKeyword("local")
		if p.cur().Kind != TokenKeyword || p.cur().Keyword != KwCheck {
			return nil, p.errAtCur("expected CHECK after WITH in view definition")
		}
		p.advance()
		if !p.acceptIdentKeyword("option") {
			return nil, p.errAtCur("expected OPTION after WITH [CASCADED|LOCAL] CHECK")
		}
	}
	return stmt, nil
}

// parseCreateRecursiveViewTail handles CREATE [OR REPLACE] RECURSIVE VIEW name(cols) AS query.
// The recursive view is stored as a plain view whose body is a CTE:
//
//	WITH RECURSIVE name(cols) AS (query) SELECT * FROM name
func (p *parser) parseCreateRecursiveViewTail(pos int, orReplace bool) (Stmt, error) {
	stmt := &CreateViewStmt{pos: pos, OrReplace: orReplace}
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Mandatory column list for recursive views.
	var cols []string
	if p.acceptSymbol("(") {
		cols, err = p.parseColumnNameList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after recursive view column list")
		}
		stmt.Columns = cols
	}
	if _, err = p.expectKeyword(KwAs); err != nil {
		return nil, err
	}
	// Parse the view body. Remember its starting token so the raw source span
	// can be captured verbatim for pg_get_viewdef. M0110-0001 (DU-002 slice 61).
	bodyStart := p.cur()
	old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
	p.selectIntoErrMsg = "views must not contain SELECT INTO"
	p.selectIntoNoPos = true
	body, err := p.parseSelect()
	p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
	if err != nil {
		return nil, err
	}
	bodySel, ok := body.(*SelectStmt)
	if !ok {
		return nil, &SyntaxError{Pos: pos, Message: "recursive view body did not produce SELECT"}
	}
	rawBody := p.captureSrcSpan(bodyStart.Pos, p.cur())
	// Wrap: WITH RECURSIVE name(cols) AS (body) SELECT * FROM name.
	cte := &CommonTableExpr{
		Name:    name.Name,
		Columns: cols,
		Query:   bodySel,
	}
	outer := &SelectStmt{
		pos: pos,
		With: &WithClause{
			Recursive: true,
			CTEs:      []*CommonTableExpr{cte},
		},
		Targets: []ResTarget{{Expr: &StarExpr{pos: pos}}},
		From:    []RangeVar{{pos: pos, Name: name.Name}},
	}
	stmt.Query = outer
	// Synthesize the wrapped-CTE form as the view's stored definition so
	// pg_get_viewdef returns a non-empty body (a recursive view with no RawDef
	// makes pg_dump abort the whole dump — the slice-57 blocker). PostgreSQL
	// stores a recursive view as a regular view over a WITH RECURSIVE CTE and
	// pg_dump re-emits it as a plain CREATE VIEW; goopg mirrors that by echoing
	// `WITH RECURSIVE name(cols) AS (<body>) SELECT cols FROM name`. The outer
	// projection lists the declared columns explicitly (PG expands the canonical
	// list; goopg has no deparser, so it spells them out — a documented fidelity
	// gap, like the verbatim plain-view body). The "WITH" prefix means
	// applyViewColumnAliases bails (it only rewrites bodies starting with SELECT),
	// so the column names come solely from this projection. M0110-0001 slice 61.
	if rawBody != "" && len(cols) > 0 {
		colList := strings.Join(cols, ", ")
		stmt.RawDef = "WITH RECURSIVE " + name.Name + "(" + colList + ") AS (" +
			rawBody + ") SELECT " + colList + " FROM " + name.Name
	}
	return stmt, nil
}

// parseCreateMatViewTail picks up after CREATE MATERIALIZED VIEW.
// Grammar: `[IF NOT EXISTS] name AS <select> [WITH [NO] DATA]`. M0097-0013.
func (p *parser) parseCreateMatViewTail(pos int) (Stmt, error) {
	stmt := &CreateMatViewStmt{pos: pos}
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwNot); err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfNotExists = true
	}
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Optional column aliases: name (col1, col2, ...) AS query
	if p.acceptSymbol("(") {
		for {
			alias, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.ColumnAliases = append(stmt.ColumnAliases, alias.Value)
			if p.acceptSymbol(")") {
				break
			}
			if !p.acceptSymbol(",") {
				return nil, &SyntaxError{Pos: p.cur().Pos, Message: "expected ',' or ')' in column alias list"}
			}
		}
	}
	// Skip optional USING index_method clause.
	if p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using") {
		_, _ = p.parseIdent()
	}
	// Skip optional WITH (storage_params).
	if p.acceptKeyword(KwWith) && p.acceptSymbol("(") {
		depth := 1
		for depth > 0 && p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				depth++
			} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				depth--
			}
			p.advance()
		}
	}
	// Optional TABLESPACE clause.
	if p.acceptIdentKeyword("tablespace") {
		_, _ = p.parseIdent()
	}
	if _, err := p.expectKeyword(KwAs); err != nil {
		return nil, err
	}
	// Remember the body's starting token so the raw source span can be captured
	// verbatim for pg_get_viewdef (mirrors parseCreateViewTail). M0110-0001 (DU-002).
	bodyStart := p.cur()
	inner, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	sel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, &SyntaxError{Pos: pos, Message: "materialized view body did not produce SELECT"}
	}
	stmt.Query = sel
	// p.cur() now points just past the body (at WITH [NO] DATA or EOF), so the
	// span runs from the SELECT/WITH keyword up to the optional data clause.
	stmt.RawDef = p.captureSrcSpan(bodyStart.Pos, p.cur())
	// Optional WITH [NO] DATA clause. "NO" is a plain identifier, not a keyword.
	if p.acceptKeyword(KwWith) {
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("data")
			stmt.WithNoData = true
		} else {
			_ = p.acceptIdentKeyword("data")
		}
	}
	return stmt, nil
}

// parseRefreshMatView parses `REFRESH MATERIALIZED VIEW [CONCURRENTLY] name
// [WITH [NO] DATA]`. Called from parser.go after consuming REFRESH. M0097-0013.
func (p *parser) parseRefreshMatView(pos int) (Stmt, error) {
	stmt := &RefreshMatViewStmt{pos: pos}
	_ = p.acceptIdentKeyword("materialized")
	_ = p.acceptKeyword(KwView) || p.acceptIdentKeyword("view")
	stmt.Concurrently = p.acceptIdentKeyword("concurrently")
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Optional WITH [NO] DATA. "NO" is a plain identifier, not a keyword.
	if p.acceptKeyword(KwWith) {
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("data")
			stmt.WithNoData = true
		} else {
			_ = p.acceptIdentKeyword("data")
		}
	}
	return stmt, nil
}

// parseConstraintDeferrable consumes an optional `[NOT] DEFERRABLE [INITIALLY
// DEFERRED | INITIALLY IMMEDIATE]` trailer (and the bare `INITIALLY DEFERRED`
// shorthand, which implies DEFERRABLE) following an inline column UNIQUE or
// PRIMARY KEY constraint, recording the result into the supplied flags. NOT
// DEFERRABLE and INITIALLY IMMEDIATE both leave the flags false (the SQL
// defaults). This is the inline-column sibling of the trailer parsing used by
// the table-level UNIQUE / PRIMARY KEY forms. DU-002 slices 141 (UNIQUE) / 142
// (PRIMARY KEY).
func (p *parser) parseConstraintDeferrable(deferrable, initiallyDeferred *bool) {
	if p.acceptKeyword(KwNot) {
		_ = p.acceptKeyword(KwDeferrable)
		return
	}
	if p.acceptKeyword(KwDeferrable) {
		*deferrable = true
		if p.acceptIdentKeyword("initially") {
			if p.acceptIdentKeyword("deferred") {
				*initiallyDeferred = true
			} else {
				_ = p.acceptIdentKeyword("immediate")
			}
		}
		return
	}
	if p.acceptIdentKeyword("initially") {
		if p.acceptIdentKeyword("deferred") {
			*deferrable = true
			*initiallyDeferred = true
		} else {
			_ = p.acceptIdentKeyword("immediate")
		}
	}
}

// parseCreateTableTail picks up after CREATE [UNLOGGED] TABLE.
func (p *parser) parseCreateTableTail(pos int, unlogged bool) (Stmt, error) {
	stmt := &CreateTableStmt{pos: pos, Unlogged: unlogged}
	if p.acceptKeyword(KwIf) {
		if !p.acceptKeyword(KwNot) {
			return nil, p.errAtCur("expected NOT after IF")
		}
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfNotExists = true
	}
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name

	// Detect optional column-alias list before AS:
	//   CREATE TABLE name (col1, col2, …) AS SELECT … [WITH NO DATA]
	// Disambiguation from regular column defs: CTAS aliases are bare identifiers
	// with no type. We speculatively consume the parenthesised list; if AS does
	// NOT follow we restore the token index and fall through to regular parsing.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		savedIdx := p.idx
		p.advance() // consume '('
		var aliasCandidate []string
		ok := true
		for p.cur().Kind != TokenEOF {
			id, err := p.parseIdent()
			if err != nil {
				ok = false
				break
			}
			aliasCandidate = append(aliasCandidate, identText(id))
			if !p.acceptSymbol(",") {
				break
			}
		}
		if ok && p.acceptSymbol(")") && p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
			// Confirmed CTAS column-alias list: keep the aliases and let the
			// AS check below consume and process the CREATE TABLE AS body.
			stmt.ColumnAliases = aliasCandidate
		} else {
			// Not a CTAS alias list: restore and let regular column-def
			// parsing handle the '(' (which may be empty `()` or real defs).
			p.idx = savedIdx
		}
	}

	// CREATE TABLE name AS SELECT/EXECUTE … [WITH NO DATA] (CTAS). M0096-0008.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
		p.advance() // consume AS
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwExecute {
			ex, err := p.parseExecute()
			if err != nil {
				return nil, err
			}
			stmt.ExecuteSource = ex.(*ExecuteStmt)
		} else {
			sel, err := p.parseSelect()
			if err != nil {
				return nil, err
			}
			if ss, ok := sel.(*SelectStmt); ok {
				stmt.SelectSource = ss
			}
		}
		// Optional WITH [NO] DATA clause.
		if p.acceptKeyword(KwWith) {
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("data")
				stmt.WithNoData = true
			} else {
				_ = p.acceptIdentKeyword("data")
			}
		}
		return stmt, nil
	}

	// CREATE TABLE child PARTITION OF parent FOR VALUES … (M0096-0007)
	if p.acceptKeyword(KwPartition) {
		if _, err := p.expectKeyword(KwOf); err != nil {
			return nil, err
		}
		parentName, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		poc := &PartitionOfClause{pos: pos, Parent: parentName}
		// Optional column definition list: `(col_name UNIQUE [, ...])`.
		// We extract UNIQUE constraints; other column attributes are ignored.
		// M0097-0028.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance()
			if !p.acceptSymbol(")") {
				// Parse each column entry looking for UNIQUE.
				// Grammar: col_name [type] [constraint ...] [, ...]
				// We track depth so nested parens (e.g. DEFAULT values) are skipped.
				depth := 1
				var curColName string
				inTableConstraint := false
				seenCols := make(map[string]bool)
				for depth > 0 && p.cur().Kind != TokenEOF {
					t := p.cur()
					if t.Kind == TokenSymbol && t.Value == "(" {
						depth++
						p.advance()
					} else if t.Kind == TokenSymbol && t.Value == ")" {
						depth--
						if depth > 0 {
							p.advance()
						}
					} else if depth == 1 && t.Kind == TokenSymbol && t.Value == "," {
						curColName = ""
						inTableConstraint = false
						p.advance()
					} else if depth == 1 && !inTableConstraint && curColName == "" && t.Kind == TokenKeyword &&
						t.Keyword == KwConstraint {
						// Table-level CONSTRAINT name CHECK (expr) in PARTITION OF column list.
						// Parse name + CHECK expression if present; otherwise skip.
						p.advance() // consume CONSTRAINT
						var constraintName string
						if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
							constraintName = p.cur().Value
							p.advance()
						}
						if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
							p.advance() // consume CHECK
							// Collect the expression text inside the balanced parens.
							if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
								p.advance() // consume opening (
								var exprTokens []string
								exprDepth := 1
								for exprDepth > 0 && p.cur().Kind != TokenEOF {
									tok := p.cur()
									if tok.Kind == TokenSymbol && tok.Value == "(" {
										exprDepth++
										exprTokens = append(exprTokens, tok.Value)
										p.advance()
									} else if tok.Kind == TokenSymbol && tok.Value == ")" {
										exprDepth--
										if exprDepth > 0 {
											exprTokens = append(exprTokens, tok.Value)
										}
										p.advance()
									} else {
										exprTokens = append(exprTokens, tok.Value)
										p.advance()
									}
								}
								if constraintName != "" {
									poc.CheckConstraints = append(poc.CheckConstraints, PartitionCheckConstraint{
										Name: constraintName,
										Expr: strings.Join(exprTokens, " "),
									})
								}
							}
						}
						inTableConstraint = true
					} else if depth == 1 && !inTableConstraint && curColName == "" && t.Kind == TokenKeyword &&
						(t.Keyword == KwCheck || t.Keyword == KwPrimary || t.Keyword == KwForeign) {
						// Other table-level constraint (CHECK without CONSTRAINT name, FK, PK) — skip.
						inTableConstraint = true
						p.advance()
					} else if depth == 1 && !inTableConstraint && curColName == "" && (t.Kind == TokenIdent || t.Kind == TokenKeyword) {
						colLower := strings.ToLower(t.Value)
						if seenCols[colLower] && poc.DuplicateColumn == "" {
							poc.DuplicateColumn = t.Value
						}
						seenCols[colLower] = true
						curColName = t.Value
						p.advance()
					} else if depth == 1 && !inTableConstraint && t.Kind == TokenKeyword && t.Keyword == KwUnique && curColName != "" {
						poc.UniqueColumns = append(poc.UniqueColumns, curColName)
						p.advance()
					} else if depth == 1 && !inTableConstraint && t.Kind == TokenKeyword && t.Keyword == KwNot && curColName != "" {
						// NOT NULL constraint in PARTITION OF column override list.
						p.advance()
						if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNull {
							poc.NotNullColumns = append(poc.NotNullColumns, curColName)
							p.advance()
						}
					} else if depth == 1 && !inTableConstraint && t.Kind == TokenKeyword && t.Keyword == KwDefault && curColName != "" {
						// DEFAULT expr override in PARTITION OF column override list.
						p.advance()
						expr, err := p.parseExpr()
						if err == nil {
							poc.ColDefaults = append(poc.ColDefaults, PartitionColDefault{
								ColName: curColName,
								Expr:    expr,
							})
						}
					} else if depth == 1 && !inTableConstraint && t.Kind == TokenKeyword && t.Keyword == KwWith && curColName != "" {
						// WITH OPTIONS GENERATED ALWAYS AS (expr) STORED — child-partition
						// generated-column expression override. M0100-0010.
						p.advance() // consume WITH
						if p.acceptIdentKeyword("options") && p.acceptIdentKeyword("generated") && p.acceptIdentKeyword("always") {
							if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
								p.advance() // consume AS
								if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
									p.advance() // consume (
									var exprToks []Token
									exprDepth := 1
									for exprDepth > 0 && p.cur().Kind != TokenEOF {
										tok := p.cur()
										if tok.Kind == TokenSymbol && tok.Value == "(" {
											exprDepth++
											exprToks = append(exprToks, tok)
										} else if tok.Kind == TokenSymbol && tok.Value == ")" {
											exprDepth--
											if exprDepth > 0 {
												exprToks = append(exprToks, tok)
											}
										} else {
											exprToks = append(exprToks, tok)
										}
										p.advance()
									}
									_ = p.acceptIdentKeyword("stored")
									poc.ColGeneratedExprs = append(poc.ColGeneratedExprs, PartitionColGenerated{
										ColName: curColName,
										Expr:    joinGeneratedExprTokens(exprToks),
									})
								}
							}
						}
					} else {
						p.advance()
					}
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			}
		}
		// FOR VALUES … or bare DEFAULT (both forms accepted by PostgreSQL).
		// `CREATE TABLE child PARTITION OF parent DEFAULT` is the short form
		// for the default partition; `FOR VALUES DEFAULT` is also valid.
		if p.acceptKeyword(KwDefault) || p.acceptIdentKeyword("default") {
			poc.Default = true
		} else if p.acceptKeyword(KwFor) {
			if _, err := p.expectKeyword(KwValues); err != nil {
				return nil, err
			}
			if p.acceptKeyword(KwDefault) || p.acceptIdentKeyword("default") {
				poc.Default = true
			} else if p.acceptKeyword(KwIn) {
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after IN")
				}
				// Empty IN list is a syntax error (PostgreSQL: "syntax error at or near ')'").
				if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
					return nil, p.errSyntaxAtCur()
				}
				vals, err := p.parseExprList()
				if err != nil {
					return nil, err
				}
				poc.InValues = vals
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			} else if p.acceptKeyword(KwWith) {
				// HASH partitioning: FOR VALUES WITH (MODULUS n, REMAINDER r). M0097-0015.
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after WITH")
				}
				poc.IsHash = true
				for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
					if p.acceptIdentKeyword("modulus") {
						if t := p.cur(); t.Kind == TokenIntLit {
							p.advance()
							n := int64(0)
							for _, c := range t.Value {
								n = n*10 + int64(c-'0')
							}
							poc.Modulus = n
						}
					} else if p.acceptIdentKeyword("remainder") {
						if t := p.cur(); t.Kind == TokenIntLit {
							p.advance()
							n := int64(0)
							for _, c := range t.Value {
								n = n*10 + int64(c-'0')
							}
							poc.Remainder = n
						}
					} else {
						p.advance()
					}
					_ = p.acceptSymbol(",")
				}
			} else if p.acceptKeyword(KwFrom) || p.acceptIdentKeyword("from") {
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after FROM")
				}
				fromVals, err := p.parsePartitionBoundValues()
				if err != nil {
					return nil, err
				}
				poc.FromValues = fromVals
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
				if !p.acceptKeyword(KwTo) {
					return nil, p.errAtCur("expected TO")
				}
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '('")
				}
				toVals, err := p.parsePartitionBoundValues()
				if err != nil {
					return nil, err
				}
				poc.ToValues = toVals
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			}
		}
		stmt.PartitionOf = poc
		// Optional PARTITION BY for nested partitions: CREATE TABLE foo3 PARTITION OF foo2
		// FOR VALUES ... PARTITION BY list(b). M0097-0020.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPartition {
			if p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwBy {
				pos2 := p.cur().Pos
				p.advance() // PARTITION
				p.advance() // BY
				method := ""
				switch {
				case p.acceptIdentKeyword("list"):
					method = "LIST"
				case p.acceptIdentKeyword("range"):
					method = "RANGE"
				case p.acceptIdentKeyword("hash"):
					method = "HASH"
				default:
					// Accept any identifier; executor validates the strategy name.
					if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
						method = p.cur().Value
						p.advance()
					} else {
						return nil, p.errAtCur("expected partition strategy after PARTITION BY")
					}
				}
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after partition method")
				}
				keyCols, keyExprs, opClasses, colls, err2 := p.parsePartitionKeyCols()
				if err2 != nil {
					return nil, err2
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
				stmt.PartitionBy = &PartitionByClause{pos: pos2, Method: method, KeyCols: keyCols, KeyExprs: keyExprs, OpClasses: opClasses, Collations: colls}
			}
		}
		// Optional USING <access_method> on a partition child, e.g.
		// `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) USING heap`.
		// PG's grammar is OptPartitionSpec table_access_method_clause OptWith
		// OnCommitOption OptTableSpace, so USING precedes WITH. The partition-child
		// arm previously omitted it, leaving the USING token unconsumed → syntax
		// error. goopg has a single (heap) access method, so the name is accepted
		// and discarded; relam stays at its default and pg_dump emits no USING
		// clause, round-tripping the child like an access-method-less leaf.
		// M0110-0001 (DU-002 slice 193).
		if p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using") {
			if _, err := p.parseIdent(); err != nil {
				return nil, err
			}
		}
		// Optional WITH (storage params) on a partition child, e.g.
		// `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) WITH (fillfactor=70)`.
		// PG allows storage parameters on a leaf partition; the executor persists
		// them (pg_class.reloptions) so pg_dump round-trips the option. The clause
		// follows FOR VALUES (and any PARTITION BY). M0110-0001 (DU-002 slice 191).
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith {
			p.advance() // WITH
			opts, werr := p.parseWithOptions()
			if werr != nil {
				return nil, werr
			}
			stmt.With = opts
			if v, ok := opts["oids"]; ok {
				stmt.WithOIDS = !strings.EqualFold(v, "false") && v != "0"
			}
		}
		// ON COMMIT clause may follow FOR VALUES ... in partition tables.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn {
			p.advance()
			if p.acceptKeyword(KwCommit) {
				_ = p.acceptIdentKeyword("preserve") || p.acceptKeyword(KwDelete)
				_ = p.acceptIdentKeyword("rows") || p.acceptKeyword(KwDrop)
			}
		}
		// Optional TABLESPACE clause on a partition child, e.g.
		// `CREATE TABLE leaf PARTITION OF parent FOR VALUES IN (1) TABLESPACE pg_default`.
		// PG's CREATE TABLE ... PARTITION OF grammar admits OptTableSpace (after
		// OptWith / OnCommitOption); the partition-child arm previously omitted it,
		// so a trailing TABLESPACE left unconsumed tokens and the whole statement
		// failed with a syntax error. Mirror the non-partition path (line ~2248):
		// accept and discard the name — goopg's storage manager does not honour
		// tablespaces, so reltablespace stays 0 and pg_dump emits no TABLESPACE
		// clause for the default tablespace, round-tripping the child unchanged.
		// M0110-0001 (DU-002 slice 192).
		if p.acceptKeyword(KwTablespace) {
			if _, err := p.parseIdent(); err != nil {
				return nil, err
			}
		}
		return stmt, nil
	}

	// CREATE TABLE name WITH (opts) AS SELECT … (CTAS with pre-AS storage params).
	// Handle WITH clause that precedes AS rather than following column definitions.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith {
		savedIdx := p.idx
		p.advance()
		if opts, err := p.parseWithOptions(); err == nil {
			stmt.With = opts
			if v, ok := opts["oids"]; ok {
				stmt.WithOIDS = !strings.EqualFold(v, "false") && v != "0"
			}
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
				p.advance()
				sel, err2 := p.parseSelect()
				if err2 != nil {
					return nil, err2
				}
				if ss, ok := sel.(*SelectStmt); ok {
					stmt.SelectSource = ss
				}
				if p.acceptKeyword(KwWith) {
					if p.acceptIdentKeyword("no") {
						_ = p.acceptIdentKeyword("data")
						stmt.WithNoData = true
					} else {
						_ = p.acceptIdentKeyword("data")
					}
				}
				return stmt, nil
			}
			// WITH without following AS: fall through (opts already consumed).
		} else {
			p.idx = savedIdx
		}
	}

	// Regular CREATE TABLE with column definitions
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '('")
	}
	// Empty column list: `()` is valid for INHERITS children that add no columns.
	if p.acceptSymbol(")") {
		// Skip remaining clauses (PARTITION BY, INHERITS, WITH) after `()`.
		p.consumeCreateTableSuffix(stmt)
		return stmt, nil
	}
	for {
		// Table-level constraint: PRIMARY KEY ( cols ) [INCLUDE ( cols )].
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary {
			p.advance()
			if _, err := p.expectKeyword(KwKey); err != nil {
				return nil, err
			}
			if !p.acceptSymbol("(") {
				return nil, p.errAtCur("expected '(' after PRIMARY KEY")
			}
			cols, err := p.parseColumnNameList()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
			stmt.PrimaryKey = cols
			// Optional INCLUDE (col, …) — parse and store covering columns. M0097-0023.
			if p.acceptIdentKeyword("include") {
				if p.acceptSymbol("(") {
					for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
						if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
							stmt.PrimaryKeyInclude = append(stmt.PrimaryKeyInclude, p.cur().Value)
						}
						p.advance()
					}
				}
			}
			// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE].
			// Captured (not discarded) so pg_get_constraintdef re-emits the clause
			// and pg_constraint emits condeferrable/condeferred on dump. The flags
			// ride the backing tbl_pkey index built in the executor. DU-002 slice 142.
			p.parseConstraintDeferrable(&stmt.PrimaryKeyDeferrable, &stmt.PrimaryKeyInitiallyDeferred)
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique {
			// Table-level UNIQUE [NULLS NOT DISTINCT] (cols) [INCLUDE (incl)] —
			// create btree index. M0097-0028 / DU-002 slice 135.
			p.advance()
			// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+) precedes the column
			// list for a constraint (ruleutils.c emits `UNIQUE NULLS NOT DISTINCT
			// (cols)`), unlike CREATE INDEX where the clause trails the columns.
			nullsNotDistinct := false
			if p.acceptIdentKeyword("nulls") {
				nullsNotDistinct = p.acceptKeyword(KwNot)
				if !p.acceptKeyword(KwDistinct) {
					_ = p.acceptIdentKeyword("distinct")
				}
			}
			if p.acceptSymbol("(") {
				var cols []string
				for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
						cols = append(cols, p.cur().Value)
					}
					p.advance()
				}
				if len(cols) > 0 {
					stmt.TableUniques = append(stmt.TableUniques, cols)
					stmt.TableUniqueNullsNotDistinct = append(stmt.TableUniqueNullsNotDistinct, nullsNotDistinct)
					// Optional INCLUDE (col, …). M0097-0023.
					var incl []string
					if p.acceptIdentKeyword("include") {
						if p.acceptSymbol("(") {
							for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
								if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
									incl = append(incl, p.cur().Value)
								}
								p.advance()
							}
						}
					}
					stmt.TableUniqueIncludes = append(stmt.TableUniqueIncludes, incl)
					// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY
					// IMMEDIATE]. Captured (not discarded) so pg_get_constraintdef
					// re-emits the clause on dump. DU-002 slice 139.
					deferrable := false
					initiallyDeferred := false
					if p.acceptKeyword(KwNot) {
						_ = p.acceptKeyword(KwDeferrable)
					} else if p.acceptKeyword(KwDeferrable) {
						deferrable = true
					}
					if p.acceptIdentKeyword("initially") {
						if p.acceptIdentKeyword("deferred") {
							// INITIALLY DEFERRED implies DEFERRABLE.
							deferrable = true
							initiallyDeferred = true
						} else {
							_ = p.acceptIdentKeyword("immediate")
						}
					}
					stmt.TableUniqueDeferrable = append(stmt.TableUniqueDeferrable, deferrable)
					stmt.TableUniqueInitiallyDeferred = append(stmt.TableUniqueInitiallyDeferred, initiallyDeferred)
				}
			}
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
			// Table-level CHECK (expr) [NOT ENFORCED | ENFORCED] [NO INHERIT]. M0097-0014.
			p.advance()
			expr, err := p.parseCheckExpr()
			if err != nil {
				return nil, err
			}
			stmt.TableChecks = append(stmt.TableChecks, expr)
			// Accept optional NOT ENFORCED / ENFORCED modifier.
			if p.acceptKeyword(KwNot) {
				_ = p.acceptIdentKeyword("enforced")
			} else {
				_ = p.acceptIdentKeyword("enforced")
			}
			// Accept optional NO INHERIT, recording it per-check so the suffix
			// round-trips through the dump. DU-002 slice 128.
			noInherit := false
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("inherit")
				stmt.TableHasNoInheritCheck = true
				noInherit = true
			}
			stmt.TableCheckNoInherit = append(stmt.TableCheckNoInherit, noInherit)
		} else if p.acceptIdentKeyword("exclude") {
			// Anonymous EXCLUDE USING method (col WITH op) [INCLUDE (cols)]. M0097-0023.
			cdef := p.parseExcludeConstraint()
			// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE].
			// Captured onto the constraint def so the executor threads it onto the
			// backing exclusion index and pg_get_constraintdef / pg_constraint
			// re-emit the clause on dump. DU-002 slice 143.
			p.parseConstraintDeferrable(&cdef.Deferrable, &cdef.InitiallyDeferred)
			stmt.TableExclusions = append(stmt.TableExclusions, cdef)
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign {
			// Anonymous table-level FOREIGN KEY (cols) REFERENCES t (cols) … —
			// the multi-column sibling of the inline column REFERENCES clause.
			// PG auto-names it <table>_<firstcol>_fkey at execution. DU-002 slice 53.
			fk, err := p.parseTableForeignKey("")
			if err != nil {
				return nil, err
			}
			stmt.TableForeignKeys = append(stmt.TableForeignKeys, fk)
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwConstraint {
			// Table-level CONSTRAINT name (PRIMARY KEY | UNIQUE | CHECK | FOREIGN KEY).
			p.advance() // CONSTRAINT
			cNameTok, _ := p.parseIdent()
			constraintName := cNameTok.Value
			switch {
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
				p.advance()
				if _, err := p.expectKeyword(KwKey); err != nil {
					return nil, err
				}
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after PRIMARY KEY")
				}
				cols, err := p.parseColumnNameList()
				if err != nil {
					return nil, err
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
				stmt.PrimaryKey = cols
				cdef := TableConstraintDef{Name: constraintName, Columns: cols, IsPrimary: true}
				// Optional INCLUDE (col, …) — parse and store covering columns.
				if p.acceptIdentKeyword("include") {
					if p.acceptSymbol("(") {
						for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
							if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
								cdef.IncludeColumns = append(cdef.IncludeColumns, p.cur().Value)
							}
							p.advance()
						}
					}
				}
				// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE].
				// Captured (not discarded) onto the constraint def so the executor
				// threads it onto the backing index (shared NamedConstraints loop) and
				// pg_get_constraintdef / pg_constraint re-emit the clause on dump.
				// Parsed before the append so cdef carries the flags. DU-002 slice 142.
				p.parseConstraintDeferrable(&cdef.Deferrable, &cdef.InitiallyDeferred)
				if constraintName != "" {
					stmt.NamedConstraints = append(stmt.NamedConstraints, cdef)
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
				// CONSTRAINT name UNIQUE [NULLS [NOT] DISTINCT] (cols) [INCLUDE (cols)]
				// M0097-0028 / DU-002 slice 138.
				p.advance()
				// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+) precedes the column
				// list, mirroring the anonymous table-level UNIQUE form above. Without
				// this the `(` lookahead failed and the whole named constraint was
				// silently dropped from the table (and thus the dump).
				namedNullsNotDistinct := false
				if p.acceptIdentKeyword("nulls") {
					namedNullsNotDistinct = p.acceptKeyword(KwNot)
					if !p.acceptKeyword(KwDistinct) {
						_ = p.acceptIdentKeyword("distinct")
					}
				}
				if p.acceptSymbol("(") {
					var cols []string
					for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
						if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
							cols = append(cols, p.cur().Value)
						}
						p.advance()
					}
					if len(cols) > 0 {
						cdef := TableConstraintDef{Name: constraintName, Columns: cols, NullsNotDistinct: namedNullsNotDistinct}
						// Optional INCLUDE (col, …).
						if p.acceptIdentKeyword("include") {
							if p.acceptSymbol("(") {
								for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
									if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
										cdef.IncludeColumns = append(cdef.IncludeColumns, p.cur().Value)
									}
									p.advance()
								}
							}
						}
						// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY
						// IMMEDIATE] trailer. Mirrors the anonymous table-level UNIQUE
						// form (slice 139); without this branch a named
						// `UNIQUE (a) DEFERRABLE …` was a HARD PARSE ERROR (trailing
						// tokens after the column list). DU-002 slice 140.
						if p.acceptKeyword(KwNot) {
							_ = p.acceptKeyword(KwDeferrable)
							// NOT DEFERRABLE → both flags stay false (default).
						} else if p.acceptKeyword(KwDeferrable) {
							cdef.Deferrable = true
							if p.acceptIdentKeyword("initially") {
								if p.acceptIdentKeyword("deferred") {
									cdef.InitiallyDeferred = true
								} else {
									_ = p.acceptIdentKeyword("immediate")
								}
							}
						} else if p.acceptIdentKeyword("initially") {
							// Bare INITIALLY DEFERRED implies DEFERRABLE.
							if p.acceptIdentKeyword("deferred") {
								cdef.Deferrable = true
								cdef.InitiallyDeferred = true
							} else {
								_ = p.acceptIdentKeyword("immediate")
							}
						}
						if constraintName != "" {
							stmt.NamedConstraints = append(stmt.NamedConstraints, cdef)
						} else {
							stmt.TableUniques = append(stmt.TableUniques, cols)
						}
					}
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
				p.advance()
				expr, err := p.parseCheckExpr()
				if err != nil {
					return nil, err
				}
				anonCheck := constraintName == ""
				if !anonCheck {
					// Named CHECK constraint: store with its name for pg_constraint.
					stmt.TableNamedChecks = append(stmt.TableNamedChecks, PartitionCheckConstraint{
						Name: constraintName, Expr: expr,
					})
				} else {
					stmt.TableChecks = append(stmt.TableChecks, expr)
				}
				if p.acceptKeyword(KwNot) {
					_ = p.acceptIdentKeyword("enforced")
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
				// Accept optional NO INHERIT (CONSTRAINT name CHECK NO INHERIT).
				noInherit := false
				if p.acceptIdentKeyword("no") {
					_ = p.acceptIdentKeyword("inherit")
					stmt.TableHasNoInheritCheck = true
					noInherit = true
				}
				// Keep TableCheckNoInherit parallel to TableChecks: only the
				// anonymous branch appended an expr. DU-002 slice 128.
				if anonCheck {
					stmt.TableCheckNoInherit = append(stmt.TableCheckNoInherit, noInherit)
				} else if noInherit && len(stmt.TableNamedChecks) > 0 {
					// Named branch appended before NO INHERIT was parsed; carry the
					// per-constraint flag so the named NO-INHERIT check re-emits the
					// suffix on dump. DU-002 slice 129.
					stmt.TableNamedChecks[len(stmt.TableNamedChecks)-1].NoInherit = true
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign:
				// CONSTRAINT name FOREIGN KEY (cols) REFERENCES t (cols) … —
				// table-level (possibly composite) FK. DU-002 slice 53.
				fk, err := p.parseTableForeignKey(constraintName)
				if err != nil {
					return nil, err
				}
				stmt.TableForeignKeys = append(stmt.TableForeignKeys, fk)
			case p.acceptIdentKeyword("exclude"):
				// CONSTRAINT name EXCLUDE USING method (col WITH op) [INCLUDE (cols)]. M0097-0023.
				cdef := p.parseExcludeConstraint()
				cdef.Name = constraintName
				// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY
				// IMMEDIATE] — captured (not discarded) so the executor threads it
				// onto the backing exclusion index via the shared NamedConstraints
				// loop and the dump re-emits the clause. DU-002 slice 143.
				p.parseConstraintDeferrable(&cdef.Deferrable, &cdef.InitiallyDeferred)
				stmt.NamedConstraints = append(stmt.NamedConstraints, cdef)
			default:
				// Unknown constraint type: skip to next comma/close-paren.
				for p.cur().Kind != TokenEOF {
					t := p.cur()
					if (t.Kind == TokenSymbol && t.Value == ")") ||
						(t.Kind == TokenSymbol && t.Value == ",") {
						break
					}
					p.advance()
				}
			}
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwLike {
			// LIKE source_table [INCLUDING/EXCLUDING option …] — copy columns. M0097-0069.
			p.advance() // consume LIKE
			srcName, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			stmt.LikeTables = append(stmt.LikeTables, srcName)
			likeKey := "@@LIKE:" + srcName.String()
			// Consume optional INCLUDING/EXCLUDING clauses.
			// Track whether IDENTITY or GENERATED or ALL is included to copy identity/generated columns.
			for {
				isIncluding := p.acceptIdentKeyword("including")
				isExcluding := !isIncluding && p.acceptIdentKeyword("excluding")
				if !isIncluding && !isExcluding {
					break
				}
				switch {
				case p.acceptIdentKeyword("defaults"):
					if isIncluding {
						likeKey += ":+defaults"
					}
				case p.acceptIdentKeyword("constraints"):
					if isIncluding {
						likeKey += ":+constraints"
					}
				case p.acceptIdentKeyword("indexes"):
					if isIncluding {
						likeKey += ":+indexes"
					}
				case p.acceptIdentKeyword("identity"):
					if isIncluding {
						likeKey += ":+identity"
					}
				case p.acceptIdentKeyword("comments"):
					if isIncluding {
						likeKey += ":+comments"
					}
				case p.acceptIdentKeyword("statistics"):
				case p.acceptIdentKeyword("storage"):
					if isIncluding {
						likeKey += ":+storage"
					}
				case p.acceptIdentKeyword("generated"):
					if isIncluding {
						likeKey += ":+generated"
					}
				case p.acceptIdentKeyword("statistics"):
					if isIncluding {
						likeKey += ":+statistics"
					}
				case p.acceptKeyword(KwAll):
					if isIncluding {
						likeKey += ":+defaults:+identity:+generated:+constraints:+indexes:+comments:+statistics:+storage"
					}
				}
			}
			stmt.BodyOrder = append(stmt.BodyOrder, likeKey)
		} else {
			col, err := p.parseColumnDef()
			if err != nil {
				return nil, err
			}
			stmt.Columns = append(stmt.Columns, col)
			stmt.BodyOrder = append(stmt.BodyOrder, col.Name)
			if col.Primary {
				stmt.PrimaryKey = append(stmt.PrimaryKey, col.Name)
			}
		}
		if p.acceptSymbol(",") {
			continue
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')'")
		}
		break
	}
	// INHERITS (parent [, …]) — table inheritance. Must come before PARTITION BY
	// to match PG grammar order.
	if p.acceptIdentKeyword("inherits") {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after INHERITS")
		}
		for {
			name, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			stmt.Inherits = append(stmt.Inherits, name)
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
	}
	// Optional PARTITION BY {LIST|RANGE|HASH} (col, …) (M0096-0007)
	if p.acceptKeyword(KwPartition) {
		if _, err := p.expectKeyword(KwBy); err != nil {
			return nil, err
		}
		method := ""
		switch {
		case p.acceptIdentKeyword("list"):
			method = "LIST"
		case p.acceptIdentKeyword("range"):
			method = "RANGE"
		case p.acceptIdentKeyword("hash"):
			method = "HASH"
		default:
			// Accept any identifier; executor validates the strategy name.
			if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
				method = p.cur().Value
				p.advance()
			} else {
				return nil, p.errAtCur("expected partition strategy after PARTITION BY")
			}
		}
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after partition method")
		}
		// Parse column names (or expressions) with optional operator class names. M0097-0015/M0097-0027/M0097-0023.
		keyCols, keyExprs, opClasses, colls, err2 := p.parsePartitionKeyCols()
		if err2 != nil {
			return nil, err2
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		stmt.PartitionBy = &PartitionByClause{pos: pos, Method: method, KeyCols: keyCols, KeyExprs: keyExprs, OpClasses: opClasses, Collations: colls}
	}
	if p.acceptKeyword(KwWith) {
		opts, err := p.parseWithOptions()
		if err != nil {
			return nil, err
		}
		stmt.With = opts
	}
	if p.acceptKeyword(KwTablespace) {
		// Accept and discard for v0; the storage manager doesn't
		// honour tablespaces yet.
		if _, err := p.parseIdent(); err != nil {
			return nil, err
		}
	}
	// ON COMMIT { PRESERVE ROWS | DELETE ROWS | DROP } for temp tables.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn {
		p.advance()
		if p.acceptKeyword(KwCommit) {
			_ = p.acceptIdentKeyword("preserve") || p.acceptKeyword(KwDelete)
			_ = p.acceptIdentKeyword("rows") || p.acceptKeyword(KwDrop)
		}
	}
	return stmt, nil
}

// consumeCreateTableSuffix skips INHERITS, PARTITION BY, WITH, and TABLESPACE
// clauses after a column list that has already been closed with ')'.
// Used when the column list is empty. M0096-0009.
func (p *parser) consumeCreateTableSuffix(stmt *CreateTableStmt) {
	for {
		switch {
		case p.acceptIdentKeyword("inherits"):
			if p.acceptSymbol("(") {
				for {
					name, err := p.parseObjectName()
					if err != nil {
						return
					}
					stmt.Inherits = append(stmt.Inherits, name)
					if !p.acceptSymbol(",") {
						break
					}
				}
				_ = p.acceptSymbol(")")
			}
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPartition:
			p.advance()
			_, _ = p.expectKeyword(KwBy)
			_ = p.acceptIdentKeyword("list") || p.acceptIdentKeyword("range") || p.acceptIdentKeyword("hash")
			if p.acceptSymbol("(") {
				_, _ = p.parseColumnNameList()
				_ = p.acceptSymbol(")")
			}
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith:
			p.advance()
			// WITH OIDS (no parens) is a syntax error in PG; only WITH (oids) is accepted.
			// If parseWithOptions fails (no '('), the token stays unread, the default case
			// fires, and the upper-level parser emits "syntax error at or near OIDS".
			opts, _ := p.parseWithOptions()
			if v, ok := opts[strings.ToLower("oids")]; ok {
				stmt.WithOIDS = !strings.EqualFold(v, "false") && v != "0"
			}
		case p.acceptIdentKeyword("without"):
			// WITHOUT OIDS — accepted and silently ignored.
			_ = p.acceptIdentKeyword("oids")
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTablespace:
			p.advance()
			_, _ = p.parseIdent()
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn:
			// ON COMMIT { PRESERVE ROWS | DELETE ROWS | DROP } — temp table option.
			p.advance()
			if !p.acceptKeyword(KwCommit) {
				return
			}
			_ = p.acceptIdentKeyword("preserve") || p.acceptKeyword(KwDelete)
			_ = p.acceptIdentKeyword("rows") || p.acceptKeyword(KwDrop)
		default:
			return
		}
	}
}

// parsePartitionKeyCols parses the column-list (and possibly expression-list)
// inside PARTITION BY (key1, key2, ...). Each key may be either a plain column
// name or a parenthesised expression such as (abs(b)) or ((a+b)/2). M0097-0023.
func (p *parser) parsePartitionKeyCols() (keyCols []string, keyExprs []Expr, opClasses []string, collations []string, err error) {
	for {
		var colName string
		var expr Expr
		var collation string
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			// Explicitly parenthesised expression key: (b+1), (a+b), etc.
			p.advance()
			expr, err = p.parseExpr()
			if err != nil {
				return
			}
			if !p.acceptSymbol(")") {
				err = p.errAtCur("expected ')' to close partition key expression")
				return
			}
		} else {
			// Parse a full expression.  For plain column names this yields a
			// ColumnRef; for function-call keys like abs(a) or COLLATE expressions
			// like (a collate "C") we get a richer node.
			expr, err = p.parseExpr()
			if err != nil {
				return
			}
			// Unwrap: ColumnRef → plain column name (most common case).
			// CollateExpr wrapping a ColumnRef → use column name + store collation.
			switch v := expr.(type) {
			case *ColumnRef:
				colName = v.Column
				expr = nil
			case *CollateExpr:
				if cr, ok := v.Operand.(*ColumnRef); ok {
					colName = cr.Column
					collation = v.CollationName
					expr = nil
				}
			}
		}
		keyCols = append(keyCols, colName)
		keyExprs = append(keyExprs, expr)
		// Optional operator class name (e.g. part_test_int4_ops). M0097-0027.
		opClass := ""
		if p.cur().Kind == TokenIdent {
			opClass = p.cur().Value
			p.advance()
		}
		opClasses = append(opClasses, opClass)
		collations = append(collations, collation)
		if !p.acceptSymbol(",") {
			break
		}
	}
	return
}

// parsePartitionBoundValues parses a comma-separated list of partition bound
// values, which may include MINVALUE/MAXVALUE keywords.
func (p *parser) parsePartitionBoundValues() ([]Expr, error) {
	var vals []Expr
	for {
		// MINVALUE / MAXVALUE are the unbounded-edge keywords, encoded as a
		// dedicated PartitionRangeBoundKeyword node so they never collide with the
		// quoted text literals 'MINVALUE' / 'MAXVALUE' (which parse as StringConst
		// via parseExpr below). DU-002 slice 261.
		kwPos := p.cur().Pos
		if p.acceptIdentKeyword("minvalue") {
			vals = append(vals, &PartitionRangeBoundKeyword{pos: kwPos, IsMax: false})
		} else if p.acceptIdentKeyword("maxvalue") {
			vals = append(vals, &PartitionRangeBoundKeyword{pos: kwPos, IsMax: true})
		} else {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			vals = append(vals, e)
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return vals, nil
}

// normalizeCompressionMethod canonicalizes a `COMPRESSION <method>` argument for
// storage on the catalog column. PG accepts "pglz", "lz4", and "default"
// (case-insensitive); "default" resets attcompression to '\0' (the
// default_toast_compression GUC applies) and is recorded as the empty string, so
// no SET COMPRESSION clause is dumped. Any other / empty token also normalizes to
// "" — goopg does not enforce the method, this is dump-fidelity only.
// DU-002 slice 183.
func normalizeCompressionMethod(method string) string {
	switch strings.ToLower(method) {
	case "pglz":
		return "pglz"
	case "lz4":
		return "lz4"
	default:
		return ""
	}
}

// parseColumnDef parses `name TYPE [NOT NULL | PRIMARY KEY]`.
// joinGeneratedExprTokens reconstructs the canonical SQL text of a
// `GENERATED ALWAYS AS (...)` expression from its captured token stream. PG
// stores the generation expression as a parsed node and pg_dump re-emits it via
// pg_get_expr, which renders function calls tightly (`upper(fn)`,
// `coalesce(a, b)`) and precedence-grouping parens tightly (`(a + b) * 2`) while
// spacing binary operators. A naive strings.Join(toks, " ") produced
// `upper ( fn )` / `( a + b ) * 2`, diverging from pg_get_expr and breaking the
// pg_dump round-trip for any function-call or parenthesised generation
// expression. This join keys off punctuation to match pg_get_expr's spacing for
// the operator / function-call / grouping-paren / qualified-name surface goopg
// supports. The result re-parses to the same node, so evalGeneratedExpr (which
// re-parses the stored string) is unaffected. DU-002 slice 289.
//
// String-literal tokens get special handling (DU-002 slice 294): the lexer
// stores a literal's UNQUOTED body (`'-'` → Value "-"), so they must be
// re-quoted (with embedded single quotes doubled) to reproduce pg_get_expr's
// rendering — otherwise `concat(ka, '-', la)` would round-trip as the malformed
// `concat(ka, -, la)`. The punctuation spacing rules are also gated on
// TokenSymbol so a literal whose body happens to be ")"/","/"("/"." can never be
// mistaken for the matching punctuator; for the operator/call/grouping-paren
// expressions of slices 283–293 (which contain no string literals) every
// punctuator is already a TokenSymbol, so this gating is a no-op there.
func joinGeneratedExprTokens(toks []Token) string {
	// renderTok reproduces a token's canonical SQL text: string literals are
	// re-quoted (their stored body is unquoted), everything else is verbatim.
	renderTok := func(t Token) string {
		if t.Kind == TokenStringLit {
			return "'" + strings.ReplaceAll(t.Value, "'", "''") + "'"
		}
		return t.Value
	}
	var b strings.Builder
	for i, t := range toks {
		if i == 0 {
			b.WriteString(renderTok(t))
			continue
		}
		prev := toks[i-1]
		noSpace := false
		// The punctuation spacing rules apply to SYMBOL tokens only — a string
		// literal whose body is ")"/","/"("/"." must not trigger them.
		if t.Kind == TokenSymbol {
			switch t.Value {
			case ")", ",":
				// Never a space before a close-paren or an argument comma.
				noSpace = true
			case "(":
				// Tight before a call paren (prev is a function name or a closing
				// paren); spaced before a grouping paren (prev is an operator,
				// keyword, or open paren).
				if prev.Kind == TokenIdent || prev.Kind == TokenQuotedIdent || (prev.Kind == TokenSymbol && prev.Value == ")") {
					noSpace = true
				}
			case ".":
				// Qualified name (`schema.func`): no space around the dot.
				noSpace = true
			}
		}
		// Tight after an open paren or a dot; the comma's trailing space is
		// supplied by the default branch (the token after a comma is spaced).
		if prev.Kind == TokenSymbol && (prev.Value == "(" || prev.Value == ".") {
			noSpace = true
		}
		if !noSpace {
			b.WriteByte(' ')
		}
		b.WriteString(renderTok(t))
	}
	return b.String()
}

func (p *parser) parseColumnDef() (ColumnDef, error) {
	pos := p.cur().Pos
	nameTok, err := p.parseIdent()
	if err != nil {
		return ColumnDef{}, err
	}
	colType, err := p.parseColumnType()
	if err != nil {
		return ColumnDef{}, err
	}
	col := ColumnDef{pos: pos, Name: identText(nameTok), Type: colType}
	for {
		switch {
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot:
			p.advance()
			if _, err := p.expectKeyword(KwNull); err != nil {
				return ColumnDef{}, err
			}
			col.NotNull = true
			// Optional NO INHERIT — PG18 NOT NULL constraints may carry NO INHERIT.
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("inherit")
				col.NotNullNoInherit = true
			}
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
			p.advance()
			if _, err := p.expectKeyword(KwKey); err != nil {
				return ColumnDef{}, err
			}
			col.Primary = true
			col.NotNull = true
			// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]
			// trailer — captured so pg_get_constraintdef / pg_dump re-emit the clause
			// (rides the backing tbl_pkey index built in the executor). Without this
			// the keyword fell through to the column-constraint loop's default arm and
			// failed the whole CREATE TABLE. DU-002 slice 142.
			p.parseConstraintDeferrable(&col.PrimaryDeferrable, &col.PrimaryInitiallyDeferred)
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNull:
			p.advance() // NULL is the default; absorb it
		// COLLATE collation_name — capture the collation so the column round-trips
		// through pg_dump (attcollation in pg_attribute → re-emitted COLLATE clause).
		// goopg v0 does not actually collate; the name is recorded for dump fidelity
		// only. Accepts an optional schema qualifier (`pg_catalog."C"`); we keep the
		// bare last component, matching pg_collation.collname. DU-002 slice 188
		// (was M0097-0071: previously discarded).
		case p.acceptIdentKeyword("collate"):
			// parseCollationName accepts an optional schema qualifier
			// (`pg_catalog."C"`) and returns the trailing component unquoted.
			collName, _ := p.parseCollationName()
			col.Collation = collName
		// COMPRESSION method — column-level compression method (PG 14+). goopg does
		// not actually TOAST/compress, but records the method so the column
		// round-trips through pg_dump (which re-emits a SET COMPRESSION clause for
		// pglz/lz4). DU-002 slice 183.
		case p.acceptIdentKeyword("compression"):
			method, _ := p.parseIdent() // consume method name (pglz, lz4, default, etc.)
			col.Compression = normalizeCompressionMethod(method.Value)
		// GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY or GENERATED ALWAYS AS (expr) STORED (M0096-0008)
		case p.acceptIdentKeyword("generated"):
			isAlways := p.acceptIdentKeyword("always")
			isByDefault := !isAlways && (p.acceptKeyword(KwBy) && p.acceptKeyword(KwDefault))
			if !isAlways && !isByDefault {
				return ColumnDef{}, p.errAtCur("expected ALWAYS or BY DEFAULT after GENERATED")
			}
			if _, err := p.expectKeyword(KwAs); err != nil {
				return ColumnDef{}, err
			}
			// GENERATED ALWAYS AS IDENTITY [(sequence_options)] — identity column.
			if p.acceptIdentKeyword("identity") {
				col.IdentityColumn = true
				col.IdentityAlways = isAlways
				// Parse optional sequence options: (START WITH n INCREMENT BY m ...)
				// We capture START WITH to initialize the sequence correctly.
				if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
					depth := 1
					p.advance()
					for depth > 0 && p.cur().Kind != TokenEOF {
						if p.cur().Kind == TokenSymbol {
							if p.cur().Value == "(" {
								depth++
							} else if p.cur().Value == ")" {
								depth--
								if depth == 0 {
									p.advance()
									break
								}
							}
						}
						// Capture START WITH value.
						if depth == 1 && p.cur().Kind == TokenIdent &&
							strings.EqualFold(p.cur().Value, "start") {
							p.advance() // consume START
							// WITH is a reserved keyword (KwWith), so accept both forms.
							_ = p.acceptKeyword(KwWith) || p.acceptIdentKeyword("with")
							neg := p.cur().Kind == TokenSymbol && p.cur().Value == "-"
							if neg {
								p.advance()
							}
							if p.cur().Kind == TokenIntLit {
								if v, err2 := strconv.ParseInt(p.cur().Value, 10, 64); err2 == nil {
									if neg {
										col.IdentityStart = -v
									} else {
										col.IdentityStart = v
									}
								}
							}
						}
						p.advance()
					}
				}
				continue
			}
			if !p.acceptSymbol("(") {
				return ColumnDef{}, p.errAtCur("expected '(' after GENERATED ALWAYS AS")
			}
			// Collect the raw expression tokens (kind + value) so the canonical
			// join below can reproduce pg_get_expr's spacing — tight function
			// calls (`upper(fn)`) and grouping parens (`(a + b) * 2`), spaced
			// binary operators. DU-002 slice 289.
			depth := 1
			start := p.cur().Pos
			var exprToks []Token
			for depth > 0 && p.cur().Kind != TokenEOF {
				t := p.cur()
				if t.Kind == TokenSymbol && t.Value == "(" {
					depth++
				} else if t.Kind == TokenSymbol && t.Value == ")" {
					depth--
					if depth == 0 {
						break
					}
				}
				exprToks = append(exprToks, t)
				p.advance()
				_ = start
			}
			if !p.acceptSymbol(")") {
				return ColumnDef{}, p.errAtCur("expected ')' to close generated expression")
			}
			// Storage strategy: `STORED` or `VIRTUAL`. PG18's default (when neither
			// keyword is given) is VIRTUAL. goopg materializes every generated
			// column on write regardless, but records the declared strategy so
			// pg_attribute.attgenerated reports the PG-faithful 'v'/'s' code and
			// pg_dump re-emits the original form. DU-002 slice 194.
			if p.acceptIdentKeyword("stored") {
				col.GeneratedVirtual = false
			} else {
				// `VIRTUAL` keyword (consumed) or omitted — both mean VIRTUAL.
				_ = p.acceptIdentKeyword("virtual")
				col.GeneratedVirtual = true
			}
			col.GeneratedAlways = true
			col.GeneratedExpr = joinGeneratedExprTokens(exprToks)
		// WITH OPTIONS modifier in PARTITION OF column override (M0096-0007)
		case p.acceptIdentKeyword("with"):
			if p.acceptIdentKeyword("options") {
				// Re-enter constraint parsing for the column override.
			}
		// DEFAULT clause — capture the expression AST so the apply worker
		// can evaluate it when filling subscriber-extra columns at
		// INSERT time (M0103-0007 rung 13). Generated columns don't
		// take DEFAULT; the GENERATED ALWAYS arm above runs first.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault:
			p.advance()
			expr, err := p.parseExpr()
			if err != nil {
				return ColumnDef{}, err
			}
			col.DefaultExpr = expr
		// REFERENCES — parse FK constraint and populate col FK fields. M0096-0011.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReferences:
			p.advance()
			refTable, err := p.parseObjectName()
			if err != nil {
				return ColumnDef{}, err
			}
			col.RefTable = refTable
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				refCols, err := p.parseColumnNameList()
				if err != nil {
					return ColumnDef{}, err
				}
				if !p.acceptSymbol(")") {
					return ColumnDef{}, p.errAtCur("expected ')'")
				}
				col.RefColumns = refCols
			}
			// Parse ON DELETE / ON UPDATE clauses. ON is KwOn (reserved).
			for p.acceptKeyword(KwOn) {
				isDelete := p.acceptKeyword(KwDelete)
				if !isDelete {
					_ = p.acceptKeyword(KwUpdate)
				}
				action := parseFKAction(p)
				if isDelete {
					col.OnDelete = action
				} else {
					col.OnUpdate = action
				}
			}
			// Parse [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE].
			// Also accept bare INITIALLY DEFERRED (implicit DEFERRABLE).
			if p.acceptKeyword(KwNot) {
				_, _ = p.expectKeyword(KwDeferrable)
				col.FKDeferrable = false
			} else if p.acceptKeyword(KwDeferrable) {
				col.FKDeferrable = true
				if p.acceptIdentKeyword("initially") {
					col.FKInitiallyDeferred = p.acceptIdentKeyword("deferred")
					_ = p.acceptIdentKeyword("immediate")
				}
			} else if p.acceptIdentKeyword("initially") {
				col.FKDeferrable = true
				col.FKInitiallyDeferred = p.acceptIdentKeyword("deferred")
				_ = p.acceptIdentKeyword("immediate")
			}
		// UNIQUE constraint on column
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
			p.advance()
			col.Unique = true
			// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+) follows the keyword
			// for an inline column UNIQUE. Threaded into catalog.Index so
			// pg_get_constraintdef re-emits `UNIQUE NULLS NOT DISTINCT (col)`.
			// DU-002 slice 136.
			if p.acceptIdentKeyword("nulls") {
				col.UniqueNullsNotDistinct = p.acceptKeyword(KwNot)
				if !p.acceptKeyword(KwDistinct) {
					_ = p.acceptIdentKeyword("distinct")
				}
			}
			// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]
			// trailer on the inline column UNIQUE. Without this, a trailing
			// DEFERRABLE fell through to the default arm and became a HARD PARSE
			// ERROR for the whole CREATE TABLE. DU-002 slice 141.
			p.parseConstraintDeferrable(&col.UniqueDeferrable, &col.UniqueInitiallyDeferred)
		// CHECK (expr) inline column constraint. M0097-0014.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
			p.advance()
			expr, err := p.parseCheckExpr()
			if err != nil {
				return ColumnDef{}, err
			}
			col.CheckExpr = expr
			// Accept optional NOT ENFORCED / ENFORCED.
			if p.acceptKeyword(KwNot) {
				_ = p.acceptIdentKeyword("enforced")
			} else {
				_ = p.acceptIdentKeyword("enforced")
			}
			// Accept optional NO INHERIT — PG18 column-level CHECK NO INHERIT.
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("inherit")
				col.CheckNoInherit = true
			}
		// CONSTRAINT name CHECK/PRIMARY KEY/UNIQUE/... column constraint.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwConstraint:
			p.advance()                   // CONSTRAINT
			cnameTok, _ := p.parseIdent() // constraint name
			switch {
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
				p.advance()
				expr, err := p.parseCheckExpr()
				if err != nil {
					return ColumnDef{}, err
				}
				col.CheckExpr = expr
				if p.acceptKeyword(KwNot) {
					_ = p.acceptIdentKeyword("enforced")
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
				// Accept optional NO INHERIT (CONSTRAINT name CHECK NO INHERIT).
				if p.acceptIdentKeyword("no") {
					_ = p.acceptIdentKeyword("inherit")
					col.CheckNoInherit = true
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
				// CONSTRAINT name PRIMARY KEY
				p.advance()
				if _, err := p.expectKeyword(KwKey); err != nil {
					return ColumnDef{}, err
				}
				col.Primary = true
				col.NotNull = true
				// Optional DEFERRABLE trailer (named inline column PRIMARY KEY).
				// DU-002 slice 142.
				p.parseConstraintDeferrable(&col.PrimaryDeferrable, &col.PrimaryInitiallyDeferred)
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
				// CONSTRAINT name UNIQUE [NULLS [NOT] DISTINCT] — named inline
				// column UNIQUE. Set col.Unique so the executor creates the
				// backing index, and carry the explicit constraint name so
				// pg_dump round-trips `ADD CONSTRAINT name UNIQUE (col)`.
				// DU-002 slice 137.
				p.advance()
				col.Unique = true
				col.UniqueConstraintName = identText(cnameTok)
				if p.acceptIdentKeyword("nulls") {
					col.UniqueNullsNotDistinct = p.acceptKeyword(KwNot)
					if !p.acceptKeyword(KwDistinct) {
						_ = p.acceptIdentKeyword("distinct")
					}
				}
				// Optional DEFERRABLE trailer (named inline column UNIQUE).
				// DU-002 slice 141.
				p.parseConstraintDeferrable(&col.UniqueDeferrable, &col.UniqueInitiallyDeferred)
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot:
				// CONSTRAINT name NOT NULL [NO INHERIT] — named inline NOT NULL.
				// PG18 lets a column carry an explicitly named NOT NULL; when the
				// name differs from the auto-name (<table>_<col>_not_null) pg_dump
				// re-emits `<col> <type> CONSTRAINT <name> NOT NULL`. Capture the
				// name + optional NO INHERIT so the constraint round-trips with its
				// user-given name instead of being dropped by the default skip arm.
				// DU-002 slice 273.
				p.advance() // NOT
				if _, err := p.expectKeyword(KwNull); err != nil {
					return ColumnDef{}, err
				}
				col.NotNull = true
				col.NotNullConstraintName = identText(cnameTok)
				if p.acceptIdentKeyword("no") {
					_ = p.acceptIdentKeyword("inherit")
					col.NotNullNoInherit = true
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReferences:
				// CONSTRAINT name REFERENCES table (cols) — FK; parsed below.
				// Fall through to FK parsing path by continuing the outer loop.
				continue
			default:
				// Other named constraints: skip to end of constraint.
				for p.cur().Kind != TokenEOF {
					t := p.cur()
					if (t.Kind == TokenSymbol && t.Value == ",") ||
						(t.Kind == TokenSymbol && t.Value == ")") {
						break
					}
					p.advance()
				}
			}
		default:
			return col, nil
		}
	}
}

// parseColumnType parses the type name plus an optional ( N [, N …] )
// argument list. v0 keeps args as int64; non-integer args (numeric(10,
// 2.5) say) are rejected here.
func (p *parser) parseColumnType() (ColumnType, error) {
	pos := p.cur().Pos
	first, err := p.parseIdent()
	if err != nil {
		return ColumnType{}, err
	}
	ct := ColumnType{pos: pos, Name: identText(first)}

	// Schema-qualified type: e.g. pg_catalog.int4
	if p.acceptSymbol(".") {
		second, err := p.parseIdent()
		if err != nil {
			return ColumnType{}, err
		}
		ct.Schema = ct.Name
		ct.Name = identText(second)
	} else {
		// Handle multi-word type names: double precision, character varying,
		// timestamp/time with/without time zone, bit varying, etc.
		switch strings.ToLower(ct.Name) {
		case "double":
			if p.acceptIdentKeyword("precision") {
				ct.Name = "float8"
			}
		case "character":
			if p.acceptIdentKeyword("varying") {
				ct.Name = "varchar"
			}
		case "bit":
			if p.acceptIdentKeyword("varying") {
				ct.Name = "varbit"
			}
		case "timestamp":
			if p.acceptKeyword(KwWith) {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamptz"
			} else if p.acceptIdentKeyword("without") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamp"
			}
		case "time":
			if p.acceptKeyword(KwWith) {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timetz"
			} else if p.acceptIdentKeyword("without") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "time"
			}
		}
	}
	if p.acceptSymbol("(") {
		for {
			t := p.cur()
			if t.Kind != TokenIntLit {
				return ColumnType{}, p.errAtCur("expected integer in type modifier")
			}
			p.advance()
			n, err := strconv.ParseInt(t.Value, 10, 64)
			if err != nil {
				return ColumnType{}, &SyntaxError{Pos: t.Pos, Message: "invalid integer: " + t.Value}
			}
			ct.Args = append(ct.Args, n)
			if p.acceptSymbol(",") {
				continue
			}
			if !p.acceptSymbol(")") {
				return ColumnType{}, p.errAtCur("expected ',' or ')'")
			}
			break
		}
	}
	// Handle "time(N) with/without time zone" and "timestamp(N) with/without time zone"
	// where the timezone qualifier follows the typmod parentheses.
	if len(ct.Args) > 0 {
		switch strings.ToLower(ct.Name) {
		case "time":
			if p.acceptKeyword(KwWith) {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timetz"
			} else if p.acceptIdentKeyword("without") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "time"
			}
		case "timestamp":
			if p.acceptKeyword(KwWith) {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamptz"
			} else if p.acceptIdentKeyword("without") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamp"
			}
		}
	}
	if first.Kind != TokenQuotedIdent && strings.EqualFold(ct.Name, "char") && len(ct.Args) == 0 {
		ct.Args = []int64{1}
	}
	// Accept trailing [] array notation (e.g. int[], text[][], integer[]).
	// We don't track the dimension count; just consume the brackets. M0097-0071.
	for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		p.advance() // consume "["
		_ = p.acceptSymbol("]")
		ct.IsArray = true
	}
	return ct, nil
}

// parseFKAction reads the referential action keyword from the current
// token position. Used by REFERENCES … ON DELETE / ON UPDATE parsing.
// M0096-0011. Note: cascade/restrict/set are registered keywords;
// "no" and "action" are plain identifiers.
func parseFKAction(p *parser) FKAction {
	if p.acceptKeyword(KwCascade) {
		return FKActionCascade
	}
	if p.acceptKeyword(KwRestrict) {
		return FKActionRestrict
	}
	if p.acceptIdentKeyword("no") {
		_ = p.acceptIdentKeyword("action")
		return FKActionNoAction
	}
	if p.acceptKeyword(KwSet) {
		if p.acceptKeyword(KwNull) {
			return FKActionSetNull
		}
		_ = p.acceptKeyword(KwDefault)
		return FKActionSetDefault
	}
	return FKActionNoAction
}

// parseWithOptions parses `WITH ( name = value [, …] )`. Values are
// stored as raw text (numbers stay numeric strings, identifiers stay
// lowercased).
func (p *parser) parseWithOptions() (map[string]string, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '('")
	}
	out := map[string]string{}
	for {
		key, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		keyName := identText(key)
		// Namespace-qualified storage parameter, e.g. `toast.autovacuum_enabled`.
		// PostgreSQL's grammar permits a single optional namespace prefix
		// (reloption_elem: ColLabel '.' ColLabel '=' def_arg in gram.y); the
		// `toast.` namespace routes the option to the table's TOAST relation.
		// Combine the two labels into one dotted key so the executor can route
		// it. A bare `.` lexes as TokenSymbol. M0110-0001 (DU-002 slice 224).
		if cur := p.cur(); cur.Kind == TokenSymbol && cur.Value == "." {
			p.advance() // consume '.'
			sub, serr := p.parseIdent()
			if serr != nil {
				return nil, serr
			}
			keyName = keyName + "." + identText(sub)
		}
		var val string
		if cur := p.cur(); cur.Kind == TokenOperator && cur.Value == "=" {
			p.advance() // consume '='
			t := p.cur()
			// Optional leading sign: PG's reloption grammar accepts def_arg =
			// NumericOnly, which permits an optional '+'/'-' before the number
			// (gram.y), so signed-integer storage parameters such as
			// `WITH (toast.log_autovacuum_min_duration=-1)` (valid floor -1)
			// round-trip. Preserve the sign in the raw text for the executor to
			// parse/bounds-check. M0110-0001 (DU-002 slice 236).
			sign := ""
			if t.Kind == TokenOperator && (t.Value == "-" || t.Value == "+") {
				if t.Value == "-" {
					sign = "-"
				}
				p.advance()
				t = p.cur()
			}
			switch t.Kind {
			case TokenIntLit, TokenStringLit, TokenNumericLit:
				// TokenNumericLit (e.g. `0.2`) is accepted so REAL-typed storage
				// parameters such as autovacuum_vacuum_scale_factor round-trip;
				// the raw text is preserved for the executor to parse/bounds-check.
				// M0110-0001 (DU-002 slice 199).
				val = sign + t.Value
			case TokenIdent, TokenQuotedIdent, TokenKeyword:
				val = sign + identText(t)
			default:
				return nil, p.errAtCur("expected option value")
			}
			p.advance()
		} else {
			// Boolean flag without '= value' (e.g. 'oids' in WITH (oids)).
			val = "true"
		}
		out[keyName] = val
		if p.acceptSymbol(",") {
			continue
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')'")
		}
		break
	}
	return out, nil
}

// parseCreateIndexTail picks up after CREATE [UNIQUE] INDEX. Pgbench
// uses ALTER TABLE ADD PRIMARY KEY rather than CREATE INDEX, but
// CREATE INDEX is the canonical form and trivial to support.
func (p *parser) parseCreateIndexTail(pos int, unique bool) (Stmt, error) {
	stmt := &CreateIndexStmt{pos: pos, Unique: unique}
	if p.acceptKeyword(KwIf) {
		if !p.acceptKeyword(KwNot) {
			return nil, p.errAtCur("expected NOT")
		}
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfNotExists = true
	}
	// CONCURRENTLY: goopg builds the index synchronously, but records the flag so
	// the build waits for already-running transactions to drain before completing.
	stmt.Concurrently = p.acceptIdentKeyword("concurrently")
	// Optional index name. If the next token is ON, the name is omitted.
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn) {
		nameTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.Name = identText(nameTok)
	}
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	stmt.OnOnly = p.acceptIdentKeyword("only")
	tbl, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Table = tbl
	if p.acceptKeyword(KwUsing) {
		mTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.Method = identText(mTok)
	}
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '('")
	}
	cols, colExprs, colOrders, opClassWithOptions, err := p.parseIndexColumnList()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols
	stmt.ColExprs = colExprs
	stmt.ColOrders = colOrders
	stmt.OpClassWithOptions = opClassWithOptions
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	// Optional INCLUDE (col, …) — parse and store covering columns.
	if p.acceptIdentKeyword("include") {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after INCLUDE")
		}
		for {
			tok := p.cur()
			if tok.Kind == TokenIdent || tok.Kind == TokenKeyword {
				stmt.IncludeColumns = append(stmt.IncludeColumns, tok.Value)
				p.advance()
			} else {
				return nil, p.errAtCur("expected column name in INCLUDE list")
			}
			if p.acceptSymbol(")") {
				break
			}
			if !p.acceptSymbol(",") {
				return nil, p.errAtCur("expected ',' or ')' in INCLUDE list")
			}
		}
	}
	// Optional storage parameters WITH (…) — extract fillfactor, discard rest.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith {
		p.advance()
		if p.acceptSymbol("(") {
			depth := 1
			for depth > 0 && p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
					depth++
				} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
					depth--
					if depth == 0 {
						p.advance()
						break
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "fillfactor" {
					p.advance() // consume "fillfactor"
					// '=' is TokenOperator in goopg's lexer, not TokenSymbol.
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if p.cur().Kind == TokenIntLit {
							if v, err := p.parseIntLit(); err == nil {
								stmt.Fillfactor = int(v)
							}
							continue
						}
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "deduplicate_items" {
					// btree boolean storage parameter. PG accepts on/off/true/
					// false/yes/no/1/0 (parse_bool); record the value so pg_dump
					// can re-emit it. DU-002 slice 219.
					p.advance() // consume "deduplicate_items"
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if b, ok := parseReloptionBool(p.cur().Value); ok {
							stmt.DeduplicateItems = &b
							p.advance()
							continue
						}
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "fastupdate" {
					// GIN boolean storage parameter. PG accepts on/off/true/
					// false/yes/no/1/0 (parse_bool); record the value so pg_dump
					// can re-emit it. DU-002 slice 220.
					p.advance() // consume "fastupdate"
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if b, ok := parseReloptionBool(p.cur().Value); ok {
							stmt.FastUpdate = &b
							p.advance()
							continue
						}
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "gin_pending_list_limit" {
					// GIN integer storage parameter (max pending-list size in kB).
					// Record the value so pg_dump can re-emit it. DU-002 slice 221.
					p.advance() // consume "gin_pending_list_limit"
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if p.cur().Kind == TokenIntLit {
							if v, err := p.parseIntLit(); err == nil {
								stmt.GinPendingListLimit = int(v)
							}
							continue
						}
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "pages_per_range" {
					// BRIN integer storage parameter (heap pages per summarized
					// range). Record the value so pg_dump can re-emit it. DU-002 slice 222.
					p.advance() // consume "pages_per_range"
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if p.cur().Kind == TokenIntLit {
							if v, err := p.parseIntLit(); err == nil {
								stmt.PagesPerRange = int(v)
							}
							continue
						}
					}
				} else if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "autosummarize" {
					// BRIN boolean storage parameter. PG accepts on/off/true/
					// false/yes/no/1/0 (parse_bool); record the value so pg_dump
					// can re-emit it. DU-002 slice 223.
					p.advance() // consume "autosummarize"
					if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
						p.advance()
						if b, ok := parseReloptionBool(p.cur().Value); ok {
							stmt.AutoSummarize = &b
							p.advance()
							continue
						}
					}
				}
				p.advance()
			}
		}
	}
	// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+ unique index option).
	// DISTINCT may be a reserved keyword token (KwDistinct), not just an identifier.
	// `NULLS NOT DISTINCT` records the flag (treat NULLs as equal for uniqueness);
	// the bare/default `NULLS DISTINCT` leaves it false. DU-002 slice 134.
	if p.acceptIdentKeyword("nulls") {
		stmt.NullsNotDistinct = p.acceptKeyword(KwNot)
		if !p.acceptKeyword(KwDistinct) {
			_ = p.acceptIdentKeyword("distinct")
		}
	}
	// Optional WHERE predicate (partial index) — parse and record.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhere {
		p.advance()
		pred, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.HasPredicate = true
		stmt.Predicate = pred
	}
	return stmt, nil
}

// parseReloptionBool maps a storage-parameter boolean token to its value,
// mirroring PostgreSQL's parse_bool (on/off/true/false/yes/no/1/0,
// case-insensitive). The second return is false for an unrecognized token.
func parseReloptionBool(v string) (bool, bool) {
	switch strings.ToLower(v) {
	case "on", "true", "yes", "1", "t", "y":
		return true, true
	case "off", "false", "no", "0", "f", "n":
		return false, true
	default:
		return false, false
	}
}

// parseIndexColumnList parses the column list inside CREATE INDEX (…).
// Unlike parseColumnNameList it handles:
//   - simple column names
//   - expression columns: lower(col) or any expr starting with ident(
//   - optional COLLATE "…" or COLLATE ident after the column/expression
//   - optional opclass name (bare ident) after the collation
//   - optional ASC/DESC and NULLS FIRST/LAST modifiers
//
// For expression entries the column name is stored as "" and the parsed
// expression is returned in the parallel exprs slice. Simple column names
// are stored verbatim with nil in exprs.
func (p *parser) parseIndexColumnList() ([]string, []Expr, []IndexColOrder, string, error) {
	var cols []string
	var exprs []Expr
	var orders []IndexColOrder
	var opClassWithOptions string
	for {
		var colName string
		var colExpr Expr
		// Expression column: starts with ident followed by '('
		// e.g. lower(fruit)
		if p.cur().Kind == TokenIdent && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			// Parse and capture the expression.
			e, err := p.parseExpr()
			if err != nil {
				return nil, nil, nil, "", err
			}
			colName = "" // expression — no simple column name
			colExpr = e
		} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			// Parenthesised expression: (expr)
			e, err := p.parseExpr()
			if err != nil {
				return nil, nil, nil, "", err
			}
			colName = ""
			colExpr = e
		} else {
			tok, err := p.parseIdent()
			if err != nil {
				return nil, nil, nil, "", err
			}
			colName = identText(tok)
		}

		// Optional COLLATE "..." or COLLATE ident
		if p.acceptIdentKeyword("collate") {
			// consume the collation name (quoted or plain ident)
			_ = p.advance()
		}

		// Optional opclass name (bare ident that is not a known keyword
		// and not ',' or ')'). `NULLS` lexes as a bare TokenIdent, so guard
		// against it here — otherwise `(col NULLS FIRST)` mis-reads NULLS as an
		// opclass name and the trailing FIRST/LAST then fails to parse.
		if p.cur().Kind == TokenIdent && !strings.EqualFold(p.cur().Value, "nulls") {
			// This is the opclass name — capture it.
			opClassName := p.cur().Value
			p.advance()
			// Optional operator class options: (foo=1, bar=2).
			// Most built-in operator classes have no options; presence here
			// is recorded so the executor can reject with the PG error.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				if opClassWithOptions == "" {
					opClassWithOptions = opClassName
				}
				depth := 1
				p.advance() // consume '('
				for depth > 0 && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
						depth++
					} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
						depth--
					}
					p.advance()
				}
			}
		}

		// Optional ASC/DESC. NULLS FIRST is the btree default for DESC, NULLS
		// LAST for ASC, so pre-resolve NullsFirst to the descending flag and let
		// an explicit NULLS clause below override it (mirrors PG's indoption).
		var order IndexColOrder
		if p.acceptKeyword(KwDesc) {
			order.Descending = true
		} else {
			_ = p.acceptKeyword(KwAsc)
		}
		order.NullsFirst = order.Descending

		// Optional NULLS FIRST/LAST
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNull {
			p.advance() // NULLS
			if p.acceptIdentKeyword("first") {
				order.NullsFirst = true
			} else if p.acceptIdentKeyword("last") {
				order.NullsFirst = false
			}
		}
		if p.acceptIdentKeyword("nulls") {
			if p.acceptIdentKeyword("first") {
				order.NullsFirst = true
			} else if p.acceptIdentKeyword("last") {
				order.NullsFirst = false
			}
		}

		cols = append(cols, colName)
		exprs = append(exprs, colExpr)
		orders = append(orders, order)
		if !p.acceptSymbol(",") {
			break
		}
		// Stop if we hit the closing paren (empty trailing comma not expected
		// but be safe).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			break
		}
	}
	return cols, exprs, orders, opClassWithOptions, nil
}

// parseDrop dispatches on the next keyword after DROP.
func (p *parser) parseDrop() (Stmt, error) {
	t, err := p.expectKeyword(KwDrop)
	if err != nil {
		return nil, err
	}
	switch p.cur().Keyword {
	case KwTable:
		p.advance()
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropTableStmt{pos: t.Pos, IfExists: ifExists, Names: names, Behavior: behavior}, nil
	case KwIndex:
		p.advance()
		concurrent := p.acceptIdentKeyword("concurrently")
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropIndexStmt{pos: t.Pos, Concurrent: concurrent, IfExists: ifExists, Names: names, Behavior: behavior}, nil
	case KwView:
		p.advance()
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropViewStmt{pos: t.Pos, IfExists: ifExists, Names: names, Behavior: behavior}, nil
	case KwPublication:
		p.advance()
		ifExists, name, err := p.parseDropPubSubTail("publication")
		if err != nil {
			return nil, err
		}
		return &DropPublicationStmt{pos: t.Pos, IfExists: ifExists, Name: name}, nil
	case KwSubscription:
		p.advance()
		ifExists, name, err := p.parseDropPubSubTail("subscription")
		if err != nil {
			return nil, err
		}
		return &DropSubscriptionStmt{pos: t.Pos, IfExists: ifExists, Name: name}, nil
	case KwFunction:
		p.advance()
		return p.parseDropFunctionTail(t.Pos)
	case KwProcedure:
		p.advance()
		return p.parseDropProcedureTail(t.Pos)
	case KwTrigger:
		p.advance()
		return p.parseDropTriggerTail(t.Pos)
	}
	// DROP TYPE [IF EXISTS] name [, …] [CASCADE|RESTRICT] — M0097-0017.
	if p.acceptIdentKeyword("type") {
		return p.parseDropType(t.Pos)
	}
	// DROP DOMAIN [IF EXISTS] name [, …] [CASCADE|RESTRICT] — M0097-0017.
	if p.acceptIdentKeyword("domain") {
		return p.parseDropDomain(t.Pos)
	}
	// DROP RULE [IF EXISTS] name ON table — real syntax.
	if p.acceptIdentKeyword("rule") {
		return p.parseDropRuleTail(t.Pos)
	}
	// DROP ROUTINE [IF EXISTS] name [(arg_types)] [CASCADE|RESTRICT].
	// Semantically equivalent to DROP PROCEDURE but uses "routine" in error messages.
	if p.acceptIdentKeyword("routine") {
		s, err := p.parseDropProcedureTail(t.Pos)
		if err != nil {
			return nil, err
		}
		if ps, ok := s.(*DropProcedureStmt); ok {
			ps.ObjKind = "routine"
		}
		return s, nil
	}
	// Old DROP RULE aliases (tuple/instance/rewrite rule) — PG rejects with
	// a syntax error at the alias keyword.
	if cur := p.cur(); cur.Kind == TokenIdent &&
		(cur.Value == "tuple" || cur.Value == "instance" || cur.Value == "rewrite") {
		return nil, p.errSyntaxAtCur()
	}
	// DROP TABLESPACE [IF EXISTS] name — removes the runtime tablespace registry
	// entry and its in-place pg_tblspc/<oid> directory. M0095-0003.
	if p.acceptKeyword(KwTablespace) {
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		nt := p.cur()
		if nt.Kind != TokenIdent && nt.Kind != TokenKeyword && nt.Kind != TokenQuotedIdent {
			return nil, p.errAtCur("expected tablespace name")
		}
		name := nt.Value
		p.advance()
		return &DropTablespaceStmt{pos: t.Pos, IfExists: ifExists, Name: name}, nil
	}
	// DROP DATABASE [IF EXISTS] name — goopg is single-database; always reports does-not-exist.
	if p.acceptIdentKeyword("database") {
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: "database", IfExists: ifExists,
			Names: names, Behavior: behavior}, nil
	}
	// DROP FOREIGN TABLE [IF EXISTS] name [, ...] [CASCADE|RESTRICT] — accepted as no-op.
	// DROP FOREIGN DATA WRAPPER [IF EXISTS] name [, ...] — accepted as no-op.
	// PG emits schema-not-found notice for schema-qualified names with non-existent schemas.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign {
		p.advance()
		objType := "foreign table"
		if p.acceptKeyword(KwTable) {
			// DROP FOREIGN TABLE — consume TABLE, use "foreign table" type.
		} else if p.acceptIdentKeyword("data") {
			// DROP FOREIGN DATA WRAPPER
			_ = p.acceptIdentKeyword("wrapper")
			objType = "foreign-data wrapper"
		}
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: objType, IfExists: ifExists,
			Names: names, Behavior: behavior}, nil
	}
	// DROP AGGREGATE [IF EXISTS] name ( argtype_list ) [CASCADE|RESTRICT]
	// PG requires the parenthesised argument-type list; without it the grammar
	// produces a syntax error at the token following the name.
	if p.acceptIdentKeyword("aggregate") {
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Require "(" — anything else (including ";") is a syntax error.
		if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
			return nil, p.errSyntaxAtCur()
		}
		p.advance() // consume "("
		// Parse the argument type name (e.g. "int4", "*", "nonesuch").
		var argType string
		if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
			argType = "*"
			p.advance()
		} else if tok, err2 := p.parseIdent(); err2 == nil {
			argType = tok.Value
			// Read schema-qualified type: schema.type (e.g. no_such_schema.no_such_type).
			if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
				p.advance()
				if tok2, err3 := p.parseIdent(); err3 == nil {
					argType = argType + "." + tok2.Value
				}
			}
		}
		// Skip rest of arg list until matching ")".
		depth := 1
		for depth > 0 && p.cur().Kind != TokenEOF {
			switch {
			case p.cur().Kind == TokenSymbol && p.cur().Value == "(":
				depth++
			case p.cur().Kind == TokenSymbol && p.cur().Value == ")":
				depth--
				if depth == 0 {
					continue
				}
			}
			p.advance()
		}
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			p.advance() // consume ")"
		}
		behavior := DropDefault
		switch {
		case p.acceptKeyword(KwCascade):
			behavior = DropCascade
		case p.acceptKeyword(KwRestrict):
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: "aggregate", IfExists: ifExists,
			Names: []ObjectName{name}, Behavior: behavior, ArgTypes: []string{argType}}, nil
	}
	// DROP OPERATOR [CLASS|FAMILY] [IF EXISTS] ... M0097-0071.
	// Handles DROP OPERATOR name (type, type), DROP OPERATOR CLASS name USING method,
	// and DROP OPERATOR FAMILY name USING method.
	if p.acceptIdentKeyword("operator") {
		// Check for CLASS or FAMILY sub-forms first.
		var opSubtype string
		if p.acceptIdentKeyword("class") {
			opSubtype = "operator class"
		} else if p.acceptIdentKeyword("family") {
			opSubtype = "operator family"
		}
		if opSubtype != "" {
			// DROP OPERATOR CLASS|FAMILY [IF EXISTS] name USING method [CASCADE|RESTRICT]
			ifExists := false
			if p.acceptKeyword(KwIf) {
				if _, err := p.expectKeyword(KwExists); err != nil {
					return nil, err
				}
				ifExists = true
			}
			name, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			// Consume USING method_name (store in UsingMethod for error formatting). M0097-0071.
			var usingMethod string
			if p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using") {
				if tok, err2 := p.parseIdent(); err2 == nil {
					usingMethod = tok.Value
				}
			}
			behavior := DropDefault
			switch {
			case p.acceptKeyword(KwCascade):
				behavior = DropCascade
			case p.acceptKeyword(KwRestrict):
			}
			return &DropCompatStmt{pos: t.Pos, ObjType: opSubtype, IfExists: ifExists,
				Names: []ObjectName{name}, Behavior: behavior, UsingMethod: usingMethod}, nil
		}
		// DROP OPERATOR [IF EXISTS] name ( left_type , right_type ) [CASCADE|RESTRICT]
		// PG requires the parenthesised type list; without it (or with just a bare
		// comma-separated identifier list) the grammar produces a syntax error.
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		// Operator name: can be an identifier OR a symbol sequence (e.g. "===", "=").
		// If the current token is "(", there is no name at all — syntax error.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			return nil, p.errSyntaxAtCur()
		}
		// Parse the operator name — handles identifiers, operator symbols, and qualified names.
		name, err := p.parseOperatorName()
		if err != nil {
			return nil, err
		}
		// After the name, "(" is required. "," means the caller wrote
		// "DROP OPERATOR name, name" without the type list.
		if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
			return nil, p.errSyntaxAtCur()
		}
		p.advance() // consume "("
		// Parse the type list.  PG requires exactly two type specs (or NONE).
		// Syntax errors: empty list "()", leading comma "( , t)", trailing comma "(t, )".
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			return nil, p.errSyntaxAtCur() // "drop operator === ()"
		}
		if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
			return nil, p.errSyntaxAtCur() // "drop operator = ( , int4)"
		}
		// Parse left type (or NONE).
		var leftType string
		if p.acceptIdentKeyword("none") {
			leftType = "none"
		} else if tok, err2 := p.parseIdent(); err2 == nil {
			leftType = tok.Value
			// Read schema-qualified type: schema.type
			if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
				p.advance()
				if tok2, err3 := p.parseIdent(); err3 == nil {
					leftType = leftType + "." + tok2.Value
				}
			}
		}
		var rightType string
		if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
			p.advance() // consume ","
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				return nil, p.errSyntaxAtCur() // "drop operator = (int4, )"
			}
			if p.acceptIdentKeyword("none") {
				rightType = "none"
			} else if tok, err2 := p.parseIdent(); err2 == nil {
				rightType = tok.Value
				// Read schema-qualified type: schema.type
				if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
					p.advance()
					if tok2, err3 := p.parseIdent(); err3 == nil {
						rightType = rightType + "." + tok2.Value
					}
				}
			}
		}
		// Skip to closing ")".
		for p.cur().Kind != TokenSymbol || p.cur().Value != ")" {
			if p.cur().Kind == TokenEOF {
				break
			}
			p.advance()
		}
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			p.advance() // consume ")"
		}
		behavior := DropDefault
		switch {
		case p.acceptKeyword(KwCascade):
			behavior = DropCascade
		case p.acceptKeyword(KwRestrict):
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: "operator", IfExists: ifExists,
			Names: []ObjectName{name}, Behavior: behavior, ArgTypes: []string{leftType, rightType}}, nil
	}
	// DROP TEXT SEARCH DICTIONARY|PARSER|TEMPLATE|CONFIGURATION — M0097-0071.
	// "text" is an identifier; "search" and sub-type keywords are also identifiers.
	if p.acceptIdentKeyword("text") {
		_ = p.acceptIdentKeyword("search") // consume "search" if present
		var tsType string
		switch {
		case p.acceptIdentKeyword("dictionary"):
			tsType = "text search dictionary"
		case p.acceptIdentKeyword("parser"):
			tsType = "text search parser"
		case p.acceptIdentKeyword("template"):
			tsType = "text search template"
		case p.acceptIdentKeyword("configuration"):
			tsType = "text search configuration"
		default:
			tsType = "text search"
		}
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropCompatStmt{
			pos: t.Pos, ObjType: tsType, IfExists: ifExists, Names: names, Behavior: behavior,
		}, nil
	}
	// DROP CAST (fromType AS toType) [IF EXISTS] [CASCADE|RESTRICT] — M0097-0071.
	// "cast" is an ident keyword; the argument list uses (type AS type) syntax.
	if p.acceptIdentKeyword("cast") {
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		// Expect "(" opening the type pair.
		if !p.acceptSymbol("(") {
			return nil, p.errSyntaxAtCur()
		}
		// Parse fromType (may be qualified).
		var fromType, toType string
		if tok, err2 := p.parseIdent(); err2 == nil {
			fromType = tok.Value
		}
		// Schema-qualified type: fromSchema.fromType
		if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
			p.advance()
			if tok, err2 := p.parseIdent(); err2 == nil {
				fromType = fromType + "." + tok.Value
			}
		}
		// Consume AS.
		_ = p.acceptKeyword(KwAs)
		// Parse toType (may be qualified).
		if tok, err2 := p.parseIdent(); err2 == nil {
			toType = tok.Value
		}
		if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
			p.advance()
			if tok, err2 := p.parseIdent(); err2 == nil {
				toType = toType + "." + tok.Value
			}
		}
		// Consume ")".
		_ = p.acceptSymbol(")")
		behavior := DropDefault
		switch {
		case p.acceptKeyword(KwCascade):
			behavior = DropCascade
		case p.acceptKeyword(KwRestrict):
		}
		return &DropCompatStmt{
			pos:       t.Pos,
			ObjType:   "cast",
			IfExists:  ifExists,
			Behavior:  behavior,
			CastTypes: []string{fromType, toType},
		}, nil
	}
	// Handle ident-based DROP targets as compatibility stubs. M0097-0008.
	for _, objType := range []string{
		"sequence", "schema",
		"collation",
		"materialized", "extension", "server",
		"language", "access", "event", "transform",
		"group", "role", "user",
		"conversion", // M0097-0071
	} {
		if p.acceptIdentKeyword(objType) {
			// "materialized view" is two words; VIEW is a keyword token, not ident. M0097-0038.
			resolvedType := objType
			if objType == "materialized" {
				_ = p.acceptIdentKeyword("view") || p.acceptKeyword(KwView)
				resolvedType = "materialized view"
			}
			// "access method" is two words; skip "method" after "access". M0097-0071.
			if objType == "access" {
				_ = p.acceptIdentKeyword("method")
				resolvedType = "access method"
			}
			ifExists, names, behavior, err := p.parseDropTail()
			if err != nil {
				return nil, err
			}
			return &DropCompatStmt{
				pos:      t.Pos,
				ObjType:  resolvedType,
				IfExists: ifExists,
				Names:    names,
				Behavior: behavior,
			}, nil
		}
	}
	// KwLanguage and KwGroup are tokenized as TokenKeyword, not TokenIdent,
	// so acceptIdentKeyword("language"/"group") fails — handle them explicitly.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwLanguage {
		p.advance()
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: "language", IfExists: ifExists, Names: names, Behavior: behavior}, nil
	}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwGroup {
		p.advance()
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropCompatStmt{pos: t.Pos, ObjType: "group", IfExists: ifExists, Names: names, Behavior: behavior}, nil
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, SEQUENCE, SCHEMA, TYPE, PUBLICATION, SUBSCRIPTION, FUNCTION, PROCEDURE, or TRIGGER after DROP")
}

// parseTableForeignKey parses a table-level FOREIGN KEY constraint, positioned
// on the FOREIGN keyword:
//
//	FOREIGN KEY (cols) REFERENCES table [(refcols)]
//	  [ON DELETE action] [ON UPDATE action]
//	  [[NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]]
//
// The constraint name (or "" for an anonymous constraint) is supplied by the
// caller. This is the multi-column sibling of the inline column REFERENCES path
// in parseColumnDef; the action/deferrable grammar is kept in lockstep with it
// so both round-trip identically through pg_constraint and pg_dump. DU-002 slice 53.
func (p *parser) parseTableForeignKey(name string) (TableForeignKeyDef, error) {
	fk := TableForeignKeyDef{Name: name}
	p.advance() // FOREIGN
	if _, err := p.expectKeyword(KwKey); err != nil {
		return TableForeignKeyDef{}, err
	}
	if !p.acceptSymbol("(") {
		return TableForeignKeyDef{}, p.errAtCur("expected '(' after FOREIGN KEY")
	}
	cols, err := p.parseColumnNameList()
	if err != nil {
		return TableForeignKeyDef{}, err
	}
	if !p.acceptSymbol(")") {
		return TableForeignKeyDef{}, p.errAtCur("expected ')'")
	}
	fk.Columns = cols
	if _, err := p.expectKeyword(KwReferences); err != nil {
		return TableForeignKeyDef{}, err
	}
	refTable, err := p.parseObjectName()
	if err != nil {
		return TableForeignKeyDef{}, err
	}
	fk.RefTable = refTable
	if p.acceptSymbol("(") {
		refCols, err := p.parseColumnNameList()
		if err != nil {
			return TableForeignKeyDef{}, err
		}
		if !p.acceptSymbol(")") {
			return TableForeignKeyDef{}, p.errAtCur("expected ')'")
		}
		fk.RefColumns = refCols
	}
	// ON DELETE / ON UPDATE referential-action clauses. ON is KwOn (reserved).
	for p.acceptKeyword(KwOn) {
		isDelete := p.acceptKeyword(KwDelete)
		if !isDelete {
			_ = p.acceptKeyword(KwUpdate)
		}
		action := parseFKAction(p)
		if isDelete {
			fk.OnDelete = action
		} else {
			fk.OnUpdate = action
		}
	}
	// [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]; also accept a
	// bare INITIALLY DEFERRED (implies DEFERRABLE), mirroring the inline path.
	if p.acceptKeyword(KwNot) {
		_, _ = p.expectKeyword(KwDeferrable)
		fk.Deferrable = false
	} else if p.acceptKeyword(KwDeferrable) {
		fk.Deferrable = true
		if p.acceptIdentKeyword("initially") {
			fk.InitiallyDeferred = p.acceptIdentKeyword("deferred")
			_ = p.acceptIdentKeyword("immediate")
		}
	} else if p.acceptIdentKeyword("initially") {
		fk.Deferrable = true
		fk.InitiallyDeferred = p.acceptIdentKeyword("deferred")
		_ = p.acceptIdentKeyword("immediate")
	}
	return fk, nil
}

// parseExcludeConstraint parses the body of EXCLUDE USING method (col WITH op) [INCLUDE (cols)]
// after the EXCLUDE keyword has already been consumed. Returns a TableConstraintDef. M0097-0023.
func (p *parser) parseExcludeConstraint() TableConstraintDef {
	cdef := TableConstraintDef{IsExclusion: true, Method: "btree"}
	// USING method — USING is a reserved keyword.
	if p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using") {
		if t, err := p.parseIdent(); err == nil {
			cdef.Method = strings.ToLower(t.Value)
		}
	}
	// (col WITH op [, …]) — WITH is a reserved keyword.
	if p.acceptSymbol("(") {
		for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
			colTok, err := p.parseIdent()
			if err != nil {
				p.advance()
				continue
			}
			cdef.Columns = append(cdef.Columns, colTok.Value)
			_ = p.acceptKeyword(KwWith) || p.acceptIdentKeyword("with")
			// Operator: may be an identifier, symbol, or operator token (e.g. "=").
			// Note: "=" is TokenOperator in goopg's lexer, not TokenSymbol.
			var opVal string
			if opTok, e := p.parseIdent(); e == nil {
				opVal = opTok.Value
			} else if p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator {
				opVal = p.cur().Value
				p.advance()
			}
			if opVal != "" && cdef.ExclusionOp == "" {
				cdef.ExclusionOp = opVal
			}
			_ = p.acceptSymbol(",")
		}
	}
	// Optional INCLUDE (cols)
	if p.acceptIdentKeyword("include") {
		if p.acceptSymbol("(") {
			for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
					cdef.IncludeColumns = append(cdef.IncludeColumns, p.cur().Value)
				}
				p.advance()
			}
		}
	}
	return cdef
}

// parseDomainCheckExpr is parseCheckExpr's domain twin: it reconstructs the raw
// CHECK predicate but renders the domain value placeholder as uppercase `VALUE`,
// matching PG's ruleutils deparse (pg_get_constraintdef). The shared
// parseCheckExpr must NOT do this — in a TABLE check `value` may be a real column
// name. String literals are left untouched (a literal `'value'` stays lowercase).
// DU-002 slice 96.
func (p *parser) parseDomainCheckExpr() (string, error) {
	if !p.acceptSymbol("(") {
		return "", p.errAtCur("expected '(' after CHECK")
	}
	depth := 1
	var parts []string
	for depth > 0 && p.cur().Kind != TokenEOF {
		t := p.cur()
		switch {
		case t.Kind == TokenSymbol && t.Value == "(":
			depth++
			parts = append(parts, "(")
		case t.Kind == TokenSymbol && t.Value == ")":
			depth--
			if depth == 0 {
				p.advance()
				return strings.Join(parts, " "), nil
			}
			parts = append(parts, ")")
		case t.Kind == TokenStringLit:
			parts = append(parts, "'"+strings.ReplaceAll(t.Value, "'", "''")+"'")
		case (t.Kind == TokenIdent || t.Kind == TokenKeyword) && strings.EqualFold(t.Value, "value"):
			parts = append(parts, "VALUE")
		default:
			parts = append(parts, t.Value)
		}
		p.advance()
	}
	return "", p.errAtCur("unterminated CHECK expression")
}

// parseCheckExpr parses `( expr )` after CHECK and returns the raw SQL expression
// reconstructed from tokens. M0097-0014.
func (p *parser) parseCheckExpr() (string, error) {
	if !p.acceptSymbol("(") {
		return "", p.errAtCur("expected '(' after CHECK")
	}
	depth := 1
	var parts []string
	for depth > 0 && p.cur().Kind != TokenEOF {
		t := p.cur()
		if t.Kind == TokenSymbol && t.Value == "(" {
			depth++
			parts = append(parts, "(")
		} else if t.Kind == TokenSymbol && t.Value == ")" {
			depth--
			if depth == 0 {
				p.advance() // consume closing )
				return strings.Join(parts, " "), nil
			}
			parts = append(parts, ")")
		} else if t.Kind == TokenStringLit {
			// Re-add quotes for string literals (token stores value without quotes).
			parts = append(parts, "'"+strings.ReplaceAll(t.Value, "'", "''")+"'")
		} else {
			parts = append(parts, t.Value)
		}
		p.advance()
	}
	return "", p.errAtCur("unterminated CHECK expression")
}

// parseCreateTriggerTail picks up after CREATE [CONSTRAINT] TRIGGER.
// Grammar (simplified):
// parseCreateSequenceTail picks up after CREATE [TEMP] SEQUENCE. M0097-0009.
func (p *parser) parseCreateSequenceTail(pos int, temp bool) (Stmt, error) {
	stmt := &CreateSequenceStmt{pos: pos, Temporary: temp}
	// Optional IF NOT EXISTS.
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwNot); err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfNotExists = true
	}
	// Sequence name.
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Option loop — consume all recognised options until we hit something else.
	for {
		switch {
		case p.acceptIdentKeyword("as") || p.acceptKeyword(KwAs):
			// AS datatype
			dt, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.DataType = identText(dt)
		case p.acceptIdentKeyword("increment"):
			_ = p.acceptKeyword(KwBy)
			val, err := p.parseInt64()
			if err != nil {
				return nil, err
			}
			stmt.Increment = &val
		case p.acceptIdentKeyword("minvalue"):
			val, err := p.parseInt64()
			if err != nil {
				return nil, err
			}
			stmt.MinValue = &val
		case p.acceptIdentKeyword("maxvalue"):
			val, err := p.parseInt64()
			if err != nil {
				return nil, err
			}
			stmt.MaxValue = &val
		case p.acceptIdentKeyword("no"):
			// NO MINVALUE / NO MAXVALUE / NO CYCLE
			_ = p.acceptIdentKeyword("minvalue") || p.acceptIdentKeyword("maxvalue") || p.acceptIdentKeyword("cycle")
		case p.acceptIdentKeyword("start"):
			_ = p.acceptKeyword(KwWith)
			val, err := p.parseInt64()
			if err != nil {
				return nil, err
			}
			stmt.Start = &val
		case p.acceptIdentKeyword("cache"):
			val, err := p.parseInt64()
			if err != nil {
				return nil, err
			}
			stmt.Cache = &val
		case p.acceptIdentKeyword("cycle"):
			stmt.Cycle = true
		case p.acceptIdentKeyword("owned"):
			_ = p.acceptKeyword(KwBy)
			// Consume table.column or NONE.
			if p.acceptIdentKeyword("none") {
				stmt.OwnedBy = ""
			} else {
				owner, err := p.parseObjectName()
				if err != nil {
					return nil, err
				}
				// Optional .col after table name.
				if p.acceptSymbol(".") {
					col, err := p.parseIdent()
					if err != nil {
						return nil, err
					}
					stmt.OwnedBy = owner.String() + "." + identText(col)
				} else {
					stmt.OwnedBy = owner.String()
				}
			}
		default:
			return stmt, nil
		}
	}
}

// parseInt64 parses a (possibly negative) integer literal. M0097-0009.
// M0097-0003: uses parseIntLiteral to support 0b/0o/0x prefixes and _ separators.
func (p *parser) parseInt64() (int64, error) {
	// The minus sign may be tokenized as TokenOperator or TokenSymbol.
	neg := (p.cur().Kind == TokenOperator || p.cur().Kind == TokenSymbol) && p.cur().Value == "-"
	if neg {
		p.advance()
	}
	t := p.cur()
	if t.Kind != TokenIntLit {
		return 0, p.errAtCur("expected integer literal")
	}
	p.advance()
	n, err := parseIntLiteral(t.Value)
	if err != nil {
		return 0, &SyntaxError{Pos: t.Pos, Message: "invalid integer literal: " + t.Value}
	}
	if neg {
		n = -n
	}
	return n, nil
}

//	name BEFORE|AFTER|INSTEAD OF event[, ...] ON table
//	FOR [EACH] {ROW|STATEMENT}
//	EXECUTE {FUNCTION|PROCEDURE} funcname([]);
//
// M0096-0012.
func (p *parser) parseCreateTriggerTail(pos int) (Stmt, error) {
	// Trigger name
	nameTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt := &CreateTriggerStmt{pos: pos, Name: identText(nameTok)}

	// Timing: BEFORE | AFTER | INSTEAD OF
	switch {
	case p.acceptIdentKeyword("before"):
		stmt.Timing = TriggerBefore
	case p.acceptIdentKeyword("after"):
		stmt.Timing = TriggerAfter
	case p.acceptIdentKeyword("instead"):
		_ = p.acceptKeyword(KwOf)
		stmt.Timing = TriggerInsteadOf
	default:
		return nil, p.errAtCur("expected BEFORE, AFTER, or INSTEAD OF after trigger name")
	}

	// Events: INSERT | UPDATE | DELETE | TRUNCATE [OR ...]
	for {
		switch {
		case p.acceptKeyword(KwInsert):
			stmt.Events = append(stmt.Events, "insert")
		case p.acceptKeyword(KwUpdate):
			stmt.Events = append(stmt.Events, "update")
		case p.acceptKeyword(KwDelete):
			stmt.Events = append(stmt.Events, "delete")
		case p.acceptKeyword(KwTruncate) || p.acceptIdentKeyword("truncate"):
			stmt.Events = append(stmt.Events, "truncate")
		default:
			return nil, p.errAtCur("expected INSERT, UPDATE, DELETE, or TRUNCATE in trigger events")
		}
		if !p.acceptKeyword(KwOr) {
			break
		}
	}

	// ON table
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	tblName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Table = tblName

	// FOR [EACH] ROW | STATEMENT
	if p.acceptKeyword(KwFor) {
		_ = p.acceptIdentKeyword("each")
		switch {
		case p.acceptIdentKeyword("row"):
			stmt.ForEachRow = true
		case p.acceptIdentKeyword("statement"):
			stmt.ForEachRow = false
		default:
			return nil, p.errAtCur("expected ROW or STATEMENT after FOR [EACH]")
		}
	}

	// Optional WHEN (condition) — skip for now.
	if p.acceptKeyword(KwWhen) {
		if p.acceptSymbol("(") {
			depth := 1
			for depth > 0 && p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
					depth++
				} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
					depth--
				}
				p.advance()
			}
		}
	}

	// EXECUTE {FUNCTION|PROCEDURE} funcname([args])
	if !p.acceptKeyword(KwExecute) {
		return nil, p.errAtCur("expected EXECUTE in trigger definition")
	}
	_ = p.acceptKeyword(KwFunction) || p.acceptKeyword(KwProcedure)
	funcName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.FuncName = funcName
	// Parse the optional argument list (string literals passed as TG_ARGV).
	if p.acceptSymbol("(") {
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				p.advance()
				break
			}
			if p.cur().Kind == TokenStringLit {
				stmt.FuncArgs = append(stmt.FuncArgs, p.cur().Value)
				p.advance()
			} else {
				p.advance() // skip non-string args
			}
			p.acceptSymbol(",")
		}
	}
	return stmt, nil
}

// parseDropRuleTail picks up after DROP RULE.
// Grammar: [IF EXISTS] name ON table [CASCADE|RESTRICT].
func (p *parser) parseDropRuleTail(pos int) (Stmt, error) {
	ifExists := false
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		ifExists = true
	}
	nameTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	tbl, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	_ = p.acceptKeyword(KwCascade) || p.acceptKeyword(KwRestrict)
	return &DropRuleStmt{pos: pos, Name: identText(nameTok), Table: tbl, IfExists: ifExists}, nil
}

// parseDropTriggerTail picks up after DROP TRIGGER.
// Grammar: [IF EXISTS] name ON table [CASCADE|RESTRICT].
func (p *parser) parseDropTriggerTail(pos int) (Stmt, error) {
	ifExists := false
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		ifExists = true
	}
	nameTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	tbl, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	// Optional CASCADE | RESTRICT.
	_ = p.acceptKeyword(KwCascade) || p.acceptKeyword(KwRestrict)
	return &DropTriggerStmt{pos: pos, Name: identText(nameTok), Table: tbl, IfExists: ifExists}, nil
}

// parseDropPubSubTail picks up after DROP PUBLICATION / DROP
// SUBSCRIPTION, parsing `[IF EXISTS] name`. Single-name form
// only — multi-target DROP for these isn't supported in v0.
func (p *parser) parseDropPubSubTail(kind string) (bool, string, error) {
	ifExists := false
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return false, "", err
		}
		ifExists = true
	}
	if p.cur().Kind != TokenIdent {
		return false, "", p.errAtCur("expected " + kind + " name")
	}
	name := p.cur().Value
	p.advance()
	return ifExists, name, nil
}

// parseDropTail picks up after DROP TABLE / DROP INDEX, parsing
// `[IF EXISTS] name [, …] [CASCADE|RESTRICT]`.
func (p *parser) parseDropTail() (bool, []ObjectName, DropBehavior, error) {
	ifExists := false
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return false, nil, DropDefault, err
		}
		ifExists = true
	}
	names, err := p.parseObjectList()
	if err != nil {
		return false, nil, DropDefault, err
	}
	behavior := DropDefault
	switch {
	case p.acceptKeyword(KwCascade):
		behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		behavior = DropDefault
	}
	return ifExists, names, behavior, nil
}

// parseAlter: ALTER TABLE [IF EXISTS] name action [, action …].
//
// Action grammar (v0):
//
//	ADD [CONSTRAINT name] PRIMARY KEY ( col [, …] )
//	ADD [COLUMN] column_def
//
// Pgbench emits `alter table pgbench_branches add primary key (bid)`
// to install primary keys after CREATE TABLE; that's the load-bearing
// shape this function unblocks.
func (p *parser) parseAlter() (Stmt, error) {
	t, err := p.expectKeyword(KwAlter)
	if err != nil {
		return nil, err
	}
	// ALTER SEQUENCE — consume options as a compat stub. M0097-0009.
	if p.acceptIdentKeyword("sequence") {
		stmt := &AlterSequenceStmt{pos: t.Pos}
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			stmt.IfExists = true
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt.Name = name
		// Parse sequence options — same switch pattern as CREATE SEQUENCE.
		for {
			switch {
			case p.acceptIdentKeyword("as") || p.acceptKeyword(KwAs):
				dt, err := p.parseIdent()
				if err != nil {
					return stmt, nil
				}
				stmt.DataType = strings.ToLower(identText(dt))
			case p.acceptIdentKeyword("increment"):
				_ = p.acceptKeyword(KwBy)
				val, err := p.parseInt64()
				if err != nil {
					return stmt, nil
				}
				stmt.Increment = &val
			case p.acceptIdentKeyword("minvalue"):
				val, err := p.parseInt64()
				if err != nil {
					return stmt, nil
				}
				stmt.MinValue = &val
			case p.acceptIdentKeyword("maxvalue"):
				val, err := p.parseInt64()
				if err != nil {
					return stmt, nil
				}
				stmt.MaxValue = &val
			case p.acceptIdentKeyword("no"):
				switch {
				case p.acceptIdentKeyword("minvalue"):
					stmt.NoMinValue = true
				case p.acceptIdentKeyword("maxvalue"):
					stmt.NoMaxValue = true
				case p.acceptIdentKeyword("cycle"):
					stmt.NoCycle = true
				default:
					p.advance()
				}
			case p.acceptIdentKeyword("start"):
				_ = p.acceptKeyword(KwWith)
				val, err := p.parseInt64()
				if err != nil {
					return stmt, nil
				}
				stmt.StartWith = &val
			case p.acceptIdentKeyword("restart"):
				// RESTART or RESTART [WITH] n
				_ = p.acceptKeyword(KwWith)
				t2 := p.cur()
				if t2.Kind == TokenIntLit || t2.Kind == TokenNumericLit || (t2.Kind == TokenOperator && t2.Value == "-") || (t2.Kind == TokenOperator && t2.Value == "+") {
					val, err := p.parseInt64()
					if err != nil {
						return stmt, nil
					}
					stmt.RestartWith = &val
				} else {
					stmt.Restart = true
				}
			case p.acceptIdentKeyword("cycle"):
				stmt.Cycle = true
			case p.acceptIdentKeyword("cache"):
				val, err := p.parseInt64()
				if err != nil {
					return stmt, err
				}
				stmt.Cache = &val
			case p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet):
				// SET LOGGED / SET UNLOGGED — no-op.
				_ = p.acceptIdentKeyword("logged") || p.acceptIdentKeyword("unlogged") || p.acceptKeyword(KwUnlogged)
			case p.acceptIdentKeyword("owned"):
				_ = p.acceptKeyword(KwBy)
				if p.acceptIdentKeyword("none") {
					stmt.ClearOwnedBy = true
					stmt.OwnedBy = ""
				} else {
					owner, err := p.parseObjectName()
					if err != nil {
						return stmt, nil
					}
					if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
						p.advance()
						col, err := p.parseIdent()
						if err != nil {
							return stmt, nil
						}
						stmt.OwnedBy = owner.String() + "." + identText(col)
					} else {
						stmt.OwnedBy = owner.String()
					}
				}
			default:
				return stmt, nil
			}
		}
	}
	// ALTER TYPE name ADD VALUE … — M0097-0017.
	if p.acceptIdentKeyword("type") {
		return p.parseAlterType(t.Pos)
	}
	// ALTER AGGREGATE name(argtype_list) RENAME TO newname. M0097-0035.
	if p.acceptIdentKeyword("aggregate") {
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Skip the argument type list (parenthesised, may contain ORDER BY).
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			depth := 1
			p.advance()
			for depth > 0 && p.cur().Kind != TokenEOF {
				switch {
				case p.cur().Kind == TokenSymbol && p.cur().Value == "(":
					depth++
				case p.cur().Kind == TokenSymbol && p.cur().Value == ")":
					depth--
					if depth == 0 {
						continue
					}
				}
				p.advance()
			}
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				p.advance()
			}
		}
		// Check for RENAME TO newname.
		if p.acceptIdentKeyword("rename") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			return &AlterAggregateRenameStmt{pos: t.Pos, OldName: name, NewName: newNameTok.Value}, nil
		}
		// Other ALTER AGGREGATE forms: consume as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &AlterTableStmt{pos: t.Pos}, nil
	}
	// ALTER INDEX name ALTER COLUMN col SET (options) — emit the action so
	// the executor can raise the appropriate error. M0097-0023.
	if p.acceptKeyword(KwIndex) {
		// Read the index name (may be schema-qualified).
		idxName, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Check for ALTER COLUMN col SET ...
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
			p.advance() // consume ALTER
			_ = p.acceptKeyword(KwColumn)
			// Column can be a name (identifier/keyword) or a 1-based integer position.
			colName := ""
			if p.cur().Kind == TokenIntLit {
				colName = p.cur().Value
				p.advance()
			} else if p.cur().Kind == TokenIdent || (p.cur().Kind == TokenKeyword && IsColNameKeyword(p.cur().Keyword)) {
				colName = p.cur().Value
				p.advance()
			}
			if p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet) {
				// SET STATISTICS value — emit action for executor to validate.
				if p.acceptIdentKeyword("statistics") {
					statsVal := ""
					if p.cur().Kind == TokenIntLit {
						statsVal = p.cur().Value
						p.advance()
					}
					stmt := &AlterTableStmt{pos: t.Pos, Name: idxName}
					stmt.Actions = append(stmt.Actions, AlterTableAction{
						Kind:       AlterTableSetStatistics,
						ColumnName: colName,
						CheckExpr:  statsVal,
					})
					return stmt, nil
				}
				if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
					// Consume the options block.
					depth := 1
					p.advance() // consume '('
					for depth > 0 && p.cur().Kind != TokenEOF {
						if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
							depth++
						} else if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
							depth--
						}
						p.advance()
					}
					stmt := &AlterTableStmt{pos: t.Pos, Name: idxName}
					stmt.Actions = append(stmt.Actions, AlterTableAction{
						Kind:       AlterTableAlterColumnSet,
						ColumnName: colName,
					})
					return stmt, nil
				}
			}
		}
		// ALTER INDEX parent ATTACH PARTITION child — register index partition hierarchy.
		if p.acceptIdentKeyword("attach") {
			if !p.acceptKeyword(KwPartition) {
				return nil, p.errAtCur("expected PARTITION after ATTACH")
			}
			childName, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			stmt := &AlterTableStmt{pos: t.Pos, Name: idxName}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				Kind:           AlterIndexAttachPartition,
				ConstraintName: idxName.Name,
				ChildIndexName: childName.Name,
			})
			return stmt, nil
		}
		// Other ALTER INDEX forms: consume rest as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &AlterTableStmt{pos: t.Pos}, nil
	}

	// ALTER FUNCTION / PROCEDURE / ROUTINE — may update volatile/security/leakproof/strict attrs.
	funcConsumed := p.acceptKeyword(KwFunction)
	procConsumed := !funcConsumed && p.acceptKeyword(KwProcedure)
	routineConsumed := !funcConsumed && !procConsumed && p.acceptIdentKeyword("routine")
	if funcConsumed || procConsumed || routineConsumed {
		isProcedure := procConsumed
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt := &AlterFunctionStmt{pos: t.Pos, Name: name, IsProcedure: isProcedure, IsRoutine: routineConsumed}
		// Optional arg list
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			args, argErr := p.parseFunctionArgList()
			if argErr == nil {
				stmt.Args = args
			}
		}
		// Consume one or more function attributes
		for p.isFunctionAttribute() || (p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "owner")) ||
			(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "rename")) ||
			(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) ||
			(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReset) {
			// OWNER TO role — no-op (no role system in goopg v0)
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "owner") {
				p.advance() // OWNER
				p.acceptKeyword(KwTo)
				p.advance() // role name (ident or CURRENT_USER etc.)
				continue
			}
			// RENAME TO new_name — store new name in stmt
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "rename") {
				p.advance() // RENAME
				p.acceptKeyword(KwTo)
				newName, _ := p.parseIdent()
				stmt.RenameTo = identText(newName)
				continue
			}
			// SET SCHEMA schema | SET guc_name {TO|=} value | SET FROM CURRENT — no-op
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set") {
				p.advance() // SET
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "schema") {
					p.advance() // SCHEMA
					p.advance() // schema name
				} else if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "from") {
					p.advance() // FROM
					p.acceptIdentKeyword("current")
				} else {
					// SET guc_name {TO|=} value — consume name and value as no-op.
					p.advance() // guc name (or quoted name)
					if p.acceptKeyword(KwTo) || p.acceptSymbol("=") {
						// Consume the value (could be DEFAULT, a literal, or FROM CURRENT).
						if p.acceptIdentKeyword("default") || p.acceptIdentKeyword("from") {
							p.acceptIdentKeyword("current")
						} else {
							p.advance() // value token
						}
					}
				}
				continue
			}
			// RESET guc_name | RESET ALL — no-op
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReset {
				p.advance() // RESET
				// ALL or a guc name.
				if !p.acceptIdentKeyword("all") {
					p.advance() // guc name
				}
				continue
			}
			cur := p.cur()
			if cur.Kind == TokenIdent {
				switch strings.ToLower(cur.Value) {
				case "volatile":
					v := "v"
					stmt.Volatile = &v
				case "stable":
					v := "s"
					stmt.Volatile = &v
				case "immutable":
					v := "i"
					stmt.Volatile = &v
				case "security":
					p.advance()
					if p.acceptIdentKeyword("definer") {
						t := true
						stmt.SecurityDefiner = &t
					} else {
						p.acceptIdentKeyword("invoker")
						f := false
						stmt.SecurityDefiner = &f
					}
					continue
				case "leakproof":
					t := true
					stmt.Leakproof = &t
				case "strict":
					t := true
					stmt.Strict = &t
				case "called":
					p.advance()
					p.acceptKeyword(KwOn)
					p.acceptKeyword(KwNull)
					p.acceptIdentKeyword("input")
					f := false
					stmt.Strict = &f
					continue
				case "returns":
					p.advance()
					p.acceptKeyword(KwNull)
					p.acceptKeyword(KwOn)
					p.acceptKeyword(KwNull)
					p.acceptIdentKeyword("input")
					t := true
					stmt.Strict = &t
					continue
				}
			} else if cur.Kind == TokenKeyword && cur.Keyword == KwNot {
				p.advance()
				p.acceptIdentKeyword("leakproof")
				f := false
				stmt.Leakproof = &f
				continue
			} else if cur.Kind == TokenKeyword && cur.Keyword == KwReturns {
				p.advance()
				p.acceptKeyword(KwNull)
				p.acceptKeyword(KwOn)
				p.acceptKeyword(KwNull)
				p.acceptIdentKeyword("input")
				t := true
				stmt.Strict = &t
				continue
			}
			p.consumeFunctionAttribute()
		}
		return stmt, nil
	}

	// ALTER MATERIALIZED VIEW name SET SCHEMA newschema (M0097-0025).
	if p.acceptIdentKeyword("materialized") {
		// VIEW is a KwCatUnreserved keyword — accept either way.
		_ = p.acceptKeyword(KwView) || p.acceptIdentKeyword("view")
		// Parse the matview name.
		mvName, _ := p.parseObjectName()
		// Check for SET SCHEMA action (SET is KwSet, SCHEMA is an identifier).
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
			p.advance() // SET
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "schema") {
				p.advance() // SCHEMA
				schemaNameTok := p.cur()
				p.advance()
				schemaName := identText(schemaNameTok)
				return &AlterTableStmt{pos: t.Pos, Name: mvName, SetSchema: schemaName}, nil
			}
		}
		// Other ALTER MATERIALIZED VIEW actions — consume until ';'.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &AlterTableStmt{pos: t.Pos}, nil
	}
	// ALTER VIEW / SCHEMA / COLLATION / DOMAIN / EXTENSION / LANGUAGE / OPERATOR / PUBLICATION /
	// SUBSCRIPTION / SYSTEM — compatibility stubs. Consume until end of statement.
	for _, objIdent := range []string{
		"schema", "view",
		"collation", "domain", "extension", "language",
		"operator", "publication", "subscription", "system",
	} {
		if p.acceptIdentKeyword(objIdent) {
			// consume until ';' or EOF
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &AlterTableStmt{pos: t.Pos}, nil
		}
	}
	// ALTER TABLE [IF EXISTS] [ONLY] name …
	if _, err := p.expectKeyword(KwTable); err != nil {
		return nil, err
	}
	stmt := &AlterTableStmt{pos: t.Pos}
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
		stmt.IfExists = true
	}
	// Optional ONLY modifier (inheritance exclusion) — accept and discard.
	_ = p.acceptIdentKeyword("only")
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
	// Optional trailing '*' (include children) — accept and discard.
	if p.cur().Kind == TokenOperator && p.cur().Value == "*" {
		p.advance()
	}
	// OWNER TO role — record the target role name so the executor can update the
	// relation's owner (drives the VACUUM/ANALYZE/CLUSTER maintenance-privilege
	// check, M0118-0008). "owner" is an identifier in goopg's lexer.
	if p.acceptIdentKeyword("owner") {
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
		// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
		// superuser in goopg (no real role identity for the session); a plain
		// identifier names the new owning role.
		if p.acceptIdentKeyword("current_user") ||
			p.acceptIdentKeyword("session_user") ||
			p.acceptIdentKeyword("current_role") {
			stmt.OwnerTo = "" // bootstrap superuser
		} else if tok, err := p.parseIdent(); err == nil {
			stmt.OwnerTo = identText(tok)
		}
		if stmt.OwnerTo == "" {
			// No explicit role captured (current_user etc.): still mark the
			// statement as an OWNER TO action so the executor takes the DDL lock
			// and does not fall through to "no actions". Use a sentinel the
			// executor maps back to the bootstrap superuser.
			stmt.OwnerTo = "current_user"
		}
		return stmt, nil
	}
	// RENAME COLUMN old TO new  |  RENAME TO new_name  |  RENAME VALUE 'old' TO 'new'.
	if p.acceptIdentKeyword("rename") {
		// RENAME COLUMN old_name TO new_name
		if p.acceptKeyword(KwColumn) {
			oldNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				pos:           oldNameTok.Pos,
				Kind:          AlterTableRenameColumn,
				OldColumnName: identText(oldNameTok),
				NewName:       identText(newNameTok),
			})
			return stmt, nil
		}
		// RENAME VALUE 'old' TO 'new' (enum) — no-op: consume rest.
		if p.acceptIdentKeyword("value") {
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return stmt, nil
		}
		// RENAME TO new_name
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
		newNameTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.Actions = append(stmt.Actions, AlterTableAction{
			pos:     newNameTok.Pos,
			Kind:    AlterTableRenameTable,
			NewName: identText(newNameTok),
		})
		return stmt, nil
	}
	// SET LOGGED / SET UNLOGGED — parse and store action for executor.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
		if p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "logged") {
			p.advance() // SET
			p.advance() // LOGGED
			stmt.SetLogged = "logged"
			return stmt, nil
		}
		if (p.peek(1).Kind == TokenIdent || p.peek(1).Kind == TokenKeyword) && strings.EqualFold(p.peek(1).Value, "unlogged") {
			p.advance() // SET
			p.advance() // UNLOGGED
			stmt.SetLogged = "unlogged"
			return stmt, nil
		}
	}
	// SET SCHEMA schema_name — update table/view schema. M0097-0025.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
		if p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
			p.advance() // SET
			p.advance() // SCHEMA
			schemaNameTok := p.cur()
			p.advance()
			stmt.SetSchema = identText(schemaNameTok)
			return stmt, nil
		}
	}
	// ENABLE/DISABLE [REPLICA|ALWAYS] TRIGGER | RULE — semantic no-op in v0.
	// The TRIGGER variant takes a ShareRowExclusiveLock in PostgreSQL, so flag
	// it for the executor to acquire that transaction-scoped lock (alter-table-3
	// isolation spec, M0118-0008). RULE / other variants keep the old no-op.
	if p.acceptIdentKeyword("enable") || p.acceptIdentKeyword("disable") {
		isTrigger := false
		// consume rest of statement until ';' or EOF, noting a TRIGGER target.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTrigger {
				isTrigger = true
			}
			p.advance()
		}
		stmt.EnableDisableTrigger = isTrigger
		return stmt, nil
	}
	// DROP CONSTRAINT name [RESTRICT|CASCADE] — real action (M0097-0036).
	// DROP sub-commands (DROP COLUMN, DROP CONSTRAINT) are handled by
	// parseAlterTableAction() to support comma-separated multi-action ALTER TABLE
	// statements (e.g. "ALTER TABLE t DROP COLUMN a, DROP COLUMN b"). Fall through
	// to the multi-action loop below. M0097-0028.
	// ALTER COLUMN — handle SET (options) and TYPE specially; consume other forms as no-op.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
		p.advance() // consume ALTER
		// Skip COLUMN keyword if present.
		_ = p.acceptKeyword(KwColumn)
		// Read the column name.
		colName := ""
		if p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent {
			colName = p.cur().Value
			p.advance()
		}
		// Check for SET (options) or SET STORAGE pattern.
		if p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet) {
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				// SET (opt=value, …) — per-column attribute options (e.g.
				// n_distinct). Capture each pair, normalized to PG's stored
				// `name=value` form, so pg_dump re-emits the clause via
				// pg_attribute.attoptions. DU-002 slice 185.
				opts := p.parseColumnSetOptions()
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:       AlterTableAlterColumnSet,
					ColumnName: colName,
					SetOptions: opts,
				})
				return stmt, nil
			}
			// SET STORAGE type — record storage strategy on the catalog column.
			if p.acceptIdentKeyword("storage") {
				storageType := ""
				if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
					storageType = strings.ToLower(p.cur().Value)
					p.advance()
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:        AlterTableSetStorage,
					ColumnName:  colName,
					StorageType: storageType,
				})
				return stmt, nil
			}
			// SET COMPRESSION method — record TOAST compression on the catalog
			// column for pg_dump round-trip fidelity (goopg does not TOAST).
			// DU-002 slice 183.
			if p.acceptIdentKeyword("compression") {
				method := ""
				if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
					method = p.cur().Value
					p.advance()
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:            AlterTableSetCompression,
					ColumnName:      colName,
					CompressionType: normalizeCompressionMethod(method),
				})
				return stmt, nil
			}
			// SET STATISTICS value — record the per-column statistics target on the
			// catalog column for pg_dump round-trip fidelity. pg_dump emits an
			// `ALTER TABLE ONLY ... ALTER COLUMN ... SET STATISTICS <n>` whenever
			// attstattarget >= 0 (pg_dump.c dumpTableSchema); the default (-1) emits
			// nothing. goopg does not sample statistics targets at this granularity;
			// the value is recorded purely so the column round-trips. DU-002 slice 184.
			if p.acceptIdentKeyword("statistics") {
				statsVal := ""
				if (p.cur().Kind == TokenOperator || p.cur().Kind == TokenSymbol) && p.cur().Value == "-" {
					// SET STATISTICS -1 resets to the default.
					statsVal = "-"
					p.advance()
				}
				if p.cur().Kind == TokenIntLit {
					statsVal += p.cur().Value
					p.advance()
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:       AlterTableSetStatistics,
					ColumnName: colName,
					CheckExpr:  statsVal,
				})
				return stmt, nil
			}
			// SET DEFAULT expr — record the parsed DEFAULT expression on the
			// catalog column. pg_dump re-emits it inline on a printed local
			// column, or as a separate `ALTER TABLE ONLY ... ALTER COLUMN ...
			// SET DEFAULT` when the column is a suppressed inherited column.
			// DU-002 slice 269.
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
				p.advance() // consume DEFAULT
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:        AlterTableSetDefault,
					ColumnName:  colName,
					DefaultExpr: expr,
				})
				return stmt, nil
			}
			// SET NOT NULL — mark the column NOT NULL. The executor records a
			// contype='n' constraint so pg_dump re-emits the NOT NULL: inline on
			// a printed local column, or as a standalone `NOT NULL <col>` item in
			// the child CREATE TABLE body when the column is a suppressed
			// inherited column. DU-002 slice 270.
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot {
				p.advance() // consume NOT
				if _, err := p.expectKeyword(KwNull); err != nil {
					return nil, err
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:       AlterTableSetNotNull,
					ColumnName: colName,
				})
				return stmt, nil
			}
		}
		// DROP DEFAULT / DROP NOT NULL — clear the column's DEFAULT expression or
		// NOT NULL flag. Other DROP forms (DROP IDENTITY, …) fall through to the
		// no-op consume below for now. DU-002 slices 269, 270.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDrop {
			p.advance() // consume DROP
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
				p.advance() // consume DEFAULT
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:       AlterTableDropDefault,
					ColumnName: colName,
				})
				return stmt, nil
			}
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot {
				p.advance() // consume NOT
				if _, err := p.expectKeyword(KwNull); err != nil {
					return nil, err
				}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind:       AlterTableDropNotNull,
					ColumnName: colName,
				})
				return stmt, nil
			}
		}
		// Check for TYPE newtype pattern.
		// "type" is not in goopg's keyword map — arrives as TokenIdent.
		if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "type") {
			p.advance() // consume TYPE
			newType, err := p.parseColumnType()
			if err != nil {
				return nil, err
			}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				Kind:       AlterTableAlterColumnType,
				ColumnName: colName,
				NewType:    newType,
			})
			return stmt, nil
		}
		// Other ALTER COLUMN forms: consume rest as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	first, err := p.parseAlterTableAction()
	if err != nil {
		return nil, err
	}
	stmt.Actions = append(stmt.Actions, first)
	for p.acceptSymbol(",") {
		next, err := p.parseAlterTableAction()
		if err != nil {
			return nil, err
		}
		stmt.Actions = append(stmt.Actions, next)
	}
	return stmt, nil
}

// parseColumnSetOptions parses a parenthesized per-column attribute option list
// (`(opt=value, …)`) starting at the opening `(`, consuming through the matching
// `)`. Each option is normalized to PG's stored `name=value` form so it can be
// recorded on catalog.Column.Options and re-emitted by pg_dump via
// pg_attribute.attoptions (the dump renders `array_to_string(attoptions, ', ')`).
// A bare option name with no `=value` is captured verbatim. DU-002 slice 185.
func (p *parser) parseColumnSetOptions() []string {
	var opts []string
	if !(p.cur().Kind == TokenSymbol && p.cur().Value == "(") {
		return opts
	}
	p.advance() // consume '('
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			p.advance() // consume ')'
			break
		}
		// Option name.
		name := ""
		if p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent || p.cur().Kind == TokenKeyword {
			name = p.cur().Value
			p.advance()
		}
		// Optional '= value'.
		val := ""
		hasVal := false
		if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
			hasVal = true
			p.advance() // consume '='
			// Collect value tokens up to ',' or ')'. A leading '-' lexes as an
			// operator (negative n_distinct), so concatenate token values with
			// no spaces to reconstruct e.g. "-0.5".
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && (p.cur().Value == "," || p.cur().Value == ")") {
					break
				}
				val += p.cur().Value
				p.advance()
			}
		}
		if name != "" {
			if hasVal {
				opts = append(opts, name+"="+val)
			} else {
				opts = append(opts, name)
			}
		}
		// Skip a trailing comma between options.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
			p.advance()
		}
	}
	return opts
}

// isAlterReloptVerb reports whether tok begins a table-level reloptions action
// (`SET (...)` or `RESET (...)`). SET/RESET are unreserved keywords, so they may
// arrive as a keyword token or, in some lexing contexts, a bare identifier;
// accept both spellings (mirroring the ALTER COLUMN ... SET dispatch). M0118-0001.
func isAlterReloptVerb(tok Token) bool {
	if tok.Kind == TokenKeyword && (tok.Keyword == KwSet || tok.Keyword == KwReset) {
		return true
	}
	if tok.Kind == TokenIdent && (strings.EqualFold(tok.Value, "set") || strings.EqualFold(tok.Value, "reset")) {
		return true
	}
	return false
}

func (p *parser) parseAlterTableAction() (AlterTableAction, error) {
	// ATTACH PARTITION child FOR VALUES … (M0096-0007)
	if p.acceptIdentKeyword("attach") {
		if !p.acceptKeyword(KwPartition) {
			return AlterTableAction{}, p.errAtCur("expected PARTITION after ATTACH")
		}
		childName, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		poc := &PartitionOfClause{pos: childName.pos, Parent: childName}
		// Accept bare DEFAULT (without FOR VALUES) — PostgreSQL allows both
		// `FOR VALUES DEFAULT` and just `DEFAULT`.
		if p.acceptKeyword(KwDefault) {
			poc.Default = true
		} else if p.acceptKeyword(KwFor) {
			if _, err := p.expectKeyword(KwValues); err != nil {
				return AlterTableAction{}, err
			}
			if p.acceptKeyword(KwDefault) || p.acceptIdentKeyword("default") {
				poc.Default = true
			} else if p.acceptKeyword(KwWith) {
				// HASH: WITH (MODULUS n, REMAINDER r). M0097-0015.
				if p.acceptSymbol("(") {
					poc.IsHash = true
					for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
						if p.acceptIdentKeyword("modulus") {
							if t := p.cur(); t.Kind == TokenIntLit {
								p.advance()
								n := int64(0)
								for _, c := range t.Value {
									n = n*10 + int64(c-'0')
								}
								poc.Modulus = n
							}
						} else if p.acceptIdentKeyword("remainder") {
							if t := p.cur(); t.Kind == TokenIntLit {
								p.advance()
								n := int64(0)
								for _, c := range t.Value {
									n = n*10 + int64(c-'0')
								}
								poc.Remainder = n
							}
						} else {
							p.advance()
						}
						_ = p.acceptSymbol(",")
					}
				}
			} else if p.acceptKeyword(KwIn) {
				if !p.acceptSymbol("(") {
					return AlterTableAction{}, p.errAtCur("expected '(' after IN")
				}
				if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
					return AlterTableAction{}, p.errSyntaxAtCur()
				}
				vals, err := p.parseExprList()
				if err != nil {
					return AlterTableAction{}, err
				}
				poc.InValues = vals
				if !p.acceptSymbol(")") {
					return AlterTableAction{}, p.errAtCur("expected ')'")
				}
			} else if p.acceptKeyword(KwFrom) || p.acceptIdentKeyword("from") {
				if !p.acceptSymbol("(") {
					return AlterTableAction{}, p.errAtCur("expected '('")
				}
				fromVals, err := p.parsePartitionBoundValues()
				if err != nil {
					return AlterTableAction{}, err
				}
				poc.FromValues = fromVals
				if !p.acceptSymbol(")") {
					return AlterTableAction{}, p.errAtCur("expected ')'")
				}
				if !p.acceptKeyword(KwTo) {
					return AlterTableAction{}, p.errAtCur("expected TO")
				}
				if !p.acceptSymbol("(") {
					return AlterTableAction{}, p.errAtCur("expected '('")
				}
				toVals, err := p.parsePartitionBoundValues()
				if err != nil {
					return AlterTableAction{}, err
				}
				poc.ToValues = toVals
				if !p.acceptSymbol(")") {
					return AlterTableAction{}, p.errAtCur("expected ')'")
				}
			}
		}
		act := AlterTableAction{pos: poc.pos, Kind: AlterTableAttachPartition, AttachPartitionOf: poc}
		return act, nil
	}
	// DETACH PARTITION child [CONCURRENTLY|FINALIZE]
	if p.acceptIdentKeyword("detach") {
		_ = p.acceptKeyword(KwPartition)
		// Accept optional CONCURRENTLY / FINALIZE (PG14+) — ignored for now.
		p.acceptIdentKeyword("concurrently")
		p.acceptIdentKeyword("finalize")
		childName, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableDetachPartition, DetachPartitionChild: childName}, nil
	}
	// INHERIT parent_table (M0097-0048)
	if p.acceptIdentKeyword("inherit") {
		parentName, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableInherit, InheritParent: parentName}, nil
	}
	// NO INHERIT parent_table (M0097-0048) — no-op in v0
	if p.acceptIdentKeyword("no") {
		if !p.acceptIdentKeyword("inherit") {
			return AlterTableAction{}, p.errAtCur("expected INHERIT after NO")
		}
		parentName, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableNoInherit, InheritParent: parentName}, nil
	}
	// VALIDATE CONSTRAINT name — validate a constraint added with NOT VALID.
	// VALIDATE is not a reserved keyword in goopg's lexer, so match it as an
	// identifier-keyword. M0118-0008 (alter-table-1).
	if p.acceptIdentKeyword("validate") {
		if !p.acceptKeyword(KwConstraint) {
			return AlterTableAction{}, p.errAtCur("expected CONSTRAINT after VALIDATE")
		}
		nameTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{
			pos:            nameTok.Pos,
			Kind:           AlterTableValidateConstraint,
			ConstraintName: identText(nameTok),
		}, nil
	}
	// DROP COLUMN name / DROP CONSTRAINT name in the multi-action loop.
	// Both forms share this path so comma-separated "DROP COLUMN a, DROP COLUMN b"
	// work correctly. M0097-0028.
	if p.acceptKeyword(KwDrop) {
		if p.acceptKeyword(KwConstraint) {
			nameTok, err := p.parseIdent()
			if err != nil {
				return AlterTableAction{}, err
			}
			restrict := true
			if p.acceptKeyword(KwCascade) {
				restrict = false
			} else {
				_ = p.acceptKeyword(KwRestrict)
			}
			return AlterTableAction{
				pos:            nameTok.Pos,
				Kind:           AlterTableDropConstraint,
				ConstraintName: identText(nameTok),
				Restrict:       restrict,
			}, nil
		}
		// DROP COLUMN [IF EXISTS] col_name [RESTRICT|CASCADE]
		_ = p.acceptKeyword(KwColumn)
		_ = p.acceptIdentKeyword("if") && p.acceptIdentKeyword("exists")
		colTok := p.cur()
		if colTok.Kind == TokenIdent || colTok.Kind == TokenQuotedIdent {
			p.advance()
			_ = p.acceptKeyword(KwCascade) || p.acceptKeyword(KwRestrict)
			return AlterTableAction{
				pos:        colTok.Pos,
				Kind:       AlterTableDropColumn,
				ColumnName: identText(colTok),
			}, nil
		}
		return AlterTableAction{}, p.errAtCur("expected column or constraint name after DROP")
	}
	// SET (reloptions) / RESET (reloptions) — table-level storage parameters,
	// e.g. `ALTER TABLE foo SET (parallel_workers = 2)` or
	// `RESET (fillfactor)`. Only the parenthesized form is a reloptions update;
	// the bare SET SCHEMA / SET TABLESPACE / SET LOGGED actions are distinct and
	// fall through to the ADD/DROP dispatch below (unchanged). RESET shares the
	// WITH-options parser — its option list carries bare names (empty values),
	// and the executor clears the named storage parameters. M0118-0001.
	if cur := p.cur(); isAlterReloptVerb(cur) && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
		reset := cur.Kind == TokenKeyword && cur.Keyword == KwReset ||
			cur.Kind == TokenIdent && strings.EqualFold(cur.Value, "reset")
		pos := cur.Pos
		p.advance() // SET or RESET
		opts, err := p.parseWithOptions()
		if err != nil {
			return AlterTableAction{}, err
		}
		kind := AlterTableSetReloptions
		if reset {
			kind = AlterTableResetReloptions
		}
		return AlterTableAction{pos: pos, Kind: kind, With: opts}, nil
	}
	if !p.acceptKeyword(KwAdd) {
		return AlterTableAction{}, p.errAtCur("expected ADD or DROP")
	}
	pos := p.cur().Pos
	act := AlterTableAction{pos: pos}
	// Optional CONSTRAINT name.
	if p.acceptKeyword(KwConstraint) {
		nameTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		act.ConstraintName = identText(nameTok)
	}
	switch {
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
		p.advance()
		if _, err := p.expectKeyword(KwKey); err != nil {
			return AlterTableAction{}, err
		}
		// PRIMARY KEY USING INDEX name — adopt existing index. Treat as no-op.
		if p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using") {
			p.acceptKeyword(KwIndex) // consume INDEX keyword
			_, _ = p.parseIdent()    // consume index name
			return AlterTableAction{pos: pos, Kind: AlterTableNoOp}, nil
		}
		if !p.acceptSymbol("(") {
			return AlterTableAction{}, p.errAtCur("expected '(' after PRIMARY KEY")
		}
		cols, err := p.parseColumnNameList()
		if err != nil {
			return AlterTableAction{}, err
		}
		if !p.acceptSymbol(")") {
			return AlterTableAction{}, p.errAtCur("expected ')'")
		}
		act.Kind = AlterTableAddPrimaryKey
		act.Columns = cols
		// Optional INCLUDE (col, …) — parse and store covering columns. M0097-0023.
		if p.acceptIdentKeyword("include") {
			if p.acceptSymbol("(") {
				for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
						act.IncludeColumns = append(act.IncludeColumns, p.cur().Value)
					}
					p.advance()
				}
			}
		}
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign:
		// ADD [CONSTRAINT name] FOREIGN KEY (cols) REFERENCES
		// table (cols) [NOT DEFERRABLE | DEFERRABLE]. v0
		// records the shape but does not enforce — mirrors
		// upstream's "syntax accepted, lookup deferred" stance
		// for the duration of TPC-H load. See
		// docs/design/0003-0004-hammerdb-tpch-integration.md.
		p.advance()
		if _, err := p.expectKeyword(KwKey); err != nil {
			return AlterTableAction{}, err
		}
		if !p.acceptSymbol("(") {
			return AlterTableAction{}, p.errAtCur("expected '(' after FOREIGN KEY")
		}
		cols, err := p.parseColumnNameList()
		if err != nil {
			return AlterTableAction{}, err
		}
		if !p.acceptSymbol(")") {
			return AlterTableAction{}, p.errAtCur("expected ')'")
		}
		if _, err := p.expectKeyword(KwReferences); err != nil {
			return AlterTableAction{}, err
		}
		refTable, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		var refCols []string
		if p.acceptSymbol("(") {
			refCols, err = p.parseColumnNameList()
			if err != nil {
				return AlterTableAction{}, err
			}
			if !p.acceptSymbol(")") {
				return AlterTableAction{}, p.errAtCur("expected ')'")
			}
		}
		// Optional ON DELETE / ON UPDATE referential-action clauses, mirroring
		// the inline column-FK path so actions survive into pg_constraint and
		// pg_dump (DU-002 slice 52). ON is KwOn (reserved).
		var onDelete, onUpdate FKAction
		for p.acceptKeyword(KwOn) {
			isDelete := p.acceptKeyword(KwDelete)
			if !isDelete {
				_ = p.acceptKeyword(KwUpdate)
			}
			action := parseFKAction(p)
			if isDelete {
				onDelete = action
			} else {
				onUpdate = action
			}
		}
		// Optional [NOT] DEFERRABLE [INITIALLY …] and/or NOT VALID trailers, in
		// any order (PG grammar allows `… DEFERRABLE NOT VALID` etc.). `NOT VALID`
		// (ALTER TABLE ADD FOREIGN KEY … NOT VALID) creates the constraint without
		// checking pre-existing rows; a later VALIDATE CONSTRAINT performs the
		// scan. M0118-0008 (alter-table-1/2 isolation specs).
		deferrable := false
		notValid := false
		for {
			if p.acceptKeyword(KwNot) {
				if p.acceptIdentKeyword("valid") {
					notValid = true
					continue
				}
				if _, err := p.expectKeyword(KwDeferrable); err != nil {
					return AlterTableAction{}, err
				}
				deferrable = false
				continue
			}
			if p.acceptKeyword(KwDeferrable) {
				deferrable = true
				if p.acceptIdentKeyword("initially") {
					_ = p.acceptIdentKeyword("deferred") || p.acceptIdentKeyword("immediate")
				}
				continue
			}
			break
		}
		act.Kind = AlterTableAddForeignKey
		act.Columns = cols
		act.RefTable = refTable
		act.RefColumns = refCols
		act.Deferrable = deferrable
		act.NotValid = notValid
		act.OnDelete = onDelete
		act.OnUpdate = onUpdate
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
		// ADD [CONSTRAINT name] CHECK (expr) — register the check constraint.
		p.advance() // consume CHECK
		expr, err := p.parseCheckExpr()
		if err != nil {
			return AlterTableAction{}, err
		}
		// Consume optional NOT VALID and/or [NOT] ENFORCED trailers (PG18+).
		// Possible orderings: NOT VALID, ENFORCED, NOT ENFORCED, NOT VALID ENFORCED.
		if p.acceptKeyword(KwNot) {
			if !p.acceptIdentKeyword("valid") {
				_ = p.acceptIdentKeyword("enforced") // NOT ENFORCED
			} else {
				// NOT VALID — also accept optional trailing [NOT] ENFORCED.
				if p.acceptKeyword(KwNot) {
					_ = p.acceptIdentKeyword("enforced")
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
			}
		} else {
			_ = p.acceptIdentKeyword("enforced") // bare ENFORCED
		}
		act.Kind = AlterTableAddCheck
		act.CheckExpr = expr
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
		// ADD [CONSTRAINT name] UNIQUE (cols) [INCLUDE (incl)] — create a unique index.
		// M0097-0023.
		p.advance()
		if !(p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using")) || !(p.acceptKeyword(KwIndex) || p.acceptIdentKeyword("index")) {
			// Normal UNIQUE (cols) form.
			if p.acceptSymbol("(") {
				var cols []string
				for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
						cols = append(cols, p.cur().Value)
					}
					p.advance()
				}
				act.Columns = cols
				// Optional INCLUDE (col, …).
				if p.acceptIdentKeyword("include") {
					if p.acceptSymbol("(") {
						for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
							if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
								act.IncludeColumns = append(act.IncludeColumns, p.cur().Value)
							}
							p.advance()
						}
					}
				}
			}
		} else {
			// ADD UNIQUE USING INDEX indexname — adopt existing index as unique constraint.
			// Treat as no-op; the index already exists in the catalog. M0097-0023.
			_, _ = p.parseIdent() // consume indexname
			return AlterTableAction{pos: pos, Kind: AlterTableNoOp}, nil
		}
		act.Kind = AlterTableAddUnique
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot:
		// ADD [CONSTRAINT name] NOT NULL col [NO INHERIT] — PG18 named NOT NULL
		// constraint. The named counterpart of `ALTER COLUMN col SET NOT NULL`:
		// when ConstraintName differs from PG's auto-name (<table>_<col>_not_null)
		// pg_dump prints `CONSTRAINT <name> NOT NULL <col>`. DU-002 slice 271.
		p.advance() // consume NOT
		if _, err := p.expectKeyword(KwNull); err != nil {
			return AlterTableAction{}, err
		}
		colTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		act.ColumnName = identText(colTok)
		// Optional `NO INHERIT` trailer (connoinherit='t').
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("inherit")
			act.NoInherit = true
		}
		act.Kind = AlterTableAddNotNull
		return act, nil
	default:
		// ADD [COLUMN] column_def — bare ident or COLUMN keyword.
		if act.ConstraintName != "" {
			// Unknown constraint type with something after it (e.g. UNIQUE, EXCLUDE).
			// Accept as no-op if there's actual content; error if at EOF/nothing.
			if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
				return AlterTableAction{}, p.errAtCur("expected PRIMARY KEY or FOREIGN KEY after CONSTRAINT name")
			}
			// Skip to semicolon or EOF.
			for p.cur().Kind != TokenSymbol || p.cur().Value != ";" {
				if p.cur().Kind == TokenEOF {
					break
				}
				p.advance()
			}
			return AlterTableAction{pos: pos, Kind: AlterTableNoOp}, nil
		}
		_ = p.acceptKeyword(KwColumn)
		col, err := p.parseColumnDef()
		if err != nil {
			return AlterTableAction{}, err
		}
		act.Kind = AlterTableAddColumn
		act.Column = col
		return act, nil
	}
}

// parseCreateType picks up after CREATE TYPE has been detected.
// Grammar: CREATE TYPE name AS ENUM ('val1' [, 'val2' ...])
// Non-ENUM forms (composite, range, base) are accepted but ignored as stubs.
// M0097-0017.
func (p *parser) parseCreateType(pos int) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt := &CreateTypeStmt{pos: pos, Name: name.Name, Schema: name.Schema}
	// Look for AS ENUM; anything else is consumed as a stub.
	if !p.acceptKeyword(KwAs) {
		// No AS — consume until ';' or EOF (composite-type without AS, or
		// other non-enum forms). Return a non-enum stub.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	if !p.acceptIdentKeyword("enum") {
		// AS ( ... ) — composite type with field list.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			stmt.IsComposite = true
			for {
				if p.cur().Kind != TokenIdent {
					break
				}
				fname := strings.ToLower(p.advance().Value)
				// Collect type tokens until a TOP-LEVEL ',' (next field) or ')'
				// (end of field list). Track paren depth so a typmod like
				// numeric(10,2) or varchar(8) — whose inner ',' / ')' would
				// otherwise prematurely terminate the field — is captured intact.
				// parseCompositeFieldType (executor) reads the space-joined form.
				// DU-002 slice 247.
				var typeParts []string
				parenDepth := 0
				for p.cur().Kind != TokenEOF {
					tok := p.cur()
					if tok.Kind == TokenSymbol && parenDepth == 0 &&
						(tok.Value == "," || tok.Value == ")") {
						break
					}
					// A top-level COLLATE clause is not part of the type — stop
					// here and capture it separately so the field's atttypid stays
					// clean and its attcollation round-trips. DU-002 slice 257.
					if parenDepth == 0 && tok.Kind != TokenSymbol &&
						strings.EqualFold(tok.Value, "collate") {
						break
					}
					if tok.Kind == TokenSymbol && tok.Value == "(" {
						parenDepth++
					} else if tok.Kind == TokenSymbol && tok.Value == ")" {
						parenDepth--
					}
					typeParts = append(typeParts, tok.Value)
					p.advance()
				}
				field := TypeField{
					Name:    fname,
					ColType: strings.Join(typeParts, " "),
				}
				// Optional per-field COLLATE "<name>" — record the collation so the
				// field's pg_attribute.attcollation shadows the type default and
				// pg_dump's dumpCompositeType re-emits a `COLLATE` clause, mirroring
				// the table-column path (slice 188). goopg v0 does not actually
				// collate; the name is kept for dump fidelity. DU-002 slice 257.
				if p.acceptIdentKeyword("collate") {
					collName, _ := p.parseCollationName()
					field.Collation = collName
				}
				stmt.CompositeFields = append(stmt.CompositeFields, field)
				if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
					p.advance() // consume ','
				} else {
					break
				}
			}
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				p.advance() // consume ')'
			}
			return stmt, nil
		}
		// AS <something-else> — stub: consume remaining tokens.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	stmt.IsEnum = true
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after ENUM")
	}
	// Parse comma-separated string literals.
	for {
		if p.cur().Kind != TokenStringLit {
			return nil, p.errAtCur("expected string literal in ENUM value list")
		}
		stmt.EnumValues = append(stmt.EnumValues, p.cur().Value)
		p.advance()
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' after ENUM value list")
	}
	return stmt, nil
}

// parseAlterType picks up after ALTER TYPE has been detected.
// Handles ADD VALUE; all other ALTER TYPE forms are consumed as stubs.
// M0097-0017.
func (p *parser) parseAlterType(pos int) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt := &AlterTypeStmt{pos: pos, Name: name.Name, Schema: name.Schema}

	// Attribute subcommand list: ADD/DROP/ALTER ATTRIBUTE …, optionally
	// comma-combined. PG's ALTER TYPE grammar only allows these three actions to
	// be combined in one statement (ADD VALUE / RENAME / OWNER TO are singular and
	// handled by the dedicated branches below). DU-002 slices 253/255/256/258/259
	// for the single forms; slice 260 adds the comma-combined case.
	// parseOneAttrCmd backtracks and reports ok=false for the non-attribute forms,
	// so this is tried first without disturbing them.
	if cmd, ok, acErr := p.parseOneAttrCmd(); acErr != nil {
		return nil, acErr
	} else if ok {
		stmt.AttrCmds = append(stmt.AttrCmds, cmd)
		for p.acceptSymbol(",") {
			cmd2, ok2, acErr2 := p.parseOneAttrCmd()
			if acErr2 != nil {
				return nil, acErr2
			}
			if !ok2 {
				// A trailing non-attribute subcommand (e.g. OWNER TO) — stub-consume
				// to ';'/EOF, matching the legacy single-subcommand trailers.
				for p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
						break
					}
					p.advance()
				}
				break
			}
			stmt.AttrCmds = append(stmt.AttrCmds, cmd2)
		}
		// Mirror the first subcommand into the legacy scalar fields so the
		// single-subcommand executor branches and the existing parser unit tests
		// observe identical AST; the executor routes len(AttrCmds) > 1 through
		// execAlterTypeAttrCmds. DU-002 slice 260.
		p.mirrorFirstAttrCmd(stmt)
		return stmt, nil
	}

	// ADD VALUE [IF NOT EXISTS] 'val' [BEFORE|AFTER 'ref']
	// NOTE: ADD is a reserved keyword (KwAdd), not an ident keyword — use acceptKeyword.
	// (ADD ATTRIBUTE is handled above by parseOneAttrCmd.)
	if p.acceptKeyword(KwAdd) {
		if !p.acceptIdentKeyword("value") {
			// consume until ';' or EOF
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return stmt, nil
		}
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwNot); err != nil {
				return nil, err
			}
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			stmt.IfNotExists = true
		}
		if p.cur().Kind != TokenStringLit {
			return nil, p.errAtCur("expected string literal for new enum value")
		}
		stmt.AddValue = p.cur().Value
		p.advance()
		// Optional BEFORE 'ref' or AFTER 'ref'.
		switch {
		case p.acceptIdentKeyword("before"):
			if p.cur().Kind != TokenStringLit {
				return nil, p.errAtCur("expected string literal after BEFORE")
			}
			stmt.Before = p.cur().Value
			p.advance()
		case p.acceptIdentKeyword("after"):
			if p.cur().Kind != TokenStringLit {
				return nil, p.errAtCur("expected string literal after AFTER")
			}
			stmt.After = p.cur().Value
			p.advance()
		}
		return stmt, nil
	}
	// (DROP ATTRIBUTE is handled above by parseOneAttrCmd.) Any other DROP
	// variant — consume as a stub. DROP is a reserved keyword (KwDrop).
	if p.acceptKeyword(KwDrop) {
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// (ALTER ATTRIBUTE is handled above by parseOneAttrCmd.) Any other ALTER
	// variant — consume as a stub. ALTER is a reserved keyword (KwAlter).
	if p.acceptKeyword(KwAlter) {
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// RENAME VALUE 'old' TO 'new' (enum label rename). M0097-0022.
	if p.acceptIdentKeyword("rename") {
		if p.acceptIdentKeyword("value") {
			// Consume old label string.
			if p.cur().Kind != TokenStringLit {
				return nil, p.errAtCur("expected string literal for old enum value")
			}
			stmt.RenameOldValue = p.cur().Value
			p.advance()
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			// Consume new label string.
			if p.cur().Kind != TokenStringLit {
				return nil, p.errAtCur("expected string literal for new enum value")
			}
			stmt.RenameNewValue = p.cur().Value
			p.advance()
			return stmt, nil
		}
		// RENAME TO new_name — parse the new type name. M0097-enum-rename.
		if p.acceptKeyword(KwTo) {
			newName, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			stmt.RenameTo = newName.Name
			return stmt, nil
		}
		// RENAME ATTRIBUTE old TO new [CASCADE|RESTRICT] — rename a composite
		// type field so the new name round-trips through pg_dump. DU-002 slice 254.
		// RENAME ATTRIBUTE is singular (PG forbids combining it), so it keeps its
		// own legacy path rather than joining AttrCmds.
		if p.acceptIdentKeyword("attribute") {
			if p.cur().Kind != TokenIdent {
				return nil, p.errAtCur("expected attribute name after RENAME ATTRIBUTE")
			}
			stmt.RenameAttrOld = strings.ToLower(p.advance().Value)
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			if p.cur().Kind != TokenIdent {
				return nil, p.errAtCur("expected new attribute name after TO")
			}
			stmt.RenameAttrNew = strings.ToLower(p.advance().Value)
			// Consume trailing CASCADE|RESTRICT to ';'/EOF as a stub.
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return stmt, nil
		}
		// Other RENAME variants — consume as stub.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// Any other ALTER TYPE variant (OWNER TO, etc.) — consume as stub.
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			break
		}
		p.advance()
	}
	return stmt, nil
}

// parseOneAttrCmd parses a single ALTER TYPE attribute subcommand
// (ADD|DROP|ALTER ATTRIBUTE …) starting at its leading keyword. It returns
// ok=true with the parsed command when the current position begins an attribute
// subcommand, and ok=false WITHOUT consuming anything when it does not (e.g.
// ADD VALUE, RENAME VALUE/TO/ATTRIBUTE, OWNER TO) so the caller can fall through
// to those dedicated branches. The cursor (p.idx) is restored on a non-match.
// DU-002 slice 260.
func (p *parser) parseOneAttrCmd() (AlterTypeAttrCmd, bool, error) {
	save := p.idx
	// ADD ATTRIBUTE name type [COLLATE name] [CASCADE|RESTRICT]. DU-002 slice 253/258.
	if p.acceptKeyword(KwAdd) {
		if p.acceptIdentKeyword("attribute") {
			if p.cur().Kind != TokenIdent {
				return AlterTypeAttrCmd{}, false, p.errAtCur("expected attribute name after ADD ATTRIBUTE")
			}
			cmd := AlterTypeAttrCmd{Kind: "add", Name: strings.ToLower(p.advance().Value)}
			cmd.Type = p.parseAttrTypeTokens()
			if p.acceptIdentKeyword("collate") {
				cmd.Collation, _ = p.parseCollationName()
			}
			p.consumeAttrCmdTrailer()
			return cmd, true, nil
		}
		p.idx = save
		return AlterTypeAttrCmd{}, false, nil
	}
	// DROP ATTRIBUTE [IF EXISTS] name [CASCADE|RESTRICT]. DU-002 slice 255.
	if p.acceptKeyword(KwDrop) {
		if p.acceptIdentKeyword("attribute") {
			cmd := AlterTypeAttrCmd{Kind: "drop"}
			if p.acceptKeyword(KwIf) {
				if _, err := p.expectKeyword(KwExists); err != nil {
					return AlterTypeAttrCmd{}, false, err
				}
				cmd.IfExists = true
			}
			if p.cur().Kind != TokenIdent {
				return AlterTypeAttrCmd{}, false, p.errAtCur("expected attribute name after DROP ATTRIBUTE")
			}
			cmd.Name = strings.ToLower(p.advance().Value)
			p.consumeAttrCmdTrailer()
			return cmd, true, nil
		}
		p.idx = save
		return AlterTypeAttrCmd{}, false, nil
	}
	// ALTER ATTRIBUTE name [SET DATA] TYPE newtype [COLLATE name] [CASCADE|RESTRICT].
	// DU-002 slice 256/259.
	if p.acceptKeyword(KwAlter) {
		if p.acceptIdentKeyword("attribute") {
			if p.cur().Kind != TokenIdent {
				return AlterTypeAttrCmd{}, false, p.errAtCur("expected attribute name after ALTER ATTRIBUTE")
			}
			cmd := AlterTypeAttrCmd{Kind: "alter", Name: strings.ToLower(p.advance().Value)}
			if p.acceptKeyword(KwSet) {
				if !p.acceptIdentKeyword("data") {
					return AlterTypeAttrCmd{}, false, p.errAtCur("expected DATA after SET in ALTER ATTRIBUTE")
				}
			}
			if !p.acceptIdentKeyword("type") {
				return AlterTypeAttrCmd{}, false, p.errAtCur("expected TYPE in ALTER ATTRIBUTE")
			}
			cmd.Type = p.parseAttrTypeTokens()
			if p.acceptIdentKeyword("collate") {
				cmd.Collation, _ = p.parseCollationName()
			}
			p.consumeAttrCmdTrailer()
			return cmd, true, nil
		}
		p.idx = save
		return AlterTypeAttrCmd{}, false, nil
	}
	return AlterTypeAttrCmd{}, false, nil
}

// parseAttrTypeTokens collects the type-token string of an ADD/ALTER ATTRIBUTE
// subcommand, paren-tracking typmods so `numeric(12,3)` / `zip[]` survive intact.
// It stops at a top-level ',' (the next subcommand), ';'/EOF, or a top-level
// COLLATE/USING/CASCADE/RESTRICT keyword (which are not part of the type and are
// parsed/consumed by the caller). DU-002 slice 260 — shared by the ADD and ALTER
// branches of parseOneAttrCmd, mirroring the inline loops of slices 256/258.
func (p *parser) parseAttrTypeTokens() string {
	var typeParts []string
	parenDepth := 0
	for p.cur().Kind != TokenEOF {
		tok := p.cur()
		if tok.Kind == TokenSymbol && tok.Value == ";" {
			break
		}
		if tok.Kind == TokenSymbol && parenDepth == 0 && tok.Value == "," {
			break
		}
		if parenDepth == 0 && tok.Kind != TokenSymbol &&
			(strings.EqualFold(tok.Value, "collate") || strings.EqualFold(tok.Value, "using") ||
				strings.EqualFold(tok.Value, "cascade") || strings.EqualFold(tok.Value, "restrict")) {
			break
		}
		if tok.Kind == TokenSymbol && tok.Value == "(" {
			parenDepth++
		} else if tok.Kind == TokenSymbol && tok.Value == ")" {
			parenDepth--
		}
		typeParts = append(typeParts, tok.Value)
		p.advance()
	}
	return strings.Join(typeParts, " ")
}

// consumeAttrCmdTrailer stub-consumes the optional per-subcommand
// USING/CASCADE/RESTRICT trailer of an attribute subcommand (goopg honours none
// of them). It paren-tracks so a comma inside parens is not mistaken for the
// next subcommand, and stops at a top-level ',' or ';'/EOF. DU-002 slice 260.
func (p *parser) consumeAttrCmdTrailer() {
	parenDepth := 0
	for p.cur().Kind != TokenEOF {
		tok := p.cur()
		if tok.Kind == TokenSymbol && tok.Value == ";" {
			break
		}
		if tok.Kind == TokenSymbol && parenDepth == 0 && tok.Value == "," {
			break
		}
		if tok.Kind == TokenSymbol && tok.Value == "(" {
			parenDepth++
		} else if tok.Kind == TokenSymbol && tok.Value == ")" {
			parenDepth--
		}
		p.advance()
	}
}

// mirrorFirstAttrCmd copies AttrCmds[0] into the legacy scalar fields of the
// statement so single-subcommand ALTER TYPE behaves identically to slices
// 253/255/256/258/259 (executor branches + parser unit tests read the scalar
// fields). The executor only consults the scalar fields when len(AttrCmds) <= 1.
// DU-002 slice 260.
func (p *parser) mirrorFirstAttrCmd(stmt *AlterTypeStmt) {
	if len(stmt.AttrCmds) == 0 {
		return
	}
	c := stmt.AttrCmds[0]
	switch c.Kind {
	case "add":
		stmt.AddAttrName = c.Name
		stmt.AddAttrType = c.Type
		stmt.AddAttrCollation = c.Collation
	case "drop":
		stmt.DropAttrName = c.Name
		stmt.DropAttrIfExists = c.IfExists
	case "alter":
		stmt.AlterAttrName = c.Name
		stmt.AlterAttrType = c.Type
		stmt.AlterAttrCollation = c.Collation
	}
}

// parseDropType picks up after DROP TYPE has been detected.
// Grammar: DROP TYPE [IF EXISTS] name [, name ...] [CASCADE|RESTRICT]
// M0097-0017.
func (p *parser) parseDropType(pos int) (Stmt, error) {
	ifExists, names, behavior, err := p.parseDropTail()
	if err != nil {
		return nil, err
	}
	cascade := behavior == DropCascade
	return &DropTypeStmt{pos: pos, Names: names, IfExists: ifExists, Cascade: cascade}, nil
}

// parseCreateDomain picks up after CREATE DOMAIN has been detected.
// Grammar: CREATE DOMAIN name [AS] base_type [DEFAULT expr] [NOT NULL] [NULL]
//
//	[CHECK (expr)] [COLLATE ...] ...
//
// M0097-0017.
func (p *parser) parseCreateDomain(pos int) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt := &CreateDomainStmt{pos: pos, Name: name.Name, Schema: name.Schema}
	// Optional AS.
	_ = p.acceptKeyword(KwAs)
	// Base type name (may be schema-qualified).
	baseTypeName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.BaseType = baseTypeName.Name
	if baseTypeName.Schema != "" {
		stmt.BaseType = baseTypeName.Schema + "." + baseTypeName.Name
	}
	// Capture optional type-modifier arguments like (20) or (10,2) so the
	// domain's base type round-trips through pg_dump with its declared length/
	// precision: dumpDomain renders format_type(typbasetype, typtypmod), and a
	// discarded typmod dumped varchar(20) as bare `character varying`. The cast
	// PG appends to a string DEFAULT stays typmod-less (format_type(consttype,
	// -1)), so only the base render needs these. DU-002 slice 95.
	if p.acceptSymbol("(") {
		for {
			t := p.cur()
			if t.Kind != TokenIntLit {
				return nil, p.errAtCur("expected integer in type modifier")
			}
			p.advance()
			n, err := strconv.ParseInt(t.Value, 10, 64)
			if err != nil {
				return nil, &SyntaxError{Pos: t.Pos, Message: "invalid integer: " + t.Value}
			}
			stmt.BaseTypeArgs = append(stmt.BaseTypeArgs, n)
			if p.acceptSymbol(",") {
				continue
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ',' or ')'")
			}
			break
		}
	}
	// Accept array notation: int[], text[], etc. Append [] to the base type
	// name and skip any dimension expressions like [N]. M0097-0065.
	for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		p.advance() // consume [
		if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
			// Empty []: plain array type.
			stmt.BaseType += "[]"
			p.advance() // consume ]
		} else {
			// Dimensioned [N]: skip until ].
			for p.cur().Kind != TokenSymbol || p.cur().Value != "]" {
				if p.cur().Kind == TokenEOF {
					break
				}
				p.advance()
			}
			stmt.BaseType += "[]"
			if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
				p.advance() // consume ]
			}
		}
	}
	// Parse constraint list.
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			break
		}
		if p.cur().Kind == TokenKeyword {
			switch p.cur().Keyword {
			case KwNot:
				p.advance()
				if _, err := p.expectKeyword(KwNull); err != nil {
					return nil, err
				}
				stmt.NotNull = true
				continue
			case KwNull:
				p.advance()
				stmt.NotNull = false
				continue
			case KwDefault:
				// DEFAULT expr — parse the expression so it round-trips through
				// pg_dump (typdefaultbin → pg_get_expr). parseExpr stops at the
				// next NOT/NULL/CHECK/CONSTRAINT/COLLATE keyword or ';'. DU-002 slice 92.
				p.advance()
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				stmt.Default = expr
				continue
			case KwConstraint:
				// CONSTRAINT name CHECK (…) — capture the explicit constraint name.
				p.advance()
				cname, _ := p.parseIdent() // constraint name
				// Fall through to CHECK handling below.
				if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
					p.advance()
					if vals := p.tryParseCheckInValues(); vals != nil {
						if stmt.CheckInValues == nil {
							stmt.CheckInValues = vals
							// Preserve the explicit CONSTRAINT name so the
							// deparsed `= ANY (ARRAY[...])` round-trips with the
							// right conname through pg_dump. DU-002 slice 97.
							if stmt.CheckName == "" {
								stmt.CheckName = cname.Value
							}
						}
					} else {
						// Generic CHECK expression: capture the raw predicate text
						// (e.g. `VALUE > 0`) so it round-trips through pg_dump via
						// pg_get_constraintdef. DU-002 slice 96.
						expr, err := p.parseDomainCheckExpr()
						if err != nil {
							return nil, err
						}
						if stmt.CheckExpr == "" {
							stmt.CheckExpr = expr
							stmt.CheckName = cname.Value
						}
					}
				}
				continue
			case KwCheck:
				p.advance()
				if vals := p.tryParseCheckInValues(); vals != nil {
					if stmt.CheckInValues == nil {
						stmt.CheckInValues = vals
					}
				} else {
					// Generic CHECK expression (auto-named <domain>_check). DU-002 slice 96.
					expr, err := p.parseDomainCheckExpr()
					if err != nil {
						return nil, err
					}
					if stmt.CheckExpr == "" {
						stmt.CheckExpr = expr
					}
				}
				continue
			}
		}
		if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "collate" {
			// COLLATE "C" or similar — skip collation name.
			p.advance()
			_, _ = p.parseIdent()
			continue
		}
		// Unknown token at this level — stop constraint parsing.
		break
	}
	return stmt, nil
}

// tryParseCheckInValues tries to parse a CHECK (VALUE IN ('a','b','c')) pattern.
// Returns the list of allowed string values if the pattern matches, or nil if
// the expression does not follow the VALUE IN (...) form (caller should fall back
// to skipParenExpr). M0097-domain-check.
func (p *parser) tryParseCheckInValues() []string {
	// Save position so we can revert if the pattern doesn't match.
	start := p.idx
	if !p.acceptSymbol("(") {
		return nil
	}
	// Expect the keyword VALUE (as identifier).
	if p.cur().Kind != TokenIdent || !strings.EqualFold(p.cur().Value, "value") {
		p.idx = start
		return nil
	}
	p.advance() // consume VALUE
	// Optionally accept a cast on the VALUE left-hand side, e.g.
	// `CHECK (VALUE::text IN (...))`. Types without an equality operator (json)
	// cannot use the bare `VALUE IN (...)` form, so the domain definition casts
	// VALUE to a comparable type (text). The cast type itself is not retained —
	// the deparse shape is decided from the domain's base type — but it must be
	// consumed here so the pattern still matches. DU-002 slice 105 (json).
	if p.cur().Kind == TokenOperator && p.cur().Value == "::" {
		p.advance() // consume "::"
		if _, err := p.parseTypeNameAfterCast(); err != nil {
			p.idx = start
			return nil
		}
	}
	// Expect IN keyword.
	if !p.acceptKeyword(KwIn) {
		p.idx = start
		return nil
	}
	// Expect opening paren for the IN list.
	if !p.acceptSymbol("(") {
		p.idx = start
		return nil
	}
	// Parse the membership list. Accept string literals (text/varchar/bpchar/date
	// domains), integer and numeric literals (integer/numeric/bigint domains), and
	// the boolean keyword literals true/false (boolean domains). The raw token
	// value is stored verbatim; whether to quote/cast it on deparse is decided
	// later from the domain's base type. DU-002 slices 99, 100.
	var vals []string
	for {
		switch {
		case p.cur().Kind == TokenStringLit, p.cur().Kind == TokenIntLit, p.cur().Kind == TokenNumericLit:
			vals = append(vals, p.cur().Value)
		case p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwTrue || p.cur().Keyword == KwFalse):
			// Boolean domain: CHECK (VALUE IN (true, false)). Store the canonical
			// lowercase literal; deparse renders it verbatim (no quotes/cast).
			vals = append(vals, string(p.cur().Keyword))
		default:
			p.idx = start
			return nil
		}
		p.advance()
		if p.acceptSymbol(",") {
			continue
		}
		break
	}
	// Expect closing paren for IN list.
	if !p.acceptSymbol(")") {
		p.idx = start
		return nil
	}
	// Expect closing paren for CHECK(...).
	if !p.acceptSymbol(")") {
		p.idx = start
		return nil
	}
	return vals
}

// parseDropDomain picks up after DROP DOMAIN has been detected.
// Grammar: DROP DOMAIN [IF EXISTS] name [, name ...] [CASCADE|RESTRICT]
// M0097-0017.
func (p *parser) parseDropDomain(pos int) (Stmt, error) {
	ifExists, names, behavior, err := p.parseDropTail()
	if err != nil {
		return nil, err
	}
	cascade := behavior == DropCascade
	return &DropDomainStmt{pos: pos, Names: names, IfExists: ifExists, Cascade: cascade}, nil
}

// parseTruncate: TRUNCATE [TABLE] [ONLY] name [, [ONLY] …]
// [RESTART IDENTITY | CONTINUE IDENTITY] [CASCADE|RESTRICT]. M0097-0069.
func (p *parser) parseTruncate() (Stmt, error) {
	t, err := p.expectKeyword(KwTruncate)
	if err != nil {
		return nil, err
	}
	_ = p.acceptKeyword(KwTable) // optional
	// Parse table list with optional ONLY prefix per entry.
	var names []ObjectName
	var onlyFlags []bool
	for {
		only := p.acceptIdentKeyword("only") // ONLY is optional before each name
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		names = append(names, name)
		onlyFlags = append(onlyFlags, only)
		if !p.acceptSymbol(",") {
			break
		}
	}
	stmt := &TruncateStmt{pos: t.Pos, Names: names, Only: onlyFlags}
	// Optional RESTART IDENTITY | CONTINUE IDENTITY clause.
	if p.acceptIdentKeyword("restart") && p.acceptIdentKeyword("identity") {
		stmt.RestartIdentity = true
	} else {
		_ = p.acceptIdentKeyword("continue") && p.acceptIdentKeyword("identity")
	}
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}
