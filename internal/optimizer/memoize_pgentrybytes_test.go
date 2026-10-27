package optimizer

import "testing"

// TestPGMemoizeEntryOverheadBytesStructSizes pins
// ExecEstimateCacheEntryOverheadBytes's ported constants (nodeMemoize.c's
// MemoizeEntry=24, MemoizeKey=24, MemoizeTuple=16 on PG's LP64 layout) so a
// future edit cannot silently drift the fixed term (48) or the per-tuple slope
// (16) away from the C struct sizes they transcribe.
func TestPGMemoizeEntryOverheadBytesStructSizes(t *testing.T) {
	if got := pgMemoizeEntryOverheadBytes(0); got != 48 {
		t.Fatalf("pgMemoizeEntryOverheadBytes(0) = %v, want 48 (sizeof(MemoizeEntry)+sizeof(MemoizeKey))", got)
	}
	if got := pgMemoizeEntryOverheadBytes(10); got != 48+160 {
		t.Fatalf("pgMemoizeEntryOverheadBytes(10) = %v, want 208 (48 + 10*sizeof(MemoizeTuple))", got)
	}
}

// TestCostMemoizeRescanUnknownWidthFallsBackToGoopgCurrency: since M0147-0022
// promoted the PG currency, the goopg kvcache currency survives only for an
// unknown pathtarget width (pgRelationByteSize declines on width 0). In that
// fallback neither term may read a PG-only input: the per-key width the caller
// passes must not move the price.
func TestCostMemoizeRescanUnknownWidthFallsBackToGoopgCurrency(t *testing.T) {
	cp := defaultCostParams()
	cp.workMem = 64 * 1024
	inner := Cost{Startup: 1, Total: 100}
	narrow, narrowEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 0, 8)
	wide, wideEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 0, 4096)
	if narrow != wide || narrowEst != wideEst {
		t.Fatalf("unknown-width fallback leaked keyWidth sensitivity: narrow %+v/%d wide %+v/%d",
			narrow, narrowEst, wide, wideEst)
	}
	pg, pgEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 8)
	if pg == narrow && pgEst == narrowEst {
		t.Fatalf("a known width must price in the PG currency, not the fallback: %+v/%d", pg, pgEst)
	}
}

// TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols: the currency pin
// required by B2 — a wide PG pathtarget width must price
// MORE cache-entry bytes (fewer entries fit workMem, so the estimate leans
// toward the eviction-charged branch) than a narrow one, even though NCols is
// held constant; the two currencies must be able to disagree.
func TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols(t *testing.T) {
	cp := defaultCostParams()
	cp.workMem = 64 * 1024
	inner := Cost{Startup: 1, Total: 100}

	narrow, narrowEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 0)
	wide, wideEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 4096, 0)
	if narrow == wide && narrowEst == wideEst {
		t.Fatalf("PG-currency arm did not distinguish width=8 from width=4096: %+v/%d vs %+v/%d",
			narrow, narrowEst, wide, wideEst)
	}
	if wideEst > narrowEst {
		t.Fatalf("wider PG pathtarget width fit MORE cache entries than narrow: wide=%d narrow=%d", wideEst, narrowEst)
	}
}

// TestCostMemoizeRescanPGEntryBytesUsesKeyWidth is TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols's
// twin for M0139-0007c's ported per-key term: with everything else (including
// the pathtarget `width`) held constant, a wider `keyWidth` (the caller's
// `get_expr_width` sum) must price MORE cache-entry bytes than a narrower one.
func TestCostMemoizeRescanPGEntryBytesUsesKeyWidth(t *testing.T) {
	cp := defaultCostParams()
	cp.workMem = 64 * 1024
	inner := Cost{Startup: 1, Total: 100}

	narrow, narrowEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 8)
	wide, wideEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 4096)
	if narrow == wide && narrowEst == wideEst {
		t.Fatalf("PG-currency arm did not distinguish keyWidth=8 from keyWidth=4096: %+v/%d vs %+v/%d",
			narrow, narrowEst, wide, wideEst)
	}
	if wideEst > narrowEst {
		t.Fatalf("wider ported per-key width fit MORE cache entries than narrow: wide=%d narrow=%d", wideEst, narrowEst)
	}
}
