package planner

// M0128-P2.4 — bitmap path generation. For each base relation with a usable
// index, a PathBitmapHeapScan wrapping a PathBitmapIndexScan is generated and
// competes against PathSeqScan and PathIndexScan in add_path.
//
// PG oracle: PG's create_index_paths (indxpath.c:235) generates both
// BitmapHeapPath and IndexPath for every index; add_path keeps whichever wins
// in each cost regime.
//
// BitmapAnd/BitmapOr are stub-deferred: goopg's TPC-H corpus uses only
// single-index bitmaps. Generation for those waits until PG's
// choose_bitmap_and (indxpath.c:1785) is ported. Ledgered.

import (
	"github.com/goopg/goopg/internal/catalog"
)

// addBaseRelBitmapPaths generates, for every base relation, a bitmap scan
// path over each usable index. The bitmap path is always generated — PG
// generates both indexscan and bitmap paths for every index, and add_path
// keeps the cheaper. The bitmap path is NOT generated when the index would
// return a single row (cheaper as plain IndexScan).
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
		added := false
		for _, idx := range cat.IndexesOnTable(tbl) {
			if s.addOneBitmapPath(rel, tbl, idx, relPages, relTuples, T, totalPages, maxEntries) {
				added = true
			}
		}
		if added {
			setCheapest(rel)
		}
	}
}

// addOneBitmapPath builds a bitmap heap scan path for one index, or declines.
// Returns whether a path was added.
func (s *searchCtx) addOneBitmapPath(
	rel *RelOptInfo, tbl *catalog.Table, idx *catalog.Index,
	relPages int64, relTuples, T, totalPages float64, maxEntries int,
) bool {
	// A non-orderable AM (hash) has no ordered scan, but it CAN produce a
	// bitmap — PG's hash AM does not implement amgetbitmap, so in practice
	// only B-tree indexes reach here. Still, gate on the index being
	// scan-capable at all.
	if idx == nil {
		return false
	}
	// Partial index — same gate as addOneOrderedIndexPath: without a
	// predicate-implication prover the safe answer is to decline.
	if idx.HasPredicate {
		return false
	}
	// Skip if the index would produce a single row per probe — a plain
	// IndexScan is cheaper in that regime. PG's create_index_paths also
	// skips the bitmap path for unique-index single-row probes.
	if idx.Unique && relTuples <= 1 {
		return false
	}

	// Index geometry — same as the regular index scan cost model.
	indexPages, indexTuples, treeHeight := estimateIndexGeometry(idx, tbl, relTuples)

	// The bitmap index scan reads the entire index (selectivity 1.0), matching
	// the unparameterised case. When quals can be pushed into the index (future:
	// bitmap qual pushdown), selectivity drops and the bitmap becomes cheaper.
	in := indexScanInputs{
		relPages:    relPages,
		relTuples:   relTuples,
		indexPages:  indexPages,
		indexTuples: indexTuples,
		treeHeight:  treeHeight,
		selectivity: 1.0,
		correlation: indexCorrelationFor(idx, leadingKeyStats(idx, tbl)),
		totalTablePages: totalPages,
	}
	tuplesFetched := clampRowEst(in.selectivity * relTuples)

	// Cost the index-access side (bitmap index scan).
	idxCost := costBitmapIndexScan(s.cp, in)

	// Compute how many distinct heap pages the bitmap visits.
	pagesFetched := computeBitmapPages(tuplesFetched, T, maxEntries)

	// Total cost: index access (startup) + heap fetch (run).
	totalCost := costBitmapHeapScan(s.cp, idxCost, pagesFetched, tuplesFetched, T)

	// Build the bitmap index path (the child of the heap scan).
	// It carries the same index identity as a regular index scan path.
	bitmapIdxPath := &Path{
		Kind:          PathBitmapIndexScan,
		Rel:           rel,
		Rows:          tuplesFetched,
		Cost:          idxCost,
		IndexInfo:     idx,
		IndexScanDir:  NoMovementScanDirection,
		IndexClauses:  nil, // full scan, no quals pushed
		RequiredOuter: 0,
	}

	// Build the bitmap heap scan path (the outer container).
	addPath(rel, &Path{
		Kind:     PathBitmapHeapScan,
		Rel:      rel,
		Rows:     rel.Rows, // the rel's post-restriction row count
		Cost:     totalCost,
		Children: []*Path{bitmapIdxPath},
	})
	return true
}
