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
	// CREATE SERVER / CREATE FOREIGN ... — accept as no-op. M0097-0071.
	// FOREIGN is a reserved keyword so acceptKeyword is required (not acceptIdentKeyword).
	case p.acceptIdentKeyword("server"):
		return p.parseSkipToSemicolon(t.Pos)
	case p.acceptKeyword(KwForeign):
		return p.parseSkipToSemicolon(t.Pos)
	// CREATE STATISTICS name ON expr, ... FROM table — accept as no-op.
	// Extended statistics are not implemented in goopg v0.
	case p.acceptIdentKeyword("statistics"):
		return p.parseSkipToSemicolon(t.Pos)
	// CREATE RULE name AS ON event TO table [WHERE cond] DO ... — accept as no-op.
	// Rules are not implemented in goopg v0; CREATE RULE succeeds silently so that
	// DROP RULE can track rule existence.
	case p.acceptIdentKeyword("rule"):
		return p.parseCreateRuleTail(t.Pos)
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, FUNCTION, PROCEDURE, or TRIGGER after CREATE")
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
	return stmt, nil
}

// parseCreateRecursiveViewTail handles CREATE [OR REPLACE] RECURSIVE VIEW name(cols) AS query.
// The recursive view is stored as a plain view whose body is a CTE:
//   WITH RECURSIVE name(cols) AS (query) SELECT * FROM name
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
	// Parse the view body.
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
	inner, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	sel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, &SyntaxError{Pos: pos, Message: "materialized view body did not produce SELECT"}
	}
	stmt.Query = sel
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
		// Optional column definition list (for adding columns to partition)
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance()
			if !p.acceptSymbol(")") {
				// skip column defs inside partition OF for now
				depth := 1
				for depth > 0 && p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
						depth++
					}
					if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
						depth--
					}
					if depth > 0 {
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
					return nil, p.errAtCur("expected LIST, RANGE, or HASH after PARTITION BY")
				}
				if !p.acceptSymbol("(") {
					return nil, p.errAtCur("expected '(' after partition method")
				}
				var keyCols, opClasses []string
				for {
					col, err := p.parseIdent()
					if err != nil {
						return nil, err
					}
					keyCols = append(keyCols, identText(col))
					opClass := ""
					if p.cur().Kind == TokenIdent {
						opClass = p.cur().Value
						p.advance()
					}
					opClasses = append(opClasses, opClass)
					if !p.acceptSymbol(",") {
						break
					}
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
				stmt.PartitionBy = &PartitionByClause{pos: pos2, Method: method, KeyCols: keyCols, OpClasses: opClasses}
			}
		}
		// ON COMMIT clause may follow FOR VALUES ... in partition tables.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn {
			p.advance()
			if p.acceptKeyword(KwCommit) {
				_ = p.acceptIdentKeyword("preserve") || p.acceptIdentKeyword("delete")
				_ = p.acceptIdentKeyword("rows") || p.acceptKeyword(KwDrop)
			}
		}
		return stmt, nil
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
			// Optional INCLUDE (col, …) — accept and discard for compat.
			if p.acceptIdentKeyword("include") {
				if p.acceptSymbol("(") {
					for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
						p.advance()
					}
				}
			}
			// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE] — accept and discard.
			if p.acceptKeyword(KwNot) {
				_ = p.acceptKeyword(KwDeferrable)
			} else {
				p.acceptKeyword(KwDeferrable)
			}
			if p.acceptIdentKeyword("initially") {
				_ = p.acceptIdentKeyword("deferred")
				_ = p.acceptIdentKeyword("immediate")
			}
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique {
			// Table-level UNIQUE (cols) — accept as no-op for now.
			p.advance()
			if p.acceptSymbol("(") {
				for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
					p.advance()
				}
			}
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
			// Table-level CHECK (expr) [NOT ENFORCED | ENFORCED]. M0097-0014.
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
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwConstraint {
			// Table-level CONSTRAINT name (PRIMARY KEY | UNIQUE | CHECK | FOREIGN KEY).
			p.advance()           // CONSTRAINT
			_, _ = p.parseIdent() // constraint name (ignore)
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
				// Optional INCLUDE (col, …) — accept and discard for compat.
				if p.acceptIdentKeyword("include") {
					if p.acceptSymbol("(") {
						for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
							p.advance()
						}
					}
				}
				// Optional [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE] — accept and discard.
				if p.acceptKeyword(KwNot) {
					_ = p.acceptKeyword(KwDeferrable)
				} else {
					p.acceptKeyword(KwDeferrable)
				}
				if p.acceptIdentKeyword("initially") {
					_ = p.acceptIdentKeyword("deferred")
					_ = p.acceptIdentKeyword("immediate")
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
				p.advance()
				if p.acceptSymbol("(") {
					for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
						p.advance()
					}
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
				p.advance()
				expr, err := p.parseCheckExpr()
				if err != nil {
					return nil, err
				}
				stmt.TableChecks = append(stmt.TableChecks, expr)
				if p.acceptKeyword(KwNot) {
					_ = p.acceptIdentKeyword("enforced")
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign:
				// Already handled by FK parsing in parseColumnDef / REFERENCES
				// at the column level. Table-level FOREIGN KEY is a no-op here.
				for p.cur().Kind != TokenEOF {
					t := p.cur()
					if t.Kind == TokenSymbol && t.Value == ")" {
						break
					}
					if t.Kind == TokenSymbol && t.Value == "," {
						break
					}
					p.advance()
				}
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
				case p.acceptIdentKeyword("identity"):
					if isIncluding {
						likeKey += ":+identity"
					}
				case p.acceptIdentKeyword("comments"):
				case p.acceptIdentKeyword("statistics"):
				case p.acceptIdentKeyword("storage"):
				case p.acceptIdentKeyword("generated"):
					if isIncluding {
						likeKey += ":+generated"
					}
				case p.acceptKeyword(KwAll):
					if isIncluding {
						likeKey += ":+defaults:+identity:+generated:+constraints"
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
			return nil, p.errAtCur("expected LIST, RANGE, or HASH after PARTITION BY")
		}
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after partition method")
		}
		// Parse column names with optional operator class names. M0097-0015/M0097-0027.
		var keyCols, opClasses []string
		for {
			col, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			keyCols = append(keyCols, identText(col))
			// Optional operator class name (e.g. part_test_int4_ops). M0097-0027.
			opClass := ""
			if p.cur().Kind == TokenIdent {
				opClass = p.cur().Value
				p.advance()
			}
			opClasses = append(opClasses, opClass)
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		stmt.PartitionBy = &PartitionByClause{pos: pos, Method: method, KeyCols: keyCols, OpClasses: opClasses}
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
	// INHERITS (parent [, …]) — table inheritance. Accept and record parent names.
	// Full inheritance semantics land in M0096-0009; for now, the syntax is accepted
	// so that `CREATE TABLE c () INHERITS (p)` does not produce a parse error.
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
			_, _ = p.parseWithOptions()
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTablespace:
			p.advance()
			_, _ = p.parseIdent()
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn:
			// ON COMMIT { PRESERVE ROWS | DELETE ROWS | DROP } — temp table option.
			p.advance()
			if !p.acceptKeyword(KwCommit) {
				return
			}
			_ = p.acceptIdentKeyword("preserve") || p.acceptIdentKeyword("delete")
			_ = p.acceptIdentKeyword("rows") || p.acceptKeyword(KwDrop)
		default:
			return
		}
	}
}

// parsePartitionBoundValues parses a comma-separated list of partition bound
// values, which may include MINVALUE/MAXVALUE keywords.
func (p *parser) parsePartitionBoundValues() ([]Expr, error) {
	var vals []Expr
	for {
		if p.acceptIdentKeyword("minvalue") {
			vals = append(vals, &StringConst{Value: "MINVALUE"})
		} else if p.acceptIdentKeyword("maxvalue") {
			vals = append(vals, &StringConst{Value: "MAXVALUE"})
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

// parseColumnDef parses `name TYPE [NOT NULL | PRIMARY KEY]`.
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
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
			p.advance()
			if _, err := p.expectKeyword(KwKey); err != nil {
				return ColumnDef{}, err
			}
			col.Primary = true
			col.NotNull = true
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNull:
			p.advance() // NULL is the default; absorb it
		// COLLATE collation_name — ignore collation; goopg v0 doesn't track collations. M0097-0071.
		case p.acceptIdentKeyword("collate"):
			_, _ = p.parseIdent() // consume collation name (may be quoted)
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
				// Skip optional sequence options: (START WITH n INCREMENT BY m ...)
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
						p.advance()
					}
				}
				continue
			}
			if !p.acceptSymbol("(") {
				return ColumnDef{}, p.errAtCur("expected '(' after GENERATED ALWAYS AS")
			}
			// Collect the raw expression text.
			depth := 1
			start := p.cur().Pos
			var exprToks []string
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
				exprToks = append(exprToks, t.Value)
				p.advance()
				_ = start
			}
			if !p.acceptSymbol(")") {
				return ColumnDef{}, p.errAtCur("expected ')' to close generated expression")
			}
			// Accept optional STORED keyword (virtual columns not yet supported).
			_ = p.acceptIdentKeyword("stored")
			_ = p.acceptIdentKeyword("virtual")
			col.GeneratedAlways = true
			col.GeneratedExpr = strings.Join(exprToks, " ")
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
			p.advance() // accepted as no-op; index will be created explicitly
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
		// CONSTRAINT name CHECK/PRIMARY KEY/UNIQUE/... column constraint.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwConstraint:
			p.advance()           // CONSTRAINT
			_, _ = p.parseIdent() // constraint name
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
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary:
				// CONSTRAINT name PRIMARY KEY
				p.advance()
				if _, err := p.expectKeyword(KwKey); err != nil {
					return ColumnDef{}, err
				}
				col.Primary = true
				col.NotNull = true
			case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
				// CONSTRAINT name UNIQUE — absorb keyword (no Unique field on ColumnDef)
				p.advance()
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
		if cur := p.cur(); !(cur.Kind == TokenOperator && cur.Value == "=") {
			return nil, p.errAtCur("expected '='")
		}
		p.advance()
		t := p.cur()
		var val string
		switch t.Kind {
		case TokenIntLit, TokenStringLit:
			val = t.Value
		case TokenIdent, TokenQuotedIdent, TokenKeyword:
			val = identText(t)
		default:
			return nil, p.errAtCur("expected option value")
		}
		p.advance()
		out[identText(key)] = val
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
	cols, colExprs, opClassWithOptions, err := p.parseIndexColumnList()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols
	stmt.ColExprs = colExprs
	stmt.OpClassWithOptions = opClassWithOptions
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	// Optional INCLUDE (col, …) — accept and discard for compat.
	if p.acceptIdentKeyword("include") {
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
				}
				p.advance()
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
				}
				p.advance()
			}
		}
	}
	// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+ unique index option) — accept and discard.
	// DISTINCT may be a reserved keyword token (KwDistinct), not just an identifier.
	if p.acceptIdentKeyword("nulls") {
		_ = p.acceptKeyword(KwNot)
		if !p.acceptKeyword(KwDistinct) {
			_ = p.acceptIdentKeyword("distinct")
		}
	}
	// Optional WHERE predicate (partial index) — parse and discard.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhere {
		p.advance()
		if _, err := p.parseExpr(); err != nil {
			return nil, err
		}
	}
	return stmt, nil
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
func (p *parser) parseIndexColumnList() ([]string, []Expr, string, error) {
	var cols []string
	var exprs []Expr
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
				return nil, nil, "", err
			}
			colName = "" // expression — no simple column name
			colExpr = e
		} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			// Parenthesised expression: (expr)
			e, err := p.parseExpr()
			if err != nil {
				return nil, nil, "", err
			}
			colName = ""
			colExpr = e
		} else {
			tok, err := p.parseIdent()
			if err != nil {
				return nil, nil, "", err
			}
			colName = identText(tok)
		}

		// Optional COLLATE "..." or COLLATE ident
		if p.acceptIdentKeyword("collate") {
			// consume the collation name (quoted or plain ident)
			_ = p.advance()
		}

		// Optional opclass name (bare ident that is not a known keyword
		// and not ',' or ')')
		if p.cur().Kind == TokenIdent {
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

		// Optional ASC/DESC
		_ = p.acceptKeyword(KwAsc) || p.acceptKeyword(KwDesc)

		// Optional NULLS FIRST/LAST
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNull {
			p.advance() // NULLS
			p.acceptIdentKeyword("first")
			p.acceptIdentKeyword("last")
		}
		if p.acceptIdentKeyword("nulls") {
			p.acceptIdentKeyword("first")
			p.acceptIdentKeyword("last")
		}

		cols = append(cols, colName)
		exprs = append(exprs, colExpr)
		if !p.acceptSymbol(",") {
			break
		}
		// Stop if we hit the closing paren (empty trailing comma not expected
		// but be safe).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			break
		}
	}
	return cols, exprs, opClassWithOptions, nil
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
		// DROP INDEX CONCURRENTLY — accept the keyword, treat as synchronous.
		// True concurrent drop protocol is out of scope for v0 (M0096-0006).
		_ = p.acceptIdentKeyword("concurrently")
		ifExists, names, behavior, err := p.parseDropTail()
		if err != nil {
			return nil, err
		}
		return &DropIndexStmt{pos: t.Pos, IfExists: ifExists, Names: names, Behavior: behavior}, nil
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
		case p.acceptIdentKeyword("as"):
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
	neg := p.cur().Kind == TokenSymbol && p.cur().Value == "-"
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

	// Events: INSERT | UPDATE | DELETE [OR ...]
	for {
		switch {
		case p.acceptKeyword(KwInsert):
			stmt.Events = append(stmt.Events, "insert")
		case p.acceptKeyword(KwUpdate):
			stmt.Events = append(stmt.Events, "update")
		case p.acceptKeyword(KwDelete):
			stmt.Events = append(stmt.Events, "delete")
		default:
			return nil, p.errAtCur("expected INSERT, UPDATE, or DELETE in trigger events")
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
	// Skip the argument list (trigger functions take no SQL arguments).
	if p.acceptSymbol("(") {
		for !p.acceptSymbol(")") && p.cur().Kind != TokenEOF {
			p.advance()
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
		// Consume options until end of statement (no semicolon, just stop).
		for p.cur().Kind != TokenEOF {
			t := p.cur()
			if t.Kind == TokenSymbol && t.Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
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
		// Check for ALTER COLUMN col SET (options).
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
			p.advance() // consume ALTER
			_ = p.acceptKeyword(KwColumn)
			// Read column name (identifier or unreserved keyword).
			if p.cur().Kind == TokenIdent || (p.cur().Kind == TokenKeyword && IsColNameKeyword(p.cur().Keyword)) {
				p.advance()
			}
			if p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet) {
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
						Kind: AlterTableAlterColumnSet,
					})
					return stmt, nil
				}
			}
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
			(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) {
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
			// SET SCHEMA schema — no-op
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set") {
				p.advance() // SET
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "schema") {
					p.advance() // SCHEMA
					p.advance() // schema name
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

	// ALTER VIEW / SCHEMA / COLLATION / DOMAIN / EXTENSION / LANGUAGE / OPERATOR / PUBLICATION /
	// SUBSCRIPTION / SYSTEM — compatibility stubs. Consume until end of statement.
	for _, objIdent := range []string{
		"schema", "view",
		"collation", "domain", "extension", "language",
		"operator", "publication", "subscription", "system",
		"materialized",
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
	// OWNER TO role — parse as a no-op (return empty AlterTableStmt).
	// "owner" is an identifier in goopg's lexer.
	if p.acceptIdentKeyword("owner") {
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
		// consume role name or CURRENT_USER / SESSION_USER / CURRENT_ROLE
		if !p.acceptIdentKeyword("current_user") &&
			!p.acceptIdentKeyword("session_user") &&
			!p.acceptIdentKeyword("current_role") {
			_, _ = p.parseIdent()
		}
		// Return empty actions list — executor will skip it.
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
	// SET SCHEMA schema_name — parse as a no-op.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
		if p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
			p.advance() // SET
			p.advance() // schema
			_, _ = p.parseIdent()
			return stmt, nil
		}
	}
	// ENABLE/DISABLE TRIGGER — parse as a no-op.
	if p.acceptIdentKeyword("enable") || p.acceptIdentKeyword("disable") {
		// consume rest of statement until ';' or EOF
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// DROP CONSTRAINT name [RESTRICT|CASCADE] — real action (M0097-0036).
	// DROP COLUMN, DROP DEFAULT, etc. — no-op (consume rest).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDrop {
		p.advance() // consume DROP
		if p.acceptKeyword(KwConstraint) {
			nameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			restrict := true // RESTRICT is the PostgreSQL default
			if p.acceptKeyword(KwCascade) {
				restrict = false
			} else {
				_ = p.acceptKeyword(KwRestrict)
			}
			act := AlterTableAction{
				pos:            nameTok.Pos,
				Kind:           AlterTableDropConstraint,
				ConstraintName: identText(nameTok),
				Restrict:       restrict,
			}
			stmt.Actions = append(stmt.Actions, act)
			return stmt, nil
		}
		// DROP COLUMN, DROP DEFAULT, etc. — consume rest as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// ALTER COLUMN — handle SET (options) specially; consume other forms as no-op.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
		p.advance() // consume ALTER
		// Skip COLUMN keyword if present.
		_ = p.acceptKeyword(KwColumn)
		// Read the column name.
		if p.cur().Kind == TokenIdent {
			p.advance()
		}
		// Check for SET (options) pattern.
		if p.acceptIdentKeyword("set") {
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
				// Emit AlterTableAlterColumnSet action.
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					Kind: AlterTableAlterColumnSet,
				})
				return stmt, nil
			}
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
	// DETACH PARTITION child (accept and ignore for v0)
	if p.acceptIdentKeyword("detach") {
		_ = p.acceptKeyword(KwPartition)
		if _, err := p.parseObjectName(); err != nil {
			return AlterTableAction{}, err
		}
		// No-op detach: return an empty ADD action that the executor ignores.
		act := AlterTableAction{pos: p.cur().Pos, Kind: AlterTableAttachPartition}
		return act, nil
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
	if !p.acceptKeyword(KwAdd) {
		return AlterTableAction{}, p.errAtCur("expected ADD")
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
		// Optional [NOT] DEFERRABLE trailer.
		deferrable := false
		if p.acceptKeyword(KwNot) {
			if _, err := p.expectKeyword(KwDeferrable); err != nil {
				return AlterTableAction{}, err
			}
			deferrable = false
		} else if p.acceptKeyword(KwDeferrable) {
			deferrable = true
		}
		act.Kind = AlterTableAddForeignKey
		act.Columns = cols
		act.RefTable = refTable
		act.RefColumns = refCols
		act.Deferrable = deferrable
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
		// ADD [CONSTRAINT name] CHECK (expr) — register the check constraint.
		p.advance() // consume CHECK
		expr, err := p.parseCheckExpr()
		if err != nil {
			return AlterTableAction{}, err
		}
		// Consume optional NOT VALID trailer.
		_ = p.acceptKeyword(KwNot) && p.acceptIdentKeyword("valid")
		act.Kind = AlterTableAddCheck
		act.CheckExpr = expr
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
				// Collect type tokens until ',' or ')'
				var typeParts []string
				for p.cur().Kind != TokenEOF &&
					!(p.cur().Kind == TokenSymbol && (p.cur().Value == "," || p.cur().Value == ")")) {
					typeParts = append(typeParts, p.cur().Value)
					p.advance()
				}
				stmt.CompositeFields = append(stmt.CompositeFields, TypeField{
					Name:    fname,
					ColType: strings.Join(typeParts, " "),
				})
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
	// ADD VALUE [IF NOT EXISTS] 'val' [BEFORE|AFTER 'ref']
	// NOTE: ADD is a reserved keyword (KwAdd), not an ident keyword — use acceptKeyword.
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
		// Other RENAME variants (e.g. RENAME ATTRIBUTE) — consume as stub.
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
	// Skip optional type arguments like (5) or (8,2).
	if p.acceptSymbol("(") {
		depth := 1
		for depth > 0 && p.cur().Kind != TokenEOF {
			t := p.cur()
			if t.Kind == TokenSymbol && t.Value == "(" {
				depth++
			} else if t.Kind == TokenSymbol && t.Value == ")" {
				depth--
				if depth == 0 {
					p.advance()
					break
				}
			}
			p.advance()
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
				// Skip DEFAULT expression (consume until NOT/NULL/CHECK/CONSTRAINT/COLLATE/';')
				p.advance()
				for p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
						break
					}
					if p.cur().Kind == TokenKeyword {
						kw := p.cur().Keyword
						if kw == KwNot || kw == KwNull || kw == KwCheck || kw == KwConstraint {
							break
						}
					}
					if p.cur().Kind == TokenIdent {
						v := strings.ToLower(p.cur().Value)
						if v == "collate" {
							break
						}
					}
					p.advance()
				}
				continue
			case KwConstraint:
				// CONSTRAINT name CHECK (…) — skip constraint name.
				p.advance()
				_, _ = p.parseIdent() // constraint name
				// Fall through to CHECK handling below.
				if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
					p.advance()
					if vals := p.tryParseCheckInValues(); vals != nil {
						if stmt.CheckInValues == nil {
							stmt.CheckInValues = vals
						}
					} else if err := p.skipParenExpr(); err != nil {
						return nil, err
					}
				}
				continue
			case KwCheck:
				p.advance()
				if vals := p.tryParseCheckInValues(); vals != nil {
					if stmt.CheckInValues == nil {
						stmt.CheckInValues = vals
					}
				} else if err := p.skipParenExpr(); err != nil {
					return nil, err
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

// skipParenExpr skips a balanced parenthesised expression.
func (p *parser) skipParenExpr() error {
	if !p.acceptSymbol("(") {
		return p.errAtCur("expected '('")
	}
	depth := 1
	for depth > 0 && p.cur().Kind != TokenEOF {
		t := p.cur()
		if t.Kind == TokenSymbol && t.Value == "(" {
			depth++
		} else if t.Kind == TokenSymbol && t.Value == ")" {
			depth--
			if depth == 0 {
				p.advance()
				break
			}
		}
		p.advance()
	}
	return nil
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
	// Parse string literals.
	var vals []string
	for {
		if p.cur().Kind != TokenStringLit {
			p.idx = start
			return nil
		}
		vals = append(vals, p.cur().Value)
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
	for {
		_ = p.acceptIdentKeyword("only") // ONLY is optional before each name
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		names = append(names, name)
		if !p.acceptSymbol(",") {
			break
		}
	}
	stmt := &TruncateStmt{pos: t.Pos, Names: names}
	// Optional RESTART IDENTITY | CONTINUE IDENTITY clause.
	_ = p.acceptIdentKeyword("restart") && p.acceptIdentKeyword("identity") ||
		p.acceptIdentKeyword("continue") && p.acceptIdentKeyword("identity")
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}
