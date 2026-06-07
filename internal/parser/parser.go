package parser

import (
	"fmt"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/mctx"
)

// tokenSlicePool recycles []Token backing arrays between calls to Parse.
// Typical pgbench queries produce 10–20 tokens; pre-sizing to 64 avoids
// any internal re-allocation for all but the most complex statements.
// M0098-0006.
var tokenSlicePool = sync.Pool{
	New: func() any {
		s := make([]Token, 0, 64)
		return &s
	},
}

// SyntaxError is the parser's structured error. Message mirrors
// upstream's `syntax error at or near "TOKEN"` shape so psql users
// can reason about it.
type SyntaxError struct {
	Pos     int
	Message string
	// Raw suppresses the "syntax error at or near …" wrapper: Error() returns
	// Message verbatim. Used for semantic errors caught during parsing (e.g.
	// "SELECT … INTO is not allowed here") that have their own wording.
	Raw bool
}

func (e *SyntaxError) Error() string {
	if e.Raw {
		return e.Message
	}
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
// ParseExpr parses a single SQL expression and returns its AST.
// Used by the PL/pgSQL body parser (M0015 Stage A step 4) to
// translate RETURN / assignment / IF-condition expressions into
// the same AST nodes a SELECT target list would produce — keeps
// the type-checker / planner / executor reusable for routine
// bodies. Trailing tokens after the expression surface a syntax
// error so a caller passing `1 + 2; garbage` gets a clean
// diagnostic.
//
// mc follows the same contract as Parse: it is a retained no-op (see Parse).
func ParseExpr(input string, mc ...*mctx.Context) (Expr, error) {
	var sctx *mctx.Context
	if len(mc) > 0 {
		sctx = mc[0]
	}

	var toks []Token
	var sp *[]Token
	var err error
	// Fast path: the heap-backed tokenSlicePool (allocation-free in steady
	// state). Token.Value is a Go string, so []Token must NOT be stored in an
	// mctx []byte arena: the slab is a GC noscan span that hides Value
	// pointers from the mark phase, and the cross-session plan cache retains
	// some Value strings by reference and would dangle on arena release.
	// The arena fast path is therefore permanently unsafe.
	// See docs/design/0107-0003d-token-pool-gc-safety.md.
	_ = sctx // mc is never used for token storage; retained for API compat.
	sp = tokenSlicePool.Get().(*[]Token)
	toks, err = lexInto((*sp)[:0], input)
	*sp = toks
	if err != nil {
		if sp != nil {
			tokenSlicePool.Put(sp)
		}
		return nil, err
	}

	// parser struct is 32 bytes; stack allocation is free. M0107-0003.
	var p parser
	p.tokens = toks
	p.idx = 0

	expr, err := p.parseExpr()
	// Check trailing tokens BEFORE returning to pool.
	var trailingErr error
	if err == nil && p.cur().Kind != TokenEOF {
		trailingErr = p.errAtCur("unexpected trailing tokens after expression")
	}
	if sp != nil {
		tokenSlicePool.Put(sp)
	}

	if err != nil {
		return nil, err
	}
	if trailingErr != nil {
		return nil, trailingErr
	}
	return expr, nil
}

// Parse splits input on statement boundaries and returns one Stmt per
// non-empty statement. A trailing semicolon is allowed; an empty input
// returns an empty slice and no error.
//
// mc was an optional mctx.Context for arena token-backing (M0107-0003 Phase
// C.3). That fast path is permanently retired: []Token cannot live in an mctx
// []byte arena because the slab is a GC noscan span (Token.Value pointers
// become invisible to the collector) and the cross-session plan cache retains
// some Value strings by reference (arena release would dangle them). mc is now
// a no-op retained for source compatibility; tokens always come from the
// heap-backed tokenSlicePool. See docs/design/0107-0003d-token-pool-gc-safety.md.
func Parse(input string, mc ...*mctx.Context) ([]Stmt, error) {
	var sctx *mctx.Context
	if len(mc) > 0 {
		sctx = mc[0]
	}

	var toks []Token
	var sp *[]Token
	var err error
	// Fast path: the heap-backed tokenSlicePool (allocation-free in steady
	// state; backing array is a GC scan span so Token.Value stays reachable).
	// See the function doc and docs/design/0107-0003d-token-pool-gc-safety.md
	// for why the mctx arena variant is unsafe and was removed.
	_ = sctx // mc is never used for token storage; retained for API compat.
	sp = tokenSlicePool.Get().(*[]Token)
	toks, err = lexInto((*sp)[:0], input)
	*sp = toks
	if err != nil {
		if sp != nil {
			tokenSlicePool.Put(sp)
		}
		return nil, err
	}

	// parser struct is 32 bytes; stack allocation is free. M0107-0003.
	var p parser
	p.tokens = toks
	p.idx = 0

	var out []Stmt
	for p.cur().Kind != TokenEOF {
		// Empty statement (just `;`).
		if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
			p.advance()
			continue
		}
		stmt, err := p.parseStatement()
		if err != nil {
			if sp != nil {
				tokenSlicePool.Put(sp)
			}
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
			err := p.errAtCur("expected ';' or end of input")
			if sp != nil {
				tokenSlicePool.Put(sp)
			}
			return nil, err
		}
	}
	if sp != nil {
		tokenSlicePool.Put(sp)
	}
	return out, nil
}

type parser struct {
	tokens []Token
	idx    int
	// selectIntoErrMsg is non-empty when SELECT … INTO is forbidden in the
	// current parse context (cursor, subquery, view body, INSERT SELECT).
	// parseSelect emits a SyntaxError with this message when INTO is seen.
	// selectIntoNoPos suppresses the FieldPosition field (for contexts where
	// PG does not emit a caret, e.g. CREATE VIEW).
	selectIntoErrMsg string
	selectIntoNoPos  bool
	// selectIntoCopyStop: when true, parseSelect stops *before* consuming
	// INTO (returning a partial SelectStmt). parseCopy uses this to detect
	// the deprecated `SELECT … INTO …` form and flag CopyStmt.SelectInto so
	// planCopy can emit the PG-compatible "COPY (SELECT INTO) is not
	// supported" error. M0097-0024.
	selectIntoCopyStop bool
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

// errSyntaxAtCur returns a bare PostgreSQL-style "syntax error at or
// near \"TOKEN\"" anchored at the current token, with no explanatory
// suffix. Used where upstream's grammar simply has no production for
// what follows (e.g. a FROM or column list after the query form of
// COPY), so the diagnostic should point at the offending token and
// say nothing more.
func (p *parser) errSyntaxAtCur() error {
	t := p.cur()
	near := t.Value
	if t.Kind == TokenEOF {
		near = "end of input"
	}
	return &SyntaxError{Pos: t.Pos, Message: near}
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
	// Parenthesised compound query: (SELECT ...) UNION ALL (SELECT ...)
	// PostgreSQL allows any set-operation branch to be wrapped in parentheses.
	// Handle this at the statement level by consuming the '(' then delegating
	// to parseParenthesisedSelectStmt.
	if t.Kind == TokenSymbol && t.Value == "(" {
		return p.parseParenthesisedSelectStmt()
	}
	if t.Kind != TokenKeyword && t.Kind != TokenIdent {
		return nil, p.errAtCur("expected statement")
	}
	if t.Kind != TokenKeyword {
		goto identLedStatement
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
	case KwCluster:
		return p.parseCluster()
	case KwMerge:
		return p.parseMerge()
	case KwPrepare:
		return p.parsePrepare()
	case KwExecute:
		return p.parseExecute()
	case KwDeallocate:
		return p.parseDeallocate()
	case KwShow:
		return p.parseShow()
	case KwSet:
		return p.parseSet()
	case KwReset:
		return p.parseReset()
	case KwSelect, KwTable, KwValues:
		// TABLE tablename is handled inside parseSelect as a shorthand. M0097-0004.
		// VALUES (...), (...) is a valid standalone statement in PostgreSQL. M0097-0049.
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
	case KwDo:
		return p.parseDoBlock()
	case KwDeclare:
		return p.parseDeclareCursor()
	}
	// Identifier-led statements. M0097-0013.
identLedStatement:
	if t.Kind == TokenIdent {
		switch strings.ToLower(t.Value) {
		case "fetch":
			return p.parseFetchCursor()
		case "move":
			return p.parseMoveCursor()
		case "close":
			return p.parseCloseCursor()
		case "refresh":
			p.advance()
			return p.parseRefreshMatView(t.Pos)
		case "grant", "revoke":
			// GRANT/REVOKE — parse as a no-op CompatNoopStmt.
			// The server's compatNoopCommandTag already handles these
			// when they fail the parser; we also accept them here so
			// they don't bubble up as parse errors when the server is
			// running multi-statement batches.
			p.advance()
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: strings.ToUpper(t.Value)}, nil
		case "comment":
			p.advance() // consume "comment" token
			// COMMENT ON {TABLE|INDEX|COLUMN|CONSTRAINT} … IS 'text'|NULL
			// For supported object types, return a CommentOnStmt so the executor
			// stores the description in pg_description. Unsupported types are
			// accepted as a silent no-op. M0097-0023.
			if !p.acceptKeyword(KwOn) {
				// bare COMMENT (no ON) — skip to semicolon
				for p.cur().Kind != TokenEOF {
					if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
						break
					}
					p.advance()
				}
				return &CompatNoopStmt{pos: t.Pos, Tag: "COMMENT"}, nil
			}
			if cs, ok, err := p.parseCommentOnTail(t.Pos); ok || err != nil {
				return cs, err
			}
			// Unsupported object type — consume rest as no-op.
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "COMMENT"}, nil
		case "security":
			// SECURITY LABEL … — parse as a no-op.
			p.advance()
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "SECURITY LABEL"}, nil
		case "lock":
			// LOCK [TABLE] [ONLY] rel [, ...] [IN lock_mode MODE] [NOWAIT].
			// M0097: parse into LockTableStmt so the executor can track locks in pg_locks.
			return p.parseLockTable(t.Pos)
		}
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

// parseDoBlock: DO [ LANGUAGE lang ] $$ body $$ — anonymous PL/pgSQL block. M0097-0003.
func (p *parser) parseDoBlock() (Stmt, error) {
	t := p.advance() // consume DO
	s := &DoStmt{pos: t.Pos, Language: "plpgsql"}
	// Optional LANGUAGE clause before or after the body.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "language") {
		p.advance()
		lang, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		s.Language = strings.ToLower(identText(lang))
	}
	// Body: dollar-quoted string literal.
	if p.cur().Kind != TokenStringLit {
		return nil, p.errAtCur("expected dollar-quoted string for DO body")
	}
	s.Body = p.cur().Value
	p.advance()
	// Optional trailing LANGUAGE clause.
	if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "language") {
		p.advance()
		if _, err := p.parseIdent(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// parseBegin: BEGIN [WORK | TRANSACTION] [transaction_mode ...]
//
// Accepted transaction modes (M0096-0002):
//
//	ISOLATION LEVEL {READ COMMITTED | READ UNCOMMITTED |
//	                 REPEATABLE READ | SERIALIZABLE}
//	READ {ONLY | WRITE}          — accepted, no-op for v0
//	[NOT] DEFERRABLE             — accepted, no-op for v0
//
// Modes may appear in any order and repeat (last ISOLATION LEVEL wins).
func (p *parser) parseBegin() (Stmt, error) {
	t := p.advance() // BEGIN
	s := &BeginStmt{pos: t.Pos}
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	// Optional transaction modes.
	for {
		switch {
		case p.acceptIdentKeyword("isolation"):
			if !p.acceptIdentKeyword("level") {
				return nil, p.errAtCur("expected LEVEL after ISOLATION")
			}
			level, err := p.parseIsolationLevelName()
			if err != nil {
				return nil, err
			}
			s.IsolationLevel = level
		case p.acceptIdentKeyword("read"):
			// READ ONLY / READ WRITE — accepted, no-op for v0.
			_ = p.acceptIdentKeyword("only") || p.acceptIdentKeyword("write")
		case p.acceptKeyword(KwNot):
			_ = p.acceptIdentKeyword("deferrable")
		case p.acceptIdentKeyword("deferrable"):
			// no-op
		default:
			goto done
		}
	}
done:
	return s, nil
}

// parseIsolationLevelName parses one of the four SQL isolation level names
// and returns the canonical lowercase form (matching mvcc.ParseIsolationLevel).
// "read" must have been consumed when this is called from a context where READ
// precedes COMMITTED/UNCOMMITTED; otherwise parse starts fresh.
func (p *parser) parseIsolationLevelName() (string, error) {
	switch {
	case p.acceptIdentKeyword("read"):
		switch {
		case p.acceptIdentKeyword("committed"):
			return "read committed", nil
		case p.acceptIdentKeyword("uncommitted"):
			return "read uncommitted", nil
		default:
			return "", p.errAtCur("expected COMMITTED or UNCOMMITTED after READ")
		}
	case p.acceptIdentKeyword("repeatable"):
		if !p.acceptIdentKeyword("read") {
			return "", p.errAtCur("expected READ after REPEATABLE")
		}
		return "repeatable read", nil
	case p.acceptIdentKeyword("serializable"):
		return "serializable", nil
	default:
		return "", p.errAtCur("expected isolation level name (READ COMMITTED, REPEATABLE READ, SERIALIZABLE, READ UNCOMMITTED)")
	}
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
			_ = p.acceptKeyword(KwTrue) || p.acceptKeyword(KwFalse) ||
				p.acceptIdentKeyword("true") || p.acceptIdentKeyword("false")
		case p.acceptKeyword(KwAnalyze) || p.acceptKeyword(KwAnalyse):
			v.Analyze = true
			_ = p.acceptKeyword(KwTrue) || p.acceptKeyword(KwFalse) ||
				p.acceptIdentKeyword("true") || p.acceptIdentKeyword("false")
		case p.acceptKeyword(KwFull):
			v.Full = true
			_ = p.acceptKeyword(KwTrue) || p.acceptKeyword(KwFalse) ||
				p.acceptIdentKeyword("true") || p.acceptIdentKeyword("false")
		case p.acceptKeyword(KwFreeze):
			v.Freeze = true
			_ = p.acceptKeyword(KwTrue) || p.acceptKeyword(KwFalse) ||
				p.acceptIdentKeyword("true") || p.acceptIdentKeyword("false")
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

// parseOperatorName parses a PostgreSQL operator name for DROP OPERATOR.
// An operator name is either a plain identifier (like "equals"), a sequence of
// operator characters ("=", "===", "||"), or schema-qualified ("pg_catalog.=").
// Returns errSyntaxAtCur for tokens that cannot start an operator name. M0097-regress.
func (p *parser) parseOperatorName() (ObjectName, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenQuotedIdent:
		p.advance()
		name := identText(t)
		if p.acceptSymbol(".") {
			// Schema-qualified: schema.op
			pos := p.cur().Pos
			opName := ""
			for p.cur().Kind == TokenOperator {
				opName += p.cur().Value
				p.advance()
			}
			if opName == "" {
				return ObjectName{}, p.errSyntaxAtCur()
			}
			return ObjectName{pos: pos, Schema: name, Name: opName}, nil
		}
		return ObjectName{pos: t.Pos, Name: name}, nil
	case TokenKeyword:
		if IsColNameKeyword(Keyword(t.Value)) {
			p.advance()
			return ObjectName{pos: t.Pos, Name: t.Value}, nil
		}
		return ObjectName{}, p.errSyntaxAtCur()
	case TokenOperator:
		// Accumulate consecutive operator chars (e.g. "===").
		opName := ""
		pos := t.Pos
		for p.cur().Kind == TokenOperator {
			opName += p.cur().Value
			p.advance()
		}
		return ObjectName{pos: pos, Name: opName}, nil
	}
	return ObjectName{}, p.errSyntaxAtCur()
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

// parseColumnAlias is like parseIdent but accepts ANY keyword token
// as an alias when the caller has already consumed an explicit AS.
// PostgreSQL allows `SELECT expr AS true`, `SELECT expr AS null`, etc.
// when the alias is preceded by AS. M0097-0003.
func (p *parser) parseColumnAlias() (Token, error) {
	t := p.cur()
	switch t.Kind {
	case TokenIdent, TokenQuotedIdent:
		p.advance()
		return t, nil
	case TokenKeyword:
		// After explicit AS, any keyword is valid as a column alias.
		p.advance()
		return t, nil
	}
	return Token{}, p.errAtCur("expected column alias after AS")
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
	isLocal := false
	if p.acceptKeyword(KwLocal) {
		s.Local = true
		isLocal = true
	} else if p.acceptKeyword(KwSession) {
		// SET SESSION AUTHORIZATION name — store the role name in s.Value so
		// the executor can update the session's non-superuser role tracking.
		// "authorization" is not a keyword in goopg so it parses as an ident.
		if p.acceptIdentKeyword("authorization") {
			s.Name = "session_authorization"
			// consume DEFAULT or the role name
			if p.acceptKeyword(KwDefault) {
				s.Default = true
			} else {
				roleTok, _ := p.parseIdent()
				s.Value = roleTok.Value
			}
			return s, nil
		}
		// otherwise fall through: SET SESSION TRANSACTION ... handled below
	}
	// SET ROLE rolename — accept as no-op. goopg does not implement role-based
	// access control; SET ROLE is accepted silently. M0097-0071.
	if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "role" {
		p.advance() // consume "role"
		// consume the role name (or DEFAULT)
		if !p.acceptKeyword(KwDefault) {
			_, _ = p.parseIdent()
		}
		s.Name = "role"
		s.Default = true
		return s, nil
	}
	// SET [LOCAL] TRANSACTION <mode> — intercept before generic GUC path.
	// M0096-0002: supports ISOLATION LEVEL; other modes accepted as no-op.
	if p.acceptKeyword(KwTransaction) {
		st := &SetTransactionStmt{pos: t.Pos, Local: isLocal}
		for {
			switch {
			case p.acceptIdentKeyword("isolation"):
				if !p.acceptIdentKeyword("level") {
					return nil, p.errAtCur("expected LEVEL after ISOLATION")
				}
				level, err := p.parseIsolationLevelName()
				if err != nil {
					return nil, err
				}
				st.IsolationLevel = level
			case p.acceptIdentKeyword("read"):
				_ = p.acceptIdentKeyword("only") || p.acceptIdentKeyword("write")
			case p.acceptKeyword(KwNot):
				_ = p.acceptIdentKeyword("deferrable")
			case p.acceptIdentKeyword("deferrable"):
			default:
				goto setTxDone
			}
		}
	setTxDone:
		return st, nil
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

// parseCluster parses CLUSTER statements (M0095-0008).
//
// Syntax accepted:
//
//	CLUSTER [VERBOSE]
//	CLUSTER [VERBOSE] tablename [USING indexname]
//
// Executor stub: CLUSTER without a table always succeeds.
// CLUSTER with a table succeeds when the table exists, errors otherwise.
func (p *parser) parseCluster() (Stmt, error) {
	t := p.advance() // consume CLUSTER
	c := &ClusterStmt{pos: t.Pos}

	// Optional VERBOSE.
	if p.acceptKeyword(KwVerbose) {
		c.Verbose = true
	}

	// If the next token starts a statement terminator or is EOF, no table.
	if p.cur().Kind == TokenEOF ||
		(p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return c, nil
	}

	// Parse table name (possibly schema-qualified).
	name, err := p.parseObjectName()
	if err != nil {
		return nil, err
	}
	c.Target = &name

	// Optional USING indexname.
	if p.acceptKeyword(KwUsing) {
		idx, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		c.IndexName = identText(idx)
	}

	return c, nil
}

// parsePrepare: PREPARE name [(param_type, …)] AS query (M0096-0006)
func (p *parser) parsePrepare() (Stmt, error) {
	t := p.advance() // PREPARE
	nameIdent, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	name := identText(nameIdent)
	// Parse optional parameter type list: (type1, type2, …)
	var paramTypes []string
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance()
		for p.cur().Kind != TokenEOF {
			if p.cur().Kind == TokenSymbol && p.cur().Value == ")" {
				p.advance()
				break
			}
			on, terr := p.parseTypeNameAfterCast()
			if terr != nil {
				paramTypes = append(paramTypes, "unknown")
			} else {
				paramTypes = append(paramTypes, on.Name)
			}
			if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
				p.advance()
			}
		}
	}
	// Consume AS
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwAs {
		p.advance()
	} else {
		// Some clients omit AS, or use different spacing — try to continue.
	}
	// Parse the prepared query
	if p.cur().Kind == TokenEOF || (p.cur().Kind == TokenSymbol && p.cur().Value == ";") {
		return &PrepareStmt{pos: t.Pos, Name: name, ParamTypes: paramTypes}, nil
	}
	query, err := p.parseStatement()
	if err != nil {
		return nil, err
	}
	return &PrepareStmt{pos: t.Pos, Name: name, ParamTypes: paramTypes, Query: query}, nil
}

// parseExecute: EXECUTE name [(param, …)] (M0096-0006)
func (p *parser) parseExecute() (Stmt, error) {
	t := p.advance() // EXECUTE
	nameIdent, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	name := identText(nameIdent)
	var params []Expr
	if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
		p.advance()
		if !(p.cur().Kind == TokenSymbol && p.cur().Value == ")") {
			params, err = p.parseExprList()
			if err != nil {
				return nil, err
			}
		}
		if !p.acceptSymbol(")") {
			return nil, p.errAtCur("expected ')'")
		}
	}
	return &ExecuteStmt{pos: t.Pos, Name: name, Params: params}, nil
}

// parseDeallocate: DEALLOCATE [PREPARE] {name | ALL} (M0096-0006)
func (p *parser) parseDeallocate() (Stmt, error) {
	t := p.advance()               // DEALLOCATE
	_ = p.acceptKeyword(KwPrepare) // optional PREPARE keyword
	name := ""
	if p.acceptKeyword(KwAll) {
		name = ""
	} else {
		nameIdent, err := p.parseIdent()
		if err != nil {
			return nil, err
		}
		name = identText(nameIdent)
	}
	return &DeallocateStmt{pos: t.Pos, Name: name}, nil
}

// parseMerge parses a MERGE INTO statement (M0096-0010).
//
// Syntax:
//
//	MERGE INTO target [AS alias]
//	USING source [AS alias]
//	ON condition
//	WHEN MATCHED [AND cond] THEN { UPDATE SET … | DELETE }
//	WHEN NOT MATCHED [AND cond] THEN INSERT [(cols)] VALUES (…)
func (p *parser) parseMerge() (Stmt, error) {
	t := p.advance() // consume MERGE
	// Optional INTO
	_ = p.acceptKeyword(KwInto)
	stmt := &MergeStmt{pos: t.Pos}

	// Target table with optional alias.
	target, err := p.parseRangeVar()
	if err != nil {
		return nil, err
	}
	stmt.Target = target

	// USING source
	if !p.acceptKeyword(KwUsing) && !p.acceptIdentKeyword("using") {
		return nil, p.errAtCur("expected USING after MERGE INTO target")
	}
	source, err := p.parseRangeVar()
	if err != nil {
		return nil, err
	}
	stmt.Source = source

	// ON condition
	if _, err := p.expectKeyword(KwOn); err != nil {
		return nil, err
	}
	onCond, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	stmt.On = onCond

	// One or more WHEN clauses.
	for p.cur().Kind == TokenKeyword && p.cur().Keyword == KwWhen {
		clause, err := p.parseMergeWhenClause()
		if err != nil {
			return nil, err
		}
		stmt.Clauses = append(stmt.Clauses, clause)
	}
	// Optional RETURNING target_list (M0097-0016 — parsed but not executed).
	if p.acceptKeyword(KwReturning) {
		ret, err := p.parseTargetList()
		if err != nil {
			return nil, err
		}
		stmt.Returning = ret
	}
	return stmt, nil
}

// parseMergeWhenClause parses one WHEN [NOT] MATCHED [BY SOURCE|TARGET] [AND cond] THEN action.
// M0097-0016 adds DO NOTHING action and BY SOURCE/TARGET modifiers.
func (p *parser) parseMergeWhenClause() (*MergeWhenClause, error) {
	t := p.advance() // WHEN
	clause := &MergeWhenClause{pos: t.Pos}

	// NOT MATCHED [BY SOURCE|TARGET] or MATCHED
	if p.acceptKeyword(KwNot) {
		clause.Matched = false
		if _, err := p.expectKeyword(KwMatched); err != nil {
			return nil, err
		}
		// Optional BY SOURCE or BY TARGET (M0097-0016).
		if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwBy {
			p.advance() // consume BY
			if p.acceptIdentKeyword("source") {
				clause.BySource = true
			} else if p.acceptIdentKeyword("target") {
				clause.ByTarget = true
			}
			// If neither, we already consumed BY — that's odd, but be tolerant.
		}
	} else if p.acceptKeyword(KwMatched) {
		clause.Matched = true
	} else {
		return nil, p.errAtCur("expected MATCHED or NOT MATCHED after WHEN")
	}

	// Optional AND condition.
	if p.acceptKeyword(KwAnd) {
		cond, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		clause.Condition = cond
	}

	// THEN
	if _, err := p.expectKeyword(KwThen); err != nil {
		return nil, err
	}

	// Action: UPDATE, DELETE, INSERT, or DO NOTHING.
	switch {
	case p.acceptKeyword(KwUpdate):
		clause.Action = MergeActionUpdate
		if _, err := p.expectKeyword(KwSet); err != nil {
			return nil, err
		}
		assigns, err := p.parseAssignList()
		if err != nil {
			return nil, err
		}
		clause.UpdateAssigns = assigns

	case p.acceptKeyword(KwDelete):
		clause.Action = MergeActionDelete

	case p.acceptKeyword(KwInsert):
		clause.Action = MergeActionInsert
		// Optional column list.
		if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
			p.advance()
			cols, err := p.parseColumnNameList()
			if err != nil {
				return nil, err
			}
			clause.InsertColumns = cols
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
		}
		// VALUES or DEFAULT VALUES.
		if p.acceptKeyword(KwValues) {
			if !p.acceptSymbol("(") {
				return nil, p.errAtCur("expected '('")
			}
			vals, err := p.parseExprList()
			if err != nil {
				return nil, err
			}
			clause.InsertValues = vals
			if !p.acceptSymbol(")") {
				return nil, p.errAtCur("expected ')'")
			}
		} else if p.acceptKeyword(KwDefault) {
			if _, err := p.expectKeyword(KwValues); err != nil {
				return nil, err
			}
			// InsertValues remains nil to signal DEFAULT VALUES.
		} else {
			return nil, p.errAtCur("expected VALUES or DEFAULT VALUES after INSERT")
		}

	case p.acceptKeyword(KwDo):
		// DO NOTHING (M0097-0016).
		if _, err := p.expectKeyword(KwNothing); err != nil {
			return nil, err
		}
		clause.Action = MergeActionDoNothing

	default:
		return nil, p.errAtCur("expected UPDATE, DELETE, INSERT, or DO NOTHING after THEN")
	}

	return clause, nil
}

// parseReset: RESET name | RESET ALL | RESET SESSION AUTHORIZATION
func (p *parser) parseReset() (Stmt, error) {
	t := p.advance()
	if p.acceptKeyword(KwAll) {
		return &ResetStmt{pos: t.Pos, All: true}, nil
	}
	// RESET SESSION AUTHORIZATION — no-op: map to "session_authorization" GUC.
	// SESSION is KwCatUnreserved so parseGUCName would consume it and leave
	// "AUTHORIZATION" as a stray token, causing a syntax error. Intercept here.
	if p.acceptKeyword(KwSession) {
		if !p.acceptIdentKeyword("authorization") {
			return nil, p.errAtCur("expected AUTHORIZATION after RESET SESSION")
		}
		return &ResetStmt{pos: t.Pos, Name: "session_authorization"}, nil
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

// ── Cursor DDL (M0097-0003) ─────────────────────────────────────────────────

// parseDeclareCursor parses DECLARE name [SCROLL|NO SCROLL] CURSOR [WITH|WITHOUT HOLD] FOR select.
func (p *parser) parseDeclareCursor() (Stmt, error) {
	pos := p.cur().Pos
	p.advance() // consume DECLARE

	// cursor name (may be an identifier or unreserved keyword)
	nameToken := p.cur()
	if nameToken.Kind != TokenIdent && nameToken.Kind != TokenKeyword {
		return nil, p.errAtCur("expected cursor name after DECLARE")
	}
	name := nameToken.Value
	p.advance()

	// optional SCROLL / NO SCROLL
	if p.acceptIdentKeyword("no") {
		p.acceptIdentKeyword("scroll")
	} else {
		p.acceptIdentKeyword("scroll")
	}

	// CURSOR
	if !p.acceptIdentKeyword("cursor") {
		return nil, p.errAtCur("expected CURSOR")
	}

	// optional WITH/WITHOUT HOLD
	if p.acceptIdentKeyword("with") || p.acceptIdentKeyword("without") {
		p.acceptIdentKeyword("hold")
	}

	// FOR
	if !p.acceptKeyword(KwFor) {
		return nil, p.errAtCur("expected FOR in DECLARE CURSOR")
	}

	// SELECT … INTO is not permitted inside a cursor (M0097-0020).
	old, oldNoPos := p.selectIntoErrMsg, p.selectIntoNoPos
	p.selectIntoErrMsg = "SELECT ... INTO is not allowed here"
	p.selectIntoNoPos = false
	query, err := p.parseSelect()
	p.selectIntoErrMsg, p.selectIntoNoPos = old, oldNoPos
	if err != nil {
		return nil, err
	}
	return &DeclareCursorStmt{pos: pos, Name: name, Query: query}, nil
}

// parseFetchCursor parses FETCH direction cursor_name.
// Supports all PG directions: NEXT, PRIOR, FIRST, LAST, ABSOLUTE n,
// RELATIVE n, FORWARD [n|ALL], BACKWARD [n|ALL], ALL, n. M0097-0069.
func (p *parser) parseFetchCursor() (Stmt, error) {
	pos := p.cur().Pos
	p.advance() // consume "fetch"

	count := int64(1) // default: NEXT = 1
	forward := true

	switch {
	case p.acceptIdentKeyword("next"):
		count = 1
	case p.acceptIdentKeyword("prior"):
		forward = false
		count = 1
	case p.acceptIdentKeyword("first"):
		count = 1
	case p.acceptIdentKeyword("last"):
		forward = false
		count = 1
	case p.acceptIdentKeyword("absolute"):
		if p.cur().Kind == TokenIntLit {
			var err error
			count, err = p.parseIntLit()
			if err != nil {
				return nil, err
			}
		}
	case p.acceptIdentKeyword("relative"):
		if p.cur().Kind == TokenIntLit {
			var err error
			count, err = p.parseIntLit()
			if err != nil {
				return nil, err
			}
		}
	case p.acceptIdentKeyword("forward"):
		if p.acceptKeyword(KwAll) {
			count = -1
		} else if p.cur().Kind == TokenIntLit {
			var err error
			count, err = p.parseIntLit()
			if err != nil {
				return nil, err
			}
		}
	case p.acceptIdentKeyword("backward"):
		forward = false
		if p.acceptKeyword(KwAll) {
			count = -1
		} else if p.cur().Kind == TokenIntLit {
			var err error
			count, err = p.parseIntLit()
			if err != nil {
				return nil, err
			}
		}
	case p.acceptKeyword(KwAll):
		count = -1
	default:
		if p.cur().Kind == TokenIntLit {
			var err error
			count, err = p.parseIntLit()
			if err != nil {
				return nil, err
			}
		}
	}

	// FROM or IN (optional)
	if !p.acceptKeyword(KwFrom) {
		p.acceptKeyword(KwIn)
	}

	// cursor name
	nameToken := p.cur()
	if nameToken.Kind != TokenIdent && nameToken.Kind != TokenKeyword {
		return nil, p.errAtCur("expected cursor name")
	}
	cursorName := nameToken.Value
	p.advance()

	return &FetchStmt{pos: pos, CursorName: cursorName, Count: count, Forward: forward}, nil
}

// parseCommentOnTail dispatches on the object type after "COMMENT ON".
// Returns (stmt, true, nil) for supported types (TABLE, INDEX, COLUMN, CONSTRAINT).
// Returns (nil, false, nil) for unsupported types (caller skips to semicolon).
// Returns (nil, ?, err) on parse error.
func (p *parser) parseCommentOnTail(pos int) (Stmt, bool, error) {
	cs := &CommentOnStmt{pos: pos}
	switch {
	case p.acceptKeyword(KwTable):
		cs.ObjKind = "table"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptKeyword(KwIndex):
		cs.ObjKind = "index"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptKeyword(KwColumn):
		// COLUMN table.col — parseObjectName reads "table.col" as Schema=table, Name=col.
		cs.ObjKind = "column"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		// Schema field holds table name; Name field holds column name.
		cs.ObjName = ObjectName{Name: name.Schema}
		cs.SubName = name.Name
	case p.acceptKeyword(KwConstraint):
		cs.ObjKind = "constraint"
		// constraint name
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, true, p.errAtCur("expected constraint name")
		}
		cs.SubName = tok.Value
		p.advance()
		// ON table
		if !p.acceptKeyword(KwOn) {
			return nil, true, p.errAtCur("expected ON after constraint name in COMMENT ON CONSTRAINT")
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("statistics"):
		// COMMENT ON STATISTICS name IS '...'. M0097-0023.
		cs.ObjKind = "statistics"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	default:
		return nil, false, nil
	}
	// IS 'text' | IS NULL
	if !p.acceptKeyword(KwIs) {
		return nil, true, p.errAtCur("expected IS after object name in COMMENT ON")
	}
	if p.acceptKeyword(KwNull) {
		cs.Description = ""
	} else {
		tok := p.cur()
		if tok.Kind != TokenStringLit {
			return nil, true, p.errAtCur("expected string literal or NULL after IS in COMMENT ON")
		}
		cs.Description = tok.Value
		p.advance()
	}
	return cs, true, nil
}

// parseMoveCursor parses MOVE [direction] [count] [FROM|IN] cursor_name.
// MOVE repositions a cursor without returning rows; executed as a no-op.
func (p *parser) parseMoveCursor() (Stmt, error) {
	pos := p.cur().Pos
	p.advance() // consume "move"
	// Consume optional direction and count tokens.
	p.acceptIdentKeyword("forward")
	p.acceptIdentKeyword("backward")
	p.acceptIdentKeyword("prior")
	p.acceptKeyword(KwAll)
	if p.cur().Kind == TokenIntLit {
		if _, err := p.parseIntLit(); err != nil {
			return nil, err
		}
	}
	// Consume optional FROM or IN.
	if !p.acceptKeyword(KwFrom) {
		p.acceptKeyword(KwIn)
	}
	// Consume cursor name.
	if p.cur().Kind == TokenIdent || p.cur().Kind == TokenKeyword {
		p.advance()
	}
	return &CompatNoopStmt{pos: pos, Tag: "MOVE"}, nil
}

// parseCloseCursor parses CLOSE {cursor_name|ALL}.
func (p *parser) parseCloseCursor() (Stmt, error) {
	pos := p.cur().Pos
	p.advance() // consume "close"

	if p.acceptKeyword(KwAll) {
		return &CloseStmt{pos: pos, Name: ""}, nil
	}

	nameToken := p.cur()
	if nameToken.Kind != TokenIdent && nameToken.Kind != TokenKeyword {
		return nil, p.errAtCur("expected cursor name or ALL")
	}
	name := nameToken.Value
	p.advance()
	return &CloseStmt{pos: pos, Name: name}, nil
}

// parseLockTable parses LOCK [TABLE] [ONLY] rel [, ...] [IN lock_mode MODE] [NOWAIT].
// Lock mode names follow PostgreSQL convention (e.g. "AccessExclusiveLock"). M0097.
func (p *parser) parseLockTable(pos int) (*LockTableStmt, error) {
	p.advance() // consume "lock"
	// skip optional TABLE keyword (it's a keyword token KwTable)
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTable {
		p.advance()
	}
	// parse relation list: [ONLY] schema.table [*] [, ...]
	var rels []LockTableRelation
	for {
		// skip optional ONLY keyword
		p.acceptIdentKeyword("only")
		// relation name: must be ident or keyword (for reserved-word table names)
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, p.errAtCur("expected relation name")
		}
		name := tok.Value
		p.advance()
		var schema string
		if p.cur().Kind == TokenSymbol && p.cur().Value == "." {
			p.advance()
			schema = name
			tok2 := p.cur()
			if tok2.Kind != TokenIdent && tok2.Kind != TokenKeyword {
				return nil, p.errAtCur("expected relation name after schema")
			}
			name = tok2.Value
			p.advance()
		}
		// skip optional * (inheritance wildcard)
		if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
			p.advance()
		}
		rels = append(rels, LockTableRelation{Schema: schema, Name: name})
		if p.cur().Kind != TokenSymbol || p.cur().Value != "," {
			break
		}
		p.advance() // consume ","
	}
	// parse optional IN <mode> MODE — "IN" is a keyword (KwIn)
	mode := "AccessExclusiveLock" // default per PostgreSQL
	if (p.cur().Kind == TokenKeyword && p.cur().Keyword == KwIn) ||
		(p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "in")) {
		p.advance() // consume "in"
		mode = p.parseLockMode()
		// consume optional MODE keyword (may be TokenIdent or TokenKeyword)
		if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "mode") {
			p.advance()
		}
	}
	// parse optional NOWAIT (KwNowait is a keyword token)
	noWait := false
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwNowait {
		noWait = true
		p.advance()
	}
	return &LockTableStmt{pos: pos, Relations: rels, Mode: mode, NoWait: noWait}, nil
}

// lockModeWords maps multi-word lock modes to their PostgreSQL internal name.
var lockModeNames = []struct {
	words []string
	name  string
}{
	{[]string{"access", "share"}, "AccessShareLock"},
	{[]string{"row", "share"}, "RowShareLock"},
	{[]string{"row", "exclusive"}, "RowExclusiveLock"},
	{[]string{"share", "update", "exclusive"}, "ShareUpdateExclusiveLock"},
	{[]string{"share", "row", "exclusive"}, "ShareRowExclusiveLock"},
	{[]string{"share"}, "ShareLock"},
	{[]string{"exclusive"}, "ExclusiveLock"},
	{[]string{"access", "exclusive"}, "AccessExclusiveLock"},
}

// parseLockMode reads the lock mode keywords (without the trailing MODE keyword).
func (p *parser) parseLockMode() string {
	// collect words until we hit MODE or a non-identifier token
	var words []string
	for {
		tok := p.cur()
		if tok.Kind == TokenIdent || tok.Kind == TokenKeyword {
			w := strings.ToLower(tok.Value)
			if w == "mode" {
				break
			}
			words = append(words, w)
			p.advance()
		} else {
			break
		}
	}
	// match against known multi-word modes (longest match first)
	for _, entry := range lockModeNames {
		if len(entry.words) == len(words) {
			match := true
			for i, w := range entry.words {
				if words[i] != w {
					match = false
					break
				}
			}
			if match {
				return entry.name
			}
		}
	}
	return "AccessExclusiveLock" // fallback
}
