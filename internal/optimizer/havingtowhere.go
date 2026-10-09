package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// moveHavingToWhere is subquery_planner's HAVING loop (planner.c,
// M0146-0024): a HAVING conjunct with no aggregate, no volatile function
// and no SubPlan moves into WHERE when the query has a GROUP BY and no
// grouping sets. Any group the clause rejects loses all its rows before
// grouping instead of after, so the output is the same, and the scan can
// use the clause (PG then prints it as the scan's Filter or index qual,
// not the grouping node's Filter).
//
// Moved conjuncts are appended after the existing WHERE conjuncts, as PG's
// list_concat does. The statement is copied, never mutated: a re-planned
// statement must not move the clause twice.
//
// Narrower than PG, fail-closed (ledgered):
//   - Grouping sets keep HAVING whole. PG may still move a clause that
//     reads no grouping column, and copy a variable-free clause into WHERE
//     when an empty grouping set exists.
//   - A clause moves only when every column it reads is written verbatim
//     as a plain GROUP BY item. PG checks grouping validity before
//     planning, so a clause reading an ungrouped column must still raise
//     that error here. A grouped expression (`a + b`), a functionally
//     dependent column, an outer reference or a differently qualified
//     spelling keeps the clause in HAVING.
//   - Expression kinds outside havingClauseMovable's list keep the clause.
func moveHavingToWhere(s *parser.SelectStmt, cat catalog.Catalog) *parser.SelectStmt {
	if s == nil || s.Having == nil || len(s.GroupBy) == 0 || s.GroupingSets != nil {
		return s
	}
	grouped := map[string]bool{}
	for _, g := range s.GroupBy {
		if c, ok := g.(*parser.ColumnRef); ok {
			grouped[havingColumnKey(c)] = true
		}
	}
	if len(grouped) == 0 {
		return s
	}
	var keep, move []parser.Expr
	for _, c := range splitParserAnd(s.Having) {
		if havingClauseMovable(c, grouped, cat) {
			move = append(move, c)
		} else {
			keep = append(keep, c)
		}
	}
	if len(move) == 0 {
		return s
	}
	out := *s
	where := s.Where
	for _, c := range move {
		if where == nil {
			where = c
			continue
		}
		where = &parser.BinaryOp{Op: parser.OpAnd, Left: where, Right: c}
	}
	out.Where = where
	out.Having = nil
	for _, c := range keep {
		if out.Having == nil {
			out.Having = c
			continue
		}
		out.Having = &parser.BinaryOp{Op: parser.OpAnd, Left: out.Having, Right: c}
	}
	return &out
}

// splitParserAnd flattens a parser AND tree into its conjuncts, left to
// right.
func splitParserAnd(e parser.Expr) []parser.Expr {
	if b, ok := e.(*parser.BinaryOp); ok && b.Op == parser.OpAnd {
		return append(splitParserAnd(b.Left), splitParserAnd(b.Right)...)
	}
	return []parser.Expr{e}
}

func havingColumnKey(c *parser.ColumnRef) string {
	return strings.ToLower(c.Schema) + "." + strings.ToLower(c.Table) + "." + strings.ToLower(c.Column)
}

// havingClauseMovable reports whether e may move from HAVING to WHERE:
// every node is of a kind listed here, every column is a plain GROUP BY
// column, and no call is an aggregate, a window function or volatile.
// Anything unlisted (sublinks included — PG's contain_subplans) declines.
func havingClauseMovable(e parser.Expr, grouped map[string]bool, cat catalog.Catalog) bool {
	if e == nil {
		return true
	}
	switch x := e.(type) {
	case *parser.IntegerConst, *parser.StringConst, *parser.NumericConst,
		*parser.TypedStringLit, *parser.NullConst, *parser.BooleanConst,
		*parser.IntervalLit, *parser.ParamRef:
		return true
	case *parser.ColumnRef:
		return grouped[havingColumnKey(x)]
	case *parser.BinaryOp:
		return havingClauseMovable(x.Left, grouped, cat) && havingClauseMovable(x.Right, grouped, cat)
	case *parser.UnaryOp:
		return havingClauseMovable(x.Operand, grouped, cat)
	case *parser.CastExpr:
		return havingClauseMovable(x.Operand, grouped, cat)
	case *parser.CollateExpr:
		return havingClauseMovable(x.Operand, grouped, cat)
	case *parser.IsNullExpr:
		return havingClauseMovable(x.Operand, grouped, cat)
	case *parser.IsBoolExpr:
		return havingClauseMovable(x.Operand, grouped, cat)
	case *parser.IsDistinctFromExpr:
		return havingClauseMovable(x.Left, grouped, cat) && havingClauseMovable(x.Right, grouped, cat)
	case *parser.InExpr:
		if x.Subquery != nil || !havingClauseMovable(x.Operand, grouped, cat) {
			return false
		}
		for _, v := range x.List {
			if !havingClauseMovable(v, grouped, cat) {
				return false
			}
		}
		return true
	case *parser.CaseExpr:
		if !havingClauseMovable(x.Operand, grouped, cat) || !havingClauseMovable(x.Else, grouped, cat) {
			return false
		}
		for _, w := range x.Whens {
			if !havingClauseMovable(w.When, grouped, cat) || !havingClauseMovable(w.Then, grouped, cat) {
				return false
			}
		}
		return true
	case *parser.FuncCall:
		if x.Over != nil || x.Filter != nil || x.Star || x.Distinct ||
			len(x.OrderBy) > 0 || len(x.WithinGroup) > 0 ||
			isAggregateFuncName(x) || isUserAggregateFunc(x, cat) ||
			parserFuncVolatile(x, cat) {
			return false
		}
		for _, a := range x.Args {
			if !havingClauseMovable(a, grouped, cat) {
				return false
			}
		}
		return true
	}
	return false
}

// parserFuncVolatile is exprListHasVolatileBuiltin's per-call test over a
// raw parse call: the volatile builtin list, then any volatile (or
// unmarked) registered routine of that name.
func parserFuncVolatile(fc *parser.FuncCall, cat catalog.Catalog) bool {
	name := strings.ToLower(fc.Name.Name)
	if pullupVolatileBuiltins[name] {
		return true
	}
	if cat == nil {
		return false
	}
	if rs := cat.Routines(); rs != nil {
		for _, r := range rs.LookupByName(parser.ObjectName{Name: name}) {
			if r.Volatile == "v" || r.Volatile == "" {
				return true
			}
		}
	}
	return false
}
