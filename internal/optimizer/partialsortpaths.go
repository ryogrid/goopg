package optimizer

// partialsortpaths.go — Phase 5 slice C-19e / P5-05 (take3 08 §8):
// re-decide `Gather Merge -> Sort -> <partial>` BY COST instead of by
// `sortPartialRootPays`' hard-coded decline.
//
// WHAT IT REPLACES. `sortPartialRootPays` (parallel.go) answers the question
// "is it worth giving every worker its own Sort and the leader a k-way merge?"
// with a type switch over the driving scan: a seq scan says yes, an index or
// index-only scan says no. That decline was MEASURED (q16 1.5 -> 2.3 s,
// q13 4.2 -> 6.8 s at M0134-0189), and the rule's own doc comment states why it
// is a rule and not a cost comparison: "the honest shape of this rule is
// 'per-worker sort must be cheaper than the merge it forces', which needs a
// cost model goopg's parallel post-pass does not have".
//
// It has one now. C-19d landed `cost_gather` / `cost_gather_merge` as real
// functions, `costSortRun` has been the sort price since P5.5, and C-19g
// (partialaggpaths.go) established the shape of a post-pass verdict reached by
// a PATH TOURNAMENT rather than by a constant: build both candidates, file them
// on an upper rel, and let `addPath` / `setCheapest` adjudicate with the same
// comparator the search uses for everything else. This file is that pattern
// applied to the second of `MaybeAddGather`'s three predicates.
//
// The two candidates are the two plans the post-pass ACTUALLY builds, not
// hypotheticals:
//
//	worker-side   Gather Merge -> Sort -> <partial subtree>      (accept)
//	leader-side   Sort -> Gather -> <partial subtree>            (decline)
//
// Declining does not serialise anything — `findPartialSubtree` falls through
// `terminatesPartial(*Sort)` and lands the Gather one level lower — so the
// comparison is between two parallel plans that differ only in WHICH SIDE OF
// THE BOUNDARY the sort runs on. That is exactly the trade the rule's comment
// describes: the same comparison work either way, plus a merge stage, minus
// whatever the divided sort saves.
//
// NO NEW CONSTANT. Every term comes from an existing PG-faithful function —
// `costSortRun` (cost_sort), `gatherCost` (cost_gather), `gatherMergeCost`
// (cost_gather_merge), `getParallelDivisor` (get_parallel_divisor). The rule
// this replaces had one hard-coded type switch; the replacement has none.
//
// SHARED INPUT SEED. Both candidates stand on the same partial subtree, and
// both cost chains are strictly additive in it (`costSortRun` is added to the
// input's total; `gatherCost`/`gatherMergeCost` add to `sub.Total`), so the
// input's absolute price appears exactly once in each arm and CANCELS in the
// comparison. This is what lets a post-pass site with no partial input path of
// its own decide honestly — the same argument partialaggpaths.go §3.1 makes,
// and the reason a `legacyDisplayCostOf` seed is adequate here.
//
// Ships behind `GOOPG_PARTIAL_SORT_PATHS`, default `off`, which delegates to
// the retired rule unchanged — so the serial control arm and the default arm
// are bit-identical to C-19g's behaviour, and the measurement decides the flip.

import (
	"os"
	"strings"
)

// partialSortMode is the admission rule for this file's verdict.
//
//   - off — the retired type switch (`sortPartialRootPays`) decides, unchanged.
//   - on  — the priced path tournament decides.
type partialSortMode int

const (
	partialSortPathsOff partialSortMode = iota
	partialSortPathsOn
)

// partialSortPathsMode is read once at process start, like every other
// plan-shaping knob in this package, so a plan cannot change shape
// mid-statement.
var partialSortPathsMode = partialSortModeFromEnv(os.Getenv("GOOPG_PARTIAL_SORT_PATHS"))

// partialSortModeFromEnv resolves the knob. Anything unrecognised is `off`:
// fail-closed, so a typo cannot silently enable a plan shape whose measurement
// has not been run. Same shape as `partialAggModeFromEnv`.
func partialSortModeFromEnv(v string) partialSortMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "paths", "true", "1":
		return partialSortPathsOn
	default:
		return partialSortPathsOff
	}
}

// partialSortModeLabel spells the mode the way an operator would export it, so
// the flag-provenance label round-trips (flaglabels.go's contract).
func partialSortModeLabel(m partialSortMode) string {
	if m == partialSortPathsOn {
		return "on"
	}
	return "off"
}

// setPartialSortPathsModeForTest pins the mode for one test and returns the
// restore func. The knob is process-global by design, so a test that flips it
// must put it back.
func setPartialSortPathsModeForTest(m partialSortMode) func() {
	prev := partialSortPathsMode
	partialSortPathsMode = m
	return func() { partialSortPathsMode = prev }
}

// SetPartialSortPathsMode is the same hook across the package boundary, taking
// the label an operator would export (`off` / `on`) and resolving it through
// the SAME function production resolves the environment variable with — so a
// caller cannot select a mode the env knob could not. Mirrors
// `SetPartialAggPathsMode` / `SetGatherPathsMode`; process-global, so a caller
// must run the returned restore.
func SetPartialSortPathsMode(label string) (restore func()) {
	return setPartialSortPathsModeForTest(partialSortModeFromEnv(label))
}

// Producer strings for the DPPATH trace (pathtrace.go), in the convention
// C-12 fixed: `producer=upper.partialsort.*`.
const (
	partialSortWorkerProducer = "upper.partialsort.gathermerge"
	partialSortLeaderProducer = "upper.partialsort.leadersort"
)

// partialSortTournament is the outcome of one comparison, kept as a struct so
// a test can assert on the two candidates rather than only on the boolean.
type partialSortTournament struct {
	ordered *RelOptInfo

	// workerSide is `Gather Merge -> Sort -> partial`; leaderSide is
	// `Sort -> Gather -> partial`.
	workerSide *Path
	leaderSide *Path

	workerFiled bool
	leaderFiled bool

	divisor       float64
	inputRows     float64
	perWorkerRows float64
}

// workerSideWins reports the verdict: whether the post-pass should take the
// Sort as the partial root.
//
// The question asked of the pathlist is "is the worker-side candidate the
// rel's cheapest?", not "did it survive?" — `addPath` can keep both when
// neither dominates on both axes, and the post-pass has to return ONE answer.
// `setCheapest`'s `CheapestTotal` is the total-cost winner, which is the axis
// the plan is selected on here (there is no LIMIT context at this site — the
// Sort the post-pass targets carries no bound; see `sortPartialRootPays`'
// "goopg's Sort carries no top-N limit").
func (t *partialSortTournament) workerSideWins() bool {
	if t == nil || t.ordered == nil || t.workerSide == nil {
		return false
	}
	return t.ordered.CheapestTotal == t.workerSide
}

// partialSortRootPays is C-19e's entry point, and the one `findPartialSubtree`
// calls. It has `sortPartialRootPays`' meaning and `partialAggSplitPays`'
// shape.
//
// `workers` is the count the post-pass would size this subtree at, computed by
// the caller from the SORT'S CHILD for the same reason the aggregate arm sizes
// from `agg.Child`: the divisor that prices the candidates has to be the one
// the built plan will actually run at.
func partialSortRootPays(srt *Sort, workers int, leaderParticipates bool) bool {
	if partialSortPathsMode == partialSortPathsOff {
		return sortPartialRootPays(srt)
	}
	// DefaultPlannerSettings, not the session's — identical reasoning to
	// `partialAggSplitPays`: `MaybeAddGather` carries `ParallelSettings` and
	// runs post-cache, so no `PlannerSettings` is in scope. PG's GUC DEFAULTS
	// through the named `costParams` fields is strictly better than the type
	// switch it replaces, and the residual gap (a session's `cpu_operator_cost`
	// never reaching the post-pass) is the one `costParams.workMem`'s comment
	// already records, ledger M0127-P5.7-a.
	t := createPartialSortPaths(srt, workers, leaderParticipates, DefaultPlannerSettings().costParams())
	if t == nil {
		// No verdict reachable (no worker budget, or no usable input estimate).
		// Fall back to the rule rather than inventing an answer: a tournament
		// that cannot be run is not evidence for either side.
		return sortPartialRootPays(srt)
	}
	return t.workerSideWins()
}

// createPartialSortPaths builds the two candidates and files them on an
// `UpperOrdered` rel. nil when the comparison cannot be priced.
func createPartialSortPaths(srt *Sort, workers int, leaderParticipates bool, cp costParams) *partialSortTournament {
	if srt == nil || srt.Child == nil || workers <= 0 {
		return nil
	}
	d := getParallelDivisor(workers, leaderParticipates)
	if d <= 0 {
		return nil
	}

	// The input row count. `legacyDisplayCostOf` is the same accessor the
	// aggregate tournament reads its seed from: the search's own numbers when
	// the node came from the path model, the legacy derivation otherwise.
	child := srt.Child
	pc := legacyDisplayCostOf(child)
	inputRows := pc.PlanRows
	if inputRows < 1 {
		inputRows = 1
	}
	perWorkerRows := inputRows / d
	if perWorkerRows < 1 {
		perWorkerRows = 1
	}

	u := newUpperRels()
	ordered := fetchUpperRel(u, UpperOrdered, 0, 0)
	ordered.Rows = inputRows
	ordered.NCols, ordered.AvgVarBytes = aggInputWidth(child)
	ordered.Width = tupleWidth(child.Output())
	// The caller has already established that the subtree is parallel-capable
	// (`drivingScan(srt.Child) != nil`), which is what this stands for at the
	// post-pass's scope — the same stand-in `partialRel.ConsiderParallel` makes
	// in the aggregate tournament.
	ordered.ConsiderParallel = true

	// The SHARED input seed: one partial subtree, priced once, entering both
	// arms additively (see the file header).
	seed := newPrebuiltPath(ordered, child)
	seed.Rows = perWorkerRows
	if pc.PlanRows > 0 || pc.TotalCost > 0 {
		seed.Cost = Cost{Startup: pc.StartupCost, Total: pc.TotalCost}
	}
	seed.ParallelSafe = true
	seed.ParallelWorkers = workers

	ncols, avgVar := ordered.NCols, ordered.AvgVarBytes

	// ── the WORKER-SIDE arm: create_sort_path over the partial path
	// (pathnode.c:3065) then create_gather_merge_path (pathnode.c:2020).
	//
	// The sort is charged on PER-WORKER rows, which is the whole saving: N
	// sorts of R/N rows cost N * (R/N)log(R/N) against one of R log R, i.e.
	// they save exactly the log N factor and nothing else. `cost_sort` charges
	// the comparisons as STARTUP on top of the input's total, which is why the
	// two arms are assembled the same way `sortPathForBounded` assembles one.
	wSort := costSortRun(cp, perWorkerRows, ncols, avgVar, -1)
	wSortCost := Cost{Startup: seed.Cost.Total + wSort.Startup, Total: seed.Cost.Total + wSort.Total}
	// `compute_gather_rows` of the sort path: the per-worker count multiplied
	// by the divisor it was priced with. Every input row crosses the boundary
	// here — unlike the aggregate tournament, a Sort emits what it reads, which
	// is precisely why this arm is the harder sell.
	crossedRows := perWorkerRows * d
	gmCost := gatherMergeCost(cp, wSortCost, workers, crossedRows)
	sortPartial := &Path{
		Kind: PathSort, Rel: ordered, Rows: perWorkerRows, Cost: wSortCost,
		DisabledNodes:   disabledNodesFor(!cp.enableSort, seed),
		ParallelSafe:    true,
		ParallelWorkers: workers,
		Pathkeys:        nil,
		Children:        []*Path{seed},
	}
	workerSide := &Path{
		Kind: PathGatherMerge, Rel: ordered, Rows: crossedRows, Cost: gmCost,
		// `input_disabled_nodes + (enable_gathermerge ? 0 : 1)`
		// (costsize.c:535) — the same term makeGatherMergePath charges.
		DisabledNodes: sortPartial.DisabledNodes + disabledNodesFor(!cp.enableGatherMerge),
		Children:      []*Path{sortPartial},
	}
	addPath(ordered, workerSide, partialSortWorkerProducer)

	// ── the LEADER-SIDE arm: cost_gather over the partial subtree, then one
	// full sort in the leader. Not a hypothetical — it is exactly what
	// `findPartialSubtree` builds when the verdict is "decline", so the
	// comparison is against the plan that really gets built.
	gCost := gatherCost(cp, seed.Cost, inputRows)
	lSort := costSortRun(cp, inputRows, ncols, avgVar, -1)
	gather := &Path{
		Kind: PathGather, Rel: ordered, Rows: inputRows, Cost: gCost,
		DisabledNodes: seed.DisabledNodes,
		Children:      []*Path{seed},
	}
	leaderSide := &Path{
		Kind: PathSort, Rel: ordered, Rows: inputRows,
		Cost:          Cost{Startup: gCost.Total + lSort.Startup, Total: gCost.Total + lSort.Total},
		DisabledNodes: disabledNodesFor(!cp.enableSort, gather),
		Children:      []*Path{gather},
	}
	addPath(ordered, leaderSide, partialSortLeaderProducer)

	setCheapest(ordered)

	return &partialSortTournament{
		ordered:       ordered,
		workerSide:    workerSide,
		leaderSide:    leaderSide,
		workerFiled:   pathIsFiled(ordered, workerSide),
		leaderFiled:   pathIsFiled(ordered, leaderSide),
		divisor:       d,
		inputRows:     inputRows,
		perWorkerRows: perWorkerRows,
	}
}
