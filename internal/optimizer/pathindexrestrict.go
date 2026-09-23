package optimizer

// M0145-0029 slice 1 — UNPARAMETERISED restriction index paths: the arm of
// `build_index_paths` (postgres/src/backend/optimizer/path/indxpath.c:975)
// whose `index_clauses` come from the relation's OWN restriction clauses
// (`baserestrictinfo`, matched by `match_restriction_clauses_to_index`,
// indxpath.c:2240) rather than from join clauses.
//
// Before this file the search had no such path. Its base relations got index
// access only through bitmap paths (`pathbitmap.go`, equality only), ordering-
// only full index scans (`pathindexordered.go`) and index-only scans; a plain
// `Index Scan` driven by `WHERE col = const` existed only on the rule-based
// bypass (`planIndexScanFromWhere`, `rewriteScanInputsWithSingleTablePredicates`)
// that single-relation scopes took on the legacy pipeline. The jointree
// pipeline plans those scopes through the search, so the flip would have lost
// the path outright (the M0145-0008 flip triage, group I) — the same
// route-borne bypass loss as Q17/Q20.
//
// Slice 1 covers the EQUALITY prefix: `col = const` conjuncts bound to a
// gapless leading prefix of the index's columns (PG's btree `amoptionalkey`
// rule, the same prefix `pickIndexCoveringLeadingPrefix` binds for the
// parameterised arm). Range bounds and ScalarArrayOp (`IN (…)`) quals are the
// next slices — ledgered.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// addRestrictionIndexPaths generates, for every base relation, one plain index
// path per index whose leading columns are bound by the relation's local
// equality restrictions.
func (s *searchCtx) addRestrictionIndexPaths(cat catalog.Catalog) {
	if s == nil || cat == nil {
		return
	}
	totalPages := s.totalTablePages()
	for i, rel := range s.levelRels(1) {
		if i >= len(s.relInfos) {
			break
		}
		tbl := s.relInfos[i].table
		if tbl == nil {
			continue
		}
		// Same consumer-side eligibility gate as the other base-rel index
		// producers: no path over a leaf createPlan cannot rebuild.
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		conjuncts := extractFilterConjuncts(rel.baseLeaf)
		if len(conjuncts) == 0 {
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		var colExprs map[string]Expr
		if s.hasUsefulPathkeys(rel) {
			colExprs = mergeableColumnExprsFor(rel.Relids, s.clausesAll())
			addQueryPathkeyColumnExprs(colExprs, s.queryPathkeys, s.relInfos[i].sourceIdx)
		}
		added := false
		for _, idx := range cat.IndexesOnTable(tbl) {
			if s.addOneRestrictionIndexPath(cat, rel, tbl, idx, conjuncts, colExprs, relPages, relTuples, totalPages) {
				added = true
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

// restrictionEqualityPrefix binds `idx`'s gapless leading prefix to the local
// `col = const` conjuncts, returning the index clauses in index-column order.
// A column with no usable equality ends the prefix; nothing past it is an
// index qual (PG's btree `amoptionalkey` handling in `build_index_paths`).
func restrictionEqualityPrefix(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	var clauses []indexPathClause
	for pos, colName := range idx.Columns {
		var found *indexPathClause
		for _, conj := range conjuncts {
			bin, ok := conj.(*BinaryOp)
			if !ok || bin.Op != parser.OpEq {
				continue
			}
			cr, val, ok := normalizeColumnConst(bin.Left, bin.Right)
			if !ok {
				continue
			}
			// For a rebuildable base leaf the output positions are the table's
			// column positions — the same assumption the bitmap arm's
			// matchBitmapIndexQuals relies on.
			if cr.Index < 0 || cr.Index >= len(tbl.Columns) || tbl.Columns[cr.Index].Name != colName {
				continue
			}
			if !restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
				continue
			}
			found = &indexPathClause{indexCol: pos, key: val, local: conj}
			break
		}
		if found == nil {
			break
		}
		clauses = append(clauses, *found)
	}
	return clauses
}

// restrictionKeyUsable admits the probe-key kinds the executor's btree key
// encoder takes directly — the set the rule-based producer
// (planIndexScanFromWhereShape) accepted. A boolean key and a string against a
// user enum column (which needed the rule path's enum cast) decline; the
// clause stays a filter and the path is simply not built from it.
//
// Built on isConstExpr (the literal kinds normalizeColumnConst already
// admitted) rather than a second Expr type switch, so the literal-kind list
// lives in one place.
func restrictionKeyUsable(cat catalog.Catalog, col catalog.Column, val Expr) bool {
	if !isConstExpr(val) {
		return false
	}
	if _, isBool := val.(*BooleanConst); isBool {
		return false
	}
	if _, isStr := val.(*StringConst); isStr {
		if im := inMemoryCat(cat); im != nil {
			if _, isEnum := im.LookupEnum(col.Type.Name); isEnum {
				return false
			}
		}
	}
	return true
}

// restrictionIndexSelectivity is the index quals' selectivity alone
// (btcostestimate's clauselist_selectivity over indexQuals), with PG's
// `isunique` short circuit for a unique index bound on every key column.
// Shared by the plain (addOneRestrictionIndexPath) and index-only
// (addOneIndexOnlyPath) producers so the two price the same probe alike.
func restrictionIndexSelectivity(tbl *catalog.Table, idx *catalog.Index, clauses []indexPathClause, relTuples float64) (float64, bool) {
	rawRows := 0.0
	if tbl.Stats != nil {
		rawRows = float64(tbl.Stats.RowCount)
	}
	sel := 1.0
	for _, c := range clauses {
		stats := columnStatsByName(tbl, idx.Columns[c.indexCol])
		sel *= eqSelectivityForColumn(stats, c.key, rawRows)
	}
	unique := idx.Unique && len(clauses) == len(idx.Columns)
	if unique && relTuples > 0 {
		sel = 1.0 / relTuples
	}
	return sel, unique
}

// addOneRestrictionIndexPath builds the equality-prefix path for one index, or
// declines. Returns whether a path was added.
func (s *searchCtx) addOneRestrictionIndexPath(cat catalog.Catalog, rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	conjuncts []Expr, colExprs map[string]Expr, relPages int64, relTuples, totalPages float64) bool {
	if idx == nil || len(idx.Columns) == 0 {
		return false
	}
	// An unproven partial index would silently drop rows (no predicate-
	// implication prover — same decline as the ordered arm, ledgered there).
	if idx.HasPredicate {
		return false
	}
	clauses := restrictionEqualityPrefix(cat, tbl, idx, conjuncts)
	if len(clauses) == 0 {
		return false
	}
	// build_index_paths builds ONE path per index and makes it index-only
	// when check_index_only holds (indxpath.c:1010, `index_only_scan`). When
	// addIndexOnlyPaths will build that index-only path with these same
	// clauses, a plain twin here would tie it on cost and — filed first —
	// win add_path's tie, electing the heap-fetching shape PG never builds.
	if s.restrictionPathIsIndexOnly(cat, tbl, idx, conjuncts) {
		return false
	}
	sel, unique := restrictionIndexSelectivity(tbl, idx, clauses, relTuples)

	var keys []PathKey
	dir := ForwardScanDirection
	if len(colExprs) > 0 && indexIsOrderable(idx) {
		keys, dir = indexPathOrdering(idx, colExprs, false)
	}

	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	qpquals := localQualOpCount(rel.baseLeaf) - float64(len(clauses))
	if qpquals < 0 {
		qpquals = 0
	}
	in := indexScanInputs{
		relPages:                relPages,
		relTuples:               relTuples,
		indexPages:              indexPages,
		indexTuples:             indexTuples,
		treeHeight:              treeHeight,
		selectivity:             sel,
		uniqueEqualityOnAllKeys: unique,
		correlation:             indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages:         totalPages,
		// qpquals = baserestrictinfo minus the index quals (costsize.c:806-820):
		// the consumed equalities are applied by the probe, not re-checked.
		numQualOps: qpquals,
	}
	cost := costIndexScan(s.cp, in)
	tgt, tgtKnown := scanPathTarget(rel)
	addPath(rel, &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		ParallelSafe:  rel.ParallelSafeForPath(),
		DisabledNodes: disabledNodesFor(!s.cp.enableIndexScan),
		// cost_index's unparameterised arm: `path->rows = baserel->rows`.
		Rows:          rel.Rows,
		Cost:          cost,
		Pathkeys:      keys,
		IndexInfo:     idx,
		IndexScanDir:  dir,
		IndexClauses:  clauses,
		RequiredOuter: 0,
		Target:        tgt,
		TargetKnown:   tgtKnown,
	}, "index.restrict")
	return true
}
