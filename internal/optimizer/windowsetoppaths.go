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
	setOpMixedAppendProducer   = "upper.setop.append.mixed"
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
	// M0140-0006b-2: the upper-rel Gather reader. No-op today (no producer
	// files partial paths on the WINDOW rel), wired so a future one does
	// not repeat 0006b's "producer with no reader" trap.
	generateUpperRelGatherPaths(winRel, cp)
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
	//
	// M0144-0003b-1 widens the accessor by one terminus so a branch that is
	// itself a finished set operation answers with ITS SETOP rel — the link
	// below's, carried out on the node it returned. Without that, the outer
	// link of every chain of three or more UNION ALL branches read nil on
	// the side holding the rest of the chain (a `*SetOp` has two boundary
	// children, so `searchedRelOf` stops at it), `ConsiderParallel` went
	// false, and `addPartialSetOpPath` returned before either arm ran. PG
	// never meets the case: `pull_up_simple_union_all` flattens the whole
	// union into one appendrel. See setopbranchrel.go.
	setOpRel.LeftBranchRel = setOpBranchRelOf(setOpNode.Left)
	setOpRel.RightBranchRel = setOpBranchRelOf(setOpNode.Right)

	// M0140-0006b: seed setOpRel.PartialPathlist from the two branches' own
	// partial paths, when the branches and the op shape allow it. See
	// addPartialSetOpPath's own header for why this cannot move a plan yet.
	// `ps.ParallelStatementOK` doubles as the top-level marker PG's
	// plan_set_operations vs add_paths_to_append_rel distinction needs —
	// see the function's header for the mixed arm's placement rule.
	// M0141-S2b-4b: a distinct UNION's folded input is generate_union_paths'
	// territory at any nesting depth, so it takes the top-level (pure-arm
	// only) placement rule.
	addPartialSetOpPath(setOpRel, setOpNode, cp, ps.ParallelStatementOK || setOpNode.UnionDistinctInput)

	addSetOpPaths(setOpRel, lseed, rseed, setOpNode, cp)
	// M0140-0006b-2: the upper-rel Gather reader — the live site. Reads the
	// PartialPathlist addPartialSetOpPath files above (which also stamps
	// ConsiderParallel, the flag the reader gates on, so this must run
	// after it). The executor whitelist for the resulting Gather already
	// landed (M0140-0006c), so a partial SetOp over bare-scan branches can
	// now be offered to the serial tournament and win on cost.
	generateUpperRelGatherPaths(setOpRel, cp)
	setCheapest(setOpRel)

	best := getCheapestFractionalPath(setOpRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: setOpNode.Pos(), Code: "0A000",
			Message: "could not implement set operation"}
	}
	node, _ := createPlanNode(best)
	// M0144-0003b-1: carry this link's SETOP rel out on the node the link
	// publishes, so the next link up can read it (setopbranchrel.go). Both
	// returnable kinds below carry the tag; anything else stays opaque, as
	// it was.
	node = stampSetOpBranchRel(node, setOpRel)
	switch node.(type) {
	case *SetOp:
		return node, nil
	case *Gather, *GatherMerge:
		// M0140-0006c-3: the winner can be the upper-rel Gather the path
		// model filed over a partial/mixed SetOp (generateUpperRelGatherPaths)
		// — a `Parallel Append` in PG terms, which is exactly a Gather over
		// the set-op node here. Reachable now that the mixed arm can file a
		// partial SetOp whose Gather beats the serial candidate.
		return node, nil
	}
	return nil, &PlanError{Pos: setOpNode.Pos(), Code: "XX000",
		Message: "createSetOpPaths: PathSetOp built no set-op node"}
}

// createUnionDistinctPaths is `generate_union_paths`' `!op->all` arm
// (prepunion.c:676) for a UNION (distinct) whose same-kind children the
// caller has already folded into one UNION ALL chain (`plan_union_children`,
// prepunion.c:1269) — M0141-S2b-4a. distinctNode.Child is that chain, which
// EXPLAIN renders as one n-ary Append.
//
// The candidates are PG's two serial arms over the Append: a hashed
// aggregate and Sort -> Unique, built by addUnionDistinctPaths (PG's hash-first
// order) and elected by
// addPath. They go on a FRESH SETOP rel (PG's fetch_upper_rel(UPPERREL_SETOP,
// relids)), never the statement's shared (DISTINCT, 0) rel, so a UNION and a
// SELECT DISTINCT in one statement cannot pollute each other's election.
// The group estimate is PG's worst case: `dNumGroups = apath->rows`, the
// Append's own input rows ("too conservative, but it's not clear how to get a
// decent estimate", prepunion.c).
//
// Not ported (ledgered): the Gather variants over a partial Append (gpath)
// and the Merge Append arm (try_sorted), the latter being M0141-S2b-4c.
func createUnionDistinctPaths(u *upperRels, distinctNode *Distinct, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if distinctNode == nil || distinctNode.Child == nil {
		return nil, &PlanError{Code: "XX000", Message: "createUnionDistinctPaths: nil union input"}
	}
	if u == nil {
		u = newUpperRels()
	}
	cp := ps.costParams()
	setOpRel := newUpperRelForNode(u, UpperSetOp, tupleFraction)
	seed := seedPathForNode(setOpRel, distinctNode.Child)
	setOpRel.Rows = seed.Rows
	if setOpRel.Rows < 1 {
		setOpRel.Rows = 1
	}
	setOpRel.Width = nodeTupleWidth(distinctNode)
	addUnionDistinctPaths(setOpRel, seed, distinctNode, distinctNode.Child, cp, ps)
	setCheapest(setOpRel)
	best := getCheapestFractionalPath(setOpRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: distinctNode.Pos(), Code: "0A000",
			Message: "could not implement UNION"}
	}
	node, _ := createPlanNode(best)
	switch node.(type) {
	case *Distinct, *DistinctOn:
		return node, nil
	}
	return nil, &PlanError{Pos: distinctNode.Pos(), Code: "XX000",
		Message: "createUnionDistinctPaths: PathDistinct built no distinct node"}
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

// addPartialSetOpPath is M0140-0006b + M0140-0006c-3: the streaming-UNION-ALL
// counterpart of addPartialHashJoinPath (joinpathsparallel.go), giving the
// SETOP rel its own entries in PartialPathlist. It files PG's TWO parallel-
// aware Append arms specialised to goopg's fixed two-child shape:
//
//   - the PURE arm (allpaths.c:1538-1577's `partial_subpaths_valid` case):
//     every child offers a partial path — M0140-0006b.
//   - the MIXED arm (allpaths.c:1588-1627's `pa_subpaths` case): per child,
//     the cheaper of its cheapest partial path and its cheapest parallel-safe
//     TOTAL path; a child whose pick is the non-partial one is claimed WHOLE
//     by one participant (Path.SetOpLeftNonPartial/SetOpRightNonPartial),
//     exactly as PG's executor CASes `pa_finished` on a non-partial subplan.
//     Filed only when ≥1 child picks non-partial — an all-partial pick would
//     duplicate the pure arm — and dead entirely when a child offers neither
//     (`pa_subpaths_valid = false`). The sole corpus witness is TPC-DS Q5's
//     third Parallel Append (design doc m0140-0006c-3).
//
// PLACEMENT (M0140-0006c-3's parity correction): the two arms do not live
// in the same function upstream. `generate_union_paths` (prepunion.c) —
// the path builder for a statement-level set operation — files ONLY the
// pure arm: `partial_paths_valid` there is "every child has a partial
// path", and a Gather over the Append is added iff that holds. The mixed
// `pa_subpaths` arm is `add_paths_to_append_rel`'s alone (allpaths.c:1321),
// reached for APPENDRELS — flattened FROM-clause/CTE UNION ALL subqueries
// and inheritance fan-outs, never for a top-level `a UNION ALL b`. goopg
// does not flatten union-all subqueries (no pull_up_union_all analog), so
// this one function serves both structures — and `topLevel`
// (ps.ParallelStatementOK, set only for the statement's top-level plain
// SELECT) is the marker that separates them: the mixed arm files only in
// nested scopes, where the SetOp stands in for an appendrel PG would have
// built. Filing it at top level emits `Gather > Append` shapes PG's setop
// pipeline cannot produce — measured on TPC-DS Q66, whose top-level
// UNION ALL picked up an all-claimed Parallel Append PG never plans.
// The residual over-fire: a union-all inside a FROM subquery PG would NOT
// flatten (LIMIT/… in the subquery) still gets the arm here — the marker
// cannot see flattenability, only nesting.
//
// The pure arm runs FIRST and its Rows are remembered: when both arms file
// (both branches had partials AND at least one non-partial pick won), the
// mixed path takes the pure path's Rows — PG's `partial_rows` argument to
// `create_append_path` (allpaths.c:1594-1627's call site, consumed at
// pathnode.c:1417-1419).
func addPartialSetOpPath(setOpRel *RelOptInfo, setOpNode *SetOp, cp costParams, topLevel bool) {
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

	// A branch OFFERS its searched rel's partial path as its own partial
	// subpath only when every boundary wrapper between the branch node and
	// the searched emission either is per-worker-safe AND
	// stamp-descendable — *Project, *Filter, *Sort, the same set
	// stampParallelScan/drivingScan descend (parallel.go) — or is dropped
	// by the splice with no row effect — *Gather/*GatherMerge, the serial
	// winner's own parallel wrappers (createplansimple.go). Any other
	// wrapper — *Limit, *Distinct, *Aggregate, *WindowAgg,
	// *OrdinalityWrap, *LockRows, *Memoize, a nested *SetOp — either
	// caps/transforms rows per worker (per-worker LIMIT 5 emits up to 5
	// per participant, not 5 total) or hides the driving scan from the
	// stamp walk, so the searched rel's partial path is not "the branch's
	// partial path". PG reaches the same verdict for free: its
	// partial_pathlist never carries Limit/Agg/... paths, so a wrapped
	// child's partial pick is NULL there too. Such a branch can still be
	// claimed WHOLE by the mixed arm below — a worker runs the wrapped
	// serial subtree verbatim, which is row-exact.
	lChainOK := setOpBranchPartialChainOK(setOpNode.Left)
	rChainOK := setOpBranchPartialChainOK(setOpNode.Right)

	// PURE ARM (allpaths.c:1538-1577). pureRows carries its row estimate
	// into the mixed arm's `partial_rows` override below; -1 marks "the
	// pure arm did not file", PG's undefined partial_rows.
	pureRows := -1.0
	if lChainOK && rChainOK && len(left.PartialPathlist) > 0 && len(right.PartialPathlist) > 0 {
		lp, rp := left.PartialPathlist[0], right.PartialPathlist[0]
		if lp != nil && rp != nil && lp.ParallelWorkers > 0 && rp.ParallelWorkers > 0 &&
			lp.ParallelSafe && rp.ParallelSafe {
			// parallel_workers: Max over the two subpaths' own worker
			// counts (allpaths.c:1544-1550), then at least
			// `pg_leftmost_one_pos32(2)+1 == 2` — PG's
			// `enable_parallel_append` arm (allpaths.c:1560-1566) bumps to
			// at least log2(numChildren)+1; goopg has no such GUC to gate
			// on (matches costSetOp's "single candidate" posture: there is
			// no serial-vs-parallel-aware Append choice to make), so the
			// bump always applies, fixed at 2 because a goopg SetOp always
			// has exactly two children, unlike PG's N-way Append.
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
			if workers > 0 {
				// Startup: "Append will start returning tuples when the
				// child node having lowest startup cost is done setting
				// up" (costsize.c:2350-2352). `first_partial_path == 0`
				// here (no non-partial subpaths), so both children are
				// eligible and the rule is min(left, right), not just the
				// first child's.
				startup := lp.Cost.Startup
				if rp.Cost.Startup < startup {
					startup = rp.Cost.Startup
				}
				// Rows and total cost: `first_partial_path == 0`, so every
				// subpath takes the `else` branch (costsize.c:2372-2381) —
				// rescale each child's per-worker row count from ITS OWN
				// divisor to the Append's chosen worker count, and add
				// each child's total cost undivided.
				divisor := getParallelDivisor(workers, cp.parallelLeaderParticipation)
				lDivisor := getParallelDivisor(lp.ParallelWorkers, cp.parallelLeaderParticipation)
				rDivisor := getParallelDivisor(rp.ParallelWorkers, cp.parallelLeaderParticipation)
				rows := clampRowEst(lp.Rows*(lDivisor/divisor) + rp.Rows*(rDivisor/divisor))
				total := lp.Cost.Total + rp.Cost.Total
				// "Although Append does not do any selection or
				// projection, it's not free; add a small per-tuple
				// overhead" (costsize.c:2400-2403).
				total += cp.cpuTupleCost * appendCPUCostMultiplier * rows
				pureRows = rows
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
		}
	}

	// MIXED ARM (allpaths.c:1408-1453's per-child pick + :1588-1627's
	// `pa_subpaths` arm). Per branch, the cheaper of its cheapest partial
	// path and its cheapest parallel-safe TOTAL path — strictly cheaper
	// wins, ties land the branch in the non-partial list exactly as PG's
	// `<` comparison does.
	//
	// Placement: `add_paths_to_append_rel` only — never generate_union_paths.
	// At the statement's top level (a genuine set operation, PG's
	// SetOperationStmt) the mixed arm does not exist upstream; filing it
	// there emits `Gather > Append` shapes PG cannot produce (TPC-DS Q66).
	// goopg's proxy for "this SetOp stands where PG would have an
	// appendrel" is NOT-top-level — the node sits inside a nested scope
	// (FROM-clause/CTE union-all subquery), the case PG flattens.
	if topLevel {
		return
	}
	lp, lnp := setOpBranchPick(setOpRel, left, setOpNode.Left, lChainOK)
	rp, rnp := setOpBranchPick(setOpRel, right, setOpNode.Right, rChainOK)
	if (lp == nil && lnp == nil) || (rp == nil && rnp == nil) {
		// `pa_subpaths_valid = false`: a branch offering neither a partial
		// path nor a parallel-safe total path kills the whole arm.
		return
	}
	if lnp == nil && rnp == nil {
		// `pa_nonpartial_subpaths != NIL` is the arm's filing condition —
		// an all-partial pick is the pure arm's shape, not this one's.
		return
	}

	// parallel_workers (allpaths.c:1596-1603): the max over the chosen
	// PARTIAL subpaths' own worker counts (a claimed-whole child
	// contributes nothing), floored at `pg_leftmost_one_pos32(2)+1 == 2`
	// — the log2 bump exists precisely for the non-partial children, and
	// it is also what makes an all-claimed two-child Append plan 2
	// workers rather than 0.
	workers := 0
	if lp != nil && lp.ParallelWorkers > workers {
		workers = lp.ParallelWorkers
	}
	if rp != nil && rp.ParallelWorkers > workers {
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

	lc, rc := lp, rp
	if lc == nil {
		lc = lnp
	}
	if rc == nil {
		rc = rnp
	}

	// Startup (costsize.c:2350-2352): min over the first parallel_workers
	// subpaths; with two children and workers >= 2 that is simply
	// min(left, right).
	startup := lc.Cost.Startup
	if rc.Cost.Startup < startup {
		startup = rc.Cost.Startup
	}

	// Rows (costsize.c:2362-2384): a claimed-whole child contributes
	// rows / append_divisor (one participant emits the whole branch, so
	// the Append's divisor amortises it); a partial child is rescaled
	// from its own divisor to the Append's, exactly as the pure arm.
	divisor := getParallelDivisor(workers, cp.parallelLeaderParticipation)
	var lRows, rRows float64
	if lp != nil {
		lRows = lp.Rows * (getParallelDivisor(lp.ParallelWorkers, cp.parallelLeaderParticipation) / divisor)
	} else {
		lRows = lnp.Rows / divisor
	}
	if rp != nil {
		rRows = rp.Rows * (getParallelDivisor(rp.ParallelWorkers, cp.parallelLeaderParticipation) / divisor)
	} else {
		rRows = rnp.Rows / divisor
	}
	rows := clampRowEst(lRows + rRows)
	// `partial_rows` override (pathnode.c:1417-1419): when the pure arm
	// also ran, the mixed path takes ITS row estimate — the Append emits
	// the same multiset either way, and the pure arm's rescale is the
	// estimate PG keeps.
	if pureRows >= 0 {
		rows = pureRows
	}

	// Total (costsize.c:2387-2403): partial children contribute their
	// totals undivided; non-partial children go through
	// `append_nonpartial_cost`'s greedy LPT scheduling simulation —
	// min(workers, n_nonpartial) buckets, each child into the currently
	// lightest bucket, return the heaviest. With at most two children and
	// workers >= 2 the simulation is max(t_left, t_right).
	total := 0.0
	if lp != nil {
		total += lp.Cost.Total
	}
	if rp != nil {
		total += rp.Cost.Total
	}
	var npCosts []float64
	if lnp != nil {
		npCosts = append(npCosts, lnp.Cost.Total)
	}
	if rnp != nil {
		npCosts = append(npCosts, rnp.Cost.Total)
	}
	total += appendNonPartialCost(npCosts, workers)
	total += cp.cpuTupleCost * appendCPUCostMultiplier * rows

	addPartialPath(setOpRel, &Path{
		Kind:                 PathSetOp,
		SetOp:                setOpNode,
		Rel:                  setOpRel,
		Rows:                 rows,
		Cost:                 Cost{Startup: startup, Total: total},
		DisabledNodes:        lc.DisabledNodes + rc.DisabledNodes,
		Children:             []*Path{lc, rc},
		Pathkeys:             nil,
		RequiredOuter:        0,
		ParallelSafe:         parallelSafeWith(setOpRel, lc, rc),
		ParallelWorkers:      workers,
		ParallelAware:        true,
		SetOpLeftNonPartial:  lnp != nil,
		SetOpRightNonPartial: rnp != nil,
	}, setOpMixedAppendProducer)
}

// setOpBranchPartialChainOK reports whether the searched rel's partial path
// can stand in for the branch's own partial subpath: every boundary wrapper
// between the branch node and the searched emission must either run
// per-worker correctly AND admit the stamp walk — *Project, *Filter,
// *Sort, the single-child kinds stampParallelScan/drivingScan descend
// (parallel.go) — or be dropped by the splice with no row effect —
// *Gather/*GatherMerge, the serial winner's own parallel wrappers, which
// spliceBranchEmission removes while continuing the substitution below
// (createplansimple.go). The walk mirrors searchedRelOf's (bounded, stops
// at the first searched root) but reads the narrower set: searchedRelOf
// descends every boundaryWalkChildren kind because it only has to FIND the
// rel, while a partial pick must also RUN under the wrappers — a *Limit
// would cap per worker, a *Distinct or *Aggregate would transform per
// worker, and a *Memoize's typed child cannot even be spliced through —
// none of which a stamped partial subtree can carry.
//
// Returns false when no searched root is reachable through the admissible
// set at all (a wrapped or unsearched branch): the branch then has no
// partial subpath to offer, exactly PG's NULL partial pick for a child
// whose own plan carries Limit/Agg/... — partial_pathlist never holds such
// paths upstream either.
func setOpBranchPartialChainOK(n Node) bool {
	for depth := 0; n != nil && depth < 32; depth++ {
		// M0144-0003b-1: a finished set operation carrying its own SETOP rel
		// is a terminus, exactly like a searched root. What that rel's
		// PartialPathlist holds is a partial SetOp path — a plan whose
		// branches are block-claimed across participants, so the link emits
		// each of its rows exactly once across the whole worker set, which
		// is precisely the contract a partial subpath owes its parent. The
		// carrier check precedes the descent for the reason given on
		// `setOpBranchRelOf`: a *Gather over a partial *SetOp is both a
		// carrier and a boundary wrapper, and the outermost carrier is the
		// one whose rel matches the node the parent actually holds.
		if c, ok := n.(setOpBranchRelNode); ok && c.setOpBranchRel() != nil {
			return true
		}
		if s, ok := n.(searchRootNode); ok && s.isFromJoinSearch() {
			return true
		}
		switch x := n.(type) {
		case *Project:
			n = x.Child
		case *Filter:
			n = x.Child
		case *Sort:
			n = x.Child
		case *Gather:
			n = x.Child
		case *GatherMerge:
			n = x.Child
		default:
			return false
		}
	}
	return false
}

// setOpBranchPick is the per-child choice PG makes at allpaths.c:1412-1453:
// `nppath` (the child's cheapest parallel-safe TOTAL path) against the
// child's cheapest partial path — the partial wins iff `nppath == NULL ||
// partial->total_cost < nppath->total_cost`, STRICTLY (ties land in the
// non-partial list). Returns (partial pick, non-partial pick), exactly one
// non-nil, or both nil when the branch offers neither — the caller's
// `pa_subpaths_valid` kill.
//
// The partial candidate is the searched rel's PartialPathlist[0] (cheapest
// by addToPartialPathlist's ordering), admissible only when chainOK — see
// setOpBranchPartialChainOK. The non-partial candidate is goopg's nppath:
// a fresh PathPrebuilt seed over the branch's own finished plan in its
// parallel-safe serial form — the node itself, or `StripGather(node)` when
// the branch's serial winner carried a Gather. The strip is not optional:
// PG's `get_cheapest_parallel_safe_total_inner` scans the child's whole
// pathlist for the cheapest parallel_safe member, and create_gather_path
// stamps `parallel_safe = false` — a Gather-topped plan is never nppath;
// the child's serial alternative is (pathnode.c:2192, allpaths.c:1417).
// The seed is row-exact by construction (wrappers included, where a
// searched-rel pathlist member would emit the rel's internal schema),
// priced with the wrappers' cost like PG's nppath total_cost, and built
// back to a whole-branch node by createPlanNode so no splice applies
// (createSetOpPlan skips PathPrebuilt children for exactly this reason).
// It is parallel-safe iff the whole-plan check says the worker may run it
// (statementIsParallelSafe — refuses LockRows/unsafe relations/DML) — PG's
// `parallel_safe` on the child's plan.
func setOpBranchPick(setOpRel, branch *RelOptInfo, branchNode Node, chainOK bool) (partial, nonPartial *Path) {
	var bp *Path
	if chainOK && len(branch.PartialPathlist) > 0 {
		if c := branch.PartialPathlist[0]; c != nil && c.ParallelWorkers > 0 && c.ParallelSafe {
			bp = c
		}
	}
	var bnp *Path
	if branchNode != nil {
		if serial := StripGather(branchNode); serial != nil && statementIsParallelSafe(serial) {
			if seed := seedPathForNode(setOpRel, serial); seed.ParallelSafe {
				bnp = seed
			}
		}
	}
	if bp != nil && (bnp == nil || bp.Cost.Total < bnp.Cost.Total) {
		return bp, nil
	}
	return nil, bnp
}

// appendNonPartialCost is `append_nonpartial_cost` (costsize.c:2168-2243)
// specialised to goopg's two-child shape: a greedy LPT scheduling simulation
// over min(workers, n) buckets — each non-partial child (PG walks them in
// descending cost, irrelevant at n <= 2) is assigned to the currently
// lightest bucket, and the answer is the heaviest bucket, i.e. the makespan
// of running the claimed-whole branches concurrently. With one child it is
// its total cost; with two children and workers >= 2 it is max(left, right);
// with fewer workers than children it degenerates to a single bucket's sum.
func appendNonPartialCost(npCosts []float64, workers int) float64 {
	n := len(npCosts)
	if n == 0 {
		return 0
	}
	buckets := workers
	if n < buckets {
		buckets = n
	}
	if buckets <= 1 {
		sum := 0.0
		for _, c := range npCosts {
			sum += c
		}
		return sum
	}
	// buckets >= 2 and n <= 2: each child gets its own bucket.
	max := npCosts[0]
	for _, c := range npCosts[1:] {
		if c > max {
			max = c
		}
	}
	return max
}
