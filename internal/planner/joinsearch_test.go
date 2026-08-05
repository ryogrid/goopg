package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0127-P5.1 guards. Two independently-falsifiable claims are covered here,
// and they are separate because they fail for unrelated reasons:
//
//  1. the level lists and the relset map are two indexes over the SAME set of
//     rels and can never disagree about where a rel lives (the failure mode is
//     phase 2 pairing a rel with itself, or add_path pruning across a split
//     pathlist), and
//  2. `buildInitialRels` admits EVERY FROM item — the leaf-whitelist gap
//     `tryBushyDP` has (bushy.go:116-123), where one subquery / CTE / VALUES
//     item abandons join reordering for the whole statement.

func mkSeqScanLeaf(name string, rows int64) *SeqScan {
	tbl := statsTable(name, rows, rows)
	return &SeqScan{Table: tbl, schema: tableSchema(tbl)}
}

func mkRelInfo(t *testing.T, leaf *SeqScan, filtered int64) baseRelInfo {
	t.Helper()
	info := estimateBaseRelInfo(rangeBinding{table: leaf.Table}, leaf, nil)
	info.filteredRows = filtered
	return info
}

// TestSearchCtxFilesRelsByRelsetPopcount pins the invariant that a rel's level
// is DERIVED from its relset rather than passed in: PG's `join_rel_level[lev]`
// holds exactly the rels with `lev` members (allpaths.c:3475-3496), and the
// enumerator's phase-2 pairing (k, lev-k) is only sound if that holds.
func TestSearchCtxFilesRelsByRelsetPopcount(t *testing.T) {
	s, err := newSearchCtx(4, defaultCostParams())
	if err != nil {
		t.Fatalf("newSearchCtx: %v", err)
	}
	for _, relids := range []RelSet{0b0001, 0b0010, 0b0100, 0b1000, 0b0011, 0b1100, 0b0111, 0b1111} {
		if err := s.addRel(newRelOptInfo(relids, 10, 8)); err != nil {
			t.Fatalf("addRel(%#04b): %v", relids, err)
		}
	}
	for lev, want := range map[int]int{1: 4, 2: 2, 3: 1, 4: 1} {
		if got := len(s.levelRels(lev)); got != want {
			t.Errorf("level %d holds %d rels; want %d", lev, got, want)
		}
	}
	// Every registered relset resolves, base and composite alike — this is
	// what makes P5.3's makeJoinRel a find-or-create.
	for _, relids := range []RelSet{0b0001, 0b0011, 0b1111} {
		rel := s.findRel(relids)
		if rel == nil {
			t.Fatalf("findRel(%#04b) = nil", relids)
		}
		if rel.Relids != relids {
			t.Errorf("findRel(%#04b).Relids = %#04b", relids, rel.Relids)
		}
		// The map and the level list must hand back the SAME pointer, or
		// add_path would prune over two disjoint pathlists.
		found := false
		for _, r := range s.levelRels(relLevel(relids)) {
			if r == rel {
				found = true
			}
		}
		if !found {
			t.Errorf("findRel(%#04b) returned a rel absent from level %d", relids, relLevel(relids))
		}
	}
	if got := s.findRel(0b0101); got != nil {
		t.Errorf("findRel of an unregistered relset = %v; want nil", got)
	}
}

// TestSearchCtxRejectsDuplicateAndOutOfRangeRels: a second RelOptInfo over an
// already-registered relset is a caller bug (it splits the pathlist add_path
// prunes within), and a relset wider than the problem cannot be filed at all.
func TestSearchCtxRejectsDuplicateAndOutOfRangeRels(t *testing.T) {
	s, err := newSearchCtx(3, defaultCostParams())
	if err != nil {
		t.Fatalf("newSearchCtx: %v", err)
	}
	if err := s.addRel(newRelOptInfo(0b011, 10, 8)); err != nil {
		t.Fatalf("first addRel: %v", err)
	}
	if err := s.addRel(newRelOptInfo(0b011, 99, 8)); err == nil {
		t.Error("duplicate relset was accepted; the pathlist would be split across two rels")
	}
	if err := s.addRel(newRelOptInfo(0b1111, 10, 8)); err == nil {
		t.Error("a 4-member relset was accepted into a 3-relation problem")
	}
	if err := s.addRel(newRelOptInfo(0, 10, 8)); err == nil {
		t.Error("an empty relset was accepted")
	}
	if err := s.addRel(nil); err == nil {
		t.Error("a nil rel was accepted")
	}
	if _, err := newSearchCtx(maxSearchRels+1, defaultCostParams()); err == nil {
		t.Errorf("newSearchCtx accepted %d rels; RelSet is %d bits wide", maxSearchRels+1, maxSearchRels)
	}
	if _, err := newSearchCtx(0, defaultCostParams()); err == nil {
		t.Error("newSearchCtx accepted an empty join problem")
	}
}

// TestSearchCtxFinalRelContract: PG asserts exactly one rel at the top level
// (allpaths.c:3508-3512). goopg reports instead of asserting, because P5.3's
// answer to a failed search is the syntactic shape, not an error.
func TestSearchCtxFinalRelContract(t *testing.T) {
	s, _ := newSearchCtx(2, defaultCostParams())
	if _, err := s.finalRel(); err == nil {
		t.Error("finalRel on an unpopulated top level returned no error")
	}
	top := newRelOptInfo(0b11, 10, 8)
	if err := s.addRel(top); err != nil {
		t.Fatalf("addRel: %v", err)
	}
	got, err := s.finalRel()
	if err != nil || got != top {
		t.Fatalf("finalRel = (%v, %v); want the sole level-2 rel", got, err)
	}
	// Two rels at the final level cannot both be the answer.
	s.joinrels[2] = append(s.joinrels[2], newRelOptInfo(0b11, 10, 8))
	if _, err := s.finalRel(); err == nil {
		t.Error("finalRel accepted two rels at the final level")
	}
}

// TestBuildInitialRelsPopulatesLevelOne: one rel per FROM item, relids in FROM
// order, rows post-local-filter, and one costed path carrying the leaf node the
// pre-search pipeline chose.
func TestBuildInitialRelsPopulatesLevelOne(t *testing.T) {
	leaves := []*SeqScan{
		mkSeqScanLeaf("lineitem", 6_000_000),
		mkSeqScanLeaf("orders", 1_500_000),
		mkSeqScanLeaf("region", 5),
	}
	// The third rel carries a local filter: its post-filter count, not its
	// base count, is what the search must see (03 §2).
	filtered := []int64{6_000_000, 1_500_000, 1}
	bindings := make([]rangeBinding, len(leaves))
	scans := make([]Node, len(leaves))
	infos := make([]baseRelInfo, len(leaves))
	for i, leaf := range leaves {
		bindings[i] = rangeBinding{table: leaf.Table}
		scans[i] = leaf
		infos[i] = mkRelInfo(t, leaf, filtered[i])
	}

	s, err := buildInitialRels(bindings, scans, infos, defaultCostParams(), 0)
	if err != nil {
		t.Fatalf("buildInitialRels: %v", err)
	}
	if got := len(s.levelRels(1)); got != len(leaves) {
		t.Fatalf("level 1 holds %d rels; want %d", got, len(leaves))
	}
	if got := len(s.levelRels(2)); got != 0 {
		t.Errorf("level 2 is populated (%d rels) before the search runs", got)
	}
	for i, leaf := range leaves {
		rel := s.levelRels(1)[i]
		if want := RelSet(1) << uint(i); rel.Relids != want {
			t.Errorf("rel %d relids = %#04b; want %#04b (FROM order)", i, rel.Relids, want)
		}
		if s.findRel(rel.Relids) != rel {
			t.Errorf("rel %d is absent from the relset map", i)
		}
		if rel.Rows != float64(filtered[i]) {
			t.Errorf("rel %d rows = %v; want the post-filter %d", i, rel.Rows, filtered[i])
		}
		if len(rel.Pathlist) != 1 {
			t.Fatalf("rel %d has %d paths; want exactly 1", i, len(rel.Pathlist))
		}
		p := rel.Pathlist[0]
		if p.Kind != PathPrebuilt {
			t.Errorf("rel %d path kind = %v; want PathPrebuilt", i, p.Kind)
		}
		if p.node != Node(leaf) {
			t.Errorf("rel %d path does not carry the leaf node the pipeline chose", i)
		}
		if rel.CheapestTotal != p {
			t.Errorf("rel %d cheapest path was not set", i)
		}
		if p.Cost.Total <= 0 {
			t.Errorf("rel %d path cost = %v; a zero-cost leaf makes every join above it free", i, p.Cost)
		}
	}
	// Costs are in one currency and monotone in the relation's size, which is
	// the property the search's comparisons depend on.
	if s.levelRels(1)[0].CheapestTotal.Cost.Total <= s.levelRels(1)[2].CheapestTotal.Cost.Total {
		t.Error("the 6M-row rel is not costed above the 1-row rel")
	}
}

// TestBuildInitialRelsAdmitsNonTableLeaves is the leaf-whitelist gap closing
// (M0125-0034 / -0036, M0125-0037 stage (ii)). The SAME FROM list that makes
// `tryBushyDP` return the unreordered tree — because one item is a VALUES and
// another is a CTE scan — produces a complete level-1 population here, with the
// non-table rels' cardinality read off their own subtree rather than off the
// synthetic catalog.Table their binding carries.
func TestBuildInitialRelsAdmitsNonTableLeaves(t *testing.T) {
	tblLeaf := mkSeqScanLeaf("orders", 1_500_000)
	valuesSchema := Schema{{Name: "a", Type: catalog.Type{Name: "int4"}}}
	valuesLeaf := &Values{
		Rows:   [][]Expr{{&IntegerConst{}}, {&IntegerConst{}}, {&IntegerConst{}}},
		schema: valuesSchema,
	}
	cteBody := mkSeqScanLeaf("cte_body", 4242)
	cteLeaf := &CTEScan{Name: "c", Child: cteBody, schema: cteBody.schema}

	scans := []Node{tblLeaf, valuesLeaf, cteLeaf}
	// A synthetic binding table is what a subquery FROM item actually carries;
	// its zero row count is exactly why `filteredRows` must not be believed for
	// a non-table leaf.
	synthetic := &catalog.Table{Name: "synthetic"}
	bindings := []rangeBinding{
		{table: tblLeaf.Table}, {table: synthetic}, {table: synthetic},
	}
	infos := []baseRelInfo{
		mkRelInfo(t, tblLeaf, 1_500_000),
		{table: synthetic},
		{table: synthetic},
	}

	// Precondition: this FROM list is exactly what today's whitelist rejects.
	for i, n := range scans {
		switch n.(type) {
		case *SeqScan, *IndexScan, *MultiHashJoin:
			if i != 0 {
				t.Fatalf("leaf %d was expected to fall outside tryBushyDP's whitelist", i)
			}
		default:
			if i == 0 {
				t.Fatalf("leaf 0 was expected to be inside tryBushyDP's whitelist")
			}
		}
	}

	s, err := buildInitialRels(bindings, scans, infos, defaultCostParams(), 0)
	if err != nil {
		t.Fatalf("buildInitialRels rejected a FROM list with non-table leaves: %v", err)
	}
	if got := len(s.levelRels(1)); got != 3 {
		t.Fatalf("level 1 holds %d rels; want 3 — a non-table leaf must not shrink the search", got)
	}
	for i, want := range []float64{1_500_000, 3, 4242} {
		rel := s.levelRels(1)[i]
		if rel.Rows != want {
			t.Errorf("rel %d rows = %v; want %v (subtree estimate for a non-table leaf)", i, rel.Rows, want)
		}
		if len(rel.Pathlist) != 1 || rel.Pathlist[0].Kind != PathPrebuilt {
			t.Errorf("rel %d does not carry exactly one PathPrebuilt", i)
		}
		if rel.CheapestTotal == nil || rel.CheapestTotal.Cost.Total <= 0 {
			t.Errorf("rel %d has no positive-cost cheapest path", i)
		}
	}
	if s.levelRels(1)[1].Pathlist[0].node != Node(valuesLeaf) {
		t.Error("the VALUES rel does not carry the already-planned VALUES node")
	}
	if s.levelRels(1)[2].Pathlist[0].node != Node(cteLeaf) {
		t.Error("the CTE rel does not carry the already-planned CTEScan node")
	}
}

// TestInitialRelRowsFloorsAtOne: a 0-row initial rel would make every join
// above it free, which is why the bushy DP's no-zero-row-singleton invariant
// (cardinality.go:311) is restated here rather than inherited.
func TestInitialRelRowsFloorsAtOne(t *testing.T) {
	empty := &Values{schema: Schema{{Name: "a"}}}
	if got := initialRelRows(empty, baseRelInfo{}); got != 1 {
		t.Errorf("initialRelRows(empty VALUES) = %v; want the 1-row floor", got)
	}
	leaf := mkSeqScanLeaf("t", 100)
	if got := initialRelRows(leaf, baseRelInfo{filteredRows: 0}); got != 1 {
		t.Errorf("initialRelRows(0 filtered rows) = %v; want the 1-row floor", got)
	}
}

// TestBuildInitialRelsRejectsMalformedInput: the three per-FROM-item slices are
// positionally aligned, so a length disagreement is a wiring bug that must not
// silently plan a subset of the FROM list.
func TestBuildInitialRelsRejectsMalformedInput(t *testing.T) {
	leaf := mkSeqScanLeaf("t", 100)
	b := []rangeBinding{{table: leaf.Table}}
	info := []baseRelInfo{mkRelInfo(t, leaf, 100)}
	cp := defaultCostParams()

	if _, err := buildInitialRels(nil, nil, nil, cp, 0); err == nil {
		t.Error("an empty FROM list was accepted")
	}
	if _, err := buildInitialRels(b, []Node{leaf, leaf}, info, cp, 0); err == nil {
		t.Error("a scan/binding length mismatch was accepted")
	}
	if _, err := buildInitialRels(b, []Node{leaf}, nil, cp, 0); err == nil {
		t.Error("a relInfo/binding length mismatch was accepted")
	}
	if _, err := buildInitialRels(b, []Node{nil}, info, cp, 0); err == nil {
		t.Error("a nil leaf node was accepted")
	}
}

// TestPgShapedDPDefaultsOff: every P5 task lands dark (08 §2). If this ever
// fails without P5.9 having flipped it deliberately, an unmeasured enumerator
// is planning production queries.
func TestPgShapedDPDefaultsOff(t *testing.T) {
	if pgShapedDPEnabled() {
		t.Error("GOOPG_PGSHAPED_DP is on; the PG-shaped join search must soak dark until P5.9")
	}
}
