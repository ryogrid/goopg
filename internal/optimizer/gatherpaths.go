package optimizer

// gatherpaths.go — Phase 5 slice C-19d / P5-04 (take3 08 §8):
// `generate_useful_gather_paths` (allpaths.c:3236), the FIRST READER of
// `RelOptInfo.PartialPathlist`.
//
// C-19a stamped `consider_parallel` on every rel, C-19b put a priced partial
// seq scan in `PartialPathlist`, C-19c put a priced partial index scan beside
// it — and nothing consumed either. This file turns the cheapest partial path
// into an ordinary candidate on the rel's SERIAL `Pathlist`, priced by
// `cost_gather` / `cost_gather_merge`, so `add_path` decides parallel-vs-serial
// with the same comparator it uses for everything else. That is the whole point
// of Phase 5: `MaybeAddGather` (parallel.go) is a SIZE rule that runs after the
// search on a finished tree, so the search has never been able to prefer a plan
// BECAUSE it will parallelise. D-05 measured what that costs — three correct
// hash-join cost fixes each lost 10-22% of TPC-H by moving the plan off the one
// shape the post-pass can gather.
//
// Design: docs/design/planner-c19d-gather-paths/DESIGN.md.
//
// ADMISSION IS OFF BY DEFAULT (`GOOPG_GATHER_PATHS`, §5 of that doc). Partial
// paths exist on BASE rels only until C-19f gives a joinrel its own, so a
// Gather chosen here sits BELOW every join and the joins run serially in the
// leader — while the post-pass puts one Gather ABOVE the whole hash-join
// subtree. Flipping the default is a measured decision (TPC-H A/B, timing per
// moved plan) and this slice does not take it.

import (
	"os"
	"strings"
)

// gatherPathMode is the admission rule for the paths this file produces.
//
//   - off  — produce none. The search is unchanged by construction, which is
//     this slice's serial-control-arm argument.
//   - top  — produce them only at the search's FINAL rel: the node the
//     post-pass targets today. Inert on any multi-rel statement until C-19f
//     populates a joinrel's partial list, and live the moment it does.
//   - all  — PG-faithful: every rel with partial paths, base rels included.
//     This is the arm that carries the ordering trap (take2 07 §3.2) and the
//     one a measurement must clear before it can become the default.
type gatherPathMode int

const (
	gatherPathsOff gatherPathMode = iota
	gatherPathsTop
	gatherPathsAll
)

// gatherPathsMode is read once at process start, like every other plan-shaping
// knob in this package, so a plan cannot change shape mid-statement.
var gatherPathsMode = gatherPathModeFromEnv(os.Getenv("GOOPG_GATHER_PATHS"))

// gatherPathModeFromEnv resolves the knob. Anything unrecognised is `off`:
// this is a fail-closed switch, and a typo must not silently enable a plan
// shape whose measurement has not been run.
func gatherPathModeFromEnv(v string) gatherPathMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "top":
		return gatherPathsTop
	case "all", "on":
		return gatherPathsAll
	default:
		return gatherPathsOff
	}
}

// gatherPathModeLabel spells the mode the way an operator would export it, so
// the flag-provenance label round-trips (flaglabels.go's contract: the token
// inside `unset(…)` re-exported verbatim reproduces the arm).
func gatherPathModeLabel(m gatherPathMode) string {
	switch m {
	case gatherPathsTop:
		return "top"
	case gatherPathsAll:
		return "all"
	default:
		return "off"
	}
}

// setGatherPathsModeForTest pins the mode for one test and returns the restore
// func. The knob is process-global by design (read once at start), so a test
// that flips it must put it back — the established shape in this package.
func setGatherPathsModeForTest(m gatherPathMode) func() {
	prev := gatherPathsMode
	gatherPathsMode = m
	return func() { gatherPathsMode = prev }
}

// SetGatherPathsMode is the same hook across the package boundary, taking the
// label an operator would export (`off` / `top` / `all`) and resolving it
// through the SAME function production resolves the environment variable with —
// so a caller cannot select a mode the env knob could not.
//
// It exists for C-19f's executor consumer check: the item requires a fixture
// where the parallel hash path WINS by cost to actually EXECUTE as a parallel
// hash, and that test lives in internal/executor, which cannot reach an
// unexported knob. `SetParallelEnabled` (parallel.go) is the same shape for the
// post-pass's kill switch. Like it, this is process-global, so a caller must
// run the returned restore.
func SetGatherPathsMode(label string) (restore func()) {
	return setGatherPathsModeForTest(gatherPathModeFromEnv(label))
}

// generateUsefulGatherPaths is `generate_useful_gather_paths` (allpaths.c:3236)
// at this slice's scope: its `generate_gather_paths` body (allpaths.c:3099) —
// one Gather over the cheapest partial path, plus one Gather Merge per partial
// path that already has an ordering.
//
// The half NOT here is upstream's :3255-3341: sorting a partial path (fully or
// incrementally) to reach an ordering it does not already have, and gathering
// THAT. It needs a Sort path over a partial path plus
// `get_useful_pathkeys_for_relation`, and it is C-19e's ("re-decide Gather
// Merge → Sort → Parallel scan by cost"). The name is upstream's because the
// call sites are upstream's; the missing half is stated rather than implied.
//
// Called immediately before `setCheapest` on each rel, which is where
// `standard_join_search` calls it (allpaths.c:3503-3517) and where
// `merge_clump` calls it in the GEQO arm (geqo_eval.c). A rel with no partial
// paths returns at the first line, as upstream does.
func (s *searchCtx) generateUsefulGatherPaths(rel *RelOptInfo) {
	if s == nil || rel == nil || len(rel.PartialPathlist) == 0 {
		return
	}
	if !s.parallelModeOK || !rel.ConsiderParallel {
		// Belt-and-braces: `addPartialPath` already refuses to file a path on a
		// rel that does not consider parallel, so an entry here means the flag
		// was cleared afterwards. Refusing fails closed.
		return
	}
	switch gatherPathsMode {
	case gatherPathsOff:
		return
	case gatherPathsTop:
		if relLevel(rel.Relids) != s.nrels {
			return
		}
	}

	// "The output of Gather is always unsorted, so there's only one partial
	// path of interest: the cheapest one. That will be the one at the front of
	// partial_pathlist because of the way add_partial_path works."
	// (allpaths.c:3116-3119; goopg's addToPartialPathlist keeps the same
	// ascending-total-cost order.)
	if g := makeGatherPath(rel, rel.PartialPathlist[0], s.cp); g != nil {
		addPath(rel, g, "gather")
	}

	// "For each useful ordering, we can consider an order-preserving Gather
	// Merge." (allpaths.c:3127-3143.)
	for _, sub := range rel.PartialPathlist {
		if len(sub.Pathkeys) == 0 {
			continue
		}
		if gm := makeGatherMergePath(rel, sub, s.cp); gm != nil {
			addPath(rel, gm, "gather.merge")
		}
	}
}

// makeGatherPath is `create_gather_path` (pathnode.c:1974) + `cost_gather`.
// nil when the subpath is not one this executor can run under a Gather.
//
// Field-for-field with upstream: `parallel_aware = false`, `parallel_safe =
// false`, `parallel_workers = 0` — a Gather is the boundary between the
// parallel and serial regions, so it is neither partial itself nor usable
// inside another partial subtree — `pathkeys = NIL` ("Gather has unordered
// result"), and `disabled_nodes = subpath->disabled_nodes` (cost_gather carries
// no enable_* flag of its own).
//
// `num_workers` is not stored: it IS `subpath->parallel_workers`, and the
// subpath is `Children[0]`, in scope at every reader. Two fields that can
// disagree about a worker count is the bug class `Path.Rows`' own comment
// warns about.
func makeGatherPath(rel *RelOptInfo, sub *Path, cp costParams) *Path {
	if !gatherSubpathIsRunnable(sub) {
		return nil
	}
	rows := computeGatherRows(sub, cp)
	return &Path{
		Kind:     PathGather,
		Rel:      rel,
		Rows:     rows,
		Cost:     gatherCost(cp, sub.Cost, rows),
		Pathkeys: nil,
		// `pathnode->path.parallel_safe = false` (pathnode.c): a Gather may
		// not appear inside another Gather's partial subtree — which is also
		// what the executor's prebuildHashJoins comment assumes.
		ParallelSafe:    false,
		ParallelWorkers: 0,
		DisabledNodes:   sub.DisabledNodes,
		Children:        []*Path{sub},
	}
}

// makeGatherMergePath is `create_gather_merge_path` (pathnode.c:2020) +
// `cost_gather_merge`. nil when the subpath is not one `gatherMergeOp` can
// actually drive (see gatherMergeSubpathIsRunnable) or has no ordering to
// preserve (upstream asserts `pathkeys`).
func makeGatherMergePath(rel *RelOptInfo, sub *Path, cp costParams) *Path {
	if !gatherMergeSubpathIsRunnable(sub) || len(sub.Pathkeys) == 0 {
		return nil
	}
	rows := computeGatherRows(sub, cp)
	return &Path{
		Kind: PathGatherMerge,
		Rel:  rel,
		Rows: rows,
		Cost: gatherMergeCost(cp, sub.Cost, sub.ParallelWorkers, rows),
		// `pathnode->path.pathkeys = pathkeys`, and upstream ERRORs when the
		// subpath does not already deliver them ("gather merge input not
		// sufficiently sorted"). goopg takes the subpath's own list, so the
		// two cannot disagree; the sort-to-reach-an-ordering arm is C-19e's.
		Pathkeys:        append([]PathKey(nil), sub.Pathkeys...),
		ParallelSafe:    false,
		ParallelWorkers: 0,
		// `input_disabled_nodes + (enable_gathermerge ? 0 : 1)`
		// (costsize.c:535). This is the counting form ParallelSettings.
		// DisableGatherMerge's comment asked P5-04 to land.
		DisabledNodes: sub.DisabledNodes + disabledNodesFor(!cp.enableGatherMerge),
		Children:      []*Path{sub},
	}
}

// gatherSubpathIsRunnable is the fail-closed admission test for a partial path
// about to be put under a Gather. Each condition names the wrong ANSWER it
// prevents, not a missed optimisation:
//
//   - `ParallelWorkers == 0` is upstream's `single_copy` Gather. goopg's
//     producers never offer a 0-worker partial path (both `continue` on
//     `workers <= 0`) and the executor's `Gather.SingleCopy` is documented
//     "Reserved; nothing sets it yet", so this shape has never run.
//   - a subpath that is not `ParallelSafe` is not a partial path at all.
//   - the SHAPE must be one the executor's per-worker walks model. `runWorker`
//     (operators_gather.go) IGNORES `attachParallelScan`'s return value, so an
//     unmodelled subtree does not "stay serial" — every worker reads the whole
//     relation and the Gather returns N copies of every row. The planner-side
//     mirror of those walks is `drivingScan`, and `createGatherPlan` asserts it
//     on the BUILT tree; here the same question is asked of the path, so a
//     shape that could not execute is never even costed.
func gatherSubpathIsRunnable(sub *Path) bool {
	if sub == nil || sub.ParallelWorkers <= 0 || !sub.ParallelSafe {
		return false
	}
	return partialPathShapeIsGatherable(sub)
}

// gatherMergeSubpathIsRunnable is now exactly gatherSubpathIsRunnable. It is
// kept as a named function because the two questions are genuinely distinct
// and were answered differently until 2026-09-06 — see below.
//
// It USED to add "and the driving scan must be a SEQ scan", because
// `gatherMergeOp` attached only `attachParallelScan` to each worker's tree and
// not the index/bitmap claim sets. A Gather Merge over a partial INDEX path
// therefore gave every worker the whole index and returned N copies of every
// row. That was measured before it was fixed, at 1/2/4 workers:
// 5802 / 8703 / 14505 rows against a serial 2901, i.e. exactly (workers+1)x —
// and IN THE CORRECT ORDER, which is why no ordering test could have caught it
// and only a values test did.
//
// E-10 closed the executor gap (`a22d995c8`): `parallelClaimSet` holds all
// three claim kinds behind a single `attachAll()` wiring site, and BOTH
// `gatherOp` and `gatherMergeOp` embed it, with an anti-drift test that fails
// if a claim kind is added without an `attachAll` arm. So the restriction has
// no reason left, and keeping it would refuse the only pathkey-carrying
// partial path goopg produces — the ordered INDEX twin from C-19c — which is
// what kept Gather Merge at zero production surface.
//
// The remaining guards are the ones that still mean something, and they are
// inherited rather than restated: `gatherSubpathIsRunnable`'s whitelist
// (`partialPathShapeIsGatherable` → `partialPathDrivingKind != PathPrebuilt`)
// admits exactly the shapes `attachAll` models, and the `RequiredOuter == 0`
// refusal for index paths is untouched. Bitmap needs no extra guard here:
// `generateUsefulGatherPaths` skips subpaths with no `Pathkeys`, and a bitmap
// heap scan carries none — so a partial bitmap path cannot reach this test at
// all (ledger `e10-gathermerge-bitmap-untested-e2e` records that this leaves
// bitmap-under-GatherMerge without an end-to-end test, because no producer
// offers such a path).
func gatherMergeSubpathIsRunnable(sub *Path) bool {
	return gatherSubpathIsRunnable(sub)
}

// partialPathShapeIsGatherable reports whether a partial path's shape bottoms
// out in a scan the executor's per-worker attach walks model. It is the PATH
// twin of `drivingScan` (parallel.go) and must stay in step with it: this one
// answers before the node exists, that one after.
//
// Deliberately a WHITELIST with no default arm falling through to true —
// C-19a's review found four fail-open holes in exactly that pattern, and the
// answer here is a wrong-results bug rather than a missed plan.
func partialPathShapeIsGatherable(p *Path) bool {
	return partialPathDrivingKind(p) != PathPrebuilt
}

// partialPathDrivingKind returns the kind of the scan that drives a partial
// path, or `PathPrebuilt` (this file's "none of them" marker — a prebuilt
// subtree is opaque and can never be driven by a worker's claim set) when the
// shape is one no attach walk models.
//
// Today's producers only ever offer a bare scan, so the walk is one step; the
// wrapper arms exist because C-19e/f will add Sort and join shapes and the
// refusal must be visible where it is decided, not implicit in a missing case.
func partialPathDrivingKind(p *Path) PathKind {
	if p == nil {
		return PathPrebuilt
	}
	switch p.Kind {
	case PathSeqScan:
		// seqScanOp: attachParallelScan's own terminal arm.
		return PathSeqScan
	case PathIndexScan:
		// indexScanOp / indexOnlyScanOp: attachParallelIndexScan (M0134-0189,
		// C-19c). A parameterised probe is not partial — the post-pass's
		// `plainIndexScanIsPartialCapable` refuses one, and no partial index
		// path is parameterised by construction (`addPartialIndexPath`
		// requires `RequiredOuter == 0`) — so a non-zero RequiredOuter here
		// means a producer changed and this must refuse.
		if p.RequiredOuter != 0 {
			return PathPrebuilt
		}
		return PathIndexScan
	case PathBitmapHeapScan:
		// bitmapHeapScanOp: attachParallelBitmapScan (S5.6). No producer
		// offers a partial bitmap path yet; the arm is here so that when one
		// does, it is admitted by a decision rather than by a default.
		return PathBitmapHeapScan
	case PathHashJoin:
		// C-19f. A hash join is partial through its PROBE side only: the build
		// is drained ONCE by the leader before fan-out
		// (`prebuildSharedHashJoins`) and published, and the probe is what the
		// workers split by scan block. `Children[0]` IS the probe side by this
		// package's child convention (pathgen.go: "Children[0] is the probe
		// (outer) side, Children[1] is the build side"), and
		// `createHashJoinPlan` leaves `BuildLeft` false — so Children[0]
		// becomes `Join.Left` and `joinProbeSideIsLeft` (= !BuildLeft) returns
		// true. The path walk and the node walk therefore descend the SAME
		// side, which is the sibling-agreement rule this whole file turns on.
		//
		// Only a path this producer built can appear here: `addPartialPath`
		// refuses a parameterised or non-parallel-safe path, and
		// `addPartialHashJoinPath` is the only producer of a partial
		// PathHashJoin. A parameterised one would still be refused, for the
		// same reason the index arm refuses one — a hash join propagates a
		// parameter rather than binding it, and no worker can supply it.
		if p.RequiredOuter != 0 || len(p.Children) != 2 {
			return PathPrebuilt
		}
		return partialPathDrivingKind(p.Children[0])
	default:
		// PathPrebuilt, joins, Sort, Memoize, Agg: not modelled by any attach
		// walk at this slice's scope. Refuse.
		return PathPrebuilt
	}
}

// addBaseRelGatherPaths is the base-relation half of C-19d's call sites: the
// `generate_useful_gather_paths(root, rel, false)` at the end of
// `set_rel_pathlist` (allpaths.c), run once per initial rel after every base
// path producer and before the level-2 search reads `CheapestTotal`.
//
// `setCheapest` is re-run per rel that gained a path, matching what
// `addOrderedIndexPaths` / `addOneIndexOnlyPath` already do after adding an
// unparameterised path: an unparameterised Gather can change CheapestTotal,
// and a stale CheapestTotal is what the join producers would read.
func (s *searchCtx) addBaseRelGatherPaths() {
	if s == nil || !s.parallelModeOK || gatherPathsMode == gatherPathsOff || len(s.joinrels) < 2 {
		return
	}
	for _, rel := range s.joinrels[1] {
		if rel == nil || len(rel.PartialPathlist) == 0 {
			continue
		}
		s.generateUsefulGatherPaths(rel)
		// Unconditional rather than "only if a path was accepted": setCheapest
		// is idempotent and a length comparison is not a verdict (addPath can
		// accept a path that evicts two incumbents, leaving the list SHORTER —
		// the exact trap `pathlistVerdict` exists for).
		setCheapest(rel)
	}
}
