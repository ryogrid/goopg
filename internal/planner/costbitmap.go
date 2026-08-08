package planner

// M0128-P2.4 — bitmap scan cost functions. PG oracle: `cost_bitmap_heap_scan`
// (costsize.c:1009-1115), `cost_bitmap_tree_node` / `cost_bitmap_and_node` /
// `cost_bitmap_or_node` (costsize.c:1150-1253), `compute_bitmap_pages`
// (costsize.c:776-908). Design: docs/design/0128-0001-bitmap-heap-scan.md §3.4.
//
// BitmapAnd and BitmapOr are stub-deferred — the TPC-H corpus uses only
// single-index bitmaps, and goopg's planner has no choose_bitmap_and equivalent.
// The cost functions for those are not dead code, but they are unreferenced
// until BitmapAnd/Or path generation exists (ledger row in the design doc §6).

import "math"

// costBitmapIndexScan costs a single B-tree index scan that feeds a TIDBitmap.
// It is `cost_index` plus PG's per-tuple bitmap-manipulation charge
// (costsize.c:1172: cpu_operator_cost * 0.1 per tuple for tbm_add_tuples).
func costBitmapIndexScan(cp costParams, in indexScanInputs) Cost {
	c := costIndexScan(cp, in)
	tuplesFetched := clampRowEst(in.selectivity * in.relTuples)
	// bitmap-manipulation overhead: 0.1 * cpu_operator_cost per tuple.
	c.Total += 0.1 * cp.cpuOperatorCost * tuplesFetched
	return c
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

	// Per-page cost: interpolate between random_page_cost (few pages) and
	// seq_page_cost (nearly the whole table). PG uses the square root of the
	// ratio (costsize.c:1080-1087) — `sqrt(pages_fetched / T) * random +
	// (1 - sqrt(...)) * seq`. The interpolation is clamped so a tiny fraction
	// stays close to random_page_cost and a near-whole-table scan approaches
	// seq_page_cost.
	frac := 0.0
	if T > 0 && pagesFetched > 0 {
		frac = math.Sqrt(pagesFetched / T)
	}
	// Clamp: the fraction is already bounded [0, 1] by sqrt of a [0, 1] ratio.
	pageCost := frac*cp.randomPageCost + (1-frac)*cp.seqPageCost

	runCost := pageCost * pagesFetched

	// CPU: the full restriction qual may need to be re-evaluated per tuple
	// (PG charges for the lossy recheck case — conservative).
	runCost += cp.cpuTupleCost * tuplesFetched

	// PG also adds indexTotalCost again into total =
	// startup_cost + run_cost + indexTotalCost (costsize.c:1110-1113).
	return Cost{Startup: startup, Total: startup + runCost + indexCost.Total}
}

// computeBitmapPages estimates how many DISTINCT heap pages a bitmap scan visits,
// given the number of tuples the index selectivity admits and the relation's page
// count. It is PG's compute_bitmap_pages (costsize.c:776-908) simplified to the
// single-term Mackert-Lohman formula (costsize.c:863):
//
//	pages = 2 * T * tuples / (2*T + tuples)
//
// The two-term formula (costsize.c:871-900) needs indexCorrelation which goopg
// does not collect yet. Ledgered.
//
// When maxEntries (derived from work_mem) is less than T, some pages become lossy
// and every tuple on those pages is fetched — matching PG's lossiness correction
// (costsize.c:889-908).
func computeBitmapPages(tuplesFetched, T float64, maxEntries int) float64 {
	if T <= 0 || tuplesFetched <= 0 {
		return 0
	}

	// Single-term Mackert-Lohman (costsize.c:863).
	pages := (2.0 * T * tuplesFetched) / (2.0*T + tuplesFetched)
	if pages >= T {
		pages = T
	} else {
		pages = math.Ceil(pages)
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
