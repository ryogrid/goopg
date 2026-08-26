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
	lastPos  int    // absolute Pos of last returned token
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
}

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
		if cur.term == resolve("IDENT") || cur.term == resolve("TIME") ||
			cur.term == resolve("TIMESTAMP") || cur.term == resolve("INTERVAL") {
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
		if len(t.Value) == 1 {
			// scan.l's {self} set: single-char operators are char-literal
			// terminals upstream — ASCII code per the yylex1 contract
			// (tables.go). The legacy lexer emits them as TokenOperator.
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
