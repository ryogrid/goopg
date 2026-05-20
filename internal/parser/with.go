package parser

// WITH clause (CTE) parsing — M0016-0001 Stage A step 1. Adds the
// AST-side parser plumbing for `WITH [RECURSIVE] cte_name [(col,
// ...)] AS (SELECT ...) [, ...] {SELECT|INSERT|UPDATE|DELETE} ...`.
// Analyzer name resolution and planner / executor integration land
// in subsequent slices.
//
// See docs/design/0016-0001-with-parser-ast-and-name-resolution.md.

// parseWithClause parses `WITH [RECURSIVE] CTE [, CTE]*`. The
// caller (parseStatementWithCTE) already saw the leading WITH
// keyword by peeking — we re-consume it here so this helper is
// usable from any future entry point that wants WITH-prefixed
// statements (e.g. INSERT WITH).
func (p *parser) parseWithClause() (*WithClause, error) {
	t, err := p.expectKeyword(KwWith)
	if err != nil {
		return nil, err
	}
	w := &WithClause{pos: t.Pos}
	if p.acceptKeyword(KwRecursive) {
		w.Recursive = true
	}
	for {
		cte, err := p.parseCTE()
		if err != nil {
			return nil, err
		}
		w.CTEs = append(w.CTEs, cte)
		if !p.acceptSymbol(",") {
			break
		}
		// After ',' we MUST see another CTE — fail clearly here so
		// `WITH foo AS (SELECT 1), SELECT ...` produces a precise
		// "expected CTE name after ," diagnostic at the SELECT
		// keyword's position rather than a generic syntax error
		// from inside parseCTE.
		if p.cur().Kind != TokenIdent && p.cur().Kind != TokenQuotedIdent {
			return nil, p.errAtCur("expected CTE name after ','")
		}
	}
	if len(w.CTEs) == 0 {
		// Unreachable in normal flow because parseCTE would have
		// errored, but guard the invariant explicitly.
		return nil, p.errAtCur("WITH clause must declare at least one CTE")
	}
	return w, nil
}

// parseCTE parses one `cte_name [(col, ...)] AS (SELECT ...)`.
func (p *parser) parseCTE() (*CommonTableExpr, error) {
	nameTok, err := p.parseIdent()
	if err != nil {
		// Replace the generic "expected identifier" with a
		// CTE-specific message at the same position so a missing
		// name in `WITH AS ...` reads cleanly.
		return nil, &SyntaxError{Pos: p.cur().Pos, Message: "expected CTE name"}
	}
	cte := &CommonTableExpr{pos: nameTok.Pos, Name: identText(nameTok)}

	// Optional column-alias list: `(col, col, ...)`.
	if p.acceptSymbol("(") {
		for {
			ct, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			cte.Columns = append(cte.Columns, identText(ct))
			if p.acceptSymbol(",") {
				continue
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ',' or ')' in CTE column list")
			}
			break
		}
	}

	if _, err := p.expectKeyword(KwAs); err != nil {
		// expectKeyword's stock message ("expected keyword as")
		// is good enough; preserves the position at the cursor.
		return nil, err
	}
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after AS in CTE")
	}

	inner, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	switch s := inner.(type) {
	case *SelectStmt:
		cte.Query = s
	case *InsertStmt, *UpdateStmt, *DeleteStmt, *MergeStmt:
		cte.DMLBody = inner.(Stmt)
	default:
		return nil, &SyntaxError{Pos: p.cur().Pos, Message: "CTE body must be a SELECT or data-modifying statement"}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' after CTE body")
	}
	return cte, nil
}

// parseSelectWithCTE / parseInsertWithCTE / parseUpdateWithCTE /
// parseDeleteWithCTE delegate to the existing per-statement parsers
// and stamp the pre-parsed WithClause onto the resulting AST. This
// keeps the existing parsers unchanged — the With field only
// appears on statements that actually had a leading WITH.

func (p *parser) parseSelectWithCTE(with *WithClause) (Stmt, error) {
	stmt, err := p.parseSelect()
	if err != nil {
		return nil, err
	}
	sel, ok := stmt.(*SelectStmt)
	if !ok {
		return nil, p.errAtCur("WITH ... SELECT did not produce a SelectStmt")
	}
	sel.With = with
	return sel, nil
}

func (p *parser) parseInsertWithCTE(with *WithClause) (Stmt, error) {
	stmt, err := p.parseInsert()
	if err != nil {
		return nil, err
	}
	ins, ok := stmt.(*InsertStmt)
	if !ok {
		return nil, p.errAtCur("WITH ... INSERT did not produce an InsertStmt")
	}
	ins.With = with
	return ins, nil
}

func (p *parser) parseUpdateWithCTE(with *WithClause) (Stmt, error) {
	stmt, err := p.parseUpdate()
	if err != nil {
		return nil, err
	}
	upd, ok := stmt.(*UpdateStmt)
	if !ok {
		return nil, p.errAtCur("WITH ... UPDATE did not produce an UpdateStmt")
	}
	upd.With = with
	return upd, nil
}

func (p *parser) parseDeleteWithCTE(with *WithClause) (Stmt, error) {
	stmt, err := p.parseDelete()
	if err != nil {
		return nil, err
	}
	del, ok := stmt.(*DeleteStmt)
	if !ok {
		return nil, p.errAtCur("WITH ... DELETE did not produce a DeleteStmt")
	}
	del.With = with
	return del, nil
}
