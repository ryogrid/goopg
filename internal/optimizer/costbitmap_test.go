package optimizer

import (
	"math"
	"testing"
)

func TestCostBitmapIndexScan_AddsBitmapOverhead(t *testing.T) {
	cp := defaultCostParams()
	in := indexScanInputs{
		relPages:    100,
		relTuples:   10000,
		indexPages:  50,
		indexTuples: 10000,
		treeHeight:  2,
		selectivity: 0.1,
		correlation: 0,
		totalTablePages: 200,
	}
	// The baseline is the INDEX SIDE, not the whole index scan. PG's
	// cost_bitmap_tree_node (costsize.c:1150) takes `indextotalcost` — what
	// amcostestimate returned — and adds only the bitmap-manipulation charge. A
	// bitmap index scan emits TIDs and never touches the heap; the
	// BitmapHeapScan above it is separately costed for exactly that.
	//
	// This assertion previously compared against costIndexScan, i.e. the WHOLE
	// scan including heap IO — so it agreed with an implementation that charged
	// the heap twice, once here invisibly and once where it belongs. It pinned
	// the defect rather than guarding against it.
	idxStartup, idxTotal := btreeIndexAMCost(cp, in)
	bitmap := costBitmapIndexScan(cp, in)
	tuplesFetched := clampRowEst(in.selectivity * in.relTuples) // 1000
	overhead := 0.1 * cp.cpuOperatorCost * tuplesFetched        // 0.1 * 0.0025 * 1000 = 0.25
	if diff := bitmap.Total - idxTotal; math.Abs(diff-overhead) > 1e-9 {
		t.Errorf("costBitmapIndexScan overhead over the index side = %v, want %v", diff, overhead)
	}
	if math.Abs(bitmap.Startup-idxStartup) > 1e-9 {
		t.Errorf("costBitmapIndexScan startup = %v, want the index-side startup %v", bitmap.Startup, idxStartup)
	}
	// And it must be strictly CHEAPER than a full index scan, which pays for
	// heap fetches this node does not perform.
	if base := costIndexScan(cp, in); bitmap.Total >= base.Total {
		t.Errorf("bitmap index side (%v) is not cheaper than a full index scan (%v); the heap is being charged twice again",
			bitmap.Total, base.Total)
	}
}

func TestComputeBitmapPages_SmallSelectivity(t *testing.T) {
	// T=1000 pages, 10 tuples fetched — should be ~10 pages (few distinct pages).
	// No cache info → falls back to single-term ML.
	pages, _ := computeBitmapPages(10, 10000, 1000, 0, 1000, 0, 0)
	// 2*1000*10/(2000+10) = 20000/2010 ≈ 9.95 → ceil → 10
	if pages != 10 {
		t.Errorf("computeBitmapPages(10, 1000, 0, ...) = %v, want 10", pages)
	}
}

func TestComputeBitmapPages_LargeSelectivity(t *testing.T) {
	// T=1000 pages, 5000 tuples fetched — many distinct pages, approaching T.
	// No cache info → falls back to single-term ML.
	pages, _ := computeBitmapPages(5000, 10000, 1000, 0, 1000, 0, 0)
	// 2*1000*5000/(2000+5000) = 10000000/7000 ≈ 1428.6 → ceil → 1429
	// But capped at T=1000.
	if pages != 1000 {
		t.Errorf("computeBitmapPages(5000, 1000, 0, ...) = %v, want 1000 (capped)", pages)
	}
}

func TestComputeBitmapPages_AllTuples(t *testing.T) {
	// T=100 pages, all tuples fetched (10000 tuples on 100 pages).
	// No cache info → falls back to single-term ML.
	pages, _ := computeBitmapPages(10000, 10000, 100, 0, 100, 0, 0)
	// Should be capped at T.
	if pages != 100 {
		t.Errorf("computeBitmapPages(10000, 100, 0, ...) = %v, want 100", pages)
	}
}

func TestComputeBitmapPages_ZeroInputs(t *testing.T) {
	if p, _ := computeBitmapPages(0, 1000, 100, 0, 100, 0, 0); p != 0 {
		t.Errorf("computeBitmapPages(0, _, _, ...) = %v, want 0", p)
	}
	if p, _ := computeBitmapPages(10, 1000, 0, 0, 100, 0, 0); p != 0 {
		t.Errorf("computeBitmapPages(_, 0, _, ...) = %v, want 0", p)
	}
}

func TestComputeBitmapPages_LossyAdjustment(t *testing.T) {
	// review/260831-2 OP1-5. When the bitmap cannot hold one entry per heap
	// page, the heap node rechecks every tuple on the lossy pages, so PG raises
	// tuples_fetched and leaves pages_fetched alone (costsize.c:6564-6588).
	// This used to compute the correction and throw it away with `_ =`, which
	// under-charged every lossy bitmap scan's CPU cost.
	//
	// T=1000 pages, 20000 live tuples, 500 tuples matched, maxEntries=200:
	//   pages     = 2*1000*500/(2000+500)      = 400   (also heap_pages)
	//   lossy     = max(0, 400 - 200/2)        = 300
	//   exact     = 400 - 300                  = 100
	//   tuples    = 500*(100/400) + (300/400)*20000 = 125 + 15000 = 15125
	pages, tuples := computeBitmapPages(500, 20000, 1000, 0, 1000, 0, 200)
	if pages != 400 {
		t.Errorf("pages = %v, want 400 (lossiness must not move the page count)", pages)
	}
	if tuples != 15125 {
		t.Errorf("tuples = %v, want 15125 (lossy-page tuples must be charged for)", tuples)
	}

	// A budget that covers the pages leaves tuples_fetched untouched — PG's
	// guard is `maxentries < heap_pages`, so an exactly-sufficient budget is
	// NOT lossy.
	if _, tuples := computeBitmapPages(500, 20000, 1000, 0, 1000, 0, 400); tuples != 500 {
		t.Errorf("non-lossy tuples = %v, want 500 (unadjusted)", tuples)
	}
	// maxEntries == 0 means "unlimited" (work_mem unset), never lossy.
	if _, tuples := computeBitmapPages(500, 20000, 1000, 0, 1000, 0, 0); tuples != 500 {
		t.Errorf("unlimited-budget tuples = %v, want 500 (unadjusted)", tuples)
	}
}

func TestBitmapMaxEntries_WorkMem(t *testing.T) {
	// work_mem=4MB → maxEntries
	entryBytes := tbmEntryBytes()
	want := int(4*1024*1024) / int(entryBytes)
	got := bitmapMaxEntries(4 * 1024 * 1024)
	if got != want {
		t.Errorf("bitmapMaxEntries(4MB) = %d, want %d (entryBytes=%d)", got, want, entryBytes)
	}
}

func TestBitmapMaxEntries_Zero(t *testing.T) {
	if got := bitmapMaxEntries(0); got != 0 {
		t.Errorf("bitmapMaxEntries(0) = %d, want 0 (unlimited)", got)
	}
	if got := bitmapMaxEntries(-1); got != 0 {
		t.Errorf("bitmapMaxEntries(-1) = %d, want 0 (unlimited)", got)
	}
}

func TestBitmapMaxEntries_Minimum(t *testing.T) {
	// Tiny work_mem should still produce at least 16 entries.
	got := bitmapMaxEntries(1)
	if got != 16 {
		t.Errorf("bitmapMaxEntries(1 byte) = %d, want 16 (minimum)", got)
	}
}

func TestCostBitmapHeapScan_Components(t *testing.T) {
	cp := defaultCostParams()
	idxCost := Cost{Startup: 5.0, Total: 50.0}
	pagesFetched := 100.0
	tuplesFetched := 500.0
	T := 1000.0 // 1000-page table

	cost := costBitmapHeapScan(cp, idxCost, pagesFetched, tuplesFetched, T)

	// Startup = indexCost.Total = 50.0
	if math.Abs(cost.Startup-50.0) > 1e-9 {
		t.Errorf("startup = %v, want 50", cost.Startup)
	}

	// Total = startup + runCost, and startup ALREADY holds indexCost.Total.
	// runCost = pageCost * pagesFetched + cpuTupleCost * tuplesFetched
	//
	// pageCost is PG's, verbatim (cost_bitmap_heap_scan, costsize.c:1071):
	//   random - (random - seq) * sqrt(pages_fetched / T)
	//         = 4 - 3*sqrt(0.1) ≈ 4 - 0.9487 = 3.0513
	// runCost ≈ 3.0513*100 + 0.01*500 = 305.13 + 5 = 310.13
	// Total   ≈ 50 + 310.13 = 360.13
	//
	// This expectation has now pinned TWO defects in turn, which is worth
	// recording as a property of the test rather than of either bug:
	//
	//  1. It first encoded `sqrt*random + (1-sqrt)*seq` — PG's interpolation
	//     RUN BACKWARDS, approaching sequential cost as the touched fraction
	//     SHRINKS. That kept Q2's `supplier` bitmap at 2588.7 vs PG's 43.46.
	//  2. It then encoded `+ indexCost.Total` a SECOND time, from a comment
	//     asserting PG does the same. PG does not: `cost_bitmap_heap_scan` ends
	//     `path->total_cost = startup_cost + run_cost` with `indexTotalCost`
	//     already inside `startup_cost`. That duplicate is what kept the same
	//     `supplier` bitmap at 66.42 vs PG's 43.46 after (1) was fixed.
	//
	// Both times the expectation was derived from the implementation, so it
	// agreed with it perfectly and could not have caught either. The figure
	// below is derived from the ORACLE's assembly instead.
	wantTotal := 50.0 + (4-3*math.Sqrt(0.1))*100 + 0.01*500
	if math.Abs(cost.Total-wantTotal) > 1e-9 {
		t.Errorf("Total = %v, want ~%v", cost.Total, wantTotal)
	}

	// Total should be > startup (heap access costs something).
	if cost.Total <= cost.Startup {
		t.Errorf("Total %v should be greater than startup %v", cost.Total, cost.Startup)
	}
}

func TestCostBitmapHeapScan_SmallFraction(t *testing.T) {
	cp := defaultCostParams()
	idxCost := Cost{Startup: 3.0, Total: 10.0}
	// 1 page out of 1000 → almost all random page cost.
	cost := costBitmapHeapScan(cp, idxCost, 1, 1, 1000)
	// pageCost ≈ sqrt(0.001)*4 + (1-sqrt(0.001))*1 ≈ 0.0316*4 + 0.9684*1 = 0.1264 + 0.9684 = 1.0948
	// runCost ≈ 1.0948*1 + 0.01*1 = 1.1048
	// Total ≈ 10 + 1.1048 + 10 = 21.1048
	if cost.Total <= cost.Startup {
		t.Errorf("tiny fraction should have positive run cost")
	}
}
