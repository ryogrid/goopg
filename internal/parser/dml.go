package parser

// parseInsert: INSERT INTO target [(col, …)] {VALUES (val, …) [, …] | SELECT …}
// [ON CONFLICT …] [RETURNING target_list].
//
// pgbench emits:
//
//	INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)
//
// M0096-0006 adds INSERT … SELECT support.  The optional ON CONFLICT
// tail (M0017-0001 step 1) parses every upstream shape.
func (p *parser) parseInsert() (Stmt, error) {
	t, err := p.expectKeyword(KwInsert)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwInto); err != nil {
		return nil, err
	}
	target, err := p.parseRangeVar()
	if err != nil {
		return nil, err
	}
	stmt := &InsertStmt{pos: t.Pos, Target: target}
	if p.acceptSymbol("(") {
		cols, err := p.parseColumnNameList()
		if err != nil {
			return nil, err
		}
		stmt.Columns = cols
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
	}
	// INSERT … SELECT | INSERT … VALUES | INSERT … DEFAULT VALUES
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSelect {
		sel, err := p.parseSelect()
		if err != nil {
			return nil, err
		}
		if ss, ok := sel.(*SelectStmt); ok {
			stmt.Select = ss
		} else {
			return nil, p.errAtCur("expected SELECT statement")
		}
	} else if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
		// M0103-0007 rung 17: `INSERT INTO t DEFAULT VALUES` — the
		// all-defaults form. The keyword pair is parsed here; the
		// planner's rewriteInsertDefaultMarkers expands it into a
		// single row of DefaultMarkers sized to the table's insertable
		// columns. A column list before DEFAULT VALUES is rejected by
		// upstream PG, but we silently accept it for forward-compat —
		// the planner will arity-check after expansion.
		p.advance() // consume DEFAULT
		if _, err := p.expectKeyword(KwValues); err != nil {
			return nil, err
		}
		stmt.DefaultValues = true
	} else {
		if _, err := p.expectKeyword(KwValues); err != nil {
			return nil, err
		}
		rows, err := p.parseValuesRows()
		if err != nil {
			return nil, err
		}
		stmt.Rows = rows
	}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn {
		oc, err := p.parseOnConflict()
		if err != nil {
			return nil, err
		}
		stmt.OnConflict = oc
	}
	if p.acceptKeyword(KwReturning) {
		ret, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = ret
	}
	return stmt, nil
}

// parseOnConflict parses the upstream `ON CONFLICT …` tail. The
// caller has already verified the next token is `ON`.
//
// Grammar:
//
//	ON CONFLICT [conflict_target] conflict_action
//	conflict_target := '(' col_name [, col_name …] ')'
//	                 | ON CONSTRAINT constraint_name
//	conflict_action := DO NOTHING
//	                 | DO UPDATE SET assign_list [WHERE expr]
//
// The constraint-name form parses but is rejected at analyze time
// in M0017 Stage A — Stage B promotes it. Likewise `excluded.col`
// references in DO UPDATE are normal qualified column refs at the
// parser level; the analyzer resolves the special pseudo-table
// scope.
func (p *parser) parseOnConflict() (*OnConflictClause, error) {
	t, err := p.expectKeyword(KwOn)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwConflict); err != nil {
		return nil, err
	}
	clause := &OnConflictClause{pos: t.Pos}

	// Optional conflict target. The two forms are disambiguated
	// by the next token: `(` → column list, `ON` → constraint
	// name. Anything else means no target — the next token must
	// be `DO`.
	switch {
	case p.acceptSymbol("("):
		tgtPos := p.cur().Pos
		cols, colExprs, err := p.parseConflictTargetColumnList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		// Optional WHERE predicate on the conflict target (partial index).
		// Parse and discard — v0 doesn't filter on partial-index predicates.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhere {
			p.advance()
			if _, werr := p.parseExpr(); werr != nil {
				return nil, werr
			}
		}
		clause.Target = &OnConflictTarget{pos: tgtPos, Columns: cols, Exprs: colExprs}
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOn:
		tgtPos := p.cur().Pos
		p.advance()
		if _, err := p.expectKeyword(KwConstraint); err != nil {
			return nil, err
		}
		name, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		clause.Target = &OnConflictTarget{pos: tgtPos, Constraint: identText(name)}
	}

	// Action: `DO NOTHING` | `DO UPDATE SET …`.
	if _, err := p.expectKeyword(KwDo); err != nil {
		return nil, err
	}
	cur := p.cur()
	if cur.Kind != TokenKeyword {
		return nil, p.errAtCur("expected NOTHING or UPDATE")
	}
	switch cur.Keyword {
	case KwNothing:
		p.advance()
		clause.Action = OnConflictNothing
	case KwUpdate:
		p.advance()
		if _, err := p.expectKeyword(KwSet); err != nil {
			return nil, err
		}
		assigns, err := p.parseAssignList()
		if err != nil {
			return nil, err
		}
		clause.Action = OnConflictUpdate
		clause.UpdateSet = assigns
		if p.acceptKeyword(KwWhere) {
			w, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			clause.UpdateWhere = w
		}
	default:
		return nil, p.errAtCur("expected NOTHING or UPDATE")
	}
	return clause, nil
}

func (p *parser) parseColumnNameList() ([]string, error) {
	var out []string
	first, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	out = append(out, identText(first))
	for p.acceptSymbol(",") {
		t, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		out = append(out, identText(t))
	}
	return out, nil
}

// parseConflictTargetColumnList parses the column list inside ON CONFLICT (…).
// Unlike parseColumnNameList it handles:
//   - expression columns: lower(col) — stored as "" in cols with the parsed
//     expression in the parallel exprs slice
//   - optional COLLATE "…" or COLLATE ident after each column/expression
//   - optional opclass name (bare ident) after the collation
//
// The stop condition is ')' or a keyword like DO/WHERE.
func (p *parser) parseConflictTargetColumnList() ([]string, []Expr, error) {
	var cols []string
	var exprs []Expr
	for {
		var colName string
		var colExpr Expr
		// Expression column: ident followed by '(' e.g. lower(fruit)
		if p.cur().Kind == TokenIdent && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			// Parse and capture the expression.
			e, err := p.parseExpr()
			if err != nil {
				return nil, nil, err
			}
			colName = ""
			colExpr = e
		} else {
			tok, err := p.parseIdent()
			if err != nil {
				return nil, nil, err
			}
			colName = identText(tok)
		}

		// Optional COLLATE "..." or COLLATE ident
		if p.acceptIdentKeyword("collate") {
			_ = p.advance()
		}

		// Optional opclass name (bare ident not followed by '(' and not a
		// reserved keyword like DO, WHERE, ',', ')')
		if p.cur().Kind == TokenIdent {
			next := p.peek(1)
			if next.Kind != TokenSymbol || (next.Value != "(" ) {
				// looks like an opclass name — skip it
				p.advance()
			}
		}

		cols = append(cols, colName)
		exprs = append(exprs, colExpr)
		if !p.acceptSymbol(",") {
			break
		}
		// Stop if we hit ')' or a keyword (DO/WHERE)
		if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
			break
		}
		if p.cur().Kind == TokenKeyword {
			kw := p.cur().Keyword
			if kw == KwDo || kw == KwWhere {
				break
			}
		}
	}
	return cols, exprs, nil
}

func (p *parser) parseValuesRows() ([][]Expr, error) {
	var rows [][]Expr
	first, err := p.parseValuesRow()
	if err != nil {
		return nil, err
	}
	rows = append(rows, first)
	for p.acceptSymbol(",") {
		next, err := p.parseValuesRow()
		if err != nil {
			return nil, err
		}
		rows = append(rows, next)
	}
	return rows, nil
}

func (p *parser) parseValuesRow() ([]Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '('")
	}
	parseCell := func() (Expr, error) {
		// rung 15 (M0103-0007): bare DEFAULT keyword in a VALUES row.
		// Substituted by planInsert with the target column's catalog
		// DefaultExpr (or NULL) — never reaches the executor.
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
			t := p.cur()
			p.advance()
			return &DefaultMarker{pos: t.Pos}, nil
		}
		return p.parseExpr()
	}
	var row []Expr
	first, err := parseCell()
	if err != nil {
		return nil, err
	}
	row = append(row, first)
	for p.acceptSymbol(",") {
		next, err := parseCell()
		if err != nil {
			return nil, err
		}
		row = append(row, next)
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	return row, nil
}

// parseUpdate: UPDATE target SET col = expr [, …] [WHERE expr]
// [RETURNING target_list].
//
// pgbench emits:
//
//	UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2
func (p *parser) parseUpdate() (Stmt, error) {
	t, err := p.expectKeyword(KwUpdate)
	if err != nil {
		return nil, err
	}
	target, err := p.parseRangeVar()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwSet); err != nil {
		return nil, err
	}
	assigns, err := p.parseAssignList()
	if err != nil {
		return nil, err
	}
	stmt := &UpdateStmt{pos: t.Pos, Target: target, Set: assigns}
	if p.acceptKeyword(KwWhere) {
		w, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = w
	}
	if p.acceptKeyword(KwReturning) {
		ret, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = ret
	}
	return stmt, nil
}

func (p *parser) parseAssignList() ([]UpdateAssign, error) {
	var out []UpdateAssign
	first, err := p.parseAssign()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		next, err := p.parseAssign()
		if err != nil {
			return nil, err
		}
		out = append(out, next)
	}
	return out, nil
}

func (p *parser) parseAssign() (UpdateAssign, error) {
	pos := p.cur().Pos
	col, err := p.parseIdent()
	if err != nil {
		return UpdateAssign{}, err
	}
	cur := p.cur()
	if cur.Kind != TokenOperator || cur.Value != "=" {
		return UpdateAssign{}, p.errAtCur("expected '='")
	}
	p.advance()
	// rung 16 (M0103-0007): bare DEFAULT keyword on the RHS of an UPDATE
	// SET assignment. Substituted by Plan() with the target column's
	// catalog DefaultExpr (or NULL) before the analyzer runs — never
	// reaches the executor. Mirrors rung 15's INSERT VALUES handling.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwDefault {
		t := p.cur()
		p.advance()
		return UpdateAssign{pos: pos, Column: identText(col), Expr: &DefaultMarker{pos: t.Pos}}, nil
	}
	expr, err := p.parseExpr()
	if err != nil {
		return UpdateAssign{}, err
	}
	return UpdateAssign{pos: pos, Column: identText(col), Expr: expr}, nil
}

// parseDelete: DELETE FROM target [WHERE expr] [RETURNING target_list].
func (p *parser) parseDelete() (Stmt, error) {
	t, err := p.expectKeyword(KwDelete)
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwFrom); err != nil {
		return nil, err
	}
	target, err := p.parseRangeVar()
	if err != nil {
		return nil, err
	}
	stmt := &DeleteStmt{pos: t.Pos, Target: target}
	if p.acceptKeyword(KwWhere) {
		w, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		stmt.Where = w
	}
	if p.acceptKeyword(KwReturning) {
		ret, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = ret
	}
	return stmt, nil
}
