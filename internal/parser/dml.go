package parser

// parseInsert: INSERT INTO target [(col, …)] VALUES (val, …) [, …]
// [RETURNING target_list].
//
// pgbench emits:
//
//	INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)
//
// so v0 supports the column list and one or more parenthesised value
// tuples. INSERT … SELECT and ON CONFLICT are deferred.
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
	if _, err := p.expectKeyword(KwValues); err != nil {
		return nil, err
	}
	rows, err := p.parseValuesRows()
	if err != nil {
		return nil, err
	}
	stmt.Rows = rows
	if p.acceptKeyword(KwReturning) {
		ret, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = ret
	}
	return stmt, nil
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
	var row []Expr
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	row = append(row, first)
	for p.acceptSymbol(",") {
		next, err := p.parseExpr()
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
