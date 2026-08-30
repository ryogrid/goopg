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
			if p := s.buildOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, totalPages, maxEntries, rel.baseLeaf); p != nil {
				singlePaths = append(singlePaths, p)
			}
		}

		if len(singlePaths) == 0 {
			continue
		}

		// Add single-index paths to the relation's pathlist.
		added := false
		for _, p := range singlePaths {
			addPath(rel, p)
			added = true
		}

		// M0129-S5.1: combine into BitmapAnd when beneficial.
		if len(singlePaths) >= 2 {
			if andPath := s.chooseBitmapAnd(singlePaths, rel, tbl, relTuples, T, totalPages, maxEntries); andPath != nil {
				addPath(rel, andPath)
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
	if idx == nil {
		return nil
	}
	// Partial index — resolve the predicate for recheck at execution time.
	// Without a predicate-implication prover (same gap as the ordered scan
	// path), goopg relies on the bitmap's recheck mechanism: the resolved
	// predicate is appended to BitmapQual and evaluated against every heap
	// tuple, so correctness is guaranteed even for lossy pages. The cost
	// model still penalises irrelevant partial indexes through high
	// selectivity, so they lose to seq scan in add_path when the query
	// quals don't match (M0129-S5.4).
	var partialPredicate Expr
	if idx.HasPredicate {
		resolved, err := ResolveIndexPredicate(idx.Predicate, tbl)
		if err != nil {
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

	// Extract local filter conjuncts from the leaf's Filter wrappers
	// and match equality conjuncts against this index's columns.
	// Matched conjuncts become index probe keys; their combined
	// selectivity replaces the full-scan default of 1.0.
	id, _, _ := scanLeafFor(leaf)
	conjuncts := extractFilterConjuncts(leaf)
	indexClauses, qualSelectivity := matchBitmapIndexQuals(idx, tbl, conjuncts, id)

	// Index geometry — same as the regular index scan cost model.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)

	selectivity := qualSelectivity
	in := indexScanInputs{
		relPages:        relPages,
		relTuples:       relTuples,
		indexPages:      indexPages,
		indexTuples:     indexTuples,
		treeHeight:      treeHeight,
		selectivity:     selectivity,
		correlation:     indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
	}
	tuplesFetched := clampRowEst(selectivity * relTuples)

	// Cost the index-access side (bitmap index scan).
	idxCost := costBitmapIndexScan(s.cp, in)

	// Compute how many distinct heap pages the bitmap visits.
	pagesFetched := computeBitmapPages(tuplesFetched, T, indexPages, totalPages, s.cp.effectiveCacheSize, maxEntries)

	// Total cost: index access (startup) + heap fetch (run).
	totalCost := costBitmapHeapScan(s.cp, idxCost, pagesFetched, tuplesFetched, T)

	// Build the bitmap index path (the child of the heap scan).
	bitmapIdxPath := &Path{
		Kind:              PathBitmapIndexScan,
		Rel:               rel,
		Rows:              tuplesFetched,
		Cost:              idxCost,
		BitmapSelectivity: selectivity,
		IndexInfo:         idx,
		IndexScanDir:      NoMovementScanDirection,
		IndexClauses:      indexClauses,
		PartialPredicate:  partialPredicate,
		RequiredOuter:     0,
	}

	// Build the bitmap heap scan path (the outer container).
	return &Path{
		Kind:     PathBitmapHeapScan,
		Rel:      rel,
		Rows:     rel.Rows, // the rel's post-restriction row count
		Cost:     totalCost,
		Children: []*Path{bitmapIdxPath},
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
// id is the scanIdentity extracted from the leaf (via scanLeafFor); its
// table provides the column-name→position mapping.
func matchBitmapIndexQuals(
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
		for _, conj := range conjuncts {
			bin, ok := conj.(*BinaryOp)
			if !ok || bin.Op != parser.OpEq {
				continue
			}
			cr, val, ok := normalizeColumnConst(bin.Left, bin.Right)
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
			// Found an equality conjunct matching this index column.
			stats := columnStatsByName(tbl, colName)
			colSel := eqSelectivityForColumn(stats, val)
			selectivity *= colSel

			// ri is nil: local quals have no restrictInfo. The Key/Keys
			// fields (from c.key) are still used by createBitmapIndexScanPlan
			// to set the index probe bounds. bitmapQualExprs skips entries
			// with nil ri — local equality conjuncts don't need recheck
			// because exact B-tree lookup is always correct.
			clauses = append(clauses, indexPathClause{
				ri:       nil,
				indexCol: pos,
				key:      val,
			})
			break // first wins per column
		}
	}

	if len(clauses) == 0 {
		return nil, 1.0
	}
	return clauses, clampSelectivity(selectivity)
}

// chooseBitmapAnd ports PG's choose_bitmap_and (indxpath.c:1786-1988):
// given a nonempty list of single-index bitmap heap scan paths, greedily
// combine them into a BitmapAnd when the combined cost is lower than
// individual scan costs. Returns nil when no AND combination beats the
// best single path.
//
// The algorithm:
//  1. Sort paths by tree cost (cheapest first).
//  2. For each path as "group leader", try adding subsequent paths.
//  3. Skip a candidate if its index columns overlap with already-chosen
//     indexes (simplified redundancy check — goopg lacks PG's clause-level
//     PathClauseUsage classification).
//  4. If the AND cost is cheaper, keep the candidate; otherwise drop it.
//  5. Return the cheapest group as a BitmapHeapScan over a BitmapAnd,
//     or nil if the best group is a singleton (no AND needed).
func (s *searchCtx) chooseBitmapAnd(
	singlePaths []*Path, rel *RelOptInfo, tbl *catalog.Table, relTuples, T, totalPages float64, maxEntries int,
) *Path {
	if len(singlePaths) < 2 {
		return nil
	}

	// Copy and sort by tree cost (cheapest first), matching PG's
	// path_usage_comparator sort order.
	paths := make([]*Path, len(singlePaths))
	copy(paths, singlePaths)
	sort.Slice(paths, func(i, j int) bool {
		ci, _ := costBitmapTree(s.cp, paths[i].Children[0])
		cj, _ := costBitmapTree(s.cp, paths[j].Children[0])
		return ci < cj
	})

	// Pre-extract index column sets for redundancy checks.
	indexCols := make([]map[string]bool, len(paths))
	for i, p := range paths {
		idx := p.Children[0].IndexInfo
		indexCols[i] = make(map[string]bool)
		for _, col := range idx.Columns {
			indexCols[i][col] = true
		}
	}

	var bestPaths []*Path
	bestCost := Cost{Total: 1e30}

	for i := 0; i < len(paths); i++ {
		group := []*Path{paths[i]}
		// Gather columns already covered by this group.
		coveredCols := make(map[string]bool)
		for col := range indexCols[i] {
			coveredCols[col] = true
		}
		indexPages := indexPagesForPath(paths[i], tbl, relTuples)
		curCost := bitmapScanCostEst(s.cp, paths[i], rel.Rows, T, indexPages, totalPages, maxEntries)

		for j := i + 1; j < len(paths); j++ {
			// Redundancy check: skip if the candidate index shares ANY
			// key column with an already-chosen index.
			overlap := false
			for col := range indexCols[j] {
				if coveredCols[col] {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}

			// Tentatively add this path and compute AND cost.
			candidate := append(group, paths[j])
			// Extract inner bitmap index paths for cost estimation.
			inners := make([]*Path, len(candidate))
			for k, cp := range candidate {
				inners[k] = cp.Children[0]
			}
			// Sum index pages across the AND group for cache-aware estimate.
			var andIP float64
			for _, inner := range inners {
				ip, _, _ := estimateIndexGeometry(inner.IndexInfo, tbl, relTuples)
				andIP += ip
			}
			newCost := bitmapAndScanCostEst(s.cp, inners, rel.Rows, T, andIP, totalPages, maxEntries)

			if newCost.Total < curCost.Total {
				// Cheaper — keep the candidate.
				group = candidate
				curCost = newCost
				for col := range indexCols[j] {
					coveredCols[col] = true
				}
			}
			// else: drop the candidate (cost did not decrease).
		}

		if curCost.Total < bestCost.Total {
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
		inners[k] = bp.Children[0] // extract BitmapIndexScan path
	}
	andCost, andSelec := costBitmapAndCost(s.cp, inners)
	bitmapAndPath := &Path{
		Kind:              PathBitmapAnd,
		Rel:               rel,
		Rows:              clampRowEst(andSelec * relTuples),
		Cost:              andCost,
		BitmapSelectivity: andSelec,
		Children:          inners,
	}

	// Recompute heap-access cost atop the AND tree.
	tuplesFetched := clampRowEst(andSelec * relTuples)
	andIP := indexPagesForPath(bitmapAndPath, tbl, relTuples)
	pagesFetched := computeBitmapPages(tuplesFetched, T, andIP, totalPages, s.cp.effectiveCacheSize, maxEntries)
	totalCost := costBitmapHeapScan(s.cp, andCost, pagesFetched, tuplesFetched, T)

	return &Path{
		Kind:     PathBitmapHeapScan,
		Rel:      rel,
		Rows:     rel.Rows,
		Cost:     totalCost,
		Children: []*Path{bitmapAndPath},
	}
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
				if pth := s.buildOneParameterizedBitmapPath(rel, tbl, idx, cands, req,
					relPages, relTuples, T, totalPages, maxEntries); pth != nil {
					addPath(rel, pth)
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
		clauses = append(clauses, indexPathClause{indexCol: pos, key: c.outerKey})
		sel *= varEqNonConstSelectivity(columnStatsByName(tbl, colName), relTuples)
	}
	if len(clauses) == 0 {
		return nil
	}
	sel = clampSelectivity(sel)
	tuplesFetched := clampRowEst(sel * relTuples)
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)
	idxCost := costBitmapIndexScan(s.cp, indexScanInputs{
		relPages: relPages, relTuples: relTuples, indexPages: indexPages,
		indexTuples: indexTuples, treeHeight: treeHeight, selectivity: sel,
		correlation: indexCorrelationFor(idx, leadingKeyStats(idx, tbl)), totalTablePages: totalPages,
	})
	// `get_loop_count` (indxpath.c:2328): the smallest row count among the
	// relations supplying the parameter. The path cost stays per-execution — PG
	// pro-rates inside `compute_bitmap_pages` — so the join above still
	// multiplies by the outer row count without double-counting.
	pagesFetched := computeBitmapPagesLooped(tuplesFetched, T, indexPages, totalPages,
		s.cp.effectiveCacheSize, maxEntries, s.loopCountFor(req))
	return &Path{
		Kind: PathBitmapHeapScan, Rel: rel,
		Rows:          parameterizedBaserelRows(rel, nil, sel, false),
		Cost:          costBitmapHeapScan(s.cp, idxCost, pagesFetched, tuplesFetched, T),
		RequiredOuter: req,
		Children: []*Path{{
			Kind: PathBitmapIndexScan, Rel: rel, Rows: tuplesFetched, Cost: idxCost,
			BitmapSelectivity: sel, IndexInfo: idx, IndexScanDir: NoMovementScanDirection,
			IndexClauses: clauses, RequiredOuter: req,
		}},
	}
}
