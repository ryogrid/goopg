package executor

import "github.com/goopg/goopg/internal/optimizer"

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
// Deliberately not modelled (deferral ledger): subqueries in FROM are
// planned in range-table order by set_base_rel_sizes, after every sublink of
// the level (Q58's InitPlan 2/1/3); a CTE declared inside a sublink body is
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
	walk = func(n optimizer.Node) {
		if n == nil {
			return
		}
		for _, ref := range optimizer.NodeSublinks(n) {
			sublink(ref)
		}
		for _, c := range renderChildren(n, r.cte) {
			walk(c)
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
