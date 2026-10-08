package executor

import (
	"sort"

	"github.com/goopg/goopg/internal/optimizer"
)

// M0146-0005bv — SubPlan/InitPlan numbers follow PG's plan_id.
//
// PG numbers a SubPlan or InitPlan by its position in glob->subplans
// (build_subplan / SS_process_ctes / SS_make_initplan_from_plan,
// optimizer/plan/subselect.c), i.e. in PLANNING order, and CTE plans take
// ids from the same sequence without printing them:
//
//   - SS_process_ctes runs first in subquery_planner, one subplan per
//     non-inlined WITH entry in declaration order, each appended after its
//     own body was planned — so the body's sublinks come before the CTE;
//   - make_subplan plans the sublink's subquery before build_subplan
//     appends it, so nested sublinks come before the one holding them;
//   - a simple EXISTS convertible to a hashable ANY is planned twice
//     (the EXISTS SubPlan, then the hashed ANY one) and setrefs keeps one
//     of the pair, so the hashed form prints the second id.
//
// goopg numbered sublinks in render order from 1, so every CTE statement
// was off by the CTE count (TPC-DS Q1/Q30/Q81 `SubPlan 1` for PG's
// `SubPlan 2`, Q14's InitPlans 1-4 for 3-6) and every nested sublink was
// inverted. reservePGPlanIDs walks the plan once before rendering and
// reserves the numbers in that order: CTE sections, then the plan spine
// pre-order (a node's own sublinks before its children's, the way
// preprocess_expression reaches the targetlist before the quals below it).
//
// M0146-0104/0105: the spine is numbered one query level at a time
// (optimizer.IsQueryLevelTop marks each level's top). preprocess_expression
// reaches a level's sublinks in query order — the targetlist (which holds
// the ORDER BY and GROUP BY items as resjunk entries), the jointree's quals,
// HAVING, LIMIT — and within each part in source order, not in the order
// goopg's chosen plan happens to place them: TPC-DS Q6's `d_month_seq =
// (select …)` is PG's InitPlan 1 and the correlated `avg` SubPlan 2, though
// goopg evaluates the SubPlan on a Gather above the scan that reads the
// InitPlan. So a level's sublinks are numbered by that part
// (preprocessRank), then by source position (plan order when any position
// is unknown). Subqueries in FROM that stay levels of their
// own are planned later, by set_base_rel_sizes, so their sublinks number
// after every sublink of the enclosing level, in plan order.
//
// Deliberately not modelled (deferral ledger): a CTE declared inside a sublink body is
// planned during that sublink (goopg hoists every section to the root); the
// MIN/MAX InitPlans planagg.c makes during grouping_planner come after the
// level's quals; and a sublink goopg decorrelated still consumed an id in PG.
func (r *subPlanReg) reservePGPlanIDs(root optimizer.Node) {
	if r == nil || root == nil {
		return
	}
	var walk func(n optimizer.Node)
	sublink := func(ref optimizer.SublinkRef) {
		if _, done := r.planID[ref.Plan]; done {
			return
		}
		walk(ref.Plan)
		r.lastID++
		if r.planID == nil {
			r.planID = map[optimizer.Node]int{}
		}
		r.planID[ref.Plan] = r.lastID
		if in, ok := ref.Expr.(*optimizer.InExpr); ok && in.UnknownEqFalse && subPlanUsesHashTable(in, r) {
			r.lastID++
			if r.hashedPlanID == nil {
				r.hashedPlanID = map[optimizer.Node]int{}
			}
			r.hashedPlanID[ref.Plan] = r.lastID
		}
	}
	// walk numbers the query level whose top is n: its own sublinks in
	// source order (each after its body), then the nested levels below it.
	walk = func(n optimizer.Node) {
		if n == nil {
			return
		}
		var refs []optimizer.SublinkRef
		var ranks []int
		var nested []optimizer.Node
		var collect func(x optimizer.Node)
		collect = func(x optimizer.Node) {
			if x == nil {
				return
			}
			if x != n && optimizer.IsQueryLevelTop(x) {
				nested = append(nested, x)
				return
			}
			own := optimizer.NodeSublinks(x)
			refs = append(refs, own...)
			for range own {
				ranks = append(ranks, preprocessRank(x))
			}
			for _, c := range renderChildren(x, r.cte) {
				collect(c)
			}
		}
		collect(n)
		known := true
		for _, ref := range refs {
			if ref.Expr == nil || ref.Expr.Pos() <= 0 {
				known = false
				break
			}
		}
		if known {
			idx := make([]int, len(refs))
			for i := range idx {
				idx[i] = i
			}
			sort.SliceStable(idx, func(a, b int) bool {
				i, j := idx[a], idx[b]
				if ranks[i] != ranks[j] {
					return ranks[i] < ranks[j]
				}
				return refs[i].Expr.Pos() < refs[j].Expr.Pos()
			})
			sorted := make([]optimizer.SublinkRef, len(refs))
			for k, i := range idx {
				sorted[k] = refs[i]
			}
			refs = sorted
		}
		for _, ref := range refs {
			sublink(ref)
		}
		for _, l := range nested {
			walk(l)
		}
	}
	if r.cte != nil {
		for _, sec := range r.cte.order {
			walk(sec.body)
			r.lastID++ // the CTE's own plan id
		}
	}
	walk(root)
}

// preprocessRank is the part of the query a node's expressions come from, in
// the order subquery_planner runs preprocess_expression over them
// (planner.c): the targetlist first — projections, and the ORDER BY, GROUP
// BY and window items it carries as resjunk entries — then the jointree's
// quals, then HAVING (a Filter over the aggregate), then LIMIT / OFFSET.
func preprocessRank(n optimizer.Node) int {
	switch x := n.(type) {
	case *optimizer.Project, *optimizer.Result, *optimizer.Sort, *optimizer.IncrementalSort,
		*optimizer.Aggregate, *optimizer.WindowAgg:
		return 0
	case *optimizer.Filter:
		c := x.Child
		for {
			f, ok := c.(*optimizer.Filter)
			if !ok {
				break
			}
			c = f.Child
		}
		if _, ok := c.(*optimizer.Aggregate); ok {
			return 2
		}
		return 1
	case *optimizer.Limit:
		return 3
	}
	return 1
}
