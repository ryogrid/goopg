package optimizer

// joinpathsparallel.go — Phase 5 slice C-19f / P5-06 (take3 08 §8):
// `try_partial_hashjoin_path` (joinpath.c:1299) and the parallel block of
// `hash_inner_and_outer` (joinpath.c:2418-2477).
//
// This is the file that gives a JOINREL its own partial paths. Until it, every
// entry in `RelOptInfo.PartialPathlist` belonged to a BASE rel (C-19b's partial
// seq scan, C-19c's partial index scan), so a Gather chosen by C-19d's reader
// sat BELOW every join and the whole relation crossed the parallel boundary.
// C-19d §5.1 quantified what that costs IN GOOPG: `parallel_tuple_cost` × rows
// is 0.1/row while the saving is the scan's per-tuple CPU worker share
// ≈0.0094/row, so `add_path` dominates a base-rel Gather at any relation size
// and any selectivity.
//
// CORRECTION (2026-09-07, C-19d DESIGN §5.1a): that is a statement about goopg,
// not about PG, and this header used to draw the wrong inference from it ("PG
// escapes that arithmetic by putting the join below the Gather"). PG puts joins
// below the Gather, but its BASE-REL Gather wins on its own — `cost_seqscan`
// divides the CPU term over `baserel->tuples` (every tuple SCANNED) while
// `cost_gather` charges transfer on `baserel->rows` (the survivors CROSSING),
// two different numbers, so a selective scan has a crossover. goopg prices the
// base-rel scan on the post-restriction row count for BOTH, so it has none —
// pinned by `TestBaseRelGatherCannotWinAtAnySelectivity` and its PG-shaped
// twin, and fixed only by unifying the scan onto `rel->pages` / `rel->tuples`
// (ledger `c19-baserel-scan-priced-on-output-rows`).
//
// None of which changes what this file is for, only what it does NOT rescue: a
// partial hash join runs the join in the workers, so
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
import "strconv"

import "github.com/goopg/goopg/internal/parser"

func addPartialHashJoinPath(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams,
	jt parser.JoinType, keys, residual []*restrictInfo, bucket float64, final hashJoinFinalCostInput) {

	// The only reader of a partial path is `generateUsefulGatherPaths`, which
	// is gated by the same mode — so producing under `off` buys nothing and
	// costs one hashJoinCost per pair per direction per level on a search whose
	// planner time the pre-commit pgbench smoke measures. Under `top` the
	// producer must still run at EVERY level: the final rel's partial path
	// exists only because the levels below propagated theirs upward.
	if gatherPathsMode == gatherPathsOff {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V0", "jt="+traceJoinTypeName(jt))
		return
	}
	// R9 (plan-parity-fix-take2, K17): the direction filter this producer
	// never had. Without it a RIGHT hash join is filed as parallel-aware and
	// `assertParallelAwareJoinIsRunnable` panics at plan build — the join's
	// per-row verdict is not worker-local, so running it with a partial probe
	// would silently drop or duplicate rows. See partialHashJoinTypeOK for
	// why the set is {INNER, LEFT, SEMI, ANTI} and why it is pinned against
	// the executor's own predicate by test rather than by comment.
	if !partialHashJoinTypeOK(jt) {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V1", "jt="+traceJoinTypeName(jt))
		return
	}
	// `joinrel->consider_parallel` (joinpath.c:2418), already propagated by
	// joinrelConsiderParallel (= build_join_rel, relnode.c:829-845).
	if s == nil || !s.parallelModeOK || joinrel == nil || !joinrel.ConsiderParallel {
		sub := "cp"
		if s == nil {
			sub = "s-nil"
		} else if !s.parallelModeOK {
			sub = "mode"
		} else if joinrel == nil {
			sub = "nil-joinrel"
		}
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V2", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	if outer == nil || inner == nil || len(keys) == 0 {
		sub := "no-keys"
		if outer == nil {
			sub = "nil-outer"
		} else if inner == nil {
			sub = "nil-inner"
		}
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V3", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// `outerrel->partial_pathlist != NIL`, and `linitial` of it — the cheapest,
	// since addToPartialPathlist keeps ascending total-cost order exactly as
	// add_partial_path's `insert_at` does.
	if len(outer.PartialPathlist) == 0 {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V4", "jt="+traceJoinTypeName(jt))
		return
	}
	o := outer.PartialPathlist[0]
	if o == nil || o.ParallelWorkers <= 0 || !o.ParallelSafe {
		sub := "unsafe"
		if o == nil {
			sub = "head-nil"
		} else if o.ParallelWorkers <= 0 {
			sub = "workers"
		}
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V5", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// The path twin of `drivingScan`, asked HERE as well as at the Gather so a
	// shape the executor's per-worker attach walks do not model is never even
	// costed. `runWorker` IGNORES attachParallelScan's return value, so an
	// unmodelled subtree does not "stay serial" — every worker reads the whole
	// relation and the Gather returns N copies of every row.
	if !partialPathShapeIsGatherable(o) {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V6", "jt="+traceJoinTypeName(jt))
		return
	}

	// `get_cheapest_parallel_safe_total_inner` (pathkeys.c:699). Upstream first
	// tries `cheapest_total_inner` and falls back to this scan; the two are
	// folded here because CheapestTotal is itself on Pathlist and would be
	// found by the same scan.
	i := cheapestParallelSafeTotalInner(inner.Pathlist)
	if i == nil {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V7", "jt="+traceJoinTypeName(jt))
		return
	}

	// `try_partial_hashjoin_path`'s own refusals (:1315-1318). Upstream asserts
	// the outer is unparameterised and returns early on a parameterised inner;
	// both are refusals here, per addPartialPath's fail-closed convention —
	// a panic inside the planner would fail the statement, while a path not
	// offered simply cannot be chosen.
	if o.RequiredOuter != 0 || i.RequiredOuter != 0 {
		sub := "inner"
		if o.RequiredOuter != 0 {
			sub = "outer"
		}
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V8a", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	if calcNonNestloopRequiredOuter(o, i) != 0 {
		tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V8b", "jt="+traceJoinTypeName(jt))
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
		final:           final,
		outerCols:       pathNCols(o), innerCols: pathNCols(i),
		outerAvgVarBytes: pathAvgVarBytes(o), innerAvgVarBytes: pathAvgVarBytes(i),
	})
	// The residual rides the join's OUTPUT cardinality, which for a partial
	// path is the per-worker one — the same rule addHashJoinPath applies to the
	// serial figure.
	cost.Total += qualEvalCost(cp, len(residual), rows)

	addPartialPath(joinrel, &Path{
		Kind:          PathHashJoin,
		Jointype:      jt, // C-03b; see addHashJoinPath.
		Rel:           joinrel,
		Rows:          rows,
		Cost:          cost,
		DisabledNodes: disabledNodesFor(!cp.enableHashJoin, o, i),
		Children:      []*Path{o, i},
		// R53 slice 1: the partition, in Children order.
		OuterRelids: outer.Relids,
		InnerRelids: inner.Relids,
		HashKeys:    keys,
		Residual:    residual,
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
	tracePVetoCtx(s, "hash", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "V9", "jt="+traceJoinTypeName(jt)+" workers="+strconv.Itoa(o.ParallelWorkers))
}

// addPartialMergeJoinPath is `try_partial_mergejoin_path`
// (joinpath.c:1145-1214) at one call site's inputs: the rel-level wrapper
// that selects the partial outer and the complete inner, then files through
// tryPartialMergeJoinPath. E-20 Cut 3.
//
// `outerKeys`/`innerKeys` are the site's merge orderings (PG's `outerkeys`/
// `innerkeys`): at the `sort_inner_and_outer` site they are the loop's
// chosen orderings, at the `match_unsorted_outer` site the outer is ordered
// by construction so `outerSortKeys` is nil. `resultKeys` is the ordering
// the join DELIVERS (`merge_pathkeys`): the loop's `outerKeys` at site 1,
// the outer's full ordering at site 2.
func addPartialMergeJoinPath(s *searchCtx, joinrel, outer, inner *RelOptInfo, cp costParams,
	jt parser.JoinType, resultKeys, outerSortKeys, innerSortKeys []PathKey,
	mergeClauses, residual []*restrictInfo, mergeTuplesFor func([]*restrictInfo) float64,
	scanSelFor func([]*restrictInfo) (float64, float64), paramSrc RelSet) {

	// Same mode gate as the hash twin: the only reader of a partial path
	// is `generateUsefulGatherPaths`, and under `off` producing buys
	// nothing while costing a mergeJoinCost per pair per direction per
	// level on a search whose planner time the pgbench smoke measures.
	if gatherPathsMode == gatherPathsOff {
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M0", "jt="+traceJoinTypeName(jt))
		return
	}
	// `joinrel->consider_parallel` (joinpath.c:2418, via the hash block's
	// guard which the merge block shares), already propagated by
	// joinrelConsiderParallel.
	if s == nil || !s.parallelModeOK || joinrel == nil || !joinrel.ConsiderParallel {
		sub := "cp"
		if s == nil {
			sub = "s-nil"
		} else if !s.parallelModeOK {
			sub = "mode"
		}
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M1", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	if outer == nil || inner == nil || len(mergeClauses) == 0 {
		sub := "no-clauses"
		if outer == nil {
			sub = "nil-outer"
		} else if inner == nil {
			sub = "nil-inner"
		}
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M2", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// `outerrel->partial_pathlist != NIL`, cheapest only. This wrapper serves
	// the `sort_inner_and_outer` site, where PG itself passes the singular
	// `cheapest_partial_outer` (joinpath.c:1535-1545) — so following the
	// hash twin's `[0]` rule here is PG-faithful, not merely sibling-
	// faithful. The `match_unsorted_outer` site is different: PG loops
	// over the WHOLE partial pathlist there (joinpath.c:2071) because the
	// cheapest partial is almost never the ORDERED one, and
	// `matchUnsortedOuterMergePartial` loops with it rather than calling
	// this wrapper. A producer enumerating more outers than every other
	// arm would change the search's shape for a reason unrelated to
	// parallelism — which is why NEITHER site enumerates beyond what PG
	// does at that site.
	if len(outer.PartialPathlist) == 0 {
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M3", "jt="+traceJoinTypeName(jt))
		return
	}
	o := outer.PartialPathlist[0]
	if o == nil || o.ParallelWorkers <= 0 || !o.ParallelSafe {
		sub := "unsafe"
		if o == nil {
			sub = "head-nil"
		} else if o.ParallelWorkers <= 0 {
			sub = "workers"
		}
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M4", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// `get_cheapest_parallel_safe_total_inner` (pathkeys.c:699), the same
	// scan the hash twin uses: the inner is read WHOLE by every worker
	// (no shared build exists for merge), so completeness is required and
	// partial-ness is not.
	i := cheapestParallelSafeTotalInner(inner.Pathlist)
	if i == nil {
		tracePVetoCtx(s, "merge", traceRelids(joinrel), traceRelids(outer), traceRelids(inner), "M5", "jt="+traceJoinTypeName(jt))
		return
	}
	tryPartialMergeJoinPath(s, joinrel, o, i, outer.Relids, inner.Relids, cp, jt, "merge", resultKeys, outerSortKeys, innerSortKeys,
		mergeClauses, residual, mergeTuplesFor, scanSelFor, paramSrc)
}

// tryPartialMergeJoinPath is `try_partial_mergejoin_path` proper over two
// chosen PATHS: the partial outer and the complete inner. It mirrors
// tryMergeJoinPath line for line, with three deliberate differences:
//
//   - outersortkeys/innersortkeys that are NOT already delivered DECLINE
//     instead of sorting. A per-worker sort is correct but unmodelled:
//     `partialPathDrivingKind` has no PathSort arm and `drivingScan` has no
//     *Sort arm, so a filed sort-under-partial would be priced and then
//     refused at the Gather — or worse, admitted and mis-executed. The
//     inner side is the exception it looks like: it is read whole by every
//     worker, never partitioned, so sorting it needs no driving — but the
//     same call must stay symmetrical with the serial twin to keep the two
//     from disagreeing about anything but the parallel terms, and PG offers
//     the no-sort shape first anyway (unsorted-outer site). Both sides
//     decline-on-sort here; widening the inner is a follow-up the A/B can
//     motivate, not a guess.
//   - the row estimate is the per-worker one: `clamp_row_est` over the
//     divisor (`final_cost_mergejoin`'s "For partial paths, scale row
//     estimate", costsize.c:3875-3881), and the merge-tuple and residual
//     charges ride that same divided figure, exactly as the hash twin's
//     outputRows does.
//   - ParallelAware is false: there is no shared prebuild for merge (each
//     worker sorts and reads the whole inner itself), so the flag whose
//     whole machinery is the hash twin's sharing protocol is not set.
// `site` is which merge site offers this candidate — "merge" for the
// `sort_inner_and_outer` wrapper above, "mergeu" for
// `matchUnsortedOuterMergePartial` — so the veto line attributes to the
// route, not just the refusal (R54 Step-1 H5).
func tryPartialMergeJoinPath(s *searchCtx, joinrel *RelOptInfo, o, i *Path, outerRelids, innerRelids RelSet, cp costParams, jt parser.JoinType,
	site string,
	resultKeys, outerSortKeys, innerSortKeys []PathKey, mergeClauses, residual []*restrictInfo,
	mergeTuplesFor func([]*restrictInfo) float64, scanSelFor func([]*restrictInfo) (float64, float64),
	paramSrc RelSet) {

	if o == nil || i == nil {
		sub := "nil-inner"
		if o == nil {
			sub = "nil-outer"
		}
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M6", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	// No-sort gate (see above): the ordering must already be delivered.
	if len(outerSortKeys) > 0 && !pathkeysContainedIn(o.Pathkeys, outerSortKeys) {
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M7", "osort="+strconv.Itoa(len(outerSortKeys))+" jt="+traceJoinTypeName(jt))
		return
	}
	if len(innerSortKeys) > 0 && !pathkeysContainedIn(i.Pathkeys, innerSortKeys) {
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M8", "isort="+strconv.Itoa(len(innerSortKeys))+" jt="+traceJoinTypeName(jt))
		return
	}
	// `calc_non_nestloop_required_outer` (joinpath.c:1071), stricter than
	// the serial twin: the hash twin refuses any nonzero requirement
	// outright (a partial path propagates a parameter rather than binding
	// it, and no worker can supply it), and this follows the sibling
	// rather than the serial arm's admit-if-wanted rule.
	if o.RequiredOuter != 0 || i.RequiredOuter != 0 {
		sub := "inner"
		if o.RequiredOuter != 0 {
			sub = "outer"
		}
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M9", "sub="+sub+" jt="+traceJoinTypeName(jt))
		return
	}
	if calcNonNestloopRequiredOuter(o, i) != 0 {
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M10", "jt="+traceJoinTypeName(jt))
		return
	}
	// The filed shape must be drivable END TO END, or it is never even
	// costed: `runWorker` ignores a failed attach and every worker reads
	// the whole relation, so an unmodelled subtree does not "stay serial"
	// (joinpathsparallel.go, hash-twin comment).
	if !partialPathShapeIsGatherable(o) {
		tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M11", "jt="+traceJoinTypeName(jt))
		return
	}

	// `final_cost_mergejoin` (costsize.c:3875-3881): "For partial paths,
	// scale row estimate." One divisor, applied here and undone by
	// cost_gather's computeGatherRows — not two. The merge-tuple charge
	// rides the same divided figure: the full-join tuples are what ALL
	// workers emit together, and each worker emits a 1/divisor share.
	divisor := getParallelDivisor(o.ParallelWorkers, cp.parallelLeaderParticipation)
	rows := clampRowEst(joinrel.Rows / divisor)
	mergeTuples := mergeTuplesFor(residual) / divisor
	outerEndSel, innerEndSel := scanSelFor(mergeClauses)
	// The partial outer's Rows is ALREADY the per-worker count
	// (costParallelSeqscan divides it); the inner's are the WHOLE inner,
	// read complete by every worker — the same asymmetry the hash twin
	// implements and `final_cost_mergejoin` prices.
	cost := mergeJoinCost(cp, o.Cost, i.Cost, o.Rows, i.Rows, mergeTuples, outerEndSel, innerEndSel)
	// The residual rides the join's OUTPUT cardinality, which for a partial
	// path is the per-worker one — the same rule the serial twin and the
	// hash twin apply.
	cost.Total += qualEvalCost(cp, len(residual), rows)

	addPartialPath(joinrel, &Path{
		Kind:          PathMergeJoin,
		Jointype:      jt,
		Rel:           joinrel,
		Rows:          rows,
		Cost:          cost,
		DisabledNodes: disabledNodesFor(!cp.enableMergeJoin, o, i),
		Children:      []*Path{o, i},
		// R53 slice 1: the partition, in Children order (relsets ride the
		// caller's parameters — the candidate paths carry none).
		OuterRelids: outerRelids,
		InnerRelids: innerRelids,
		HashKeys:    mergeClauses,
		Residual:    residual,
		// `build_join_pathkeys` of the delivered ordering (joinpath.c:1932
		// at the unsorted site; the loop's own ordering at site 1) — NOT
		// nil like the hash twin ("a hashjoin never has pathkeys",
		// pathnode.c:2879): a merge join delivers its outer's order, and
		// FULL/RIGHT deliver none.
		Pathkeys:      buildJoinPathkeys(jt, resultKeys),
		RequiredOuter: 0,
		// create_mergejoin_path field for field, minus parallel_aware:
		//   parallel_safe   = consider_parallel && both inputs safe
		//   parallel_workers= outer_path->parallel_workers
		ParallelSafe:    parallelSafeWith(joinrel, o, i),
		ParallelWorkers: o.ParallelWorkers,
		ParallelAware:   false,
	}, "join.merge.partial")
	tracePVetoCtx(s, site, traceRelids(joinrel), outerRelids, innerRelids, "M12", "jt="+traceJoinTypeName(jt)+" workers="+strconv.Itoa(o.ParallelWorkers))
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
