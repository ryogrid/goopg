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
	// CREATE [GLOBAL|LOCAL] TEMP[ORARY] TABLE → treat as CREATE TABLE but mark as Temporary.
	// Temporary flag enables shadow-on-conflict behavior in execCreateTable. M0097-0003.
	_ = p.acceptIdentKeyword("global") || p.acceptIdentKeyword("local")
	if p.acceptIdentKeyword("temp") || p.acceptIdentKeyword("temporary") {
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
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, FUNCTION, PROCEDURE, or TRIGGER after CREATE")
}

// parseCreatePublicationTail picks up after CREATE PUBLICATION.
// Grammar: `name [FOR ALL TABLES | FOR TABLE t1 [, t2 ...]]
//           [WITH (option = value [, ...])]`.
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
//           [WITH (option = value [, ...])]`.
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
	if _, err := p.expectKeyword(KwAs); err != nil {
		return nil, err
	}
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSelect) {
		return nil, p.errAtCur("expected SELECT after AS")
	}
	inner, err := p.parseSelect()
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
	if p.acceptIdentKeyword("using") {
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
	// Optional WITH [NO] DATA clause.
	if p.acceptKeyword(KwWith) {
		if p.acceptKeyword(KwNot) {
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
	// Optional WITH [NO] DATA.
	if p.acceptKeyword(KwWith) {
		if p.acceptKeyword(KwNot) {
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

	// CREATE TABLE name AS SELECT … (CTAS). M0096-0008.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
		p.advance() // consume AS
		sel, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		if ss, ok := sel.(*SelectStmt); ok {
			stmt.SelectSource = ss
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
					if p.cur().Kind == TokenSymbol && p.cur().Value == "(" { depth++ }
					if p.cur().Kind == TokenSymbol && p.cur().Value == ")" { depth-- }
					if depth > 0 { p.advance() }
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			}
		}
		// FOR VALUES ...
		if p.acceptKeyword(KwFor) {
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
			} else if p.acceptIdentKeyword("from") {
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
		// Table-level constraint: PRIMARY KEY ( cols ).
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
			p.advance() // CONSTRAINT
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
		} else {
			col, err := p.parseColumnDef()
			if err != nil {
				return nil, err
			}
			stmt.Columns = append(stmt.Columns, col)
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
		// Parse column names, skipping optional operator class names. M0097-0015.
		var keyCols []string
		for {
			col, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			keyCols = append(keyCols, identText(col))
			// Optional operator class name (e.g. part_test_int4_ops) — skip it.
			if p.cur().Kind == TokenIdent {
				p.advance() // operator class name
			}
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		stmt.PartitionBy = &PartitionByClause{pos: pos, Method: method, KeyCols: keyCols}
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
			return
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith:
			p.advance()
			_, _ = p.parseWithOptions()
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTablespace:
			p.advance()
			_, _ = p.parseIdent()
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
		// GENERATED ALWAYS AS (expr) STORED  (M0096-0008)
		case p.acceptIdentKeyword("generated"):
			if !p.acceptIdentKeyword("always") {
				return ColumnDef{}, p.errAtCur("expected ALWAYS after GENERATED")
			}
			if _, err := p.expectKeyword(KwAs); err != nil {
				return ColumnDef{}, err
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
		// DEFAULT clause — skip for generated columns context
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault:
			p.advance()
			// Skip the default expression (consume tokens until ',', ')', or ';')
			depth := 0
			for p.cur().Kind != TokenEOF {
				t := p.cur()
				if t.Kind == TokenSymbol && t.Value == "(" {
					depth++
				} else if t.Kind == TokenSymbol && t.Value == ")" {
					if depth == 0 {
						break
					}
					depth--
				} else if t.Kind == TokenSymbol && (t.Value == "," || t.Value == ";") && depth == 0 {
					break
				}
				p.advance()
			}
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
			p.advance() // CONSTRAINT
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
			if p.acceptIdentKeyword("with") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamptz"
			} else if p.acceptIdentKeyword("without") {
				p.acceptIdentKeyword("time")
				p.acceptIdentKeyword("zone")
				ct.Name = "timestamp"
			}
		case "time":
			if p.acceptIdentKeyword("with") {
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
	cols, err := p.parseIndexColumnList()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols
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
	// Optional storage parameters WITH (…) — accept and discard.
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
				}
				p.advance()
			}
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
// For expression entries the column name is stored as "" (the important
// thing is the parser doesn't crash). Simple column names are stored
// verbatim.
func (p *parser) parseIndexColumnList() ([]string, error) {
	var cols []string
	for {
		var colName string
		// Expression column: starts with ident followed by '('
		// e.g. lower(fruit)
		if p.cur().Kind == TokenIdent && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			// Parse and discard the expression.
			_, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			colName = "" // expression — no simple column name
		} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			// Parenthesised expression: (expr)
			_, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			colName = ""
		} else {
			tok, err := p.parseIdent()
			if err != nil {
				return nil, err
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
			// This is the opclass name — skip it.
			p.advance()
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
		if !p.acceptSymbol(",") {
			break
		}
		// Stop if we hit the closing paren (empty trailing comma not expected
		// but be safe).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			break
		}
	}
	return cols, nil
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
	// Handle ident-based DROP targets as compatibility stubs. M0097-0008.
	for _, objType := range []string{
		"sequence", "schema",
		"aggregate", "collation", "operator", "cast",
		"materialized", "rule", "extension", "server",
		"language", "access", "event", "transform",
		"group", "role", "user",
	} {
		if p.acceptIdentKeyword(objType) {
			// "materialized view" is two words
			if objType == "materialized" {
				_ = p.acceptIdentKeyword("view")
			}
			ifExists, names, behavior, err := p.parseDropTail()
			if err != nil {
				return nil, err
			}
			return &DropCompatStmt{
				pos:      t.Pos,
				ObjType:  objType,
				IfExists: ifExists,
				Names:    names,
				Behavior: behavior,
			}, nil
		}
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

//   name BEFORE|AFTER|INSTEAD OF event[, ...] ON table
//   FOR [EACH] {ROW|STATEMENT}
//   EXECUTE {FUNCTION|PROCEDURE} funcname([]);
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
	// ALTER VIEW / SCHEMA / INDEX / FUNCTION / PROCEDURE / AGGREGATE /
	// COLLATION / DOMAIN / EXTENSION / LANGUAGE / OPERATOR / PUBLICATION /
	// SUBSCRIPTION / SYSTEM — compatibility stubs. Consume until end of
	// statement and return an empty AlterTableStmt (executor no-ops it).
	for _, objIdent := range []string{
		"schema", "view", "index", "function", "procedure",
		"aggregate", "collation", "domain", "extension", "language",
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
	// RENAME TO new_name — parse as a no-op.
	if p.acceptIdentKeyword("rename") {
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
		_, _ = p.parseIdent()
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
	// DROP COLUMN — parse as no-op (consume rest).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDrop {
		// consume rest of statement until ';' or EOF
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
				break
			}
			p.advance()
		}
		return stmt, nil
	}
	// ALTER COLUMN — parse as no-op (consume rest).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAlter {
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
								for _, c := range t.Value { n = n*10 + int64(c-'0') }
								poc.Modulus = n
							}
						} else if p.acceptIdentKeyword("remainder") {
							if t := p.cur(); t.Kind == TokenIntLit {
								p.advance()
								n := int64(0)
								for _, c := range t.Value { n = n*10 + int64(c-'0') }
								poc.Remainder = n
							}
						} else { p.advance() }
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
			} else if p.acceptIdentKeyword("from") {
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
	default:
		// ADD [COLUMN] column_def — bare ident or COLUMN keyword.
		if act.ConstraintName != "" {
			return AlterTableAction{}, p.errAtCur("expected PRIMARY KEY or FOREIGN KEY after CONSTRAINT name")
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
	if p.acceptIdentKeyword("add") {
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
	// Any other ALTER TYPE variant (RENAME, OWNER TO, etc.) — consume as stub.
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
					if err := p.skipParenExpr(); err != nil {
						return nil, err
					}
				}
				continue
			case KwCheck:
				p.advance()
				if err := p.skipParenExpr(); err != nil {
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

// parseTruncate: TRUNCATE [TABLE] name [, …] [CASCADE|RESTRICT].
func (p *parser) parseTruncate() (Stmt, error) {
	t, err := p.expectKeyword(KwTruncate)
	if err != nil {
		return nil, err
	}
	_ = p.acceptKeyword(KwTable) // optional
	names, err := p.parseObjectList()
	if err != nil {
		return nil, err
	}
	stmt := &TruncateStmt{pos: t.Pos, Names: names}
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}
