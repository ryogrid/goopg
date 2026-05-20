package parser

import (
	"fmt"
	"strings"
)

// CREATE / DROP FUNCTION parser surface for M0015 Stage A
// (function-first delivery). Parser-only slice — analyzer rejects the
// resulting AST node with SQLSTATE 0A000 until subsequent loops add
// PL/pgSQL execution support.
//
// Grammar (Stage A scope):
//
//   CREATE [OR REPLACE] FUNCTION name([arg [, ...]])
//       RETURNS rettype
//       [LANGUAGE lang]
//       AS $$body$$
//
//   arg ::= [arg_name] [IN] type [DEFAULT expr]
//
//   DROP FUNCTION [IF EXISTS] name [(arg [, ...])]
//        [CASCADE | RESTRICT]
//
// Procedure / CALL / OUT / INOUT / VARIADIC live in Stage B
// (M0015 procedure follow-up) and aren't part of this slice.
//
// See docs/design/0015-0001-create-function-parser-and-ast.md.

// parseCreateFunctionTail picks up after CREATE [OR REPLACE]
// FUNCTION and returns a populated CreateFunctionStmt. Caller has
// already consumed the leading CREATE [OR REPLACE] FUNCTION tokens.
func (p *parser) parseCreateFunctionTail(pos int, orReplace bool) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	args, err := p.parseFunctionArgList()
	if err != nil {
		return nil, err
	}
	if _, err := p.expectKeyword(KwReturns); err != nil {
		return nil, err
	}
	// Accept optional SETOF modifier (set-returning functions). M0096-0007.
	_ = p.acceptIdentKeyword("setof")
	retType, err := p.parseColumnType()
	if err != nil {
		return nil, err
	}
	stmt := &CreateFunctionStmt{
		pos:        pos,
		OrReplace:  orReplace,
		Name:       name,
		Args:       args,
		ReturnType: retType,
	}
	// The LANGUAGE / AS clauses can appear in either order (mirrors
	// upstream). Loop until both have been seen or we hit an
	// unrecognised token.
	sawLanguage := false
	sawAs := false
	for {
		switch {
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwLanguage:
			if sawLanguage {
				return nil, p.errAtCur("duplicate LANGUAGE clause")
			}
			p.advance()
			lang, err := p.parseLanguageName()
			if err != nil {
				return nil, err
			}
			stmt.Language = lang
			sawLanguage = true
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs:
			if sawAs {
				return nil, p.errAtCur("duplicate AS clause")
			}
			p.advance()
			body, err := p.parseFunctionBody()
			if err != nil {
				return nil, err
			}
			stmt.Body = body
			sawAs = true
		case p.isFunctionAttribute():
			// Consume IMMUTABLE/VOLATILE/STABLE/STRICT/SECURITY DEFINER
			// and other function attributes; they have no runtime effect
			// in goopg but must be parsed to reach the AS $$body$$ clause.
			p.consumeFunctionAttribute()
		default:
			if !sawAs {
				return nil, p.errAtCur("expected AS $$body$$ for CREATE FUNCTION")
			}
			return stmt, nil
		}
	}
}

// parseLanguageName accepts either an identifier (the upstream
// surface — LANGUAGE plpgsql) or a single-quoted string literal
// (LANGUAGE 'plpgsql', also upstream-compatible). Returns the
// lower-cased language name.
func (p *parser) parseLanguageName() (string, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent:
		p.advance()
		return identText(t), nil
	case TokenStringLit:
		p.advance()
		return strings.ToLower(t.Value), nil
	}
	return "", p.errAtCur("expected language name after LANGUAGE")
}

// parseFunctionBody accepts either a dollar-quoted string literal or
// a SQL-standard `BEGIN ATOMIC stmts; END` body. M0097-0012.
func (p *parser) parseFunctionBody() (string, error) {
	t := p.cur()
	if t.Kind == TokenStringLit {
		p.advance()
		return t.Value, nil
	}
	// SQL-standard body: BEGIN ATOMIC ... END
	if t.Kind == TokenKeyword && t.Keyword == KwBegin {
		// Collect token values between BEGIN ATOMIC and the matching END.
		p.advance() // BEGIN
		_ = p.acceptIdentKeyword("atomic")
		var parts []string
		depth := 1
		for depth > 0 && p.cur().Kind != TokenEOF {
			ct := p.cur()
			if ct.Kind == TokenKeyword && ct.Keyword == KwBegin {
				depth++
			} else if ct.Kind == TokenKeyword && ct.Keyword == KwEnd {
				depth--
				if depth == 0 {
					p.advance() // END
					p.acceptSymbol(";")
					return strings.Join(parts, " "), nil
				}
			}
			parts = append(parts, ct.Value)
			p.advance()
		}
		return "", p.errAtCur("unterminated BEGIN ATOMIC body")
	}
	return "", p.errAtCur("expected $$body$$ or BEGIN ATOMIC for function body")
}

// parseFunctionArgList parses an optional `( arg [, ...] )` list.
// Returns nil for the no-parenthesis case (caller distinguishes
// "no arg list" from "empty arg list" — both are equivalent for
// CreateFunctionStmt but matter for DropFunctionStmt overload
// resolution).
func (p *parser) parseFunctionArgList() ([]FunctionArg, error) {
	if !p.acceptSymbol("(") {
		return nil, nil
	}
	if p.acceptSymbol(")") {
		// Explicit empty list — distinguish from absent list by
		// returning a non-nil empty slice.
		return []FunctionArg{}, nil
	}
	args := make([]FunctionArg, 0, 2)
	for {
		arg, err := p.parseFunctionArg()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if p.acceptSymbol(",") {
			continue
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')' in function arg list")
		}
		return args, nil
	}
}

// parseProcedureArgList parses an optional `( arg [, ...] )` list
// for CREATE PROCEDURE, using parseProcedureArg which accepts
// OUT/INOUT/VARIADIC modes.
func (p *parser) parseProcedureArgList() ([]FunctionArg, error) {
	if !p.acceptSymbol("(") {
		return nil, nil
	}
	if p.acceptSymbol(")") {
		return []FunctionArg{}, nil
	}
	args := make([]FunctionArg, 0, 2)
	for {
		arg, err := p.parseProcedureArg()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if p.acceptSymbol(",") {
			continue
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ',' or ')' in procedure arg list")
		}
		return args, nil
	}
}

func (p *parser) parseFunctionArg() (FunctionArg, error) {
	pos := p.cur().Pos
	arg := FunctionArg{pos: pos, Mode: FuncArgIn}

	// Reject Stage B mode keywords for functions (but not procedures).
	if err := p.rejectStageBModes(pos); err != nil {
		return FunctionArg{}, err
	}

	// Optional `IN` mode keyword.
	if p.acceptKeyword(KwIn) {
		arg.Mode = FuncArgIn
	}

	return p.parseArgNameAndType(pos, arg)
}

// parseProcedureArg parses a single procedure argument: `[name] [IN|OUT|INOUT] type
// [DEFAULT expr]`. Stage B allows OUT, INOUT, and VARIADIC modes.
func (p *parser) parseProcedureArg() (FunctionArg, error) {
	pos := p.cur().Pos
	arg := FunctionArg{pos: pos, Mode: FuncArgIn}

	// Accept IN / OUT / INOUT / VARIADIC mode keywords.
	if p.cur().Kind == TokenKeyword {
		switch p.cur().Keyword {
		case KwIn:
			p.advance()
			arg.Mode = FuncArgIn
		case KwOut:
			p.advance()
			arg.Mode = FuncArgOut
		case KwInout:
			p.advance()
			arg.Mode = FuncArgInout
		case KwVariadic:
			p.advance()
			arg.Mode = FuncArgVariadic
		}
	}

	return p.parseArgNameAndType(pos, arg)
}

// rejectStageBModes returns an error if the current token is an
// OUT / INOUT / VARIADIC keyword, which are not valid in CREATE FUNCTION.
func (p *parser) rejectStageBModes(pos int) error {
	if p.cur().Kind == TokenIdent {
		if isReservedFutureMode(p.cur().Value) {
			return p.errAtCur(fmt.Sprintf(
				"function argument mode %q is not supported in v0 (Stage A: IN only)",
				p.cur().Value))
		}
	}
	if p.cur().Kind == TokenKeyword {
		switch p.cur().Keyword {
		case KwOut, KwInout, KwVariadic:
			return p.errAtCur(fmt.Sprintf(
				"function argument mode %q is not supported in v0 (Stage A: IN only)",
				p.cur().Value))
		}
	}
	return nil
}

// parseArgNameAndType handles the common `[name] type [DEFAULT expr]` portion
// shared by parseFunctionArg and parseProcedureArg.
func (p *parser) parseArgNameAndType(pos int, arg FunctionArg) (FunctionArg, error) {
	// Distinguish `arg_name TYPE` from `TYPE` (positional only):
	// Look at the next two non-comma tokens. If we see
	// `ident <type-start>` where <type-start> is another ident or
	// keyword, treat the first ident as the name. If we see
	// `ident )` or `ident ,` or `ident DEFAULT`, treat ident as
	// the type with no name.
	if p.cur().Kind == TokenIdent {
		save := p.idx
		nameTok := p.cur()
		p.advance()
		if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
			// Looks like `name type` — but only if the next
			// token isn't itself a separator.
			next := p.cur()
			if next.Kind == TokenIdent || (next.Kind == TokenKeyword && !isSeparatorKeyword(next.Keyword)) {
				arg.Name = identText(nameTok)
			} else {
				// Rewind: treat nameTok as the type name.
				p.idx = save
			}
		} else {
			// nameTok is the type, no name. Rewind.
			p.idx = save
		}
	}

	colType, err := p.parseColumnType()
	if err != nil {
		return FunctionArg{}, err
	}
	arg.Type = colType

	if p.acceptKeyword(KwDefault) {
		expr, err := p.parseExpr()
		if err != nil {
			return FunctionArg{}, err
		}
		arg.Default = expr
	}
	return arg, nil
}

// isReservedFutureMode reports whether the lower-cased identifier
// names a function-argument mode that Stage A explicitly defers.
// Stage B drops these names from the identifier path back into the
// keyword path.
func isReservedFutureMode(s string) bool {
	switch s {
	case "out", "inout", "variadic":
		return true
	}
	return false
}

// isSeparatorKeyword reports whether the keyword acts as an
// argument-list separator after a name token (`name DEFAULT expr`,
// `name )`, etc.) — used by parseFunctionArg to distinguish
// `name type` from `type-only-no-name`.
func isSeparatorKeyword(k Keyword) bool {
	switch k {
	case KwDefault:
		return true
	}
	return false
}

// parseDropFunctionTail picks up after DROP FUNCTION (the leading
// keywords are already consumed). Parses [IF EXISTS] name
// [(arg, ...)] [CASCADE|RESTRICT].
func (p *parser) parseDropFunctionTail(pos int) (Stmt, error) {
	stmt := &DropFunctionStmt{pos: pos}
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
	args, err := p.parseFunctionArgList()
	if err != nil {
		return nil, err
	}
	stmt.Args = args
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}

// parseCreateProcedureTail picks up after CREATE [OR REPLACE]
// PROCEDURE and returns a populated CreateProcedureStmt. Caller has
// already consumed the leading CREATE [OR REPLACE] PROCEDURE tokens.
// Stage B (procedure follow-up) of M0015.
func (p *parser) parseCreateProcedureTail(pos int, orReplace bool) (Stmt, error) {
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	args, err := p.parseProcedureArgList()
	if err != nil {
		return nil, err
	}
	stmt := &CreateProcedureStmt{
		pos:       pos,
		OrReplace: orReplace,
		Name:      name,
		Args:      args,
	}
	// LANGUAGE / AS clauses in either order (mirrors CREATE FUNCTION)
	sawLanguage := false
	sawAs := false
	for {
		switch {
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwLanguage:
			if sawLanguage {
				return nil, p.errAtCur("duplicate LANGUAGE clause")
			}
			p.advance()
			lang, err := p.parseLanguageName()
			if err != nil {
				return nil, err
			}
			stmt.Language = lang
			sawLanguage = true
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs:
			if sawAs {
				return nil, p.errAtCur("duplicate AS clause")
			}
			p.advance()
			body, err := p.parseFunctionBody()
			if err != nil {
				return nil, err
			}
			stmt.Body = body
			sawAs = true
		case p.isFunctionAttribute():
			p.consumeFunctionAttribute()
		default:
			if !sawAs {
				return nil, p.errAtCur("expected AS $$body$$ for CREATE PROCEDURE")
			}
			return stmt, nil
		}
	}
}

// parseCallStatement parses `CALL proc_name([expr [, ...]])`.
// Stage B (procedure follow-up) of M0015.
func (p *parser) parseCallStatement(pos int) (Stmt, error) {
	// CALL keyword already consumed
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	stmt := &CallStmt{
		pos:  pos,
		Name: name,
	}
	// Optional argument list
	if p.acceptSymbol("(") {
		args := make([]Expr, 0, 2)
		if !p.acceptSymbol(")") {
			for {
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				args = append(args, expr)
				if p.acceptSymbol(",") {
					continue
				}
				if !p.acceptSymbol(")") {
					return nil, p.errAtCur("expected ',' or ')' in CALL argument list")
				}
				break
			}
		}
		stmt.Args = args
	}
	return stmt, nil
}

// isFunctionAttribute reports whether the current token starts a CREATE
// FUNCTION / CREATE PROCEDURE attribute clause (IMMUTABLE, VOLATILE, STABLE,
// STRICT, SECURITY DEFINER, PARALLEL, COST, ROWS, etc.).
func (p *parser) isFunctionAttribute() bool {
	cur := p.cur()
	if cur.Kind == TokenKeyword {
		switch cur.Keyword {
		case KwNot, KwParallel:
			return true
		}
	}
	if cur.Kind == TokenIdent {
		switch strings.ToLower(cur.Value) {
		case "immutable", "volatile", "stable", "strict",
			"security", "external", "leakproof", "window",
			"called", "returns", "cost", "rows", "support", "set":
			return true
		}
	}
	return false
}

// consumeFunctionAttribute advances past a single function attribute token or
// clause (e.g. "IMMUTABLE", "SECURITY DEFINER", "PARALLEL SAFE", "COST 100").
func (p *parser) consumeFunctionAttribute() {
	cur := p.cur()
	switch {
	case cur.Kind == TokenKeyword && cur.Keyword == KwNot:
		p.advance()
		p.acceptIdentKeyword("leakproof")
	case cur.Kind == TokenKeyword && cur.Keyword == KwParallel:
		p.advance()
		p.acceptIdentKeyword("safe", "unsafe", "restricted")
	case cur.Kind == TokenIdent:
		switch strings.ToLower(cur.Value) {
		case "immutable", "volatile", "stable", "strict", "leakproof", "window":
			p.advance()
		case "security":
			p.advance()
			p.acceptIdentKeyword("definer", "invoker")
		case "external":
			p.advance()
			p.acceptIdentKeyword("security")
			p.acceptIdentKeyword("definer", "invoker")
		case "called":
			// CALLED ON NULL INPUT
			p.advance()
			p.acceptIdentKeyword("on")
			p.acceptIdentKeyword("null")
			p.acceptIdentKeyword("input")
		case "returns":
			// RETURNS NULL ON NULL INPUT
			p.advance()
			p.acceptIdentKeyword("null")
			p.acceptIdentKeyword("on")
			p.acceptIdentKeyword("null")
			p.acceptIdentKeyword("input")
		case "cost", "rows":
			p.advance()
			// consume the numeric argument
			if p.cur().Kind == TokenIntLit || p.cur().Kind == TokenNumericLit {
				p.advance()
			}
		case "support":
			p.advance()
			// consume the support function name
			_, _ = p.parseObjectName()
		case "set":
			// SET guc TO value — skip to end of clause
			p.advance()
			_, _ = p.parseObjectName()
			if p.acceptKeyword(KwTo) || (p.cur().Kind == TokenOperator && p.cur().Value == "=") {
				if p.cur().Kind == TokenOperator {
					p.advance()
				}
				for !(p.cur().Kind == TokenSymbol && p.cur().Value == ";") && p.cur().Kind != TokenEOF &&
					!(p.cur().Kind == TokenKeyword && (p.cur().Keyword == KwAs || p.cur().Keyword == KwLanguage)) {
					p.advance()
				}
			}
		default:
			p.advance()
		}
	default:
		p.advance()
	}
}

// parseDropProcedureTail picks up after DROP PROCEDURE and returns
// a populated DropProcedureStmt (mirrors parseDropFunctionTail).
func (p *parser) parseDropProcedureTail(pos int) (Stmt, error) {
	stmt := &DropProcedureStmt{pos: pos}
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
	args, err := p.parseFunctionArgList()
	if err != nil {
		return nil, err
	}
	stmt.Args = args
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}
