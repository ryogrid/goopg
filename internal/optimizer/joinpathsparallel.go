package optimizer

// joinpathsparallel.go — Phase 5 slice C-19f / P5-06 (take3 08 §8):
// `try_partial_hashjoin_path` (joinpath.c:1299) and the parallel block of
// `hash_inner_and_outer` (joinpath.c:2418-2477).
//
// This is the file that gives a JOINREL its own partial paths. Until it, every
// entry in `RelOptInfo.PartialPathlist` belonged to a BASE rel (C-19b's partial
// seq scan, C-19c's partial index scan), so a Gather chosen by C-19d's reader
// sat BELOW every join and the whole relation crossed the parallel boundary.
// C-19d §5.1 quantified what that costs: `parallel_tuple_cost` × rows is
// 0.1/row while the saving is `cpu_tuple_cost`'s worker share ≈0.0075/row, so
// `add_path` correctly dominates a base-rel Gather at ANY relation size. PG
// escapes that arithmetic by putting the join below the Gather. This file is
// how goopg does the same: a partial hash join runs the join in the workers, so
// only the JOIN'S OUTPUT is charged parallel_tuple_cost — and, because a
// joinrel's partial path is the partial OUTER of the join above it, the paths
// propagate upward until one Gather can sit over a whole join tree.
//
// Design: docs/design/planner-c19f-parallel-hashjoin/DESIGN.md.
//
// # Which of PG's two parallel hash joins this is
//
// Upstream has both, selected by `try_partial_hashjoin_path`'s `parallel_hash`
// argument (its header, joinpath.c:1290-1297): a partial outer over a COMPLETE
// inner replicated into a private hash table per process, and `Parallel Hash`,
// where a partial inner is built cooperatively into one DSM table behind a
// barrier protocol.
//
// goopg has neither, and something better than the first. Workers are
// goroutines in one address space, so `gatherOp.Open` pre-builds each shareable
// hash join's table ONCE in the leader (`prebuildSharedHashJoins`) and every
// participant adopts it by pointer — since E-09a, including a build that
// spilled to batch files, and since E-09b loading each batch once. So the shape
// priced here is: PARTIAL OUTER, COMPLETE INNER, ONE SHARED BUILD.
//
// The direct consequence for the price, and the reason this item was sequenced
// after E-09a/E-09b: the build is charged ONCE, undivided. A reverted D-05
// experiment charged a 5× participant multiplier on a spilling build, derived
// from the sharing-decline rule E-09a deleted; at HEAD that multiplier is
// simply wrong, and it is also not what upstream charges in either variant
// (`initial_cost_hashjoin`: `startup_cost += inner_path->total_cost`,
// costsize.c:4187 — no multiplier anywhere).
//
// `parallel_hash = true` is REFUSED: no goopg executor builds a hash table from
// a partial inner. The refusal is structural (this file never reads
// `inner.PartialPathlist`) and is stated rather than left as an absence.

// addPartialHashJoinPath is `try_partial_hashjoin_path(..., parallel_hash =
// false)` together with the guard `hash_inner_and_outer` wraps it in
// (joinpath.c:2418-2422). It files ONE partial hash join path — `outer` partial
// and driving the probe, `inner` complete and hashed — into `joinrel`'s
// PartialPathlist, or files nothing.
//
// `keys` / `residual` are the caller's split for THIS direction and `bucket`
// the inner-bucket fraction it measured on the build side, so the partial path
// is priced from exactly the inputs its serial twin was — the two must not be
// able to disagree about anything but the parallel terms.
//
// Called from `addPathsToJoinrel` immediately after `addHashJoinPath`, which is
// where upstream's parallel block sits relative to its serial one (the
// `try_hashjoin_path` loop closes at :2398 and the parallel block opens at
// :2418).
import "github.com/goopg/goopg/internal/parser"

func addPartialHashJoinPath(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams,
	keys, residual []*restrictInfo, bucket float64) {

	// The only reader of a partial path is `generateUsefulGatherPaths`, which
	// is gated by the same mode — so producing under `off` buys nothing and
	// costs one hashJoinCost per pair per direction per level on a search whose
	// planner time the pre-commit pgbench smoke measures. Under `top` the
	// producer must still run at EVERY level: the final rel's partial path
	// exists only because the levels below propagated theirs upward.
	if gatherPathsMode == gatherPathsOff {
		return
	}
	// `joinrel->consider_parallel` (joinpath.c:2418), already propagated by
	// joinrelConsiderParallel (= build_join_rel, relnode.c:829-845).
	if s == nil || !s.parallelModeOK || joinrel == nil || !joinrel.ConsiderParallel {
		return
	}
	if outer == nil || inner == nil || len(keys) == 0 {
		return
	}
	// `outerrel->partial_pathlist != NIL`, and `linitial` of it — the cheapest,
	// since addToPartialPathlist keeps ascending total-cost order exactly as
	// add_partial_path's `insert_at` does.
	if len(outer.PartialPathlist) == 0 {
		return
	}
	o := outer.PartialPathlist[0]
	if o == nil || o.ParallelWorkers <= 0 || !o.ParallelSafe {
		return
	}
	// The path twin of `drivingScan`, asked HERE as well as at the Gather so a
	// shape the executor's per-worker attach walks do not model is never even
	// costed. `runWorker` IGNORES attachParallelScan's return value, so an
	// unmodelled subtree does not "stay serial" — every worker reads the whole
	// relation and the Gather returns N copies of every row.
	if !partialPathShapeIsGatherable(o) {
		return
	}

	// `get_cheapest_parallel_safe_total_inner` (pathkeys.c:699). Upstream first
	// tries `cheapest_total_inner` and falls back to this scan; the two are
	// folded here because CheapestTotal is itself on Pathlist and would be
	// found by the same scan.
	i := cheapestParallelSafeTotalInner(inner.Pathlist)
	if i == nil {
		return
	}

	// `try_partial_hashjoin_path`'s own refusals (:1315-1318). Upstream asserts
	// the outer is unparameterised and returns early on a parameterised inner;
	// both are refusals here, per addPartialPath's fail-closed convention —
	// a panic inside the planner would fail the statement, while a path not
	// offered simply cannot be chosen.
	if o.RequiredOuter != 0 || i.RequiredOuter != 0 {
		return
	}
	if calcNonNestloopRequiredOuter(o, i) != 0 {
		return
	}

	// `final_cost_hashjoin` (costsize.c:4307-4314): "For partial paths, scale
	// row estimate." One divisor, applied here and undone by cost_gather's
	// computeGatherRows — not two.
	divisor := getParallelDivisor(o.ParallelWorkers, cp.parallelLeaderParticipation)
	rows := clampRowEst(joinrel.Rows / divisor)

	cost := hashJoinCost(cp, hashJoinInputs{
		// The partial outer's Rows is ALREADY the per-worker count
		// (costParallelSeqscan divides it), so the probe terms come out
		// per-worker with no further division. The inner's are the WHOLE
		// inner: it is a complete path, and upstream's parallel_hash arm
		// multiplies the count back up for exactly this reason
		// (costsize.c:4209-4210) in the variant goopg does not have.
		outer: o.Cost, inner: i.Cost,
		outerRows: o.Rows, innerRows: i.Rows,
		// PG derives hashjointuples independently via approx_tuple_count over
		// the already-divided outer_path_rows, arriving at ≈rows/divisor by a
		// different route. Using the one clamped figure for both Path.Rows and
		// the per-tuple charge is what stops the two from disagreeing — the
		// bug class Path.Rows' own comment warns about.
		outputRows:      rows,
		numHashClauses:  len(keys),
		innerBucketSize: bucket,
		outerCols:       pathNCols(o), innerCols: pathNCols(i),
		outerAvgVarBytes: pathAvgVarBytes(o), innerAvgVarBytes: pathAvgVarBytes(i),
	})
	// The residual rides the join's OUTPUT cardinality, which for a partial
	// path is the per-worker one — the same rule addHashJoinPath applies to the
	// serial figure.
	cost.Total += qualEvalCost(cp, len(residual), rows)

	addPartialPath(joinrel, &Path{
		Kind:          PathHashJoin,
		Jointype:      parser.JoinInner, // C-03a; see addHashJoinPath.
		Rel:           joinrel,
		Rows:          rows,
		Cost:          cost,
		DisabledNodes: disabledNodesFor(!cp.enableHashJoin, o, i),
		Children:      []*Path{o, i},
		HashKeys:      keys,
		Residual:      residual,
		// "A hashjoin never has pathkeys" (pathnode.c:2879).
		Pathkeys:      nil,
		RequiredOuter: 0,
		// create_hashjoin_path (pathnode.c:2861-2866), field for field:
		//   parallel_safe   = consider_parallel && both inputs safe
		//   parallel_workers= outer_path->parallel_workers  ("a foolish way to
		//                     estimate parallel_workers, but for now…")
		//   parallel_aware  = consider_parallel && parallel_hash
		// The last conjunct differs deliberately: goopg's parallel-aware
		// mechanism is the shared prebuild, which applies to EVERY hash join in
		// a Gather's partial subtree, not only to a cooperatively-built one.
		// DESIGN.md §5.
		ParallelSafe:    parallelSafeWith(joinrel, o, i),
		ParallelWorkers: o.ParallelWorkers,
		ParallelAware:   true,
	}, "join.hash.partial")
}

// cheapestParallelSafeTotalInner is `get_cheapest_parallel_safe_total_inner`
// (pathkeys.c:699): "the unparameterized parallel-safe path with the least
// total cost".
//
// DIVERGENCE, recorded rather than hidden: upstream returns the FIRST path in
// the list satisfying the two predicates and relies on `pathlist`'s order to
// make that the cheapest; goopg scans for the minimum total, which is what the
// function's own name promises and which removes a dependence on
// addToPathlist's ordering that no test pins. Where the two would differ, goopg
// picks the cheaper — never a wrong answer.
//
// The `ParallelSafe` predicate is the load-bearing one, and not only for the
// reason its name suggests: `makeGatherPath` sets `ParallelSafe: false` (a
// Gather is the parallel/serial boundary) and `parallelSafeWith` ANDs its
// children's flags, so a path carrying a Gather ANYWHERE beneath it is excluded
// here. Without that, a Gather could land on the build side of a join whose
// build the leader runs inside `gatherOp.Open` — the shape
// `prebuildHashJoins`' "a Gather never appears inside another Gather's partial
// subtree" comment assumes away.
func cheapestParallelSafeTotalInner(paths []*Path) *Path {
	var best *Path
	for _, p := range paths {
		if p == nil || !p.ParallelSafe || p.RequiredOuter != 0 {
			continue
		}
		if best == nil || p.Cost.Total < best.Cost.Total {
			best = p
		}
	}
	return best
}
