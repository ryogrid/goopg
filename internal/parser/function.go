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
	returnsTable := false
	var retType ColumnType
	// RETURNS TABLE (col type, ...) — syntactic sugar for RETURNS SETOF RECORD
	// with named OUT parameters. Append OUT params to args list. M0097-0028.
	if !returnsSet && p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTable {
		p.advance() // consume TABLE
		if !p.acceptSymbol("(") {
			return nil, p.errAtCur("expected '(' after RETURNS TABLE")
		}
		for {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				break
			}
			colPos := p.cur().Pos
			colName, cerr := p.parseIdent()
			if cerr != nil {
				return nil, cerr
			}
			colType, cerr := p.parseColumnType()
			if cerr != nil {
				return nil, cerr
			}
			args = append(args, FunctionArg{
				pos:          colPos,
				Name:         identText(colName),
				Mode:         FuncArgOut,
				ModeExplicit: true,
				Type:         colType,
			})
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' to close RETURNS TABLE column list")
		}
		returnsSet = true
		retType = ColumnType{Name: "record"}
		// Mark the RETURNS TABLE form so catalog deparsers (pg_get_function_result /
		// pg_get_function_arguments) can re-render `RETURNS TABLE(...)` rather than the
		// equivalent-but-divergent `OUT col ... RETURNS SETOF record`. The columns stay
		// stored as trailing OUT args so the planner's OUT-column expansion is unchanged.
		returnsTable = true
	} else {
		var err error
		retType, err = p.parseColumnType()
		if err != nil {
			return nil, err
		}
	}
	stmt := &CreateFunctionStmt{
		pos:          pos,
		OrReplace:    orReplace,
		Name:         name,
		Args:         args,
		ReturnType:   retType,
		ReturnsSet:   returnsSet,
		ReturnsTable: returnsTable,
		Volatile:     "v", // default: volatile
		Parallel:     "u", // default: parallel unsafe (PG CREATE FUNCTION default)
	}
	// The LANGUAGE / AS clauses can appear in either order (mirrors
	// upstream). Loop until both have been seen or we hit an
	// unrecognised token.
	sawLanguage := false
	sawAs := false
	sawTwoItemAs := false
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
			// AS 'body1', 'body2' (two quoted items) is only valid for
			// LANGUAGE C (obj file + link symbol) — upstream's
			// interpret_AS_clause rejects it for every other language,
			// including "internal" (functioncmds.c interpret_AS_clause).
			// LANGUAGE can appear before or after AS, so the language
			// check is deferred to the loop's exit below.
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				p.advance() // consume ","
				if p.cur().Kind == TokenStringLit {
					p.advance() // consume second body string
				}
				sawTwoItemAs = true
			}
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
		// SET name {TO|=} value | SET name FROM CURRENT — real PG's
		// FunctionSetResetClause, part of common_func_opt_item (shared with
		// ALTER FUNCTION's identical clause, ddl.go). Captured into
		// stmt.ConfigOps for pg_proc.proconfig instead of raising a syntax
		// error — SET always lexes as the real keyword KwSet (never
		// TokenIdent), so this could never have been reached via
		// isFunctionAttribute()'s TokenIdent-only "set" case below, exactly
		// like the ALTER FUNCTION bug fixed in M0097-0150 (DU-002 follow-up).
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwSet:
			p.advance() // consume SET
			op, ok, err := p.parseFunctionConfigSetClause()
			if err != nil {
				return nil, err
			}
			if ok {
				stmt.ConfigOps = append(stmt.ConfigOps, op)
			}
		// RESET name | RESET ALL — the other half of FunctionSetResetClause.
		case p.cur().Kind == TokenKeyword && p.cur().Keyword == KwReset:
			p.advance() // consume RESET
			stmt.ConfigOps = append(stmt.ConfigOps, p.parseFunctionConfigResetClause())
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
				case "window":
					stmt.Window = true
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
				case "cost":
					// COST <n> — planner per-row cost. Capture the numeric
					// literal verbatim so the pg_proc view / dump can re-emit
					// the non-default value (previously parsed-then-discarded).
					p.advance()
					if p.cur().Kind == TokenIntLit || p.cur().Kind == TokenNumericLit {
						stmt.Cost = p.cur().Value
						p.advance()
					}
					continue
				case "rows":
					// ROWS <n> — set-returning-function result-row estimate.
					p.advance()
					if p.cur().Kind == TokenIntLit || p.cur().Kind == TokenNumericLit {
						stmt.Rows = p.cur().Value
						p.advance()
					}
					continue
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
				switch {
				case p.acceptIdentKeyword("safe"):
					stmt.Parallel = "s"
				case p.acceptIdentKeyword("restricted"):
					stmt.Parallel = "r"
				case p.acceptIdentKeyword("unsafe"):
					stmt.Parallel = "u"
				}
				continue
			}
			p.consumeFunctionAttribute()
		default:
			if !sawAs {
				return nil, p.errAtCur("expected AS $$body$$ for CREATE FUNCTION")
			}
			if sawTwoItemAs {
				lang := strings.ToLower(stmt.Language)
				if lang != "c" {
					if lang == "" {
						lang = "sql"
					}
					return nil, &SyntaxError{Raw: true, Message: fmt.Sprintf("only one AS item needed for language %q", lang)}
				}
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
	case TokenParam:
		return "$" + t.Value // restore $N parameter reference prefix
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
			arg.ModeExplicit = true
		case KwOut:
			p.advance()
			arg.Mode = FuncArgOut
			arg.ModeExplicit = true
		case KwInout:
			p.advance()
			arg.Mode = FuncArgInout
			arg.ModeExplicit = true
		case KwVariadic:
			p.advance()
			arg.Mode = FuncArgVariadic
			arg.ModeExplicit = true
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
					arg.ModeExplicit = true
				case KwOut:
					p.advance()
					arg.Mode = FuncArgOut
					arg.ModeExplicit = true
				case KwInout:
					p.advance()
					arg.Mode = FuncArgInout
					arg.ModeExplicit = true
				case KwVariadic:
					p.advance()
					arg.Mode = FuncArgVariadic
					arg.ModeExplicit = true
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
//
//	[mode] [name] type      — mode-first
//	[name] [mode] type      — name-first (PG also accepts this)
//	[DEFAULT expr]
//
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
			arg.ModeExplicit = true
		case KwOut:
			p.advance()
			arg.Mode = FuncArgOut
			arg.ModeExplicit = true
		case KwInout:
			p.advance()
			arg.Mode = FuncArgInout
			arg.ModeExplicit = true
		case KwVariadic:
			p.advance()
			arg.Mode = FuncArgVariadic
			arg.ModeExplicit = true
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
					arg.ModeExplicit = true
				case KwOut:
					p.advance()
					arg.Mode = FuncArgOut
					arg.ModeExplicit = true
				case KwInout:
					p.advance()
					arg.Mode = FuncArgInout
					arg.ModeExplicit = true
				case KwVariadic:
					p.advance()
					arg.Mode = FuncArgVariadic
					arg.ModeExplicit = true
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
	// `ident <type-start>` where <type-start> is another ident,
	// quoted ident, or keyword, treat the first ident as the name. If we see
	// `ident )` or `ident ,` or `ident DEFAULT`, treat ident as
	// the type with no name.
	if p.cur().Kind == TokenIdent || p.cur().Kind == TokenQuotedIdent {
		save := p.idx
		nameTok := p.cur()
		p.advance()
		if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword || p.cur().Kind == TokenQuotedIdent {
			// Looks like `name type` — but only if the next
			// token isn't itself a separator.
			next := p.cur()
			if next.Kind == TokenIdent || next.Kind == TokenQuotedIdent ||
				(next.Kind == TokenKeyword && !isSeparatorKeyword(next.Keyword)) {
				// A quoted name can never be a multi-word type leader
				// (`"char" int` is name="char" type=int, not a multi-word
				// type), so skip the rewind for TokenQuotedIdent — mirroring
				// the first.Kind != TokenQuotedIdent idiom at ddl.go:4666.
				if nameTok.Kind != TokenQuotedIdent && isMultiWordTypeStart(nameTok.Value, next) {
					// `bit varying` / `double precision` / `timestamp with
					// time zone` … is ONE type, not `arg_name type`. Rewind
					// so parseColumnType consumes the whole multi-word
					// spelling (M0119-0006, deferral 74th slice).
					p.idx = save
				} else {
					arg.Name = identText(nameTok)
				}
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

// isMultiWordTypeStart reports whether the identifier nameTok followed by the
// lookahead token `next` opens a multi-word built-in type name that
// parseColumnType consumes as ONE type. parseArgNameAndType uses it to
// disambiguate `arg_name type` from a bare multi-word type in a function
// argument list: in `f(bit varying)` the tokens are `bit` then `varying`,
// which the generic `ident ident` heuristic would read as an argument named
// "bit" of type "varying" — but PostgreSQL (gram.y func_type → Typename)
// parses the pair as the single type `bit varying`. The set mirrors the
// multi-word spellings parseColumnType/parseMultiWordTypeName/
// parseIntervalColumnQualifier actually consume, so a rewind is always
// followed by a successful full-type parse (M0119-0006, deferral 74th slice).
func isMultiWordTypeStart(nameTok string, next Token) bool {
	switch strings.ToLower(nameTok) {
	case "double":
		return next.Kind == TokenIdent && strings.EqualFold(next.Value, "precision")
	case "character", "char", "nchar":
		// character/char/nchar — the plain and SQL national aliases of the
		// bpchar/varchar family (gram.y `character: CHARACTER|CHAR_P|NCHAR
		// opt_varying`). A following `varying` continues the multi-word type
		// name; a following OTHER identifier PG rejects as a syntax error
		// (`CREATE FUNCTION f(char int)`), because these keywords can never
		// start an arg name — return true so we rewind and let
		// parseColumnType consume the leading word, and the dangling ident
		// then errors out the same way.
		return next.Kind == TokenIdent
	case "national":
		// `national character [varying]` / `national char [varying]`
		// (gram.y `character: NATIONAL CHARACTER|NATIONAL CHAR opt_varying`).
		// Like the plain aliases above, `national` can never start an arg
		// name (`f(national int)` is a PG syntax error).
		return next.Kind == TokenIdent
	case "bit":
		return next.Kind == TokenIdent && strings.EqualFold(next.Value, "varying")
	case "timestamp", "time":
		// "with" is the KwWith keyword; "without" is an ordinary identifier
		// (parseMultiWordTypeName accepts it via acceptIdentKeyword).
		return (next.Kind == TokenKeyword && next.Keyword == KwWith) ||
			(next.Kind == TokenIdent && strings.EqualFold(next.Value, "without"))
	case "interval":
		return next.Kind == TokenIdent && intervalTypmodField[strings.ToLower(next.Value)]
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

// parseFunctionConfigSetClause parses the generic configuration-parameter
// `SET name {TO|=} value[, value...]` / `SET name FROM CURRENT` clause shared
// by CREATE FUNCTION/PROCEDURE (common_func_opt_item) and ALTER FUNCTION/
// PROCEDURE/ROUTINE (alterfunc_opt_list, ddl.go) — NOT ALTER FUNCTION's
// separate top-level `SET SCHEMA` rule, which callers must check for first.
// Caller has already consumed the SET keyword. Mirrors gram.y's var_name
// {TO|=} var_value | var_name FROM CURRENT grammar order: the config name is
// always parsed BEFORE the TO/=/FROM-CURRENT form. Returns ok=false for
// `FROM CURRENT` / `TO DEFAULT` (goopg has no per-session GUC snapshot to
// capture at CREATE/ALTER time, so both collapse to "leave unset" rather
// than a distinguishable stored value — a documented simplification, see
// docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md), true with
// a populated FunctionConfigOp otherwise.
func (p *parser) parseFunctionConfigSetClause() (FunctionConfigOp, bool, error) {
	if p.cur().Kind != TokenIdent && p.cur().Kind != TokenQuotedIdent && p.cur().Kind != TokenKeyword {
		return FunctionConfigOp{}, false, p.errAtCur("expected configuration parameter name after SET")
	}
	name := identText(p.cur())
	p.advance()
	if (p.cur().Kind == TokenSymbol || p.cur().Kind == TokenOperator) && p.cur().Value == "=" {
		p.advance()
	} else {
		p.acceptKeyword(KwTo)
	}
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwFrom {
		p.advance() // FROM
		p.acceptIdentKeyword("current")
		return FunctionConfigOp{}, false, nil
	}
	// DEFAULT lexes as the reserved keyword KwDefault (TokenKeyword), never
	// TokenIdent — acceptIdentKeyword("default") would never match it, the
	// same keyword/ident lexing mismatch as SET/FROM/ALL elsewhere in this
	// file. Must be checked before parseSetValueAtoms, which would otherwise
	// happily consume the bare keyword as if "default" were a literal value.
	if p.acceptKeyword(KwDefault) {
		return FunctionConfigOp{}, false, nil
	}
	if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
		return FunctionConfigOp{}, false, nil
	}
	values, err := p.parseSetValueAtoms()
	if err != nil {
		return FunctionConfigOp{}, false, err
	}
	return FunctionConfigOp{Name: name, Value: strings.Join(values, ",")}, true, nil
}

// parseFunctionConfigResetClause parses `RESET name` / `RESET ALL`, the
// other half of FunctionSetResetClause. Caller has already consumed RESET.
func (p *parser) parseFunctionConfigResetClause() FunctionConfigOp {
	// ALL lexes as the reserved keyword KwAll (TokenKeyword), never
	// TokenIdent — acceptIdentKeyword("all") would never match it, the same
	// keyword/ident lexing mismatch as SET/FROM above.
	if p.acceptKeyword(KwAll) {
		return FunctionConfigOp{ResetAll: true}
	}
	name := identText(p.cur())
	p.advance()
	return FunctionConfigOp{Reset: true, Name: name}
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
