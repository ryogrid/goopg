package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// B-17d: retire producer-skipping for scans where PG counts instead of
// gating (take3 02 §1.2). The searched scan producers — seqscan,
// parameterised/ordered/index-only index scans, plain/parameterised bitmap
// scans — always generate their path and carry the session toggle in
// Path.DisabledNodes, exactly as the join producers do since P2-05.
//
// The generation gates that stay gates are pinned here too:
// enable_indexonlyscan (check_index_only), enable_memoize
// (get_memoize_path — see joinpathsmemoize_test.go), and the vacuous TID
// and incremental-sort halves, which have no producer at all. The
// rule-based legacy scan choice keeps its own declines (scan_toggles_test.go
// pins them): it has no cost competition to express a preference in.

// scanSearchWithCP builds the one-relation search the bitmap tests use, but
// under an explicit cost currency so the toggles under test are in scope
// from the first path — including the seqscan buildInitialRels adds.
func scanSearchWithCP(t *testing.T, tbl *catalog.Table, cp costParams) *searchCtx {
	t.Helper()
	leaf := &SeqScan{
		pos:    0,
		Table:  tbl,
		Alias:  "t",
		schema: tableSchema(tbl),
	}
	relInfos := []baseRelInfo{{
		table:        tbl,
		baseRows:     1000,
		filteredRows: 1000,
	}}
	bindings := []rangeBinding{{offset: 0}}
	s, err := buildInitialRels(bindings, []Node{leaf}, relInfos, cp, 0, nil)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}
	return s
}

func scanPathsOfKind(pl []*Path, kind PathKind) []*Path {
	var out []*Path
	for _, p := range pl {
		if p.Kind == kind {
			out = append(out, p)
		}
	}
	return out
}

// TestEnableSeqScanCountsNotSkips: generateScanPaths still emits the seqscan
// with enable_seqscan=off, carrying DisabledNodes=1 instead of disappearing.
func TestEnableSeqScanCountsNotSkips(t *testing.T) {
	if ps := DefaultPlannerSettings(); !ps.EnableSeqScan {
		t.Fatal("enable_seqscan must default to ENABLED")
	}
	if cp := defaultCostParams(); !cp.enableSeqScan {
		t.Fatal("defaultCostParams must agree with DefaultPlannerSettings")
	}
	off := DefaultPlannerSettings()
	off.EnableSeqScan = false
	if off.costParams().enableSeqScan {
		t.Error("costParams() dropped EnableSeqScan=false")
	}

	newRel := func() *RelOptInfo { return newRelOptInfo(relsetOf(0), 1000, 32) }

	on := newRel()
	generateScanPaths(on, DefaultPlannerSettings().costParams(), 8, 0, 0, true)
	seqs := scanPathsOfKind(on.Pathlist, PathSeqScan)
	if len(seqs) != 1 || seqs[0].DisabledNodes != 0 {
		t.Fatalf("enabled seqscan: got %d paths DisabledNodes=%v, want 1 path at 0",
			len(seqs), disabledOf(seqs))
	}

	gone := newRel()
	generateScanPaths(gone, off.costParams(), 8, 0, 0, true)
	seqs = scanPathsOfKind(gone.Pathlist, PathSeqScan)
	if len(seqs) != 1 {
		t.Fatalf("disabled seqscan produced %d paths, want 1 (counted, not skipped)", len(seqs))
	}
	if seqs[0].DisabledNodes != 1 {
		t.Errorf("disabled seqscan DisabledNodes = %d, want 1", seqs[0].DisabledNodes)
	}
}

func disabledOf(pl []*Path) []int {
	out := make([]int, len(pl))
	for i, p := range pl {
		out[i] = p.DisabledNodes
	}
	return out
}

// TestEnableIndexScanCountsNotSkips: the ordered index producer still emits
// with enable_indexscan=off, carrying the count.
func TestEnableIndexScanCountsNotSkips(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	off := DefaultPlannerSettings()
	off.EnableIndexScan = false
	if off.costParams().enableIndexScan {
		t.Fatal("costParams() dropped EnableIndexScan=false")
	}
	s := scanSearchWithCP(t, tbl, off.costParams())
	rel := s.levelRels(1)[0]
	colExprs := map[string]Expr{"a": &ColumnRef{Name: "a"}}

	if !s.addOneOrderedIndexPath(rel, tbl, idx, colExprs, 8, 1000, 8) {
		t.Fatal("ordered index producer declined with enable_indexscan=off — it must count, not skip")
	}
	idxPaths := scanPathsOfKind(rel.Pathlist, PathIndexScan)
	if len(idxPaths) == 0 {
		t.Fatal("no index path in the pathlist after the producer ran")
	}
	for _, p := range idxPaths {
		if p.DisabledNodes != 1 {
			t.Errorf("disabled index path DisabledNodes = %d, want 1", p.DisabledNodes)
		}
	}
}

// TestEnableBitmapScanCountsNotSkips: buildOneBitmapPath still emits with
// enable_bitmapscan=off, carrying the count on the heap path.
func TestEnableBitmapScanCountsNotSkips(t *testing.T) {
	_, tbl, idx := testCatWithIdx(t)
	off := DefaultPlannerSettings()
	off.EnableBitmapScan = false
	if off.costParams().enableBitmapScan {
		t.Fatal("costParams() dropped EnableBitmapScan=false")
	}
	s := scanSearchWithCP(t, tbl, off.costParams())
	rel := s.levelRels(1)[0]
	rel.baseLeaf = &Filter{
		Child:     rel.baseLeaf,
		Predicate: &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 5}},
		LeafLocal: true,
	}
	p := s.buildOneBitmapPath(rel, tbl, idx, 8, 1000, 8, s.totalTablePages(), bitmapMaxEntries(s.cp.workMem), rel.baseLeaf)
	if p == nil {
		t.Fatal("bitmap producer declined with enable_bitmapscan=off — it must count, not skip")
	}
	if p.DisabledNodes != 1 {
		t.Errorf("disabled bitmap heap path DisabledNodes = %d, want 1", p.DisabledNodes)
	}
}

// TestIndexOnlyScanGateStaysAGate: enable_indexonlyscan=off still builds no
// index-only path, while enable_indexscan=off alone still builds it counted.
//
// The hard gate lives in the caller (addBaseRelIndexPaths consults
// indexOnlyHardDisabled = check_index_only), not in addIndexOnlyPaths itself,
// so this test drives the caller. Each arm clears the rel's pathlist first:
// with the seqscan competitor present, add_path would prune a cost-losing
// index-only path either way and the test could not tell gating from costing.
// An empty pathlist makes the producer's decision observable.
func TestIndexOnlyScanGateStaysAGate(t *testing.T) {
	cat, tbl, _ := testCatWithIdx(t)

	// Hard-gate unit pin: only enable_indexonlyscan=off hard-disables.
	if !indexOnlyHardDisabled(withScanToggles(cat, false, false, true)) {
		t.Fatal("indexOnlyHardDisabled = false with enable_indexonlyscan=off")
	}
	if indexOnlyHardDisabled(withScanToggles(cat, true, false, false)) {
		t.Fatal("indexOnlyHardDisabled = true with only enable_indexscan=off — " +
			"indexscan must count, not gate")
	}
	if indexOnlyHardDisabled(cat) {
		t.Fatal("indexOnlyHardDisabled = true with everything on")
	}

	// Hard gate end-to-end: indexonlyscan=off ⇒ no index-only path at all.
	s := scanSearchWithCP(t, tbl, defaultCostParams())
	s.neededCols, s.neededColsKnown = map[string]bool{"a": true}, true
	rel := s.levelRels(1)[0]
	rel.Pathlist = nil
	s.addBaseRelIndexPaths(withScanToggles(cat, false, false, true))
	for _, p := range scanPathsOfKind(rel.Pathlist, PathIndexScan) {
		if p.IndexOnly {
			t.Fatalf("an IndexOnlyScan path survived enable_indexonlyscan=off: %+v", p)
		}
	}

	// Counted: indexscan=off alone ⇒ the path is built with DisabledNodes=1.
	off := DefaultPlannerSettings()
	off.EnableIndexScan = false
	s2 := scanSearchWithCP(t, tbl, off.costParams())
	s2.neededCols, s2.neededColsKnown = map[string]bool{"a": true}, true
	rel2 := s2.levelRels(1)[0]
	rel2.Pathlist = nil
	s2.addBaseRelIndexPaths(withScanToggles(cat, true, false, false))
	found := false
	for _, p := range scanPathsOfKind(rel2.Pathlist, PathIndexScan) {
		if p.IndexOnly {
			found = true
			if p.DisabledNodes != 1 {
				t.Errorf("disabled index-only path DisabledNodes = %d, want 1", p.DisabledNodes)
			}
		}
	}
	if !found {
		t.Fatal("no index-only path with enable_indexscan=off — it must count, not skip")
	}
}
