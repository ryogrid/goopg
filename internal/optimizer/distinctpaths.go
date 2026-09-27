package optimizer

// The DISTINCT upper rel — planner-refactor take3 C-16 (P4-07).
//
// `create_distinct_paths` (planner.c:4816) is the fourth Phase-4 upper
// producer after C-15's GROUP_AGG: for the finished DISTINCT input it
// offers hashed and unique-over-sorted `PathDistinct` candidates on the
// `(DISTINCT, NULL)` upper rel, prices each, and lets `add_path` select.
// There is no distinct rule to retire — SELECT DISTINCT planned as a bare
// `&Distinct{}` wrapper (M0097-0005), and `distinctOp` hash-dedups either
// shape — so this cut only ADDS a choice where none existed.
//
// Design: docs/design/planner-p4-distinct-paths/DESIGN.md. The shape is
// option (b) as C-12/C-15: the finished child is wrapped in a
// `PathPrebuilt` seed over the DISTINCT rel (input rows/cost), candidates
// stack above it, `setCheapest` runs, and `createPlanNode` on the winner
// emits the node through the new `PathDistinct` arm (`createDistinctPlan`,
// createplansimple.go).
//
// Two slices share this file: C-16a (hashed vs unique choice) and
// C-16b (unique-over-sorted via `DistinctOn` reuse — `distinctOnOp`
// already streams adjacent dedup, and both node kinds already render
// `"Unique"`, so ~0 executor LOC). C-16b is NOT a second producer: it is
// the second candidate below plus the arm mapping.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// distinctProducer strings for the DPPATH trace (pathtrace.go). With
// `Relids = 0` the lines read `producer=upper.distinct.* relids=-`, the
// same convention C-12/C-15 established.
const (
	distinctHashedProducer = "upper.distinct.hashed"
	distinctUniqueProducer = "upper.distinct.unique"
	// distinctPartialUniqueProducer is the `Unique -> Gather Merge ->
	// Unique -> Sort -> <partial>` candidate's trace label — the partial
	// DISTINCT arm of `create_partial_distinct_paths` (planner.c:4852),
	// M0146-0027 slice 2.
	distinctPartialUniqueProducer = "upper.distinct.partial.unique"
)

// createDistinctPaths is `create_distinct_paths` for the one DISTINCT
// goopg plans above the seam: the finished `distinctNode` (spec built at
// the wrapper site). It returns the winning node — `*Distinct`, or
// `*DistinctOn` with all-output-columns keys when the unique candidate
// wins — or a `PlanError` when no candidate exists (PG's "could not
// implement DISTINCT"; unreachable — hashed is always offered — defensive
// as C-15).
//
// DISTINCT ON never reaches here: both wrapper sites gate on
// `len(s.DistinctOn) == 0` (defense-in-depth; both parsers leave
// `Distinct=false` for DISTINCT ON today).
func createDistinctPaths(u *upperRels, distinctNode *Distinct, cat catalog.Catalog, ps PlannerSettings, tupleFraction float64) (Node, error) {
	if distinctNode == nil {
		return nil, &PlanError{Code: "XX000", Message: "createDistinctPaths: nil distinct node"}
	}
	if u == nil {
		u = newUpperRels()
	}
	cp := ps.costParams()
	distinctRel := fetchUpperRel(u, UpperDistinct, 0, tupleFraction)
	sizeDistinctRelFromNode(distinctRel, distinctNode)

	child := distinctNode.Child
	inputRows := float64(EstimateRows(child))
	if inputRows < 0 {
		inputRows = 0
	}
	seed := newPrebuiltPath(distinctRel, child)
	seed.Rows = inputRows
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}

	addDistinctPaths(distinctRel, seed, distinctNode, child, cp, ps)
	addPartialDistinctPaths(u, distinctRel, seed, distinctNode, child, cp, ps)
	setCheapest(distinctRel)

	best := getCheapestFractionalPath(distinctRel, tupleFraction)
	if best == nil {
		return nil, &PlanError{Pos: distinctNode.Pos(), Code: "0A000",
			Message: "could not implement DISTINCT"}
	}
	node, _ := createPlanNode(best)
	if node == nil {
		return nil, &PlanError{Pos: distinctNode.Pos(), Code: "XX000",
			Message: "createDistinctPaths: PathDistinct built no node"}
	}
	// The arm builds a FRESH node (spec copy for Distinct, DistinctOn for
	// Unique) — unlike C-15 there is no aliasing to preserve, because the
	// spec was just built at the wrapper site and nothing else references
	// it. Call sites adopt the return value.
	switch node.(type) {
	case *Distinct, *DistinctOn:
		return node, nil
	}
	return nil, &PlanError{Pos: distinctNode.Pos(), Code: "XX000",
		Message: "createDistinctPaths: PathDistinct built no distinct node"}
}

// sizeDistinctRelFromNode sizes the DISTINCT rel: Rows from P1-25's
// `estimateDistinctRows` (grouping over every output column — F3, clamped
// ≥ 1); Width/NCols/AvgVarBytes describe the output (the §4.3 duty; no
// spill arm exists for distinct).
func sizeDistinctRelFromNode(rel *RelOptInfo, distinctNode *Distinct) {
	if rel == nil || distinctNode == nil {
		return
	}
	cols := distinctNode.Output()
	rows := estimateDistinctRows(cols, distinctNode.Child)
	if rows < 1 {
		rows = 1
	}
	rel.Rows = float64(rows)
	rel.Width = nodeTupleWidth(distinctNode)
	rel.NCols = len(cols)
	rel.AvgVarBytes = nodeAvgVarBytes(cols)
}

// distinctOutputSatisfiesOrder reports whether a DISTINCT node's output
// already delivers a required ORDER BY, making the M0097-0046 outer Sort
// redundant (R84). Sound iff ALL of the following hold:
//
//   - out is *Distinct (type-gate, fail-closed): its executor,
//     distinctOp, hash-dedups then ALWAYS re-sorts ascending,
//     NULLs last, over all columns — input order is destroyed,
//     so the delivered order is exactly (c0 ASC NL, c1 ASC NL,
//     …). *DistinctOn streams input order instead (different
//     operator, different argument — out of scope here).
//   - every required key is ASC with nulls-last (read from the
//     effective SortKey entries — sortByNullsFirst already
//     applied — never the raw parser flags).
//   - the required keys are a positional prefix of the output:
//     key i is a ColumnRef with Index == i, for positions
//     0..n-1 in order. A full-row lexicographic ASC/NL order
//     satisfies exactly its prefixes — ORDER BY (c1) alone is
//     NOT satisfied by a (c0,c1) ordering.
//
// Anything else keeps the Sort. In particular DESC, explicit
// NULLS FIRST, non-ColumnRef keys, and non-prefix keys all
// decline — root-0036/DESC behavior is preserved by
// construction.
func distinctOutputSatisfiesOrder(out Node, outerKeys []SortKey) bool {
	if _, ok := out.(*Distinct); !ok {
		return false
	}
	if len(outerKeys) == 0 {
		return false
	}
	for i, k := range outerKeys {
		if k.Desc || k.NullsFirst {
			return false
		}
		cr, ok := k.Expr.(*ColumnRef)
		if !ok || cr.Index != i {
			return false
		}
	}
	return true
}

// distinctAllColKeys is one ascending SortKey per output column — the input
// order a streaming dedup consumes (and the Sort the producer stacks when
// the input does not deliver it).
func distinctAllColKeys(child Node) []SortKey {
	cols := child.Output()
	keys := make([]SortKey, 0, len(cols))
	for i, c := range cols {
		keys = append(keys, SortKey{
			Expr:       &ColumnRef{Index: i, Name: c.Name, Type: c.Type},
			Desc:       false,
			NullsFirst: false,
		})
	}
	return keys
}

// distinctClauseKeys is transformDistinctClause's distinct-clause order
// (parse_clause.c) for SELECT DISTINCT with an ORDER BY: each ORDER BY item
// first, keeping its direction and NULLS placement, then every output
// column the ORDER BY did not name, ascending, in output order. PG requires
// every SELECT DISTINCT sort key to be an output column (42P10 otherwise),
// so an ORDER BY key that is not a plain output-column reference returns
// nil — the caller keeps the all-columns-ascending default.
func distinctClauseKeys(orderKeys []SortKey, cols Schema) []SortKey {
	if len(orderKeys) == 0 {
		return nil
	}
	seen := make([]bool, len(cols))
	keys := make([]SortKey, 0, len(cols))
	for _, k := range orderKeys {
		cr, ok := k.Expr.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(cols) {
			return nil
		}
		if seen[cr.Index] {
			continue
		}
		seen[cr.Index] = true
		c := cols[cr.Index]
		keys = append(keys, SortKey{Expr: &ColumnRef{Index: cr.Index, Name: c.Name, Type: c.Type}, Desc: k.Desc, NullsFirst: k.NullsFirst})
	}
	for i, c := range cols {
		if !seen[i] {
			keys = append(keys, SortKey{Expr: &ColumnRef{Index: i, Name: c.Name, Type: c.Type}})
		}
	}
	return keys
}

// distinctAllKeyCols is every output position — the `DistinctOn.KeyCols`
// for the unique candidate (full-row dedup).
func distinctAllKeyCols(child Node) []int {
	n := len(child.Output())
	cols := make([]int, 0, n)
	for i := 0; i < n; i++ {
		cols = append(cols, i)
	}
	return cols
}

// distinctCost prices the dedup work both DISTINCT forms share: PG's Unique
// price (`cpu_operator` per input row for the adjacent comparison +
// `cpu_tuple` per output row) on top of the input. The hashed form pays the
// same terms — the executor hash-dedups per row either way — so hashed vs
// unique differ only in their INPUT price (seed vs Sort), never here.
// Startup carries the input's startup plus the per-row compare (the Sort
// blocks anyway, so streaming buys nothing here — stated, not modeled).
func distinctCost(inputStartup, inputTotal, inputRows, outputRows float64, cp costParams) Cost {
	startup := inputStartup + cp.cpuOperatorCost*inputRows
	total := inputTotal + cp.cpuOperatorCost*inputRows + cp.cpuTupleCost*outputRows
	return Cost{Startup: startup, Total: total}
}

// addDistinctPaths is the per-input body of `create_final_distinct_paths`
// for goopg's one input: hashed always, unique-over-sorted always (over
// the producer-stacked Sort — input order guaranteed by construction).
// Single candidate per shape by construction.
//
// Insertion order is hashed first. PG's create_final_distinct_paths adds the
// sorted (Unique) paths first, but in PG both survive add_path (the Unique
// has pathkeys) and the choice is made after the ORDER BY stage has costed
// its Sort. goopg elects ONE winner here, before ORDER BY, where addPath
// keeps the first of two fuzzily-tied candidates — so PG's order alone elects
// Unique on `SELECT DISTINCT a, b … ORDER BY a, b LIMIT 100`, where PG
// elects HashAggregate + Sort. PG's order belongs with carrying both
// candidates to the ordered rel (ledgered, M0141-S2b-4d).
func addDistinctPaths(distinctRel *RelOptInfo, seed *Path, distinctNode *Distinct, child Node, cp costParams, ps PlannerSettings) {
	hashed, unique := distinctCandidates(distinctRel, seed, distinctNode, child, cp, ps)
	addPath(distinctRel, hashed, distinctHashedProducer)
	addPath(distinctRel, unique, distinctUniqueProducer)
}

// addUnionDistinctPaths is the same pair for a UNION (distinct), in
// generate_union_paths' order: the hashed aggregate first, then Sort ->
// Unique (prepunion.c, `if (can_hash)` precedes `if (can_sort)`).
func addUnionDistinctPaths(rel *RelOptInfo, seed *Path, distinctNode *Distinct, child Node, cp costParams, ps PlannerSettings) {
	hashed, unique := distinctCandidates(rel, seed, distinctNode, child, cp, ps)
	addPath(rel, hashed, distinctHashedProducer)
	addPath(rel, unique, distinctUniqueProducer)
}

// distinctCandidates builds the hashed and unique-over-sorted PathDistinct
// candidates shared by addDistinctPaths and addUnionDistinctPaths.
func distinctCandidates(distinctRel *RelOptInfo, seed *Path, distinctNode *Distinct, child Node, cp costParams, ps PlannerSettings) (hashed, unique *Path) {
	inputRows := seed.Rows
	numDistinct := distinctRel.Rows

	// HASHED: the executor hash-dedups the seed as-is (today's behavior).
	// `enable_hashagg = off` marks it DisabledNodes (B-17a preference,
	// never skip) instead of deleting it.
	hashed = &Path{
		Kind: PathDistinct, Distinct: distinctNode,
		Rel: distinctRel, Rows: numDistinct,
		DisabledNodes: disabledNodesFor(!ps.EnableHashAgg, seed),
		Cost: costAgg(cp, AggStrategyHashed, inputRows, seed.Cost.Startup, seed.Cost.Total,
			len(child.Output()), numDistinct, 0, 0, 0),
		Children: []*Path{seed},
	}

	// UNIQUE over the producer-stacked Sort (streaming adjacent dedup).
	// This is the ONLY Sort-driven candidate: a "sorted Distinct" (hash
	// dedup over the same Sort) would price and order identically to it
	// and be rejected as a duplicate by add_path — offering both would be
	// noise, not choice. PG likewise builds Unique, not sorted-Agg, for
	// the sorted DISTINCT shape.
	//
	// With no output columns there is nothing to sort by: a zero-column UNION
	// (SQL allows `SELECT FROM … UNION SELECT FROM …`; SELECT DISTINCT never
	// reaches here without targets) takes the Unique straight over its input,
	// as generate_union_paths does (`if (groupList != NIL) path =
	// create_sort_path(...)`, prepunion.c) — M0141-S2b-4a.
	sortInput := seed
	keys := distinctAllColKeys(child)
	if len(distinctNode.SortKeys) == len(keys) {
		keys = distinctNode.SortKeys
	}
	if len(keys) > 0 {
		sortInput = sortPathForBounded(seed, pathkeysForSortKeys(keys), cp, -1)
	}
	uniqueCost := distinctCost(sortInput.Cost.Startup, sortInput.Cost.Total, inputRows, numDistinct, cp)
	unique = &Path{
		Kind: PathDistinct, Distinct: distinctNode, Unique: true,
		Rel: distinctRel, Rows: numDistinct,
		DisabledNodes: sortInput.DisabledNodes,
		Cost:          uniqueCost,
		Pathkeys:      sortInput.Pathkeys, Children: []*Path{sortInput},
	}
	return hashed, unique
}

// addPartialDistinctPaths is `create_partial_distinct_paths`
// (planner.c:4852) at its sorted arm — the arm every observed TPC-DS
// witness elects (Q38/Q87:
// `Unique -> Gather Merge -> Unique -> Sort -> <partial subtree>`).
//
// It is the M0146-0025 partial-Group arm's DISTINCT sibling, built on the
// same node-level model partialaggupper.go's header states: the partial
// input is the SERIAL subtree run once per worker (a Gather unwraps to
// its own child), not a separate partial path — PG's
// `input_rel->partial_pathlist` iteration has no goopg counterpart
// because the searched rel's partial list is where the gather-path
// arm already files the same subtree's candidates, and the prebuilt
// node carries no coordinate boundary to cross. Same guards as the
// aggregate arm, same `PartialUnique`≙`PartialGroup` marker discipline:
// the per-worker dedup is only correct because the leader-side Unique
// re-dedups the merge, so the driving-scan walks gate the descent on
// the mark rather than on the node kind.
//
// The arm is sorted-only, deliberately narrower than upstream's
// producer: upstream also offers a partial HASHED aggregate for DISTINCT
// (planner.c:4989) and a LIMIT-1 partial path for an empty distinct
// pathkey list; goopg's hashed partial emission is the transition-state
// transport, which a dedup has no state to feed, and the empty-keys case
// is left serial — both refusals, never approximations.
func addPartialDistinctPaths(u *upperRels, distinctRel *RelOptInfo, seed *Path, distinctNode *Distinct, child Node, cp costParams, ps PlannerSettings) {
	if !parallelOn.Load() || ps.MaxParallelWorkersPerGather <= 0 {
		traceUpperGate("distinct-upper", "refused", "gate=statement")
		return
	}
	if distinctRel == nil || seed == nil || distinctNode == nil || child == nil {
		traceUpperGate("distinct-upper", "refused", "gate=nil-arg")
		return
	}
	// The ordering the per-worker Sort imposes — the dedup's full-column
	// key (distinct-clause order when the statement carries one), the same
	// derivation `distinctCandidates` makes for the serial Unique. A
	// zero-column output is upstream's LIMIT-1 arm — declined here rather
	// than modelled.
	keys := distinctAllColKeys(child)
	if len(distinctNode.SortKeys) == len(keys) {
		keys = distinctNode.SortKeys
	}
	if len(keys) == 0 {
		traceUpperGate("distinct-upper", "refused", "gate=keys")
		return
	}
	// Drop the leader-side Sort the serial Unique candidate's input
	// carries: it exists to feed exactly the dedup this arm relocates,
	// and the worker-side Sort below imposes the same ordering per
	// partition. Stripping it is also what lets the Gather splice see the
	// boundary underneath (`Sort{Gather{X}}` -> `Gather{X}` -> `X`).
	if s, ok := child.(*Sort); ok && s.Child != nil {
		child = s.Child
	}
	// Statement-level gate, mirroring addPartialAggSplitPath verbatim: a
	// nested planning scope may only reuse a parallel boundary the input
	// already carries (it may not introduce a NEW Gather under a
	// parallel-aware parent), and a correlated input is never split.
	if !ps.ParallelStatementOK {
		if _, ok := gatherToUnwrapForPartialAgg(child); !ok || planHasOuterRef(child) {
			traceUpperGate("distinct-upper", "refused", "gate=statement")
			return
		}
	}
	// Unwrap a search-placed Gather — direct, or under the pass-through
	// wrappers gatherToUnwrapForPartialAgg peels (schema-preserving all).
	if g, ok := gatherToUnwrapForPartialAgg(child); ok {
		child = g
	}
	// The subtree must be worker-executable, Gather-free, and have a
	// driving scan — the identical triple the aggregate arm asserts, with
	// the identical second route when the only refusal is a Gather on the
	// driving spine (spliceGatherOnPartialSpine; TPC-DS Q38's input is
	// exactly `Sort{Gather{NL…}}` before the strip above).
	unsafe, gathered, noScan := subtreeHasUnsafeNode(child), subtreeHasGather(child), drivingScan(child) == nil
	if unsafe || gathered || noScan {
		if ps.ParallelStatementOK {
			if gc, ok := spliceGatherOnPartialSpine(child); ok &&
				!subtreeHasUnsafeNode(gc) && !subtreeHasGather(gc) && drivingScan(gc) != nil {
				child = gc
				unsafe, gathered, noScan = false, false, false
			}
		}
	}
	if unsafe || gathered || noScan {
		traceUpperGate("distinct-upper", "refused", "gate=subtree subtree="+subtreeRefusalKind(unsafe, gathered, noScan))
		return
	}
	workers := upperSplitWorkers(child, cp, ps)
	if workers <= 0 {
		traceUpperGate("distinct-upper", "refused", "gate=workers")
		return
	}
	d := getParallelDivisor(workers, ps.ParallelLeaderParticipation)
	if d <= 1 {
		traceUpperGate("distinct-upper", "refused", "gate=divisor "+upperSplitDetail(workers, d))
		return
	}
	inputRows := seed.Rows
	if inputRows < 1 {
		inputRows = 1
	}
	perWorkerRows := inputRows / d
	// Upstream's numDistinctRows (planner.c:4897-4900): estimate_num_groups
	// over the cheapest_partial_path's row count — the per-worker scale.
	// Same ColumnRef-over-output-schema exprs estimateDistinctRows builds.
	cols := child.Output()
	exprs := make([]Expr, 0, len(cols))
	for i, c := range cols {
		exprs = append(exprs, &ColumnRef{Index: i, Name: c.Name, Type: c.Type, SourceTableIdx: c.SourceTableIdx})
	}
	partialGroups := float64(estimateNumGroups(exprs, child, int64(perWorkerRows)))
	if perWorkerRows >= 1 && partialGroups > perWorkerRows {
		partialGroups = perWorkerRows
	}
	if partialGroups < 1 {
		partialGroups = 1
	}
	finalRows := distinctRel.Rows
	if finalRows < 1 {
		finalRows = 1
	}

	partialRel := fetchUpperRel(u, UpperPartialDistinct, 0, 0)
	partialRel.Rows = partialGroups
	partialRel.Width, partialRel.NCols, partialRel.AvgVarBytes = distinctRel.Width, distinctRel.NCols, distinctRel.AvgVarBytes
	// `partial_distinct_rel->consider_parallel =
	// input_rel->consider_parallel` (planner.c:4880); a rel reaches here
	// only with the subtree guard's green light.
	partialRel.ConsiderParallel = true

	pseed := newPrebuiltPath(partialRel, child)
	pseed.Rows = perWorkerRows
	pseed.Cost = parallelSeedCost(seed.Cost, d)
	pseed.ParallelSafe = true
	pseed.ParallelWorkers = workers

	mergeKeys := pathkeysForSortKeys(keys)
	// `make_ordered_path` (planner.c:4926): the worker's own sort, priced
	// on the per-worker row count — sortPathForBounded already prices the
	// path's own rows, so the worker-side saving is charged exactly.
	workerSort := sortPathForBounded(pseed, mergeKeys, cp, -1)
	// `sortPathForBounded` prices ParallelSafe but never plans workers —
	// gatherChildPlan panics on a 0-worker subpath, the R56 rule the
	// partial-Group arm records verbatim.
	workerSort.ParallelWorkers = workers

	// The PARTIAL UNIQUE — `create_upper_unique_path` on the
	// partial_distinct_rel (planner.c:4973): each worker adjacent-dedups
	// its own sorted partition. The spec clone carries PartialUnique, the
	// flag every driving/attach walk gates the descent on; the clone's
	// other fields are the statement's own (pos/schema are the only ones
	// createDistinctPlan reads for the Unique shape).
	partialSpec := *distinctNode
	partialSpec.PartialUnique = true
	partialPath := &Path{
		Kind: PathDistinct, Unique: true, Distinct: &partialSpec,
		Rel: partialRel, Rows: partialGroups,
		Cost:            distinctCost(workerSort.Cost.Startup, workerSort.Cost.Total, perWorkerRows, partialGroups, cp),
		DisabledNodes:   workerSort.DisabledNodes,
		ParallelSafe:    true,
		ParallelWorkers: workers,
		Pathkeys:        append([]PathKey(nil), mergeKeys...),
		Children:        []*Path{workerSort},
	}
	addPath(partialRel, partialPath, distinctPartialUniqueProducer)

	// BOUNDARY — `cost_gather_merge` over the partial unique's rows: what
	// crosses is each worker's DEDUPLICATED output (partialGroups × d),
	// the whole economic argument of the shape. Upstream files it through
	// generate_useful_gather_paths on the partial rel (planner.c:5017);
	// this arm builds the same node directly, the partial-Group arm's
	// construction verbatim.
	crossed := partialGroups * d
	gmCost := gatherMergeCost(cp, partialPath.Cost, workers, crossed)
	gmPath := &Path{
		Kind: PathGatherMerge, Rel: distinctRel, Rows: crossed, Cost: gmCost,
		Pathkeys:      append([]PathKey(nil), mergeKeys...),
		ParallelSafe:  false,
		DisabledNodes: partialPath.DisabledNodes + disabledNodesFor(!cp.enableGatherMerge),
		Children:      []*Path{partialPath},
	}

	// FINAL UNIQUE — the leader-side `create_upper_unique_path` of
	// `create_final_distinct_paths` over the gathered rel (planner.c:5167):
	// re-dedups cross-worker duplicates the merge brings adjacent.
	finalPath := &Path{
		Kind: PathDistinct, Unique: true, Distinct: distinctNode,
		Rel: distinctRel, Rows: finalRows,
		DisabledNodes: gmPath.DisabledNodes,
		Cost:          distinctCost(gmCost.Startup, gmCost.Total, crossed, finalRows, cp),
		Pathkeys:      gmPath.Pathkeys,
		Children:      []*Path{gmPath},
	}
	addPath(distinctRel, finalPath, distinctPartialUniqueProducer)
}
