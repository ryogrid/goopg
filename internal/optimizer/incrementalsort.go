package optimizer

// incrementalsort.go — M0141-S7-exec-a: the `IncrementalSort` optimizer.Node
// type, PG's `IncrementalSort` plan node (nodeIncrementalSort.c /
// plannodes.h's `nPresortedCols`). Mirrors `Sort` (plan.go) with one
// addition, `PresortedCount`: the number of leading `Keys` the child already
// delivers in order.
//
// STANDALONE by design, same posture `pathkeysCountContainedIn`
// (pathkeys.go) and `costIncrementalSort` (cost_funcs.go) used before it:
// `createPlanNode` (createplansimple.go) has no arm producing this node yet
// (M0141-S7-exec-b), so it has zero production callers today — constructed
// only by this task's own unit tests and, later, exec-b's `createPlanNode`
// arm for `PathIncrementalSort` (path.go).
type IncrementalSort struct {
	// PlanCost carries the search's cost for this node (plancost.go).
	PlanCost
	// searchedTree: mirrors Sort's own embed (searchedtree.go).
	searchedTree
	pos   int
	Child Node
	Keys  []SortKey

	// PresortedCount is PG's nPresortedCols: 0 < PresortedCount < len(Keys).
	// The child's output already arrives ordered by Keys[:PresortedCount]
	// (each key under its own direction/NULL placement); only the
	// remaining keys need sorting within each presorted-prefix group. See
	// the executor operator's own contract note
	// (internal/executor/operators_incremental_sort.go) for the equality
	// rule that defines a group.
	PresortedCount int
}

func (n *IncrementalSort) Pos() int       { return n.pos }
func (n *IncrementalSort) Output() Schema { return n.Child.Output() }
