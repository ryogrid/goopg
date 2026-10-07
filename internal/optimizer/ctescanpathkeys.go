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
		var keys []PathKey
		if scan, ok := leafBaseScan(rel.baseLeaf).(*CTEScan); ok {
			if scan.Child == nil {
				continue
			}
			keys = s.cteScanPathkeys(rel, i, scan)
		} else if isSubplanLeaf(rel.baseLeaf) {
			keys = s.subqueryLeafPathkeys(rel, i)
		}
		if len(keys) == 0 {
			continue
		}
		for _, p := range rel.Pathlist {
			if p != nil && p.Kind == PathPrebuilt && p.RequiredOuter == 0 && len(p.Pathkeys) == 0 {
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
