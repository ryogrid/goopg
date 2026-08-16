package executor

import "github.com/goopg/goopg/internal/optimizer"

// M0127-P4.2 (design leftdeep-joins/07 §3): outer-join fill for the hash join.
//
// PostgreSQL's hash join decides, once per join, which of the two inputs may
// emit rows that matched nothing: `HJ_FILL_OUTER` for the probe (outer) side and
// `HJ_FILL_INNER` for the hash (inner) side (nodeHashjoin.c's macros of the same
// names). goopg had only the first half, and only in the one orientation the
// planner ever produced — `LEFT JOIN` with the preserved side on the probe. That
// gap is why `RIGHT`/`FULL` were pinned onto the merge join by planning rule
// (planner.go's join-algorithm switch), which in turn made every RIGHT/FULL join
// inherit merge's sort of both inputs whether or not either side was already
// ordered.
//
// The two halves are named from the executor's own vocabulary rather than
// PG's outer/inner, because goopg's `BuildLeft` makes "outer" ambiguous:
//
//   - fillProbeSide — a probe row that matched nothing is emitted with the build
//     side null-padded. Streaming, per probe row, no extra state (PG
//     HJ_FILL_OUTER).
//   - fillBuildSide — a build row that no probe row ever matched is emitted with
//     the probe side null-padded. This needs the matched bitmap and the
//     post-probe sweep below (PG HJ_FILL_INNER + HJ_FILL_INNER_TUPLES).
//
// FULL is exactly both at once, which is why it costs the sweep and RIGHT (in
// the orientation the planner picks: build on the non-preserved left, probe the
// preserved right) does not.

// hashBuildIsLeft reports the orientation the lazy-hash runtime will actually
// use. It is NOT `plan.BuildLeft`: buildLazyHashTable forces the build onto the
// right for Semi/Anti so the probe stream stays the emit-once outer side, and a
// fill decision taken from the raw flag would disagree with the table that got
// built.
func (o *joinOp) hashBuildIsLeft() bool {
	if o.plan.Type == optimizer.JoinTypeSemi || o.plan.Type == optimizer.JoinTypeAnti {
		return false
	}
	return o.plan.BuildLeft
}

// fillProbeSide reports whether an unmatched probe row still emits.
func (o *joinOp) fillProbeSide() bool {
	switch o.plan.Type {
	case optimizer.JoinTypeLeft:
		return !o.hashBuildIsLeft()
	case optimizer.JoinTypeRight:
		return o.hashBuildIsLeft()
	case optimizer.JoinTypeFull:
		return true
	}
	return false
}

// fillBuildSide reports whether an unmatched build row still emits — the half
// that needs the bitmap and the sweep.
func (o *joinOp) fillBuildSide() bool {
	switch o.plan.Type {
	case optimizer.JoinTypeLeft:
		return o.hashBuildIsLeft()
	case optimizer.JoinTypeRight:
		return !o.hashBuildIsLeft()
	case optimizer.JoinTypeFull:
		return true
	}
	return false
}

// matchedStrBucket returns the matched bitmap parallel to lazyHash[key],
// creating it on first probe of that key. Buckets never probed have no bitmap at
// all, which is both the cheap and the correct default: every row in them is
// unmatched.
//
// The bitmap is a []bool rather than 07 §3's suggested []uint64 because it is
// per BUCKET, not per table: goopg's build files rows into `map[K][]Row`, so a
// packed word would cost a 24-byte slice header plus a full word for the
// one-to-three-row buckets that dominate an equi-join. Sized by row COUNT, the
// same shape M0127-P4.1's `mergeJoinStream.groupMatched` uses.
func (o *joinOp) matchedStrBucket(key string, n int) []bool {
	if o.lazyMatchedS == nil {
		o.lazyMatchedS = make(map[string][]bool)
	}
	m := o.lazyMatchedS[key]
	if len(m) != n {
		m = make([]bool, n)
		o.lazyMatchedS[key] = m
	}
	return m
}

func (o *joinOp) matchedIntBucket(key int64, n int) []bool {
	if o.lazyMatchedI == nil {
		o.lazyMatchedI = make(map[int64][]bool)
	}
	m := o.lazyMatchedI[key]
	if len(m) != n {
		m = make([]bool, n)
		o.lazyMatchedI[key] = m
	}
	return m
}

// recordBuildNullKey retains a build row whose join key was NULL. Such a row can
// never match a probe row (equality is never true against NULL), so the build
// loops drop it — correct for every join type except the two that must emit it
// null-extended.
//
// PG keeps these rows in the hash table itself: ExecHashTableCreate sets
// `hashtable->keepNulls = HJ_FILL_INNER(node)` and MultiExecPrivateHash inserts
// null-keyed tuples when it is set, so they spill and sweep with everything
// else. goopg cannot: its buckets are keyed by the key's canonical form, and a
// synthetic key would risk colliding with a real one. They are held in a plain
// list instead and swept after the last batch (deferral ledger 2026-08-04
// M0127-P4.2 — the list is not work_mem-bounded).
func (o *joinOp) recordBuildNullKey(row Row) {
	if !o.fillBuildSide() {
		return
	}
	o.fillNullBuild = append(o.fillNullBuild, row)
}

// fillSweepReset drops the sweep cursor so the next batch is swept from the
// start. The matched bitmaps go with the hash table (resetHashTable), not here:
// a batch's bitmaps must survive until its own sweep has run.
func (o *joinOp) fillSweepReset() {
	o.sweepInit = false
	o.sweepRows = nil
	o.sweepMatched = nil
	o.sweepKeyIdx = 0
	o.sweepRowIdx = 0
}

// fillSweepNext yields the next unmatched build row of the RESIDENT hash table,
// or nil when this batch's sweep is done. PG's HJ_FILL_INNER_TUPLES state
// (nodeHashjoin.c:400), running once per batch after that batch's probe replay
// hits EOF (06 §2.5).
//
// The cursor is a snapshot of the map's KEYS, not of its rows: Go maps cannot be
// iterated across calls, and the key list is one slice header per distinct key
// while a row snapshot would be a second copy of the unmatched build side.
func (o *joinOp) fillSweepNext() TupleSlot { //nolint:ireturn
	if !o.sweepInit {
		o.sweepInit = true
		o.sweepKeyIdx, o.sweepRowIdx = 0, 0
		o.sweepKeysS = o.sweepKeysS[:0]
		o.sweepKeysI = o.sweepKeysI[:0]
		// Both maps are read: the single-key int lane fills lazyIntHash, the
		// string and composite lanes fill lazyHash, and demoteIntHash can move a
		// build from the first to the second mid-flight.
		for k := range o.lazyHash {
			o.sweepKeysS = append(o.sweepKeysS, k)
		}
		for k := range o.lazyIntHash {
			o.sweepKeysI = append(o.sweepKeysI, k)
		}
	}
	for {
		if o.sweepRowIdx < len(o.sweepRows) {
			i := o.sweepRowIdx
			o.sweepRowIdx++
			if i < len(o.sweepMatched) && o.sweepMatched[i] {
				continue
			}
			return o.emitBuildFill(o.sweepRows[i])
		}
		if o.sweepKeyIdx < len(o.sweepKeysS) {
			k := o.sweepKeysS[o.sweepKeyIdx]
			o.sweepKeyIdx++
			o.sweepRows, o.sweepMatched = o.lazyHash[k], o.lazyMatchedS[k]
			o.sweepRowIdx = 0
			continue
		}
		if j := o.sweepKeyIdx - len(o.sweepKeysS); j < len(o.sweepKeysI) {
			k := o.sweepKeysI[j]
			o.sweepKeyIdx++
			o.sweepRows, o.sweepMatched = o.lazyIntHash[k], o.lazyMatchedI[k]
			o.sweepRowIdx = 0
			continue
		}
		o.sweepRows, o.sweepMatched = nil, nil
		return nil
	}
}

// fillNullKeyNext yields the build rows whose key was NULL. They belong to no
// batch, so they are swept exactly once, after the last one.
func (o *joinOp) fillNullKeyNext() TupleSlot { //nolint:ireturn
	if o.fillNullIdx >= len(o.fillNullBuild) {
		return nil
	}
	row := o.fillNullBuild[o.fillNullIdx]
	o.fillNullIdx++
	return o.emitBuildFill(row)
}

// emitBuildFill composes one unmatched build row against a null-padded probe
// side. It reuses the same lazyVirtualOut the matched path emits through, so the
// column order (which BuildLeft decides — ensureLazyVirtual) cannot drift
// between the two.
func (o *joinOp) emitBuildFill(row Row) TupleSlot { //nolint:ireturn
	buildIsLeft := o.hashBuildIsLeft()
	nullProbe := o.lazyNullLeft
	if buildIsLeft {
		nullProbe = o.lazyNullRight
	}
	o.lazyProbeSlot.row = nullProbe
	o.lazyVirtualOut.sources[o.lazyProbeSrcIdx] = o.lazyProbeSlot
	o.lazyProbeSrc = o.lazyProbeSlot
	o.lazyBuildSlot.row = row
	// M0097-0060: FULL JOIN USING coalescing. The USING column of a build-only
	// row lives at the RIGHT position while `SELECT *` reads the LEFT one, so
	// the value has to be copied across — but only when the build side IS the
	// right side; with the build on the left the value is already where the
	// star expansion looks.
	if len(o.plan.UsingLeftCols) > 0 && !buildIsLeft {
		ms := o.lazyVirtualOut.Materialize()
		o.coalesceUsingRow(ms.row)
		return ms
	}
	return o.lazyVirtualOut
}
