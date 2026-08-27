package sqlparser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// This file adapts the existing hand-written lexer's token stream
// (parser.Token slices — see internal/parser.Lex and the dispatch splitter)
// to the goyacc yyLexer interface.
//
// Design notes (docs/design/not_ralph/01-architecture.md §4, 02 §4-§6):
//
//   - Keyword terminals are resolved by TERMINAL NAME against the generated
//     parser's yyToknames at init time; keywords_gen.go stores names, never
//     numbers, so grammar regenerations cannot skew the two.
//   - Single-character symbol terminals (';' '(' ',' ...) get SEQUENTIAL
//     numbers in goyacc — NOT their ASCII codes (verified empirically,
//     2026-08-25) — so they are resolved through the same name table by
//     normalizing yyToknames entries of the form "'c'".
//   - Named operator terminals (TYPECAST etc.) exist because scan.l emits
//     them distinctly while our legacy lexer folds them into generic
//     operator strings; splitByName restores the distinction (05-risks #11).
//   - Syntax errors surface as *parser.SyntaxError carrying the ABSOLUTE
//     byte position of the offending token, preserving the wire-level
//     ErrorResponse Position contract (01-architecture §6).

// lexerState implements the goyacc yyLexer interface over a pre-split token
// subslice. Absolute byte positions are preserved end to end.
type lexerState struct {
	src       string // original SQL text (ParseOneSrc); "" = span capture off
	spanStart int
	toks []parser.Token // this statement's tokens (absolute Pos)
	i    int            // next raw index into toks

	out []parser.Stmt       // filled by the start production
	err *parser.SyntaxError // first error wins

	lastText string // original-case source text, for error wording
	prevText string // text of the token before lastText
	lastPos  int    // absolute Pos of last returned token
	fragEnd  int    // exclusive end offset of fragment's last real token; trailing ';' excluded
	endMark  int    // optional explicit span end (>=0); set by with_data_kw
	genSpanEnd int  // span end for a generated-column expression: the ')' position

	// lastIntervalNode / lastIntervalRaw remember the most recent
	// `INTERVAL SCONST` reduction so `AT TIME ZONE INTERVAL '...'` can recover
	// the literal's RAW body. Legacy deliberately degrades that zone to a plain
	// StringConst (select.go:2442: "the standard interval parser only handles
	// day/month/year units, so INTERVAL '00:00' / '-10:00' fall through here as
	// strings"), and the body is not recoverable from the IntervalLit node.
	// A single slot is enough, and is exact: the check is POINTER identity
	// against the zone expression, so `AT TIME ZONE 'UTC' + interval '1 day'`
	// — whose zone is a BinaryOp, not the interval node — correctly does not
	// match.
	lastIntervalNode parser.Expr
	lastIntervalRaw  string
	prevPos  int    // absolute Pos of the token before that (mid-rule pos capture)

	// base_yylex one-token pushback (base_yylex.go)
	pushed  bool
	pushedR lexResult
}

// applyNulls applies an opt_nulls_order marker ("f"/"l") to a SortBy value
// stored in the union (NullsFirst nil = default).
func (l *lexerState) applyNulls(sb parser.SortBy, marker string) {
	switch marker {
	case "f":
		v := true
		sb.NullsFirst = &v
	case "l":
		v := false
		sb.NullsFirst = &v
	}
}

// lastConsumedPos is the byte offset of the most recently CONSUMED-but-one
// token — used by grammar mid-rule helpers like select_pos, which run right
// after the keyword whose position they need.
func (l *lexerState) lastConsumedPos() int { return l.prevPos }

// markSpanStart records where a raw-span capture begins (next token pos).
func (l *lexerState) markSpanStart() { l.spanStart = l.peek().pos }

// spanText returns src[spanStart:end-of-last-consumed-token], trimmed like
// legacy captureSrcSpan. Empty when src was unavailable.
func (l *lexerState) spanText() string {
	end := l.lastPos + len(l.lastText)
	if l.src == "" || l.spanStart < 0 || end < l.spanStart || end > len(l.src) {
		return ""
	}
	return strings.TrimSpace(l.src[l.spanStart:end])
}

// spanTextCloseParen captures a raw span whose body is immediately followed
// by the ')' the rule has already shifted (CHECK (expr), and any future
// parenthesised body). spanText() would run to the END of that ')' and
// return "a > 0)"; the ')' START is the body's exclusive end. Verified
// against the legacy captureSrcSpan output for column- and table-level
// CHECK in TestCheckConstraintSpanParity.
func (l *lexerState) spanTextCloseParen() string { return l.spanTextUpTo(l.lastPos) }

// spanEnd returns endMark when set, else fragEnd (covers stmts with a
// trailing optional clause that must stay out of the captured span).
func (l *lexerState) spanEnd() int {
	if l.endMark >= 0 {
		return l.endMark
	}
	return l.fragEnd
}

// spanTextUpTo captures src[spanStart:end] trimmed like legacy captureSrcSpan;
// end is an exclusive byte offset (e.g. the lookahead token's position).
func (l *lexerState) spanTextUpTo(end int) string {
	if l.src == "" || l.spanStart < 0 || end < l.spanStart || end > len(l.src) {
		return ""
	}
	return strings.TrimSpace(l.src[l.spanStart:end])
}

// lexResult is one mapped terminal plus its semantic values and position.
type lexResult struct {
	term int
	str  string
	ival int
	pos  int
	text string // original-case source text
}

// ParseOne parses ONE statement's token subslice through the generated LALR
// grammar. toks must carry absolute byte positions (the dispatch splitter
// guarantees this); baseOffset documents the slice origin and is currently
// redundant because positions are already absolute.
// ParseOneSrc parses one fragment with the ORIGINAL SQL text available for
// raw-span capture (CHECK constraint bodies etc.).
func ParseOneSrc(src string, toks []parser.Token) ([]parser.Stmt, error) {
	l := &lexerState{toks: toks, src: src}
	l.lastPos = eofPos(toks)
	l.fragEnd = fragEndPos(src, toks)
	l.endMark = -1
	if yyParse(l) != 0 {
		if l.err != nil {
			return nil, l.err
		}
		return nil, &parser.SyntaxError{Message: "syntax error", Raw: true}
	}
	return l.out, l.errAsError()
}

func ParseOne(toks []parser.Token, baseOffset int) ([]parser.Stmt, error) {
	_ = baseOffset
	l := &lexerState{toks: toks}
	l.lastPos = eofPos(toks)
	l.endMark = -1
	if yyParse(l) != 0 {
		if l.err != nil {
			return nil, l.err
		}
		return nil, &parser.SyntaxError{Message: "syntax error", Raw: true}
	}
	return l.out, l.errAsError()
}

func (l *lexerState) errAsError() error {
	if l.err != nil {
		return l.err
	}
	return nil
}

// fragEndPos returns the exclusive end offset of the fragment's last real
// token (a trailing ';' contributes its START, so the span excludes it).
//
// Prefer a delimiter's POSITION over the last token's Pos+len(Value): Value is
// the DECODED text, so a trailing quoted literal ('64MB' -> Value "64MB") or
// quoted identifier under-counts by its quotes and truncates the span. Both
// delimiters are exact: the lexer appends an EOF token whose Pos is the end of
// input, and a fragment that ends at ';' has the ';' Pos.
func fragEndPos(src string, toks []parser.Token) int {
	eof := -1
	for i := len(toks) - 1; i >= 0; i-- {
		t := toks[i]
		if t.Kind == parser.TokenEOF {
			eof = t.Pos // end of input; only whitespace can follow the last token
			continue
		}
		if t.Value == ";" {
			return t.Pos // trailing ';' is excluded from the span
		}
		if eof >= 0 {
			return eof // exact, and quote-safe (TrimSpace drops the gap)
		}
		return t.Pos + len(t.Value) // no delimiter available: best effort
	}
	return len(src)
}

// setValueAtoms reconstructs a SET statement's value the way legacy does.
//
// This is NOT a raw source span: internal/parser/parseSetValueAtoms
// (parser.go:3056) walks the value tokens and joins their DECODED text with
// ", ". So `SET work_mem = '64MB'` yields `64MB` — quotes stripped — while
// `SET search_path TO public, pg_catalog` yields the two atoms rejoined. A
// source span matches only by accident, whenever the input already has single
// spaces and no quotes.
//
// Scanning the token slice also sidesteps the mid-rule markSpanStart() this
// rule used to use, which is not lookahead-stable: in `SET name = value` the
// parser has already consumed the first value token to decide the set_eq_to
// reduce, so peek() pointed one token PAST the value — `SET x = 1` captured ""
// (peek was EOF) and `SET search_path TO public, pg_catalog` captured
// ", pg_catalog". Every routed SET was therefore storing a wrong value.
//
// The GUC name is a ColId and can be neither '=' nor TO, so the first
// occurrence of either is always the separator.
// setValueIsDefault reports whether a SET statement's value is the bare
// DEFAULT keyword (as opposed to the string literal 'default', which legacy
// keeps as a value). Only the token KIND separates them, which is why this
// cannot be a grammar alternative without a reduce/reduce against the
// permissive value list.
func (l *lexerState) setValueIsDefault() bool {
	i := l.setValueStart()
	if i < 0 || i >= len(l.toks) {
		return false
	}
	t := l.toks[i]
	if t.Kind != parser.TokenKeyword && t.Kind != parser.TokenIdent {
		return false
	}
	if !strings.EqualFold(t.Value, "default") {
		return false
	}
	// must be the ONLY value token
	for j := i + 1; j < len(l.toks); j++ {
		if l.toks[j].Kind != parser.TokenEOF && l.toks[j].Value != ";" {
			return false
		}
	}
	return true
}

// setValueStart returns the index of the first value token (after '=' / TO),
// or -1. The GUC name is a ColId and can be neither, so the first occurrence
// of either is always the separator.
func (l *lexerState) setValueStart() int {
	for i, t := range l.toks {
		if t.Kind == parser.TokenOperator && t.Value == "=" {
			return i + 1
		}
		if (t.Kind == parser.TokenKeyword || t.Kind == parser.TokenIdent) && strings.EqualFold(t.Value, "to") {
			return i + 1
		}
	}
	return -1
}

func (l *lexerState) setValueAtoms() string {
	if l.setValueIsDefault() {
		return "" // legacy records Default=true and leaves Value empty
	}
	i := l.setValueStart() - 1
	if i < 0 {
		return ""
	}
	var atoms []string
	for i++; i < len(l.toks); i++ {
		t := l.toks[i]
		switch t.Kind {
		case parser.TokenIntLit, parser.TokenNumericLit, parser.TokenStringLit,
			parser.TokenIdent, parser.TokenQuotedIdent, parser.TokenKeyword:
			atoms = append(atoms, t.Value)
		case parser.TokenOperator:
			// Leading minus on a numeric value (legacy parser.go:3075-3085).
			if t.Value == "-" && i+1 < len(l.toks) {
				n := l.toks[i+1]
				if n.Kind == parser.TokenIntLit || n.Kind == parser.TokenNumericLit {
					atoms = append(atoms, "-"+n.Value)
					i++
					continue
				}
			}
			return strings.Join(atoms, ", ")
		case parser.TokenSymbol:
			if t.Value == "," {
				continue
			}
			return strings.Join(atoms, ", ")
		default:
			return strings.Join(atoms, ", ")
		}
	}
	return strings.Join(atoms, ", ")
}

func eofPos(toks []parser.Token) int {
	if n := len(toks); n > 0 {
		last := toks[n-1]
		return last.Pos + len(last.Value)
	}
	return 0
}

// Lex implements yyLexer. It applies the base_yylex lookahead substitutions
// (base_yylex.go) on top of the raw token mapping.
func (l *lexerState) Lex(lval *yySymType) int {
	res := l.next()
	l.prevPos = l.lastPos
	l.prevText = l.lastText
	lval.str = res.str
	lval.ival = res.ival
	lval.p = res.pos
	l.lastText = res.text
	l.lastPos = res.pos
	if res.term <= 0 {
		l.lastText = ""
	}
	return res.term
}

// knownTypeNames maps bare identifiers that act as typed-literal prefixes
// (gram.y Typename 'string') to their canonical type name. When followed by
// SCONST, the identifier is consumed as part of the literal.
var knownTypeNames = map[string]string{
	"date":      "date",
	"time":      "time",
	"timestamp": "timestamp",
	// gram.y allows ANY Typename before a string literal (`text 'x'`,
	// `json '{}'`). This list is deliberately EXACTLY legacy's accepted set
	// (measured, not guessed): widening it would make the routed parser accept
	// statements the pre-migration parser rejected, which is a behaviour change
	// even though PostgreSQL itself allows them. legacy additionally rejects
	// jsonpath, uuid, bytea, money, inet, cidr, macaddr, macaddr8, xml,
	// tsvector, tsquery and varbit — see TODO.md.
	"text": "text", "varchar": "varchar", "bpchar": "bpchar",
	"bool": "bool", "boolean": "boolean",
	"json": "json", "jsonb": "jsonb",
	"numeric": "numeric", "int2": "int2", "int4": "int4", "int8": "int8",
	"float4": "float4", "float8": "float8", "oid": "oid", "name": "name",
	"point": "point", "line": "line", "lseg": "lseg", "box": "box",
	"path": "path", "polygon": "polygon", "circle": "circle",
	"pg_lsn": "pg_lsn", "timestamptz": "timestamptz", "timetz": "timetz",
	// Keyword-tokenised names legacy also accepts as typed-literal prefixes.
	// Measured against legacy 2026-08-27 — it takes exactly these six and
	// still REJECTS character, nchar, bit, int, float, double precision and
	// national character, which therefore stay out.
	"char": "char", "decimal": "decimal", "integer": "integer",
	"smallint": "smallint", "bigint": "bigint", "real": "real",
}

// scanSelfChars is scan.l's {self} set verbatim
// (postgres/src/backend/parser/scan.l:366). Any OTHER single-character
// operator is part of {op_chars} and reaches the grammar as Op, exactly as
// upstream does.
const scanSelfChars = `,()[].;:+-*/%^<>=`

// next returns the next terminal after base_yylex substitution.
func (l *lexerState) next() lexResult {
	if l.pushed {
		l.pushed = false
		return l.pushedR
	}
	cur := l.raw()

	// Typed-literal normalization: IDENT(type-name) + SCONST → TYPEDLIT
	// (synthetic terminal; type rides in str). Mirrors legacy TypedStringLit.
	// time/timestamp/interval are kwlist COL_NAME keywords (TIME/TIMESTAMP/
	// INTERVAL terminals); date is a plain IDENT. Match on text either way.
	lower := strings.ToLower(cur.str)
	if _, isKw := knownTypeNames[lower]; isKw {
		// Match on TEXT, not on a fixed terminal set: most type names are
		// col_name_keywords with terminals of their own (TEXT_P, JSON, ...),
		// so an IDENT/TIME/TIMESTAMP-only check folded `date '...'` but not
		// `text '...'` or `json '...'`. The SCONST lookahead is what makes this
		// unambiguous — no valid statement has a column reference or alias
		// immediately followed by a string literal.
		if cur.term > 0 {
			if nxt := l.peek(); nxt.term == resolve("SCONST") {
				l.i++ // consume the SCONST too
				// Fold to one synthetic terminal so c_expr can build a
				// TypedStringLit (legacy parity) instead of a bare string —
				// `date 'X' + interval 'Y'` needs the type for + resolution.
				return lexResult{term: resolve("TYPEDLIT"), str: knownTypeNames[lower] + "\x1f" + nxt.str, pos: cur.pos, text: cur.text + " " + nxt.text}
			}
		}
	}

	if rule, ok := substFor(cur.term); ok {
		if rule.followers[l.peek().term] {
			cur.term = rule.subst
		}
	}
	return cur
}

// peek maps the next raw token WITHOUT consuming it (a pure function of the
// slice — which is why pushback-free lookahead works here).
func (l *lexerState) peek() lexResult { return l.mapToken(l.i) }

// raw consumes and maps the next token.
func (l *lexerState) raw() lexResult {
	r := l.mapToken(l.i)
	l.i++
	return r
}

func (l *lexerState) mapToken(i int) lexResult {
	if i >= len(l.toks) {
		// EOF contract for goyacc's yylex1: return <= 0 and it becomes $end
		// (yyTok1[0]). Returning a positive number here would be misread as
		// an ASCII char code — the original infinite-loop/syntax-error bug.
		return lexResult{term: 0}
	}
	t := l.toks[i]
	switch t.Kind {
	case parser.TokenEOF:
		// The legacy lexer appends an explicit EOF token; same contract.
		return lexResult{term: 0}
	case parser.TokenIdent, parser.TokenKeyword:
		lower := strings.ToLower(t.Value)
		if def, ok := keywordByText[lower]; ok {
			return lexResult{term: resolve(def.Token), str: lower, pos: t.Pos, text: t.Value}
		}
		// Unquoted identifiers fold to lowercase upstream (scan.l).
		return lexResult{term: resolve("IDENT"), str: lower, pos: t.Pos, text: t.Value}
	case parser.TokenQuotedIdent:
		// Quoted identifiers are never keywords; Value is decoded/unquoted.
		return lexResult{term: resolve("IDENT"), str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenStringLit:
		return lexResult{term: resolve("SCONST"), str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenIntLit:
		n, _ := strconv.Atoi(strings.ReplaceAll(t.Value, "_", ""))
		return lexResult{term: resolve("ICONST"), ival: n, str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenNumericLit:
		return lexResult{term: resolve("FCONST"), str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenBitStringLit:
		// BCONST vs XCONST split is lossy in the legacy lexer (05-risks #4):
		// both arrive as TokenBitStringLit with a marker byte inside Value.
		// Conformance fixture due at P2; BCONST is the safe default today.
		return lexResult{term: resolve("BCONST"), str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenParam:
		n, _ := strconv.Atoi(strings.TrimPrefix(t.Value, "$"))
		return lexResult{term: resolve("PARAM"), ival: n, str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenOperator:
		if name, ok := namedOperator[t.Value]; ok {
			return lexResult{term: resolve(name), str: t.Value, pos: t.Pos, text: t.Value}
		}
		if len(t.Value) == 1 && strings.ContainsRune(scanSelfChars, rune(t.Value[0])) {
			// scan.l's {self} set: single-char operators are char-literal
			// terminals upstream — ASCII code per the yylex1 contract
			// (tables.go). The legacy lexer emits them as TokenOperator.
			//
			// The membership test is load-bearing. Treating EVERY one-character
			// operator as a char terminal made `~`, `&`, `#`, `|`, `@` and `?`
			// unreachable, because the grammar declares only the {self}
			// characters: `a ~ 'x'` was a syntax error while its multi-character
			// siblings `!~` and `~*` parsed fine, since those reach Op.
			return lexResult{term: int(t.Value[0]), str: t.Value, pos: t.Pos, text: t.Value}
		}
		return lexResult{term: resolve("Op"), str: t.Value, pos: t.Pos, text: t.Value}
	case parser.TokenSymbol:
		if len(t.Value) == 1 {
			// goyacc contract: single-char symbols are returned as their
			// ASCII code; yylex1 translates via yyTok1[ascii]. (Sequential
			// yyToknames numbers apply ONLY to named terminals — verified
			// 2026-08-25, see tables.go notes.)
			return lexResult{term: int(t.Value[0]), str: t.Value, pos: t.Pos, text: t.Value}
		}
		// Multi-char symbols shouldn't exist (operators arrive as
		// TokenOperator); fall through to $unk for a clean syntax error.
		return lexResult{term: yyUnkCode, str: t.Value, pos: t.Pos, text: t.Value}
	default:
		return lexResult{term: yyUnkCode, str: t.Value, pos: t.Pos, text: t.Value}
	}
}

// Error implements yyLexer: first error wins; wording follows upstream's
// scanner_yyerror shape ("syntax error at or near "TOKEN""), echoing the
// ORIGINAL source spelling (M0134-0070 fidelity).
func (l *lexerState) Error(msg string) {
	if l.err != nil {
		return
	}
	if l.lastText == "" {
		l.err = &parser.SyntaxError{
			Message: "syntax error at end of input",
			Raw:     true,
			Pos:     l.lastPos,
		}
		return
	}
	l.err = &parser.SyntaxError{
		Message: fmt.Sprintf("syntax error at or near %s", pgQuote(l.lastText)),
		Raw:     true,
		Pos:     l.lastPos,
	}
}

// pgQuote renders s the way psql echoes tokens in syntax errors: double
// quotes, doubling any embedded quote.
func pgQuote(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}
