package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// P4-01 Slice 1 (planner-p4-01-target DESIGN, "Slice 1"): the scan Target is
// computed from NeededCols at path-creation time and NEVER applied — no
// createPlan change, no cost change. These tests pin the computation, the
// emitted-schema ORDER contract (ascending leaf-output positions, the Slice-2
// guard contract), the decline on unknown, and the NCols/AvgVarBytes
// consistency. The must-avoid list (comparePaths dims, cost readers, DPPATH
// format, EXPLAIN output) is honoured by changing none of those files — there
// is nothing to assert about them here beyond the cost-reader fallback pins
// below.

// ptRel builds a synthetic base rel whose leaf emits the named columns, with
// the given needed set stamped on it — the state `stampNeededColsOnRels`
// produces for a real base rel after `searchOneProblem` assigns the set.
func ptRel(names []string, needed map[string]bool, known bool) *RelOptInfo {
	rel := newRelOptInfo(1, 1000, 32)
	rel.baseLeaf = &noNode{sch: noSchema(names...)}
	rel.NCols = len(names)
	rel.NeededCols, rel.NeededColsKnown = needed, known
	return rel
}

// ptNames resolves target positions back to column names through the leaf.
func ptNames(t *testing.T, rel *RelOptInfo, tgt []int) []string {
	t.Helper()
	out := rel.baseLeaf.Output()
	got := make([]string, len(tgt))
	for i, c := range tgt {
		if c < 0 || c >= len(out) {
			t.Fatalf("target position %d out of range for %d-column output", c, len(out))
		}
		got[i] = out[c].Name
	}
	return got
}

// ptContains reports whether the named column's leaf-output position is in tgt.
func ptContains(rel *RelOptInfo, tgt []int, name string) bool {
	out := rel.baseLeaf.Output()
	for _, c := range tgt {
		if c >= 0 && c < len(out) && out[c].Name == name {
			return true
		}
	}
	return false
}

func ptEqualInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScanPathTargetSubsetOfOutput: a needed subset yields exactly the needed
// positions, and every one of them is inside the rel's output.
func TestScanPathTargetSubsetOfOutput(t *testing.T) {
	rel := ptRel([]string{"k", "v", "unused"}, map[string]bool{"k": true, "v": true}, true)
	tgt, known := scanPathTarget(rel)
	if !known {
		t.Fatal("known needed set declined; want a target")
	}
	if !ptEqualInts(tgt, []int{0, 1}) {
		t.Fatalf("target = %v, want [0 1]", tgt)
	}
	if got := ptNames(t, rel, tgt); len(got) != 2 || got[0] != "k" || got[1] != "v" {
		t.Fatalf("target names = %v, want [k v]", got)
	}
}

// TestScanPathTargetFullOutput: needing every column yields the whole schema.
func TestScanPathTargetFullOutput(t *testing.T) {
	rel := ptRel([]string{"a", "b"}, map[string]bool{"a": true, "b": true}, true)
	tgt, known := scanPathTarget(rel)
	if !known {
		t.Fatal("known needed set declined; want a target")
	}
	if !ptEqualInts(tgt, []int{0, 1}) {
		t.Fatalf("target = %v, want [0 1]", tgt)
	}
}

// TestScanPathTargetUnknownDeclines: every shape in which the needed set
// carries no information — the P4-01b lesson-1 ordering hazard (a scan path
// created before `stampNeededColsOnRels` runs) among them — records unknown
// rather than a wrong list, and never panics.
func TestScanPathTargetUnknownDeclines(t *testing.T) {
	for _, tc := range []struct {
		name string
		rel  *RelOptInfo
	}{
		{"collector declined", ptRel([]string{"a", "b"}, map[string]bool{"a": true}, false)},
		{"known but nil set", ptRel([]string{"a", "b"}, nil, true)},
		{"declined with nil set", ptRel([]string{"a", "b"}, nil, false)},
		{"no leaf schema", func() *RelOptInfo {
			r := newRelOptInfo(1, 100, 32)
			r.NeededCols, r.NeededColsKnown = map[string]bool{"a": true}, true
			return r
		}()},
		{"nil rel", nil},
	} {
		tgt, known := scanPathTarget(tc.rel)
		if known || tgt != nil {
			t.Errorf("%s: got (%v, %v); want (nil, false)", tc.name, tgt, known)
		}
	}
}

// TestScanPathTargetKnownEmpty: a known-but-empty needed set is information
// ("this rel supplies nothing the statement reads"), not ignorance — it
// yields an empty known target, distinct from the unknown decline above.
func TestScanPathTargetKnownEmpty(t *testing.T) {
	rel := ptRel([]string{"a", "b"}, map[string]bool{}, true)
	tgt, known := scanPathTarget(rel)
	if !known {
		t.Fatal("known empty set declined; want an empty known target")
	}
	if len(tgt) != 0 {
		t.Fatalf("target = %v, want []", tgt)
	}
}

// TestScanPathTargetOrderContract: the target is stored in EMITTED-SCHEMA
// order (ascending leaf-output positions) regardless of map iteration order —
// the Slice-2 guard contract, so `neededKeepSet`-style ascending checks pass
// without guard loosening.
func TestScanPathTargetOrderContract(t *testing.T) {
	rel := ptRel(
		[]string{"c0", "c1", "c2", "c3", "c4", "c5"},
		map[string]bool{"c5": true, "c1": true, "c3": true},
		true,
	)
	tgt, known := scanPathTarget(rel)
	if !known {
		t.Fatal("known needed set declined; want a target")
	}
	if !ptEqualInts(tgt, []int{1, 3, 5}) {
		t.Fatalf("target = %v, want ascending [1 3 5]", tgt)
	}
	for i := 1; i < len(tgt); i++ {
		if tgt[i] <= tgt[i-1] {
			t.Fatalf("target %v is not strictly ascending", tgt)
		}
	}
}

// TestScanPathTargetCoversJoinKeys: join keys are in the needed set by
// construction (`neededColumnNames` walks the whole statement, ON/WHERE
// included), so the scan target of each side covers its own key. The needed
// set here comes from the real collector over a parsed join, not a hand-made
// map, so the test pins the collector-to-target chain.
func TestScanPathTargetCoversJoinKeys(t *testing.T) {
	stmts, err := parser.Parse("select a.a_v from pt_a a, pt_b b where a.a_k = b.b_k")
	if err != nil {
		t.Fatal(err)
	}
	needed, known := neededColumnNames(stmts[0].(*parser.SelectStmt))
	if !known {
		t.Fatal("collector declined a plain two-table join; the fixture is wrong")
	}
	for _, name := range []string{"a_v", "a_k", "b_k"} {
		if !needed[name] {
			t.Fatalf("%q is referenced by the statement but missing from the needed set", name)
		}
	}

	// The equijoin's key split, as the search would place it.
	ri := &restrictInfo{
		leftKey:    &ColumnRef{Name: "a_k"},
		rightKey:   &ColumnRef{Name: "b_k"},
		leftRelids: 1, rightRelids: 2,
		isEquijoin: true,
	}
	relA := ptRel([]string{"a_k", "a_v", "a_unused"}, needed, known)
	relB := ptRel([]string{"b_k", "b_v", "b_unused"}, needed, known)

	tgtA, knownA := scanPathTarget(relA)
	tgtB, knownB := scanPathTarget(relB)
	if !knownA || !knownB {
		t.Fatalf("known targets declined: A=%v B=%v", knownA, knownB)
	}
	// Name-matched, per-side: the collector over-states across relations, so
	// each side's own key must be in its own target.
	if lk := ri.leftKey.(*ColumnRef); !ptContains(relA, tgtA, lk.Name) {
		t.Errorf("join key %q missing from A-side target %v", lk.Name, tgtA)
	}
	if rk := ri.rightKey.(*ColumnRef); !ptContains(relB, tgtB, rk.Name) {
		t.Errorf("join key %q missing from B-side target %v", rk.Name, tgtB)
	}
	// And the projected column rides along on its own side.
	if !ptContains(relA, tgtA, "a_v") {
		t.Errorf("projected column a_v missing from A-side target %v", tgtA)
	}
}

// TestGenerateScanPathsCarriesTarget: the real PathSeqScan producer stamps the
// target at path-creation time, while the width pair stays "not narrowed" so
// the cost readers fall back to the rel exactly as before.
func TestGenerateScanPathsCarriesTarget(t *testing.T) {
	rel := ptRel([]string{"a", "b", "c", "d"}, map[string]bool{"b": true, "d": true}, true)
	generateScanPaths(rel, defaultCostParams(), 100, 0, 0, true)
	if len(rel.Pathlist) != 1 {
		t.Fatalf("pathlist has %d paths, want 1", len(rel.Pathlist))
	}
	p := rel.Pathlist[0]
	if p.Kind != PathSeqScan {
		t.Fatalf("path kind = %v, want PathSeqScan", p.Kind)
	}
	if !p.TargetKnown || !ptEqualInts(p.Target, []int{1, 3}) {
		t.Errorf("seq path target = (%v, %v), want ([1 3], true)", p.Target, p.TargetKnown)
	}
	// Slice 1 changes no cost reader: NCols stays zero ("not narrowed") and
	// the pair resolves through the rel.
	if p.NCols != 0 {
		t.Errorf("seq path NCols = %d, want 0 (not narrowed in Slice 1)", p.NCols)
	}
	if got := pathNCols(p); got != 4 {
		t.Errorf("pathNCols = %d, want 4 (the rel fallback)", got)
	}
	if got := pathAvgVarBytes(p); got != rel.AvgVarBytes {
		t.Errorf("pathAvgVarBytes = %v, want the rel figure %v", got, rel.AvgVarBytes)
	}
}

// TestGenerateScanPathsUnknownStaysUnknown: a rel with no stamped set (a unit
// caller driving the producer directly, or a pre-stamp creation) yields scan
// paths with no target — never a wrong list.
func TestGenerateScanPathsUnknownStaysUnknown(t *testing.T) {
	rel := newRelOptInfo(1, 1000, 32)
	rel.baseLeaf = &noNode{sch: noSchema("a", "b")}
	generateScanPaths(rel, defaultCostParams(), 100, 0, 2, true)
	if len(rel.Pathlist) != 1 || len(rel.PartialPathlist) != 1 {
		t.Fatalf("serial/partial pathlists = %d/%d, want 1/1",
			len(rel.Pathlist), len(rel.PartialPathlist))
	}
	for _, p := range []*Path{rel.Pathlist[0], rel.PartialPathlist[0]} {
		if p.TargetKnown || p.Target != nil {
			t.Errorf("unstamped rel: path target = (%v, %v); want (nil, false)",
				p.Target, p.TargetKnown)
		}
	}
}

// TestIndexOnlyPathTargetMatchesWidth: the real index-only producer — the one
// scan path that already narrows — carries a target whose length agrees with
// the landed NCols/AvgVarBytes pair, naming exactly the covered columns.
func TestIndexOnlyPathTargetMatchesWidth(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pt_t"}, []catalog.Column{
		{Name: "k", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
		{Name: "unused", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "pt_t_kv"}, tbl,
		[]string{"k", "v"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	idxs := c.IndexesOnTable(tbl)
	if len(idxs) != 1 {
		t.Fatalf("got %d indexes, want 1", len(idxs))
	}

	s := &searchCtx{cp: defaultCostParams()}
	s.neededCols, s.neededColsKnown = map[string]bool{"k": true, "v": true}, true
	rel := ptRel([]string{"k", "v", "unused"}, s.neededCols, true)
	needed := s.neededColumnsOf(tbl)
	if len(needed) != 2 {
		t.Fatalf("neededColumnsOf = %v, want [k v]", needed)
	}
	if !s.addOneIndexOnlyPath(rel, tbl, idxs[0], needed, 100, 1000, 100) {
		t.Fatal("addOneIndexOnlyPath declined a covered index; the fixture is wrong")
	}
	p := rel.Pathlist[len(rel.Pathlist)-1]
	if !p.TargetKnown {
		t.Fatal("index-only path target unknown; want known")
	}
	if !ptEqualInts(p.Target, []int{0, 1}) {
		t.Fatalf("index-only target = %v, want [0 1]", p.Target)
	}
	// Consistency with the landed pair: the narrowed path emits exactly the
	// covered columns, so the target length is the column count.
	if len(p.Target) != p.NCols || p.NCols != 2 {
		t.Errorf("len(target)=%d disagrees with NCols=%d; want 2/2",
			len(p.Target), p.NCols)
	}
	if p.AvgVarBytes != coveredAvgVarBytes(tbl, p.IndexOnlyCovered) {
		t.Errorf("AvgVarBytes = %v, want the covered-columns figure", p.AvgVarBytes)
	}
}
