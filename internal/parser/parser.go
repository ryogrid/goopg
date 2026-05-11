package parser

import (
	"fmt"
	"strings"
)

// SyntaxError is the parser's structured error. Message mirrors
// upstream's `syntax error at or near "TOKEN"` shape so psql users
// can reason about it.
type SyntaxError struct {
	Pos     int
	Message string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("syntax error at or near %q (byte %d)", e.Message, e.Pos)
}

// Parse splits input on statement boundaries and returns one Stmt per
// non-empty statement. A trailing semicolon is allowed; an empty input
// returns an empty slice and no error.
// ParseExpr parses a single SQL expression and returns its AST.
// Used by the PL/pgSQL body parser (M0015 Stage A step 4) to
// translate RETURN / assignment / IF-condition expressions into
// the same AST nodes a SELECT target list would produce — keeps
// the type-checker / planner / executor reusable for routine
// bodies. Trailing tokens after the expression surface a syntax
// error so a caller passing `1 + 2; garbage` gets a clean
// diagnostic.
func ParseExpr(input string) (Expr, error) {
	toks, err := Lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: toks}
	expr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != TokenEOF {
		return nil, p.errAtCur("unexpected trailing tokens after expression")
	}
	return expr, nil
}

func Parse(input string) ([]Stmt, error) {
	toks, err := Lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: toks}
	var out []Stmt
	for p.cur().Kind != TokenEOF {
		// Empty statement (just `;`).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			p.advance()
			continue
		}
		stmt, err := p.parseStatement()
		if err != nil {
			return nil, err
		}
		out = append(out, stmt)
		// Optional trailing semicolon between statements; mandatory
		// before another statement starts.
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			p.advance()
			continue
		}
		if p.cur().Kind != TokenEOF {
			return nil, p.errAtCur("expected ';' or end of input")
		}
	}
	return out, nil
}

type parser struct {
	tokens []Token
	idx    int
}

func (p *parser) cur() Token {
	if p.idx >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[p.idx]
}

func (p *parser) peek(off int) Token {
	if p.idx+off >= len(p.tokens) {
		return Token{Kind: TokenEOF}
	}
	return p.tokens[p.idx+off]
}

func (p *parser) advance() Token {
	t := p.cur()
	p.idx++
	return t
}

// errAtCur builds a SyntaxError pinned at the current token. The
// message echoes the token text, matching upstream's "at or near".
func (p *parser) errAtCur(msg string) error {
	t := p.cur()
	near := t.Value
	if t.Kind == TokenEOF {
		near = "end of input"
	}
	return &SyntaxError{Pos: t.Pos, Message: msg + " (got " + near + ")"}
}

// expectKeyword consumes the current token if it's the named keyword;
// otherwise it returns a syntax error.
func (p *parser) expectKeyword(kw Keyword) (Token, error) {
	t := p.cur()
	if t.Kind != TokenKeyword || t.Keyword != kw {
		return Token{}, p.errAtCur("expected keyword " + string(kw))
	}
	p.advance()
	return t, nil
}

// acceptKeyword returns true and advances when the current token is kw.
func (p *parser) acceptKeyword(kw Keyword) bool {
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == kw {
		p.advance()
		return true
	}
	return false
}

// acceptSymbol returns true and advances when the current token is the
// given punctuation symbol.
func (p *parser) acceptSymbol(sym string) bool {
	if p.cur().Kind == TokenSymbol && p.cur().Value == sym {
		p.advance()
		return true
	}
	return false
}

// acceptIdentKeyword consumes a TokenIdent matching any of the
// given (case-insensitive) names. Used for SQL words upstream
// treats as unreserved keywords — `FETCH`, `FIRST`, `NEXT`,
// `ROW`, `ROWS`, `ONLY`. Returns false (without advancing)
// when the current token doesn't match.
func (p *parser) acceptIdentKeyword(names ...string) bool {
	t := p.cur()
	if t.Kind != TokenIdent {
		return false
	}
	for _, n := range names {
		if strings.EqualFold(t.Value, n) {
			p.advance()
			return true
		}
	}
	return false
}

// parseStatement dispatches on the leading keyword.
func (p *parser) parseStatement() (Stmt, error) {
	t := p.cur()
	if t.Kind != TokenKeyword {
		return nil, p.errAtCur("expected statement")
	}
	switch t.Keyword {
	case KwBegin:
		return p.parseBegin()
	case KwCommit, KwEnd:
		return p.parseCommit()
	case KwRollback, KwAbort:
		return p.parseRollback()
	case KwSavepoint:
		return p.parseSavepoint()
	case KwRelease:
		return p.parseReleaseSavepoint()
	case KwVacuum:
		return p.parseVacuum()
	case KwAnalyze, KwAnalyse:
		return p.parseAnalyze()
	case KwReindex:
		return p.parseReindex()
	case KwShow:
		return p.parseShow()
	case KwSet:
		return p.parseSet()
	case KwReset:
		return p.parseReset()
	case KwSelect:
		return p.parseSelect()
	case KwInsert:
		return p.parseInsert()
	case KwUpdate:
		return p.parseUpdate()
	case KwDelete:
		return p.parseDelete()
	case KwCreate:
		return p.parseCreate()
	case KwDrop:
		return p.parseDrop()
	case KwTruncate:
		return p.parseTruncate()
	case KwAlter:
		return p.parseAlter()
	case KwCopy:
		return p.parseCopy()
	case KwCheckpoint:
		return p.parseCheckpoint()
	case KwExplain:
		return p.parseExplain()
	case KwWith:
		return p.parseStatementWithCTE()
	case KwCall:
		p.advance()
		return p.parseCallStatement(t.Pos)
	}
	return nil, p.errAtCur("unsupported statement")
}

// parseStatementWithCTE handles a `WITH cte ...` prefix: parses
// the WithClause then dispatches on the next keyword to the
// appropriate per-statement parser. The dispatched parser is
// invoked through its `*WithCTE` overload which threads the
// pre-parsed WithClause onto the resulting AST node.
//
// See docs/design/0016-0001-with-parser-ast-and-name-resolution.md.
func (p *parser) parseStatementWithCTE() (Stmt, error) {
	with, err := p.parseWithClause()
	if err != nil {
		return nil, err
	}
	t := p.cur()
	if t.Kind != TokenKeyword {
		return nil, p.errAtCur("expected SELECT / INSERT / UPDATE / DELETE after WITH list")
	}
	switch t.Keyword {
	case KwSelect:
		return p.parseSelectWithCTE(with)
	case KwInsert:
		return p.parseInsertWithCTE(with)
	case KwUpdate:
		return p.parseUpdateWithCTE(with)
	case KwDelete:
		return p.parseDeleteWithCTE(with)
	}
	return nil, p.errAtCur("WITH clause must be followed by SELECT, INSERT, UPDATE, or DELETE")
}

// parseExplain handles the three EXPLAIN surface forms upstream
// supports:
//
//	EXPLAIN <stmt>
//	EXPLAIN [ANALYZE] [VERBOSE] <stmt>
//	EXPLAIN ( option [VALUE] [, ...] ) <stmt>
//
// The keyword form is parsed first when the token after EXPLAIN
// matches ANALYZE/VERBOSE; the parenthesised form takes over when
// the next token is `(`. Any other token routes straight to the
// inner statement (preserving bare-EXPLAIN for byte-for-byte
// pre-M0018 compatibility).
//
// See docs/design/0018-0001-explain-parser-options-and-ast.md.
func (p *parser) parseExplain() (Stmt, error) {
	t := p.advance() // EXPLAIN
	var opts ExplainOptions

	// Parenthesised option list (`EXPLAIN (option [, ...]) <stmt>`).
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		if err := p.parseExplainOptionList(&opts); err != nil {
			return nil, err
		}
	} else {
		// Keyword form: ANALYZE and VERBOSE may appear in either
		// order, matching upstream's `opt_analyze`/`opt_verbose`.
		for {
			if p.acceptKeyword(KwAnalyze) {
				opts.Analyze = true
				opts.Set.Analyze = true
				continue
			}
			if p.acceptKeyword(KwVerbose) {
				opts.Verbose = true
				opts.Set.Verbose = true
				continue
			}
			break
		}
	}

	inner, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	return &ExplainStmt{pos: t.Pos, Options: opts, Inner: inner}, nil
}

// parseExplainOptionList parses the parenthesised option list:
//
//	"(" name [VALUE] ("," name [VALUE])* ")"
//
// On entry the cursor sits on the opening `(`. On success the
// cursor sits past the closing `)`. Errors carry the precise
// byte position where the offending token sits.
func (p *parser) parseExplainOptionList(opts *ExplainOptions) error {
	if !p.acceptSymbol("(") {
		return p.errAtCur("expected '(' after EXPLAIN")
	}
	if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
		// Empty list — `EXPLAIN () SELECT ...` is a syntax error
		// in upstream too.
		return &SyntaxError{Pos: p.cur().Pos, Message: "EXPLAIN option list is empty"}
	}
	for {
		if err := p.parseExplainOneOption(opts); err != nil {
			return err
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return p.errAtCur("expected ')' to close EXPLAIN option list")
	}
	return nil
}

// parseExplainOneOption parses one option entry. The name is
// matched case-insensitively against the supported set; FORMAT
// takes a TEXT|JSON value, all others take an optional bool.
func (p *parser) parseExplainOneOption(opts *ExplainOptions) error {
	tok := p.cur()
	if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
		return p.errAtCur("expected EXPLAIN option name")
	}
	name := strings.ToLower(tok.Value)
	if tok.Kind == TokenKeyword {
		// Keyword tokens carry the lowercased form in tok.Value
		// already; this branch lets ANALYZE / VERBOSE be used
		// inside the parenthesised list too (upstream allows it).
	}
	pos := tok.Pos
	p.advance()

	if name == "format" {
		// FORMAT requires a value: TEXT or JSON.
		valTok := p.cur()
		if valTok.Kind != TokenIdent && valTok.Kind != TokenKeyword && valTok.Kind != TokenStringLit && valTok.Kind != TokenQuotedIdent {
			return &SyntaxError{Pos: valTok.Pos, Message: "FORMAT requires a value (TEXT or JSON)"}
		}
		v := strings.ToLower(valTok.Value)
		p.advance()
		switch v {
		case "text":
			opts.Format = ExplainFormatText
		case "json":
			opts.Format = ExplainFormatJSON
		default:
			return &SyntaxError{Pos: valTok.Pos, Message: fmt.Sprintf("unsupported FORMAT %q (TEXT or JSON only)", valTok.Value)}
		}
		opts.Set.Format = true
		return nil
	}

	// All other options are bool. Read the optional value.
	val := true
	if v, present, err := p.tryReadBoolOptionValue(); err != nil {
		return err
	} else if present {
		val = v
	}

	switch name {
	case "analyze":
		opts.Analyze = val
		opts.Set.Analyze = true
	case "verbose":
		opts.Verbose = val
		opts.Set.Verbose = true
	case "costs":
		opts.Costs = val
		opts.Set.Costs = true
	case "buffers":
		opts.Buffers = val
		opts.Set.Buffers = true
	case "settings":
		opts.Settings = val
		opts.Set.Settings = true
	case "timing":
		opts.Timing = val
		opts.Set.Timing = true
	case "summary":
		opts.Summary = val
		opts.Set.Summary = true
	default:
		return &SyntaxError{Pos: pos, Message: fmt.Sprintf("unknown EXPLAIN option %q", tok.Value)}
	}
	return nil
}

// tryReadBoolOptionValue reads an optional bool value following an
// EXPLAIN option name. Returns (val, true, nil) when a value was
// consumed, (false, false, nil) when the next token isn't a bool
// value (caller's responsibility to default to true), and a
// non-nil error when the next token looks like a value but isn't
// a recognised bool form.
func (p *parser) tryReadBoolOptionValue() (val bool, present bool, err error) {
	t := p.cur()
	switch t.Kind {
	case TokenKeyword:
		if t.Keyword == KwTrue {
			p.advance()
			return true, true, nil
		}
		if t.Keyword == KwFalse {
			p.advance()
			return false, true, nil
		}
		// `on` is a keyword in the lexer (KwOn — used by ON
		// DELETE etc.). For EXPLAIN's bool-option-value position
		// it stands in as `true` to match upstream's
		// defGetBoolean. `off` is just an identifier (no
		// collision with any keyword) and is handled in the
		// TokenIdent branch below.
		if t.Keyword == KwOn {
			p.advance()
			return true, true, nil
		}
		return false, false, nil
	case TokenIdent:
		switch strings.ToLower(t.Value) {
		case "on":
			p.advance()
			return true, true, nil
		case "off":
			p.advance()
			return false, true, nil
		}
		return false, false, nil
	case TokenIntLit:
		// Upstream accepts 1/0 as ON/OFF.
		switch t.Value {
		case "0":
			p.advance()
			return false, true, nil
		case "1":
			p.advance()
			return true, true, nil
		}
		return false, false, nil
	}
	return false, false, nil
}

// parseCheckpoint: CHECKPOINT
func (p *parser) parseCheckpoint() (Stmt, error) {
	t := p.advance()
	return &CheckpointStmt{pos: t.Pos}, nil
}

// parseBegin: BEGIN [WORK | TRANSACTION]
func (p *parser) parseBegin() (Stmt, error) {
	t := p.advance() // BEGIN
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &BeginStmt{pos: t.Pos}, nil
}

// parseCommit: COMMIT [WORK | TRANSACTION] | END [WORK | TRANSACTION]
func (p *parser) parseCommit() (Stmt, error) {
	t := p.advance()
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &CommitStmt{pos: t.Pos}, nil
}

// parseRollback: ROLLBACK [WORK | TRANSACTION] | ROLLBACK TO [SAVEPOINT] name | ABORT [WORK | TRANSACTION]
func (p *parser) parseRollback() (Stmt, error) {
	t := p.advance()
	// ROLLBACK TO [SAVEPOINT] name
	if p.acceptKeyword(KwTo) {
		_ = p.acceptKeyword(KwSavepoint)
		name, err := p.parseSavepointName()
		if err != nil {
			return nil, err
		}
		return &RollbackToSavepointStmt{pos: t.Pos, Name: name}, nil
	}
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &RollbackStmt{pos: t.Pos}, nil
}

// parseSavepoint: SAVEPOINT name
func (p *parser) parseSavepoint() (Stmt, error) {
	t := p.advance() // consume SAVEPOINT
	name, err := p.parseSavepointName()
	if err != nil {
		return nil, err
	}
	return &SavepointStmt{pos: t.Pos, Name: name}, nil
}

// parseReleaseSavepoint: RELEASE [SAVEPOINT] name
func (p *parser) parseReleaseSavepoint() (Stmt, error) {
	t := p.advance() // consume RELEASE
	_ = p.acceptKeyword(KwSavepoint)
	name, err := p.parseSavepointName()
	if err != nil {
		return nil, err
	}
	return &ReleaseSavepointStmt{pos: t.Pos, Name: name}, nil
}

// parseSavepointName reads the savepoint identifier. Accepts both
// TokenIdent and keyword tokens so names like "my_savepoint" and
// unreserved-keyword names work without quoting.
func (p *parser) parseSavepointName() (string, error) {
	t := p.cur()
	if t.Kind != TokenIdent && t.Kind != TokenKeyword {
		return "", p.errAtCur("expected savepoint name")
	}
	p.advance()
	return t.Value, nil
}

// parseVacuum: VACUUM [(opt [, opt …])] [target [, target …]]
// Accepts both legacy syntax (VACUUM [VERBOSE] [ANALYZE] [FULL] [FREEZE] …)
// and PostgreSQL 9.0+ parenthesized syntax (VACUUM (SKIP_DATABASE_STATS, …) …).
func (p *parser) parseVacuum() (Stmt, error) {
	t := p.advance()
	v := &VacuumStmt{pos: t.Pos, ParallelWorkers: -1}

	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		// Parenthesized option list.
		p.advance() // consume (
		if err := p.parseVacuumOptionList(v); err != nil {
			return nil, err
		}
	} else {
		// Legacy syntax: VACUUM [VERBOSE] [ANALYZE] [FULL] [FREEZE]
		for {
			switch {
			case p.acceptKeyword(KwVerbose):
				v.Verbose = true
			case p.acceptKeyword(KwAnalyze) || p.acceptKeyword(KwAnalyse):
				v.Analyze = true
			case p.acceptKeyword(KwFull):
				v.Full = true
			case p.acceptKeyword(KwFreeze):
				v.Freeze = true
			default:
				goto targets
			}
		}
	}
targets:
	if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return v, nil
	}
	tgts, err := p.parseObjectList()
	if err != nil {
		return nil, err
	}
	v.Targets = tgts
	return v, nil
}

// parseVacuumOptionList parses the parenthesized option list for VACUUM.
// Caller has already consumed the opening '('.
func (p *parser) parseVacuumOptionList(v *VacuumStmt) error {
	for {
		switch {
		case p.acceptKeyword(KwVerbose):
			v.Verbose = true
		case p.acceptKeyword(KwAnalyze) || p.acceptKeyword(KwAnalyse):
			v.Analyze = true
		case p.acceptKeyword(KwFull):
			v.Full = true
		case p.acceptKeyword(KwFreeze):
			v.Freeze = true
		case p.acceptIdentKeyword("disable_page_skipping"):
			v.DisablePageSkipping = true
		case p.acceptIdentKeyword("skip_database_stats"):
			v.SkipDatabaseStats = true
		case p.acceptIdentKeyword("only_database_stats"):
			v.OnlyDatabaseStats = true
		case p.acceptIdentKeyword("skip_locked"):
			v.SkipLocked = true
		case p.acceptIdentKeyword("index_cleanup"):
			// INDEX_CLEANUP { TRUE | FALSE | AUTO }
			if p.acceptKeyword(KwTrue) || p.acceptIdentKeyword("true") {
				v.ForceIndexCleanup = true
			} else if p.acceptKeyword(KwFalse) || p.acceptIdentKeyword("false") {
				v.NoIndexCleanup = true
			} else {
				_ = p.acceptIdentKeyword("auto")
			}
		case p.acceptIdentKeyword("truncate"):
			if p.acceptKeyword(KwFalse) || p.acceptIdentKeyword("false") {
				v.NoTruncate = true
			} else {
				_ = p.acceptKeyword(KwTrue) || p.acceptIdentKeyword("true")
			}
		case p.acceptIdentKeyword("process_main"):
			if p.acceptKeyword(KwFalse) || p.acceptIdentKeyword("false") {
				v.NoProcessMain = true
			} else {
				_ = p.acceptKeyword(KwTrue) || p.acceptIdentKeyword("true")
			}
		case p.acceptIdentKeyword("process_toast"):
			if p.acceptKeyword(KwFalse) || p.acceptIdentKeyword("false") {
				v.NoProcessToast = true
			} else {
				_ = p.acceptKeyword(KwTrue) || p.acceptIdentKeyword("true")
			}
		case p.acceptKeyword(KwParallel):
			n, err := p.parseIntLit()
			if err != nil {
				return err
			}
			v.ParallelWorkers = int(n)
		case p.acceptIdentKeyword("buffer_usage_limit"):
			lit, err := p.parseStrLit()
			if err != nil {
				return err
			}
			v.BufferUsageLimit = lit
		default:
			return p.errAtCur("unrecognised VACUUM option")
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	if !p.acceptSymbol(")") {
		return p.errAtCur("expected ')'")
	}
	return nil
}

// parseIntLit consumes a TokenIntLit and returns its value.
func (p *parser) parseIntLit() (int64, error) {
	t := p.cur()
	if t.Kind != TokenIntLit {
		return 0, p.errAtCur("expected integer")
	}
	p.advance()
	var n int64
	for _, ch := range t.Value {
		if ch < '0' || ch > '9' {
			return 0, &SyntaxError{Pos: t.Pos, Message: "invalid integer: " + t.Value}
		}
		n = n*10 + int64(ch-'0')
	}
	return n, nil
}

// parseStrLit consumes a TokenStringLit and returns its (unquoted) value.
func (p *parser) parseStrLit() (string, error) {
	t := p.cur()
	if t.Kind != TokenStringLit {
		return "", p.errAtCur("expected string literal")
	}
	p.advance()
	return t.Value, nil
}

// parseAnalyze: ANALYZE [(opt [, opt …])] [target [, target …]]
// Accepts both legacy syntax (ANALYZE [VERBOSE] …)
// and parenthesized syntax (ANALYZE (SKIP_LOCKED, …) …).
func (p *parser) parseAnalyze() (Stmt, error) {
	t := p.advance()
	a := &AnalyzeStmt{pos: t.Pos}

	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance() // consume (
		for {
			switch {
			case p.acceptKeyword(KwVerbose):
				a.Verbose = true
			case p.acceptIdentKeyword("skip_locked"):
				a.SkipLocked = true
			case p.acceptIdentKeyword("buffer_usage_limit"):
				_, err := p.parseStrLit()
				if err != nil {
					return nil, err
				}
			default:
				return nil, p.errAtCur("unrecognised ANALYZE option")
			}
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
	} else if p.acceptKeyword(KwVerbose) {
		a.Verbose = true
	}
	if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return a, nil
	}
	tgts, err := p.parseObjectList()
	if err != nil {
		return nil, err
	}
	a.Targets = tgts
	return a, nil
}

// parseObjectList parses one or more comma-separated ObjectNames.
func (p *parser) parseObjectList() ([]ObjectName, error) {
	var out []ObjectName
	first, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	out = append(out, first)
	for p.acceptSymbol(",") {
		o, err := p.parseObjectName()
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

// parseObjectName parses [schema.]name where each part is an
// identifier (possibly quoted).
func (p *parser) parseObjectName() (ObjectName, error) {
	first, err := p.parseIdent()
	if err != nil {
		return ObjectName{}, err
	}
	o := ObjectName{pos: first.Pos, Name: identText(first)}
	if p.acceptSymbol(".") {
		second, err := p.parseIdent()
		if err != nil {
			return ObjectName{}, err
		}
		o.Schema = o.Name
		o.Name = identText(second)
	}
	return o, nil
}

func (p *parser) parseIdent() (Token, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenQuotedIdent:
		p.advance()
		return t, nil
	case TokenKeyword:
		// Accept the keyword as an identifier only when it is not reserved.
		// Unreserved, col_name, and type_func_name keywords (per upstream's
		// kwlist.h split) are safe as column names, table names, and aliases.
		// Reserved keywords (SELECT, FROM, WHERE, AND, …) must be quoted.
		if IsColNameKeyword(Keyword(t.Value)) {
			p.advance()
			return t, nil
		}
	}
	return Token{}, p.errAtCur("expected identifier")
}

func identText(t Token) string {
	// TokenIdent and TokenKeyword carry already-lowercased text;
	// TokenQuotedIdent preserves its original case.
	return t.Value
}

// parseShow: SHOW name | SHOW ALL
func (p *parser) parseShow() (Stmt, error) {
	t := p.advance()
	if p.acceptKeyword(KwAll) {
		return &ShowStmt{pos: t.Pos, All: true}, nil
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	return &ShowStmt{pos: t.Pos, Name: name}, nil
}

// parseSet: SET [LOCAL|SESSION] name { = | TO } { value | DEFAULT }
func (p *parser) parseSet() (Stmt, error) {
	t := p.advance()
	s := &SetStmt{pos: t.Pos}
	if p.acceptKeyword(KwLocal) {
		s.Local = true
	} else {
		_ = p.acceptKeyword(KwSession)
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	s.Name = name
	// Either '=' or TO.
	switch {
	case p.cur().Kind == TokenOperator && p.cur().Value == "=":
		p.advance()
	default:
		if _, err := p.expectKeyword(KwTo); err != nil {
			return nil, err
		}
	}
	if p.acceptKeyword(KwDefault) {
		s.Default = true
		return s, nil
	}
	val, err := p.parseSetValue()
	if err != nil {
		return nil, err
	}
	s.Value = val
	return s, nil
}

// parseReindex parses REINDEX statements (M0095-0005).
//
// Syntax accepted:
//
//	REINDEX [(VERBOSE)] [CONCURRENTLY] {INDEX|TABLE|DATABASE|SCHEMA|SYSTEM}
//	  [IF EXISTS] name
//
// Executor stub: always succeeds without performing any index rebuild.
func (p *parser) parseReindex() (Stmt, error) {
	t := p.advance() // consume REINDEX
	r := &ReindexStmt{pos: t.Pos}

	// Optional parenthesized option list: REINDEX (VERBOSE) ...
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance() // consume (
		for {
			if p.acceptKeyword(KwVerbose) {
				r.Verbose = true
			} else if p.acceptIdentKeyword("tablespace") {
				// TABLESPACE option: consume the tablespace name
				_, _ = p.parseIdent()
			} else {
				break
			}
			if !p.acceptSymbol(",") {
				break
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')' after REINDEX options")
		}
	}

	// Optional CONCURRENTLY
	if p.acceptIdentKeyword("concurrently") {
		r.Concurrently = true
	}

	// Object type keyword (treated as identifiers to avoid keyword conflicts).
	switch {
	case p.acceptKeyword(KwIndex):
		r.ObjectType = "INDEX"
	case p.acceptKeyword(KwTable):
		r.ObjectType = "TABLE"
	case p.acceptIdentKeyword("database"):
		r.ObjectType = "DATABASE"
	case p.acceptIdentKeyword("schema"):
		r.ObjectType = "SCHEMA"
	case p.acceptIdentKeyword("system"):
		r.ObjectType = "SYSTEM"
	default:
		return nil, p.errAtCur("expected INDEX, TABLE, DATABASE, SCHEMA, or SYSTEM after REINDEX")
	}

	// Optional IF EXISTS
	if p.acceptKeyword(KwIf) {
		if _, err := p.expectKeyword(KwExists); err != nil {
			return nil, err
		}
	}

	// Object name (possibly schema-qualified)
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	r.Name = name.String()
	return r, nil
}

// parseReset: RESET name | RESET ALL
func (p *parser) parseReset() (Stmt, error) {
	t := p.advance()
	if p.acceptKeyword(KwAll) {
		return &ResetStmt{pos: t.Pos, All: true}, nil
	}
	name, err := p.parseGUCName()
	if err != nil {
		return nil, err
	}
	return &ResetStmt{pos: t.Pos, Name: name}, nil
}

// parseGUCName accepts `name` or `name.subname` (for namespaced GUCs
// like `myext.foo`). Returns the dotted form.
func (p *parser) parseGUCName() (string, error) {
	first, err := p.parseIdent()
	if err != nil {
		return "", err
	}
	name := identText(first)
	for p.acceptSymbol(".") {
		next, err := p.parseIdent()
		if err != nil {
			return "", err
		}
		name = name + "." + identText(next)
	}
	return name, nil
}

// parseSetValue accepts an int literal, string literal, or identifier.
// Multiple comma-separated values are concatenated with commas (rare
// in pgbench but accepted upstream for things like
// `SET search_path TO a, b`).
func (p *parser) parseSetValue() (string, error) {
	parts, err := p.parseSetValueAtoms()
	if err != nil {
		return "", err
	}
	return strings.Join(parts, ", "), nil
}

func (p *parser) parseSetValueAtoms() ([]string, error) {
	var out []string
	for {
		t := p.cur()
		switch t.Kind {
		case TokenIntLit:
			out = append(out, t.Value)
			p.advance()
		case TokenStringLit:
			out = append(out, t.Value)
			p.advance()
		case TokenIdent, TokenQuotedIdent, TokenKeyword:
			out = append(out, identText(t))
			p.advance()
		default:
			if t.Kind == TokenOperator && t.Value == "-" {
				// Allow leading minus on a numeric value (rare).
				p.advance()
				n := p.cur()
				if n.Kind != TokenIntLit {
					return nil, p.errAtCur("expected integer after '-'")
				}
				out = append(out, "-"+n.Value)
				p.advance()
				break
			}
			return nil, p.errAtCur("expected value")
		}
		if !p.acceptSymbol(",") {
			break
		}
	}
	return out, nil
}
