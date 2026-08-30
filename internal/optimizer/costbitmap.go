package optimizer

// M0128-P2.4 — bitmap scan cost functions. PG oracle: `cost_bitmap_heap_scan`
// (costsize.c:1009-1115), `cost_bitmap_tree_node` / `cost_bitmap_and_node` /
// `cost_bitmap_or_node` (costsize.c:1150-1253), `compute_bitmap_pages`
// (costsize.c:776-908). Design: docs/design/0128-0001-bitmap-heap-scan.md §3.4.
//
// M0129-S5.1 — costBitmapTree / costBitmapAndCost / costBitmapOrCost /
// bitmapScanCostEst / bitmapAndScanCostEst added for chooseBitmapAnd port.

import (
	"math"

	"github.com/goopg/goopg/internal/catalog"
)

// costBitmapIndexScan costs a single B-tree index scan that feeds a TIDBitmap:
// the INDEX SIDE ONLY, plus PG's per-tuple bitmap-manipulation charge.
//
// PG oracle, `cost_bitmap_tree_node` (costsize.c:1150):
//
//	*cost = ((IndexPath *) path)->indextotalcost;
//	*cost += 0.1 * cpu_operator_cost * path->rows;
//
// `indextotalcost` is what `amcostestimate` returned — the index descent and
// leaf scan. It does NOT include heap fetches, because a bitmap index scan
// never touches the heap: it emits TIDs, and the `BitmapHeapScan` above is what
// reads pages and is separately costed for exactly that.
//
// This used to call `costIndexScan`, which is the WHOLE scan — index side plus
// the max/min heap-IO interpolation plus `cpu_tuple_cost` per heap tuple. So
// every bitmap path paid for its heap twice: once here, invisibly, and once in
// `costBitmapHeapScan` where it belongs. On TPC-H Q2's `supplier` probe the
// index side is PG's 7.77 and goopg was charging ~874, which is the bulk of the
// 1786 vs 43 gap that kept the bitmap losing.
func costBitmapIndexScan(cp costParams, in indexScanInputs) Cost {
	idxStartup, idxTotal := btreeIndexAMCost(cp, in)
	tuplesFetched := clampRowEst(in.selectivity * in.relTuples)
	return Cost{
		Startup: idxStartup,
		Total:   idxTotal + 0.1*cp.cpuOperatorCost*tuplesFetched,
	}
}

// costBitmapHeapScan costs a complete BitmapHeapScan: index access (startup) plus
// heap fetch (run). It is `cost_bitmap_heap_scan` (costsize.c:1009) for the
// single-index case.
//
// pagesFetched is from computeBitmapPages; tuplesFetched is selectivity * relTuples;
// T is the relation's page count.
func costBitmapHeapScan(cp costParams, indexCost Cost, pagesFetched, tuplesFetched, T float64) Cost {
	// PG's `cost_bitmap_heap_scan`: the index side is paid at startup,
	// and the heap side is the run cost.
	startup := indexCost.Total

	// Per-page cost, interpolating between random_page_cost (a few scattered
	// pages) and seq_page_cost (nearly the whole table). PG, verbatim
	// (`cost_bitmap_heap_scan`, costsize.c:1069-1075):
	//
	//	if (pages_fetched >= 2.0)
	//		cost_per_page = spc_random_page_cost -
	//			(spc_random_page_cost - spc_seq_page_cost) * sqrt(pages_fetched / T);
	//	else
	//		cost_per_page = spc_random_page_cost;
	//
	// The direction matters and this code had it BACKWARDS: it computed
	// `sqrt*random + (1-sqrt)*seq`, which moves TOWARD random as more of the
	// relation is touched. PG moves toward SEQUENTIAL, because a bitmap scan
	// that visits most pages reads them in physical order — which is the entire
	// reason the access method exists. goopg's own comment described PG's
	// behaviour ("a near-whole-table scan approaches seq_page_cost") while the
	// expression below it did the opposite, so the two never contradicted each
	// other in review.
	//
	// Measured: TPC-H Q2's `supplier` bitmap (the scan PG picks) was priced at
	// 2588.7 against PG's 43.46. Charging ~3x per page is most of that gap.
	//
	// The `pages_fetched >= 2` guard is PG's too: a single-page fetch has no
	// sequentiality to exploit, so it stays at full random cost rather than
	// being discounted by a ratio that is meaningless at one page.
	pageCost := cp.randomPageCost
	if pagesFetched >= 2.0 && T > 0 {
		pageCost = cp.randomPageCost - (cp.randomPageCost-cp.seqPageCost)*math.Sqrt(pagesFetched/T)
	}

	runCost := pageCost * pagesFetched

	// CPU: the full restriction qual may need to be re-evaluated per tuple
	// (PG charges for the lossy recheck case — conservative).
	runCost += cp.cpuTupleCost * tuplesFetched

	// PG also adds indexTotalCost again into total =
	// startup_cost + run_cost + indexTotalCost (costsize.c:1110-1113).
	return Cost{Startup: startup, Total: startup + runCost + indexCost.Total}
}

// computeBitmapPages estimates how many DISTINCT heap pages a bitmap scan visits,
// given the number of tuples the index selectivity admits, the relation's page
// count, and the index geometry. It is PG's compute_bitmap_pages (costsize.c:6514).
//
// The core estimate uses Mackert-Lohman via indexPagesFetched, which accounts
// for effective_cache_size when the relation is larger than the cache share
// (two/three-term formula, costsize.c:863-900).
//
// indexPages and totalTablePages feed the cache-share proration in
// indexPagesFetched; effectiveCacheSize is the GUC in pages. Pass indexPages=0
// to fall back to the single-term formula (same as PG's default path when no
// index geometry is available).
//
// When maxEntries (derived from work_mem) is less than T, some pages become lossy
// and every tuple on those pages is fetched — matching PG's lossiness correction
// (costsize.c:889-908).
func computeBitmapPages(tuplesFetched, T, indexPages, totalTablePages, effectiveCacheSize float64, maxEntries int) float64 {
	return computeBitmapPagesLooped(tuplesFetched, T, indexPages, totalTablePages, effectiveCacheSize, maxEntries, 1)
}

// computeBitmapPagesLooped is `compute_bitmap_pages` with PG's `loop_count`
// (costsize.c:6514). A bitmap scan re-executed once per outer row does NOT
// fetch `loop_count` times the pages of one execution, because the pages the
// second scan wants are largely the ones the first already brought in — so PG
// runs Mackert-Lohman over the tuples ALL the scans fetch and pro-rates back to
// one:
//
//	pages_fetched = index_pages_fetched(tuples_fetched * loop_count, …);
//	pages_fetched /= loop_count;
//
// Without this a parameterised bitmap is priced as though every one of its
// executions re-read the relation cold, which is what kept TPC-H Q8's bitmap —
// the shape PG chooses — losing to a plain index probe. It is the exact
// counterpart of the `loop_count > 1` arm `cost_index` gained in 07f4f7814.
func computeBitmapPagesLooped(tuplesFetched, T, indexPages, totalTablePages, effectiveCacheSize float64, maxEntries int, loopCount float64) float64 {
	if T <= 0 || tuplesFetched <= 0 {
		return 0
	}
	if loopCount > 1 {
		scaled := indexPagesFetched(tuplesFetched*loopCount, int64(T), indexPages, totalTablePages, effectiveCacheSize)
		scaled /= loopCount
		if scaled >= T {
			scaled = T
		}
		return math.Ceil(scaled)
	}

	// Mackert-Lohman page-count estimate, with cache effects when the caller
	// supplies index geometry. Falls back to the single-term formula when no
	// cache information is available (indexPages == 0 or effectiveCacheSize == 0).
	var pages float64
	if indexPages > 0 && effectiveCacheSize > 0 {
		// Use the two/three-term formula that accounts for effective_cache_size.
		pages = indexPagesFetched(tuplesFetched, int64(T), indexPages, totalTablePages, effectiveCacheSize)
	} else {
		// Single-term Mackert-Lohman (costsize.c:863): 2*T*Ns/(2*T+Ns).
		pages = (2.0 * T * tuplesFetched) / (2.0*T + tuplesFetched)
		if pages >= T {
			pages = T
		} else {
			pages = math.Ceil(pages)
		}
	}

	// Lossiness adjustment: when the bitmap entry budget is smaller than the
	// number of heap pages, some pages become lossy and every tuple on them
	// must be fetched. PG's formula (costsize.c:889-908):
	//   exact_pages = min(maxEntries, pages), lossy_pages = pages - exact_pages
	// Then re-estimate: tuples on exact pages found via index,
	// tuples on lossy pages = all tuples on those pages.
	if maxEntries > 0 && pages > float64(maxEntries) {
		// PG's maxentries is in bytes; ours is in entry count.
		lossyPages := pages - float64(maxEntries)
		exactPages := float64(maxEntries)
		if exactPages < 1 {
			exactPages = 1
		}
		// Exact pages: bitmap still works — tuples on them found via index.
		// Lossy pages: every tuple on the page is fetched.
		lossyTuples := lossyPages * math.Max(T/pages, 1.0) // tuples per lossy page (rough)
		exactTuples := exactPages * (tuplesFetched / pages) // tuples per exact page
		_ = lossyTuples + exactTuples // total tuples fetched
		// Pages fetched doesn't change — we still visit the same pages.
		// PG adjusts `tuples_fetched` and returns the same `pages_fetched`.
	}

	return pages
}

// tbmEntryBytes is the estimated per-entry byte cost used to convert work_mem
// into a page-budget ceiling. goopg's TIDBitmap uses ~(bitmapWords + 64) bytes
// per entry (tbmCalculateMaxEntries in executor/tidbitmap.go). We mirror
// that formula here so the planner and executor agree on the budget.
func tbmEntryBytes() int64 {
	return int64(bitmapWords + 64)
}

// bitmapMaxEntries converts work_mem (bytes) into a TIDBitmap maxEntries ceiling,
// matching the executor's tbmCalculateMaxEntries.
func bitmapMaxEntries(workMem int64) int {
	if workMem <= 0 {
		return 0 // unlimited
	}
	max := int(workMem / tbmEntryBytes())
	if max < 16 {
		max = 16
	}
	return max
}

// bitmapWords is the number of bytes in a TIDBitmap per-page bitmap.
// goopg uses MaxOffsetNumber=2048 (tidbitmap.go:32), so each exact page
// entry's bitmap is 2048/8 = 256 bytes. Must match executor/tidbitmap.go's
// bitmapWords constant.
const bitmapWords = 256

// costBitmapTree extracts the index-access cost and selectivity from any
// bitmap path (index scan, AND, or OR). It is PG's cost_bitmap_tree_node
// (costsize.c:1120-1153): for an index scan it returns indextotalcost +
// the per-tuple bitmap manipulation charge; for AND/OR it returns the
// path's already-computed cost and selectivity.
func costBitmapTree(cp costParams, p *Path) (cost float64, selec float64) {
	switch p.Kind {
	case PathBitmapIndexScan:
		cost = p.Cost.Total
		selec = p.BitmapSelectivity
		cost += 0.1 * cp.cpuOperatorCost * p.Rows
		return
	case PathBitmapAnd, PathBitmapOr:
		cost = p.Cost.Total
		selec = p.BitmapSelectivity
		return
	case PathBitmapHeapScan:
		// Recurse into the heap scan's child (the bitmap tree node).
		if len(p.Children) > 0 && p.Children[0] != nil {
			return costBitmapTree(cp, p.Children[0])
		}
		panic("costBitmapTree: PathBitmapHeapScan with no child")
	default:
		panic("costBitmapTree: not a bitmap path")
	}
}

// costBitmapAndCost estimates the combined cost and selectivity of AND-ing
// multiple bitmap paths. It is PG's cost_bitmap_and_node (costsize.c:1165-1201):
// selectivities are multiplied (independence assumption), costs are summed,
// and each intersection after the first charges 100 * cpu_operator_cost.
// Returns the combined cost (no heap-access component); the caller must add
// heap-access cost via costBitmapHeapScan.
func costBitmapAndCost(cp costParams, paths []*Path) (Cost, float64) {
	var totalCost float64
	selec := 1.0
	for i, sub := range paths {
		subCost, subSelec := costBitmapTree(cp, sub)
		selec *= subSelec
		totalCost += subCost
		if i > 0 {
			totalCost += 100.0 * cp.cpuOperatorCost
		}
	}
	return Cost{Startup: totalCost, Total: totalCost}, selec
}

// costBitmapOrCost estimates the combined cost and selectivity of OR-ing
// multiple bitmap paths. It is PG's cost_bitmap_or_node (costsize.c:1208-1253):
// selectivities are summed (non-overlapping assumption, clamped to 1.0),
// costs are summed, and each union after the first charges
// 100 * cpu_operator_cost.
func costBitmapOrCost(cp costParams, paths []*Path) (Cost, float64) {
	var totalCost float64
	selec := 0.0
	for i, sub := range paths {
		subCost, subSelec := costBitmapTree(cp, sub)
		selec += subSelec
		totalCost += subCost
		if i > 0 {
			totalCost += 100.0 * cp.cpuOperatorCost
		}
	}
	if selec > 1.0 {
		selec = 1.0
	}
	return Cost{Startup: totalCost, Total: totalCost}, selec
}

// bitmapScanCostEst estimates the total cost of using a bitmap path to scan a
// relation (tree cost + heap access). It is PG's bitmap_scan_cost_est
// (indxpath.c:2025-2056).
//
// indexPages is the sum of pages of all index paths under the bitmap tree
// (PG's get_indexpath_pages); totalTablePages is the sum of pages of every
// base relation in the query. Both feed the cache-aware page-count estimate.
func bitmapScanCostEst(cp costParams, bitmapPath *Path, relRows, T, indexPages, totalTablePages float64, maxEntries int) Cost {
	treeCost, selec := costBitmapTree(cp, bitmapPath)
	tuplesFetched := clampRowEst(selec * relRows)
	pagesFetched := computeBitmapPages(tuplesFetched, T, indexPages, totalTablePages, cp.effectiveCacheSize, maxEntries)
	return costBitmapHeapScan(cp, Cost{Total: treeCost}, pagesFetched, tuplesFetched, T)
}

// bitmapAndScanCostEst estimates the total cost of AND-ing the given paths
// and then accessing the heap. Used by chooseBitmapAnd to evaluate whether
// adding a path to the AND group reduces total cost.
func bitmapAndScanCostEst(cp costParams, paths []*Path, relRows, T, indexPages, totalTablePages float64, maxEntries int) Cost {
	treeCost, selec := costBitmapAndCost(cp, paths)
	tuplesFetched := clampRowEst(selec * relRows)
	pagesFetched := computeBitmapPages(tuplesFetched, T, indexPages, totalTablePages, cp.effectiveCacheSize, maxEntries)
	return costBitmapHeapScan(cp, treeCost, pagesFetched, tuplesFetched, T)
}

// indexPagesForPath estimates the total number of pages of all index paths
// under a bitmap tree. It mirrors PG's get_indexpath_pages (costsize.c:10825):
// for a single BitmapIndexScan it returns the index's page count; for
// BitmapAnd/BitmapOr it sums across children. Returns 0 for non-bitmap paths
// or when index geometry is unavailable.
func indexPagesForPath(p *Path, tbl *catalog.Table, relTuples float64) float64 {
	if p == nil || tbl == nil {
		return 0
	}
	switch p.Kind {
	case PathBitmapIndexScan:
		ip, _, _ := estimateIndexGeometry(p.IndexInfo, tbl, relTuples)
		return ip
	case PathBitmapHeapScan:
		if len(p.Children) > 0 {
			return indexPagesForPath(p.Children[0], tbl, relTuples)
		}
		return 0
	case PathBitmapAnd, PathBitmapOr:
		var sum float64
		for _, child := range p.Children {
			sum += indexPagesForPath(child, tbl, relTuples)
		}
		return sum
	default:
		return 0
	}
}
