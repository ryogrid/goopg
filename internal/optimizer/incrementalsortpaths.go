package optimizer

// incrementalsortpaths.go — M0141-S2b-2c / S7's `addOrderedPaths` THIRD arm
// (planner.c's own `create_ordered_paths` loops every surviving candidate of
// the input rel, not just the cheapest-total one; upperordered.go's first two
// arms still handle only the single seed). Landed once both halves of the
// prerequisite chain existed: S2b-2a/2b (upperordered.go, upperorderedinput.go)
// publish and validate the search's full `Pathlist` onto the ORDERED rel, and
// S7's own groundwork (`pathkeysCountContainedIn`, pathkeys.go;
// `costIncrementalSort`, cost_funcs.go) gives a per-candidate prefix credit a
// Sort's flat cost cannot express. S2b-2's own recon
// (m0141-s2b-scoping-decomposition.md §"S2b-2 result") proved analytically
// that offering the full Pathlist WITHOUT that credit cannot move a single
// plan — this file is what makes the offer non-trivial.
//
// WHY GATED OFF BY DEFAULT. `createPlanNode` (createplan.go) and the executor
// (`incrementalSortOp`) both got their arms in exec-a/b (M0141-S7), so a
// winning tournament no longer panics. The flag stays off anyway: the
// 2026-09-17h corpus measurement ran the full TPC-DS SF0.25 corpus (99
// queries, including all 14 Incremental-Sort witnesses) with
// `GOOPG_INCREMENTAL_SORT=on` and found ZERO Incremental Sort nodes anywhere
// — this arm reads `ordered.SearchCandidates`/`SearchCandidateKeys`, and
// those are populated only inside `createOrderedPaths`
// (upperordered.go:106-118); `electOrderedGrouping`
// (upperorderedgrouping.go), the GROUP_AGG upper rel's own ORDER BY election
// loop, calls `addOrderedPaths` directly without going through
// `createOrderedPaths` first, so every GroupAgg-shaped witness (5 of the 14)
// structurally cannot reach this arm regardless of how far S2b-5/S2b-6 take
// the `anyTranslated` gate. See the design doc's 2026-09-17h update for the
// full writeup and the follow-up this filed. Matches the off-by-default
// convention every other experimental path family in this package already
// uses (`GOOPG_PARTIAL_SORT_PATHS`, `GOOPG_PARTIAL_AGG_PATHS`, …). With the
// flag at its default the ORDERED tournament is byte-identical to before
// this file existed: `addIncrementalSortPaths` returns immediately.
//
// CHILDREN ARE ALREADY SAFE TO MATERIALIZE. The candidates this arm stacks a
// PathIncrementalSort over come from `ordered.SearchCandidates` — real
// `*Path` entries the search itself built (PathSeqScan / PathIndexScan /
// PathHashJoin / PathMergeJoin / PathNestLoop / …), every one of which already
// has a `createPlanNode` arm because the search materializes exactly these
// kinds when ITS OWN tournament picks one. Only the new wrapper kind is
// unhandled; nothing about this arm asks `createPlanNode` to translate a
// candidate it could not already translate on its own. Path construction here
// is also lazy by S2b-2b's own contract: building a `*Path` allocates no
// executor Node, so offering N-1 losing candidates costs nothing beyond the
// struct itself, and `createPlanNode` runs exactly once, on whichever path
// `setCheapest` picks — unchanged from every other upper rel in this package.
//
// GUC NOTE. PG prices this decision under its own `enable_incremental_sort`
// GUC (catalog.go / defaults.go already declare it, default on, but nothing
// in costParams reads it — the same "declared but unconsumed" shape the
// project has hit before). This arm reuses `cp.enableSort` instead of adding a
// second unconsumed-until-wired GUC field: an Incremental Sort is still a
// sort, and `enable_sort=off` already disables `sortPathForBounded`'s
// full-sort arm the same way. Wiring the dedicated GUC is deferred (ledger
// row `m0141-s7-incremental-sort-guc`), because it is orthogonal to whether
// the arm exists at all and the flag gate already keeps this inert.

import (
	"os"
	"strings"
)

// incrementalSortMode is the admission rule for the third arm.
//
//   - off — addOrderedPaths behaves exactly as before this file (arms 1/2
//     only). This is the default: no plan can change.
//   - on — every OTHER surviving search candidate whose own ordering shares a
//     genuine partial prefix with the required sort keys is also offered, as
//     an Incremental Sort, to the ORDERED rel's tournament.
type incrementalSortMode int

const (
	incrementalSortOff incrementalSortMode = iota
	incrementalSortOn
)

// incrementalSortPathsMode is read once at process start, like every other
// plan-shaping knob in this package, so a plan cannot change shape
// mid-statement.
var incrementalSortPathsMode = incrementalSortModeFromEnv(os.Getenv("GOOPG_INCREMENTAL_SORT"))

// incrementalSortModeFromEnv resolves the knob. Anything unrecognised is
// `off`: fail-closed, so a typo cannot silently enable a plan shape whose
// executor operator does not exist yet. Same shape as `partialSortModeFromEnv`.
func incrementalSortModeFromEnv(v string) incrementalSortMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "paths", "true", "1":
		return incrementalSortOn
	default:
		return incrementalSortOff
	}
}

// incrementalSortModeLabel spells the mode the way an operator would export
// it, so the flag-provenance label round-trips (flaglabels.go's contract).
func incrementalSortModeLabel(m incrementalSortMode) string {
	if m == incrementalSortOn {
		return "on"
	}
	return "off"
}

// setIncrementalSortPathsModeForTest pins the mode for one test and returns
// the restore func. The knob is process-global by design, so a test that
// flips it must put it back.
func setIncrementalSortPathsModeForTest(m incrementalSortMode) func() {
	prev := incrementalSortPathsMode
	incrementalSortPathsMode = m
	return func() { incrementalSortPathsMode = prev }
}

// SetIncrementalSortPathsMode is the same hook across the package boundary,
// taking the label an operator would export (`off` / `on`) and resolving it
// through the SAME function production resolves the environment variable
// with — so a caller cannot select a mode the env knob could not. Mirrors
// `SetPartialSortPathsMode` / `SetPartialAggPathsMode`; process-global, so a
// caller must run the returned restore.
func SetIncrementalSortPathsMode(label string) (restore func()) {
	return setIncrementalSortPathsModeForTest(incrementalSortModeFromEnv(label))
}

// upperOrderedIncrementalSortProducer is this arm's DPPATH trace string
// (pathtrace.go), sibling to upperOrderedInputProducer/upperOrderedSortProducer
// declared in upperordered.go.
const upperOrderedIncrementalSortProducer = "upper.ordered.incrementalsort"

// addIncrementalSortPaths is `addOrderedPaths`'s third arm: PG's own
// `create_ordered_paths` loop, restricted to the candidates goopg can see at
// this seam (`ordered.SearchCandidates`, published by S2b-2a) whose validated
// ordering claim (`ordered.SearchCandidateKeys`, S2b-2b) shares a genuine
// PARTIAL prefix with `sortPathkeys` — a full match is arm 1's case for the
// seed and buys nothing new here; zero shared prefix is a full Sort, which is
// what arm 2 already prices for the seed (a losing candidate with zero shared
// prefix competing on a full Sort is exactly the still-blocked "actual
// tournament" this arm intentionally does NOT re-open beyond the prefix
// credit that motivates it).
//
// `input` is the seed `*Path` (`newPrebuiltPath`'s wrapper over the finished
// child Node) `createOrderedPaths` already built — its `.node` is the
// materialized Node for THIS rel's relids, reused here as the `child Node`
// `estimateNumGroups` needs to look up column statistics. That reuse is sound
// because S2b-2's own recon (Finding 2) established that every candidate in
// one call's Pathlist is an alternate PHYSICAL strategy for the SAME logical
// relation — same base tables, same columns, same statistics — so the seed's
// materialized Node is a faithful stats source for a sibling candidate's own
// prefix, not an approximation specific to the seed.
func addIncrementalSortPaths(ordered *RelOptInfo, input *Path, sortPathkeys []PathKey, cp costParams, limitTuples float64) {
	if incrementalSortPathsMode != incrementalSortOn {
		return
	}
	for i, candidate := range ordered.SearchCandidates {
		if candidate == nil || i >= len(ordered.SearchCandidateKeys) {
			continue
		}
		keys := ordered.SearchCandidateKeys[i]
		if len(keys) == 0 {
			continue
		}
		contained, nCommon := pathkeysCountContainedIn(keys, sortPathkeys)
		if pathTraceEnabled {
			traceIncrementalSortCandidate(i, candidate.Kind, len(keys), contained, nCommon, candidate.Cost.Total)
		}
		if contained || nCommon == 0 {
			continue
		}
		groupExprs := make([]Expr, nCommon)
		for j := 0; j < nCommon; j++ {
			groupExprs[j] = sortPathkeys[j].Expr
		}
		groups := estimateNumGroups(groupExprs, input.node, int64(candidate.Rows))
		sp := &Path{
			Kind: PathIncrementalSort,
			// `cost_incremental_sort` folds enable_sort's flag in via
			// `cost_sort` upstream (costsize.c:2144); see the file header
			// GUC note for why this reuses cp.enableSort rather than a
			// second, still-unwired flag.
			DisabledNodes: disabledNodesFor(!cp.enableSort, candidate),
			Rel:           ordered,
			Rows:          candidate.Rows,
			Cost: costIncrementalSort(cp, candidate.Cost, candidate.Rows, float64(groups),
				pathNCols(candidate), pathAvgVarBytes(candidate), limitTuples, pathWidth(candidate)),
			Pathkeys: sortPathkeys,
			// M0141-S7-exec-b: stashed for `createIncrementalSortPlan` —
			// see the field's own doc comment (path.go) for why it is
			// carried here rather than re-derived at createPlanNode time.
			PresortedCount: nCommon,
			Children:       []*Path{candidate},
			RequiredOuter:  candidate.RequiredOuter,
			ParallelSafe:   parallelSafeWith(candidate.Rel, candidate),
		}
		inheritNarrowedWidths(sp, candidate)
		addPath(ordered, sp, upperOrderedIncrementalSortProducer)
	}
}
