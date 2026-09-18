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

// TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy: the flag defaults off,
// and off must reproduce the exact pre-M0139-0007b currency (goopg's
// hashsize.EntryBytes/kvcache entry size) regardless of what width is passed —
// width is a PG-only input and must not leak into the legacy arm.
func TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy(t *testing.T) {
	restore := setPGMemoizeEntryBytesCostForTest(false)
	defer restore()
	cp := defaultCostParams()
	inner := Cost{Startup: 1, Total: 100}
	for _, width := range []int{0, 8, 700} {
		rescan, est := costMemoizeRescan(cp, inner, 50, 10000, 200, false, 4, 1, width, 0)
		want, wantEst := costMemoizeRescan(cp, inner, 50, 10000, 200, false, 4, 1, 0, 0)
		if width == 0 {
			continue // trivially equal to itself
		}
		if rescan != want || est != wantEst {
			t.Fatalf("width=%d switch-off diverged from width=0: got (%+v,%d) want (%+v,%d)",
				width, rescan, est, want, wantEst)
		}
	}
}

// TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols: the currency pin
// required by B2 — with the switch on, a wide PG pathtarget width must price
// MORE cache-entry bytes (fewer entries fit workMem, so the estimate leans
// toward the eviction-charged branch) than a narrow one, even though NCols is
// held constant; the two currencies must be able to disagree.
func TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols(t *testing.T) {
	restore := setPGMemoizeEntryBytesCostForTest(true)
	defer restore()
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

	// Off-switch must not see the same divergence from width alone (NCols is
	// unchanged, and the legacy currency never reads width).
	restoreOff := setPGMemoizeEntryBytesCostForTest(false)
	offNarrow, offNarrowEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 0)
	offWide, offWideEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 4096, 0)
	restoreOff()
	if offNarrow != offWide || offNarrowEst != offWideEst {
		t.Fatalf("legacy currency leaked width sensitivity: narrow %+v/%d wide %+v/%d",
			offNarrow, offNarrowEst, offWide, offWideEst)
	}
}

// TestCostMemoizeRescanPGEntryBytesUsesKeyWidth is TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols's
// twin for M0139-0007c's ported per-key term: with the PG currency on and
// everything else (including the pathtarget `width`) held constant, a wider
// `keyWidth` (the caller's `get_expr_width` sum) must price MORE cache-entry
// bytes than a narrower one, and the legacy off-currency arm must not see the
// same divergence — it never reads `keyWidth`.
func TestCostMemoizeRescanPGEntryBytesUsesKeyWidth(t *testing.T) {
	restore := setPGMemoizeEntryBytesCostForTest(true)
	defer restore()
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

	restoreOff := setPGMemoizeEntryBytesCostForTest(false)
	offNarrow, offNarrowEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 8)
	offWide, offWideEst := costMemoizeRescan(cp, inner, 200, 10000, 5000, false, 4, 1, 8, 4096)
	restoreOff()
	if offNarrow != offWide || offNarrowEst != offWideEst {
		t.Fatalf("legacy currency leaked keyWidth sensitivity: narrow %+v/%d wide %+v/%d",
			offNarrow, offNarrowEst, offWide, offWideEst)
	}
}
