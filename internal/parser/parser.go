package parser

import (
	"fmt"
	"strings"
	"sync"

	"github.com/goopg/goopg/internal/utils/mmgr"
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
	// Code overrides the SQLSTATE the wire layer reports for this error.
	// Empty means the parser default, 42601 (syntax_error). PG's grammar
	// raises a handful of non-42601 errors from inside a production — e.g.
	// opt_float's precision checks are ERRCODE_INVALID_PARAMETER_VALUE
	// (22023) — and those cases set this so goopg reports the same code.
	// Kept as a bare string so internal/parser stays free of an sqlstate
	// import.
	Code string
	// Hint carries an optional HINT wire field alongside Code, for the same
	// kind of non-syntax error raised during parsing (e.g. SIMILAR TO's
	// parse-time constant-fold rejecting a >1-char ESCAPE string with
	// 22025/"Escape string must be empty or one character."). Empty means no
	// HINT field. M0134-0070.
	Hint string
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
func ParseExpr(input string, mc ...*mmgr.Context) (Expr, error) {
	var sctx *mmgr.Context
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
// RouteBatch is the parser-rewrite dispatch hook (docs/design/not_ralph/
// 03-strangler-migration.md §2): when non-nil it receives the freshly lexed
// token slice and either owns the whole batch (handled=true, stmts valid) or
// declines (handled=false → the legacy recursive-descent path below runs).
// Wired by production startup code to sqlparser.RouteBatch; nil in tests and
// by default, which keeps behavior identical to pre-rewrite Parse.
var RouteBatch func(toks []Token) (stmts []Stmt, handled bool, err error)

func Parse(input string, mc ...*mmgr.Context) ([]Stmt, error) {
	var sctx *mmgr.Context
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

	// Parser-rewrite routing hook (docs/design/not_ralph/03-strangler-
	// migration.md §2). Nil by default = all-legacy, so this is inert until
	// postmaster/server main wires sqlparser's implementation in. When the
	// batch routes (every fragment's class ported + gated), the new LALR
	// parser owns it wholesale; mixed batches stay legacy by design.
	//
	// POOL OWNERSHIP: `toks` borrows from tokenSlicePool. Only paths that
	// RETURN from inside this hook may Put the slice — declining must fall
	// through WITHOUT Put so the legacy body below keeps sole ownership and
	// performs its own Put on each exit (a premature Put here handed the
	// backing array to a concurrent lexInto mid-parse; pgbench smoke caught
	// it as cross-client token corruption).
	if RouteBatch != nil {
		stmts, handled, rerr := RouteBatch(toks)
		if handled || rerr != nil {
			if sp != nil {
				tokenSlicePool.Put(sp)
			}
			if rerr != nil {
				return nil, rerr
			}
			return stmts, nil
		}
	}

	// parser struct is 32 bytes; stack allocation is free. M0107-0003.
	var p parser
	p.tokens = toks
	p.idx = 0
	p.src = input

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
			err := p.errSyntaxAtCur()
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
	// src holds the original input string, used to capture raw source spans
	// (e.g. the verbatim view body for pg_get_viewdef). Token.Pos values are
	// byte offsets into this string.
	src string
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

// captureSrcSpan returns the raw source text from byte offset startPos up to
// the start of endTok (its Pos), trimmed of surrounding whitespace and any
// trailing ';'. When endTok is EOF (Pos not meaningful) the span runs to the
// end of the source. Returns "" when the source is unavailable or the offsets
// are out of range. Used to preserve verbatim sub-statement text (e.g. the
// view body for pg_get_viewdef).
func (p *parser) captureSrcSpan(startPos int, endTok Token) string {
	if p.src == "" || startPos < 0 || startPos > len(p.src) {
		return ""
	}
	end := endTok.Pos
	if endTok.Kind == TokenEOF || end <= 0 || end > len(p.src) {
		end = len(p.src)
	}
	if end < startPos {
		return ""
	}
	span := p.src[startPos:end]
	span = strings.TrimSpace(span)
	span = strings.TrimRight(span, "; \t\n\r")
	return span
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

// grantObjectName resolves the relation name in a GRANT/REVOKE object list,
// given the name token and the two tokens that follow it. A schema-qualified
// `schema.table` (name, ".", table) yields the table component; otherwise the
// bare name. Used to key a table ACL change to its pg_class tuple (0118-0109).
func grantObjectName(name, after1, after2 Token) string {
	if after1.Kind == TokenSymbol && after1.Value == "." {
		return after2.Value
	}
	return name.Value
}

// grantNonTableClass reports whether word names a GRANT object class other than
// TABLE (the default). Such grants do not change a pg_class ACL tuple, so the
// in-place relhasindex serialization (0118-0109) does not apply.
func grantNonTableClass(word string) bool {
	switch strings.ToLower(word) {
	case "schema", "sequence", "function", "procedure", "routine",
		"domain", "type", "language", "tablespace", "foreign",
		"large", "all", "parameter", "system":
		return true
	}
	return false
}

// scanGrantTrailingClause scans a grantee list starting at roleStart for the
// shared trailing-clause grammar every object-privilege GRANT/REVOKE variant
// (TABLE column-level, TYPE/DOMAIN, DATABASE, PARAMETER — gram.y's
// GrantStmt/RevokeStmt) allows
// after the role list: GRANT's `opt_grant_grant_option opt_granted_by` or
// REVOKE's `opt_granted_by opt_drop_behavior`. Mirrors
// buildRoleMembershipChange's inline scan, generalized across the three
// builders below so a fix (e.g. this function's own continue-after-WITH,
// needed so GRANTED BY is still found when it follows WITH GRANT OPTION) only
// needs to land once. Returns the role-list end index, whether WITH GRANT
// OPTION was seen, and the GRANTED BY role name (quote-stripped, "" if
// absent). M0119-0004-ACLHEAP.
func scanGrantTrailingClause(toks []Token, roleStart int) (roleEnd int, withGrantOption bool, grantedBy string) {
	roleEnd = len(toks)
	for i := roleStart; i < len(toks); i++ {
		switch strings.ToLower(toks[i].Value) {
		case "with":
			// WITH GRANT OPTION — record the flag, end the role list, and keep
			// scanning: GRANT's grammar allows a following GRANTED BY clause.
			if roleEnd > i {
				roleEnd = i
			}
			if i+2 < len(toks) &&
				strings.EqualFold(toks[i+1].Value, "grant") &&
				strings.EqualFold(toks[i+2].Value, "option") {
				withGrantOption = true
				i += 2
			}
			continue
		case "granted":
			// GRANTED BY <role> — record the explicit grantor, end the role
			// list, and keep scanning (REVOKE's opt_drop_behavior follows
			// opt_granted_by, so a trailing CASCADE/RESTRICT can still appear).
			if roleEnd > i {
				roleEnd = i
			}
			if i+2 < len(toks) && strings.EqualFold(toks[i+1].Value, "by") {
				grantedBy = strings.Trim(toks[i+2].Value, `"`)
				i += 2
			}
			continue
		case "cascade", "restrict":
			if roleEnd > i {
				roleEnd = i
			}
		default:
			continue
		}
		break
	}
	return roleEnd, withGrantOption, grantedBy
}

// buildTypeACLChange parses the token run of a GRANT/REVOKE … ON TYPE|DOMAIN …
// statement into a TypeACLChange. toks is every token after the GRANT/REVOKE
// keyword with the trailing ';' already excluded. It mirrors the server-side
// string recorder (grant_ddl.go) — privilege list before ON, type names between
// the TYPE/DOMAIN keyword and TO|FROM, and the role list after TO|FROM, with a
// trailing WITH GRANT OPTION / GRANTED BY / CASCADE / RESTRICT clause captured
// via scanGrantTrailingClause. Returns nil when any of the three lists is
// empty (an unparseable form the caller leaves as a successful no-op).
// M0119-0004-ACLHEAP.
func buildTypeACLChange(revoke, isDomain bool, toks []Token) *TypeACLChange {
	onIdx := tokIndexOf(toks, 0, "on")
	if onIdx < 0 || onIdx+2 > len(toks) {
		return nil
	}
	nameStart := onIdx + 2 // skip the ON and the TYPE/DOMAIN keyword
	sep := "to"
	if revoke {
		sep = "from"
	}
	sepIdx := tokIndexOf(toks, nameStart, sep)
	if sepIdx < 0 || sepIdx < nameStart {
		return nil
	}
	roleStart := sepIdx + 1
	roleEnd, withGrantOption, grantedBy := scanGrantTrailingClause(toks, roleStart)
	tac := &TypeACLChange{
		Revoke:          revoke,
		IsDomain:        isDomain,
		Privileges:      splitTokPrivileges(toks[:onIdx]),
		TypeNames:       splitTokObjectNames(toks[nameStart:sepIdx]),
		Grantees:        splitTokRoles(toks[roleStart:roleEnd]),
		WithGrantOption: withGrantOption,
		GrantedBy:       grantedBy,
	}
	if len(tac.Privileges) == 0 || len(tac.TypeNames) == 0 || len(tac.Grantees) == 0 {
		return nil
	}
	return tac
}

// buildDatabaseACLChange parses the token run of a GRANT/REVOKE … ON DATABASE …
// statement into a DatabaseACLChange, mirroring buildTypeACLChange's shape.
// toks is every token after the GRANT/REVOKE keyword with the trailing ';'
// excluded. Returns nil when any required list is empty (an unparseable form
// the caller leaves as the pre-existing xmax-only no-op).
// M0119-0004-ACLHEAP (datacl half).
func buildDatabaseACLChange(revoke bool, toks []Token) *DatabaseACLChange {
	onIdx := tokIndexOf(toks, 0, "on")
	if onIdx < 0 || onIdx+2 > len(toks) {
		return nil
	}
	nameStart := onIdx + 2 // skip the ON and the DATABASE keyword
	sep := "to"
	if revoke {
		sep = "from"
	}
	sepIdx := tokIndexOf(toks, nameStart, sep)
	if sepIdx < 0 || sepIdx < nameStart {
		return nil
	}
	roleStart := sepIdx + 1
	roleEnd, withGrantOption, grantedBy := scanGrantTrailingClause(toks, roleStart)
	dac := &DatabaseACLChange{
		Revoke:          revoke,
		Privileges:      splitTokPrivileges(toks[:onIdx]),
		DatabaseNames:   splitTokRoles(toks[nameStart:sepIdx]),
		Grantees:        splitTokRoles(toks[roleStart:roleEnd]),
		WithGrantOption: withGrantOption,
		GrantedBy:       grantedBy,
	}
	if len(dac.Privileges) == 0 || len(dac.DatabaseNames) == 0 || len(dac.Grantees) == 0 {
		return nil
	}
	return dac
}

// splitTokDottedNames renders each comma-separated run as a single joined
// string, preserving embedded "." separators verbatim rather than splitting
// into schema/name (GUC parameter names may themselves be dotted, e.g.
// "pgaudit.log" — gram.y's parameter_name production, not qualified_name).
// Quoting is stripped per token and the result lower-cased, mirroring
// PostgreSQL's convert_GUC_name_for_parameter_acl (guc.c) case-folding.
// M0119-0004-ACLHEAP (parameter ACL half).
func splitTokDottedNames(toks []Token) []string {
	var out []string
	for _, run := range splitTokRuns(toks) {
		var b strings.Builder
		for _, tk := range run {
			b.WriteString(strings.Trim(tk.Value, `"`))
		}
		if s := b.String(); s != "" {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

// buildParameterACLChange parses the token run of a GRANT/REVOKE … ON
// PARAMETER … statement into a ParameterACLChange, mirroring
// buildDatabaseACLChange's shape. toks is every token after the GRANT/REVOKE
// keyword with the trailing ';' already excluded. Returns nil when any
// required list is empty (an unparseable form the caller leaves as a
// successful no-op). M0119-0004-ACLHEAP (parameter ACL half).
func buildParameterACLChange(revoke bool, toks []Token) *ParameterACLChange {
	onIdx := tokIndexOf(toks, 0, "on")
	if onIdx < 0 || onIdx+2 > len(toks) {
		return nil
	}
	nameStart := onIdx + 2 // skip the ON and the PARAMETER keyword
	sep := "to"
	if revoke {
		sep = "from"
	}
	sepIdx := tokIndexOf(toks, nameStart, sep)
	if sepIdx < 0 || sepIdx < nameStart {
		return nil
	}
	roleStart := sepIdx + 1
	roleEnd, withGrantOption, grantedBy := scanGrantTrailingClause(toks, roleStart)
	pac := &ParameterACLChange{
		Revoke:          revoke,
		Privileges:      splitTokPrivileges(toks[:onIdx]),
		ParamNames:      splitTokDottedNames(toks[nameStart:sepIdx]),
		Grantees:        splitTokRoles(toks[roleStart:roleEnd]),
		WithGrantOption: withGrantOption,
		GrantedBy:       grantedBy,
	}
	if len(pac.Privileges) == 0 || len(pac.ParamNames) == 0 || len(pac.Grantees) == 0 {
		return nil
	}
	return pac
}

// defaclObjTypeFromTarget maps a defacl_privilege_target keyword run (the
// token(s) right after `ON` in a DefACLAction) to the AlterDefaultPrivilegesStmt
// ObjType string, and reports how many tokens it consumed (1, or 2 for the
// two-word "LARGE OBJECTS" form). Mirrors gram.y's defacl_privilege_target
// production; an unrecognized keyword yields ("", 0). M0110-0001 (DU-002
// slice 438 follow-up).
func defaclObjTypeFromTarget(toks []Token, at int) (string, int) {
	if at >= len(toks) {
		return "", 0
	}
	switch strings.ToLower(toks[at].Value) {
	case "tables":
		return "table", 1
	case "sequences":
		return "sequence", 1
	case "functions", "routines":
		return "function", 1
	case "types":
		return "type", 1
	case "schemas":
		return "schema", 1
	case "large":
		if at+1 < len(toks) && strings.EqualFold(toks[at+1].Value, "objects") {
			return "largeobject", 2
		}
	}
	return "", 0
}

// buildAlterDefaultPrivileges parses the token run of an `ALTER DEFAULT
// PRIVILEGES [FOR ROLE|USER role_list] [IN SCHEMA schema_list]
// {GRANT|REVOKE} ...` statement (everything after the "PRIVILEGES" keyword,
// trailing ';' already excluded) into an AlterDefaultPrivilegesStmt. Unlike
// the GRANT/REVOKE-family builders above (buildDatabaseACLChange et al.),
// which discriminate the object class from an ambiguous `ON <name-or-class>`
// clause, ALTER DEFAULT PRIVILEGES has an unambiguous, linear grammar
// (gram.y's DefACLOptionList DefACLAction), so this scans forward
// deterministically rather than searching for a fixed marker token.
// M0110-0001 (DU-002 slice 438 follow-up).
func buildAlterDefaultPrivileges(toks []Token) (*AlterDefaultPrivilegesStmt, error) {
	i := 0
	kwAt := func(idx int, kw string) bool {
		return idx < len(toks) && strings.EqualFold(toks[idx].Value, kw)
	}
	// nextBoundary finds the next top-level option/action keyword starting a
	// new DefACLOption or the DefACLAction itself, bounding a role/schema list.
	nextBoundary := func(from int) int {
		for j := from; j < len(toks); j++ {
			switch strings.ToLower(toks[j].Value) {
			case "for", "in", "grant", "revoke":
				return j
			}
		}
		return len(toks)
	}
	stmt := &AlterDefaultPrivilegesStmt{}
	// DefACLOptionList: any mix of "FOR ROLE|USER role_list" and
	// "IN SCHEMA schema_list", in either order, zero or more times (PG's
	// grammar allows repeats; the last one wins, matching a plain list
	// accumulation — goopg simply overwrites, which is what a single
	// occurrence — the overwhelmingly common case — does either way).
	for {
		if kwAt(i, "for") && (kwAt(i+1, "role") || kwAt(i+1, "user")) {
			i += 2
			end := nextBoundary(i)
			stmt.Roles = splitTokRoles(toks[i:end])
			i = end
			continue
		}
		if kwAt(i, "in") && kwAt(i+1, "schema") {
			i += 2
			end := nextBoundary(i)
			stmt.Schemas = splitTokRoles(toks[i:end])
			i = end
			continue
		}
		break
	}
	if !kwAt(i, "grant") && !kwAt(i, "revoke") {
		return nil, &SyntaxError{Pos: 0, Message: "ALTER DEFAULT PRIVILEGES requires a GRANT or REVOKE action"}
	}
	stmt.Revoke = kwAt(i, "revoke")
	i++
	if stmt.Revoke && kwAt(i, "grant") && kwAt(i+1, "option") && kwAt(i+2, "for") {
		stmt.GrantOptionFor = true
		i += 3
	}
	onIdx := tokIndexOf(toks, i, "on")
	if onIdx < 0 {
		return nil, &SyntaxError{Pos: 0, Message: "ALTER DEFAULT PRIVILEGES requires ON TABLES|SEQUENCES|FUNCTIONS|ROUTINES|TYPES|SCHEMAS|LARGE OBJECTS"}
	}
	stmt.Privileges = splitTokPrivileges(toks[i:onIdx])
	objType, consumed := defaclObjTypeFromTarget(toks, onIdx+1)
	if consumed == 0 {
		return nil, &SyntaxError{Pos: 0, Message: "ALTER DEFAULT PRIVILEGES requires ON TABLES|SEQUENCES|FUNCTIONS|ROUTINES|TYPES|SCHEMAS|LARGE OBJECTS"}
	}
	stmt.ObjType = objType
	i = onIdx + 1 + consumed
	sep := "to"
	if stmt.Revoke {
		sep = "from"
	}
	if !kwAt(i, sep) {
		return nil, &SyntaxError{Pos: 0, Message: "ALTER DEFAULT PRIVILEGES requires " + strings.ToUpper(sep) + " <role_list>"}
	}
	i++
	roleStart := i
	roleEnd := len(toks)
	for j := roleStart; j < len(toks); j++ {
		switch strings.ToLower(toks[j].Value) {
		case "with":
			if j+2 < len(toks) && strings.EqualFold(toks[j+1].Value, "grant") && strings.EqualFold(toks[j+2].Value, "option") {
				stmt.WithGrantOption = true
			}
			roleEnd = j
		case "cascade", "restrict":
			roleEnd = j
		default:
			continue
		}
		break
	}
	stmt.Grantees = splitTokRoles(toks[roleStart:roleEnd])
	if len(stmt.Privileges) == 0 || len(stmt.Grantees) == 0 {
		return nil, &SyntaxError{Pos: 0, Message: "ALTER DEFAULT PRIVILEGES: empty privilege or grantee list"}
	}
	return stmt, nil
}

// buildRoleMembershipChange parses the token run of a `GRANT <role>[, ...]
// TO <role>[, ...] [WITH ADMIN OPTION] [GRANTED BY <role>]` or `REVOKE
// [{ADMIN|INHERIT|SET} OPTION FOR] <role>[, ...] FROM <role>[, ...] [GRANTED
// BY <role>] [CASCADE|RESTRICT]` statement into a RoleMembershipChange. toks
// is every token after the GRANT/REVOKE keyword with the trailing ';'
// excluded. The caller only reaches this builder when no "on" token appeared
// anywhere in the statement — every privilege-GRANT variant requires one, so
// its absence is the discriminator. Returns nil when either role list is
// empty (an unparseable form the caller leaves as a successful no-op).
// M0119-0004-ACLHEAP.
func buildRoleMembershipChange(revoke bool, toks []Token) *RoleMembershipChange {
	start := 0
	revokeOption := ""
	if revoke && len(toks) >= 3 &&
		strings.EqualFold(toks[1].Value, "option") &&
		strings.EqualFold(toks[2].Value, "for") {
		// REVOKE's `ColId OPTION FOR` prefix (gram.y): ColId is any
		// identifier, but pg_auth_members only recognizes these three —
		// GrantRole (user.c) raises "unrecognized role option" for anything
		// else. goopg mirrors only the recognized set; an unrecognized
		// leading word falls through untouched (treated as the start of the
		// role list, matching the pre-existing lenient-parse posture of this
		// builder for other unparseable forms).
		switch strings.ToLower(toks[0].Value) {
		case "admin", "inherit", "set":
			revokeOption = strings.ToLower(toks[0].Value)
			start = 3
		}
	}
	sep := "to"
	if revoke {
		sep = "from"
	}
	sepIdx := tokIndexOf(toks, start, sep)
	if sepIdx < 0 || sepIdx <= start {
		return nil
	}
	granteeStart := sepIdx + 1
	granteeEnd := len(toks)
	var adminOpt, inheritOpt, setOpt *bool
	grantedBy := ""
	cascade := false
	for i := granteeStart; i < len(toks); i++ {
		switch strings.ToLower(toks[i].Value) {
		case "with":
			// GRANT's trailing `WITH { ADMIN | INHERIT | SET } { OPTION |
			// TRUE | FALSE } [, ...]` list (grant_role_opt_list, gram.y).
			// REVOKE has no WITH clause, so this only fires for GRANT.
			// End the grantee list here and resume scanning (for a
			// following GRANTED BY) right after the option list.
			granteeEnd = i
			var next int
			adminOpt, inheritOpt, setOpt, next = parseGrantRoleOptList(toks, i+1)
			i = next - 1
			continue
		case "granted":
			// GRANTED BY <role> — record the explicit grantor and end the
			// grantee list, then resume scanning (REVOKE's opt_drop_behavior,
			// gram.y, follows opt_granted_by — a trailing CASCADE/RESTRICT
			// can still appear after GRANTED BY <role>).
			if granteeEnd > i {
				granteeEnd = i
			}
			if i+2 < len(toks) && strings.EqualFold(toks[i+1].Value, "by") {
				grantedBy = strings.Trim(toks[i+2].Value, `"`)
				i += 2
			}
			continue
		case "cascade", "restrict":
			cascade = strings.EqualFold(toks[i].Value, "cascade")
			if granteeEnd > i {
				granteeEnd = i
			}
		default:
			continue
		}
		break
	}
	rmc := &RoleMembershipChange{
		Revoke:          revoke,
		RevokeOption:    revokeOption,
		WithAdminOption: adminOpt != nil && *adminOpt,
		AdminOption:     adminOpt,
		InheritOption:   inheritOpt,
		SetOption:       setOpt,
		Roles:           splitTokRoles(toks[start:sepIdx]),
		Grantees:        splitTokRoles(toks[granteeStart:granteeEnd]),
		GrantedBy:       grantedBy,
		Cascade:         cascade,
	}
	if len(rmc.Roles) == 0 || len(rmc.Grantees) == 0 {
		return nil
	}
	return rmc
}

// parseGrantRoleOptList parses grant_role_opt_list (gram.y): a comma-separated
// run of `{ ADMIN | INHERIT | SET } { OPTION | TRUE | FALSE }` pairs starting
// at toks[from] (already past the WITH keyword). Returns the tri-state
// admin/inherit/set pointers (nil = that option never appeared in the list)
// and the index of the first unconsumed token (e.g. "granted"/"cascade", or
// len(toks) at end of input). M0119-0004-ACLHEAP.
func parseGrantRoleOptList(toks []Token, from int) (admin, inherit, set *bool, next int) {
	i := from
	for i < len(toks) {
		optName := strings.ToLower(toks[i].Value)
		if optName != "admin" && optName != "inherit" && optName != "set" {
			break
		}
		if i+1 >= len(toks) {
			break
		}
		var val bool
		switch strings.ToLower(toks[i+1].Value) {
		case "option", "true":
			val = true
		case "false":
			val = false
		default:
			return admin, inherit, set, i
		}
		v := val
		switch optName {
		case "admin":
			admin = &v
		case "inherit":
			inherit = &v
		case "set":
			set = &v
		}
		i += 2
		if i < len(toks) && toks[i].Kind == TokenSymbol && toks[i].Value == "," {
			i++
			continue
		}
		break
	}
	return admin, inherit, set, i
}

// grantHasColumnList reports whether the GRANT/REVOKE token run carries a
// parenthesised column list BEFORE the ON keyword — the signature of a
// column-level grant (`GRANT SELECT (a, b) ON TABLE t …`). A function grant's
// parentheses follow ON (`GRANT EXECUTE ON FUNCTION f(int) …`), so depth/position
// matters. toks is every token after the GRANT/REVOKE keyword. M0119-0004-ACLHEAP.
func grantHasColumnList(toks []Token) bool {
	onIdx := tokIndexOf(toks, 0, "on")
	if onIdx < 0 {
		onIdx = len(toks)
	}
	for i := 0; i < onIdx; i++ {
		if toks[i].Kind == TokenSymbol && toks[i].Value == "(" {
			return true
		}
	}
	return false
}

// buildAttrACLChange parses the token run of a column-level GRANT/REVOKE —
// `GRANT <priv>(<cols>) ON [TABLE] <names> TO|FROM <roles> [WITH GRANT OPTION]
// [GRANTED BY <role>]` — into an AttrACLChange, sharing the trailing-clause
// scan (scanGrantTrailingClause) with the table/type/database/parameter ACL
// builders. toks is every token after the GRANT/REVOKE keyword with the
// trailing ';' excluded. Returns nil when any required list is empty (an
// unparseable form the caller leaves as a successful no-op). M0119-0004-ACLHEAP.
func buildAttrACLChange(revoke bool, toks []Token) *AttrACLChange {
	onIdx := tokIndexOf(toks, 0, "on")
	if onIdx < 0 {
		return nil
	}
	nameStart := onIdx + 1
	if nameStart < len(toks) && strings.EqualFold(toks[nameStart].Value, "table") {
		nameStart++ // skip the optional TABLE keyword
	}
	sep := "to"
	if revoke {
		sep = "from"
	}
	sepIdx := tokIndexOf(toks, nameStart, sep)
	if sepIdx < 0 || sepIdx < nameStart {
		return nil
	}
	roleStart := sepIdx + 1
	roleEnd, withGrantOption, grantedBy := scanGrantTrailingClause(toks, roleStart)
	aac := &AttrACLChange{
		Revoke:          revoke,
		Privileges:      splitTokColumnPrivileges(toks[:onIdx]),
		TableNames:      splitTokObjectNames(toks[nameStart:sepIdx]),
		Grantees:        splitTokRoles(toks[roleStart:roleEnd]),
		WithGrantOption: withGrantOption,
		GrantedBy:       grantedBy,
	}
	if len(aac.Privileges) == 0 || len(aac.TableNames) == 0 || len(aac.Grantees) == 0 {
		return nil
	}
	return aac
}

// splitTokRunsParenAware splits a token slice on top-level (paren-depth-0) ","
// symbols, dropping empty runs. Unlike splitTokRuns it ignores commas nested
// inside parentheses, so `SELECT (a, b), UPDATE (c)` splits into two privilege
// runs rather than four. M0119-0004-ACLHEAP.
func splitTokRunsParenAware(toks []Token) [][]Token {
	var out [][]Token
	start := 0
	depth := 0
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind == TokenSymbol {
			switch toks[i].Value {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			case ",":
				if depth == 0 {
					if i > start {
						out = append(out, toks[start:i])
					}
					start = i + 1
				}
			}
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// splitTokColumnPrivileges parses the privilege section of a column GRANT
// (everything before ON) into a slice of ColumnPrivilege. Each top-level run is
// `PRIV ( col [, col …] )`; a run with no column list is skipped (a whole-table
// privilege never reaches this column path). M0119-0004-ACLHEAP.
func splitTokColumnPrivileges(toks []Token) []ColumnPrivilege {
	var out []ColumnPrivilege
	for _, run := range splitTokRunsParenAware(toks) {
		lp := -1
		for i, tk := range run {
			if tk.Kind == TokenSymbol && tk.Value == "(" {
				lp = i
				break
			}
		}
		if lp <= 0 {
			continue // no privilege keyword or no column list
		}
		privParts := make([]string, 0, lp)
		for _, tk := range run[:lp] {
			privParts = append(privParts, strings.ToUpper(tk.Value))
		}
		priv := strings.Join(privParts, " ")
		rp := len(run)
		for i := lp + 1; i < len(run); i++ {
			if run[i].Kind == TokenSymbol && run[i].Value == ")" {
				rp = i
				break
			}
		}
		cols := splitTokColumnNames(run[lp+1 : rp])
		if priv == "" || len(cols) == 0 {
			continue
		}
		out = append(out, ColumnPrivilege{Privilege: priv, Columns: cols})
	}
	return out
}

// splitTokColumnNames renders each comma-separated column-name run inside a
// column list as an unquoted identifier. M0119-0004-ACLHEAP.
func splitTokColumnNames(toks []Token) []string {
	var out []string
	for _, run := range splitTokRuns(toks) {
		if len(run) == 0 {
			continue
		}
		name := strings.Trim(run[len(run)-1].Value, `"`)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// tokIndexOf returns the index of the first token at or after `from` whose value
// equals kw (case-insensitive), or -1.
func tokIndexOf(toks []Token, from int, kw string) int {
	for i := from; i < len(toks); i++ {
		if strings.EqualFold(toks[i].Value, kw) {
			return i
		}
	}
	return -1
}

// splitTokRuns splits a token slice on top-level "," symbols, dropping empty runs.
func splitTokRuns(toks []Token) [][]Token {
	var out [][]Token
	start := 0
	for i := 0; i < len(toks); i++ {
		if toks[i].Kind == TokenSymbol && toks[i].Value == "," {
			if i > start {
				out = append(out, toks[start:i])
			}
			start = i + 1
		}
	}
	if start < len(toks) {
		out = append(out, toks[start:])
	}
	return out
}

// splitTokPrivileges renders each comma-separated privilege run as an upper-cased
// keyword string (e.g. "USAGE", "ALL", "ALL PRIVILEGES"); the executor expands
// ALL / ALL PRIVILEGES to the per-class default set.
func splitTokPrivileges(toks []Token) []string {
	var out []string
	for _, run := range splitTokRuns(toks) {
		parts := make([]string, 0, len(run))
		for _, tk := range run {
			parts = append(parts, strings.ToUpper(tk.Value))
		}
		if p := strings.Join(parts, " "); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitTokObjectNames renders each comma-separated object-name run as an
// ObjectName, honouring a schema-qualified `schema.name`.
func splitTokObjectNames(toks []Token) []ObjectName {
	var out []ObjectName
	for _, run := range splitTokRuns(toks) {
		out = append(out, objectNameFromTokens(run))
	}
	return out
}

// objectNameFromTokens builds an ObjectName from a single object-name token run,
// splitting on a "." symbol into schema/name and stripping any quoting.
func objectNameFromTokens(run []Token) ObjectName {
	for i, tk := range run {
		if tk.Kind == TokenSymbol && tk.Value == "." && i > 0 && i+1 < len(run) {
			return ObjectName{
				Schema: strings.Trim(run[i-1].Value, `"`),
				Name:   strings.Trim(run[i+1].Value, `"`),
			}
		}
	}
	if len(run) > 0 {
		return ObjectName{Name: strings.Trim(run[len(run)-1].Value, `"`)}
	}
	return ObjectName{}
}

// splitTokRoles renders each comma-separated role run as a single role string,
// stripping quoting. An unquoted PUBLIC arrives lower-cased from the lexer; the
// executor/catalog folds it to the reserved PUBLIC pseudo-role case-insensitively.
func splitTokRoles(toks []Token) []string {
	var out []string
	for _, run := range splitTokRuns(toks) {
		parts := make([]string, 0, len(run))
		for _, tk := range run {
			parts = append(parts, strings.Trim(tk.Value, `"`))
		}
		if r := strings.Join(parts, ""); r != "" {
			out = append(out, r)
		}
	}
	return out
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
	} else if t.Kind == TokenIdent {
		// PG lexes some words as keywords (e.g. OIDS) and prints them uppercase
		// in "syntax error at or near" messages. Mirror that for known soft keywords.
		switch strings.ToLower(t.Value) {
		case "oids":
			near = strings.ToUpper(t.Value)
		}
	} else if t.Kind == TokenStringLit {
		// PG's scanner_yyerror echoes the raw source text of the offending
		// token (postgres/src/backend/parser/scan.c scanner_yyerror), so a
		// string literal's "near" text is its quoted source form — not the
		// decoded value — with embedded quotes doubled.
		near = "'" + strings.ReplaceAll(t.Value, "'", "''") + "'"
	} else if t.Kind == TokenQuotedIdent {
		near = "\"" + strings.ReplaceAll(t.Value, "\"", "\"\"") + "\""
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

// peekTimeZone reports whether the next two tokens are the idents "time"
// then "zone" — PG's dedicated two-word alias for the "timezone" GUC in
// SET/SHOW/RESET (postgres/src/backend/parser/gram.y:1709 set_rest, :1904
// generic_reset, :1974 VariableShowStmt). TIME and ZONE are both plain
// idents in goopg's lexer (no KwTime/KwZone), so this is a pure two-token
// lookahead — peeking rather than consume-then-unwind so a false match
// (e.g. a GUC literally named "time") never disturbs parser state.
// Does not consume; callers must advance() twice on a true result.
func (p *parser) peekTimeZone() bool {
	return p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "time") &&
		p.peek(1).Kind == TokenIdent && strings.EqualFold(p.peek(1).Value, "zone")
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
		case "start":
			// START [TRANSACTION [...]] is a synonym for BEGIN.
			p.advance() // consume "start"
			_ = p.acceptKeyword(KwTransaction)
			return p.parseBeginStart(t.Pos)
		case "discard":
			// DISCARD { ALL | SEQUENCES | PLANS | TEMP | TEMPORARY }
			p.advance() // consume "discard"
			mode := "ALL"
			switch {
			case p.acceptIdentKeyword("all"):
				mode = "ALL"
			case p.acceptIdentKeyword("sequences"):
				mode = "SEQUENCES"
			case p.acceptIdentKeyword("plans"):
				mode = "PLANS"
			case p.acceptIdentKeyword("temp"), p.acceptIdentKeyword("temporary"):
				mode = "TEMP"
			}
			return &DiscardStmt{pos: t.Pos, Mode: mode}, nil
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
			// Scan the remaining tokens for the ACL object class so the executor can
			// model PG's catalog-tuple-xmax serialization between an ACL change and a
			// concurrent in-place catalog update: `ON DATABASE` (datfrozenxid via
			// VACUUM, design 0118-0098) or `ON [TABLE] <name>` (relhasindex via ALTER
			// TABLE ADD PRIMARY KEY, design 0118-0109). The default GRANT object class
			// is TABLE, so a bare `ON <ident>` is a table grant.
			databaseACL := false
			tableACL := ""
			// typeClass becomes "type"/"domain" when the object class is ON
			// TYPE|DOMAIN. Unlike the virtual classes, pg_type is heap-backed, so the
			// full clause is captured (toks) and parsed into CompatNoopStmt.TypeACL
			// for the executor to apply (M0119-0004-ACLHEAP).
			typeClass := ""
			// parameterACL is true for `ON PARAMETER <names>` — a GUC-level ACL
			// (pg_parameter_acl). Unlike TYPE/DOMAIN/DATABASE, pg_parameter_acl is
			// goopg-virtual-only (no heap relfilenode to re-sync), but it still needs
			// the executor's *Context to reach the ACL store, so the clause is
			// captured here exactly like the heap-backed classes.
			// M0119-0004-ACLHEAP (parameter ACL half).
			parameterACL := false
			// sawOn is true the moment ANY "on" token appears anywhere in the
			// statement -- the discriminator between every privilege-GRANT variant
			// above (which all require an ON <object> clause) and role membership
			// (GRANT <role> TO <role>, which never has one). M0119-0004-ACLHEAP.
			sawOn := false
			var toks []Token
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				if strings.EqualFold(p.cur().Value, "on") {
					sawOn = true
					next := p.peek(1)
					switch {
					case strings.EqualFold(next.Value, "database"):
						databaseACL = true
					case strings.EqualFold(next.Value, "table"):
						// `ON TABLE <name>` — the name follows the TABLE keyword.
						tableACL = grantObjectName(p.peek(2), p.peek(3), p.peek(4))
					case strings.EqualFold(next.Value, "type"):
						// `ON TYPE <names>` — heap-backed pg_type ACL (captured below).
						typeClass = "type"
					case strings.EqualFold(next.Value, "domain"):
						// `ON DOMAIN <names>` — also a pg_type row (typtype='d').
						typeClass = "domain"
					case strings.EqualFold(next.Value, "parameter"):
						// `ON PARAMETER <names>` — GUC-level ACL (captured below).
						parameterACL = true
					case grantNonTableClass(next.Value):
						// SCHEMA/SEQUENCE/FUNCTION/… — not a per-table ACL change.
					default:
						// Default object class is TABLE: `ON <name>`.
						tableACL = grantObjectName(next, p.peek(2), p.peek(3))
					}
				}
				toks = append(toks, p.cur())
				p.advance()
			}
			ns := &CompatNoopStmt{pos: t.Pos, Tag: strings.ToUpper(t.Value), DatabaseACL: databaseACL, TableACL: tableACL}
			if typeClass != "" {
				ns.TypeACL = buildTypeACLChange(strings.EqualFold(t.Value, "revoke"), typeClass == "domain", toks)
			} else if databaseACL {
				// `GRANT/REVOKE … ON DATABASE …` also changes pg_database.datacl,
				// heap-backed exactly like pg_type.typacl — capture the full clause
				// so execDatabaseACLChange can apply it. DatabaseACL (bool) above is
				// left set unconditionally: it independently drives the
				// intra-grant-inplace xmax lock-wait mechanism (design 0118-0098),
				// which is unrelated to whether the clause parsed cleanly here.
				// M0119-0004-ACLHEAP (datacl half).
				ns.DatabaseACLChange = buildDatabaseACLChange(strings.EqualFold(t.Value, "revoke"), toks)
			} else if parameterACL {
				// `GRANT/REVOKE … ON PARAMETER …` — pg_parameter_acl is
				// goopg-virtual-only, so unlike TYPE/DATABASE there is no heap row to
				// re-sync; the executor (execParameterACLChange) applies the change
				// directly to the in-memory ACL store. M0119-0004-ACLHEAP (parameter
				// ACL half).
				ns.ParameterACLChange = buildParameterACLChange(strings.EqualFold(t.Value, "revoke"), toks)
			} else if grantHasColumnList(toks) {
				// A column-level GRANT/REVOKE targets pg_attribute.attacl (heap-backed),
				// not the whole-relation pg_class.relacl. Capture the parsed clause for
				// execAttrACLChange and clear TableACL so the relacl-xmax serialization
				// machinery (intra-grant-inplace) does not fire for a column grant.
				if aac := buildAttrACLChange(strings.EqualFold(t.Value, "revoke"), toks); aac != nil {
					ns.AttrACL = aac
					ns.TableACL = ""
				}
			} else if !sawOn {
				// No `ON <object>` clause anywhere in the statement — the
				// role-membership form (`GRANT <role> TO <role>`/`REVOKE ...
				// FROM <role>`), a distinct capability from every
				// object-privilege GRANT/REVOKE above. Unparseable input (e.g.
				// PG's `GRANT <role> TO PUBLIC`, always rejected by PG itself)
				// falls back to the pre-existing no-op. M0119-0004-ACLHEAP.
				ns.RoleMembership = buildRoleMembershipChange(strings.EqualFold(t.Value, "revoke"), toks)
			}
			return ns, nil
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
			// SECURITY LABEL [FOR provider] ON <object> IS 'text'|NULL. goopg
			// loads no security-label providers (no C extension mechanism to
			// load one), so per PG's own ExecSecLabelStmt (seclabel.c) — which
			// checks the provider list BEFORE resolving the target object —
			// this must always raise "security label provider ... is not
			// loaded" (FOR given) or "no security label providers have been
			// loaded" (bare form) rather than silently succeeding. The object
			// clause is parsed-and-discarded: real PG never reaches it either.
			// DU-002 slice 438.
			p.advance() // consume SECURITY
			if p.cur().Kind == TokenIdent && strings.EqualFold(p.cur().Value, "label") {
				p.advance() // consume LABEL
			}
			provider := ""
			if p.acceptKeyword(KwFor) {
				if pt := p.cur(); pt.Kind == TokenStringLit {
					provider = pt.Value
					p.advance()
				} else if id, err := p.parseIdent(); err == nil {
					provider = identText(id)
				}
			}
			for p.cur().Kind != TokenEOF {
				if p.cur().Kind == TokenSymbol && p.cur().Value == ";" {
					break
				}
				p.advance()
			}
			return &CompatNoopStmt{pos: t.Pos, Tag: "SECURITY LABEL", SecurityLabelProvider: provider}, nil
		case "lock":
			// LOCK [TABLE] [ONLY] rel [, ...] [IN lock_mode MODE] [NOWAIT].
			// M0097: parse into LockTableStmt so the executor can track locks in pg_locks.
			return p.parseLockTable(t.Pos)
		case "listen":
			return p.parseListen()
		case "notify":
			return p.parseNotify()
		case "unlisten":
			return p.parseUnlisten()
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
		// FORMAT requires a value: TEXT, XML, JSON, or YAML.
		valTok := p.cur()
		if valTok.Kind != TokenIdent && valTok.Kind != TokenKeyword && valTok.Kind != TokenStringLit && valTok.Kind != TokenQuotedIdent {
			return &SyntaxError{Pos: valTok.Pos, Message: "FORMAT requires a value (TEXT, XML, JSON, or YAML)"}
		}
		v := strings.ToLower(valTok.Value)
		p.advance()
		switch v {
		case "text":
			opts.Format = ExplainFormatText
		case "json":
			opts.Format = ExplainFormatJSON
		case "xml":
			opts.Format = ExplainFormatXML
		case "yaml":
			opts.Format = ExplainFormatYAML
		default:
			return &SyntaxError{Pos: valTok.Pos, Message: fmt.Sprintf("unsupported FORMAT %q (TEXT, XML, JSON, or YAML only)", valTok.Value)}
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
	case "generic_plan":
		opts.GenericPlan = val
		opts.Set.GenericPlan = true
	case "wal":
		opts.Wal = val
		opts.Set.Wal = true
	case "memory":
		opts.Memory = val
		opts.Set.Memory = true
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
//	READ {ONLY | WRITE}          — recorded in BeginStmt.ReadOnly
//	[NOT] DEFERRABLE             — recorded in BeginStmt.Deferrable; for a
//	                               SERIALIZABLE READ ONLY xact it requests the
//	                               GetSafeSnapshot deferral (M0118-0001)
//
// Modes may appear in any order and repeat (last ISOLATION LEVEL wins).
func (p *parser) parseBegin() (Stmt, error) {
	t := p.advance() // BEGIN
	s := &BeginStmt{pos: t.Pos}
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return p.parseBeginModes(s)
}

// parseBeginStart handles START TRANSACTION [...] — a synonym for BEGIN.
func (p *parser) parseBeginStart(pos int) (Stmt, error) {
	// "start" ident and optional TRANSACTION keyword already consumed.
	s := &BeginStmt{pos: pos}
	return p.parseBeginModes(s)
}

func (p *parser) parseBeginModes(s *BeginStmt) (Stmt, error) {
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
			if p.acceptIdentKeyword("only") {
				s.ReadOnly = true
			} else {
				_ = p.acceptIdentKeyword("write")
				s.ReadOnly = false
			}
		case p.acceptKeyword(KwNot):
			// DEFERRABLE is an unreserved keyword token (KwDeferrable), not an
			// identifier, so it must be matched with acceptKeyword — using
			// acceptIdentKeyword silently failed to consume it (the cause of the
			// "syntax error ... got deferrable" on BEGIN ... READ ONLY
			// DEFERRABLE). M0118-0001.
			if p.acceptKeyword(KwDeferrable) {
				s.Deferrable = false
			}
		case p.acceptKeyword(KwDeferrable):
			s.Deferrable = true
		default:
			goto done
		}
		// Transaction modes may be comma-separated, e.g.
		// `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` (receipt-report.spec).
		_ = p.acceptSymbol(",")
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
	// COMMIT PREPARED 'gid' — two-phase-commit commit phase. "PREPARED" is an
	// unreserved word lexed as an identifier. M0118-0009 (prepared-transactions).
	if p.peekIdentText("prepared") {
		p.advance() // PREPARED
		gid, gerr := p.parseStrLit()
		if gerr != nil {
			return nil, gerr
		}
		return &CommitPreparedStmt{pos: t.Pos, Gid: gid}, nil
	}
	_ = p.acceptKeyword(KwWork) || p.acceptKeyword(KwTransaction)
	return &CommitStmt{pos: t.Pos}, nil
}

// peekIdentText reports whether the current token is an identifier matching the
// given (lowercase) text. The lexer lowercases unquoted identifiers, so callers
// pass a lowercase literal. M0118-0009.
func (p *parser) peekIdentText(text string) bool {
	t := p.cur()
	return t.Kind == TokenIdent && t.Value == text
}

// parseRollback: ROLLBACK [WORK | TRANSACTION] | ROLLBACK TO [SAVEPOINT] name | ABORT [WORK | TRANSACTION]
func (p *parser) parseRollback() (Stmt, error) {
	t := p.advance()
	// ROLLBACK PREPARED 'gid' — two-phase-commit abort phase. M0118-0009.
	if p.peekIdentText("prepared") {
		p.advance() // PREPARED
		gid, gerr := p.parseStrLit()
		if gerr != nil {
			return nil, gerr
		}
		return &RollbackPreparedStmt{pos: t.Pos, Gid: gid}, nil
	}
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

// parseChannelName reads a LISTEN/NOTIFY/UNLISTEN channel identifier. An
// unquoted identifier is matched case-folded (the lexer already lowercases
// TokenIdent / TokenKeyword); a double-quoted identifier preserves case. This
// mirrors PostgreSQL, which treats the channel as a regular identifier.
func (p *parser) parseChannelName() (string, error) {
	t := p.cur()
	if t.Kind != TokenIdent && t.Kind != TokenKeyword && t.Kind != TokenQuotedIdent {
		return "", p.errAtCur("expected channel name")
	}
	p.advance()
	return t.Value, nil
}

// parseListen: LISTEN channel. M0118-0009 (async-notify).
func (p *parser) parseListen() (Stmt, error) {
	t := p.advance() // consume "listen"
	ch, err := p.parseChannelName()
	if err != nil {
		return nil, err
	}
	return &ListenStmt{pos: t.Pos, Channel: ch}, nil
}

// parseNotify: NOTIFY channel [, 'payload']. M0118-0009 (async-notify).
func (p *parser) parseNotify() (Stmt, error) {
	t := p.advance() // consume "notify"
	ch, err := p.parseChannelName()
	if err != nil {
		return nil, err
	}
	stmt := &NotifyStmt{pos: t.Pos, Channel: ch}
	if p.cur().Kind == TokenSymbol && p.cur().Value == "," {
		p.advance() // consume ,
		payload, perr := p.parseStrLit()
		if perr != nil {
			return nil, perr
		}
		stmt.Payload = payload
		stmt.HasPayload = true
	}
	return stmt, nil
}

// parseUnlisten: UNLISTEN { channel | * }. M0118-0009 (async-notify).
func (p *parser) parseUnlisten() (Stmt, error) {
	t := p.advance() // consume "unlisten"
	if p.cur().Kind == TokenSymbol && p.cur().Value == "*" {
		p.advance()
		return &UnlistenStmt{pos: t.Pos, All: true}, nil
	}
	ch, err := p.parseChannelName()
	if err != nil {
		return nil, err
	}
	return &UnlistenStmt{pos: t.Pos, Channel: ch}, nil
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
	tgts, cols, err := p.parseVacuumTargets()
	if err != nil {
		return nil, err
	}
	v.Targets = tgts
	v.TargetCols = cols
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
		case p.acceptKeyword(KwTruncate) || p.acceptIdentKeyword("truncate"):
			// TRUNCATE lexes as the unreserved keyword KwTruncate (it also
			// leads the TRUNCATE TABLE statement), so acceptIdentKeyword alone
			// never matches inside the VACUUM option list — accept the keyword
			// token too. goopg's VACUUM (vacuumCore) never physically truncates
			// trailing empty pages, so NoTruncate is honoured trivially; we
			// still record it for parity and future relation-truncation work.
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

// parseSignedIntLit parses an optional leading '-' (a separate unary-minus
// operator token, since integer literals never carry a sign in the lexer)
// followed by an integer literal, e.g. for `FETCH ABSOLUTE -1`. M0134-0056.
func (p *parser) parseSignedIntLit() (int64, error) {
	neg := false
	if p.cur().Kind == TokenOperator && p.cur().Value == "-" {
		neg = true
		p.advance()
	}
	n, err := p.parseIntLit()
	if err != nil {
		return 0, err
	}
	if neg {
		n = -n
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
	tgts, cols, err := p.parseVacuumTargets()
	if err != nil {
		return nil, err
	}
	a.Targets = tgts
	a.TargetCols = cols
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

// parseVacuumTargets parses the VACUUM/ANALYZE relation list, where each
// relation may carry an optional parenthesised column list (PG grammar
// vacuum_relation: relation_expr opt_name_list, gram.y:12021-12026). Returns
// parallel slices: cols[i] pairs with names[i], and is nil when names[i] has no
// column list. ANALYZE tab (a, b) and VACUUM ANALYZE tab (a) both go through
// here.
func (p *parser) parseVacuumTargets() ([]ObjectName, [][]string, error) {
	var names []ObjectName
	var cols [][]string
	for {
		name, err := p.parseObjectName()
		if err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		var colList []string
		if p.acceptSymbol("(") {
			for {
				ident, err := p.parseIdent()
				if err != nil {
					return nil, nil, err
				}
				colList = append(colList, identText(ident))
				if !p.acceptSymbol(",") {
					break
				}
			}
			if !p.acceptSymbol(")") {
				return nil, nil, p.errAtCur("expected ')'")
			}
		}
		cols = append(cols, colList)
		if !p.acceptSymbol(",") {
			break
		}
	}
	return names, cols, nil
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

// parseOperatorRefName parses an operator reference as used in a CREATE
// OPERATOR statement's COMMUTATOR = / NEGATOR = clause. PG's own pg_dump
// always emits the schema-qualified `OPERATOR(schema.op)` form (dumpOpr's
// getFormattedOperatorName, pg_dump.c), but hand-written SQL may also use a
// bare (optionally schema-qualified) operator symbol, so both are accepted
// here — the latter simply falls through to parseOperatorName. DU-002
// slice 407.
func (p *parser) parseOperatorRefName() (ObjectName, error) {
	if t := p.cur(); t.Kind == TokenIdent && strings.EqualFold(t.Value, "operator") &&
		p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "(" {
		p.advance() // OPERATOR
		p.advance() // (
		schema := ""
		if p.cur().Kind == TokenIdent && p.peek(1).Kind == TokenSymbol && p.peek(1).Value == "." {
			schema = identText(p.cur())
			p.advance() // schema name
			p.advance() // .
		}
		pos := p.cur().Pos
		opName := ""
		for p.cur().Kind == TokenOperator {
			opName += p.cur().Value
			p.advance()
		}
		if opName == "" && p.cur().Kind != TokenSymbol {
			opName = p.cur().Value
			p.advance()
		}
		if opName == "" || !p.acceptSymbol(")") {
			return ObjectName{}, p.errSyntaxAtCur()
		}
		return ObjectName{pos: pos, Schema: schema, Name: opName}, nil
	}
	return p.parseOperatorName()
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
	// SHOW TIME ZONE — M0134-0028a.
	if p.peekTimeZone() {
		p.advance() // consume "time"
		p.advance() // consume "zone"
		return &ShowStmt{pos: t.Pos, Name: "timezone"}, nil
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
		// SET LOCAL SESSION AUTHORIZATION rolename — accept as session_authorization GUC.
		if p.acceptKeyword(KwSession) {
			if p.acceptIdentKeyword("authorization") {
				s.Name = "session_authorization"
				// NOTE: no TO/= separator here — unlike SET ROLE, SESSION
				// AUTHORIZATION has NO generic_set grammar upstream
				// (gram.y:1764/:1774 dedicated productions accept only a bare
				// rolename or DEFAULT), so `SET [LOCAL] SESSION
				// AUTHORIZATION TO x` / `= x` is a 42601 syntax error in PG
				// 18.3 (oracle-verified). Accepting it here would diverge.
				// M0134-0155.
				if p.acceptKeyword(KwDefault) {
					s.Default = true
				} else {
					roleTok, _ := p.parseIdent()
					s.Value = roleTok.Value
				}
				return s, nil
			}
			// SET LOCAL SESSION TRANSACTION ... — treat SESSION as having been consumed.
		}
	} else if p.acceptKeyword(KwSession) {
		// SET SESSION AUTHORIZATION name — store the role name in s.Value so
		// the executor can update the session's non-superuser role tracking.
		// "authorization" is not a keyword in goopg so it parses as an ident.
		if p.acceptIdentKeyword("authorization") {
			s.Name = "session_authorization"
			// No TO/= separator — see the SET LOCAL SESSION AUTHORIZATION
			// arm above (no generic_set grammar upstream; oracle-verified
			// 42601). M0134-0155.
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
	// SET ROLE rolename — capture the role name (or DEFAULT) in s.Value/
	// s.Default so the executor can update the session's non-superuser role
	// tracking (mirrors the SET SESSION AUTHORIZATION handling above).
	// M0097-0071 originally discarded the role name entirely; M0119-0004
	// restores it since goopg does enforce SET-ROLE-scoped privilege checks
	// (e.g. TRUNCATE, M0118-0008) via connTx.NonSuperuserRole.
	if p.cur().Kind == TokenIdent && strings.ToLower(p.cur().Value) == "role" {
		p.advance() // consume "role"
		s.Name = "role"
		// Optional TO/= separator — `SET ROLE` is dispatched via PG's
		// generic_set grammar (`var_name TO var_list | var_name '=' var_list`,
		// gram.y:1656-1693) same as SESSION AUTHORIZATION above, so `SET ROLE
		// TO x` / `SET ROLE = x` must parse identically to `SET ROLE x`.
		// Previously unhandled: `SET ROLE TO x` 42601'd outright via this
		// parser path, and the sibling string-matching fast paths
		// (server/query.go, extended.go) silently stored the literal garbage
		// role name "TO x" instead of erroring or parsing correctly — see
		// stripSetToOrEquals's doc comment for the corruption this caused.
		// M0134-0155.
		p.acceptKeyword(KwTo)
		if p.cur().Kind == TokenOperator && p.cur().Value == "=" {
			p.advance()
		}
		if p.acceptKeyword(KwDefault) {
			s.Default = true
		} else {
			roleTok, _ := p.parseIdent()
			s.Value = roleTok.Value
		}
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
			// Transaction modes may be comma-separated, e.g. pg_dump's
			// `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY`.
			_ = p.acceptSymbol(",")
		}
	setTxDone:
		return st, nil
	}
	// SET CONSTRAINTS { ALL | name [, ...] } { DEFERRED | IMMEDIATE }.
	// "constraints", "deferred" and "immediate" are unreserved keyword idents
	// (matched with acceptIdentKeyword, the same way the INITIALLY DEFERRED
	// constraint trailer is parsed); ALL is the reserved KwAll. 0119-0004.
	if p.acceptIdentKeyword("constraints") {
		sc := &SetConstraintsStmt{pos: t.Pos}
		if p.acceptKeyword(KwAll) {
			sc.All = true
		} else {
			for {
				nm, err := p.parseQualifiedConstraintName()
				if err != nil {
					return nil, err
				}
				sc.Names = append(sc.Names, nm)
				if !p.acceptSymbol(",") {
					break
				}
			}
		}
		switch {
		case p.acceptIdentKeyword("deferred"):
			sc.Deferred = true
		case p.acceptIdentKeyword("immediate"):
			sc.Deferred = false
		default:
			return nil, p.errAtCur("expected DEFERRED or IMMEDIATE after SET CONSTRAINTS")
		}
		return sc, nil
	}
	// SET TIME ZONE zone_value — PG's dedicated two-word alias for the
	// "timezone" GUC (gram.y:1709 set_rest). Unlike the generic
	// `SET name = value` path below, TIME ZONE has no '=' / TO separator
	// before the value, so the DEFAULT/parseSetValue tail is duplicated
	// here rather than falling through (LOCAL — PG's "reset to server
	// default zone" spelling — already tokenizes as the KwLocal keyword,
	// which parseSetValueAtoms accepts as a plain value atom; no extra
	// handling needed). M0134-0028a.
	if p.peekTimeZone() {
		p.advance() // consume "time"
		p.advance() // consume "zone"
		s.Name = "timezone"
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
//	  [CONCURRENTLY] [IF EXISTS] name
//
// CONCURRENTLY is accepted in either the legacy pre-type position or the
// modern post-type position (`REINDEX TABLE CONCURRENTLY name`).
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

	// CONCURRENTLY may also appear AFTER the object-type keyword and before the
	// name — this is the modern PostgreSQL position, e.g.
	// `REINDEX TABLE CONCURRENTLY name` (the pre-type position handled above is
	// the legacy form). Accept either spelling.
	if !r.Concurrently && p.acceptIdentKeyword("concurrently") {
		r.Concurrently = true
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
	// PREPARE TRANSACTION 'gid' — two-phase-commit prepare phase. Shares the
	// PREPARE keyword with prepared statements; the TRANSACTION keyword
	// disambiguates. M0118-0009 (prepared-transactions).
	if p.cur().Kind == TokenKeyword && p.cur().Keyword == KwTransaction {
		p.advance() // TRANSACTION
		gid, gerr := p.parseStrLit()
		if gerr != nil {
			return nil, gerr
		}
		return &PrepareTransactionStmt{pos: t.Pos, Gid: gid}, nil
	}
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
				// parseTypeNameAfterCast fails WITHOUT consuming the
				// offending token; skip it or this loop never advances
				// (observed: PREPARE f(regclass[]) spun here allocating
				// "unknown" entries until OOM).
				if !(p.cur().Kind == TokenSymbol && (p.cur().Value == ")" || p.cur().Value == ",")) {
					p.advance()
				}
			} else {
				tn := on.Name
				// Array suffix: type[] (or type[][]…) — same handling as
				// the CAST type-name path (select.go).
				for p.cur().Kind == TokenSymbol && p.cur().Value == "[" {
					p.advance()
					if p.cur().Kind == TokenSymbol && p.cur().Value == "]" {
						p.advance()
					}
					tn += "[]"
				}
				paramTypes = append(paramTypes, tn)
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
	// RESET TIME ZONE — M0134-0028a.
	if p.peekTimeZone() {
		p.advance() // consume "time"
		p.advance() // consume "zone"
		return &ResetStmt{pos: t.Pos, Name: "timezone"}, nil
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

// parseQualifiedConstraintName parses one constraint name for SET CONSTRAINTS.
// PG accepts a schema-qualified name (schema.constraint); goopg matches the
// queued deferred checks by the bare constraint name, so any leading schema
// qualifier is parsed and discarded (the final component is returned). 0119-0004.
func (p *parser) parseQualifiedConstraintName() (string, error) {
	tok, err := p.parseIdent()
	if err != nil {
		return "", err
	}
	name := identText(tok)
	for p.acceptSymbol(".") {
		next, err := p.parseIdent()
		if err != nil {
			return "", err
		}
		name = identText(next)
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
		case TokenIntLit, TokenNumericLit:
			// Accept integer and floating-point GUC values, e.g.
			// `SET seq_page_cost = 0.1`. Real-typed GUCs (cost params,
			// cpu_*_cost) are set with fractional literals upstream.
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
				if n.Kind != TokenIntLit && n.Kind != TokenNumericLit {
					return nil, p.errAtCur("expected number after '-'")
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

	// optional WITH/WITHOUT HOLD. WITH is a reserved keyword (lexed as
	// TokenKeyword/KwWith, never TokenIdent) so it must be matched via
	// acceptKeyword, not acceptIdentKeyword — M0134-0056.
	if p.acceptKeyword(KwWith) || p.acceptIdentKeyword("without") {
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
		// Optional leading '-' (unary minus token) before the literal, e.g.
		// `FETCH ABSOLUTE -1`. M0134-0056.
		if p.cur().Kind == TokenIntLit || (p.cur().Kind == TokenOperator && p.cur().Value == "-") {
			var err error
			count, err = p.parseSignedIntLit()
			if err != nil {
				return nil, err
			}
		}
	case p.acceptIdentKeyword("relative"):
		// Optional leading '-' (unary minus token) before the literal, e.g.
		// `FETCH RELATIVE -1`. M0134-0056.
		if p.cur().Kind == TokenIntLit || (p.cur().Kind == TokenOperator && p.cur().Value == "-") {
			var err error
			count, err = p.parseSignedIntLit()
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
		// COLUMN [schema.]table.col. parseObjectName consumes up to two dotted
		// parts; a trailing ".col" distinguishes the schema-qualified 3-part form
		// (schema.table.col — the canonical form pg_dump itself emits) from the
		// bare 2-part form (table.col). Both must round-trip dump→restore.
		cs.ObjKind = "column"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		if p.acceptSymbol(".") {
			// 3-part schema.table.col: parseObjectName read schema.table; the
			// remaining identifier is the column.
			col, err := p.parseIdent()
			if err != nil {
				return nil, true, err
			}
			cs.ObjName = ObjectName{Schema: name.Schema, Name: name.Name}
			cs.SubName = identText(col)
		} else {
			// 2-part table.col: parseObjectName read it as Schema=table, Name=col.
			cs.ObjName = ObjectName{Name: name.Schema}
			cs.SubName = name.Name
		}
	case p.acceptKeyword(KwConstraint):
		cs.ObjKind = "constraint"
		// constraint name
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, true, p.errAtCur("expected constraint name")
		}
		cs.SubName = tok.Value
		p.advance()
		// ON table (or ON DOMAIN domain — mirrors the top-level COMMENT ON
		// DOMAIN case above: an optional "domain" ident-keyword before the
		// object name distinguishes a domain constraint from a table one, so
		// the executor can dispatch — M0134-0005ai).
		if !p.acceptKeyword(KwOn) {
			return nil, true, p.errAtCur("expected ON after constraint name in COMMENT ON CONSTRAINT")
		}
		if p.acceptIdentKeyword("domain") {
			cs.ObjKind = "domain constraint"
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptKeyword(KwTrigger):
		// COMMENT ON TRIGGER <name> ON [schema.]table IS '...'. Triggers live in
		// pg_trigger (classoid 2620); pg_dump's dumpTrigger re-emits
		// `COMMENT ON TRIGGER <name> ON <table> IS '...'`. The shape mirrors
		// COMMENT ON CONSTRAINT — a bare trigger name followed by ON <table>.
		// DU-002 slice 370.
		cs.ObjKind = "trigger"
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, true, p.errAtCur("expected trigger name")
		}
		cs.SubName = tok.Value
		p.advance()
		if !p.acceptKeyword(KwOn) {
			return nil, true, p.errAtCur("expected ON after trigger name in COMMENT ON TRIGGER")
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("policy"):
		// COMMENT ON POLICY <name> ON [schema.]table IS '...'. Policies live in
		// pg_policy (classoid 3256); pg_dump's dumpPolicy re-emits
		// `COMMENT ON POLICY <name> ON <table> IS '...'`. The shape mirrors
		// COMMENT ON TRIGGER/CONSTRAINT — a bare policy name followed by ON
		// <table>. DU-002 slice 371.
		cs.ObjKind = "policy"
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, true, p.errAtCur("expected policy name")
		}
		cs.SubName = tok.Value
		p.advance()
		if !p.acceptKeyword(KwOn) {
			return nil, true, p.errAtCur("expected ON after policy name in COMMENT ON POLICY")
		}
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("rule"):
		// COMMENT ON RULE <name> ON [schema.]table IS '...'. Rules live in
		// pg_rewrite (classoid 2618); pg_dump's dumpRule re-emits
		// `COMMENT ON RULE <name> ON <table> IS '...'`. The shape mirrors
		// COMMENT ON TRIGGER/POLICY — a bare rule name followed by ON <table>.
		// DU-002 slice 372.
		cs.ObjKind = "rule"
		tok := p.cur()
		if tok.Kind != TokenIdent && tok.Kind != TokenKeyword {
			return nil, true, p.errAtCur("expected rule name")
		}
		cs.SubName = tok.Value
		p.advance()
		if !p.acceptKeyword(KwOn) {
			return nil, true, p.errAtCur("expected ON after rule name in COMMENT ON RULE")
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
	case p.acceptKeyword(KwView):
		// COMMENT ON VIEW [schema.]name IS '...'. Views are pg_class relations;
		// pg_dump re-emits `COMMENT ON VIEW …`. DU-002 slice 145.
		cs.ObjKind = "view"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("sequence"):
		// COMMENT ON SEQUENCE [schema.]name IS '...'. Sequences are pg_class
		// relations (relkind='S'); pg_dump re-emits `COMMENT ON SEQUENCE …`.
		// DU-002 slice 145.
		cs.ObjKind = "sequence"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("schema"):
		// COMMENT ON SCHEMA name IS '...'. Schemas live in pg_namespace; pg_dump
		// re-emits `COMMENT ON SCHEMA …`. DU-002 slice 145.
		cs.ObjKind = "schema"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("materialized"):
		// COMMENT ON MATERIALIZED VIEW [schema.]name IS '...'. Materialized views
		// are pg_class relations (relkind='m'); pg_dump re-emits
		// `COMMENT ON MATERIALIZED VIEW …`. "materialized"/"view" are not lexer
		// keywords, so accept either spelling for VIEW. DU-002 slice 146.
		_ = p.acceptKeyword(KwView) || p.acceptIdentKeyword("view")
		cs.ObjKind = "materialized view"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("type"):
		// COMMENT ON TYPE [schema.]name IS '...'. User-defined types live in
		// pg_type; pg_dump re-emits `COMMENT ON TYPE …`. DU-002 slice 146.
		cs.ObjKind = "type"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("domain"):
		// COMMENT ON DOMAIN [schema.]name IS '...'. Domains live in pg_type
		// (typtype='d'); pg_dump re-emits `COMMENT ON DOMAIN …`. DU-002 slice 146.
		cs.ObjKind = "domain"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("collation"):
		// COMMENT ON COLLATION [schema.]name IS '...'. Collations live in
		// pg_collation (classoid 3456); pg_dump's dumpCollation re-emits
		// `COMMENT ON COLLATION <schema>.<name> IS '...'` keyed on the collation's
		// catalogId (tableoid=pg_collation=3456) and objsubid 0. A user collation
		// lives in a user schema, so parse a schema-qualifiable object name.
		// DU-002 slice 390.
		cs.ObjKind = "collation"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("access"):
		// COMMENT ON ACCESS METHOD <name> IS '...'. Access methods live in pg_am
		// (classoid 2601); pg_dump's dumpAccessMethod re-emits `COMMENT ON ACCESS
		// METHOD <name> IS '...'` (dumpComment(fout, "ACCESS METHOD", qamname, ...)
		// — pg_dump.c). "ACCESS" is unreserved and unregistered as a keyword
		// (matched via the ident path, same as CREATE ACCESS METHOD); an access
		// method is a top-level object (no schema), so parse a bare name.
		// DU-002 slice 434.
		if !p.acceptIdentKeyword("method") {
			return nil, true, p.errAtCur("expected METHOD after ACCESS in COMMENT ON")
		}
		cs.ObjKind = "access method"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("server"):
		// COMMENT ON SERVER <name> IS '...'. Foreign servers live in
		// pg_foreign_server (classoid 1417); pg_dump's dumpForeignServer re-emits
		// `COMMENT ON SERVER <name> IS '...'`. A foreign server is a top-level
		// object (no schema), so parse a bare name. DU-002 slice 386.
		cs.ObjKind = "server"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptKeyword(KwForeign):
		// COMMENT ON FOREIGN TABLE [schema.]name IS '...' or COMMENT ON FOREIGN
		// DATA WRAPPER <name> IS '...'. Foreign tables are pg_class relations
		// (relkind='f') sharing goopg's ordinary table registry (CREATE FOREIGN
		// TABLE, DU-002 slice 417/418); pg_dump's dumpTableSchema re-emits
		// `COMMENT ON FOREIGN TABLE <name> IS '...'` for a commented foreign
		// table, keyed on the same classoid=pg_class (1259) lookup as COMMENT
		// ON TABLE. Without this branch, "FOREIGN" only recognized "FOREIGN
		// DATA WRAPPER" and any other continuation (including TABLE) was a
		// hard parse error. FOREIGN is a reserved keyword (KwForeign); TABLE,
		// DATA, and WRAPPER disambiguate which object kind follows. DU-002
		// slice 435 (TABLE arm); slice 387 (DATA WRAPPER arm, pre-existing).
		if p.acceptKeyword(KwTable) {
			cs.ObjKind = "foreign table"
			name, err := p.parseObjectName()
			if err != nil {
				return nil, true, err
			}
			cs.ObjName = name
			break
		}
		if !p.acceptIdentKeyword("data") || !p.acceptIdentKeyword("wrapper") {
			return nil, true, p.errAtCur("expected TABLE or DATA WRAPPER after FOREIGN in COMMENT ON")
		}
		cs.ObjKind = "foreign data wrapper"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("extension"):
		// COMMENT ON EXTENSION <name> IS '...'. Extensions live in pg_extension
		// (classoid 3079); pg_dump's dumpExtension re-emits `COMMENT ON EXTENSION
		// <name> IS '...'`. EXTENSION is an unreserved ident-keyword. An extension
		// is a top-level object (no schema), so parse a bare name. DU-002 slice 388.
		cs.ObjKind = "extension"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
	case p.acceptIdentKeyword("cast"):
		// COMMENT ON CAST (source AS target) IS '...'. Casts live in pg_cast
		// (classoid 2605); pg_dump's dumpCast keys the comment lookup on the cast's
		// catalogId (tableoid=pg_cast=2605) and objsubid 0, then re-emits
		// `COMMENT ON CAST (<source> AS <target>) IS '...'`. The identity is the
		// (source, target) type pair, not a name — parse `(src AS tgt)` with the
		// same helper CREATE/DROP CAST uses. "cast" is an unreserved ident-keyword.
		// DU-002 slice 396 (follows the CREATE CAST object family, slice 395).
		cs.ObjKind = "cast"
		if !p.acceptSymbol("(") {
			return nil, true, p.errAtCur("expected ( after CAST in COMMENT ON")
		}
		cs.CastSource = p.parseCastTypeName()
		_ = p.acceptKeyword(KwAs)
		cs.CastTarget = p.parseCastTypeName()
		if !p.acceptSymbol(")") {
			return nil, true, p.errAtCur("expected ) after cast types in COMMENT ON CAST")
		}
	case p.acceptKeyword(KwFunction):
		// COMMENT ON FUNCTION [schema.]name([argtypes]) IS '...'. Functions live
		// in pg_proc (classoid 1255); pg_dump re-emits `COMMENT ON FUNCTION …`
		// with the full identity-argument signature. The argument list is
		// required to disambiguate overloads — parse it with the same helper
		// DROP FUNCTION uses. DU-002 slice 147.
		cs.ObjKind = "function"
		name, err := p.parseObjectName()
		if err != nil {
			return nil, true, err
		}
		cs.ObjName = name
		args, err := p.parseFunctionArgList()
		if err != nil {
			return nil, true, err
		}
		cs.Args = args
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
