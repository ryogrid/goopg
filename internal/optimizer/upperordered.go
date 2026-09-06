package optimizer

// The ORDERED upper rel — planner-refactor take3 C-12 (P4-03).
//
// `create_ordered_paths` (planner.c:5308) is the tail of PG's upper pipeline:
// for every path of the input rel it either takes the path as-is when its
// pathkeys already deliver the ORDER BY, or stacks a `create_sort_path` over
// it, and offers each to `add_path` on the `(ORDERED, NULL)` upper rel. goopg
// did the same job as an unconditional Node rewrite — `orderSort = &Sort{…}`
// at the two ORDER BY sites of `planSelectWithSettings` — and that rewrite had
// one consequence the design (docs/design/planner-p4-upper-rels/DESIGN.md
// §0 F1) found: `costSortRun`'s only caller was the merge-join input sort, so
// every top-level ORDER BY Sort in the tree was priced by
// `DeriveLegacyDisplayCost`, the in-memory comparison term alone, and TPC-H
// Q18's `Sort (rows=1565307)` — the largest sort in the suite — contributed
// nothing to any comparison and no spill charge to its own EXPLAIN line.
//
// This file is the producer. It is option (b) of DESIGN §4: the finished
// child Node is wrapped in a `PathPrebuilt` over the ORDERED rel (the C0
// bridge `newPrebuiltPath` exists for exactly this), a `PathSort` is stacked
// on it through the SAME `sortPathFor` the merge side uses, both are offered
// to `addPath`, `setCheapest` runs, and `createPlanNode` on the winner emits
// the `*Sort` — through `createSortPlan`, whose key translation is a no-op
// for an upper rel because the rel has no `baseLeaf` (§4.1: the keys were
// resolved against the child's output schema, which is the coordinate space
// the Sort runs in). Same node, same position, same keys as the rewrite; the
// difference is that the node now carries `cost_sort`'s number, external
// merge arm included, and that the path went through the dominance
// tournament once.
//
// What this cut deliberately does NOT do (DESIGN §5.5): give the rel a second
// candidate. Nothing above the search root carries `Pathkeys` — the seam
// publishes a Node and drops them — so the input path never delivers the
// keys and the `upper.ordered.input` producer below never fires today. The
// arm is kept and unit-tested rather than omitted because it is the line
// C-12a (widening `addOrderedIndexPaths`' useful-column set) turns live, and
// "verify both candidates were generated" is then a grep on the two producer
// strings under GOOPG_PGSHAPED_DP_TRACE=1.

// Producer strings for the DPPATH trace (pathtrace.go). With `Relids = 0` the
// lines read `producer=upper.ordered.* relids=-`, which is how an upper-rel
// path is told apart from a search path with no format change.
const (
	upperOrderedInputProducer = "upper.ordered.input"
	upperOrderedSortProducer  = "upper.ordered.sort"
)

// createOrderedPaths is `create_ordered_paths` for the one input goopg has
// above the seam: the finished `input` Node. It returns the Node that delivers
// `keys` — a fresh `*Sort` over `input` at `pos`, or `input` itself if it
// already carried the order (not reachable today, see the file comment).
//
// `tupleFraction` is `root->tuple_fraction` and reaches the rel through
// `fetchUpperRel` (`consider_startup`) and the final `getCheapestFractionalPath`
// selection, exactly where PG reads it; with one path in the list both are
// identity today.
func createOrderedPaths(u *upperRels, input Node, keys []SortKey, pos int, cp costParams, tupleFraction float64) Node {
	if input == nil || len(keys) == 0 {
		return input
	}
	if u == nil {
		// A caller with no registry still gets its Sort: dropping the
		// ORDER BY would be a wrong answer with green row counts, and the
		// only thing a throwaway registry loses is the (kind, relids)
		// identity a later upper rel would have shared.
		u = newUpperRels()
	}
	ordered := fetchUpperRel(u, UpperOrdered, 0, tupleFraction)
	// The load-bearing step DESIGN §4.3 names: a fresh rel has NCols == 0,
	// which `costSortRun` reads as "width unknown, charge no I/O".
	sizeUpperRelFromNode(ordered, input)

	// The input path. `newPrebuiltPath` leaves the cost zero (the C0 bridge
	// never needed one); here the cost is the child's own — the search's
	// stamp where the child came out of the search, the legacy display
	// estimate where it did not — because `cost_sort` adds the sort on top
	// of `input_total_cost` and the Sort's EXPLAIN line must be monotone
	// over its child's. The stamp `createPlanNode` re-applies to the child
	// on the way back out is therefore value-identical to what EXPLAIN
	// already printed for it: nothing below the Sort moves.
	seed := newPrebuiltPath(ordered, input)
	pc := legacyDisplayCostOf(input)
	// The seed's Rows already match: newPrebuiltPath carried rel.Rows (sized
	// from this same input above) into it, and the sort prices sub.Rows —
	// so the two agree by construction, not by coincidence.
	seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}

	addOrderedPaths(ordered, seed, pathkeysForSortKeys(keys), cp)
	setCheapest(ordered)

	best := getCheapestFractionalPath(ordered, tupleFraction)
	node, _ := createPlanNode(best)
	if srt, ok := node.(*Sort); ok && best.Kind == PathSort {
		// `createSortPlan` positions the node at its child; the rewrite this
		// replaces positioned it at the statement, which is where an error
		// raised through the Sort should point.
		srt.pos = pos
	}
	return node
}

// addOrderedPaths is the per-input body of create_ordered_paths
// (planner.c:5333-5352): a path whose pathkeys already contain the sort
// pathkeys is offered as-is; every other path gets a `create_sort_path` over
// it. One input, so exactly one of the two producers fires per call. Split
// from `createOrderedPaths` so the input arm — unreachable from a Node today —
// is driven by a test with a hand-ordered seed rather than left untested.
func addOrderedPaths(ordered *RelOptInfo, input *Path, sortPathkeys []PathKey, cp costParams) {
	if pathkeysContainedIn(input.Pathkeys, sortPathkeys) {
		addPath(ordered, input, upperOrderedInputProducer)
		return
	}
	addPath(ordered, sortPathFor(input, sortPathkeys, cp), upperOrderedSortProducer)
}
