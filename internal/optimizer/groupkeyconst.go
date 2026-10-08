package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005ak — GROUP BY keys made redundant by a WHERE constant.
//
// Since PostgreSQL 16, standard_qp_callback builds the group pathkeys with
// make_pathkeys_for_sortclauses_extended(..., remove_redundant = true) and
// keeps in root->processed_groupClause only the GROUP BY items whose pathkey
// survived: an item whose equivalence class contains a constant sorts and
// groups nothing, so it is dropped (planner.c standard_qp_callback,
// pathkeys.c pathkey_is_redundant). `GROUP BY d_year, i_category ...
// WHERE d_year = 1998` groups on i_category alone — TPC-DS Q42/Q52, whose
// `Group Key:` lines omit `dt.d_year`. The column stays in the output: every
// row of a group carries the same constant, which is exactly goopg's
// functionally-determined passthrough (remove_useless_groupby_columns uses
// the same mechanism).
//
// Scope, fail-closed:
//
//   - the equivalence is a top-level WHERE conjunct `col = const` (either
//     order) over a bare column; equalities reached transitively through
//     other columns (a = b AND b = 5) are not derived here;
//   - every key may go (M0146-0102). PG then runs a KEYLESS sorted
//     aggregate — `GroupAggregate` / `Group` with no Group Key and no Sort —
//     that still returns no row on empty input, because the query is
//     grouped (parse->groupClause is set) even though processed_groupClause
//     is empty. The caller marks such a node Aggregate.GroupedNoKeys, which
//     keeps the executor from emitting the ungrouped aggregate's empty-input
//     row;
//   - grouping sets and GROUPING() calls never prune (the caller's gate).

// redundantConstGroupKeys returns keep[i] == false for each group expression
// equated to a constant by a top-level WHERE conjunct, plus the input-schema
// indices of those columns; (nil, nil) when nothing can be dropped.
func redundantConstGroupKeys(groupExprs []Expr, s *parser.SelectStmt, ctx *resolveContext) ([]bool, map[int]bool) {
	if s == nil || s.Where == nil || ctx == nil || len(groupExprs) < 1 {
		return nil, nil
	}
	constInput := map[int]bool{}
	for _, c := range splitParserAnd(s.Where) {
		b, ok := c.(*parser.BinaryOp)
		if !ok || b.Op != parser.OpEq {
			continue
		}
		var side parser.Expr
		switch {
		case parserPseudoConstant(b.Right):
			side = b.Left
		case parserPseudoConstant(b.Left):
			side = b.Right
		default:
			continue
		}
		pc, ok := side.(*parser.ColumnRef)
		if !ok {
			continue
		}
		e, err := resolveExpr(pc, ctx)
		if err != nil {
			continue
		}
		if cr, ok := e.(*ColumnRef); ok && cr.Index >= 0 {
			constInput[cr.Index] = true
		}
	}
	if len(constInput) == 0 {
		return nil, nil
	}
	keep := make([]bool, len(groupExprs))
	pruned := map[int]bool{}
	for i, g := range groupExprs {
		keep[i] = true
		if cr, ok := g.(*ColumnRef); ok && constInput[cr.Index] {
			keep[i] = false
			pruned[cr.Index] = true
		}
	}
	if len(pruned) == 0 {
		return nil, nil
	}
	return keep, pruned
}

// parserPseudoConstant reports whether e is a literal (possibly cast), or
// an operator over such literals — the constant member an equivalence class
// needs for pathkey_is_redundant. PG builds its equivalence classes after
// eval_const_expressions has folded `1+1` to `2` (TPC-DS Q39's
// `inv2.d_moy = 1+1`); a built-in operator is never volatile, so the
// unfolded form is as constant as the folded one.
func parserPseudoConstant(e parser.Expr) bool {
	switch x := e.(type) {
	case *parser.IntegerConst, *parser.StringConst, *parser.NumericConst,
		*parser.TypedStringLit, *parser.BooleanConst:
		return true
	case *parser.CastExpr:
		return parserPseudoConstant(x.Operand)
	case *parser.UnaryOp:
		return parserPseudoConstant(x.Operand)
	case *parser.BinaryOp:
		return parserPseudoConstant(x.Left) && parserPseudoConstant(x.Right)
	}
	return false
}

// orderItemPinnedByWhere reports whether an ORDER BY item (after alias and
// ordinal substitution) is a bare column that a top-level WHERE conjunct
// `col = const` pins. PG drops such items from sort_pathkeys
// (pathkey_is_redundant: the equivalence class holds a constant), so a
// Sort orders only by the rest — TPC-DS Q42's `Sort Key:` omits
// `dt.d_year` — and none is needed when nothing remains.
//
// Matching is on the written column: same column name, and the same
// qualifier or an unqualified side (an unqualified name that resolved at
// all is unambiguous, so it names the same column).
func orderItemPinnedByWhere(item parser.Expr, s *parser.SelectStmt) bool {
	oc, ok := item.(*parser.ColumnRef)
	if !ok || s == nil || s.Where == nil || oc.Column == "" || oc.Column == "*" {
		return false
	}
	// Grouping sets null out a set's non-member columns ABOVE the WHERE
	// (PG marks those Vars nullable, so the constant's equivalence class
	// does not reach the sort): `WHERE x = 1 GROUP BY GROUPING SETS (x, y)
	// ORDER BY x, y` still sorts the (NULL, y) rows by x. A set
	// operation's ORDER BY sorts the combined result, not this arm's rows.
	if s.GroupingSets != nil || s.SetOp != nil || s.SetOpOperand != nil {
		return false
	}
	for _, c := range splitParserAnd(s.Where) {
		b, ok := c.(*parser.BinaryOp)
		if !ok || b.Op != parser.OpEq {
			continue
		}
		var side parser.Expr
		switch {
		case parserPseudoConstant(b.Right):
			side = b.Left
		case parserPseudoConstant(b.Left):
			side = b.Right
		default:
			continue
		}
		wc, ok := side.(*parser.ColumnRef)
		if !ok || !strings.EqualFold(wc.Column, oc.Column) {
			continue
		}
		if wc.Table == "" || oc.Table == "" || strings.EqualFold(wc.Table, oc.Table) {
			return true
		}
	}
	return false
}

// distinctKeysAllPinned reports whether every SELECT DISTINCT target is
// redundant as a distinct key: a constant, or a column a top-level WHERE
// `col = const` pins (M0146-0113). PG's distinct_pathkeys is then empty and
// create_final_distinct_paths plans the DISTINCT as a LIMIT 1 over its input
// — every input row has the same distinct key, so the first one is the answer
// (`SELECT DISTINCT four, 1 FROM tenk1 WHERE four = 0` → `Limit -> Seq Scan`).
// Grouped, windowed and set-operation queries keep the distinct step.
func distinctKeysAllPinned(s *parser.SelectStmt) bool {
	if s == nil || !s.Distinct || len(s.DistinctOn) > 0 || len(s.Targets) == 0 ||
		len(s.GroupBy) > 0 || s.GroupingSets != nil || s.Having != nil ||
		s.SetOp != nil || s.SetOpOperand != nil {
		return false
	}
	for _, t := range s.Targets {
		if parserPseudoConstant(t.Expr) {
			continue
		}
		if !orderItemPinnedByWhere(t.Expr, s) {
			return false
		}
	}
	return true
}

// deepenOuterRefs returns e with every outer reference one query level
// further out (M0146-0114): the min/max rewrite moves a WHERE into a new
// sublink nested inside this level, so a reference to the enclosing level
// must now skip this one. It declines (false) when e holds a sublink, whose
// inner plan the shift does not reach.
func deepenOuterRefs(e Expr) (Expr, bool) {
	hasSublink := false
	walkExprTree(e, func(x Expr) {
		if len(ExprSubplans(x)) > 0 {
			hasSublink = true
		}
	})
	if hasSublink {
		return nil, false
	}
	return cloneExprRefs(e, scopeIgnore, exprRewriter{
		Rewrite: func(n Expr) Expr {
			if o, ok := n.(*OuterColumnRef); ok {
				cp := *o
				cp.Level++
				return &cp
			}
			return n
		},
	})
}
