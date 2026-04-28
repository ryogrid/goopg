package parser

import (
	"strconv"
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
// excludes any keyword that would start the next clause.
func isAliasStart(t Token) bool {
	if t.Kind == TokenIdent || t.Kind == TokenQuotedIdent {
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
		}
	}
	if t.Kind == TokenIdent || t.Kind == TokenQuotedIdent {
		return p.parseColumnOrCall()
	}
	return nil, p.errAtCur("expected expression")
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
