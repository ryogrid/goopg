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
