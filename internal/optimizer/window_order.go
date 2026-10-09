package optimizer

import (
	"sort"

	"github.com/goopg/goopg/internal/parser"
)

// windowSortItem is one entry of a window's `uniqueOrder` list
// (select_active_windows, planner.c): the SortGroupClause fields
// common_prefix_cmp compares.
type windowSortItem struct {
	ref        int  // tleSortGroupRef
	desc       bool // sortop: the `>` operator of a type outranks its `<`
	nullsFirst bool
}

// orderWindowDefsLikePG returns the permutation of `defs` (one per active
// window, in the order goopg collected them) that PG's select_active_windows
// produces: a stable sort by common_prefix_cmp over each window's partition
// clauses followed by its order clauses (duplicates of a partition item
// dropped). The first window in the result is the lowest WindowAgg, and
// name_active_windows names the unnamed ones in this order (M0146-0005dm).
//
// The comparison keys are tleSortGroupRefs, which parse analysis hands out
// to each distinct sort/group expression on first use, in this order:
// ORDER BY (transformSortClause), GROUP BY, DISTINCT / DISTINCT ON, then the
// window definitions — the WINDOW clause's named windows first, then each
// inline OVER spec in turn, order items before partition items
// (transformWindowDefinitions). A larger ref sorts first, then a larger
// sortop (DESC, whose `>` operator has the higher OID than `<` for the
// built-in types), then NULLS FIRST; on a common prefix the window with
// more items sorts first, so the stronger sort is applied before the
// weaker one can reuse it.
//
// Expressions are matched by their parser key: two spellings PG resolves
// to one target entry (a qualified and an unqualified column) count as two
// here, which can only make the order differ, never the results.
func orderWindowDefsLikePG(s *parser.SelectStmt, defs []*parser.WindowDef) []int {
	refs := map[string]int{}
	assign := func(e parser.Expr) {
		if e == nil {
			return
		}
		k := parserExprKey(e)
		if _, ok := refs[k]; !ok {
			refs[k] = len(refs) + 1
		}
	}
	for _, sb := range s.OrderBy {
		assign(resolveOrderBySubstitution(sb.Expr, s.Targets))
	}
	for _, g := range s.GroupBy {
		assign(resolveOrderBySubstitution(g, s.Targets))
	}
	if len(s.DistinctOn) > 0 {
		for _, e := range s.DistinctOn {
			assign(resolveOrderBySubstitution(e, s.Targets))
		}
	} else if s.Distinct {
		for _, t := range s.Targets {
			assign(t.Expr)
		}
	}
	// transformWindowDefinitions transforms a window's ORDER BY before its
	// PARTITION BY, so the order items take refs first.
	assignDef := func(w *parser.WindowDef) {
		if w == nil {
			return
		}
		for _, o := range w.OrderBy {
			assign(o.Expr)
		}
		for _, p := range w.PartitionBy {
			assign(p)
		}
	}
	named := map[string]bool{}
	for _, nw := range s.WindowClause {
		assignDef(nw.Def)
		named[windowSpecKey(nw.Def)] = true
	}
	for _, w := range defs {
		if !named[windowSpecKey(w)] {
			assignDef(w)
		}
	}

	orders := make([][]windowSortItem, len(defs))
	for i, w := range defs {
		var items []windowSortItem
		seen := map[windowSortItem]bool{}
		// A partition item that is also an order item takes that item's
		// sort operator (transformGroupClauseExpr is handed the window's
		// orderClause and copies the matching SortGroupClause's operators).
		orderOf := map[int]windowSortItem{}
		for _, o := range w.OrderBy {
			r := refs[parserExprKey(o.Expr)]
			if _, dup := orderOf[r]; !dup {
				orderOf[r] = windowSortItem{ref: r, desc: o.Desc, nullsFirst: sortByNullsFirst(o)}
			}
		}
		for _, p := range w.PartitionBy {
			it := windowSortItem{ref: refs[parserExprKey(p)]}
			if o, ok := orderOf[it.ref]; ok {
				it = o
			}
			if !seen[it] {
				seen[it] = true
				items = append(items, it)
			}
		}
		for _, o := range w.OrderBy {
			it := windowSortItem{ref: refs[parserExprKey(o.Expr)], desc: o.Desc, nullsFirst: sortByNullsFirst(o)}
			if !seen[it] {
				seen[it] = true
				items = append(items, it)
			}
		}
		orders[i] = items
	}
	perm := make([]int, len(defs))
	for i := range perm {
		perm[i] = i
	}
	sort.SliceStable(perm, func(a, b int) bool {
		return commonPrefixCmp(orders[perm[a]], orders[perm[b]]) < 0
	})
	return perm
}

// commonPrefixCmp is planner.c's common_prefix_cmp.
func commonPrefixCmp(a, b []windowSortItem) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		switch {
		case x.ref > y.ref:
			return -1
		case x.ref < y.ref:
			return 1
		case x.desc && !y.desc:
			return -1
		case !x.desc && y.desc:
			return 1
		case x.nullsFirst && !y.nullsFirst:
			return -1
		case !x.nullsFirst && y.nullsFirst:
			return 1
		}
	}
	switch {
	case len(a) > len(b):
		return -1
	case len(a) < len(b):
		return 1
	}
	return 0
}
