package sqlparser

import (
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
