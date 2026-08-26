package sqlparser

import (
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
type castType struct{ schema, name string }

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
	notNull  bool
	primary  bool
	unique   bool
	defExpr  parser.Expr
}

// tableElem is one element of a CREATE TABLE parens list: a column spec or a
// table-level PRIMARY KEY / UNIQUE marker.
type tableElem struct {
	col   *colSpec
	pk    []string
	uq    [][]string
	check string
	fkDef *parser.TableForeignKeyDef
}

// colConstraints accumulates a column's constraint suffix in CREATE TABLE.
type colConstraints struct {
	args      []int64
	notNull   bool
	primary   bool
	unique    bool
	defExpr   parser.Expr
	checkText string
	fk        *fkInfo
}

// colConstraint is one parsed column-constraint keyword group.
type colConstraint struct {
	kind string // "nn" | "pk" | "uq" | "def" | "check" | "fk"
	text string          // check: raw source span
	fk   *fkInfo         // fk: referenced table/cols + actions
	expr parser.Expr
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
	del parser.FKAction
	up  parser.FKAction
}

// colConstraint kinds: "nn" | "pk" | "uq" | "def" | "check" | "fk".

// fkInfo carries a column-level FK target parsed inline.
type fkInfo struct {
	refTable parser.ObjectName
	refCols  []string
	onDel    parser.FKAction
	onUp     parser.FKAction
}

// namedFkAct is one ON DELETE/UPDATE referential action occurrence.
type namedFkAct struct {
	del bool
	up  bool
	act parser.FKAction
}

// applyFkAction folds an action occurrence into the accumulator.
func applyFkAction(a *fkActs, n *namedFkAct) *fkActs {
	if n.del {
		a.del = n.act
	}
	if n.up {
		a.up = n.act
	}
	return a
}
