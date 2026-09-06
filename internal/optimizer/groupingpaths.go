package optimizer

// The GROUP_AGG upper rel — planner-refactor take3 C-15 (P4-06).
//
// `create_grouping_paths` (planner.c:3780) is the last of the Phase-4 upper
// producers after C-12's ORDERED: for the finished aggregate child it offers
// hashed, sorted (and plain) `PathAgg` candidates on the `(GROUP_AGG, NULL)`
// upper rel, prices each with `cost_agg`, and lets `add_path` select. It
// retires the three aggregate rules (`applyIndexOrderedGroupingRule`,
// `applyPresortedAggregateRule`, `applyEnableHashAggRule`), which reproduced
// cost-model outcomes without a cost model: GUC-gated, order-dependent node
// mutation with no price comparison anywhere.
//
// Design: docs/design/planner-p4-grouping-paths/DESIGN.md. The shape is
// option (b) as C-12: the finished child is wrapped in a `PathPrebuilt`
// seed over the GROUP_AGG rel (input rows/cost, NOT the rel's grouped rows),
// candidates stack above it, `setCheapest` runs, and `createPlanNode` on the
// winner emits the `*Aggregate` through the new `PathAgg` arm
// (`createAggPlan`, createplansimple.go).

import (
	"github.com/goopg/goopg/internal/catalog"
)

// groupingProducer strings for the DPPATH trace (pathtrace.go). With
// `Relids = 0` the lines read `producer=upper.groupagg.* relids=-`, the
// same convention C-12 established.
const (
	groupAggHashedProducer    = "upper.groupagg.hashed"
	groupAggSortedProducer    = "upper.groupagg.sort"
	groupAggPlainProducer     = "upper.groupagg.plain"
	groupAggSortedIdxProducer = "upper.groupagg.sortedidx"
)

// createGroupingPaths is `create_grouping_paths` for the one aggregate
// goopg plans above the seam: the finished `aggNode` (spec built by
// `buildAggregateStage`, strategy at default). It returns the winning
// `*Aggregate` — same spec, winning strategy, priced input — or a
// `PlanError` when no candidate exists (PG's "could not implement GROUP
// BY", planner.c:4144; never a nil node).
//
// `tupleFraction` travels the C-12 door (`fetchUpperRel` +
// `getCheapestFractionalPath`); with one candidate per shape both are
// identity today. `cat` serves the index-ordered input variant; nil cat
// simply never matches one.
func createGroupingPaths(u *upperRels, aggNode *Aggregate, cat catalog.Catalog, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if aggNode == nil {
		return nil, &PlanError{Code: "XX000", Message: "createGroupingPaths: nil aggregate node"}
	}
	if u == nil {
		// As C-12: a caller with no registry still aggregates. The only
		// thing a throwaway registry loses is the (kind, relids) identity
		// a later upper rel would have shared.
		u = newUpperRels()
	}
	// Non-simple modes (parallel Partial/Final) are not shaped here at
	// all — the old rules skipped them too, and the only production caller
	// (buildAggregateStage's site) builds Simple. Pass through untouched
	// rather than price a node this producer does not own.
	if aggNode.Mode != AggModeSimple {
		return aggNode, nil
	}
	cp := ps.costParams()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, tupleFraction)
	sizeGroupingRelFromAgg(grouped, aggNode)

	child := aggNode.Child
	inputRows := float64(EstimateRows(child))
	if inputRows < 0 {
		inputRows = 0
	}
	// The input seed: the finished CHILD (not the aggregate — the rel's
	// rows are grouped rows, the input paths price input rows). Rows and
	// cost are the child's own, by the C-12 monotonicity argument.
	seed := newPrebuiltPath(grouped, child)
	seed.Rows = inputRows
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}

	addGroupingPaths(grouped, seed, aggNode, child, cat, cp, ps)
	setCheapest(grouped)

	best := getCheapestFractionalPath(grouped, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: aggNode.Pos(), Code: "0A000",
			Message: "could not implement GROUP BY"}
	}
	node, _ := createPlanNode(best)
	built, ok := node.(*Aggregate)
	if !ok || built == nil {
		return nil, &PlanError{Pos: aggNode.Pos(), Code: "XX000",
			Message: "createGroupingPaths: PathAgg built no node"}
	}
	// Copy back onto the PASSED node, not a fresh pointer: the rules this
	// replaces mutated in place, so `node`, the HAVING filter above it, and
	// `agg.node` all alias this pointer — a fresh node would silently drop
	// out of the tree (the HAVING filter would keep filtering the stale
	// child). The copy carries the winner's full spec (Child, Strategy,
	// and — for the index variant — the narrowed GroupExprs/GroupKeyOrder);
	// everything else is the same spec the arm copied. `*Aggregate` carries
	// no PlanCost (no planCostSetter — EXPLAIN recomputes legacy), so the
	// copy loses no stamp; the B-01c keep is recomputed afterwards by the
	// caller, ordered after any index-narrowing remap as today.
	*aggNode = *built
	return aggNode, nil
}

// sizeGroupingRelFromAgg is the §3.4 duty: a fresh GROUP_AGG rel prices a
// spilling hash as an in-memory one unless its Rows/Width/NCols say
// otherwise. Rows come from the PG-faithful `estimateNumGroups` (never
// below 1 — it divides by zero upstream otherwise); Width/NCols/AvgVarBytes
// describe the aggregate OUTPUT, exactly as `sizeUpperRelFromNode` does for
// sorts. (The spill arm reads INPUT width from the seed's child — §3.3 —
// never these fields.)
func sizeGroupingRelFromAgg(rel *RelOptInfo, aggNode *Aggregate) {
	if rel == nil || aggNode == nil {
		return
	}
	cols := aggNode.Output()
	rel.Rows = float64(estimateNumGroups(aggNode.GroupExprs, aggNode.Child, EstimateRows(aggNode.Child)))
	if rel.Rows < 1 {
		rel.Rows = 1
	}
	rel.Width = nodeTupleWidth(aggNode)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// groupingHashable is the HASHED arm's gate — PG's `numOrderedAggs == 0`
// (`create_grouping_paths`): with usable presorted keys the executor runs
// sorted, and hashing would store every input value per group, so the
// hashed arm is declined exactly when the presorted-keys variant exists.
// Without usable keys the hashed executor RUNS ordered aggs (every grouped
// query today defaults to hashed unless a rule claimed it, suites pass),
// so the gate is not per-type: it is the presorted outcome. Grouping sets
// always hash (one hash table per set; the presorted selection bails on
// them, so `presorted` is never true for them).
// `enable_hashagg = off` does not delete the arm (B-17a preference —
// `DisabledNodes`, never skip), it only loses the comparison.
func groupingHashable(aggNode *Aggregate, presorted bool) bool {
	if presorted {
		return false
	}
	return len(aggNode.GroupExprs) > 0
}

// groupingHasSpecialAgg reports whether any aggregate needs ordered input:
// internal ORDER BY, DISTINCT, or an ordered-set (WITHIN GROUP) clause.
// A sorted candidate over plain group keys cannot serve these — only the
// presorted-keys variant can — so this decides whether the group-keys Sort
// is a valid sorted input at all. (WithinGroup counts here although the
// presorted selection skips it: with no usable keys from other aggregates,
// group-keys order is not a valid ordered-set input either.)
func groupingHasSpecialAgg(aggNode *Aggregate) bool {
	for i := range aggNode.Aggs {
		a := &aggNode.Aggs[i]
		if len(a.OrderBy) > 0 || a.Distinct || a.WithinGroup {
			return true
		}
	}
	return false
}

// presortedAggKeysOrAbsent is the surviving half of
// `applyPresortedAggregateRule`: the greedy covering key selection over
// internal ORDER BY / DISTINCT aggregates (FILTER-safety, volatility,
// WithinGroup skip, dropped-constant delimiters — all verbatim below),
// returning keys-or-absent instead of mutating. The rule's doc comment
// (groupagg_presorted.go, deleted by this cut) names the PG provenance
// per block; the blocks move unchanged.
func presortedAggKeysOrAbsent(aggNode *Aggregate, ps PlannerSettings) ([]SortKey, bool) {
	// take2 P2-02c: per-statement enable_presorted_aggregate. Off means no
	// presorted variant at all — the group-keys Sort below is not a valid
	// ordered-agg input, so callers must not fall back to it for special
	// aggregates (see addGroupingPaths).
	if !ps.EnablePresortedAggregate {
		return nil, false
	}
	// Grouping sets never presort (the rule bailed on them; the executor
	// runs one hash table per set and no sorted groupingsets execution
	// exists).
	if aggNode.GroupingSets != nil {
		return nil, false
	}
	type aggCandidate struct {
		pathkeys []PathKey
	}
	var candidates []aggCandidate
	for i := range aggNode.Aggs {
		a := &aggNode.Aggs[i]
		if a.WithinGroup {
			continue
		}
		if len(a.OrderBy) == 0 && !a.Distinct {
			continue
		}
		if a.Filter != nil && !aggArgsAllVarConst(a) {
			continue
		}
		pathkeys := makeCandidatePathkeys(aggregateSortlist(a))
		if len(pathkeys) == 0 {
			continue
		}
		if pathkeysContainVolatile(pathkeys) {
			continue
		}
		candidates = append(candidates, aggCandidate{pathkeys: pathkeys})
	}
	if len(candidates) == 0 {
		return nil, false
	}

	var grouppathkeys []PathKey
	for _, g := range aggNode.GroupExprs {
		grouppathkeys = append(grouppathkeys, PathKey{Expr: g, SortAsc: true, NullsFirst: false})
	}

	bestCount := 0
	var bestpathkeys []PathKey
	unprocessed := make([]int, len(candidates))
	for i := range candidates {
		unprocessed[i] = i
	}
	for len(unprocessed) > bestCount {
		var currpathkeys []PathKey
		covered := make([]bool, len(candidates))
		for _, ui := range unprocessed {
			pk := appendPathKeys(append([]PathKey(nil), grouppathkeys...), candidates[ui].pathkeys)
			if currpathkeys == nil {
				currpathkeys = pk
				covered[ui] = true
				continue
			}
			switch comparePathkeysDim(currpathkeys, pk) {
			case dimBetter2:
				currpathkeys = pk
				fallthrough
			case dimBetter1, dimEqual:
				covered[ui] = true
			case dimIncomparable:
			}
		}
		next := unprocessed[:0]
		for _, ui := range unprocessed {
			if !covered[ui] {
				next = append(next, ui)
			}
		}
		unprocessed = next
		n := 0
		for _, c := range covered {
			if c {
				n++
			}
		}
		if n > bestCount {
			bestCount = n
			bestpathkeys = currpathkeys
		}
	}
	if bestpathkeys == nil {
		return nil, false
	}

	finalSortKeys := make([]SortKey, 0, len(bestpathkeys))
	for _, pk := range bestpathkeys {
		finalSortKeys = append(finalSortKeys, SortKey{Expr: pk.Expr, Desc: !pk.SortAsc, NullsFirst: pk.NullsFirst})
	}
	return finalSortKeys, true
}

// groupKeysSortKeys is one ascending SortKey per group expression — the
// input order a plain sorted aggregate consumes.
func groupKeysSortKeys(aggNode *Aggregate) []SortKey {
	keys := make([]SortKey, 0, len(aggNode.GroupExprs))
	for _, g := range aggNode.GroupExprs {
		keys = append(keys, SortKey{Expr: g, Desc: false, NullsFirst: false})
	}
	return keys
}

// aggInputWidth reads the seed child's width: the width of the rows being
// hashed (input side), never the GROUP_AGG rel's output sizing. The child
// is a finished Node, so its own output schema is the honest source;
// AvgVarBytes follows the same `nodeAvgVarBytes` rule base rels take from
// ANALYZE. (Kept for the spill arm's resume — costAgg takes both as future
// inputs; see the NO-spill note there.)
func aggInputWidth(child Node) (ncols int, avgVarBytes float64) {
	if child == nil {
		return 0, 0
	}
	cols := child.Output()
	return len(cols), nodeAvgVarBytes(cols)
}

// addGroupingPaths is the per-input body of `add_paths_to_grouping_rel`
// (planner.c:7114) for goopg's one input: at most one hashed, one sorted
// (Sort- or index-driven), and one plain candidate. Single candidate per
// shape by construction — a pathlist holding more means the producer
// over-generates (§5 negative).
func addGroupingPaths(grouped *RelOptInfo, seed *Path, aggNode *Aggregate, child Node, cat catalog.Catalog, cp costParams, ps PlannerSettings) {
	inputRows := seed.Rows
	inputStartup, inputTotal := seed.Cost.Startup, seed.Cost.Total
	numGroups := grouped.Rows
	inNcols, inAvgVar := aggInputWidth(child)

	groupedOut := len(aggNode.GroupExprs) > 0 || aggNode.GroupingSets != nil
	presortedKeys, presorted := presortedAggKeysOrAbsent(aggNode, ps)
	if !groupedOut {
		// PLAIN (no GROUP BY, no grouping sets): one candidate. Usable
		// presorted keys still sort the input first (PG's AGG_PLAIN over
		// sorted input; today's rule wrapped the Sort even ungrouped).
		// Priced by the HASHED arm at 0 group columns and 1 group, which
		// is term-for-term PG's PLAIN arm (no grouping comparisons, trans
		// per input tuple, final once, emit once).
		input := seed
		producer := groupAggPlainProducer
		if presorted {
			input = sortPathForBounded(seed, pathkeysForSortKeys(presortedKeys), cp, -1)
			producer = groupAggSortedProducer
		}
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: aggNode,
			Rel: grouped, Rows: 1,
			Cost: costAgg(cp, AggStrategyHashed, inputRows, inputStartup, inputTotal,
				0, 1, len(aggNode.Aggs), inNcols, inAvgVar),
			Pathkeys: input.Pathkeys, Children: []*Path{input},
		}, producer)
		return
	}

	// HASHED. Offered whenever hashable; enable_hashagg = off marks it
	// DisabledNodes (B-17a preference, never skip) instead of deleting it.
	// Grouping sets always hash (today's fall-through; executor has one
	// hash table per set).
	if groupingHashable(aggNode, presorted) || aggNode.GroupingSets != nil {
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: aggNode,
			Rel: grouped, Rows: numGroups,
			DisabledNodes: disabledNodesFor(!ps.EnableHashAgg, seed),
			Cost: costAgg(cp, AggStrategyHashed, inputRows, inputStartup, inputTotal,
				len(aggNode.GroupExprs), numGroups, len(aggNode.Aggs), inNcols, inAvgVar),
			Children: []*Path{seed},
		}, groupAggHashedProducer)
	}

	// SORTED. Grouping sets stay out (executor cannot run them sorted).
	// Validity first: a group-keys Sort serves only aggregates that need
	// no ordered input. With special aggregates the ONLY valid sorted
	// input is the presorted-keys variant; with usable keys absent (or the
	// GUC off) there is no sorted candidate at all — hashed alone. This is
	// principled conservatism, and deliberately narrower than the retired
	// bridge (which wrapped a group-keys Sort even for unusable ordered
	// aggs under GUC-off): a group-keys order is not a valid ordered-agg
	// input, and declining the candidate can only forfeit a price contest,
	// never correctness. The executor sorts array_agg/string_agg ORDER BY
	// internally, so the common built-ins stay correct under hashed either
	// way.
	if aggNode.GroupingSets == nil {
		keys := presortedKeys
		if !presorted {
			if groupingHasSpecialAgg(aggNode) {
				return
			}
			keys = groupKeysSortKeys(aggNode)
		}
		if idxChild, idxSpec, ok := indexOrderedAggInput(aggNode, child, cat); ok {
			// The index-driven variant: no Sort, narrowed spec. The spec
			// is the builder's clone (remapped to narrowed positions);
			// the input price is the index child's own.
			idxSeed := newPrebuiltPath(grouped, idxChild)
			idxSeed.Rows = inputRows
			if pc := legacyDisplayCostOf(idxChild); pc.PlanRows > 0 || pc.TotalCost > 0 {
				idxSeed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
			}
			idxNcols, idxAvgVar := aggInputWidth(idxChild)
			addPath(grouped, &Path{
				Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: idxSpec,
				Rel: grouped, Rows: numGroups,
				Cost: costAgg(cp, AggStrategySorted, inputRows, idxSeed.Cost.Startup, idxSeed.Cost.Total,
					len(idxSpec.GroupExprs), numGroups, len(idxSpec.Aggs), idxNcols, idxAvgVar),
				Pathkeys: pathkeysForSortKeys(keys), Children: []*Path{idxSeed},
			}, groupAggSortedIdxProducer)
			return
		}
		sortedInput := sortPathForBounded(seed, pathkeysForSortKeys(keys), cp, -1)
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: aggNode,
			Rel: grouped, Rows: numGroups,
			Cost: costAgg(cp, AggStrategySorted, inputRows, sortedInput.Cost.Startup, sortedInput.Cost.Total,
				len(aggNode.GroupExprs), numGroups, len(aggNode.Aggs), inNcols, inAvgVar),
			Pathkeys: sortedInput.Pathkeys, Children: []*Path{sortedInput},
		}, groupAggSortedProducer)
	}
}

// indexOrderedAggInput is the surviving half of
// `applyIndexOrderedGroupingRule`: the catalog matcher + `buildIndexOrderedScan`
// as a candidate builder. It runs on a SHALLOW CLONE of the spec (the builder
// remaps GroupExprs/Args to narrowed positions and sets GroupKeyOrder) with a
// CLONED Filter node (the builder mutates `filt.Predicate`/`filt.Child` in
// place — the shared original must not move, or the hashed candidate's input
// would silently change underneath it).
//
// Fail-closed three ways: child shape must be SeqScan or Filter-over-SeqScan
// (the rule's scope); the returned child must still contain the Filter when
// one was present (the builder's non-narrowing arm drops it — declining here
// rather than replicating that); no usable index ⇒ absent. The deleted proxy
// gate (`enable_hashagg = off`) is deliberately NOT re-checked: price
// competition replaces it, and the GUC-on PK-FD pin (§5 gate) adjudicates.
func indexOrderedAggInput(aggNode *Aggregate, child Node, cat catalog.Catalog) (newChild Node, spec *Aggregate, ok bool) {
	if aggNode == nil || cat == nil {
		return nil, nil, false
	}
	if aggNode.Mode != AggModeSimple || aggNode.GroupingSets != nil || len(aggNode.GroupExprs) == 0 {
		return nil, nil, false
	}
	// Plain column refs only (the rule's scope line); duplicate names bail.
	groupCols := make([]string, len(aggNode.GroupExprs))
	groupSet := make(map[string]bool, len(aggNode.GroupExprs))
	for i, g := range aggNode.GroupExprs {
		cr, ok := g.(*ColumnRef)
		if !ok {
			return nil, nil, false
		}
		if groupSet[cr.Name] {
			return nil, nil, false
		}
		groupSet[cr.Name] = true
		groupCols[i] = cr.Name
	}

	clone := *aggNode
	var filtClone *Filter
	scanChild := clone.Child
	if f, isFilt := scanChild.(*Filter); isFilt {
		fc := *f
		filtClone = &fc
		scanChild = fc.Child
		clone.Child = filtClone
	}
	seqScan, ok := scanChild.(*SeqScan)
	if !ok || seqScan.Table == nil {
		return nil, nil, false
	}
	for _, idx := range cat.IndexesOnTable(seqScan.Table) {
		if idx == nil || idx.DeclaredHash || idx.HasPredicate {
			continue
		}
		if idx.Method != "" && idx.Method != "btree" {
			continue
		}
		if len(idx.Columns) < len(groupCols) {
			continue
		}
		prefix := idx.Columns[:len(groupCols)]
		ordered := true
		prefixSet := make(map[string]bool, len(prefix))
		for i, c := range prefix {
			if i < len(idx.ColDescending) && idx.ColDescending[i] {
				ordered = false
				break
			}
			if i < len(idx.ColNullsFirst) && idx.ColNullsFirst[i] {
				ordered = false
				break
			}
			prefixSet[c] = true
		}
		if !ordered || len(prefixSet) != len(groupSet) {
			continue
		}
		match := true
		for c := range groupSet {
			if !prefixSet[c] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		built, ok := buildIndexOrderedScan(seqScan, idx, &clone, filtClone)
		if !ok {
			continue
		}
		// Fail-closed on the dropped Filter: the builder's non-narrowing
		// arm returns a bare IndexScan, abandoning the predicate. The
		// narrowed arm returns the (cloned) Filter itself.
		if filtClone != nil && built != Node(filtClone) {
			continue
		}
		// The retired rule skipped an index child when the session
		// disabled that scan shape (review/260831-2 X-8); the producer
		// declines the variant instead of offering an unusable input.
		if scanShapeDisabled(built, cat) {
			continue
		}
		order := make([]int, len(prefix))
		for i, c := range prefix {
			for gi, gc := range groupCols {
				if gc == c {
					order[i] = gi
					break
				}
			}
		}
		clone.GroupKeyOrder = order
		return built, &clone, true
	}
	return nil, nil, false
}
