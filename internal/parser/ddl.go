package parser

import "strconv"

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
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, or FUNCTION after CREATE")
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
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '('")
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
	return stmt, nil
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
	}
	return nil, p.errAtCur("expected TABLE, INDEX, VIEW, PUBLICATION, SUBSCRIPTION, or FUNCTION after DROP")
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
