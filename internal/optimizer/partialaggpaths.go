package optimizer

// partialaggpaths.go — Phase 5 slice C-19g / P5-07 (take3 08 §8):
// `create_partial_grouping_paths` (planner.c:7351) at the one site goopg can
// legally take the decision — the post-cache parallel pass.
//
// WHAT THIS REPLACES. `splitAggregateIsProfitable` (parallel_agg.go) decides
// split-vs-no-split with five constants — cXfer 2.0, cTrans 1.0, cHash 0.25,
// cMerge 4.0, cOut 1.0 — that its own comment calls "calibrated against one
// query". None is a PG cost constant and none is reachable from a GUC. Worse,
// its ratio input (`groupsToRowsRatio` -> `aggColumnStats`) refuses to descend
// through a Join or a Project, so a join-fed aggregate — most of TPC-H — is
// refused outright, and refusal drops the Gather BELOW the aggregate: the whole
// join output funnels into one leader-side aggregate, which is exactly the
// serial tail the split exists to remove.
//
// This file replaces that verdict with a two-candidate PATH tournament priced
// by `costAgg` + `gatherCost` and adjudicated by `addPath` / `setCheapest` —
// the same comparator, with the same fuzz band, that decides everything else in
// the planner. It adds no cost function and no constant.
//
// WHY IT IS NOT A PORT INSIDE C-15's PRODUCER. Upstream builds
// `partially_grouped_rel` inside `add_paths_to_grouping_rel` (planner.c:4092)
// from `input_rel->partial_pathlist` (planner.c:7386). goopg cannot, for two
// independent reasons, both load-bearing:
//
//  1. No input rel. C-15's `createGroupingPaths` receives a finished
//     `*Aggregate` NODE; the rel carrying `PartialPathlist` dies inside
//     `planJoinlistSearch` before the aggregate stage runs.
//  2. The parallel decision may not be CACHED. `MaybeAddGather` runs after the
//     plan-cache lookup and `internal/postmaster/dispatch.go:1480-1490` says
//     why: the cache is process-wide, keyed on (dbOid, normalised SQL) with no
//     GUC fingerprint, so a plan built under max_parallel_workers_per_gather=4
//     and cached would be served to a session that set it to 0. C-15's producer
//     runs PRE-cache.
//
// Design: docs/design/planner-c19g-partial-agg/DESIGN.md, §2.2 for the above
// and §8 for the remainder (the upper-rel-resident port, which needs a change
// in `groupingpaths.go` this slice does not own).

import (
	"os"
	"strings"
)

// partialAggMode is the admission rule for this file's verdict.
//
//   - off — the retired size rule decides, unchanged. This is the serial
//     control arm: the search and the post-pass behave exactly as at C-19f.
//   - on  — the priced path tournament decides.
type partialAggMode int

const (
	partialAggPathsOff partialAggMode = iota
	partialAggPathsOn
)

// partialAggPathsMode is read once at process start, like every other
// plan-shaping knob in this package, so a plan cannot change shape
// mid-statement.
var partialAggPathsMode = partialAggModeFromEnv(os.Getenv("GOOPG_PARTIAL_AGG_PATHS"))

// partialAggModeFromEnv resolves the knob. Anything unrecognised is `off`:
// fail-closed, so a typo cannot silently enable a plan shape whose measurement
// has not been run. Same shape as `gatherPathModeFromEnv`.
func partialAggModeFromEnv(v string) partialAggMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "paths", "true", "1":
		return partialAggPathsOn
	default:
		return partialAggPathsOff
	}
}

// partialAggModeLabel spells the mode the way an operator would export it, so
// the flag-provenance label round-trips (flaglabels.go's contract).
func partialAggModeLabel(m partialAggMode) string {
	if m == partialAggPathsOn {
		return "on"
	}
	return "off"
}

// setPartialAggPathsModeForTest pins the mode for one test and returns the
// restore func. The knob is process-global by design, so a test that flips it
// must put it back.
func setPartialAggPathsModeForTest(m partialAggMode) func() {
	prev := partialAggPathsMode
	partialAggPathsMode = m
	return func() { partialAggPathsMode = prev }
}

// SetPartialAggPathsMode is the same hook across the package boundary, taking
// the label an operator would export (`off` / `on`) and resolving it through
// the SAME function production resolves the environment variable with — so a
// caller cannot select a mode the env knob could not.
//
// It exists for this item's EXECUTOR CONSUMER CHECK: the gate requires a
// fixture where the split verdict is reached BY COST to actually execute as
// Finalize -> Gather -> Partial and return the serial plan's values, and that
// test lives in internal/executor, which cannot reach an unexported knob.
// `SetGatherPathsMode` (gatherpaths.go) and `SetParallelEnabled` (parallel.go)
// are the same shape. Like them, this is process-global, so a caller must run
// the returned restore.
func SetPartialAggPathsMode(label string) (restore func()) {
	return setPartialAggPathsModeForTest(partialAggModeFromEnv(label))
}

// Producer strings for the DPPATH trace (pathtrace.go). With `Relids = 0` the
// lines read `producer=upper.partialgroupagg.* relids=-`, the convention C-12
// established and C-15 followed.
const (
	partialAggPartialProducer = "upper.partialgroupagg.partial"
	partialAggSplitProducer   = "upper.groupagg.finalize"
	partialAggNoSplitProducer = "upper.groupagg.gathered"
)

// partialAggNotionalRows is the input row count used when the real one is
// unavailable. It is a NORMALISATION, not an estimate: the comparison is
// homogeneous of degree one in the row count (see createPartialGroupingPaths),
// so this scales both candidates' prices and cannot move the verdict.
// TestPartialAggVerdictIsScaleFree pins that. The value is large enough that
// the per-group terms do not disappear into float rounding against the
// per-row ones, and small enough that no product overflows.
const partialAggNotionalRows = 1e6

// partialAggTournament is the two-candidate contest, returned whole so a test
// can assert that BOTH candidates were generated before it reads which won.
//
// That is not defensive tidiness: five hypotheses were burned on Q8 because a
// producer emitted nothing at that parameterisation and the cost comparison was
// therefore vacuous.
//
// `splitFiled` / `noSplitFiled` are read out of the rel's pathlist AFTER both
// additions, so they report survival of `addToPathlist`'s dominance pruning,
// not generation. Both candidates carry the same `Rows` and no pathkeys, so the
// dearer one is normally evicted — exactly one of these is normally true, and
// the winner's must be. Generation is the non-nil `split` / `noSplit` pointer,
// which is what a "were both candidates produced" assertion reads.
type partialAggTournament struct {
	grouped *RelOptInfo
	partial *RelOptInfo

	split   *Path // Finalize over Gather over Partial
	noSplit *Path // Simple aggregate over Gather over the whole input

	splitFiled   bool
	noSplitFiled bool

	// The estimates the verdict turns on, exposed for tests and for the
	// arithmetic in DESIGN §3.4.
	divisor       float64
	inputRows     float64
	finalGroups   float64
	partialGroups float64
	crossedRows   float64 // partialGroups * divisor — the group-states that cross
}

// splitWins reports the tournament's verdict: the cheapest path on the
// GROUP_AGG rel is the finalize-over-gather-over-partial candidate.
//
// Read off `CheapestTotal` rather than by comparing `Cost.Total` directly, so
// the verdict is `compare_path_costs_fuzzily`'s — including its 1% fuzz band —
// and not a second, subtly different comparator.
func (t *partialAggTournament) splitWins() bool {
	if t == nil || t.grouped == nil || t.split == nil {
		return false
	}
	return t.grouped.CheapestTotal == t.split
}

// createPartialGroupingPaths is `create_partial_grouping_paths` (planner.c:7351)
// plus the `gather_grouping_paths` (planner.c:7704) and AGGSPLIT_FINAL_DESERIAL
// (planner.c:7250) steps that consume it, collapsed into one producer because
// goopg has exactly one input path rather than a pathlist to iterate.
//
// nil when no tournament is possible — a non-decomposable aggregate, a
// non-positive worker count, or a divisor that cannot beat serial. A nil result
// means "no split", the same fail-closed direction the retired rule took.
func createPartialGroupingPaths(agg *Aggregate, workers int, leaderParticipates bool, cp costParams) *partialAggTournament {
	// The decomposability gate runs FIRST and is re-asserted here even though
	// findPartialSubtree already checked it. considerparallel.go is fail-CLOSED
	// by hard-won design (a review found four fail-open holes), and a producer
	// that trusts its caller's gate is one refactor away from being a fifth.
	// An aggregate that is not decomposable is never priced, let alone split.
	if agg == nil || workers <= 0 || !aggregateSplitIsSafe(agg) {
		return nil
	}
	// `get_parallel_divisor` (costsize.c:6474) via cost_funcs.go:175 — the same
	// function every partial path in this package is priced through, so the
	// per-worker row count here and the one `costParallelSeqscan` uses cannot
	// disagree.
	d := getParallelDivisor(workers, leaderParticipates)
	if d <= 1 {
		return nil
	}

	child := agg.Child
	if child == nil {
		return nil
	}

	// ── THE ROW COUNT, AND WHY IT IS NORMALISED ────────────────────────────
	//
	// The comparison this producer performs is HOMOGENEOUS of degree one in the
	// input row count. Writing R for input rows, Gp = rho*R for the group-states
	// that cross the boundary and Gw = rho*R/d for one worker's groups, the
	// difference between the two candidates' totals is
	//
	//	R * [ ptc*(1-rho) + coc*(A+K)*(1-1/d)
	//	      - (coc*A + ctc)*rho/d - coc*(A+K)*rho ]
	//
	// — the shared input price cancels (DESIGN §3.1), `parallel_setup_cost`
	// cancels (both shapes place exactly one Gather), and every surviving term
	// carries exactly one factor of R. So R scales both candidates and CANNOT
	// change the verdict; only the reduction ratio `rho`, the divisor, the
	// aggregate count and the group-column count can.
	//
	// That matters because goopg's post-pass is frequently BLIND to absolute
	// row counts: `TableStats.RowCount` is not restored at startup even though
	// the column statistics are (internal/initdb/open.go; deferral ledger
	// pq-P6), and it is 0 on a freshly ANALYZEd fixture too. A model that
	// needed absolute rows would refuse every split on such a server — including
	// TPC-H Q1's, the one the split was built for. The retired size rule was
	// designed around exactly this ("the model needs no absolute quantities at
	// all"); this one inherits the property instead of losing it, while keeping
	// PG's real cost constants.
	//
	// So: R is the real estimate when there is one, and a NORMALISATION
	// otherwise. The absolute prices on the resulting paths are then notional,
	// and the comparison between them is not. `TestPartialAggVerdictIsScaleFree`
	// pins the homogeneity that makes this legitimate.
	inputRows := float64(EstimateRows(child))
	if inputRows <= 0 {
		if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 {
			inputRows = float64(pc.PlanRows)
		}
	}
	rowsAreReal := inputRows > 0
	if !rowsAreReal {
		inputRows = partialAggNotionalRows
	}
	perWorkerRows := inputRows / d

	// ── THE REDUCTION RATIO ────────────────────────────────────────────────
	//
	// `rho = Gp/R`, the single quantity the verdict turns on. Two sources, in
	// order of reach:
	//
	//  1. With a real row count, `estimateNumGroups` (cardinality.go:1202 —
	//     PG's `estimate_num_groups`, the estimator C-15's GROUP_AGG rel is
	//     sized by) over the PER-WORKER row count, which is upstream's
	//     `dNumPartialPartialGroups = get_number_of_groups(root,
	//     cheapest_partial_path->rows, …)` (planner.c:7452). It never refuses:
	//     it decomposes expressions and falls back to defaultNumDistinct.
	//  2. Blind, the distinct-to-rows FRACTION from `groupsToRowsRatio` —
	//     `NDistinctFrac`, which ANALYZE does restore. It refuses when the group
	//     keys sit above a Join or a Project (`aggColumnStats` declines to
	//     descend through either), and a refusal here means NO SPLIT, which is
	//     exactly today's behaviour for those shapes. Widening it needs either
	//     the restored row count or join-level distinct estimates; neither is
	//     this slice's.
	var partialGroups float64
	if rowsAreReal {
		partialGroups = float64(estimateNumGroups(agg.GroupExprs, child, int64(perWorkerRows)))
	} else {
		rho, ok := groupsToRowsRatio(agg, d)
		if !ok {
			return nil
		}
		partialGroups = rho * inputRows / d
	}
	// A worker cannot produce more groups than it reads rows. Upstream gets this
	// from clamp_row_est over the path's own rows; here it is explicit because
	// the two estimates are taken independently.
	if perWorkerRows >= 1 && partialGroups > perWorkerRows {
		partialGroups = perWorkerRows
	}
	if partialGroups < 1 {
		partialGroups = 1
	}

	u := newUpperRels()
	grouped := fetchUpperRel(u, UpperGroupAgg, 0, 0)
	// C-15's sizing, reused rather than restated: `Rows` is the PG-faithful
	// `estimateNumGroups` and Width/NCols/AvgVarBytes describe the aggregate
	// OUTPUT. `Rows` is upstream's `dNumGroups`, and it enters BOTH candidates
	// through identical `costAgg` terms (`finalPerGroup*groups +
	// cpuTupleCost*groups`), so it cancels in the comparison exactly as the
	// input price does — which is why a blind `Rows` of 1 is harmless here.
	sizeGroupingRelFromAgg(grouped, agg)
	finalGroups := grouped.Rows
	if finalGroups < 1 {
		finalGroups = 1
	}

	partialRel := fetchUpperRel(u, UpperPartialGroupAgg, 0, 0)
	partialRel.Rows = partialGroups
	partialRel.Width, partialRel.NCols, partialRel.AvgVarBytes = grouped.Width, grouped.NCols, grouped.AvgVarBytes
	// `partially_grouped_rel->consider_parallel = grouped_rel->consider_parallel`
	// (planner.c:7405). The caller has already established that the subtree is
	// parallel-capable (drivingScan != nil), which is what this stands for at
	// the post-pass's scope.
	partialRel.ConsiderParallel = true

	// The SHARED input seed. Both candidates stand on the same partial input
	// path — the same scan/join subtree run by the same workers — and both cost
	// functions are strictly additive in it (`costAgg`: `inputTotal + …`;
	// `gatherCost`: `sub.Total + …`), so its absolute price appears exactly
	// once in each candidate and CANCELS in the comparison. That is why a
	// post-pass site with no partial input path of its own can still decide
	// this honestly: DESIGN §3.1.
	seed := newPrebuiltPath(partialRel, child)
	seed.Rows = perWorkerRows
	if pc := legacyDisplayCostOf(child); pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}
	seed.ParallelSafe = true
	seed.ParallelWorkers = workers

	nAggs := len(agg.Aggs)
	nGroupCols := len(agg.GroupExprs)
	inNcols, inAvgVar := aggInputWidth(child)
	strategy := agg.Strategy

	t := &partialAggTournament{
		grouped: grouped, partial: partialRel,
		divisor: d, inputRows: inputRows,
		finalGroups: finalGroups, partialGroups: partialGroups,
		crossedRows: partialGroups * d,
	}

	// ── the PARTIAL arm: create_agg_path(… AGGSPLIT_INITIAL_SERIAL …),
	// planner.c:7606. Per-worker rows in, per-worker groups out.
	partialCost := costAgg(cp, strategy, perWorkerRows, seed.Cost.Startup, seed.Cost.Total,
		nGroupCols, partialGroups, nAggs, inNcols, inAvgVar)
	partialPath := &Path{
		Kind: PathAgg, AggStrategy: strategy, Agg: agg,
		Rel: partialRel, Rows: partialGroups, Cost: partialCost,
		ParallelSafe: true, ParallelWorkers: workers,
		Children: []*Path{seed},
	}
	addPath(partialRel, partialPath, partialAggPartialProducer)

	// ── the BOUNDARY: cost_gather (costsize.c:446) over the partial-agg path.
	//
	// The output-row argument is `compute_gather_rows` of that path — the number
	// of GROUP-STATES that reach the leader, not input tuples. In PG those are
	// tuples on a shm_mq; in goopg the Partial node "emits NOTHING" and merges
	// each group into a mutex-guarded accumulator instead
	// (internal/executor/operators_join_agg.go:2351-2372). Charging both at
	// `parallel_tuple_cost` is this slice's one deliberate adaptation, and it is
	// the faithful one: that GUC prices transferring ONE tuple from a worker to
	// the leader, and goopg transfers one group-state where PG transfers one
	// tuple. It is also the conservative direction — a mutex-guarded map merge
	// is not obviously cheaper than a queue write.
	//
	// This term is the whole economic argument of C-19g. On TPC-H Q1 it is 16
	// group-states against 5.9 M tuples for the no-split arm below.
	gatherAbove := gatherCost(cp, partialCost, t.crossedRows)
	gatherPath := &Path{
		Kind: PathGather, Rel: grouped, Rows: t.crossedRows, Cost: gatherAbove,
		DisabledNodes: partialPath.DisabledNodes,
		Children:      []*Path{partialPath},
	}

	// ── the FINALIZE arm: create_agg_path(… AGGSPLIT_FINAL_DESERIAL …),
	// planner.c:7250. Upstream charges the combine per INPUT row of the
	// finalize node and the final function per output group. goopg's combines
	// happen inside `pub.merge` rather than in the leader's Agg loop, but the
	// work is the same per group-state, so the arm is priced unchanged —
	// pricing it where PG prices it keeps ONE cost model rather than two halves.
	t.split = &Path{
		Kind: PathAgg, AggStrategy: strategy, Agg: agg,
		Rel: grouped, Rows: finalGroups,
		Cost: costAgg(cp, strategy, t.crossedRows, gatherAbove.Startup, gatherAbove.Total,
			nGroupCols, finalGroups, nAggs, inNcols, inAvgVar),
		Children: []*Path{gatherPath},
	}
	addPath(grouped, t.split, partialAggSplitProducer)
	t.splitFiled = pathIsFiled(grouped, t.split)

	// ── the NO-SPLIT arm. Not a hypothetical: refusing the split is exactly
	// what `findPartialSubtree` does — the walk falls through to
	// `terminatesPartial(*Aggregate)` and the Gather lands BELOW the aggregate.
	// So the comparison is against the plan that is really built, and the whole
	// relation crosses the boundary (C-19d DESIGN §5.1's inequality, here as a
	// competing path rather than as prose).
	nsGatherCost := gatherCost(cp, seed.Cost, inputRows)
	nsGather := &Path{
		Kind: PathGather, Rel: grouped, Rows: inputRows, Cost: nsGatherCost,
		Children: []*Path{seed},
	}
	t.noSplit = &Path{
		Kind: PathAgg, AggStrategy: strategy, Agg: agg,
		Rel: grouped, Rows: finalGroups,
		Cost: costAgg(cp, strategy, inputRows, nsGatherCost.Startup, nsGatherCost.Total,
			nGroupCols, finalGroups, nAggs, inNcols, inAvgVar),
		Children: []*Path{nsGather},
	}
	addPath(grouped, t.noSplit, partialAggNoSplitProducer)
	t.noSplitFiled = pathIsFiled(grouped, t.noSplit)

	setCheapest(grouped)
	return t
}

// pathIsFiled reports whether a path survived `addToPathlist`'s dominance
// pruning and is on the rel's pathlist. It scans rather than checking the tail,
// because the caller asks the question after LATER paths have been added.
func pathIsFiled(rel *RelOptInfo, p *Path) bool {
	if rel == nil || p == nil {
		return false
	}
	for _, q := range rel.Pathlist {
		if q == p {
			return true
		}
	}
	return false
}

// partialAggSplitPays is the verdict `findPartialSubtree` consumes, and the
// one-line replacement for `splitAggregateIsProfitable`.
//
// Mode `off` (the default until the measurement in DESIGN §6 says otherwise)
// delegates to the retired size rule unchanged, so the serial control arm is
// bit-identical to C-19f's behaviour.
//
// It returns only a boolean and constructs no node. `splitAggregate`
// (parallel.go) still BUILDS the Finalize -> Gather -> Partial shape, with the
// shallow copies the process-wide plan cache requires. That single construction
// site is also the whole answer to "how is double-splitting prevented": nothing
// else builds a split, and C-19d's `subtreeHasGather` stand-down still stops the
// post-pass on any tree that already carries a costed Gather.
func partialAggSplitPays(agg *Aggregate, workers int, leaderParticipates bool) bool {
	if partialAggPathsMode == partialAggPathsOff {
		return splitAggregateIsProfitable(agg, workers, leaderParticipates)
	}
	// DefaultPlannerSettings, not the session's: `MaybeAddGather` carries
	// `ParallelSettings` and runs post-cache, so no PlannerSettings is in
	// scope. This is strictly better than what it replaces — PG's GUC DEFAULTS
	// through the named `costParams` fields, instead of five hardcoded non-GUC
	// constants — and the residual gap (session cpu_operator_cost etc. never
	// reaching the post-pass) is the same one `costParams.workMem`'s comment
	// records, ledger M0127-P5.7-a. C-19h closes it by wiring the parallel
	// block into `plannerSettingsFrom`.
	t := createPartialGroupingPaths(agg, workers, leaderParticipates, DefaultPlannerSettings().costParams())
	return t.splitWins()
}
