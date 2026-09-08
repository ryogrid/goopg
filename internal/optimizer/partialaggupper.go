package optimizer

// partialaggupper.go — C-19g's REMAINDER (DESIGN §8): the upper-rel-resident
// half of `create_partial_grouping_paths`.
//
// WHAT WAS MISSING. C-19g replaced the split VERDICT (`partialAggSplitPays`,
// which "returns only a boolean and constructs no node") but not the
// CONSTRUCTION: `splitAggregate` (parallel.go) still built
// `Finalize -> Gather -> Partial` inside the `MaybeAddGather` POST-PASS. So the
// whole of C-19g's Q1 win was delivered through the post-pass and died with it,
// which is what blocked C-19h (`docs/design/planner-c19h-gather-postpass/DESIGN.md`
// §3: 12/22 TPC-H queries carry a Gather with the post-pass, 0/22 without; a
// stand-down build under `GOOPG_GATHER_PATHS=all GOOPG_PARTIAL_AGG_PATHS=on`
// reached only 7/22, losing Q1/Q6/Q14/Q15a/Q16/Q19 — every one of them an
// aggregate query).
//
// This file makes the split a PATH on the GROUP_AGG upper rel, filed by
// `addGroupingPaths` and adjudicated by `addPath`/`setCheapest` against the
// hashed / sorted / plain serial candidates — upstream's
// `add_paths_to_grouping_rel` (planner.c:7114) reaching the same shape from
// `partially_grouped_rel` (planner.c:7351) + `gather_grouping_paths`
// (planner.c:7704) + AGGSPLIT_FINAL_DESERIAL (planner.c:7250).
//
// THE TWO BLOCKERS C-19g's §2.2 NAMED, AND HOW EACH IS ANSWERED.
//
//  1. "No input rel" — upstream seeds `partially_grouped_rel` from
//     `input_rel->partial_pathlist`, and the rel carrying `PartialPathlist`
//     dies inside `planJoinlistSearch` before the aggregate stage runs.
//     ANSWER: goopg does not need a distinct partial PLAN. `gatherOp.runWorker`
//     builds each worker's own copy of the Gather's child subtree and calls
//     `attachParallelScan` on it, so the partial plan IS the serial subtree
//     with its driving scan stamped — which is exactly what `splitAggregate`
//     has always relied on. What upstream's partial path supplies that goopg's
//     Node cannot is the PRICE, and that is supplied here by dividing the
//     subtree's RUN cost by `get_parallel_divisor` (see `parallelSeedCost`),
//     the same adjustment `cost_seqscan` makes for `parallel_workers > 0`.
//     The construction is `splitAggregate` itself, called from this kind's
//     `createPlan` arm, so the path model and the post-pass emit a
//     BYTE-IDENTICAL node shape (rule #2, sibling paths must agree).
//
//  2. "The parallel decision may not be CACHED" — `MaybeAddGather` runs after
//     the plan-cache lookup because the cache is process-wide, keyed on
//     (dbOid, normalised SQL), and carried no parallel fingerprint, so a plan
//     built under `max_parallel_workers_per_gather = 4` would be served to a
//     session that set it to 0.
//     ANSWER, in two halves, both landed with this file:
//     (a) the parallel block is now read from the session into
//     `PlannerSettings` and is part of `plannerCacheFingerprint`
//     (internal/postmaster/dispatch.go), so a session with a different
//     parallel setting keys into its own cache entry instead of borrowing
//     one; and
//     (b) the residual inputs that are NOT session GUCs — the transaction's
//     isolation level, and the statement-shape refusals `statementIsParallelSafe`
//     makes — are enforced POST-cache by `StripGather` (parallel.go), which the
//     post-pass calls on any plan it is not allowed to parallelise. A cached
//     parallel plan can therefore never be executed by a session that may not
//     run it.
//
// Design: docs/design/planner-c19g-partial-agg/DESIGN.md §8.

import "github.com/goopg/goopg/internal/catalog"

// partialAggSplitProducer is this producer's DPPATH trace string
// (pathtrace.go). It reads `producer=upper.groupagg.split relids=-`.
const partialAggSplitPathProducer = "upper.groupagg.split"

// addPartialAggSplitPath files the parallel `Finalize -> Gather -> Partial`
// candidate onto the GROUP_AGG rel, or files nothing.
//
// Every refusal below is fail-CLOSED, in `considerparallel.go`'s house style: a
// missing fact means no candidate, never an optimistic one. The order is the
// cheapest test first.
// gatherToUnwrapForPartialAgg returns the child of a Gather that the SEARCH
// placed directly under the aggregate's input, so a partial aggregate can be
// built below it (R21 slice 2b, K23).
//
// Deliberately narrow, because breadth here would be unsound:
//
//   - only a Gather reached through the boundary chain's single-child
//     wrappers is unwrapped. A Gather buried under a join is another rel's
//     parallelism and is none of this producer's business;
//   - `*GatherMerge` is NOT unwrapped. It carries an ordering its consumer
//     may depend on, and dropping it would silently lose that ordering —
//     the class of change that returns wrong-ordered rows rather than an
//     error. A GatherMerge input keeps today's refusal.
//
// Returns (child, true) only when the unwrap is safe.
//
// NOT ENABLED. Two attempts, both measured, so the next one starts from what
// is actually true rather than from either of my guesses:
//
//  1. Enable the unwrap alone -> TPC-H Q9/Q13 panic (below).
//  2. Enable it AND clear the stale stamp on a copy of the aggregate spec ->
//     THE SAME PANIC, unchanged. So the stamp is not carried on the spec the
//     producer passes down: it is applied POST-HOC to the emitted node by the
//     caller (the same ordering `createWindowPlan`'s comment describes for
//     windows — "buildWindowStage stamps it on the emitted node after the
//     producer returns"). Clearing a spec the assertion never reads changes
//     nothing.
//
// So the real question is why `deriveAggregateInputKeep` returns an EMPTY
// keep marked KNOWN for the unwrapped shape — `[]` with
// `InputTargetKnown = true` is what the panic reports, and per plan.go an
// empty list "is NOT the same as unknown". Either the derivation should
// return `ok = false` here (unknown, the safe direction), or it is failing to
// enumerate group inputs through the new child and that is the bug. Start
// there, in `group_input_target.go`, not in the producer.
//
// Original symptom, unchanged across both attempts:
//
//	createPlan: Aggregate input target [] drops group-input column "l_year"
//	of a 26-column input row
//
// because the aggregate carries a B-01c INPUT TARGET — a keep-list of the child
// columns it needs — computed against the child it was given, i.e. the
// GATHER. Swapping in the Gather's child changes what "the input row" is,
// and the stamped target no longer describes it, so the totality assertion
// fires (correctly).
//
// A Gather is schema-preserving, so the target is not wrong in CONTENT; it
// is stale in PROVENANCE. The fix is therefore to re-derive the aggregate's
// input target against the unwrapped child (`stampAggInputTarget`'s path)
// after the swap, not to weaken the assertion — which is load-bearing:
// dropping a group-input column silently changes GROUP BY semantics.
//
// Verified this crash is MINE and not the flip's: R19 captured all 22 TPC-H
// plans under `GOOPG_GATHER_PATHS=all` with no failure.
func gatherToUnwrapForPartialAgg(n Node) (Node, bool) {
	for depth := 0; n != nil && depth < 32; depth++ {
		if g, ok := n.(*Gather); ok {
			if g.Child == nil {
				return nil, false
			}
			return g.Child, true
		}
		kids := boundaryWalkChildren(n)
		if len(kids) != 1 {
			return nil, false
		}
		n = kids[0]
	}
	return nil, false
}

func addPartialAggSplitPath(u *upperRels, grouped *RelOptInfo, seed *Path, aggNode *Aggregate, child Node, cp costParams, ps PlannerSettings) *Path {
	if partialAggPathsMode != partialAggPathsOn {
		return nil
	}
	// The statement-level refusals `MaybeAddGather` makes at its own entry.
	// `ParallelStatementOK` is the top-level statement's shape (a bare SELECT,
	// not a DML/DDL/utility statement and not a nested planning scope); the
	// per-node refusals are `subtreeHasUnsafeNode`'s.
	if !ps.ParallelStatementOK || !parallelOn.Load() || ps.MaxParallelWorkersPerGather <= 0 {
		return nil
	}
	if grouped == nil || seed == nil || aggNode == nil || child == nil {
		return nil
	}
	// The subtree must be one a worker can execute, must not already carry a
	// Gather (C-19d's coexistence rule, here as a generation refusal rather
	// than a post-pass stand-down), and must have a driving scan — without one
	// every worker reads the whole relation and the Gather returns N+1 copies
	// of every row (`createplangather.go`'s file header).
	// R21 slice 2b (K23): UNWRAP a Gather the search already placed, rather
	// than refusing because of it.
	//
	// Under `GOOPG_GATHER_PATHS=all` the search puts a Gather at the join
	// level, so `child` contains one, so the guard below refused and goopg
	// lost the `Partial`/`Finalize` split it emits without the flip — the
	// whole of `aggregation-strategy` 10 -> 14.
	//
	// The fix does not need a partial PATH, and that is this file's own
	// point (blocker 1 above): "goopg does not need a distinct partial PLAN.
	// `gatherOp.runWorker` builds each worker's own copy of the Gather's
	// child subtree ... so the partial plan IS the serial subtree". So the
	// partial input is simply the Gather's CHILD — a node that already
	// exists, in the coordinate space the aggregate above already consumes.
	// Nothing is rebuilt from a Path, so there is no coordinate translation
	// and no boundary-map hole to fall into.
	//
	// Having unwrapped, the EXISTING arm below builds
	// `partial agg -> Gather -> finalise` over it, which is PG's shape
	// (`create_partial_grouping_paths` + `gather_grouping_paths`,
	// planner.c:7351/:7704). The guard is then satisfied honestly rather
	// than bypassed: there is genuinely no Gather left in the input, so the
	// two-Gather hazard it protects against cannot arise.
	// STILL DISABLED — see the sharpened note above the helper. Clearing the
	// spec's stamp (below) is NOT sufficient: the target is stamped POST-HOC
	// on the emitted node, so the spec copy never reaches the assertion.
	if g, ok := gatherToUnwrapForPartialAgg(child); ok && false {
		child = g
		// The aggregate's B-01c input target was derived against the child
		// we just replaced (the Gather), so it is STALE IN PROVENANCE — not
		// wrong in content, since a Gather is schema-preserving, but it no
		// longer describes "the input row" the assertion checks against.
		// Leaving it stamped panics `assertAggregateInputTargetCoversKeys`
		// on TPC-H Q9/Q13.
		//
		// Clearing it is what the field's own contract prescribes: the
		// target is "NEVER applied: no Project insertion, no schema change,
		// no cost change — behaviour-neutral by construction ... read by
		// nothing except assertAggregateInputTargetCoversKeys", and
		// "InputTargetKnown false means unknown ... a clone that drops the
		// stamp reads as unknown, THE SAFE DIRECTION" (plan.go).
		//
		// So this forfeits an assertion, not an optimisation, and forfeits
		// it only on the arm whose child changed. A shallow copy keeps every
		// other field (GroupExprs, Aggs, GroupingSets) shared, and the
		// caller's own aggNode is left untouched for the other arms.
		unwrapped := *aggNode
		unwrapped.InputTarget = nil
		unwrapped.InputTargetKnown = false
		aggNode = &unwrapped
	}
	// The guard stays, and now MEANS something for both routes: after the
	// unwrap above there is no Gather left, and on the post-pass route there
	// never was one. Two Gathers would have every worker read the whole
	// relation and return N+1 copies of every row.
	if subtreeHasUnsafeNode(child) || subtreeHasGather(child) || drivingScan(child) == nil {
		return nil
	}
	workers := upperSplitWorkers(child, cp, ps)
	if workers <= 0 {
		return nil
	}
	d := getParallelDivisor(workers, ps.ParallelLeaderParticipation)
	if d <= 1 {
		return nil
	}

	inputRows := seed.Rows
	if inputRows < 1 {
		inputRows = 1
	}
	perWorkerRows := inputRows / d

	// Upstream's `dNumPartialPartialGroups = get_number_of_groups(root,
	// cheapest_partial_path->rows, …)` (planner.c:7452), through the same
	// PG-faithful `estimate_num_groups` port C-15 sizes the GROUP_AGG rel with.
	partialGroups := float64(estimateNumGroups(aggNode.GroupExprs, child, int64(perWorkerRows)))
	if perWorkerRows >= 1 && partialGroups > perWorkerRows {
		partialGroups = perWorkerRows
	}
	if partialGroups < 1 {
		partialGroups = 1
	}
	finalGroups := grouped.Rows
	if finalGroups < 1 {
		finalGroups = 1
	}

	partialRel := fetchUpperRel(u, UpperPartialGroupAgg, 0, 0)
	partialRel.Rows = partialGroups
	partialRel.Width, partialRel.NCols, partialRel.AvgVarBytes = grouped.Width, grouped.NCols, grouped.AvgVarBytes
	// `partially_grouped_rel->consider_parallel = grouped_rel->consider_parallel`
	// (planner.c:7405).
	partialRel.ConsiderParallel = true

	// The PARTIAL INPUT path. Same subtree, per-worker rows, run cost divided
	// by the parallel divisor — see the file header, blocker 1.
	pseed := newPrebuiltPath(partialRel, child)
	pseed.Rows = perWorkerRows
	pseed.Cost = parallelSeedCost(seed.Cost, d)
	pseed.ParallelSafe = true
	pseed.ParallelWorkers = workers

	nAggs := len(aggNode.Aggs)
	nGroupCols := len(aggNode.GroupExprs)
	inNcols, inAvgVar := aggInputWidth(child)
	strategy := aggNode.Strategy

	// ── the SPLIT family, offered only for a DECOMPOSABLE aggregate ─────────
	//
	// Decomposability is re-asserted here rather than assumed from the caller,
	// for the reason `createPartialGroupingPaths` states: considerparallel.go
	// is fail-closed by hard-won design and a producer that trusts a caller's
	// gate is one refactor away from being a hole. It gates the SPLIT ONLY —
	// the gathered arm below is valid for any aggregate, and gating the whole
	// producer on it cost TPC-H Q16 its Gather in the C-19h census (a
	// `count(distinct …)` cannot be split, but the Gather still belongs below
	// it, which is exactly what `terminatesPartial` makes the post-pass do).
	var split *Path
	if aggregateSplitIsSafe(aggNode) {
		split = addPartialAggSplitArm(grouped, partialRel, pseed, aggNode, cp,
			workers, d, perWorkerRows, partialGroups, finalGroups,
			nGroupCols, nAggs, inNcols, inAvgVar, strategy)
	}

	// ── the GATHERED NO-SPLIT arm ───────────────────────────────────────────
	//
	// The whole relation crosses the boundary and ONE leader-side aggregate
	// consumes it: `Agg -> Gather -> input`. This is the shape `MaybeAddGather`
	// builds when `findPartialSubtree` falls through `terminatesPartial`, so it
	// is not a hypothetical — it is the plan that really ships today for most
	// of TPC-H.
	//
	// It has to be a CANDIDATE, not an absence, and C-19g's tournament is why:
	// the serial arms filed by `addGroupingPaths` are priced over the undivided
	// input, so a split beats them on the parallel divisor alone and would win
	// even where it pre-aggregates NOTHING. TPC-H Q3 and Q10 are exactly that
	// shape — ~300 k groups out of ~300 k rows — and without this arm the split
	// won both and cost them the leader-side aggregate for no reduction at all.
	// With it, `add_path` compares the two parallel shapes against each other
	// on the reduction ratio, which is the quantity the decision turns on
	// (DESIGN §3.4).
	nsGatherCost := gatherCost(cp, pseed.Cost, inputRows)
	nsGather := &Path{
		Kind: PathGather, Rel: grouped, Rows: inputRows, Cost: nsGatherCost,
		ParallelWorkers: workers,
		Children:        []*Path{pseed},
	}
	// STRATEGY. The gathered arm is `addGroupingPaths`' own body over a
	// PARALLEL input, and it has to offer the same shapes for the same
	// reasons — offering only the hashed one is how TPC-H Q16 flipped from
	// `GroupAggregate` to `HashAggregate` on a comparison that had no sorted
	// candidate in it at all.
	//
	// The one thing that cannot carry over is PRESORTEDNESS: a Gather
	// interleaves its workers' streams, so whatever order the input had is
	// gone above it. Both arms below are therefore built as if no usable keys
	// existed — hashed on `groupingHashable(agg, false)`, sorted over an
	// explicit `Sort`, which is exactly the shape the post-pass produces for
	// Q16 today (Sort above Gather, GroupAggregate above that).
	if len(aggNode.GroupExprs) == 0 && aggNode.GroupingSets == nil {
		// PLAIN: one candidate, priced by the hashed arm at 0 group columns
		// and 1 group — term-for-term PG's PLAIN arm, as C-15 does.
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: aggNode,
			Rel: grouped, Rows: 1,
			Cost: costAgg(cp, AggStrategyHashed, inputRows, nsGatherCost.Startup, nsGatherCost.Total,
				0, 1, nAggs, inNcols, inAvgVar),
			Children: []*Path{nsGather},
		}, partialAggNoSplitProducer)
		return split
	}
	if groupingHashable(aggNode, false) || aggNode.GroupingSets != nil {
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategyHashed, Agg: aggNode,
			Rel: grouped, Rows: finalGroups,
			DisabledNodes: disabledNodesFor(!ps.EnableHashAgg, nsGather),
			Cost: costAgg(cp, AggStrategyHashed, inputRows, nsGatherCost.Startup, nsGatherCost.Total,
				nGroupCols, finalGroups, nAggs, inNcols, inAvgVar),
			Children: []*Path{nsGather},
		}, partialAggNoSplitProducer)
	}
	if aggNode.GroupingSets == nil && !groupingHasSpecialAgg(aggNode) {
		sortedInput := sortPathForBounded(nsGather, pathkeysForSortKeys(groupKeysSortKeys(aggNode)), cp, -1)
		addPath(grouped, &Path{
			Kind: PathAgg, AggStrategy: AggStrategySorted, Agg: aggNode,
			Rel: grouped, Rows: finalGroups,
			Cost: costAgg(cp, AggStrategySorted, inputRows, sortedInput.Cost.Startup, sortedInput.Cost.Total,
				nGroupCols, finalGroups, nAggs, inNcols, inAvgVar),
			Pathkeys: sortedInput.Pathkeys, Children: []*Path{sortedInput},
		}, partialAggNoSplitProducer)
	}
	return split
}

// addPartialAggSplitArm files `Finalize -> Gather -> Partial` on the GROUP_AGG
// rel and returns the finalize path. Split out of its caller so the gathered
// no-split arm above stays reachable for an aggregate that cannot be split.
func addPartialAggSplitArm(grouped, partialRel *RelOptInfo, pseed *Path, aggNode *Aggregate,
	cp costParams, workers int, d, perWorkerRows, partialGroups, finalGroups float64,
	nGroupCols, nAggs, inNcols int, inAvgVar float64, strategy AggStrategy) *Path {
	crossedRows := partialGroups * d

	// PARTIAL arm — `create_agg_path(… AGGSPLIT_INITIAL_SERIAL …)`,
	// planner.c:7606.
	partialCost := costAgg(cp, strategy, perWorkerRows, pseed.Cost.Startup, pseed.Cost.Total,
		nGroupCols, partialGroups, nAggs, inNcols, inAvgVar)
	partialPath := &Path{
		Kind: PathAgg, AggStrategy: strategy, Agg: aggNode,
		Rel: partialRel, Rows: partialGroups, Cost: partialCost,
		ParallelSafe: true, ParallelWorkers: workers,
		Children: []*Path{pseed},
	}
	addPath(partialRel, partialPath, partialAggPartialProducer)

	// BOUNDARY — `cost_gather` (costsize.c:446) over the partial-agg path. The
	// row argument is `compute_gather_rows` of that path: the GROUP-STATES that
	// reach the leader, not input tuples. goopg's Partial node emits no rows at
	// all and merges each group into a mutex-guarded accumulator instead
	// (operators_join_agg.go:2351-2372); charging a group-state at
	// `parallel_tuple_cost` is C-19g's one deliberate adaptation and its whole
	// economic argument (TPC-H Q1: 16 group-states against 5.9 M tuples).
	gatherAbove := gatherCost(cp, partialCost, crossedRows)
	gatherPath := &Path{
		Kind: PathGather, Rel: grouped, Rows: crossedRows, Cost: gatherAbove,
		DisabledNodes:   partialPath.DisabledNodes,
		ParallelWorkers: workers,
		Children:        []*Path{partialPath},
	}

	// FINALIZE arm — `create_agg_path(… AGGSPLIT_FINAL_DESERIAL …)`,
	// planner.c:7250: the combine charged per INPUT row of the finalize node
	// and the final function per output group.
	split := &Path{
		Kind: PathFinalizeAgg, AggStrategy: strategy, Agg: aggNode,
		Rel: grouped, Rows: finalGroups,
		Cost: costAgg(cp, strategy, crossedRows, gatherAbove.Startup, gatherAbove.Total,
			nGroupCols, finalGroups, nAggs, inNcols, inAvgVar),
		ParallelWorkers: workers,
		Children:        []*Path{gatherPath},
	}
	addPath(grouped, split, partialAggSplitPathProducer)
	return split
}

// parallelSeedCost is the partial input's price: startup unchanged, RUN cost
// divided by the parallel divisor.
//
// This is `cost_seqscan`'s own parallel adjustment ("the CPU cost is divided
// among all the workers", costsize.c) applied to a whole subtree rather than to
// one scan, and it is an APPROXIMATION with a known direction: upstream leaves
// the per-page I/O term undivided because the workers share one relation, so
// dividing the entire run cost OVERSTATES the speedup of an I/O-bound subtree.
// It is stated here rather than hidden because it is the one quantity in this
// producer that is not transcribed from a PG cost function — the honest
// alternative would be a partial path per search rel, which is C-19f's surface
// and reaches only the join tree, not the aggregate above the seam.
func parallelSeedCost(serial Cost, d float64) Cost {
	if d <= 1 {
		return serial
	}
	run := serial.Total - serial.Startup
	if run < 0 {
		run = 0
	}
	return Cost{Startup: serial.Startup, Total: serial.Startup + run/d}
}

// upperSplitWorkers sizes the split's worker count.
//
// It is `compute_parallel_worker` (allpaths.c:4274) through the SAME entry the
// path model uses for a partial base-rel scan (`computeParallelWorkerForRel`,
// considerparallel.go), sized on `baseRelPages` — deliberately NOT the
// post-pass's `computeParallelWorkers`, which needs a live block count through
// `ParallelSettings.BlocksForTable` and returns 0 without one. There is no such
// function at the pre-cache planner; `rel->pages` is what the search itself
// sizes every partial path on, and using it here is what keeps one worker-count
// rule in the planner instead of two.
//
// The index-only arm mirrors `computeParallelWorkers`' own: an index-only scan
// never reads the heap, so the INDEX's page count is what bounds it
// (`create_index_paths` passes `index->pages`). Measured on TPC-H q13/q16
// (M0134-0189) and repeated here so the two sizings cannot disagree.
func upperSplitWorkers(child Node, cp costParams, ps PlannerSettings) int {
	scan := drivingScan(child)
	tbl := scanTable(scan)
	if tbl == nil {
		return 0
	}
	if tableIsUnsafeForParallel(tbl) {
		return 0
	}
	// The LIVE main-fork size, exactly as the post-pass reads it (`compute_
	// parallel_worker` takes `rel->pages`, which `estimate_rel_size` fills from
	// `RelationGetNumberOfBlocks()`). This is a size query, not a statistics
	// lookup, and the difference is load-bearing: `TableStats.RowCount` and
	// `Pages` are NOT restored at startup (ledger pq-P6), so a worker count
	// derived from them would refuse every query on a freshly started server —
	// the exact failure `parallelRelationBlocks`' comment records having been
	// bitten by once already. `baseRelPages` is the fallback for a table the
	// storage hook cannot answer for (unit fixtures above all).
	pages, ok := catalog.TableRealPages(tbl)
	if !ok || pages <= 0 {
		relTuples := float64(tableRows(tbl))
		if relTuples < 1 {
			relTuples = 1
		}
		pages = baseRelPages(tbl, relTuples)
	}
	if ios, isIOS := scan.(*IndexOnlyScan); isIOS && ios.Index != nil {
		if ipages, ok := catalog.IndexRealPages(ios.Index); ok && ipages > 0 {
			pages = ipages
		}
	}
	workers := computeParallelWorkerForRel(cp, pages, tableParallelWorkersReloption(tbl))
	if workers > ps.MaxParallelWorkersPerGather {
		workers = ps.MaxParallelWorkersPerGather
	}
	return workers
}
