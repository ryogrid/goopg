package optimizer

// Path generation — building the candidate Paths for a relation and letting
// add_path prune them. See docs/design/cost-model/ chapter 06. Phase C3.2: the
// generation primitives as pure functions over RelOptInfo, unit-tested in
// isolation.
//
// # Live since M0127-P5.9 (2026-08-06)
//
// The C4 wiring this header used to say was still pending ("NOT yet called from
// the live DP … so they cannot change a plan") HAPPENED: `addPathsToJoinrel`
// (joinpaths.go:197) calls `addHashJoinPath` for every pair the live search
// offers, and `GOOPG_PGSHAPED_DP` defaults ON. These primitives generate the
// paths production selects from.
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
	// Row counts come from the CHILD PATHS, not their RelOptInfos: for a
	// parameterised path the two differ, and the path's own count is the
	// per-parameterisation one (PG's ppi_rows; cost_hashjoin reads
	// outer_path->rows / inner_path->rows, costsize.c:3563-3564). See
	// leftdeep-joins 03 §9 rule 3 and Path.Rows.
	cost := hashJoinCost(cp, hashJoinInputs{
		outer: p.Cost, inner: b.Cost,
		outerRows: p.Rows, innerRows: b.Rows,
		outputRows:     joinRel.Rows,
		numHashClauses: len(keys),
		// Column counts come from the RELS, not the paths: a parameterised
		// path returns fewer ROWS than its rel but the same columns.
		outerCols: relNCols(probe), innerCols: relNCols(build),
		// AvgVarBytes come from the rels for the same reason (M0128-P3.1).
		outerAvgVarBytes: probe.AvgVarBytes, innerAvgVarBytes: build.AvgVarBytes,
	})
	// The residual is evaluated only on tuples that already matched on the
	// keys, so it rides the join's OUTPUT cardinality (PG charges qpqual on
	// `hashjointuples`, costsize.c:4432).
	cost.Total += qualEvalCost(cp, len(residual), joinRel.Rows)
	addPath(joinRel, &Path{
		Kind:          PathHashJoin,
		Rel:           joinRel,
		Rows:          joinRel.Rows,
		Cost:          cost,
		Children:      []*Path{p, b},
		HashKeys:      keys,
		Residual:      residual,
		RequiredOuter: calcNonNestloopRequiredOuter(p, b),
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
	// Child-path row counts, per 03 §9 rule 3 (see addHashJoinPath). The cross
	// product a plain nested loop evaluates its quals on is therefore
	// `o.Rows * i.Rows`, which for a parameterised inner is the per-outer-row
	// count — exactly PG's cost_nestloop (costsize.c:3355-3356).
	cost := nestloopCost(cp, o.Cost, i.Cost, o.Rows, i.Rows, i.Cost.Total)
	cost.Total += qualEvalCost(cp, len(quals), o.Rows*i.Rows)
	addPath(joinRel, &Path{
		Kind:     PathNestLoop,
		Rel:      joinRel,
		Rows:     joinRel.Rows,
		Cost:     cost,
		Children: []*Path{o, i},
		Residual: quals,
		// A nested loop DISCHARGES an inner parameterised by the outer, so
		// this is a subtraction, not a union (pathnode.c:2592).
		RequiredOuter: calcNestloopRequiredOuter(outer.Relids, o.RequiredOuter, inner.Relids, i.RequiredOuter),
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

// The C1-era `generateNLIPath` used to live here. It was retired by
// M0127-P5.4b-ii-b-1: it priced every rescan at a flat `indexProbeCost`
// regardless of which inner path was actually being rescanned, which was the
// best a path-level NLI could do while no inner could be parameterised. The
// real arm is `addNLIPaths` (joinpathsnli.go), which reads the parameterised
// inner PATH's own cost — PG's `cost_rescan` — and there is deliberately only
// one NLI path constructor now, for the same reason 03 §5.2 insists on one
// operator constructor: two of them drift, and the drift is invisible until a
// plan is costed by one and built by the other.

// `generateMultiHashJoinPath` was here until M0127-P6.2. It added a
// PathMultiHash over one driving (probe) rel and several dimension (build)
// rels, to compete in add_path against the equivalent hash cascade. It never
// acquired a production caller — nothing in the PG-shaped search ever
// enumerated the N-way shape — so the DP only ever saw the cascade, which is
// the shape PG has. 0126-0011 §3 asked how this constructor should be
// disposed of; the answer is: deleted.
