package optimizer

// M0128-P2.4 — bitmap path generation. For each base relation with a usable
// index, a PathBitmapHeapScan wrapping a PathBitmapIndexScan is generated and
// competes against PathSeqScan and PathIndexScan in add_path.
//
// PG oracle: PG's create_index_paths (indxpath.c:235) generates both
// BitmapHeapPath and IndexPath for every index; add_path keeps whichever wins
// in each cost regime.
//
// M0129-S5.1 — chooseBitmapAnd ported from PG's choose_bitmap_and
// (indxpath.c:1786-1988): greedily combine single-index bitmap paths into
// BitmapAnd/BitmapOr combinations when the combined cost beats the individual
// costs.

import (
	"sort"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// addBaseRelBitmapPaths generates, for every base relation, a bitmap scan
// path over each usable index. The bitmap path is always generated — PG
// generates both indexscan and bitmap paths for every index, and add_path
// keeps the cheaper. The bitmap path is NOT generated when the index would
// return a single row (cheaper as plain IndexScan).
//
// After generating single-index paths, chooseBitmapAnd combines them into
// BitmapAnd paths when the combined cost is lower.
func (s *searchCtx) addBaseRelBitmapPaths(cat catalog.Catalog) {
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
		// Same consumer-side eligibility gate as the ordered-index arm: no
		// bitmap path over a leaf createPlan cannot rebuild.
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		T := float64(relPages)
		if T < 1 {
			T = 1
		}
		maxEntries := bitmapMaxEntries(s.cp.workMem)

		// Generate single-index bitmap paths, collecting them for AND combination.
		var singlePaths []*Path
		for _, idx := range cat.IndexesOnTable(tbl) {
			// A catalog-only index (gist/spgist/gin/brin) has nothing to scan (M0146-0069).
			if !idx.HasStorage() {
				continue
			}
			if p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, totalPages, maxEntries, rel.baseLeaf); p != nil {
				singlePaths = append(singlePaths, p)
			}
		}

		// M0148-0001c3a2: BitmapOr paths over the leaf's OR conjuncts
		// (generate_bitmap_or_paths over rel->baserestrictinfo).
		orPaths := s.bitmapOrHeapPaths(rel, tbl, cat.IndexesOnTable(tbl), relPages, relTuples, T, totalPages, maxEntries, rel.baseLeaf)
		for _, p := range orPaths {
			addPath(rel, p, "bitmap.orclause")
		}
		if len(singlePaths) == 0 {
			if len(orPaths) > 0 {
				setCheapest(rel)
			}
			continue
		}

		// Add single-index paths to the relation's pathlist.
		added := false
		for _, p := range singlePaths {
			addPath(rel, p, "bitmap.heap")
			added = true
		}

		// M0129-S5.1: combine into BitmapAnd when beneficial. The OR paths
		// are candidates too: create_index_paths hands choose_bitmap_and
		// every restriction bitmap path, generate_bitmap_or_paths' included
		// (M0148-0001c3a2).
		if cands := append(append([]*Path(nil), singlePaths...), orPaths...); len(cands) >= 2 {
			if andPath := s.chooseBitmapAnd(cands, rel, tbl, relTuples, T, totalPages, maxEntries); andPath != nil {
				addPath(rel, andPath, "bitmap.and")
				added = true
			}
		}

		if added {
			setCheapest(rel)
		}
	}
}

// buildOneBitmapPath builds a single bitmap heap scan path for one index.
// Returns nil when the index is unsuitable (nil, partial, unique+single-row).
// Sets BitmapSelectivity on the inner bitmap index path so costBitmapTree
// can extract it.
//
// leaf is the base relation's leaf node (possibly wrapped in *Filter). Local
// filter conjuncts are extracted from the Filter wrappers and matched against
// index columns; matched equality conjuncts are pushed into the index as probe
// keys, reducing selectivity below 1.0.
func (s *searchCtx) buildOneBitmapPath(
	rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
	leaf Node,
) *Path {
	bitmapIdxPath := s.buildBitmapIndexChild(rel, tbl, idx, relPages, relTuples, totalPages, leaf, extractFilterConjuncts(leaf))
	if bitmapIdxPath == nil {
		return nil
	}
	// The index path's row count is the lossiness-corrected tuple count
	// computeBitmapPages returns (the figure costBitmapTree charges the
	// per-tuple bitmap manipulation against when chooseBitmapAnd compares
	// paths).
	_, bitmapIdxPath.Rows = computeBitmapPages(bitmapIdxPath.Rows, relTuples, T,
		indexPagesForPath(s.cat, bitmapIdxPath, tbl, relTuples), totalPages, s.cp.effectiveCacheSize, maxEntries)
	return s.bitmapHeapPathOver(rel, tbl, bitmapIdxPath, relTuples, T, totalPages, maxEntries, leaf)
}

// bitmapHeapPathOver wraps a bitmap qual path (a Bitmap Index Scan, or a
// BitmapAnd / BitmapOr tree) in the BitmapHeapScan path that reads its heap
// pages — create_bitmap_heap_path + cost_bitmap_heap_scan (pathnode.c,
// costsize.c). The bitmap qual's own cost is the heap scan's startup cost.
func (s *searchCtx) bitmapHeapPathOver(
	rel *RelOptInfo, tbl *catalog.Table, bitmapqual *Path,
	relTuples, T, totalPages float64, maxEntries int, leaf Node,
) *Path {
	tuplesFetched := clampRowEst(bitmapqual.BitmapSelectivity * relTuples)
	// Compute how many distinct heap pages the bitmap visits.
	indexPages := indexPagesForPath(s.cat, bitmapqual, tbl, relTuples)
	pagesFetched, tuplesFetched := computeBitmapPages(tuplesFetched, relTuples, T, indexPages, totalPages, s.cp.effectiveCacheSize, maxEntries)

	// Total cost: index access (startup) + heap fetch (run). R1
	// (plan-parity-fix-take2): the heap side rechecks every local
	// conjunct, so the count is the leaf's full conjunct list.
	totalCost := costBitmapHeapScan(s.cp, bitmapqual.Cost, pagesFetched, tuplesFetched, T, localQualOpCount(leaf))

	// Build the bitmap heap scan path (the outer container).
	// B-17d: `cost_bitmap_heap_scan`'s own flag (costsize.c:1023), on top of
	// the index child's count (normally zero — the child carries no flag of
	// its own). The producer always runs.
	return &Path{
		Kind:          PathBitmapHeapScan,
		Rel:           rel,
		Rows:          rel.Rows, // the rel's post-restriction row count
		Cost:          totalCost,
		DisabledNodes: disabledNodesFor(!s.cp.enableBitmapScan, bitmapqual),
		Children:      []*Path{bitmapqual},
		// create_bitmap_heap_path (pathnode.c:1149): the rel's flag AND the
		// bitmapqual's. C-19a.
		ParallelSafe: parallelSafeWith(rel, bitmapqual),
	}
}

// buildBitmapIndexChild builds the Bitmap Index Scan path of one index from
// the restriction clauses in conjuncts — build_index_paths' ST_BITMAPSCAN
// arm. conjuncts is the leaf's own conjunct list for a plain bitmap path, and
// one OR arm's clauses plus the other restriction clauses for a BitmapOr arm
// (build_paths_for_OR, M0148-0001c3a2). Returns nil when the index offers no
// index clause and no proven partial predicate.
func (s *searchCtx) buildBitmapIndexChild(
	rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	relPages int64, relTuples, totalPages float64,
	leaf Node, conjuncts []Expr,
) *Path {
	if idx == nil {
		return nil
	}
	// Partial index. PG builds index paths only for a partial index whose
	// predicate the query's restriction clauses PROVE (`check_index_predicates`
	// sets `index->predOK`; `create_index_paths` skips an unproven one,
	// postgres/src/backend/optimizer/path/indxpath.c). M0145-0008r: this arm
	// used to admit any partial index on the premise that appending the
	// predicate to the heap recheck keeps it correct. It does not — a row the
	// predicate excludes has no index entry, so no recheck can bring it back
	// (`onek2 WHERE unique1 = 50` over `onek2_u1_prtl ... WHERE stringu1 < 'B'`
	// returned 0 rows, regress `portals_p2`). The proof is the narrow
	// `Var op Const` prover the other producers use (M0134-0017b), checked
	// against each of the conjuncts below.
	var partialPredicate Expr
	if idx.HasPredicate {
		resolved, err := ResolveIndexPredicateOf(idx, tbl)
		if err != nil || resolved == nil {
			// Predicate resolution failed — skip this index.
			return nil
		}
		partialPredicate = resolved
	}
	// Skip if the index would produce a single row per probe — a plain
	// IndexScan is cheaper in that regime.
	if idx.Unique && relTuples <= 1 {
		return nil
	}

	// Match the conjuncts against this index's columns. Matched conjuncts
	// become index probe keys; their combined selectivity replaces the
	// full-scan default of 1.0.
	id, _, _ := scanLeafFor(leaf)
	if partialPredicate != nil && !partialPredicateProvenBy(partialPredicate, conjuncts) {
		return nil
	}
	indexClauses, qualSelectivity := matchBitmapIndexQuals(s.cat, idx, tbl, conjuncts, id)
	// M0148-0001c3a2: a ScalarArrayOp qual — `col IN (…)`, or an OR of
	// equalities on one column (match_orclause_to_indexcol) — on the column
	// after the equality prefix (the leading column when there is none):
	// one probe per element into the one bitmap, priced by scalararraysel
	// and charged one descent per element (btcostestimate's num_sa_scans).
	numSAScans := 0.0
	saop := false
	if n := len(indexClauses); n < len(idx.Columns) {
		if sc, selExpr := bitmapSAOPOnColumn(s.cat, tbl, idx, n, conjuncts); sc != nil {
			indexClauses = append(indexClauses, *sc)
			qualSelectivity = clampSelectivity(qualSelectivity * clauseSelectivity(selExpr, leaf))
			for _, q := range sc.saopColQuals {
				qualSelectivity = clampSelectivity(qualSelectivity * clauseSelectivity(q.local, leaf))
			}
			numSAScans = saopDescents(*sc)
			saop = true
		}
	}
	// M0148-0001c3a1 / c3a2: on the column after the equality prefix, its
	// bounds and its NullTest are index clauses too (build_index_paths binds
	// every matching clause of each column). Exact — the probe bounds them
	// through indexRangeBounds (restrictionRangeBehindPrefix) — so their
	// conjuncts are the heap scan's recheck, not a Filter. Constant bounds
	// only, as bitmapLeadingRange takes them.
	if n := len(indexClauses); !saop && n > 0 && n < len(idx.Columns) {
		var rng []indexPathClause
		for _, c := range restrictionRangeBehindPrefix(s.cat, tbl, idx, n, conjuncts) {
			if bitmapConstKey(c.key) && !c.redundantBound {
				rng = append(rng, c)
			}
		}
		if nts := restrictionNullTestsOnColumn(idx, n, conjuncts); len(nts) > 0 {
			rng = withNullTestsInConjunctOrder(rng, nts, conjuncts)
		}
		if len(rng) > 0 {
			indexClauses = append(indexClauses, rng...)
			qualSelectivity = clampSelectivity(qualSelectivity * rangeIndexSelectivity(leaf, conjuncts, rng))
		}
	}
	// M0146-0061: with no equality on the leading column, a range bound on it
	// is still an index clause. match_clause_to_indexcol takes any btree
	// operator of the column's opfamily, so build_index_paths gives PG a
	// bitmap path for `a > 97` as for `a = 97`. The bounds are priced by
	// clauselist_selectivity over them, which pairs a lower and an upper
	// bound into one band (rangeIndexSelectivity), as the plain index arm
	// prices them.
	if len(indexClauses) == 0 {
		rng := bitmapLeadingRange(s.cat, tbl, idx, conjuncts)
		// M0148-0001c3a1: the leading column's NullTest, alone or beside the
		// bounds, as the plain restriction producer files it.
		if nts := restrictionNullTestsOnColumn(idx, 0, conjuncts); len(nts) > 0 {
			rng = withNullTestsInConjunctOrder(rng, nts, conjuncts)
		}
		if len(rng) > 0 {
			indexClauses = rng
			qualSelectivity = rangeIndexSelectivity(leaf, conjuncts, rng)
		}
	}
	// M0148-0001c3a4 (j): a range on a later column, nothing bound before
	// it — PG 18's skip scan; constant bounds only, as bitmapLeadingRange.
	skipCol := 0
	if len(indexClauses) == 0 {
		if rng, skip := restrictionSkipRange(s.cat, tbl, idx, conjuncts); len(rng) > 0 {
			allConst := true
			for _, c := range rng {
				if !bitmapConstKey(c.key) || c.redundantBound {
					allConst = false
				}
			}
			if allConst {
				indexClauses, skipCol = rng, skip
				qualSelectivity = rangeIndexSelectivity(leaf, conjuncts, rng)
			}
		}
	}

	// PG builds a bitmap path only FROM index clauses: `get_index_paths`
	// (indxpath.c) collects into `bitindexpaths` the paths `build_index_paths`
	// made from the clause set (or a proven partial-index predicate), and a
	// clause-less full-table bitmap is never generated at all. goopg used to
	// build one for every index and rely on the cost model to reject it —
	// "they lose to seq scan in add_path" — which held only while
	// `costBitmapHeapScan` double-charged the index side. The moment that
	// oracle-verified duplicate was removed, full-table bitmaps beat the seq
	// scan on every table with a non-trivial filter (the bitmap side pays no
	// qual-eval CPU) and 13 of 21 TPC-H plans flipped to a shape PG cannot
	// produce. Generation, not costing, is PG's guard — so it is goopg's too.
	if len(indexClauses) == 0 && partialPredicate == nil {
		return nil
	}
	// The heap recheck cannot restore rows the index never yields: goopg
	// stores no index entry whose key has a NULL column, so a key column the
	// clauses leave unbound must be NOT NULL (indexUnboundKeysNotNull).
	if !indexUnboundKeysNotNull(tbl, idx, boundIndexColumns(indexClauses)) {
		return nil
	}

	// Index geometry — same as the regular index scan cost model.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(s.cat, idx, tbl, relTuples)

	selectivity := qualSelectivity
	// The skip scan's descents and a reverted estimate's whole-index index
	// side (btcostestimate's skip-array arm, as the plain skip path).
	boundSel := 0.0
	if skipCol > 0 {
		var boundCounted bool
		numSAScans, boundCounted = skipScanDescents(tbl, idx, skipCol, relTuples, indexPages)
		if !boundCounted {
			boundSel = 1
		}
	}
	in := indexScanInputs{
		relPages:        relPages,
		relTuples:       relTuples,
		indexPages:      indexPages,
		indexTuples:     indexTuples,
		treeHeight:      treeHeight,
		selectivity:     selectivity,
		correlation:     indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
		// R1 (plan-parity-fix-take2): PG's cost_bitmap_heap_scan charges
		// "the full freight for all the scan clauses" (assume-rechecked-
		// always), so every local conjunct counts — not minus
		// index-satisfied ones.
		numQualOps:       localQualOpCount(leaf),
		numIndexQuals:    indexQualCount(indexClauses),
		numSAScans:       numSAScans,
		boundSelectivity: boundSel,
	}

	// Cost the index-access side (bitmap index scan).
	idxCost, amTotal := costBitmapIndexScan(s.cp, in, rel.Rows)

	return &Path{
		Kind:               PathBitmapIndexScan,
		BitmapIndexAMTotal: amTotal,
		Rel:                rel,
		Rows:               clampRowEst(selectivity * relTuples),
		Cost:               idxCost,
		IndexPathTotalCost: costIndexScan(s.cp, in).Total,
		BitmapSelectivity:  selectivity,
		IndexInfo:          idx,
		IndexScanDir:       NoMovementScanDirection,
		IndexClauses:       indexClauses,
		PartialPredicate:   partialPredicate,
		RequiredOuter:      0,
		// create_bitmap_index_path: `rel->consider_parallel`. C-19a.
		ParallelSafe: rel.ParallelSafeForPath(),
	}
}

// extractFilterConjuncts extracts individual AND-conjuncts from the *Filter
// chain above a base-scan leaf. The conjuncts are in leaf-local coordinates
// (ColumnRef.Index matches the inner scan's output positions).
func extractFilterConjuncts(leaf Node) []Expr {
	var conjuncts []Expr
	n := leaf
	for {
		f, ok := n.(*Filter)
		if !ok {
			break
		}
		conjuncts = append(conjuncts, flattenExprAnd(f.Predicate)...)
		n = f.Child
		if n == nil {
			break
		}
	}
	return conjuncts
}

// flattenExprAnd flattens a planner Expr's top-level AND tree into individual
// conjuncts. Non-AND expressions are returned in a singleton slice.
func flattenExprAnd(expr Expr) []Expr {
	if expr == nil {
		return nil
	}
	bin, ok := expr.(*BinaryOp)
	if !ok || bin.Op != parser.OpAnd {
		return []Expr{expr}
	}
	return append(flattenExprAnd(bin.Left), flattenExprAnd(bin.Right)...)
}

// matchBitmapIndexQuals matches leaf-local equality conjuncts against an
// index's columns. Returns the matched clauses in index-column order and
// the combined selectivity. When no columns match, returns (nil, 1.0)
// for a full index scan.
//
// Only top-level equality conjuncts (col = const) are eligible — range
// predicates and disjunctions remain as recheck / residual quals and do
// not contribute selectivity reduction on the index side.
//
// ScalarArrayOp (`col IN (consts)`) is deliberately NOT matched here,
// though PG wires `match_saopclause_to_indexcol` into its bitmap build too
// (indxpath.c:3136). The bitmap carrier end-to-end is single-key per column
// (indexPathClause.key → BitmapIndexScan.Key/Keys → single-descent probe),
// while SAOP needs a multi-key carrier at every layer; the SAOP probe
// therefore lives on IndexScan.SAOPKeys (trySAOPIndexScan, planner.go) with
// multi-descent in indexScanOp. The cost side is already shared —
// costBitmapIndexScan reads btreeIndexAMCost, which charges num_sa_scans —
// so a future bitmap SAOP arm prices identically to the plain one (P2-09b).
//
// id is the scanIdentity extracted from the leaf (via scanLeafFor); its
// table provides the column-name→position mapping.
func matchBitmapIndexQuals(
	cat catalog.Catalog,
	idx *catalog.Index, tbl *catalog.Table,
	conjuncts []Expr,
	id *scanIdentity,
) ([]indexPathClause, float64) {
	if idx == nil || tbl == nil || id == nil {
		return nil, 1.0
	}

	selectivity := 1.0
	var clauses []indexPathClause

	for pos, colName := range idx.Columns {
		// The bound columns must be a GAPLESS leading prefix: the probe binds
		// Keys[i] to Index.Columns[i] positionally, and createBitmapIndexScanPlan
		// panics on a list that skips a column ("claims index column 2"). An
		// equality on a later column with an earlier one unbound is not an
		// index qual here (PG 18 would use a btree skip scan, which goopg's
		// probe cannot express — ledgered). M0145-0029: reached on the
		// jointree arm, where single-relation scopes plan through the search.
		if len(clauses) < pos {
			break
		}
		for _, conj := range conjuncts {
			bin, ok := conj.(*BinaryOp)
			if !ok || bin.Op != parser.OpEq {
				continue
			}
			cr, val, _, ok := normalizeColumnIndexKey(bin.Left, bin.Right)
			if !ok {
				continue
			}
			// Map ColumnRef.Index to column name.
			// For a bare SeqScan leaf, output positions = table column positions.
			if cr.Index < 0 || cr.Index >= len(tbl.Columns) {
				continue
			}
			if tbl.Columns[cr.Index].Name != colName {
				continue
			}
			// An outer-level key must carry the column's own type: the probe
			// encodes its Datum against the btree byte key uncast.
			var local Expr
			if t, isOuter := outerParamKeyType(val); isOuter {
				ct := tbl.Columns[cr.Index].Type
				if t.Name != ct.Name || t.IsArray != ct.IsArray {
					continue
				}
				// M0146-0012: that same-type probe is exact, so the conjunct
				// is the heap scan's recheck rather than a Filter beside it
				// (createBitmapHeapScanPlan).
				local = conj
			} else if _, isIntCast := integerCastLiteral(val); isIntCast && !restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
				// M0148-0001c3a6 (i): an integer Const outside the column
				// type's range cannot be encoded into the column's key (PG's
				// cross-type compare finds no match); it stays a Filter.
				continue
			} else if restrictionKeyUsable(cat, tbl.Columns[cr.Index], val) {
				// M0148-0001c3a1: a literal key the btree encoder takes
				// exactly (restrictionKeyUsable — the plain restriction
				// producer's test for dropping the conjunct from its Filter)
				// is an exact probe too, so its conjunct joins the recheck
				// list as PG's bitmapqualorig has it (create_bitmap_scan_plan):
				// `Recheck Cond: (stringu1 = 'TVAAAA'::name)`, not a Filter.
				local = conj
			}
			// Found an equality conjunct matching this index column.
			stats := columnStatsByName(tbl, colName)
			// take2 P2-09: the relation's raw tuple count resolves the
			// relative ndistinct form; the table's own stats carry it here.
			rawRows := 0.0
			if tbl.Stats != nil {
				rawRows = float64(tableRows(tbl))
			}
			colSel := indexKeyEqSelectivity(stats, val, rawRows, columnTypeByName(tbl, colName))
			selectivity *= colSel

			// ri is nil: local quals have no restrictInfo. The Key/Keys
			// fields (from c.key) are still used by createBitmapIndexScanPlan
			// to set the index probe bounds. bitmapQualExprs skips entries
			// with nil ri; createBitmapHeapScanPlan moves a set `local`
			// into the recheck list instead.
			clauses = append(clauses, indexPathClause{
				ri:       nil,
				indexCol: pos,
				key:      val,
				local:    local,
			})
			break // first wins per column
		}
	}

	if len(clauses) == 0 {
		return nil, 1.0
	}
	return clauses, clampSelectivity(selectivity)
}

// bitmapLeadingRange is restrictionLeadingRange for the bitmap producer, with
// constant bounds only. An outer-level bound would make the bitmap a
// correlated probe, and the decorrelation clone (clonePlanReplacingOuter)
// harvests only equality keys from a BitmapIndexScan; such a bound stays a
// Filter, as before.
func bitmapLeadingRange(cat catalog.Catalog, tbl *catalog.Table, idx *catalog.Index, conjuncts []Expr) []indexPathClause {
	var out []indexPathClause
	for _, c := range restrictionLeadingRange(cat, tbl, idx, conjuncts) {
		if bitmapConstKey(c.key) && !c.redundantBound {
			out = append(out, c)
		}
	}
	return out
}

// chooseBitmapAnd ports PG's choose_bitmap_and (indxpath.c): given bitmap
// heap scan paths (single-index, BitmapOr), greedily AND their bitmap quals
// when that is cheaper than the best single one. Returns nil when no AND
// combination beats the best single path.
//
// M0148-0001c3a2 ported the redundancy model verbatim. Each candidate's
// clause usage is classify_index_clause_usage: the clauses its index paths
// use (an OR path's are its arms' clauses, find_indexpath_quals) plus its
// partial-index predicates, numbered by equal() position in one shared list.
// Candidates with the same clause set keep only the cheaper; the rest are
// sorted by path_usage_comparator; and a candidate is skipped when it shares
// any clause with the group so far, or when the group's quals imply one of
// its predicates. This replaced an index-COLUMN overlap test that read
// Children[0].IndexInfo — which a BitmapOr has not — and skipped every pair
// of indexes sharing a column even when they used different clauses.
func (s *searchCtx) chooseBitmapAnd(
	singlePaths []*Path, rel *RelOptInfo, tbl *catalog.Table, relTuples, T, totalPages float64, maxEntries int,
) *Path {
	if len(singlePaths) < 2 {
		return nil
	}
	type pathUsage struct {
		heap           *Path
		quals, preds   []Expr
		ids            map[int]bool
		unclassifiable bool
		cost, selec    float64
	}
	var clauselist []Expr
	position := func(e Expr) int {
		for i, c := range clauselist {
			if exprEqual(c, e) {
				return i
			}
		}
		clauselist = append(clauselist, e)
		return len(clauselist) - 1
	}
	var infos []*pathUsage
	for _, p := range singlePaths {
		u := &pathUsage{heap: p}
		u.cost, u.selec = costBitmapTree(s.cp, p.Children[0])
		var ok bool
		u.quals, u.preds, ok = findIndexpathQuals(p.Children[0])
		if !ok || len(u.quals)+len(u.preds) > 100 {
			u.unclassifiable = true
			infos = append(infos, u)
			continue
		}
		u.ids = make(map[int]bool, len(u.quals)+len(u.preds))
		for _, q := range u.quals {
			u.ids[position(q)] = true
		}
		for _, q := range u.preds {
			u.ids[position(q)] = true
		}
		dup := -1
		for i, o := range infos {
			if !o.unclassifiable && sameIDSet(o.ids, u.ids) {
				dup = i
				break
			}
		}
		if dup >= 0 {
			if u.cost < infos[dup].cost {
				infos[dup] = u
			}
			continue
		}
		infos = append(infos, u)
	}
	if len(infos) < 2 {
		return nil
	}
	// path_usage_comparator: cost, then selectivity.
	sort.SliceStable(infos, func(i, j int) bool {
		if infos[i].cost != infos[j].cost {
			return infos[i].cost < infos[j].cost
		}
		return infos[i].selec < infos[j].selec
	})

	var bestPaths []*Path
	var bestCost Cost
	for i := range infos {
		lead := infos[i]
		group := []*Path{lead.heap}
		// bitmap_scan_cost_est / bitmap_and_cost_est price the candidate as
		// cost_bitmap_heap_scan does: tuples fetched = selectivity *
		// baserel->tuples. These used to pass rel.Rows — the rows AFTER the
		// restriction — so a leader on `hundred = 42 AND unique2 < 3000`
		// fetched 0.3 tuples, cost 9.06 against its real 143, and no AND
		// could ever beat it (M0148-0001c3a2).
		curCost := bitmapScanCostEst(s.cp, lead.heap, relTuples, T, indexPagesForPath(s.cat, lead.heap, tbl, relTuples), totalPages, maxEntries, relQualOpCount(rel))
		qualsSoFar := append(append([]Expr(nil), lead.quals...), lead.preds...)
		idsSoFar := make(map[int]bool, len(lead.ids))
		for k := range lead.ids {
			idsSoFar[k] = true
		}
		for j := i + 1; j < len(infos); j++ {
			cand := infos[j]
			overlap := false
			for k := range cand.ids {
				if idsSoFar[k] {
					overlap = true
					break
				}
			}
			if overlap {
				continue // consider it redundant
			}
			redundant := false
			for _, np := range cand.preds {
				if len(qualsSoFar) > 0 && predicateImpliedBy(combineAnd(qualsSoFar), np) {
					redundant = true
					break
				}
			}
			if redundant {
				continue
			}
			candidate := append(append([]*Path(nil), group...), cand.heap)
			inners := make([]*Path, len(candidate))
			var andIP float64
			for k, hp := range candidate {
				inners[k] = hp.Children[0]
				andIP += indexPagesForPath(s.cat, inners[k], tbl, relTuples)
			}
			newCost := bitmapAndScanCostEst(s.cp, inners, relTuples, T, andIP, totalPages, maxEntries, relQualOpCount(rel))
			if newCost.Total < curCost.Total {
				group = candidate
				curCost = newCost
				qualsSoFar = append(qualsSoFar, cand.quals...)
				qualsSoFar = append(qualsSoFar, cand.preds...)
				for k := range cand.ids {
					idsSoFar[k] = true
				}
			}
		}
		if i == 0 || curCost.Total < bestCost.Total {
			bestPaths = group
			bestCost = curCost
		}
	}

	// If the best group is a single path, no AND needed.
	if len(bestPaths) < 2 {
		return nil
	}

	// Build a BitmapAnd inner (combines the bitmap index scans).
	inners := make([]*Path, len(bestPaths))
	for k, bp := range bestPaths {
		inners[k] = bp.Children[0] // extract the bitmap qual path
	}
	andCost, andSelec := costBitmapAndCost(s.cp, inners)
	bitmapAndPath := &Path{
		Kind:              PathBitmapAnd,
		Rel:               rel,
		Rows:              clampRowEst(andSelec * relTuples),
		Cost:              andCost,
		BitmapSelectivity: andSelec,
		Children:          inners,
		// create_bitmap_and_path (pathnode.c:1189): `rel->consider_parallel`.
		ParallelSafe: rel.ParallelSafeForPath(),
	}

	// Recompute heap-access cost atop the AND tree.
	tuplesFetched := clampRowEst(andSelec * relTuples)
	andIP := indexPagesForPath(s.cat, bitmapAndPath, tbl, relTuples)
	pagesFetched, tuplesFetched := computeBitmapPages(tuplesFetched, relTuples, T, andIP, totalPages, s.cp.effectiveCacheSize, maxEntries)
	totalCost := costBitmapHeapScan(s.cp, andCost, pagesFetched, tuplesFetched, T, relQualOpCount(rel))

	return &Path{
		Kind:          PathBitmapHeapScan,
		Rel:           rel,
		Rows:          rel.Rows,
		Cost:          totalCost,
		DisabledNodes: disabledNodesFor(!s.cp.enableBitmapScan, bitmapAndPath),
		Children:      []*Path{bitmapAndPath},
		ParallelSafe:  parallelSafeWith(rel, bitmapAndPath),
	}
}

// findIndexpathQuals is find_indexpath_quals (indxpath.c): the clauses a
// bitmap tree's index paths use (an IndexClause's original clause) and their
// partial-index predicates (indpred, as AND items), gathered through
// BitmapAnd and BitmapOr nodes alike. ok is false when an index clause has no
// clause expression to identify it by (an inexact restriction key, whose
// conjunct stays a Filter); the caller treats such a path as unclassifiable.
func findIndexpathQuals(p *Path) (quals, preds []Expr, ok bool) {
	switch p.Kind {
	case PathBitmapAnd, PathBitmapOr:
		for _, ch := range p.Children {
			q, pr, ok := findIndexpathQuals(ch)
			if !ok {
				return nil, nil, false
			}
			quals = append(quals, q...)
			preds = append(preds, pr...)
		}
		return quals, preds, true
	case PathBitmapIndexScan:
		for _, c := range p.IndexClauses {
			switch {
			case c.ri != nil && c.ri.clause != nil:
				quals = append(quals, c.ri.clause)
			case c.local != nil:
				quals = append(quals, c.local)
			default:
				return nil, nil, false
			}
		}
		if p.PartialPredicate != nil {
			preds = append(preds, flattenExprAnd(p.PartialPredicate)...)
		}
		return quals, preds, true
	}
	return nil, nil, false
}

// sameIDSet is bms_equal over chooseBitmapAnd's clause-id sets.
func sameIDSet(a, b map[int]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (s *searchCtx) addParameterizedBitmapPaths(cat catalog.Catalog) {
	if s == nil || cat == nil || s.clauses == nil || len(s.clauses.all) == 0 {
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
		if _, _, ok := scanLeafFor(rel.baseLeaf); !ok {
			continue
		}
		// The leaf's local quals ride inside the node on `BitmapHeapScan.Cond`
		// (plan.go), exactly as they do on `IndexScan.Cond` since 80a5e334d.
		// This used to be `scanLeafIsBare`, which declined every FILTERED
		// relation — four of PG's six TPC-H bitmap scans (Q2, Q11, Q20, Q21).
		// A leaf whose wrappers are not leaf-local is still declined; see
		// `absorbableLeafCond` for why that is a coordinates question.
		if _, _, ok := absorbableLeafCond(rel.baseLeaf); !ok {
			continue
		}
		cands := indexableJoinClausesFor(rel.Relids, s.clauses.all)
		if len(cands) == 0 {
			continue
		}
		relTuples := float64(s.relInfos[i].baseRows)
		if relTuples < 1 {
			relTuples = rel.Rows
		}
		if relTuples < 1 {
			relTuples = 1
		}
		relPages := baseRelPages(tbl, relTuples)
		T := float64(relPages)
		if T < 1 {
			T = 1
		}
		maxEntries := bitmapMaxEntries(s.cp.workMem)
		added := false
		for _, req := range consideredParameterizations(cands) {
			for _, idx := range cat.IndexesOnTable(tbl) {
				// A catalog-only index (gist/spgist/gin/brin) has nothing to scan (M0146-0069).
				if !idx.HasStorage() {
					continue
				}
				if pth := s.buildOneParameterizedBitmapPath(rel, tbl, idx, cands, req,
					relPages, relTuples, T, totalPages, maxEntries); pth != nil {
					addPath(rel, pth, "bitmap.or")
					added = true
				}
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

func (s *searchCtx) buildOneParameterizedBitmapPath(
	rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	cands []paramIndexClause, req RelSet,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
) *Path {
	if idx == nil || idx.HasPredicate {
		return nil
	}
	bound := make(map[string]paramIndexClause, len(cands))
	for _, c := range cands {
		if !relsSubset(c.outerRels, req) {
			continue
		}
		if _, dup := bound[c.innerCol]; dup {
			continue
		}
		bound[c.innerCol] = c
	}
	var clauses []indexPathClause
	sel := 1.0
	for pos, colName := range idx.Columns {
		c, ok := bound[colName]
		if !ok {
			break
		}
		// `ri` carried for the SAME reason `indexPathClauses` carries it
		// (pathindexclauses.go): `probeEnforcedClauses` identifies a clause the
		// probe already applies BY its restrictInfo, and `create_nestloop_path`
		// drops such clauses from the join residual (pathnode.c:2478). Without
		// it the bitmap inner's own join clause was re-charged by
		// `qualEvalCost` as a residual — ~5.0 on TPC-H Q2's supplier probe,
		// enough to flip a 5-loop near-tie to the index inner. The index
		// producer's list has carried `ri` since P5.5-b; this one silently did
		// not (rule #2: sibling paths must agree).
		clauses = append(clauses, indexPathClause{ri: c.ri, indexCol: pos, key: c.outerKey})
		sel *= varEqNonConstSelectivity(columnStatsByName(tbl, colName), relTuples)
	}
	if len(clauses) == 0 {
		return nil
	}
	// Same NULL-key rule as the unparameterised bitmap and index producers.
	if !indexUnboundKeysNotNull(tbl, idx, len(clauses)) {
		return nil
	}
	sel = clampSelectivity(sel)
	tuplesFetched := clampRowEst(sel * relTuples)
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(s.cat, idx, tbl, relTuples)
	// The probe's path rows are its parameterised row count (ppi_rows) —
	// cost_bitmap_tree_node's per-row charge (costBitmapIndexScan).
	paramRows := clampRowEst(parameterizedBaserelRows(rel, nil, sel, false) * s.movableNonEquiJoinSelectivity(rel, req))
	idxCost, amTotal := costBitmapIndexScan(s.cp, indexScanInputs{
		relPages: relPages, relTuples: relTuples, indexPages: indexPages,
		indexTuples: indexTuples, treeHeight: treeHeight, selectivity: sel,
		correlation: indexCorrelationFor(idx, leadingKeyStats(idx, tbl)), totalTablePages: totalPages,
		numIndexQuals: float64(len(clauses)),
	}, paramRows)
	// `get_loop_count` (indxpath.c:2328): the smallest row count among the
	// relations supplying the parameter. The path cost stays per-execution — PG
	// pro-rates inside `compute_bitmap_pages` — so the join above still
	// multiplies by the outer row count without double-counting.
	pagesFetched, tuplesFetched := computeBitmapPagesLooped(tuplesFetched, relTuples, T, indexPages, totalPages,
		s.cp.effectiveCacheSize, maxEntries, s.loopCountForKeys(rel, req, boundOuterKeys(bound)))
	child := &Path{
		Kind: PathBitmapIndexScan, Rel: rel, Rows: tuplesFetched, Cost: idxCost, BitmapIndexAMTotal: amTotal,
		BitmapSelectivity: sel, IndexInfo: idx, IndexScanDir: NoMovementScanDirection,
		IndexClauses: clauses, RequiredOuter: req,
		ParallelSafe: rel.ParallelSafeForPath(),
	}
	return &Path{
		Kind: PathBitmapHeapScan, Rel: rel,
		Rows:          paramRows,
		Cost:          costBitmapHeapScan(s.cp, idxCost, pagesFetched, tuplesFetched, T, relQualOpCount(rel)),
		// B-17d: `cost_bitmap_heap_scan`'s own flag (costsize.c:1023), on
		// top of the index child's count.
		DisabledNodes: disabledNodesFor(!s.cp.enableBitmapScan, child),
		RequiredOuter: req,
		// On the HEAP path, not only the child: `probeEnforcedClauses` and
		// `memoizeCacheKeys` both read the INNER CANDIDATE's own list, and the
		// candidate `addNLIPaths` iterates is this node. PG's equivalent
		// (`is_redundant_with_indexclauses` via `bitmapqual`) reaches the same
		// clauses by walking the child tree; goopg carries them here so "what
		// the probe enforces" stays one list read twice. Without this the
		// bitmap inner's own join clause was re-charged as a nestloop residual
		// (~5.0 on Q2's supplier probe) that the sibling index path did not
		// pay.
		IndexClauses: clauses,
		Children:     []*Path{child},
		ParallelSafe: parallelSafeWith(rel, child),
	}
}

// partialPredicateProvenBy reports whether the restriction conjuncts prove a
// partial index's predicate — PG's `predOK` (`check_index_predicates`,
// indxpath.c): predicate_implied_by(indpred, the restriction list), the
// conjuncts taken together as one AND. predicateImpliedBy is the
// predicate_implied_by_recurse port (AND/OR rules, equal(), btree operator
// inference), so `unique1 = 0` proves `unique1 < 20 OR unique1 > 980`
// (M0148-0001c3a4 (e)). An unproven partial index may not be scanned at
// all: its missing entries are rows the query can still return.
func partialPredicateProvenBy(predicate Expr, conjuncts []Expr) bool {
	if predicate == nil || len(conjuncts) == 0 {
		return false
	}
	var clause Expr = conjuncts[0]
	for _, c := range conjuncts[1:] {
		clause = &BinaryOp{Op: parser.OpAnd, Left: clause, Right: c}
	}
	return predicateImpliedBy(clause, predicate)
}

// boundOuterKeys lists the outer values of a bitmap probe's bound clauses.
func boundOuterKeys(bound map[string]paramIndexClause) []Expr {
	out := make([]Expr, 0, len(bound))
	for _, c := range bound {
		out = append(out, c.outerKey)
	}
	return out
}

// bitmapConstKey is a bound the bitmap producers take: a literal, or an
// explicitly cast integer literal (`tenthous > 5000::int8`, a Const of the
// cast's type in PG, M0148-0001c3a6 (i)).
func bitmapConstKey(e Expr) bool {
	if isConstExpr(e) {
		return true
	}
	_, ok := integerCastLiteral(e)
	return ok
}
