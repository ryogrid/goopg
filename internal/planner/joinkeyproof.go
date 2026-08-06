package planner

// M0127-P5.6-f — `get_foreign_key_join_selectivity` (costsize.c:5651) for the
// LEGACY estimator, and the base-column resolver it needs.
//
// Design: leftdeep-joins
// [04](../../docs/design/leftdeep-joins/04-cost-and-cardinality.md) §3.1 (the
// FK/unique-superkey generalisation) and §3.3 (the key-implied row bound);
// [09](../../docs/design/leftdeep-joins/09-verification-and-acceptance.md) §5.4
// (the measurement that made it mandatory).
//
// WHY THIS FILE EXISTS SEPARATELY FROM joinrelsize.go. The same mechanism is
// already implemented, reviewed and unit-tested on the PG-shaped DP path
// (`superkeyJoinSelectivity`, joinrelsize.go). That path is no longer inert —
// P5.9 flipped `GOOPG_PGSHAPED_DP` ON by default on 2026-08-06, so BOTH arms
// now run in production and the sibling-paths rule below binds harder than when
// this file was written, not more weakly. Its inputs are `RelOptInfo`s and
// `restrictInfo`s — a coordinate space the production planner's finished plan
// tree does not have. The production estimator is `estimateJoin`
// (cardinality.go), which sees `*Join` nodes, merged left‖right ColumnRef
// indices, and no catalog at all. The ALGORITHM below is deliberately the same
// one, arm for arm, with joinrelsize.go's comments as its specification: what
// differs is only how the evidence is reached. Both must move together (the
// sibling-paths rule); the pointers between them are in each file's header.
//
// WHY IT MATTERS. Q9 equates `l_suppkey = ps_suppkey AND l_partkey =
// ps_partkey`. `estimateJoin` priced ONE of those pairs while
// `joinResidualSelectivity` excluded BOTH, so the joinrel read
// `6M · 800k / 10 000 = 481M` against an actual `5 997 241` and the search put
// it below the `part` filter instead of above it (09 §5.4). Pricing every pair
// the way `clauselist_selectivity` does swings it the OTHER way — the two
// marginal distincts multiply to `1/(200 000 · 10 000)`, i.e. ≈ 2 rows — which
// is exactly the error upstream avoids by removing the covered clauses and
// substituting ONE `1/ntuples` for the key as a whole. The two halves are one
// change; landing either alone is a measured regression.

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
)

// uniqueKeyColumnSets is the plan-build-time stamp behind `SeqScan.UniqueKeys`
// / `IndexScan.UniqueKeys`: the column list of every UNIQUE index on tbl, read
// through the planner's OWN catalog so the active database's index set is used
// (the dbOid hazard, cost-model/14 §2).
//
// A composite unique index is accepted as the evidence upstream accepts a
// composite FK for. PG derives no-fan-out from `root->fkey_list` and — for
// SINGLE-column keys only (`has_unique_index`, plancat.c:2244 requires
// `nkeycolumns == 1`) — from `vardata->isunique`; neither reaches Q9's
// two-column `partsupp` PK. The substitution reproduces
// `get_variable_numdistinct`'s `isunique ⇒ nd = ntuples` on the key as a whole
// and is the one cost-model/14 §2 argued for.
func uniqueKeyColumnSets(cat catalog.Catalog, tbl *catalog.Table) [][]string {
	if cat == nil || tbl == nil {
		return nil
	}
	var out [][]string
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx == nil || !idx.Unique || len(idx.Columns) == 0 {
			continue
		}
		cols := make([]string, len(idx.Columns))
		copy(cols, idx.Columns)
		out = append(out, cols)
	}
	return out
}

// baseColumnRef is one join-key operand resolved all the way down to the leaf
// scan it comes from.
//
// `scan` is the leaf `Node` itself and is what identifies the RELATION
// INSTANCE — not `table`. A self-join (`nation n1, nation n2`, Q8) puts the
// SAME `*catalog.Table` pointer behind two scans, and keying the superkey
// bookkeeping on the table would let `n1.a = x AND n2.b = y` look like a
// two-column key of one relation. Pointer identity of the scan node is the
// cheapest thing that cannot make that mistake.
type baseColumnRef struct {
	scan  Node
	table *catalog.Table
	col   string
	// uniqueKeys is the scan's stamped evidence, carried here so the prover
	// does not have to re-switch on the node type.
	uniqueKeys [][]string
	// rawRows is the relation's RAW, unfiltered tuple count — the divisor
	// upstream insists on ("we should use the raw table tuple count, not any
	// estimate of its filtered or joined size", costsize.c:5852). Dividing by
	// the filtered count instead would claim every surviving row still finds a
	// partner. 0 when unknown, which disqualifies the relation as evidence.
	rawRows float64
	// ndistinct is the column's ANALYZE distinct count, 0 when unavailable —
	// what `columnNDistinctForChild` publishes.
	ndistinct int64
	// stats is the column's whole pg_statistic-equivalent row (nil when the
	// relation was never ANALYZEd), carried so a caller that needs a slot
	// this struct does not flatten — the MCV list and the null fraction,
	// which M0127-P5.6-g's `eqjoinsel_semi` MCV arm reads — does not have to
	// go through `columnStatsForChild` to get it.
	//
	// That distinction is the point: `columnStatsForChild` is the third
	// member of the resolver family described on `resolveBaseColumn`, and it
	// is the one still missing the `*IndexScan` arm. Reading MCVs through it
	// would make a semi-join's match fraction depend on whether the planner
	// happened to pick an index scan for the input. Reading them here cannot.
	stats *catalog.ColumnStats
}

// resolveBaseColumn walks a child plan tree to the base relation behind the
// column at logical index `idx`.
//
// It is the THIRD member of the resolver family whose other two are
// `columnNDistinctForChild` (cardinality.go) and `columnStatsForChild`
// (selectivity.go): same arm list, same coordinate rule, three different
// answers about the same column. That trio is exactly the sibling-path shape
// hard-won rule #2 is about — the `*Project` remap and the `*Join` arm each
// went in to ONE of them first and the divergence was a live defect both times
// (M0125-0038, M0127-P5.6-e-ii/-e-iii). `columnNDistinctForChild` now delegates
// its base-relation arms to this function so two of the three can no longer
// drift; `columnStatsForChild` still has its own copy and is missing the
// `*IndexScan` arm, which is ledgered rather than fixed here (changing which
// nodes resolve MCV lists moves estimates, and this loop's measurement already
// has two variables).
//
// Coordinate rule, identical to both twins: a `*Join`'s Predicate and key
// coordinates count from the start of the merged left‖right schema, so an index
// at or past the left input's width belongs to the right child, shifted down.
func resolveBaseColumn(idx int, child Node) (baseColumnRef, bool) {
	switch x := child.(type) {
	case *SeqScan:
		return baseColumnOfTable(x, x.Table, x.UniqueKeys, idx)
	case *IndexScan:
		// Heap-fetching probe: output schema is the table's column order,
		// same as *SeqScan.
		return baseColumnOfTable(x, x.Table, x.UniqueKeys, idx)
	case *Filter:
		return resolveBaseColumn(idx, x.Child)
	case *Sort:
		return resolveBaseColumn(idx, x.Child)

	// M0125-0038 (C5): a join input is routinely Project-wrapped, and this
	// lookup returning nothing through the wrapper is what made every such
	// equi-join fall back to defaultEqSelectivity — the "equi-join key
	// contributes no selectivity" symptom (Q10's rows=131280740 is exactly
	// l·r·0.005). The remap through `Targets` is also what P5.6-e-ii had to
	// add to `columnStatsForChild`, whose arm passed `idx` straight through:
	// a reordering or narrowing Project then answered for ANOTHER column,
	// which is worse than answering for none.
	case *Project:
		if idx >= 0 && idx < len(x.Targets) {
			if cr, ok := x.Targets[idx].(*ColumnRef); ok {
				return resolveBaseColumn(cr.Index, x.Child)
			}
		}
	case *Limit:
		return resolveBaseColumn(idx, x.Child)
	case *LockRows:
		return resolveBaseColumn(idx, x.Child)
	case *Gather:
		return resolveBaseColumn(idx, x.Child)
	case *GatherMerge:
		return resolveBaseColumn(idx, x.Child)
	case *CTEScan:
		// The scan's schema is the body's output schema, position for
		// position.
		return resolveBaseColumn(idx, x.Child)

	// M0127-P5.6-e-iii added this arm to the ndistinct lookup (P5.6-e-ii to
	// the stats twin). Upstream resolves a join-level Var straight to its
	// base relation's pg_statistic row (`examine_variable`, selfuncs.c) and
	// so does this, by walking down the side the merged coordinate lands in.
	// It is what makes a multi-level PK-FK chain size correctly rather than
	// compounding: `(lineitem ⋈ orders) ⋈ customer` on custkey needs
	// `orders.custkey`'s ndistinct from BELOW the first join, and resolving
	// nothing there made the whole chain fall through to
	// defaultEqSelectivity. A SEMI/ANTI join's Output() is left-only, so its
	// indices never reach the shift.
	case *Join:
		if x.Left == nil || x.Right == nil {
			return baseColumnRef{}, false
		}
		lw := len(x.Left.Output())
		if lw == 0 {
			return baseColumnRef{}, false
		}
		if idx >= lw {
			return resolveBaseColumn(idx-lw, x.Right)
		}
		return resolveBaseColumn(idx, x.Left)
	}
	return baseColumnRef{}, false
}

// groupUniqueNDistinct is `examine_simple_variable`'s GROUP-BY / DISTINCT arm
// (selfuncs.c) followed by `get_variable_numdistinct`'s `isunique` branch
// (selfuncs.c:6338) — the ONLY thing upstream is willing to learn about a
// column that comes out of a grouped subquery.
//
// M0127-P5.6-g-ii. The item was filed as "the `*HashAggregate` arm for
// `resolveBaseColumn`", and the reason the arm as filed measured WORSE is that
// upstream does not have it. `examine_simple_variable`, having walked into a
// subquery RTE, reaches:
//
//	/* The same idea as with DISTINCT clause works for a GROUP-BY too */
//	if (subquery->groupClause)
//	{
//	    if (list_length(subquery->groupClause) == 1 &&
//	        targetIsInSortList(ste, InvalidOid, subquery->groupClause))
//	        vardata->isunique = true;
//	    /* cannot go further */
//	    return;
//	}
//
// — it propagates UNIQUENESS and refuses to propagate STATISTICS. Grouping
// mashes the underlying column's distribution beyond recognition (the MCV list
// and the histogram describe the pre-grouping multiset), but it cannot destroy
// the fact that one row survives per distinct group value. A resolver arm that
// handed the base column's `ndistinct` and `stats` up through the aggregate
// would be answering with the wrong relation's distribution, which is the
// P5.6-e-ii defect class in a new place. This function therefore does not
// return a `baseColumnRef` at all: there is no base column to return.
//
// What upstream then does with `isunique` is the second half:
//
//	if (vardata->isunique)
//	    stadistinct = -1.0 * (1.0 - stanullfrac);
//
// A negative `stadistinct` is a FRACTION of the relation's rows, so the answer
// is `rel->tuples · (1 - nullfrac)`, and `stanullfrac` is 0 here because there
// is no statistics tuple (upstream returned before finding one). The distinct
// count of the grouped output is therefore exactly its row count — which is the
// truth, not a heuristic: `GROUP BY c` emits one row per distinct `c`.
//
// The row count is this subtree's own `EstimateRows`, which is upstream's
// `vardata->rel->tuples` — the SUBQUERY relation's size,
// i.e. after its HAVING qual. That is what makes this arm carry a restriction
// on the inner side into a semi-join estimate: Q18's inner is
// `lineitem GROUP BY l_orderkey HAVING sum(l_quantity) > 313`, whose HAVING is
// a `*Filter` above the `*Aggregate`, and the filter's selectivity reaches the
// join only through this number.
//
// The single-grouping-column restriction is upstream's and is load-bearing, not
// conservatism: with two grouping columns neither one is unique on its own
// (Q20's `GROUP BY ps_partkey, ps_suppkey` is exactly that shape), so the
// counting argument does not exist.
// The structural walk runs BEFORE `EstimateRows`, not after: this is called
// from `columnNDistinctForChild`, which the join search runs on every candidate
// pair, and an unconditional `EstimateRows` would re-walk the whole child
// subtree on every coordinate that resolves to no base column at all.
func groupUniqueNDistinct(idx int, child Node) (int64, bool) {
	if !resolvesToGroupUniqueColumn(idx, child) {
		return 0, false
	}
	rows := EstimateRows(child)
	if rows <= 0 {
		return 0, false
	}
	return rows, true
}

// resolvesToGroupUniqueColumn walks the same wrapper arms `resolveBaseColumn`
// walks — the two must agree about which node a coordinate lands on, or they
// would be describing different columns (hard-won rule #2) — and reports
// whether the coordinate lands on the sole grouping/DISTINCT key of a node that
// makes it unique.
//
// It deliberately does NOT recurse through `*Join`: upstream's comment on the
// recursive call says so ("even if the underlying column is unique, the
// subquery may have joined to other tables in a way that creates duplicates"),
// and a join below a grouping node is on the far side of the grouping anyway.
func resolvesToGroupUniqueColumn(idx int, child Node) bool {
	switch x := child.(type) {
	case *Filter:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *Sort:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *Limit:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *LockRows:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *Gather:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *GatherMerge:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *CTEScan:
		return resolvesToGroupUniqueColumn(idx, x.Child)
	case *Project:
		if idx >= 0 && idx < len(x.Targets) {
			if cr, ok := x.Targets[idx].(*ColumnRef); ok {
				return resolvesToGroupUniqueColumn(cr.Index, x.Child)
			}
		}
	case *Aggregate:
		// `Aggregate`'s output is [group exprs..., aggregate calls...,
		// passthrough...], so the sole grouping column is output index 0 —
		// `targetIsInSortList(ste, InvalidOid, subquery->groupClause)` with a
		// one-element groupClause. A partial/final split does not change which
		// output column the grouping key is, but a PARTIAL node's rows are one
		// worker's share rather than the group count, so only a whole-input
		// aggregate can answer.
		return x.Mode != AggModePartial && len(x.GroupExprs) == 1 && idx == 0
	case *Distinct:
		// The DISTINCT half of the same upstream test: `list_length(
		// subquery->distinctClause) == 1`. goopg's `*Distinct` is whole-row
		// over its output schema, so "exactly one DISTINCT column" is a
		// one-column output.
		return len(x.Output()) == 1 && idx == 0
	case *DistinctOn:
		// "We do the test this way so that it works for cases involving
		// DISTINCT ON" — `SELECT DISTINCT ON (a) a, b` has a one-element
		// distinctClause naming `a`, so `a` is unique in the output while `b`
		// is not. `KeyCols` is that clause in goopg's coordinates.
		return len(x.KeyCols) == 1 && x.KeyCols[0] == idx
	}
	return false
}

// baseColumnOfTable is the leaf arm shared by the two scan node types.
func baseColumnOfTable(scan Node, tbl *catalog.Table, uniq [][]string, idx int) (baseColumnRef, bool) {
	if tbl == nil || idx < 0 || idx >= len(tbl.Columns) {
		return baseColumnRef{}, false
	}
	ref := baseColumnRef{scan: scan, table: tbl, col: tbl.Columns[idx].Name, uniqueKeys: uniq}
	if tbl.Stats != nil {
		if tbl.Stats.RowCount > 0 {
			ref.rawRows = float64(tbl.Stats.RowCount)
		}
		if idx < len(tbl.Stats.Columns) {
			ref.ndistinct = tbl.Stats.Columns[idx].NDistinct
			ref.stats = &tbl.Stats.Columns[idx]
		}
	}
	return ref, true
}

// soleBaseScan returns the ONE leaf scan a join input reduces to, or nil when
// the input is a join (or anything else) over more than one relation.
//
// It is the precondition of the key-implied row bound below: the counting
// argument needs the key relation to BE the whole side. If a join below has
// already duplicated the key relation's rows, an outer row matching a single
// key row can match several rows of the side and the bound gives nothing.
func soleBaseScan(n Node) Node {
	switch x := n.(type) {
	case *SeqScan:
		return x
	case *IndexScan:
		return x
	case *Filter:
		return soleBaseScan(x.Child)
	case *Sort:
		return soleBaseScan(x.Child)
	case *Project:
		return soleBaseScan(x.Child)
	case *Limit:
		return soleBaseScan(x.Child)
	case *LockRows:
		return soleBaseScan(x.Child)
	case *Gather:
		return soleBaseScan(x.Child)
	case *GatherMerge:
		return soleBaseScan(x.Child)
	}
	return nil
}

// joinSuperkeyEstimate is what the superkey pass tells `estimateJoin`. The
// field meanings are `superkeyEstimate`'s (joinrelsize.go) — see there for why
// `fired` cannot be recovered from `sel` after the fact.
type joinSuperkeyEstimate struct {
	sel     float64
	covered []bool
	fired   bool
	// rowsBound is the tightest STRUCTURAL upper bound on the join's output
	// implied by the proven keys, or +Inf when none is provable.
	rowsBound float64
}

// resolvedPair is one equi-pair with each end resolved to a base column
// INDEPENDENTLY of the other.
//
// Requiring BOTH ends was this mechanism's first shape and it was measured
// wrong (Q20's `partsupp ⋈ (SELECT … GROUP BY ps_partkey, ps_suppkey)`, whose
// right side is a HashAggregate no resolver can see through: the proof went
// unmade and the joinrel read 283 against 236 624 actual). The uniqueness
// argument only ever concerns the KEY side — `partsupp` is unique over
// `(ps_partkey, ps_suppkey)`, so each row of the other side matches at most one
// of its rows whatever that other side IS, and the pairs being join clauses of
// THIS join is already the guarantee that the other end lives on the other
// side. Only the declared-FK arm genuinely needs the far end's identity, since
// it has to find the referenced PARENT relation; that arm still demands it.
type resolvedPair struct {
	left, right     baseColumnRef
	leftOK, rightOK bool
}

// superkeyJoinEstimate is `get_foreign_key_join_selectivity` over a finished
// `*Join`: it removes from `pairs` every pair covered by a proven key on one
// side and returns 1/(that side's RAW tuple count) in their place, multiplied
// over each key it can prove.
//
// The three properties reproduced from upstream are spelled out in
// joinrelsize.go's `superkeyJoinSelectivity` header and hold identically here:
// the divisor is the RAW count; the WHOLE key must be covered ("if we failed to
// remove all the matching clauses we expected to find, chicken out",
// costsize.c:5760) while the CLAUSE list may be partial; and a pair is consumed
// once, so two overlapping keys cannot both charge for it. Largest divisor
// first, because a key on either side gives an upper bound and the estimate is
// the minimum of the available bounds.
func superkeyJoinEstimate(j *Join, pairs []JoinKeyPair) joinSuperkeyEstimate {
	est := joinSuperkeyEstimate{sel: 1.0, covered: make([]bool, len(pairs)), rowsBound: math.Inf(1)}
	if j == nil || j.Left == nil || j.Right == nil || len(pairs) == 0 {
		return est
	}
	leftWidth := len(j.Left.Output())
	if leftWidth == 0 {
		return est
	}

	resolved := make([]resolvedPair, len(pairs))
	live := false
	for i, p := range pairs {
		lcr, lok := p.Left.(*ColumnRef)
		rcr, rok := p.Right.(*ColumnRef)
		if !lok || !rok {
			continue
		}
		// A key column equated to an EXPRESSION rather than to a stored
		// column constrains nothing about fan-out, which is why only bare
		// ColumnRefs get this far (joinrelsize.go's `joinKeyPairOf` makes
		// the same test).
		l, okL := resolveBaseColumn(lcr.Index, j.Left)
		ridx := rcr.Index
		if ridx >= leftWidth {
			ridx -= leftWidth
		}
		r, okR := resolveBaseColumn(ridx, j.Right)
		if !okL && !okR {
			continue
		}
		resolved[i] = resolvedPair{left: l, right: r, leftOK: okL, rightOK: okR}
		live = true
	}
	if !live {
		return est
	}

	// The sides the bound may be taken against, resolved once.
	leftSole := soleBaseScan(j.Left)
	rightSole := soleBaseScan(j.Right)

	for {
		key, ok := bestProvableJoinKey(resolved, est.covered)
		if !ok {
			break
		}
		for _, i := range key.pairs {
			est.covered[i] = true
		}
		est.sel *= 1.0 / key.rawTuples
		est.fired = true
		// The key-implied bound (04 §3.3): every row of the OTHER side
		// matches at most one row of the key relation, so the join cannot
		// emit more rows than the other side brings — but only when the key
		// relation IS that whole side. `Rows` there is the POST-filter
		// estimate, because the rows it will actually bring are the ones
		// that survived its quals; unlike the DIVISOR (a match fraction,
		// hence raw) this is a count of probes.
		switch {
		case key.keyScan != nil && key.keyScan == leftSole:
			if b := float64(EstimateRows(j.Right)); b < est.rowsBound {
				est.rowsBound = b
			}
		case key.keyScan != nil && key.keyScan == rightSole:
			if b := float64(EstimateRows(j.Left)); b < est.rowsBound {
				est.rowsBound = b
			}
		}
	}
	est.sel = clampSelectivity(est.sel)
	return est
}

// provenJoinKey is one applicable key: which pairs it covers, what to divide
// by, and which SCAN the key makes unique.
//
// `keyScan` is not always the scan the constraint was declared on — for a
// declared FK it is the referenced PARENT, since that is the side the key makes
// unique — and it is the field the row bound reads, so the same asymmetry
// `rawTuples` encodes has to be encoded here too.
type provenJoinKey struct {
	pairs     []int
	rawTuples float64
	keyScan   Node
}

// bestProvableJoinKey finds the key with the largest divisor over the pairs not
// yet consumed. Scans are visited in pair order and each scan's candidate keys
// in stamped (catalog) order, so the answer does not move between runs.
func bestProvableJoinKey(resolved []resolvedPair, covered []bool) (provenJoinKey, bool) {
	// equated[scan] = the columns of that relation instance which a still
	// available pair equates to something on the other side of this join.
	type scanState struct {
		ref  baseColumnRef
		cols map[string]bool
	}
	order := make([]Node, 0, 2*len(resolved))
	state := make(map[Node]*scanState, 2*len(resolved))
	note := func(ref baseColumnRef) {
		st := state[ref.scan]
		if st == nil {
			st = &scanState{ref: ref, cols: make(map[string]bool)}
			state[ref.scan] = st
			order = append(order, ref.scan)
		}
		st.cols[ref.col] = true
	}
	for i, rp := range resolved {
		if covered[i] {
			continue
		}
		if rp.leftOK {
			note(rp.left)
		}
		if rp.rightOK {
			note(rp.right)
		}
	}

	var best provenJoinKey
	found := false
	consider := func(cand provenJoinKey) {
		if cand.rawTuples < 1 || len(cand.pairs) == 0 {
			return
		}
		if !found || cand.rawTuples > best.rawTuples {
			best, found = cand, true
		}
	}
	for _, scan := range order {
		st := state[scan]
		// A UNIQUE index on this relation makes the relation itself the key
		// side: each row of the other side matches at most one of its rows,
		// so the divisor is its OWN raw count.
		if st.ref.rawRows >= 1 {
			for _, key := range st.ref.uniqueKeys {
				if !columnsSubset(key, st.cols) {
					continue
				}
				consider(provenJoinKey{
					pairs:     coveringJoinPairs(resolved, covered, scan, key),
					rawTuples: st.ref.rawRows,
					keyScan:   scan,
				})
			}
		}
		// A FOREIGN KEY declared ON this relation makes it the CHILD side.
		// Each child row matches exactly one PARENT row, so the divisor is
		// the PARENT's raw count (`1.0 / ref_tuples`, costsize.c:5847) —
		// dividing by the child's instead would divide the fact table's
		// cardinality out of the join, the defect ledgered against
		// `uniqueNoFanoutRawCount` (bushy.go).
		if st.ref.table == nil {
			continue
		}
		for _, fk := range st.ref.table.ForeignKeys {
			if fk.NotValid || fk.NotEnforced || !columnsSubset(fk.Columns, st.cols) {
				continue
			}
			parent, ok := fkParentScan(resolved, covered, scan, fk)
			if !ok {
				continue
			}
			consider(provenJoinKey{
				pairs:     coveringJoinPairs(resolved, covered, scan, fk.Columns),
				rawTuples: parent.rawRows,
				keyScan:   parent.scan,
			})
		}
	}
	return best, found
}

// coveringJoinPairs returns the indexes of the still-available pairs that
// equate relation instance `scan`'s column set `key` to the other side, or nil
// unless EVERY key column has such a pair behind it — PG's chicken-out branch
// (costsize.c:5760), which is what makes a partial cover prove nothing.
func coveringJoinPairs(resolved []resolvedPair, covered []bool, scan Node, key []string) []int {
	want := make(map[string]bool, len(key))
	for _, c := range key {
		want[c] = true
	}
	var out []int
	seen := make(map[string]bool, len(key))
	for i, rp := range resolved {
		if covered[i] {
			continue
		}
		ends := make([]baseColumnRef, 0, 2)
		if rp.leftOK {
			ends = append(ends, rp.left)
		}
		if rp.rightOK {
			ends = append(ends, rp.right)
		}
		for _, end := range ends {
			if end.scan != scan || !want[end.col] {
				continue
			}
			out = append(out, i)
			seen[end.col] = true
			break
		}
	}
	if len(seen) != len(want) {
		return nil
	}
	return out
}

// fkParentScan resolves a declared FK's referenced side to a relation instance
// this join actually equates the child against.
//
// Matching through the pairs rather than through `fk.RefTable` alone is what
// keeps the proof tied to the join in front of us: a query may join the child
// to a DIFFERENT table of the same name in another schema, or not to the parent
// at all.
func fkParentScan(resolved []resolvedPair, covered []bool, child Node, fk catalog.ForeignKey) (baseColumnRef, bool) {
	for i, rp := range resolved {
		if covered[i] {
			continue
		}
		// Both ends must resolve here: this arm has to name the referenced
		// PARENT relation, which the unique-index arm never needs to.
		if !rp.leftOK || !rp.rightOK {
			continue
		}
		var other baseColumnRef
		switch {
		case rp.left.scan == child:
			other = rp.right
		case rp.right.scan == child:
			other = rp.left
		default:
			continue
		}
		if other.table == nil || other.table.Name != fk.RefTable || other.rawRows < 1 {
			continue
		}
		return other, true
	}
	return baseColumnRef{}, false
}

// ---------------------------------------------------------------------------
// M0127-P5.6-f-ii — the same proof, in the join-GRAPH coordinate space.
//
// Everything above proves keys over a FINISHED `*Join`, which is the space
// `estimateJoin` (cardinality.go) works in. The legacy join-order SEARCH never
// reaches that space: `estimateJoinCost` (bushy.go) runs DURING enumeration,
// over subset bitmasks and `joinEdge`s, and no `*Join` exists yet for the
// candidate it is pricing. That is why P5.6-f made Q9's joinrel estimate exact
// without moving its plan by a single node (09 §5.5) — the number the search
// selected on was computed by a DIFFERENT estimator, whose production branch
// (the integer DP) took `ndv` to be the maximum NDistinct across EVERY column
// of the edge's two tables, ignoring the join key entirely.
//
// The functions below are `superkeyJoinEstimate` / `bestProvableJoinKey` /
// `coveringJoinPairs` / `fkParentScan`, arm for arm, with the evidence reached
// differently: a relation instance is the graph's table INDEX rather than a
// leaf-scan pointer (which is the STRONGER identity — two arms of a self-join
// occupy two FROM-list positions and therefore two indices, so the `n1.a = x
// AND n2.b = y` confusion `baseColumnRef.scan` exists to prevent cannot arise
// here at all), and a key column is named by `edgeColName` off the edge's key
// expression instead of by walking a plan tree. Both must move together — this
// is the sibling-paths rule (#2), and the trio of column resolvers in this same
// file is the standing evidence of what happens when they don't.
//
// TWO DELIBERATE DIFFERENCES from the `*Join` sibling, both because the graph
// space supplies less:
//
//   - No `covered` list is returned. The `*Join` estimator hands its back to
//     `joinResidualSelectivity` so a proven pair is not ALSO priced as a
//     residual conjunct; the integer DP has no per-clause pricing to exclude
//     from — its non-proof path is a single table-wide maximum — so there is
//     nothing to subtract. Edges left uncovered by a proven key therefore
//     contribute no selectivity of their own (ledgered).
//   - No `rowsBound`. 04 §3.3's bound (the join cannot emit more rows than the
//     non-key side brings) is already IMPLIED here whenever it is valid: it
//     needs the key relation to BE the whole side, and in that case the side's
//     `dpEntry.rows` is that relation's own filtered count, which is ≤ its raw
//     count — so `leftRows·rightRows/raw ≤ rightRows` falls out of the divide.
//     When the key relation sits inside a larger subset the bound does not hold
//     anyway, which is exactly what `soleBaseScan` tests for over there.
//
// This replaces `uniqueNoFanoutRawCount` (deleted from bushy.go), whose FK arm
// divided by the CHILD's raw count where upstream divides by the PARENT's
// (`1.0 / ref_tuples`, costsize.c:5847) — dividing the fact table's own
// cardinality out of the join.

// graphKeyEnd is one END of a join edge resolved to (relation instance, base
// column name). `ok` false means the end named no base-table column, which
// disqualifies it as evidence without disqualifying the edge's other end.
type graphKeyEnd struct {
	ti  int
	col string
	ok  bool
}

// graphEndOf resolves one edge end. `ti` indexes `g.tables`.
func graphEndOf(ti int, key Expr, g *joinGraph) graphKeyEnd {
	if g == nil || ti < 0 || ti >= len(g.tables) || g.tables[ti] == nil {
		return graphKeyEnd{}
	}
	name, ok := edgeColName(key, g.tables[ti])
	if !ok {
		return graphKeyEnd{}
	}
	return graphKeyEnd{ti: ti, col: name, ok: true}
}

// graphProvenKey is one applicable key: which edges it covers and what to
// divide by. Unlike `provenJoinKey` it carries no key-relation identity,
// because the row bound that field feeds is implied here (see the header).
type graphProvenKey struct {
	edges     []int
	rawTuples int64
}

// graphJoinKeyDivisor is `superkeyJoinEstimate` for the search: it returns the
// product of the RAW tuple counts of every key it can prove over `edges`,
// consuming each edge once so two overlapping keys cannot both charge for it.
//
// The three upstream properties hold as they do in the `*Join` sibling: the
// divisor is the RAW, unfiltered count ("we should use the raw table tuple
// count, not any estimate of its filtered or joined size", costsize.c:5852);
// the WHOLE key must be covered or the proof chickens out (costsize.c:5760);
// and the largest divisor is taken first, because a key on either side gives an
// upper bound and the estimate is the minimum of the available bounds.
func graphJoinKeyDivisor(edges []*joinEdge, g *joinGraph, cat catalog.Catalog) (int64, bool) {
	if cat == nil || g == nil || len(edges) == 0 {
		return 0, false
	}
	ends := make([][2]graphKeyEnd, len(edges))
	for i, e := range edges {
		if e == nil {
			continue
		}
		ends[i][0] = graphEndOf(e.leftTable, e.leftKey, g)
		ends[i][1] = graphEndOf(e.rightTable, e.rightKey, g)
	}
	covered := make([]bool, len(edges))
	divisor := int64(1)
	fired := false
	for {
		key, ok := bestProvableGraphKey(ends, covered, g, cat)
		if !ok {
			break
		}
		for _, i := range key.edges {
			covered[i] = true
		}
		divisor = satMul(divisor, key.rawTuples)
		fired = true
	}
	if !fired {
		return 0, false
	}
	return divisor, true
}

// bestProvableGraphKey finds the key with the largest divisor over the edges
// not yet consumed. Relations are visited in edge order and each relation's
// candidate keys in catalog order, so the answer does not move between runs.
func bestProvableGraphKey(ends [][2]graphKeyEnd, covered []bool, g *joinGraph, cat catalog.Catalog) (graphProvenKey, bool) {
	// equated[ti] = the columns of relation instance ti which a still
	// available edge equates to something on the other side of this join.
	order := make([]int, 0, 2*len(ends))
	equated := make(map[int]map[string]bool, 2*len(ends))
	note := func(e graphKeyEnd) {
		if !e.ok {
			return
		}
		if equated[e.ti] == nil {
			equated[e.ti] = map[string]bool{}
			order = append(order, e.ti)
		}
		equated[e.ti][e.col] = true
	}
	for i := range ends {
		if covered[i] {
			continue
		}
		note(ends[i][0])
		note(ends[i][1])
	}

	var best graphProvenKey
	found := false
	consider := func(cand graphProvenKey) {
		if cand.rawTuples < 1 || len(cand.edges) == 0 {
			return
		}
		if !found || cand.rawTuples > best.rawTuples {
			best, found = cand, true
		}
	}
	for _, ti := range order {
		tbl := g.tables[ti]
		if tbl == nil {
			continue
		}
		cols := equated[ti]
		// A UNIQUE index on this relation makes the relation itself the key
		// side: each row of the other side matches at most one of its rows,
		// so the divisor is its OWN raw count. Read through the planner's own
		// catalog so the active database's index set is used (the dbOid
		// hazard, cost-model/14 §2).
		if tbl.Stats != nil && tbl.Stats.RowCount >= 1 {
			for _, key := range uniqueKeyColumnSets(cat, tbl) {
				if !columnsSubset(key, cols) {
					continue
				}
				consider(graphProvenKey{
					edges:     coveringGraphEdges(ends, covered, ti, key),
					rawTuples: tbl.Stats.RowCount,
				})
			}
		}
		// A FOREIGN KEY declared ON this relation makes it the CHILD side.
		// Each child row matches exactly one PARENT row, so the divisor is the
		// PARENT's raw count — the arm `uniqueNoFanoutRawCount` had backwards.
		for _, fk := range tbl.ForeignKeys {
			if fk.NotValid || fk.NotEnforced || !columnsSubset(fk.Columns, cols) {
				continue
			}
			parentRaw, ok := graphFKParentRaw(ends, covered, ti, fk, g)
			if !ok {
				continue
			}
			consider(graphProvenKey{
				edges:     coveringGraphEdges(ends, covered, ti, fk.Columns),
				rawTuples: parentRaw,
			})
		}
	}
	return best, found
}

// coveringGraphEdges returns the indexes of the still-available edges that
// equate relation instance ti's column set `key` to the other side, or nil
// unless EVERY key column has such an edge behind it — PG's chicken-out branch
// (costsize.c:5760), which is what makes a partial cover prove nothing.
func coveringGraphEdges(ends [][2]graphKeyEnd, covered []bool, ti int, key []string) []int {
	want := make(map[string]bool, len(key))
	for _, c := range key {
		want[c] = true
	}
	var out []int
	seen := make(map[string]bool, len(key))
	for i := range ends {
		if covered[i] {
			continue
		}
		for _, end := range ends[i] {
			if !end.ok || end.ti != ti || !want[end.col] {
				continue
			}
			out = append(out, i)
			seen[end.col] = true
			break
		}
	}
	if len(seen) != len(want) {
		return nil
	}
	return out
}

// graphFKParentRaw resolves a declared FK's referenced side to a relation
// instance this join actually equates the child against, and returns that
// PARENT's raw tuple count. Matching through the edges rather than through
// `fk.RefTable` alone keeps the proof tied to the join being priced: a query
// may join the child to a DIFFERENT table of the same name, or not to the
// parent at all.
func graphFKParentRaw(ends [][2]graphKeyEnd, covered []bool, child int, fk catalog.ForeignKey, g *joinGraph) (int64, bool) {
	for i := range ends {
		if covered[i] {
			continue
		}
		l, r := ends[i][0], ends[i][1]
		// Both ends must resolve here: this arm has to NAME the referenced
		// parent relation, which the unique-index arm never needs to.
		if !l.ok || !r.ok {
			continue
		}
		var other graphKeyEnd
		switch {
		case l.ti == child:
			other = r
		case r.ti == child:
			other = l
		default:
			continue
		}
		tbl := g.tables[other.ti]
		if tbl == nil || tbl.Name != fk.RefTable || tbl.Stats == nil || tbl.Stats.RowCount < 1 {
			continue
		}
		return tbl.Stats.RowCount, true
	}
	return 0, false
}
