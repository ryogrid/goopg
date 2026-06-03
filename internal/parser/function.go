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
	returnsSet := p.acceptIdentKeyword("setof")
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
		ReturnsSet: returnsSet,
		Volatile:   "v", // default: volatile
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
		// PG14 SQL-standard function body: BEGIN ATOMIC ... END (without AS).
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwBegin:
			if sawAs {
				return nil, p.errAtCur("duplicate body clause (BEGIN ATOMIC after AS)")
			}
			body, err := p.parseFunctionBody()
			if err != nil {
				return nil, err
			}
			stmt.Body = body
			stmt.BeginAtomic = true
			sawAs = true
		// PG14 SQL-standard function body: RETURN expr (without AS $$...$$).
		// Treated as equivalent to SELECT expr; store as "SELECT <tokens>" body. M0097-0071.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReturn:
			if sawAs {
				return nil, &SyntaxError{Raw: true, Message: "duplicate function body specified"}
			}
			p.advance() // consume RETURN
			// Collect tokens until EOF, semicolon, or end of statement.
			// Use tokenBodySQL to restore string literal quotes.
			var bodyToks []string
			for p.cur().Kind != TokenEOF {
				t := p.cur()
				if t.Kind == TokenSymbol && t.Value == ";" {
					break
				}
				bodyToks = append(bodyToks, tokenBodySQL(t))
				p.advance()
			}
			stmt.Body = "SELECT " + strings.Join(bodyToks, " ")
			stmt.IsReturnForm = true
			sawAs = true
		case p.isFunctionAttribute():
			cur := p.cur()
			// Capture known attributes before consuming them.
			if cur.Kind == TokenIdent {
				switch strings.ToLower(cur.Value) {
				case "strict":
					stmt.Strict = true
				case "returns":
					// RETURNS NULL ON NULL INPUT — Strict=false (explicit)
					stmt.Strict = false
				case "immutable":
					stmt.Volatile = "i"
				case "stable":
					stmt.Volatile = "s"
				case "volatile":
					stmt.Volatile = "v"
				case "leakproof":
					stmt.Leakproof = true
				case "security":
					// peek ahead for "definer" or "invoker"
					p.advance()
					if p.acceptIdentKeyword("definer") {
						stmt.SecurityDefiner = true
					} else {
						p.acceptIdentKeyword("invoker")
					}
					continue
				case "external":
					// EXTERNAL SECURITY DEFINER/INVOKER
					p.advance()
					p.acceptIdentKeyword("security")
					if p.acceptIdentKeyword("definer") {
						stmt.SecurityDefiner = true
					} else {
						p.acceptIdentKeyword("invoker")
					}
					continue
				case "called":
					// CALLED ON NULL INPUT — explicit not-strict
					stmt.Strict = false
				}
			} else if cur.Kind == TokenKeyword && cur.Keyword == KwNot {
				// NOT LEAKPROOF
				p.advance()
				p.acceptIdentKeyword("leakproof")
				stmt.Leakproof = false
				continue
			} else if cur.Kind == TokenKeyword && cur.Keyword == KwReturns {
				// RETURNS NULL ON NULL INPUT (same as STRICT)
				p.advance() // RETURNS
				p.acceptKeyword(KwNull)
				p.acceptKeyword(KwOn)
				p.acceptKeyword(KwNull)
				p.acceptIdentKeyword("input")
				stmt.Strict = true
				continue
			} else if cur.Kind == TokenKeyword && cur.Keyword == KwParallel {
				p.advance()
				p.acceptIdentKeyword("safe", "unsafe", "restricted")
				continue
			}
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
		// Track BEGIN...END and CASE...END pairs to handle nested constructs.
		p.advance() // BEGIN
		_ = p.acceptIdentKeyword("atomic")
		var parts []string
		depth := 1
		caseDepth := 0 // track CASE ... END (CASE does not consume a BEGIN)
		for depth > 0 && p.cur().Kind != TokenEOF {
			ct := p.cur()
			if ct.Kind == TokenKeyword && ct.Keyword == KwBegin {
				depth++
			} else if ct.Kind == TokenKeyword && ct.Keyword == KwCase {
				caseDepth++
			} else if ct.Kind == TokenKeyword && ct.Keyword == KwEnd {
				if caseDepth > 0 {
					caseDepth--
					// This END belongs to a CASE; don't decrement BEGIN depth.
				} else {
					depth--
					if depth == 0 {
						p.advance() // END
						p.acceptSymbol(";")
						return strings.Join(parts, " "), nil
					}
				}
			}
			parts = append(parts, tokenBodySQL(ct))
			p.advance()
		}
		return "", p.errAtCur("unterminated BEGIN ATOMIC body")
	}
	return "", p.errAtCur("expected $$body$$ or BEGIN ATOMIC for function body")
}

// tokenBodySQL reconstructs the SQL representation of a token for storage in
// function/procedure bodies. String literals get their quotes restored.
func tokenBodySQL(t Token) string {
	switch t.Kind {
	case TokenStringLit:
		return "'" + strings.ReplaceAll(t.Value, "'", "''") + "'"
	case TokenKeyword:
		// Boolean literals stay lowercase in PG function bodies.
		switch t.Keyword {
		case KwTrue, KwFalse, KwNull:
			return t.Value
		}
		return strings.ToUpper(t.Value)
	default:
		return t.Value
	}
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

	// Accept IN / OUT / INOUT / VARIADIC mode keywords (same as procedures).
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
	} else if p.cur().Kind == TokenIdent {
		// Also accept "name mode type" form (e.g. `a OUT int`).
		next := p.peek(1)
		if next.Kind == TokenKeyword {
			switch next.Keyword {
			case KwIn, KwOut, KwInout, KwVariadic:
				arg.Name = p.cur().Value
				p.advance()
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
				colType, err := p.parseColumnType()
				if err != nil {
					return FunctionArg{}, err
				}
				arg.Type = colType
				if p.acceptKeyword(KwDefault) || p.acceptSymbol("=") {
					expr, err := p.parseExpr()
					if err != nil {
						return FunctionArg{}, err
					}
					arg.Default = expr
				}
				return arg, nil
			}
		}
	}

	return p.parseArgNameAndType(pos, arg)
}

// parseProcedureArg parses a single procedure argument in any of the PG forms:
//   [mode] [name] type      — mode-first
//   [name] [mode] type      — name-first (PG also accepts this)
//   [DEFAULT expr]
// Stage B allows OUT, INOUT, and VARIADIC modes.
func (p *parser) parseProcedureArg() (FunctionArg, error) {
	pos := p.cur().Pos
	arg := FunctionArg{pos: pos, Mode: FuncArgIn}

	// Accept IN / OUT / INOUT / VARIADIC mode keywords (mode-first form).
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
	} else if p.cur().Kind == TokenIdent {
		// Might be "name mode type" form (e.g. `a OUT int`). Peek ahead: if
		// the next token is a mode keyword, consume name then mode.
		next := p.peek(1)
		if next.Kind == TokenKeyword {
			switch next.Keyword {
			case KwIn, KwOut, KwInout, KwVariadic:
				arg.Name = p.cur().Value
				p.advance() // consume name
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
				// Now parse just the type (name already consumed).
				colType, err := p.parseColumnType()
				if err != nil {
					return FunctionArg{}, err
				}
				arg.Type = colType
				if p.acceptKeyword(KwDefault) || p.acceptSymbol("=") {
					expr, err := p.parseExpr()
					if err != nil {
						return FunctionArg{}, err
					}
					arg.Default = expr
				}
				return arg, nil
			}
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
	// Multi-target: DROP FUNCTION f1(args), f2(args), f3(args)
	for p.acceptSymbol(",") {
		extraName, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		extraArgs, err := p.parseFunctionArgList()
		if err != nil {
			return nil, err
		}
		stmt.Extras = append(stmt.Extras, DropFunctionItem{Name: extraName, Args: extraArgs})
	}
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
		Volatile:  "v",
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
		// PG14 SQL-standard procedure body: BEGIN ATOMIC ... END (without AS).
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwBegin:
			if sawAs {
				return nil, p.errAtCur("duplicate body clause (BEGIN ATOMIC after AS)")
			}
			body, err := p.parseFunctionBody()
			if err != nil {
				return nil, err
			}
			stmt.Body = body
			stmt.BeginAtomic = true
			sawAs = true
		case p.isFunctionAttribute():
			// Track WINDOW and STRICT attributes for validation in executor.
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "window") {
				stmt.Window = true
			}
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "strict") {
				stmt.Strict = true
			}
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
	// Optional argument list; supports named args: CALL proc(name => val)
	if p.acceptSymbol("(") {
		args := make([]Expr, 0, 2)
		argNames := make([]string, 0, 2)
		hasNamed := false
		if !p.acceptSymbol(")") {
			for {
				// Detect name => expr (named argument).
				argName := ""
				if p.cur().Kind == TokenIdent &&
					p.peek(1).Kind == TokenOperator && p.peek(1).Value == "=>" {
					argName = p.cur().Value
					p.advance() // name
					p.advance() // =>
					hasNamed = true
				}
				expr, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				args = append(args, expr)
				argNames = append(argNames, argName)
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
		if hasNamed {
			stmt.ArgNames = argNames
		}
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
		case KwNot, KwParallel, KwReturns:
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
	case cur.Kind == TokenKeyword && cur.Keyword == KwReturns:
		// RETURNS NULL ON NULL INPUT
		p.advance()
		p.acceptKeyword(KwNull)
		p.acceptKeyword(KwOn)
		p.acceptKeyword(KwNull)
		p.acceptIdentKeyword("input")
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
			p.acceptKeyword(KwOn)
			p.acceptKeyword(KwNull)
			p.acceptIdentKeyword("input")
		case "returns":
			// RETURNS NULL ON NULL INPUT
			p.advance()
			p.acceptKeyword(KwNull)
			p.acceptKeyword(KwOn)
			p.acceptKeyword(KwNull)
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
	// Support comma-separated list: DROP PROCEDURE a, b, c
	for p.acceptSymbol(",") {
		extraName, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		stmt.Names = append(stmt.Names, extraName)
		// Optionally skip arg list for extra names.
		_, _ = p.parseFunctionArgList()
	}
	switch {
	case p.acceptKeyword(KwCascade):
		stmt.Behavior = DropCascade
	case p.acceptKeyword(KwRestrict):
		stmt.Behavior = DropDefault
	}
	return stmt, nil
}
