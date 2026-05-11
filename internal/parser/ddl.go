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
		keyCols, err := p.parseColumnNameList()
		if err != nil {
			return nil, err
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
	if p.acceptSymbol(".") {
		second, err := p.parseIdent()
		if err != nil {
			return ColumnType{}, err
		}
		ct.Schema = ct.Name
		ct.Name = identText(second)
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
	cols, err := p.parseColumnNameList()
	if err != nil {
		return nil, err
	}
	stmt.Columns = cols
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	return stmt, nil
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
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, FUNCTION, PROCEDURE, or TRIGGER after DROP")
}

// parseCreateTriggerTail picks up after CREATE [CONSTRAINT] TRIGGER.
// Grammar (simplified):
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
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt.Name = name
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
