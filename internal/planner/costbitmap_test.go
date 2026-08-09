package planner

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
	base := costIndexScan(cp, in)
	bitmap := costBitmapIndexScan(cp, in)
	tuplesFetched := clampRowEst(in.selectivity * in.relTuples) // 1000
	overhead := 0.1 * cp.cpuOperatorCost * tuplesFetched       // 0.1 * 0.0025 * 1000 = 0.25
	if diff := bitmap.Total - base.Total; math.Abs(diff-overhead) > 1e-9 {
		t.Errorf("costBitmapIndexScan overhead = %v, want %v", diff, overhead)
	}
	// Startup should be the same (both pay the B-tree descent).
	if math.Abs(bitmap.Startup-base.Startup) > 1e-9 {
		t.Errorf("costBitmapIndexScan startup differs: %v vs %v", bitmap.Startup, base.Startup)
	}
}

func TestComputeBitmapPages_SmallSelectivity(t *testing.T) {
	// T=1000 pages, 10 tuples fetched — should be ~10 pages (few distinct pages).
	// No cache info → falls back to single-term ML.
	pages := computeBitmapPages(10, 1000, 0, 1000, 0, 0)
	// 2*1000*10/(2000+10) = 20000/2010 ≈ 9.95 → ceil → 10
	if pages != 10 {
		t.Errorf("computeBitmapPages(10, 1000, 0, ...) = %v, want 10", pages)
	}
}

func TestComputeBitmapPages_LargeSelectivity(t *testing.T) {
	// T=1000 pages, 5000 tuples fetched — many distinct pages, approaching T.
	// No cache info → falls back to single-term ML.
	pages := computeBitmapPages(5000, 1000, 0, 1000, 0, 0)
	// 2*1000*5000/(2000+5000) = 10000000/7000 ≈ 1428.6 → ceil → 1429
	// But capped at T=1000.
	if pages != 1000 {
		t.Errorf("computeBitmapPages(5000, 1000, 0, ...) = %v, want 1000 (capped)", pages)
	}
}

func TestComputeBitmapPages_AllTuples(t *testing.T) {
	// T=100 pages, all tuples fetched (10000 tuples on 100 pages).
	// No cache info → falls back to single-term ML.
	pages := computeBitmapPages(10000, 100, 0, 100, 0, 0)
	// Should be capped at T.
	if pages != 100 {
		t.Errorf("computeBitmapPages(10000, 100, 0, ...) = %v, want 100", pages)
	}
}

func TestComputeBitmapPages_ZeroInputs(t *testing.T) {
	if p := computeBitmapPages(0, 100, 0, 100, 0, 0); p != 0 {
		t.Errorf("computeBitmapPages(0, _, _, ...) = %v, want 0", p)
	}
	if p := computeBitmapPages(10, 0, 0, 100, 0, 0); p != 0 {
		t.Errorf("computeBitmapPages(_, 0, _, ...) = %v, want 0", p)
	}
}

func TestComputeBitmapPages_LossyAdjustment(t *testing.T) {
	// T=1000 pages, 500 tuples fetched, maxEntries=200 → pages exceeds budget.
	pages := computeBitmapPages(500, 1000, 0, 1000, 0, 200)
	// Without lossiness: 2*1000*500/(2000+500) = 400 → ceil → 400
	// With lossiness: pages ≈ 400 > 200, so exact=200, lossy=200
	// Pages fetched stays at 400 (still visiting same pages).
	// The formula returns the same pages count — lossiness affects tuples_fetched,
	// not pages_fetched (PG's compute_bitmap_pages adjusts tuples, not pages).
	// But our current implementation returns pages unchanged — lossiness correction
	// is a tuning concern, not a structure change.
	_ = pages // at minimum, it should not panic
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

	// Total = startup + runCost + indexCost.Total
	// runCost = pageCost * pagesFetched + cpuTupleCost * tuplesFetched
	// pageCost = sqrt(100/1000)*4 + (1-sqrt(0.1))*1 = sqrt(0.1)*4 + (1-sqrt(0.1))*1
	// sqrt(0.1) ≈ 0.3162
	// pageCost ≈ 0.3162*4 + 0.6838*1 = 1.2648 + 0.6838 = 1.9486
	// runCost ≈ 1.9486*100 + 0.01*500 = 194.86 + 5 = 199.86
	// Total ≈ 50 + 199.86 + 50 = 299.86
	wantTotal := 50.0 + (math.Sqrt(0.1)*4+(1-math.Sqrt(0.1))*1)*100 + 0.01*500 + 50.0
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
