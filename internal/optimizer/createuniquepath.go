package optimizer

import "github.com/goopg/goopg/internal/parser"

// createUniquePath is PG's `create_unique_path`
// (postgres/src/backend/optimizer/util/pathnode.c:1729): builds and caches a
// candidate that de-duplicates a SEMI join's RHS `rel` by its correlation
// columns (`sjinfo.SemiRhsExprs`). `joinIsLegal`'s `unique_ified` admission
// arm (M0142-0008c-2, joinrels.c:445-489, not yet wired) is the intended
// caller: once a rel can prove it is unique-ified, a SEMI join with it is
// legal against ANY outer, not only its syntactic `MinRighthand` — this is a
// LEGALITY relaxation, not a costing choice (design doc §16.1).
//
// Scope (M0142-0008c-1 — the design doc's item 1 of 4): only the cache field
// and this producer. The `joinIsLegal` wiring is -0008c-2, the two synthetic
// jointypes threading it through every join-path builder are -0008c-3, and
// the unique-index/distinct-subquery NOOP fast paths (pathnode.c:1932-1985)
// are -0008c-4 — this function therefore always takes the paid Sort+Unique
// route, never the free NOOP one.
//
// Reachable domain today: `sjinfo.SemiRhsExprs` is populated only by
// `existsUnnestSJInfo` (unnest.go), for the EXISTS/IN-unnest SEMI join whose
// RHS is the whole subquery body wrapped as ONE atomic search participant
// (design doc §16, the "atomic-RHS" note) — `makeSpecialJoinInfoScoped`
// leaves it empty because ordinary FROM-clause SEMI never reaches
// deconstruction (specialjoin.go's own comment). That atomic wrapping is a
// `PathPrebuilt` (newPrebuiltPath) over the subquery's already-planned Node,
// so `cr.Index` in each `SemiRhsExprs` entry is a position in THAT node's own
// `Output()` — requiring `subpath.Kind == PathPrebuilt` below is not a
// temporary restriction, it is the entire domain PG's own mechanism reaches
// for goopg today (SEMI/ANTI are planner-internal; only ANTI reaches ordinary
// deconstruction, per specialjoin.go).
//
// HASH is NOT implemented (a real, if currently unreached, gap — recorded in
// the deferral ledger as M0142-0008c-1a): PG's UNIQUE_PATH_HASH
// (pathnode.c:2026-2043) groups by `uniq_exprs` while passing every OTHER
// needed target-list column through ungrouped
// (postgres/src/backend/optimizer/plan/createplan.c:1796-1811's `groupColIdx`
// is a strict subset of the Agg's own tlist) — a shape goopg's executor
// cannot express: `*Distinct` hash-dedups on its FULL input row
// (`distinctOp`), never a column subset, and there is no other hash-keyed-
// subset-with-passthrough node to reuse. Building one is new executor
// surface, which contradicts this item's "reuse `Unique`/`DistinctOn`, no new
// executor code" boundary from the design doc's original sizing — found only
// once this producer was actually written. `existsUnnestSJInfo` always sets
// `SemiCanBtree`/`SemiCanHash` together (never independently), so
// `!SemiCanBtree && SemiCanHash` is unreachable for the one live caller and
// this gate never actually declines a real query today.
func createUniquePath(rel *RelOptInfo, subpath *Path, sjinfo *SpecialJoinInfo, cp costParams) *Path {
	if rel == nil || subpath == nil || sjinfo == nil {
		return nil
	}
	// Cache: PG asserts `subpath == rel->cheapest_total_path` and returns
	// the cached result on a repeat call (pathnode.c:1748-1750). goopg has
	// no assertion story to mirror that with, so the cache check alone is
	// the contract; callers are responsible for passing `rel.CheapestTotal`.
	if rel.CheapestUnique != nil {
		return rel.CheapestUnique
	}
	if sjinfo.Jointype != parser.JoinSemi {
		return nil
	}
	// "If it's not possible to unique-ify, return NULL" (pathnode.c:1752-1753).
	if !sjinfo.SemiCanBtree {
		return nil
	}
	if len(sjinfo.SemiRhsExprs) == 0 {
		return nil
	}
	if sjinfo.SemiRhsProblemSpace {
		return createPulledUniquePath(rel, subpath, sjinfo, cp)
	}
	// See the domain note above: the only live producer of SemiRhsExprs
	// wraps its subquery body as a PathPrebuilt, and cr.Index is only
	// meaningful against THAT node's Output(). Any other Path kind is
	// unreachable today; decline rather than guess.
	if subpath.Kind != PathPrebuilt || subpath.node == nil {
		return nil
	}
	child := subpath.node
	out := child.Output()

	keyCols := make([]int, 0, len(sjinfo.SemiRhsExprs))
	for _, e := range sjinfo.SemiRhsExprs {
		cr, ok := e.(*ColumnRef)
		if !ok {
			// PG's uniq_exprs may be arbitrary expressions (pathnode.c
			// builds a fresh TargetEntry per uniqexpr); goopg's one live
			// producer only ever emits bare column refs
			// (existsUnnestSJInfo's params are always a *ColumnRef pair).
			// Decline on anything else rather than guess a position.
			return nil
		}
		if cr.Index < 0 || cr.Index >= len(out) {
			return nil
		}
		oc := out[cr.Index]
		if oc.Name != cr.Name || oc.SourceTableIdx != cr.SourceTableIdx {
			// The index no longer names the same column — the subpath's
			// schema drifted from what SemiRhsExprs was built against.
			// Safe-default decline (specialjoin.go's own "any uncertainty
			// falls back to the safe answer" rule).
			return nil
		}
		keyCols = append(keyCols, cr.Index)
	}

	// estimate_num_groups(uniq_exprs, rel->rows) — pathnode.c:1992-1996.
	numDistinct := float64(estimateNumGroups(sjinfo.SemiRhsExprs, child, int64(rel.Rows)))
	if numDistinct < 1 {
		numDistinct = 1
	}

	// cost_sort over rel->rows, keyed by the correlation columns
	// (pathnode.c:2001-2011); the sort key ORDER among them is otherwise
	// unconstrained (PG picks whichever ordering operator
	// get_ordering_op_for_equality_op finds), so ascending in SemiRhsExprs
	// order is as good as any other.
	keys := make([]SortKey, len(sjinfo.SemiRhsExprs))
	for i, e := range sjinfo.SemiRhsExprs {
		keys[i] = SortKey{Expr: e}
	}
	sortedInput := sortPathForBounded(subpath, pathkeysForSortKeys(keys), cp, -1)

	// "Charge one cpu_operator_cost per comparison per input tuple"
	// (pathnode.c:2015-2019) — added to TOTAL only, exactly as PG's
	// sort_path.total_cost += ... (startup is untouched: the sort already
	// blocks before any row is compared).
	cost := sortedInput.Cost
	cost.Total += cp.cpuOperatorCost * rel.Rows * float64(len(keyCols))

	uniquePath := &Path{
		Kind:          PathUnique,
		Rel:           rel,
		Rows:          numDistinct,
		Cost:          cost,
		DisabledNodes: sortedInput.DisabledNodes,
		UniqueKeyCols: keyCols,
		Children:      []*Path{sortedInput},
		// create_unique_path: parallel_safe = rel->consider_parallel &&
		// subpath->parallel_safe; parallel_workers = subpath's.
		ParallelSafe:    rel.ConsiderParallel && subpath.ParallelSafe,
		ParallelWorkers: subpath.ParallelWorkers,
	}
	rel.CheapestUnique = uniquePath
	return uniquePath
}

// createPulledUniquePath is createUniquePath for a SEMI join whose RHS is a
// pulled sublink body (jointreepullup.go), whose SemiRhsExprs are written in
// the join problem's column space.
//
// Only PG's UNIQUE_PATH_NOOP arm is ported for it (M0145-0008ab): an
// ANY_subquery whose sub-select `query_is_distinct_for` its output
// (postgres/src/backend/optimizer/util/pathnode.c:1955-1975). That path is the
// subpath itself — rows = rel->rows, the subpath's own costs and pathkeys — so
// returning subpath unchanged is exactly PG's node. Every other pulled RHS
// still declines: a base relation would need PG's own NOOP arm first
// (`relation_has_unique_index_for`, pathnode.c:1940) and the HASH method
// (ledger M0142-0008c-1a), and electing a paid Sort+Unique where PG takes one
// of those would be a divergence of goopg's own making.
func createPulledUniquePath(rel *RelOptInfo, subpath *Path, sjinfo *SpecialJoinInfo, cp costParams) *Path {
	if !sjinfo.SemiRhsDistinct {
		return createPulledBaseUniquePath(rel, subpath, sjinfo, cp)
	}
	// The RHS must be exactly one base leaf — the derived ANY_subquery — and
	// every uniq expr must name a column of it; anything else is a desync to
	// decline on, not a shape to guess at.
	if rel.baseLeaf == nil || subpath.Kind != PathPrebuilt || subpath.node == nil {
		return nil
	}
	out := subpath.node.Output()
	for _, e := range sjinfo.SemiRhsExprs {
		cr, ok := e.(*ColumnRef)
		if !ok {
			return nil
		}
		local := cr.Index - rel.baseOffset
		if local < 0 || local >= len(out) || out[local].Name != cr.Name {
			return nil
		}
	}
	rel.CheapestUnique = subpath
	return subpath
}

// createPulledBaseUniquePath is create_unique_path's paid arm for a pulled
// semijoin RHS that is ONE base relation (M0146-0005dk): estimate the
// distinct uniq-expr groups, price sort+unique (cost_sort plus one
// cpu_operator_cost per column per input row) and hashed aggregation
// (cost_agg AGG_HASHED with no aggregates, refused when
// (width + 64) * groups exceeds the hash memory limit), and keep the cheaper
// — fewer disabled nodes first, then total cost (pathnode.c). TPC-DS Q83
// unique-ifies `date_dim_8` by d_week_seq this way, and a plain
// `x IN (SELECT y FROM t)` over a small t hashes t's y values.
//
// PG's NOOP arm for a relation (relation_has_unique_index_for) is
// pre-empted: reduce_unique_semijoins (pulledSemiRhsIsUnique) already
// deletes the SpecialJoinInfo of a single-relation RHS whose unique index
// the equated columns cover, so no unique-ify is asked for. The proofs it
// does not port (a key completed by a restriction constant, a cross-type
// equality) would reach here and pay for a unique PG takes free — ledgered.
// A multi-relation RHS still declines.
func createPulledBaseUniquePath(rel *RelOptInfo, subpath *Path, sjinfo *SpecialJoinInfo, cp costParams) *Path {
	if subpath.RequiredOuter != 0 {
		return nil
	}
	if rel.baseLeaf == nil {
		return createPulledJoinUniquePath(rel, subpath, sjinfo, cp)
	}
	out := rel.baseLeaf.Output()
	// local: leaf-relative refs, for estimate_num_groups over the leaf.
	// bound: the same refs in problem space, which is what a Sort key and
	// the lowering's layout translation are written in.
	local := make([]Expr, 0, len(sjinfo.SemiRhsExprs))
	bound := make([]Expr, 0, len(sjinfo.SemiRhsExprs))
	keyCols := make([]int, 0, len(sjinfo.SemiRhsExprs))
	seen := map[int]bool{}
	for _, e := range sjinfo.SemiRhsExprs {
		cr, ok := e.(*ColumnRef)
		if !ok {
			return nil
		}
		i := cr.Index - rel.baseOffset
		if i < 0 || i >= len(out) || out[i].Name != cr.Name {
			return nil
		}
		if seen[i] {
			// The same column equated twice is one sort/group column
			// (make_pathkeys_for_sortclauses drops the redundant one).
			continue
		}
		seen[i] = true
		lc := *cr
		lc.Index = i
		local = append(local, &lc)
		bound = append(bound, cr)
		keyCols = append(keyCols, i)
	}
	if len(keyCols) == 0 {
		return nil
	}
	numGroups := float64(estimateNumGroups(local, rel.baseLeaf, int64(rel.Rows)))
	return finishPulledUniquePath(rel, subpath, keyCols, bound, numGroups, cp)
}

// createPulledJoinUniquePath is create_unique_path for a pulled semijoin RHS
// made of several base relations (M0146-0005dy): populate_joinrel_with_paths'
// JOIN_SEMI arm (joinrels.c) unique-ifies any rel equal to the semijoin's
// syn_righthand, so TPC-DS Q69's `store_sales ⋈ date_dim` is grouped on
// ss_customer_sk and probed into customer_pkey. Each uniq expr is resolved
// to the member base relation that produces it, and the group count is
// estimate_num_groups' per-relation product (selfuncs.c), each relation's
// estimate taken over its own leaf, clamped to the RHS's rows.
func createPulledJoinUniquePath(rel *RelOptInfo, subpath *Path, sjinfo *SpecialJoinInfo, cp costParams) *Path {
	members := rel.memberRels
	if len(members) == 0 {
		return nil
	}
	bound := make([]Expr, 0, len(sjinfo.SemiRhsExprs))
	keyCols := make([]int, 0, len(sjinfo.SemiRhsExprs))
	locals := map[*RelOptInfo][]Expr{}
	var order []*RelOptInfo
	seen := map[int]bool{}
	for _, e := range sjinfo.SemiRhsExprs {
		cr, ok := e.(*ColumnRef)
		if !ok {
			return nil
		}
		var owner *RelOptInfo
		local := -1
		for _, m := range members {
			if m == nil || m.baseLeaf == nil {
				return nil
			}
			out := m.baseLeaf.Output()
			if i := cr.Index - m.baseOffset; i >= 0 && i < len(out) && out[i].Name == cr.Name {
				owner, local = m, i
				break
			}
		}
		if owner == nil {
			return nil
		}
		if seen[cr.Index] {
			continue
		}
		seen[cr.Index] = true
		lc := *cr
		lc.Index = local
		if _, ok := locals[owner]; !ok {
			order = append(order, owner)
		}
		locals[owner] = append(locals[owner], &lc)
		bound = append(bound, cr)
		keyCols = append(keyCols, cr.Index)
	}
	if len(keyCols) == 0 {
		return nil
	}
	numGroups := 1.0
	for _, m := range order {
		numGroups *= float64(estimateNumGroups(locals[m], m.baseLeaf, int64(rel.Rows)))
	}
	if numGroups > rel.Rows {
		numGroups = rel.Rows
	}
	return finishPulledUniquePath(rel, subpath, keyCols, bound, numGroups, cp)
}

// finishPulledUniquePath prices create_unique_path's two methods over a
// pulled RHS and keeps the cheaper — sort+unique (cost_sort plus one
// cpu_operator_cost per column per input row) or hashed aggregation
// (cost_agg AGG_HASHED, refused when (width + 64) * groups exceeds the hash
// memory limit), fewer disabled nodes first (pathnode.c).
func finishPulledUniquePath(rel *RelOptInfo, subpath *Path, keyCols []int, bound []Expr, numGroups float64, cp costParams) *Path {
	if numGroups < 1 {
		numGroups = 1
	}
	numCols := float64(len(keyCols))

	keys := make([]SortKey, len(bound))
	for i, e := range bound {
		keys[i] = SortKey{Expr: e}
	}
	sortedInput := sortPathForBounded(subpath, pathkeysForSortKeys(keys), cp, -1)
	sortCost := sortedInput.Cost
	sortCost.Total += cp.cpuOperatorCost * rel.Rows * numCols
	best := &Path{
		Kind:          PathUnique,
		Rel:           rel,
		Rows:          numGroups,
		Cost:          sortCost,
		DisabledNodes: sortedInput.DisabledNodes,
		UniqueKeyCols: keyCols,
		UniqueExprs:   bound,
		Children:      []*Path{sortedInput},
		// create_unique_path: parallel_safe = rel->consider_parallel &&
		// subpath->parallel_safe. A parallel-unsafe unique path would make
		// every join over it lose add_path's parallel-safety tie-break to
		// the semi join it competes with.
		ParallelSafe:    rel.ConsiderParallel && subpath.ParallelSafe,
		ParallelWorkers: subpath.ParallelWorkers,
	}

	if hashEntry := float64(pathWidth(subpath) + 64); hashEntry*numGroups <= float64(cp.workMem) {
		aggCost := costAgg(cp, AggStrategyHashed, rel.Rows, subpath.Cost.Startup, subpath.Cost.Total,
			len(keyCols), numGroups, 0, pathWidth(subpath))
		if subpath.DisabledNodes < best.DisabledNodes ||
			(subpath.DisabledNodes == best.DisabledNodes && aggCost.Total < best.Cost.Total) {
			best = &Path{
				Kind:            PathUnique,
				Rel:             rel,
				Rows:            numGroups,
				Cost:            aggCost,
				DisabledNodes:   subpath.DisabledNodes,
				UniqueKeyCols:   keyCols,
				UniqueExprs:     bound,
				UniqueHashed:    true,
				Children:        []*Path{subpath},
				ParallelSafe:    rel.ConsiderParallel && subpath.ParallelSafe,
				ParallelWorkers: subpath.ParallelWorkers,
			}
		}
	}
	rel.CheapestUnique = best
	return best
}
