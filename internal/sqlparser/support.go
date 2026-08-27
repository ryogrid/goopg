package sqlparser

import (
	"errors"
	"strconv"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/utils/adt/similarto"
	"github.com/goopg/goopg/internal/parser"
)

// Support helpers for grammar actions (docs/design/not_ralph/
// 02-grammar-porting-guide.md §3 rule 3): anything longer than ~10 lines
// lives here instead of inline, keeping the .y diffable against upstream.

// qname is a dotted-name carrier: qualified_name builds it, consumers
// (column refs, table refs) interpret the part count. Mirrors upstream's
// {list} handling in ColId '.' attr_name chains.
type qname struct {
	parts []string
	pos   int // absolute offset of the FIRST part
}

// distinctInfo carries opt_all_distinct's result (upstream splits this into
// opt_all_clause + distinct_clause; see pg_grammar.y note).
type distinctInfo struct {
	distinct bool
	on       []parser.Expr // DISTINCT ON list (P1.3)
}

// columnRefFromParts interprets 1..3 parts as column / table.column /
// schema.table.column (gram.y :15640 c_expr case via ColumnRef). More than
// three parts is upstream's "improper qualified name" error; we surface the
// same wording through the legacy parser at P2 when Typename rules land —
// for now a 4-part name degrades to its last three parts, which no current
// differential fixture exercises.
func columnRefFromParts(q qname) parser.Expr {
	parts := q.parts
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return parser.NewColumnRef(q.pos, parts)
}

// rangeVarFromName interprets a FROM-item dotted name: last part = relation
// name, second-to-last = schema (when present). Three-plus-part names
// degrade like columnRefFromParts above.
func rangeVarFromName(q qname, alias string) parser.RangeVar {
	schema := ""
	name := ""
	switch n := len(q.parts); {
	case n == 1:
		name = q.parts[0]
	default:
		schema = q.parts[n-2]
		name = q.parts[n-1]
	}
	return parser.NewRangeVar(q.pos, schema, name, alias)
}

// foldNegate mirrors gram.y's doNegate (:10874): unary minus applied
// directly to a numeric literal folds INTO the constant instead of building
// a UnaryOp node.
//
// This DIVERGES from legacy, which never folds — `SELECT -1`,
// `INSERT INTO t VALUES (-1)`, `LIMIT -1` and `FOR VALUES FROM (-1)` all build
// UnaryOp{OpUnaryNeg, IntegerConst{1}} there. The divergence is deliberate and
// pre-existing: difftest_known_diffs.md rules it "(b)-inverted — yacc is RIGHT,
// made moot at cutover", pinned on both sides by TestKnownDiffUnaryMinusFold.
// b_expr's new unary-sign alternatives inherit it, which is why BETWEEN's
// signed bounds fold too; that is consistent, not a new divergence.
func foldNegate(e parser.Expr) parser.Expr {
	switch v := e.(type) {
	case *parser.IntegerConst:
		v.Value = -v.Value
		return v
	case *parser.NumericConst:
		v.Value = "-" + v.Value
		return v
	default:
		return parser.NewUnaryOp(e.Pos(), parser.OpUnaryNeg, e)
	}
}

// selectLimit carries opt_select_limit's result (gram.y :13261 SelectLimit).
type selectLimit struct {
	count    parser.Expr // LIMIT n / FETCH n
	offset   parser.Expr // OFFSET n
	withTies bool        // FETCH ... WITH TIES
	set      bool
}

// gateSyntaxError records a mid-parse hard error (the LIMIT #,# shape,
// gram.y :13290-13296 raises ereport from inside an action). The parser
// keeps reducing; ParseOne surfaces the recorded error afterwards.
func gateSyntaxError(l *lexerState, msg, hint string) {
	if l.err == nil {
		l.err = &parser.SyntaxError{Message: msg, Hint: hint, Raw: true, Pos: l.lastConsumedPos()}
	}
}

// joinSpec carries the join-type prefix of a JOIN clause (NATURAL? LEFT?
// CROSS? ...) before the right-hand table ref.
type joinSpec struct {
	jt      parser.JoinType
	natural bool
	pos     int // byte offset of the LAST keyword of the join prefix
}

// newJoinSpec maps the grammar's spelling to JoinType + natural flag,
// mirroring parseJoinClause's switch (select.go:1276-1313).
func newJoinSpec(natural bool, kind string) *joinSpec {
	jt := parser.JoinInner
	switch kind {
	case "left":
		jt = parser.JoinLeft
	case "right":
		jt = parser.JoinRight
	case "full":
		jt = parser.JoinFull
	case "cross":
		jt = parser.JoinCross
	}
	return &joinSpec{jt: jt, natural: natural}
}

// joinQual carries a join qualifier pair (ON expr | USING cols | neither).
type joinQual struct {
	on    parser.Expr
	using []string
}

// buildJoin assembles one JoinExpr and validates the qualifier combination
// exactly like parseJoinClause (:1329-1360): NATURAL CROSS JOIN rejected;
// NATURAL implies no ON/USING; everything else REQUIRES ON or USING.
func buildJoin(l *lexerState, spec *joinSpec, right parser.RangeVar, q joinQual) parser.JoinExpr {
	j := parser.NewJoinExpr(spec.pos, spec.jt, spec.natural, right, q.on, q.using)
	if spec.jt == parser.JoinCross && spec.natural {
		l.err = &parser.SyntaxError{Message: "NATURAL CROSS JOIN is not supported", Raw: true, Pos: spec.pos}
		return j
	}
	if !spec.natural && spec.jt != parser.JoinCross && q.on == nil && len(q.using) == 0 {
		l.err = &parser.SyntaxError{Message: "expected ON or USING in JOIN", Raw: true, Pos: spec.pos}
	}
	return j
}

// derivedRangeVar mirrors parseRangeVar's subquery arm (:1416-1452):
// mandatory-in-practice alias with a synthetic __sq_<pos-hex> fallback and
// the optional column-alias list.
func derivedRangeVar(l *lexerState, pos int, sub *parser.SelectStmt, alias string, cols []string, lateral bool) parser.RangeVar {
	if alias == "" {
		alias = "__sq_" + strconvFormatHex(pos)
	}
	// NewRangeVar seeds the unexported pos ('(' position, matching legacy);
	// Subquery/Lateral/Columns are exported and set directly.
	rv := parser.NewRangeVar(pos, "", "", alias)
	rv.Subquery = sub
	rv.Lateral = lateral
	rv.Columns = cols
	return rv
}

func strconvFormatHex(v int) string {
	const digits = "0123456789abcdef"
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = digits[v&0xf]
		v >>= 4
	}
	return string(buf[i:])
}

// LATERAL presence sentinels for opt_lateral (compares against $1 == latYes
// in the derived-table action).
const (
	latNo  = 0
	latYes = 1
)

// derivedAlias carries the optional alias/column-list after a FROM
// subquery's closing paren.
type derivedAlias struct {
	alias   string
	cols    []string
	lateral bool
}

// lerr records a mid-parse hard error (first wins), like gateSyntaxError.
func lerr(yylex yyLexer, msg string, pos int) {
	l := yylex.(*lexerState)
	if l.err == nil {
		l.err = &parser.SyntaxError{Message: msg, Raw: true, Pos: pos}
	}
}

// knownSRF mirrors parseRangeVar's builtin list (select.go:1487-1497);
// builtins get lowercased canonical names, everything else passes through
// as written.
var knownSRF = map[string]bool{
	"generate_series": true, "pg_input_error_info": true, "parse_ident": true,
	"pg_get_publication_tables": true, "pg_available_wal_summaries": true,
	"verify_heapam": true, "unnest": true, "generate_subscripts": true,
	"pg_options_to_table": true, "ts_token_type": true,
}

// funcTableName replicates legacy name normalization (:1499-1528): known
// builtins are canonicalized lowercase; other functions keep their source
// spelling; schema qualifiers are dropped either way.
func funcTableName(schema, name string) string {
	if knownSRF[strings.ToLower(name)] {
		return strings.ToLower(name)
	}
	_ = schema // qualifier discarded; dispatch is by bare name
	return name
}

// rowsFromName renders RowsFromEntry.Name the way ObjectName.String() does
// for parseRowsFrom (:5247 area): dotted when qualified.
func rowsFromName(parts []string) string {
	return strings.Join(parts, ".")
}

// syntheticParenSelect wraps a grouped FROM item into `SELECT * FROM ...`
// exactly like tryParseParenJoin (:1199-1208). Note Parenthesized stays
// FALSE there — the wrapper is structural, not user-parenthesized.
func syntheticParenSelect(pos int, fe parser.FromExpr) *parser.SelectStmt {
	s := parser.NewSelectStmt(pos)
	s.Targets = []parser.ResTarget{parser.NewResTarget(pos, "", parser.NewStarExpr(pos, "", ""))}
	s.From = flattenFrom(fe)
	s.FromExprs = []parser.FromExpr{fe}
	return s
}

// flattenFrom extracts Base plus every join right side.
func flattenFrom(fe parser.FromExpr) []parser.RangeVar {
	out := []parser.RangeVar{fe.Base}
	for _, j := range fe.Joins {
		out = append(out, j.Right)
	}
	return out
}

func newTableFuncRef(pos int, name string, args []parser.Expr, ord bool, rows []parser.RowsFromEntry) *parser.TableFuncRef {
	return parser.NewTableFuncRef(pos, name, args, ord, rows)
}

// WITH ORDINALITY presence sentinels (opt_with_ordinality).
const (
	ordNo  = 0
	ordYes = 1
)

// funcTable couples a TableFuncRef with its source schema qualifier so the
// consumer can populate RangeVar{Schema, Name:""} like legacy does
// (select.go:1474-1566 builds RangeVar from obj first).
type funcTable struct {
	ref    *parser.TableFuncRef
	schema string
	name   string
}

// splitFuncName extracts (schema, name) from a dotted qualified name.
func splitFuncName(q qname) *funcTable {
	schema := ""
	if len(q.parts) >= 2 {
		schema = q.parts[len(q.parts)-2]
	}
	name := q.parts[len(q.parts)-1]
	return &funcTable{schema: schema, name: name}
}

// groupClause carries the GROUP BY result into simple_select's action.
type groupClause struct {
	list []parser.Expr
	sets *parser.GroupingSetsSpec
}

// groupItem is one comma-separated GROUP BY element after legacy's
// parseGroupByElems: its contribution to the FLAT GroupBy list, and its
// grouping-set alternatives (one entry for a plain expression).
type groupItem struct {
	flat      []parser.Expr
	alts      [][]parser.Expr
	construct bool
}

func plainGroupItem(e parser.Expr) *groupItem {
	return &groupItem{flat: []parser.Expr{e}, alts: [][]parser.Expr{{e}}}
}

// groupingUnits reads a ROLLUP / CUBE / GROUPING SETS operand list the way
// parseGroupingUnitList does: a parenthesised group `(a, b)` — which the
// expression grammar hands back as a RowExpr — is ONE unit, everything else
// is a unit of one.
func groupingUnits(exprs []parser.Expr) [][]parser.Expr {
	var units [][]parser.Expr
	for _, e := range exprs {
		if r, ok := e.(*parser.RowExpr); ok {
			units = append(units, r.Elems)
		} else {
			units = append(units, []parser.Expr{e})
		}
	}
	return units
}

func flattenUnits(units [][]parser.Expr) []parser.Expr {
	var out []parser.Expr
	for _, u := range units {
		out = append(out, u...)
	}
	return out
}

// rollupAlternatives / cubeAlternatives / cartesianGroupingSets are legacy's
// (select.go:856-907), including their orderings: ROLLUP lists the LONGEST
// prefix first, CUBE walks bit masks upward, and comma-separated elements
// combine by cartesian product with the earlier element outermost.
func rollupAlternatives(units [][]parser.Expr) [][]parser.Expr {
	alts := make([][]parser.Expr, 0, len(units)+1)
	for i := len(units); i >= 0; i-- {
		var set []parser.Expr
		for _, u := range units[:i] {
			set = append(set, u...)
		}
		alts = append(alts, set)
	}
	return alts
}

func cubeAlternatives(units [][]parser.Expr) [][]parser.Expr {
	n := len(units)
	alts := make([][]parser.Expr, 0, 1<<uint(n))
	for mask := 0; mask < 1<<uint(n); mask++ {
		var set []parser.Expr
		for j := 0; j < n; j++ {
			if mask&(1<<uint(j)) != 0 {
				set = append(set, units[j]...)
			}
		}
		alts = append(alts, set)
	}
	return alts
}

func cartesianGroupingSets(components [][][]parser.Expr) [][]parser.Expr {
	sets := [][]parser.Expr{{}}
	for _, alts := range components {
		next := make([][]parser.Expr, 0, len(sets)*len(alts))
		for _, prefix := range sets {
			for _, alt := range alts {
				combined := make([]parser.Expr, 0, len(prefix)+len(alt))
				combined = append(combined, prefix...)
				combined = append(combined, alt...)
				next = append(next, combined)
			}
		}
		sets = next
	}
	return sets
}

// buildGroupClause assembles GroupBy (the flat list, duplicates and all) and
// the expanded GroupingSets exactly as parseGroupByElems returns them; no
// construct means no GroupingSets at all.
func buildGroupClause(pos int, items []*groupItem) *groupClause {
	gc := &groupClause{}
	var comps [][][]parser.Expr
	construct := false
	for _, it := range items {
		gc.list = append(gc.list, it.flat...)
		comps = append(comps, it.alts)
		construct = construct || it.construct
	}
	if construct {
		gc.sets = parser.NewGroupingSetsSpec(pos, cartesianGroupingSets(comps))
	}
	return gc
}

// joinLegacyTokens is joinGeneratedExprTokens (ddl.go:4530) over the token
// stream between two absolute positions (exclusive of both): values joined
// with single spaces, string literals re-quoted, and no space before ')' ','
// '.', after '(' '.', or before a '(' that follows a name or ')'.
func joinLegacyTokens(l yyLexer, from, to int) string {
	ls, ok := l.(*lexerState)
	if !ok {
		return ""
	}
	render := func(tk parser.Token) string {
		if tk.Kind == parser.TokenStringLit {
			return "'" + strings.ReplaceAll(tk.Value, "'", "''") + "'"
		}
		return tk.Value
	}
	var b strings.Builder
	var prev *parser.Token
	for i := range ls.toks {
		tk := ls.toks[i]
		if tk.Pos <= from || tk.Pos >= to {
			continue
		}
		if prev == nil {
			b.WriteString(render(tk))
			prev = &ls.toks[i]
			continue
		}
		noSpace := false
		if tk.Kind == parser.TokenSymbol {
			switch tk.Value {
			case ")", ",", ".":
				noSpace = true
			case "(":
				if prev.Kind == parser.TokenIdent || prev.Kind == parser.TokenQuotedIdent || (prev.Kind == parser.TokenSymbol && prev.Value == ")") {
					noSpace = true
				}
			}
		}
		if prev.Kind == parser.TokenSymbol && (prev.Value == "(" || prev.Value == ".") {
			noSpace = true
		}
		if !noSpace {
			b.WriteByte(' ')
		}
		b.WriteString(render(tk))
		prev = &ls.toks[i]
	}
	return b.String()
}

// substringSimilar ports buildSubstringSimilar (select.go): SUBSTRING(str
// SIMILAR pattern ESCAPE esc) with LITERAL pattern and escape is folded at
// parse time into substring(str, <POSIX regex>::text) through the shared
// similarto converter; a NULL pattern or escape folds to NULL; a non-literal
// operand is the same error legacy raises.
func substringSimilar(l yyLexer, pos int, str, pat, esc parser.Expr) parser.Expr {
	patVal, patNull, patOK := literalValue(pat)
	escVal, escNull, escOK := literalValue(esc)
	ls, _ := l.(*lexerState)
	if !patOK || !escOK {
		if ls != nil && ls.err == nil {
			ls.err = &parser.SyntaxError{Message: "SUBSTRING(... SIMILAR ... ESCAPE ...) with non-literal operands is not yet supported", Raw: true, Pos: pos}
		}
		return parser.NewNullConst(pos)
	}
	if patNull || escNull {
		return parser.NewNullConst(pos)
	}
	if err := similarto.ValidateEscape(escVal); err != nil {
		if ls != nil && ls.err == nil {
			ls.err = &parser.SyntaxError{Message: "invalid escape string", Raw: true, Pos: pos}
		}
		return parser.NewNullConst(pos)
	}
	conv, err := similarto.ConvertSubstring(patVal, escVal)
	if err != nil {
		if ls != nil && ls.err == nil {
			ls.err = &parser.SyntaxError{Message: err.Error(), Raw: true, Pos: pos}
		}
		return parser.NewNullConst(pos)
	}
	return specialFormCall(pos, "substring", []parser.Expr{str, parser.NewTypedStringLit(pos, "text", conv)})
}

func literalValue(e parser.Expr) (string, bool, bool) {
	switch x := e.(type) {
	case *parser.StringConst:
		return x.Value, false, true
	case *parser.NullConst:
		return "", true, true
	}
	return "", false, false
}


// setop machinery types live here so grammar actions stay tiny.
type opSpec struct {
	typ parser.SetOpType
	all bool
	pos int
}

type setopPair struct {
	op    *opSpec
	right *parser.SelectStmt
}

type setopChain struct {
	pairs []setopPair
}

// foldSetOps nests the chain right-to-left onto each left select's single
// SetOp slot, reproducing legacy's recursive parseSelect shape
// (A U B U C => A{Right:B{Right:C}}), INCLUDING the trailer lift: an inner
// RHS greedily takes its own ORDER BY/LIMIT/OFFSET (base_select), so each
// fold lifts them out to the left when present there and the RHS is not
// explicitly parenthesized (M0097-0024 / M0097-0042).
func foldSetOps(base *parser.SelectStmt, pairs []setopPair) *parser.SelectStmt {
	if len(pairs) == 0 {
		return base
	}
	node := pairs[len(pairs)-1].right
	for i := len(pairs) - 1; i >= 0; i-- {
		pr := pairs[i]
		if i < len(pairs)-1 {
			node = pr.right
		}
		left := base
		if i > 0 {
			left = pr.right
		}
		left.SetOp = parser.NewSetOpClause(pr.op.pos, pr.op.typ, pr.op.all, node)
		if right := node; right != nil && !right.Parenthesized {
			if len(left.OrderBy) == 0 && len(right.OrderBy) != 0 {
				left.OrderBy = right.OrderBy
				right.OrderBy = nil
			}
			if left.Limit == nil && right.Limit != nil {
				left.Limit = right.Limit
				right.Limit = nil
			}
			if left.Offset == nil && right.Offset != nil {
				left.Offset = right.Offset
				right.Offset = nil
			}
		}
	}
	return base
}


// tableSelect builds the TABLE t shorthand (legacy :144-158): SELECT * FROM t.
func tableSelect(pos int, q qname) *parser.SelectStmt {
	schema, name := "", ""
	if len(q.parts) == 1 {
		name = q.parts[0]
	} else {
		schema, name = q.parts[len(q.parts)-2], q.parts[len(q.parts)-1]
	}
	star := parser.NewStarExpr(pos, "", "")
	rt := parser.NewResTarget(pos, "", star) // legacy leaves ResTarget.pos zero here
	rv := parser.NewRangeVar(q.pos, schema, name, "")
	s := parser.NewSelectStmt(pos)
	s.Targets = []parser.ResTarget{rt}
	fe := parser.NewFromExpr(rv.Pos(), rv, nil)
	s.From = []parser.RangeVar{rv}
	s.FromExprs = []parser.FromExpr{fe}
	return s
}

// cteItem carries a built CTE through a node-typed union field.
type cteItem struct{ cte *parser.CommonTableExpr }

// buildBetween desugars `[NOT] BETWEEN [SYMMETRIC] low AND high` exactly
// like parseBetweenTail (select.go:4134-4160):
//
//	x >= low AND x <= high
//	SYMMETRIC: (x>=low AND x<=high) OR (x>=high AND x<=low)
//	NOT: NOT(<the above>)
func buildBetween(left parser.Expr, low, high parser.Expr, negated, symmetric bool) parser.Expr {
	pos := left.Pos()
	ge := func(a, b parser.Expr) parser.Expr {
		return parser.NewBinaryOp(pos, parser.OpGe, a, b)
	}
	le := func(a, b parser.Expr) parser.Expr {
		return parser.NewBinaryOp(pos, parser.OpLe, a, b)
	}
	and := func(a, b parser.Expr) parser.Expr {
		return parser.NewBinaryOp(pos, parser.OpAnd, a, b)
	}
	or := func(a, b parser.Expr) parser.Expr {
		return parser.NewBinaryOp(pos, parser.OpOr, a, b)
	}

	var tree parser.Expr
	if symmetric {
		tree = or(and(ge(left, low), le(left, high)), and(ge(left, high), le(left, low)))
	} else {
		tree = and(ge(left, low), le(left, high))
	}
	if negated {
		return parser.NewUnaryOp(pos, parser.OpNot, tree)
	}
	return tree
}

// similarToLiteralValue mirrors select.go:4000.
func similarToLiteralValue(e parser.Expr) (value string, isNull bool, ok bool) {
	switch x := e.(type) {
	case *parser.StringConst:
		return x.Value, false, true
	case *parser.NullConst:
		return "", true, true
	}
	return "", false, false
}

// buildSimilarTo ports select.go:4031 constant folding for SIMILAR TO.
func buildSimilarTo(l yyLexer, left, pattern, escape parser.Expr, pos int, negate bool) parser.Expr {
	patVal, patNull, patOK := similarToLiteralValue(pattern)
	escVal, escNull, escOK := similarto.DefaultEscape, false, true
	if escape != nil {
		escVal, escNull, escOK = similarToLiteralValue(escape)
	}
	if !patOK || !escOK {
		return parser.NewSimilarToPattern(pos, left, pattern, escape, negate)
	}
	if patNull || escNull {
		return parser.NewNullConst(pos)
	}
	if err := similarto.ValidateEscape(escVal); err != nil {
		if ls, ok2 := l.(*lexerState); ok2 && ls.err == nil {
			ls.err = &parser.SyntaxError{Raw: true, Code: "22025", Message: "invalid escape string", Hint: "Escape string must be empty or one character."}
		}
		return parser.NewNullConst(pos)
	}
	converted := similarto.Convert(patVal, escVal)
	op := parser.OpRegexMatch
	if negate {
		op = parser.OpRegexNoMatch
	}
	return parser.NewBinaryOp(pos, op, left, parser.NewTypedStringLit(pos, "text", converted))
}

// binOp maps an operator spelling to its OpCode; unknown spellings raise a hard error.
func binOp(l yyLexer, s string) parser.OpCode {
	if op := parser.ParseBinaryOp(s); op != parser.OpUnknown {
		return op
	}
	if ls, ok := l.(*lexerState); ok && ls.err == nil {
		ls.err = &parser.SyntaxError{Message: fmt.Sprintf("unsupported operator %q", s), Raw: true, Pos: ls.lastConsumedPos()}
	}
	return parser.OpUnknown
}

// whens is a CaseWhen list carrier (union field).
type whenList struct {
	items []parser.CaseWhen
}

// caseBody accumulates CASE WHEN clauses.
type caseBody struct {
	whens    []parser.CaseWhen
	elseExpr parser.Expr
}

// scalarSub is reserved for future use when internal/parser gains a
// dedicated ScalarSublink Expr node type (P2.2 remainder).

func lowerIdent(s string) string { return strings.ToLower(s) }

// intervalRangeOK mirrors legacy intervalRangeLowField: valid `<hi> TO <lo>`
// pairs for the interval typmod qualifier. The stored Unit is the LOW field.
var intervalRangeOK = map[[2]string]bool{
	{"year", "month"}: true,
	{"day", "hour"}: true, {"day", "minute"}: true, {"day", "second"}: true,
	{"hour", "minute"}: true, {"hour", "second"}: true,
	{"minute", "second"}: true,
}

// buildIntervalQualified builds the Form-1 `interval '<body>' <qualifier>`
// IntervalLit (legacy tryIntervalTypmodQualifier parity): Value keeps the raw
// body, Unit carries the low field of the range, precision clamps at 6.
func buildIntervalQualified(pos int, body, hi, lo string, prec int) parser.Expr {
	unit := hi
	if lo != "" {
		if !intervalRangeOK[[2]string{hi, lo}] {
			unit = hi // invalid pair: caller grammar never produces one
		} else {
			unit = lo
		}
	}
	hasPrec := prec >= 0
	if !hasPrec {
		prec = 0 // legacy parity: Prec stays 0 when HasPrec is false
	} else if prec > 6 {
		prec = 6 // MAX_INTERVAL_PRECISION clamp
	}
	return parser.NewIntervalLitQualified(pos, body, unit, hasPrec, prec)
}

// castType carries a cast-target type name through the grammar: optional
// leading schema plus the (possibly array-suffixed) type name.
// castType carries a parsed type name. args is the INLINE typmod, used only by
// the datetime targets: `time(2) with time zone` puts its precision BEFORE the
// tz mark, so col_type_name's trailing `'(' ICONST ')'` suffix cannot reach it
// and the production has to carry it itself.
type castType struct {
	schema, name string
	args         []int64
	// ivCol is the COLUMN-position typmod of an interval qualifier. The cast
	// and column paths pack the same qualifier into different numbers (see
	// parser.IntervalQualTypmods), and cast_target cannot know which position
	// it is in, so it fills args with the cast packing — which every cast site
	// already consumes — and parks the column packing here for col_type_name.
	ivCol []int64
}

// withArrays appends n "[]" pairs the way legacy folds them into Name.
func (c castType) withArrays(n int) castType {
	for i := 0; i < n; i++ {
		c.name += "[]"
	}
	return c
}

// typmodsFor applies legacy's bare-type stamps at cast sites: bare "char"
// carries the bpchar length-1 typmod (ddl.go M0134-0070 note); everything
// else passes the grammar's typmods through untouched.
func typmodsFor(name string, given []int64, _ int) []int64 {
	if name == "char" && len(given) == 0 {
		return []int64{1}
	}
	return given
}

// tzJoin appends the tz suffix for cast targets: "time"+"tz" -> "timetz",
// "timestamp"+"" -> "timestamp" (legacy parseMultiWordTypeName parity).
func tzJoin(base, mark string) string {
	if mark == "tz" {
		return base + "tz"
	}
	return base
}

// buildIntervalLit mirrors legacy tryTypedLiteral's embedded-string form
// (`interval '<body>'`): parse the body into interval components at AST-build
// time so the executor sees an IntervalLit datum, not text.
func buildIntervalLit(pos int, body string) parser.Expr {
	// ORDER MATTERS, and it was wrong: legacy's parseIntervalLiteral
	// (select.go:3376) tries the Form-2 split FIRST — a plain `<N> <unit>` body
	// keeps Value/Unit and PreComputed=false — and only falls back to the
	// whole-body decode for multi-field or HH:MM:SS bodies. Going straight to
	// ParseIntervalBody made `interval '1 day'` a PreComputed literal, so every
	// routed statement using the single-unit spelling — which is exactly what
	// the TPC-H query templates use — carried a different node than legacy.
	if val, unit, ok := parser.SplitEmbeddedInterval(body); ok {
		return parser.NewIntervalLitEmbedded(pos, val, unit)
	}
	if m, d, us, ok := parser.ParseIntervalBody(body); ok {
		return parser.NewIntervalLitPre(pos, m, d, us)
	}
	return parser.NewTypedStringLit(pos, "interval", body)
}

// typedLitParts splits the adapter's folded TYPEDLIT payload "type\x1fvalue"
// back into its type name and literal text.
func typedLitParts(s string) (typ, val string) {
	if i := strings.IndexByte(s, 0x1f); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// partFrameBound / partFrameExtent / partFrameExcl are grammar carriers for
// window frame clauses (support-carrier pattern: joinSpec, distinctInfo).
type partFrameBound struct {
	k   parser.FrameBoundKind
	off parser.Expr
}

type partFrameExtent struct {
	start      parser.FrameBoundKind
	startOff   parser.Expr
	end        parser.FrameBoundKind
	endOff     parser.Expr
	hasBetween bool
}

type partFrameExcl struct {
	x parser.FrameExclusion
}

// finishFrame assembles the WindowFrame from the parsed parts. A single-bound
// extent (no BETWEEN) defaults EndKind to CURRENT ROW, matching gram.y.
func finishFrame(mode parser.FrameMode, ext *partFrameExtent, ex *partFrameExcl) *parser.WindowFrame {
	return &parser.WindowFrame{
		Mode:       mode,
		StartKind:  ext.start,
		StartOffset: ext.startOff,
		EndKind:    ext.end,
		EndOffset:  ext.endOff,
		Exclusion:  ex.x,
		HasBetween: ext.hasBetween,
	}
}

// insSrc carries the INSERT source triple (values rows | select | defaults)
// from insert_source to insert_stmt's action (grammar carrier pattern).
type insSrc struct {
	rows [][]parser.Expr
	sel  *parser.SelectStmt
	def  bool
}

// updWhere carries UPDATE's WHERE tail: a plain expression or the CURRENT OF
// cursor form (mutually exclusive).
type updWhere struct {
	expr      parser.Expr
	currentOf string
}

// colSpec carries one CREATE TABLE column definition before conversion to
// parser.ColumnDef (grammar carrier pattern).
type colSpec struct {
	checkText string
	fkInfo    *fkInfo
	refTable parser.ObjectName
	refCols  []string
	onDel    parser.FKAction
	onUp     parser.FKAction
	name     string
	schema   string
	typ      string
	args     []int64
	isArray  bool
	nullsNotDistinct  bool
	deferrable        bool
	initiallyDeferred bool
	genExpr           string
	genAlways         bool
	genVirtual        bool
	identity          bool
	identityAlways    bool
	identitySeq       *identityOpts
	notNull  bool
	primary  bool
	unique   bool
	defExpr  parser.Expr
	checkName, nnName, uqName string
	collation, compression    string
	nnNoInherit, checkNoInherit bool
	checkNotEnforced bool
	storage          string
}

// tableElem is one element of a CREATE TABLE parens list: a column spec or a
// table-level PRIMARY KEY / UNIQUE marker.
type tableElem struct {
	col        *colSpec
	pk         []string
	uq         [][]string
	check      string
	checkName  string
	fkDef      *parser.TableForeignKeyDef
	namedPk    *parser.TableConstraintDef
	namedUq    *parser.TableConstraintDef
	exclusion  *parser.TableConstraintDef
	like       *parser.ObjectName
	likeOpts   string
	uqIncl     []string
	uqNND      bool
	uqAttrs    *constrAttrs
	pkIncl     []string
	pkAttrs    *constrAttrs
	withPairs  [][2]string
	inherits   []parser.ObjectName
	asSelect   *parser.SelectStmt
	partition  *parser.PartitionByClause
	notNull    *tableNotNull
	checkNoInh bool // table-level CHECK ... NO INHERIT
	checkNotEnf bool // table-level CHECK ... NOT ENFORCED
}

// colConstraints accumulates a column's constraint suffix in CREATE TABLE.
type colConstraints struct {
	// deferrable / initiallyDeferred accumulate the ConstraintAttr items
	// (gram.y ConstraintAttr), which upstream collects as SIBLING alternatives
	// of the same col_constraint loop rather than as a trailer on each
	// constraint — that is what keeps `NOT NULL` and `NOT DEFERRABLE`
	// LALR(1)-separable. nullsNotDistinct is UNIQUE's opt_unique_null_treatment.
	deferrable        bool
	initiallyDeferred bool
	nullsNotDistinct  bool
	genExpr           string
	genAlways         bool
	genVirtual        bool
	identity          bool
	identityAlways    bool
	identitySeq       *identityOpts
	notNull    bool
	primary    bool
	unique     bool
	defExpr    parser.Expr
	checkText  string
	checkName  string
	nnName     string
	uqName     string
	collation   string
	compression string
	nnNoInherit    bool
	checkNoInherit bool
	checkNotEnforced bool
	storage        string
	fk         *fkInfo
}

// createTail accumulates trailing CREATE TABLE options (WITH/INHERITS/
// PARTITION BY/AS).
type createTail struct {
	withPairs [][2]string
	inherits  []parser.ObjectName
	partition *parser.PartitionByClause
	asSelect  *parser.SelectStmt
}

// withMap folds accumulated WITH pairs into the map shape CreateTableStmt
// carries (nil when no pairs).
func (t *createTail) withMap() map[string]string {
	if len(t.withPairs) == 0 {
		return nil
	}
	m := make(map[string]string, len(t.withPairs))
	for _, pr := range t.withPairs {
		m[pr[0]] = pr[1]
	}
	return m
}


// colConstraint is one parsed column-constraint keyword group.
type colConstraint struct {
	kind string // "nn" | "pk" | "uq" | "def" | "check" | "fk" | ...
	text string          // check: raw source span; collate/compression: the name
	fk   *fkInfo         // fk: referenced table/cols + actions
	expr parser.Expr
	seq  *identityOpts   // identity_*: the `(START WITH n ...)` option list
	name string          // `CONSTRAINT name` prefix; legacy keeps it only for NOT NULL / UNIQUE / CHECK
	notEnforced bool     // check: trailing NOT ENFORCED
}

// identityOpts carries an identity column's sequence options — legacy's
// ColumnDef.Identity{Start,Increment,Min,Max,Cache,Cycle}. Start is a plain
// int64 (0 = unset) and the rest are pointers, exactly as the AST has them.
type identityOpts struct {
	start                 int64
	inc, min, max, cache *int64
	cycle                 bool
}

// applyIdentityOpts copies the option list onto the column.
func applyIdentityOpts(cd *parser.ColumnDef, o *identityOpts) {
	if o == nil {
		return
	}
	cd.IdentityStart = o.start
	cd.IdentityIncrement, cd.IdentityMin, cd.IdentityMax, cd.IdentityCache = o.inc, o.min, o.max, o.cache
	cd.IdentityCycle = o.cycle
}

// tableElem is one CREATE TABLE parens element: a column or a table-level
// PRIMARY KEY / UNIQUE marker.
// typeWithArgs pairs a cast-typename with parenthesised typmod args.
type typeWithArgs struct {
	ct   castType
	args []int64
}

// createPrefix carries CREATE [TEMP|TEMPORARY|UNLOGGED] modifiers.
type createPrefix struct {
	temporary bool
	unlogged  bool
}

// dropBehavior maps the trailing CASCADE/RESTRICT keyword ("" = default).
func dropBehavior(s string) parser.DropBehavior {
	if s == "cascade" {
		return parser.DropCascade
	}
	return parser.DropDefault
}

// objectNameFromQn converts the grammar's dotted-name carrier into an
// ObjectName (last part = name, previous = schema).
func objectNameFromQn(q qname) parser.ObjectName {
	var o parser.ObjectName
	if n := len(q.parts); n > 0 {
		o.Name = q.parts[n-1]
		if n > 1 {
			o.Schema = q.parts[n-2]
		}
	}
	return o
}

// fkActs accumulates ON DELETE / ON UPDATE referential actions.
type fkActs struct {
	del        parser.FKAction
	up         parser.FKAction
	delSetCols []string // ON DELETE SET NULL|DEFAULT (cols); legacy drops the ON UPDATE form's list
}

// colConstraint kinds: "nn" | "pk" | "uq" | "def" | "check" | "fk".

// fkInfo carries a column-level FK target parsed inline.
type fkInfo struct {
	refTable   parser.ObjectName
	refCols    []string
	onDel      parser.FKAction
	onUp       parser.FKAction
	delSetCols []string
	matchFull  bool
}

// namedFkAct is one ON DELETE/UPDATE referential action occurrence.
type namedFkAct struct {
	del     bool
	up      bool
	act     parser.FKAction
	setCols []string
}

// applyFkAction folds an action occurrence into the accumulator.
func applyFkAction(a *fkActs, n *namedFkAct) *fkActs {
	if n.del {
		a.del = n.act
		a.delSetCols = n.setCols
	}
	if n.up {
		a.up = n.act
	}
	return a
}

// ctTail carries trailing CREATE TABLE options (v2 flat alternatives).
type ctTail struct {
	partOf    parser.ObjectName
	partOfElems []*partOfElem // PARTITION OF ... ( elements )
	onCommit  string // ON COMMIT DELETE ROWS | DROP | PRESERVE ROWS
	fromVals  []parser.Expr
	toVals    []parser.Expr
	inVals    []parser.Expr
	bDefault  bool
	modulus   int64
	remainder int64
	isHash    bool

	withKv    []string
	inherits  []parser.ObjectName
	partition *parser.PartitionByClause
	asSelect  *parser.SelectStmt
}

// splitKV splits a "key=value" WITH-pair string (first '=' only).
func splitKV(kv string) []string {
	return strings.SplitN(kv, "=", 2)
}

// upperIdent uppercases (PARTITION BY strategy word).
func upperIdent(s string) string { return strings.ToUpper(s) }

// txModes accumulates BEGIN/START TRANSACTION mode clauses.
type txModes struct {
	iso string
	ro  bool
	def bool
}

// constrAttrs carries a table-level constraint's ConstraintAttributeSpec
// (gram.y): [NOT] DEFERRABLE / INITIALLY DEFERRED|IMMEDIATE. INITIALLY DEFERRED
// implies DEFERRABLE, matching legacy's parseConstraintDeferrable.
type constrAttrs struct {
	deferrable        bool
	initiallyDeferred bool
}

func mergeConstrAttr(acc *constrAttrs, kind string) *constrAttrs {
	if acc == nil {
		acc = &constrAttrs{}
	}
	switch kind {
	case "deferrable":
		acc.deferrable = true
	case "not_deferrable":
		acc.deferrable, acc.initiallyDeferred = false, false
	case "initially_deferred":
		acc.deferrable, acc.initiallyDeferred = true, true
	case "initially_immediate":
		acc.initiallyDeferred = false
	}
	return acc
}

// namedTableConstraint builds a CONSTRAINT-named table-level PK/UNIQUE with
// its INCLUDE list, NULLS NOT DISTINCT flag and ConstraintAttributeSpec.
func namedTableConstraint(name string, cols []string, isPrimary bool, incl []string, nnd bool, a *constrAttrs) *parser.TableConstraintDef {
	d := parser.NewTableConstraintDef(name, cols, isPrimary)
	d.IncludeColumns = incl
	d.NullsNotDistinct = nnd
	if a != nil {
		d.Deferrable, d.InitiallyDeferred = a.deferrable, a.initiallyDeferred
	}
	return d
}

// sessionAuthzStmt builds `SET [SESSION|LOCAL] AUTHORIZATION name|DEFAULT`.
//
// A separate `AUTHORIZATION DEFAULT` alternative would reduce/reduce against
// set_value_atom's own DEFAULT (14 conflicts), and the atom TEXT is "default"
// for both the bare keyword and the string literal 'default' — only the token
// KIND separates them, so the check is made here rather than in the grammar.
func sessionAuthzStmt(l *lexerState, local bool, atom string) parser.Stmt {
	if l.authzIsDefaultKeyword() {
		return parser.NewSetStmt(0, local, "session_authorization", "", true)
	}
	return parser.NewSetStmt(0, local, "session_authorization", atom, false)
}

// authzIsDefaultKeyword reports whether the token after AUTHORIZATION is the
// bare DEFAULT keyword (not the string literal 'default').
func (l *lexerState) authzIsDefaultKeyword() bool {
	for i, t := range l.toks {
		if (t.Kind != parser.TokenKeyword && t.Kind != parser.TokenIdent) ||
			!strings.EqualFold(t.Value, "authorization") {
			continue
		}
		if i+1 >= len(l.toks) {
			return false
		}
		n := l.toks[i+1]
		return (n.Kind == parser.TokenKeyword || n.Kind == parser.TokenIdent) &&
			strings.EqualFold(n.Value, "default")
	}
	return false
}

// indexOpts carries the CREATE INDEX `WITH (...)` storage parameters that
// reach the AST. Legacy records exactly these seven and silently discards
// every other name (ddl.go:5590ff); the pointer-valued ones are three-state
// (unset / true / false) because pg_dump re-emits only what was written.
type indexOpts struct {
	fillfactor       int
	ginPendingLimit  int
	pagesPerRange    int
	buffering        string
	deduplicateItems *bool
	fastUpdate       *bool
	autoSummarize    *bool
}

// indexOptsFrom parses the `name=value` pairs str_pair_list collected. An
// unparsable value leaves the option unset, which is what legacy's guarded
// `if ok { ... }` blocks do.
func indexOptsFrom(kvs []string) *indexOpts {
	o := &indexOpts{}
	for _, kv := range kvs {
		parts := splitKV(kv)
		if len(parts) != 2 {
			continue
		}
		name, val := strings.ToLower(parts[0]), parts[1]
		switch name {
		case "fillfactor":
			o.fillfactor = reloptInt(val)
		case "gin_pending_list_limit":
			o.ginPendingLimit = reloptInt(val)
		case "pages_per_range":
			o.pagesPerRange = reloptInt(val)
		case "buffering":
			o.buffering = strings.ToLower(val)
		case "deduplicate_items", "fastupdate", "autosummarize":
			b, ok := parser.ParseReloptionBool(val)
			if !ok {
				continue
			}
			switch name {
			case "deduplicate_items":
				o.deduplicateItems = &b
			case "fastupdate":
				o.fastUpdate = &b
			case "autosummarize":
				o.autoSummarize = &b
			}
		}
	}
	return o
}

// reloptInt accepts only a bare run of digits, mirroring legacy's
// `if p.cur().Kind == TokenIntLit` guard: anything else leaves the option at 0.
func reloptInt(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func applyIndexOpts(ix *parser.CreateIndexStmt, v any) {
	o, _ := v.(*indexOpts)
	if o == nil {
		return
	}
	ix.Fillfactor = o.fillfactor
	ix.GinPendingListLimit = o.ginPendingLimit
	ix.PagesPerRange = o.pagesPerRange
	ix.Buffering = o.buffering
	ix.DeduplicateItems = o.deduplicateItems
	ix.FastUpdate = o.fastUpdate
	ix.AutoSummarize = o.autoSummarize
}


// likeAllOpts is what `INCLUDING ALL` expands to in legacy's BodyOrder marker
// encoding — the nine option names in this exact order (internal/parser).
const likeAllOpts = ":+defaults:+identity:+generated:+constraints:+indexes:+comments:+statistics:+storage:+compression"

// excludeElem is one `col WITH op` item of an EXCLUDE constraint.
type excludeElem struct {
	col  string
	op   string
	cols []string // a parenthesised element: every identifier token inside, as legacy records it
}

// newExclusionConstraint builds the TableConstraintDef legacy produces for an
// EXCLUDE constraint. Legacy keeps the columns as a list but only ONE
// ExclusionOp (the first), and defaults Method to "btree" when USING is
// omitted (internal/parser/ddl.go).
func newExclusionConstraint(name, method string, elems []excludeElem, incl []string, where parser.Expr, a *constrAttrs) *parser.TableConstraintDef {
	var cols []string
	op := ""
	for _, e := range elems {
		if e.cols != nil {
			cols = append(cols, e.cols...)
		} else {
			cols = append(cols, e.col)
		}
		if op == "" {
			op = e.op
		}
	}
	if method == "" {
		method = "btree"
	}
	d := parser.NewTableConstraintDef(name, cols, false)
	d.IsExclusion = true
	d.ExclusionOp = op
	d.Method = method
	d.IncludeColumns = incl
	d.ExclusionWhere = where
	if a != nil {
		d.Deferrable, d.InitiallyDeferred = a.deferrable, a.initiallyDeferred
	}
	return d
}

// callArgs carries a function call's argument list plus the per-argument
// VARIADIC flags, which FuncCall.Variadic keeps PARALLEL to Args.
//
// This exists rather than reusing opt_func_call_args (a plain []Expr) because
// VARIADIC has to attach to an individual argument. Only the name_or_call
// alternatives switch to it — they all share the `qualified_name '('` prefix,
// so they must move together — while ARRAY[...] and the SQL value functions,
// which cannot take VARIADIC, keep the plain list.
type callArgs struct {
	exprs    []parser.Expr
	variadic []bool
}

// callArg is one parsed argument before it joins the list.
type callArg struct {
	expr     parser.Expr
	variadic bool
	// name/named are used only by CALL's argument list, whose named form
	// keeps the name (CallStmt.ArgNames) instead of dropping it.
	name  string
	named bool
}

// appendCallArg adds one argument, reproducing legacy's VARIADIC array
// EXPANSION: `f(VARIADIC array[a,b])` becomes two arguments, each flagged
// variadic, rather than one array argument (internal/parser/select.go
// parseFuncCallTail, :4815-4875). Callers that skip the expansion produce a
// silently different Variadic slice.
func appendCallArg(acc *callArgs, a callArg) *callArgs {
	if acc == nil {
		acc = &callArgs{}
	}
	if a.variadic {
		// `VARIADIC array[a,b]::int[]` — legacy strips the array suffix and
		// pushes the ELEMENT type onto each expanded element as its own cast
		// (select.go:4846-4860), so the cast has to be unwrapped before the
		// expansion and re-applied afterwards.
		elemCast := ""
		expr := a.expr
		if ce, ok := expr.(*parser.CastExpr); ok {
			if _, isArr := ce.Operand.(*parser.ArrayConstructorExpr); isArr {
				elemCast = strings.TrimSuffix(ce.Type.Name, "[]")
				expr = ce.Operand
			}
		}
		if arr, ok := expr.(*parser.ArrayConstructorExpr); ok {
			for _, el := range arr.Elements {
				if elemCast != "" {
					el = parser.NewCastExpr(el.Pos(), el, parser.ObjectName{Name: elemCast}, nil)
				}
				acc.exprs = append(acc.exprs, el)
				acc.variadic = append(acc.variadic, true)
			}
			return acc
		}
	}
	acc.exprs = append(acc.exprs, a.expr)
	acc.variadic = append(acc.variadic, a.variadic)
	return acc
}

// callFuncExpr builds a FuncCall from a callArgs carrier. NewFuncCall fills
// Variadic with one false per argument; the explicit flags replace that.
func callFuncExpr(pos int, name parser.ObjectName, ca *callArgs) *parser.FuncCall {
	if ca == nil {
		return parser.NewFuncCall(pos, name, nil, false)
	}
	fc := parser.NewFuncCall(pos, name, ca.exprs, false)
	fc.Variadic = ca.variadic
	if name.Schema == "" {
		switch strings.ToLower(name.Name) {
		case "substring", "overlay", "position":
			// Legacy builds these as SPECIAL FORMS whichever way they are
			// spelled — `substring(x, 1, 2)` included — and never touches
			// Variadic. The keyword rules cannot own the comma spelling
			// (they would reduce/reduce against this path), so the
			// name-based call blanks it here.
			fc.Variadic = nil
		}
	}
	return fc
}

// eqFold is strings.EqualFold, exposed for grammar actions (the generated
// parser does not import strings).
func eqFold(a, b string) bool { return strings.EqualFold(a, b) }

// opClassRef is a per-column operator class plus whether it carried an option
// list. Legacy surfaces the option-carrying case separately, in
// CreateIndexStmt.OpClassWithOptions.
type opClassRef struct {
	name        string
	withOptions bool
}

// indexElem is one CREATE INDEX key column: either a NAME or an EXPRESSION,
// plus its per-column ordering options. Legacy keeps Columns / ColExprs /
// ColOrders as three PARALLEL slices, with Columns[i]=="" marking an
// expression column (internal/parser/ddl.go).
type indexElem struct {
	name  string
	expr  parser.Expr
	order parser.IndexColOrder
	// optsOpClass is the opclass NAME when it carried an option list
	// (`int4_ops(foo=1)`). CreateIndexStmt records only the first such name,
	// in OpClassWithOptions; the options themselves are discarded, as in legacy.
	optsOpClass string
}

// newIndexElem classifies a parsed key item. Parsing every item as an a_expr
// and classifying afterwards avoids the ColId-vs-expression ambiguity a
// two-alternative rule would introduce — the same trick arbiterFromExprs uses.
//
// A collation may arrive either from index_col's own opt_index_collate (the
// name form) or, for the parenthesised expression form, as a CollateExpr that
// a_expr already absorbed; legacy records both as ColOrders[i].Collation
// rather than as a CollateExpr node, so the wrapper is unwrapped here.
func newIndexElem(e parser.Expr, collation string, opclass opClassRef, desc bool, nullsFirst *bool) indexElem {
	el := indexElem{order: parser.IndexColOrder{Descending: desc, OpClass: opclass.name, Collation: collation}}
	if opclass.withOptions {
		el.optsOpClass = opclass.name
	}
	if ce, ok := e.(*parser.CollateExpr); ok {
		el.order.Collation = ce.CollationName
		e = ce.Operand
	}
	if cr, ok := e.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
		el.name = cr.Column
	} else {
		el.expr = e
	}
	// DESC implies NULLS FIRST unless the clause says otherwise (PG default).
	el.order.NullsFirst = desc
	if nullsFirst != nil {
		el.order.NullsFirst = *nullsFirst
	}
	return el
}

// arbiterFromExprs turns an `ON CONFLICT ( ... )` item list into the legacy
// column/expression split. Parsing every item as an a_expr and classifying
// afterwards avoids the ColId-vs-a_expr ambiguity a two-alternative item rule
// would introduce: a bare unqualified ColumnRef IS the column form
// (Columns[i]=name, Exprs[i]=nil), anything else is an expression column
// (Columns[i]="", Exprs[i]=expr) — internal/parser/dml.go
// parseConflictTargetColumnList.
func arbiterFromExprs(items []parser.Expr) *parser.OnConflictTarget {
	cols := make([]string, len(items))
	exprs := make([]parser.Expr, len(items))
	for i, e := range items {
		// `key COLLATE "C"` in an arbiter: legacy keeps the bare column.
		if ce, ok := e.(*parser.CollateExpr); ok {
			e = ce.Operand
		}
		if cr, ok := e.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
			cols[i] = cr.Column
			continue
		}
		exprs[i] = e
	}
	t := parser.NewOnConflictTarget(cols, "", nil)
	t.Exprs = exprs
	return t
}

// onConflictColumnTarget builds an `ON CONFLICT (cols)` arbiter with the Exprs
// slice legacy keeps PARALLEL to Columns — one entry per column, nil for a
// plain name, non-nil only for an expression column (internal/parser/dml.go
// parseConflictTargetColumnList). Leaving it nil made every column arbiter
// diverge as Exprs=∅ vs legacy's [∅]; NewOnConflictTarget's third parameter is
// `where`, not the expression list, so it has to be filled in afterwards.
func onConflictColumnTarget(cols []string) *parser.OnConflictTarget {
	t := parser.NewOnConflictTarget(cols, "", nil)
	t.Exprs = make([]parser.Expr, len(cols))
	return t
}

// mergeTxModes folds a later transaction_mode_item into the accumulated set.
// `BEGIN ISOLATION LEVEL SERIALIZABLE, READ ONLY` used to keep only the first
// item because tx_mode_list's recursive alternative returned $1 unchanged, so
// every mode after the comma was silently dropped on the routed path. Later
// items win, matching legacy's sequential assignment (internal/parser/ddl.go
// transaction-mode loop); READ WRITE / NOT DEFERRABLE carry the zero value and
// therefore correctly leave the accumulated flags alone.
func mergeTxModes(acc, next *txModes) *txModes {
	if next == nil {
		return acc
	}
	if acc == nil {
		return next
	}
	if next.iso != "" {
		acc.iso = next.iso
	}
	if next.ro {
		acc.ro = true
	}
	if next.def {
		acc.def = true
	}
	return acc
}

type partBound struct {
	from, to, inVals []parser.Expr
	isDefault        bool
	// modulus/remainder carry the HASH partition bound
	// (`FOR VALUES WITH (modulus 4, remainder 0)`); PartitionOfClause keeps
	// them as a third mutually exclusive shape alongside IN and FROM/TO.
	modulus, remainder int64
	isHash             bool
}

// truncTargets carries TRUNCATE's relation list, whose entries each have their
// own ONLY flag — unlike drop_name_list, which is a bare name list.
type truncTargets struct {
	names []parser.ObjectName
	only  []bool
}

// sortUsingIsDesc mirrors the legacy parser's sortUsingIsDesc
// (internal/parser/select.go:1810). Real PostgreSQL never guesses a direction
// here — it resolves the operator against the opclass at analysis time — but
// legacy stamps SortBy.Desc from the operator's spelling, and the AST is the
// migration's contract, so the heuristic is reproduced verbatim.
func sortUsingIsDesc(op string) bool {
	if op == ">" || op == ">=" || op == "~>~" || op == "~>=~" {
		return true
	}
	lower := strings.ToLower(op)
	if idx := strings.LastIndex(lower, "."); idx >= 0 {
		lower = lower[idx+1:]
	}
	return strings.HasSuffix(lower, "gt") || strings.HasSuffix(lower, "greater") || strings.Contains(lower, "_gt_")
}

// tableNotNull carries a table-level NOT NULL constraint element. Its three
// fields land in CreateTableStmt's parallel TableNotNullNames /
// TableNotNullCols / TableNotNullNoInherit slices; an unnamed one contributes
// an empty string to Names rather than being skipped, so the slices stay
// index-aligned.
type tableNotNull struct {
	name      string
	col       string
	noInherit bool
}

// prefixOp maps a prefix operator token to its OpCode. Legacy's prefix set is
// exactly {-, +, NOT, ~} (internal/parser/select.go:3005-3031); '-', '+' and
// NOT reach the grammar as their own terminals, so '~' is the only spelling
// that arrives here. Anything else is rejected rather than silently accepted:
// widening past legacy is a behaviour change, not a port.
func prefixOp(l yyLexer, s string) parser.OpCode {
	if s == "~" {
		return parser.OpBitNot
	}
	if ls, ok := l.(*lexerState); ok && ls.err == nil {
		ls.err = &parser.SyntaxError{Message: fmt.Sprintf("operator does not exist: %s", s), Raw: true, Pos: ls.lastConsumedPos()}
	}
	return parser.OpUnknown
}

// partKey is one entry of a PARTITION BY key list.
type partKey struct {
	name      string      // plain column key; "" for an expression key
	pos       int         // byte offset of the column token; 0 for an expression key
	expr      parser.Expr // expression key; nil for a plain column key
	opClass   string
	collation string
}

// newPartKey classifies one part_elem the way newIndexElem classifies an index
// key: a bare ColumnRef is a COLUMN key (recorded by name, with its position
// for M0134-0016b errposition), anything else is an EXPRESSION key. A COLLATE
// that rode in on the expression is unwrapped into its own field, because
// legacy records it in Collations rather than as a CollateExpr.
func newPartKey(e parser.Expr, pos int, collation, opClass string) partKey {
	k := partKey{collation: collation, opClass: opClass}
	if ce, ok := e.(*parser.CollateExpr); ok {
		k.collation = ce.CollationName
		e = ce.Operand
	}
	if cr, ok := e.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
		k.name, k.pos = cr.Column, pos
	} else {
		k.expr = e
	}
	return k
}

// partitionByFrom assembles the five parallel slices PartitionByClause keeps.
func partitionByFrom(method string, methodPos int, keys []partKey) *parser.PartitionByClause {
	pb := parser.NewPartitionByClause(upperIdent(method), nil)
	pb.MethodPos = methodPos
	for _, k := range keys {
		pb.KeyCols = append(pb.KeyCols, k.name)
		pb.KeyColPos = append(pb.KeyColPos, k.pos)
		pb.KeyExprs = append(pb.KeyExprs, k.expr)
		pb.OpClasses = append(pb.OpClasses, k.opClass)
		pb.Collations = append(pb.Collations, k.collation)
	}
	return pb
}

// specialFormCall builds a call that the grammar SYNTHESISES rather than
// parses as a function call — TRIM's btrim/ltrim/rtrim, AT TIME ZONE's
// timezone(). It cannot go through NewFuncCall: that ctor appends one Variadic
// flag per argument to match legacy's general function-call path, but legacy
// builds these special forms directly and never touches Variadic, so it stays
// nil. canonDump distinguishes nil from []bool{false,false}, so using the
// general ctor here diverges even when every form parses.
func specialFormCall(pos int, name string, args []parser.Expr) *parser.FuncCall {
	fc := parser.NewFuncCall(pos, parser.ObjectName{Name: name}, args, false)
	fc.Variadic = nil
	return fc
}

// tzZone reproduces legacy's AT TIME ZONE special case: a zone written as
// `INTERVAL '<body>'` degrades to a plain StringConst of that body rather than
// staying an interval literal (internal/parser/select.go:2442). The check is
// pointer identity against the most recent INTERVAL SCONST reduction, so a
// zone that merely CONTAINS an interval — `AT TIME ZONE 'UTC' + interval '1
// day'`, whose zone is a BinaryOp — is left alone.
func tzZone(l yyLexer, zone parser.Expr) parser.Expr {
	ls, ok := l.(*lexerState)
	if !ok || ls.lastIntervalNode == nil || ls.lastIntervalNode != zone {
		return zone
	}
	return parser.NewStringConst(zone.Pos(), ls.lastIntervalRaw)
}

// ctasSrc carries CREATE TABLE ... AS's source, which is either a query or a
// prepared statement. Exactly one field is non-nil.
type ctasSrc struct {
	sel  *parser.SelectStmt
	exec *parser.ExecuteStmt
}

// intoWrap turns `SELECT ... INTO name` into the CreateTableStmt legacy builds
// for it. The INTO target was recorded against the simple_select's SelectStmt
// (see lexerState.intoFor); the wrap happens here, at the SelectStmt rule, so
// the captured query already carries ORDER BY / LIMIT.
func intoWrap(l yyLexer, s parser.Stmt) parser.Stmt {
	ls, ok := l.(*lexerState)
	if !ok || ls.intoFor == nil {
		return s
	}
	sel, _ := s.(*parser.SelectStmt)
	if sel == nil {
		return s
	}
	tgt, ok := ls.intoFor[sel]
	if !ok {
		return s
	}
	delete(ls.intoFor, sel)
	ct := parser.NewCreateTableStmt(0, tgt, nil, nil)
	ct.SelectSource = sel
	return ct
}

// parenTail is what follows a parenthesised left operand — a set operation,
// or a trailing ORDER BY / LIMIT / OFFSET.
type parenTail struct {
	op      *opSpec
	right   *parser.SelectStmt
	orderBy []parser.SortBy
	limit   parser.Expr
	offset  parser.Expr
}

// parenGroup reproduces legacy parseParenthesisedSelectStmt's wrapper
// (select.go:1043): the parenthesised query is stamped and hung under a FRESH
// node as SetOpOperand, and everything written after the ')' attaches to that
// node — never into the operand. A bare (unparenthesised) right branch of the
// set operation gives up its trailing ORDER BY / LIMIT / OFFSET to the wrapper,
// the same lift foldSetOps applies.
func parenGroup(pos int, inner *parser.SelectStmt, t *parenTail) *parser.SelectStmt {
	inner.Parenthesized = true
	grp := parser.NewSelectStmt(pos)
	grp.SetOpOperand = inner
	if t.op != nil {
		grp.SetOp = parser.NewSetOpClause(t.op.pos, t.op.typ, t.op.all, t.right)
		if r := t.right; r != nil && !r.Parenthesized {
			if grp.OrderBy == nil && r.OrderBy != nil {
				grp.OrderBy, r.OrderBy = r.OrderBy, nil
			}
			if grp.Limit == nil && r.Limit != nil {
				grp.Limit, r.Limit = r.Limit, nil
			}
			if grp.Offset == nil && r.Offset != nil {
				grp.Offset, r.Offset = r.Offset, nil
			}
		}
		return grp
	}
	grp.OrderBy, grp.Limit, grp.Offset = t.orderBy, t.limit, t.offset
	return grp
}

// insRest carries INSERT's optional column list together with its source.
type insRest struct {
	cols []string
	src  *insSrc
}

// quantifiedAny builds `x op ANY|SOME (...)` the way legacy does
// (select.go:2300-2323, parseAnyTail): `= ANY` IS `IN` (AnyOp stays
// OpUnknown), `<> ANY` / `!= ANY` is the IN shape flagged NotEqualAny (an OR of
// inequalities — deliberately NOT NOT IN, which is an AND), and any other
// operator is carried as AnyOp. The same desugaring applies whether the right
// side is a list or a subquery; before this helper only the list path did it.
func quantifiedAny(l yyLexer, pos int, left parser.Expr, op parser.OpCode, sub *parser.SelectStmt, list []parser.Expr, listPos int) parser.Expr {
	anyOp := op
	notEq := false
	switch op {
	case parser.OpEq:
		anyOp = parser.OpUnknown
	case parser.OpNe:
		anyOp, notEq = parser.OpUnknown, true
	}
	ie := parser.NewInExpr(pos, left, false, anyOp, false, sub, unwrapAnyArray(l, list, listPos))
	ie.NotEqualAny = notEq
	return ie
}

// unwrapAnyArray reproduces parseAnyTail's treatment of `ANY (ARRAY[...])`
// (select.go:2574-2606): when the right side is a DIRECT array constructor,
// legacy splices its elements into InExpr.List and DISCARDS any trailing
// `::type[]` casts. Legacy's test is TOKEN-literal — the two tokens after the
// '(' are ARRAY and '[' — so `((ARRAY[1]))` stays an ordinary expression
// there. The AST alone cannot tell those apart (the grammar's `'(' a_expr ')'`
// leaves no node), which is why the check goes back to the token stream at
// listPos, the position of the list's first terminal.
func unwrapAnyArray(l yyLexer, list []parser.Expr, listPos int) []parser.Expr {
	if len(list) != 1 {
		return list
	}
	if ls, ok := l.(*lexerState); ok && !ls.directArrayAt(listPos) {
		return list
	}
	e := list[0]
	for {
		c, ok := e.(*parser.CastExpr)
		if !ok {
			break
		}
		e = c.Operand
	}
	if ac, ok := e.(*parser.ArrayConstructorExpr); ok && len(ac.Elements) > 0 {
		return ac.Elements
	}
	return list
}

// copyColConstraints moves the constraint carrier's per-column fields onto a
// ColumnDef. It is the ONE place both CREATE TABLE and ALTER TABLE ADD COLUMN
// go through for the fields added since 2026-08-27, so the two siblings cannot
// drift again the way identity did.
func copyColConstraints(cd *parser.ColumnDef, collation, compression, nnName, uqName, checkName string, nnNoInherit, checkNoInherit bool, checkNotEnforced bool, storage string) {
	cd.Collation, cd.Compression = collation, compression
	cd.NotNullConstraintName, cd.UniqueConstraintName, cd.CheckConstraintName = nnName, uqName, checkName
	cd.NotNullNoInherit, cd.CheckNoInherit = nnNoInherit, checkNoInherit
	cd.CheckNotEnforced, cd.Storage = checkNotEnforced, storage
}

// partOfElem is one entry of `CREATE TABLE c PARTITION OF p ( ... )`. Legacy
// keeps these on the PartitionOfClause rather than as columns (ddl.go:3290-
// 3410): a named CHECK, and per-column NOT NULL / UNIQUE / DEFAULT / GENERATED
// overrides. An anonymous CHECK is accepted and DROPPED there.
type partOfElem struct {
	col       string
	notNull   bool
	unique    bool
	def       parser.Expr
	hasDef    bool
	genExpr   string
	hasGen    bool
	checkName string
	checkExpr string
	hasCheck  bool
}

func applyPartOfElems(poc *parser.PartitionOfClause, elems []*partOfElem) {
	seen := map[string]bool{}
	for _, e := range elems {
		// Legacy flags the FIRST column that is listed twice (DuplicateColumn)
		// and lets the executor reject it.
		if !e.hasCheck && e.col != "" {
			if seen[e.col] && poc.DuplicateColumn == "" {
				poc.DuplicateColumn = e.col
			}
			seen[e.col] = true
		}
		switch {
		case e.hasCheck:
			if e.checkName != "" {
				poc.CheckConstraints = append(poc.CheckConstraints, parser.PartitionCheckConstraint{Name: e.checkName, Expr: e.checkExpr})
			}
		default:
			if e.notNull {
				poc.NotNullColumns = append(poc.NotNullColumns, e.col)
			}
			if e.unique {
				poc.UniqueColumns = append(poc.UniqueColumns, e.col)
			}
			if e.hasDef {
				poc.ColDefaults = append(poc.ColDefaults, parser.PartitionColDefault{ColName: e.col, Expr: e.def})
			}
			if e.hasGen {
				poc.ColGeneratedExprs = append(poc.ColGeneratedExprs, parser.PartitionColGenerated{ColName: e.col, Expr: e.genExpr})
			}
		}
	}
}

// tokenJoinLower reproduces legacy's token-join expression text
// (joinGeneratedExprTokens / the PARTITION OF check capture): every token's
// decoded Value between the two absolute positions, space-separated, keywords
// and identifiers lower-cased, string literals kept as their content without
// quotes — `upper ( a ) = X`.
func tokenJoinLower(l yyLexer, from, to int) string {
	ls, ok := l.(*lexerState)
	if !ok {
		return ""
	}
	var parts []string
	for _, tk := range ls.toks {
		if tk.Pos < from || tk.Pos >= to {
			continue
		}
		v := tk.Value
		if tk.Kind != parser.TokenStringLit {
			v = strings.ToLower(v)
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, " ")
}

// parenExcludeElem reproduces legacy's reading of a PARENTHESISED exclusion
// element, `(c2::circle) WITH &&` (ddl.go, the EXCLUDE token loop): it never
// parses the expression — it records every identifier token inside the parens
// as a column name and takes the FIRST operator token it meets as the
// element's operator, so `(c2::circle) WITH &&` yields Columns [c2 circle] and
// ExclusionOp "::". Reproduced from the token stream, bug for bug, because the
// AST is the migration's contract.
func parenExcludeElem(l yyLexer, from, to int, withOp string) excludeElem {
	el := excludeElem{op: withOp, cols: []string{}}
	ls, ok := l.(*lexerState)
	if !ok {
		return el
	}
	firstOp := ""
	for _, tk := range ls.toks {
		if tk.Pos < from || tk.Pos >= to {
			continue
		}
		switch tk.Kind {
		case parser.TokenIdent, parser.TokenKeyword:
			el.cols = append(el.cols, strings.ToLower(tk.Value))
		case parser.TokenOperator:
			if firstOp == "" {
				firstOp = tk.Value
			}
		}
	}
	if firstOp != "" {
		el.op = firstOp
	}
	return el
}

// bitStringConst reproduces legacy decodeBitStringLit (expr.go:54): the
// lexer's Value carries a marker byte ('b' or 'x') ahead of the body; a binary
// body is kept verbatim and a hex body is expanded to four bits per digit.
// Both become a plain StringConst.
func bitStringConst(pos int, v string) parser.Expr {
	if v == "" {
		return parser.NewStringConst(pos, "")
	}
	marker, body := v[0], v[1:]
	if marker != 'x' {
		return parser.NewStringConst(pos, body)
	}
	var out strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		var d byte
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		}
		for bit := 3; bit >= 0; bit-- {
			if d&(1<<uint(bit)) != 0 {
				out.WriteByte('1')
			} else {
				out.WriteByte('0')
			}
		}
	}
	return parser.NewStringConst(pos, out.String())
}

// mergeCtTail folds one CREATE TABLE trailing clause into the accumulated
// tail: option lists and INHERITS append, PARTITION BY / PARTITION OF replace.
func mergeCtTail(dst, src *ctTail) *ctTail {
	dst.withKv = append(dst.withKv, src.withKv...)
	dst.inherits = append(dst.inherits, src.inherits...)
	if src.partition != nil {
		dst.partition = src.partition
	}
	if src.partOf.Name != "" {
		dst.partOf = src.partOf
		dst.fromVals, dst.toVals, dst.inVals, dst.bDefault = src.fromVals, src.toVals, src.inVals, src.bDefault
		dst.modulus, dst.remainder, dst.isHash = src.modulus, src.remainder, src.isHash
		dst.partOfElems = src.partOfElems
	}
	if src.onCommit != "" {
		dst.onCommit = src.onCommit
	}
	return dst
}

// viewReloptions applies a CREATE VIEW `WITH (...)` list the way legacy's
// parseViewOptions does: security_barrier / security_invoker become *bool
// (present without a value = true; a value goes through the reloption boolean
// spellings), everything else is ignored.
func viewReloptions(cv *parser.CreateViewStmt, kvs []string) {
	for _, kv := range kvs {
		name, val, hasVal := kv, "", false
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name, val, hasVal = kv[:i], kv[i+1:], true
		}
		b := true
		if hasVal {
			switch strings.ToLower(val) {
			case "true", "on", "yes", "1", "t", "y":
				b = true
			default:
				b = false
			}
		}
		switch strings.ToLower(name) {
		case "security_barrier":
			v := b
			cv.SecurityBarrier = &v
		case "security_invoker":
			v := b
			cv.SecurityInvoker = &v
		}
	}
}

// nonNilExprs is the ARRAY[...] element-list convention: an empty constructor
// carries an empty slice, never nil.
func nonNilExprs(e []parser.Expr) []parser.Expr {
	if e == nil {
		return []parser.Expr{}
	}
	return e
}

// insertTarget reproduces a legacy quirk of `INSERT INTO t AS alias (cols)`:
// with an alias present, the column list is recorded as the RangeVar's
// alias column list and InsertStmt.Columns stays nil.
func insertTarget(q qname, alias string, cols []string) (parser.RangeVar, []string) {
	rv := rangeVarFromName(q, alias)
	if alias != "" && cols != nil {
		rv.Columns = cols
		return rv, nil
	}
	return rv, cols
}

// joinCheckTokens is legacy parseCheckExpr's capture: a PLAIN space join of the
// tokens between the parens — no spacing rules at all (`( y ) . a > 0`,
// `a in ( 1 , 2 )`) — with string literals re-quoted. Distinct from the
// generated-column join (joinLegacyTokens) and from the partition-of check
// join (tokenJoinLower, which drops the quotes).
func joinCheckTokens(l yyLexer, from, to int) string {
	ls, ok := l.(*lexerState)
	if !ok {
		return ""
	}
	var parts []string
	for _, tk := range ls.toks {
		if tk.Pos <= from || tk.Pos >= to {
			continue
		}
		if tk.Kind == parser.TokenStringLit {
			parts = append(parts, "'"+strings.ReplaceAll(tk.Value, "'", "''")+"'")
		} else {
			parts = append(parts, tk.Value)
		}
	}
	return strings.Join(parts, " ")
}

// tblCheckTail carries a table-level CHECK's trailer words.
type tblCheckTail struct {
	noInherit, notEnforced bool
}

// checkOpt carries CREATE VIEW's WITH ... CHECK OPTION plus the byte position
// of its WITH, so RawDef can stop there the way legacy's span does.
type checkOpt struct {
	opt string
	pos int
}

// multiSetRHS reproduces legacy's reading of a multi-column SET's right-hand
// side: a parenthesised list is always a RowExpr there, even with one element,
// while the expression grammar collapses `(1)` to `1`. The token at the RHS's
// first position says which was written.
func multiSetRHS(l yyLexer, e parser.Expr, pos int) parser.Expr {
	switch e.(type) {
	case *parser.RowExpr, *parser.SubqueryExpr:
		return e
	}
	if ls, ok := l.(*lexerState); ok {
		for _, tk := range ls.toks {
			if tk.Pos == pos {
				if tk.Kind == parser.TokenSymbol && tk.Value == "(" {
					return parser.NewRowExpr(pos, []parser.Expr{e})
				}
				break
			}
		}
	}
	return e
}

// explainOpt is one `name [value]` entry of an EXPLAIN option list.
type explainOpt struct {
	name  string
	value string // "" = no value written
	has   bool
}

// applyExplainOpts reproduces legacy parseExplainOneOption: each boolean
// option sets its flag AND its Set marker (absent value = true, PG's boolean
// spellings otherwise), FORMAT requires TEXT/XML/JSON/YAML, and an unknown
// option or value is the same syntax error legacy raises.
func applyExplainOpts(l yyLexer, pos int, opts []*explainOpt) parser.ExplainOptions {
	var o parser.ExplainOptions
	fail := func(msg string) {
		if ls, ok := l.(*lexerState); ok && ls.err == nil {
			ls.err = &parser.SyntaxError{Message: msg, Raw: true, Pos: pos}
		}
	}
	for _, e := range opts {
		name := strings.ToLower(e.name)
		if name == "format" {
			if !e.has {
				fail("FORMAT requires a value (TEXT, XML, JSON, or YAML)")
				continue
			}
			switch strings.ToLower(e.value) {
			case "text":
				o.Format = parser.ExplainFormatText
			case "json":
				o.Format = parser.ExplainFormatJSON
			case "xml":
				o.Format = parser.ExplainFormatXML
			case "yaml":
				o.Format = parser.ExplainFormatYAML
			default:
				fail(fmt.Sprintf("unsupported FORMAT %q (TEXT, XML, JSON, or YAML only)", e.value))
				continue
			}
			o.Set.Format = true
			continue
		}
		val := true
		if e.has {
			switch strings.ToLower(e.value) {
			case "true", "on", "1", "yes", "t", "y":
				val = true
			case "false", "off", "0", "no", "f", "n":
				val = false
			default:
				fail(fmt.Sprintf("invalid value for EXPLAIN option %q: %q", e.name, e.value))
				continue
			}
		}
		switch name {
		case "analyze", "analyse":
			o.Analyze, o.Set.Analyze = val, true
		case "verbose":
			o.Verbose, o.Set.Verbose = val, true
		case "costs":
			o.Costs, o.Set.Costs = val, true
		case "buffers":
			o.Buffers, o.Set.Buffers = val, true
		case "settings":
			o.Settings, o.Set.Settings = val, true
		case "timing":
			o.Timing, o.Set.Timing = val, true
		case "summary":
			o.Summary, o.Set.Summary = val, true
		case "generic_plan":
			o.GenericPlan, o.Set.GenericPlan = val, true
		case "wal":
			o.Wal, o.Set.Wal = val, true
		case "memory":
			o.Memory, o.Set.Memory = val, true
		default:
			fail(fmt.Sprintf("unknown EXPLAIN option %q", e.name))
		}
	}
	return o
}

// atConstrTail carries an ALTER TABLE constraint's trailing words. They are one
// FLAT left-recursive list rather than opt_constr_attrs + opt_not_valid,
// because NOT is followed by DEFERRABLE, VALID or ENFORCED and a two-part tail
// cannot tell them apart with one token of lookahead.
type atConstrTail struct {
	deferrable, initiallyDeferred bool
	hasDeferrability              bool
	notValid                      bool
	enforced, hasEnforceability   bool
	noInherit, hasInheritability  bool
}

func mergeATTail(t *atConstrTail, w string) *atConstrTail {
	if t == nil {
		t = &atConstrTail{}
	}
	switch w {
	case "deferrable":
		t.deferrable, t.hasDeferrability = true, true
	case "not_deferrable":
		t.deferrable, t.initiallyDeferred, t.hasDeferrability = false, false, true
	case "initially_deferred":
		t.deferrable, t.initiallyDeferred, t.hasDeferrability = true, true, true
	case "initially_immediate":
		t.initiallyDeferred, t.hasDeferrability = false, true
	case "not_valid":
		t.notValid = true
	case "not_enforced":
		t.enforced, t.hasEnforceability = false, true
	case "enforced":
		t.enforced, t.hasEnforceability = true, true
	case "no_inherit":
		t.noInherit, t.hasInheritability = true, true
	case "inherit":
		t.noInherit, t.hasInheritability = false, true
	}
	return t
}

// atAction builds a bare ALTER TABLE action of the given kind.
func atAction(k parser.AlterTableActionKind) *parser.AlterTableAction {
	return parser.NewATAction(k)
}

// applyATTail copies the trailing words onto the action.
func applyATTail(a *parser.AlterTableAction, t *atConstrTail) *parser.AlterTableAction {
	if t == nil {
		return a
	}
	a.Deferrable, a.InitiallyDeferred = t.deferrable, t.initiallyDeferred
	a.NotValid, a.NoInherit = t.notValid, t.noInherit
	if t.hasEnforceability && !t.enforced {
		a.FKNotEnforced = true
	}
	return a
}

// applyATCheckTail is applyATTail for a CHECK, whose enforcement flag lives in
// CheckNotEnforced rather than FKNotEnforced.
func applyATCheckTail(a *parser.AlterTableAction, t *atConstrTail) *parser.AlterTableAction {
	applyATTail(a, t)
	if t != nil {
		a.FKNotEnforced = false
		a.CheckNotEnforced = t.hasEnforceability && !t.enforced
	}
	return a
}

// applyAlterConstraint maps the same tail onto ALTER CONSTRAINT's own flags,
// each category carrying its own "was written" marker.
func applyAlterConstraint(a *parser.AlterTableAction, t *atConstrTail) *parser.AlterTableAction {
	if t == nil {
		return a
	}
	a.AlterConstraintDeferrable = t.deferrable
	a.AlterConstraintInitiallyDeferred = t.initiallyDeferred
	a.AlterConstraintHasDeferrability = t.hasDeferrability
	a.AlterConstraintEnforced = t.enforced
	a.AlterConstraintHasEnforceability = t.hasEnforceability
	a.AlterConstraintNoInherit = t.noInherit
	a.AlterConstraintHasInheritability = t.hasInheritability
	return a
}

// atStatValue renders SET STATISTICS' number the way legacy stores it — as a
// STRING in CheckExpr, not a number field.
func atStatValue(n int64) string { return strconv.FormatInt(n, 10) }

// mustATTail coerces the boxed (possibly nil) tail carrier.
func mustATTail(v any) *atConstrTail {
	t, _ := v.(*atConstrTail)
	return t
}

// fnAttrs accumulates CREATE FUNCTION / PROCEDURE's attribute list. Legacy
// applies them in written order onto one statement, so the carrier mirrors the
// statement's own fields rather than a list of clauses.
type fnAttrs struct {
	// sawLanguage / twoItemAs / errMsg / errRaw carry the bookkeeping legacy's
	// option loop keeps in local variables (function.go parseCreateFunction).
	sawLanguage bool
	twoItemAs   bool
	errMsg      string
	errRaw      bool
	errPos      int
	language        string
	body            string
	hasBody         bool
	beginAtomic     bool
	isReturnForm    bool
	strict          bool
	volatility      string
	securityDefiner bool
	leakproof       bool
	window          bool
	parallel        string
	cost            string
	rows            string
	configOps       []parser.FunctionConfigOp
}

func newFnAttrs() *fnAttrs { return &fnAttrs{volatility: "v", parallel: "u"} }

// fnAttrErr records the FIRST attribute-list error legacy would have raised.
// The grammar cannot fail mid-list (a reduce has already happened by then), so
// the error is carried to the statement rule and raised there.
func (a *fnAttrs) fail(msg string, raw bool, pos int) {
	if a.errMsg == "" {
		a.errMsg, a.errRaw, a.errPos = msg, raw, pos
	}
}

// fnAttrsCheck reports the error a completed CREATE FUNCTION / PROCEDURE
// attribute list carries, or "" when it is well formed. Two of these appear in
// the create_function_sql regress case and are raised by legacy AFTER a
// successful parse, so the grammar has to reproduce them explicitly:
//
//   - a body is MANDATORY ("expected AS $$body$$ ..."): legacy's option loop
//     falls into its default arm at end of input and rejects when it never saw
//     one.
//   - `AS 'a', 'b'` is only legal for LANGUAGE C ("only one AS item needed for
//     language %q"), with an unset language reported as "sql".
func fnAttrsCheck(l yyLexer, a *fnAttrs, what string) (string, bool, int) {
	if a.errMsg != "" {
		return a.errMsg, a.errRaw, a.errPos
	}
	if !a.hasBody {
		// Legacy stops on the token that ended the option loop, which for a
		// well-formed statement is the EOF/';' one.
		return "expected AS $$body$$ for CREATE " + what + " (got end of input)", false, endPos(l)
	}
	if a.twoItemAs {
		lang := strings.ToLower(a.language)
		if lang == "" {
			lang = "sql"
		}
		if lang != "c" {
			return fmt.Sprintf("only one AS item needed for language %q", lang), true, endPos(l)
		}
	}
	return "", false, 0
}

// endPos is the byte offset legacy reports for an error raised at end of
// statement: the position of the terminating token.
func endPos(l yyLexer) int {
	st, ok := l.(*lexerState)
	if !ok || len(st.toks) == 0 {
		return 0
	}
	return st.toks[len(st.toks)-1].Pos
}

func mustFnAttrs(v any) *fnAttrs {
	a, _ := v.(*fnAttrs)
	if a == nil {
		a = newFnAttrs()
	}
	return a
}

// applyFnAttrs copies the accumulated attributes onto a CREATE FUNCTION.
func applyFnAttrs(st *parser.CreateFunctionStmt, a *fnAttrs) *parser.CreateFunctionStmt {
	st.Language, st.Body = a.language, a.body
	st.BeginAtomic, st.IsReturnForm = a.beginAtomic, a.isReturnForm
	st.Strict, st.Volatile = a.strict, a.volatility
	st.SecurityDefiner, st.Leakproof, st.Window = a.securityDefiner, a.leakproof, a.window
	st.Parallel, st.Cost, st.Rows = a.parallel, a.cost, a.rows
	st.ConfigOps = a.configOps
	return st
}

// applyProcAttrs is the procedure's subset — CreateProcedureStmt has no
// Leakproof / Parallel / Cost / Rows / ConfigOps fields.
func applyProcAttrs(st *parser.CreateProcedureStmt, a *fnAttrs) *parser.CreateProcedureStmt {
	// CREATE PROCEDURE's attribute loop (function.go parseCreateProcedureTail)
	// handles LANGUAGE / AS / BEGIN ATOMIC / SET explicitly and sends every
	// other attribute through consumeFunctionAttribute, which DROPS it — only
	// WINDOW and STRICT are peeked at on the way past. So SECURITY DEFINER,
	// the volatility words, COST and ROWS leave no trace on a procedure, and
	// Volatile stays at its "v" default.
	st.Language, st.Body = a.language, a.body
	st.BeginAtomic = a.beginAtomic
	st.Strict, st.Window = a.strict, a.window
	return st
}

// tableArgs turns a RETURNS TABLE (...) column list into the trailing OUT
// arguments legacy records it as.
func tableArgs(base []parser.FunctionArg, cols []parser.FunctionArg) []parser.FunctionArg {
	for _, c := range cols {
		c.Mode, c.ModeExplicit = parser.FuncArgOut, true
		base = append(base, c)
	}
	return base
}

// fnConfigUnset is the sentinel a SET clause returns for the two forms legacy
// records NO config op for: `SET x FROM CURRENT` and `SET x TO DEFAULT`
// (function.go parseFunctionConfigSetClause returns ok=false for both).
const fnConfigUnset = "\x00\x00unset"

// fnReturn carries the three shapes of a RETURNS clause: a plain type, SETOF
// that type, or TABLE (cols) — which legacy folds into trailing OUT arguments.
type fnReturn struct {
	typ   parser.ColumnType
	setof bool
	table bool
	cols  []parser.FunctionArg
}

// colTypeOf converts the grammar's type carrier to the AST's column type.
// cast_typename folds array brackets into the name (castType.withArrays), but
// ColumnType keeps them in a separate flag, so they are split back out here.
func colTypeOf(tw *typeWithArgs) parser.ColumnType {
	name, args := tw.ct.name, tw.args
	if tw.ct.schema == "" {
		// FLOAT [ (p) ] -> float4/float8 with the precision folded into the
		// NAME (gram.y opt_float; ddl.go normalizeFloatTypeName). A schema
		// qualifier means a user type that merely happens to be called float.
		name, args, _ = parser.NormalizeFloatTypeName(name, args)
	}
	elem, isArray := trimArrayTail(name)
	return parser.NewColumnType(tw.ct.schema, elem, args, isArray)
}

// parallelCode maps PARALLEL's word to pg_proc.proparallel's one-byte code.
func parallelCode(s string) string {
	switch strings.ToLower(s) {
	case "safe":
		return "s"
	case "restricted":
		return "r"
	default:
		return "u"
	}
}

// fnDropBehavior mirrors parseDropFunctionTail's switch, which maps RESTRICT
// to DropDefault (not DropRestrict) — RESTRICT is already the default and
// legacy does not record it distinctly here.
func fnDropBehavior(s string) parser.DropBehavior {
	if s == "cascade" {
		return parser.DropCascade
	}
	return parser.DropDefault
}

// dropProcNames reduces DROP PROCEDURE's extra targets to bare names:
// DropProcedureStmt keeps additional names only, without their arg lists.
func dropProcNames(extras []parser.DropFunctionItem) []parser.ObjectName {
	if len(extras) == 0 {
		return nil
	}
	out := make([]parser.ObjectName, 0, len(extras))
	for _, e := range extras {
		out = append(out, e.Name)
	}
	return out
}

// namedCallArgs accumulates CALL's argument list. Legacy fills ArgNames only
// when at least one argument was written `name => value`; otherwise the field
// stays nil even though every position has an (empty) name.
type namedCallArgs struct {
	exprs    []parser.Expr
	argNames []string
	hasNamed bool
}

func (c *namedCallArgs) names() []string {
	if c == nil || !c.hasNamed {
		return nil
	}
	return c.argNames
}

func appendNamedCallArg(c *namedCallArgs, a callArg) *namedCallArgs {
	if c == nil {
		c = &namedCallArgs{exprs: []parser.Expr{}, argNames: []string{}}
	}
	c.exprs = append(c.exprs, a.expr)
	c.argNames = append(c.argNames, a.name)
	c.hasNamed = c.hasNamed || a.named
	return c
}

// joinBodyTokens renders every token from byte position `from` to the end of
// the fragment (stopping at a top-level ';') the way legacy's RETURN-form body
// does: tokenBodySQL per token, single spaces between.
func joinBodyTokens(l yyLexer, from int) string {
	st, ok := l.(*lexerState)
	if !ok {
		return ""
	}
	var parts []string
	for _, t := range st.toks {
		if t.Pos < from {
			continue
		}
		if t.Kind == parser.TokenEOF {
			break
		}
		if t.Kind == parser.TokenSymbol && t.Value == ";" {
			break
		}
		parts = append(parts, parser.TokenBodySQL(t))
	}
	return strings.Join(parts, " ")
}

// isDefaultKeywordAt reports whether the token at byte position `pos` is the
// DEFAULT keyword itself rather than an identifier or string that happens to
// spell it. `SET x = DEFAULT` records no config op; `SET x = 'default'` does.
func isDefaultKeywordAt(l yyLexer, pos int) bool {
	st, ok := l.(*lexerState)
	if !ok {
		return false
	}
	for _, t := range st.toks {
		if t.Pos == pos {
			return t.Kind == parser.TokenKeyword && t.Keyword == parser.KwDefault
		}
	}
	return false
}

// exprIdentName recovers the bare identifier a named CALL argument was written
// with. The grammar has to accept a full a_expr on the left of `=>` (a ColId
// there is reduce/reduce-ambiguous with the positional form), so the name is
// extracted afterwards; anything that is not a bare column reference yields ""
// and the argument is treated as positional, which is what legacy does when
// the left side is not a TokenIdent.
func exprIdentName(e parser.Expr) string {
	cr, ok := e.(*parser.ColumnRef)
	if !ok || cr.Schema != "" || cr.Table != "" {
		return ""
	}
	return cr.Column
}

// implicitCharLen reproduces the implicit length-1 typmod a bare char /
// character / nchar / national character carries (ddl.go:5196). Any explicit
// typmod overwrites it; `bpchar` spelled directly is deliberately excluded and
// stays unbounded, exactly as legacy has it.
func implicitCharLen(l yyLexer, pos int, ct castType) []int64 {
	if st, ok := l.(*lexerState); ok {
		for _, t := range st.toks {
			if t.Pos == pos && t.Kind == parser.TokenQuotedIdent {
				// `"char"` names PG's one-byte internal type, not bpchar, and
				// legacy leaves it unbounded (ddl.go:5195 excludes quoted).
				return ct.args
			}
		}
	}
	if len(ct.args) == 0 && ct.schema == "" &&
		(strings.EqualFold(ct.name, "char") || strings.EqualFold(ct.name, "character")) {
		return []int64{1}
	}
	return ct.args
}

// ivQual carries an interval type qualifier between the grammar rule that
// recognises it and the two packers. prec is -1 when no `(p)` was written.
type ivQual struct {
	hi, lo string
	prec   int
}

// colTypmodArgs applies a trailing `( N )` typmod. Interval is the one type
// where the number is not stored raw: legacy packs it with the full range mask
// (packIntervalColumnTypmod), so `interval(3)` and `numeric(3)` differ.
func colTypmodArgs(tw *typeWithArgs, n int64) []int64 {
	if strings.EqualFold(tw.ct.name, "interval") && tw.ct.schema == "" {
		if _, col, ok := parser.IntervalQualTypmods("", "", int(n)); ok {
			return []int64{col}
		}
	}
	return []int64{n}
}

// trimArrayTail splits the `[]` suffixes cast_typename folds into a type name
// back into (element name, isArray) — the shape ColumnType stores.
func trimArrayTail(name string) (string, bool) {
	isArray := false
	for strings.HasSuffix(name, "[]") {
		name, isArray = strings.TrimSuffix(name, "[]"), true
	}
	return name, isArray
}

// tzSetStmt builds `SET [LOCAL] TIME ZONE v`, which legacy records as a plain
// SetStmt named "timezone". DEFAULT is a Default=true statement with an empty
// value, exactly as the generic `SET x = DEFAULT` path has it; LOCAL is an
// ordinary value word here, not a scope.
func tzSetStmt(local bool, v string) parser.Stmt {
	if strings.EqualFold(v, "default") {
		return parser.NewSetStmt(0, local, "timezone", "", true)
	}
	return parser.NewSetStmt(0, local, "timezone", v, false)
}

// fetchSpec carries FETCH/MOVE's direction+count before they collapse into the
// two fields FetchStmt actually stores.
type fetchSpec struct {
	count   int64
	forward bool
	name    string
}

// fetchStmt builds FETCH, or the parsed-and-discarded CompatNoopStmt legacy
// records for MOVE (which reaches the executor as a no-op with an empty body).
func fetchStmt(pos int, v any, isMove bool) parser.Stmt {
	if isMove {
		return parser.NewCompatNoopStmt(pos, "MOVE")
	}
	f, _ := v.(*fetchSpec)
	if f == nil {
		f = &fetchSpec{count: 1, forward: true}
	}
	return parser.NewFetchStmt(pos, f.name, f.count, f.forward)
}

// vacTarget / vacTargets carry VACUUM's and ANALYZE's `rel [(cols)]` list.
// Targets and TargetCols are PARALLEL slices, and a target with no column list
// contributes a nil entry rather than being skipped.
type vacTarget struct {
	name parser.ObjectName
	cols []string
}

type vacTargets struct {
	names []parser.ObjectName
	cols  [][]string
}

func appendVacTarget(t *vacTargets, one *vacTarget) *vacTargets {
	if t == nil {
		t = &vacTargets{}
	}
	t.names = append(t.names, one.name)
	t.cols = append(t.cols, one.cols)
	return t
}

// isFalseWord reports whether an option's value word is the FALSE spelling.
// Legacy treats anything else — including an absent word — as true.
func isFalseWord(s string) bool { return strings.EqualFold(s, "false") }

// vacuumNamedOpt handles the VACUUM options whose names are ordinary
// identifiers rather than keywords (parser.go parseVacuumOptionList). An
// unrecognised name is legacy's "unrecognised VACUUM option" error, but the
// grammar cannot raise from a closure, so it is left as a no-op and the
// statement simply carries no such flag — the same outcome the executor sees
// for an option goopg does not implement.
func vacuumNamedOpt(name, val string) func(*parser.VacuumStmt) {
	switch strings.ToLower(name) {
	// The four keyword options are spelled as their own vacuum_opt_name
	// alternatives (they are type_func_name keywords, not ColIds) but land here
	// with the rest. Their value word is consumed and IGNORED by legacy.
	case "verbose":
		return func(v *parser.VacuumStmt) { v.Verbose = true }
	case "analyze", "analyse":
		return func(v *parser.VacuumStmt) { v.Analyze = true }
	case "full":
		return func(v *parser.VacuumStmt) { v.Full = true }
	case "freeze":
		return func(v *parser.VacuumStmt) { v.Freeze = true }
	case "truncate":
		return func(v *parser.VacuumStmt) { v.NoTruncate = isFalseWord(val) }
	case "parallel":
		n := reloptInt(val)
		return func(v *parser.VacuumStmt) { v.ParallelWorkers = n }
	case "disable_page_skipping":
		return func(v *parser.VacuumStmt) { v.DisablePageSkipping = true }
	case "skip_database_stats":
		return func(v *parser.VacuumStmt) { v.SkipDatabaseStats = true }
	case "only_database_stats":
		return func(v *parser.VacuumStmt) { v.OnlyDatabaseStats = true }
	case "skip_locked":
		return func(v *parser.VacuumStmt) { v.SkipLocked = true }
	case "index_cleanup":
		// Three-valued: true forces cleanup, false suppresses it, and the
		// default "auto" sets neither field.
		switch strings.ToLower(val) {
		case "true":
			return func(v *parser.VacuumStmt) { v.ForceIndexCleanup = true }
		case "false":
			return func(v *parser.VacuumStmt) { v.NoIndexCleanup = true }
		}
		return func(v *parser.VacuumStmt) {}
	case "process_main":
		return func(v *parser.VacuumStmt) { v.NoProcessMain = isFalseWord(val) }
	case "process_toast":
		return func(v *parser.VacuumStmt) { v.NoProcessToast = isFalseWord(val) }
	case "buffer_usage_limit":
		return func(v *parser.VacuumStmt) { v.BufferUsageLimit = val }
	}
	return func(v *parser.VacuumStmt) {}
}

// typeNameOf renders a PREPARE parameter type back to the plain name legacy
// stores in ParamTypes (no schema, no typmod).
func typeNameOf(v any) string {
	tw, _ := v.(*typeWithArgs)
	if tw == nil {
		return ""
	}
	if tw.ct.schema != "" {
		return tw.ct.schema + "." + tw.ct.name
	}
	return tw.ct.name
}

// qnText renders a qualified name the way REINDEX's single Name field wants it.
func qnText(q qname) string {
	return strings.Join(q.parts, ".")
}

// vacuumAt re-creates the VACUUM carrier at the statement's real position.
// The option-list fold has to allocate the statement to accumulate into, and
// VacuumStmt.pos is unexported, so the flags are copied onto a correctly
// positioned node instead of mutating the original in place.
func vacuumAt(pos int, v any) *parser.VacuumStmt {
	src, _ := v.(*parser.VacuumStmt)
	if src == nil {
		return parser.NewVacuumStmt(pos)
	}
	return parser.NewVacuumStmtFrom(pos, src)
}

// mergeAction carries a MERGE WHEN clause's THEN branch until it is folded
// onto the clause. The three payload shapes (UPDATE assignments, INSERT
// columns+values, nothing) share one carrier because the grammar rule returns
// a single value.
type mergeAction struct {
	kind    parser.MergeActionKind
	assigns []parser.UpdateAssign
	cols    []string
	vals    []parser.Expr
}

func applyMergeAction(c *parser.MergeWhenClause, v any) {
	a, _ := v.(*mergeAction)
	if a == nil {
		return
	}
	c.Action = a.kind
	c.UpdateAssigns = a.assigns
	c.InsertColumns = a.cols
	c.InsertValues = a.vals
}

// mergeNotMatchedBy resolves `WHEN NOT MATCHED BY <word>`. Legacy accepts only
// SOURCE and TARGET and silently records NEITHER flag for anything else
// (parser.go parseMergeWhenClause's two acceptIdentKeyword calls), so an
// unknown word degrades to a plain NOT MATCHED rather than erroring.
func mergeNotMatchedBy(word string) *parser.MergeWhenClause {
	switch strings.ToLower(word) {
	case "source":
		return parser.NewMergeWhenClause(0, false, true, false)
	case "target":
		return parser.NewMergeWhenClause(0, false, false, true)
	}
	return parser.NewMergeWhenClause(0, false, false, false)
}

// kvPair carries one `NAME = value` option from CREATE TYPE ... AS RANGE.
// val is the raw token join legacy stores for SUBTYPE; last is the final
// dotted component, which is what it stores for the three name-valued options.
type kvPair struct {
	key  string
	val  string
	last string
}

func appendRangeOpt(list any, one *kvPair) []any {
	l, _ := list.([]any)
	return append(l, any(one))
}

func applyRangeOpts(st *parser.CreateTypeStmt, v any) {
	opts, _ := v.([]any)
	for _, raw := range opts {
		o, ok := raw.(*kvPair)
		if !ok {
			continue
		}
		switch o.key {
		case "subtype":
			st.RangeSubtype = o.val
		case "multirange_type_name":
			st.RangeMultirangeName = o.last
		case "subtype_opclass":
			st.RangeOpclassName = o.last
		case "collation":
			st.RangeCollationName = o.last
		}
	}
}

// qnLastPart is the final component of a dotted name — legacy's parseObjectName
// followed by `.Name`, which DISCARDS the schema for these options.
func qnLastPart(q qname) string {
	if len(q.parts) == 0 {
		return ""
	}
	return q.parts[len(q.parts)-1]
}


// rawTypeSpan reproduces legacy's inner token scan: from the token at byte
// position `from`, join raw token values with single spaces until a top-level
// ',' or ')' or the word COLLATE. That is how CREATE TYPE stores a composite
// field's type and a range's SUBTYPE — as text, not as a parsed type.
func rawTypeSpan(l yyLexer, from int) string {
	st, ok := l.(*lexerState)
	if !ok {
		return ""
	}
	var parts []string
	depth := 0
	for _, t := range st.toks {
		if t.Pos < from {
			continue
		}
		if t.Kind == parser.TokenEOF {
			break
		}
		if t.Kind == parser.TokenSymbol {
			switch t.Value {
			case "(":
				depth++
			case ")":
				if depth == 0 {
					return strings.Join(parts, " ")
				}
				depth--
			case ",", ";":
				if depth == 0 && t.Value == "," {
					return strings.Join(parts, " ")
				}
				if t.Value == ";" {
					return strings.Join(parts, " ")
				}
			}
		}
		if depth == 0 && t.Kind != parser.TokenSymbol {
			// COLLATE opens the next clause; CASCADE / RESTRICT end an ALTER
			// TYPE attribute subcommand (parseAttrTypeTokens stops on all
			// three), and none of them can be part of a type name.
			switch strings.ToLower(t.Value) {
			case "collate", "cascade", "restrict":
				return strings.Join(parts, " ")
			}
		}
		parts = append(parts, t.Value)
	}
	return strings.Join(parts, " ")
}

// domainOp is one CREATE DOMAIN constraint clause, applied in written order.
type domainOp func(*parser.CreateDomainStmt)

func domainNotNull(v bool) domainOp {
	return func(d *parser.CreateDomainStmt) { d.NotNull = v }
}

func domainDefault(e parser.Expr) domainOp {
	return func(d *parser.CreateDomainStmt) { d.Default = e }
}

func domainNoop() domainOp { return func(*parser.CreateDomainStmt) {} }

// domainCheck rebuilds the two shapes legacy stores for a domain CHECK: the
// membership list of `CHECK (VALUE IN (...))` (Expr empty, InValues filled) and
// otherwise the raw token join of the body, with VALUE upper-cased and string
// literals re-quoted (ddl.go parseDomainCheckExpr / tryParseCheckInValues).
// The adapter has already folded `CHECK ( ... )` into one terminal whose pos is
// the '(', so the body is read back out of the token stream here.
func domainCheck(l yyLexer, name string, openParen int) domainOp {
	expr, vals := domainCheckParts(l, openParen)
	return func(d *parser.CreateDomainStmt) {
		d.Checks = append(d.Checks, parser.NewDomainCheckClause(name, expr, vals))
	}
}

// domainCheckParts is the shared reader behind CREATE DOMAIN's check list and
// ALTER DOMAIN ADD CONSTRAINT: exactly one of (expr, inValues) is populated.
func domainCheckParts(l yyLexer, openParen int) (string, []string) {
	st, ok := l.(*lexerState)
	if !ok {
		return "", nil
	}
	if vals, ok := domainCheckInValues(st, openParen); ok {
		return "", vals
	}
	var parts []string
	depth := 0
	for _, t := range st.toks {
		if t.Pos < openParen {
			continue
		}
		if t.Kind == parser.TokenSymbol && t.Value == "(" {
			depth++
			if depth == 1 {
				continue // the CHECK's own opening paren is not part of the text
			}
		} else if t.Kind == parser.TokenSymbol && t.Value == ")" {
			depth--
			if depth == 0 {
				break
			}
		}
		switch {
		case t.Kind == parser.TokenStringLit:
			parts = append(parts, "'"+strings.ReplaceAll(t.Value, "'", "''")+"'")
		case (t.Kind == parser.TokenIdent || t.Kind == parser.TokenKeyword) && strings.EqualFold(t.Value, "value"):
			parts = append(parts, "VALUE")
		default:
			parts = append(parts, t.Value)
		}
	}
	return strings.Join(parts, " "), nil
}

// domainCheckInValues recognises `( VALUE [::type] IN ( v, ... ) )` and returns
// the raw literal values. Anything else falls back to the text form.
func domainCheckInValues(st *lexerState, openParen int) ([]string, bool) {
	i := 0
	for ; i < len(st.toks); i++ {
		if st.toks[i].Pos >= openParen {
			break
		}
	}
	next := func() parser.Token {
		if i < len(st.toks) {
			t := st.toks[i]
			i++
			return t
		}
		return parser.Token{Kind: parser.TokenEOF}
	}
	if t := next(); t.Kind != parser.TokenSymbol || t.Value != "(" {
		return nil, false
	}
	t := next()
	if !strings.EqualFold(t.Value, "value") {
		return nil, false
	}
	t = next()
	if t.Kind == parser.TokenOperator && t.Value == "::" {
		// Skip a cast target of one or two tokens (`::text`, `::character
		// varying`); anything longer is not a shape legacy recognises either.
		t = next()
		if t.Kind == parser.TokenIdent || t.Kind == parser.TokenKeyword {
			t = next()
		}
	}
	if !strings.EqualFold(t.Value, "in") {
		return nil, false
	}
	if t := next(); t.Kind != parser.TokenSymbol || t.Value != "(" {
		return nil, false
	}
	var vals []string
	for {
		t = next()
		switch t.Kind {
		case parser.TokenStringLit, parser.TokenIntLit, parser.TokenNumericLit:
			vals = append(vals, t.Value)
		case parser.TokenKeyword:
			if t.Keyword != parser.KwTrue && t.Keyword != parser.KwFalse {
				return nil, false
			}
			vals = append(vals, t.Value)
		default:
			return nil, false
		}
		t = next()
		if t.Kind == parser.TokenSymbol && t.Value == "," {
			continue
		}
		if t.Kind == parser.TokenSymbol && t.Value == ")" {
			return vals, true
		}
		return nil, false
	}
}

func applyDomainConstraints(st *parser.CreateDomainStmt, v any) {
	ops, _ := v.([]any)
	for _, o := range ops {
		if f, ok := o.(domainOp); ok {
			f(st)
		}
	}
}

// seqOp is one CREATE SEQUENCE option, applied in written order.
type seqOp func(*parser.CreateSequenceStmt)

func seqDataType(name string) seqOp {
	n := lowerIdent(name)
	return func(s *parser.CreateSequenceStmt) { s.DataType = n }
}

func seqInt(which string, n int64) seqOp {
	v := n
	return func(s *parser.CreateSequenceStmt) {
		switch which {
		case "increment":
			s.Increment = &v
		case "minvalue":
			s.MinValue = &v
		case "maxvalue":
			s.MaxValue = &v
		case "start":
			s.Start = &v
		case "cache":
			s.Cache = &v
		}
	}
}

func seqCycle() seqOp { return func(s *parser.CreateSequenceStmt) { s.Cycle = true } }

// seqNoop is NO MINVALUE / NO MAXVALUE / NO CYCLE: legacy consumes the word
// pair and records nothing at all, so a later NO CYCLE does NOT undo an
// earlier CYCLE.
func seqNoop() seqOp { return func(*parser.CreateSequenceStmt) {} }

func seqOwnedBy(owner string) seqOp {
	o := owner
	return func(s *parser.CreateSequenceStmt) { s.OwnedBy = o }
}

// seqOwnerName renders OWNED BY's target. NONE is the "no owner" spelling and
// stores an empty string, not the word.
func seqOwnerName(q qname) string {
	if len(q.parts) == 1 && strings.EqualFold(q.parts[0], "none") {
		return ""
	}
	return strings.Join(q.parts, ".")
}

func applySeqOpts(st *parser.CreateSequenceStmt, v any) {
	ops, _ := v.([]any)
	for _, o := range ops {
		if f, ok := o.(seqOp); ok {
			f(st)
		}
	}
}

// twoItemAS reports whether the AS body at byte position `pos` is followed by a
// comma — the LANGUAGE C two-item form. Legacy sets its flag on the COMMA
// alone, whether or not a second string actually follows (function.go:145-150),
// so the check is on the token stream rather than on the parsed list.
func twoItemAS(l yyLexer, pos int) bool {
	st, ok := l.(*lexerState)
	if !ok {
		return false
	}
	for i, t := range st.toks {
		if t.Pos == pos {
			return i+1 < len(st.toks) &&
				st.toks[i+1].Kind == parser.TokenSymbol && st.toks[i+1].Value == ","
		}
	}
	return false
}

// trigEvent is one INSERT / UPDATE [OF cols] / DELETE / TRUNCATE entry of a
// trigger's event list. The column list belongs to UPDATE only, and legacy
// accumulates every event's columns into ONE flat UpdateColumns slice.
type trigEvent struct {
	name string
	cols []string
}

func appendTrigEvent(list any, one *trigEvent) []any {
	l, _ := list.([]any)
	return append(l, any(one))
}

// constrDefer carries a CONSTRAINT trigger's [NOT] DEFERRABLE / INITIALLY
// trailer, which only the CONSTRAINT form reads.
type constrDefer struct {
	deferrable   bool
	initDeferred bool
}

func initiallyDeferred(word string) bool { return strings.EqualFold(word, "deferred") }

// trigForEach carries FOR [EACH] ROW|STATEMENT. A nil pointer means the clause
// was absent, which legacy leaves as ForEachRow=false — the same value
// STATEMENT gives, so the two are indistinguishable in the AST.
type trigForEach struct{ row bool }

func trigForEachOf(word string) *trigForEach {
	return &trigForEach{row: strings.EqualFold(word, "row")}
}

// trigTiming maps BEFORE / AFTER / INSTEAD to the AST's enum. An unrecognised
// word yields 0, which the statement rule turns into legacy's error.
func trigTiming(word string) int {
	switch strings.ToLower(word) {
	case "before":
		return int(parser.TriggerBefore)
	case "after":
		return int(parser.TriggerAfter)
	case "instead":
		return int(parser.TriggerInsteadOf)
	}
	return 0
}

// trigTransition is one REFERENCING entry; which side it names is decided by
// its first word, and an unrecognised word is legacy's "expected OLD or NEW".
type trigTransition2 struct {
	old  bool
	new_ bool
	name string
}

func trigTransition(side, name string) any {
	switch strings.ToLower(side) {
	case "old":
		return &trigTransition2{old: true, name: name}
	case "new":
		return &trigTransition2{new_: true, name: name}
	}
	return &trigTransition2{name: name}
}

// buildTrigger assembles CREATE [CONSTRAINT] TRIGGER from the pieces the
// grammar collected. Everything here mirrors parseCreateTriggerTail's field
// assignments; nothing is inferred.
func buildTrigger(pos int, name string, table parser.ObjectName, isConstraint bool,
	timing int, events any, deferAny any, refs any,
	eachAny any, when parser.Expr, fn parser.ObjectName, args []string) parser.Stmt {
	st := parser.NewCreateTriggerStmt(pos, name, table, isConstraint)
	st.Timing = parser.TriggerTiming(timing)
	defer_, _ := deferAny.(*constrDefer)
	each, _ := eachAny.(*trigForEach)
	for _, raw := range asAnySlice(events) {
		e, ok := raw.(*trigEvent)
		if !ok {
			continue
		}
		st.Events = append(st.Events, e.name)
		st.UpdateColumns = append(st.UpdateColumns, e.cols...)
	}
	if defer_ != nil {
		st.Deferrable, st.InitDeferred = defer_.deferrable, defer_.initDeferred
	}
	for _, raw := range asAnySlice(refs) {
		r, ok := raw.(*trigTransition2)
		if !ok {
			continue
		}
		if r.old {
			st.OldTransitionTable = r.name
		} else if r.new_ {
			st.NewTransitionTable = r.name
		}
	}
	if each != nil {
		st.ForEachRow = each.row
	}
	st.WhenExpr = when
	st.FuncName = fn
	st.FuncArgs = args
	return st
}

func asAnySlice(v any) []any {
	l, _ := v.([]any)
	return l
}

// commentAt re-stamps a COMMENT ON statement with its real position: the
// object-kind rule builds the node before the statement's position is known,
// and CommentOnStmt.pos is unexported.
func commentAt(pos int, cs *parser.CommentOnStmt) parser.Stmt {
	out := parser.NewCommentOnStmt(pos, cs.ObjKind, cs.ObjName, cs.SubName)
	out.Args, out.CastSource, out.CastTarget, out.Description = cs.Args, cs.CastSource, cs.CastTarget, cs.Description
	return out
}

// commentColumn splits `COMMENT ON COLUMN a.b[.c]` the way parseCommentOnTail
// does: the LAST component is the column, and a two-part name leaves the schema
// empty rather than treating the first part as one.
func commentColumn(q qname) *parser.CommentOnStmt {
	parts := q.parts
	if len(parts) < 2 {
		return parser.NewCommentOnStmt(0, "column", parser.ObjectName{}, strings.Join(parts, "."))
	}
	col := parts[len(parts)-1]
	rest := parts[:len(parts)-1]
	name := parser.ObjectName{Name: rest[len(rest)-1]}
	if len(rest) > 1 {
		name.Schema = rest[len(rest)-2]
	}
	return parser.NewCommentOnStmt(0, "column", name, col)
}

// commentConstraint — `ON DOMAIN` switches the kind, which is the only place
// the two constraint flavours differ.
func commentConstraint(name string, onDomain bool, table parser.ObjectName) *parser.CommentOnStmt {
	kind := "constraint"
	if onDomain {
		kind = "domain constraint"
	}
	return parser.NewCommentOnStmt(0, kind, table, name)
}

func commentCast(src, dst string) *parser.CommentOnStmt {
	cs := parser.NewCommentOnStmt(0, "cast", parser.ObjectName{}, "")
	cs.CastSource, cs.CastTarget = src, dst
	return cs
}

// alterFnOp is one ALTER FUNCTION action, applied in written order.
type alterFnOp func(*parser.AlterFunctionStmt)

func alterFnVolatile(v string) alterFnOp {
	s := v
	return func(a *parser.AlterFunctionStmt) { a.Volatile = &s }
}

func alterFnStrict(v bool) alterFnOp {
	b := v
	return func(a *parser.AlterFunctionStmt) { a.Strict = &b }
}

func alterFnLeakproof(v bool) alterFnOp {
	b := v
	return func(a *parser.AlterFunctionStmt) { a.Leakproof = &b }
}

func alterFnSecurity(v bool) alterFnOp {
	b := v
	return func(a *parser.AlterFunctionStmt) { a.SecurityDefiner = &b }
}

func alterFnNoop() alterFnOp { return func(*parser.AlterFunctionStmt) {} }

func alterFnOwner(name string) alterFnOp {
	n := name
	return func(a *parser.AlterFunctionStmt) { a.NewOwner = n }
}

// alterFnOwnerName maps the three self-referential role spellings to the
// sentinel legacy stores for them.
func alterFnOwnerName(name string) string {
	switch strings.ToLower(name) {
	case "current_user", "session_user", "current_role":
		return "current_user"
	}
	return name
}

func alterFnRename(name string) alterFnOp {
	n := name
	return func(a *parser.AlterFunctionStmt) { a.RenameTo = n }
}

func alterFnSchema(name string) alterFnOp {
	n := name
	return func(a *parser.AlterFunctionStmt) { a.NewSchema = n }
}

// alterFnConfig records a SET/RESET clause. keep is false for the two forms
// legacy drops (`FROM CURRENT`, `TO DEFAULT`).
func alterFnConfig(op parser.FunctionConfigOp, keep bool) alterFnOp {
	if !keep {
		return alterFnNoop()
	}
	o := op
	return func(a *parser.AlterFunctionStmt) { a.ConfigOps = append(a.ConfigOps, o) }
}

func applyAlterFnActions(st *parser.AlterFunctionStmt, v any) {
	for _, raw := range asAnySlice(v) {
		if f, ok := raw.(alterFnOp); ok {
			f(st)
		}
	}
}

// dropWithArgs builds the DROP AGGREGATE / DROP OPERATOR shape: one name plus
// a signature stored in ArgTypes.
func dropWithArgs(pos int, kind string, ifExists bool, q qname, args []string, behavior parser.DropBehavior) parser.Stmt {
	st := parser.NewDropCompatStmt(pos, kind, ifExists, []parser.ObjectName{objectNameFromQn(q)}, behavior)
	parser.SetDropCompatExtras(st, args, "", nil, "", "")
	return st
}

// firstArg keeps only the head of a signature list, which is what a
// DropCompatStmt records for an aggregate.
func firstArg(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return args[:1]
}

// buildView assembles CREATE [OR REPLACE] [TEMP] VIEW. The two grammar
// alternatives exist only to keep the optional modifier in one position, so
// the body lives here rather than being written twice.
func buildView(l yyLexer, orReplace bool, modifier any, q qname, cols []string, with []string, sel parser.Stmt, check any) parser.Stmt {
	nm := q.parts
	name := parser.ObjectName{Name: nm[len(nm)-1]}
	if len(nm) > 1 {
		name.Schema = nm[len(nm)-2]
	}
	s, _ := sel.(*parser.SelectStmt)
	cv := parser.NewCreateViewStmt(name, cols, s)
	cv.OrReplace = orReplace
	// opt_create_modifier is shared with CREATE TABLE, which also accepts
	// UNLOGGED; a view records only the temp flag.
	if pfx, _ := modifier.(*createPrefix); pfx != nil {
		cv.Temporary = pfx.temporary
	}
	viewReloptions(cv, with)
	st := l.(*lexerState)
	co, _ := check.(*checkOpt)
	if co != nil {
		cv.CheckOption = co.opt
		if co.pos >= 0 {
			// RawDef stops at WITH: legacy's span excludes the option.
			cv.RawDef = st.spanTextUpTo(co.pos)
			return cv
		}
	}
	cv.RawDef = st.spanTextUpTo(st.fragEnd)
	return cv
}

// rewriteIndirectionStars runs legacy's end-of-parseSelect rewrite: every
// `(f(x)).*` target becomes a synthetic `__irs_N`-aliased table-function FROM
// entry plus a qualified star. Sharing parser.RewriteIndirectionStarTargets
// rather than reimplementing it keeps the alias numbering (which is the TARGET
// INDEX, not a running counter) and the aggregate rejection identical.
func rewriteIndirectionStars(l yyLexer, s *parser.SelectStmt) error {
	if s == nil {
		return nil
	}
	if err := parser.RewriteIndirectionStarTargets(s, nil); err != nil {
		if st, ok := l.(*lexerState); ok {
			var se *parser.SyntaxError
			if errors.As(err, &se) {
				st.raise(se.Message, se.Raw, se.Pos)
				return err
			}
			st.raise(err.Error(), true, 0)
		}
		return err
	}
	return nil
}

// extOp is one CREATE EXTENSION option, applied in written order.
type extOp func(*parser.CreateExtensionStmt)

func extSchema(name string) extOp {
	n := name
	return func(e *parser.CreateExtensionStmt) { e.Schema = n }
}

func extVersion(v string) extOp {
	s := v
	return func(e *parser.CreateExtensionStmt) { e.Version = s }
}

func extCascade() extOp { return func(e *parser.CreateExtensionStmt) { e.Cascade = true } }

func applyExtOpts(st *parser.CreateExtensionStmt, v any) {
	for _, raw := range asAnySlice(v) {
		if f, ok := raw.(extOp); ok {
			f(st)
		}
	}
}

// altSeqOp is one ALTER SEQUENCE option. Unlike CREATE SEQUENCE's, the NO
// forms are RECORDED: a sequence already has values, so "reset to the type
// default" is a different statement from "leave unchanged".
type altSeqOp func(*parser.AlterSequenceStmt)

func altSeqDataType(name string) altSeqOp {
	n := lowerIdent(name)
	return func(s *parser.AlterSequenceStmt) { s.DataType = n }
}

func altSeqInt(which string, n int64) altSeqOp {
	v := n
	return func(s *parser.AlterSequenceStmt) {
		switch which {
		case "increment":
			s.Increment = &v
		case "minvalue":
			s.MinValue = &v
		case "maxvalue":
			s.MaxValue = &v
		case "start":
			s.StartWith = &v
		case "restart":
			s.RestartWith = &v
		case "cache":
			s.Cache = &v
		}
	}
}

func altSeqRestart() altSeqOp { return func(s *parser.AlterSequenceStmt) { s.Restart = true } }

func altSeqFlag(which string) altSeqOp {
	return func(s *parser.AlterSequenceStmt) {
		switch which {
		case "cycle":
			s.Cycle = true
		case "nominvalue":
			s.NoMinValue = true
		case "nomaxvalue":
			s.NoMaxValue = true
		case "nocycle":
			s.NoCycle = true
		}
	}
}

func altSeqLogged(v string) altSeqOp {
	s := v
	return func(a *parser.AlterSequenceStmt) { a.SetLogged = s }
}

// altSeqOwnedBy — `OWNED BY NONE` clears the owner, which is distinct from an
// absent clause, so it sets its own flag rather than storing an empty string.
func altSeqOwnedBy(owner string) altSeqOp {
	o := owner
	return func(s *parser.AlterSequenceStmt) {
		if o == "" {
			s.ClearOwnedBy = true
			return
		}
		s.OwnedBy = o
	}
}

func applyAlterSeqOpts(st *parser.AlterSequenceStmt, v any) {
	for _, raw := range asAnySlice(v) {
		if f, ok := raw.(altSeqOp); ok {
			f(st)
		}
	}
}

// enumPos carries ALTER TYPE ... ADD VALUE's optional BEFORE / AFTER anchor.
type enumPos struct {
	before string
	after  string
}

type alterTypeOp func(*parser.AlterTypeStmt)

func altTypeAddValue(ifNotExists bool, label string, pos *enumPos) alterTypeOp {
	return func(a *parser.AlterTypeStmt) {
		a.AddValue, a.IfNotExists = label, ifNotExists
		a.Before, a.After = pos.before, pos.after
	}
}

func altTypeRenameValue(old, new_ string) alterTypeOp {
	o, n := old, new_
	return func(a *parser.AlterTypeStmt) { a.RenameOldValue, a.RenameNewValue = o, n }
}

func altTypeRenameTo(name string) alterTypeOp {
	n := name
	return func(a *parser.AlterTypeStmt) { a.RenameTo = n }
}

func altTypeOwner(name string) alterTypeOp {
	n := name
	return func(a *parser.AlterTypeStmt) { a.NewOwner = n }
}

func altTypeAddAttr(name, typ, collation string) alterTypeOp {
	n, ty, c := name, typ, collation
	return func(a *parser.AlterTypeStmt) {
		a.AddAttrName, a.AddAttrType, a.AddAttrCollation = n, ty, c
	}
}

// alterDomainOp builds the statement from the domain name, which the action
// rule does not have: the name is parsed before the action.
type alterDomainOp func(name string) *parser.AlterDomainStmt

func altDomAction(action string) alterDomainOp {
	a := action
	return func(name string) *parser.AlterDomainStmt {
		return parser.NewAlterDomainStmt(0, name, a)
	}
}

func altDomDefault(e parser.Expr) alterDomainOp {
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "setdefault")
		st.DefaultExpr = e
		return st
	}
}

// altDomAddCheck reuses the domain-CHECK reader: the same `VALUE IN (...)`
// special case and the same raw token join with VALUE upper-cased.
func altDomAddCheck(l yyLexer, cname string, openParen int) alterDomainOp {
	expr, vals := domainCheckParts(l, openParen)
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "addconstraint")
		st.ConstraintName, st.CheckExpr, st.CheckInValues = cname, expr, vals
		return st
	}
}

func altDomDropConstraint(ifExists bool, cname string) alterDomainOp {
	ie, c := ifExists, cname
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "dropconstraint")
		st.ConstraintName, st.IfExists = c, ie
		return st
	}
}

func altDomRenameConstraint(old, new_ string) alterDomainOp {
	o, n := old, new_
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "renameconstraint")
		st.ConstraintName, st.NewConstraintName = o, n
		return st
	}
}

func altDomRenameTo(newName string) alterDomainOp {
	n := newName
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "rename")
		st.NewName = n
		return st
	}
}

func altDomOwner(owner string) alterDomainOp {
	o := owner
	return func(name string) *parser.AlterDomainStmt {
		st := parser.NewAlterDomainStmt(0, name, "owner")
		st.NewOwner = o
		return st
	}
}

// alterDomainAt re-stamps the statement with its real position: the action
// rule builds the node before the position is known and pos is unexported.
func alterDomainAt(pos int, st *parser.AlterDomainStmt) parser.Stmt {
	out := parser.NewAlterDomainStmt(pos, st.Name, st.Action)
	out.NewName, out.NewOwner = st.NewName, st.NewOwner
	out.ConstraintName, out.NewConstraintName = st.ConstraintName, st.NewConstraintName
	out.CheckExpr, out.CheckInValues = st.CheckExpr, st.CheckInValues
	out.IfExists, out.DefaultExpr = st.IfExists, st.DefaultExpr
	return out
}

// altTypeAttrCmds records the comma-separated attribute subcommand list and
// mirrors the first into the scalar fields, exactly as legacy's
// mirrorFirstAttrCmd does.
func altTypeAttrCmds(v any) alterTypeOp {
	cmds := asAnySlice(v)
	return func(a *parser.AlterTypeStmt) {
		for _, raw := range cmds {
			if c, ok := raw.(parser.AlterTypeAttrCmd); ok {
				a.AttrCmds = append(a.AttrCmds, c)
			}
		}
		parser.MirrorFirstAttrCmd(a)
	}
}
