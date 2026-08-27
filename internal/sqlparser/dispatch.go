package sqlparser

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// Batch-level routing (docs/design/not_ralph/03-strangler-migration.md §2).
//
// Semantics pinned at P0.8:
//
//   - The WHOLE batch routes to the new parser only when EVERY statement
//     fragment's leading terminal belongs to a routed class. Mixed batches
//     (one routed + one unrouted statement) stay on the legacy parser for
//     now — per-fragment mixing would require legacy to parse from token
//     slices, which it cannot do yet. Revisit per wave; the wrapper
//     invariant below is what later waves must preserve.
//   - Deterministic: no try-new-fall-back. A class is added to routedStmts
//     only after its wave gate is green.

// SplitStatements splits a token slice into per-statement fragments at
// top-level ';' symbols. Dollar-quoted bodies are opaque single tokens by
// the time we see them (legacy lexer, lexer.go:430-464), so every ';' symbol
// token is a genuine separator; a ';' that would land inside parens belongs
// to invalid SQL and both parsers reject it either way.
func SplitStatements(toks []parser.Token) [][]parser.Token {
	var out [][]parser.Token
	start := 0
	for i, t := range toks {
		if t.Kind == parser.TokenSymbol && t.Value == ";" && i > start {
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	if start < len(toks) {
		rest := toks[start:]
		// The legacy lexer appends an explicit TokenEOF; a trailing
		// semicolon leaves just that sentinel behind — not a fragment.
		for i := range rest {
			if rest[i].Kind != parser.TokenEOF {
				out = append(out, rest)
				break
			}
		}
	}
	return out
}

// routedStmts maps leading-terminal KEYS to their routing decision. Keys are
// lowercase leading words — keywords arrive via TokenKeyword/TokenIdent and
// ident-led statements (start/discard/fetch/move/close/refresh/grant/revoke/
// listen/notify/unlisten/lock) via TokenIdent, matched case-insensitively.
//
// select: P2-F flip. Grammar coverage floor enforced by
// TestTPCHGrammarCoverage (19/22); typed-literal normalization handles
// date/time/timestamp/interval SCONST forms.
var routedStmts = map[string]bool{
	"explain": true, // gated further by explainInnerRouted
	"select": true,
	"insert": true, // P3.1
	"update": true, // P3.2
	"delete": true, // P3.3
	"truncate": true, // P4.4
	// P6.1 v0 transaction keywords:
	"begin": true,
	"start": true,
	"commit": true,
	"rollback": true,
	"abort": true,
	"end": true,
	"set": true,
	"show": true,
	"reset": true,
	// P5.1 — REFRESH leads no other statement in PostgreSQL, so the single
	// refresh_matview_stmt alternative covers the whole keyword.
	"refresh": true,
	// P5.2 — CALL leads no other statement.
	"call": true,
	// P5.3 utility statements. Each of these leads exactly one statement
	// class, so no second-keyword gate is needed.
	"savepoint": true,
	"release": true,
	"checkpoint": true,
	"discard": true,
	"deallocate": true,
	"prepare": true,
	"execute": true,
	"close": true,
	"declare": true,
	"fetch": true,
	"move": true,
	"analyze": true,
	"analyse": true,
	"vacuum": true,
	"reindex": true,
	"cluster": true,
	"lock": true,
	// P5.4
	"merge": true,
	// P5.5 — DO leads no other statement.
	"do": true,
	// P5.6
	"comment": true,
	// P5.8 — the two SELECT shorthands. Both already parsed identically; only
	// the routing entry was missing.
	"table": true,
	"values": true,
	// "create table": routed via createClassRouted two-keyword check (P4.1)
}

// routeBatch reports whether the whole batch can go to the new parser, plus
// the parsed statements when it can.
func routeBatch(src string, toks []parser.Token) ([]parser.Stmt, bool, error) {
	frags := SplitStatements(toks)
	if len(frags) == 0 {
		return nil, false, nil // let legacy decide what an empty batch means
	}
	for _, f := range frags {
		if !fragmentRouted(f) {
			return nil, false, nil
		}
	}
	var all []parser.Stmt
	for _, f := range frags {
		stmts, err := ParseOneSrc(src, f)
		if err != nil {
			return nil, true, err
		}
		all = append(all, stmts...)
	}
	return all, true, nil
}

// fragmentRouted inspects the fragment's first meaningful token.
func fragmentRouted(frag []parser.Token) bool {
	t := firstMeaningful(frag)
	if t == nil {
		return false // empty fragments stay legacy
	}
	switch t.Kind {
	case parser.TokenKeyword, parser.TokenIdent:
		key := strings.ToLower(t.Value)
		if key == "with" {
			return withFollowerRouted(frag)
		}
		if key == "explain" {
			return explainInnerRouted(frag)
		}
		if key == "comment" {
			return commentRouted(frag)
		}
		if key == "create" || key == "drop" || key == "alter" {
			// These lead many DDL classes; route only ported pairs by
			// inspecting the SECOND meaningful keyword. "alter" was missing
			// here until 2026-08-27, so routedCreatePairs["alter"] and the
			// whole alter_table_stmt grammar were dead code that had never
			// executed despite eight commits claiming a routing flip.
			return secondKeywordRouted(frag, key)
		}
		return routedStmts[key]
	case parser.TokenSymbol:
		if t.Value == "(" {
			return routedStmts["select"]
		}
		return false
	default:
		return false
	}
}

// withFollowerRouted scans past the balanced-paren CTE list after WITH and
// checks whether the FOLLOWER keyword (SELECT/INSERT/etc) is routed.
// Returns false if the leading token is not WITH or if the scan hits an
// unexpected token.
// withFollowerRouted scans past WITH tracking paren depth and returns true
// iff any keyword at depth 0 is in routedStmts. The CTE bodies are inside
// parens (depth > 0) so they're skipped naturally; the main query keyword
// (SELECT/INSERT/etc) appears at depth 0 after the last CTE body closes.
func withFollowerRouted(toks []parser.Token) bool {
	depth := 0
	for i := 1; i < len(toks); i++ { // skip WITH
		tok := toks[i]
		if tok.Kind == parser.TokenSymbol {
			switch tok.Value {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth > 0 || tok.Kind != parser.TokenKeyword {
			continue
		}
		kw := strings.ToLower(tok.Value)
		switch kw {
		case "as", "recursive", "materialized", "not":
			// Words of the WITH clause itself: `WITH RECURSIVE`, `AS [NOT]
			// MATERIALIZED (`. RECURSIVE used to reach the default arm, so
			// EVERY recursive CTE — routedStmts["recursive"] — stayed legacy.
			continue
		default:
			return routedStmts[kw]
		}
	}
	return false
}



// explainInnerRouted routes an EXPLAIN only when the statement it wraps
// would be routed on its own: skip the bare ANALYZE / VERBOSE words and a
// parenthesised option list, then apply fragmentRouted to what follows. An
// EXPLAIN over an unported class (MERGE, EXECUTE, ...) stays on legacy.
func explainInnerRouted(frag []parser.Token) bool {
	i := 1
	for i < len(frag) {
		tk := frag[i]
		if tk.Kind == parser.TokenSymbol && tk.Value == "(" {
			depth := 0
			for ; i < len(frag); i++ {
				if frag[i].Kind != parser.TokenSymbol {
					continue
				}
				if frag[i].Value == "(" {
					depth++
				} else if frag[i].Value == ")" {
					depth--
					if depth == 0 {
						i++
						break
					}
				}
			}
			continue
		}
		if tk.Kind == parser.TokenKeyword || tk.Kind == parser.TokenIdent {
			switch strings.ToLower(tk.Value) {
			case "analyze", "analyse", "verbose":
				i++
				continue
			}
		}
		break
	}
	if i >= len(frag) {
		return false
	}
	return fragmentRouted(frag[i:])
}

// routedCreatePairs maps leading keyword -> set of ported second keywords.
var routedCreatePairs = map[string]map[string]bool{
	"create": {"table": true, "index": true, "view": true, "materialized": true,
		"function": true, "procedure": true, // P5.2
		"type": true, "domain": true, "sequence": true, // P5.5
		"trigger": true, "constraint": true, // P5.6 ("constraint" = CREATE CONSTRAINT TRIGGER)
		"extension": true, "policy": true}, // P5.9
	"alter": {"table": true,
		"function": true, "procedure": true, "routine": true, // P5.6
		"schema": true}, // P5.9
	"drop": {"table": true, "index": true, "view": true, "materialized": true, // P5.1
		"function": true, "procedure": true, "routine": true, // P5.2
		"type": true, "domain": true, // P5.5
		"trigger": true, // P5.6
		"database": true, // P5.9
		// P5.7 — the rest of the DROP family. Every one of these is a plain
		// name-list form in legacy, not a skip-to-semicolon compat scan.
		"sequence": true, "schema": true, "extension": true, "statistics": true,
		"collation": true, "server": true, "conversion": true, "event": true,
		"access": true, "foreign": true, "text": true, "aggregate": true,
		"operator": true, "cast": true, "transform": true, "rule": true,
		"policy": true, "publication": true, "subscription": true,
		"tablespace": true},
}

// secondKeywordRouted reports whether the fragment's first+second keyword
// pair names a ported DDL class. Unported classes stay legacy.
func secondKeywordRouted(frag []parser.Token, key string) bool {
	allowed := routedCreatePairs[key]
	if allowed == nil {
		return false
	}
	var second string
	found := false
	skip := map[string]bool{"or": true, "replace": true}
	modifierSeen := false
	for _, tok := range frag[1:] {
		if tok.Kind != parser.TokenKeyword && tok.Kind != parser.TokenIdent {
			continue
		}
		w := strings.ToLower(tok.Value)
		if !found {
			if w == "temp" || w == "temporary" || w == "unlogged" {
				// CREATE TEMP|TEMPORARY|UNLOGGED <kind>: opt_create_modifier
				// is taken by TABLE, VIEW and SEQUENCE; no other kind takes
				// one yet.
				modifierSeen = true
				continue
			}
			if w == "unique" && key == "create" {
				continue // CREATE UNIQUE INDEX: opt_unique on create_index_stmt
			}
			if skip[w] {
				continue
			}
			second = w
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if modifierSeen && second != "table" && second != "view" && second != "sequence" {
		return false // CREATE TEMP <other kind>: stays legacy
	}
	if !allowed[second] {
		return false
	}
	if key == "alter" && second == "table" {
		return alterTableActionsRouted(frag)
	}
	if key == "create" && (second == "function" || second == "procedure") {
		return createRoutineRouted(frag)
	}
	return true
}

// commentRouted gates COMMENT ON by its object kind. parseCommentOnTail's
// switch covers a fixed vocabulary and falls through to a bare
// CompatNoopStmt{Tag:"COMMENT"} for anything else — and that fallback is a
// skip-to-semicolon scan, which a grammar cannot reproduce without accepting
// arbitrary token soup. So the ported kinds route and the rest stay legacy,
// where they already work.
//
// The list is exactly comment_target's alternatives in goopg_ext.y; widen the
// two together.
var commentKinds = map[string]bool{
	"table": true, "index": true, "column": true, "constraint": true,
	"trigger": true, "policy": true, "rule": true, "statistics": true,
	"view": true, "sequence": true, "materialized": true, "type": true,
	"domain": true, "collation": true, "access": true, "server": true,
	"foreign": true, "extension": true, "cast": true, "function": true,
	"schema": true,
}

func commentRouted(frag []parser.Token) bool {
	// frag[0] is COMMENT; the kind is the word after ON.
	sawOn := false
	for _, tok := range frag[1:] {
		if tok.Kind != parser.TokenKeyword && tok.Kind != parser.TokenIdent {
			continue
		}
		w := strings.ToLower(tok.Value)
		if !sawOn {
			if w != "on" {
				return false // COMMENT must be followed by ON
			}
			sawOn = true
			continue
		}
		return commentKinds[w]
	}
	return false
}

// createRoutineRouted vetoes the two CREATE FUNCTION / PROCEDURE sub-forms the
// grammar deliberately does not cover, both of which would otherwise become a
// hard 42601 (routeBatch never falls back once a fragment is routed):
//
//   - BEGIN ATOMIC ... END — legacy does not parse this body at all, it scans
//     raw tokens to the matching END (function.go parseFunctionBody). There is
//     no grammar to mirror, only a token walk, so it stays on the legacy path.
//   - TRANSFORM FOR TYPE — legacy REJECTS it outright; keeping it unrouted
//     preserves the legacy error message rather than substituting a yacc one.
//
// Both words are matched at paren depth 0 so an argument or column named
// "transform", or a default expression containing BEGIN, cannot trip the veto.
func createRoutineRouted(frag []parser.Token) bool {
	depth := 0
	for i, tok := range frag {
		if tok.Kind == parser.TokenSymbol {
			switch tok.Value {
			case "(":
				depth++
			case ")":
				depth--
			}
			continue
		}
		if depth != 0 || (tok.Kind != parser.TokenKeyword && tok.Kind != parser.TokenIdent) {
			continue
		}
		switch strings.ToLower(tok.Value) {
		case "transform":
			return false
		case "begin":
			// `BEGIN ATOMIC`; a bare BEGIN at depth 0 is not a body opener in
			// any legal spelling, but the ATOMIC check keeps the veto tight.
			if i+1 < len(frag) && strings.EqualFold(frag[i+1].Value, "atomic") {
				return false
			}
		}
	}
	return true
}

// routedAlterTableActions names the ALTER TABLE actions that
// grammar/goopg_ext.y's alter_table_action (and the whole-statement
// alter_table_stmt alternatives) actually cover. The key is the action's
// leading word, optionally plus its second word where the first alone is
// ambiguous.
//
// WHY AN ALLOWLIST AND NOT A PLAIN ROUTING FLIP: the legacy parser accepts
// ~138 distinct ALTER TABLE forms; this grammar covers about 20. routeBatch
// does NOT fall back to legacy once a fragment is routed (see :91-95) — it
// surfaces the yacc error — so routing the statement class wholesale would
// turn every uncovered action into a hard 42601. This is the same strangler
// shape as routedCreatePairs, one level deeper.
//
// Widen this set ONLY together with the matching grammar alternative AND a
// differential case in alter_table_test.go. TestAlterTableRoutingIsNarrow
// mechanically proves that everything not listed here stays on legacy.
var routedAlterTableActions = map[string]bool{
	"add column":          true,
	"add primary":         true, // ADD PRIMARY KEY (cols)
	"add <col>":           true, // bare ADD colname type
	"drop column":         true,
	"drop constraint":     true,
	"alter column type":   true,
	"alter column set":    true, // SET DEFAULT / SET NOT NULL only (checked below)
	"alter column drop":   true, // DROP DEFAULT / DROP NOT NULL only (checked below)
	"rename to":           true,
	"rename column":       true,
	"validate constraint": true,
	"replica identity":    true,
	"attach partition":    true,
	"detach partition":    true,
	// whole-statement forms (alter_table_stmt, not alter_action_list)
	"owner to":   true,
	"set schema": true,
	"set logged": true,
	"set (":      true,
}

// alterTableActionsRouted reports whether EVERY comma-separated action in an
// ALTER TABLE fragment is one the grammar covers. A single uncovered action
// sends the whole statement to legacy.
func alterTableActionsRouted(frag []parser.Token) bool {
	words := actionWords(frag)
	if len(words) == 0 {
		return false
	}
	for _, act := range splitTopLevelCommas(words) {
		if !alterActionRouted(act) {
			return false
		}
	}
	return true
}

// actionWords returns the lowercased word/symbol sequence following
// `ALTER TABLE [IF EXISTS] [ONLY] <qualified_name>`.
func actionWords(frag []parser.Token) []string {
	var w []string
	for _, t := range frag {
		switch t.Kind {
		case parser.TokenKeyword, parser.TokenIdent:
			w = append(w, strings.ToLower(t.Value))
		case parser.TokenSymbol, parser.TokenOperator:
			w = append(w, t.Value)
		default:
			w = append(w, "\x00lit") // literals: placeholder, never a keyword
		}
	}
	i := 0
	eat := func(s string) bool {
		if i < len(w) && w[i] == s {
			i++
			return true
		}
		return false
	}
	if !eat("alter") || !eat("table") {
		return nil
	}
	if eat("if") {
		eat("exists")
	}
	eat("only")
	// qualified_name: ident ('.' ident)*
	if i >= len(w) {
		return nil
	}
	i++
	for i+1 < len(w) && w[i] == "." {
		i += 2
	}
	if i < len(w) && w[i] == "*" {
		i++ // the inheritance star, `ALTER TABLE e_star* ...`
	}
	return w[i:]
}

// splitTopLevelCommas splits an action list on commas at paren depth 0.
func splitTopLevelCommas(w []string) [][]string {
	var out [][]string
	depth, start := 0, 0
	for i, t := range w {
		switch t {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				out = append(out, w[start:i])
				start = i + 1
			}
		}
	}
	return append(out, w[start:])
}

// alterActionRouted classifies one action's leading words against
// routedAlterTableActions. Anything it does not positively recognise stays
// legacy — the safe direction.
func alterActionRouted(a []string) bool {
	if len(a) == 0 {
		return false
	}
	at := func(n int) string {
		if n < len(a) {
			return a[n]
		}
		return ""
	}
	switch a[0] {
	case "add":
		// Every ADD form the grammar has: a column (with or without the
		// COLUMN keyword and IF NOT EXISTS) and every table constraint.
		switch at(1) {
		case "generated":
			return false
		}
		return true
	case "drop":
		// DROP [COLUMN] [IF EXISTS] name, DROP CONSTRAINT [IF EXISTS] name.
		return true
	case "alter":
		i := 1
		if at(i) == "constraint" {
			return true // ALTER CONSTRAINT name <deferrability>
		}
		if at(i) == "column" {
			i++
		}
		if at(i) == "" {
			return false
		}
		i++ // the column name
		switch at(i) {
		case "type", "set", "drop", "reset", "add":
			// SET/DROP OPTIONS (foreign tables) has no grammar.
			return at(i+1) != "options"
		default:
			return false
		}
	case "rename":
		return true // TO / [COLUMN] old TO new / CONSTRAINT old TO new
	case "validate":
		return at(1) == "constraint"
	case "replica":
		return at(1) == "identity"
	case "attach":
		return at(1) == "partition"
	case "detach":
		return at(1) == "partition"
	case "inherit", "of":
		return true
	case "not":
		return at(1) == "of"
	case "cluster":
		return at(1) == "on"
	case "reset":
		return at(1) == "("
	case "enable", "disable", "force":
		// TRIGGER is a statement-level flag; ROW LEVEL SECURITY is an action.
		// RULE has no grammar.
		return at(1) == "trigger" || at(1) == "row" || at(1) == "always" || at(1) == "replica"
	case "no":
		return at(1) == "inherit" || (at(1) == "force" && at(2) == "row")
	case "owner":
		switch at(2) {
		case "current_user", "session_user", "current_role":
			return false
		}
		return at(1) == "to"
	case "set":
		switch at(1) {
		case "schema", "logged", "unlogged", "(", "tablespace":
			return true
		case "access":
			return at(2) == "method"
		case "without":
			return at(2) == "oids" || at(2) == "cluster"
		default:
			return false
		}
	default:
		return false
	}
}

func firstMeaningful(frag []parser.Token) *parser.Token {
	for i := range frag {
		switch frag[i].Kind {
		case parser.TokenEOF:
			continue
		default:
			return &frag[i]
		}
	}
	return nil
}

// fragmentBase returns the absolute byte offset of a fragment's first token
// relative to the parent slice start (both carry absolute positions today,
// so this is 0 in practice; kept for the day slices become re-based).
func fragmentBase(parent, frag []parser.Token) int {
	if len(parent) == 0 || len(frag) == 0 {
		return 0
	}
	base := parent[0].Pos
	if frag[0].Pos >= base {
		return frag[0].Pos - base
	}
	return frag[0].Pos
}

// RouteBatch is the production wiring point: postmaster assigns
// parser.RouteBatch = sqlparser.RouteBatch at startup. See the hook doc in
// internal/parser for the contract.
func RouteBatch(src string, toks []parser.Token) ([]parser.Stmt, bool, error) {
	return routeBatch(src, toks)
}

// RouteExpr is the production wiring point for parser.RouteExprBatch: it
// parses a bare expression by wrapping the token slice in a synthetic SELECT
// target (prepended token carries Pos-7 so the real tokens keep their source
// offsets — expression node positions stay byte-exact for plpgsql carets).
// A wrapped parse that leaves residue outside Targets[0] maps to legacy's
// trailing-tokens diagnostic.
func RouteExpr(src string, toks []parser.Token) (parser.Expr, bool, error) {
	if len(toks) == 0 {
		return nil, false, nil
	}
	_ = src
	prep := make([]parser.Token, 0, len(toks)+2)
	prep = append(prep, parser.Token{Kind: parser.TokenKeyword, Value: "select", Pos: toks[0].Pos - 7})
	prep = append(prep, toks...)
	stmts, err := ParseOne(prep, 0)
	if err != nil {
		return nil, true, err
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok || len(sel.Targets) != 1 || sel.From != nil || sel.Where != nil ||
		len(sel.WindowClause) != 0 || sel.SetOp != nil || len(sel.ValuesRows) != 0 ||
		len(sel.OrderBy) != 0 || sel.GroupBy != nil || sel.Having != nil {
		return nil, true, &parser.SyntaxError{Message: "unexpected trailing tokens after expression"}
	}
	return sel.Targets[0].Expr, true, nil
}
