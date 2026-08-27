package sqlparser

import (
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

// fillfactorFrom pulls the fillfactor out of a CREATE INDEX `WITH (...)` list.
// Only fillfactor reaches the AST, as in legacy; other storage parameters are
// accepted and discarded.
func fillfactorFrom(kvs []string) int {
	for _, kv := range kvs {
		parts := splitKV(kv)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "fillfactor") {
			continue
		}
		n := 0
		for _, c := range parts[1] {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
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
