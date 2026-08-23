package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/utils/adt/similarto"
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
	// WITH ... SELECT ... inside a subquery or view body.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWith {
		return p.parseStatementWithCTE()
	}
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
	isSetOpOrClause := (p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwUnion ||
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
			p.advance()                  // consume INTO
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
		flat, sets, err := p.parseGroupByElems()
		if err != nil {
			return nil, err
		}
		s.GroupBy = flat
		s.GroupingSets = sets
	}
	if p.acceptKeyword(KwHaving) {
		h, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		s.Having = h
	}
	if p.acceptIdentKeyword("window") {
		items, err := p.parseWindowClauseList()
		if err != nil {
			return nil, err
		}
		s.WindowClause = items
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
	if err := p.parseSelectLimitClauses(s); err != nil {
		return nil, err
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
	// PostgreSQL's grammar permits select_limit to FOLLOW the
	// for_locking_clause as well: `… ORDER BY id FOR UPDATE [SKIP LOCKED |
	// NOWAIT] LIMIT n` (used by the upstream skip-locked / nowait isolation
	// specs). Accept a trailing select_limit when a locking clause was parsed
	// and no LIMIT/OFFSET/FETCH preceded it. M0118-0003.
	if len(s.Locking) > 0 && s.Limit == nil && s.Offset == nil {
		if err := p.parseSelectLimitClauses(s); err != nil {
			return nil, err
		}
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
		"percentile_cont", "percentile_disc", "mode",
		// Hypothetical-set aggregates. These are also window functions (rank, etc.),
		// but when used with WITHIN GROUP they become aggregate functions. The parser
		// cannot distinguish here since isParserAggregateName only sees the name;
		// include them so exprContainsAggregateCall fires correctly. M0097-0035.
		"rank", "dense_rank", "cume_dist", "percent_rank":
		return true
	}
	return false
}

// parseSelectLimitClauses parses the optional select_limit tail —
// `[LIMIT n] [OFFSET n [ROW|ROWS]] [LIMIT n]` plus the SQL-standard
// `FETCH {FIRST|NEXT} [n] {ROW|ROWS} {ONLY | WITH TIES}` synonym — into s.
// PostgreSQL's grammar (gram.y select_no_parens) permits this select_limit
// either before OR after a for_locking_clause, so parseSelect invokes it at
// both sites. Combining FETCH and LIMIT in one SELECT is an error.
func (p *parser) parseSelectLimitClauses(s *SelectStmt) error {
	if p.acceptKeyword(KwLimit) {
		e, err := p.parseExpr()
		if err != nil {
			return err
		}
		s.Limit = e
	}
	if p.acceptKeyword(KwOffset) {
		e, err := p.parseExpr()
		if err != nil {
			return err
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
					return lerr
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
			return p.errAtCur("expected FIRST or NEXT after FETCH")
		}
		// Count is optional — `FETCH FIRST ROW ONLY` defaults to 1.
		var count Expr
		if !(p.cur().Kind == TokenIdent && (strings.EqualFold(p.cur().Value, "row") || strings.EqualFold(p.cur().Value, "rows"))) {
			e, err := p.parseExpr()
			if err != nil {
				return err
			}
			count = e
		} else {
			count = &IntegerConst{pos: p.cur().Pos, Value: 1}
		}
		if !p.acceptIdentKeyword("row", "rows") {
			return p.errAtCur("expected ROW or ROWS after FETCH count")
		}
		if p.acceptIdentKeyword("only") {
			// FETCH FIRST n ROWS ONLY — standard SQL limit.
		} else if p.acceptKeyword(KwWith) {
			// FETCH FIRST n ROWS WITH TIES — include rows that tie with the n-th row. M0097-0042.
			if !p.acceptIdentKeyword("ties") {
				return p.errAtCur("expected TIES after WITH in FETCH … WITH TIES")
			}
			s.WithTies = true
		} else {
			return p.errAtCur("expected ONLY or WITH TIES after FETCH … ROW(S)")
		}
		if s.Limit != nil {
			return p.errAtCur("LIMIT and FETCH FIRST cannot both be specified")
		}
		s.Limit = count
		// SQL standard also allows OFFSET to follow FETCH FIRST … ONLY/WITH TIES.
		// E.g. `ORDER BY x FETCH FIRST 5 ROWS WITH TIES OFFSET 10`. M0097-0042.
		if s.Offset == nil && p.acceptKeyword(KwOffset) {
			e, err := p.parseExpr()
			if err != nil {
				return err
			}
			s.Offset = e
			p.acceptIdentKeyword("row", "rows")
		}
	}
	return nil
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

// maxGeneratedGroupingSets bounds ROLLUP/CUBE/GROUPING SETS expansion
// (CUBE(n) yields 2^n sets, and multiple grouping elements cross-multiply)
// against runaway memory from a large column list — e.g. CUBE of 20
// columns would otherwise silently attempt to build a million-branch UNION.
const maxGeneratedGroupingSets = 4096

// parseGroupByElems parses one or more GROUP BY elements separated by
// commas. flat is the flattened list of every expression that appears in
// any grouping element (unaffected shape for a plain GROUP BY — existing
// consumers such as analyzer resolution and the FOR-UPDATE/GROUP-BY check
// keep working unmodified). sets is nil unless at least one element used
// GROUPING SETS/ROLLUP/CUBE, in which case it holds the fully expanded,
// cross-multiplied grouping-set list the planner rewrites into a UNION ALL
// of plain-GROUP-BY branches (SQL:1999 §7.9). M0122-0004.
func (p *parser) parseGroupByElems() (flat []Expr, sets *GroupingSetsSpec, err error) {
	pos := p.cur().Pos
	var components [][][]Expr
	hasConstruct := false

	for {
		switch {
		case p.acceptIdentKeyword("grouping"):
			if !p.acceptIdentKeyword("sets") {
				return nil, nil, p.errAtCur("expected SETS after GROUPING")
			}
			alts, err := p.parseGroupingSetsList()
			if err != nil {
				return nil, nil, err
			}
			hasConstruct = true
			components = append(components, alts)
			for _, s := range alts {
				flat = append(flat, s...)
			}
		case p.acceptIdentKeyword("rollup"):
			units, err := p.parseGroupingUnitList()
			if err != nil {
				return nil, nil, err
			}
			alts := rollupAlternatives(units)
			hasConstruct = true
			components = append(components, alts)
			for _, u := range units {
				flat = append(flat, u...)
			}
		case p.acceptIdentKeyword("cube"):
			units, err := p.parseGroupingUnitList()
			if err != nil {
				return nil, nil, err
			}
			if len(units) > 20 {
				return nil, nil, p.errAtCur("CUBE grouping list too large")
			}
			alts := cubeAlternatives(units)
			if len(alts) > maxGeneratedGroupingSets {
				return nil, nil, p.errAtCur("CUBE expands to too many grouping sets")
			}
			hasConstruct = true
			components = append(components, alts)
			for _, u := range units {
				flat = append(flat, u...)
			}
		default:
			expr, err := p.parseExpr()
			if err != nil {
				return nil, nil, err
			}
			flat = append(flat, expr)
			components = append(components, [][]Expr{{expr}})
		}
		if !p.acceptSymbol(",") {
			break
		}
	}

	if !hasConstruct {
		return flat, nil, nil
	}
	expanded, err := cartesianProductGroupingSets(components)
	if err != nil {
		return nil, nil, p.errAtCur(err.Error())
	}
	return flat, &GroupingSetsSpec{pos: pos, Sets: expanded}, nil
}

// parseGroupingUnitList parses the parenthesised argument list of ROLLUP(...)
// or CUBE(...): a comma-separated list of "units", each either a single
// expression or a parenthesised sub-list `(e1, e2, ...)` that is tied
// together — included or excluded from a generated set as one indivisible
// group, matching upstream's multi-column ROLLUP/CUBE element semantics.
func (p *parser) parseGroupingUnitList() ([][]Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after ROLLUP/CUBE")
	}
	var units [][]Expr
	if p.acceptSymbol(")") {
		return units, nil // ROLLUP()/CUBE() — degenerate, single empty set
	}
	for {
		if p.acceptSymbol("(") {
			var unit []Expr
			if !p.acceptSymbol(")") {
				for {
					e, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					unit = append(unit, e)
					if !p.acceptSymbol(",") {
						break
					}
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			}
			units = append(units, unit)
		} else {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			units = append(units, []Expr{e})
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	return units, nil
}

// parseGroupingSetsList parses the parenthesised body of GROUPING SETS
// (...): a comma-separated list of grouping-set items, each an empty `()`,
// a parenthesised expression list `(e1, e2, ...)`, a bare expression
// (shorthand for a singleton set), or a nested ROLLUP(...)/CUBE(...) (which
// itself expands to multiple sets, all folded into this GROUPING SETS'
// result — upstream permits nesting these constructs). Returns the
// flattened list of generated sets; unlike the cross-multiplied form used
// for multiple GROUP BY elements, GROUPING SETS lists its own alternatives
// verbatim (no further cross product across its own items).
func (p *parser) parseGroupingSetsList() ([][]Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after GROUPING SETS")
	}
	var out [][]Expr
	for {
		switch {
		case p.acceptIdentKeyword("rollup"):
			units, err := p.parseGroupingUnitList()
			if err != nil {
				return nil, err
			}
			out = append(out, rollupAlternatives(units)...)
		case p.acceptIdentKeyword("cube"):
			units, err := p.parseGroupingUnitList()
			if err != nil {
				return nil, err
			}
			out = append(out, cubeAlternatives(units)...)
		case p.cur().Kind == TokenSymbol && p.cur().Value == "(":
			p.advance()
			var set []Expr
			if !p.acceptSymbol(")") {
				for {
					e, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					set = append(set, e)
					if !p.acceptSymbol(",") {
						break
					}
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
			}
			out = append(out, set)
		default:
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			out = append(out, []Expr{e})
		}
		if !p.acceptSymbol(",") {
			break
		}
		if len(out) > maxGeneratedGroupingSets {
			return nil, p.errAtCur("GROUPING SETS list too large")
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')'")
	}
	return out, nil
}

// rollupAlternatives expands ROLLUP(u1, u2, ..., un) into its n+1 prefix
// sets — the full set, then each shorter prefix down to the empty set —
// upstream's standard hierarchical rollup semantics.
func rollupAlternatives(units [][]Expr) [][]Expr {
	alts := make([][]Expr, 0, len(units)+1)
	for i := len(units); i >= 0; i-- {
		var set []Expr
		for _, u := range units[:i] {
			set = append(set, u...)
		}
		alts = append(alts, set)
	}
	return alts
}

// cubeAlternatives expands CUBE(u1, ..., un) into all 2^n subsets.
func cubeAlternatives(units [][]Expr) [][]Expr {
	n := len(units)
	total := 1 << n
	alts := make([][]Expr, 0, total)
	for mask := 0; mask < total; mask++ {
		var set []Expr
		for j := 0; j < n; j++ {
			if mask&(1<<j) != 0 {
				set = append(set, units[j]...)
			}
		}
		alts = append(alts, set)
	}
	return alts
}

// cartesianProductGroupingSets combines the alternative-set lists of every
// comma-separated GROUP BY element into the final expanded grouping-set
// list, per SQL:1999's cross-product rule for multiple grouping elements
// (e.g. `GROUP BY a, ROLLUP(b, c)` = {a} x {[b,c],[b],[]}).
func cartesianProductGroupingSets(components [][][]Expr) ([][]Expr, error) {
	sets := [][]Expr{{}}
	for _, alts := range components {
		next := make([][]Expr, 0, len(sets)*len(alts))
		for _, prefix := range sets {
			for _, alt := range alts {
				combined := make([]Expr, 0, len(prefix)+len(alt))
				combined = append(combined, prefix...)
				combined = append(combined, alt...)
				next = append(next, combined)
			}
		}
		sets = next
		if len(sets) > maxGeneratedGroupingSets {
			return nil, fmt.Errorf("GROUP BY expands to too many grouping sets")
		}
	}
	return sets, nil
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

// trailingClauseFollowsParens reports whether t continues a query after a
// `select_with_parens` operand — a set operation, or one of ORDER BY /
// LIMIT / OFFSET. Anything else (`)`, `;`, an alias, `RETURNING`, …) means the
// parenthesised query stands alone. Used by parseParenthesisedSelectStmt to
// decide whether a grouping node is needed; it looks at the same keyword set
// continuesParenthesisedQuery accepts, minus the ones that cannot introduce a
// clause parseParenthesisedSelectStmt itself parses (`)`, FOR). M0125-0020.
func trailingClauseFollowsParens(t Token) bool {
	if t.Kind != TokenKeyword {
		return false
	}
	switch t.Keyword {
	case KwUnion, KwIntersect, KwExcept, KwOrder, KwLimit, KwOffset:
		return true
	}
	return false
}

// parseParenthesisedSelectStmt handles a query whose first branch is wrapped in
// parentheses, e.g.:
//
//	(SELECT a FROM t1 EXCEPT SELECT a FROM t2) UNION ALL (SELECT a FROM t3)
//
// PostgreSQL allows any branch of a set-operation to be parenthesised. We
// consume '(' … ')' and then look for a trailing UNION / INTERSECT / EXCEPT
// clause and ORDER BY / LIMIT / OFFSET.
//
// Anything found after the ')' is attached to a fresh GROUPING node whose
// SetOpOperand is the parenthesised query — never into that query's own chain
// or sort/limit slots. That mirrors gram.y, where `select_with_parens` is a
// LEAF operand of `select_clause`, so trailing text always attaches above it.
// M0125-0020.
func (p *parser) parseParenthesisedSelectStmt() (Stmt, error) {
	lparen := p.cur().Pos
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
	// Everything innerSel holds — its chain and its own sort/limit — was
	// written inside the ')' just consumed, including anything a nested
	// parseParenthesisedSelectStmt returned (`((B) EXCEPT (C))`: the inner
	// level built a grouping node for the EXCEPT, and this outer paren really
	// does cover it). M0097-0042.
	innerSel.Parenthesized = true
	if !trailingClauseFollowsParens(p.cur()) {
		// Nothing was written after the ')': the parenthesised query is the
		// whole operand/statement and needs no grouping node above it.
		return innerSel, nil
	}
	// Text follows the ')'. It attaches ABOVE the parenthesised query, never
	// into it — `select_with_parens` is a leaf in gram.y. Give the branch its
	// own node and build the enclosing one. M0125-0020.
	grp := &SelectStmt{pos: lparen, SetOpOperand: innerSel}
	// Optional trailing set-operation (UNION ALL / EXCEPT / INTERSECT …).
	// Chains stay left-deep because each subsequent operand is parsed by
	// parseSetOpClause, which recurses back here for a parenthesised one:
	//   (A) EXCEPT (B) EXCEPT (C) → grp(A) EXCEPT grp(B) EXCEPT C.
	if setOp, present, err := p.parseSetOpClause(); err != nil {
		return nil, err
	} else if present {
		grp.SetOp = setOp
		// Lift a trailing ORDER BY / LIMIT / OFFSET off a BARE right branch:
		// parseSetOpClause parses the RHS with a full parseSelect, which
		// greedily attaches clauses that actually bind to the whole chain.
		// A parenthesised right branch keeps its own (`(A) UNION (C ORDER BY 1
		// LIMIT 1)` — PG limits C alone). Same rule, same code, as
		// parseSelect's two lift sites. M0097-0024 / M0097-0042.
		if right := setOp.Right; right != nil && !right.Parenthesized {
			if grp.OrderBy == nil && right.OrderBy != nil {
				grp.OrderBy = right.OrderBy
				right.OrderBy = nil
			}
			if grp.Limit == nil && right.Limit != nil {
				grp.Limit = right.Limit
				right.Limit = nil
			}
			if grp.Offset == nil && right.Offset != nil {
				grp.Offset = right.Offset
				right.Offset = nil
			}
		}
	}
	// Optional ORDER BY / LIMIT / OFFSET after the parenthesised compound.
	// These bind to the whole chain, which is exactly what grp is — the
	// operand's own clauses live on innerSel and are no longer overwritten.
	if p.acceptKeyword(KwOrder) {
		if !p.acceptKeyword(KwBy) {
			return nil, p.errAtCur("expected BY after ORDER")
		}
		ob, err := p.parseSortList()
		if err != nil {
			return nil, err
		}
		grp.OrderBy = ob
	}
	if p.acceptKeyword(KwLimit) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		grp.Limit = e
	}
	if p.acceptKeyword(KwOffset) {
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		grp.Offset = e
	}
	return grp, nil
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
	base, err := p.parseRangeVar(true)
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
	sawLateral := p.acceptKeyword(KwLateral)
	right, err := p.parseRangeVar(true)
	if err != nil {
		return JoinExpr{}, false, err
	}
	if sawLateral {
		right.Lateral = true
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

func (p *parser) parseRangeVar(allowUserSRF ...bool) (RangeVar, error) {
	fromClause := len(allowUserSRF) > 0 && allowUserSRF[0]
	// Accept optional LATERAL keyword before a derived table. goopg
	// still evaluates a lateral subquery as an ordinary derived table
	// (no correlated-outer-reference evaluation), but WHETHER the
	// keyword was written is recorded on the RangeVar: a LATERAL item
	// may reference an earlier FROM item, which is exactly what makes
	// a FROM permutation unsafe (planner/joinorder.go, M0125-0034).
	sawLateral := p.acceptKeyword(KwLateral)

	// FROM ONLY tablename — consume the ONLY keyword; the planner will skip
	// inheritance children for this table reference. M0097-0099.
	onlyModifier := p.acceptIdentKeyword("only")

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
		// Use parseParenthesisedSelectStmt which correctly handles:
		//   (SELECT ...)                        — simple derived table
		//   (((SELECT ...)))                    — multi-wrapped
		//   ((SELECT ...) EXCEPT (SELECT ...))  — parenthesised set-ops
		// It consumes the leading '(' and the matching ')' plus any
		// trailing UNION/INTERSECT/EXCEPT chain, so we never need the
		// old depth-counter approach that broke on set-ops in FROM.
		// SELECT … INTO is not permitted in a derived-table subquery (M0097-0020).
		old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
		p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
		p.selectIntoNoPos = false
		inner, err := p.parseParenthesisedSelectStmt()
		p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
		if err != nil {
			return RangeVar{}, err
		}
		sel, ok := inner.(*SelectStmt)
		if !ok {
			return RangeVar{}, &SyntaxError{Pos: pos, Message: "subquery in FROM did not produce SELECT"}
		}
		rv := RangeVar{pos: pos, Subquery: sel, Lateral: sawLateral}
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
	// ROWS FROM(func1(args), func2(args), ...) [WITH ORDINALITY] [AS alias]
	// Used in rangefuncs and similar multi-SRF scenarios.
	if fromClause &&
		p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "rows") &&
		p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwFrom {
		return p.parseRowsFrom()
	}
	obj, err := p.parseObjectName()
	if err != nil {
		return RangeVar{}, err
	}
	rv := RangeVar{pos: obj.pos, Schema: obj.Schema, Name: obj.Name, Only: onlyModifier}

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
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		lower := strings.ToLower(obj.Name)
		isKnownBuiltin := false
		switch lower {
		case "generate_series", "pg_input_error_info", "parse_ident",
			"pg_get_publication_tables", "pg_available_wal_summaries",
			"verify_heapam", "unnest", "generate_subscripts",
			"pg_options_to_table", "ts_token_type":
			isKnownBuiltin = true
		}
		switch {
		case obj.Schema == "" || strings.EqualFold(obj.Schema, "pg_catalog"):
			// Unqualified or pg_catalog-qualified.
			if isKnownBuiltin {
				srfFuncName = lower
			} else if fromClause {
				// Accept any other name(args) in FROM as a potential user-defined SRF.
				// Only do this in FROM clause context to avoid breaking INSERT INTO t (cols).
				srfFuncName = obj.Name
			}
		case fromClause:
			// Schema-qualified by a user schema, e.g. `"public".verify_heapam(...)`.
			// pg_amcheck builds each per-relation heap check as
			//   FROM pg_catalog.pg_class c, "public".verify_heapam(...) v
			// i.e. it qualifies the SRF with the *relation's* schema, not
			// pg_catalog. The schema qualifier is discarded here; dispatch is by
			// bare name. Known builtins use the lowercased canonical name so the
			// executor's name switch matches; everything else passes through as a
			// user-defined SRF. M0110-0003 AC-002 gap #5.
			if isKnownBuiltin {
				srfFuncName = lower
			} else {
				srfFuncName = obj.Name
			}
		}
	}
	if srfFuncName != "" {
		p.advance() // (
		var args []Expr
		if !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") {
			for {
				// Named argument: `name => value` / `name := value` — strip the
				// name and map positionally, mirroring parseFuncCallTail's
				// expression-context handling (sibling path). pg_amcheck emits
				// `verify_heapam(relation := c.oid, on_error_stop := false, …)` as
				// a FROM-clause SRF, so this path must accept it too (M0110-0003 S5).
				if isNamedArgNameToken(p.cur().Kind) &&
					p.peek(1).Kind == TokenOperator &&
					(p.peek(1).Value == "=>" || p.peek(1).Value == ":=") {
					p.advance() // skip name
					p.advance() // skip the `=>` / `:=` separator
				}
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
		// WITH ORDINALITY: adds a bigint ordinal column (1-based) to SRF output.
		if p.acceptKeyword(KwWith) {
			if !p.acceptIdentKeyword("ordinality") {
				return RangeVar{}, p.errAtCur("expected ORDINALITY after WITH")
			}
			rv.TableFunc.WithOrdinality = true
		}
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
// (`FETCH` for `FETCH FIRST n ROWS ONLY`, `WINDOW` for a trailing
// named-window clause — both are also reused for the same implicit-alias
// check in parseTargetEntry, e.g. `sum(x) OVER w WINDOW w AS (...)` must
// not swallow `window` as sum(x)'s column alias).
func isAliasStart(t Token) bool {
	if t.Kind == TokenIdent {
		switch strings.ToLower(t.Value) {
		case "fetch", "window":
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
		// KwReturns is allowed so that TPC-DS queries using "returns"
		// as a column alias (e.g. coalesce(returns, 0) returns) parse.
		if t.Keyword == KwReturns {
			return true
		}
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
// `en_US`, or a keyword used as collation name (e.g. `default`). Returns
// the bare collation name (last component). M0097-0127.
func (p *parser) parseCollationName() (string, error) {
	name := ""
	// Accept identifier or any keyword that can appear as a collation name.
	if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
		name = p.cur().Value
		p.advance()
	} else {
		tok, err := p.parseIdent()
		if err != nil {
			return "", err
		}
		name = tok.Value
	}
	for p.acceptSymbol(".") {
		// After a dot, also accept keywords as name components.
		if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
			name = p.cur().Value
			p.advance()
		} else {
			tok, err := p.parseIdent()
			if err != nil {
				return "", err
			}
			name = tok.Value
		}
	}
	return name, nil
}

// skipCollationName consumes a collation name and discards it.
// Kept for callers in DDL/DML that don't need the value.
func (p *parser) skipCollationName() error {
	_, err := p.parseCollationName()
	return err
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
// Multi-character operators like ~<~ are tokenized as separate single-char operator
// tokens (~, <, ~) by the lexer, so we greedily concatenate adjacent operator tokens.
func (p *parser) parseSortUsingOperator() string {
	cur := p.cur()
	switch cur.Kind {
	case TokenOperator, TokenSymbol:
		// Greedily consume consecutive operator tokens to handle multi-char
		// operators like ~<~, ~>~, ~<=~, ~>=~ which the lexer splits into parts.
		// Only TokenOperator tokens are concatenated; TokenSymbol (,;().[]) are delimiters.
		op := cur.Value
		p.advance()
		if cur.Kind == TokenOperator {
			for p.cur().Kind == TokenOperator {
				op += p.cur().Value
				p.advance()
			}
		}
		return op
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
	// Symbol operators: > or >= (and locale-independent variants ~>~ / ~>=~).
	if op == ">" || op == ">=" || op == "~>~" || op == "~>=~" {
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
	// JSON accessor operators (-> ->>) are "other operators" in PG's
	// precedence table, the same class as ||. M0118-0009.
	precJSON   = 6
	precAddSub = 7
	precMulDiv = 8
	precPow    = 9 // ^ — between * / % and unary minus, matching gram.y's %left '^'
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
			// Array slice `expr[lower:upper]` (M0134-0079, gram.y
			// `indirection_el`): either bound may be omitted
			// (`[:upper]`, `[lower:]`, `[:]`), so only parse an
			// operand when it isn't immediately the ':' separator or
			// the closing ']'.
			curSym := func(sym string) bool {
				c := p.cur()
				return c.Kind == TokenSymbol && c.Value == sym
			}
			var lower, upper Expr
			if !curSym(":") && !curSym("]") {
				var err error
				lower, err = p.parseExpr()
				if err != nil {
					return nil, err
				}
			}
			if curSym(":") {
				p.advance() // consume ':'
				if !curSym("]") {
					var err error
					upper, err = p.parseExpr()
					if err != nil {
						return nil, err
					}
				}
				if !p.acceptSymbol("]") {
					return nil, p.errAtCur("expected ']' after array slice")
				}
				left = &ArraySubscriptExpr{pos: pos, Base: left, IsSlice: true, Index: lower, Upper: upper}
				continue
			}
			if !p.acceptSymbol("]") {
				return nil, p.errAtCur("expected ']' after array subscript")
			}
			left = &ArraySubscriptExpr{pos: pos, Base: left, Index: lower}
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
			collPos := t.Pos
			p.advance() // consume COLLATE
			collName, err := p.parseCollationName()
			if err != nil {
				return nil, err
			}
			// Wrap in CollateExpr so planner can detect explicit-collation
			// mismatches in WITHIN GROUP ORDER BY. M0097-0127.
			left = &CollateExpr{pos: collPos, Operand: left, CollationName: collName}
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
			// `expr [NOT] LIKE pattern [ESCAPE escape_expr]` mirrors the IN
			// handling. The pattern is a comparison-precedence operand so a
			// bare string literal binds correctly without parens; nesting
			// LIKE inside arithmetic expressions still works because we
			// ascend at precCompare+1 for the rhs. An optional trailing
			// ESCAPE clause (PG oracle: gram.y `a_expr LIKE a_expr ESCAPE
			// a_expr`) is wrapped into a LikeEscapePattern so it survives
			// as the BinaryOp's Right operand without touching the
			// BinaryOp struct itself. M0134-0070.
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwLike {
				pos := t.Pos
				p.advance()
				// `expr LIKE ANY|SOME|ALL (array)` — ScalarArrayOpExpr,
				// same desugaring as the regex operators below. M0134-0003.
				if isAnyOrSomeTok(p.cur()) || isAllTok(p.cur()) {
					allQuant := isAllTok(p.cur())
					p.advance() // ANY / SOME / ALL
					inExpr, err := p.parseAnyTail(left, pos)
					if err != nil {
						return nil, err
					}
					if ie, ok := inExpr.(*InExpr); ok {
						ie.AnyOp = OpLike
						ie.AllOp = allQuant
					}
					left = inExpr
					continue
				}
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				rhs, err = p.wrapLikeEscape(rhs)
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
				// `expr NOT LIKE ANY|SOME|ALL (array)`. M0134-0003.
				if isAnyOrSomeTok(p.cur()) || isAllTok(p.cur()) {
					allQuant := isAllTok(p.cur())
					p.advance() // ANY / SOME / ALL
					inExpr, err := p.parseAnyTail(left, pos)
					if err != nil {
						return nil, err
					}
					if ie, ok := inExpr.(*InExpr); ok {
						ie.AnyOp = OpNotLike
						ie.AllOp = allQuant
					}
					left = inExpr
					continue
				}
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				rhs, err = p.wrapLikeEscape(rhs)
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
				// `expr ILIKE ANY|SOME|ALL (array)`. M0134-0003.
				if isAnyOrSomeTok(p.cur()) || isAllTok(p.cur()) {
					allQuant := isAllTok(p.cur())
					p.advance() // ANY / SOME / ALL
					inExpr, err := p.parseAnyTail(left, pos)
					if err != nil {
						return nil, err
					}
					if ie, ok := inExpr.(*InExpr); ok {
						ie.AnyOp = OpILike
						ie.AllOp = allQuant
					}
					left = inExpr
					continue
				}
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				rhs, err = p.wrapLikeEscape(rhs)
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
				// `expr NOT ILIKE ANY|SOME|ALL (array)`. M0134-0003.
				if isAnyOrSomeTok(p.cur()) || isAllTok(p.cur()) {
					allQuant := isAllTok(p.cur())
					p.advance() // ANY / SOME / ALL
					inExpr, err := p.parseAnyTail(left, pos)
					if err != nil {
						return nil, err
					}
					if ie, ok := inExpr.(*InExpr); ok {
						ie.AnyOp = OpNotILike
						ie.AllOp = allQuant
					}
					left = inExpr
					continue
				}
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				rhs, err = p.wrapLikeEscape(rhs)
				if err != nil {
					return nil, err
				}
				left = &BinaryOp{pos: pos, Op: OpNotILike, Left: left, Right: rhs}
				continue
			}
			// `expr SIMILAR TO pattern [ESCAPE escape_expr]` and its NOT
			// variant. PG oracle: postgres/src/backend/parser/gram.y:15080-
			// 15115 desugars this to `x ~ similar_to_escape(pattern[,
			// escape])` / `x !~ similar_to_escape(...)` — real PG's planner
			// constant-folds that function call away when its arguments are
			// literals, so `EXPLAIN` shows the plain `~`/`!~` BinaryOp with a
			// pre-converted POSIX pattern. goopg mirrors that by performing
			// the conversion here, at parse time, via buildSimilarTo.
			// M0134-0070.
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwSimilar &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwTo {
				pos := t.Pos
				p.advance() // SIMILAR
				p.advance() // TO
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				escExpr, err := p.parseOptionalEscape()
				if err != nil {
					return nil, err
				}
				newLeft, err := p.buildSimilarTo(left, rhs, escExpr, pos, false)
				if err != nil {
					return nil, err
				}
				left = newLeft
				continue
			}
			if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwNot &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwSimilar &&
				p.peek(2).Kind == TokenKeyword && p.peek(2).Keyword == KwTo {
				pos := t.Pos
				p.advance() // NOT
				p.advance() // SIMILAR
				p.advance() // TO
				rhs, err := p.parseExprPrec(precCompare + 1)
				if err != nil {
					return nil, err
				}
				escExpr, err := p.parseOptionalEscape()
				if err != nil {
					return nil, err
				}
				newLeft, err := p.buildSimilarTo(left, rhs, escExpr, pos, true)
				if err != nil {
					return nil, err
				}
				left = newLeft
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
					// `expr ~ ANY|SOME(array)` / `expr ~* ALL(subquery)` etc. —
					// ScalarArrayOpExpr. Desugar to InExpr with AnyOp set so the
					// executor applies the operator element-wise and OR-s
					// (ANY/SOME) or AND-s (ALL) the results. M0097-0068, M0122-0004.
					if isAnyOrSomeTok(p.cur()) || isAllTok(p.cur()) {
						allQuant := isAllTok(p.cur())
						p.advance() // ANY / SOME / ALL
						inExpr, err := p.parseAnyTail(left, pos)
						if err != nil {
							return nil, err
						}
						if ie, ok := inExpr.(*InExpr); ok {
							ie.AnyOp = op
							ie.AllOp = allQuant
						}
						left = inExpr
						continue
					}
					rhs, err := p.parseExprPrec(precCompare + 1)
					if err != nil {
						return nil, err
					}
					left = &BinaryOp{pos: pos, Op: op, Left: left, Right: rhs}
					continue
				}
			}
			// OPERATOR(schema.op) qualified operator syntax (psql \df etc.)
			if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "operator") &&
				p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
				pos := t.Pos
				if op, ok := p.parseQualifiedOperator(); ok {
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
				symmetric := p.acceptBetweenOrdering()
				expanded, err := p.parseBetweenTail(left, pos, false, symmetric)
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
				symmetric := p.acceptBetweenOrdering()
				expanded, err := p.parseBetweenTail(left, pos, true, symmetric)
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

		// `expr = ANY|SOME (array[...])` — desugar to `expr IN (...)`.
		// `expr != ANY|SOME (array[...])` / `expr <> ANY|SOME (...)` — desugar to
		// `expr NOT IN (...)` (semantically "at least one element is !=",
		// which for a finite non-null list equals NOT IN). M0097-0067.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenOperator && (t.Value == "=" || t.Value == "!=" || t.Value == "<>") &&
				isAnyOrSomeTok(p.peek(1)) {
				notEq := t.Value != "="
				pos := t.Pos
				p.advance() // = / != / <>
				p.advance() // ANY / SOME
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

		// `expr = ALL (array[...])` — desugar to NOT (expr != ANY (array)).
		// `= ALL` is true iff the operand equals every element of the array.
		// M0097-enum-all.
		if precCompare >= min {
			if t := p.cur(); t.Kind == TokenOperator && t.Value == "=" &&
				p.peek(1).Kind == TokenKeyword && p.peek(1).Keyword == KwAll {
				pos := t.Pos
				p.advance() // =
				p.advance() // ALL
				inExpr, err := p.parseAnyTail(left, pos)
				if err != nil {
					return nil, err
				}
				// Mark as NotEqualAny; wrap in NOT so the executor returns
				// true only when all elements equal the operand.
				if ie, ok := inExpr.(*InExpr); ok {
					ie.NotEqualAny = true
				}
				left = &UnaryOp{pos: pos, Op: OpNot, Operand: inExpr}
				continue
			}
		}

		// `expr <op> ANY|SOME|ALL (array[...] | subquery)` for the ordering
		// comparisons (< > <= >=) and `!=`/`<>` ALL — the remaining operator
		// x quantifier combinations not covered by the `=`/`!=`/`<>` ANY and
		// `=` ALL forms above. Generalizes via InExpr.AnyOp/AllOp: ANY/SOME
		// ORs the per-element comparison, ALL ANDs it (evalInExpr,
		// internal/executor/expr.go). M0122-0004.
		if precCompare >= min {
			var ordOp OpCode
			if t := p.cur(); t.Kind == TokenOperator {
				switch t.Value {
				case "<":
					ordOp = OpLt
				case ">":
					ordOp = OpGt
				case "<=":
					ordOp = OpLe
				case ">=":
					ordOp = OpGe
				case "!=", "<>":
					ordOp = OpNe
				}
			}
			if ordOp != OpUnknown {
				isAll := isAllTok(p.peek(1))
				if isAll || (ordOp != OpNe && isAnyOrSomeTok(p.peek(1))) {
					pos := p.cur().Pos
					p.advance() // the operator
					p.advance() // ANY / SOME / ALL
					inExpr, err := p.parseAnyTail(left, pos)
					if err != nil {
						return nil, err
					}
					if ie, ok := inExpr.(*InExpr); ok {
						ie.AnyOp = ordOp
						ie.AllOp = isAll
					}
					left = inExpr
					continue
				}
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
						p.advance()           // INTERVAL
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

// isAnyOrSomeTok reports whether t is the ANY or SOME keyword. SOME is an
// accepted synonym for ANY in scalar-array/subquery comparisons
// (`expr op SOME (...)`). M0122-0004.
func isAnyOrSomeTok(t Token) bool {
	return t.Kind == TokenKeyword && (t.Keyword == KwAny || t.Keyword == KwSome)
}

// isAllTok reports whether t is the ALL keyword used as a comparison
// quantifier (`expr op ALL (...)`).
func isAllTok(t Token) bool {
	return t.Kind == TokenKeyword && t.Keyword == KwAll
}

// parseAnyTail parses the parenthesised operand of `ANY|SOME|ALL (...)`
// after the quantifier keyword has been consumed: either
// `ANY (array[e1, e2, ...])`, a bare value list, or a `(SELECT ...)`
// subquery. The caller sets AnyOp/AllOp on the returned InExpr to select
// the comparison operator and ANY-vs-ALL semantics; a bare `IN`-style
// equality check leaves both zero/false.
func (p *parser) parseAnyTail(left Expr, pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after ANY/SOME/ALL")
	}
	if p.selectWithParensAhead() {
		// `x = ANY ((A) UNION (B))` — `sub_type … select_with_parens`, the
		// third sibling of the IN / EXISTS operand parsers (Hard-won Rule #2).
		// M0125-0018.
		sel, err := p.parseQueryOperandWithParens()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close ANY/ALL subquery")
		}
		return &InExpr{pos: pos, Operand: left, Negated: false, Subquery: sel}, nil
	}
	// `expr op ANY|ALL (SELECT ...)` — subquery form, mirroring parseInTail.
	if p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwSelect || p.cur().Keyword == KwValues || p.cur().Keyword == KwWith) {
		old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
		p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
		p.selectIntoNoPos = false
		inner, err := p.parseSelect()
		p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close ANY/ALL subquery")
		}
		sel, ok := inner.(*SelectStmt)
		if !ok {
			return nil, &SyntaxError{Pos: pos, Message: "ANY/ALL subquery did not produce SELECT"}
		}
		return &InExpr{pos: pos, Operand: left, Negated: false, Subquery: sel}, nil
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

// synthesizeBareCharTypmod mirrors parseColumnType's handling of the bare
// `char`/CHARACTER keyword (internal/parser/ddl.go), which PostgreSQL's
// grammar always resolves to bpchar with an implicit length of 1. The
// quoted identifier `"char"` names a distinct type (pg_type OID 18, a
// 1-byte internal type) and never takes a typmod. Because the parser
// otherwise discards whether the type-name token was quoted, an explicit
// (possibly synthesized) typmod is the only signal later stages have to
// tell the two apart — so a bare, typmod-less `char` gets length 1
// synthesized here, exactly as the column-type path already does.
func synthesizeBareCharTypmod(quoted bool, name string, typmods []int64) []int64 {
	if !quoted && len(typmods) == 0 && strings.EqualFold(name, "char") {
		return []int64{1}
	}
	return typmods
}

// parseCastTail consumes a single `:: typename [(typmods)]` after the
// caller has already produced the operand expression. Returns the
// CastExpr; the caller chains by re-checking for `::` after.
func (p *parser) parseCastTail(operand Expr) (Expr, error) {
	tok := p.advance() // consumes "::"
	firstTok := p.cur()
	name, err := p.parseTypeNameAfterCast()
	if err != nil {
		return nil, err
	}
	// Consume optional array suffix `[]` — preserve it in the type name for
	// casts like ::regtype[] that need different handling than ::regtype. M0097-0003.
	for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		p.advance() // '['
		if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
			p.advance() // ']'
		}
		name.Name += "[]"
	}
	var typmods []int64
	// Interval carries a field/precision typmod qualifier in the type-name
	// position (`x::interval hour to minute`, `x::interval second(2)`,
	// `x::interval(2)`) rather than a generic `(N[,M])` typmod list.
	// unimplemented_feat #5(d-iv).
	if name.Schema == "" && strings.EqualFold(name.Name, "interval") {
		tm, matched, terr := p.parseIntervalCastQualifier()
		if terr != nil {
			return nil, terr
		}
		if matched {
			typmods = []int64{tm}
		}
	} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
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
	typmods = synthesizeBareCharTypmod(firstTok.Kind == TokenQuotedIdent, name.Name, typmods)
	if firstTok.Kind != TokenQuotedIdent && name.Schema == "" {
		tn, tm, ferr := normalizeFloatTypeName(name.Name, typmods, firstTok.Pos)
		if ferr != nil {
			return nil, ferr
		}
		name.Name, typmods = tn, tm
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
	// Multi-word built-in type name (double precision, character varying,
	// bit varying, timestamp/time [with|without time zone]) — same shared
	// helper parseColumnType/parseCreateDomain use, so `x::character
	// varying`/`CAST(x AS double precision)` accept the same spellings
	// pg_dump emits for the AS-clause/column-type positions. DU-002.
	first = p.parseMultiWordTypeName(first)
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
	firstTok := p.cur()
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
	// Interval carries a field/precision typmod qualifier in the type-name
	// position (`CAST(x AS interval hour to minute)`, `... interval second(2)`,
	// `... interval(2)`) rather than a generic `(N[,M])` typmod list.
	// unimplemented_feat #5(d-iv).
	if name.Schema == "" && strings.EqualFold(name.Name, "interval") {
		tm, matched, terr := p.parseIntervalCastQualifier()
		if terr != nil {
			return nil, terr
		}
		if matched {
			typmods = []int64{tm}
		}
	} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
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
	typmods = synthesizeBareCharTypmod(firstTok.Kind == TokenQuotedIdent, name.Name, typmods)
	if firstTok.Kind != TokenQuotedIdent && name.Schema == "" {
		tn, tm, ferr := normalizeFloatTypeName(name.Name, typmods, firstTok.Pos)
		if ferr != nil {
			return nil, ferr
		}
		name.Name, typmods = tn, tm
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
// Handles nested sub-array syntax ARRAY[[a,b],[c,d]] for 2D arrays. M0097-0125.
func (p *parser) parseArrayConstructor(pos int) (Expr, error) {
	p.advance() // consume '['
	var elems []Expr
	if !(p.cur().Kind == TokenSymbol && p.cur().Value == "]") {
		first, err := p.parseArrayElement(pos)
		if err != nil {
			return nil, err
		}
		elems = append(elems, first)
		for p.acceptSymbol(",") {
			elem, err := p.parseArrayElement(pos)
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

// parseArrayElement parses one element inside ARRAY[...]. If the element
// starts with '[', it is a nested sub-array (2D array syntax). M0097-0125.
func (p *parser) parseArrayElement(_ int) (Expr, error) {
	if p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
		subPos := p.cur().Pos
		return p.parseArrayConstructor(subPos)
	}
	return p.parseExpr()
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
		case "^":
			return OpPow, precPow, true
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
		case "<@":
			return OpContainedBy, precCompare, true
		case "@>":
			return OpContains, precCompare, true
		case "&&":
			return OpOverlap, precCompare, true
		case "->":
			return OpJSONGet, precJSON, true
		case "->>":
			return OpJSONGetText, precJSON, true
		case "#>":
			return OpJSONPathGet, precJSON, true
		case "#>>":
			return OpJSONPathGetText, precJSON, true
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
	case t.Kind == TokenOperator && t.Value == "~":
		p.advance()
		operand, err := p.parseExprPrec(precUnary)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: OpBitNot, Operand: operand}, nil
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
			// `( SELECT … )` is a subquery expression. Also handles
			// nested-paren forms like `((SELECT …) UNION SELECT …)`.
			// Look ahead through leading `(` tokens to find SELECT/VALUES.
			isSubqStart := false
			if p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwSelect || p.cur().Keyword == KwValues || p.cur().Keyword == KwWith) {
				isSubqStart = true
			} else if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				for i := 0; ; i++ {
					tok := p.peek(i)
					if tok.Kind == TokenSymbol && tok.Value == "(" {
						continue
					}
					if tok.Kind == TokenKeyword && (tok.Keyword == KwSelect || tok.Keyword == KwValues || tok.Keyword == KwWith) {
						isSubqStart = true
					}
					break
				}
			}
			if isSubqStart && p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
				// Nested-paren subquery like `((SELECT …) UNION …)`.
				// Delegate to parseParenthesisedSelectStmt (no leading `(`
				// consumed yet — it will consume it).
				inner, err := p.parseParenthesisedSelectStmt()
				if err != nil {
					return nil, err
				}
				sel, ok := inner.(*SelectStmt)
				if !ok {
					return nil, &SyntaxError{Pos: t.Pos, Message: "subquery did not produce SELECT"}
				}
				// Consume optional extra closing parens added by caller's depth tracking.
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')' to close subquery")
				}
				return &SubqueryExpr{pos: t.Pos, Inner: sel}, nil
			}
			if isSubqStart {
				// SELECT … INTO is not permitted in a scalar subquery (M0097-0020).
				old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
				p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
				p.selectIntoNoPos = false
				inner, err := p.parseSelect()
				p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
				if err != nil {
					return nil, err
				}
				// Allow set operations inside the parenthesised subquery:
				// (SELECT 2 UNION SELECT 3) is a valid scalar subquery.
				if innerSel, ok := inner.(*SelectStmt); ok {
					if setOp, present, err2 := p.parseSetOpClause(); err2 != nil {
						return nil, err2
					} else if present {
						if innerSel.SetOp == nil {
							innerSel.SetOp = setOp
						} else {
							rightmost := innerSel.SetOp.Right
							for rightmost.SetOp != nil {
								rightmost = rightmost.SetOp.Right
							}
							rightmost.SetOp = setOp
						}
					}
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
			// Row constructor: `(a, b, ...)` — collect remaining elements. M0097-0020.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				elems := []Expr{inner}
				for p.acceptSymbol(",") {
					next, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					elems = append(elems, next)
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')'")
				}
				return &RowExpr{pos: t.Pos, Elems: elems}, nil
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
		"name", "oid", "pg_lsn",
		// txid_snapshot/pg_snapshot 'string' cast syntax (M0134-0080), used
		// by txid.sql: `txid_snapshot '1000...:1000...:1000...'`.
		"txid_snapshot", "pg_snapshot":
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
				p.advance()           // type name (time/timestamp)
				p.advance()           // WITH/WITHOUT
				p.advance()           // time
				p.advance()           // zone
				strTok := p.advance() // string literal
				return &TypedStringLit{pos: pos, Type: typName, Value: strTok.Value}, true
			}
		}
		// `typename ( intlit ) 'string'` — the character-string types char(N),
		// varchar(N), bpchar(N) take a single-integer length typmod. PG
		// grammar `character '(' Iconst ')' Sconst` (gram.y ConstTypename Sconst
		// → makeStringConstCast); padding/truncation is applied later at
		// coercion, so the typmod is deliberately ignored here and the raw
		// string is returned (M0134-0070). Restricted to char/bpchar/varchar;
		// text/numeric/etc. must NOT consume this shape.
		if (name == "char" || name == "bpchar" || name == "varchar") &&
			p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			numTok := p.peek(2)
			closeTok := p.peek(3)
			strTok := p.peek(4)
			if numTok.Kind == TokenIntLit && closeTok.Kind == TokenSymbol &&
				closeTok.Value == ")" && strTok.Kind == TokenStringLit {
				p.advance() // type name
				p.advance() // (
				p.advance() // N
				p.advance() // )
				p.advance() // string literal
				return &TypedStringLit{pos: t.Pos, Type: name, Value: strTok.Value}, true
			}
			// `char(` that is not a valid `( N ) '...'` is not a literal.
			return nil, false
		}
		next := p.peek(1)
		if next.Kind != TokenStringLit {
			return nil, false
		}
		p.advance() // consume type ident
		strTok := p.advance()
		return &TypedStringLit{pos: t.Pos, Type: name, Value: strTok.Value}, true
	case "interval":
		// Leading-precision form `interval ( p ) '<lit>'` (PG grammar
		// ConstInterval '(' Iconst ')' Sconst): the precision paren precedes
		// the string. It packs a FULL-range typmod with only a fractional-
		// seconds precision p — the body is parsed with the default (seconds)
		// unit and NO field is truncated; only sub-second digits beyond p are
		// rounded (AdjustIntervalForTypmod, full range). Modelled with
		// Unit="second" so the no-op SECOND truncation leaves every field
		// intact while the precision arm still rounds. unimplemented_feat
		// #5(d-iv).
		if p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
			numTok := p.peek(2)
			closeTok := p.peek(3)
			strTok := p.peek(4)
			if numTok.Kind == TokenIntLit && closeTok.Kind == TokenSymbol &&
				closeTok.Value == ")" && strTok.Kind == TokenStringLit {
				if prec, cerr := strconv.Atoi(numTok.Value); cerr == nil && prec >= 0 {
					// PostgreSQL clamps precision above MAX_INTERVAL_PRECISION
					// (6) to 6 with a warning (intervaltypmodin); clamp silently.
					if prec > 6 {
						prec = 6
					}
					p.advance() // INTERVAL
					p.advance() // (
					p.advance() // p
					p.advance() // )
					p.advance() // string literal
					return &IntervalLit{pos: t.Pos, Value: strTok.Value, Unit: "second",
						Qualified: true, HasPrec: true, Prec: prec}, true
				}
			}
			// `interval (` that is not a valid `( p ) 'lit'` is not a literal.
			return nil, false
		}
		next := p.peek(1)
		if next.Kind != TokenStringLit {
			return nil, false
		}
		// Form 1: `interval '<N>' <typmod-qualifier>` — the trailing SQL
		// interval typmod field (`interval '1.5' hour` = 01:00:00), an
		// optional `<hi> TO <lo>` range (`interval '5' hour to minute`) and
		// an optional `SECOND(p)` precision (`interval '5' second(2)`).
		// Only the SINGULAR field keywords year/month/day/hour/minute/second
		// are grammar keywords; a plural (`days`) or abbreviation (`min`) is
		// NOT — PostgreSQL then parses `interval '5' days` as the bare interval
		// `interval '5'` (= 00:00:05) with `days` a column alias, so those must
		// fall through to Form 2 here. unimplemented_feat #5(d-iv).
		if lit, consumed, ok := p.tryIntervalTypmodQualifier(t.Pos, next.Value); ok {
			p.advance() // INTERVAL
			p.advance() // string literal
			for i := 0; i < consumed; i++ {
				p.advance()
			}
			return lit, true
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
		// Multi-field / HH:MM:SS bodies (`interval '1 day 05:00:00'`,
		// `interval '1 year 2 mons 3 days'`, bare `interval '05:00:00'`):
		// decode the whole body up front and carry the pre-computed
		// months/days/micros. unimplemented_feat #5(b).
		if mo, d, mu, ok := ParseIntervalBody(next.Value); ok {
			p.advance() // INTERVAL
			p.advance() // string literal
			return &IntervalLit{pos: t.Pos, PreComputed: true, PreMonths: mo, PreDays: d, PreMicros: mu}, true
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
	if p.selectWithParensAhead() {
		// `EXISTS ((A) EXCEPT (B))` — gram.y spells EXISTS' operand
		// `select_with_parens`, so the branch may itself be parenthesised
		// (and the chain may continue past it). M0125-0018.
		sel, err := p.parseQueryOperandWithParens()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close EXISTS subquery")
		}
		return &ExistsExpr{pos: pos, Negated: negated, Subquery: sel}, nil
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
//	"2 hours"   → ("2", "hour", true)
//	"30 minutes"→ ("30", "minute", true)
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
	// The magnitude must be a plain number. A first field that is not a bare
	// magnitude — most importantly a SQL year-month field (`1-2 days`, where the
	// `days` is a unit word absorbed as a no-op) — must NOT be mis-split here; it
	// falls through to ParseIntervalBody, which decodes the whole body faithfully
	// (`1-2 days` → 1 year 2 mons). Without this guard `1-2` would reach
	// evalIntervalLit as the magnitude and raise "invalid interval count".
	if _, _, ok := ParseIntervalMagnitude(num); !ok {
		return "", "", false
	}
	switch unit {
	case "day", "days", "month", "months", "year", "years",
		"hour", "hours", "minute", "minutes",
		"second", "seconds", "millisecond", "milliseconds":
		return num, strings.TrimSuffix(unit, "s"), true
	}
	return "", "", false
}

// intervalTypmodField is the set of SINGULAR interval field keywords that
// PostgreSQL's opt_interval grammar accepts as a trailing typmod qualifier on
// `interval '<body>' <field>`. Plurals (`days`) and abbreviations (`min`) are
// intentionally excluded: they are not grammar keywords, so a token like `days`
// after the literal is a column alias on the bare interval, never a typmod
// (`interval '5' days` = 00:00:05 AS days). See postgres gram.y opt_interval.
var intervalTypmodField = map[string]bool{
	"year": true, "month": true, "day": true,
	"hour": true, "minute": true, "second": true,
}

// intervalRangeLowField returns the rightmost (low) field of a valid SQL
// interval range `<hi> TO <lo>`, or ("", false) if the pair is not one of the
// combinations PostgreSQL's opt_interval grammar permits (YEAR TO MONTH; DAY TO
// {HOUR,MINUTE,SECOND}; HOUR TO {MINUTE,SECOND}; MINUTE TO SECOND). The low
// field alone determines both how a bare-magnitude body is interpreted
// (datetime.c DecodeInterval `switch (range)`) and the granularity
// AdjustIntervalForTypmod truncates to (timestamp.c), so callers keep only it.
func intervalRangeLowField(hi, lo string) (string, bool) {
	switch hi {
	case "year":
		if lo == "month" {
			return "month", true
		}
	case "day":
		switch lo {
		case "hour", "minute", "second":
			return lo, true
		}
	case "hour":
		switch lo {
		case "minute", "second":
			return lo, true
		}
	case "minute":
		if lo == "second" {
			return "second", true
		}
	}
	return "", false
}

// tryIntervalTypmodQualifier decodes the trailing typmod qualifier of a Form-1
// interval literal `interval '<body>' <qualifier>` by lookahead only, without
// consuming any tokens. On success it returns the IntervalLit and the number of
// qualifier tokens (after the string literal) the caller must advance; on a
// non-match it returns ok=false so the caller falls through to the embedded
// forms (which is how a plural like `days` becomes a column alias).
//
// Grammar (postgres gram.y opt_interval):
//
//	<field>
//	<hi> TO <lo>
//	SECOND '(' p ')'
//	<hi> TO SECOND '(' p ')'
//
// The range collapses to its low field for both interpretation and truncation
// (see intervalRangeLowField); precision is only valid when the low field is
// SECOND. Peek offsets are relative to the INTERVAL token at peek(0): peek(1) is
// the string body and peek(2) the first qualifier token.
func (p *parser) tryIntervalTypmodQualifier(pos int, body string) (*IntervalLit, int, bool) {
	f1 := p.peek(2)
	if f1.Kind != TokenIdent {
		return nil, 0, false
	}
	hi := strings.ToLower(identText(f1))
	if !intervalTypmodField[hi] {
		return nil, 0, false
	}
	lowField := hi
	consumed := 1 // the field ident
	off := 3      // next token to inspect

	// Optional `TO <lo>` range.
	if tk := p.peek(off); tk.Kind == TokenKeyword && tk.Keyword == KwTo {
		f2 := p.peek(off + 1)
		if f2.Kind != TokenIdent {
			return nil, 0, false
		}
		low, ok := intervalRangeLowField(hi, strings.ToLower(identText(f2)))
		if !ok {
			return nil, 0, false
		}
		lowField = low
		consumed += 2
		off += 2
	}

	// Optional `( p )` precision — only when the low field is SECOND
	// (postgres interval_second grammar). A parenthesis after any other field
	// is left for the caller to reject, so a valid `interval '5' hour` is not
	// lost to a stray token.
	hasPrec, prec := false, 0
	if lowField == "second" {
		if tk := p.peek(off); tk.Kind == TokenSymbol && tk.Value == "(" {
			numTok := p.peek(off + 1)
			closeTok := p.peek(off + 2)
			if numTok.Kind == TokenIntLit && closeTok.Kind == TokenSymbol && closeTok.Value == ")" {
				if n, err := strconv.Atoi(numTok.Value); err == nil && n >= 0 {
					// PostgreSQL clamps precision above MAX_INTERVAL_PRECISION
					// (6) to 6 with a warning (intervaltypmodin); clamp silently.
					if n > 6 {
						n = 6
					}
					hasPrec, prec = true, n
					consumed += 3
				}
			}
		}
	}

	return &IntervalLit{pos: pos, Value: body, Unit: lowField, Qualified: true, HasPrec: hasPrec, Prec: prec}, consumed, true
}

// Interval typmod packing for the cast / type-name position
// (`CAST(x AS interval hour to minute)`, `x::interval second(2)`,
// `x::interval(2)`), mirroring PostgreSQL's INTERVAL_TYPMOD packing
// (postgres/src/include/utils/timestamp.h): typmod = (range << 16) | precision,
// where `range` is the OR of INTERVAL_MASK(field) bits and `precision` defaults
// to INTERVAL_FULL_PRECISION when unspecified. Only the LOW field's mask bit is
// stored — the range collapses to its low field for both interpretation and
// AdjustIntervalForTypmod truncation (see intervalRangeLowField) — which is
// enough for the executor to reconstruct the truncation and the bare-magnitude
// default unit. unimplemented_feat #5(d-iv).
const (
	intervalFullPrecision = 0xFFFF
	intervalFullRange     = 0x7FFF
)

// intervalFieldTypmodBit maps a singular interval field to its PostgreSQL DTK
// bit position (MONTH=1, YEAR=2, DAY=3, HOUR=10, MINUTE=11, SECOND=12; see
// postgres/src/include/utils/datetime.h). INTERVAL_MASK(f) == 1<<bit.
var intervalFieldTypmodBit = map[string]int{
	"month": 1, "year": 2, "day": 3, "hour": 10, "minute": 11, "second": 12,
}

// packIntervalCastTypmod packs an interval cast typmod. lowField=="" means no
// field qualifier (precision-only or bare); prec<0 means no precision. The
// result is always non-zero when any qualifier is present, so 0 unambiguously
// means "bare interval, no typmod".
func packIntervalCastTypmod(lowField string, prec int) int64 {
	rangeMask := intervalFullRange
	if bit, ok := intervalFieldTypmodBit[lowField]; ok {
		rangeMask = 1 << bit
	}
	p := intervalFullPrecision
	if prec >= 0 {
		p = prec
	}
	return (int64(rangeMask) << 16) | int64(p)
}

// packIntervalColumnTypmod packs an interval COLUMN typmod the way PG's
// intervaltypmodin does (timestamp.c:1047): the full range bitmask (not the
// low-field collapse the cast path uses) in the high 16 bits and the precision
// in the low 16. Unlike the cast typmod — which is internal-only and may
// collapse `year to month` to `month` because the truncation is identical —
// the column typmod is stored in pg_attribute.atttypmod and a hosted PG /
// pg_dump must see the declared spelling, so `year to month` keeps YEAR|MONTH.
func packIntervalColumnTypmod(rangeMask int, prec int) int64 {
	p := intervalFullPrecision
	if prec >= 0 {
		p = prec
	}
	return (int64(rangeMask) << 16) | int64(p)
}

// intervalRangeMask returns the full INTERVAL_MASK OR for a `hi [TO lo]`
// interval range — the exact bitmask intervaltypmodin stores for that range
// (INTERVAL_MASK(YEAR)|INTERVAL_MASK(MONTH), etc.). hi and lo are the singular
// field keywords and the pair has already been validated by
// intervalRangeLowField, so only the legal combinations are enumerated.
func intervalRangeMask(hi, lo string) int {
	bit := func(f string) int { return 1 << intervalFieldTypmodBit[f] }
	switch hi {
	case "year":
		if lo == "month" {
			return bit("year") | bit("month")
		}
		return bit("year")
	case "month":
		return bit("month")
	case "day":
		switch lo {
		case "hour":
			return bit("day") | bit("hour")
		case "minute":
			return bit("day") | bit("hour") | bit("minute")
		case "second":
			return bit("day") | bit("hour") | bit("minute") | bit("second")
		default:
			return bit("day")
		}
	case "hour":
		switch lo {
		case "minute":
			return bit("hour") | bit("minute")
		case "second":
			return bit("hour") | bit("minute") | bit("second")
		default:
			return bit("hour")
		}
	case "minute":
		if lo == "second" {
			return bit("minute") | bit("second")
		}
		return bit("minute")
	case "second":
		return bit("second")
	}
	return intervalFullRange
}

// parseIntervalColumnQualifier parses the interval typmod qualifier in the
// COLUMN-definition / type-name position (`c interval year to month`,
// `c interval second(2)`, `c interval(2)`), after the `interval` keyword has
// been consumed. It is the column twin of parseIntervalCastQualifier: the same
// token grammar, but it returns the FULL range mask (see intervalRangeMask)
// instead of collapsing to the low field, because the column typmod is written
// to pg_attribute.atttypmod and must round-trip the declared spelling.
func (p *parser) parseIntervalColumnQualifier() (typmod int64, matched bool, err error) {
	// Precision-only `interval(p)`.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		prec, ok := p.tryConsumeIntervalPrecParen()
		if !ok {
			return 0, false, nil
		}
		return packIntervalColumnTypmod(intervalFullRange, prec), true, nil
	}
	f1 := p.cur()
	if f1.Kind != TokenIdent {
		return 0, false, nil
	}
	hi := strings.ToLower(identText(f1))
	if !intervalTypmodField[hi] {
		return 0, false, nil
	}
	p.advance() // high field
	lo := hi
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTo {
		p.advance() // TO
		f2 := p.cur()
		if f2.Kind != TokenIdent {
			return 0, false, p.errAtCur("expected interval field after TO")
		}
		loName := strings.ToLower(identText(f2))
		if _, ok := intervalRangeLowField(hi, loName); !ok {
			return 0, false, p.errAtCur("invalid interval field combination")
		}
		p.advance() // low field
		lo = loName
	}
	prec := -1
	if lo == "second" && p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		if pp, ok := p.tryConsumeIntervalPrecParen(); ok {
			prec = pp
		}
	}
	return packIntervalColumnTypmod(intervalRangeMask(hi, lo), prec), true, nil
}

// DecodeIntervalCastTypmod unpacks a typmod produced by packIntervalCastTypmod
// back into the executor-facing (lowField, hasPrec, prec). A typmod of 0 (a
// bare interval with no qualifier) yields ("", false, 0).
func DecodeIntervalCastTypmod(typmod int64) (lowField string, hasPrec bool, prec int) {
	if typmod == 0 {
		return "", false, 0
	}
	rangeMask := int((typmod >> 16) & intervalFullRange)
	p := int(typmod & intervalFullPrecision)
	if p != intervalFullPrecision {
		hasPrec, prec = true, p
	}
	if rangeMask != intervalFullRange {
		for f, bit := range intervalFieldTypmodBit {
			if rangeMask == 1<<bit {
				lowField = f
				break
			}
		}
	}
	return lowField, hasPrec, prec
}

// parseIntervalCastQualifier parses the optional interval typmod qualifier in
// the CAST / `::` type-name position, after the `interval` type keyword has been
// consumed:
//
//	interval <field> [TO <lo>] [(p)]   -- CAST(x AS interval hour to minute)
//	interval second (p)                -- SECOND(p) precision
//	interval (p)                       -- precision-only (full range)
//
// It consumes the tokens it recognises and returns the packed typmod with
// matched=true. When no qualifier is present it consumes nothing and returns
// matched=false, so a bare `::interval` (and any following column alias) is
// unaffected. Only the singular field keywords are recognised (intervalTypmodField);
// a plural / abbreviation falls through as an alias, matching the literal-form
// grammar. unimplemented_feat #5(d-iv).
func (p *parser) parseIntervalCastQualifier() (typmod int64, matched bool, err error) {
	// Precision-only `interval(p)`.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		prec, ok := p.tryConsumeIntervalPrecParen()
		if !ok {
			return 0, false, nil
		}
		return packIntervalCastTypmod("", prec), true, nil
	}
	// Field qualifier `interval <field> [TO <lo>] [(p)]`.
	f1 := p.cur()
	if f1.Kind != TokenIdent {
		return 0, false, nil
	}
	hi := strings.ToLower(identText(f1))
	if !intervalTypmodField[hi] {
		return 0, false, nil
	}
	p.advance() // consume the high field
	lowField := hi
	// Optional `TO <lo>` range. Once a field keyword has been consumed the TO is
	// unambiguously part of the interval typmod, so an invalid low field is an
	// error rather than a fall-through.
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTo {
		p.advance() // TO
		f2 := p.cur()
		if f2.Kind != TokenIdent {
			return 0, false, p.errAtCur("expected interval field after TO")
		}
		low, ok := intervalRangeLowField(hi, strings.ToLower(identText(f2)))
		if !ok {
			return 0, false, p.errAtCur("invalid interval field combination")
		}
		p.advance() // low field
		lowField = low
	}
	// Optional `( p )` precision — only valid when the low field is SECOND
	// (postgres interval_second grammar).
	prec := -1
	if lowField == "second" && p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		if pp, ok := p.tryConsumeIntervalPrecParen(); ok {
			prec = pp
		}
	}
	return packIntervalCastTypmod(lowField, prec), true, nil
}

// tryConsumeIntervalPrecParen consumes a simple `( <int> )` precision group when
// the current token is `(` and the group is exactly a non-negative integer
// literal followed by `)`. PostgreSQL clamps a precision above
// MAX_INTERVAL_PRECISION (6) to 6 (intervaltypmodin); the clamp is applied
// silently here. Returns ok=false without consuming anything if the group is not
// a bare integer precision, so a caller can fall through.
func (p *parser) tryConsumeIntervalPrecParen() (int, bool) {
	if p.cur().Kind != TokenSymbol || p.cur().Value != "(" {
		return 0, false
	}
	numTok := p.peek(1)
	closeTok := p.peek(2)
	if numTok.Kind != TokenIntLit || closeTok.Kind != TokenSymbol || closeTok.Value != ")" {
		return 0, false
	}
	n, convErr := strconv.Atoi(numTok.Value)
	if convErr != nil || n < 0 {
		return 0, false
	}
	if n > 6 {
		n = 6
	}
	p.advance() // (
	p.advance() // int
	p.advance() // )
	return n, true
}

// selectWithParensAhead reports whether the tokens starting at the CURRENT
// position form PostgreSQL's `select_with_parens` production rather than an
// ordinary parenthesised expression:
//
//	select_with_parens: '(' select_no_parens ')' | '(' select_with_parens ')'
//
// (postgres/src/backend/parser/gram.y). The IN / ANY-SOME-ALL / EXISTS operand
// parsers each have to choose between this production and an expression
// alternative — `in_expr: select_with_parens | '(' expr_list ')'` and
// `sub_type: … select_with_parens | … '(' a_expr ')'`; EXISTS admits only
// select_with_parens. Each of them used to test only "is the very next token
// SELECT/VALUES", so a nested `(` — the shape every parenthesised set-op chain
// starts with — fell through to the expression alternative. M0125-0018.
//
// Two conditions must hold:
//
//  1. peeling the leading run of `(` reaches SELECT or VALUES, so `((1),(2))`
//     and `(a + b)` stay expressions; and
//  2. the token after the group's matching `)` continues a QUERY rather than an
//     expression. This is what separates `((SELECT 1),(SELECT 2))` (an
//     expr_list of two scalar subqueries) and `((SELECT 1)::int)` (a cast) from
//     `((SELECT 1) UNION (SELECT 2))` and the bare `((SELECT 1))`.
//
// Condition 2's treatment of the bare `((SELECT …))` form is not a guess:
// PostgreSQL 18.3 answers `t` to `select 1 in ((select 1 union select 2))`,
// which the expr_list reading cannot produce — as a scalar subquery it would
// raise 21000 "more than one row returned by a subquery used as an
// expression", which is exactly what goopg used to do.
func (p *parser) selectWithParensAhead() bool {
	if !(p.cur().Kind == TokenSymbol && p.cur().Value == "(") {
		return false
	}
	// (1) Peel the leading `(` run and require a query keyword under it.
	for i := 0; ; i++ {
		tok := p.peek(i)
		if tok.Kind == TokenSymbol && tok.Value == "(" {
			continue
		}
		if !(tok.Kind == TokenKeyword && (tok.Keyword == KwSelect || tok.Keyword == KwValues)) {
			return false
		}
		break
	}
	// (2) Walk to the `)` that closes the group the current `(` opened, and
	// judge the token after it.
	depth := 0
	for j := 0; ; j++ {
		tok := p.peek(j)
		switch {
		case tok.Kind == TokenEOF:
			return false
		case tok.Kind == TokenSymbol && tok.Value == "(":
			depth++
		case tok.Kind == TokenSymbol && tok.Value == ")":
			depth--
			if depth == 0 {
				return continuesParenthesisedQuery(p.peek(j + 1))
			}
		}
	}
}

// continuesParenthesisedQuery reports whether t can follow a select_with_parens
// inside a query operand: either it closes the operand outright, or it opens
// one of select_no_parens' trailing clauses (a set operation, ORDER BY, the
// LIMIT/OFFSET pair, or a locking clause). Anything else — `,`, `::`, an
// arithmetic operator — means the parentheses belonged to an expression.
func continuesParenthesisedQuery(t Token) bool {
	if t.Kind == TokenSymbol && t.Value == ")" {
		return true
	}
	if t.Kind != TokenKeyword {
		return false
	}
	switch t.Keyword {
	case KwUnion, KwIntersect, KwExcept, KwOrder, KwLimit, KwOffset, KwFor:
		return true
	}
	return false
}

// parseQueryOperandWithParens parses a select_with_parens operand whose leading
// `(` has NOT been consumed, restoring the caller's SELECT-INTO error context
// afterwards. Callers must have established via selectWithParensAhead() that
// this really is a query; on entry the current token is that `(`. M0125-0018.
func (p *parser) parseQueryOperandWithParens() (*SelectStmt, error) {
	// SELECT … INTO is not permitted in a subquery operand (M0097-0020).
	old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
	p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
	p.selectIntoNoPos = false
	inner, err := p.parseParenthesisedSelectStmt()
	p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
	if err != nil {
		return nil, err
	}
	sel, ok := inner.(*SelectStmt)
	if !ok {
		return nil, p.errAtCur("expected SELECT inside parentheses")
	}
	return sel, nil
}

// parseInTail consumes the right side of `expr [NOT] IN (...)`
// after the IN keyword. The parenthesised body is either a
// parenthesised SELECT (uncorrelated subquery) or a value list
// — disambiguated by whether the first inner token is SELECT.
// wrapLikeEscape checks for a trailing `ESCAPE escape_expr` clause after a
// LIKE/NOT LIKE/ILIKE/NOT ILIKE pattern rhs. If present, it consumes the
// ESCAPE keyword and the escape expression (parsed at the same
// comparison-precedence level as the pattern rhs) and wraps rhs in a
// LikeEscapePattern; otherwise rhs is returned unchanged. PG oracle:
// postgres/src/backend/parser/gram.y `a_expr LIKE a_expr ESCAPE a_expr`.
// M0134-0070.
func (p *parser) wrapLikeEscape(rhs Expr) (Expr, error) {
	if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwEscape {
		pos := t.Pos
		p.advance()
		escExpr, err := p.parseExprPrec(precCompare + 1)
		if err != nil {
			return nil, err
		}
		return &LikeEscapePattern{pos: pos, Pattern: rhs, Escape: escExpr}, nil
	}
	return rhs, nil
}

// parseOptionalEscape consumes a trailing `ESCAPE escape_expr` clause if
// present (same comparison-precedence level as the preceding pattern
// operand) and returns it, or nil if no ESCAPE clause was written. Shared by
// buildSimilarTo's callers; unlike wrapLikeEscape it does not wrap the
// pattern — SIMILAR TO's pattern and escape are combined by buildSimilarTo
// instead. M0134-0070.
func (p *parser) parseOptionalEscape() (Expr, error) {
	if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwEscape {
		p.advance()
		return p.parseExprPrec(precCompare + 1)
	}
	return nil, nil
}

// similarToLiteralValue reports whether e is a literal usable for
// parse-time SIMILAR TO constant folding: a plain string literal (returns
// its Value, isNull=false) or the NULL literal (isNull=true, Value
// meaningless). ok=false for anything else (a column ref, subquery, etc.),
// signalling the caller to fall back to runtime evaluation. M0134-0070.
func similarToLiteralValue(e Expr) (value string, isNull bool, ok bool) {
	switch x := e.(type) {
	case *StringConst:
		return x.Value, false, true
	case *NullConst:
		return "", true, true
	}
	return "", false, false
}

// buildSimilarTo implements the parse-time constant-fold half of `expr
// SIMILAR TO pattern [ESCAPE escape]` / `expr NOT SIMILAR TO pattern
// [ESCAPE escape]` (PG oracle: postgres/src/backend/parser/gram.y:15080-
// 15115 desugar to similar_to_escape_1/2; postgres/src/backend/utils/adt/
// regexp.c:768-1063 similar_escape_internal). escape is nil when no ESCAPE
// clause was written (defaults to backslash, similarto.DefaultEscape).
//
// When pattern and escape (if present) are both literal, the conversion
// runs immediately and the result is a plain BinaryOp{Op: OpRegexMatch /
// OpRegexNoMatch} with a `'<posix>'::text` right operand — the same shape
// real PG's planner constant-folding produces, so EXPLAIN output matches
// byte-for-byte. A NULL pattern or escape literal folds the whole
// expression to NULL (the underlying PG function is STRICT). An
// escape string of more than one character is ERROR 22025 raised
// immediately, matching similar_escape_internal's ereport (no
// errposition call in the C source, hence Pos: -1 here to suppress the
// wire LINE/caret PG itself doesn't emit for this error).
//
// When either operand isn't a literal, folding is deferred to a
// SimilarToPattern runtime node — not exercised by strings.sql, see
// M0134-0070 report for the deferral note.
func (p *parser) buildSimilarTo(left, pattern, escape Expr, pos int, negate bool) (Expr, error) {
	patVal, patNull, patOK := similarToLiteralValue(pattern)
	escVal, escNull, escOK := similarto.DefaultEscape, false, true
	if escape != nil {
		escVal, escNull, escOK = similarToLiteralValue(escape)
	}
	if !patOK || !escOK {
		return &SimilarToPattern{pos: pos, Left: left, Pattern: pattern, Escape: escape, Negate: negate}, nil
	}
	if patNull || escNull {
		return &NullConst{pos: pos}, nil
	}
	if err := similarto.ValidateEscape(escVal); err != nil {
		return nil, &SyntaxError{
			Pos: -1, Raw: true, Code: "22025",
			Message: "invalid escape string",
			Hint:    "Escape string must be empty or one character.",
		}
	}
	converted := similarto.Convert(patVal, escVal)
	op := OpRegexMatch
	if negate {
		op = OpRegexNoMatch
	}
	return &BinaryOp{
		pos:  pos,
		Op:   op,
		Left: left,
		Right: &TypedStringLit{
			pos:   pos,
			Type:  "text",
			Value: converted,
		},
	}, nil
}

func (p *parser) parseInTail(left Expr, pos int, negated bool) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after IN")
	}
	if p.selectWithParensAhead() {
		// `x IN ((A) EXCEPT (B))` — `in_expr: select_with_parens`, so the
		// nested parentheses group a query, not a value list. M0125-0018.
		sel, err := p.parseQueryOperandWithParens()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close IN subquery")
		}
		return &InExpr{pos: pos, Operand: left, Negated: negated, Subquery: sel}, nil
	}
	if p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwSelect || p.cur().Keyword == KwValues || p.cur().Keyword == KwWith) {
		// SELECT/VALUES … inside IN (...). M0097-0020.
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

// acceptBetweenOrdering consumes an optional SYMMETRIC/ASYMMETRIC keyword
// following `[NOT] BETWEEN` and reports whether SYMMETRIC was present.
// ASYMMETRIC is the (already-default) no-op spelling; both are accepted for
// upstream compatibility since ASYMMETRIC is documented explicitly in PG's
// grammar even though it changes nothing.
func (p *parser) acceptBetweenOrdering() bool {
	if p.acceptKeyword(KwSymmetric) {
		return true
	}
	p.acceptKeyword(KwAsymmetric)
	return false
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
// `expr BETWEEN SYMMETRIC low AND high` becomes
//
//	(expr >= low AND expr <= high) OR (expr >= high AND expr <= low)
//
// since SYMMETRIC drops the requirement that low <= high (upstream
// desugars identically, see gram.y's `a_expr BETWEEN SYMMETRIC`
// production).
//
// `expr NOT BETWEEN [SYMMETRIC] low AND high` becomes
//
//	NOT (<the corresponding non-NOT expansion above>)
//
// so SQL three-valued logic flows through the same Kleene
// evaluator as `expr >= low AND expr <= high`. The `low` and
// `high` operands parse at `precAnd + 1` so the trailing `AND`
// terminates `low` instead of being consumed as a boolean
// conjunction inside it (e.g. `BETWEEN a AND b AND c` parses as
// `BETWEEN a AND b` followed by a top-level AND with c, matching
// upstream).
// parseQualifiedOperator parses OPERATOR(schema.op) and maps it to an OpCode.
// Returns (op, true) on success; the caller has already verified the leading
// OPERATOR( tokens are present.
func (p *parser) parseQualifiedOperator() (OpCode, bool) {
	p.advance() // OPERATOR
	p.advance() // (
	// Skip optional schema prefix: schema.
	if p.cur().Kind == TokenIdent && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "." {
		p.advance() // schema name
		p.advance() // .
	}
	// The operator itself is a TokenOperator or a symbol.
	opStr := ""
	if p.cur().Kind == TokenOperator || (p.cur().Kind == TokenSymbol && p.cur().Value != ")") {
		opStr = p.cur().Value
		p.advance()
	} else if p.cur().Kind == TokenIdent {
		opStr = p.cur().Value
		p.advance()
	}
	p.acceptSymbol(")") // consume )
	switch opStr {
	case "~":
		return OpRegexMatch, true
	case "~*":
		return OpRegexIMatch, true
	case "!~":
		return OpRegexNoMatch, true
	case "!~*":
		return OpRegexINoMatch, true
	case "=":
		return OpEq, true
	case "!=", "<>":
		return OpNe, true
	case "<":
		return OpLt, true
	case ">":
		return OpGt, true
	case "<=":
		return OpLe, true
	case ">=":
		return OpGe, true
	case "+":
		return OpAdd, true
	case "-":
		return OpSub, true
	case "*":
		return OpMul, true
	case "/":
		return OpDiv, true
	}
	return OpUnknown, false
}

func (p *parser) parseBetweenTail(left Expr, pos int, negated bool, symmetric bool) (Expr, error) {
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
	rangeExpr := func(lo, hi Expr) Expr {
		ge := &BinaryOp{pos: pos, Op: OpGe, Left: left, Right: lo}
		le := &BinaryOp{pos: pos, Op: OpLe, Left: left, Right: hi}
		return &BinaryOp{pos: pos, Op: OpAnd, Left: ge, Right: le}
	}
	var combined Expr
	if symmetric {
		combined = &BinaryOp{pos: pos, Op: OpOr, Left: rangeExpr(low, high), Right: rangeExpr(high, low)}
	} else {
		combined = rangeExpr(low, high)
	}
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

// parseGroupingCallExpr parses `GROUPING(expr [, ...])`. Requires at least
// one argument (upstream rejects the bare `GROUPING()` form too).
func (p *parser) parseGroupingCallExpr(pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after GROUPING")
	}
	var args []Expr
	for {
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close GROUPING")
	}
	return &GroupingCall{pos: pos, Args: args}, nil
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

	// SQL:1999/2003 form: SUBSTRING(str SIMILAR pattern ESCAPE escape).
	// PG oracle: postgres/src/backend/parser/gram.y substr_list production
	// `a_expr SIMILAR a_expr ESCAPE a_expr` — unlike the boolean `SIMILAR TO`
	// operator's optional ESCAPE clause (parseOptionalEscape), the grammar
	// makes ESCAPE mandatory here; there is no escape-optional alternative
	// for this production. Desugars (via system_functions.sql's
	// substring(text,text,text) SQL wrapper, `RETURN substring($1,
	// similar_to_escape($2, $3))`) to a plain 2-arg substring(str, pattern)
	// call once pattern/escape are constant-folded through
	// similarto.ConvertSubstring. M0134-0070; see
	// docs/design/m0134-0070-substring-similar-escape.md. The older SQL:1999
	// `SUBSTRING(str FROM pattern FOR escape)` overload spelling of this same
	// semantics is a separate, out-of-scope mechanism (overload resolution
	// on FROM/FOR operand types, not a distinct grammar production) — not
	// handled here.
	if t := p.cur(); t.Kind == TokenKeyword && t.Keyword == KwSimilar {
		p.advance() // SIMILAR
		pattern, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwEscape); err != nil {
			return nil, err
		}
		escape, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close SUBSTRING")
		}
		return p.buildSubstringSimilar(str, pattern, escape, pos)
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
		// Older SQL:1999 overload: SUBSTRING(str FROM pattern FOR escape). PG
		// desugars this spelling (`a_expr FROM a_expr FOR a_expr` in gram.y's
		// substr_list production) to the SAME 3-arg substring(text,text,text)
		// SQL wrapper as the SIMILAR form (system_functions.sql: `RETURN
		// substring($1, similar_to_escape($2, $3))`), disambiguating it from
		// `start FOR count` by function overload resolution on operand types
		// at plan time. goopg has no plan-time overload machinery, so
		// constant-fold here: when BOTH FROM and FOR operands are
		// string-or-NULL literals, route through the same buildSubstringSimilar
		// fold as the SIMILAR form. M0134-0070; see
		// docs/design/m0134-0070-substring-similar-escape.md §"Explicitly out
		// of scope".
		if _, _, startOK := similarToLiteralValue(start); startOK {
			if _, _, countOK := similarToLiteralValue(count); countOK {
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ')' to close SUBSTRING")
				}
				return p.buildSubstringSimilar(str, start, count, pos)
			}
		}
		args = append(args, count)
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close SUBSTRING")
	}
	return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: name}, Args: args}, nil
}

// buildSubstringSimilar constant-folds SUBSTRING(str SIMILAR pattern ESCAPE
// escape) (SQL:1999/2003 form) into a plain 2-arg substring(str,
// <converted-pattern>) FuncCall, mirroring buildSimilarTo's literal-check →
// validate-escape → convert → fold shape but adapted for this form's
// 3-operand STRICT semantics: str, pattern, AND escape all participate in
// NULL propagation (PG oracle: system_functions.sql's substring(text,text,
// text) wrapper is declared STRICT, so any of the three being SQL NULL
// makes the whole call NULL), not just pattern/escape as in buildSimilarTo's
// 2-operand boolean-operator case. The converted pattern lands directly on
// the existing evalSubstr/evalSubstrRegex 2-arg path — no executor changes.
// M0134-0070.
func (p *parser) buildSubstringSimilar(str, pattern, escape Expr, pos int) (Expr, error) {
	_, strNull, strOK := similarToLiteralValue(str)
	patVal, patNull, patOK := similarToLiteralValue(pattern)
	escVal, escNull, escOK := similarToLiteralValue(escape)
	if !strOK || !patOK || !escOK {
		return nil, p.errAtCur("SUBSTRING(... SIMILAR ... ESCAPE ...) with non-literal operands is not yet supported")
	}
	if strNull || patNull || escNull {
		return &NullConst{pos: pos}, nil
	}
	if err := similarto.ValidateEscape(escVal); err != nil {
		return nil, &SyntaxError{
			Pos: -1, Raw: true, Code: "22025",
			Message: "invalid escape string",
			Hint:    "Escape string must be empty or one character.",
		}
	}
	converted, err := similarto.ConvertSubstring(patVal, escVal)
	if err != nil {
		if err == similarto.ErrTooManyQuoteSeparators {
			return nil, &SyntaxError{
				Pos: -1, Raw: true, Code: "2200C",
				Message: err.Error(),
			}
		}
		return nil, err
	}
	return &FuncCall{
		pos:  pos,
		Name: ObjectName{pos: pos, Name: "substring"},
		Args: []Expr{
			str,
			&TypedStringLit{pos: pos, Type: "text", Value: converted},
		},
	}, nil
}


// parseOverlayFuncCall parses the SQL-standard
// OVERLAY(str PLACING replacement FROM start [FOR count]) syntax and
// desugars it to a plain overlay(str, replacement, start[, count]) FuncCall.
// PLACING is not a `KwXxx` token in goopg's lexer (unlike FROM/FOR), so it is
// matched via acceptIdentKeyword rather than expectKeyword. M0134-0070.
// PG oracle: postgres/src/backend/parser/gram.y:15910-15927 (OVERLAY
// productions), :16797-16808 (overlay_list — arg order target, replacement,
// start[, length]).
func (p *parser) parseOverlayFuncCall(pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after OVERLAY")
	}
	str, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.acceptIdentKeyword("placing") {
		return nil, p.errAtCur("expected PLACING")
	}
	repl, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwFrom); err != nil {
		return nil, err
	}
	start, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	args := []Expr{str, repl, start}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFor {
		p.advance() // consume FOR
		count, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, count)
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close OVERLAY")
	}
	return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: "overlay"}, Args: args}, nil
}

// parsePositionFuncCall handles the SQL-standard POSITION(substring IN
// string) form, alongside the goopg-legacy comma form
// POSITION(substring, string) — both desugar to the existing two-arg
// "position" FuncCall dispatch (internal/executor/expr.go,
// `case "strpos", "position":`). Modeled on parseSubstringFuncCall's
// dual-form pattern. PG oracle: postgres/src/backend/parser/gram.y,
// `POSITION_P '(' b_expr IN_P b_expr ')'` (func_expr_common_subexpr).
func (p *parser) parsePositionFuncCall(pos int) (Expr, error) {
	if !p.acceptSymbol("(") {
		return nil, p.errAtCur("expected '(' after POSITION")
	}
	// Parse the substring operand at precCompare+1 so a trailing IN
	// keyword is left for us to detect below rather than being consumed
	// by the generic precedence-climbing loop's `expr [NOT] IN (...)`
	// postfix check (which expects '(' right after IN and would error
	// on the SQL-standard `IN str` form here).
	sub, err := p.parseExprPrec(precCompare + 1)
	if err != nil {
		return nil, err
	}

	// Comma form: POSITION(sub, str). Already reachable via the generic
	// call-parsing fallback too, but handling it here keeps both forms
	// together in one place, mirroring parseSubstringFuncCall.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
		p.advance()
		str, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close POSITION")
		}
		return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: "position"}, Args: []Expr{sub, str}}, nil
	}

	// SQL-standard form: POSITION(sub IN str)
	if _, err := p.expectKeyword(KwIn); err != nil {
		return nil, err
	}
	str, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' to close POSITION")
	}
	// The executor's `case "strpos", "position":` (internal/executor/expr.go)
	// treats Args[0] as the haystack string and Args[1] as the needle
	// substring — the strpos(string, substring) convention. The SQL-standard
	// IN form is written substring-first (`POSITION(sub IN str)`), so the
	// desugared FuncCall args must be reordered to {str, sub} to match.
	return &FuncCall{pos: pos, Name: ObjectName{pos: pos, Name: "position"}, Args: []Expr{str, sub}}, nil
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
	// ARRAY(SELECT ...) — collect subquery rows into an array. M0097-0127.
	if len(parts) == 1 && strings.EqualFold(parts[0], "array") &&
		p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		// Peek: if the token after '(' is SELECT/VALUES/WITH keyword, treat as subquery.
		saved := p.idx
		p.advance() // consume '('
		if p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwSelect || p.cur().Keyword == KwWith) {
			inner, err := p.parseSelect()
			if err != nil {
				return nil, err
			}
			innerSel, ok := inner.(*SelectStmt)
			if !ok {
				return nil, p.errAtCur("expected SELECT inside ARRAY()")
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')' after ARRAY subquery")
			}
			return &ArraySubqueryExpr{pos: startPos, Inner: innerSel}, nil
		}
		// Not a subquery — restore position and fall through to function call.
		p.idx = saved
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
		// GROUPING(expr [, ...]) — SQL-standard pseudo-function valid
		// alongside GROUPING SETS/ROLLUP/CUBE. Not a real catalog
		// function (like EXTRACT), so intercept before the generic
		// call path. M0122-0004.
		if len(parts) == 1 && strings.EqualFold(parts[0], "grouping") {
			return p.parseGroupingCallExpr(startPos)
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
		// POSITION(substring IN string) — SQL standard, desugars to the
		// existing two-arg position(sub, str) FuncCall. M0134-0070.
		if len(parts) == 1 && strings.EqualFold(parts[0], "position") {
			return p.parsePositionFuncCall(startPos)
		}
		// OVERLAY(str PLACING replacement FROM start [FOR count]) — SQL
		// standard, desugars to overlay(str, replacement, start[, count]).
		// M0134-0070.
		if len(parts) == 1 && strings.EqualFold(parts[0], "overlay") {
			return p.parseOverlayFuncCall(startPos)
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
	if len(parts) == 1 && IsNoParenFuncName(parts[0]) {
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

// IsNoParenFuncName reports whether name (already lower-cased by the
// lexer) is one of the SQL standard niladic functions that don't
// require parentheses on the call side. Mirrors upstream's
// SystemFuncName classification — we cover the ones the executor
// already knows how to evaluate. Exported so default-expression
// renderers (catalog.formatExprForAttrdef, executor.defaultExprToSQL)
// can detect a parenless niladic FuncCall and deparse it the way
// PostgreSQL's get_sql_value_function does — as the bare uppercase
// keyword, NOT `name()` (DU-002 slice 174).
func IsNoParenFuncName(name string) bool {
	switch name {
	case "current_timestamp", "current_date", "current_time",
		"localtime", "localtimestamp",
		"current_user", "session_user", "user", "current_role",
		"current_catalog", "current_schema":
		return true
	}
	return false
}

// isNamedArgNameToken reports whether a token of kind k may serve as the name
// of a `name := value` / `name => value` named function argument. The name is
// an identifier, a quoted identifier, or an unreserved keyword (e.g. `index`,
// `skip` in pg_amcheck's amcheck calls); the trailing `:=`/`=>` lookahead the
// callers apply makes accepting keywords here unambiguous.
func isNamedArgNameToken(k TokenKind) bool {
	return k == TokenIdent || k == TokenQuotedIdent || k == TokenKeyword
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
		// Named argument: `name => value` or the legacy `name := value` form —
		// skip the name and use only the value. PostgreSQL named arguments are
		// positionally mapped for built-ins. M0097-0003. The `:=` spelling is the
		// pre-9.5 syntax that `pg_amcheck` still emits, e.g.
		// `verify_heapam(relation := c.oid, on_error_stop := false, …)` and
		// `bt_index_check(index := c.oid, heapallindexed := false)` (M0110-0003 S5).
		// The name may itself be an unreserved keyword (e.g. `index`, `skip`); the
		// trailing `:=`/`=>` lookahead disambiguates so accepting TokenKeyword here
		// is unambiguous.
		if isNamedArgNameToken(p.cur().Kind) &&
			p.peek(1).Kind == TokenOperator &&
			(p.peek(1).Value == "=>" || p.peek(1).Value == ":=") {
			p.advance() // skip name
			p.advance() // skip the `=>` / `:=` separator
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
			p.advance()                 // array
			p.advance()                 // [
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

// maybeWindowTail consumes optional `WITHIN GROUP (ORDER BY ...)`,
// `FILTER (WHERE ...)`, and/or `OVER (...)` clauses after a function-call's
// closing `)`.
// WITHIN GROUP (M0097-0035) stamps fc.WithinGroup.
// FILTER (M0097-0007) stamps fc.Filter; OVER (M0020) stamps fc.Over.
func (p *parser) maybeWindowTail(fc *FuncCall) (Expr, error) {
	// WITHIN GROUP (ORDER BY sortlist) — ordered-set aggregate.
	// `within` is an identifier (not a reserved keyword), so use acceptIdentKeyword.
	// Record the `within` token's offset: PG's "cannot use multiple ORDER BY
	// clauses with WITHIN GROUP" caret points at this keyword (gram.y
	// func_expr: parser_errposition(@2)). M0134-0001 P3b.
	withinPos := 0
	if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "within") {
		withinPos = t.Pos
	}
	if p.acceptIdentKeyword("within") {
		fc.WithinGroupPos = withinPos
		if !p.acceptKeyword(KwGroup) {
			return nil, p.errAtCur("expected GROUP after WITHIN")
		}
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after WITHIN GROUP")
		}
		if !p.acceptKeyword(KwOrder) {
			return nil, p.errAtCur("expected ORDER after WITHIN GROUP (")
		}
		if !p.acceptKeyword(KwBy) {
			return nil, p.errAtCur("expected BY after WITHIN GROUP (ORDER")
		}
		wg, wgerr := p.parseSortList()
		if wgerr != nil {
			return nil, wgerr
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close WITHIN GROUP clause")
		}
		fc.WithinGroup = wg
	}
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
// [ORDER BY sortlist] [frame clause] )` body, or the bare `OVER
// window_name` form (M0020 named-window slice) that defers
// PartitionBy/OrderBy/Frame to the analyzer's WindowClause lookup.
func (p *parser) parseWindowDef() (*WindowDef, error) {
	t, err := p.expectKeyword(KwOver)
	if err != nil {
		return nil, err
	}
	if p.acceptSymbol("(") {
		return p.parseWindowSpecBody(t.Pos)
	}
	if p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent {
		name := p.cur().Value
		p.advance()
		return &WindowDef{pos: t.Pos, RefName: name, IsBareRef: true}, nil
	}
	return nil, p.errAtCur("expected '(' or window name after OVER")
}

// parseWindowSpecBody parses the shared `[PARTITION BY exprs]
// [ORDER BY sortlist] )` body used both by an anonymous `OVER (...)`
// tail and by a named `WINDOW name AS (...)` clause item — the two
// forms must stay byte-for-byte in sync (pattern_sibling_paths_must_agree),
// so they share this single implementation. The caller has already
// consumed the opening `(`; pos is the position to stamp on the result.
func (p *parser) parseWindowSpecBody(pos int) (*WindowDef, error) {
	wd := &WindowDef{pos: pos}
	// Optional existing_window_name (SQL:2008 7.11 <window clause> syntax
	// rule 10 / gram.y's opt_existing_window_name): a bare identifier
	// immediately after '(' that isn't one of the frame-mode words, which
	// must stay available to parseFrameClause below (mirrors gram.y's
	// PARTITION/RANGE/ROWS/GROUPS precedence trick — those unreserved
	// keywords are never taken as an existing_window_name). Merging against
	// the referenced window (partition/order/frame override rules) happens
	// later in the analyzer, once all named windows are visible.
	if t := p.cur(); t.Kind == TokenQuotedIdent ||
		(t.Kind == TokenIdent && !strings.EqualFold(t.Value, "rows") &&
			!strings.EqualFold(t.Value, "range") && !strings.EqualFold(t.Value, "groups")) {
		wd.RefName = t.Value
		p.advance()
	}
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
	frame, err := p.parseFrameClause()
	if err != nil {
		return nil, err
	}
	wd.Frame = frame
	if !p.acceptSymbol(")") {
		return nil, p.errAtCur("expected ')' after window definition")
	}
	return wd, nil
}

// parseFrameClause parses the optional `{ROWS|RANGE|GROUPS} frame_extent
// [frame_exclusion]` window frame clause trailing a window spec's
// PARTITION BY/ORDER BY (M0122-0004). Returns nil, nil when no frame
// clause is present — the default frame applies (see
// internal/executor/operators_window.go). Mirrors gram.y's
// opt_frame_clause/frame_extent productions; ROWS/RANGE/GROUPS,
// UNBOUNDED, PRECEDING, FOLLOWING, CURRENT, EXCLUDE, TIES, and OTHERS
// are all soft (unreserved) keywords like WITHIN/FILTER/WINDOW, so no
// lexer/token-table change is needed. Bound-ordering validation
// (SQLSTATE 42P20) and the RANGE/GROUPS scope limitation are left to
// the analyzer, which is the only place in this codebase that raises
// non-syntax SQLSTATEs.
func (p *parser) parseFrameClause() (*WindowFrame, error) {
	var mode FrameMode
	switch {
	case p.acceptIdentKeyword("rows"):
		mode = FrameModeRows
	case p.acceptIdentKeyword("range"):
		mode = FrameModeRange
	case p.acceptIdentKeyword("groups"):
		mode = FrameModeGroups
	default:
		return nil, nil
	}
	fr := &WindowFrame{Mode: mode, EndKind: FrameBoundCurrentRow}
	if p.acceptKeyword(KwBetween) {
		startKind, startOffset, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		if _, err := p.expectKeyword(KwAnd); err != nil {
			return nil, err
		}
		endKind, endOffset, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		fr.StartKind, fr.StartOffset = startKind, startOffset
		fr.EndKind, fr.EndOffset = endKind, endOffset
		fr.HasBetween = true
	} else {
		startKind, startOffset, err := p.parseFrameBound()
		if err != nil {
			return nil, err
		}
		fr.StartKind, fr.StartOffset = startKind, startOffset
		// frame_extent: frame_bound (single-bound form) — end defaults
		// to CURRENT ROW (already set above).
	}
	excl, err := p.parseFrameExclusion()
	if err != nil {
		return nil, err
	}
	fr.Exclusion = excl
	return fr, nil
}

// parseFrameBound parses one `UNBOUNDED PRECEDING|FOLLOWING`,
// `CURRENT ROW`, or `<expr> PRECEDING|FOLLOWING` frame bound.
func (p *parser) parseFrameBound() (FrameBoundKind, Expr, error) {
	switch {
	case p.acceptIdentKeyword("unbounded"):
		switch {
		case p.acceptIdentKeyword("preceding"):
			return FrameBoundUnboundedPreceding, nil, nil
		case p.acceptIdentKeyword("following"):
			return FrameBoundUnboundedFollowing, nil, nil
		default:
			return 0, nil, p.errAtCur("expected PRECEDING or FOLLOWING after UNBOUNDED")
		}
	case p.acceptIdentKeyword("current"):
		if !p.acceptIdentKeyword("row") {
			return 0, nil, p.errAtCur("expected ROW after CURRENT")
		}
		return FrameBoundCurrentRow, nil, nil
	default:
		offset, err := p.parseExpr()
		if err != nil {
			return 0, nil, err
		}
		switch {
		case p.acceptIdentKeyword("preceding"):
			return FrameBoundOffsetPreceding, offset, nil
		case p.acceptIdentKeyword("following"):
			return FrameBoundOffsetFollowing, offset, nil
		default:
			return 0, nil, p.errAtCur("expected PRECEDING or FOLLOWING after frame bound offset")
		}
	}
}

// parseFrameExclusion parses the optional `EXCLUDE {CURRENT ROW |
// GROUP | TIES | NO OTHERS}` clause. Returns FrameExcludeNone (which
// also represents the omitted clause) when EXCLUDE isn't present.
func (p *parser) parseFrameExclusion() (FrameExclusion, error) {
	if !p.acceptIdentKeyword("exclude") {
		return FrameExcludeNone, nil
	}
	switch {
	case p.acceptIdentKeyword("current"):
		if !p.acceptIdentKeyword("row") {
			return 0, p.errAtCur("expected ROW after EXCLUDE CURRENT")
		}
		return FrameExcludeCurrentRow, nil
	case p.acceptKeyword(KwGroup):
		return FrameExcludeGroup, nil
	case p.acceptIdentKeyword("ties"):
		return FrameExcludeTies, nil
	case p.acceptIdentKeyword("no"):
		if !p.acceptIdentKeyword("others") {
			return 0, p.errAtCur("expected OTHERS after EXCLUDE NO")
		}
		return FrameExcludeNone, nil
	default:
		return 0, p.errAtCur("expected CURRENT ROW, GROUP, TIES, or NO OTHERS after EXCLUDE")
	}
}

// parseWindowClauseList parses the comma-separated `name AS (...)`
// items of a trailing SELECT `WINDOW` clause (M0020 named-window
// slice). Each item's body is the same PARTITION BY/ORDER BY spec as
// an anonymous OVER(...), parsed via the shared parseWindowSpecBody.
func (p *parser) parseWindowClauseList() ([]NamedWindowDef, error) {
	var items []NamedWindowDef
	for {
		if p.cur().Kind != TokenIdent && p.cur().Kind != TokenQuotedIdent {
			return nil, p.errAtCur("expected window name")
		}
		name := p.cur().Value
		namePos := p.cur().Pos
		p.advance()
		if !p.acceptKeyword(KwAs) {
			return nil, p.errAtCur("expected AS after window name")
		}
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after AS in window definition")
		}
		wd, err := p.parseWindowSpecBody(namePos)
		if err != nil {
			return nil, err
		}
		items = append(items, NamedWindowDef{Name: name, Def: wd})
		if p.acceptSymbol(",") {
			continue
		}
		break
	}
	return items, nil
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

// parseRowsFrom handles the ROWS FROM(func1(args), func2(args), ...) [WITH ORDINALITY] syntax.
// Each entry is a function call that produces a set of rows; the results are
// zipped together (shorter arrays are NULL-padded to the longest).
func (p *parser) parseRowsFrom() (RangeVar, error) {
	p.advance() // consume "rows"
	pos := p.cur().Pos
	p.advance() // consume FROM
	if !p.acceptSymbol("(") {
		return RangeVar{}, p.errAtCur("expected '(' after ROWS FROM")
	}
	var entries []RowsFromEntry
	for {
		// Parse function name
		obj, err := p.parseObjectName()
		if err != nil {
			return RangeVar{}, err
		}
		if !p.acceptSymbol("(") {
			return RangeVar{}, p.errAtCur("expected '(' for function in ROWS FROM")
		}
		var args []Expr
		if !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") {
			for {
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
			return RangeVar{}, p.errAtCur("expected ')' after ROWS FROM function arguments")
		}
		entries = append(entries, RowsFromEntry{Name: obj.Name, Args: args})
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return RangeVar{}, p.errAtCur("expected ')' after ROWS FROM list")
	}

	rv := RangeVar{pos: pos}
	rv.TableFunc = &TableFuncRef{pos: pos, RowsFuncs: entries}
	// WITH ORDINALITY
	if p.acceptKeyword(KwWith) {
		if !p.acceptIdentKeyword("ordinality") {
			return RangeVar{}, p.errAtCur("expected ORDINALITY after WITH")
		}
		rv.TableFunc.WithOrdinality = true
	}
	// Parse alias
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
	// Optional column alias list
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance()
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
