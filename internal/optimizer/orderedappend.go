package optimizer

import (
	"math"

	"github.com/goopg/goopg/internal/parser"
)

// mergeAppendCost is cost_merge_append (costsize.c) over inputs whose summed
// startup and total costs are given: a heap of N streams built at startup,
// log2 N comparisons per output tuple, and cpu_tuple_cost *
// APPEND_CPU_COST_MULTIPLIER per tuple for the Merge Append's own overhead.
// PG treats a single stream as two ("N = (n_streams < 2) ? 2.0 : n_streams").
func mergeAppendCost(cp costParams, streams int, rows, inputStartup, inputTotal float64) Cost {
	n := float64(streams)
	if n < 2 {
		n = 2
	}
	logN := math.Log2(n)
	comparison := 2.0 * cp.cpuOperatorCost
	startup := comparison * n * logN
	run := rows*comparison*logN + cp.cpuTupleCost*appendCPUCostMultiplier*rows
	return Cost{Startup: startup + inputStartup, Total: startup + run + inputTotal}
}

// unionAllMembers flattens a plain serial UNION ALL chain — PG's appendrel —
// into its member plans, left to right. It declines (nil) on anything that is
// not one: a distinct UNION's folded input, a chain that already merges, a
// parallel-aware link, or a link whose branches were coerced from differing
// types (the appendrel PG would not have flattened, SetOp.TlistTypesDiffer).
func unionAllMembers(n Node) []Node {
	so, ok := n.(*SetOp)
	if !ok || !unionAllAppendLink(so) {
		return nil
	}
	var members []Node
	var walk func(Node) bool
	walk = func(x Node) bool {
		if l, ok := x.(*SetOp); ok {
			if !unionAllAppendLink(l) {
				return false
			}
			return walk(l.Left) && walk(l.Right)
		}
		members = append(members, x)
		return true
	}
	if !walk(so) {
		return nil
	}
	return members
}

func unionAllAppendLink(so *SetOp) bool {
	return so != nil && so.Op == parser.SetOpUnion && so.All && so.pinnedSchema == nil &&
		len(so.MergeKeys) == 0 && !so.UnionDistinctInput && !so.ParallelAware &&
		!so.TlistTypesDiffer
}

// orderedAppendInput is generate_orderedappend_paths (allpaths.c) for the one
// ordering goopg's grouping stage asks of it: the sort order `keys` over a
// UNION ALL appendrel's output columns. PG files, for every ordering some
// child can deliver, a Merge Append whose children each supply that ordering
// — the child's own sorted path when it has one, else an explicit Sort
// (create_merge_append_path) — and add_paths_to_grouping_rel then consumes it
// as presorted input. TPC-DS Q33/Q56/Q60 group a UNION ALL of per-channel
// GroupAggregates that each emit rows ordered on the outer group key; PG
// merges them where goopg sorted the whole Append again.
//
// Only positional keys are expressible here: every key must name one of the
// Append's output columns. At least one member must already deliver the
// ordering — a Merge Append whose every child needs a Sort is an ordering
// PG's all_child_pathkeys never proposes.
//
// Returns the Merge Append chain (left-deep SetOp links carrying MergeKeys,
// as addUnionMergeAppendPath builds) and its path, or nil.
func orderedAppendInput(rel *RelOptInfo, child Node, keys []SortKey, cp costParams) (Node, *Path) {
	members := unionAllMembers(child)
	if len(members) < 2 || len(keys) == 0 {
		return nil, nil
	}
	out := child.Output()
	idx := make([]int, len(keys))
	mergeKeys := make([]SortKey, len(keys))
	for i, k := range keys {
		cr, ok := k.Expr.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(out) {
			return nil, nil
		}
		idx[i] = cr.Index
		c := out[cr.Index]
		mergeKeys[i] = SortKey{Expr: &ColumnRef{Index: cr.Index, Name: c.Name, Type: c.Type}, Desc: k.Desc, NullsFirst: k.NullsFirst}
	}
	pathkeys := pathkeysForSortKeys(mergeKeys)

	var startupSum, totalSum, rows float64
	disabled, presorted := 0, 0
	inputs := make([]Node, 0, len(members))
	for _, m := range members {
		mOut := m.Output()
		if len(mOut) != len(out) {
			return nil, nil
		}
		seed := seedPathForNode(rel, m)
		if memberDeliversOrdering(m, idx, keys) {
			presorted++
			startupSum += seed.Cost.Startup
			totalSum += seed.Cost.Total
			rows += seed.Rows
			disabled += seed.DisabledNodes
			inputs = append(inputs, m)
			continue
		}
		sp := sortPathForBounded(seed, pathkeys, cp, -1)
		startupSum += sp.Cost.Startup
		totalSum += sp.Cost.Total
		rows += sp.Rows
		disabled += sp.DisabledNodes
		sortKeys := make([]SortKey, len(keys))
		for i, k := range keys {
			c := mOut[idx[i]]
			sortKeys[i] = SortKey{Expr: &ColumnRef{Index: idx[i], Name: c.Name, Type: c.Type}, Desc: k.Desc, NullsFirst: k.NullsFirst}
		}
		srt := &Sort{pos: m.Pos(), Child: m, Keys: sortKeys}
		srt.setPlanCost(PlanCost{StartupCost: sp.Cost.Startup, TotalCost: sp.Cost.Total, PlanRows: sp.Rows, PlanWidth: TupleWidth(mOut)})
		inputs = append(inputs, srt)
	}
	if presorted == 0 {
		return nil, nil
	}
	head := child.(*SetOp)
	var chain Node = inputs[0]
	for _, in := range inputs[1:] {
		chain = &SetOp{pos: head.pos, Left: chain, Right: in, Op: parser.SetOpUnion, All: true, MergeKeys: mergeKeys}
	}
	merge := newPrebuiltPath(rel, chain)
	merge.Rows = rows
	merge.Cost = mergeAppendCost(cp, len(inputs), rows, startupSum, totalSum)
	merge.Pathkeys = pathkeys
	merge.DisabledNodes = disabled
	return chain, merge
}

// memberDeliversOrdering reports whether member m already emits its rows in
// the order keys name by output position (pathkeys_contained_in over the
// member's own ordering, memberOrdering).
func memberDeliversOrdering(m Node, idx []int, keys []SortKey) bool {
	have := memberOrdering(m, 0)
	if len(have) < len(keys) {
		return false
	}
	for i, k := range keys {
		cr, ok := have[i].Expr.(*ColumnRef)
		if !ok || cr.Index != idx[i] ||
			have[i].SortAsc == k.Desc || have[i].NullsFirst != k.NullsFirst {
			return false
		}
	}
	return true
}

// memberOrdering is the ordering an appendrel member emits, in its own output
// positions: convert_subquery_pathkeys over the member subquery's target
// list. A UNION ALL member is usually a Project choosing and renaming its
// body's columns (TPC-DS Q33's `SELECT i_manufact_id, total_sales FROM ss`),
// over an inlined CTE reference or a subquery scan whose rows are the body's,
// position for position. A key crosses the Project only as a bare column
// reference; like convert_subquery_pathkeys, the translation stops at the
// first key the target list does not carry, keeping a true prefix.
func memberOrdering(n Node, depth int) []PathKey {
	if n == nil || depth > 16 {
		return nil
	}
	switch x := n.(type) {
	case *Project:
		below := memberOrdering(x.Child, depth+1)
		var keys []PathKey
		for _, bk := range below {
			cr, ok := bk.Expr.(*ColumnRef)
			if !ok {
				break
			}
			pos := -1
			for j, t := range x.Targets {
				if tc, ok := t.(*ColumnRef); ok && tc.Index == cr.Index {
					pos = j
					break
				}
			}
			if pos < 0 || pos >= len(x.Output()) {
				break
			}
			c := x.Output()[pos]
			keys = append(keys, PathKey{Expr: &ColumnRef{Index: pos, Name: c.Name, Type: c.Type}, SortAsc: bk.SortAsc, NullsFirst: bk.NullsFirst})
		}
		return keys
	case *CTEScan:
		if x.Inlined() && x.Child != nil && len(x.Child.Output()) == len(x.Output()) {
			return memberOrdering(x.Child, depth+1)
		}
		return nil
	case *SubqueryScan:
		if x.Child != nil && len(x.Child.Output()) == len(x.Output()) {
			return memberOrdering(x.Child, depth+1)
		}
		return nil
	}
	return inputNodePathkeys(n)
}
