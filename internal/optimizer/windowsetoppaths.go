package optimizer

// The WINDOW and SETOP upper rels — planner-refactor take3 C-18 (P4-09).
//
// This is the last pair of Phase-4 upper producers, on the C-11 registry and
// the structural template C-12/C-15/C-16 established: fetch the rel, size it,
// seed it with the finished input(s) as `PathPrebuilt`, offer candidates,
// `setCheapest`, `getCheapestFractionalPath`, and rebuild the winner through
// `createPlanNode`.
//
// Design: docs/design/planner-p4-window-setop-paths/DESIGN.md. Two honest
// facts that design states first and this file repeats, because a reviewer
// looking for a plan move will otherwise read the gate wrong:
//
//  1. NEITHER HALF HAS A CHOICE TO OFFER, and that is the design. goopg's
//     `windowOp` (`internal/executor/operators_window.go:14`) sorts by
//     PARTITION BY/ORDER BY *internally* — PG's `create_one_window_path`
//     (planner.c:4620) stacks a Sort/IncrementalSort above the input because
//     PG's `nodeWindowAgg` assumes sorted input, and goopg's does not — so
//     there is no presorted variant to build; and `setOp`
//     (`operators_setop.go:16`) has exactly one form per node. Above the
//     search seam the inputs are finished Nodes with no pathkeys, so no
//     order-aware variant can be constructed either (C-14 Incremental Sort is
//     BLOCKED with no executor counterpart — that is the resume point for a
//     second window candidate).
//
//  2. THE PRICES ARE SELECTION-NEUTRAL AND DISPLAY-INVISIBLE. `*WindowAgg`
//     and `*SetOp` carry no `PlanCost` (no `planCostSetter`), so EXPLAIN
//     recomputes the legacy display number exactly as before, just as for
//     `*Aggregate`/`*Distinct`. A single candidate always wins its own rel.
//     The gate therefore asserts SHAPE IDENTITY, never cost movement; any
//     plan diff at all is a defect.
//
// What the cut buys is nonetheless real: the window sort and the set-op hash
// are PRICED for the first time (load-bearing the day a second candidate
// exists — C-14 for windows, a sorted set-op strategy for set-ops), and
// EVERY upper rel now exists, which is precisely what makes C-17's
// "`tuple_fraction` reaches every upper rel" a census rather than a build.
//
// DEVIATION FROM THE DESIGN, recorded here rather than silently: the design
// named three `*SetOp` construction sites (planner.go:1047, 3480, 3531). Only
// the first is a set operation. The other two are the partition- and
// inheritance-expansion fan-outs, which build `*SetOp{All: true}` as goopg's
// UNION ALL *node*; in PG those are APPENDRELS
// (`expand_inherited_rtentry` → `add_paths_to_append_rel`,
// allpaths.c:1300ff), planned as `AppendPath` over base rels far BELOW the
// upper-rel pipeline, and `plan_set_operations` (prepunion.c:93) never sees
// them. Filing them on the SETOP upper rel would transcribe a goopg node-reuse
// coincidence as a PG structure. They stay where they are; an APPEND path over
// an appendrel is its own item.

import (
	"github.com/goopg/goopg/internal/parser"
)

// Producer strings for the DPPATH trace (pathtrace.go). With `Relids = 0`
// the lines read `producer=upper.window.* relids=-`, the convention C-12
// established.
const (
	windowProducer      = "upper.window.sorted"
	setOpAppendProducer = "upper.setop.append"
	setOpHashedProducer = "upper.setop.hashed"
)

// createWindowPaths is `create_window_paths` (planner.c:4533) plus
// `create_one_window_path` (planner.c:4620) for the chain
// `buildWindowStage` just built: `windows` is one `*WindowAgg` per distinct
// window specification, BOTTOM-UP (the order the groups were folded, which is
// upstream's `activeWindows` order), and `input` is the finished Node below
// the first of them.
//
// The shape is upstream's exactly, and it is the reason this is ONE call
// rather than one per group: `create_one_window_path` walks `activeWindows`
// stacking a WindowAggPath per clause on top of the previous one and calls
// `add_path` ONCE, on the topmost. So the (WINDOW, NULL) rel receives a single
// candidate spanning the whole chain, and the intermediate prices ride inside
// it. Calling the producer per group instead would file every group on the one
// relids-0 rel and let `set_cheapest` answer group 2's question with group 1's
// candidate.
//
// PG loops over `input_rel->pathlist` for the outer choice (cheapest-total
// plus any path satisfying `root->window_pathkeys`); goopg's input is a single
// finished Node, so that loop has one iteration and the rel gets one
// candidate. `set_cheapest` and the fractional pick still run, on the same
// rails as every other upper rel.
//
// Returns a FRESH `*WindowAgg` chain carrying the same specs over the same
// input — the caller adopts the returned top. An empty path list yields PG's
// "could not implement window function" refusal; unreachable (one candidate is
// always offered), defensive as C-15/C-16.
func createWindowPaths(u *upperRels, windows []*WindowAgg, input Node, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if len(windows) == 0 {
		return nil, &PlanError{Code: "XX000", Message: "createWindowPaths: no window nodes"}
	}
	top := windows[len(windows)-1]
	if input == nil {
		return nil, &PlanError{Pos: top.Pos(), Code: "XX000", Message: "createWindowPaths: window chain with no input"}
	}
	if u == nil {
		u = newUpperRels()
	}
	cp := ps.costParams()
	winRel := fetchUpperRel(u, UpperWindow, 0, tupleFraction)
	sizeWindowRelFromNode(winRel, top, input)

	seed := seedPathForNode(winRel, input)
	addWindowPaths(winRel, seed, windows, input, cp)
	setCheapest(winRel)

	best := getCheapestFractionalPath(winRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: top.Pos(), Code: "0A000",
			Message: "could not implement window function"}
	}
	node, _ := createPlanNode(best)
	win, ok := node.(*WindowAgg)
	if !ok || win == nil {
		return nil, &PlanError{Pos: top.Pos(), Code: "XX000",
			Message: "createWindowPaths: PathWindow built no window node"}
	}
	return win, nil
}

// sizeWindowRelFromNode sizes the WINDOW rel from the chain's TOP node (the
// rel's output — PG's `output_target`) and its `input` (the row count).
//
// Rows is the INPUT's count, not a reduced one: a WindowAgg emits exactly one
// row per input row (`cost_windowagg`: "path->rows = input_tuples",
// costsize.c:3165). Width/NCols/AvgVarBytes describe the rel's own output —
// the DESIGN §4.3 duty a producer may not skip, because `costSortRun`'s
// external-merge arm is gated on `ncols > 0` and a fresh `RelOptInfo` has
// none, so an unsized rel prices a spilling sort as an in-memory quicksort.
func sizeWindowRelFromNode(rel *RelOptInfo, top *WindowAgg, input Node) {
	if rel == nil || top == nil {
		return
	}
	cols := top.Output()
	rel.Rows = seedRowsForNode(input)
	rel.Width = nodeTupleWidth(top)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// seedRowsForNode is the row count of a finished Node, clamped non-negative:
// `legacyDisplayCostOf`'s `PlanRows`, which is the path's OWN count for a
// search-produced child (it carries a `PlanCost`) and the legacy estimator's
// otherwise. That is the read `sizeUpperRelFromNode` (C-12) makes, and the two
// agree wherever both exist. Temporary for the same reason C-12 gives: there
// is no other row count above the seam until C-20a retires `EstimateRows`.
func seedRowsForNode(n Node) float64 {
	if n == nil {
		return 0
	}
	r := legacyDisplayCostOf(n).PlanRows
	if r < 0 {
		return 0
	}
	return r
}

// costWindow is `cost_windowagg` (costsize.c:3098) with the two goopg
// adaptations this file's header names.
//
// Upstream's terms, transcribed:
//
//   - per window function, `argcosts.per_tuple × input_tuples`
//     (costsize.c:3151). goopg's catalog HAS `procost` but the planner never
//     reads it (the F3 finding C-15's `costAgg` recorded), so each function is
//     charged a flat `cpu_operator_cost` per input row — upstream's
//     per-function/per-input-row SHAPE with a flat rate, which is what lets a
//     real `procost` plug in later without re-plumbing.
//   - `cpu_operator_cost × (numPartCols + numOrderCols) × input_tuples` for
//     the grouping comparisons (costsize.c:3161).
//   - `cpu_tuple_cost × input_tuples` general overhead (costsize.c:3162).
//
// The goopg-specific term is the SORT. Upstream's input is already sorted —
// `create_one_window_path` stacks a `create_sort_path` above the input and
// prices it as a separate path (planner.c:4676) because `nodeWindowAgg`
// assumes sorted input. goopg's `windowOp` drains its child and sorts
// internally (`operators_window.go` Open), so the sort is part of THIS node's
// price or it is charged nowhere at all.
//
// It is priced with `costSortRun` over the INPUT's column count and
// variable-width payload — never over the key count, which is a different
// quantity: `ncols` sizes one ROW through `hashsize.EntryBytes` for the
// external-merge arm, so passing the key count there would model a 2-column
// row and silently suppress the disk charge for a wide one.
//
// Because the executor sorts before emitting anything, the node BLOCKS:
// startup is the input's whole total plus the sort's comparison work, not the
// input's startup. Upstream's `get_windowclause_startup_tuples` proration
// (costsize.c:3178) has no analogue and is deliberately absent — it exists to
// reward a streaming WindowAgg that can stop early, and goopg's cannot.
func costWindow(cp costParams, inputTotal, inputRows float64,
	numPartCols, numOrderCols, numFuncs, inNcols int, inAvgVarBytes float64) Cost {
	tuples := inputRows
	if tuples < 0 {
		tuples = 0
	}
	sortRun := costSortRun(cp, tuples, inNcols, inAvgVarBytes, -1)
	// The sort consumes the input in full, so the input's TOTAL is the
	// blocking node's startup floor; `costSortRun` prices only the sort's own
	// work, split the same way it splits it for a `PathSort`.
	startup := inputTotal + sortRun.Startup
	total := startup + (sortRun.Total - sortRun.Startup)
	total += cp.cpuOperatorCost * float64(numFuncs) * tuples
	total += cp.cpuOperatorCost * float64(numPartCols+numOrderCols) * tuples
	total += cp.cpuTupleCost * tuples
	return Cost{Startup: startup, Total: total}
}

// addWindowPaths is `create_one_window_path`'s body: stack ONE `PathWindow`
// per window spec group on top of the previous one, and `add_path` the
// topmost — once (planner.c:4620-4760). The intermediate paths are real and
// priced, and they reach the rel only as `Children` of the one that is added,
// exactly as upstream's intermediate `WindowAggPath`s do.
//
// No Sort is stacked between levels (the executor sorts internally, and
// `costWindow` charges it), and therefore no presorted second candidate exists
// — see the file header's fact 1.
func addWindowPaths(winRel *RelOptInfo, seed *Path, windows []*WindowAgg, input Node, cp costParams) {
	if len(windows) == 0 {
		// No spec group, no candidate. `createWindowPaths` refuses this case
		// before calling here; the guard keeps a direct caller (a test) from
		// reaching `addPath` with a nil path.
		return
	}
	below := seed
	belowNode := input
	var top *Path
	for _, w := range windows {
		cols := belowNode.Output()
		p := &Path{
			Kind: PathWindow, Window: w,
			Rel: winRel, Rows: winRel.Rows,
			DisabledNodes: below.DisabledNodes,
			Cost: costWindow(cp, below.Cost.Total, below.Rows,
				len(w.PartitionBy), len(w.OrderBy), len(w.Funcs),
				len(cols), nodeAvgVarBytes(cols)),
			Children: []*Path{below},
		}
		below = p
		belowNode = w
		top = p
	}
	addPath(winRel, top, windowProducer)
}

// createSetOpPaths is the set-operation half of `plan_set_operations`
// (prepunion.c:93) for goopg's one shape: the finished `*SetOp` the
// `applySetOp` fold just built, over two finished branch Nodes.
//
// It is the ONLY two-input upper candidate in the tree (`Children` holds the
// left and right seeds in that order, which is the order
// `createSetOpPlan` reads them back in).
//
// Returns a FRESH `*SetOp` with the same Op/All over the same branches; the
// caller adopts it.
func createSetOpPaths(u *upperRels, setOpNode *SetOp, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if setOpNode == nil {
		return nil, &PlanError{Code: "XX000", Message: "createSetOpPaths: nil set-op node"}
	}
	if setOpNode.Left == nil || setOpNode.Right == nil {
		return nil, &PlanError{Code: "XX000", Message: "createSetOpPaths: set-op node with a missing branch"}
	}
	if u == nil {
		u = newUpperRels()
	}
	cp := ps.costParams()
	// One rel PER NODE, as PG keys its SETOP rel by the node's relids
	// (prepunion.c:805) — see `newUpperRelForNode` for why sharing one
	// relids-0 rel across a chain returns the wrong subtree.
	setOpRel := newUpperRelForNode(u, UpperSetOp, tupleFraction)
	sizeSetOpRelFromNode(setOpRel, setOpNode)

	lseed := seedPathForNode(setOpRel, setOpNode.Left)
	rseed := seedPathForNode(setOpRel, setOpNode.Right)

	addSetOpPaths(setOpRel, lseed, rseed, setOpNode, cp)
	setCheapest(setOpRel)

	best := getCheapestFractionalPath(setOpRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: setOpNode.Pos(), Code: "0A000",
			Message: "could not implement set operation"}
	}
	node, _ := createPlanNode(best)
	so, ok := node.(*SetOp)
	if !ok || so == nil {
		return nil, &PlanError{Pos: setOpNode.Pos(), Code: "XX000",
			Message: "createSetOpPaths: PathSetOp built no set-op node"}
	}
	return so, nil
}

// seedPathForNode wraps a finished branch Node as a `PathPrebuilt` carrying
// its legacy rows and cost — the C-12 door, once per branch.
func seedPathForNode(rel *RelOptInfo, n Node) *Path {
	seed := newPrebuiltPath(rel, n)
	seed.Rows = seedRowsForNode(n)
	if pc := legacyDisplayCostOf(n); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}
	return seed
}

// sizeSetOpRelFromNode sizes the SETOP rel. Rows comes from `estimateSetOp`
// (cardinality.go) — goopg's existing UNION/INTERSECT/EXCEPT heuristic, wild
// but SINGLE-SOURCED, so the rel and every legacy reader agree. Width/NCols/
// AvgVarBytes describe the output, the §4.3 duty.
func sizeSetOpRelFromNode(rel *RelOptInfo, setOpNode *SetOp) {
	if rel == nil || setOpNode == nil {
		return
	}
	cols := setOpNode.Output()
	rows := float64(estimateSetOp(setOpNode))
	if rows < 1 {
		rows = 1
	}
	rel.Rows = rows
	rel.Width = nodeTupleWidth(setOpNode)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// setOpStreams reports whether goopg's `setOp` executor runs this node in its
// streaming form. It mirrors `newSetOp`'s own predicate
// (`operators_setop.go:32`: `p.All && p.Op == parser.SetOpUnion`) — the
// sibling-paths rule: the price and the operator must agree about which form
// runs, or the blocking arm is charged for a node that streams.
func setOpStreams(n *SetOp) bool {
	return n.All && n.Op == parser.SetOpUnion
}

// costSetOp prices one `*SetOp` node in the form its executor will run.
//
// STREAMING (UNION ALL) is `cost_append` (costsize.c:2250): the first
// subpath's startup, the sum of the subpaths' totals, and no per-row term of
// its own — an Append does no qual-checking or projection, and neither does
// goopg's streaming arm, which forwards left then right.
//
// BUFFERED (everything else — UNION, INTERSECT [ALL], EXCEPT [ALL]) is
// `create_setop_path`'s hashed arm (pathnode.c:3849): both inputs are read in
// full before anything is emitted, so their TOTALS are this node's startup,
// plus `cpu_operator_cost` per comparison column per input row on both sides
// for the hash lookups; the total adds `cpu_operator_cost` per OUTPUT row
// ("charge only operator cost not cpu_tuple_cost, since SetOp does no
// qual-checking or projection", pathnode.c:3862). Every output column is a
// comparison column here: goopg's `computeBuffered` keys on the whole row.
//
// PG's SETOP_SORTED strategy has no goopg counterpart (one executor form) and
// therefore no candidate; PG's two hash-arm `disabled_nodes` bumps
// (`enable_hashagg` off, and the hash table not fitting `hash_mem`) are also
// omitted — with a single candidate a disabled-node count cannot change a
// selection, and inventing one would only leak into `DisabledNodes` sums
// above. Both are resume points for the day a sorted set-op form exists.
func costSetOp(cp costParams, streaming bool, leftStartup, leftTotal, leftRows,
	rightStartup, rightTotal, rightRows, outputRows float64, numCols int) Cost {
	_ = rightStartup
	if streaming {
		return Cost{Startup: leftStartup, Total: leftTotal + rightTotal}
	}
	cmp := cp.cpuOperatorCost * (leftRows + rightRows) * float64(numCols)
	startup := leftTotal + rightTotal + cmp
	return Cost{Startup: startup, Total: startup + cp.cpuOperatorCost*outputRows}
}

// addSetOpPaths offers the one candidate the executor can run for this node.
func addSetOpPaths(setOpRel *RelOptInfo, lseed, rseed *Path, setOpNode *SetOp, cp costParams) {
	streaming := setOpStreams(setOpNode)
	producer := setOpHashedProducer
	if streaming {
		producer = setOpAppendProducer
	}
	addPath(setOpRel, &Path{
		Kind: PathSetOp, SetOp: setOpNode,
		Rel: setOpRel, Rows: setOpRel.Rows,
		DisabledNodes: lseed.DisabledNodes + rseed.DisabledNodes,
		Cost: costSetOp(cp, streaming,
			lseed.Cost.Startup, lseed.Cost.Total, lseed.Rows,
			rseed.Cost.Startup, rseed.Cost.Total, rseed.Rows,
			setOpRel.Rows, setOpRel.NCols),
		Children: []*Path{lseed, rseed},
	}, producer)
}
