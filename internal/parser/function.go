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

// parseFunctionBody requires a dollar-quoted string literal — the
// only body form Stage A accepts. A plain single-quoted string is
// upstream-legal but is fragile (every internal quote needs
// escaping) and almost nobody writes it; rejecting it here surfaces
// a clean diagnostic now and keeps the parser surface narrow.
func (p *parser) parseFunctionBody() (string, error) {
	t := p.cur()
	if t.Kind != TokenStringLit {
		return "", p.errAtCur("expected $$body$$ for function body")
	}
	p.advance()
	return t.Value, nil
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

// parseFunctionArg parses a single argument: `[name] [IN] type
// [DEFAULT expr]`. Stage A pins Mode=FuncArgIn; OUT / INOUT /
// VARIADIC surface a clean parse error so handwritten functions
// using those modes get a specific diagnostic instead of a generic
// "expected type".
func (p *parser) parseFunctionArg() (FunctionArg, error) {
	pos := p.cur().Pos
	arg := FunctionArg{pos: pos, Mode: FuncArgIn}

	// Reject Stage B keywords explicitly. KwOut / KwInout / KwVariadic
	// are now registered keywords; check both keyword and ident forms.
	if p.cur().Kind == TokenIdent {
		if isReservedFutureMode(p.cur().Value) {
			return FunctionArg{}, p.errAtCur(fmt.Sprintf(
				"function argument mode %q is not supported in v0 (Stage A: IN only)",
				p.cur().Value))
		}
	}
	if p.cur().Kind == TokenKeyword {
		switch p.cur().Keyword {
		case KwOut, KwInout, KwVariadic:
			return FunctionArg{}, p.errAtCur(fmt.Sprintf(
				"function argument mode %q is not supported in v0 (Stage A: IN only)",
				p.cur().Value))
		}
	}

	// Optional `IN` mode keyword. Stage A treats IN as a no-op;
	// kept here so handwritten functions migrated from upstream
	// (which often spell `IN` explicitly) parse cleanly.
	if p.acceptKeyword(KwIn) {
		arg.Mode = FuncArgIn
	}

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
	args, err := p.parseFunctionArgList()
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
