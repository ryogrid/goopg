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

// generateHashJoinPaths adds both build-side orientations of a hash join over the
// two child rels' cheapest paths to joinRel, and add_path keeps the cheaper. The
// build side is charged as startup, so the orientation with the smaller inner
// side wins automatically — retiring the SmallDimension name-tag as the primary
// rule (design ch. 06 §2.1). setCheapest must have been called on the child rels.
func generateHashJoinPaths(joinRel, outer, inner *RelOptInfo, cp costParams, numHashClauses int) {
	o, i := outer.CheapestTotal, inner.CheapestTotal
	if o == nil || i == nil {
		return
	}
	// Child convention for a hash-join path: Children[0] is the probe (outer)
	// side, Children[1] is the build (inner) side. createPlan (C4) reads it to set
	// the executor Join's BuildLeft.
	// Orientation 1: build the inner side.
	addPath(joinRel, &Path{
		Kind:     PathHashJoin,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     hashJoinCost(cp, o.Cost, i.Cost, outer.Rows, inner.Rows, joinRel.Rows, numHashClauses),
		Children: []*Path{o, i},
	})
	// Orientation 2: build the outer side (swap the roles). The join output is
	// the same; only which side is hashed differs.
	addPath(joinRel, &Path{
		Kind:     PathHashJoin,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     hashJoinCost(cp, i.Cost, o.Cost, inner.Rows, outer.Rows, joinRel.Rows, numHashClauses),
		Children: []*Path{i, o},
	})
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
