package parser

import (
	"strconv"
	"strings"
)

// parseTSObjectNameLoose parses a possibly schema-qualified `[schema.]name`
// like parseObjectName, but accepts ANY keyword token (not just
// col_name-class ones) as a bare identifier component. It exists for the
// `PARSER = ...` / `TEMPLATE = ...` clause values of CREATE TEXT SEARCH
// CONFIGURATION/DICTIONARY: real PG's grammar lets these name a reserved
// keyword unquoted (`PARSER = pg_catalog.default` is valid DDL, even though
// "default" is IsColNameKeyword-false and would otherwise fail
// parseObjectName's parseIdent). There is no ambiguity to guard against in
// this position — the value is always immediately followed by `,`/`)`.
// DU-002 slice 446 (M0119-0004).
func (p *parser) parseTSObjectNameLoose() (ObjectName, error) {
	first := p.cur()
	if first.Kind != TokenIdent && first.Kind != TokenQuotedIdent && first.Kind != TokenKeyword {
		return ObjectName{}, p.errAtCur("expected identifier")
	}
	p.advance()
	o := ObjectName{pos: first.Pos, Name: identText(first)}
	if p.acceptSymbol(".") {
		second := p.cur()
		if second.Kind != TokenIdent && second.Kind != TokenQuotedIdent && second.Kind != TokenKeyword {
			return ObjectName{}, p.errAtCur("expected identifier")
		}
		p.advance()
		o.Schema = o.Name
		o.Name = identText(second)
	}
	return o, nil
}

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
	// CREATE EVENT TRIGGER name ON event [WHEN ...] EXECUTE FUNCTION fn() —
	// register in the runtime pg_event_trigger registry so pg_dump round-trips
	// it. DU-002 (M0119-0004). "EVENT" is unreserved and unregistered as a
	// keyword, so it is matched via the ident path like "collation"/"transform".
	case p.acceptIdentKeyword("event"):
		if unlogged || orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED / OR REPLACE not valid for CREATE EVENT TRIGGER"}
		}
		if _, err := p.expectKeyword(KwTrigger); err != nil {
			return nil, err
		}
		return p.parseCreateEventTriggerTail(t.Pos)
	// CREATE ACCESS METHOD name TYPE {INDEX|TABLE} HANDLER handler_name —
	// register in the runtime pg_am registry so pg_dump round-trips it
	// (getAccessMethods/dumpAccessMethod). DU-002 (M0119-0004). "ACCESS" is
	// unreserved and unregistered as a keyword, matched via the ident path
	// like "event"/"collation"; DROP ACCESS METHOD already parses generically
	// (internal/parser/ddl.go's ident-DROP-target list).
	case p.acceptIdentKeyword("access"):
		if unlogged || orReplace {
			return nil, &SyntaxError{Pos: t.Pos, Message: "UNLOGGED / OR REPLACE not valid for CREATE ACCESS METHOD"}
		}
		if !p.acceptIdentKeyword("method") {
			return nil, p.errAtCur("expected METHOD after ACCESS")
		}
		return p.parseCreateAccessMethodTail(t.Pos)
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
		return p.parseCreateTriggerTail(t.Pos, false)
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
	// CREATE COLLATION [IF NOT EXISTS] name (option = value [, ...])
	//   | name FROM existing_collation — register in the runtime pg_collation
	// registry so pg_dump round-trips it. DU-002 (M0119-0004).
	case p.acceptIdentKeyword("collation"):
		return p.parseCreateCollationTail(t.Pos)
	// CREATE CONSTRAINT TRIGGER (CONSTRAINT is a reserved keyword, so match the
	// keyword token — acceptIdentKeyword never matches it). DU-002 slice 327.
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwConstraint:
		p.advance()
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTrigger {
			p.advance()
			return p.parseCreateTriggerTail(t.Pos, true)
		}
		return nil, p.errAtCur("expected TRIGGER after CREATE CONSTRAINT")
	// CREATE CAST (source AS target) { WITHOUT FUNCTION | WITH INOUT |
	//   WITH FUNCTION fn[(args)] } [ AS ASSIGNMENT | AS IMPLICIT ] — register in the
	//   runtime pg_cast registry so pg_dump round-trips it. DU-002 slice 395.
	case p.acceptIdentKeyword("cast"):
		return p.parseCreateCastTail(t.Pos)
	// CREATE [OR REPLACE] TRANSFORM FOR type LANGUAGE lang ( FROM SQL WITH
	//   FUNCTION fn[(args)] [, TO SQL WITH FUNCTION fn[(args)]] | ... ) —
	//   register in the runtime pg_transform registry so pg_dump round-trips
	//   it. DU-002 (M0119-0004).
	case p.acceptIdentKeyword("transform"):
		return p.parseCreateTransformTail(t.Pos)
	// CREATE AGGREGATE name (sfunc=F, basetype=T, stype=S [, ...]) — validate basetype.
	// M0097-regress.
	case p.acceptIdentKeyword("aggregate"):
		return p.parseCreateAggregateTail(t.Pos)
	// CREATE OPERATOR CLASS name FOR TYPE t USING hash AS … — register hash func. M0097-0027.
	case p.acceptIdentKeyword("operator"):
		if p.acceptIdentKeyword("class") {
			return p.parseCreateOpClassTail(t.Pos)
		}
		// CREATE OPERATOR FAMILY name USING method — register in the runtime
		// pg_opfamily registry so it round-trips through pg_dump
		// (getOpfamilies/dumpOpfamily). Unlike CREATE OPERATOR CLASS, the
		// grammar has no AS clause: members are added later via a separate
		// ALTER OPERATOR FAMILY ... ADD statement (opfamilycmds.c
		// CreateOpFamily). DU-002 (M0119-0004).
		if p.acceptIdentKeyword("family") {
			return p.parseCreateOpFamilyTail(t.Pos)
		}
		// CREATE OPERATOR name (leftarg=T, rightarg=T, function=fn, ...) — parse
		// name + arg types (for the DROP OPERATOR compat registry, M0097-regress)
		// plus the FUNCTION/PROCEDURE clause (so the operator round-trips through
		// pg_dump's getOperators/dumpOpr; the two names are synonyms in PG's
		// operatorcmds.c). DU-002 (M0119-0004).
		opName, _ := p.parseOperatorName()
		// Extract leftarg/rightarg/function/commutator/negator/restrict/join/
		// merges/hashes from the parenthesised option list. DU-002 slice 407
		// extends the original FUNCTION/LEFTARG/RIGHTARG-only skeleton.
		var leftArg, rightArg string
		var opFunc, commutatorOp, negatorOp, restrictFunc, joinFunc ObjectName
		var canMerge, canHash bool
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
				// Look for "key = value" pairs (leftarg/rightarg/function/procedure/...)
				// or the bare MERGES/HASHES flags (no "= value" at all).
				if depth == 1 && (tok.Kind == TokenIdent || tok.Kind == TokenKeyword) {
					key := strings.ToLower(tok.Value)
					p.advance()
					if key == "merges" || key == "hashes" {
						val := true
						if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
							p.advance()
							if p.cur().Kind == TokenKeyword && strings.EqualFold(p.cur().Value, "false") {
								val = false
							}
							if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
								p.advance()
							}
						}
						if key == "merges" {
							canMerge = val
						} else {
							canHash = val
						}
						continue
					}
					if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
						p.advance()
						switch key {
						case "function", "procedure":
							// A (possibly schema-qualified) function name — no
							// parenthesised arg-type list in this grammar position
							// (PG infers the signature from LEFTARG/RIGHTARG).
							if fn, ferr := p.parseObjectName(); ferr == nil {
								opFunc = fn
							}
						case "restrict":
							if fn, ferr := p.parseObjectName(); ferr == nil {
								restrictFunc = fn
							}
						case "join":
							if fn, ferr := p.parseObjectName(); ferr == nil {
								joinFunc = fn
							}
						case "commutator":
							if ref, oerr := p.parseOperatorRefName(); oerr == nil {
								commutatorOp = ref
							}
						case "negator":
							if ref, oerr := p.parseOperatorRefName(); oerr == nil {
								negatorOp = ref
							}
						default:
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
			ns.Tag = "CREATE OPERATOR"
			ns.ObjType = "operator"
			ns.ObjName = ObjectName{Name: opName.Name, Schema: opName.Schema}
			ns.ArgTypes = []string{leftArg, rightArg}
			ns.OpFuncName = opFunc
			ns.OpCommutatorName = commutatorOp
			ns.OpNegatorName = negatorOp
			ns.OpRestrictFuncName = restrictFunc
			ns.OpJoinFuncName = joinFunc
			ns.OpCanMerge = canMerge
			ns.OpCanHash = canHash
		}
		return stmt, nil
	// CREATE [DEFAULT] CONVERSION name FOR 'src' TO 'dest' FROM func — register so
	// it round-trips through pg_dump (pg_conversion view → getConversions /
	// dumpConversion). The bare-DEFAULT form is dispatched via the DEFAULT arm
	// below. M0097-0071; full round-trip DU-002 slice 399.
	case p.acceptIdentKeyword("conversion"):
		return p.parseCreateConversionTail(t.Pos, false)
	// CREATE DEFAULT CONVERSION … — DEFAULT only precedes CONVERSION in CREATE, so
	// this arm consumes DEFAULT then requires CONVERSION. DU-002 slice 399.
	case p.acceptKeyword(KwDefault):
		if !p.acceptIdentKeyword("conversion") {
			return nil, p.errAtCur("expected CONVERSION after DEFAULT")
		}
		return p.parseCreateConversionTail(t.Pos, true)
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
		// CREATE TEXT SEARCH DICTIONARY name ( TEMPLATE = tmpl [, key = value, ...] )
		// and CREATE TEXT SEARCH CONFIGURATION name ( PARSER = parser_name ) are
		// the only TS object kinds whose option list is actually parsed (not
		// just skipped) — they round-trip through pg_dump (pg_ts_dict →
		// dumpTSDictionary, pg_ts_config → dumpTSConfig); PARSER/TEMPLATE stay
		// parsed-and-discarded compat no-ops. DU-002 slices 437, 446.
		var tmplName, parserName, copySourceName ObjectName
		var dictOptions []TSDictOption
		if (tsType == "text search dictionary" || tsType == "text search configuration") &&
			p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			for {
				c := p.cur()
				if c.Kind == TokenEOF || (c.Kind == TokenSymbol && c.Value == ")") {
					break
				}
				if c.Kind == TokenSymbol && c.Value == "," {
					p.advance()
					continue
				}
				if c.Kind != TokenIdent && c.Kind != TokenKeyword {
					p.advance()
					continue
				}
				key := strings.ToLower(c.Value)
				p.advance()
				if !((p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=") {
					continue
				}
				p.advance() // consume '='
				if key == "template" {
					if fn, ferr := p.parseTSObjectNameLoose(); ferr == nil {
						tmplName = fn
					}
					continue
				}
				if key == "parser" {
					if fn, ferr := p.parseTSObjectNameLoose(); ferr == nil {
						parserName = fn
					}
					continue
				}
				if key == "copy" && tsType == "text search configuration" {
					if fn, ferr := p.parseTSObjectNameLoose(); ferr == nil {
						copySourceName = fn
					}
					continue
				}
				v := p.cur()
				switch v.Kind {
				case TokenIntLit, TokenNumericLit:
					dictOptions = append(dictOptions, TSDictOption{Key: key, Value: v.Value, IsNumeric: true})
					p.advance()
				case TokenStringLit, TokenIdent, TokenKeyword:
					dictOptions = append(dictOptions, TSDictOption{Key: key, Value: v.Value})
					p.advance()
				}
			}
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				p.advance()
			}
		}
		stmt, err := p.parseSkipToSemicolon(t.Pos)
		if err != nil {
			return nil, err
		}
		if ns, ok := stmt.(*CompatNoopStmt); ok && tsType != "" {
			ns.Tag = "CREATE " + strings.ToUpper(tsType)
			ns.ObjType = tsType
			ns.ObjName = tsName
			if tsType == "text search dictionary" {
				ns.TSDictTemplate = tmplName
				ns.TSDictOptions = dictOptions
			}
			if tsType == "text search configuration" {
				ns.TSConfigParser = parserName
				ns.TSConfigCopySource = copySourceName
			}
		}
		return stmt, nil
	// CREATE SERVER name ... FOREIGN DATA WRAPPER fdwname [OPTIONS (...)] — register as compat object. M0097-0071.
	case p.acceptIdentKeyword("server"):
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Look for FOREIGN DATA WRAPPER fdwname to record the FDW association, and
		// an OPTIONS (...) clause so the server's options round-trip through pg_dump
		// (pg_foreign_server.srvoptions → dumpForeignServer). DU-002 slice 378.
		var fdwName ObjectName
		var options []string
		var serverType, serverVersion string
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
			// Detect "TYPE 'servertype'" / "VERSION 'serverversion'" — each takes a
			// string literal and round-trips through pg_foreign_server.srvtype /
			// srvversion (dumpForeignServer re-emits the TYPE/VERSION clauses).
			// DU-002 slice 381.
			if (tok.Kind == TokenIdent || tok.Kind == TokenKeyword) && strings.EqualFold(tok.Value, "type") {
				p.advance()
				if v := p.cur(); v.Kind == TokenStringLit {
					serverType = v.Value
					p.advance()
				}
				continue
			}
			if (tok.Kind == TokenIdent || tok.Kind == TokenKeyword) && strings.EqualFold(tok.Value, "version") {
				p.advance()
				if v := p.cur(); v.Kind == TokenStringLit {
					serverVersion = v.Value
					p.advance()
				}
				continue
			}
			// Detect "OPTIONS ( name 'value', … )".
			if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "options") {
				options = p.scanFDWOptionsList()
				continue
			}
			p.advance()
		}
		ns := &CompatNoopStmt{pos: t.Pos, Tag: "CREATE SERVER", ObjType: "server", ObjName: name}
		if fdwName.Name != "" {
			ns.TableName = fdwName // reuse TableName field to store FDW association
		}
		ns.Options = options
		ns.ServerType = serverType
		ns.ServerVersion = serverVersion
		return ns, nil
	// CREATE USER MAPPING FOR <user> SERVER <server> [OPTIONS (...)] — register so
	// it round-trips through pg_dump (pg_user_mappings view → dumpUserMappings).
	// Only the MAPPING form is parsed here; plain CREATE USER/ROLE is handled at
	// the server layer (role DDL), so when "mapping" does not follow we return a
	// parse error and the statement falls through to that path. DU-002 slice 377.
	case p.acceptIdentKeyword("user"):
		if !p.acceptIdentKeyword("mapping") {
			return nil, p.errAtCur("expected MAPPING after CREATE USER")
		}
		userName, srvName, umOptions := p.scanUserMappingForServer()
		ns := &CompatNoopStmt{pos: t.Pos, Tag: "CREATE USER MAPPING", ObjType: "user mapping", ObjName: ObjectName{Name: userName}}
		ns.TableName = ObjectName{Name: srvName} // reuse TableName for the server association
		ns.Options = umOptions
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
			// CREATE FOREIGN DATA WRAPPER name [HANDLER f | NO HANDLER]
			// [VALIDATOR f | NO VALIDATOR] [OPTIONS (...)] — register as compat
			// object. The OPTIONS clause (always last) round-trips through pg_dump
			// (pg_foreign_data_wrapper.fdwoptions → dumpForeignDataWrapper). The
			// HANDLER/VALIDATOR func names are captured (FDWHandlerFunc/
			// FDWValidatorFunc) so the executor can resolve them to real pg_proc
			// OIDs (DU-002 M0119-0004, closing the "func references are skipped"
			// gap slice 380 left open).
			name, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			var options []string
			var handlerFunc, validatorFunc *ObjectName
			for {
				tok := p.cur()
				if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
					break
				}
				// Detect "OPTIONS ( name 'value', … )".
				if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "options") {
					options = p.scanFDWOptionsList()
					continue
				}
				if kind, fn, consumed, err := p.scanFDWFuncClause(); consumed {
					if err != nil {
						return nil, err
					}
					if kind == "handler" {
						handlerFunc = fn
					} else {
						validatorFunc = fn
					}
					continue
				}
				p.advance()
			}
			ns := &CompatNoopStmt{pos: t.Pos, Tag: "CREATE FOREIGN DATA WRAPPER", ObjType: "foreign-data wrapper", ObjName: name}
			ns.Options = options
			ns.FDWHandlerFunc = handlerFunc
			ns.FDWValidatorFunc = validatorFunc
			return ns, nil
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
	// CREATE POLICY name ON table [AS {PERMISSIVE|RESTRICTIVE}] [FOR cmd]
	// [TO role[, ...]] [USING (expr)] [WITH CHECK (expr)] — records a
	// row-security policy so it round-trips through pg_dump (pg_policy →
	// dumpPolicy). goopg does NOT enforce RLS. DU-002 slice 323.
	case p.acceptIdentKeyword("policy"):
		return p.parseCreatePolicyTail(t.Pos)
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

// scanUserMappingForServer loosely scans the tail of a
//
//	[CREATE|DROP] USER MAPPING [IF ...] FOR <user> SERVER <server> [OPTIONS (...)]
//
// statement, returning the mapped user name (the token after FOR), the server
// name (the token after SERVER), and the OPTIONS list as "name=value" elements,
// then skipping to the statement terminator. The user may be a role name,
// PUBLIC, or CURRENT_USER/CURRENT_ROLE/SESSION_USER/USER; goopg keeps only enough
// to round-trip the mapping (including its OPTIONS) through pg_dump, so the
// precise user-spec kind is intentionally not modelled. DU-002 slice 377 (OPTIONS:
// slice 379).
func (p *parser) scanUserMappingForServer() (user, server string, options []string) {
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
			break
		}
		if tok.Kind == TokenKeyword && tok.Keyword == KwFor {
			p.advance()
			if ut := p.cur(); ut.Kind == TokenIdent || ut.Kind == TokenKeyword {
				user = ut.Value
				p.advance()
			}
			continue
		}
		if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "server") {
			p.advance()
			if st := p.cur(); st.Kind == TokenIdent || st.Kind == TokenKeyword {
				server = st.Value
				p.advance()
			}
			continue
		}
		// OPTIONS ( name 'value', … ) → umoptions text[] elements so the mapping's
		// options round-trip through pg_dump (pg_user_mappings.umoptions →
		// dumpUserMappings). Reuses the shared CREATE-form OPTIONS scanner.
		if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "options") {
			options = p.scanFDWOptionsList()
			continue
		}
		p.advance()
	}
	return user, server, options
}

// scanFDWFuncClause recognises one `HANDLER handler_name | NO HANDLER` or
// `VALIDATOR handler_name | NO VALIDATOR` clause of a CREATE/ALTER FOREIGN
// DATA WRAPPER statement (gram.y's fdw_option), assuming the cursor is
// positioned on the leading token. handler_name is a plain (possibly
// schema-qualified) function name with no parenthesized arg list — PostgreSQL
// resolves it via a fixed signature (LookupFuncName), mirrored by
// resolveFDWHandlerFunc/resolveFDWValidatorFunc in the executor. Returns
// consumed=false (and advances nothing) when the cursor is on neither form,
// so the caller's generic skip-loop handles it. kind is "handler" or
// "validator"; fn is nil for the `NO ...` form. DU-002 (M0119-0004).
func (p *parser) scanFDWFuncClause() (kind string, fn *ObjectName, consumed bool, err error) {
	tok := p.cur()
	isHandler := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "handler")
	isValidator := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "validator")
	if isHandler || isValidator {
		p.advance()
		name, perr := p.parseObjectName()
		if perr != nil {
			return "", nil, true, perr
		}
		if isHandler {
			return "handler", &name, true, nil
		}
		return "validator", &name, true, nil
	}
	if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "no") {
		next := p.peek(1)
		nextIsHandler := next.Kind == TokenIdent && strings.EqualFold(next.Value, "handler")
		nextIsValidator := next.Kind == TokenIdent && strings.EqualFold(next.Value, "validator")
		if nextIsHandler || nextIsValidator {
			p.advance() // NO
			p.advance() // HANDLER|VALIDATOR
			if nextIsHandler {
				return "handler", nil, true, nil
			}
			return "validator", nil, true, nil
		}
	}
	return "", nil, false, nil
}

// scanFDWOptionsList consumes an `OPTIONS ( name 'value' [, …] )` clause,
// assuming the cursor is positioned ON the OPTIONS keyword. Each option is
// returned as a "name=value" string — the on-disk srvoptions/fdwoptions text[]
// element form that pg_dump's pg_options_to_table SRF expands. The CREATE form
// has no ADD/SET/DROP action verbs (those belong to ALTER), so every entry is a
// bare `name 'value'` pair. A malformed clause (no opening paren / missing
// value) is tolerated: the helper returns what it has parsed and leaves the
// cursor for the caller's outer skip loop. DU-002 slice 378.
func (p *parser) scanFDWOptionsList() []string {
	p.advance() // consume OPTIONS
	if c := p.cur(); c.Kind != TokenSymbol || c.Value != "(" {
		return nil
	}
	p.advance() // consume '('
	var opts []string
	for {
		c := p.cur()
		if c.Kind == TokenEOF || (c.Kind == TokenSymbol && c.Value == ")") {
			break
		}
		if c.Kind == TokenSymbol && c.Value == "," {
			p.advance()
			continue
		}
		// Option name: an identifier or (rarely) a keyword used as a name.
		name := c.Value
		p.advance()
		// Option value: a string literal.
		if v := p.cur(); v.Kind == TokenStringLit {
			opts = append(opts, name+"="+v.Value)
			p.advance()
		}
	}
	if c := p.cur(); c.Kind == TokenSymbol && c.Value == ")" {
		p.advance()
	}
	return opts
}

// scanAlterFDWOptionsList parses the verb-tagged option list of
//
//	OPTIONS ( [ADD|SET|DROP] name ['value'], … )
//
// used by `ALTER FOREIGN TABLE ... ALTER COLUMN col OPTIONS (...)` (PG's
// alter_generic_option_elem, gram.y). Unlike scanFDWOptionsList (the flat
// CREATE-time form), each entry here carries an explicit verb; a bare
// `name 'value'` (no ADD/SET/DROP prefix) defaults to Add, matching PG's
// DEFELEM_UNSPEC-treated-as-ADD rule. DROP takes no value. Assumes the
// cursor sits on the OPTIONS token. DU-002 slice 419.
func (p *parser) scanAlterFDWOptionsList() []FDWOptionChange {
	p.advance() // consume OPTIONS
	if c := p.cur(); c.Kind != TokenSymbol || c.Value != "(" {
		return nil
	}
	p.advance() // consume '('
	var changes []FDWOptionChange
	for {
		c := p.cur()
		if c.Kind == TokenEOF || (c.Kind == TokenSymbol && c.Value == ")") {
			break
		}
		if c.Kind == TokenSymbol && c.Value == "," {
			p.advance()
			continue
		}
		verb := FDWOptionAdd
		switch {
		case p.acceptKeyword(KwAdd):
			verb = FDWOptionAdd
		case p.acceptKeyword(KwSet):
			verb = FDWOptionSet
		case p.acceptKeyword(KwDrop):
			verb = FDWOptionDrop
		}
		name := p.cur().Value
		p.advance()
		value := ""
		if verb != FDWOptionDrop {
			if v := p.cur(); v.Kind == TokenStringLit {
				value = v.Value
				p.advance()
			}
		}
		changes = append(changes, FDWOptionChange{Verb: verb, Name: name, Value: value})
	}
	if c := p.cur(); c.Kind == TokenSymbol && c.Value == ")" {
		p.advance()
	}
	return changes
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

// parseCreateCollationTail parses the tail of
//
//	CREATE COLLATION [IF NOT EXISTS] name ( option = value [, ...] )
//	CREATE COLLATION [IF NOT EXISTS] name FROM existing_collation
//
// after the COLLATION keyword. Mirrors gram.y's DefineStmt for OBJECT_COLLATION.
// Recognised options: LOCALE, LC_COLLATE, LC_CTYPE, PROVIDER, DETERMINISTIC,
// RULES (VERSION and unknown options are accepted and ignored for forward
// compatibility). DU-002 (M0119-0004).
func (p *parser) parseCreateCollationTail(pos int) (Stmt, error) {
	stmt := &CreateCollationStmt{pos: pos}
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
	// FROM existing_collation form.
	if p.acceptKeyword(KwFrom) {
		from, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt.FromName = from
		return stmt, nil
	}
	// Parenthesised option list.
	if c := p.cur(); !(c.Kind == TokenSymbol && c.Value == "(") {
		return nil, p.errAtCur("expected ( or FROM in CREATE COLLATION")
	}
	p.advance() // consume '('
	for {
		if c := p.cur(); c.Kind == TokenSymbol && c.Value == ")" {
			p.advance()
			break
		}
		kt := p.cur()
		if kt.Kind != TokenIdent && kt.Kind != TokenKeyword {
			return nil, p.errAtCur("expected option name in CREATE COLLATION")
		}
		key := strings.ToLower(kt.Value)
		p.advance()
		if c := p.cur(); (c.Kind == TokenOperator || c.Kind == TokenSymbol) && c.Value == "=" {
			p.advance()
		} else {
			return nil, p.errAtCur("expected = after option name in CREATE COLLATION")
		}
		vt := p.cur()
		if vt.Kind != TokenIdent && vt.Kind != TokenKeyword && vt.Kind != TokenStringLit && vt.Kind != TokenQuotedIdent {
			return nil, p.errAtCur("expected value after = in CREATE COLLATION")
		}
		val := vt.Value
		p.advance()
		switch key {
		case "locale":
			stmt.Locale = val
		case "lc_collate":
			stmt.LcCollate = val
		case "lc_ctype":
			stmt.LcCtype = val
		case "provider":
			stmt.Provider = strings.ToLower(val)
		case "deterministic":
			stmt.Deterministic = strings.ToLower(val)
		case "rules":
			// ICU tailoring rules (provider = icu only). Stored verbatim and
			// re-emitted by pg_dump's dumpCollation as `rules = '...'`; goopg
			// does not interpret the rules for actual collation.
			stmt.Rules = val
		default:
			// VERSION and any unknown option: accept and ignore.
		}
		if c := p.cur(); c.Kind == TokenSymbol && c.Value == "," {
			p.advance()
		}
	}
	return stmt, nil
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

// parseCastTypeName reads a (possibly schema-qualified, possibly multi-word)
// type name in a CREATE/DROP CAST `(source AS target)` clause, stopping at the
// AS keyword, a comma, or a closing/opening paren. CREATE CAST type names carry
// no typmod, so the scan never has to balance parens. DU-002 slice 395.
func (p *parser) parseCastTypeName() string {
	return p.parseSimpleTypeName(KwAs)
}

// parseSimpleTypeName reads a (possibly schema-qualified, possibly multi-word)
// type name, stopping at any of the given stop keywords, a comma, or a
// closing/opening paren. Shared by CREATE/DROP CAST (stops at AS) and
// CREATE/DROP TRANSFORM (stops at LANGUAGE) — neither carries a typmod, so
// the scan never has to balance parens. DU-002 (slice 395; TRANSFORM
// M0119-0004).
func (p *parser) parseSimpleTypeName(stopKeywords ...Keyword) string {
	var parts []string
	for {
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			break
		}
		// A stop keyword separates the type name from what follows (e.g. AS
		// introduces the cast target / ASSIGNMENT|IMPLICIT, LANGUAGE introduces
		// a CREATE TRANSFORM's language name); never part of a type name.
		stop := false
		for _, kw := range stopKeywords {
			if tok.Kind == TokenKeyword && tok.Keyword == kw {
				stop = true
				break
			}
		}
		if stop {
			break
		}
		word := tok.Value
		p.advance()
		// schema-qualified: <schema>.<type>
		if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
			p.advance()
			if nt := p.cur(); nt.Kind == TokenIdent || nt.Kind == TokenKeyword {
				word = word + "." + nt.Value
				p.advance()
			}
		}
		parts = append(parts, word)
		if p.cur().Kind == TokenSymbol && (p.cur().Value == ")" || p.cur().Value == "(" || p.cur().Value == ",") {
			break
		}
	}
	return strings.Join(parts, " ")
}

// parseCreateCastTail picks up after "CREATE CAST". It parses the
//
//	(source AS target) { WITHOUT FUNCTION | WITH INOUT | WITH FUNCTION fn[(args)] }
//	  [ AS ASSIGNMENT | AS IMPLICIT ]
//
// form, recording the source/target types, castmethod and castcontext on a
// CompatNoopStmt so the executor registers the cast and pg_dump round-trips it
// (pg_cast virtual view → getCasts/dumpCast). The WITH FUNCTION form also
// captures the referenced function name + arg types (CastFuncName/CastFuncArgs)
// so the executor can resolve pg_cast.castfunc. DU-002 slices 395, 397.
func (p *parser) parseCreateCastTail(pos int) (Stmt, error) {
	ns := &CompatNoopStmt{pos: pos, Tag: "CREATE CAST", ObjType: "cast"}
	if !p.acceptSymbol("(") {
		return nil, p.errSyntaxAtCur()
	}
	source := p.parseCastTypeName()
	_ = p.acceptKeyword(KwAs)
	target := p.parseCastTypeName()
	_ = p.acceptSymbol(")")
	ns.ArgTypes = []string{source, target}

	// Coercion method. Default to binary so a malformed tail still produces a
	// sane WITHOUT FUNCTION cast rather than nothing.
	method := "b"
	switch {
	case p.acceptIdentKeyword("without"):
		_ = p.acceptKeyword(KwFunction)
		method = "b"
	case p.acceptKeyword(KwWith):
		if p.acceptKeyword(KwInout) {
			method = "i"
		} else {
			_ = p.acceptKeyword(KwFunction)
			method = "f"
			// WITH FUNCTION funcname [ (argtype [, ...]) ]. Capture the function
			// name and its explicit argument-type list so the executor can
			// resolve the routine's pg_proc OID for pg_cast.castfunc. PG's cast
			// grammar permits only bare argument types here (no arg names), so a
			// comma-separated parseCastTypeName loop suffices. DU-002 slice 397.
			if fn, err := p.parseObjectName(); err == nil {
				ns.CastFuncName = fn
			}
			if p.acceptSymbol("(") {
				for !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") && p.cur().Kind != TokenEOF {
					at := p.parseCastTypeName()
					if at != "" {
						ns.CastFuncArgs = append(ns.CastFuncArgs, at)
					}
					if !p.acceptSymbol(",") {
						break
					}
				}
				_ = p.acceptSymbol(")")
			}
		}
	}
	ns.CastMethod = method

	// Coercion context: `AS ASSIGNMENT` → 'a', `AS IMPLICIT` → 'i', absent → 'e'
	// (explicit). The remaining tail (a WITH FUNCTION signature) is discarded by
	// the skip-to-semicolon below.
	context := "e"
	if p.acceptKeyword(KwAs) {
		switch {
		case p.acceptIdentKeyword("assignment"):
			context = "a"
		case p.acceptIdentKeyword("implicit"):
			context = "i"
		}
	}
	ns.CastContext = context

	return parseSkipToSemicolonHelper(p, ns)
}

// parseCreateTransformTail picks up after "CREATE [OR REPLACE] TRANSFORM". It
// parses
//
//	FOR type_name LANGUAGE lang_name (
//	    { FROM SQL WITH FUNCTION fn[(argtypes)]
//	        [ , TO SQL WITH FUNCTION fn[(argtypes)] ]
//	    | TO SQL WITH FUNCTION fn[(argtypes)]
//	        [ , FROM SQL WITH FUNCTION fn[(argtypes)] ]
//	    }
//	)
//
// (PostgreSQL's transform_element_list: either half alone, or both in either
// order) and records the type/language/from-function/to-function on a
// CompatNoopStmt so the executor registers the transform and pg_dump round-
// trips it (pg_transform virtual view → getTransforms/dumpTransform). "SQL" is
// an unreserved ident (no dedicated keyword token), matched via
// acceptIdentKeyword. DU-002 (M0119-0004).
func (p *parser) parseCreateTransformTail(pos int) (Stmt, error) {
	ns := &CompatNoopStmt{pos: pos, Tag: "CREATE TRANSFORM", ObjType: "transform"}
	if !p.acceptKeyword(KwFor) {
		return nil, p.errAtCur("expected FOR in CREATE TRANSFORM")
	}
	ns.TransformType = p.parseSimpleTypeName(KwLanguage)
	if !p.acceptKeyword(KwLanguage) {
		return nil, p.errAtCur("expected LANGUAGE in CREATE TRANSFORM")
	}
	lang, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	ns.TransformLang = lang.Value
	if !p.acceptSymbol("(") {
		return nil, p.errSyntaxAtCur()
	}
	// At most 2 elements (FROM SQL and/or TO SQL, either order); each iteration
	// requires a leading FROM/TO or the element list is done.
elements:
	for elem := 0; elem < 2; elem++ {
		var isFrom bool
		switch {
		case p.acceptKeyword(KwFrom):
			isFrom = true
		case p.acceptKeyword(KwTo):
			isFrom = false
		default:
			break elements
		}
		if !p.acceptIdentKeyword("sql") {
			return nil, p.errAtCur("expected SQL after FROM/TO in CREATE TRANSFORM")
		}
		if !p.acceptKeyword(KwWith) {
			return nil, p.errAtCur("expected WITH in CREATE TRANSFORM")
		}
		if !p.acceptKeyword(KwFunction) {
			return nil, p.errAtCur("expected FUNCTION in CREATE TRANSFORM")
		}
		fn, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		var args []string
		if p.acceptSymbol("(") {
			for !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") && p.cur().Kind != TokenEOF {
				if at := p.parseCastTypeName(); at != "" {
					args = append(args, at)
				}
				if !p.acceptSymbol(",") {
					break
				}
			}
			_ = p.acceptSymbol(")")
		}
		if isFrom {
			ns.TransformFromFunc = fn
			ns.TransformFromArgs = args
		} else {
			ns.TransformToFunc = fn
			ns.TransformToArgs = args
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errSyntaxAtCur()
	}
	return parseSkipToSemicolonHelper(p, ns)
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
			case "finalfunc_modify":
				stmt.FinalFuncModify = strings.ToLower(valStr)
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

// parseCreateOpFamilyTail picks up after "CREATE OPERATOR FAMILY". Grammar:
//
//	name USING index_method
//
// the entire statement — PG has no AS clause here (unlike CREATE OPERATOR
// CLASS); members are added later via ALTER OPERATOR FAMILY ... ADD.
// DU-002 (M0119-0004).
func (p *parser) parseCreateOpFamilyTail(pos int) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	var method string
	if p.acceptKeyword(KwUsing) {
		methodTok := p.cur()
		if methodTok.Kind == TokenIdent || methodTok.Kind == TokenKeyword {
			method = strings.ToLower(methodTok.Value)
			p.advance()
		}
	}
	stmt, err := p.parseSkipToSemicolon(pos)
	if err != nil {
		return nil, err
	}
	if ns, ok := stmt.(*CompatNoopStmt); ok {
		ns.Tag = "CREATE OPERATOR FAMILY"
		ns.ObjType = "operator family"
		ns.ObjName = name
		ns.OpFamilyMethod = method
	}
	return stmt, nil
}

// parseCreateOpClassTail picks up after "CREATE OPERATOR CLASS". Captures
// the full class-level shape (schema-qualified name, DEFAULT, FOR TYPE,
// USING method, optional FAMILY clause, STORAGE entry) so CREATE OPERATOR
// CLASS can populate a real pg_opclass row (DU-002, M0119-0004); the FUNCTION
// 2 (hash extended support func) capture from M0097-0027 is preserved.
// OPERATOR/FUNCTION entries otherwise remain accepted-and-ignored.
func (p *parser) parseCreateOpClassTail(pos int) (Stmt, error) {
	stmt := &CreateOpClassStmt{pos: pos}
	// Name (possibly schema-qualified).
	name, err := p.parseObjectName()
	if err != nil {
		return nil, p.errAtCur("expected operator class name")
	}
	stmt.Schema = name.Schema
	stmt.Name = name.Name
	// Optional DEFAULT (reserved keyword).
	stmt.IsDefault = p.acceptKeyword(KwDefault)
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
	// USING method (USING is a reserved keyword).
	if !p.acceptKeyword(KwUsing) {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	if methodTok := p.cur(); methodTok.Kind == TokenIdent || methodTok.Kind == TokenKeyword {
		stmt.Method = strings.ToLower(methodTok.Value)
		p.advance()
	}
	// Optional FAMILY family_name ("family" is not in goopg's keyword map —
	// arrives as TokenIdent, mirroring CREATE OPERATOR CLASS's other bare
	// contextual keywords "type"/"operator"/"storage").
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "family") {
		p.advance()
		if famName, ferr := p.parseObjectName(); ferr == nil {
			stmt.FamilySchema = famName.Schema
			stmt.FamilyName = famName.Name
		}
	}
	// AS list of entries (AS is a reserved keyword).
	if !p.acceptKeyword(KwAs) {
		return parseSkipToSemicolonHelper(p, stmt)
	}
	// Scan entries: (STORAGE type | OPERATOR n op | FUNCTION n name(args))[, ...]
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		// "operator"/"storage" are not in goopg's keyword map → TokenIdent.
		isOperator := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "operator")
		// "function" IS in the keyword map as KwCatUnreserved → TokenKeyword.
		isFunction := tok.Kind == TokenKeyword && tok.Keyword == KwFunction
		isStorage := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "storage")
		if isStorage {
			p.advance() // consume "storage"
			storageType, _ := p.parseTypeNameAfterCast()
			stmt.StorageType = strings.ToLower(storageType.String())
		} else if isOperator {
			p.advance() // consume "operator"
			strategyNum := 0
			if p.cur().Kind == TokenIntLit {
				strategyNum, _ = strconv.Atoi(p.cur().Value)
				p.advance()
			}
			// Operator name (may be a qualified OPERATOR(schema.op) form, a
			// bare/schema-qualified symbol, or a named operator).
			opRef, operr := p.parseOperatorRefName()
			if operr != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			member := OpClassMember{Number: strategyNum, Schema: opRef.Schema, Name: opRef.Name}
			// Grammar: OPERATOR num any_operator opclass_purpose (no operand
			// types — resolved from the operator itself at exec time) OR
			// OPERATOR num operator_with_argtypes opclass_purpose, where
			// operator_with_argtypes appends an explicit (lefttype, righttype).
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				lt, _ := p.parseTypeNameAfterCast()
				member.LeftType = strings.ToLower(lt.String())
				if p.acceptSymbol(",") {
					rt, _ := p.parseTypeNameAfterCast()
					member.RightType = strings.ToLower(rt.String())
				}
				p.acceptSymbol(")")
			}
			// opclass_purpose: FOR SEARCH | FOR ORDER BY any_name | empty.
			// "search" after FOR is not in goopg's keyword map (ORDER/BY ARE
			// reserved keywords elsewhere). FOR ORDER BY's family name is
			// captured on the member — resolved against the btree access
			// method at exec time (get_opfamily_oid(BTREE_AM_OID, ...),
			// opclasscmds.c: the sort family lookup ALWAYS uses btree,
			// regardless of the containing opclass's own access method).
			if p.acceptKeyword(KwFor) {
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "search") {
					p.advance()
				} else {
					p.acceptKeyword(KwOrder)
					p.acceptKeyword(KwBy)
					if famName, ferr := p.parseObjectName(); ferr == nil {
						member.SortFamilySchema = famName.Schema
						member.SortFamilyName = famName.Name
					}
				}
			}
			stmt.Members = append(stmt.Members, member)
		} else if isFunction {
			p.advance() // consume "function"
			supportNum := 0
			if p.cur().Kind == TokenIntLit {
				supportNum, _ = strconv.Atoi(p.cur().Value)
				p.advance()
			}
			member := OpClassMember{IsFunction: true, Number: supportNum}
			// Grammar: FUNCTION num '(' type_list ')' function_with_argtypes
			// puts an explicit (lefttype, righttype) BEFORE the function
			// name (distinct from the function's OWN argument-type list,
			// which follows the name); FUNCTION num function_with_argtypes
			// (no leading parens) leaves both unspecified — resolved from
			// the function's own signature at exec time.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				lt, _ := p.parseTypeNameAfterCast()
				member.LeftType = strings.ToLower(lt.String())
				if p.acceptSymbol(",") {
					rt, _ := p.parseTypeNameAfterCast()
					member.RightType = strings.ToLower(rt.String())
				}
				p.acceptSymbol(")")
			}
			funcName, err := p.parseObjectName()
			if err != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			member.Schema = funcName.Schema
			member.Name = funcName.Name
			// Skip the function's own argument-type list.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.skipBalancedParens()
			}
			// FUNCTION 2 is the hash extended support function.
			if supportNum == 2 {
				stmt.HashFuncName = strings.ToLower(funcName.String())
			}
			stmt.Members = append(stmt.Members, member)
		} else {
			break
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return stmt, nil
}

// parseAlterOpFamilyTail picks up after "ALTER OPERATOR FAMILY". Grammar
// (opclasscmds.c AlterOpFamilyStmt):
//
//	name USING method (ADD opclass_item_list | DROP opclass_drop_list)
//
// The ADD form is modeled as AlterOpFamilyAddStmt, reusing the same
// OPERATOR/FUNCTION entry grammar as CREATE OPERATOR CLASS's own AS list
// (opclass_item is shared upstream) except that an OPERATOR entry here
// REQUIRES an explicit (lefttype, righttype) pair — captured via
// OpClassMember.HasExplicitArgTypes and checked at exec time
// (execAlterOpFamilyAdd), matching PG's own phase for that error. The DROP
// form is modeled as AlterOpFamilyDropStmt: its opclass_drop production
// (gram.y) is narrower than opclass_item — a mandatory Iconst strategy/
// support number and a mandatory parenthesized type list, no operator/
// function name. Any other tail (RENAME TO, OWNER TO, SET SCHEMA, or any
// unrecognized form) keeps the pre-existing *AlterTableStmt no-op stub.
// DU-002 (M0119-0004).
func (p *parser) parseAlterOpFamilyTail(pos int) (Stmt, error) {
	noop := func() (Stmt, error) {
		return parseSkipToSemicolonHelper(p, &CompatNoopStmt{pos: pos, Tag: "ALTER OPERATOR FAMILY"})
	}
	name, err := p.parseObjectName()
	if err != nil {
		return noop()
	}
	if !p.acceptKeyword(KwUsing) {
		return noop()
	}
	method := ""
	if methodTok := p.cur(); methodTok.Kind == TokenIdent || methodTok.Kind == TokenKeyword {
		method = strings.ToLower(methodTok.Value)
		p.advance()
	}
	if p.acceptKeyword(KwDrop) {
		return p.parseAlterOpFamilyDropTail(pos, name, method)
	}
	if !p.acceptKeyword(KwAdd) {
		// RENAME TO / OWNER TO / SET SCHEMA / any unrecognized tail — noop.
		return noop()
	}
	stmt := &AlterOpFamilyAddStmt{pos: pos, Schema: name.Schema, Name: name.Name, Method: method}
	for {
		tok := p.cur()
		if tok.Kind == TokenEOF {
			break
		}
		// "operator" is not in goopg's keyword map → TokenIdent; "function" IS
		// (KwCatUnreserved) → TokenKeyword. Mirrors parseCreateOpClassTail's
		// own entry-scan loop.
		isOperator := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "operator")
		isFunction := tok.Kind == TokenKeyword && tok.Keyword == KwFunction
		if isOperator {
			p.advance() // consume "operator"
			strategyNum := 0
			if p.cur().Kind == TokenIntLit {
				strategyNum, _ = strconv.Atoi(p.cur().Value)
				p.advance()
			}
			opRef, operr := p.parseOperatorRefName()
			if operr != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			member := OpClassMember{Number: strategyNum, Schema: opRef.Schema, Name: opRef.Name}
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				lt, _ := p.parseTypeNameAfterCast()
				member.LeftType = strings.ToLower(lt.String())
				if p.acceptSymbol(",") {
					rt, _ := p.parseTypeNameAfterCast()
					member.RightType = strings.ToLower(rt.String())
				}
				p.acceptSymbol(")")
				member.HasExplicitArgTypes = true
			}
			// opclass_purpose: FOR SEARCH | FOR ORDER BY any_name | empty.
			if p.acceptKeyword(KwFor) {
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "search") {
					p.advance()
				} else {
					p.acceptKeyword(KwOrder)
					p.acceptKeyword(KwBy)
					if famName, ferr := p.parseObjectName(); ferr == nil {
						member.SortFamilySchema = famName.Schema
						member.SortFamilyName = famName.Name
					}
				}
			}
			stmt.Members = append(stmt.Members, member)
		} else if isFunction {
			p.advance() // consume "function"
			supportNum := 0
			if p.cur().Kind == TokenIntLit {
				supportNum, _ = strconv.Atoi(p.cur().Value)
				p.advance()
			}
			member := OpClassMember{IsFunction: true, Number: supportNum}
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				lt, _ := p.parseTypeNameAfterCast()
				member.LeftType = strings.ToLower(lt.String())
				if p.acceptSymbol(",") {
					rt, _ := p.parseTypeNameAfterCast()
					member.RightType = strings.ToLower(rt.String())
				}
				p.acceptSymbol(")")
			}
			funcName, ferr := p.parseObjectName()
			if ferr != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			member.Schema = funcName.Schema
			member.Name = funcName.Name
			// Skip the function's own argument-type list.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.skipBalancedParens()
			}
			stmt.Members = append(stmt.Members, member)
		} else {
			// STORAGE (rejected by AlterOpFamilyAdd itself) or any unexpected
			// token — tolerantly stop scanning members rather than hard-failing.
			return parseSkipToSemicolonHelper(p, stmt)
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return stmt, nil
}

// parseAlterOpFamilyDropTail parses the DROP form's opclass_drop_list
// (gram.y opclass_drop): a comma-separated list of
//
//	OPERATOR strategy_number '(' type [',' type] ')'
//	FUNCTION support_number '(' type [',' type] ')'
//
// Unlike opclass_item (the ADD form), the number is mandatory (no default)
// and there is no operator/function name — the member is identified purely
// by (family, strategy-or-procnum, lefttype, righttype), resolved at exec
// time (execAlterOpFamilyDrop). DU-002 (M0119-0004).
func (p *parser) parseAlterOpFamilyDropTail(pos int, name ObjectName, method string) (Stmt, error) {
	stmt := &AlterOpFamilyDropStmt{pos: pos, Schema: name.Schema, Name: name.Name, Method: method}
	for {
		tok := p.cur()
		isOperator := tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "operator")
		isFunction := tok.Kind == TokenKeyword && tok.Keyword == KwFunction
		if !isOperator && !isFunction {
			return parseSkipToSemicolonHelper(p, stmt)
		}
		p.advance() // consume "operator" / "function"
		member := OpClassMember{IsFunction: isFunction}
		if p.cur().Kind == TokenIntLit {
			member.Number, _ = strconv.Atoi(p.cur().Value)
			p.advance()
		}
		if !p.acceptSymbol("(") {
			return parseSkipToSemicolonHelper(p, stmt)
		}
		lt, lterr := p.parseTypeNameAfterCast()
		if lterr != nil {
			return parseSkipToSemicolonHelper(p, stmt)
		}
		member.LeftType = strings.ToLower(lt.String())
		member.RightType = member.LeftType // opclass_drop defaults righttype = lefttype (processTypesSpec)
		if p.acceptSymbol(",") {
			rt, rterr := p.parseTypeNameAfterCast()
			if rterr != nil {
				return parseSkipToSemicolonHelper(p, stmt)
			}
			member.RightType = strings.ToLower(rt.String())
		}
		p.acceptSymbol(")")
		member.HasExplicitArgTypes = true
		stmt.Members = append(stmt.Members, member)
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
	// Scan for "ON <event>", "TO <tablename>" and detect the rule kind (DO ALSO,
	// DO INSTEAD NOTHING, multi-statement, conditional, or utility action).
	// M0097-0140.
	depth := 0
	seenDo := false      // passed DO keyword at depth 0
	seenInstead := false // passed INSTEAD keyword at depth 0 after DO
	seenAlso := false    // DO ALSO detected
	hasWhere := false    // WHERE clause present before DO
	gotKind := false     // rule kind already determined
	event := ""          // ON <event> captured before TO (DU-002 slice 324)
	isNothing := false   // a NOTHING action was seen at depth 0 after DO
	hasAction := false   // a non-NOTHING action token was seen after DO
	var qual Expr        // WHERE qualification captured before DO (DU-002 slice 359)

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
				if depth == 0 && seenDo {
					hasAction = true
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
			case kw == "ON" && event == "" && !seenDo && ns.TableName.Name == "":
				p.advance()
				if ev := p.cur(); ev.Kind == TokenKeyword || ev.Kind == TokenIdent {
					event = strings.ToUpper(ev.Value)
					p.advance()
				}
				continue
			case kw == "TO" && ns.TableName.Name == "" && !seenDo:
				p.advance()
				tname, _ := p.parseObjectName()
				ns.TableName = tname
				continue
			case kw == "WHERE" && !seenDo:
				hasWhere = true
				// DU-002 slice 359: capture the WHERE qualification as a real
				// expression AST so a conditional DO INSTEAD NOTHING rule can
				// round-trip through pg_get_ruledef. Parse the a_expr directly
				// rather than letting the flat token scan discard it; parseExpr
				// consumes the whole balanced expression (including any parens) and
				// leaves us positioned at DO, so skip the trailing p.advance().
				p.advance() // consume WHERE
				q, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				qual = q
				continue
			case kw == "DO" && !seenDo:
				seenDo = true
			case seenDo && !seenInstead && !seenAlso && kw == "INSTEAD":
				seenInstead = true
			case seenDo && !seenInstead && !seenAlso && kw == "ALSO":
				ns.RuleKind = "DO ALSO"
				seenAlso = true
				gotKind = true
			case seenDo && kw == "NOTHING":
				isNothing = true
				if seenInstead && !gotKind {
					ns.RuleKind = "DO INSTEAD NOTHING"
					gotKind = true
				}
			case seenInstead && !gotKind && kw == "NOTIFY":
				ns.RuleKind = "utility"
				gotKind = true
				hasAction = true
			case seenInstead && !gotKind:
				// First meaningful token after INSTEAD: not NOTHING, not (, not NOTIFY.
				if hasWhere {
					ns.RuleKind = "conditional DO INSTEAD"
				} else {
					ns.RuleKind = "DO INSTEAD"
				}
				gotKind = true
				hasAction = true
			case seenDo && (seenAlso || (!seenInstead && !seenAlso)) && kw != "NOTHING":
				// A command after DO / DO ALSO (e.g. DO INSERT …) — a real action.
				hasAction = true
			}
		}
		p.advance()
	}

	// DU-002 slice 324: the unconditional DO-NOTHING form (no WHERE, no action
	// command) on an INSERT/UPDATE/DELETE event is modelled as a proper
	// CreateRuleStmt so it round-trips through pg_dump. DU-002 slice 359 extends
	// this to the CONDITIONAL DO-NOTHING form (`WHERE (qual) DO [INSTEAD]
	// NOTHING`): the captured qual rides CreateRuleStmt.Qual and is deparsed at
	// dump time. Every other form (action commands, DO ALSO with an action,
	// utility actions) keeps the historical CompatNoopStmt behaviour untouched.
	if isNothing && !hasAction && ns.ObjName.Name != "" && ns.TableName.Name != "" &&
		(event == "INSERT" || event == "UPDATE" || event == "DELETE") &&
		(!hasWhere || qual != nil) {
		return &CreateRuleStmt{
			pos:      pos,
			Name:     ns.ObjName.Name,
			Event:    event,
			Table:    ns.TableName,
			Instead:  seenInstead,
			Qual:     qual,
			RuleKind: ns.RuleKind,
		}, nil
	}
	return ns, nil
}

// parseCreateStatisticsTail parses CREATE STATISTICS after the STATISTICS keyword.
// Grammar: [IF NOT EXISTS] name [(kinds)] ON expr_list FROM table_name
// The optional kinds list and the ON column list are captured so pg_dump's
// pg_get_statisticsobjdef can reconstruct the object. DU-002 slice 314.
func (p *parser) parseCreateStatisticsTail(pos int) (Stmt, error) {
	stmt := &CreateStatisticsStmt{pos: pos}
	// IF NOT EXISTS (IF/NOT/EXISTS all lex as keyword tokens).
	if p.acceptKeyword(KwIf) {
		if !p.acceptKeyword(KwNot) {
			return p.parseSkipToSemicolon(pos)
		}
		if !p.acceptKeyword(KwExists) {
			return p.parseSkipToSemicolon(pos)
		}
		stmt.IfNotExists = true
	}
	name, err := p.parseObjectName()
	if err != nil {
		return p.parseSkipToSemicolon(pos)
	}
	stmt.Name = name
	// Optional (statistics_kind, ...) type list, e.g. `(ndistinct, mcv)`. Capture
	// each kind ident (lowercased) so the dump path can re-emit a non-default
	// kinds clause. A nested non-ident token (unexpected) bails to skip mode.
	if p.acceptSymbol("(") {
		for {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				return stmt, nil
			}
			if tok.Kind == TokenSymbol && tok.Value == ")" {
				p.advance()
				break
			}
			if tok.Kind == TokenIdent || tok.Kind == TokenKeyword {
				stmt.Kinds = append(stmt.Kinds, strings.ToLower(tok.Value))
				p.advance()
			} else if tok.Kind == TokenSymbol && tok.Value == "," {
				p.advance()
			} else {
				// Unexpected token inside the kinds list — fall back to the
				// tolerant skip-to-FROM path so parsing never hard-fails.
				p.advance()
			}
		}
	}
	// ON column list. Each target is either a simple column name or an
	// expression. Capture simple column names; flag any expression so the dump
	// path knows the column set is incomplete. The list runs until FROM.
	if p.acceptKeyword(KwOn) {
		for {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				return stmt, nil
			}
			if tok.Kind == TokenKeyword && tok.Keyword == KwFrom {
				break
			}
			if tok.Kind == TokenSymbol && tok.Value == "," {
				p.advance()
				continue
			}
			// A simple column reference: a bare ident NOT immediately followed by
			// '(' (which would start a function-call expression) and not '.'
			// (a qualified reference, treated as an expression target here).
			if tok.Kind == TokenIdent &&
				!(p.peek(1).Kind == TokenSymbol && (p.peek(1).Value == "(" || p.peek(1).Value == ".")) {
				stmt.Columns = append(stmt.Columns, identText(tok))
				p.advance()
				continue
			}
			// Anything else is an expression target. PG's grammar requires
			// expression statistics elements to be parenthesized (`ON (a + b)`),
			// so parse it as a full expression and capture the AST so the dump
			// path (pg_get_statisticsobjdef) can reconstruct it (DU-002 slice 316).
			stmt.HasExpr = true
			exprStart := p.idx
			if expr, err := p.parseExpr(); err == nil {
				stmt.Exprs = append(stmt.Exprs, expr)
			} else {
				// Tolerant fallback: the expression did not parse. Leave Exprs
				// empty (the dump path then declines to reconstruct) and skip to
				// the next comma at paren-depth 0 or to FROM.
				p.idx = exprStart
				depth := 0
				for {
					t := p.cur()
					if t.Kind == TokenEOF || (t.Kind == TokenSymbol && t.Value == ";") {
						return stmt, nil
					}
					if t.Kind == TokenSymbol && t.Value == "(" {
						depth++
					} else if t.Kind == TokenSymbol && t.Value == ")" {
						depth--
					} else if depth == 0 {
						if t.Kind == TokenSymbol && t.Value == "," {
							break
						}
						if t.Kind == TokenKeyword && t.Keyword == KwFrom {
							break
						}
					}
					p.advance()
				}
			}
		}
	}
	// Skip any remaining tokens until FROM keyword (defensive — the ON loop
	// normally stops exactly at FROM).
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

// parseCreateConversionTail parses the body of CREATE [DEFAULT] CONVERSION after
// the CONVERSION keyword has been consumed:
//
//	name FOR 'src_encoding' TO 'dest_encoding' FROM func_name
//
// It records the parsed pieces on a CompatNoopStmt so the executor registers the
// conversion in the catalog conversion registry (pg_dump getConversions /
// dumpConversion round-trip). isDefault is true for CREATE DEFAULT CONVERSION.
// DU-002 slice 399.
func (p *parser) parseCreateConversionTail(pos int, isDefault bool) (Stmt, error) {
	convName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	if !p.acceptKeyword(KwFor) {
		return nil, p.errAtCur("expected FOR in CREATE CONVERSION")
	}
	forEnc := p.cur()
	if forEnc.Kind != TokenStringLit {
		return nil, p.errAtCur("expected source encoding string literal in CREATE CONVERSION")
	}
	p.advance()
	if !p.acceptKeyword(KwTo) {
		return nil, p.errAtCur("expected TO in CREATE CONVERSION")
	}
	toEnc := p.cur()
	if toEnc.Kind != TokenStringLit {
		return nil, p.errAtCur("expected destination encoding string literal in CREATE CONVERSION")
	}
	p.advance()
	if !p.acceptKeyword(KwFrom) {
		return nil, p.errAtCur("expected FROM in CREATE CONVERSION")
	}
	funcName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt, err := p.parseSkipToSemicolon(pos)
	if err != nil {
		return nil, err
	}
	if ns, ok := stmt.(*CompatNoopStmt); ok {
		ns.Tag = "CREATE CONVERSION"
		ns.ObjType = "conversion"
		ns.ObjName = convName
		ns.ConvForEncoding = forEnc.Value
		ns.ConvToEncoding = toEnc.Value
		ns.ConvFuncName = funcName
		ns.ConvDefault = isDefault
	}
	return stmt, nil
}

// parseCreateForeignTableTail parses the tail of CREATE FOREIGN TABLE after the TABLE keyword.
// Grammar: name [(colDefs)] SERVER servername [OPTIONS (...)]
// Returns a CreateTableStmt with ForeignServer/ForeignOptions populated so
// pg_dump's getTables (relkind='f') + pg_foreign_table (ftserver/ftoptions)
// round-trip the `SERVER ... OPTIONS (...)` clause. DU-002 slice 417 — the
// column-level `OPTIONS (...)` clause (parseColumnDef) is captured onto
// ColumnDef.FDWOptions (DU-002 slice 418). Foreign tables are treated as
// regular tables for storage purposes in goopg v0.
func (p *parser) parseCreateForeignTableTail(pos int) (Stmt, error) {
	// Reuse the regular CREATE TABLE parser for name + column list.
	stmt, err := p.parseCreateTableTail(pos, false)
	if err != nil {
		return nil, err
	}
	ts, _ := stmt.(*CreateTableStmt)
	// Consume the SERVER name and optional OPTIONS (...) that follow the
	// column list.
	if p.acceptIdentKeyword("server") {
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		if ts != nil {
			ts.ForeignServer = name.String()
		}
	}
	if tok := p.cur(); tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "options") {
		opts := p.scanFDWOptionsList()
		if ts != nil {
			ts.ForeignOptions = opts
		}
	}
	// Skip anything else up to ';' or EOF (defensive; the grammar has no
	// further clauses here).
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

// parseCreateEventTriggerTail picks up after CREATE EVENT TRIGGER.
// Grammar (postgres/src/backend/parser/gram.y CreateEventTrigStmt):
//
//	name ON event
//	  [WHEN filtervar IN (value [, ...]) [AND filtervar IN (value [, ...])]]
//	  EXECUTE {FUNCTION|PROCEDURE} funcname()
//
// Only one filter variable is meaningful upstream ("tag"); a second, distinct
// filter variable is still captured (as FilterVar, last-write-wins) so
// execCreateEventTrigger can raise PostgreSQL's own "unrecognized filter
// variable" error at exec time rather than silently dropping it here.
func (p *parser) parseCreateEventTriggerTail(pos int) (Stmt, error) {
	stmt := &CreateEventTriggerStmt{pos: pos}
	name, err := p.parseIdent()
	if err != nil {
		return nil, p.errAtCur("expected event trigger name after CREATE EVENT TRIGGER")
	}
	stmt.Name = name.Value
	if !p.acceptKeyword(KwOn) {
		return nil, p.errAtCur("expected ON after event trigger name")
	}
	event, err := p.parseIdent()
	if err != nil {
		return nil, p.errAtCur("expected event name after ON")
	}
	stmt.Event = event.Value
	if p.acceptKeyword(KwWhen) {
		for {
			filterVar, err := p.parseIdent()
			if err != nil {
				return nil, p.errAtCur("expected filter variable in WHEN clause")
			}
			if !p.acceptKeyword(KwIn) {
				return nil, p.errAtCur("expected IN after filter variable")
			}
			if !p.acceptSymbol("(") {
				return nil, p.errAtCur("expected '(' after IN")
			}
			stmt.FilterVar = filterVar.Value
			for {
				if p.cur().Kind != TokenStringLit {
					return nil, p.errAtCur("expected string literal in filter value list")
				}
				if strings.EqualFold(filterVar.Value, "tag") {
					stmt.Tags = append(stmt.Tags, p.cur().Value)
				}
				p.advance()
				if p.acceptSymbol(",") {
					continue
				}
				break
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' to close filter value list")
			}
			if !p.acceptKeyword(KwAnd) {
				break
			}
		}
	}
	if !p.acceptKeyword(KwExecute) {
		return nil, p.errAtCur("expected EXECUTE in CREATE EVENT TRIGGER")
	}
	_ = p.acceptKeyword(KwFunction) || p.acceptKeyword(KwProcedure)
	funcName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.FuncName = funcName
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after function name")
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("event trigger functions take no arguments")
	}
	return stmt, nil
}

// parseCreateAccessMethodTail picks up after CREATE ACCESS METHOD.
// Grammar (postgres/src/backend/parser/gram.y CreateAmStmt):
//
//	name TYPE_P {INDEX | TABLE} HANDLER handler_name
//
// handler_name is a plain (possibly schema-qualified) function name with no
// parenthesized arg list — PostgreSQL resolves it via LookupFuncName against
// the fixed one-argument-of-type-internal handler signature (amcmds.c
// lookup_am_handler_func), mirrored by resolveAccessMethodHandlerFunc in the
// executor.
func (p *parser) parseCreateAccessMethodTail(pos int) (Stmt, error) {
	stmt := &CreateAccessMethodStmt{pos: pos}
	name, err := p.parseIdent()
	if err != nil {
		return nil, p.errAtCur("expected access method name after CREATE ACCESS METHOD")
	}
	stmt.Name = name.Value
	if !p.acceptIdentKeyword("type") {
		return nil, p.errAtCur("expected TYPE in CREATE ACCESS METHOD")
	}
	switch {
	case p.acceptKeyword(KwIndex):
		stmt.AMType = "i"
	case p.acceptKeyword(KwTable):
		stmt.AMType = "t"
	default:
		return nil, p.errAtCur("expected INDEX or TABLE in CREATE ACCESS METHOD")
	}
	if !p.acceptIdentKeyword("handler") {
		return nil, p.errAtCur("expected HANDLER in CREATE ACCESS METHOD")
	}
	handlerName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.HandlerName = handlerName
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
	// goopg captures security_barrier (M0119-0004 slice 366), security_invoker
	// (slice 367), and check_option (M0122-0004 follow-up) so all three round-trip
	// as pg_class.reloptions.
	if p.acceptKeyword(KwWith) {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after WITH in CREATE VIEW")
		}
		for !p.acceptSymbol(")") {
			// option name (identifier)
			optName, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			// optional = value. The value may be an identifier (true/off),
			// a boolean keyword token (true/false/on), a string literal
			// (pg_dump re-emits `security_barrier='true'`), or a number — so
			// capture the raw token text rather than insisting on an ident.
			optVal, hasVal := "", false
			if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
				p.advance()
				optVal = p.cur().Value
				p.advance()
				hasVal = true
			}
			// security_barrier surfaces as the `security_barrier=<bool>`
			// pg_class.reloption. A bare option (no `= value`) defaults to true,
			// matching PostgreSQL's boolean reloption handling (reloptions.c).
			if strings.EqualFold(optName.Value, "security_barrier") {
				b := true
				if hasVal {
					b = parseBoolReloptionValue(optVal)
				}
				stmt.SecurityBarrier = &b
			}
			// security_invoker surfaces as the `security_invoker=<bool>`
			// pg_class.reloption (slice 367). Same bare-option-defaults-true
			// boolean handling as security_barrier.
			if strings.EqualFold(optName.Value, "security_invoker") {
				b := true
				if hasVal {
					b = parseBoolReloptionValue(optVal)
				}
				stmt.SecurityInvoker = &b
			}
			// check_option surfaces as the `check_option=<local|cascaded>`
			// pg_class.reloption (view_reloptions' RELOPT_TYPE_ENUM entry,
			// reloptions.c) — the reloption-form spelling of the trailing
			// `WITH [CASCADED|LOCAL] CHECK OPTION` clause parsed below; both
			// set the same stmt.CheckOption field, so whichever spelling
			// appears (or a later one, if a statement absurdly used both)
			// wins. PG compares the value case-insensitively and defaults an
			// unrecognized/omitted value to cascaded — mirrored here rather
			// than raising a semantic error, matching this loop's existing
			// lenient handling of security_barrier/security_invoker above
			// (neither validates its boolean token either).
			if strings.EqualFold(optName.Value, "check_option") {
				mode := "cascaded"
				if hasVal && strings.EqualFold(strings.TrimSpace(optVal), "local") {
					mode = "local"
				}
				stmt.CheckOption = mode
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
	// Optional trailing WITH [CASCADED|LOCAL] CHECK OPTION clause. The mode is
	// captured into CheckOption ("cascaded" is the default when the qualifier is
	// omitted) so it surfaces as the `check_option` pg_class.reloption for pg_dump
	// AND is enforced at INSERT/UPDATE time via checkViewCheckOption
	// (internal/executor/operators_fk.go, M0119-0004 slice-365 follow-up).
	if p.acceptKeyword(KwWith) {
		mode := "cascaded"
		if p.acceptIdentKeyword("cascaded") {
			mode = "cascaded"
		} else if p.acceptKeyword(KwLocal) || p.acceptIdentKeyword("local") {
			mode = "local"
		}
		if p.cur().Kind != TokenKeyword || p.cur().Keyword != KwCheck {
			return nil, p.errAtCur("expected CHECK after WITH in view definition")
		}
		p.advance()
		if !p.acceptIdentKeyword("option") {
			return nil, p.errAtCur("expected OPTION after WITH [CASCADED|LOCAL] CHECK")
		}
		stmt.CheckOption = mode
	}
	return stmt, nil
}

// parseBoolReloptionValue maps a boolean reloption value token to a bool,
// mirroring PostgreSQL's parse_bool (true/on/yes/1 → true; everything else →
// false). Used for the view `security_barrier` storage option. M0119-0004 slice 366.
func parseBoolReloptionValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "on", "yes", "1", "t", "y":
		return true
	default:
		return false
	}
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
		// Optionally consume INITIALLY {IMMEDIATE|DEFERRED} — pg_dump
		// emits NOT DEFERRABLE INITIALLY IMMEDIATE as two independent
		// constraint attributes. DU-002.
		if p.acceptIdentKeyword("initially") {
			_ = p.acceptIdentKeyword("immediate") || p.acceptIdentKeyword("deferred")
		}
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

// isEnforcedAttr reports whether the token stream at cur begins a `[NOT]
// ENFORCED` attribute (bare ENFORCED, or NOT immediately followed by
// ENFORCED). Used to detect a duplicate after a single-shot consume;
// deliberately does NOT match NOT NULL / NOT VALID (the peek must be ENFORCED).
// M0134-0002 C2 slice 12.
func (p *parser) isEnforcedAttr() bool {
	t := p.cur()
	if t.Kind == TokenIdent && strings.EqualFold(t.Value, "enforced") {
		return true
	}
	return t.Kind == TokenKeyword && t.Keyword == KwNot &&
		p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "enforced")
}

// rejectDuplicateEnforced returns PG's "multiple ENFORCED/NOT ENFORCED clauses
// not allowed" when the current token begins a second `[NOT] ENFORCED`
// attribute. PG's transformConstraintAttrs (parse_utilcmd.c:3999-4027)
// rejects any second ENFORCED-ish ConstraintAttr with exactly this message
// (42601) — DIFFERENT from the table-level ConstraintAttributeSpec's
// "conflicting constraint properties" (gram.y:6234). Raw suppresses the
// "syntax error at or near" wrapper so the bare message matches PG verbatim.
// M0134-0002 C2 slice 12.
func (p *parser) rejectDuplicateEnforced() error {
	if p.isEnforcedAttr() {
		return &SyntaxError{
			Pos:     p.cur().Pos,
			Message: "multiple ENFORCED/NOT ENFORCED clauses not allowed",
			Raw:     true,
		}
	}
	return nil
}

// parseFKConstraintAttrs consumes the `[NOT] DEFERRABLE [INITIALLY DEFERRED |
// INITIALLY IMMEDIATE]`, `NOT VALID`, and `[NOT] ENFORCED` trailer that can
// follow a FOREIGN KEY constraint's REFERENCES/ON DELETE/ON UPDATE clauses, in
// any order (PG gram.y's ConstraintAttributeSpec list applies identically
// regardless of which FK form is being parsed). Shared by the inline column
// REFERENCES path, the table-level FOREIGN KEY path, and ALTER TABLE ADD
// FOREIGN KEY so all three stay in lockstep — NOT VALID/NOT ENFORCED
// previously only round-tripped through the ALTER TABLE form (DU-002 slice
// 431); this extends the same trailer to CREATE TABLE-time FK constraints
// (DU-002 slice 432). A second [NOT] ENFORCED is rejected like the CHECK
// forms (saw_enforced in transformConstraintAttrs) rather than silently
// overwriting the flag. M0134-0002 C2 slice 12.
func (p *parser) parseFKConstraintAttrs() (deferrable, initiallyDeferred, notValid, notEnforced bool, err error) {
	sawEnforced := false
	for {
		// Check for a duplicate before the consume so the caret lands on the
		// 2nd attribute's first token (its location in transformConstraintAttrs).
		if sawEnforced && p.isEnforcedAttr() {
			return deferrable, initiallyDeferred, notValid, notEnforced, p.rejectDuplicateEnforced()
		}
		if p.acceptKeyword(KwNot) {
			if p.acceptIdentKeyword("valid") {
				notValid = true
				continue
			}
			if p.acceptIdentKeyword("enforced") {
				sawEnforced = true
				notEnforced = true
				continue
			}
			_, _ = p.expectKeyword(KwDeferrable)
			deferrable = false
			continue
		}
		if p.acceptKeyword(KwDeferrable) {
			deferrable = true
			if p.acceptIdentKeyword("initially") {
				initiallyDeferred = p.acceptIdentKeyword("deferred")
				if !initiallyDeferred {
					_ = p.acceptIdentKeyword("immediate")
				}
			}
			continue
		}
		if p.acceptIdentKeyword("initially") {
			deferrable = true
			initiallyDeferred = p.acceptIdentKeyword("deferred")
			if !initiallyDeferred {
				_ = p.acceptIdentKeyword("immediate")
			}
			continue
		}
		if p.acceptIdentKeyword("enforced") { // bare ENFORCED — already the default
			sawEnforced = true
			continue
		}
		break
	}
	return
}

// parseAlterConstraintAttrs consumes the ConstraintAttributeSpec trailer of
// `ALTER TABLE ... ALTER CONSTRAINT name ...` (PG18 gram.y): `[NOT]
// DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE]` and/or `[NOT]
// ENFORCED`, in either order. Unlike parseFKConstraintAttrs (used by the
// FK-defining forms), NOT VALID is not part of this grammar production —
// real PG rejects it with "constraints cannot be altered to be NOT VALID"
// (processCASbits, gram.y ~19513) — so a bare NOT not followed by
// DEFERRABLE/ENFORCED is deliberately left unconsumed for the statement's
// normal trailing-token check to reject. The two has* return values record
// whether that attribute class was mentioned at all, mirroring
// ATAlterConstraint.alterDeferrability/alterEnforceability (tablecmds.c):
// ALTER CONSTRAINT only touches the attribute(s) actually named. DU-002
// slice 433.
func (p *parser) parseAlterConstraintAttrs() (deferrable, initiallyDeferred, enforced, hasDeferrability, hasEnforceability bool) {
	for {
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot {
			switch {
			case p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "enforced"):
				p.advance() // NOT
				p.advance() // ENFORCED
				hasEnforceability = true
				continue
			case p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwDeferrable:
				p.advance() // NOT
				p.advance() // DEFERRABLE
				hasDeferrability = true
				deferrable = false
				continue
			}
			break
		}
		if p.acceptKeyword(KwDeferrable) {
			deferrable = true
			hasDeferrability = true
			if p.acceptIdentKeyword("initially") {
				initiallyDeferred = p.acceptIdentKeyword("deferred")
				if !initiallyDeferred {
					_ = p.acceptIdentKeyword("immediate")
				}
			}
			continue
		}
		if p.acceptIdentKeyword("initially") {
			deferrable = true
			hasDeferrability = true
			initiallyDeferred = p.acceptIdentKeyword("deferred")
			if !initiallyDeferred {
				_ = p.acceptIdentKeyword("immediate")
			}
			continue
		}
		if p.acceptIdentKeyword("enforced") { // bare ENFORCED
			enforced = true
			hasEnforceability = true
			continue
		}
		break
	}
	return
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

	// CREATE TABLE name OF type_name [ ( column_options ) ] — a typed table
	// whose columns are derived from a composite type. The optional list
	// following OF type_name may contain `column_name WITH OPTIONS
	// column_constraint [...]` entries (parsed into stmt.OfTypeColumnOptions
	// and applied to the composite-derived columns by the executor) and/or
	// table_constraint entries (PRIMARY KEY/UNIQUE/CHECK/EXCLUDE/FOREIGN KEY/
	// CONSTRAINT at table level), interleavable in either order per PG's
	// grammar `TypedTableElement: columnOptions | TableConstraint`
	// (gram.y:3809-3812) — parsed via the same parseTableConstraintElement
	// helper the ordinary CREATE TABLE column list uses. DU-002 slice 374
	// follow-up.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOf {
		p.advance() // consume OF
		typeName, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt.OfType = &typeName
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			if !p.acceptSymbol(")") {
				for {
					if matched, err := p.parseTableConstraintElement(stmt); err != nil {
						return nil, err
					} else if !matched {
						nameTok, err := p.parseIdent()
						if err != nil {
							return nil, err
						}
						if !p.acceptKeyword(KwWith) && !p.acceptIdentKeyword("with") {
							return nil, p.errAtCur("expected WITH OPTIONS after column name in typed-table column list")
						}
						if !p.acceptIdentKeyword("options") {
							return nil, p.errAtCur("expected OPTIONS after WITH")
						}
						override := ColumnDef{Name: identText(nameTok)}
						if err := p.parseColumnConstraintList(&override); err != nil {
							return nil, err
						}
						stmt.OfTypeColumnOptions = append(stmt.OfTypeColumnOptions, override)
					}
					if p.acceptSymbol(")") {
						break
					}
					if !p.acceptSymbol(",") {
						return nil, p.errAtCur("expected ',' or ')' in typed-table column list")
					}
				}
			}
		}
		// Optional trailing clauses (PARTITION BY, WITH, TABLESPACE) follow the
		// same grammar as a normal CREATE TABLE.
		p.consumeCreateTableSuffix(stmt)
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
		// failed with a syntax error. Capture the name (M0122-0007 resolves and
		// stores it on the executor side, the non-partition path's sibling).
		// M0110-0001 (DU-002 slice 192).
		if p.acceptKeyword(KwTablespace) {
			tsTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Tablespace = identText(tsTok)
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
		// Table-level constraint: PRIMARY KEY/UNIQUE/CHECK/EXCLUDE/FOREIGN KEY/
		// CONSTRAINT name ... — parsed via the shared parseTableConstraintElement
		// helper (also used by the OF type_name typed-table list). DU-002 slice 374.
		if matched, err := p.parseTableConstraintElement(stmt); err != nil {
			return nil, err
		} else if matched {
			// handled by parseTableConstraintElement
		} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot &&
			p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwNull {
			// Standalone NOT NULL colname column constraint — pg_dump emits
			// these for inherited NOT NULL columns in CREATE TABLE ...
			// INHERITS (...). The column is inherited from the parent and
			// only the NOT NULL constraint is redeclared. DU-002 (M0119-0004).
			p.advance() // NOT
			p.advance() // NULL
			colNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			col := ColumnDef{Name: identText(colNameTok), NotNull: true}
			// Optional NO INHERIT
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("inherit")
				col.NotNullNoInherit = true
			}
			stmt.Columns = append(stmt.Columns, col)
			stmt.BodyOrder = append(stmt.BodyOrder, col.Name)
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
		// M0122-0007: capture the name; the executor resolves it against the
		// tablespace registry and stores/renders reltablespace. goopg's storage
		// manager still does not relocate the relation's physical files into
		// the tablespace's directory (catalog-metadata fidelity only).
		tsTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.Tablespace = identText(tsTok)
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

// parseTableConstraintElement attempts to parse a single table-level
// constraint clause (PRIMARY KEY/UNIQUE/CHECK/EXCLUDE/FOREIGN KEY/CONSTRAINT
// name ...) at the parser's current position, appending it to stmt's
// constraint fields. Returns matched=false (without consuming input) if the
// current token does not start a table constraint, so callers can fall
// through to column-definition or other list-element parsing. Shared between
// the ordinary CREATE TABLE column list and the OF type_name typed-table
// column/constraint list (DU-002 slice 374 follow-up).
func (p *parser) parseTableConstraintElement(stmt *CreateTableStmt) (bool, error) {
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwPrimary {
		p.advance()
		if _, err := p.expectKeyword(KwKey); err != nil {
			return false, err
		}
		if !p.acceptSymbol("(") {
			return false, p.errAtCur("expected '(' after PRIMARY KEY")
		}
		cols, err := p.parseColumnNameList()
		if err != nil {
			return false, err
		}
		if !p.acceptSymbol(")") {
			return false, p.errAtCur("expected ')'")
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
			return false, err
		}
		stmt.TableChecks = append(stmt.TableChecks, expr)
		// Accept optional NOT ENFORCED / ENFORCED modifier, recording
		// NOT ENFORCED per-check (parallel to TableChecks) so pg_dump
		// re-emits the trailing ` NOT ENFORCED` and pg_constraint reports
		// conenforced=false. DU-002 slice 430.
		notEnforced := false
		if p.acceptKeyword(KwNot) {
			if p.acceptIdentKeyword("enforced") {
				notEnforced = true
			}
		} else {
			_ = p.acceptIdentKeyword("enforced")
		}
		// A second [NOT] ENFORCED is a PG error (transformConstraintAttrs,
		// parse_utilcmd.c:3999-4027). M0134-0002 C2 slice 12.
		if err := p.rejectDuplicateEnforced(); err != nil {
			return false, err
		}
		// Accept optional NO INHERIT, recording it per-check so the suffix
		// round-trips through the dump. DU-002 slice 128.
		noInherit := false
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("inherit")
			stmt.TableHasNoInheritCheck = true
			noInherit = true
		}
		// Accept optional NOT VALID trailer, consumed-and-dropped. PG
		// auto-validates NOT VALID at CREATE TABLE — transformCheckConstraints
		// (parse_utilcmd.c:2946) overrides it (skip_validation=true,
		// initially_valid=is_enforced) and heap.c:2584-2587 writes convalidated
		// from initially_valid, so a fresh table's constraint is created
		// validated (no convalidated='f' recorded here). M0134-0002 C2 slice 10.
		if p.acceptKeyword(KwNot) {
			_ = p.acceptIdentKeyword("valid")
		}
		stmt.TableCheckNoInherit = append(stmt.TableCheckNoInherit, noInherit)
		stmt.TableCheckNotEnforced = append(stmt.TableCheckNotEnforced, notEnforced)
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
			return false, err
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
				return false, err
			}
			if !p.acceptSymbol("(") {
				return false, p.errAtCur("expected '(' after PRIMARY KEY")
			}
			cols, err := p.parseColumnNameList()
			if err != nil {
				return false, err
			}
			if !p.acceptSymbol(")") {
				return false, p.errAtCur("expected ')'")
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
				return false, err
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
			notEnforced := false
			if p.acceptKeyword(KwNot) {
				if p.acceptIdentKeyword("enforced") {
					notEnforced = true
				}
			} else {
				_ = p.acceptIdentKeyword("enforced")
			}
			// A second [NOT] ENFORCED is a PG error (transformConstraintAttrs,
			// parse_utilcmd.c:3999-4027). M0134-0002 C2 slice 12.
			if err := p.rejectDuplicateEnforced(); err != nil {
				return false, err
			}
			// Accept optional NO INHERIT (CONSTRAINT name CHECK NO INHERIT).
			noInherit := false
			if p.acceptIdentKeyword("no") {
				_ = p.acceptIdentKeyword("inherit")
				stmt.TableHasNoInheritCheck = true
				noInherit = true
			}
			// Accept optional NOT VALID trailer, consumed-and-dropped (same
			// reason as the anonymous arm: PG auto-validates at CREATE TABLE,
			// parse_utilcmd.c:2946 + heap.c:2584-2587). M0134-0002 C2 slice 10.
			if p.acceptKeyword(KwNot) {
				_ = p.acceptIdentKeyword("valid")
			}
			// Keep TableCheckNoInherit/TableCheckNotEnforced parallel to
			// TableChecks: only the anonymous branch appended an expr.
			// DU-002 slices 128, 430.
			if anonCheck {
				stmt.TableCheckNoInherit = append(stmt.TableCheckNoInherit, noInherit)
				stmt.TableCheckNotEnforced = append(stmt.TableCheckNotEnforced, notEnforced)
			} else if len(stmt.TableNamedChecks) > 0 {
				// Named branch appended before NO INHERIT/NOT ENFORCED were
				// parsed; carry the per-constraint flags so the named check
				// re-emits the suffixes on dump. DU-002 slices 129, 430.
				if noInherit {
					stmt.TableNamedChecks[len(stmt.TableNamedChecks)-1].NoInherit = true
				}
				if notEnforced {
					stmt.TableNamedChecks[len(stmt.TableNamedChecks)-1].NotEnforced = true
				}
			}
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign:
			// CONSTRAINT name FOREIGN KEY (cols) REFERENCES t (cols) … —
			// table-level (possibly composite) FK. DU-002 slice 53.
			fk, err := p.parseTableForeignKey(constraintName)
			if err != nil {
				return false, err
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
	} else {
		return false, nil
	}
	return true, nil
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
			if tsTok, err := p.parseIdent(); err == nil {
				stmt.Tablespace = identText(tsTok)
			}
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
	if err := p.parseColumnConstraintList(&col); err != nil {
		return ColumnDef{}, err
	}
	return col, nil
}

// parseColumnConstraintList parses the constraint-clause suffix shared by a
// normal typed column definition and the untyped `column_name WITH OPTIONS
// column_constraint` form used by CREATE TABLE ... OF type_name ( ... ), where
// the column has no type of its own (it is derived from the composite type)
// but may still carry NOT NULL/DEFAULT/CHECK/etc. overrides. Stops at the
// first token it does not recognise as a constraint (comma, closing paren,
// ...), leaving it unconsumed for the caller. DU-002 slice 374 follow-up.
func (p *parser) parseColumnConstraintList(col *ColumnDef) error {
	for {
		switch {
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot:
			p.advance()
			if _, err := p.expectKeyword(KwNull); err != nil {
				return err
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
				return err
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
		// STORAGE mode — column-level storage strategy, PG gram.y column_storage
		// (gram.y:3888-3896): `col type STORAGE {PLAIN|EXTERNAL|EXTENDED|MAIN}`.
		// `storage` and the four mode names lex as TokenIdent (unreserved words).
		// goopg does not actually TOAST, but records the mode so the synthesized
		// pg_attribute row reports attstorage and pg_dump re-emits a SET STORAGE
		// clause when it differs from the type default; the executor enforces
		// GetAttributeStorage's datatype-vs-mode rule (tablecmds.c:22082-22112)
		// when the CREATE TABLE runs. Mirrors the COMPRESSION arm's structure.
		// M0134-0002 C2 slice 9.
		case p.acceptIdentKeyword("storage"):
			switch {
			case p.acceptIdentKeyword("plain"):
				col.Storage = "plain"
			case p.acceptIdentKeyword("external"):
				col.Storage = "external"
			case p.acceptIdentKeyword("extended"):
				col.Storage = "extended"
			case p.acceptIdentKeyword("main"):
				col.Storage = "main"
			default:
				return p.errAtCur("expected plain, external, extended, or main after STORAGE")
			}
		// OPTIONS ( name 'value', … ) — per-column FOREIGN TABLE options
		// (`c1 int OPTIONS (column_name 'col1')`). Captured onto
		// catalog.Column.FDWOptions so pg_attribute.attfdwoptions round-trips
		// through pg_dump (`ALTER FOREIGN TABLE ... ALTER COLUMN ... OPTIONS`).
		// DU-002 slice 418.
		case p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "options"):
			col.FDWOptions = p.scanFDWOptionsList()
		// GENERATED [ALWAYS|BY DEFAULT] AS IDENTITY or GENERATED ALWAYS AS (expr) STORED (M0096-0008)
		case p.acceptIdentKeyword("generated"):
			isAlways := p.acceptIdentKeyword("always")
			isByDefault := !isAlways && (p.acceptKeyword(KwBy) && p.acceptKeyword(KwDefault))
			if !isAlways && !isByDefault {
				return p.errAtCur("expected ALWAYS or BY DEFAULT after GENERATED")
			}
			if _, err := p.expectKeyword(KwAs); err != nil {
				return err
			}
			// GENERATED ALWAYS AS IDENTITY [(sequence_options)] — identity column.
			if p.acceptIdentKeyword("identity") {
				col.IdentityColumn = true
				col.IdentityAlways = isAlways
				// Parse the optional `(sequence_options)` clause. Every option is
				// threaded to the backing sequence so pg_dump's
				// `ADD GENERATED ... AS IDENTITY (...)` re-emits the non-default
				// INCREMENT BY / MINVALUE / MAXVALUE / CACHE / CYCLE, not just
				// START WITH. The option grammar matches CREATE SEQUENCE
				// (parseCreateSequenceTail). DU-002 (pg_dump 002–010).
				if p.acceptSymbol("(") {
					for !p.acceptSymbol(")") {
						switch {
						case p.acceptIdentKeyword("start"):
							_ = p.acceptKeyword(KwWith) || p.acceptIdentKeyword("with")
							v, err := p.parseInt64()
							if err != nil {
								return err
							}
							col.IdentityStart = v
						case p.acceptIdentKeyword("increment"):
							_ = p.acceptKeyword(KwBy)
							v, err := p.parseInt64()
							if err != nil {
								return err
							}
							col.IdentityIncrement = &v
						case p.acceptIdentKeyword("minvalue"):
							v, err := p.parseInt64()
							if err != nil {
								return err
							}
							col.IdentityMin = &v
						case p.acceptIdentKeyword("maxvalue"):
							v, err := p.parseInt64()
							if err != nil {
								return err
							}
							col.IdentityMax = &v
						case p.acceptIdentKeyword("cache"):
							v, err := p.parseInt64()
							if err != nil {
								return err
							}
							col.IdentityCache = &v
						case p.acceptIdentKeyword("cycle"):
							col.IdentityCycle = true
						case p.acceptIdentKeyword("no"):
							// NO MINVALUE / NO MAXVALUE / NO CYCLE — leave the
							// field nil/false so the type default applies.
							_ = p.acceptIdentKeyword("minvalue") || p.acceptIdentKeyword("maxvalue") || p.acceptIdentKeyword("cycle")
						case p.cur().Kind == TokenEOF:
							return p.errAtCur("unterminated identity sequence options")
						default:
							return p.errAtCur("unrecognised identity sequence option")
						}
					}
				}
				continue
			}
			if !p.acceptSymbol("(") {
				return p.errAtCur("expected '(' after GENERATED ALWAYS AS")
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
				return p.errAtCur("expected ')' to close generated expression")
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
				return err
			}
			col.DefaultExpr = expr
		// REFERENCES — parse FK constraint and populate col FK fields. M0096-0011.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReferences:
			p.advance()
			refTable, err := p.parseObjectName()
			if err != nil {
				return err
			}
			col.RefTable = refTable
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				p.advance()
				refCols, err := p.parseColumnNameList()
				if err != nil {
					return err
				}
				if !p.acceptSymbol(")") {
					return p.errAtCur("expected ')'")
				}
				col.RefColumns = refCols
			}
			// Optional MATCH FULL | PARTIAL | SIMPLE, between the referenced column
			// list and the ON DELETE/UPDATE clauses (PG gram.y key_match). DU-002 slice 309.
			col.FKMatchFull = parseFKMatchType(p)
			// Parse ON DELETE / ON UPDATE clauses. ON is KwOn (reserved).
			for p.acceptKeyword(KwOn) {
				isDelete := p.acceptKeyword(KwDelete)
				if !isDelete {
					_ = p.acceptKeyword(KwUpdate)
				}
				action, setCols, aerr := parseFKAction(p)
				if aerr != nil {
					return aerr
				}
				if isDelete {
					col.OnDelete = action
					col.OnDeleteSetCols = setCols
				} else {
					col.OnUpdate = action
				}
			}
			// Parse [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE],
			// NOT VALID, and [NOT] ENFORCED, in any order (PG18 for the latter
			// two). DU-002 slice 432 — extends slice 431's ALTER TABLE-only fix
			// to the inline column REFERENCES form.
			var fkErr error
			col.FKDeferrable, col.FKInitiallyDeferred, col.FKNotValid, col.FKNotEnforced, fkErr = p.parseFKConstraintAttrs()
			if fkErr != nil {
				return fkErr
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
				return err
			}
			col.CheckExpr = expr
			// Accept optional NOT ENFORCED / ENFORCED, recording NOT ENFORCED
			// so pg_dump re-emits the trailing ` NOT ENFORCED` and
			// pg_constraint reports conenforced=false. DU-002 slice 430.
			if p.acceptKeyword(KwNot) {
				if p.acceptIdentKeyword("enforced") {
					col.CheckNotEnforced = true
				}
			} else {
				_ = p.acceptIdentKeyword("enforced")
			}
			// A second [NOT] ENFORCED is a PG error (transformConstraintAttrs,
			// parse_utilcmd.c:3999-4027), not an unconsumed trailing token.
			// M0134-0002 C2 slice 12.
			if err := p.rejectDuplicateEnforced(); err != nil {
				return err
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
					return err
				}
				col.CheckExpr = expr
				if p.acceptKeyword(KwNot) {
					if p.acceptIdentKeyword("enforced") {
						col.CheckNotEnforced = true
					}
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
				// A second [NOT] ENFORCED is a PG error (transformConstraintAttrs,
				// parse_utilcmd.c:3999-4027). M0134-0002 C2 slice 12.
				if err := p.rejectDuplicateEnforced(); err != nil {
					return err
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
					return err
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
					return err
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
			return nil
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
		ct.Name = p.parseMultiWordTypeName(ct.Name)
	}
	// Interval carries a field/precision typmod qualifier in the column
	// position (`c interval year to month`, `c interval second(2)`,
	// `c interval(2)`) rather than a generic `(N[,M])` typmod list. The packed
	// INTERVAL_TYPMOD is stored in Args[0]; it keeps the FULL range mask so
	// pg_attribute.atttypmod round-trips the declared spelling.
	// unimplemented_feat #5(d-iv).
	if ct.Schema == "" && strings.EqualFold(ct.Name, "interval") {
		if tm, matched, terr := p.parseIntervalColumnQualifier(); terr != nil {
			return ColumnType{}, terr
		} else if matched {
			ct.Args = []int64{tm}
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
		ct.Name = p.parseTimeZoneQualifierAfterArgs(ct.Name)
	}
	// Bare `char`/`character` (and the SQL national aliases `nchar` /
	// `national character`, which parseMultiWordTypeName collapses to
	// "character") default to an implicit length of 1 upstream (gram.y
	// CharacterWithoutLength → bpchar with typmod 1). `bpchar` spelled
	// directly takes typmod -1 (unbounded) and is deliberately NOT stamped.
	// M0119-0006 (77th slice): `character` was missing from this default, so
	// a bare `character`/`nchar` column was character(-1) where PG 18.3
	// makes it character(1).
	if first.Kind != TokenQuotedIdent && len(ct.Args) == 0 &&
		(strings.EqualFold(ct.Name, "char") || strings.EqualFold(ct.Name, "character")) {
		ct.Args = []int64{1}
	}
	// FLOAT [ (precision) ] → float4/float8 (gram.y opt_float). A quoted
	// "float" names a user type, not the standard alias, so skip it.
	if first.Kind != TokenQuotedIdent && ct.Schema == "" {
		name, args, err := normalizeFloatTypeName(ct.Name, ct.Args, pos)
		if err != nil {
			return ColumnType{}, err
		}
		ct.Name, ct.Args = name, args
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

// normalizeFloatTypeName resolves the SQL-standard `FLOAT [ ( precision ) ]`
// spelling to the concrete goopg/PG type it names, mirroring upstream's
// opt_float production (postgres/src/backend/parser/gram.y): no precision →
// float8, 1..24 → float4, 25..53 → float8, and out-of-range precisions are
// hard errors with ERRCODE_INVALID_PARAMETER_VALUE. Upstream builds a
// SystemTypeName("float4"/"float8") that carries NO typmod, so the precision
// is consumed by the reduction rather than stored — the returned args are
// therefore nil for a recognised float(p).
//
// Without this, "float" fell through catalog.TypeNameToOID's `default:
// return OIDText` fallback and a `c3 float` column was created as text while
// the executor's own type tables (codec.go, expr.go) still read the name as
// float8 — the encode/decode sibling split that made an INSERT report
// "INSERT 0 1" and then return zero rows (regress `index_including` §10).
//
// name must already be lower-cased or is compared case-insensitively; an
// optional trailing "[]" array suffix (the cast paths append it to the type
// name) is preserved. Types other than float are returned unchanged.
func normalizeFloatTypeName(name string, args []int64, pos int) (string, []int64, error) {
	base, suffix := name, ""
	if strings.HasSuffix(base, "[]") {
		base, suffix = base[:len(base)-2], "[]"
	}
	if !strings.EqualFold(base, "float") {
		return name, args, nil
	}
	if len(args) == 0 {
		return "float8" + suffix, nil, nil
	}
	// PG's opt_float only ever sees a single Iconst; a second one is a
	// syntax error in the grammar, so anything else keeps the raw spelling
	// and lets the downstream type lookup report it.
	if len(args) != 1 {
		return name, args, nil
	}
	switch p := args[0]; {
	case p < 1:
		return "", nil, &SyntaxError{
			Pos: pos, Raw: true, Code: "22023",
			Message: "precision for type float must be at least 1 bit",
		}
	case p <= 24:
		return "float4" + suffix, nil, nil
	case p <= 53:
		return "float8" + suffix, nil, nil
	default:
		return "", nil, &SyntaxError{
			Pos: pos, Raw: true, Code: "22023",
			Message: "precision for type float must be less than 54 bits",
		}
	}
}

// parseMultiWordTypeName consumes the trailing keywords of a multi-word
// built-in type name (double precision, character varying, bit varying,
// timestamp/time [with|without time zone]) that follows an already-parsed
// leading identifier, returning the canonical short type name. If nothing
// matches, leading is returned unchanged and no tokens are consumed. Shared
// by parseColumnType (CREATE TABLE column types) and parseCreateDomain
// (CREATE DOMAIN's AS base_type clause) so both accept the same spellings
// pg_dump emits.
func (p *parser) parseMultiWordTypeName(leading string) string {
	switch strings.ToLower(leading) {
	case "double":
		if p.acceptIdentKeyword("precision") {
			return "float8"
		}
	case "character", "char":
		if p.acceptIdentKeyword("varying") {
			return "varchar"
		}
	case "nchar":
		if p.acceptIdentKeyword("varying") {
			return "varchar"
		}
		// Bare `nchar` is the SQL national-character alias of `character`
		// (bpchar): `f(nchar)` ≡ `f(character)` (gram.y character: NCHAR
		// opt_varying). `character`/`char` bare are NOT collapsed here —
		// they keep their display names (parseColumnType stamps the bare
		// `char` typmod separately, and the CHAROID `"char"` is quoted).
		return "character"
	case "national":
		if p.acceptIdentKeyword("character", "char") {
			if p.acceptIdentKeyword("varying") {
				return "varchar"
			}
			return "character"
		}
	case "bit":
		if p.acceptIdentKeyword("varying") {
			return "varbit"
		}
	case "timestamp":
		if p.acceptKeyword(KwWith) {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timestamptz"
		} else if p.acceptIdentKeyword("without") {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timestamp"
		}
	case "time":
		if p.acceptKeyword(KwWith) {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timetz"
		} else if p.acceptIdentKeyword("without") {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "time"
		}
	}
	return leading
}

// parseTimeZoneQualifierAfterArgs consumes an optional WITH/WITHOUT TIME ZONE
// qualifier that follows the typmod parens, for "time(N) with/without time
// zone" / "timestamp(N) with/without time zone" (the qualifier trails the
// parens rather than the bare type name in this form). Shared by
// parseColumnType and parseCreateDomain.
func (p *parser) parseTimeZoneQualifierAfterArgs(name string) string {
	switch strings.ToLower(name) {
	case "time":
		if p.acceptKeyword(KwWith) {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timetz"
		} else if p.acceptIdentKeyword("without") {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "time"
		}
	case "timestamp":
		if p.acceptKeyword(KwWith) {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timestamptz"
		} else if p.acceptIdentKeyword("without") {
			p.acceptIdentKeyword("time")
			p.acceptIdentKeyword("zone")
			return "timestamp"
		}
	}
	return name
}

// parseFKAction reads the referential action keyword from the current
// token position. Used by REFERENCES … ON DELETE / ON UPDATE parsing.
// M0096-0011. Note: cascade/restrict/set are registered keywords;
// "no" and "action" are plain identifiers.
func parseFKAction(p *parser) (FKAction, []string, error) {
	if p.acceptKeyword(KwCascade) {
		return FKActionCascade, nil, nil
	}
	if p.acceptKeyword(KwRestrict) {
		return FKActionRestrict, nil, nil
	}
	if p.acceptIdentKeyword("no") {
		_ = p.acceptIdentKeyword("action")
		return FKActionNoAction, nil, nil
	}
	if p.acceptKeyword(KwSet) {
		act := FKActionSetDefault
		if p.acceptKeyword(KwNull) {
			act = FKActionSetNull
		} else {
			_ = p.acceptKeyword(KwDefault)
		}
		// PG15: an optional column list after SET NULL | SET DEFAULT restricts the
		// action to a subset of the FK's referencing columns
		// (pg_constraint.confdelsetcols). The grammar (gram.y key_action) permits
		// the list after SET NULL/DEFAULT regardless of ON UPDATE vs ON DELETE; PG
		// rejects it for ON UPDATE in parse-analysis (errcode 0A000), which the
		// caller mirrors via its isDelete gate. pg_get_constraintdef appends it as
		// ` (col1, col2)` after the ON DELETE clause (ruleutils.c:2376). DU-002 slice 311.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance()
			cols, err := p.parseColumnNameList()
			if err != nil {
				return act, nil, err
			}
			if !p.acceptSymbol(")") {
				return act, nil, p.errAtCur("expected ')'")
			}
			return act, cols, nil
		}
		return act, nil, nil
	}
	return FKActionNoAction, nil, nil
}

// parseFKMatchType consumes an optional `MATCH FULL | MATCH PARTIAL | MATCH
// SIMPLE` clause at the current token position and reports whether the match
// type is FULL. It is positioned between the REFERENCES column list and the ON
// DELETE / ON UPDATE clauses, mirroring PG's gram.y key_match production. MATCH
// SIMPLE is the default (returns false); MATCH PARTIAL is accepted for grammar
// completeness but, like upstream, is treated as non-FULL (PG itself errors at
// constraint-creation time, not parse time). `MATCH` and `FULL`/`PARTIAL`/
// `SIMPLE` are unreserved, so they are matched as plain identifiers. DU-002 slice 309.
func parseFKMatchType(p *parser) bool {
	if !p.acceptIdentKeyword("match") {
		return false
	}
	if p.acceptKeyword(KwFull) || p.acceptIdentKeyword("full") {
		return true
	}
	// MATCH PARTIAL / MATCH SIMPLE — consume the keyword, non-FULL.
	_ = p.acceptIdentKeyword("partial") || p.acceptIdentKeyword("simple")
	return false
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
	// Optional TABLESPACE clause. Comes after WITH (storage_parameter) and
	// before WHERE in real PG grammar (gram.y IndexStmt: OptTableSpace before
	// where_clause). M0122-0007.
	if p.acceptKeyword(KwTablespace) {
		tsTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		stmt.Tablespace = identText(tsTok)
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

		// Optional COLLATE "..." or COLLATE ident. Capture the name so
		// pg_get_indexdef / pg_dump can re-emit a non-default per-column COLLATE
		// clause (after the column, before the opclass). parseCollationName
		// accepts an optional schema qualifier (`pg_catalog."C"`) and returns the
		// trailing component, matching pg_collation.collname. DU-002 slice 313.
		var colCollation string
		if p.acceptIdentKeyword("collate") {
			collName, _ := p.parseCollationName()
			colCollation = collName
		}

		// Optional opclass name (bare ident that is not a known keyword
		// and not ',' or ')'). `NULLS` lexes as a bare TokenIdent, so guard
		// against it here — otherwise `(col NULLS FIRST)` mis-reads NULLS as an
		// opclass name and the trailing FIRST/LAST then fails to parse.
		var colOpClass string
		if p.cur().Kind == TokenIdent && !strings.EqualFold(p.cur().Value, "nulls") {
			// This is the opclass name — capture it so pg_get_indexdef / pg_dump
			// can re-emit a non-default operator class after the column. DU-002
			// slice 312.
			opClassName := p.cur().Value
			colOpClass = opClassName
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
		order.OpClass = colOpClass
		order.Collation = colCollation
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
	// DROP POLICY [IF EXISTS] name ON table [CASCADE|RESTRICT] — DU-002 slice 323.
	if p.acceptIdentKeyword("policy") {
		return p.parseDropPolicyTail(t.Pos)
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
	// DROP USER MAPPING [IF EXISTS] FOR <user> SERVER <server> — must be caught
	// before the generic "user" compat-stub (which would treat MAPPING as a role
	// name). Records the (user, server) pair so the executor unregisters it.
	// DU-002 slice 377.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "user") &&
		(p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "mapping")) {
		p.advance() // user
		p.advance() // mapping
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		userName, srvName, _ := p.scanUserMappingForServer() // DROP ignores OPTIONS
		return &DropCompatStmt{pos: t.Pos, ObjType: "user mapping", IfExists: ifExists,
			Names: []ObjectName{{Name: userName}, {Name: srvName}}}, nil
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
	// DROP TRANSFORM [IF EXISTS] FOR type LANGUAGE lang [CASCADE|RESTRICT] —
	// "transform" is an ident keyword; the identity is a (type, language) pair,
	// not a name, so it cannot use the generic ident-based stub loop below (that
	// loop's parseDropTail expects a comma-separated name list). DU-002
	// (M0119-0004).
	if p.acceptIdentKeyword("transform") {
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if _, err := p.expectKeyword(KwExists); err != nil {
				return nil, err
			}
			ifExists = true
		}
		if !p.acceptKeyword(KwFor) {
			return nil, p.errAtCur("expected FOR in DROP TRANSFORM")
		}
		typeName := p.parseSimpleTypeName(KwLanguage)
		if !p.acceptKeyword(KwLanguage) {
			return nil, p.errAtCur("expected LANGUAGE in DROP TRANSFORM")
		}
		lang, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		behavior := DropDefault
		switch {
		case p.acceptKeyword(KwCascade):
			behavior = DropCascade
		case p.acceptKeyword(KwRestrict):
		}
		return &DropCompatStmt{
			pos:           t.Pos,
			ObjType:       "transform",
			IfExists:      ifExists,
			Behavior:      behavior,
			TransformType: typeName,
			TransformLang: lang.Value,
		}, nil
	}
	// Handle ident-based DROP targets as compatibility stubs. M0097-0008.
	for _, objType := range []string{
		"sequence", "schema",
		"collation",
		"materialized", "extension", "server",
		"language", "access", "event",
		"group", "role", "user",
		"conversion", // M0097-0071
		"statistics", // DU-002 restart-persistence follow-up (previously unparsed)
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
			// "event trigger" is two words; TRIGGER is a keyword token, not
			// ident (unlike "materialized"/"access"'s continuations, which
			// are ident-typed). DU-002 (M0119-0004).
			if objType == "event" {
				if _, err := p.expectKeyword(KwTrigger); err != nil {
					return nil, err
				}
				resolvedType = "event trigger"
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
	// Optional MATCH FULL | PARTIAL | SIMPLE, between the referenced column list
	// and the ON DELETE/UPDATE clauses (PG gram.y key_match). DU-002 slice 309.
	fk.MatchFull = parseFKMatchType(p)
	// ON DELETE / ON UPDATE referential-action clauses. ON is KwOn (reserved).
	for p.acceptKeyword(KwOn) {
		isDelete := p.acceptKeyword(KwDelete)
		if !isDelete {
			_ = p.acceptKeyword(KwUpdate)
		}
		action, setCols, aerr := parseFKAction(p)
		if aerr != nil {
			return TableForeignKeyDef{}, aerr
		}
		if isDelete {
			fk.OnDelete = action
			fk.OnDeleteSetCols = setCols
		} else {
			fk.OnUpdate = action
		}
	}
	// [NOT] DEFERRABLE [INITIALLY DEFERRED | INITIALLY IMMEDIATE], NOT VALID,
	// and [NOT] ENFORCED, in any order (PG18 for the latter two). DU-002 slice
	// 432 — extends slice 431's ALTER TABLE-only fix to the table-level
	// FOREIGN KEY form.
	var fkErr error
	fk.Deferrable, fk.InitiallyDeferred, fk.NotValid, fk.NotEnforced, fkErr = p.parseFKConstraintAttrs()
	if fkErr != nil {
		return TableForeignKeyDef{}, fkErr
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
	// Optional WHERE (predicate) — a partial EXCLUDE constraint. PG renders this
	// after the operator/INCLUDE list and before DEFERRABLE in
	// pg_get_constraintdef (via pg_get_indexdef_worker). Captured as an Expr so
	// the executor can store it on the backing index's PredicateString and
	// pg_dump re-emits ` WHERE (pred)`. Previously silently dropped, downgrading a
	// partial exclusion to one applying to every row on restore. DU-002 slice 310.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhere {
		p.advance()
		if pred, err := p.parseExpr(); err == nil {
			cdef.ExclusionWhere = pred
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

// parseCreatePolicyTail picks up after CREATE POLICY. Grammar:
//
//	CREATE POLICY name ON table_name
//	  [ AS { PERMISSIVE | RESTRICTIVE } ]
//	  [ FOR { ALL | SELECT | INSERT | UPDATE | DELETE } ]
//	  [ TO { role_name | PUBLIC } [, ...] ]
//	  [ USING ( using_expression ) ]
//	  [ WITH CHECK ( check_expression ) ]
//
// goopg does not enforce row-level security; the statement is parsed and stored
// so the policy round-trips through pg_dump (pg_policy → dumpPolicy). DU-002
// slice 323.
func (p *parser) parseCreatePolicyTail(pos int) (Stmt, error) {
	nameTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt := &CreatePolicyStmt{
		pos:        pos,
		Name:       identText(nameTok),
		Permissive: true,  // PG default is PERMISSIVE
		Command:    "all", // PG default is FOR ALL
	}
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	tblName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Table = tblName

	// [ AS { PERMISSIVE | RESTRICTIVE } ]
	if p.acceptKeyword(KwAs) {
		switch {
		case p.acceptIdentKeyword("permissive"):
			stmt.Permissive = true
		case p.acceptIdentKeyword("restrictive"):
			stmt.Permissive = false
		default:
			return nil, p.errAtCur("expected PERMISSIVE or RESTRICTIVE after AS")
		}
	}

	// [ FOR { ALL | SELECT | INSERT | UPDATE | DELETE } ]
	if p.acceptKeyword(KwFor) {
		switch {
		case p.acceptKeyword(KwAll):
			stmt.Command = "all"
		case p.acceptKeyword(KwSelect):
			stmt.Command = "select"
		case p.acceptKeyword(KwInsert):
			stmt.Command = "insert"
		case p.acceptKeyword(KwUpdate):
			stmt.Command = "update"
		case p.acceptKeyword(KwDelete):
			stmt.Command = "delete"
		default:
			return nil, p.errAtCur("expected ALL, SELECT, INSERT, UPDATE, or DELETE after FOR")
		}
	}

	// [ TO role [, ...] ] — PUBLIC and named roles both accepted as identifiers.
	if p.acceptKeyword(KwTo) {
		for {
			roleTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Roles = append(stmt.Roles, identText(roleTok))
			if !p.acceptSymbol(",") {
				break
			}
		}
	}

	// [ USING ( expr ) ]
	if p.acceptKeyword(KwUsing) {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after USING")
		}
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after USING expression")
		}
		stmt.Using = expr
	}

	// [ WITH CHECK ( expr ) ]
	if p.acceptKeyword(KwWith) {
		if _, err := p.expectKeyword(KwCheck); err != nil {
			return nil, err
		}
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after WITH CHECK")
		}
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after WITH CHECK expression")
		}
		stmt.WithCheck = expr
	}

	return stmt, nil
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
func (p *parser) parseCreateTriggerTail(pos int, isConstraint bool) (Stmt, error) {
	// Trigger name
	nameTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	stmt := &CreateTriggerStmt{pos: pos, Name: identText(nameTok), IsConstraint: isConstraint}

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
			// Optional column-specific list: UPDATE OF col1, col2, …
			// (only valid for the UPDATE event). DU-002 slice 326.
			if p.acceptKeyword(KwOf) {
				for {
					colTok, err := p.parseIdent()
					if err != nil {
						return nil, err
					}
					stmt.UpdateColumns = append(stmt.UpdateColumns, identText(colTok))
					if !p.acceptSymbol(",") {
						break
					}
				}
			}
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

	// CONSTRAINT-trigger deferrability: `[NOT] DEFERRABLE [INITIALLY
	// {IMMEDIATE|DEFERRED}]` appears between ON table and FOR EACH ROW. PG only
	// accepts this for CREATE CONSTRAINT TRIGGER; default is NOT DEFERRABLE
	// INITIALLY IMMEDIATE. (The `FROM referenced_table` clause is FK-internal and
	// not modelled here.) DU-002 slice 327.
	if isConstraint {
		p.parseConstraintDeferrable(&stmt.Deferrable, &stmt.InitDeferred)
	}

	// REFERENCING { OLD | NEW } TABLE [AS] name [ … ] — transition-table aliases
	// for an AFTER trigger (the OLD/NEW statement-level row sets). Either or both
	// clauses may appear, in either order; the AS keyword is optional. goopg
	// records the names for pg_dump fidelity only. DU-002 slice 328.
	if p.acceptIdentKeyword("referencing") {
		for {
			isOld := p.acceptIdentKeyword("old")
			isNew := false
			if !isOld {
				isNew = p.acceptIdentKeyword("new")
			}
			if !isOld && !isNew {
				return nil, p.errAtCur("expected OLD or NEW in REFERENCING clause")
			}
			if _, err := p.expectKeyword(KwTable); err != nil {
				return nil, err
			}
			_ = p.acceptKeyword(KwAs) // AS is optional
			nameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			if isOld {
				stmt.OldTransitionTable = identText(nameTok)
			} else {
				stmt.NewTransitionTable = identText(nameTok)
			}
			// Another transition-table clause may follow with no separator.
			next := p.cur()
			if next.Kind != TokenIdent ||
				(!strings.EqualFold(next.Value, "old") && !strings.EqualFold(next.Value, "new")) {
				break
			}
		}
	}

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

	// Optional WHEN ( condition ) — a boolean qualification (PG grammar:
	// `WHEN '(' a_expr ')'`) evaluated before the trigger fires, usually comparing
	// OLD.<col>/NEW.<col>. Parse it into an expression so pg_get_triggerdef can
	// re-emit it; the OLD/NEW qualifiers are preserved on the *ColumnRef. goopg
	// records it for dump fidelity only — it is not yet evaluated at firing time.
	// DU-002 slice 329.
	if p.acceptKeyword(KwWhen) {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected ( after WHEN in trigger definition")
		}
		whenExpr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.WhenExpr = whenExpr
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ) to close WHEN condition in trigger definition")
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
			// PG's TriggerFuncArg (gram.y) stores EVERY argument form as a
			// string in pg_trigger.tgargs: a string literal verbatim, an
			// integer via psprintf("%d") (canonicalised, leading zeros
			// dropped), a float by its lexeme, and a bare identifier/keyword
			// (ColLabel) by its text. pg_get_triggerdef then re-quotes them
			// all as `'…'` literals, so capturing the token text here lets
			// buildTriggerDefString round-trip `f(42, 'x', foo)` →
			// `f('42', 'x', 'foo')`. DU-002 slice 369.
			switch tok := p.cur(); tok.Kind {
			case TokenStringLit, TokenNumericLit, TokenIdent:
				stmt.FuncArgs = append(stmt.FuncArgs, tok.Value)
				p.advance()
			case TokenIntLit:
				stmt.FuncArgs = append(stmt.FuncArgs, canonicalTriggerIntArg(tok.Value))
				p.advance()
			default:
				p.advance() // skip anything unexpected
			}
			p.acceptSymbol(",")
		}
	}
	return stmt, nil
}

// canonicalTriggerIntArg mirrors PG's TriggerFuncArg integer handling
// (gram.y: `Iconst { makeString(psprintf("%d", $1)) }`): the integer is parsed
// and reprinted, so a lexeme like "0042" canonicalises to "42". If the literal
// does not fit a Go int (PG would reject it long before here), the raw lexeme is
// kept so no information is lost.
func canonicalTriggerIntArg(lexeme string) string {
	n, err := strconv.Atoi(lexeme)
	if err != nil {
		return lexeme
	}
	return strconv.Itoa(n)
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

// parseDropPolicyTail picks up after DROP POLICY.
// Grammar: [IF EXISTS] name ON table [CASCADE|RESTRICT]. DU-002 slice 323.
func (p *parser) parseDropPolicyTail(pos int) (Stmt, error) {
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
	return &DropPolicyStmt{pos: pos, Name: identText(nameTok), Table: tbl, IfExists: ifExists}, nil
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
//
// parseAlterDefaultPrivileges parses `ALTER DEFAULT PRIVILEGES
// [FOR ROLE|USER role_list] [IN SCHEMA schema_list] {GRANT|REVOKE} ...`
// (gram.y's AlterDefaultPrivilegesStmt). The "default"/"privileges" tokens
// have already been peeked (not consumed) by the caller; every remaining
// token through the trailing ';' is captured and handed to
// buildAlterDefaultPrivileges, mirroring how GRANT/REVOKE ON DATABASE/TYPE/
// PARAMETER are parsed elsewhere in this file (capture-then-postprocess,
// reusing splitTokRoles/splitTokPrivileges). M0110-0001 (DU-002 slice 438
// follow-up).
func (p *parser) parseAlterDefaultPrivileges(pos int) (Stmt, error) {
	p.advance() // "default"
	p.advance() // "privileges"
	var toks []Token
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			break
		}
		toks = append(toks, p.cur())
		p.advance()
	}
	stmt, err := buildAlterDefaultPrivileges(toks)
	if err != nil {
		return nil, err
	}
	stmt.pos = pos
	return stmt, nil
}

// parseTSTokenTypeCommaList parses a bare `tok [, tok ...]` comma-separated
// token-type list (no leading FOR keyword — the caller consumes that,
// conditionally in ALTER MAPPING's case since its REPLACE form's token-type
// list is optional). DU-002 slice 446 follow-up (M0119-0004); split out of
// parseTSMappingTokenTypeList for the replacedict follow-up.
func parseTSTokenTypeCommaList(p *parser) ([]string, error) {
	var tokenTypes []string
	for {
		tok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		tokenTypes = append(tokenTypes, strings.ToLower(identText(tok)))
		if !p.acceptSymbol(",") {
			break
		}
	}
	return tokenTypes, nil
}

// parseTSMappingTokenTypeList parses the `FOR tok [, tok ...]` token-type
// list shared by ALTER TEXT SEARCH CONFIGURATION's ADD MAPPING and DROP
// MAPPING forms. DU-002 slice 446 follow-up (M0119-0004).
func parseTSMappingTokenTypeList(p *parser) ([]string, error) {
	if _, err := p.expectKeyword(KwFor); err != nil {
		return nil, err
	}
	return parseTSTokenTypeCommaList(p)
}

// parseAlterTSDictOptionList parses ALTER TEXT SEARCH DICTIONARY name's
// `( key [= value] [, ...] )` option list (gram.y's `definition`
// production), assuming the caller has already confirmed the current token
// is "(". A bare `key` (no `= value`) sets HasValue=false on its
// TSDictOption — a delete-only directive, mirroring
// AlterTSDictionary's `if (defel->arg)` check (tsearchcmds.c). Reuses the
// same token-scanning shape as CREATE TEXT SEARCH DICTIONARY's option-list
// parse (ddl.go's `text search dictionary` CREATE-tail), which never
// records a bare option at all since ALTER is the only form that needs
// delete-only semantics. DU-002 ALTER TEXT SEARCH DICTIONARY follow-up
// (M0119-0004).
func parseAlterTSDictOptionList(p *parser) ([]TSDictOption, error) {
	p.advance() // consume "("
	var opts []TSDictOption
	for {
		c := p.cur()
		if c.Kind == TokenEOF || (c.Kind == TokenSymbol && c.Value == ")") {
			break
		}
		if c.Kind == TokenSymbol && c.Value == "," {
			p.advance()
			continue
		}
		if c.Kind != TokenIdent && c.Kind != TokenKeyword {
			p.advance()
			continue
		}
		key := strings.ToLower(c.Value)
		p.advance()
		if !((p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=") {
			opts = append(opts, TSDictOption{Key: key})
			continue
		}
		p.advance() // consume '='
		v := p.cur()
		switch v.Kind {
		case TokenIntLit, TokenNumericLit:
			opts = append(opts, TSDictOption{Key: key, Value: v.Value, IsNumeric: true, HasValue: true})
			p.advance()
		case TokenStringLit, TokenIdent, TokenKeyword:
			opts = append(opts, TSDictOption{Key: key, Value: v.Value, HasValue: true})
			p.advance()
		default:
			opts = append(opts, TSDictOption{Key: key})
		}
	}
	if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
		p.advance()
	}
	return opts, nil
}

func (p *parser) parseAlter() (Stmt, error) {
	t, err := p.expectKeyword(KwAlter)
	if err != nil {
		return nil, err
	}
	// ALTER DEFAULT PRIVILEGES [FOR ROLE|USER ...] [IN SCHEMA ...]
	// {GRANT|REVOKE} ... — a distinct top-level form (gram.y's
	// AlterDefaultPrivilegesStmt), not a relation/sequence/etc ALTER, so it is
	// detected and dispatched before any of the object-keyword branches
	// below. M0110-0001 (DU-002 slice 438 follow-up).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault &&
		strings.EqualFold(p.peek(1).Value, "privileges") {
		return p.parseAlterDefaultPrivileges(t.Pos)
	}
	// ALTER TEXT SEARCH CONFIGURATION name {ADD MAPPING|DROP MAPPING|RENAME
	// TO|SET SCHEMA|ALTER MAPPING REPLACE} — the ALTER TEXT SEARCH * forms
	// goopg actually applies (ADD/DROP MAPPING is how a config's
	// pg_ts_config_map rows get populated/removed, which dumpTSConfig's own
	// ADD MAPPING re-emission depends on). The ALTER MAPPING FOR tok WITH
	// dict override form (no REPLACE), OWNER TO, and DICTIONARY/PARSER/
	// TEMPLATE entirely fall through to the generic skip-to-semicolon compat
	// no-op below, matching CREATE TEXT SEARCH's existing pattern. DU-002
	// slice 446 (M0119-0004); RENAME TO/SET SCHEMA/DROP MAPPING added as a
	// slice 446 follow-up; ALTER MAPPING REPLACE added as a further
	// follow-up.
	if p.acceptIdentKeyword("text") {
		_ = p.acceptIdentKeyword("search") // consume "search"
		if p.acceptIdentKeyword("configuration") {
			cfgName, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			if p.acceptKeyword(KwAdd) && p.acceptIdentKeyword("mapping") {
				tokenTypes, err := parseTSMappingTokenTypeList(p)
				if err != nil {
					return nil, err
				}
				if _, err := p.expectKeyword(KwWith); err != nil {
					return nil, err
				}
				var dicts []ObjectName
				for {
					dn, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					dicts = append(dicts, dn)
					if !p.acceptSymbol(",") {
						break
					}
				}
				return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "addmapping", TokenTypes: tokenTypes, Dictionaries: dicts}, nil
			}
			if p.acceptKeyword(KwDrop) && p.acceptIdentKeyword("mapping") {
				ifExists := false
				if p.acceptKeyword(KwIf) {
					if _, err := p.expectKeyword(KwExists); err != nil {
						return nil, err
					}
					ifExists = true
				}
				tokenTypes, err := parseTSMappingTokenTypeList(p)
				if err != nil {
					return nil, err
				}
				return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "dropmapping", TokenTypes: tokenTypes, IfExists: ifExists}, nil
			}
			if p.acceptIdentKeyword("rename") {
				if _, err := p.expectKeyword(KwTo); err != nil {
					return nil, err
				}
				newNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "rename", NewName: identText(newNameTok)}, nil
			}
			if (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
				p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
				p.advance() // SET
				p.advance() // SCHEMA
				schemaTok := p.cur()
				p.advance()
				return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "setschema", NewSchema: identText(schemaTok)}, nil
			}
			if p.acceptKeyword(KwAlter) && p.acceptIdentKeyword("mapping") {
				// ALTER MAPPING [FOR tok [, ...]] REPLACE olddict WITH
				// newdict — ALTER_TSCONFIG_REPLACE_DICT(_FOR_TOKEN) in
				// gram.y. The FOR token-type list is optional (its absence
				// means "replace across every mapped token type"), which is
				// why it can't reuse parseTSMappingTokenTypeList directly
				// (that helper requires FOR). The sibling
				// ALTER_TSCONFIG_ALTER_MAPPING_FOR_TOKEN form (`FOR tok
				// WITH dict [, ...]`, no REPLACE) stays an unimplemented
				// no-op, falling through below.
				var tokenTypes []string
				if p.acceptKeyword(KwFor) {
					tt, err := parseTSTokenTypeCommaList(p)
					if err != nil {
						return nil, err
					}
					tokenTypes = tt
				}
				if p.acceptKeyword(KwReplace) {
					oldDict, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					if _, err := p.expectKeyword(KwWith); err != nil {
						return nil, err
					}
					newDict, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "replacedict", TokenTypes: tokenTypes, OldDict: oldDict, NewDict: newDict}, nil
				}
				// ALTER MAPPING FOR tok [, ...] WITH dict [, ...] override
				// form (ALTER_TSCONFIG_ALTER_MAPPING_FOR_TOKEN) — replaces
				// each named token type's entire dictionary list. Unlike
				// REPLACE, gram.y requires the FOR token-type list for this
				// form (there is no bare "ALTER MAPPING WITH dict" rule), so
				// only dispatch here when tokenTypes was actually parsed.
				// DU-002 slice 446 follow-up (M0119-0004).
				if len(tokenTypes) > 0 && p.acceptKeyword(KwWith) {
					var dicts []ObjectName
					for {
						dn, err := p.parseObjectName()
						if err != nil {
							return nil, err
						}
						dicts = append(dicts, dn)
						if !p.acceptSymbol(",") {
							break
						}
					}
					return &AlterTSConfigStmt{pos: t.Pos, ConfigName: cfgName, Action: "altermapping", TokenTypes: tokenTypes, Dictionaries: dicts}, nil
				}
				// OWNER TO (or any other unrecognized trailer) — unimplemented
				// compat no-op; discard the rest of the statement.
				stmt, err := p.parseSkipToSemicolon(t.Pos)
				if err != nil {
					return nil, err
				}
				if ns, ok := stmt.(*CompatNoopStmt); ok {
					ns.Tag = "ALTER TEXT SEARCH CONFIGURATION"
					ns.ObjType = "text search configuration"
					ns.ObjName = cfgName
				}
				return stmt, nil
			}
			// OWNER TO — unimplemented compat no-op; discard the rest of
			// the statement.
			stmt, err := p.parseSkipToSemicolon(t.Pos)
			if err != nil {
				return nil, err
			}
			if ns, ok := stmt.(*CompatNoopStmt); ok {
				ns.Tag = "ALTER TEXT SEARCH CONFIGURATION"
				ns.ObjType = "text search configuration"
				ns.ObjName = cfgName
			}
			return stmt, nil
		}
		// ALTER TEXT SEARCH DICTIONARY name {RENAME TO|SET SCHEMA|( key
		// [= value] [, ...] )} — DU-002 ALTER TEXT SEARCH DICTIONARY
		// follow-up (M0119-0004). OWNER TO and PARSER/TEMPLATE fall through
		// to the generic skip-to-semicolon compat no-op below, matching the
		// ALTER TEXT SEARCH CONFIGURATION precedent.
		if p.acceptIdentKeyword("dictionary") {
			dictName, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			if p.acceptIdentKeyword("rename") {
				if _, err := p.expectKeyword(KwTo); err != nil {
					return nil, err
				}
				newNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				return &AlterTSDictStmt{pos: t.Pos, DictName: dictName, Action: "rename", NewName: identText(newNameTok)}, nil
			}
			if (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
				p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
				p.advance() // SET
				p.advance() // SCHEMA
				schemaTok := p.cur()
				p.advance()
				return &AlterTSDictStmt{pos: t.Pos, DictName: dictName, Action: "setschema", NewSchema: identText(schemaTok)}, nil
			}
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				opts, err := parseAlterTSDictOptionList(p)
				if err != nil {
					return nil, err
				}
				return &AlterTSDictStmt{pos: t.Pos, DictName: dictName, Action: "options", Options: opts}, nil
			}
			// OWNER TO (or any other unrecognized trailer) — unimplemented
			// compat no-op; discard the rest of the statement.
			stmt, err := p.parseSkipToSemicolon(t.Pos)
			if err != nil {
				return nil, err
			}
			if ns, ok := stmt.(*CompatNoopStmt); ok {
				ns.Tag = "ALTER TEXT SEARCH DICTIONARY"
				ns.ObjType = "text search dictionary"
				ns.ObjName = dictName
			}
			return stmt, nil
		}
		// ALTER TEXT SEARCH PARSER|TEMPLATE — unimplemented compat no-op.
		stmt, err := p.parseSkipToSemicolon(t.Pos)
		if err != nil {
			return nil, err
		}
		return stmt, nil
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
		// ALTER SEQUENCE name RENAME TO / OWNER TO / SET SCHEMA — distinct
		// top-level forms in PG's grammar (RenameStmt / AlterOwnerStmt /
		// AlterObjectSchemaStmt), not combinable with the SeqOptList options
		// parsed below, so detect and short-circuit before that loop. All
		// three reuse the generic relation executor (execAlterTable) via
		// AlterTableStmt — a sequence is just a relation (relkind='S') and
		// execAlterTable's AlterTableRenameTable case already cascades into
		// the sequence registry (tbl.IsSequence). Previously these three
		// forms had no case at all here, so the leftover RENAME/OWNER/SET
		// token surfaced as a bare syntax error. DU-002 slice 439.
		if p.acceptIdentKeyword("rename") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			renameStmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: stmt.IfExists, TagOverride: "ALTER SEQUENCE"}
			renameStmt.Actions = append(renameStmt.Actions, AlterTableAction{
				pos:     newNameTok.Pos,
				Kind:    AlterTableRenameTable,
				NewName: identText(newNameTok),
			})
			return renameStmt, nil
		}
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			ownerStmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: stmt.IfExists, TagOverride: "ALTER SEQUENCE"}
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the
			// bootstrap superuser in goopg (mirrors ALTER TABLE OWNER TO).
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				ownerStmt.OwnerTo = ""
			} else if tok, err := p.parseIdent(); err == nil {
				ownerStmt.OwnerTo = identText(tok)
			}
			if ownerStmt.OwnerTo == "" {
				ownerStmt.OwnerTo = "current_user"
			}
			return ownerStmt, nil
		}
		if (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
			p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
			p.advance() // SET
			p.advance() // SCHEMA
			schemaTok := p.cur()
			p.advance()
			return &AlterTableStmt{pos: t.Pos, Name: name, IfExists: stmt.IfExists, SetSchema: identText(schemaTok), TagOverride: "ALTER SEQUENCE"}, nil
		}
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
	// ALTER STATISTICS name SET STATISTICS n — set the extended-statistics
	// sample target (pg_statistic_ext.stxstattarget); round-trips through
	// pg_dump's dumpStatisticsExt. DU-002 slice 317. RENAME TO / OWNER TO /
	// SET SCHEMA — distinct top-level forms (RenameStmt/AlterOwnerStmt/
	// AlterObjectSchemaStmt in PG's grammar) — actually move/rename/
	// re-own the object (catalog.RenameStatisticsObject/SetStatisticsOwner/
	// SetStatisticsSchema). DU-002 slice 441.
	if p.acceptIdentKeyword("statistics") {
		stmt := &AlterStatisticsStmt{pos: t.Pos}
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
		if p.acceptIdentKeyword("rename") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Action = "rename"
			stmt.NewName = identText(newNameTok)
			return stmt, nil
		}
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			stmt.Action = "owner"
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				stmt.NewOwner = "current_user"
			} else {
				tok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				stmt.NewOwner = identText(tok)
			}
			return stmt, nil
		}
		// SET SCHEMA / SET STATISTICS n both start with the same SET token
		// (KwSet or the bare ident); branch on the following keyword.
		if p.acceptKeyword(KwSet) || p.acceptIdentKeyword("set") {
			if p.acceptIdentKeyword("schema") {
				schemaTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				stmt.Action = "setschema"
				stmt.NewSchema = identText(schemaTok)
				return stmt, nil
			}
			if p.acceptIdentKeyword("statistics") {
				neg := false
				if (p.cur().Kind == TokenOperator || p.cur().Kind == TokenSymbol) && p.cur().Value == "-" {
					neg = true
					p.advance()
				}
				if p.cur().Kind == TokenIntLit {
					n, err := strconv.Atoi(p.cur().Value)
					if err != nil {
						return nil, err
					}
					p.advance()
					if neg {
						n = -n
					}
					stmt.Target = n
					stmt.HasTarget = true
				}
			}
		}
		// Consume any trailing tokens of an unmodelled form up to the terminator.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
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
		// Check for OWNER TO new_owner. M0119-0004 (DU-002, loop #57 follow-up).
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			stmt := &AlterAggregateOwnerStmt{pos: t.Pos, Name: name}
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
			// superuser sentinel, mirroring ALTER COLLATION … OWNER TO.
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				stmt.NewOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				stmt.NewOwner = identText(tok)
			} else {
				stmt.NewOwner = "current_user"
			}
			return stmt, nil
		}
		// Other ALTER AGGREGATE forms: consume as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER AGGREGATE"}, nil
	}
	// ALTER COLLATION [IF EXISTS] name RENAME TO newname | OWNER TO role |
	// REFRESH VERSION. M0119-0004 (DU-002, loop #50 ledger follow-up).
	if p.acceptIdentKeyword("collation") {
		stmt := &AlterCollationStmt{pos: t.Pos}
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
		switch {
		case p.acceptIdentKeyword("rename"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Action = "rename"
			stmt.NewName = identText(newNameTok)
		case p.acceptIdentKeyword("owner"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			stmt.Action = "owner"
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
			// superuser sentinel, mirroring ALTER TABLE … OWNER TO.
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				stmt.NewOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				stmt.NewOwner = identText(tok)
			} else {
				stmt.NewOwner = "current_user"
			}
		case p.acceptIdentKeyword("refresh"):
			_ = p.acceptIdentKeyword("version")
			stmt.Action = "refresh"
		case (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
			p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema"):
			// SET SCHEMA newschema — DU-002 slice 442, closes the last
			// unmodelled ALTER COLLATION form (RENAME TO / OWNER TO were
			// already dedicated cases; only this one fell to the no-op
			// default below).
			p.advance() // SET
			p.advance() // SCHEMA
			schemaTok := p.cur()
			p.advance()
			stmt.Action = "setschema"
			stmt.NewSchema = identText(schemaTok)
		default:
			// Unmodelled form — consume as a no-op.
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
		}
		return stmt, nil
	}
	// ALTER CONVERSION [IF EXISTS] name RENAME TO newname | OWNER TO role |
	// SET SCHEMA newschema. Mirrors ALTER COLLATION above (minus REFRESH
	// VERSION, which is collation-specific). M0122-0007 4e follow-up (DU-002
	// round-trip probe unblock).
	if p.acceptIdentKeyword("conversion") {
		stmt := &AlterConversionStmt{pos: t.Pos}
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
		switch {
		case p.acceptIdentKeyword("rename"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Action = "rename"
			stmt.NewName = identText(newNameTok)
		case p.acceptIdentKeyword("owner"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			stmt.Action = "owner"
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
			// superuser sentinel, mirroring ALTER COLLATION … OWNER TO.
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				stmt.NewOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				stmt.NewOwner = identText(tok)
			} else {
				stmt.NewOwner = "current_user"
			}
		case (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
			p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema"):
			// SET SCHEMA newschema, mirroring ALTER COLLATION's slice-442 case.
			p.advance() // SET
			p.advance() // SCHEMA
			schemaTok := p.cur()
			p.advance()
			stmt.Action = "setschema"
			stmt.NewSchema = identText(schemaTok)
		default:
			// Unmodelled form — consume as a no-op.
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
		}
		return stmt, nil
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
		// ALTER INDEX name SET TABLESPACE tablespace_name — catalog metadata
		// only, no physical relocation. Checked before the generic reloptions
		// SET below since that branch unconditionally expects `(`. M0122-0007.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet &&
			p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwTablespace {
			p.advance() // SET
			p.advance() // TABLESPACE
			tsTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt := &AlterTableStmt{pos: t.Pos, Name: idxName, TagOverride: "ALTER INDEX"}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				pos:            tsTok.Pos,
				Kind:           AlterTableSetTablespace,
				TablespaceName: identText(tsTok),
			})
			return stmt, nil
		}
		// ALTER INDEX name SET (param = value, …) — index storage parameters.
		// Only GIN fastupdate is acted on (drives predicate-gin SSI granularity,
		// design 0118-0140); other options round-trip as accepted no-ops.
		if p.acceptKeyword(KwSet) {
			opts, err := p.parseWithOptions()
			if err != nil {
				return nil, err
			}
			stmt := &AlterTableStmt{pos: t.Pos, Name: idxName}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				Kind: AlterIndexSetReloptions,
				With: opts,
			})
			return stmt, nil
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
		// ALTER INDEX name RENAME TO newname — share the executor's
		// AlterTableRenameTable path (carries Name so a synthetic pg_toast index
		// rename can be intercepted). Previously fell into the no-op branch below
		// with an empty Name, so the rename was silently dropped. M0118-0008
		// TOAST-exposure slice 4 (design 0118-0087).
		if p.acceptIdentKeyword("rename") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt := &AlterTableStmt{pos: t.Pos, Name: idxName, TagOverride: "ALTER INDEX"}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				pos:     newNameTok.Pos,
				Kind:    AlterTableRenameTable,
				NewName: identText(newNameTok),
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
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER INDEX"}, nil
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
			(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet) ||
			(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReset) {
			// OWNER TO new_owner — store the resolved owner in stmt. M0097-0150.
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "owner") {
				p.advance() // OWNER
				p.acceptKeyword(KwTo)
				// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the
				// bootstrap superuser sentinel, mirroring ALTER AGGREGATE/
				// COLLATION ... OWNER TO.
				if p.acceptIdentKeyword("current_user") ||
					p.acceptIdentKeyword("session_user") ||
					p.acceptIdentKeyword("current_role") {
					stmt.NewOwner = "current_user"
				} else if tok, err := p.parseIdent(); err == nil {
					stmt.NewOwner = identText(tok)
				} else {
					stmt.NewOwner = "current_user"
				}
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
			// SET SCHEMA schema — store the new schema in stmt (M0097-0150).
			// SET guc_name {TO|=} value | SET guc_name FROM CURRENT are
			// captured into stmt.ConfigOps for pg_proc.proconfig (DU-002
			// follow-up). SET itself is a real keyword token (KwSet), not an
			// ident — as is FROM (KwFrom) below; both were previously matched
			// against TokenIdent, so this whole branch was unreachable dead
			// code (any `ALTER FUNCTION ... SET ...` form hit a syntax error
			// instead of ever reaching here).
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
				p.advance() // SET
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "schema") {
					p.advance() // SCHEMA
					if schemaTok, err := p.parseIdent(); err == nil {
						stmt.NewSchema = identText(schemaTok)
					}
				} else {
					op, ok, err := p.parseFunctionConfigSetClause()
					if err != nil {
						return nil, err
					}
					if ok {
						stmt.ConfigOps = append(stmt.ConfigOps, op)
					}
				}
				continue
			}
			// RESET guc_name | RESET ALL — captured into stmt.ConfigOps.
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReset {
				p.advance() // RESET
				stmt.ConfigOps = append(stmt.ConfigOps, p.parseFunctionConfigResetClause())
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
				return &AlterTableStmt{pos: t.Pos, Name: mvName, SetSchema: schemaName, TagOverride: "ALTER MATERIALIZED VIEW"}, nil
			}
		}
		// Other ALTER MATERIALIZED VIEW actions — consume until ';'.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER MATERIALIZED VIEW"}, nil
	}
	// ALTER VIEW name RENAME TO / OWNER TO / SET SCHEMA — same treatment as
	// ALTER SEQUENCE (DU-002 slice 439): a view is just a relation
	// (pg_class.relkind='v'), and real PostgreSQL backs all three with the
	// same generic commands (RenameRelation/AlterTableOwner/
	// AlterTableNamespace, postgres/src/backend/commands/tablecmds.c), so
	// they reuse the generic relation executor (execAlterTable) exactly like
	// the materialized-view SET SCHEMA case above. Previously ALTER VIEW had
	// no dedicated case at all here, so it fell into the blanket
	// "schema/view/collation/..." compat-stub loop below, which silently
	// consumed and discarded RENAME/OWNER/SET SCHEMA — a functional no-op,
	// not merely a mistagging bug. DU-002 slice 440.
	if p.acceptKeyword(KwView) {
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
		if p.acceptIdentKeyword("rename") {
			if p.acceptKeyword(KwTo) {
				newNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				renameStmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
				renameStmt.Actions = append(renameStmt.Actions, AlterTableAction{
					pos:     newNameTok.Pos,
					Kind:    AlterTableRenameTable,
					NewName: identText(newNameTok),
				})
				return renameStmt, nil
			}
			// RENAME [COLUMN] old_name TO new_name — COLUMN is optional in PG's
			// grammar (ATT_VIEW like ATT_TABLE). Previously the `&&
			// p.acceptKeyword(KwTo)` short-circuit above already consumed the
			// "rename" token and then fell through to the catch-all no-op loop
			// below on any non-RENAME-TO form, so this was a silent no-op
			// exactly like the pre-slice-440 RENAME TO/OWNER TO/SET SCHEMA gap.
			// DU-002 slice 444 (closes the slice-440 ledger row's resume point).
			_ = p.acceptKeyword(KwColumn)
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
			renameColStmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
			renameColStmt.Actions = append(renameColStmt.Actions, AlterTableAction{
				pos:           oldNameTok.Pos,
				Kind:          AlterTableRenameColumn,
				OldColumnName: identText(oldNameTok),
				NewName:       identText(newNameTok),
			})
			return renameColStmt, nil
		}
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			ownerStmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the
			// bootstrap superuser in goopg (mirrors ALTER SEQUENCE/TABLE OWNER TO).
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				ownerStmt.OwnerTo = ""
			} else if tok, err := p.parseIdent(); err == nil {
				ownerStmt.OwnerTo = identText(tok)
			}
			if ownerStmt.OwnerTo == "" {
				ownerStmt.OwnerTo = "current_user"
			}
			return ownerStmt, nil
		}
		if (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet || p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "set")) &&
			p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "schema") {
			p.advance() // SET
			p.advance() // SCHEMA
			schemaTok := p.cur()
			p.advance()
			return &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, SetSchema: identText(schemaTok), TagOverride: "ALTER VIEW"}, nil
		}
		// SET (view_option = value, ...) / RESET (view_option, ...) — view-level
		// storage-parameter reloptions (e.g. `security_barrier`,
		// `check_option`). Reuses the same AlterTableSetReloptions/
		// AlterTableResetReloptions actions ALTER TABLE's own SET/RESET form
		// produces (both are relations, execAlterTable's dispatch is generic
		// over tbl.Reloptions). Checked after SET SCHEMA (which matches on the
		// "schema" identifier, not "(") so the two forms never collide. DU-002
		// slice 444.
		if cur := p.cur(); isAlterReloptVerb(cur) && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			reset := cur.Kind == TokenKeyword && cur.Keyword == KwReset ||
				cur.Kind == TokenIdent && strings.EqualFold(cur.Value, "reset")
			p.advance() // SET or RESET
			opts, err := p.parseWithOptions()
			if err != nil {
				return nil, err
			}
			kind := AlterTableSetReloptions
			if reset {
				kind = AlterTableResetReloptions
			}
			stmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
			stmt.Actions = append(stmt.Actions, AlterTableAction{pos: t.Pos, Kind: kind, With: opts})
			return stmt, nil
		}
		// ALTER [COLUMN] col SET DEFAULT expr / DROP DEFAULT — PG's
		// updatable-view column-default form (backs an INSERT/UPDATE through
		// the view when the target column is omitted). Reuses the identical
		// AlterTableSetDefault/AlterTableDropDefault actions ALTER TABLE's own
		// ALTER COLUMN form produces; execAlterTable's dispatch mutates
		// tbl.Columns[i].DefaultExpr generically regardless of relkind. DU-002
		// slice 444.
		if p.acceptKeyword(KwAlter) {
			_ = p.acceptKeyword(KwColumn)
			colTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			colName := identText(colTok)
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet {
				p.advance() // SET
				if _, err := p.expectKeyword(KwDefault); err != nil {
					return nil, err
				}
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				stmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					pos:         colTok.Pos,
					Kind:        AlterTableSetDefault,
					ColumnName:  colName,
					DefaultExpr: expr,
				})
				return stmt, nil
			}
			if p.acceptKeyword(KwDrop) {
				if _, err := p.expectKeyword(KwDefault); err != nil {
					return nil, err
				}
				stmt := &AlterTableStmt{pos: t.Pos, Name: name, IfExists: ifExists, TagOverride: "ALTER VIEW"}
				stmt.Actions = append(stmt.Actions, AlterTableAction{
					pos:        colTok.Pos,
					Kind:       AlterTableDropDefault,
					ColumnName: colName,
				})
				return stmt, nil
			}
			// Unrecognized ALTER COLUMN form on a view — consume as a no-op.
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER VIEW"}, nil
		}
		// Other ALTER VIEW forms not yet modeled — consume as a no-op like the
		// pre-existing compat stub did for everything (see deferral ledger).
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER VIEW"}, nil
	}
	// ALTER OPERATOR name (left_type, right_type) SET (option = value, ...) —
	// PG's post-creation attribute-edit form (AlterOperator, operatorcmds.c).
	// Checked before the generic operator compat-stub loop below (which would
	// otherwise silently swallow this form as a no-op), mirroring ALTER
	// COLLATION's dedicated branch above. ALTER OPERATOR CLASS|FAMILY are a
	// different object type (guarded out below); OWNER TO / SET SCHEMA on a
	// plain operator, or any other trailing form, fall back to the same
	// consume-and-succeed no-op the generic stub loop used to give this whole
	// statement — goopg does not yet track per-operator ownership/namespace
	// changes at ALTER time (only at CREATE, via UserOperator.Owner/
	// NamespaceOID) — so only the SET (...) def-list form is modeled here.
	// M0119-0004 (DU-002), closes the slice-407 ledger follow-up.
	if p.acceptIdentKeyword("operator") {
		if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "family") {
			p.advance() // consume "family"
			return p.parseAlterOpFamilyTail(t.Pos)
		}
		if tok := p.cur(); tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "class") {
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER OPERATOR CLASS"}, nil
		}
		opName, err := p.parseOperatorName()
		if err != nil {
			return nil, err
		}
		parseArgType := func() string {
			if p.acceptIdentKeyword("none") {
				return ""
			}
			tok, err := p.parseIdent()
			if err != nil {
				return ""
			}
			typeName := tok.Value
			if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
				p.advance()
				if tok2, err2 := p.parseIdent(); err2 == nil {
					typeName += "." + tok2.Value
				}
			}
			return typeName
		}
		var leftType, rightType string
		if p.acceptSymbol("(") {
			leftType = parseArgType()
			if p.acceptSymbol(",") {
				rightType = parseArgType()
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' after operator argument types")
			}
		}
		// Only `SET ( option = value, ... )` — the def-list form — is modeled.
		// `SET SCHEMA name`, `OWNER TO role`, or any other/unrecognized tail is
		// consumed as a no-op, preserving the prior stub's always-succeeds
		// behaviour for those forms.
		if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet &&
			p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(") {
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER OPERATOR"}, nil
		}
		p.advance() // SET
		p.advance() // '('
		stmt := &AlterOperatorSetStmt{pos: t.Pos, Name: opName, LeftType: leftType, RightType: rightType}
		for {
			if p.cur().Kind != TokenIdent && p.cur().Kind != TokenKeyword {
				return nil, p.errAtCur("expected option name in ALTER OPERATOR SET list")
			}
			key := strings.ToLower(p.cur().Value)
			p.advance()
			hasVal := false
			if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
				p.advance()
				hasVal = true
			}
			switch key {
			case "restrict":
				stmt.RestrictSet = true
				if hasVal && !p.acceptIdentKeyword("none") {
					if fn, ferr := p.parseObjectName(); ferr == nil {
						stmt.Restrict = fn
					}
				}
			case "join":
				stmt.JoinSet = true
				if hasVal && !p.acceptIdentKeyword("none") {
					if fn, ferr := p.parseObjectName(); ferr == nil {
						stmt.Join = fn
					}
				}
			case "commutator":
				stmt.CommutatorSet = true
				if hasVal {
					if ref, oerr := p.parseOperatorRefName(); oerr == nil {
						stmt.Commutator = ref
					}
				}
			case "negator":
				stmt.NegatorSet = true
				if hasVal {
					if ref, oerr := p.parseOperatorRefName(); oerr == nil {
						stmt.Negator = ref
					}
				}
			case "merges", "hashes":
				val := true
				if hasVal {
					if p.cur().Kind == TokenKeyword && strings.EqualFold(p.cur().Value, "false") {
						val = false
					}
					if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
						p.advance()
					}
				}
				if key == "merges" {
					stmt.Merges = &val
				} else {
					stmt.Hashes = &val
				}
			case "leftarg", "rightarg", "function", "procedure":
				return nil, p.errAtCur("operator attribute \"" + key + "\" cannot be changed")
			default:
				return nil, p.errAtCur("operator attribute \"" + key + "\" not recognized")
			}
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close ALTER OPERATOR SET list")
		}
		return stmt, nil
	}
	// ALTER PUBLICATION/SUBSCRIPTION name OWNER TO ... — the only ALTER
	// PUBLICATION/SUBSCRIPTION form goopg models; every other tail (RENAME TO,
	// SET, ADD/DROP/SET TABLE, SKIP, ...) drains to the statement end as a
	// no-op, matching the generic compatibility-stub loop below. Publication/
	// subscription names are unqualified idents (CreatePublicationStmt.Name/
	// CreateSubscriptionStmt.Name), unlike the schema-qualified ObjectName
	// ALTER COLLATION/AGGREGATE use, so this is handled as its own case
	// instead of falling into that generic loop. M0119-0004 (DU-002, loop #65
	// ledger follow-up).
	for _, pubSubKind := range []Keyword{KwPublication, KwSubscription} {
		// "publication"/"subscription" are registered keywords (used by
		// CREATE SUBSCRIPTION's PUBLICATION clause), so they arrive as
		// TokenKeyword, not TokenIdent — acceptIdentKeyword (which requires
		// TokenIdent) can never match them; that's what made this whole
		// case unreachable before this fix (loop #65 ledger follow-up).
		if !p.acceptKeyword(pubSubKind) {
			continue
		}
		if p.cur().Kind != TokenIdent {
			return nil, p.errAtCur("expected " + string(pubSubKind) + " name")
		}
		name := p.cur().Value
		p.advance()
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			var newOwner string
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
			// superuser sentinel, mirroring ALTER COLLATION … OWNER TO.
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				newOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				newOwner = identText(tok)
			} else {
				newOwner = "current_user"
			}
			if pubSubKind == KwPublication {
				return &AlterPublicationOwnerStmt{pos: t.Pos, Name: name, NewOwner: newOwner}, nil
			}
			return &AlterSubscriptionOwnerStmt{pos: t.Pos, Name: name, NewOwner: newOwner}, nil
		}
		// Other ALTER PUBLICATION/SUBSCRIPTION forms: consume as no-op.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		tag := "ALTER PUBLICATION"
		if pubSubKind == KwSubscription {
			tag = "ALTER SUBSCRIPTION"
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: tag}, nil
	}
	// ALTER EVENT TRIGGER name {DISABLE | ENABLE [REPLICA|ALWAYS] | RENAME TO
	// newname | OWNER TO newowner} — the only ALTER EVENT TRIGGER forms goopg
	// models. "event" is an ident-keyword (like DROP EVENT TRIGGER's), TRIGGER
	// is a registered keyword token, so this is handled explicitly rather than
	// falling into the generic ident-based compat-stub loop below. DU-002
	// (M0119-0004, loop #69 ledger follow-up).
	if p.acceptIdentKeyword("event") {
		if _, err := p.expectKeyword(KwTrigger); err != nil {
			return nil, err
		}
		if p.cur().Kind != TokenIdent {
			return nil, p.errAtCur("expected event trigger name")
		}
		name := p.cur().Value
		p.advance()
		switch {
		case p.acceptIdentKeyword("disable"):
			return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "disable"}, nil
		case p.acceptIdentKeyword("enable"):
			switch {
			case p.acceptIdentKeyword("replica"):
				return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "enable_replica"}, nil
			case p.acceptIdentKeyword("always"):
				return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "enable_always"}, nil
			default:
				return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "enable"}, nil
			}
		case p.acceptIdentKeyword("rename"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newName, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "rename", NewName: identText(newName)}, nil
		case p.acceptIdentKeyword("owner"):
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			var newOwner string
			// CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the bootstrap
			// superuser sentinel, mirroring ALTER PUBLICATION/SUBSCRIPTION OWNER TO.
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				newOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				newOwner = identText(tok)
			} else {
				newOwner = "current_user"
			}
			return &AlterEventTriggerStmt{pos: t.Pos, Name: name, Action: "owner", NewOwner: newOwner}, nil
		}
		return nil, p.errAtCur("expected DISABLE, ENABLE, RENAME TO, or OWNER TO in ALTER EVENT TRIGGER")
	}
	// ALTER SCHEMA name RENAME TO newname / ALTER SCHEMA name OWNER TO role —
	// real PostgreSQL's only two ALTER SCHEMA forms (schemacmds.c's
	// RenameSchema/AlterSchemaOwner). Previously "schema" fell into the
	// generic compat-stub loop below, which silently consumed and discarded
	// both forms — a functional no-op, not merely a mistagging bug, the same
	// class of gap ALTER VIEW had before DU-002 slice 440. DU-002 slice 440
	// resume point (3) (M0110-0001).
	if p.acceptIdentKeyword("schema") {
		nameTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		name := identText(nameTok)
		if p.acceptIdentKeyword("rename") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			return &AlterSchemaStmt{pos: t.Pos, Name: name, Action: "rename", NewName: identText(newNameTok)}, nil
		}
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			ownerStmt := &AlterSchemaStmt{pos: t.Pos, Name: name, Action: "owner"}
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				ownerStmt.NewOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				ownerStmt.NewOwner = identText(tok)
			} else {
				ownerStmt.NewOwner = "current_user"
			}
			return ownerStmt, nil
		}
		// Unmodelled ALTER SCHEMA form — consume to the terminator and
		// return a no-op, mirroring the compat-stub loop's prior behavior.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER SCHEMA"}, nil
	}
	// ALTER DOMAIN name RENAME TO newname / OWNER TO role / RENAME CONSTRAINT
	// old TO new / ADD-DROP CONSTRAINT / SET-DROP DEFAULT / SET-DROP NOT
	// NULL — the ALTER DOMAIN forms goopg models so far. Previously "domain"
	// fell entirely into the generic collation/domain/extension/... compat-
	// stub loop below, a silent no-op for every form including these — the
	// same class of gap ALTER SCHEMA's dedicated case (immediately above)
	// closed for schemas. Only SET SCHEMA still falls through to a no-op
	// below. M0122-0005 (domain follow-up); RENAME CONSTRAINT/ADD-DROP
	// CONSTRAINT/SET-DROP DEFAULT/SET-DROP NOT NULL added in later
	// follow-ups (real PG's gram.y models RENAME CONSTRAINT as a RenameStmt
	// with renameType == OBJECT_DOMCONSTRAINT, not part of AlterDomainStmt,
	// but goopg groups every ALTER DOMAIN form under one AST node).
	if p.acceptIdentKeyword("domain") {
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		if p.acceptIdentKeyword("rename") {
			if p.acceptKeyword(KwConstraint) {
				oldNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				if _, err := p.expectKeyword(KwTo); err != nil {
					return nil, err
				}
				newConNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "renameconstraint", ConstraintName: identText(oldNameTok), NewConstraintName: identText(newConNameTok)}, nil
			}
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			newNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "rename", NewName: identText(newNameTok)}, nil
		}
		if p.acceptIdentKeyword("owner") {
			if _, err := p.expectKeyword(KwTo); err != nil {
				return nil, err
			}
			ownerStmt := &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "owner"}
			if p.acceptIdentKeyword("current_user") ||
				p.acceptIdentKeyword("session_user") ||
				p.acceptIdentKeyword("current_role") {
				ownerStmt.NewOwner = "current_user"
			} else if tok, err := p.parseIdent(); err == nil {
				ownerStmt.NewOwner = identText(tok)
			} else {
				ownerStmt.NewOwner = "current_user"
			}
			return ownerStmt, nil
		}
		if p.acceptKeyword(KwAdd) {
			// ALTER DOMAIN name ADD [CONSTRAINT constraint_name] CHECK (expr)
			// [NOT VALID] — the only DomainConstraintElem real PG's grammar
			// allows (CHECK; a domain NOT NULL constraint is set via SET NOT
			// NULL, not ADD). Reuses tryParseCheckInValues/parseDomainCheckExpr,
			// the same helpers CREATE DOMAIN's CHECK clause uses, so the stored
			// Expr/InValues shape matches exactly. M0122-0005 domain follow-up.
			var cname string
			if p.acceptKeyword(KwConstraint) {
				nameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				cname = identText(nameTok)
			}
			if _, err := p.expectKeyword(KwCheck); err != nil {
				return nil, err
			}
			addStmt := &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "addconstraint", ConstraintName: cname}
			if vals := p.tryParseCheckInValues(); vals != nil {
				addStmt.CheckInValues = vals
			} else {
				expr, err := p.parseDomainCheckExpr()
				if err != nil {
					return nil, err
				}
				addStmt.CheckExpr = expr
			}
			// Optional NOT VALID trailer (ConstraintAttributeSpec). Parsed and
			// discarded: goopg's DomainCheck has no convalidated flag, and
			// (unlike real PG) never scans existing column data when a CHECK is
			// added either way, NOT VALID or not — see deferral ledger.
			if p.acceptKeyword(KwNot) {
				p.acceptIdentKeyword("valid")
			}
			return addStmt, nil
		}
		if p.acceptKeyword(KwSet) {
			// ALTER DOMAIN name SET DEFAULT expr / SET NOT NULL — reuses
			// parseExpr the same way CREATE DOMAIN's own DEFAULT clause does,
			// so the stored AST round-trips through pg_dump identically
			// either way. `SET` alone (without a following DEFAULT/NOT NULL)
			// falls through to the generic no-op below — SET is already
			// consumed by then, which is harmless since that loop just
			// discards tokens to the statement terminator. M0122-0005 domain
			// follow-up (SET/DROP DEFAULT; SET/DROP NOT NULL added in a later
			// follow-up).
			if p.acceptKeyword(KwDefault) {
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "setdefault", DefaultExpr: expr}, nil
			}
			if p.acceptKeyword(KwNot) {
				if _, err := p.expectKeyword(KwNull); err != nil {
					return nil, err
				}
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "setnotnull"}, nil
			}
		}
		if p.acceptKeyword(KwDrop) {
			if p.acceptKeyword(KwConstraint) {
				// ALTER DOMAIN name DROP CONSTRAINT [IF EXISTS] constraint_name
				// [RESTRICT | CASCADE]. M0122-0005 domain follow-up.
				ifExists := false
				if p.acceptKeyword(KwIf) {
					if _, err := p.expectKeyword(KwExists); err != nil {
						return nil, err
					}
					ifExists = true
				}
				conNameTok, err := p.parseIdent()
				if err != nil {
					return nil, err
				}
				// Optional RESTRICT/CASCADE drop-behavior trailer — parsed and
				// discarded; goopg tracks no dependents on a domain CHECK to
				// cascade over.
				if !p.acceptKeyword(KwCascade) {
					p.acceptKeyword(KwRestrict)
				}
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "dropconstraint", ConstraintName: identText(conNameTok), IfExists: ifExists}, nil
			}
			if p.acceptKeyword(KwDefault) {
				// ALTER DOMAIN name DROP DEFAULT. M0122-0005 domain follow-up.
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "dropdefault"}, nil
			}
			if p.acceptKeyword(KwNot) {
				// ALTER DOMAIN name DROP NOT NULL. M0122-0005 domain follow-up.
				if _, err := p.expectKeyword(KwNull); err != nil {
					return nil, err
				}
				return &AlterDomainStmt{pos: t.Pos, Name: name.Name, Action: "dropnotnull"}, nil
			}
		}
		// Unmodelled ALTER DOMAIN form (SET SCHEMA) —
		// consume to the terminator and return a no-op, mirroring the
		// compat-stub loop's prior behavior for this statement family.
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER DOMAIN"}, nil
	}
	// ALTER COLLATION / EXTENSION / LANGUAGE / OPERATOR / SYSTEM —
	// compatibility stubs. Consume until end of statement. (ALTER VIEW has
	// its own dedicated case above, DU-002 slice 440 — "view" is
	// intentionally not in this list; ALTER SCHEMA has its own dedicated
	// case immediately above — DU-002 slice 440 resume point (3); ALTER
	// DOMAIN has its own dedicated case immediately above too — M0122-0005
	// domain follow-up.)
	for _, objIdent := range []string{
		"collation", "extension", "language",
		"operator", "system",
	} {
		if p.acceptIdentKeyword(objIdent) {
			// consume until ';' or EOF
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER " + strings.ToUpper(objIdent)}, nil
		}
	}
	// ALTER FOREIGN DATA WRAPPER name [HANDLER h|NO HANDLER] [VALIDATOR h|NO
	// VALIDATOR] [OPTIONS ([ADD|SET|DROP] name ['value'], …)] — a structurally
	// distinct statement from ALTER [FOREIGN] TABLE (no TABLE keyword, no
	// relation actions), so it must be recognised BEFORE the FOREIGN-TABLE
	// check below consumes FOREIGN expecting TABLE to follow. Mirrors CREATE
	// FOREIGN DATA WRAPPER's parsing (captures HANDLER/VALIDATOR func names —
	// DU-002 M0119-0004, closing the "goopg tracks no funcs" gap slice 380 left
	// open) but captures the OPTIONS clause as a verb-tagged change list
	// (ADD/SET/DROP), not a flat replace, since ALTER merges onto the existing
	// fdwoptions (transformGenericOptions, gram.y AlterFdwStmt) rather than
	// recreating it. DU-002 slice 421.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign &&
		p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "data") {
		p.advance() // consume FOREIGN
		p.advance() // consume DATA
		_ = p.acceptIdentKeyword("wrapper")
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		var changes []FDWOptionChange
		var handlerFunc, validatorFunc *ObjectName
		var handlerGiven, validatorGiven bool
		for {
			tok := p.cur()
			if tok.Kind == TokenEOF || (tok.Kind == TokenSymbol && tok.Value == ";") {
				break
			}
			if tok.Kind == TokenIdent && strings.EqualFold(tok.Value, "options") {
				changes = p.scanAlterFDWOptionsList()
				continue
			}
			if kind, fn, consumed, err := p.scanFDWFuncClause(); consumed {
				if err != nil {
					return nil, err
				}
				if kind == "handler" {
					handlerFunc, handlerGiven = fn, true
				} else {
					validatorFunc, validatorGiven = fn, true
				}
				continue
			}
			p.advance()
		}
		return &CompatNoopStmt{
			pos: t.Pos, Tag: "ALTER FOREIGN DATA WRAPPER", ObjType: "foreign-data wrapper", ObjName: name,
			FDWOptionChanges: changes,
			FDWHandlerFunc:   handlerFunc, FDWHandlerGiven: handlerGiven,
			FDWValidatorFunc: validatorFunc, FDWValidatorGiven: validatorGiven,
		}, nil
	}
	// ALTER SERVER name [OWNER TO role | RENAME TO name | OPTIONS (...)
	// | VERSION 'ver'] — compat no-op for pg_dump round-trip. The server
	// was already registered by CREATE SERVER; ALTER only needs to be
	// accepted without error. DU-002 slice 376 follow-up (M0119-0004).
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "server") {
		p.advance() // consume SERVER
		name, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		// Consume trailing clauses up to the terminator.
		for p.cur().Kind != TokenEOF && !(p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
			if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				depth := 1
				p.advance()
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
					p.advance()
				}
				continue
			}
			p.advance()
		}
		return &CompatNoopStmt{pos: t.Pos, Tag: "ALTER SERVER", ObjType: "server", ObjName: name}, nil
	}
	// ALTER FOREIGN TABLE ... shares the plain ALTER TABLE grammar below (IF
	// EXISTS, ONLY, name, comma-separated actions) — FOREIGN is simply
	// consumed here so the rest of this function (including the ALTER
	// COLUMN ... OPTIONS (...) case, DU-002 slice 419) applies unchanged.
	// Only consumed when TABLE follows: ALTER FOREIGN DATA WRAPPER is handled
	// above and never reaches here.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwForeign &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwTable {
		p.advance() // consume FOREIGN
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
	// RENAME CONSTRAINT old TO new | RENAME VALUE 'old' TO 'new' | RENAME TO
	// new_name | RENAME [COLUMN] old_name TO new_name. M0134-0002 C2 slice 8:
	// COLUMN is optional in PG's grammar (opt_column: COLUMN | /*EMPTY*/,
	// gram.y:9974; the RENAME opt_column name TO name production at gram.y:9720),
	// so the bare form `RENAME a TO b` must reach the column path via the
	// fallthrough below. RENAME TO is checked before that fallthrough because TO
	// is a RESERVED keyword that parseIdent cannot consume.
	if p.acceptIdentKeyword("rename") {
		// RENAME CONSTRAINT old TO new — mirrors the ALTER DOMAIN RENAME
		// CONSTRAINT arm (ddl.go:8215-8228). Renames an existing table
		// constraint (CHECK/FK/UNIQUE/PK/EXCLUDE) in place. M0134-0002 C2.
		if p.acceptKeyword(KwConstraint) {
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
				pos:               oldNameTok.Pos,
				Kind:              AlterTableRenameConstraint,
				OldConstraintName: identText(oldNameTok),
				NewName:           identText(newNameTok),
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
		// RENAME TO new_name — must precede the bare-column fallthrough below:
		// TO is a RESERVED keyword that parseIdent cannot consume, so a table
		// rename would otherwise misparse as a column rename.
		if p.acceptKeyword(KwTo) {
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
		// RENAME [COLUMN] old_name TO new_name — COLUMN is optional in PG's
		// grammar, so the bare form `RENAME a TO b` flows into the existing
		// AlterTableRenameColumn executor path. M0134-0002 C2 slice 8.
		_ = p.acceptKeyword(KwColumn)
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
	// ENABLE/DISABLE ROW LEVEL SECURITY — real action: toggles
	// pg_class.relrowsecurity, which pg_dump re-emits as `ALTER TABLE <t> ENABLE
	// ROW LEVEL SECURITY;` (DU-002 slice 322). Detected before the trigger/rule
	// no-op arm below so the row-security clause is captured as an action rather
	// than silently consumed.
	if (p.cur().Kind == TokenIdent && (strings.EqualFold(p.cur().Value, "enable") || strings.EqualFold(p.cur().Value, "disable"))) &&
		strings.EqualFold(p.peek(1).Value, "row") &&
		strings.EqualFold(p.peek(2).Value, "level") &&
		strings.EqualFold(p.peek(3).Value, "security") {
		isEnable := strings.EqualFold(p.cur().Value, "enable")
		p.advance() // ENABLE/DISABLE
		p.advance() // ROW
		p.advance() // LEVEL
		p.advance() // SECURITY
		kind := AlterTableEnableRowSecurity
		if !isEnable {
			kind = AlterTableDisableRowSecurity
		}
		stmt.Actions = append(stmt.Actions, AlterTableAction{Kind: kind})
		return stmt, nil
	}
	// [NO] FORCE ROW LEVEL SECURITY — real action: toggles
	// pg_class.relforcerowsecurity, which pg_dump re-emits as `ALTER TABLE ONLY
	// <t> FORCE ROW LEVEL SECURITY;` (DU-002 slice 322).
	{
		forceOff := strings.EqualFold(p.cur().Value, "no")
		base := 0
		if forceOff {
			base = 1
		}
		if strings.EqualFold(p.peek(base).Value, "force") &&
			strings.EqualFold(p.peek(base+1).Value, "row") &&
			strings.EqualFold(p.peek(base+2).Value, "level") &&
			strings.EqualFold(p.peek(base+3).Value, "security") {
			if forceOff {
				p.advance() // NO
			}
			p.advance() // FORCE
			p.advance() // ROW
			p.advance() // LEVEL
			p.advance() // SECURITY
			kind := AlterTableForceRowSecurity
			if forceOff {
				kind = AlterTableNoForceRowSecurity
			}
			stmt.Actions = append(stmt.Actions, AlterTableAction{Kind: kind})
			return stmt, nil
		}
	}
	// {ENABLE | DISABLE} [REPLICA | ALWAYS] RULE name — records the rule's
	// pg_rewrite.ev_enabled so pg_dump's dumpRule re-emits the ALTER TABLE clause.
	// goopg implements no query rewrite; this is schema fidelity only. Detected
	// before the generic ENABLE/DISABLE no-op arm below (a RULE target is captured
	// as a real action; TRIGGER and other variants still fall through). DU-002
	// slice 325.
	if p.cur().Kind == TokenIdent &&
		(strings.EqualFold(p.cur().Value, "enable") || strings.EqualFold(p.cur().Value, "disable")) {
		isEnable := strings.EqualFold(p.cur().Value, "enable")
		// state = ev_enabled char; mod = peek index of the word that must be RULE.
		state := byte('D')
		mod := 1
		if isEnable {
			state = 'O'
			switch {
			case strings.EqualFold(p.peek(1).Value, "replica"):
				state, mod = 'R', 2
			case strings.EqualFold(p.peek(1).Value, "always"):
				state, mod = 'A', 2
			}
		}
		if strings.EqualFold(p.peek(mod).Value, "rule") {
			for i := 0; i <= mod; i++ {
				p.advance() // ENABLE/DISABLE [REPLICA|ALWAYS] RULE
			}
			ruleNameTok, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			stmt.Actions = append(stmt.Actions, AlterTableAction{
				Kind:             AlterTableEnableDisableRule,
				RuleName:         identText(ruleNameTok),
				RuleEnabledState: state,
			})
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
	// OPTIONS ( [ADD|SET|DROP] name ['value'], … ) — ALTER FOREIGN TABLE's
	// table-level generic option list (AT_GenericOptions in real PG), the
	// counterpart of the ALTER COLUMN ... OPTIONS (...) case below but without
	// an ALTER COLUMN prefix. Sets pg_foreign_table.ftoptions. The executor
	// rejects this on a non-foreign table. DU-002 slice 420, closes the
	// loop #56 deferral-ledger resume point.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "options") {
		changes := p.scanAlterFDWOptionsList()
		stmt.Actions = append(stmt.Actions, AlterTableAction{
			Kind:             AlterTableSetForeignOptions,
			FDWOptionChanges: changes,
		})
		return stmt, nil
	}
	// DROP CONSTRAINT name [RESTRICT|CASCADE] — real action (M0097-0036).
	// DROP sub-commands (DROP COLUMN, DROP CONSTRAINT) are handled by
	// parseAlterTableAction() to support comma-separated multi-action ALTER TABLE
	// statements (e.g. "ALTER TABLE t DROP COLUMN a, DROP COLUMN b"). Fall through
	// to the multi-action loop below. M0097-0028.
	// ALTER CONSTRAINT name ConstraintAttributeSpec (PG18) — re-declares an
	// EXISTING constraint's deferrability/enforceability rather than adding a
	// new one. Must be checked before the generic "ALTER COLUMN" branch below:
	// both start with the bare ALTER keyword, and CONSTRAINT (not COLUMN) is
	// the only thing distinguishing them at this point in the grammar — the
	// ALTER COLUMN branch would otherwise consume "CONSTRAINT" as if it were a
	// (quoted) column name. DU-002 slice 433.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwConstraint {
		p.advance() // ALTER
		p.advance() // CONSTRAINT
		nameTok, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		deferrable, initiallyDeferred, enforced, hasDeferrability, hasEnforceability := p.parseAlterConstraintAttrs()
		stmt.Actions = append(stmt.Actions, AlterTableAction{
			pos:                              nameTok.Pos,
			Kind:                             AlterTableAlterConstraint,
			ConstraintName:                   identText(nameTok),
			AlterConstraintDeferrable:        deferrable,
			AlterConstraintInitiallyDeferred: initiallyDeferred,
			AlterConstraintHasDeferrability:  hasDeferrability,
			AlterConstraintEnforced:          enforced,
			AlterConstraintHasEnforceability: hasEnforceability,
		})
		return stmt, nil
	}
	// ALTER COLUMN sub-commands (SET/DROP/TYPE/OPTIONS/STORAGE/COMPRESSION/
	// STATISTICS/DEFAULT/NOT NULL) are handled by parseAlterTableAction() to
	// support comma-separated multi-action ALTER TABLE statements (e.g. "ALTER
	// TABLE t ALTER COLUMN a SET NOT NULL, ALTER COLUMN b DROP NOT NULL"). Fall
	// through to the multi-action loop below.
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

// parseAlterColumnAction parses a single `ALTER [COLUMN] name …` sub-command of
// an ALTER TABLE statement — the AT_AlterColumn* family in upstream grammar
// (postgres/src/backend/parser/gram.y alter_table_cmd). It returns the one
// action rather than appending to the statement, so the caller's comma loop can
// build a multi-action ALTER TABLE list (e.g. "ALTER TABLE t ALTER COLUMN a SET
// NOT NULL, ALTER COLUMN b DROP NOT NULL"). Previously this block lived inline
// in parseAlter and early-returned, so a comma was never consumed and bubbled to
// the statement loop as `syntax error ... (got ,)`. M0134-0002 C2 slice 6.
func (p *parser) parseAlterColumnAction() (AlterTableAction, error) {
	p.advance() // consume ALTER
	// Skip COLUMN keyword if present.
	_ = p.acceptKeyword(KwColumn)
	// Read the column name.
	colName := ""
	if p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent {
		colName = p.cur().Value
		p.advance()
	}
	// OPTIONS ( [ADD|SET|DROP] name ['value'], … ) — ALTER FOREIGN TABLE's
	// per-column generic option list (AT_AlterColumnGenericOptions in real
	// PG). The executor rejects this on a non-foreign table. DU-002 slice
	// 419, closes the loop #55 deferral-ledger resume point.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "options") {
		changes := p.scanAlterFDWOptionsList()
		return AlterTableAction{
			Kind:             AlterTableAlterColumnOptions,
			ColumnName:       colName,
			FDWOptionChanges: changes,
		}, nil
	}
	// Check for SET (options) or SET STORAGE pattern.
	if p.acceptIdentKeyword("set") || p.acceptKeyword(KwSet) {
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			// SET (opt=value, …) — per-column attribute options (e.g.
			// n_distinct). Capture each pair, normalized to PG's stored
			// `name=value` form, so pg_dump re-emits the clause via
			// pg_attribute.attoptions. DU-002 slice 185.
			opts := p.parseColumnSetOptions()
			return AlterTableAction{
				Kind:       AlterTableAlterColumnSet,
				ColumnName: colName,
				SetOptions: opts,
			}, nil
		}
		// SET STORAGE type — record storage strategy on the catalog column.
		if p.acceptIdentKeyword("storage") {
			storageType := ""
			if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
				storageType = strings.ToLower(p.cur().Value)
				p.advance()
			}
			return AlterTableAction{
				Kind:        AlterTableSetStorage,
				ColumnName:  colName,
				StorageType: storageType,
			}, nil
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
			return AlterTableAction{
				Kind:            AlterTableSetCompression,
				ColumnName:      colName,
				CompressionType: normalizeCompressionMethod(method),
			}, nil
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
			return AlterTableAction{
				Kind:       AlterTableSetStatistics,
				ColumnName: colName,
				CheckExpr:  statsVal,
			}, nil
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
				return AlterTableAction{}, err
			}
			return AlterTableAction{
				Kind:        AlterTableSetDefault,
				ColumnName:  colName,
				DefaultExpr: expr,
			}, nil
		}
		// SET NOT NULL — mark the column NOT NULL. The executor records a
		// contype='n' constraint so pg_dump re-emits the NOT NULL: inline on
		// a printed local column, or as a standalone `NOT NULL <col>` item in
		// the child CREATE TABLE body when the column is a suppressed
		// inherited column. DU-002 slice 270.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot {
			p.advance() // consume NOT
			if _, err := p.expectKeyword(KwNull); err != nil {
				return AlterTableAction{}, err
			}
			return AlterTableAction{
				Kind:       AlterTableSetNotNull,
				ColumnName: colName,
			}, nil
		}
	}
	// RESET (opt, …) — clear the named per-column attribute options set by
	// the SET (...) form above. Unlike SET, RESET has no STORAGE/COMPRESSION/
	// STATISTICS/DEFAULT/NOT NULL counterparts in upstream grammar — only the
	// parenthesized attribute-option list is valid here.
	if p.acceptIdentKeyword("reset") || p.acceptKeyword(KwReset) {
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			opts := p.parseColumnSetOptions()
			return AlterTableAction{
				Kind:       AlterTableAlterColumnReset,
				ColumnName: colName,
				SetOptions: opts,
			}, nil
		}
	}
	// DROP DEFAULT / DROP NOT NULL — clear the column's DEFAULT expression or
	// NOT NULL flag. Other DROP forms (DROP IDENTITY, …) fall through to the
	// no-op consume below for now. DU-002 slices 269, 270.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDrop {
		p.advance() // consume DROP
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
			p.advance() // consume DEFAULT
			return AlterTableAction{
				Kind:       AlterTableDropDefault,
				ColumnName: colName,
			}, nil
		}
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNot {
			p.advance() // consume NOT
			if _, err := p.expectKeyword(KwNull); err != nil {
				return AlterTableAction{}, err
			}
			return AlterTableAction{
				Kind:       AlterTableDropNotNull,
				ColumnName: colName,
			}, nil
		}
	}
	// Check for TYPE newtype pattern.
	// "type" is not in goopg's keyword map — arrives as TokenIdent.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "type") {
		p.advance() // consume TYPE
		newType, err := p.parseColumnType()
		if err != nil {
			return AlterTableAction{}, err
		}
		// TYPE newtype [USING expr] — consume an optional USING
		// expression (gram.y alter_using). Before this slice the
		// trailer was left unconsumed and bubbled to the statement
		// loop as `syntax error at or near ... (got using)`; PG
		// carries it in AlterTableCmd->def->raw_default
		// (postgres/src/backend/parser/gram.y:3028) and evaluates it
		// per-row against the original row type. Mirrors the SET
		// DEFAULT arm above (which stores its parsed expr on
		// DefaultExpr). M0134-0002 C2 slice 5.
		var usingExpr Expr
		if p.acceptKeyword(KwUsing) {
			usingExpr, err = p.parseExpr()
			if err != nil {
				return AlterTableAction{}, err
			}
		}
		return AlterTableAction{
			Kind:       AlterTableAlterColumnType,
			ColumnName: colName,
			NewType:    newType,
			UsingExpr:  usingExpr,
		}, nil
	}
	// Other ALTER COLUMN forms: consume rest as no-op. Break on both ',' (so the
	// caller's comma loop can pick up the next action) and ';' (statement end);
	// the executor ignores AlterTableNoOp actions.
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol && (p.cur().Value == ";" || p.cur().Value == ",") {
			break
		}
		p.advance()
	}
	return AlterTableAction{Kind: AlterTableNoOp}, nil
}

func (p *parser) parseAlterTableAction() (AlterTableAction, error) {
	// ALTER COLUMN sub-command — comma-combinable with the other alter_table_cmds.
	// The bare ALTER token cannot collide with ATTACH/DETACH/DROP/SET/RESET/etc.
	// (distinct tokens); ALTER CONSTRAINT is intercepted in parseAlter.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
		return p.parseAlterColumnAction()
	}
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
		childName, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		// The optional CONCURRENTLY / FINALIZE trailer (PG14+) follows the
		// child name, not precedes it. CONCURRENTLY is recorded for the
		// (deferred) two-phase detach; FINALIZE is accepted and ignored. The
		// executor performs a synchronous detach in either case for now.
		concurrently := p.acceptIdentKeyword("concurrently")
		p.acceptIdentKeyword("finalize")
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableDetachPartition, DetachPartitionChild: childName, DetachConcurrently: concurrently}, nil
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
	// REPLICA IDENTITY { DEFAULT | FULL | NOTHING | USING INDEX name }.
	// Records the table's relreplident on catalog.Table so pg_dump round-trips
	// the `ALTER TABLE ONLY ... REPLICA IDENTITY ...` clause. DU-002 slice 305.
	if p.acceptIdentKeyword("replica") {
		pos := p.cur().Pos
		if !p.acceptIdentKeyword("identity") {
			return AlterTableAction{}, p.errAtCur("expected IDENTITY after REPLICA")
		}
		mode := ""
		index := ""
		switch {
		case p.acceptKeyword(KwDefault) || p.acceptIdentKeyword("default"):
			mode = "d"
		case p.acceptKeyword(KwFull) || p.acceptIdentKeyword("full"):
			mode = "f"
		case p.acceptKeyword(KwNothing) || p.acceptIdentKeyword("nothing"):
			mode = "n"
		case p.acceptKeyword(KwUsing) || p.acceptIdentKeyword("using"):
			if !(p.acceptKeyword(KwIndex) || p.acceptIdentKeyword("index")) {
				return AlterTableAction{}, p.errAtCur("expected INDEX after USING")
			}
			idxTok, err := p.parseIdent()
			if err != nil {
				return AlterTableAction{}, err
			}
			mode = "i"
			index = identText(idxTok)
		default:
			return AlterTableAction{}, p.errAtCur("expected DEFAULT, FULL, NOTHING or USING INDEX after REPLICA IDENTITY")
		}
		return AlterTableAction{
			pos:                  pos,
			Kind:                 AlterTableReplicaIdentity,
			ReplicaIdentityMode:  mode,
			ReplicaIdentityIndex: index,
		}, nil
	}
	// CLUSTER ON index_name — mark the named index as the table's clustering
	// index (pg_index.indisclustered). This is the exact form pg_dump EMITS for
	// a clustered table, so goopg must accept it to restore its own dumps. The
	// `CLUSTER <t> USING <idx>` statement records the same selection (slice 320).
	// DU-002 slice 321.
	if p.acceptKeyword(KwCluster) {
		pos := p.cur().Pos
		if !p.acceptKeyword(KwOn) {
			return AlterTableAction{}, p.errAtCur("expected ON after CLUSTER")
		}
		idxTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{
			pos:              pos,
			Kind:             AlterTableClusterOn,
			ClusterIndexName: identText(idxTok),
		}, nil
	}
	// SET WITHOUT CLUSTER — clear the table's clustering selection (every index's
	// pg_index.indisclustered → false). Distinct from `SET (reloptions)` below,
	// which is the parenthesized form. DU-002 slice 321.
	// SET WITHOUT OIDS — same arm: PG gram.y:2731-2738 maps `SET WITHOUT OIDS` to
	// AT_DropOids, whose ATExecCmd is a silent no-op ("nothing to do here, oid
	// columns don't exist anymore", tablecmds.c:5528-5530; alter_table.out:1503
	// is empty). goopg returns the existing AlterTableNoOp (a silent no-op in
	// execAlterTable) for the same result. M0134-0002 C2 slice 12.
	if cur := p.cur(); (cur.Kind == TokenKeyword && cur.Keyword == KwSet) &&
		p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "without") {
		pos := cur.Pos
		p.advance() // SET
		p.advance() // WITHOUT
		if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "oids") {
			p.advance() // OIDS
			return AlterTableAction{pos: pos, Kind: AlterTableNoOp}, nil
		}
		if !p.acceptKeyword(KwCluster) {
			return AlterTableAction{}, p.errAtCur("expected CLUSTER after SET WITHOUT")
		}
		return AlterTableAction{pos: pos, Kind: AlterTableSetWithoutCluster}, nil
	}
	// DROP COLUMN name / DROP CONSTRAINT name in the multi-action loop.
	// Both forms share this path so comma-separated "DROP COLUMN a, DROP COLUMN b"
	// work correctly. M0097-0028.
	if p.acceptKeyword(KwDrop) {
		if p.acceptKeyword(KwConstraint) {
			// DROP CONSTRAINT [IF EXISTS] name (gram.y opt_if_exists:
			// IF_P EXISTS). Match with acceptKeyword — NOT acceptIdentKeyword,
			// which only matches TokenIdent and would silently drop the
			// KwIf/KwExists keyword tokens. M0134-0002 C2.
			ifExists := false
			if p.acceptKeyword(KwIf) {
				if !p.acceptKeyword(KwExists) {
					return AlterTableAction{}, p.errAtCur("expected EXISTS after IF")
				}
				ifExists = true
			}
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
				IfExists:       ifExists,
			}, nil
		}
		// DROP COLUMN [IF EXISTS] col_name [RESTRICT|CASCADE]
		_ = p.acceptKeyword(KwColumn)
		// Optional `IF EXISTS` (gram.y opt_if_exists: IF_P EXISTS). Match it
		// with acceptKeyword — NOT acceptIdentKeyword, which only matches
		// TokenIdent and would silently drop the KwIf/KwExists keyword tokens
		// (same trap the ADD COLUMN arm documents at ddl.go:9701). M0134-0002 C2.
		ifExists := false
		if p.acceptKeyword(KwIf) {
			if !p.acceptKeyword(KwExists) {
				return AlterTableAction{}, p.errAtCur("expected EXISTS after IF")
			}
			ifExists = true
		}
		colTok := p.cur()
		if colTok.Kind == TokenIdent || colTok.Kind == TokenQuotedIdent {
			p.advance()
			_ = p.acceptKeyword(KwCascade) || p.acceptKeyword(KwRestrict)
			return AlterTableAction{
				pos:        colTok.Pos,
				Kind:       AlterTableDropColumn,
				ColumnName: identText(colTok),
				IfExists:   ifExists,
			}, nil
		}
		return AlterTableAction{}, p.errAtCur("expected column or constraint name after DROP")
	}
	// SET TABLESPACE name — an alter_table_cmd in its own right (gram.y's
	// AT_SetTableSpace), combinable with other comma-separated actions, unlike
	// SET SCHEMA/SET LOGGED which the caller intercepts as whole-statement
	// fields before this per-action parser ever runs. Catalog metadata only —
	// no physical relocation of the relation's files. M0122-0007.
	if cur := p.cur(); cur.Kind == TokenKeyword && cur.Keyword == KwSet &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwTablespace {
		pos := cur.Pos
		p.advance() // SET
		p.advance() // TABLESPACE
		tsTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: pos, Kind: AlterTableSetTablespace, TablespaceName: identText(tsTok)}, nil
	}
	// SET ACCESS METHOD name — change the table's access method (pg_class.relam).
	// goopg only supports `heap`; the executor rejects any other AM.
	// DU-002: pg_dump emits `ALTER TABLE ... SET ACCESS METHOD heap` for
	// partitioned tables whose relam differs from the default.
	if cur := p.cur(); cur.Kind == TokenKeyword && cur.Keyword == KwSet &&
		p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "access") &&
		p.peek(2).Kind == TokenIdent && strings.EqualFold(p.peek(2).Value, "method") {
		pos := cur.Pos
		p.advance() // SET
		p.advance() // ACCESS
		p.advance() // METHOD
		amTok, err := p.parseIdent()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: pos, Kind: AlterTableSetAccessMethod, AccessMethodName: identText(amTok)}, nil
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
	// OF type_name — make the table a typed table of the composite type
	// (PG gram.y `OF any_name` → AT_AddOf). `OF` is a distinct keyword token
	// (KwOf, token.go:169). All type resolution + column validation happens at
	// EXECUTE time (ATExecAddOf, tablecmds.c:18216); here we only capture the
	// name, mirroring the INHERIT arm above. M0134-0002 C2 slice 11.
	if p.acceptKeyword(KwOf) {
		tn, err := p.parseObjectName()
		if err != nil {
			return AlterTableAction{}, err
		}
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableAddOf, OfType: tn}, nil
	}
	// NOT OF — detach a typed table from its originating type
	// (PG gram.y `NOT OF` → AT_DropOf). `NOT` matches KwNot (token.go:81) — do
	// NOT confuse with the acceptIdentKeyword("no") NO INHERIT path above.
	// M0134-0002 C2 slice 11.
	if p.acceptKeyword(KwNot) {
		if !p.acceptKeyword(KwOf) {
			return AlterTableAction{}, p.errAtCur("expected OF after NOT")
		}
		return AlterTableAction{pos: p.cur().Pos, Kind: AlterTableDropOf}, nil
	}
	// SET WITH … — no production in PG's gram.y alter_table_cmd (no SET WITH
	// arm), so the generic bison error points at the WITH keyword. PG's
	// scanner_yyerror echoes the raw source token (scan.l:1234-1241), which for
	// the regress input `SET WITH OIDS` (alter_table.sql:1044) is uppercase;
	// goopg's lexer lowercases keyword Values, so re-uppercase it. M0134-0002
	// C2 slice 12.
	if cur := p.cur(); cur.Kind == TokenKeyword && cur.Keyword == KwSet &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwWith {
		withTok := p.peek(1)
		return AlterTableAction{}, &SyntaxError{Pos: withTok.Pos, Message: strings.ToUpper(withTok.Value)}
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
		// Optional DEFERRABLE [INITIALLY {DEFERRED|IMMEDIATE}].
		// pg_dump emits `PRIMARY KEY (col) DEFERRABLE INITIALLY DEFERRED`
		// for constraints whose condeferrable/condeferred are set. DU-002.
		p.parseConstraintDeferrable(&act.Deferrable, &act.InitiallyDeferred)
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
		// Optional MATCH FULL | PARTIAL | SIMPLE, between the referenced column
		// list and the ON DELETE/UPDATE clauses (PG gram.y key_match). DU-002 slice 309.
		matchFull := parseFKMatchType(p)
		// Optional ON DELETE / ON UPDATE referential-action clauses, mirroring
		// the inline column-FK path so actions survive into pg_constraint and
		// pg_dump (DU-002 slice 52). ON is KwOn (reserved).
		var onDelete, onUpdate FKAction
		var onDeleteSetCols []string
		for p.acceptKeyword(KwOn) {
			isDelete := p.acceptKeyword(KwDelete)
			if !isDelete {
				_ = p.acceptKeyword(KwUpdate)
			}
			action, setCols, aerr := parseFKAction(p)
			if aerr != nil {
				return AlterTableAction{}, aerr
			}
			if isDelete {
				onDelete = action
				onDeleteSetCols = setCols
			} else {
				onUpdate = action
			}
		}
		// Optional [NOT] DEFERRABLE [INITIALLY …], NOT VALID, and/or [NOT]
		// ENFORCED trailers, in any order (PG grammar's ConstraintAttributeSpec
		// allows e.g. `… DEFERRABLE NOT VALID` or `… NOT ENFORCED` in any
		// sequence — gram.y's ConstraintAttributeElem list applies identically
		// to CHECK and FOREIGN KEY constraints). `NOT VALID` (ALTER TABLE ADD
		// FOREIGN KEY … NOT VALID) creates the constraint without checking
		// pre-existing rows; a later VALIDATE CONSTRAINT performs the scan.
		// M0118-0008 (alter-table-1/2 isolation specs). `NOT ENFORCED` (PG18)
		// disables the constraint's action/check triggers entirely — mirrored
		// here the same way the CHECK-constraint form already threads it
		// (DU-002 slice 430): the AST field stays independent of NotValid (a
		// bare NOT ENFORCED does NOT set NotValid here, matching
		// TestParseCheckNotEnforced's CHECK-form precedent); the catalog layer
		// derives convalidated='f' from `NotValid || NotEnforced` instead
		// (mirroring PG's processCASbits, which sets *not_valid=true only as
		// an internal consistency detail, not something surfaced back through
		// this AST). DU-002 slice 431. Shared with the CREATE TABLE-time FK
		// forms (inline column REFERENCES, table-level FOREIGN KEY) via
		// parseFKConstraintAttrs so all three stay in lockstep (DU-002 slice
		// 432); AlterTableAction has no InitiallyDeferred field to populate, so
		// that return value is discarded here.
		deferrable, _, notValid, notEnforced, fkErr := p.parseFKConstraintAttrs()
		if fkErr != nil {
			return AlterTableAction{}, fkErr
		}
		act.Kind = AlterTableAddForeignKey
		act.Columns = cols
		act.RefTable = refTable
		act.RefColumns = refCols
		act.Deferrable = deferrable
		act.NotValid = notValid
		act.FKNotEnforced = notEnforced
		act.MatchFull = matchFull
		act.OnDelete = onDelete
		act.OnUpdate = onUpdate
		act.OnDeleteSetCols = onDeleteSetCols
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck:
		// ADD [CONSTRAINT name] CHECK (expr) — register the check constraint.
		p.advance() // consume CHECK
		expr, err := p.parseCheckExpr()
		if err != nil {
			return AlterTableAction{}, err
		}
		// Consume optional NOT VALID and/or [NOT] ENFORCED trailers (PG18+).
		// Possible orderings: NOT VALID, ENFORCED, NOT ENFORCED, NOT VALID ENFORCED,
		// NOT VALID NOT ENFORCED. NOT VALID must survive into
		// pg_constraint.convalidated='f' so pg_dump re-emits the ` NOT VALID`
		// tail and loads data before the constraint (DU-002 slice 308); NOT
		// ENFORCED must likewise survive into conenforced='f' so pg_dump
		// re-emits the ` NOT ENFORCED` tail instead (which takes precedence
		// over NOT VALID in the rendered text). DU-002 slice 430.
		notValid := false
		notEnforced := false
		noInherit := false
		// Accept optional NO INHERIT trailer (connoinherit='t'). PG's
		// ConstraintAttributeSpec (gram.y) is order-independent, so NO INHERIT
		// may precede OR follow the NOT VALID/[NOT] ENFORCED block — consume at
		// both points. alter_table.sql has `check (a = 2) no inherit not valid`
		// (:420) and trailing ` NO INHERIT` (:309/663/670/1582). M0134-0002 C2.
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("inherit")
			noInherit = true
		}
		if p.acceptKeyword(KwNot) {
			if !p.acceptIdentKeyword("valid") {
				if p.acceptIdentKeyword("enforced") { // NOT ENFORCED
					notEnforced = true
				}
			} else {
				notValid = true
				// NOT VALID — also accept optional trailing [NOT] ENFORCED.
				if p.acceptKeyword(KwNot) {
					if p.acceptIdentKeyword("enforced") {
						notEnforced = true
					}
				} else {
					_ = p.acceptIdentKeyword("enforced")
				}
			}
		} else {
			_ = p.acceptIdentKeyword("enforced") // bare ENFORCED
		}
		// A second [NOT] ENFORCED is a PG error (transformConstraintAttrs,
		// parse_utilcmd.c:3999-4027). M0134-0002 C2 slice 12.
		if err := p.rejectDuplicateEnforced(); err != nil {
			return AlterTableAction{}, err
		}
		// NO INHERIT may also trail the NOT VALID/[NOT] ENFORCED block.
		if p.acceptIdentKeyword("no") {
			_ = p.acceptIdentKeyword("inherit")
			noInherit = true
		}
		act.Kind = AlterTableAddCheck
		act.CheckExpr = expr
		act.NotValid = notValid
		act.CheckNotEnforced = notEnforced
		act.NoInherit = noInherit
		return act, nil
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwUnique:
		// ADD [CONSTRAINT name] UNIQUE (cols) [INCLUDE (incl)] — create a unique index.
		// M0097-0023.
		p.advance()
		// Optional NULLS [NOT] DISTINCT (PostgreSQL 15+) precedes the column
		// list for a constraint (ruleutils.c emits `UNIQUE NULLS NOT DISTINCT
		// (col)` when the flag is set). DU-002.
		if p.acceptIdentKeyword("nulls") {
			act.NullsNotDistinct = p.acceptKeyword(KwNot)
			if !p.acceptKeyword(KwDistinct) {
				_ = p.acceptIdentKeyword("distinct")
			}
		}
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
		// Optional DEFERRABLE [INITIALLY {DEFERRED|IMMEDIATE}].
		// pg_dump emits `UNIQUE (col) DEFERRABLE INITIALLY DEFERRED`
		// for constraints whose condeferrable/condeferred are set. DU-002.
		p.parseConstraintDeferrable(&act.Deferrable, &act.InitiallyDeferred)
		act.Kind = AlterTableAddUnique
		return act, nil
	case p.acceptIdentKeyword("exclude"):
		// ADD [CONSTRAINT name] EXCLUDE USING method (col WITH op)
		// [INCLUDE (cols)] [WHERE (pred)] — create an exclusion
		// constraint. DU-002.
		cdef := p.parseExcludeConstraint()
		act.Columns = cdef.Columns
		act.IncludeColumns = cdef.IncludeColumns
		act.ExclusionOp = cdef.ExclusionOp
		act.ExclusionMethod = cdef.Method
		act.ExclusionWhere = cdef.ExclusionWhere
		// Optional DEFERRABLE [INITIALLY {DEFERRED|IMMEDIATE}].
		p.parseConstraintDeferrable(&act.Deferrable, &act.InitiallyDeferred)
		act.Kind = AlterTableAddExclude
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
		// Optional `NOT VALID` trailer (convalidated='f'), accepted before OR
		// after NO INHERIT — PG's ConstraintAttributeSpec is order-independent
		// (gram.y:6213-6252). M0134-0002 C2 slice 10.
		if p.acceptKeyword(KwNot) {
			if !p.acceptIdentKeyword("valid") {
				return AlterTableAction{}, p.errAtCur("expected VALID after NOT")
			}
			act.NotValid = true
		}
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
		// Optional `IF NOT EXISTS` (gram.y opt_if_not_exists:
		// IF_P NOT EXISTS). Match it with acceptKeyword — NOT acceptIdentKeyword,
		// which only matches TokenIdent and would silently drop the KwIf keyword
		// (same trap as the DROP COLUMN arm at ddl.go:9096). Mirrors the CREATE
		// TABLE IF NOT EXISTS pattern in parseCreateTableTail. M0134-0002 C2.
		if p.acceptKeyword(KwIf) {
			if !p.acceptKeyword(KwNot) {
				return AlterTableAction{}, p.errAtCur("expected NOT after IF")
			}
			if _, err := p.expectKeyword(KwExists); err != nil {
				return AlterTableAction{}, err
			}
			act.IfExists = true
		}
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
		// AS RANGE ( subtype = ..., multirange_type_name = ..., ... ) — range
		// type. `subtype`, `multirange_type_name`, `subtype_opclass`, and
		// `collation` are captured; `canonical`/`subtype_diff` are consumed so
		// the statement still parses but are not yet applied (DU-002,
		// M0110-0001, slice 429 follow-up sub-item (a)).
		if p.acceptIdentKeyword("range") {
			stmt.IsRange = true
			if !p.acceptSymbol("(") {
				return nil, p.errAtCur("expected '(' after RANGE")
			}
			for {
				if p.cur().Kind != TokenIdent {
					break
				}
				key := strings.ToLower(p.advance().Value)
				if c := p.cur(); (c.Kind == TokenSymbol || c.Kind == TokenOperator) && c.Value == "=" {
					p.advance()
				} else {
					return nil, p.errAtCur("expected '=' in range type option")
				}
				switch key {
				case "multirange_type_name":
					mrName, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					stmt.RangeMultirangeName = mrName.Name
				case "subtype_opclass":
					ocName, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					stmt.RangeOpclassName = ocName.Name
				case "collation":
					collName, err := p.parseObjectName()
					if err != nil {
						return nil, err
					}
					stmt.RangeCollationName = collName.Name
				default:
					// Collect value tokens until a top-level ',' or ')', tracking
					// paren depth so a typmod-bearing subtype (e.g. numeric(10,2))
					// stays intact. Mirrors the composite-field type collector above.
					var valueParts []string
					parenDepth := 0
					for p.cur().Kind != TokenEOF {
						tok := p.cur()
						if tok.Kind == TokenSymbol && parenDepth == 0 &&
							(tok.Value == "," || tok.Value == ")") {
							break
						}
						if tok.Kind == TokenSymbol && tok.Value == "(" {
							parenDepth++
						} else if tok.Kind == TokenSymbol && tok.Value == ")" {
							parenDepth--
						}
						valueParts = append(valueParts, tok.Value)
						p.advance()
					}
					if key == "subtype" {
						stmt.RangeSubtype = strings.Join(valueParts, " ")
					}
				}
				if p.acceptSymbol(",") {
					continue
				}
				break
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' after RANGE option list")
			}
			return stmt, nil
		}
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
	// OWNER TO role — capture the new owner so the executor can update
	// typowner instead of silently no-op'ing. M0122-0005 (m0097-0017
	// follow-up). CURRENT_USER / SESSION_USER / CURRENT_ROLE resolve to the
	// bootstrap superuser sentinel, mirroring ALTER COLLATION ... OWNER TO.
	if p.acceptIdentKeyword("owner") {
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
		if p.acceptIdentKeyword("current_user") ||
			p.acceptIdentKeyword("session_user") ||
			p.acceptIdentKeyword("current_role") {
			stmt.NewOwner = "current_user"
		} else if tok, err := p.parseIdent(); err == nil {
			stmt.NewOwner = identText(tok)
		} else {
			stmt.NewOwner = "current_user"
		}
		return stmt, nil
	}
	// Any other ALTER TYPE variant — consume as stub.
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
	// Base type name (may be schema-qualified, or a multi-word built-in type
	// like "double precision"/"character varying"/"timestamp with time zone" —
	// mirrors parseColumnType's CREATE TABLE column-type grammar so CREATE
	// DOMAIN's AS clause accepts the same type spellings pg_dump emits).
	baseTypeName, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.BaseType = baseTypeName.Name
	if baseTypeName.Schema != "" {
		stmt.BaseType = baseTypeName.Schema + "." + baseTypeName.Name
	} else {
		stmt.BaseType = p.parseMultiWordTypeName(stmt.BaseType)
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
	// Handle "time(N) with/without time zone" and "timestamp(N) with/without
	// time zone" where the timezone qualifier follows the typmod parens.
	if len(stmt.BaseTypeArgs) > 0 {
		stmt.BaseType = p.parseTimeZoneQualifierAfterArgs(stmt.BaseType)
	}
	// FLOAT [ (precision) ] → float4/float8, same opt_float reduction the
	// CREATE TABLE column-type path applies.
	if baseTypeName.Schema == "" {
		bt, btArgs, ferr := normalizeFloatTypeName(stmt.BaseType, stmt.BaseTypeArgs, pos)
		if ferr != nil {
			return nil, ferr
		}
		stmt.BaseType, stmt.BaseTypeArgs = bt, btArgs
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
				// Every CHECK clause is appended to stmt.Checks; PG stores each as a
				// separate pg_constraint row. DU-002 slice 385 (multi-CHECK).
				p.advance()
				cname, _ := p.parseIdent() // constraint name
				// Fall through to CHECK handling below.
				if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwCheck {
					p.advance()
					if vals := p.tryParseCheckInValues(); vals != nil {
						// Preserve the explicit CONSTRAINT name so the deparsed
						// `= ANY (ARRAY[...])` round-trips with the right conname
						// through pg_dump. DU-002 slice 97.
						stmt.Checks = append(stmt.Checks, DomainCheckClause{Name: cname.Value, InValues: vals})
					} else {
						// Generic CHECK expression: capture the raw predicate text
						// (e.g. `VALUE > 0`) so it round-trips through pg_dump via
						// pg_get_constraintdef. DU-002 slice 96.
						expr, err := p.parseDomainCheckExpr()
						if err != nil {
							return nil, err
						}
						stmt.Checks = append(stmt.Checks, DomainCheckClause{Name: cname.Value, Expr: expr})
					}
				}
				continue
			case KwCheck:
				p.advance()
				if vals := p.tryParseCheckInValues(); vals != nil {
					stmt.Checks = append(stmt.Checks, DomainCheckClause{InValues: vals})
				} else {
					// Generic CHECK expression (auto-named <domain>_check). DU-002 slice 96.
					expr, err := p.parseDomainCheckExpr()
					if err != nil {
						return nil, err
					}
					stmt.Checks = append(stmt.Checks, DomainCheckClause{Expr: expr})
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
