package parser

import (
	"fmt"
	"strconv"
	"strings"
)

// parseValuesStmt parses a bare VALUES (row1), (row2), ... statement.
// Used as a subquery source in FROM clauses: `FROM (VALUES ...) AS t(col)`.
// M0097-0003.
func (p *parser) parseValuesStmt() (Stmt, error) {
	t, err := p.expectKeyword(KwValues)
	if err != nil {
		return nil, err
	}
	s := &SelectStmt{pos: t.Pos}
	for {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' for VALUES row")
		}
		row, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after VALUES row")
		}
		s.ValuesRows = append(s.ValuesRows, row)
		if !p.acceptSymbol(",") {
			break
		}
	}
	return s, nil
}

// parseValuesSelect parses a VALUES statement that may be followed by
// UNION/INTERSECT/EXCEPT and/or ORDER BY/LIMIT/OFFSET. This is called
// from parseSelect when the current token is VALUES. M0097-0049.
func (p *parser) parseValuesSelect() (Stmt, error) {
	t, err := p.expectKeyword(KwValues)
	if err != nil {
		return nil, err
	}
	s := &SelectStmt{pos: t.Pos}
	for {
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' for VALUES row")
		}
		row, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after VALUES row")
		}
		s.ValuesRows = append(s.ValuesRows, row)
		if !p.acceptSymbol(",") {
			break
		}
	}
	// Handle trailing ORDER BY / LIMIT / OFFSET / FETCH FIRST.
	if p.acceptKeyword(KwOrder) {
		if !p.acceptKeyword(KwBy) {
			return nil, p.errAtCur("expected BY after ORDER")
		}
		orderBy, err := p.parseSortList()
		if err != nil {
			return nil, err
		}
		s.OrderBy = orderBy
	}
	if p.acceptKeyword(KwLimit) {
		if !p.acceptKeyword(KwAll) {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			s.Limit = e
		}
	}
	if p.acceptKeyword(KwOffset) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Offset = e
		p.acceptIdentKeyword("row", "rows")
	}
	// Handle UNION/INTERSECT/EXCEPT set-op.
	if setOp, ok, err := p.parseSetOpClause(); err != nil {
		return nil, err
	} else if ok {
		s.SetOp = setOp
		// Lift trailing ORDER BY / LIMIT / OFFSET from RHS to outermost level
		// (same lifting logic as in parseSelect). M0097-0024.
		if right := setOp.Right; right != nil && !right.Parenthesized {
			if s.OrderBy == nil && right.OrderBy != nil {
				s.OrderBy = right.OrderBy
				right.OrderBy = nil
			}
			if s.Limit == nil && right.Limit != nil {
				s.Limit = right.Limit
				right.Limit = nil
			}
			if s.Offset == nil && right.Offset != nil {
				s.Offset = right.Offset
				right.Offset = nil
			}
		}
	}
	return s, nil
}

// parseSelect parses a SELECT statement.
//
// Grammar (v0):
//
//	SELECT [DISTINCT] target_list
//	  [FROM from_item [, from_item …]]
//	  [WHERE expr]
//	  [GROUP BY expr [, expr …]] [HAVING expr]
//	  [ORDER BY sort_list]
//	  [LIMIT expr] [OFFSET expr]
//	  [{UNION|INTERSECT|EXCEPT} [ALL|DISTINCT] SELECT ...]
//
// Planner support for JOIN/group/set semantics lands separately.
func (p *parser) parseSelect() (Stmt, error) {
	// A bare VALUES(...) is a valid standalone statement in PostgreSQL.
	// When used as a subquery (SELECT * FROM (VALUES ...) AS t), the inner
	// parsing entry point is parseSelect, so we handle VALUES here.
	// We do NOT return immediately — we fall through to handle trailing
	// UNION/INTERSECT/EXCEPT and ORDER BY/LIMIT that may follow the VALUES.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwValues {
		return p.parseValuesSelect()
	}
	// TABLE tablename is shorthand for SELECT * FROM tablename. M0097-0003.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTable {
		pos := p.cur().Pos
		p.advance() // consume TABLE
		tbl, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		star := &StarExpr{pos: pos}
		rv := RangeVar{pos: pos, Schema: tbl.Schema, Name: tbl.Name}
		s := &SelectStmt{
			pos:       pos,
			Targets:   []ResTarget{{Expr: star}},
			From:      []RangeVar{rv},
			FromExprs: []FromExpr{{pos: pos, Base: rv}},
		}
		return s, nil
	}
	t, err := p.expectKeyword(KwSelect)
	if err != nil {
		return nil, err
	}
	s := &SelectStmt{pos: t.Pos}
	if p.acceptKeyword(KwDistinct) {
		// DISTINCT ON (expr [, ...]) — parse the expression list.
		// The ON keyword is reserved (KwCatReserved) so we use acceptKeyword.
		if p.acceptKeyword(KwOn) {
			if !p.acceptSymbol("(") {
				return nil, p.errAtCur("expected '(' after DISTINCT ON")
			}
			exprs, err := p.parseExprList()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' after DISTINCT ON expression list")
			}
			s.DistinctOn = exprs
			// s.Distinct stays false: deduplication is handled by DistinctOn,
			// not by a separate Distinct node.
		} else {
			// Plain SELECT DISTINCT (no ON clause).
			s.Distinct = true
		}
	}
	// Empty target list: `SELECT FROM <table>` is valid in upstream PG
	// (returns one zero-column row per source row). HammerDB writes
	// `SELECT EXISTS (SELECT FROM pg_tables WHERE …)` with this shape.
	// Also `SELECT;` (no targets, no FROM) is allowed — PG returns 1 row.
	// Also `SELECT UNION SELECT` (empty list + set-op) — M0097-0042.
	// Skip the target-list parse when the next token is FROM, ';', EOF,
	// UNION, INTERSECT, EXCEPT, ORDER, LIMIT, OFFSET, FETCH, or FOR.
	isSemiOrEOF := p.cur().Kind == TokenSymbol && p.cur().Value == ";" ||
		p.cur().Kind == TokenEOF
	isSetOpOrClause := (p.cur().Kind == TokenKeyword && (
		p.cur().Keyword == KwUnion ||
		p.cur().Keyword == KwIntersect ||
		p.cur().Keyword == KwExcept ||
		p.cur().Keyword == KwOrder ||
		p.cur().Keyword == KwLimit ||
		p.cur().Keyword == KwOffset ||
		p.cur().Keyword == KwFor)) ||
		// FETCH FIRST … is an ident keyword in this parser's classification.
		(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "fetch"))
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFrom) && !isSemiOrEOF && !isSetOpOrClause {
		tgts, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		s.Targets = tgts
	}

	// SELECT … INTO [TABLE] tablename … is PostgreSQL's old syntax for CTAS.
	// It is only permitted at the top level (not inside cursors, subqueries,
	// views, or INSERT's SELECT). M0097-0020.
	var selectIntoTable *ObjectName
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwInto {
		switch {
		case p.selectIntoCopyStop:
			// COPY inner-query mode: stop before INTO so parseCopy can detect
			// and flag CopyStmt.SelectInto for planCopy's error path. M0097-0024.
		case p.selectIntoErrMsg != "":
			// Forbidden context: produce the appropriate error at the INTO position.
			errPos := p.cur().Pos
			if p.selectIntoNoPos {
				errPos = -1
			}
			return nil, &SyntaxError{Pos: errPos, Message: p.selectIntoErrMsg, Raw: true}
		default:
			p.advance()                   // consume INTO
			_ = p.acceptKeyword(KwTable) // optional TABLE keyword
			name, err := p.parseObjectName()
			if err != nil {
				return nil, err
			}
			selectIntoTable = &name
		}
	}

	if p.acceptKeyword(KwFrom) {
		fromExprs, from, err := p.parseFromList()
		if err != nil {
			return nil, err
		}
		s.FromExprs = fromExprs
		s.From = from
	}
	if p.acceptKeyword(KwWhere) {
		w, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Where = w
	}
	if p.acceptKeyword(KwGroup) {
		if _, err := p.expectKeyword(KwBy); err != nil {
			return nil, err
		}
		list, err := p.parseGroupByElems()
		if err != nil {
			return nil, err
		}
		s.GroupBy = list
	}
	if p.acceptKeyword(KwHaving) {
		h, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Having = h
	}
	if p.acceptKeyword(KwOrder) {
		if _, err := p.expectKeyword(KwBy); err != nil {
			return nil, err
		}
		ob, err := p.parseSortList()
		if err != nil {
			return nil, err
		}
		s.OrderBy = ob
	}
	if p.acceptKeyword(KwLimit) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Limit = e
	}
	if p.acceptKeyword(KwOffset) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Offset = e
		// Optional `{ROW | ROWS}` trailer per SQL standard.
		// Both are no-ops for v0 — OFFSET applies the same way.
		p.acceptIdentKeyword("row", "rows")
		// SQL allows OFFSET to precede LIMIT (e.g. `ORDER BY x OFFSET 10 LIMIT 5`).
		// If no LIMIT was parsed before the OFFSET, check for one now.
		if s.Limit == nil {
			if p.acceptKeyword(KwLimit) {
				lim, lerr := p.parseExpr()
				if lerr != nil {
					return nil, lerr
				}
				s.Limit = lim
			}
		}
	}
	// SQL-standard `FETCH {FIRST | NEXT} [n] {ROW | ROWS} ONLY`
	// is accepted as a synonym for `LIMIT n`. Upstream allows it
	// after both LIMIT and OFFSET; v0 treats it as an alternative
	// to LIMIT — combining FETCH and LIMIT in the same SELECT is
	// an error.
	if p.acceptIdentKeyword("fetch") {
		if !p.acceptIdentKeyword("first", "next") {
			return nil, p.errAtCur("expected FIRST or NEXT after FETCH")
		}
		// Count is optional — `FETCH FIRST ROW ONLY` defaults to 1.
		var count Expr
		if !(p.cur().Kind == TokenIdent && (strings.EqualFold(p.cur().Value, "row") || strings.EqualFold(p.cur().Value, "rows"))) {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			count = e
		} else {
			count = &IntegerConst{pos: p.cur().Pos, Value: 1}
		}
		if !p.acceptIdentKeyword("row", "rows") {
			return nil, p.errAtCur("expected ROW or ROWS after FETCH count")
		}
		if p.acceptIdentKeyword("only") {
			// FETCH FIRST n ROWS ONLY — standard SQL limit.
		} else if p.acceptKeyword(KwWith) {
			// FETCH FIRST n ROWS WITH TIES — include rows that tie with the n-th row. M0097-0042.
			if !p.acceptIdentKeyword("ties") {
				return nil, p.errAtCur("expected TIES after WITH in FETCH … WITH TIES")
			}
			s.WithTies = true
		} else {
			return nil, p.errAtCur("expected ONLY or WITH TIES after FETCH … ROW(S)")
		}
		if s.Limit != nil {
			return nil, p.errAtCur("LIMIT and FETCH FIRST cannot both be specified")
		}
		s.Limit = count
		// SQL standard also allows OFFSET to follow FETCH FIRST … ONLY/WITH TIES.
		// E.g. `ORDER BY x FETCH FIRST 5 ROWS WITH TIES OFFSET 10`. M0097-0042.
		if s.Offset == nil && p.acceptKeyword(KwOffset) {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			s.Offset = e
			p.acceptIdentKeyword("row", "rows")
		}
	}
	if setOp, ok, err := p.parseSetOpClause(); err != nil {
		return nil, err
	} else if ok {
		s.SetOp = setOp
		// A trailing ORDER BY / LIMIT / OFFSET written after the final
		// set-op branch binds to the *entire* set operation (SQL §7.6),
		// not the right branch alone. parseSetOpClause parses the RHS via
		// a full recursive parseSelect, which greedily attaches them to
		// that branch; lift them up to s when s carries none of its own.
		// Chains lift bottom-up (each recursive level lifts from its RHS),
		// so the outermost SELECT ends up owning them and the planner's
		// wrapSetOpSortLimit resolves them against the combined output
		// (e.g. copyselect's `… UNION … ORDER BY 1`). M0097-0024.
		if right := setOp.Right; right != nil {
			// Only lift ORDER BY / LIMIT / OFFSET when the right branch is
			// NOT explicitly parenthesised. If the user wrote `A UNION (B
			// ORDER BY 1 LIMIT 2)`, the ORDER BY/LIMIT belong to the inner
			// `(B)` group only — they are NOT the trailing sort/limit for the
			// whole UNION result. M0097-0042.
			if !right.Parenthesized {
				if s.OrderBy == nil && right.OrderBy != nil {
					s.OrderBy = right.OrderBy
					right.OrderBy = nil
				}
				if s.Limit == nil && right.Limit != nil {
					s.Limit = right.Limit
					right.Limit = nil
				}
				if s.Offset == nil && right.Offset != nil {
					s.Offset = right.Offset
					right.Offset = nil
				}
			}
		}
	}
	// Trailing locking clause(s) — `FOR UPDATE / FOR SHARE [OF
	// …] [NOWAIT | SKIP LOCKED]`. M0021-0001 step 1 (parser
	// only). Multiple clauses with different OF lists / wait
	// policies are allowed per upstream.
	for p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFor {
		lc, err := p.parseLockingClause()
		if err != nil {
			return nil, err
		}
		s.Locking = append(s.Locking, lc)
	}
	if err := RewriteIndirectionStarTargets(s, nil); err != nil {
		return nil, err
	}
	// SELECT INTO: wrap the SelectStmt in a CreateTableStmt to be handled
	// identically to CTAS by the planner/executor. M0097-0020.
	if selectIntoTable != nil {
		return &CreateTableStmt{
			pos:          s.pos,
			Name:         *selectIntoTable,
			SelectSource: s,
		}, nil
	}
	return s, nil
}

// RewriteIndirectionStarTargets scans the SELECT's target list for
// `(srf(...)).*` IndirectionStar nodes and rewrites them in-place into a
// FROM-clause set-returning function reference: the SRF is appended to
// s.From under a synthetic alias `__irs_N` and the target becomes
// qualified `__irs_N.*`. The optional `onAggregate` callback fires when
// any SRF argument contains an aggregate function call — this is the
// libpqrcv `fetch_table_list` probe shape and needs ProjectSet-style
// composite-expansion support in the planner (not yet implemented). When
// nil, aggregate-arg cases return a generic SyntaxError; the planner
// supplies an onAggregate that builds a PG-compatible PlanError instead.
// M0103-0008 probe-survival.
func RewriteIndirectionStarTargets(s *SelectStmt, onAggregate func(pos int) error) error {
	for i := range s.Targets {
		is, ok := s.Targets[i].Expr.(*IndirectionStar)
		if !ok {
			continue
		}
		fc, ok := is.Source.(*FuncCall)
		if !ok {
			return &SyntaxError{Pos: is.Pos(),
				Message: "(expr).* requires a function-call source"}
		}
		hasAgg := false
		for _, a := range fc.Args {
			if exprContainsAggregateCall(a) {
				hasAgg = true
				break
			}
		}
		if hasAgg {
			// Aggregate-in-args is the libpqrcv probe shape and
			// requires ProjectSet support in the planner. The
			// parser leaves IndirectionStar in place when called
			// without an onAggregate (e.g. by parseSelect's
			// post-pass): downstream layers may still need to see
			// the AST. The planner supplies a non-nil onAggregate
			// so its own pre-analyzer call surfaces a clean
			// PG-compatible error.
			if onAggregate != nil {
				return onAggregate(is.Pos())
			}
			continue
		}
		alias := fmt.Sprintf("__irs_%d", i)
		rv := RangeVar{
			Alias: alias,
			TableFunc: &TableFuncRef{
				Name: fc.Name.String(),
				Args: fc.Args,
			},
		}
		s.From = append(s.From, rv)
		if len(s.FromExprs) > 0 {
			s.FromExprs = append(s.FromExprs, FromExpr{Base: rv})
		}
		s.Targets[i].Expr = &StarExpr{Table: alias}
	}
	return nil
}

// exprContainsAggregateCall walks e looking for a FuncCall whose name
// matches one of PostgreSQL's standard aggregate functions. The list
// mirrors planner.isAggregateFunc — kept local to the parser to keep
// the rewrite layer self-contained (the parser does not otherwise know
// "aggregate" as a category).
func exprContainsAggregateCall(e Expr) bool {
	switch x := e.(type) {
	case *FuncCall:
		if x.Over == nil && isParserAggregateName(x.Name.Name) {
			return true
		}
		for _, a := range x.Args {
			if exprContainsAggregateCall(a) {
				return true
			}
		}
	case *BinaryOp:
		return exprContainsAggregateCall(x.Left) || exprContainsAggregateCall(x.Right)
	case *UnaryOp:
		return exprContainsAggregateCall(x.Operand)
	case *CastExpr:
		return exprContainsAggregateCall(x.Operand)
	case *IsNullExpr:
		return exprContainsAggregateCall(x.Operand)
	case *IsBoolExpr:
		return exprContainsAggregateCall(x.Operand)
	case *IndirectionStar:
		return exprContainsAggregateCall(x.Source)
	}
	return false
}

func isParserAggregateName(name string) bool {
	switch strings.ToLower(name) {
	case "count", "sum", "avg", "min", "max",
		"var_pop", "var_samp", "variance", "stddev_pop", "stddev_samp", "stddev",
		"corr", "covar_pop", "covar_samp",
		"regr_count", "regr_sxx", "regr_syy", "regr_sxy",
		"regr_avgx", "regr_avgy", "regr_r2", "regr_slope", "regr_intercept",
		"bool_and", "bool_or", "every",
		"bit_and", "bit_or", "bit_xor",
		"string_agg", "array_agg", "json_agg", "jsonb_agg",
		"json_object_agg", "jsonb_object_agg",
		"xmlagg", "any_value",
		"percentile_cont", "percentile_disc", "mode":
		return true
	}
	return false
}

// parseLockingClause parses one `FOR { UPDATE | SHARE | NO KEY UPDATE |
// KEY SHARE } [ OF table_name [, …] ] [ NOWAIT | SKIP LOCKED ]` tail.
// The leading FOR keyword is the current token on entry.
//
// Locking strengths (M0096-0004):
//   - FOR UPDATE           → LockStrengthForUpdate
//   - FOR NO KEY UPDATE    → LockStrengthForNoKeyUpdate (mapped to ForUpdate in executor)
//   - FOR SHARE            → LockStrengthForShare
//   - FOR KEY SHARE        → LockStrengthForKeyShare (mapped to ForShare in executor)
func (p *parser) parseLockingClause() (*LockingClause, error) {
	t, err := p.expectKeyword(KwFor)
	if err != nil {
		return nil, err
	}
	lc := &LockingClause{pos: t.Pos}
	switch {
	case p.acceptKeyword(KwUpdate):
		lc.Strength = LockStrengthForUpdate

	case p.acceptKeyword(KwShare):
		lc.Strength = LockStrengthForShare

	// FOR KEY SHARE: peek confirms KEY SHARE sequence before consuming.
	case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwKey &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwShare:
		p.advance() // KEY
		p.advance() // SHARE
		lc.Strength = LockStrengthForKeyShare

	// FOR NO KEY UPDATE: peek confirms NO KEY UPDATE sequence.
	// "NO" is not a reserved keyword in goopg; it appears as TokenIdent.
	case (p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "no")) &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwKey &&
		p.peek(2).Kind == TokenKeyword && p.peek(2).Keyword == KwUpdate:
		p.advance() // NO
		p.advance() // KEY
		p.advance() // UPDATE
		lc.Strength = LockStrengthForNoKeyUpdate

	default:
		return nil, p.errAtCur("expected UPDATE, SHARE, KEY SHARE, or NO KEY UPDATE after FOR")
	}
	if p.acceptKeyword(KwOf) {
		first, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		lc.Targets = []string{identText(first)}
		for p.acceptSymbol(",") {
			next, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			lc.Targets = append(lc.Targets, identText(next))
		}
	}
	switch {
	case p.acceptKeyword(KwNowait):
		lc.WaitPolicy = LockWaitNoWait
	case p.acceptKeyword(KwSkip):
		if _, err := p.expectKeyword(KwLocked); err != nil {
			return nil, err
		}
		lc.WaitPolicy = LockWaitSkipLocked
	}
	return lc, nil
}

func (p *parser) parseExprList() ([]Expr, error) {
	var out []Expr
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		next, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out = append(out, next)
	}
	return out, nil
}

// parseGroupByElems parses one or more GROUP BY elements separated by commas.
// It handles GROUPING SETS (...), ROLLUP (...), and CUBE (...) by consuming
// them and injecting a sentinel IntegerConst(0) so that len(GroupBy) > 0 remains
// true — allowing downstream FOR UPDATE / HAVING checks to fire correctly.
// Regular expressions are returned normally.
func (p *parser) parseGroupByElems() ([]Expr, error) {
	var out []Expr
	for {
		// GROUPING SETS / ROLLUP / CUBE: consume the grouping element and add a
		// sentinel so the GROUP BY list is never empty after them.
		if p.acceptIdentKeyword("grouping") {
			sentPos := p.cur().Pos
			p.acceptIdentKeyword("sets")
			p.skipBalancedParens()
			out = append(out, &IntegerConst{pos: sentPos, Value: 0})
		} else if p.acceptIdentKeyword("rollup") || p.acceptIdentKeyword("cube") {
			sentPos := p.cur().Pos
			p.skipBalancedParens()
			out = append(out, &IntegerConst{pos: sentPos, Value: 0})
		} else {
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			out = append(out, expr)
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return out, nil
}

// skipBalancedParens consumes a parenthesised token sequence (including nested
// parens) that starts with the current "(" token. Does nothing if the current
// token is not "(". Used to skip unsupported grouping-set operands.
func (p *parser) skipBalancedParens() {
	if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
		return
	}
	depth := 0
	for p.cur().Kind != TokenEOF {
		if p.cur().Kind == TokenSymbol {
			switch p.cur().Value {
			case "(":
				depth++
			case ")":
				depth--
				if depth == 0 {
					p.advance()
					return
				}
			}
		}
		p.advance()
	}
}

func (p *parser) parseSetOpClause() (*SetOpClause, bool, error) {
	t := p.cur()
	if t.Kind != TokenKeyword {
		return nil, false, nil
	}
	clause := &SetOpClause{pos: t.Pos}
	switch t.Keyword {
	case KwUnion:
		clause.Type = SetOpUnion
	case KwIntersect:
		clause.Type = SetOpIntersect
	case KwExcept:
		clause.Type = SetOpExcept
	default:
		return nil, false, nil
	}
	p.advance()
	if p.acceptKeyword(KwAll) {
		clause.All = true
	} else {
		_ = p.acceptKeyword(KwDistinct)
	}
	// The RHS may itself be a parenthesised compound query.
	var rhsStmt Stmt
	var err error
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		rhsStmt, err = p.parseParenthesisedSelectStmt()
	} else {
		rhsStmt, err = p.parseSelect()
	}
	if err != nil {
		return nil, false, err
	}
	rhs, ok := rhsStmt.(*SelectStmt)
	if !ok {
		return nil, false, p.errAtCur("expected SELECT after set operation")
	}
	clause.Right = rhs
	return clause, true, nil
}

// parseParenthesisedSelectStmt handles a top-level compound query where the
// first branch is wrapped in parentheses, e.g.:
//
//	(SELECT a FROM t1 EXCEPT SELECT a FROM t2) UNION ALL (SELECT a FROM t3)
//
// PostgreSQL allows any branch of a set-operation to be parenthesised.
// We handle this at the statement level by consuming '(' ... ')' and then
// looking for a trailing UNION / INTERSECT / EXCEPT clause.
func (p *parser) parseParenthesisedSelectStmt() (Stmt, error) {
	p.advance() // consume '('
	// Handle nested parentheses: (((SELECT ...))) recurses here so that any
	// depth of wrapping works. M0097-0042.
	var inner Stmt
	var err error
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		inner, err = p.parseParenthesisedSelectStmt()
	} else {
		inner, err = p.parseSelect()
	}
	if err != nil {
		return nil, err
	}
	innerSel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, p.errAtCur("expected SELECT inside parentheses")
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' after SELECT in parenthesised query")
	}
	// Mark that this SelectStmt came from an explicit parenthesised expression
	// so the planner's left-associativity flattening stops here (the content
	// was explicitly grouped and must be treated as an atomic unit). M0097-0042.
	innerSel.Parenthesized = true
	// Optional trailing set-operation (UNION ALL / EXCEPT / INTERSECT …).
	if setOp, present, err := p.parseSetOpClause(); err != nil {
		return nil, err
	} else if present {
		// If innerSel already has a SetOp from the parenthesised content
		// (e.g. "(A EXCEPT B) UNION ALL (C ...)"), we need to attach the
		// outer set-op to the RIGHTMOST node in the existing chain rather
		// than overwriting innerSel.SetOp. This mirrors how PostgreSQL's
		// grammar builds left-to-right set-op trees:
		//   (A EXCEPT B) UNION ALL C → A.SetOp=EXCEPT→B.SetOp=UNION_ALL→C
		if innerSel.SetOp == nil {
			innerSel.SetOp = setOp
		} else {
			// Walk to the rightmost SelectStmt and attach there.
			// Count inner segments while walking — used below to record
			// InnerSegmentCount when the inner compound has ORDER BY.
			innerSegCount := 1 // the innerSel.SetOp itself is segment 1
			rightmost := innerSel.SetOp.Right
			for rightmost.SetOp != nil {
				innerSegCount++
				rightmost = rightmost.SetOp.Right
			}
			rightmost.SetOp = setOp
			// When the inner compound has ORDER BY / LIMIT / OFFSET, those
			// clauses belong to the INNER result, not the outer set-op we
			// just appended. Record InnerSegmentCount so the planner can
			// apply the sort/limit to the inner result only.
			// Example: (((A INTERSECT B ORDER BY 1))) UNION ALL C
			//   innerSegCount=1 (INTERSECT), ORDER BY is on innerSel.
			// M0097-0044.
			if innerSel.OrderBy != nil || innerSel.Limit != nil || innerSel.Offset != nil {
				innerSel.InnerSegmentCount = innerSegCount
			}
		}
		// Lift ORDER BY/LIMIT/OFFSET from right branch up to outermost level
		// (mirrors what parseSelect does for non-parenthesised set ops).
		if right := setOp.Right; right != nil {
			if innerSel.OrderBy == nil && right.OrderBy != nil {
				innerSel.OrderBy = right.OrderBy
				right.OrderBy = nil
			}
			if innerSel.Limit == nil && right.Limit != nil {
				innerSel.Limit = right.Limit
				right.Limit = nil
			}
			if innerSel.Offset == nil && right.Offset != nil {
				innerSel.Offset = right.Offset
				right.Offset = nil
			}
		}
	}
	// Optional ORDER BY / LIMIT / OFFSET after the parenthesised compound.
	if p.acceptKeyword(KwOrder) {
		if !p.acceptKeyword(KwBy) {
			return nil, p.errAtCur("expected BY after ORDER")
		}
		ob, err := p.parseSortList()
		if err != nil {
			return nil, err
		}
		innerSel.OrderBy = ob
	}
	if p.acceptKeyword(KwLimit) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		innerSel.Limit = e
	}
	if p.acceptKeyword(KwOffset) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		innerSel.Offset = e
	}
	return innerSel, nil
}

func (p *parser) parseTargetList() ([]ResTarget, error) {
	var out []ResTarget
	first, err := p.parseTargetEntry()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		next, err := p.parseTargetEntry()
		if err != nil {
			return nil, err
		}
		out = append(out, next)
	}
	return out, nil
}

func (p *parser) parseTargetEntry() (ResTarget, error) {
	pos := p.cur().Pos
	// Bare `*` target.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
		p.advance()
		return ResTarget{pos: pos, Expr: &StarExpr{pos: pos}}, nil
	}
	expr, err := p.parseExpr()
	if err != nil {
		return ResTarget{}, err
	}
	rt := ResTarget{pos: pos, Expr: expr}
	if p.acceptKeyword(KwAs) {
		// Use parseColumnAlias (not parseIdent) so reserved keywords
		// like TRUE, FALSE, NULL are accepted as explicit aliases. M0097-0003.
		alias, err := p.parseColumnAlias()
		if err != nil {
			return ResTarget{}, err
		}
		rt.Alias = identText(alias)
		return rt, nil
	}
	// Implicit alias: a bare identifier or quoted ident after the expression
	// (e.g. `pg_relation_size('x') size_after`). Only applies when the current
	// token is NOT a clause-starting keyword or comma. M0097-0003.
	if cur := p.cur(); isAliasStart(cur) {
		rt.Alias = identText(p.advance())
	}
	return rt, nil
}

func (p *parser) parseFromList() ([]FromExpr, []RangeVar, error) {
	var out []FromExpr
	var flat []RangeVar
	first, firstFlat, err := p.parseFromItem()
	if err != nil {
		return nil, nil, err
	}
	out = append(out, first)
	flat = append(flat, firstFlat...)
	for p.acceptSymbol(",") {
		next, nextFlat, err := p.parseFromItem()
		if err != nil {
			return nil, nil, err
		}
		out = append(out, next)
		flat = append(flat, nextFlat...)
	}
	return out, flat, nil
}

// tryParseParenJoin attempts to parse a parenthesized join expression of
// the form `(T1 JOIN T2 ON ...) [AS alias] [(col1, col2, ...)]`. On success
// it returns a RangeVar whose Subquery is a synthetic SELECT * FROM <join>.
// On failure (the "(" is not a valid join grouping) it restores the parser
// position and returns (zero, false, nil) so the caller can fall back.
// M0097-0059.
func (p *parser) tryParseParenJoin() (RangeVar, bool, error) {
	saved := p.idx
	pos := p.cur().Pos

	if !p.acceptSymbol("(") {
		return RangeVar{}, false, nil
	}

	// Parse the inner content as a FROM item (table + optional JOINs).
	item, flat, err := p.parseFromItem()
	if err != nil {
		// Not a valid join expression inside; restore and report failure.
		p.idx = saved
		return RangeVar{}, false, nil
	}

	// Require the matching closing ")".
	if !p.acceptSymbol(")") {
		p.idx = saved
		return RangeVar{}, false, nil
	}

	// Build a synthetic `SELECT * FROM <join>` so the caller can treat the
	// grouped join as a derived-table subquery. The SelectStmt carries both
	// the structured FromExpr (for planFromClause's explicit-JOIN branch) and
	// the flat From list (for the planner's range-var lookup).
	synSel := &SelectStmt{
		pos:       pos,
		Targets:   []ResTarget{{pos: pos, Expr: &StarExpr{pos: pos}}},
		From:      flat,
		FromExprs: []FromExpr{item},
	}

	rv := RangeVar{pos: pos, Subquery: synSel}

	// Parse optional `AS alias` or bare alias.
	if p.acceptKeyword(KwAs) {
		t, err := p.parseIdent()
		if err != nil {
			return RangeVar{}, false, err
		}
		rv.Alias = identText(t)
	} else if isAliasStart(p.cur()) {
		t := p.advance()
		rv.Alias = identText(t)
	}

	if rv.Alias == "" {
		// Provide a synthetic alias so downstream code can build a valid binding.
		rv.Alias = fmt.Sprintf("__sq_%x", pos)
	}

	// Optional column-alias list: `AS alias (col1, col2, ...)`.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance() // consume "("
		for {
			t, err := p.parseIdent()
			if err != nil {
				return RangeVar{}, false, err
			}
			rv.Columns = append(rv.Columns, identText(t))
			if p.acceptSymbol(",") {
				continue
			}
			break
		}
		if !p.acceptSymbol(")") {
			return RangeVar{}, false, p.errAtCur("expected ')' after column alias list")
		}
	}
	return rv, true, nil
}

func (p *parser) parseFromItem() (FromExpr, []RangeVar, error) {
	base, err := p.parseRangeVar()
	if err != nil {
		return FromExpr{}, nil, err
	}
	item := FromExpr{pos: base.Pos(), Base: base}
	flat := []RangeVar{base}
	for {
		join, ok, err := p.parseJoinClause()
		if err != nil {
			return FromExpr{}, nil, err
		}
		if !ok {
			break
		}
		item.Joins = append(item.Joins, join)
		flat = append(flat, join.Right)
	}
	return item, flat, nil
}

func (p *parser) parseJoinClause() (JoinExpr, bool, error) {
	t := p.cur()
	natural := p.acceptKeyword(KwNatural)
	jt := JoinInner

	switch {
	case p.acceptKeyword(KwJoin):
		jt = JoinInner
	case p.acceptKeyword(KwInner):
		jt = JoinInner
		if _, err := p.expectKeyword(KwJoin); err != nil {
			return JoinExpr{}, false, err
		}
	case p.acceptKeyword(KwLeft):
		jt = JoinLeft
		_ = p.acceptKeyword(KwOuter)
		if _, err := p.expectKeyword(KwJoin); err != nil {
			return JoinExpr{}, false, err
		}
	case p.acceptKeyword(KwRight):
		jt = JoinRight
		_ = p.acceptKeyword(KwOuter)
		if _, err := p.expectKeyword(KwJoin); err != nil {
			return JoinExpr{}, false, err
		}
	case p.acceptKeyword(KwFull):
		jt = JoinFull
		_ = p.acceptKeyword(KwOuter)
		if _, err := p.expectKeyword(KwJoin); err != nil {
			return JoinExpr{}, false, err
		}
	case p.acceptKeyword(KwCross):
		jt = JoinCross
		if _, err := p.expectKeyword(KwJoin); err != nil {
			return JoinExpr{}, false, err
		}
	default:
		if natural {
			return JoinExpr{}, false, p.errAtCur("expected JOIN after NATURAL")
		}
		_ = t
		return JoinExpr{}, false, nil
	}

	// Accept optional LATERAL keyword before the right-side range variable.
	// LATERAL allows the derived table to reference columns from earlier FROM
	// items. goopg treats it as a regular derived table (acceptable for
	// vacuumdb's use case where the lateral subquery doesn't depend on outer
	// column values at goopg's execution level).
	_ = p.acceptKeyword(KwLateral)
	right, err := p.parseRangeVar()
	if err != nil {
		return JoinExpr{}, false, err
	}
	join := JoinExpr{pos: t.Pos, Type: jt, Natural: natural, Right: right}
	if join.Type == JoinCross {
		if natural {
			return JoinExpr{}, false, &SyntaxError{Pos: t.Pos, Message: "NATURAL CROSS JOIN is not supported"}
		}
		return join, true, nil
	}
	if natural {
		return join, true, nil
	}
	if p.acceptKeyword(KwOn) {
		onExpr, err := p.parseExpr()
		if err != nil {
			return JoinExpr{}, false, err
		}
		join.On = onExpr
		return join, true, nil
	}
	if p.acceptKeyword(KwUsing) {
		if !p.acceptSymbol("(") {
			return JoinExpr{}, false, p.errAtCur("expected '('")
		}
		cols, err := p.parseColumnNameList()
		if err != nil {
			return JoinExpr{}, false, err
		}
		if !p.acceptSymbol(")") {
			return JoinExpr{}, false, p.errAtCur("expected ')'")
		}
		join.Using = cols
		return join, true, nil
	}
	return JoinExpr{}, false, p.errAtCur("expected ON or USING in JOIN")
}

func (p *parser) parseRangeVar() (RangeVar, error) {
	// Accept optional LATERAL keyword before a derived table.
	// LATERAL is silently consumed; goopg treats lateral subqueries as
	// ordinary derived tables (no correlated-outer-reference evaluation).
	_ = p.acceptKeyword(KwLateral)

	// Derived table: `(SELECT …) AS alias`. The alias is mandatory
	// in upstream PG; we mirror that. Scan past any leading `(`
	// tokens to find SELECT/VALUES/WITH/TABLE so that double-nested
	// forms like `((SELECT ...)) AS t` also work (M0097-0055).
	isSubqueryStart := false
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		for i := 0; ; i++ {
			tok := p.peek(i)
			if tok.Kind == TokenSymbol && tok.Value == "(" {
				continue // skip additional opening parens
			}
			if tok.Kind == TokenKeyword && (tok.Keyword == KwSelect || tok.Keyword == KwValues || tok.Keyword == KwWith || tok.Keyword == KwTable) {
				isSubqueryStart = true
			}
			break
		}
	}
	if isSubqueryStart {
		pos := p.cur().Pos
		// Consume ALL leading '(' tokens so that ((SELECT ...)) is handled
		// as well as the simple (SELECT ...) case. Track depth to match the
		// right number of closing ')' after the subquery. M0097-0055.
		depth := 0
		for p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance()
			depth++
		}
		// SELECT … INTO is not permitted in a derived-table subquery (M0097-0020).
		old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
		p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
		p.selectIntoNoPos = false
		inner, err := p.parseSelect()
		p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
		if err != nil {
			return RangeVar{}, err
		}
		// Consume all matching closing parens.
		for i := 0; i < depth; i++ {
			if !p.acceptSymbol(")") {
				return RangeVar{}, p.errAtCur("expected ')' after subquery in FROM")
			}
		}
		sel, ok := inner.(*SelectStmt)
		if !ok {
			return RangeVar{}, &SyntaxError{Pos: pos, Message: "subquery in FROM did not produce SELECT"}
		}
		rv := RangeVar{pos: pos, Subquery: sel}
		if p.acceptKeyword(KwAs) {
			t, err := p.parseIdent()
			if err != nil {
				return RangeVar{}, err
			}
			rv.Alias = identText(t)
		} else if isAliasStart(p.cur()) {
			t := p.advance()
			rv.Alias = identText(t)
		}
		if rv.Alias == "" {
			// PostgreSQL 16 allows omitting the alias for FROM subqueries when the
			// subquery's columns are referenced directly (not by table-qualified name).
			// Generate a synthetic alias so the planner can build a RangeVar node.
			rv.Alias = fmt.Sprintf("__sq_%x", pos)
		}
		// Optional column-alias list: (SELECT …) AS t (col1, col2, …)
		// Used by Q13-style derived tables that rename columns.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume (
			for {
				t, err := p.parseIdent()
				if err != nil {
					return RangeVar{}, err
				}
				rv.Columns = append(rv.Columns, identText(t))
				if p.acceptSymbol(",") {
					continue
				}
				break
			}
			if !p.acceptSymbol(")") {
				return RangeVar{}, p.errAtCur("expected ')' after column alias list")
			}
		}
		return rv, nil
	}
	// M0097-0059: Parenthesized join expression:
	//   FROM (T1 JOIN T2 ON ...) AS alias
	//   FROM (T1 CROSS JOIN T2) AS tx (col1, col2, ...)
	// When "(" is NOT followed by a subquery keyword, try to parse
	// it as a grouped join expression and wrap it in a synthetic
	// SELECT * FROM ... subquery.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		if rv, ok, err := p.tryParseParenJoin(); err != nil {
			return RangeVar{}, err
		} else if ok {
			return rv, nil
		}
	}
	obj, err := p.parseObjectName()
	if err != nil {
		return RangeVar{}, err
	}
	rv := RangeVar{pos: obj.pos, Schema: obj.Schema, Name: obj.Name}

	// Table-valued function call: name(arg, …) [AS alias] (M0096-0006).
	// Recognized: generate_series, pg_input_error_info, parse_ident,
	// pg_get_publication_tables (M0103-0008).
	//
	// Accept both unqualified (`pg_get_publication_tables(...)`) and
	// `pg_catalog`-qualified (`pg_catalog.pg_get_publication_tables(...)`)
	// shapes. libpqwalreceiver's `fetch_table_list` probe issued by
	// PG's CREATE SUBSCRIPTION uses the schema-qualified form inside a
	// LATERAL FROM clause; without this dispatch the parser falls
	// through to the derived-subquery branch and chokes on the `(` after
	// the function name with "expected ')' after subquery in FROM
	// (got ()". See M0103-0008 rung 13 / docs/design/0103-0019-*.
	srfFuncName := ""
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" &&
		(obj.Schema == "" || strings.EqualFold(obj.Schema, "pg_catalog")) {
		lower := strings.ToLower(obj.Name)
		switch lower {
		case "generate_series", "pg_input_error_info", "parse_ident",
			"pg_get_publication_tables":
			srfFuncName = lower
		}
	}
	if srfFuncName != "" {
		p.advance() // (
		var args []Expr
		if !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") {
			for {
				// Accept (and ignore) a leading VARIADIC marker on this argument.
				// libpqrcv's fetch_table_list probe emits the shape
				// `pg_get_publication_tables(VARIADIC array_agg(...))` against a
				// publisher (M0103-0008). The runtime operator already handles
				// either spread or scalar shapes; the parser just needs to
				// consume the keyword.
				_ = p.acceptKeyword(KwVariadic)
				e, err := p.parseExpr()
				if err != nil {
					return RangeVar{}, err
				}
				args = append(args, e)
				if !p.acceptSymbol(",") {
					break
				}
			}
		}
		if !p.acceptSymbol(")") {
			return RangeVar{}, p.errAtCur("expected ')' after function arguments")
		}
		rv.Name = ""
		rv.TableFunc = &TableFuncRef{pos: obj.pos, Name: srfFuncName, Args: args}
	}

	// `table*` syntax: the `*` means "include inheritance children".  In
	// goopg the planner always includes inheritance children when the
	// catalog has registered them, so `*` is a syntax-level no-op.
	// M0097-0046.
	_ = p.acceptSymbol("*")

	// Optional alias: AS ident [(col, ...)], or bare ident for the "implicit alias"
	// shorthand that pgbench uses (`pgbench_accounts a`).
	// The optional column alias list (SELECT ...) AS t(c1, c2) is consumed
	// and stored in rv.Columns for downstream schema renaming. M0097-0003.
	if p.acceptKeyword(KwAs) {
		t, err := p.parseIdent()
		if err != nil {
			return RangeVar{}, err
		}
		rv.Alias = identText(t)
		// Optional column alias list: AS t(c1, c2, ...).
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			for {
				colTok, cerr := p.parseIdent()
				if cerr != nil {
					return RangeVar{}, cerr
				}
				rv.Columns = append(rv.Columns, identText(colTok))
				if !p.acceptSymbol(",") {
					break
				}
			}
			if !p.acceptSymbol(")") {
				return RangeVar{}, p.errAtCur("expected ')' after column alias list")
			}
		}
		return rv, nil
	}
	if isAliasStart(p.cur()) {
		t := p.advance()
		rv.Alias = identText(t)
		// Optional column alias list after a bare (no-AS) alias:
		// `tbl alias (c1, c2, ...)` — used by table-function refs
		// like `generate_series(1,3) g(x)` and the implicit-alias
		// shorthand for ordinary tables.  Mirrors the AS-branch
		// list parser above.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance() // consume '('
			for {
				colTok, cerr := p.parseIdent()
				if cerr != nil {
					return RangeVar{}, cerr
				}
				rv.Columns = append(rv.Columns, identText(colTok))
				if !p.acceptSymbol(",") {
					break
				}
			}
			if !p.acceptSymbol(")") {
				return RangeVar{}, p.errAtCur("expected ')' after column alias list")
			}
		}
	}
	return rv, nil
}

// isAliasStart returns true when the current token can plausibly begin
// a relation alias following a from-item without an AS keyword. It
// excludes any keyword that would start the next clause, and the
// SQL-standard unreserved idents that double as clause introducers
// (`FETCH` for `FETCH FIRST n ROWS ONLY`).
func isAliasStart(t Token) bool {
	if t.Kind == TokenIdent {
		switch strings.ToLower(t.Value) {
		case "fetch":
			return false
		}
		return true
	}
	if t.Kind == TokenQuotedIdent {
		return true
	}
	if t.Kind != TokenKeyword {
		return false
	}
	switch t.Keyword {
	case KwWhere, KwGroup, KwHaving, KwOrder, KwLimit, KwOffset, KwFrom, KwBy,
		KwJoin, KwInner, KwLeft, KwRight, KwFull, KwCross, KwNatural,
		KwUnion, KwIntersect, KwExcept:
		return false
	}
	// Conservative: don't treat keywords as aliases unless we know
	// they're harmless. Upstream's "unreserved keyword" list is more
	// permissive; we tighten it here and let the analyzer relax later.
	return false
}

func (p *parser) parseSortList() ([]SortBy, error) {
	var out []SortBy
	first, err := p.parseSortItem()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		next, err := p.parseSortItem()
		if err != nil {
			return nil, err
		}
		out = append(out, next)
	}
	return out, nil
}

// skipCollationName consumes a (possibly schema-qualified) collation
// name following the COLLATE keyword, e.g. `"C"`, `pg_catalog."C"`, or
// `en_US`. The reference is discarded — goopg has no non-default
// collation support (see the COLLATE postfix in parseExprPrec).
func (p *parser) skipCollationName() error {
	if _, err := p.parseIdent(); err != nil {
		return err
	}
	for p.acceptSymbol(".") {
		if _, err := p.parseIdent(); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) parseSortItem() (SortBy, error) {
	pos := p.cur().Pos
	e, err := p.parseExpr()
	if err != nil {
		return SortBy{}, err
	}
	sb := SortBy{pos: pos, Expr: e}
	if p.acceptKeyword(KwUsing) {
		// ORDER BY expr USING operator — parse the operator name and infer direction.
		// Operators ending in ">" or the name "gt" substring → DESC; else ASC.
		op := p.parseSortUsingOperator()
		sb.UsingOp = op
		sb.Desc = sortUsingIsDesc(op)
	} else if p.acceptKeyword(KwDesc) {
		sb.Desc = true
	} else {
		_ = p.acceptKeyword(KwAsc)
	}
	// Optional NULLS FIRST / NULLS LAST (PostgreSQL extension to SQL standard).
	if p.acceptIdentKeyword("nulls") {
		if p.acceptIdentKeyword("first") {
			t := true
			sb.NullsFirst = &t
		} else if p.acceptIdentKeyword("last") {
			f := false
			sb.NullsFirst = &f
		}
		// Unknown token after NULLS — leave NullsFirst nil (use default).
	}
	return sb, nil
}

// parseSortUsingOperator consumes the operator (or identifier/qualified name) that
// follows ORDER BY expr USING.  Returns the raw operator string.
func (p *parser) parseSortUsingOperator() string {
	cur := p.cur()
	switch cur.Kind {
	case TokenOperator, TokenSymbol:
		p.advance()
		return cur.Value
	case TokenIdent:
		// Could be a simple name (varchar_lt) or schema-qualified (pg_catalog.int4lt).
		name := cur.Value
		p.advance()
		for p.cur().Kind == TokenSymbol && p.cur().Value == "." {
			p.advance()
			if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
				name += "." + p.cur().Value
				p.advance()
			}
		}
		return name
	case TokenKeyword:
		// Some operators overlap with keywords; consume the keyword token as a name.
		name := string(cur.Keyword)
		p.advance()
		return name
	}
	return ""
}

// sortUsingIsDesc returns true when op is a "greater-than"-style operator,
// meaning ORDER BY x USING op should sort descending.
func sortUsingIsDesc(op string) bool {
	// Symbol operators: > or >=
	if op == ">" || op == ">=" {
		return true
	}
	// Named operators: anything containing "gt" as a suffix or standalone.
	lower := strings.ToLower(op)
	// Strip schema prefix (pg_catalog.int4gt → int4gt).
	if idx := strings.LastIndex(lower, "."); idx >= 0 {
		lower = lower[idx+1:]
	}
	return strings.HasSuffix(lower, "gt") || strings.HasSuffix(lower, "greater") || strings.Contains(lower, "_gt_")
}

// --- Expression parsing (Pratt / precedence climbing) ---------------

// Precedence levels (higher binds tighter), aligned with upstream's
// gram.y operator precedence.
const (
	precOr       = 1
	precAnd      = 2
	precNot      = 3
	precIs       = 4
	precCompare  = 5 // = <> < > <= >=
	precBitOr    = 5 // | (same as compare in PG)
	precBitXor   = 5 // # (same as compare in PG)
	precBitAnd   = 6 // & (higher than | in PG)
	precBitShift = 6 // << >> (same as & in PG)
	// precConcat (||) must be BELOW precAddSub so that `'x' || 2+3` parses
	// as `'x' || (2+3)`, matching PostgreSQL's operator precedence table
	// where || is in the "other operators" group below + and -. M0097-0063.
	precConcat = 6 // || — below +/- (7) so `a || 2+3` → `a || (2+3)`
	precAddSub = 7
	precMulDiv = 8
	precUnary  = 10
)

// parseExpr drives the precedence-climbing loop.
func (p *parser) parseExpr() (Expr, error) {
	return p.parseExprPrec(0)
}

func (p *parser) parseExprPrec(min int) (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		// `::` typecast binds tighter than every binary operator and
		// is left-associative on the operand: `a::int + b::int` parses
		// as `(a::int) + (b::int)`. Loop here (not in the binary-op
		// switch) so it can chain like `expr::int8::text`.
		if t := p.cur(); t.Kind == TokenOperator && t.Value == "::" {
			cast, err := p.parseCastTail(left)
			if err != nil {
				return nil, err
			}
			left = cast
			continue
		}
		// `expr[index]` array subscript — handled at the same precedence
		// level as :: (tighter than binary operators).
		if t := p.cur(); t.Kind == TokenSymbol && t.Value == "[" {
			pos := t.Pos
			p.advance() // consume '['
			idx, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol("]") {
				return nil, p.errAtCur("expected ']' after array subscript")
			}
			left = &ArraySubscriptExpr{pos: pos, Base: left, Index: idx}
			continue
		}
		// `expr COLLATE collation_name` — a high-precedence postfix in
		// PG's grammar (`a_expr COLLATE any_name`), valid anywhere an
		// expression appears: target lists, WHERE, ORDER BY, etc. goopg
		// has no non-default collation machinery, so we consume the
		// collation reference and leave the operand unchanged. This is
		// exactly correct for `"C"`/`"POSIX"` (byte order == Go's default
		// string comparison) and matches the collation-skipping already
		// done in DDL/conflict-target parsing.
		if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "collate") {
			p.advance() // consume COLLATE
			if err := p.skipCollationName(); err != nil {
				return nil, err
			}
			continue
		}
		// `expr [NOT] IN (...)` is a postfix-style construct at
		// comparison precedence. Check for it before the standard
		// binary-op match so the IN keyword doesn't get parsed as
		// an unrelated identifier.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwIn {
				pos := t.Pos
				p.advance()
				inExpr, err := p.parseInTail(left, pos, false)
				if err != nil {
					return nil, err
				}
				left = inExpr
				continue
			}
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwNot &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwIn {
				pos := t.Pos
				p.advance() // NOT
				p.advance() // IN
				inExpr, err := p.parseInTail(left, pos, true)
				if err != nil {
					return nil, err
				}
				left = inExpr
				continue
			}
			// `expr [NOT] LIKE pattern` mirrors the IN handling. The
			// pattern is a comparison-precedence operand so a bare
			// string literal binds correctly without parens; nesting
			// LIKE inside arithmetic expressions still works because
			// we ascend at precCompare+1 for the rhs.
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwLike {
				pos := t.Pos
				p.advance()
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				left = &BinaryOp{pos: pos, Op: OpLike, Left: left, Right: rhs}
				continue
			}
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwNot &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwLike {
				pos := t.Pos
				p.advance() // NOT
				p.advance() // LIKE
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				left = &BinaryOp{pos: pos, Op: OpNotLike, Left: left, Right: rhs}
				continue
			}
			// ILIKE / NOT ILIKE (case-insensitive LIKE). M0097-0011.
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwIlike {
				pos := t.Pos
				p.advance()
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				left = &BinaryOp{pos: pos, Op: OpILike, Left: left, Right: rhs}
				continue
			}
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwNot &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwIlike {
				pos := t.Pos
				p.advance() // NOT
				p.advance() // ILIKE
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				left = &BinaryOp{pos: pos, Op: OpNotILike, Left: left, Right: rhs}
				continue
			}
			// POSIX regex operators: ~ ~* !~ !~* (M0097-0011).
			if t := p.cur(); t.Kind == TokenOperator {
				var op OpCode
				switch t.Value {
				case "~":
					op = OpRegexMatch
				case "~*":
					op = OpRegexIMatch
				case "!~":
					op = OpRegexNoMatch
				case "!~*":
					op = OpRegexINoMatch
				}
				if op != OpUnknown {
					pos := t.Pos
					p.advance() // consume the operator token
					rhs, err := p.parseExprPrec(precCompare + 1)
					if err != nil {
						return nil, err
					}
					left = &BinaryOp{pos: pos, Op: op, Left: left, Right: rhs}
					continue
				}
			}
			// `expr [NOT] BETWEEN low AND high` desugars to
			//   `expr >= low AND expr <= high`
			// (or wrapped in NOT for the inverse). The low and high
			// operands parse at precAnd+1 so the AND that ends the
			// construct doesn't get gobbled as a boolean conjunction
			// on the rhs of `low`. NOT BETWEEN's outer wrap is a
			// UnaryOp{NOT} so three-valued logic flows through
			// the existing evalAnd / evalNot Kleene paths.
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwBetween {
				pos := t.Pos
				p.advance()
				expanded, err := p.parseBetweenTail(left, pos, false)
				if err != nil {
					return nil, err
				}
				left = expanded
				continue
			}
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwNot &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwBetween {
				pos := t.Pos
				p.advance() // NOT
				p.advance() // BETWEEN
				expanded, err := p.parseBetweenTail(left, pos, true)
				if err != nil {
					return nil, err
				}
				left = expanded
				continue
			}
		}
		// `expr IS [NOT] NULL` — non-NULL-propagating null test.
		// Unlike unary NOT, this always returns a boolean: NULL IS NULL = true.
		// Added M0096-0004 for advisory lock WHERE clauses.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwIs {
				pos := t.Pos
				p.advance() // IS
				negated := false
				if p.acceptKeyword(KwNot) {
					negated = true
				}
				if p.acceptKeyword(KwNull) {
					left = &IsNullExpr{pos: pos, Operand: left, Negated: negated}
					continue
				}
				// IS [NOT] TRUE / FALSE / UNKNOWN. M0097-0003.
				if p.acceptKeyword(KwTrue) {
					left = &IsBoolExpr{pos: pos, Operand: left, TestTrue: true, Negated: negated}
					continue
				}
				if p.acceptKeyword(KwFalse) {
					left = &IsBoolExpr{pos: pos, Operand: left, TestFalse: true, Negated: negated}
					continue
				}
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "unknown") {
					p.advance()
					left = &IsBoolExpr{pos: pos, Operand: left, Negated: negated}
					continue
				}
				// IS [NOT] DISTINCT FROM expr — null-safe equality.
				// Semantics: a IS DISTINCT FROM b = NOT (a = b OR (a IS NULL AND b IS NULL))
				if p.acceptKeyword(KwDistinct) {
					if !p.acceptKeyword(KwFrom) {
						return nil, p.errAtCur("expected FROM after IS [NOT] DISTINCT")
					}
					right, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					left = &IsDistinctFromExpr{pos: pos, Left: left, Right: right, Negated: negated}
					continue
				}
				// Not IS NULL / IS NOT NULL — put the parser back
				// by not consuming further; produce an IS-predicate
				// error only if we consumed NOT.
				if negated {
					return nil, p.errAtCur("expected NULL, TRUE, FALSE, UNKNOWN, or DISTINCT FROM after IS NOT")
				}
				// IS <something> — not yet supported;
				// the caller will see an error on the next token.
			}
		}

		// `expr = ANY (array[...])` — desugar to `expr IN (...)`.
		// `expr != ANY (array[...])` / `expr <> ANY (...)` — desugar to
		// `expr NOT IN (...)` (semantically "at least one element is !=",
		// which for a finite non-null list equals NOT IN). M0097-0067.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenOperator && (t.Value == "=" || t.Value == "!=" || t.Value == "<>") &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwAny {
				notEq := t.Value != "="
				pos := t.Pos
				p.advance() // = / != / <>
				p.advance() // ANY
				inExpr, err := p.parseAnyTail(left, pos)
				if err != nil {
					return nil, err
				}
				if notEq {
					// != ANY → mark as NotEqualAny (OR of != comparisons),
					// not as Negated/NOT IN (AND of != comparisons). M0097-0067.
					if ie, ok := inExpr.(*InExpr); ok {
						ie.NotEqualAny = true
					}
				}
				left = inExpr
				continue
			}
		}

		// OPERATOR(schema.op) — qualified operator: desugar to its base operator.
		// Used by vacuumdb and other PostgreSQL client tools:
		// `nspname OPERATOR(pg_catalog.=) 'public'`.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "operator") &&
				p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
				if op, n := p.peekQualifiedOp(); op != OpUnknown {
					for i := 0; i < n; i++ {
						p.advance()
					}
					// OPERATOR(schema.=) ANY (array[...]) — desugar to IN.
					if op == OpEq && p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAny {
						pos := t.Pos
						p.advance() // ANY
						inExpr, err := p.parseAnyTail(left, pos)
						if err != nil {
							return nil, err
						}
						left = inExpr
						continue
					}
					right, err := p.parseExprPrec(precCompare + 1)
					if err != nil {
						return nil, err
					}
					left = &BinaryOp{pos: t.Pos, Op: op, Left: left, Right: right}
					continue
				}
			}
		}

		// `expr AT LOCAL` and `expr AT TIME ZONE zone-expr` — timezone conversion.
		// Desugar to FuncCall to avoid introducing new plan nodes:
		//   f1 AT LOCAL                    → FuncCall{Name:"timezone", Args:[f1]}
		//   f1 AT TIME ZONE 'UTC+10'       → FuncCall{Name:"timezone", Args:['UTC+10', f1]}
		//   f1 AT TIME ZONE INTERVAL '-10' → FuncCall{Name:"timezone", Args:[StringConst("-10:00"), f1]}
		// Arg order matches timetz_at_local(timetz), timetz_zone(text,timetz),
		// timetz_izone(interval,timetz) — PG convention puts zone first.
		if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "at") {
			pos := t.Pos
			p.advance() // consume AT
			if p.acceptKeyword(KwLocal) {
				left = &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: "timezone"}, Args: []Expr{left}}
				continue
			}
			// AT TIME ZONE <zone-expr>
			if p.acceptIdentKeyword("time") {
				if !p.acceptIdentKeyword("zone") {
					return nil, p.errAtCur("expected ZONE after AT TIME")
				}
				// Special-case: INTERVAL 'HH:MM' as a sub-day timezone offset.
				// The standard interval parser only handles day/month/year units, so
				// INTERVAL '00:00' / INTERVAL '-10:00' fall through here as strings.
				var zone Expr
				if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "interval") {
					if p.peek(1).Kind == TokenStringLit {
						p.advance() // INTERVAL
						strTok := p.advance() // string literal
						zone = &StringConst{pos: strTok.Pos, Value: strTok.Value}
					}
				}
				if zone == nil {
					var err error
					zone, err = p.parseExprPrec(precCompare + 1)
					if err != nil {
						return nil, err
					}
				}
				left = &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: "timezone"}, Args: []Expr{zone, left}}
				continue
			}
			return nil, p.errAtCur("expected LOCAL or TIME ZONE after AT")
		}

		op, prec, ok := p.peekBinaryOp()
		if !ok || prec < min {
			return left, nil
		}
		opTok := p.advance()
		right, err := p.parseExprPrec(prec + 1)
		if err != nil {
			return nil, err
		}
		left = &BinaryOp{pos: opTok.Pos, Op: op, Left: left, Right: right}
	}
}

// peekQualifiedOp peeks at OPERATOR(schema.op) or OPERATOR(op).
// Returns the OpCode and the number of tokens to consume (including
// OPERATOR, (, schema, ., op, )), or (OpUnknown, 0) if not recognised.
func (p *parser) peekQualifiedOp() (OpCode, int) {
	// peek(0) = OPERATOR (already confirmed by caller)
	// peek(1) = '('      (already confirmed by caller)
	i := 2
	// optional schema.
	if p.peek(i).Kind == TokenIdent && p.peek(i+1).Kind == TokenSymbol && p.peek(i+1).Value == "." {
		i += 2
	}
	opTok := p.peek(i)
	i++
	// closing )
	if p.peek(i).Kind != TokenSymbol || p.peek(i).Value != ")" {
		return OpUnknown, 0
	}
	i++ // now i is count of tokens to consume
	// map the operator symbol to OpCode
	if opTok.Kind == TokenOperator || opTok.Kind == TokenSymbol {
		switch opTok.Value {
		case "=":
			return OpEq, i
		case "<>", "!=":
			return OpNe, i
		case "<":
			return OpLt, i
		case ">":
			return OpGt, i
		case "<=":
			return OpLe, i
		case ">=":
			return OpGe, i
		}
	}
	return OpUnknown, 0
}

// parseAnyTail parses `ANY (array[e1, e2, ...])` after `=` has been consumed.
// Returns an InExpr equivalent to `left IN (e1, e2, ...)`.
func (p *parser) parseAnyTail(left Expr, pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after ANY")
	}
	var elems []Expr
	// array[e1, e2, ...] constructor form.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "array") &&
		p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "[" {
		p.advance() // array
		p.advance() // [
		if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
			p.advance()
			// Consume optional `::type[]` cast after empty ARRAY[]. M0097-0067.
			for p.cur().Kind == TokenOperator && p.cur().Value == "::" {
				dummy := &NullConst{}
				if _, err := p.parseCastTail(dummy); err != nil {
					return nil, err
				}
			}
		} else {
			first, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			elems = append(elems, first)
			for p.acceptSymbol(",") {
				next, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				elems = append(elems, next)
			}
			if !p.acceptSymbol("]") {
				return nil, p.errAtCur("expected ']'")
			}
			// Consume optional `::type[]` cast after ARRAY[...] — e.g.
			// ctid = ANY(ARRAY['(0,1)','(0,2)']::tid[]).
			// We already have the elements; the cast type is discarded since
			// InExpr coerces at runtime. M0097-0067.
			for p.cur().Kind == TokenOperator && p.cur().Value == "::" {
				dummy := &NullConst{}
				if _, err := p.parseCastTail(dummy); err != nil {
					return nil, err
				}
			}
		}
	} else {
		// Non-array expression: parse a single value.
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		elems = []Expr{inner}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	return &InExpr{pos: pos, Operand: left, Negated: false, List: elems}, nil
}

// parseCastTail consumes a single `:: typename [(typmods)]` after the
// caller has already produced the operand expression. Returns the
// CastExpr; the caller chains by re-checking for `::` after.
func (p *parser) parseCastTail(operand Expr) (Expr, error) {
	tok := p.advance() // consumes "::"
	name, err := p.parseTypeNameAfterCast()
	if err != nil {
		return nil, err
	}
	// Consume optional array suffix `[]` — treat `type[]` as the same type
	// (goopg v0 doesn't implement array types distinctly; the cast is a no-op). M0097-0003.
	for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		p.advance() // '['
		if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
			p.advance() // ']'
		}
	}
	var typmods []int64
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance()
		for {
			if p.cur().Kind != TokenIntLit {
				return nil, p.errAtCur("expected integer typmod")
			}
			n, err := strconv.ParseInt(p.cur().Value, 10, 64)
			if err != nil {
				return nil, p.errAtCur("invalid typmod")
			}
			typmods = append(typmods, n)
			p.advance()
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				p.advance()
				continue
			}
			break
		}
		if p.cur().Kind != TokenSymbol || p.cur().Value != ")" {
			return nil, p.errAtCur("expected ')' to close typmod list")
		}
		p.advance()
	}
	return &CastExpr{pos: tok.Pos, Operand: operand, Type: name, Typmods: typmods}, nil
}

// parseTypeNameAfterCast accepts an unquoted dotted name as the cast
// target. Reserved-word type aliases (int, varchar, char, timestamp
// etc.) are not yet handled — the lexer maps the common ones to
// keywords; this loop only needs the `pg_catalog.regclass` shape so
// we accept any identifier-or-keyword token that reads as a type
// name and store it in ObjectName{Schema, Name}.
func (p *parser) parseTypeNameAfterCast() (ObjectName, error) {
	first, err := p.consumeTypeIdent()
	if err != nil {
		return ObjectName{}, err
	}
	if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
		p.advance()
		second, err := p.consumeTypeIdent()
		if err != nil {
			return ObjectName{}, err
		}
		return ObjectName{Schema: first, Name: second}, nil
	}
	return ObjectName{Name: first}, nil
}

func (p *parser) consumeTypeIdent() (string, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenQuotedIdent:
		p.advance()
		return t.Value, nil
	case TokenKeyword:
		// Type-name aliases (int, integer, text, varchar, char, …) are
		// keywords in our lexer; their text is lower-cased in t.Value.
		p.advance()
		return t.Value, nil
	}
	return "", p.errAtCur("expected type name")
}

// parseCastFuncExpr parses the SQL standard CAST(expr AS type) form.
// The parser has already consumed the "cast" identifier; we are positioned
// just before the opening '('. M0097-0042.
func (p *parser) parseCastFuncExpr(pos int) (Expr, error) {
	p.advance() // consume '('
	expr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.acceptKeyword(KwAs) {
		return nil, p.errAtCur("expected AS in CAST(expr AS type)")
	}
	// Parse the target type using the same shared type-name parser as ::.
	name, err := p.parseTypeNameAfterCast()
	if err != nil {
		return nil, err
	}
	// Optional array suffix — same semantics as :: cast.
	for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		p.advance()
		if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
			p.advance()
		}
	}
	// Optional typmod list: char(4), varchar(100), numeric(10,2), etc.
	var typmods []int64
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance()
		for {
			if p.cur().Kind != TokenIntLit {
				return nil, p.errAtCur("expected integer typmod in CAST type")
			}
			n, err := strconv.ParseInt(p.cur().Value, 10, 64)
			if err != nil {
				return nil, p.errAtCur("invalid typmod in CAST type")
			}
			typmods = append(typmods, n)
			p.advance()
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				p.advance()
				continue
			}
			break
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close typmod list in CAST")
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close CAST()")
	}
	return &CastExpr{pos: pos, Operand: expr, Type: name, Typmods: typmods}, nil
}

// parseTrimFuncCall parses TRIM([LEADING|TRAILING|BOTH] [chars FROM] string)
// and normalises it to a regular ltrim/rtrim/btrim FuncCall. The "trim"
// identifier has already been consumed; we are positioned just before '('.
// M0097-0042.
func (p *parser) parseTrimFuncCall(pos int) (Expr, error) {
	p.advance() // consume '('

	// Detect direction keyword: LEADING → ltrim, TRAILING → rtrim, BOTH → btrim
	funcName := "btrim" // default
	switch {
	case p.acceptIdentKeyword("leading"):
		funcName = "ltrim"
	case p.acceptIdentKeyword("trailing"):
		funcName = "rtrim"
	case p.acceptIdentKeyword("both"):
		funcName = "btrim"
	}

	// Optional: characters-to-trim expression before FROM.
	// Detect: if next token is NOT "from", parse an expr as the chars arg.
	var charsExpr Expr
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFrom) &&
		!(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "from")) {
		var err error
		charsExpr, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}

	// Consume FROM (either keyword or identifier).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFrom {
		p.advance()
	} else if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "from") {
		p.advance()
	}

	// The string expression.
	strExpr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close TRIM()")
	}

	// Build a FuncCall equivalent. PostgreSQL exposes trim(string, chars) as
	// btrim/ltrim/rtrim(string, chars). The chars argument is optional.
	var args []Expr
	args = append(args, strExpr)
	if charsExpr != nil {
		args = append(args, charsExpr)
	}
	return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: funcName}, Args: args}, nil
}

// parseArrayConstructor parses ARRAY[e1, e2, ...] — the caller has already
// consumed the `array` identifier and the current token is `[`. M0097-0042.
func (p *parser) parseArrayConstructor(pos int) (Expr, error) {
	p.advance() // consume '['
	var elems []Expr
	if !(p.cur().Kind == TokenSymbol && p.cur().Value == "]") {
		first, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		elems = append(elems, first)
		for p.acceptSymbol(",") {
			elem, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			elems = append(elems, elem)
		}
	}
	if !p.acceptSymbol("]") {
		return nil, p.errAtCur("expected ']' after array elements")
	}
	return &ArrayConstructorExpr{pos: pos, Elements: elems}, nil
}

// peekBinaryOp returns the OpCode and precedence of the current
// token if it can extend an expression as a left-associative binary
// operator. Returns ok=false when the current token can't.
//
// M0073-0003: returns OpCode (was string) so the construction site
// at parseExprPrec can build BinaryOp without re-parsing the token
// text.
func (p *parser) peekBinaryOp() (OpCode, int, bool) {
	t := p.cur()
	switch t.Kind {
	case TokenOperator:
		switch t.Value {
		case "+":
			return OpAdd, precAddSub, true
		case "-":
			return OpSub, precAddSub, true
		case "*":
			return OpMul, precMulDiv, true
		case "/":
			return OpDiv, precMulDiv, true
		case "%":
			return OpMod, precMulDiv, true
		case "||":
			return OpConcat, precConcat, true
		case "=":
			return OpEq, precCompare, true
		case "<":
			return OpLt, precCompare, true
		case ">":
			return OpGt, precCompare, true
		case "<=":
			return OpLe, precCompare, true
		case ">=":
			return OpGe, precCompare, true
		case "<>", "!=":
			return OpNe, precCompare, true
		case "<<":
			return OpBitShiftLeft, precBitShift, true
		case ">>":
			return OpBitShiftRight, precBitShift, true
		case "&":
			return OpBitAnd, precBitAnd, true
		case "|":
			return OpBitOr, precBitOr, true
		case "#":
			return OpBitXor, precBitXor, true
		}
	case TokenSymbol:
		// '*' is also a symbol token (target-list wildcard) — but in
		// expression context it's a multiplication operator.
		if t.Value == "*" {
			return OpMul, precMulDiv, true
		}
	case TokenKeyword:
		switch t.Keyword {
		case KwAnd:
			return OpAnd, precAnd, true
		case KwOr:
			return OpOr, precOr, true
		}
	}
	return OpUnknown, 0, false
}

// parseUnary handles prefix operators and falls through to parsePrimary.
func (p *parser) parseUnary() (Expr, error) {
	t := p.cur()
	switch {
	case t.Kind == TokenOperator && t.Value == "-":
		p.advance()
		operand, err := p.parseExprPrec(precUnary)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: OpUnaryNeg, Operand: operand}, nil
	case t.Kind == TokenOperator && t.Value == "+":
		p.advance()
		operand, err := p.parseExprPrec(precUnary)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: OpUnaryPos, Operand: operand}, nil
	case t.Kind == TokenKeyword && t.Keyword == KwNot:
		p.advance()
		operand, err := p.parseExprPrec(precNot)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: OpNot, Operand: operand}, nil
	}
	return p.parsePrimary()
}

// parsePrimary handles the leaves: literals, parameters, identifier
// references (possibly qualified, possibly function calls), and
// parenthesised subexpressions.
func (p *parser) parsePrimary() (Expr, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIntLit:
		p.advance()
		return parseIntLiteralExpr(t)
	case TokenNumericLit:
		p.advance()
		return &NumericConst{pos: t.Pos, Value: t.Value}, nil
	case TokenStringLit:
		p.advance()
		return &StringConst{pos: t.Pos, Value: t.Value}, nil
	case TokenParam:
		p.advance()
		n, err := strconv.Atoi(t.Value)
		if err != nil || n <= 0 {
			return nil, &SyntaxError{Pos: t.Pos, Message: "invalid parameter number: $" + t.Value}
		}
		return &ParamRef{pos: t.Pos, Number: n}, nil
	case TokenSymbol:
		if t.Value == "(" {
			p.advance()
			// `( SELECT … )` is a subquery expression. v0
			// supports only the scalar form (one column, at
			// most one row at evaluation time); IN / EXISTS
			// have their own grammars and are deferred to a
			// follow-up loop.
			if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSelect {
				// SELECT … INTO is not permitted in a scalar subquery (M0097-0020).
				old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
				p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
				p.selectIntoNoPos = false
				inner, err := p.parseSelect()
				p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
				if err != nil {
					return nil, err
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')' to close subquery")
				}
				sel, ok := inner.(*SelectStmt)
				if !ok {
					return nil, &SyntaxError{Pos: t.Pos, Message: "subquery did not produce SELECT"}
				}
				return &SubqueryExpr{pos: t.Pos, Inner: sel}, nil
			}
			inner, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
			// `(expr).*` — composite-record star expansion. Upstream's
			// libpqrcv `fetch_table_list` emits this shape against an
			// SRF returning a composite type. Recognised here so the
			// planner can route into a set-expanding plan. M0103-0008.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
				if nxt := p.peek(1); nxt.Kind == TokenSymbol && nxt.Value == "*" {
					p.advance() // consume `.`
					p.advance() // consume `*`
					return &IndirectionStar{pos: t.Pos, Source: inner}, nil
				}
			}
			return inner, nil
		}
	case TokenKeyword:
		switch t.Keyword {
		case KwNull:
			p.advance()
			return &NullConst{pos: t.Pos}, nil
		case KwTrue:
			p.advance()
			return &BooleanConst{pos: t.Pos, Value: true}, nil
		case KwFalse:
			p.advance()
			return &BooleanConst{pos: t.Pos, Value: false}, nil
		case KwCase:
			return p.parseCaseExpr()
		case KwExists:
			return p.parseExistsExpr(false)
		}
	}
	if t.Kind == TokenIdent || t.Kind == TokenQuotedIdent {
		// Typed-literal sugar: `date '...'`, `timestamp '...'`,
		// `timestamptz '...'`, `interval 'N' unit`. Recognised
		// only when the type name is unquoted and immediately
		// followed by a TokenStringLit. Anything else falls
		// through to the normal column / function-call parse.
		if t.Kind == TokenIdent {
			if lit, ok := p.tryTypedLiteral(); ok {
				return lit, nil
			}
		}
		return p.parseColumnOrCall()
	}
	// Unreserved keywords (KwCatUnreserved / KwCatColName) may appear as
	// column references in expressions. Example: `lower(key)` where `key`
	// is a column name that happens to be an unreserved SQL keyword.
	if t.Kind == TokenKeyword && IsColNameKeyword(t.Keyword) {
		return p.parseColumnOrCall()
	}
	return nil, p.errAtCur("expected expression")
}

// tryTypedLiteral peeks at the current ident and the token after
// it; if the pair matches `<typename> '...'`, it consumes both
// (and the trailing unit ident for INTERVAL) and returns the
// typed literal. Otherwise it leaves the parser position
// unchanged and returns ok=false so the caller can fall back to
// the normal column/function-call parse.
func (p *parser) tryTypedLiteral() (Expr, bool) {
	t := p.cur()
	name := strings.ToLower(identText(t))
	switch name {
	case "date", "timestamp", "timestamptz", "time", "timetz",
		// Scalar types for `typename 'string'` cast syntax (M0097-0003).
		"bool", "boolean",
		"int2", "int4", "int8", "smallint", "integer", "bigint",
		"float4", "float8", "real",
		"numeric", "decimal",
		"text", "varchar", "char", "bpchar",
		"name", "oid", "pg_lsn":
		// Handle multi-word type names: "TIME WITH TIME ZONE 'lit'" and
		// "TIMESTAMP WITH TIME ZONE 'lit'" / "WITHOUT TIME ZONE 'lit'".
		// Layout: peek(0)=cur(time/timestamp) peek(1)=with/without peek(2)=time peek(3)=zone peek(4)=string
		if name == "time" || name == "timestamp" {
			tok1 := p.peek(1)
			tok2 := p.peek(2)
			tok3 := p.peek(3)
			tok4 := p.peek(4)
			isWithKw := tok1.Kind == TokenKeyword && tok1.Keyword == KwWith
			isWithoutIdent := tok1.Kind == TokenIdent && strings.EqualFold(tok1.Value, "without")
			if (isWithKw || isWithoutIdent) && tok4.Kind == TokenStringLit &&
				tok2.Kind == TokenIdent && strings.EqualFold(tok2.Value, "time") &&
				tok3.Kind == TokenIdent && strings.EqualFold(tok3.Value, "zone") {
				typName := name + "tz"
				if isWithoutIdent {
					typName = name // WITHOUT TIME ZONE = plain time/timestamp
				}
				pos := t.Pos
				p.advance() // type name (time/timestamp)
				p.advance() // WITH/WITHOUT
				p.advance() // time
				p.advance() // zone
				strTok := p.advance() // string literal
				return &TypedStringLit{pos: pos, Type: typName, Value: strTok.Value}, true
			}
		}
		next := p.peek(1)
		if next.Kind != TokenStringLit {
			return nil, false
		}
		p.advance() // consume type ident
		strTok := p.advance()
		return &TypedStringLit{pos: t.Pos, Type: name, Value: strTok.Value}, true
	case "interval":
		next := p.peek(1)
		if next.Kind != TokenStringLit {
			return nil, false
		}
		// Form 1: `interval '<N>' <unit>` — three tokens with the
		// unit as a trailing identifier. v0's original shape.
		unitTok := p.peek(2)
		if unitTok.Kind == TokenIdent {
			unit := strings.ToLower(identText(unitTok))
			switch unit {
			case "day", "days", "month", "months", "year", "years":
				canonical := strings.TrimSuffix(unit, "s")
				p.advance() // INTERVAL
				strTok := p.advance()
				p.advance() // unit
				return &IntervalLit{pos: t.Pos, Value: strTok.Value, Unit: canonical}, true
			}
		}
		// Form 2: `interval '<N> <unit>'` — two tokens with the
		// unit embedded in the string literal. HammerDB's TPC-H
		// query templates use this form (e.g.
		// `interval '90 day'`, `interval '1 year'`,
		// `interval '3 month'`). Parse the string body so
		// downstream IntervalLit handling stays unchanged.
		if v, u, ok := splitEmbeddedInterval(next.Value); ok {
			p.advance() // INTERVAL
			p.advance() // string literal
			return &IntervalLit{pos: t.Pos, Value: v, Unit: u}, true
		}
	}
	return nil, false
}

// parseExistsExpr parses `EXISTS (subquery)`. The leading
// EXISTS keyword is the current token on entry. v0 supports
// only the parenthesised-SELECT form; row-constructor /
// scalar-expression `EXISTS (expr)` forms are not standard SQL
// anyway. NOT is handled by the caller's NOT-prefix branch
// (parses as `UnaryOp{NOT, ExistsExpr}`).
func (p *parser) parseExistsExpr(negated bool) (Expr, error) {
	pos := p.cur().Pos
	p.advance() // EXISTS
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after EXISTS")
	}
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSelect) {
		return nil, p.errAtCur("EXISTS requires a parenthesised SELECT")
	}
	// SELECT … INTO is not permitted in an EXISTS subquery (M0097-0020).
	old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
	p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
	p.selectIntoNoPos = false
	inner, err := p.parseSelect()
	p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close EXISTS subquery")
	}
	sel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, &SyntaxError{Pos: pos, Message: "EXISTS subquery did not produce SELECT"}
	}
	return &ExistsExpr{pos: pos, Negated: negated, Subquery: sel}, nil
}

// splitEmbeddedInterval parses a SQL interval literal body where
// the magnitude and unit share a single string literal:
//
//	"90 day"    → ("90", "day", true)
//	"1 year"    → ("1", "year", true)
//	"3 months"  → ("3", "month", true)
//	"-1 day"    → ("-1", "day", true)
//
// HammerDB's TPC-H query templates use this form (Q1's
// `interval ':1 day'` after parameter substitution becomes
// `interval '90 day'`; Q5/Q6 use `interval '1 year'`; Q4 uses
// `interval '3 month'`). Whitespace tolerance and the
// plural-to-singular normalisation match the trailing-unit form
// so downstream IntervalLit handling stays uniform. Returns
// false for any string that doesn't decompose cleanly so the
// caller can fall back to non-typed-literal parsing.
func splitEmbeddedInterval(body string) (string, string, bool) {
	parts := strings.Fields(strings.TrimSpace(body))
	if len(parts) != 2 {
		return "", "", false
	}
	num, unit := parts[0], strings.ToLower(parts[1])
	switch unit {
	case "day", "days", "month", "months", "year", "years":
		return num, strings.TrimSuffix(unit, "s"), true
	}
	return "", "", false
}

// parseInTail consumes the right side of `expr [NOT] IN (...)`
// after the IN keyword. The parenthesised body is either a
// parenthesised SELECT (uncorrelated subquery) or a value list
// — disambiguated by whether the first inner token is SELECT.
func (p *parser) parseInTail(left Expr, pos int, negated bool) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after IN")
	}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSelect {
		// SELECT … INTO is not permitted in an IN subquery (M0097-0020).
		old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
		p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
		p.selectIntoNoPos = false
		inner, err := p.parseSelect()
		p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close IN subquery")
		}
		sel, ok := inner.(*SelectStmt)
		if !ok {
			return nil, &SyntaxError{Pos: pos, Message: "IN subquery did not produce SELECT"}
		}
		return &InExpr{pos: pos, Operand: left, Negated: negated, Subquery: sel}, nil
	}
	// Value list. At least one expression required.
	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	list := []Expr{first}
	for p.acceptSymbol(",") {
		next, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		list = append(list, next)
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close IN list")
	}
	return &InExpr{pos: pos, Operand: left, Negated: negated, List: list}, nil
}

// parseBetweenTail consumes `low AND high` after `[NOT] BETWEEN`
// has been advanced and rewrites the construct as an AST tree of
// existing comparison + boolean operators. We do this at parse
// time so downstream passes (analyzer, planner, executor) don't
// need to learn about BETWEEN; the comparison/boolean nodes are
// already there.
//
// `expr BETWEEN low AND high` becomes
//
//	(expr >= low) AND (expr <= high)
//
// `expr NOT BETWEEN low AND high` becomes
//
//	NOT ((expr >= low) AND (expr <= high))
//
// so SQL three-valued logic flows through the same Kleene
// evaluator as `expr >= low AND expr <= high`. The `low` and
// `high` operands parse at `precAnd + 1` so the trailing `AND`
// terminates `low` instead of being consumed as a boolean
// conjunction inside it (e.g. `BETWEEN a AND b AND c` parses as
// `BETWEEN a AND b` followed by a top-level AND with c, matching
// upstream).
func (p *parser) parseBetweenTail(left Expr, pos int, negated bool) (Expr, error) {
	low, err := p.parseExprPrec(precAnd + 1)
	if err != nil {
		return nil, err
	}
	if !p.acceptKeyword(KwAnd) {
		return nil, p.errAtCur("expected AND after BETWEEN low operand")
	}
	high, err := p.parseExprPrec(precAnd + 1)
	if err != nil {
		return nil, err
	}
	ge := &BinaryOp{pos: pos, Op: OpGe, Left: left, Right: low}
	le := &BinaryOp{pos: pos, Op: OpLe, Left: left, Right: high}
	combined := Expr(&BinaryOp{pos: pos, Op: OpAnd, Left: ge, Right: le})
	if negated {
		combined = &UnaryOp{pos: pos, Op: OpNot, Operand: combined}
	}
	return combined, nil
}

// parseCaseExpr parses both forms of CASE:
//
//	CASE WHEN cond THEN result [WHEN …] [ELSE result] END   -- searched
//	CASE expr WHEN val THEN result [WHEN …] [ELSE result] END -- simple
//
// Distinguishes by what follows `CASE`: a `WHEN` keyword starts
// the searched form; anything else is parsed as the simple-form
// operand. At least one `WHEN` clause is required (mirrors
// upstream); the `ELSE` clause is optional.
func (p *parser) parseCaseExpr() (Expr, error) {
	caseTok, err := p.expectKeyword(KwCase)
	if err != nil {
		return nil, err
	}
	out := &CaseExpr{pos: caseTok.Pos}
	// Simple form: `CASE expr WHEN val THEN result …`. The
	// searched form goes straight into WHEN.
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhen) {
		operand, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out.Operand = operand
	}
	for p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhen {
		p.advance() // WHEN
		when, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwThen); err != nil {
			return nil, err
		}
		then, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out.Whens = append(out.Whens, CaseWhen{When: when, Then: then})
	}
	if len(out.Whens) == 0 {
		return nil, p.errAtCur("CASE requires at least one WHEN clause")
	}
	if p.acceptKeyword(KwElse) {
		els, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		out.Else = els
	}
	if _, err := p.expectKeyword(KwEnd); err != nil {
		return nil, err
	}
	return out, nil
}

// parseExtractExpr parses the EXTRACT special-grammar: the
// leading `EXTRACT` ident has already been consumed; this
// helper is called when the parser is positioned on the `(`.
// Grammar: `( ident FROM expr )` where `ident` is the lower-
// case calendar component (year, month, day, …).
func (p *parser) parseExtractExpr(pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after EXTRACT")
	}
	fieldTok, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	field := strings.ToLower(identText(fieldTok))
	if _, err := p.expectKeyword(KwFrom); err != nil {
		return nil, err
	}
	source, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close EXTRACT")
	}
	return &ExtractExpr{pos: pos, Field: field, Source: source}, nil
}

// parseSubstringFuncCall parses both SUBSTRING syntax forms:
// (1) comma form: `SUBSTRING(str, start [, count])`
// (2) SQL-standard form: `SUBSTRING(str FROM start [FOR count])`
// The leading identifier has already been consumed and the parser
// is positioned on `(`. Both forms are desugared into a regular
// FuncCall with comma-separated arguments.
func (p *parser) parseSubstringFuncCall(pos int, name string) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after SUBSTRING")
	}
	str, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	// Comma form: SUBSTRING(str, start [, count])
	if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
		args := []Expr{str}
		for p.acceptSymbol(",") {
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')'")
		}
		return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: name}, Args: args}, nil
	}

	// SQL-standard form: SUBSTRING(str FROM start [FOR count])
	if _, err := p.expectKeyword(KwFrom); err != nil {
		return nil, err
	}
	start, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	args := []Expr{str, start}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFor {
		p.advance() // consume FOR
		count, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, count)
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close SUBSTRING")
	}
	return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: name}, Args: args}, nil
}

// parseColumnOrCall handles `name`, `name.name`, `name.name.name`,
// `name(args)`, `name.name(args)`, `name.*`, `name.name.*`. The
// distinction between a function call and a column reference is the
// next token being '('.
func (p *parser) parseColumnOrCall() (Expr, error) {
	first := p.advance()
	startPos := first.Pos
	parts := []string{identText(first)}
	starQualified := false
	for p.acceptSymbol(".") {
		t := p.cur()
		if t.Kind == TokenSymbol && t.Value == "*" {
			p.advance()
			starQualified = true
			break
		}
		ident, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		parts = append(parts, identText(ident))
	}
	if starQualified {
		switch len(parts) {
		case 1:
			return &StarExpr{pos: startPos, Table: parts[0]}, nil
		case 2:
			return &StarExpr{pos: startPos, Schema: parts[0], Table: parts[1]}, nil
		default:
			return nil, &SyntaxError{Pos: startPos, Message: "too many name parts before .*"}
		}
	}
	// ARRAY[e1, e2, ...] constructor — intercept before the subscript
	// path in parseExprPrec converts `array[i]` into a subscript on a
	// hypothetical column named "array". M0097-0042.
	if len(parts) == 1 && strings.EqualFold(parts[0], "array") &&
		p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		return p.parseArrayConstructor(startPos)
	}
	// Function call?
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		// EXTRACT has its own grammar: EXTRACT(field FROM expr).
		// Field is a positional identifier, not a value expr,
		// so the regular comma-arg parser would mishandle it.
		// Match case-insensitively on the bare ident form.
		if len(parts) == 1 && strings.EqualFold(parts[0], "extract") {
			return p.parseExtractExpr(startPos)
		}
		if len(parts) == 1 && (strings.EqualFold(parts[0], "substring") || strings.EqualFold(parts[0], "substr")) {
			return p.parseSubstringFuncCall(startPos, strings.ToLower(parts[0]))
		}
		// CAST(expr AS type) — SQL standard type cast syntax. The `cast`
		// identifier is not a keyword in goopg, so it falls through here as
		// a function name. Intercept it before the generic call path. M0097-0042.
		if len(parts) == 1 && strings.EqualFold(parts[0], "cast") {
			return p.parseCastFuncExpr(startPos)
		}
		// TRIM([LEADING|TRAILING|BOTH] [chars FROM] string) — SQL standard trim.
		// Intercept before the generic call path. M0097-0042.
		if len(parts) == 1 && strings.EqualFold(parts[0], "trim") {
			return p.parseTrimFuncCall(startPos)
		}
		var name ObjectName
		switch len(parts) {
		case 1:
			name = ObjectName{pos: startPos, Name: parts[0]}
		case 2:
			name = ObjectName{pos: startPos, Schema: parts[0], Name: parts[1]}
		default:
			return nil, &SyntaxError{Pos: startPos, Message: "function name has too many qualifying parts"}
		}
		return p.parseFuncCallTail(startPos, name)
	}
	// SQL standard "no-parens" niladic functions: when the bare
	// identifier is one of these and isn't being treated as a
	// function call (no following `(`), upstream parses it as a
	// niladic FuncCall not a ColumnRef. pgbench's TPC-B emits
	// `... VALUES (..., CURRENT_TIMESTAMP)` and relies on this
	// shape.
	if len(parts) == 1 && isNoParenFuncName(parts[0]) {
		return &FuncCall{pos: startPos, Name: ObjectName{pos: startPos, Name: parts[0]}}, nil
	}
	// Column reference.
	switch len(parts) {
	case 1:
		return &ColumnRef{pos: startPos, Column: parts[0]}, nil
	case 2:
		return &ColumnRef{pos: startPos, Table: parts[0], Column: parts[1]}, nil
	case 3:
		return &ColumnRef{pos: startPos, Schema: parts[0], Table: parts[1], Column: parts[2]}, nil
	}
	return nil, &SyntaxError{Pos: startPos, Message: "column reference has too many name parts"}
}

// isNoParenFuncName reports whether name (already lower-cased by the
// lexer) is one of the SQL standard niladic functions that don't
// require parentheses on the call side. Mirrors upstream's
// SystemFuncName classification — we cover the ones the executor
// already knows how to evaluate.
func isNoParenFuncName(name string) bool {
	switch name {
	case "current_timestamp", "current_date", "current_time",
		"localtime", "localtimestamp",
		"current_user", "session_user", "user",
		"current_catalog", "current_schema":
		return true
	}
	return false
}

func (p *parser) parseFuncCallTail(pos int, name ObjectName) (Expr, error) {
	// '(' already on the cursor.
	p.advance()
	fc := &FuncCall{pos: pos, Name: name}
	// `f()`
	if p.acceptSymbol(")") {
		return p.maybeWindowTail(fc)
	}
	// `f(*)` — count(*) etc.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
		p.advance()
		fc.Star = true
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		return p.maybeWindowTail(fc)
	}
	if p.acceptKeyword(KwDistinct) {
		fc.Distinct = true
	}
	for {
		// Named argument: `name => value` — skip the name and use only the value.
		// PostgreSQL named arguments are positionally mapped for built-ins. M0097-0003.
		if (p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent) &&
			p.peek(1).Kind == TokenOperator && p.peek(1).Value == "=>" {
			p.advance() // skip name
			p.advance() // skip =>
		}
		// VARIADIC marker — used by libpqrcv fetch_table_list against
		// `pg_get_publication_tables(VARIADIC array_agg(...))` (M0103-0008
		// probe-survival). We record the flag in parallel to fc.Args; the
		// argument itself is still parsed as a regular expression (the array
		// passes through unchanged, which is exactly the spread-equivalent
		// shape variadic-callees expect).
		variadic := p.acceptKeyword(KwVariadic)
		// VARIADIC array[e1, e2, ...] — expand array constructor elements into
		// individual args at parse time. Used by satisfies_hash_partition tests.
		// M0097-0027.
		if variadic && p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "array") &&
			p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "[" {
			expandStart := len(fc.Args) // index of first expanded element
			p.advance() // array
			p.advance() // [
			if !(p.cur().Kind == TokenSymbol && p.cur().Value == "]") {
				first, ferr := p.parseExpr()
				if ferr != nil {
					return nil, ferr
				}
				fc.Args = append(fc.Args, first)
				fc.Variadic = append(fc.Variadic, true) // variadic-expanded
				for p.acceptSymbol(",") {
					elem, ferr := p.parseExpr()
					if ferr != nil {
						return nil, ferr
					}
					fc.Args = append(fc.Args, elem)
					fc.Variadic = append(fc.Variadic, true) // variadic-expanded
				}
			}
			if !p.acceptSymbol("]") {
				return nil, p.errAtCur("expected ']' after VARIADIC array elements")
			}
			// Optional ::type[] cast on array — capture element type and apply to each element.
			if p.cur().Kind == TokenOperator && p.cur().Value == "::" {
				castPos := p.cur().Pos
				p.advance() // ::
				elemType, _ := p.parseTypeNameAfterCast()
				// Skip optional [] array dimension suffixes
				for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
					p.advance()
					if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
						p.advance()
					}
				}
				// Wrap each expanded element with a CastExpr to propagate the element type.
				if elemType.Name != "" {
					for j := expandStart; j < len(fc.Args); j++ {
						fc.Args[j] = &CastExpr{pos: castPos, Operand: fc.Args[j], Type: ObjectName{Name: elemType.Name}}
					}
				}
			}
			if p.acceptSymbol(",") {
				continue
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ',' or ')'")
			}
			return p.maybeWindowTail(fc)
		}
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		fc.Args = append(fc.Args, arg)
		fc.Variadic = append(fc.Variadic, variadic)
		if p.acceptSymbol(",") {
			continue
		}
		// ORDER BY inside aggregate: func(expr ORDER BY sort_list)
		if p.acceptKeyword(KwOrder) {
			if !p.acceptKeyword(KwBy) {
				return nil, p.errAtCur("expected BY after ORDER")
			}
			ob, oerr := p.parseSortList()
			if oerr != nil {
				return nil, oerr
			}
			fc.OrderBy = ob
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
			return p.maybeWindowTail(fc)
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')'")
		}
		return p.maybeWindowTail(fc)
	}
}

// maybeWindowTail consumes optional `FILTER (WHERE ...)` and/or
// `OVER (...)` clauses after a function-call's closing `)`.
// FILTER (M0097-0007) stamps fc.Filter; OVER (M0020) stamps fc.Over.
func (p *parser) maybeWindowTail(fc *FuncCall) (Expr, error) {
	// FILTER (WHERE condition) — aggregate filter clause.
	if p.acceptIdentKeyword("filter") {
		if p.acceptSymbol("(") {
			if _, err := p.expectKeyword(KwWhere); err != nil {
				return nil, err
			}
			cond, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' to close FILTER clause")
			}
			fc.Filter = cond
		}
	}
	// OVER (...) window definition.
	if !(p.cur().Kind == TokenKeyword && p.cur().Keyword == KwOver) {
		return fc, nil
	}
	wd, err := p.parseWindowDef()
	if err != nil {
		return nil, err
	}
	fc.Over = wd
	return fc, nil
}

// parseWindowDef parses the `OVER ( [PARTITION BY exprs]
// [ORDER BY sortlist] )` body. Frame clauses (ROWS / RANGE /
// GROUPS) are deferred — `parseWindowDef` errors on any token
// that isn't `)` after the optional ORDER BY.
func (p *parser) parseWindowDef() (*WindowDef, error) {
	t, err := p.expectKeyword(KwOver)
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after OVER")
	}
	wd := &WindowDef{pos: t.Pos}
	if p.acceptKeyword(KwPartition) {
		if _, err := p.expectKeyword(KwBy); err != nil {
			return nil, err
		}
		exprs, err := p.parseExprList()
		if err != nil {
			return nil, err
		}
		wd.PartitionBy = exprs
	}
	if p.acceptKeyword(KwOrder) {
		if _, err := p.expectKeyword(KwBy); err != nil {
			return nil, err
		}
		sl, err := p.parseSortList()
		if err != nil {
			return nil, err
		}
		wd.OrderBy = sl
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' after window definition (frame clauses are not supported in v0)")
	}
	return wd, nil
}

// parseIntLiteral converts a TokenIntLit value to int64, handling:
//   - Binary literals: 0b[01]+ or 0B[01]+
//   - Octal literals:  0o[0-7]+ or 0O[0-7]+
//   - Hex literals:    0x[0-9a-fA-F]+ or 0X[0-9a-fA-F]+
//   - Numeric separators: underscore (_) stripped before parsing
//   - Plain decimal: base 10
//
// M0097-0003: PostgreSQL 16+ numeric literal syntax support.
func parseIntLiteral(s string) (int64, error) {
	s = strings.ReplaceAll(s, "_", "") // strip numeric separators
	if len(s) >= 2 && s[0] == '0' {
		switch s[1] {
		case 'b', 'B':
			return strconv.ParseInt(s[2:], 2, 64)
		case 'o', 'O':
			return strconv.ParseInt(s[2:], 8, 64)
		case 'x', 'X':
			return strconv.ParseInt(s[2:], 16, 64)
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

// parseIntLiteralExpr parses a TokenIntLit and returns either IntegerConst
// (fits int64) or NumericConst (overflow) as PostgreSQL does.
// M0097-0003.
func parseIntLiteralExpr(t Token) (Expr, error) {
	v, err := parseIntLiteral(t.Value)
	if err == nil {
		return &IntegerConst{pos: t.Pos, Value: v}, nil
	}
	// Overflow — emit as NumericConst preserving the original value.
	// Strip underscores and prefix for display consistency.
	s := strings.ReplaceAll(t.Value, "_", "")
	if len(s) >= 2 && s[0] == '0' {
		switch s[1] {
		case 'b', 'B':
			if u, uerr := strconv.ParseUint(s[2:], 2, 64); uerr == nil {
				return &NumericConst{pos: t.Pos, Value: strconv.FormatUint(u, 10)}, nil
			}
		case 'o', 'O':
			if u, uerr := strconv.ParseUint(s[2:], 8, 64); uerr == nil {
				return &NumericConst{pos: t.Pos, Value: strconv.FormatUint(u, 10)}, nil
			}
		case 'x', 'X':
			if u, uerr := strconv.ParseUint(s[2:], 16, 64); uerr == nil {
				return &NumericConst{pos: t.Pos, Value: strconv.FormatUint(u, 10)}, nil
			}
		}
	}
	// Decimal overflow — return the string as a numeric literal.
	return &NumericConst{pos: t.Pos, Value: s}, nil
}
