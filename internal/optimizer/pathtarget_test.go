package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor/hashsize"
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

// ---- P4-01 Slice 2: Target-driven build-side keep-set ----
//
// Slice 2 rewires `narrowBuildInput`'s keep-set from the NeededCols-by-name
// re-derivation to the inner path's Slice-1 Target (ordered emitted
// positions), with `narrowPlanOutput`'s ascending/unique/in-range guard
// EXACTLY as strict and a bit-identical fallback wherever the Target does not
// apply. These tests pin the derivation (`buildKeepSet`), the fallback
// identity, the coordinate-mismatch precondition (the IndexOnlyScan hazard:
// leaf positions must never index an index-ordered subset schema), and the
// model-currency arithmetic the gate is stated in.

// ptScanPath wraps a real Slice-1 Target computation in a scan path, the way
// generateScanPaths stamps it at path-creation time.
func ptScanPath(rel *RelOptInfo) *Path {
	tgt, known := scanPathTarget(rel)
	return &Path{Kind: PathSeqScan, Rel: rel, Rows: rel.Rows, Target: tgt, TargetKnown: known}
}

// TestBuildKeepSetUsesTargetOnAlignedScan: where the built node IS the leaf
// schema the Target indexes (the plain SeqScan case), the keep-set is the
// Target itself.
func TestBuildKeepSetUsesTargetOnAlignedScan(t *testing.T) {
	rel := ptRel([]string{"a", "b", "c", "d"}, map[string]bool{"b": true, "d": true}, true)
	p := ptScanPath(rel)
	if !p.TargetKnown || !ptEqualInts(p.Target, []int{1, 3}) {
		t.Fatalf("scan target = (%v, %v), want ([1 3], true)", p.Target, p.TargetKnown)
	}
	keep, ok := buildKeepSet(rel.baseLeaf, p)
	if !ok {
		t.Fatal("aligned scan declined the Target; want the Target arm")
	}
	if !ptEqualInts(keep, []int{1, 3}) {
		t.Fatalf("keep = %v, want the Target [1 3]", keep)
	}
}

// TestBuildKeepSetDeclinesWithoutTarget: every shape with no usable target —
// unknown, nil path/rel, a target with no leaf schema to check coordinates
// against, a nil node — reports false so the caller falls back.
func TestBuildKeepSetDeclinesWithoutTarget(t *testing.T) {
	rel := ptRel([]string{"a", "b"}, map[string]bool{"a": true}, true)
	node := rel.baseLeaf
	for _, tc := range []struct {
		name string
		node Node
		path *Path
	}{
		{"nil path", node, nil},
		{"unknown target", node, &Path{Kind: PathSeqScan, Rel: rel}},
		{"nil rel with known target", node, &Path{Kind: PathSeqScan, Target: []int{0}, TargetKnown: true}},
		{"known target but no leaf schema", node, &Path{
			Kind: PathSeqScan, Rel: newRelOptInfo(1, 100, 32),
			Target: []int{0}, TargetKnown: true,
		}},
		{"nil node", nil, ptScanPath(rel)},
	} {
		if keep, ok := buildKeepSet(tc.node, tc.path); ok || keep != nil {
			t.Errorf("%s: got (%v, %v); want (nil, false)", tc.name, keep, ok)
		}
	}
}

// TestNarrowBuildInputTargetArmNarrows: end to end through narrowBuildInput,
// the Target arm emits the same Project the NeededCols arm would — the
// provable identity (same function over the same schema and set) made
// observable.
func TestNarrowBuildInputTargetArmNarrows(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	rel := ptRel([]string{"a", "b", "c", "d"}, map[string]bool{"b": true, "d": true}, true)
	p := ptScanPath(rel)
	node := &noNode{sch: noSchema("a", "b", "c", "d")}
	lay := outputLayout{10, 11, 12, 13}

	got, gotLay := narrowBuildInput("PathHashJoin", node, lay, p)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("aligned scan build: expected a *Project, got %T", got)
	}
	if len(gotLay) != 2 || gotLay[0] != 11 || gotLay[1] != 13 {
		t.Errorf("layout = %v, want [11 13] (the kept coordinates)", gotLay)
	}
	if out := proj.Output(); len(out) != 2 || out[0].Name != "b" || out[1].Name != "d" {
		t.Errorf("schema = %v, want [b d]", out)
	}
	for i, want := range []int{1, 3} {
		cr, ok := proj.Targets[i].(*ColumnRef)
		if !ok {
			t.Fatalf("target %d is %T, want *ColumnRef", i, proj.Targets[i])
		}
		if cr.Index != want {
			t.Errorf("target %d indexes child column %d, want Target position %d", i, cr.Index, want)
		}
	}

	// And it is the SAME Project the fallback derivation builds: pin the
	// identity rather than re-deriving it by eye.
	want, wantLay := narrowPlanOutput(node, lay, neededKeepSet(node.Output(), rel.NeededCols))
	wantProj, ok := want.(*Project)
	if !ok {
		t.Fatalf("fallback arm: expected a *Project, got %T", want)
	}
	if len(proj.Targets) != len(wantProj.Targets) || len(gotLay) != len(wantLay) {
		t.Fatalf("Target arm targets/layout %d/%d disagree with fallback %d/%d",
			len(proj.Targets), len(gotLay), len(wantProj.Targets), len(wantLay))
	}
	for i := range proj.Targets {
		if proj.Targets[i].(*ColumnRef).Index != wantProj.Targets[i].(*ColumnRef).Index || gotLay[i] != wantLay[i] {
			t.Fatalf("Target arm diverges from fallback at %d; the arms must agree on an aligned scan", i)
		}
	}
}

// TestNarrowBuildInputCoordinateMismatchFallsBack: the IndexOnlyScan hazard.
// The leaf is {a,b,c} needing {a}, so the Target is leaf position [0]; the
// built node emits the covered columns in INDEX order {c,a}. Leaf position 0
// names c — applying it would keep the wrong column, and the
// ascending/unique/in-range guard cannot catch an in-range-but-shifted
// position. The derivation must decline the Target and fall back to the
// name-based keep, which correctly keeps a.
func TestNarrowBuildInputCoordinateMismatchFallsBack(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	rel := ptRel([]string{"a", "b", "c"}, map[string]bool{"a": true}, true)
	p := ptScanPath(rel)
	if !p.TargetKnown || !ptEqualInts(p.Target, []int{0}) {
		t.Fatalf("scan target = (%v, %v), want ([0], true)", p.Target, p.TargetKnown)
	}
	// The node an index-only path over index (c,a) builds: covered columns,
	// index order — a different coordinate space from the leaf.
	node := &noNode{sch: noSchema("c", "a")}
	lay := outputLayout{20, 21}

	if keep, ok := buildKeepSet(node, p); ok || keep != nil {
		t.Fatalf("buildKeepSet on an index-ordered subset = (%v, %v); want (nil, false)", keep, ok)
	}

	got, gotLay := narrowBuildInput("PathHashJoin", node, lay, p)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("expected the fallback *Project, got %T", got)
	}
	if out := proj.Output(); len(out) != 1 || out[0].Name != "a" {
		t.Errorf("fallback schema = %v, want [a] (the NEEDED column, not leaf position 0 = c)", out)
	}
	if len(gotLay) != 1 || gotLay[0] != 21 {
		t.Errorf("fallback layout = %v, want [21] (a's coordinate)", gotLay)
	}
}

// TestNarrowBuildInputSameLengthReorderedFallsBack: even a full-coverage index
// emitting every column in index order ({c,b,a} over leaf {a,b,c}) is a
// different coordinate space — the comparison is names IN ORDER, not a set —
// so the Target declines and the name-based fallback keeps the right column.
func TestNarrowBuildInputSameLengthReorderedFallsBack(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	rel := ptRel([]string{"a", "b", "c"}, map[string]bool{"a": true}, true)
	p := ptScanPath(rel)
	node := &noNode{sch: noSchema("c", "b", "a")}
	lay := outputLayout{30, 31, 32}

	if keep, ok := buildKeepSet(node, p); ok || keep != nil {
		t.Fatalf("buildKeepSet on a reordered full schema = (%v, %v); want (nil, false)", keep, ok)
	}
	got, gotLay := narrowBuildInput("PathHashJoin", node, lay, p)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("expected the fallback *Project, got %T", got)
	}
	if out := proj.Output(); len(out) != 1 || out[0].Name != "a" {
		t.Errorf("fallback schema = %v, want [a]", out)
	}
	if len(gotLay) != 1 || gotLay[0] != 32 {
		t.Errorf("fallback layout = %v, want [32] (a's coordinate in node order)", gotLay)
	}
}

// TestSameOutputColumnsComparesOrderNotSet: the precondition's comparison is
// names in order — length, then position by position.
func TestSameOutputColumnsComparesOrderNotSet(t *testing.T) {
	full := noSchema("a", "b", "c")
	if !sameOutputColumns(full, noSchema("a", "b", "c")) {
		t.Error("identical schemas compare unequal")
	}
	for _, tc := range []struct {
		name  string
		other Schema
	}{
		{"subset", noSchema("a", "b")},
		{"superset", noSchema("a", "b", "c", "d")},
		{"reordered", noSchema("c", "b", "a")},
		{"renamed", noSchema("a", "b", "x")},
	} {
		if sameOutputColumns(full, tc.other) {
			t.Errorf("%s schema compares equal to [a b c]", tc.name)
		}
	}
}

// TestSlice2WitnessModelArithmetic demonstrates the Slice-2 gate in MODEL
// currency — `hashsize.Choose` NBatch and `hashJoinCost` DPPATH totals, the
// same functions the search calls — on bench-scale inputs, with explicit
// budgets (unit currency pins no GUC default).
//
// Inputs: the P4-A §8 table (part build, 200 000 rows) and the Q9 retake
// witness (analysis/planner-refactor-take3/p401-retake-20260904/README.md,
// plan /tmp/opencode/p401-q9plan.txt): the orders⋈(partsupp⋈(lineitem⋈part))
// hash join, planner estimate 242 450 rows, actual 321 056 rows, build widths
// 30 cols full (16+9+5), 10 cols under today's name-based keep (the width the
// recorded runtime `Batches: 2` builds), 6 cols truly needed above the build.
// Budget 128 MB = bench work_mem 64 MB × hash_mem_multiplier 2, set
// explicitly: the planner-seen work_mem on a bench run is the owner's gate
// variable (compare the DPPATH join.hash total against the two totals below),
// not something this test can pin.
func TestSlice2WitnessModelArithmetic(t *testing.T) {
	const mb = int64(1) << 20

	// §8 table, pinned: at a flat 64 MB budget 200 000 full-width part rows
	// batch (NBatch 2) and narrowed 2-column rows fit (NBatch 1).
	if got := hashsize.Choose(200000, 9, 0, 64*mb); got.NBatch != 2 {
		t.Errorf("§8 before: Choose(200000, 9c) NBatch = %d, want 2", got.NBatch)
	}
	if got := hashsize.Choose(200000, 2, 0, 64*mb); got.NBatch != 1 {
		t.Errorf("§8 after: Choose(200000, 2c) NBatch = %d, want 1", got.NBatch)
	}
	if got := hashsize.Choose(200000, 2, 25, 64*mb); got.NBatch != 1 {
		t.Errorf("§8 after with payload: Choose(200000, 2c, 25B) NBatch = %d, want 1", got.NBatch)
	}

	// Witness ladder at the 128 MB bench-equivalent budget, estimate and
	// actual rows alike: full width batches twice over, today's runtime width
	// batches once (the recorded `Batches: 2`), the 6-column narrowing fits.
	for _, rows := range []float64{242450, 321056} {
		if got := hashsize.Choose(rows, 30, 0, 128*mb); got.NBatch != 4 {
			t.Errorf("witness cost input (%.0f rows, 30c): NBatch = %d, want 4", rows, got.NBatch)
		}
		if got := hashsize.Choose(rows, 10, 0, 128*mb); got.NBatch != 2 {
			t.Errorf("witness runtime width (%.0f rows, 10c): NBatch = %d, want 2", rows, got.NBatch)
		}
		if got := hashsize.Choose(rows, 6, 0, 128*mb); got.NBatch != 1 {
			t.Errorf("witness narrowed (%.0f rows, 6c): NBatch = %d, want 1", rows, got.NBatch)
		}
	}

	// Threshold correction (§8 R2): the narrowed build is ≈100 MB of inner
	// bytes at actual rows — "Width ≈100, not 6" (decimal MB, the unit the
	// retake README uses: 91586 kB witness build = "91.6 MB").
	if got, want := 321056*hashsize.EntryBytes(6, 0), 321056*312.0; got != want {
		t.Fatalf("EntryBytes(6, 0) arithmetic moved: 321056 × %v = %v, want %v",
			hashsize.EntryBytes(6, 0), got, want)
	} else if got < 99e6 || got > 101e6 {
		t.Errorf("narrowed inner bytes = %.1f MB, want ≈100 MB", got/1e6)
	}

	// DPPATH currency on the witness join: the same `hashJoinCost`
	// `addHashJoinPath` calls, with scan costs anchored to the retake EXPLAIN
	// (orders scan 0.00..97273.00 @1.5M rows; build side J2
	// 493404.33..563110.84 @242450 rows, single-key o_orderkey = l_orderkey).
	cp := defaultCostParams()
	cp.workMem = 128 * mb
	outer := costSeqscan(cp, 82273, 1500000, 0)
	inner := Cost{Startup: 493404.33, Total: 563110.84}
	inputs := func(innerCols int) hashJoinInputs {
		return hashJoinInputs{
			outer: outer, inner: inner,
			outerRows: 1500000, innerRows: 242450, outputRows: 242450,
			numHashClauses: 1, outerCols: 9, innerCols: innerCols,
		}
	}
	before, after := hashJoinCost(cp, inputs(30)), hashJoinCost(cp, inputs(6))
	// De-pricing the spill is exact: inner 43 329 + inner 43 329 + 2× outer
	// 83 497 pages at seq_page_cost.
	if got, want := before.Total-after.Total, 253652.0; got < want-1 || got > want+1 {
		t.Errorf("spill de-priced = %.0f, want %.0f (inner 43329 + inner 43329 + 2×outer 83497)", got, want)
	}
	// The §8 bar: join.hash 923 247 (above the 754 717 mergejoin bar) drops
	// to 669 589 (below it) once the build is costed narrowed.
	if before.Total <= 754717 {
		t.Errorf("before total = %.0f, want above the 754717 bar", before.Total)
	}
	if after.Total >= 754717 {
		t.Errorf("after total = %.0f, want below the 754717 bar", after.Total)
	}
}
