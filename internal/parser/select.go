package parser

import (
	"strconv"
	"strings"
)

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
	t, err := p.expectKeyword(KwSelect)
	if err != nil {
		return nil, err
	}
	s := &SelectStmt{pos: t.Pos}
	if p.acceptKeyword(KwDistinct) {
		s.Distinct = true
	}
	tgts, err := p.parseTargetList()
	if err != nil {
		return nil, err
	}
	s.Targets = tgts

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
		list, err := p.parseExprList()
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
		if !p.acceptIdentKeyword("only") {
			return nil, p.errAtCur("expected ONLY after FETCH … ROW(S) (WITH TIES is not supported in v0)")
		}
		if s.Limit != nil {
			return nil, p.errAtCur("LIMIT and FETCH FIRST cannot both be specified")
		}
		s.Limit = count
	}
	if setOp, ok, err := p.parseSetOpClause(); err != nil {
		return nil, err
	} else if ok {
		s.SetOp = setOp
	}
	return s, nil
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
	rhsStmt, err := p.parseSelect()
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
		alias, err := p.parseIdent()
		if err != nil {
			return ResTarget{}, err
		}
		rt.Alias = identText(alias)
		return rt, nil
	}
	// Implicit alias: a bare identifier or quoted ident immediately
	// after the expression — but the v0 expression parser already
	// consumes those into a ColumnRef, so we don't try to peel one
	// off here. AS is the only path until the analyzer disambiguates.
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
	obj, err := p.parseObjectName()
	if err != nil {
		return RangeVar{}, err
	}
	rv := RangeVar{pos: obj.pos, Schema: obj.Schema, Name: obj.Name}
	// Optional alias: AS ident, or bare ident for the "implicit alias"
	// shorthand that pgbench uses (`pgbench_accounts a`).
	if p.acceptKeyword(KwAs) {
		t, err := p.parseIdent()
		if err != nil {
			return RangeVar{}, err
		}
		rv.Alias = identText(t)
		return rv, nil
	}
	if isAliasStart(p.cur()) {
		t := p.advance()
		rv.Alias = identText(t)
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

func (p *parser) parseSortItem() (SortBy, error) {
	pos := p.cur().Pos
	e, err := p.parseExpr()
	if err != nil {
		return SortBy{}, err
	}
	sb := SortBy{pos: pos, Expr: e}
	if p.acceptKeyword(KwDesc) {
		sb.Desc = true
	} else {
		_ = p.acceptKeyword(KwAsc)
	}
	return sb, nil
}

// --- Expression parsing (Pratt / precedence climbing) ---------------

// Precedence levels (higher binds tighter), aligned with upstream's
// gram.y operator precedence.
const (
	precOr      = 1
	precAnd     = 2
	precNot     = 3
	precIs      = 4
	precCompare = 5 // = <> < > <= >=
	precAddSub  = 6
	precMulDiv  = 7
	precConcat  = 8
	precUnary   = 9
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

// parseCastTail consumes a single `:: typename [(typmods)]` after the
// caller has already produced the operand expression. Returns the
// CastExpr; the caller chains by re-checking for `::` after.
func (p *parser) parseCastTail(operand Expr) (Expr, error) {
	tok := p.advance() // consumes "::"
	name, err := p.parseTypeNameAfterCast()
	if err != nil {
		return nil, err
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

// peekBinaryOp returns the operator text and precedence of the current
// token if it can extend an expression as a left-associative binary
// operator. Returns ok=false when the current token can't.
func (p *parser) peekBinaryOp() (string, int, bool) {
	t := p.cur()
	switch t.Kind {
	case TokenOperator:
		switch t.Value {
		case "+", "-":
			return t.Value, precAddSub, true
		case "*", "/", "%":
			return t.Value, precMulDiv, true
		case "||":
			return t.Value, precConcat, true
		case "=", "<", ">", "<=", ">=", "<>", "!=":
			return t.Value, precCompare, true
		}
	case TokenSymbol:
		// '*' is also a symbol token (target-list wildcard) — but in
		// expression context it's a multiplication operator.
		if t.Value == "*" {
			return "*", precMulDiv, true
		}
	case TokenKeyword:
		switch t.Keyword {
		case KwAnd:
			return "AND", precAnd, true
		case KwOr:
			return "OR", precOr, true
		}
	}
	return "", 0, false
}

// parseUnary handles prefix operators and falls through to parsePrimary.
func (p *parser) parseUnary() (Expr, error) {
	t := p.cur()
	switch {
	case t.Kind == TokenOperator && (t.Value == "-" || t.Value == "+"):
		p.advance()
		operand, err := p.parseExprPrec(precUnary)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: t.Value, Operand: operand}, nil
	case t.Kind == TokenKeyword && t.Keyword == KwNot:
		p.advance()
		operand, err := p.parseExprPrec(precNot)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: t.Pos, Op: "NOT", Operand: operand}, nil
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
		v, err := strconv.ParseInt(t.Value, 10, 64)
		if err != nil {
			return nil, &SyntaxError{Pos: t.Pos, Message: "invalid integer literal: " + t.Value}
		}
		return &IntegerConst{pos: t.Pos, Value: v}, nil
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
			inner, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
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
	case "date", "timestamp", "timestamptz":
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
		unitTok := p.peek(2)
		if unitTok.Kind != TokenIdent {
			return nil, false
		}
		unit := strings.ToLower(identText(unitTok))
		switch unit {
		case "day", "days", "month", "months", "year", "years":
			// Normalise plural→singular so the executor
			// only sees three unit values.
			canonical := strings.TrimSuffix(unit, "s")
			p.advance() // INTERVAL
			strTok := p.advance()
			p.advance() // unit
			return &IntervalLit{pos: t.Pos, Value: strTok.Value, Unit: canonical}, true
		}
	}
	return nil, false
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
	// Function call?
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		// EXTRACT has its own grammar: EXTRACT(field FROM expr).
		// Field is a positional identifier, not a value expr,
		// so the regular comma-arg parser would mishandle it.
		// Match case-insensitively on the bare ident form.
		if len(parts) == 1 && strings.EqualFold(parts[0], "extract") {
			return p.parseExtractExpr(startPos)
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
		return fc, nil
	}
	// `f(*)` — count(*) etc.
	if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
		p.advance()
		fc.Star = true
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
		return fc, nil
	}
	if p.acceptKeyword(KwDistinct) {
		fc.Distinct = true
	}
	for {
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		fc.Args = append(fc.Args, arg)
		if p.acceptSymbol(",") {
			continue
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')'")
		}
		return fc, nil
	}
}
