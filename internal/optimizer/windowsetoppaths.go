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
	windowProducer             = "upper.window.sorted"
	setOpAppendProducer        = "upper.setop.append"
	setOpHashedProducer        = "upper.setop.hashed"
	setOpPartialAppendProducer = "upper.setop.append.partial"
)

// appendCPUCostMultiplier is APPEND_CPU_COST_MULTIPLIER (costsize.c:120):
// "Although Append does not do any selection or projection, it's not free."
const appendCPUCostMultiplier = 0.5

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
//
// `chainKeep`/`relKeep` are M0141-S2a-fix1-sweep-b's narrow cost-input
// keep-sets (window_sort_narrow.go), derived by the caller before this
// runs — `chainKeep` aligned index-for-index with `windows`, `relKeep`
// positions into the top window's own Output(). Either may be nil (decline:
// today's full-width sizing).
func createWindowPaths(u *upperRels, windows []*WindowAgg, input Node, ps PlannerSettings, tupleFraction float64, chainKeep [][]int, relKeep []int) (Node, error) {
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
	// M0141-S2a-fix1-sweep-b: refine the rel's own NCols/AvgVarBytes to the
	// caller's narrow keep-set, when it derived one. A nil relKeep leaves
	// the full-width sizing above untouched.
	narrowWindowRelWidth(winRel, top, relKeep)

	seed := seedPathForNode(winRel, input)
	addWindowPaths(winRel, seed, windows, input, cp, chainKeep)
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
//
// M0141-S2b-3a: `presortedCount` is the number of LEADING `windowSortKeys`
// columns the input already delivers in order (0 when nothing is known about
// the input's ordering, which is every caller today — `addWindowPaths` still
// sees one collapsed input Node, not a Pathlist to check; wiring a real value
// is S2b-3b). Two credits fall out of it, mirroring
// `addIncrementalSortPaths`'s own full-match/partial-match split
// (incrementalsortpaths.go):
//
//   - a FULL match (`presortedCount >= numPartCols+numOrderCols`, and there is
//     at least one sort key) charges no sort at all — `createWindowPlan`
//     (createplansimple.go) stacks no Sort node in this case
//     (`childDeliversSortKeys`), so there is nothing here to price;
//   - a PARTIAL match reuses `costIncrementalSort`'s per-group formula, called
//     with a ZERO input `Cost` rather than this input's real one: unlike
//     `addIncrementalSortPaths`'s ORDERED-rel caller, where the returned Cost
//     IS the candidate's whole price, `costWindow` still needs to add the
//     input's real total on top of the sort afterward for the BLOCKING reason
//     above, so the call here must return only the sort's OWN marginal
//     work — exactly the role `costSortRunWithWidth` plays in the no-credit
//     branch below, and exactly why passing the real input cost through
//     `costIncrementalSort` here would double-count it.
func costWindow(cp costParams, inputTotal, inputRows float64,
	numPartCols, numOrderCols, numFuncs, inNcols int, inAvgVarBytes float64, inWidth int,
	presortedCount int, presortedGroups float64) Cost {
	tuples := inputRows
	if tuples < 0 {
		tuples = 0
	}
	var sortRun Cost
	switch {
	case presortedCount <= 0:
		sortRun = costSortRunWithWidth(cp, tuples, inNcols, inAvgVarBytes, -1, inWidth, "window")
	case numPartCols+numOrderCols > 0 && presortedCount >= numPartCols+numOrderCols:
		sortRun = Cost{}
	default:
		sortRun = costIncrementalSort(cp, Cost{}, tuples, presortedGroups, inNcols, inAvgVarBytes, -1, inWidth)
	}
	// The sort consumes the input in full, so the input's TOTAL is the
	// blocking node's startup floor; the sort term above prices only the
	// sort's own work, split the same way it splits it for a `PathSort`.
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
// — see the file header's fact 1. `costWindow`'s presorted credit
// (M0141-S2b-3a) is always called with `presortedCount=0` here: `input` is
// still the single collapsed Node `createWindowPaths` receives, with no
// Pathlist to check for a shared ordering prefix — wiring that check is
// S2b-3b, filed and gated on this landing.
//
// `chainKeep`, when non-nil, is M0141-S2a-fix1-sweep-b's per-level narrow
// cost-input keep-set (window_sort_narrow.go), index-aligned with `windows`:
// `chainKeep[i]` holds ascending positions into that level's `belowNode.Output()`
// to price `costWindow`'s `inNcols`/`inAvgVarBytes` from, in place of the
// full row. A nil entry (or a nil `chainKeep` altogether) leaves that level's
// full-width sizing untouched — the always-safe decline.
func addWindowPaths(winRel *RelOptInfo, seed *Path, windows []*WindowAgg, input Node, cp costParams, chainKeep [][]int) {
	if len(windows) == 0 {
		// No spec group, no candidate. `createWindowPaths` refuses this case
		// before calling here; the guard keeps a direct caller (a test) from
		// reaching `addPath` with a nil path.
		return
	}
	below := seed
	belowNode := input
	var top *Path
	for i, w := range windows {
		cols := belowNode.Output()
		if i < len(chainKeep) {
			if narrowed := narrowedWindowCols(cols, chainKeep[i]); narrowed != nil {
				cols = narrowed
			}
		}
		p := &Path{
			Kind: PathWindow, Window: w,
			Rel: winRel, Rows: winRel.Rows,
			DisabledNodes: below.DisabledNodes,
			Cost: costWindow(cp, below.Cost.Total, below.Rows,
				len(w.PartitionBy), len(w.OrderBy), len(w.Funcs),
				len(cols), nodeAvgVarBytes(cols), nodeTupleWidth(belowNode),
				0, 0),
			Children: []*Path{below},
		}
		below = p
		belowNode = w
		top = p
	}
	addPath(winRel, top, windowProducer)
}

// narrowedWindowCols returns the columns of cols at the positions named by
// keep, or nil when keep is empty (the caller's "leave cols alone" signal).
// Positions are trusted (produced by deriveWindowChainNarrowKeeps against
// this exact column slice's coordinate space) but bounds-checked defensively
// the same way narrowOrderedRelWidths' equivalent loop does.
func narrowedWindowCols(cols []SchemaColumn, keep []int) []SchemaColumn {
	if len(keep) == 0 {
		return nil
	}
	narrowed := make([]SchemaColumn, 0, len(keep))
	for _, i := range keep {
		if i >= 0 && i < len(cols) {
			narrowed = append(narrowed, cols[i])
		}
	}
	if len(narrowed) == 0 {
		return nil
	}
	return narrowed
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

	// M0140-0006a: thread each branch's own searched RelOptInfo onto the
	// SETOP rel (see RelOptInfo.LeftBranchRel/RightBranchRel's doc comment)
	// instead of letting it die inside the branch's own nested
	// planSelectWithSettings call — the same carry `searchedRelOf` already
	// does for the ORDERED rel's single input (upperordered.go), applied
	// here to the SetOp's two inputs. Plumbing only: nothing below reads
	// these fields yet, so addSetOpPaths still offers the one candidate it
	// always has and no plan can change.
	setOpRel.LeftBranchRel = searchedRelOf(setOpNode.Left)
	setOpRel.RightBranchRel = searchedRelOf(setOpNode.Right)

	// M0140-0006b: seed setOpRel.PartialPathlist from the two branches' own
	// partial paths, when the branches and the op shape allow it. See
	// addPartialSetOpPath's own header for why this cannot move a plan yet.
	addPartialSetOpPath(setOpRel, setOpNode, cp)

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


// addPartialSetOpPath is M0140-0006b: the streaming-UNION-ALL counterpart of
// addPartialHashJoinPath (joinpathsparallel.go), giving the SETOP rel its own
// entry in PartialPathlist when both branches offer one. PG's cost_append
// (costsize.c:2250-2403) parallel-aware arm, specialised to goopg's fixed
// two-child shape (`Left`/`Right`) and restricted to the case where BOTH
// children are already partial — PG's own "mix of partial and non-partial
// subpaths" arm (allpaths.c:1592-1622) needs `append_nonpartial_cost`'s
// worker-rotation arithmetic over an arbitrary-length subpath list, out of
// this task's bound; a branch with no partial path at all simply gets no
// partial SetOp path (a resume point, not a silently dropped case — see the
// deferral ledger).
//
// NOT WIRED TO ANYTHING THAT CAN SELECT IT, and that is by construction, not
// by an added flag. `generateUsefulGatherPaths` — the only reader of
// PartialPathlist (gatherpaths.go's own file header) — is never called for
// ANY upper rel (WINDOW/ORDERED/GROUP_AGG/SETOP alike): its three call sites
// (gatherpaths.go's addBaseRelGatherPaths, joinsearchlevel.go, geqo.go) all
// walk a *searchCtx's own joinrels, and createSetOpPaths (this file) runs
// from the SetOp fold in planner.go, entirely outside any *searchCtx — there
// is no `s` to call it with. This answers the open question M0140-0006's
// decomposition doc left for this task ("verify generateUsefulGatherPaths
// reads it for free... unverified"): the answer is NO, and the gap is
// upper-rel-wide, not SetOp-specific — filed as M0140-0006b-2
// (.ralph/fix_plan.md). Until that lands AND M0140-0006c's executor
// claim-set fix lands, a partial SetOp path cannot be chosen by any gate:
// the field this function writes has no reader.
func addPartialSetOpPath(setOpRel *RelOptInfo, setOpNode *SetOp, cp costParams) {
	if setOpRel == nil || setOpNode == nil || !setOpStreams(setOpNode) {
		// Only Op=UNION ALL streams (setOpStreams); goopg's buffered/hashed
		// arm drains both inputs fully before emitting anything
		// (operators_setop.go's computeBuffered) and has no partial-safe
		// executor shape — PG's SetOp HASHED strategy is not an Append
		// either, so cost_append never applies to it upstream.
		return
	}
	if gatherPathsMode == gatherPathsOff {
		// The only reader is (eventually) generateUsefulGatherPaths, gated
		// by the same mode — producing under `off` buys nothing, same
		// reasoning as addPartialHashJoinPath's identical guard.
		return
	}
	left, right := setOpRel.LeftBranchRel, setOpRel.RightBranchRel
	// C-19a's join-rel rule (build_join_rel, relnode.c:842), applied to the
	// SetOp's two inputs instead of a join's two sides: both must consider
	// parallel. nil (branch never reached by the search) reads as false —
	// short-circuits before either field is dereferenced.
	setOpRel.ConsiderParallel = left != nil && right != nil &&
		left.ConsiderParallel && right.ConsiderParallel
	if !setOpRel.ConsiderParallel {
		return
	}
	if len(left.PartialPathlist) == 0 || len(right.PartialPathlist) == 0 {
		// The pure-partial arm only (allpaths.c:1538-1577's
		// `partial_subpaths_valid` case). A branch with no partial path
		// needs the mixed arm this function does not build.
		return
	}
	lp, rp := left.PartialPathlist[0], right.PartialPathlist[0]
	if lp == nil || rp == nil || lp.ParallelWorkers <= 0 || rp.ParallelWorkers <= 0 ||
		!lp.ParallelSafe || !rp.ParallelSafe {
		return
	}

	// parallel_workers: Max over the two subpaths' own worker counts
	// (allpaths.c:1544-1550), then at least `pg_leftmost_one_pos32(2)+1 ==
	// 2` — PG's `enable_parallel_append` arm (allpaths.c:1560-1566) bumps
	// to at least log2(numChildren)+1; goopg has no such GUC to gate on
	// (matches costSetOp's "single candidate" posture: there is no
	// serial-vs-parallel-aware Append choice to make), so the bump always
	// applies, fixed at 2 because a goopg SetOp always has exactly two
	// children, unlike PG's N-way Append.
	workers := lp.ParallelWorkers
	if rp.ParallelWorkers > workers {
		workers = rp.ParallelWorkers
	}
	if workers < 2 {
		workers = 2
	}
	if workers > cp.maxParallelWorkersPerGather {
		workers = cp.maxParallelWorkersPerGather
	}
	if workers <= 0 {
		return
	}

	// Startup: "Append will start returning tuples when the child node
	// having lowest startup cost is done setting up" (costsize.c:2350-2352).
	// `first_partial_path == 0` here (no non-partial subpaths), so both
	// children are eligible and the rule is min(left, right), not just the
	// first child's.
	startup := lp.Cost.Startup
	if rp.Cost.Startup < startup {
		startup = rp.Cost.Startup
	}

	// Rows and total cost: `first_partial_path == 0`, so every subpath
	// takes the `else` branch (costsize.c:2372-2381) — rescale each
	// child's per-worker row count from ITS OWN divisor to the Append's
	// chosen worker count, and add each child's total cost undivided.
	divisor := getParallelDivisor(workers, cp.parallelLeaderParticipation)
	lDivisor := getParallelDivisor(lp.ParallelWorkers, cp.parallelLeaderParticipation)
	rDivisor := getParallelDivisor(rp.ParallelWorkers, cp.parallelLeaderParticipation)
	rows := clampRowEst(lp.Rows*(lDivisor/divisor) + rp.Rows*(rDivisor/divisor))
	total := lp.Cost.Total + rp.Cost.Total
	// "Although Append does not do any selection or projection, it's not
	// free; add a small per-tuple overhead" (costsize.c:2400-2403).
	total += cp.cpuTupleCost * appendCPUCostMultiplier * rows

	addPartialPath(setOpRel, &Path{
		Kind:            PathSetOp,
		SetOp:           setOpNode,
		Rel:             setOpRel,
		Rows:            rows,
		Cost:            Cost{Startup: startup, Total: total},
		DisabledNodes:   lp.DisabledNodes + rp.DisabledNodes,
		Children:        []*Path{lp, rp},
		Pathkeys:        nil,
		RequiredOuter:   0,
		ParallelSafe:    parallelSafeWith(setOpRel, lp, rp),
		ParallelWorkers: workers,
		ParallelAware:   true,
	}, setOpPartialAppendProducer)
}
