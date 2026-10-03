package optimizer

// M0146-0005dr — SS_charge_for_initplans (subselect.c:2248): the cost of a
// query level's initPlans is charged to the plan node that runs them.
//
// PG gives every query level (the statement, each sublink's subquery, each
// subquery it keeps as a Subquery Scan) its own list of initPlans: the
// uncorrelated sublinks it turned into InitPlans, and — SS_process_ctes —
// one per kept CTE. SS_charge_for_initplans adds their summed
// startup_cost + per_call_cost (SS_compute_initplan_cost, subselect.c:2312)
// to both the startup and the total cost of every path of the level's final
// rel, so the plan node at the top of the level carries the work its
// initPlans do; SS_attach_initplans hangs them there. TPC-DS Q59's Limit is
// 62384.58 in PG — its Sort's 8024.44 plus the `wss` CTE's 54360.13 — and
// goopg printed 7949.85, the Sort alone.
//
// goopg plans initPlans and CTE bodies as subtrees hung off expressions and
// CTE scans, priced nowhere above themselves. chargeInitPlans runs once, at
// Plan()'s tail while the Subquery Scan wrappers still mark PG's levels, and
// records each level's charge on its top node; the cost display
// (legacyDisplayCostOf / explainCostFields) adds it, so every node above a
// charged top — derived from its children — carries it too, as PG's do.

// InitPlanCharge is embedded in the plan nodes that can be the top of a
// query level. It holds that level's initPlan cost (zero elsewhere).
type InitPlanCharge struct {
	initPlanCharge float64
}

// InitPlanChargeCost is the cost of the initPlans this node runs.
func (c *InitPlanCharge) InitPlanChargeCost() float64 { return c.initPlanCharge }

func (c *InitPlanCharge) setInitPlanCharge(v float64) { c.initPlanCharge = v }

type initPlanCharger interface {
	InitPlanChargeCost() float64
	setInitPlanCharge(float64)
}

// InitPlanChargeOf returns n's initPlan charge, zero when n carries none.
func InitPlanChargeOf(n Node) float64 {
	if c, ok := n.(initPlanCharger); ok {
		return c.InitPlanChargeCost()
	}
	return 0
}

// withInitPlanCharge adds n's charge to a display cost.
func withInitPlanCharge(n Node, pc PlanCost) PlanCost {
	if ch := InitPlanChargeOf(n); ch != 0 {
		pc.StartupCost += ch
		pc.TotalCost += ch
	}
	return pc
}

// chargeInitPlans computes and records every query level's initPlan charge
// beneath root. Idempotent: a charge is set, never added, so the re-entrant
// Plan() calls a view body or EXPLAIN's inner statement makes leave the same
// numbers.
func chargeInitPlans(root Node) {
	if root == nil {
		return
	}
	c := &initPlanChargeWalk{done: map[Node]bool{}, cteSeen: map[Node]bool{}}
	c.level(root)
	// CTE bodies: PG charges each kept CTE to the level whose WITH declared
	// it. goopg's plan carries no declaring-level marker — EXPLAIN hoists
	// every `CTE <name>` section to the root for the same reason
	// (explain_cte.go) — so the statement's top is charged with every kept
	// body, which is the declaring level for a top-level WITH.
	if len(c.cteBodies) > 0 {
		target := chargeTarget(root)
		total := InitPlanChargeOf(target)
		for _, b := range c.cteBodies {
			total += legacyDisplayCostOf(chargeTarget(b)).TotalCost
		}
		if ch, ok := target.(initPlanCharger); ok {
			ch.setInitPlanCharge(total)
		}
	}
}

type initPlanChargeWalk struct {
	done      map[Node]bool
	cteSeen   map[Node]bool
	cteBodies []Node
}

// level charges the query level whose top node is top: the sum of
// initPlanCost over the uncorrelated sublinks hung off the level's own
// nodes. A sublink's plan, a kept Subquery Scan's subplan and a CTE body
// are levels of their own, charged first so a parent reads their charged
// cost.
func (c *initPlanChargeWalk) level(top Node) {
	if top == nil || c.done[top] {
		return
	}
	c.done[top] = true
	charge := 0.0
	counted := map[Node]bool{}
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		for _, sl := range NodeSublinks(n) {
			if sl.Plan == nil {
				continue
			}
			c.level(sl.Plan)
			if SublinkIsInitPlan(sl.Expr) && !counted[sl.Plan] {
				counted[sl.Plan] = true
				charge += initPlanCost(sl.Expr, sl.Plan)
			}
		}
		switch x := n.(type) {
		case *SubqueryScan:
			c.level(x.Child)
			return
		case *CTEScan:
			if x.Child == nil {
				return
			}
			c.level(x.Child)
			if !x.Inlined() && !c.cteSeen[x.Child] {
				c.cteSeen[x.Child] = true
				c.cteBodies = append(c.cteBodies, x.Child)
			}
			return
		}
		kids, _ := planChildNodes(n)
		for _, k := range kids {
			walk(k)
		}
	}
	walk(top)
	if ch, ok := chargeTarget(top).(initPlanCharger); ok {
		ch.setInitPlanCharge(charge)
	}
}

// chargeTarget is the node a level's charge is recorded on: its top, below
// any Project. PG's projection is the final path's target, not a node, and
// EXPLAIN renders a goopg Project into the node beneath it
// (walkPlanFiltered), so that node is where PG's final-rel path shows.
func chargeTarget(top Node) Node {
	for {
		p, ok := top.(*Project)
		if !ok || p.Child == nil {
			return top
		}
		top = p.Child
	}
}

// initPlanCost is SS_compute_initplan_cost's per-initPlan term,
// startup_cost + per_call_cost as cost_subplan (costsize.c) sets them for a
// non-hashed sublink with no parameters: an EXISTS fetches one row
// (startup + run cost / rows), every other kind reads all of them (the
// plan's total cost). The test expression's own cost is not added.
func initPlanCost(e Expr, plan Node) float64 {
	// The subplan's cost is its top printed node's, as for the charge.
	pc := legacyDisplayCostOf(chargeTarget(plan))
	if _, isExists := e.(*ExistsExpr); isExists {
		rows := pc.PlanRows
		if rows < 1 {
			rows = 1
		}
		return pc.StartupCost + (pc.TotalCost-pc.StartupCost)/rows
	}
	return pc.TotalCost
}
