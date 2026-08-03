package planner

// Path generation — building the candidate Paths for a relation and letting
// add_path prune them. See docs/design/cost-model/ chapter 06. Phase C3.2: the
// generation primitives as pure functions over RelOptInfo, unit-tested in
// isolation. They are NOT yet called from the live DP (that wiring, and the
// switch of selection from the integer cost to these pathlists, is C4), so they
// cannot change a plan.
//
// The build side and the parallel/serial choice are not decided by a private
// rule here: both hash-build orientations (and, from C4, merge / nestloop / MHJ)
// are generated and add_path keeps whichever is cheapest (design ch. 06 §2.1).
// The small dimension ends up on the build side because building it is cheaper,
// not because it was tagged by name.

// generateScanPaths adds the base-relation scan paths to rel: a serial SeqScan
// and, when the relation clears the parallel size ladder (parallelWorkers > 0), a
// partial SeqScan whose cost is divided by the parallel divisor (design ch. 06
// §1.1). relPages is the live block count; numQualOps is the per-tuple operator
// count of the scan's restriction qual.
func generateScanPaths(rel *RelOptInfo, cp costParams, relPages int64, numQualOps, parallelWorkers int, leaderParticipates bool) {
	seqCost := costSeqscan(cp, relPages, rel.Rows, numQualOps)
	addPath(rel, &Path{
		Kind:         PathSeqScan,
		Rel:          rel,
		Rows:         rel.Rows,
		Cost:         seqCost,
		ParallelSafe: parallelWorkers > 0,
	})
	if parallelWorkers > 0 {
		// Each worker processes ~1/d of the pages and tuples; the seq scan's
		// startup is zero, so dividing the total by the divisor is exact.
		d := getParallelDivisor(parallelWorkers, leaderParticipates)
		addPartialPath(rel, &Path{
			Kind:            PathSeqScan,
			Rel:             rel,
			Rows:            rel.Rows / d,
			Cost:            Cost{Startup: 0, Total: seqCost.Total / d},
			ParallelSafe:    true,
			ParallelWorkers: parallelWorkers,
		})
	}
}

// addHashJoinPath adds ONE hash-join orientation: `probe` streams, `build` is
// hashed. `keys` is the equality clause set the hash keys on (all of them —
// multi-column keys are the rule, not a special case, leftdeep-joins 05 §5) and
// `residual` the clauses the join evaluates on the tuples that survive the key
// match.
//
// Single-orientation is the primitive because that is the granularity the join
// search offers pairs at: `makeJoinRel` calls `add_paths_to_joinrel` once per
// input order (joinsearchlevel.go:335-340), so generating both here would
// produce each path twice. `generateHashJoinPaths` below is the two-orientation
// wrapper for callers that own both directions themselves.
//
// Child convention: Children[0] is the probe (outer) side, Children[1] is the
// build (inner) side. createPlan reads it to set the executor Join's BuildLeft.
func addHashJoinPath(joinRel, probe, build *RelOptInfo, cp costParams, keys, residual []*restrictInfo) {
	p, b := probe.CheapestTotal, build.CheapestTotal
	if p == nil || b == nil {
		return
	}
	cost := hashJoinCost(cp, p.Cost, b.Cost, probe.Rows, build.Rows, joinRel.Rows, len(keys))
	// The residual is evaluated only on tuples that already matched on the
	// keys, so it rides the join's OUTPUT cardinality (PG charges qpqual on
	// `hashjointuples`, costsize.c:4432).
	cost.Total += qualEvalCost(cp, len(residual), joinRel.Rows)
	addPath(joinRel, &Path{
		Kind:     PathHashJoin,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     cost,
		Children: []*Path{p, b},
		HashKeys: keys,
		Residual: residual,
	})
}

// addNestLoopPath adds a plain nested loop with `outer` driving: for every
// outer row the inner path is rescanned from scratch. It keys on nothing, so
// EVERY clause is residual — and it is evaluated on the full cross product,
// which is what makes this path correctly ruinous for two large inputs and the
// only available path for a cartesian pair.
//
// The inner rescan cost is the inner path's own total: no `Material` is
// interposed, because Material is a plan node placed by `cost_rescan` and is
// P5.7's (leftdeep-joins 04 §4 / the P4.3 ledger row). Until it lands this
// over-charges a rescan of a cheap inner, which biases against nested loops —
// the safe direction.
func addNestLoopPath(joinRel, outer, inner *RelOptInfo, cp costParams, quals []*restrictInfo) {
	o, i := outer.CheapestTotal, inner.CheapestTotal
	if o == nil || i == nil {
		return
	}
	cost := nestloopCost(cp, o.Cost, i.Cost, outer.Rows, joinRel.Rows, i.Cost.Total)
	cost.Total += qualEvalCost(cp, len(quals), outer.Rows*inner.Rows)
	addPath(joinRel, &Path{
		Kind:     PathNestLoop,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     cost,
		Children: []*Path{o, i},
		Residual: quals,
	})
}

// generateHashJoinPaths adds both build-side orientations of a hash join over the
// two child rels' cheapest paths to joinRel, and add_path keeps the cheaper. The
// build side is charged as startup, so the orientation with the smaller inner
// side wins automatically — retiring the SmallDimension name-tag as the primary
// rule (design ch. 06 §2.1). setCheapest must have been called on the child rels.
//
// The key set is orientation-independent — PG's `clause_sides_match_join`
// (joinpath.c:2205) accepts an equality whose operands land on the two sides in
// either order — so both calls pass the same `keys`/`residual`.
func generateHashJoinPaths(joinRel, outer, inner *RelOptInfo, cp costParams, keys, residual []*restrictInfo) {
	// Orientation 1: build the inner side.
	addHashJoinPath(joinRel, outer, inner, cp, keys, residual)
	// Orientation 2: build the outer side (swap the roles). The join output is
	// the same; only which side is hashed differs.
	addHashJoinPath(joinRel, inner, outer, cp, keys, residual)
}

// generateNLIPath adds a nested-loop-index-join path to joinRel: for each outer
// row, one index probe of the inner. `inner` must be a base relation the executor
// can index-probe on the join key (the DP checks this before calling, matching
// rewriteJoinsToNLI's conversion conditions, nl_index_join.go). It is cheap only
// when the outer side is small — for a large outer this cost is correctly ruinous,
// which is what a binary-hash-only cost model could not see (ch. 07 §4.5). The
// path is parameterized by the inner's index dependency on the outer key
// (RequiredOuter), design ch. 03 §3.1.
func generateNLIPath(joinRel, outer, inner *RelOptInfo, cp costParams) {
	o, i := outer.CheapestTotal, inner.CheapestTotal
	if o == nil || i == nil {
		return
	}
	addPath(joinRel, &Path{
		Kind:          PathNestLoop,
		Rel:           joinRel,
		Rows:          joinRel.Rows,
		Cost:          nestloopCost(cp, o.Cost, i.Cost, outer.Rows, joinRel.Rows, indexProbeCost(cp)),
		Children:      []*Path{o, i},
		RequiredOuter: inner.Relids, // the inner index depends on the outer key
	})
}

// generateMultiHashJoinPath adds a MultiHashJoin path to joinRel over one driving
// (probe) rel and several dimension (build) rels, under the ch. 06 §4.1
// comparability invariant. Competes in add_path against the equivalent hash
// cascade; the DP keeps whichever is cheaper. setCheapest must have run on every
// child rel.
func generateMultiHashJoinPath(joinRel, probe *RelOptInfo, dims []*RelOptInfo, cp costParams) {
	p := probe.CheapestTotal
	if p == nil {
		return
	}
	dimCosts := make([]Cost, 0, len(dims))
	dimRows := make([]float64, 0, len(dims))
	children := make([]*Path, 0, len(dims)+1)
	children = append(children, p)
	for _, d := range dims {
		if d.CheapestTotal == nil {
			return
		}
		dimCosts = append(dimCosts, d.CheapestTotal.Cost)
		dimRows = append(dimRows, d.Rows)
		children = append(children, d.CheapestTotal)
	}
	addPath(joinRel, &Path{
		Kind:     PathMultiHash,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     multiHashJoinCost(cp, p.Cost, probe.Rows, dimCosts, dimRows, joinRel.Rows),
		Children: children,
	})
}
