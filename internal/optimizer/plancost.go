package optimizer

import "math"

// PlanCost is PostgreSQL's per-node cost annotation — `startup_cost`,
// `total_cost`, `plan_rows` and `plan_width` of `struct Plan`
// (postgres/src/include/nodes/plannodes.h) — carried on goopg's plan nodes.
//
// WHY IT LIVES ON THE NODE. The path search computes a real cost for every
// candidate and `add_path` picks a winner by comparing them, but `createPlan`
// then threw the winning numbers away: EXPLAIN printed the literal string
// `cost=0.00..0.00 ... width=0` on every node of every plan. So no artefact in
// this repository stated what the planner believed, and no artefact could
// attribute a wrong plan to a wrong cost — which is the question the whole
// planner-refactor programme has to answer.
//
// The obvious alternative, a side index built during createPlan and handed to
// the renderer, does not work here. There is no per-statement planner context
// on the chain from `Plan` down to `createPlanNode`, `createPlanNode` returns
// its `outputLayout` bottom-up rather than threading state down, and a
// `map[Node]PlanCost` keyed by pointer would collapse genuinely shared subtrees
// into one entry (a second CTEScan reference shares the same body buffer —
// internal/executor/explain_cte.go). Putting the numbers ON the node dissolves
// all three: no channel is needed, and a shared node has one cost because it
// IS one node. That is also exactly what upstream does.
//
// design: docs/design/not_ralph/planner_refactor_take2/impl/P0-A-explain-instrument.md §3
type PlanCost struct {
	// StartupCost is the cost expended before the first row can be returned.
	StartupCost float64
	// TotalCost is the cost to return all rows.
	TotalCost float64
	// PlanRows is the estimated row count of THIS node.
	PlanRows float64
	// PlanWidth is the estimated average row width in bytes.
	PlanWidth int
	// CostSet distinguishes "priced at zero" from "never priced". Without it
	// a node the search did not produce is indistinguishable from a free one,
	// and the renderer cannot tell which nodes need the legacy derivation.
	CostSet bool
}

// PlanCostInfo returns the node's cost annotation and whether one was set.
//
// The field names are deliberately `StartupCost`/`PlanRows`/... rather than
// `Startup`/`Rows`: PlanCost is EMBEDDED in node structs, and shorter names
// would collide with existing fields (`Values.Rows`, for one) — a collision
// that promotes silently to whichever field the compiler prefers.
func (c *PlanCost) PlanCostInfo() (PlanCost, bool) { return *c, c.CostSet }

func (c *PlanCost) setPlanCost(v PlanCost) {
	v.CostSet = true
	*c = v
}

// PlanCostCarrier is implemented by every plan node that can carry the cost the
// search computed for it. Embedding PlanCost is the whole of implementing it.
type PlanCostCarrier interface {
	Node
	PlanCostInfo() (PlanCost, bool)
}

type planCostSetter interface {
	setPlanCost(PlanCost)
}

// TupleWidth is the exported form of tupleWidth — PG's set_rel_width analogue
// (costsize.c), summing per-column average widths from the same table
// get_typavgwidth consults. EXPLAIN's `width=` column is one of the consumers
// tupleWidth's own doc comment already named but did not yet have.
func TupleWidth(cols []SchemaColumn) int { return tupleWidth(cols) }

// stampPlanCost records on the emitted node the cost of the path that produced
// it. Called from the single funnel every search-produced node passes through,
// so a new path kind cannot forget to do it.
//
// Nodes the search did not produce — everything the legacy rewriter builds
// above the seam — are simply not carriers, or are carriers with CostSet
// false; the renderer prices those separately rather than printing zeros,
// because a plan mixing real costs with 0.00 is worse than one where all are
// 0.00: nothing can then distinguish a free node from an unpriced one.
func stampPlanCost(n Node, p *Path) {
	if n == nil || p == nil {
		return
	}
	setter, ok := n.(planCostSetter)
	if !ok {
		return
	}
	setter.setPlanCost(PlanCost{
		StartupCost: p.Cost.Startup,
		TotalCost:   p.Cost.Total,
		PlanRows:    p.Rows,
		PlanWidth:   TupleWidth(n.Output()),
	})
}

// DeriveLegacyDisplayCost prices a node the path search did not produce.
//
// SCOPE, STATED PLAINLY: this is NOT a cost model and nothing may plan against
// it. It exists so that EXPLAIN's cost column is legible while the upper
// planner is still a legacy node-tree rewriter — aggregation, sorting, limits,
// window functions, set operations and every statement the search declines are
// built above the seam and carry no Path. Printing 0.00 for those while the
// scan/join region prints real numbers would be worse than printing 0.00
// everywhere: a reader could not distinguish a genuinely free node from an
// unpriced one, and a cost-column diff against PostgreSQL would be noise.
//
// It is deliberately crude and monotone: a parent costs at least its child.
// Phase 4 of the planner refactor replaces every one of these nodes with a real
// upper-relation path priced by cost_agg / cost_sort / cost_limit, and deletes
// this function. Until then, its numbers say "the legacy estimate, made
// visible" and nothing more.
//
// design: docs/design/not_ralph/planner_refactor_take2/impl/P0-A-explain-instrument.md §4
func DeriveLegacyDisplayCost(n Node, rows int64) PlanCost {
	cp := defaultCostParams()
	out := PlanCost{
		PlanRows:  float64(rows),
		PlanWidth: 0,
	}
	if n == nil {
		return out
	}
	out.PlanWidth = TupleWidth(n.Output())

	childStartup, childTotal := 0.0, 0.0
	for _, c := range legacyDisplayChildren(n) {
		cc := legacyDisplayCostOf(c)
		// A node with several children pays for all of them and cannot start
		// before the slowest-to-start of them.
		childTotal += cc.TotalCost
		if cc.StartupCost > childStartup {
			childStartup = cc.StartupCost
		}
	}

	perRow := cp.cpuTupleCost * float64(rows)
	switch x := n.(type) {
	case *Sort:
		// PG's cost_sort charges a comparison term and is BLOCKING: nothing
		// emerges until the input is consumed, so startup is the child's
		// total (costsize.c, cost_tuplesort).
		out.StartupCost = childTotal + sortComparisonDisplayCost(cp, rows)
		out.TotalCost = out.StartupCost + perRow
	case *Aggregate:
		// Charge cpu_operator_cost per aggregate per input row, as cost_agg
		// does. A hashed or plain aggregate is blocking; a sorted one streams,
		// but goopg's legacy node does not distinguish reliably here and this
		// function is not permitted to guess about plan shape, so it takes the
		// blocking reading — the conservative one for a startup column.
		out.StartupCost = childTotal + cp.cpuOperatorCost*float64(len(x.Aggs))*childRowsOf(n)
		out.TotalCost = out.StartupCost + perRow
	default:
		// Pass-through wrappers: Project, Filter, Limit, Distinct, Result,
		// SetOp, WindowAgg, LockRows and the rest. They stream, so startup is
		// the child's startup.
		out.StartupCost = childStartup
		out.TotalCost = childTotal + perRow
	}
	return out
}

func legacyDisplayCostOf(n Node) PlanCost {
	if n == nil {
		return PlanCost{}
	}
	if c, ok := n.(PlanCostCarrier); ok {
		if pc, set := c.PlanCostInfo(); set {
			return pc
		}
	}
	return DeriveLegacyDisplayCost(n, EstimateRows(n))
}

func childRowsOf(n Node) float64 {
	var total float64
	for _, c := range legacyDisplayChildren(n) {
		total += float64(EstimateRows(c))
	}
	return total
}

// sortComparisonDisplayCost is cost_sort's comparison term, 2*N*log2(N) charged
// at cpu_operator_cost (costsize.c). The whole-sort variants (external merge,
// bounded top-N) are Phase 4's business; this is the in-memory shape only.
func sortComparisonDisplayCost(cp costParams, rows int64) float64 {
	if rows <= 1 {
		return 0
	}
	n := float64(rows)
	return 2.0 * cp.cpuOperatorCost * n * math.Log2(n)
}

// legacyDisplayChildren returns the child nodes DeriveLegacyDisplayCost must
// price beneath n.
//
// It is deliberately a SEPARATE, smaller walker from the renderer's
// planChildren: this one exists only to make a cost monotone, so an arm it
// lacks costs a node slightly low rather than hiding a subtree, whereas a
// missing arm in the renderer's walker makes a plan print short (which is why
// that one has its own coverage gate). Keeping them apart stops a display
// concern from constraining the renderer's tree.
func legacyDisplayChildren(n Node) []Node {
	switch p := n.(type) {
	case *Project:
		return []Node{p.Child}
	case *Filter:
		return []Node{p.Child}
	case *Sort:
		return []Node{p.Child}
	case *Limit:
		return []Node{p.Child}
	case *Distinct:
		return []Node{p.Child}
	case *DistinctOn:
		return []Node{p.Child}
	case *Aggregate:
		return []Node{p.Child}
	case *WindowAgg:
		return []Node{p.Child}
	case *Gather:
		return []Node{p.Child}
	case *GatherMerge:
		return []Node{p.Child}
	case *LockRows:
		return []Node{p.Child}
	case *Memoize:
		return []Node{p.Child}
	case *Result:
		if p.Child == nil {
			return nil
		}
		return []Node{p.Child}
	case *Join:
		return []Node{p.Left, p.Right}
	case *RecursiveUnion:
		return []Node{p.Anchor, p.Recursive}
	case *SetOp:
		return []Node{p.Left, p.Right}
	}
	return nil
}
