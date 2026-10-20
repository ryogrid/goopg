package optimizer

// addCTEScanPathkeys gives every CTE-scan base relation's prebuilt path the
// ordering its CTE body delivers, translated into this query's terms — PG's
// set_cte_pathlist (allpaths.c), which since PG 17 hands the CTE scan path
//
//	pathkeys = convert_subquery_pathkeys(root, rel, ctepath->pathkeys, …)
//
// instead of leaving it unordered. The CTE's rows are materialised once in
// body order and every scan returns them in that order (the executor's
// CTERowCache), so the claim holds for each reference.
//
// Without it TPC-DS Q47's `v1` self-join could not merge-join its CTE scans
// without sorting them, where PG merge-joins them presorted (0..369). Once a
// stats-less hash key was priced at PG's default bucket, goopg's cheapest
// alternative became a cached-inner nested loop that times out
// (M0146-0005a/0005b).
//
// M0146-0100: every other sub-plan leaf (a derived table) gets the same
// conversion of its own plan's ordering — set_subquery_pathlist's
// convert_subquery_pathkeys (subqueryLeafPathkeys).
//
// Translation: inputNodePathkeys derives the body's output ordering in the
// body's output positions (it walks Filter/Limit/identity Project/WindowAgg
// down to the Sort or searched root that establishes it). Position k is the
// scan's output column k; that column is named in this query by the
// expression the join clauses (or the query pathkeys) use for it — the same
// "useful column" map the ordered index paths consult. Like
// convert_subquery_pathkeys, the translation stops at the first key with no
// counterpart in this query: an ordering prefix is still a true ordering, a
// gapped one is not.
func (s *searchCtx) addCTEScanPathkeys() {
	if s == nil {
		return
	}
	for i, rel := range s.levelRels(1) {
		if rel == nil || rel.baseLeaf == nil {
			continue
		}
		if len(rel.altLeaves) > 0 {
			s.addSubproblemAltPaths(rel)
		}
		var keys []PathKey
		if scan, ok := leafBaseScan(rel.baseLeaf).(*CTEScan); ok {
			if scan.Child == nil {
				continue
			}
			keys = s.cteScanPathkeys(rel, i, scan)
		} else if i < len(s.relInfos) && s.relInfos[i].subproblemAlts != nil {
			// M0146-0222: a searched sub-problem's WINNING tree delivers its
			// ordering too. PG's sub-joinlist joinrel keeps every path's
			// pathkeys, the cheapest one included. goopg filed only the
			// runner-ups' (addSubproblemAltPaths), so a sub-problem won by its
			// Gather Merge -> Sort lost the ordering at the boundary, and the
			// grouping step sorted again (TPC-DS Q72).
			keys = subproblemLeafPathkeys(rel.baseOffset, rel.baseLeaf)
		} else if isSubplanLeaf(rel.baseLeaf) {
			keys = s.subqueryLeafPathkeys(rel, i)
		}
		for _, p := range rel.Pathlist {
			if len(keys) > 0 && p != nil && p.Kind == PathPrebuilt && p.RequiredOuter == 0 && len(p.Pathkeys) == 0 {
				p.Pathkeys = keys
			}
		}
	}
}

// cteScanPathkeys is convert_subquery_pathkeys for one CTE-scan relation.
func (s *searchCtx) cteScanPathkeys(rel *RelOptInfo, relIdx int, scan *CTEScan) []PathKey {
	return s.convertLeafPathkeys(rel, relIdx, inputNodePathkeys(scan.Child), scan.Output())
}

// subqueryLeafPathkeys is set_subquery_pathlist's convert_subquery_pathkeys
// (allpaths.c) for a sub-plan leaf — a derived table, or any other finished
// sub-plan the join search treats as one relation (M0146-0100). PG gives each
// subquery path the ordering its subpath delivers, in the outer query's
// terms, so a merge join over a GroupAggregate'd derived table merges it
// presorted instead of sorting it (TPC-DS Q78's `ss` and `cs`). The ordering
// is the leaf plan's own, read by inputNodePathkeys in the leaf's output
// positions; a leaf that claims none (a hash aggregate, a plain Gather)
// publishes none.
//
// goopg plans the subquery once, so only the chosen plan's ordering is
// offered. PG also keeps the subquery's sorted alternatives beside its
// cheapest path; that second path is not built here (ledgered).
func (s *searchCtx) subqueryLeafPathkeys(rel *RelOptInfo, relIdx int) []PathKey {
	return s.convertLeafPathkeys(rel, relIdx, inputNodePathkeys(rel.baseLeaf), rel.baseLeaf.Output())
}

// convertLeafPathkeys translates a leaf's delivered ordering (positions in
// out) into this query's expressions, stopping at the first key nothing in
// this query names.
func (s *searchCtx) convertLeafPathkeys(rel *RelOptInfo, relIdx int, bodyKeys []PathKey, out Schema) []PathKey {
	if len(bodyKeys) == 0 {
		return nil
	}
	colExprs := mergeableColumnExprsFor(rel.Relids, s.clausesAll())
	if relIdx < len(s.relInfos) {
		addQueryPathkeyColumnExprs(colExprs, s.queryPathkeys, s.relInfos[relIdx].sourceIdx)
	}
	var keys []PathKey
	for _, bk := range bodyKeys {
		cr, ok := bk.Expr.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(out) {
			break
		}
		e, ok := colExprs[out[cr.Index].Name]
		if !ok {
			break
		}
		keys = append(keys, PathKey{Expr: e, SortAsc: bk.SortAsc, NullsFirst: bk.NullsFirst})
	}
	return keys
}

// addSubproblemAltPaths files a searched sub-problem leaf's ordered runner-up
// trees (RelOptInfo.altLeaves) as further prebuilt paths on its leaf rel,
// each priced like the winner's leaf (costSubplanLeaf) and carrying its own
// ordering (M0146-0148). PG's enclosing search reads the sub-problem's
// RelOptInfo, so its sorted Gather Merge competes there beside the Gather; an
// ordered path that costs a little more survives add_path on its pathkeys and
// lets the joins above it (and the grouping step) skip a Sort.
func (s *searchCtx) addSubproblemAltPaths(rel *RelOptInfo) {
	added := false
	for _, alt := range rel.altLeaves {
		keys := subproblemLeafPathkeys(rel.baseOffset, alt)
		if len(keys) == 0 {
			continue
		}
		p := newPrebuiltPath(rel, alt)
		p.Cost = costSubplanLeaf(s.cp, alt, rel.Rows)
		p.Pathkeys = keys
		addPath(rel, p, "joinsearch.prebuilt.alt")
		added = true
	}
	if added {
		setCheapest(rel)
	}
}

// subproblemLeafPathkeys translates a sub-problem tree's delivered ordering
// from its published row (positions 0..width-1, inputNodePathkeys) into the
// enclosing search's coordinates. The tree publishes the binding-order window
// that starts at the leaf's binding offset, so position i is coordinate
// off+i — the same coordinate the enclosing problem's clauses and the query
// pathkeys name that column by. The translation stops at the first key that
// is not a column of the row.
func subproblemLeafPathkeys(off int, n Node) []PathKey {
	out := n.Output()
	var keys []PathKey
	for _, bk := range inputNodePathkeys(n) {
		cr, ok := bk.Expr.(*ColumnRef)
		if !ok || cr.Index < 0 || cr.Index >= len(out) {
			break
		}
		col := out[cr.Index]
		keys = append(keys, PathKey{
			Expr:       &ColumnRef{Index: off + cr.Index, Name: col.Name, Type: col.Type, SourceTableIdx: col.SourceTableIdx},
			SortAsc:    bk.SortAsc,
			NullsFirst: bk.NullsFirst,
		})
	}
	return keys
}

// addSubqueryLeafAltPaths runs addSubqueryAltPaths over every initial rel
// that is not a searched sub-problem (those file their own runner-ups,
// addSubproblemAltPaths). It runs after setBaseRelConsiderParallel, as PG's
// set_rel_consider_parallel (set_rel_size) precedes set_subquery_pathlist:
// add_path compares the alternatives on a parallel_safe that already holds.
func (s *searchCtx) addSubqueryLeafAltPaths() {
	if s == nil {
		return
	}
	for i, rel := range s.levelRels(1) {
		if i < len(s.relInfos) && s.relInfos[i].subproblemAlts != nil {
			continue
		}
		s.addSubqueryAltPaths(rel)
	}
}

// addSubqueryAltPaths is set_subquery_pathlist's loop over the subquery's
// final rel (allpaths.c) for a derived or inlined-CTE leaf over a grouped
// subquery: every path that survived add_path on the subquery's GROUP_AGG
// rel becomes a further path of the leaf rel, beside the prebuilt winner
// (M0146-0231). PG keeps a parallel-safe GroupAggregate there beside the
// cheaper one over a Gather Merge, and an enclosing partial merge join takes
// it as its presorted inner (TPC-DS Q64's cs_ui, 0.72..42006.29).
//
// Each alternative is built here, as the winner's node was: the finished
// Aggregate (after its scope's own post-passes) with the alternative's input
// and strategy, under clones of the wrappers that sit between it and the
// leaf. An alternative whose aggregate spec is not the winner's (the
// index-narrowed variant remaps its group-input positions) or whose input
// publishes a different row is not offered. Each path's parallel_safe is
// create_subqueryscan_path's: the leaf rel's consider_parallel and the
// subquery path's own parallel_safe.
func (s *searchCtx) addSubqueryAltPaths(rel *RelOptInfo) {
	if rel == nil || rel.baseLeaf == nil {
		return
	}
	agg, rebuild := subqueryLeafAggregate(rel.baseLeaf)
	if agg == nil {
		return
	}
	rec, ok := groupedRelOf(s.cat, agg)
	if !ok || rec.best == nil || rec.best.Agg == nil {
		return
	}
	var winner *Path
	for _, p := range rel.Pathlist {
		if p.Kind == PathPrebuilt && p.node == rel.baseLeaf && p.RequiredOuter == 0 {
			winner = p
		}
	}
	added := false
	for _, p := range rec.rel.Pathlist {
		if p == rec.best || p.Kind != PathAgg || p.Agg == nil || len(p.Children) != 1 {
			continue
		}
		if !sameAggSpec(p.Agg, rec.best.Agg) {
			continue
		}
		child, _ := createPlanNode(p.Children[0])
		if child == nil || !sameSchemaShape(child.Output(), agg.Child.Output()) {
			continue
		}
		alt := *agg
		alt.Child = child
		alt.Strategy = p.AggStrategy
		// The winner's stamp came with the copy; the alternative carries its
		// own path's cost, as createAggPlan's node would.
		alt.PlanCost = PlanCost{}
		stampPlanCost(&alt, p)
		stampAggregateInputTarget(&alt, nil)
		leaf := rebuild(&alt)
		pp := newPrebuiltPath(rel, leaf)
		pp.ParallelSafe = rel.ParallelSafeForPath() && p.ParallelSafe && !planHasGatherNode(leaf)
		pp.Cost = costSubplanLeaf(s.cp, leaf, rel.Rows)
		// convert_subquery_pathkeys is a function of the subquery path's
		// pathkeys: an alternative ordered as the winner is converts to the
		// winner's leaf ordering.
		switch {
		case len(p.Pathkeys) == 0:
		case winner != nil && comparePathkeysDim(p.Pathkeys, rec.best.Pathkeys) == dimEqual:
			pp.Pathkeys = winner.Pathkeys
		default:
			if scan, ok := leafBaseScan(leaf).(*CTEScan); ok && scan.Child != nil {
				pp.Pathkeys = s.cteScanPathkeys(rel, relIndexOf(s, rel), scan)
			} else {
				pp.Pathkeys = s.convertLeafPathkeys(rel, relIndexOf(s, rel), inputNodePathkeys(leaf), leaf.Output())
			}
		}
		addPath(rel, pp, "joinsearch.prebuilt.subqueryalt")
		added = true
	}
	if added {
		setCheapest(rel)
	}
}

// relIndexOf is rel's position among the search's initial rels.
func relIndexOf(s *searchCtx, rel *RelOptInfo) int {
	for i, r := range s.levelRels(1) {
		if r == rel {
			return i
		}
	}
	return -1
}

// subqueryLeafAggregate finds the Aggregate a sub-plan leaf delivers through
// pass-through wrappers only — local-qual and HAVING Filters, Projects, the
// SubqueryScan label and an inlined CTE reference — and returns it with a
// function that rebuilds the leaf over a different Aggregate (each wrapper
// cloned, its stamped cost cleared so the clone is priced from its new
// input). nil when anything else sits on the way down.
func subqueryLeafAggregate(n Node) (*Aggregate, func(*Aggregate) Node) {
	switch x := n.(type) {
	case *Aggregate:
		if x.Mode != AggModeSimple || x.Child == nil {
			return nil, nil
		}
		return x, func(a *Aggregate) Node { return a }
	case *Filter:
		agg, below := subqueryLeafAggregate(x.Child)
		if agg == nil {
			return nil, nil
		}
		return agg, func(a *Aggregate) Node {
			c := *x
			c.PlanCost = PlanCost{}
			c.Child = below(a)
			return &c
		}
	case *Project:
		agg, below := subqueryLeafAggregate(x.Child)
		if agg == nil {
			return nil, nil
		}
		return agg, func(a *Aggregate) Node {
			c := *x
			c.Child = below(a)
			return &c
		}
	case *SubqueryScan:
		agg, below := subqueryLeafAggregate(x.Child)
		if agg == nil {
			return nil, nil
		}
		return agg, func(a *Aggregate) Node {
			c := *x
			c.Child = below(a)
			return &c
		}
	case *CTEScan:
		if !x.Inlined() {
			return nil, nil
		}
		agg, below := subqueryLeafAggregate(x.Child)
		if agg == nil {
			return nil, nil
		}
		return agg, func(a *Aggregate) Node {
			c := *x
			c.Child = below(a)
			return &c
		}
	}
	return nil, nil
}

// sameAggSpec reports whether two grouping candidates share one aggregate
// spec: the same group expressions and aggregates, not a remapped copy.
func sameAggSpec(a, b *Aggregate) bool {
	if len(a.GroupExprs) != len(b.GroupExprs) || len(a.Aggs) != len(b.Aggs) {
		return false
	}
	if len(a.GroupExprs) > 0 && &a.GroupExprs[0] != &b.GroupExprs[0] {
		return false
	}
	if len(a.Aggs) > 0 && &a.Aggs[0] != &b.Aggs[0] {
		return false
	}
	return a.GroupingSets == nil && b.GroupingSets == nil
}

// sameSchemaShape reports whether two rows carry the same columns in the
// same positions.
func sameSchemaShape(a, b Schema) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Type.Name != b[i].Type.Name || a[i].Type.IsArray != b[i].Type.IsArray {
			return false
		}
	}
	return true
}
