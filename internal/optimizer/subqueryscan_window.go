package optimizer

// M0146-0092 — two inputs of setrefs' trivial_subqueryscan that the
// consumption proxy in stripTrivialSubqueryScans cannot see.
//
//  1. A resjunk column in the subquery's own target list. A GROUP BY,
//     ORDER BY, DISTINCT ON or window PARTITION/ORDER BY item that matches
//     no select-list entry is added to the subquery's tlist as a resjunk
//     TargetEntry (findTargetlistEntrySQL92/SQL99, parse_clause.c), and the
//     subplan keeps it. A pathtarget-regime scan tlist names only the
//     columns the parent needs, so the two tlists differ in length and PG
//     keeps `Subquery Scan` (`select * from (select sum(b) s from t group by
//     a) x`). goopg's body Project drops the junk column, so the flag is
//     computed from the AST (selectHasResjunk). The physical tlist
//     (build_physical_tlist, plancat.c) carries the resjunk entries too, so
//     under a join or Agg the wrapper is still trivial and stripped.
//
//  2. A leaf below a WindowAgg. make_window_input_target (planner.c) builds
//     the window's input target from the final target: entries that are
//     window partition/order keys first (in final-target order, the resjunk
//     key entries at the tlist's end included), then the Vars of every
//     other entry (pull_var_clause, recursing into window function
//     arguments). That target is the leaf's tlist when the leaf sits under
//     the WindowAgg (CP_SMALL_TLIST) or under the window's Sort, so a key
//     column ahead of a non-key one reorders it and PG keeps the node —
//     where the first-reference order goopg used reads identity.
//     windowInputOrder computes PG's order for such a leaf.

import (
	"github.com/goopg/goopg/internal/parser"
)

// selectHasResjunk reports whether PG's analysis of s adds a resjunk entry
// to its target list: a GROUP BY, ORDER BY or DISTINCT ON item that matches
// no select-list entry by output name, position or expression (SQL92 rules),
// or a window PARTITION BY / ORDER BY item that matches none by expression
// (SQL99 rules, findTargetlistEntrySQL99). Set operations and GROUPING SETS
// report false (no change from the consumption test).
//
// Expression matching uses parserExprKey, which is qualifier-blind: a match
// it reports where PG would not only ever means "no resjunk", the old
// behaviour.
func selectHasResjunk(s *parser.SelectStmt) bool {
	if s == nil || s.SetOp != nil || s.SetOpOperand != nil || s.GroupingSets != nil {
		return false
	}
	keys := make(map[string]bool, len(s.Targets))
	hasStar := false
	for _, t := range s.Targets {
		if _, ok := t.Expr.(*parser.StarExpr); ok {
			hasStar = true
			continue
		}
		keys[parserExprKey(t.Expr)] = true
	}
	matched := func(e parser.Expr, sql92 bool) bool {
		if e == nil {
			return true
		}
		if sql92 {
			if _, ok := e.(*parser.IntegerConst); ok {
				return true // a select-list position
			}
			if sub := resolveOrderBySubstitution(e, s.Targets); sub != e {
				return true // an output column name
			}
		}
		if keys[parserExprKey(e)] {
			return true
		}
		if _, ok := e.(*parser.ColumnRef); ok && hasStar {
			return true
		}
		return false
	}
	for _, g := range s.GroupBy {
		if !matched(g, true) {
			return true
		}
	}
	for _, o := range s.OrderBy {
		if !matched(o.Expr, true) {
			return true
		}
	}
	for _, d := range s.DistinctOn {
		if !matched(d, true) {
			return true
		}
	}
	windowJunk := func(w *parser.WindowDef) bool {
		if w == nil {
			return false
		}
		for _, p := range w.PartitionBy {
			if !matched(p, false) {
				return true
			}
		}
		for _, o := range w.OrderBy {
			if !matched(o.Expr, false) {
				return true
			}
		}
		return false
	}
	calls, err := collectWindowCalls(s)
	if err != nil {
		return false
	}
	for _, fc := range calls {
		if windowJunk(fc.Over) {
			return true
		}
	}
	for _, nw := range s.WindowClause {
		if windowJunk(nw.Def) {
			return true
		}
	}
	return false
}

// windowInputOrder returns the leaf-local positions of scan's tlist as PG's
// make_window_input_target orders them, when the leaf is the input of a
// window stack: path is the chain from the region root to the leaf's parent,
// and the leaf's parent is a WindowAgg or that WindowAgg's Sort. applies is
// false when the leaf is not in that position — the caller then keeps the
// consumption test. ok is false when the order cannot be derived or contains
// something other than a bare leaf column (a computed key): the leaf's tlist
// is then not an identity and the wrapper stays.
func windowInputOrder(path []Node, scan *SubqueryScan) (order []int, ok, applies bool) {
	i := len(path) - 1
	if i < 0 {
		return nil, false, false
	}
	switch path[i].(type) {
	case *Sort, *IncrementalSort:
		i--
	}
	if i < 0 {
		return nil, false, false
	}
	if _, isWin := path[i].(*WindowAgg); !isWin {
		return nil, false, false
	}
	// The window stack, bottom first. Each window may carry its own Sort.
	var stack []*WindowAgg
	for i >= 0 {
		switch x := path[i].(type) {
		case *WindowAgg:
			stack = append(stack, x)
			i--
			continue
		case *Sort, *IncrementalSort:
			if i > 0 {
				if _, above := path[i-1].(*WindowAgg); above {
					i--
					continue
				}
			}
		}
		break
	}
	base := len(scan.Output())
	if len(stack[0].Child.Output()) != base {
		return nil, false, true
	}

	// funcAt maps a position at or past the base columns to the window
	// function that produced it: each WindowAgg emits its input then its
	// functions, so the stack's output is base ++ f(w1) ++ f(w2) ...
	funcAt := func(idx int) *WindowFunc {
		off := base
		for _, w := range stack {
			if idx < off+len(w.Funcs) {
				return &w.Funcs[idx-off]
			}
			off += len(w.Funcs)
		}
		return nil
	}

	isKey := map[int]bool{}
	var keyOrder []int
	for _, w := range stack {
		exprs := append([]Expr(nil), w.PartitionBy...)
		for _, k := range w.OrderBy {
			exprs = append(exprs, k.Expr)
		}
		for _, e := range exprs {
			cr, isCol := e.(*ColumnRef)
			if !isCol || cr.Index < 0 || cr.Index >= base {
				return nil, false, true // a computed key lands in the tlist as itself
			}
			if !isKey[cr.Index] {
				isKey[cr.Index] = true
				keyOrder = append(keyOrder, cr.Index)
			}
		}
	}

	// The final target: the Project above the stack, past nodes that pass
	// their input through unchanged; without one, the top window's output.
	var final []Expr
	for j := i; j >= 0; j-- {
		if p, isProj := path[j].(*Project); isProj {
			final = p.Targets
			break
		}
		switch path[j].(type) {
		case *Sort, *IncrementalSort, *Limit, *Filter, *Distinct:
			continue
		}
		break
	}
	if final == nil {
		top := stack[len(stack)-1]
		for idx := range top.Output() {
			final = append(final, &ColumnRef{Index: idx})
		}
	}

	seen := map[int]bool{}
	add := func(p int) {
		if !seen[p] {
			seen[p] = true
			order = append(order, p)
		}
	}
	// Kept entries: final-target entries that are window keys, in order.
	var flat []Expr
	for _, e := range final {
		if cr, isCol := e.(*ColumnRef); isCol && cr.Index >= 0 && cr.Index < base && isKey[cr.Index] {
			add(cr.Index)
			continue
		}
		flat = append(flat, e)
	}
	// Keys absent from the select list are resjunk entries at the tlist's
	// end — still window keys, so still kept, ahead of the flattened Vars.
	for _, k := range keyOrder {
		add(k)
	}
	// Everything else contributes its Vars, window function arguments
	// included (PVC_RECURSE_WINDOWFUNCS).
	ok = true
	var flatten func(e Expr)
	flatten = func(e Expr) {
		walkExprTree(e, func(sub Expr) {
			cr, isCol := sub.(*ColumnRef)
			if !isCol {
				return
			}
			switch {
			case cr.Index >= 0 && cr.Index < base:
				add(cr.Index)
			default:
				f := funcAt(cr.Index)
				if f == nil {
					ok = false
					return
				}
				for _, a := range f.Args {
					flatten(a)
				}
				if f.Filter != nil {
					flatten(f.Filter)
				}
			}
		})
	}
	for _, e := range flat {
		flatten(e)
	}
	return order, ok, true
}
