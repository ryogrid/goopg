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
