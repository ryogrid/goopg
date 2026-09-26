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
// ADMISSION IS `all` BY DEFAULT SINCE M0140-0003 (`GOOPG_GATHER_PATHS`, §5 of
// that doc). Partial paths exist on BASE rels only until C-19f gives a
// joinrel its own, so a Gather chosen here can sit BELOW a join and the joins
// above it run serially in the leader — while the post-pass instead puts one
// Gather ABOVE the whole hash-join subtree. The measured decision this
// comment used to defer (TPC-H A/B, timing per moved plan) is M0140-0003
// (docs/design/0100-0149/m0140-0003-gather-paths-flip-lands-default-on.md):
// pre-registered "no match flip" per R43 rev 3 / K38 — the category-movement
// criterion the plan-parity harness holds this milestone to, not a match-
// count rise — and TPC-H/TPC-DS values gates held at the baseline arm.
// `GOOPG_GATHER_PATHS=off` still reproduces the pre-M0140-0003 serial-search
// control arm exactly, for any future A/B that needs it.
//
// A BASE rel's Gather is WINNABLE since 2026-09-07 (ledger
// `c19-baserel-scan-priced-on-output-rows`). It was not before: goopg priced a
// base-rel scan's CPU over the POST-restriction row count, the same number
// `cost_gather` charges transfer on, so `gather − serial` was
// `parallel_setup_cost + (parallel_tuple_cost − per_tuple_cpu × (1 − 1/d)) ×
// rows` — positive everywhere, and `all` could not change a plan. Both sites
// now read `baserel->pages` / `baserel->tuples` through
// `baseSeqScanCostInputs` (joinsearch.go), as PG's `cost_seqscan` does, and
// the crossover is pinned through the production producers in
// `gatherpaths_crossover_test.go`. See DESIGN §5.1a.
//
// That does NOT discharge C-19h's non-aggregate root, and the reason is not a
// cost one: a ONE-RELATION statement never enters the path search
// (`makeRelFromJoinlist` returns at `len(items) == 1`, allpaths.c:3399-3404),
// so no `RelOptInfo` and no partial path exist for it to read. The post-pass
// is still the only producer of parallelism there — TODO_ALL C-19h, blocker 4.

import (
	"os"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// gatherPathMode is the admission rule for the paths this file produces.
//
//   - off  — produce none. The search is unchanged by construction, which is
//     this slice's serial-control-arm argument, and reproduces goopg's
//     pre-M0140-0003 behaviour exactly.
//   - top  — produce them only at the search's FINAL rel: the node the
//     post-pass targets today. Inert on any multi-rel statement until C-19f
//     populates a joinrel's partial list, and live the moment it does.
//   - all  — PG-faithful: every rel with partial paths, base rels included.
//     This is the arm that carries the ordering trap (take2 07 §3.2), and
//     the DEFAULT since M0140-0003 measured it clear (category movement, no
//     match-count regression, values gates held).
type gatherPathMode int

const (
	gatherPathsOff gatherPathMode = iota
	gatherPathsTop
	gatherPathsAll
)

// gatherPathsMode is read once at process start, like every other plan-shaping
// knob in this package, so a plan cannot change shape mid-statement.
var gatherPathsMode = gatherPathModeFromEnv(os.Getenv("GOOPG_GATHER_PATHS"))

// gatherPathModeFromEnv resolves the knob. An unset/empty environment
// resolves to `all` — M0140-0003's landed default. An explicit,
// unrecognised value (a typo, or the empty string spelled some other way)
// resolves to `off` rather than falling through to the new default: this
// stays a fail-closed switch for anyone deliberately typing an opt-out
// string, on the same reasoning the pre-M0140-0003 version of this function
// applied to the (then unmeasured) `all` arm.
func gatherPathModeFromEnv(v string) gatherPathMode {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return gatherPathsAll
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
func (s *searchCtx) generateUsefulGatherPaths(rel *RelOptInfo, overrideRows bool) {
	if s == nil || rel == nil || len(rel.PartialPathlist) == 0 {
		// R54 Step-0: S4's "never generated" arm. Reachable with s and rel
		// non-nil (empty partial list); the nil cases have no relset to name.
		if s != nil && rel != nil {
			s.trace.gather(rel.Relids, 0, "no-partials")
		}
		return
	}
	// R54 Step-0: the S0/S1 split. Upstream's single gate is two verdicts
	// here with identical behaviour — both arms still refuse — so the trace
	// can tell "session closed" from "this rel closed".
	if !s.parallelModeOK {
		s.trace.gather(rel.Relids, len(rel.PartialPathlist), "no-parallel-mode")
		return
	}
	if !rel.ConsiderParallel {
		// Belt-and-braces: `addPartialPath` already refuses to file a path on a
		// rel that does not consider parallel, so an entry here means the flag
		// was cleared afterwards. Refusing fails closed.
		s.trace.gather(rel.Relids, len(rel.PartialPathlist), "no-cp")
		return
	}
	switch gatherPathsMode {
	case gatherPathsOff:
		s.trace.gather(rel.Relids, len(rel.PartialPathlist), "mode")
		return
	case gatherPathsTop:
		if relLevel(rel.Relids) != s.nrels {
			s.trace.gather(rel.Relids, len(rel.PartialPathlist), "mode")
			return
		}
	}
	// Gates passed: "admitted" means candidacy, not victory — whether a Gather
	// path was filed and won reads off the `cost` line's cheapest kind. This
	// separation is what makes S4's "generated but lost" distinguishable from
	// "never generated".
	s.trace.gather(rel.Relids, len(rel.PartialPathlist), "admitted")

	// "The output of Gather is always unsorted, so there's only one partial
	// path of interest: the cheapest one. That will be the one at the front of
	// partial_pathlist because of the way add_partial_path works."
	// (allpaths.c:3116-3119; goopg's addToPartialPathlist keeps the same
	// ascending-total-cost order.)
	if g := makeGatherPath(rel, rel.PartialPathlist[0], s.cp, overrideRows); g != nil {
		addPath(rel, g, "gather")
	}

	// "For each useful ordering, we can consider an order-preserving Gather
	// Merge." (allpaths.c:3127-3143.)
	for _, sub := range rel.PartialPathlist {
		if len(sub.Pathkeys) == 0 {
			continue
		}
		if gm := makeGatherMergePath(rel, sub, s.cp, overrideRows); gm != nil {
			addPath(rel, gm, "gather.merge")
		}
	}
}

// generateUpperRelGatherPaths is M0140-0006b-2's upper-rel entry point for
// generateUsefulGatherPaths (allpaths.c:3236's upper-rel callers —
// `create_grouping_paths`, `create_window_paths`, `create_setop_paths` —
// each call `generate_gather_paths` on their own rel; goopg's Phase-4
// producers never did, so no upper rel's PartialPathlist ever had a reader).
//
// The method needs a *searchCtx, but everything it actually reads off the
// context is available without one: `parallelModeOK(cp)` is a pure function
// of the cost currency (considerparallel.go:61), `cp` is already in scope at
// every upper-rel producer, `trace` is nil-safe (nothing traces an upper-rel
// Gather decision today), and `nrels` only feeds the `top`-mode final-rel
// check. So this builds the minimal context and delegates to the one body —
// no twin to keep in step (Hard-won Rule #2) — rather than re-stating the
// gates.
//
// The one policy this states rather than delegates is `top`: that mode
// admits only the search's FINAL rel ("the node the post-pass targets
// today"), and an upper rel is never a member of any search level, so it is
// refused here, fail-closed. `all` (the default since M0140-0003) admits
// every rel with partial paths, upper rels included; `off` is refused inside
// the delegated call, same as for a search rel.
//
// Only the SETOP rel can have partial paths today (addPartialSetOpPath,
// M0140-0006b is the sole upper-rel partial producer); every other upper rel
// returns at the first line, so wiring this into WINDOW/ORDERED/GROUP_AGG is
// provably inert until a producer files there. Callers run this after all
// other candidates are offered and before setCheapest, mirroring
// addBaseRelGatherPaths' own placement.
func generateUpperRelGatherPaths(rel *RelOptInfo, cp costParams) {
	if rel == nil || len(rel.PartialPathlist) == 0 {
		return
	}
	if gatherPathsMode == gatherPathsTop {
		return
	}
	s := &searchCtx{parallelModeOK: parallelModeOK(cp), cp: cp}
	// Upper rels take upstream's override_rows=true arm (planner.c:5022,
	// gather_grouping_paths planner.c:7721): the stamp stays
	// computeGatherRows.
	s.generateUsefulGatherPaths(rel, true)
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
func makeGatherPath(rel *RelOptInfo, sub *Path, cp costParams, overrideRows bool) *Path {
	if !gatherSubpathIsRunnable(sub) {
		return nil
	}
	// cost_gather's row stamp (costsize.c:455-458): `rows` when the caller
	// overrides, else `param_info->ppi_rows` or `rel->rows`. Upstream's
	// override is always `compute_gather_rows(subpath)`; every scan/join-rel
	// call site passes override_rows=false (allpaths.c:557, :3518,
	// geqo_eval.c:277, planner.c:7880) so the Gather carries the relation's
	// own total, and only the grouped/partially-grouped and partial-distinct
	// upper rels pass true (planner.c:5022, gather_grouping_paths
	// planner.c:7721). The param_info arm has no counterpart: parameterized
	// subpaths are refused above and goopg's RelOptInfo is per-relid-set, so
	// rel.Rows is the only estimate a gather rel carries.
	rows := rel.Rows
	if overrideRows {
		rows = computeGatherRows(sub, cp)
	}
	g := &Path{
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
	// R121 Slice A(ii): a Gather projects nothing, so it emits its child's row.
	inheritNarrowedWidths(g, sub)
	return g
}

// makeGatherMergePath is `create_gather_merge_path` (pathnode.c:2020) +
// `cost_gather_merge`. nil when the subpath is not one `gatherMergeOp` can
// actually drive (see gatherMergeSubpathIsRunnable) or has no ordering to
// preserve (upstream asserts `pathkeys`).
func makeGatherMergePath(rel *RelOptInfo, sub *Path, cp costParams, overrideRows bool) *Path {
	if !gatherMergeSubpathIsRunnable(sub) || len(sub.Pathkeys) == 0 {
		return nil
	}
	// cost_gather_merge stamps rows exactly as cost_gather does
	// (costsize.c:493-496): the caller's override (always
	// `compute_gather_rows`) or the rel's own estimate — see makeGatherPath.
	rows := rel.Rows
	if overrideRows {
		rows = computeGatherRows(sub, cp)
	}
	gm := &Path{
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
	// R121 Slice A(ii): GatherMerge reorders rows, it does not project them.
	inheritNarrowedWidths(gm, sub)
	return gm
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
// partialNestLoopJointype is the ONE jointype set the ordinary partial
// nested-loop family admits, shared by `partialPathDrivingKind`'s PathNestLoop
// arm and its spine mirror so the two cannot drift.
//
// They had drifted: the mirror's comment claimed to follow the arm
// "guard-for-guard" while testing `!= JoinInner` against the arm's
// {INNER, SEMI}. That was a refusal, hence harmless, but a documented
// invariant that is only true in prose is the same shape of defect as the
// 2026-09-21 SEMI wrong answer — which was a divergence between gates whose
// comments also said they must agree. Sharing the predicate makes the claim
// structural.
//
// The set is PG's nestloop dispatch set (`joinpath.c:1842-1846`) minus RIGHT
// and FULL, which need the cross-worker inner-match reduction this family does
// not model. The executor twin `ordinaryInnerNestedLoopPartial`
// (internal/executor/parallel_scan.go) and the node gate
// `nestedLoopJoinIsPartialCapable` carry the same set; all four move together
// or not at all.
func partialNestLoopJointype(t parser.JoinType) bool {
	switch t {
	case parser.JoinInner, parser.JoinLeft, parser.JoinSemi, parser.JoinAnti:
		return true
	}
	return false
}

// partialProbeNestLoopJointype is the ONE jointype set the parameterized-probe
// partial nested-loop arms admit — narrower than `partialNestLoopJointype`
// because the probe's per-outer-row verdict must additionally be provable
// worker-local at every downstream gate, and only INNER and SEMI have been.
// The three probe gates — this arm's R95 tail below,
// `lateralProbeJoinIsPartialCapable` (parallel.go) and the executor twin
// `lateralProbeJoinPartial` (internal/executor/parallel_scan.go) — carry the
// same set and move together or not at all; the fused family
// (`NestedLoopIndexJoinIsPartialCapable`) verified all four of PG's dispatch
// jointypes by measurement (M0145-0010) and stays on its own wider set.
//
// SEMI joins INNER for M0146-0002i: one qualifying probe row decides the
// outer row, the probe scan breaks (`finishOuter`, join_nl_stream.go), and
// the joined row is never emitted — worker-local by the same argument the
// whole-inner SEMI arm already records. Its named consumer is TPC-H Q4,
// whose PG plan is this exact shape: `Nested Loop Semi Join` inside the
// Gather, probing lineitem's index per worker.
//
// ANTI stays refused until M0146-0002j lands the producer gate with it —
// widening here alone would admit a filed-by-nobody arm. LEFT stays
// refused: worker-local in principle but never executor-verified for the
// probe shape, and no measured consumer exists (ledger
// `m0146-0002a-left-probe`).
func partialProbeNestLoopJointype(t parser.JoinType) bool {
	switch t {
	case parser.JoinInner, parser.JoinSemi:
		return true
	}
	return false
}

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
	case PathSetOp:
		// M0140-0006c. Unlike a join (partial through ONE side, the other
		// prebuilt/shared once), a partial SetOp streams BOTH branches
		// (addPartialSetOpPath, M0140-0006b) — each needs its OWN driving
		// scan the executor's *setOp arm of attachAll can claim
		// independently (parallel_scan.go), so this recurses into BOTH
		// children rather than the single Children[0]/[1] pick the join
		// arms make.
		//
		// Widened past a bare scan on each side by M0140-0006c-2: a
		// hash-join-driven branch partial through its probe side, a
		// merge-join-driven branch partial through its outer side, a
		// nested-loop-driven branch partial through its outer side, and
		// a bitmap-driven branch (slice C) whose heap scan attaches the
		// branch's own leaf claim set — prebuildBitmap now publishes the
		// prebuilt TIDBitmap per branch (cs.setOpLeft/setOpRight.pbm)
		// instead of only to the top-level claim set. The bitmap arm is
		// dormant for the same reason the top-level one is: no producer
		// files a partial bitmap path today.
		//
		// M0140-0006c-3: a branch marked claimed-whole
		// (SetOpLeftNonPartial/SetOpRightNonPartial — PG's
		// pa_nonpartial_subpaths) needs NO driving-kind check at all: one
		// participant CAS-claims and drains it serially, so any
		// parallel-safe serial plan qualifies, matching PG which puts
		// `nppath` in regardless of its driving shape. The ParallelSafe
		// re-check below is this file's standing posture — re-verify the
		// producer's pick rather than trust it.
		if len(p.Children) != 2 {
			return PathPrebuilt
		}
		nonPartial := [2]bool{p.SetOpLeftNonPartial, p.SetOpRightNonPartial}
		for i, c := range p.Children {
			if nonPartial[i] {
				if c == nil || !c.ParallelSafe {
					return PathPrebuilt
				}
				continue
			}
			if !setOpBranchDrivingKindIsSupported(c) {
				return PathPrebuilt
			}
		}
		return PathSetOp
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
		// M0146-0002: a Parallel Hash join's BUILD side is partial too, and
		// the executor claims it through the join's own claim set
		// (attachParallelHashBuildSides). Only a partial seq scan is admitted
		// there: a bitmap build would find no prebuilt bitmap in that set,
		// and any other shape is one this slice never proved — an unclaimed
		// build side means every participant builds the whole relation into
		// the shared table, N copies of every inner row.
		if p.ParallelHash && partialPathDrivingKind(p.Children[1]) != PathSeqScan {
			return PathPrebuilt
		}
		return partialPathDrivingKind(p.Children[0])
	case PathMergeJoin:
		// E-20 Cut 3 (`try_partial_mergejoin_path`, joinpath.c:1145). A
		// merge join is partial through its OUTER side only: each worker
		// merge-joins its outer partition against the whole inner.
		// `Children[0]` IS the outer side by the same child convention the
		// hash arm cites, and `createMergeJoinPlan` leaves `BuildLeft`
		// false — so Children[0] becomes `Join.Left`, the side
		// `mergeJoinIsPartialCapable`/`attachParallelScan` descend. Only a
		// path this track's producer built can appear here:
		// `addPartialMergeJoinPath` is the only producer of a partial
		// PathMergeJoin, and it files nothing it did not prove drivable
		// (no-sort gate + this check's own recursion), so the arm below
		// cannot meet a shape the executor walks do not model.
		if p.RequiredOuter != 0 || len(p.Children) != 2 {
			return PathPrebuilt
		}
		return partialPathDrivingKind(p.Children[0])
	case PathNestLoop:
		// R94 (plan-parity-fix-take2). An ordinary nested loop is partial
		// through its OUTER side only: each worker joins its outer
		// partition against the WHOLE inner, which it materializes and
		// replays itself. Admit ONLY the evidenced INNER shape — the
		// node twin (nestedLoopJoinIsPartialCapable) agrees, and no path
		// is admitted by PathKind alone.
		//
		// R95 extends the arm to the lateral-probe inner (parameterized
		// index probe re-opened per worker-local outer row): the node
		// twin is lateralProbeJoinIsPartialCapable, and the subset test
		// below is re-checked rather than trusted.
		//
		// Only a path R60's producer built can appear here, and R60 files
		// INNER and (since M0137-0019b) SEMI only — its V1 gate refuses
		// every other jointype for this arm, since a refused head would
		// starve admittable siblings (makeGatherPath reads
		// PartialPathlist[0] only). The jointype test below re-asserts it
		// rather than trusting the caller, for the reason
		// createPartialGroupingPaths states: a producer that trusts a
		// caller's gate is one refactor away from a hole.
		//
		// SEMI joins the set for M0137-0019b: its per-outer-row verdict is
		// worker-local (`finishOuter`, join_nl_stream.go — one qualifying
		// inner tuple decides the outer tuple, the joined row is never
		// emitted, and the inner-matched bitmap RIGHT/FULL would need is
		// never touched), so a partitioned outer is transparent.
		//
		// LEFT and ANTI join it for M0145-0010 scope (c), on the same
		// rationale the producer already recorded for them, and widened in
		// ONE change with the producer, the spine mirror below and the
		// executor twin — the discipline the 2026-09-21 SEMI defect teaches.
		// PG admits {INNER, LEFT, SEMI, ANTI} at the same dispatch gate
		// (`joinpath.c:1842-1846`). RIGHT and FULL stay out: they need the
		// cross-worker inner-match reduction no gate here models.
		if !partialNestLoopJointype(p.Jointype) {
			return PathPrebuilt
		}
		if p.RequiredOuter != 0 || len(p.Children) != 2 {
			return PathPrebuilt
		}
		o, in := p.Children[0], p.Children[1]
		// The V5-class check every landed producer repeats: a 0-worker or
		// parallel-unsafe outer breaks the ParallelWorkers convention.
		if o == nil || o.ParallelWorkers <= 0 || !o.ParallelSafe || o.RequiredOuter != 0 {
			return PathPrebuilt
		}
		if in == nil {
			return PathPrebuilt
		}
		if in.RequiredOuter == 0 {
			// The inner is read WHOLE by every worker: it must be complete
			// (unparameterised) and must not be a Memoize cache — the
			// probe's parameter is what justifies caching (getMemoizePath
			// only wraps a probe carrying RequiredOuter), so a
			// whole-inner PathMemoize here means a producer changed.
			if in.Kind == PathMemoize {
				return PathPrebuilt
			}
			return partialPathDrivingKind(o)
		}
		// R95 (plan-parity-fix-take2): the lateral-probe inner. A
		// parameterized probe re-opened per worker-local outer row needs
		// no outer claim and no whole-inner materialization — but only
		// when the parameter is satisfiable by THIS outer: re-check the
		// V8 subset test here rather than trusting the filing site (a
		// producer that trusts a caller's gate is one refactor away from
		// a hole). The probe shape itself (bare index equality probe) is
		// proven at the node twin; here the kinds that can only be probes
		// are admitted — a parameterized PathIndexScan from R60's
		// producer carrying index clauses, or (M0142-0005a) that probe's
		// Memoize-wrapped twin: every worker already builds a private
		// memoizeOp/kvcache over the shared read-only plan (executor.go's
		// "each worker builds its OWN operator tree"), matching PG's
		// per-worker MemoizeState whose DSM shuttles only
		// instrumentation counters (nodeMemoize.c:1190-1260) — the
		// wrapper adds no claim and no shared state, so it unwraps once
		// (Children[0] is always the wrapped probe, getMemoizePath
		// joinpathsmemoize.go:292-303) and the bare-probe check runs on
		// the child. RequiredOuter propagates, so in.RequiredOuter below
		// reads the same value the child carries. Anything else
		// parameterized is refused: no worker can supply its parameter.
		// M0146-0002i: the probe jointype set — INNER plus SEMI — read
		// through the shared predicate, so the three probe gates cannot
		// drift apart the way the 2026-09-21 ordinary-SEMI gates did.
		if !partialProbeNestLoopJointype(p.Jointype) {
			return PathPrebuilt
		}
		probe := in
		if in.Kind == PathMemoize {
			if len(in.Children) != 1 {
				return PathPrebuilt
			}
			probe = in.Children[0]
		}
		if probe == nil || probe.Kind != PathIndexScan || len(probe.IndexClauses) == 0 {
			return PathPrebuilt
		}
		if p.OuterRelids == 0 || p.InnerRelids == 0 {
			// Unpartitioned path: satisfiability is unprovable.
			return PathPrebuilt
		}
		if req := calcNestloopRequiredOuter(p.OuterRelids, o.RequiredOuter, p.InnerRelids, in.RequiredOuter); req != 0 {
			return PathPrebuilt
		}
		return partialPathDrivingKind(o)
	default:
		// PathPrebuilt, joins, Sort, Memoize, Agg: not modelled by any attach
		// walk at this slice's scope. Refuse.
		return PathPrebuilt
	}
}

// setOpBranchDrivingKindIsSupported is the PathSetOp arm's own, narrower
// admission test for one branch — a bare seq or (unparameterised) index
// scan, a bitmap heap scan, a hash join partial through its probe side, a
// merge join partial through its outer side, a nested loop partial through
// its outer side (M0140-0006c-2, slices hash + A + B + C), or — M0145-0004a
// — a nested partial SetOp: the inner link of a right-leaning UNION ALL
// chain, which is exactly the shape the whole-chain appendrel candidate
// carries. It still does NOT delegate to partialPathDrivingKind's general
// recursion: the branch's nested shapes must be ones the SetOp claim-set
// story models — each join arm re-verifies its own guards rather than
// trusting a caller's, and no other kind (Sort, Memoize, Aggregate) has a
// branch-local executor twin. The SetOp arm is admitted because the
// executor's claim tree now nests with it: attachAll hands each branch a
// lazily grown leaf claim set (parallel_scan.go's setOpBranch), so an
// N-link chain materialises N-1 levels of branch state and every member
// scan gets its own independent block claim.
func setOpBranchDrivingKindIsSupported(p *Path) bool {
	if p == nil {
		return false
	}
	switch p.Kind {
	case PathSeqScan:
		return true
	case PathIndexScan:
		return p.RequiredOuter == 0
	case PathBitmapHeapScan:
		// M0140-0006c-2 (slice C). Mirrors the top-level arm's
		// unconditional admit: the branch's bitmap heap op attaches
		// through its own LEAF claim set — attachAll hands each branch
		// cs.setOpLeft/setOpRight, whose pbm prebuildBitmap now publishes
		// per branch. Dormant until a producer files a partial bitmap
		// path (none does today — the top-level arm is dormant for the
		// same reason); admitted by a decision, not by a default.
		return true
	case PathHashJoin:
		// M0140-0006c-2. Partial through the PROBE side only: Children[0]
		// by this package's child convention (pathgen.go), the same side
		// partialPathDrivingKind's PathHashJoin arm, stampParallelScan and
		// the executor's attach walk all descend. The build side is drained
		// once by the leader via prebuildSharedHashJoins, which now sees
		// through a *setOp (collectShareableJoins and HasShareableHashJoin
		// both descend). The RequiredOuter/len guards mirror the
		// PathHashJoin arm; the jointype trust is the producer's
		// (addPartialHashJoinPath files only partial-capable jointypes),
		// re-checked at runtime by hashJoinIsPartialCapable on the node
		// twin. A merge join on the spine is admitted (slice B) but a
		// hash below a merge outer is never COLLECTED for prebuild — the
		// branch's workers each private-build its inner, correct and Nx
		// the memory, the same E-20 deferral the top level already carries.
		if p.RequiredOuter != 0 || len(p.Children) != 2 {
			return false
		}
		// M0146-0002: no Parallel Hash inside a partial SetOp branch — the
		// branch claim sets carry no per-join build state.
		if p.ParallelHash {
			return false
		}
		return setOpBranchDrivingKindIsSupported(p.Children[0])
	case PathMergeJoin:
		// M0140-0006c-2 (slice B). Mirrors partialPathDrivingKind's own
		// PathMergeJoin arm guard-for-guard (E-20 Cut 3): a merge join is
		// partial through its OUTER side only — each worker merge-joins
		// its partition of the outer against the WHOLE inner, which it
		// sorts and reads itself. No shared build exists for merge, so
		// unlike the hash arm nothing here needs a collector: a hash
		// below this merge's outer is simply built privately by every
		// worker (correct, Nx the memory — the same E-20 deferral the
		// top-level arm already carries). Children[0] is the outer by
		// this package's child convention — the same side
		// createMergeJoinPlan leaves as Join.Left and the three
		// executor attach walks descend (each now names
		// JoinAlgoMerge -> left literally rather than answering from
		// BuildLeft, which a merge join leaves false by construction).
		// Recursion stays through THIS test so each kind on the spine is
		// admitted by its own arm's decision, not the general one's. The
		// jointype trust is the producer's — the
		// same stance the top-level arm takes — re-checked at runtime
		// by mergeJoinIsPartialCapable on the node twin (INNER/SEMI/
		// ANTI/LEFT; FULL/RIGHT refuse).
		if p.RequiredOuter != 0 || len(p.Children) != 2 {
			return false
		}
		return setOpBranchDrivingKindIsSupported(p.Children[0])
	case PathNestLoop:
		// M0140-0006c-2 (slice A). Mirrors partialPathDrivingKind's
		// PathNestLoop arm guard-for-guard (R94's ordinary whole-inner,
		// R95's lateral index-probe inner), recursing through THIS test so
		// each kind on the spine is admitted by its own arm's decision
		// (hash-, merge-, NL-, and bitmap-driven outers all admit through
		// their own arms). The executor side
		// needed nothing new: attachAll's *setOp arm hands each branch its
		// own leaf claim set, and attachParallelScan/
		// attachParallelBitmapScan/attachParallelIndexScan each already
		// carry the JoinAlgoNestedLoop arm (parallel_scan.go) — the inner
		// takes no claim in either shape (whole-inners are materialised
		// per worker, parameterized probes re-open per worker-local outer
		// row). The Jointype guard subsumes the general arm's second
		// identical re-check: the field cannot change between the two.
		if !partialNestLoopJointype(p.Jointype) || p.RequiredOuter != 0 || len(p.Children) != 2 {
			return false
		}
		o, in := p.Children[0], p.Children[1]
		if o == nil || o.ParallelWorkers <= 0 || !o.ParallelSafe || o.RequiredOuter != 0 {
			return false
		}
		if in == nil {
			return false
		}
		if in.RequiredOuter == 0 {
			// Whole-inner PathMemoize is refused for the same reason the
			// general arm states: the probe's parameter is what justifies
			// caching, so a parameterless memoize means a producer
			// changed.
			if in.Kind == PathMemoize {
				return false
			}
		} else {
			// R95 probe shape: a parameterized inner must be a bare index
			// probe whose requirement THIS outer can satisfy — re-checked
			// here rather than trusted from the filing site.
			// M0142-0005a: the probe's Memoize-wrapped twin is admitted by
			// the same unwrap the general arm performs — the cache is
			// per-worker-local under a branch exactly as under a top-level
			// Gather (attachAll hands the branch op tree to the same
			// attachParallel* walks, whose nestedLoopIndexJoinOp arm
			// descends the outer and never touches the probe).
			probe := in
			if in.Kind == PathMemoize {
				if len(in.Children) != 1 {
					return false
				}
				probe = in.Children[0]
			}
			if probe == nil || probe.Kind != PathIndexScan || len(probe.IndexClauses) == 0 {
				return false
			}
			if p.OuterRelids == 0 || p.InnerRelids == 0 {
				return false
			}
			if calcNestloopRequiredOuter(p.OuterRelids, o.RequiredOuter, p.InnerRelids, in.RequiredOuter) != 0 {
				return false
			}
		}
		return setOpBranchDrivingKindIsSupported(o)
	case PathSetOp:
		// M0145-0004a: a nested streaming UNION ALL link as a branch. The
		// admission contract is the parent's own PathSetOp arm applied one
		// level down — two children, each either claimed-whole
		// (ParallelSafe, no driving kind needed) or itself a supported
		// branch — so the check delegates back to partialPathDrivingKind
		// rather than restating it. The recursion is bounded by the chain
		// length: each level peels one link off the right-leaning chain.
		// What makes this safe is the executor twin, not the shape:
		// parallelClaimSet grows a fresh pair of leaf claim sets per level
		// (setOpBranch), so a nested link's member scans claim
		// independently of the outer link's — the N-copies defect the
		// previous `default: refuse` was protecting against.
		return partialPathDrivingKind(p) == PathSetOp
	default:
		return false
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
		// Scan rels take upstream's override_rows=false arm
		// (allpaths.c:557): the stamp is the rel's own estimate.
		s.generateUsefulGatherPaths(rel, false)
		// Unconditional rather than "only if a path was accepted": setCheapest
		// is idempotent and a length comparison is not a verdict (addPath can
		// accept a path that evicts two incumbents, leaving the list SHORTER —
		// the exact trap `pathlistVerdict` exists for).
		setCheapest(rel)
	}
}
