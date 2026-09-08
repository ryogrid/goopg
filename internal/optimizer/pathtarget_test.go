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

// ---- P4-01 Slice 3 FIRST CUT: parent-aware joinrel keep-sets ----
//
// Slice 3 changes the keep-set SOURCE at the Slice-2 site
// (joinInputsFor → narrowBuildInput) from the statement-wide NeededCols to
// the parent-aware joinrel derivation: one scanjoin target from the union
// needed above the scan/join tree (F1), hash-only (F2), fixup-complete by
// fail-closed walks (F3), name-keyed with provably-safe over-keep on
// self-joins (F4). The three Slice-2 guards carry over unchanged. These
// tests pin the above-tree collector, the top-down derivation on a
// Q9-witness-shaped tree (11→6 at the witness level), the decline arms,
// the never-stamp-paths rule, and the model numbers the flip is stated in.

// TestOutputColumnNamesSkipsTreeInternalRefs: join keys and filter columns
// live in WHERE/ON — placed in-tree or residual-checked — so the above-tree
// set holds only the SELECT/GROUP/ORDER names.
func TestOutputColumnNamesSkipsTreeInternalRefs(t *testing.T) {
	stmts, err := parser.Parse("select a.a_v from pt_a a, pt_b b where a.a_k = b.b_k group by a.a_v order by a.a_v")
	if err != nil {
		t.Fatal(err)
	}
	out, known := outputColumnNames(stmts[0].(*parser.SelectStmt))
	if !known {
		t.Fatal("plain join declined; want a known above-tree set")
	}
	if !out["a_v"] {
		t.Errorf("projected/grouped/ordered a_v missing from %v", out)
	}
	for _, name := range []string{"a_k", "b_k"} {
		if out[name] {
			t.Errorf("tree-internal %q present in the above-tree set %v", name, out)
		}
	}
}

// TestOutputColumnNamesDeclinesLikeNeeded: every shape the needed collector
// declines, the above-tree collector declines too — an unaccounted
// above-tree reader is a dropped column.
func TestOutputColumnNamesDeclinesLikeNeeded(t *testing.T) {
	for _, sql := range []string{
		"select * from pt_a a",
		"with x as (select 1) select a.a_v from pt_a a",
		"select rank() over (order by a.a_v) from pt_a a",
	} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		if out, known := outputColumnNames(stmts[0].(*parser.SelectStmt)); known || out != nil {
			t.Errorf("%q: got (%v, %v); want (nil, false)", sql, out, known)
		}
	}
}

// TestOutputColumnNamesCollectsSublinkOuters: EXISTS correlations and IN
// operands are read above the tree (unnested spine / subplan), so the walk
// descends into sublink constructs alone — plain WHERE conjuncts still
// contribute nothing.
func TestOutputColumnNamesCollectsSublinkOuters(t *testing.T) {
	exists, err := parser.Parse("select a.a_v from pt_a a where exists (select 1 from pt_b b where b.b_k = a.a_k)")
	if err != nil {
		t.Fatal(err)
	}
	out, known := outputColumnNames(exists[0].(*parser.SelectStmt))
	if !known {
		t.Fatal("exists query declined; want a known set")
	}
	for _, name := range []string{"a_v", "a_k", "b_k"} {
		if !out[name] {
			t.Errorf("sublink outer %q missing from %v", name, out)
		}
	}

	in, err := parser.Parse("select a.a_v from pt_a a where a.a_k in (select b.b_k from pt_b b)")
	if err != nil {
		t.Fatal(err)
	}
	out, known = outputColumnNames(in[0].(*parser.SelectStmt))
	if !known {
		t.Fatal("in query declined; want a known set")
	}
	for _, name := range []string{"a_v", "a_k", "b_k"} {
		if !out[name] {
			t.Errorf("in outer %q missing from %v", name, out)
		}
	}

	plain, err := parser.Parse("select a.a_v from pt_a a where a.a_k = 1")
	if err != nil {
		t.Fatal(err)
	}
	out, known = outputColumnNames(plain[0].(*parser.SelectStmt))
	if !known {
		t.Fatal("plain filter declined; want a known set")
	}
	if out["a_k"] {
		t.Errorf("plain filter column a_k present in %v; plain conjuncts are tree-internal", out)
	}
}

// slice3Key builds an equijoin restrictInfo over two named columns, the
// shape the search places in HashKeys.
func slice3Key(l, r string, lids, rids RelSet) *restrictInfo {
	lk := &ColumnRef{Name: l}
	rk := &ColumnRef{Name: r}
	return &restrictInfo{
		leftKey: lk, rightKey: rk,
		leftRelids: lids, rightRelids: rids,
		isEquijoin: true,
		clause:     &BinaryOp{Op: parser.OpEq, Left: lk, Right: rk},
	}
}

// slice3Base builds a base rel + scan path pair over the named leaf columns.
func slice3Base(t *testing.T, ids RelSet, names []string, needed, out map[string]bool) (*RelOptInfo, *Path) {
	t.Helper()
	rel := newRelOptInfo(ids, 1000, 32)
	rel.baseLeaf = &noNode{sch: noSchema(names...)}
	rel.NCols = len(names)
	rel.NeededCols, rel.NeededColsKnown = needed, needed != nil
	rel.OutputCols, rel.OutputColsKnown = out, out != nil
	return rel, &Path{Kind: PathSeqScan, Rel: rel, Rows: rel.Rows}
}

// slice3Join builds a hash-join path over two child paths.
func slice3Join(ids RelSet, outer, inner *Path, keys ...*restrictInfo) (*RelOptInfo, *Path) {
	rel := newRelOptInfo(ids, 1000, 32)
	rel.NCols = 0
	return rel, &Path{Kind: PathHashJoin, Rel: rel, Rows: 1000, Children: []*Path{outer, inner}, HashKeys: keys}
}

// slice3WitnessTree builds the Q9-witness-shaped tree the Slice-3 gate is
// stated in: R(W(orders ⋈ J2(ps ⋈ J(line ⋈ part))) ⋈ supp), with the
// statement-wide needed set (11 names on the J2 build) and the above-tree
// set (the amount/expression columns). Returns the root path and the rels
// and schemas the assertions need.
func slice3WitnessTree(t *testing.T) (root *Path, rels map[string]*RelOptInfo, needed, out map[string]bool) {
	t.Helper()
	out = map[string]bool{
		"l_extendedprice": true, "l_discount": true, "l_quantity": true,
		"ps_supplycost": true, "o_orderdate": true, "s_name": true,
	}
	needed = map[string]bool{
		"l_extendedprice": true, "l_discount": true, "l_quantity": true,
		"ps_supplycost": true, "o_orderdate": true, "s_name": true,
		"o_orderkey": true, "l_orderkey": true,
		"l_partkey": true, "p_partkey": true,
		"ps_partkey": true, "ps_suppkey": true,
		"l_suppkey": true, "s_suppkey": true,
		"p_name": true,
	}
	rels = make(map[string]*RelOptInfo)
	var pPart, pLine, pPS, pOrders, pSupp *Path
	rels["part"], pPart = slice3Base(t, 1, []string{"p_partkey", "p_name", "p_mfgr"}, needed, out)
	rels["line"], pLine = slice3Base(t, 2, []string{"l_orderkey", "l_partkey", "l_suppkey", "l_extendedprice", "l_discount", "l_quantity", "l_comment"}, needed, out)
	rels["ps"], pPS = slice3Base(t, 4, []string{"ps_partkey", "ps_suppkey", "ps_supplycost", "ps_comment"}, needed, out)
	rels["orders"], pOrders = slice3Base(t, 8, []string{"o_orderkey", "o_custkey"}, needed, out)
	rels["supp"], pSupp = slice3Base(t, 16, []string{"s_suppkey", "s_name"}, needed, out)

	var pJ, pJ2, pW *Path
	rels["J"], pJ = slice3Join(1|2, pLine, pPart, slice3Key("l_partkey", "p_partkey", 2, 1))
	rels["J"].NeededCols, rels["J"].NeededColsKnown = needed, true
	rels["J"].OutputCols, rels["J"].OutputColsKnown = out, true
	rels["J2"], pJ2 = slice3Join(1|2|4, pPS, pJ,
		slice3Key("ps_partkey", "l_partkey", 4, 1|2),
		slice3Key("ps_suppkey", "l_suppkey", 4, 1|2))
	rels["J2"].NeededCols, rels["J2"].NeededColsKnown = needed, true
	rels["J2"].OutputCols, rels["J2"].OutputColsKnown = out, true
	rels["W"], pW = slice3Join(1|2|4|8, pOrders, pJ2, slice3Key("o_orderkey", "l_orderkey", 8, 1|2|4))
	rels["W"].NeededCols, rels["W"].NeededColsKnown = needed, true
	rels["W"].OutputCols, rels["W"].OutputColsKnown = out, true
	var pR *Path
	rels["R"], pR = slice3Join(1|2|4|8|16, pW, pSupp, slice3Key("l_suppkey", "s_suppkey", 1|2|4|8, 16))
	rels["R"].NeededCols, rels["R"].NeededColsKnown = needed, true
	rels["R"].OutputCols, rels["R"].OutputColsKnown = out, true
	return pR, rels, needed, out
}

// slice3KeepNames resolves a stamped JoinKeep against a schema to the kept
// names in schema order.
func slice3KeepNames(t *testing.T, rel *RelOptInfo, sch Schema) []string {
	t.Helper()
	if !rel.JoinKeepKnown {
		t.Fatalf("rel %#08x has no derived keep; want one", uint32(rel.Relids))
	}
	keep := neededKeepSet(sch, rel.JoinKeep)
	got := make([]string, len(keep))
	for i, c := range keep {
		got[i] = sch[c].Name
	}
	return got
}

func slice3EqualNames(a, b []string) bool {
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

// TestDeriveJoinKeepsWitnessShape: the top-down derivation on the
// Q9-witness-shaped tree. The witness build (J2) drops from the
// statement-wide 11 to the parent-aware 6 (below-only join keys and the
// part filter column fall out); every level keeps what the joins above it
// read; and no PATH is written (F1: derived joinrel tlists, never
// parent-stamped shared paths).
func TestDeriveJoinKeepsWitnessShape(t *testing.T) {
	root, rels, needed, _ := slice3WitnessTree(t)

	type pathSnap struct {
		tgt   []int
		known bool
		ncols int
	}
	var paths []*Path
	var snaps []pathSnap
	var collect func(p *Path)
	collect = func(p *Path) {
		if p == nil {
			return
		}
		paths = append(paths, p)
		snaps = append(snaps, pathSnap{p.Target, p.TargetKnown, p.NCols})
		for _, c := range p.Children {
			collect(c)
		}
	}
	collect(root)

	deriveJoinKeeps(root)

	for i, p := range paths {
		s := snaps[i]
		if !ptEqualInts(p.Target, s.tgt) || p.TargetKnown != s.known || p.NCols != s.ncols {
			t.Errorf("path %d mutated: target (%v,%v)/NCols %d, want (%v,%v)/%d",
				i, p.Target, p.TargetKnown, p.NCols, s.tgt, s.known, s.ncols)
		}
		if p.Rel.NeededColsKnown {
			got := 0
			for k := range p.Rel.NeededCols {
				if needed[k] {
					got++
				}
			}
			if got != len(needed) {
				t.Errorf("path %d rel NeededCols no longer the statement set", i)
			}
		}
	}

	j2sch := append(append(append(Schema(nil),
		rels["ps"].baseLeaf.Output()...),
		rels["line"].baseLeaf.Output()...),
		rels["part"].baseLeaf.Output()...)
	if got := slice3KeepNames(t, rels["J2"], j2sch); !slice3EqualNames(got,
		[]string{"ps_supplycost", "l_orderkey", "l_suppkey", "l_extendedprice", "l_discount", "l_quantity"}) {
		t.Errorf("J2 keep = %v, want [ps_supplycost l_orderkey l_suppkey l_extendedprice l_discount l_quantity]", got)
	}
	if got := neededKeepSet(j2sch, needed); len(got) != 11 {
		t.Errorf("statement-wide J2 keep = %d columns, want 11 (the Slice-2 width)", len(got))
	}

	jsch := append(append(Schema(nil), rels["line"].baseLeaf.Output()...), rels["part"].baseLeaf.Output()...)
	if got := slice3KeepNames(t, rels["J"], jsch); !slice3EqualNames(got,
		[]string{"l_orderkey", "l_partkey", "l_suppkey", "l_extendedprice", "l_discount", "l_quantity"}) {
		t.Errorf("J keep = %v, want [l_orderkey l_partkey l_suppkey l_extendedprice l_discount l_quantity]", got)
	}
	if got := slice3KeepNames(t, rels["part"], rels["part"].baseLeaf.Output()); !slice3EqualNames(got,
		[]string{"p_partkey"}) {
		t.Errorf("part keep = %v, want [p_partkey] (the filter column drops above its own join)", got)
	}
	if got := slice3KeepNames(t, rels["supp"], rels["supp"].baseLeaf.Output()); !slice3EqualNames(got,
		[]string{"s_suppkey", "s_name"}) {
		t.Errorf("supp keep = %v, want [s_suppkey s_name]", got)
	}
	// Hash outer sides are never narrowed: no stamps on the outer rels.
	// (Merge outer sides DO derive keeps — B-01a, pinned in
	// TestDeriveJoinKeepsMergeStampsBothSides; this tree is hash-only.)
	for _, name := range []string{"W", "R", "orders", "line", "ps"} {
		if rels[name].JoinKeepKnown {
			t.Errorf("outer rel %s carries a keep; only build sides derive one", name)
		}
	}
}

// TestDeriveJoinKeepsDeclines: every shape the derivation cannot prove safe
// stamps nothing and the caller falls back bit-identically.
func TestDeriveJoinKeepsDeclines(t *testing.T) {
	mkTree := func(t *testing.T, mutate func(w *Path, j2 *Path)) (root *Path, rels map[string]*RelOptInfo) {
		t.Helper()
		root, rels, _, _ = slice3WitnessTree(t)
		// Find W (parent of J2) and J2 in the fixed shape: root R children
		// are [W, supp], W children are [orders, J2].
		w := root.Children[0]
		j2 := w.Children[1]
		mutate(w, j2)
		return root, rels
	}
	t.Run("unknown above-tree set", func(t *testing.T) {
		root, rels, _, _ := slice3WitnessTree(t)
		for _, r := range rels {
			r.OutputCols, r.OutputColsKnown = nil, false
		}
		deriveJoinKeeps(root)
		for name, r := range rels {
			if r.JoinKeepKnown {
				t.Errorf("rel %s stamped without an above-tree set", name)
			}
		}
	})
	t.Run("uncollectable parent qual", func(t *testing.T) {
		root, rels := mkTree(t, func(w *Path, j2 *Path) {
			w.Residual = []*restrictInfo{{clause: &OuterColumnRef{}}}
		})
		deriveJoinKeeps(root)
		if rels["J2"].JoinKeepKnown {
			t.Error("J2 stamped under an uncollectable parent qual; want fallback")
		}
		if rels["J"].JoinKeepKnown || rels["part"].JoinKeepKnown {
			t.Error("levels below an uncollectable qual stamped; the poison must propagate down")
		}
		if !rels["supp"].JoinKeepKnown {
			t.Error("sibling level above the failure lost its stamp; the poison must not propagate up")
		}
	})
	t.Run("nested-loop build side", func(t *testing.T) {
		root, rels := mkTree(t, func(w *Path, j2 *Path) {
			nl := &Path{Kind: PathNestLoop, Rel: j2.Rel, Children: []*Path{j2}, HashKeys: w.HashKeys}
			w.Children[1] = nl
		})
		deriveJoinKeeps(root)
		if rels["J2"].JoinKeepKnown {
			t.Error("nested-loop-rooted build side stamped; NLI probe internals are uninventoried (F3)")
		}
	})
	t.Run("merge build side", func(t *testing.T) {
		// B-01a: a WELL-FORMED merge level stamps both sides (see
		// TestDeriveJoinKeepsMergeStampsBothSides). What still declines
		// is a MALFORMED merge — here a single-child node no producer
		// emits — which must stamp nothing rather than mis-derive.
		root, rels := mkTree(t, func(w *Path, j2 *Path) {
			m := &Path{Kind: PathMergeJoin, Rel: j2.Rel, Children: []*Path{j2}, HashKeys: w.HashKeys}
			w.Children[1] = m
		})
		deriveJoinKeeps(root)
		if rels["J2"].JoinKeepKnown {
			t.Error("malformed single-child merge stamped; want fallback")
		}
	})
	t.Run("ctid qual vetoes", func(t *testing.T) {
		root, rels := mkTree(t, func(w *Path, j2 *Path) {
			w.Residual = []*restrictInfo{{clause: &CTIDExpr{}}}
		})
		deriveJoinKeeps(root)
		if rels["J2"].JoinKeepKnown {
			t.Error("J2 stamped under a CTID qual; want fallback")
		}
	})
}

// TestNarrowBuildInputJoinKeepArm: end to end through narrowBuildInput, the
// parent-aware arm emits the narrower Project — and where it declines, the
// Slice-2 arms run unchanged.
func TestNarrowBuildInputJoinKeepArm(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	root, rels, _, _ := slice3WitnessTree(t)
	deriveJoinKeeps(root)
	j2rel := rels["J2"]
	j2sch := append(append(append(Schema(nil),
		rels["ps"].baseLeaf.Output()...),
		rels["line"].baseLeaf.Output()...),
		rels["part"].baseLeaf.Output()...)
	node := &noNode{sch: j2sch}
	lay := make(outputLayout, len(j2sch))
	for i := range lay {
		lay[i] = 100 + i
	}
	j2path := &Path{Kind: PathHashJoin, Rel: j2rel}

	got, gotLay := narrowBuildInput("PathHashJoin", node, lay, j2path)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("stamped join build: expected a *Project, got %T", got)
	}
	wantNames := []string{"ps_supplycost", "l_orderkey", "l_suppkey", "l_extendedprice", "l_discount", "l_quantity"}
	out := proj.Output()
	if len(out) != len(wantNames) {
		t.Fatalf("schema = %v, want the 6-column parent-aware keep", out)
	}
	for i, name := range wantNames {
		if out[i].Name != name {
			t.Errorf("schema[%d] = %q, want %q", i, out[i].Name, name)
		}
		if cr, isCol := proj.Targets[i].(*ColumnRef); !isCol || cr.Index < 0 || j2sch[cr.Index].Name != name {
			t.Errorf("target %d does not address %q in the child schema", i, name)
		}
		if gotLay[i] != lay[proj.Targets[i].(*ColumnRef).Index] {
			t.Errorf("layout[%d] = %d, want the kept coordinate", i, gotLay[i])
		}
	}
}

// TestJoinKeepSetEmptyDeclinesToSlice2: a derivation naming nothing in the
// built schema declines to the Slice-2 arms rather than emitting an empty
// (or full-width) Project.
func TestJoinKeepSetEmptyDeclinesToSlice2(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	rel := ptRel([]string{"a", "b", "c", "d"}, map[string]bool{"b": true, "d": true}, true)
	rel.JoinKeep, rel.JoinKeepKnown = map[string]bool{"zzz": true}, true
	p := ptScanPath(rel)
	node := rel.baseLeaf
	lay := outputLayout{10, 11, 12, 13}

	got, _ := narrowBuildInput("PathHashJoin", node, lay, p)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("expected the Target-arm *Project, got %T", got)
	}
	if out := proj.Output(); len(out) != 2 || out[0].Name != "b" || out[1].Name != "d" {
		t.Errorf("schema = %v, want the Slice-2 Target keep [b d]", out)
	}
}

// TestJoinKeepSetSelfJoinOverkeeps: same-named columns of different sources
// are kept together (F4) — the derivation never wrong-narrows a self-join,
// it just narrows less.
func TestJoinKeepSetSelfJoinOverkeeps(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	int4 := catalog.Type{Name: "int4"}
	sch := Schema{
		{Name: "n_nationkey", Type: int4, SourceTableIdx: 1},
		{Name: "n_name", Type: int4, SourceTableIdx: 1},
		{Name: "n_nationkey", Type: int4, SourceTableIdx: 2},
		{Name: "n_name", Type: int4, SourceTableIdx: 2},
	}
	rel := newRelOptInfo(3, 100, 32)
	rel.NeededCols, rel.NeededColsKnown = map[string]bool{"n_nationkey": true}, true
	rel.JoinKeep, rel.JoinKeepKnown = map[string]bool{"n_nationkey": true}, true
	p := &Path{Kind: PathHashJoin, Rel: rel}
	lay := outputLayout{1, 2, 3, 4}

	got, gotLay := narrowBuildInput("PathHashJoin", &noNode{sch: sch}, lay, p)
	proj, ok := got.(*Project)
	if !ok {
		t.Fatalf("expected a *Project, got %T", got)
	}
	out := proj.Output()
	if len(out) != 2 || len(gotLay) != 2 {
		t.Fatalf("schema/layout = %v/%v, want both nationkey copies kept", out, gotLay)
	}
	for i, wantSrc := range []int16{1, 2} {
		if out[i].Name != "n_nationkey" || out[i].SourceTableIdx != wantSrc {
			t.Errorf("kept[%d] = (%q, source %d), want (n_nationkey, source %d)",
				i, out[i].Name, out[i].SourceTableIdx, wantSrc)
		}
	}
}

// TestSlice3WitnessModelArithmetic states the Slice-3 gate in MODEL currency
// — `hashsize.Choose` NBatch and `hashJoinCost` DPPATH totals, the same
// functions the search calls — for the widths the derivation produces: the
// statement-wide 11 at the witness build against the parent-aware 6. The
// NBatch flip (2→1) and the join.hash bar crossing are the per-commit
// predictions (§13.6 lesson); the byte count pins "width ≈100, not 6".
func TestSlice3WitnessModelArithmetic(t *testing.T) {
	const mb = int64(1) << 20

	root, rels, needed, _ := slice3WitnessTree(t)
	deriveJoinKeeps(root)
	j2sch := append(append(append(Schema(nil),
		rels["ps"].baseLeaf.Output()...),
		rels["line"].baseLeaf.Output()...),
		rels["part"].baseLeaf.Output()...)
	stmtWidth := len(neededKeepSet(j2sch, needed))
	derivedWidth := len(neededKeepSet(j2sch, rels["J2"].JoinKeep))
	if stmtWidth != 11 {
		t.Fatalf("statement-wide witness width = %d, want 11", stmtWidth)
	}
	if derivedWidth != 6 {
		t.Fatalf("parent-aware witness width = %d, want 6", derivedWidth)
	}

	// The pinned regime (bench work_mem 64 MB × hash_mem_multiplier 2):
	// estimate and actual rows alike, the statement width batches once and
	// the derived width fits.
	for _, rows := range []float64{242450, 321056} {
		if got := hashsize.Choose(rows, stmtWidth, 0, 128*mb); got.NBatch != 2 {
			t.Errorf("statement width (%.0f rows, %dc): NBatch = %d, want 2", rows, stmtWidth, got.NBatch)
		}
		if got := hashsize.Choose(rows, derivedWidth, 0, 128*mb); got.NBatch != 1 {
			t.Errorf("derived width (%.0f rows, %dc): NBatch = %d, want 1", rows, derivedWidth, got.NBatch)
		}
	}
	// The literal 10→6 prediction from the live witness behaves the same.
	for _, rows := range []float64{242450, 321056} {
		if got := hashsize.Choose(rows, 10, 0, 128*mb); got.NBatch != 2 {
			t.Errorf("live statement width (%.0f rows, 10c): NBatch = %d, want 2", rows, got.NBatch)
		}
		if got := hashsize.Choose(rows, 6, 0, 128*mb); got.NBatch != 1 {
			t.Errorf("live derived width (%.0f rows, 6c): NBatch = %d, want 1", rows, got.NBatch)
		}
	}

	// Threshold correction: the narrowed build is ≈100 MB of inner bytes at
	// actual rows (decimal MB, the unit the retake README uses).
	if got := 321056 * hashsize.EntryBytes(6, 0); got < 99e6 || got > 101e6 {
		t.Errorf("narrowed inner bytes = %.1f MB, want ≈100 MB", got/1e6)
	}

	// DPPATH currency on the witness join (same anchors as the Slice-2
	// gate): the derived width costs join.hash below the 754 717 bar.
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
	if got := hashJoinCost(cp, inputs(derivedWidth)); got.Total >= 754717 {
		t.Errorf("derived join.hash = %.0f, want below the 754717 bar", got.Total)
	}
	if wide, narrow := hashJoinCost(cp, inputs(stmtWidth)), hashJoinCost(cp, inputs(derivedWidth)); narrow.Total >= wide.Total {
		t.Errorf("derived total %.0f not below statement total %.0f", narrow.Total, wide.Total)
	}
}

// ---- P4-01 Slice 3 SECOND CUT: wake the derivation over prebuilt leaves,
// decline correlated bodies ----
//
// Diagnosis (instrumented, not assumed): on the TPC-H Q9 witness shape the
// first cut is dormant — OutputCols IS stamped (the DT inner statement is
// the top problem of its own planSelect call, so outputEligible holds and
// the seed is the 6 above-tree names) — but zero JoinKeeps land, because
// every leaf path in the chosen tree is a PathPrebuilt (leaf-local filters
// such as p_name LIKE '%green%' force the prebuilt, and cost ties keep it
// elsewhere), and joinSubtreeNarrowable vetoed prebuilt subtrees. The
// sub-joinlist-ineligibility hypothesis is refuted for this shape: the
// problem is flat and eligible; the veto is the dormancy.
//
// The wake-up treats PathPrebuilt as a narrowable boundary leaf: keeps apply
// ABOVE the built subtree by name, interiors run below. The correlated-body
// decline (corrAbove) is its load-bearing companion, found by the Q2
// acceptance test: an unnest agg body's group/probe keys read body-local
// columns above the body tree that no collector sees, so a correlated body
// narrows by the Slice-2 arms only. LATERAL needs no new code (seam + needed
// declines already cover it) and is pinned below.

// slice3Q9Catalog builds the six TPC-H relations at realistic widths with
// analyzed stats (no indexes — the live Q9 baseline is all seq scans), so
// the 6-way comma join plans as hash joins the derivation can narrow.
func slice3Q9Catalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	types := map[string]string{"p_name": "text", "o_orderdate": "date"}
	rows := map[string]int64{
		"part": 200_000, "supplier": 10_000, "lineitem": 6_000_000,
		"partsupp": 800_000, "orders": 1_500_000, "nation": 25,
	}
	mk := func(name string, cols ...string) {
		t.Helper()
		cc := make([]catalog.Column, len(cols))
		for i, cn := range cols {
			ty := "int4"
			if v, ok := types[cn]; ok {
				ty = v
			}
			cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: ty}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
		if err != nil {
			t.Fatal(err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows[name], Pages: int(rows[name] / 100), Analyzed: true})
	}
	mk("part", "p_partkey", "p_name", "p_mfgr", "p_brand", "p_type", "p_size", "p_container", "p_retailprice", "p_comment")
	mk("supplier", "s_suppkey", "s_name", "s_address", "s_nationkey", "s_phone", "s_acctbal", "s_comment")
	mk("lineitem", "l_orderkey", "l_partkey", "l_suppkey", "l_linenumber", "l_quantity", "l_extendedprice", "l_discount", "l_tax", "l_returnflag", "l_linestatus", "l_shipdate", "l_commitdate", "l_receiptdate", "l_shipinstruct", "l_shipmode", "l_comment")
	mk("partsupp", "ps_partkey", "ps_suppkey", "ps_availqty", "ps_supplycost", "ps_comment")
	mk("orders", "o_orderkey", "o_custkey", "o_orderstatus", "o_totalprice", "o_orderdate", "o_orderpriority", "o_clerk", "o_shippriority", "o_comment")
	mk("nation", "n_nationkey", "n_name", "n_regionkey", "n_comment")
	return c
}

// slice3Q9Inner is the live Q9 derived-table body verbatim in shape: six
// comma joins, equi-keys, and the single-table p_name LIKE filter.
const slice3Q9Inner = `select n_name as nation, extract(year from o_orderdate) as o_year, l_extendedprice * (1 - l_discount) - ps_supplycost * l_quantity as amount from part, supplier, lineitem, partsupp, orders, nation where s_suppkey = l_suppkey and ps_suppkey = l_suppkey and ps_partkey = l_partkey and p_partkey = l_partkey and o_orderkey = l_orderkey and s_nationkey = n_nationkey and p_name like '%green%'`

// slice3Q9Full is the live Q9 shape: the six-way tree owned by derived
// table profit, consumed under published aliases.
const slice3Q9Full = `select nation, o_year, sum(amount) as sum_profit from (` + slice3Q9Inner + `) profit group by nation, o_year order by nation, o_year desc`

// slice3BuildProjects collects the build-side narrowing Projects: a Project
// that is a direct input of a *Join, strictly narrower than its own child,
// and renaming nothing (a narrowing Project keeps a name-subset of its
// child's schema in order). Target, boundary-republish (never narrower than
// its child) and aggregate-input Projects never match — only a build-side
// narrow sits directly under a join while dropping columns. In particular a
// derived table's own target list (which renames, e.g. l_orderkey AS o)
// never matches even where it feeds a lateral join directly.
func slice3BuildProjects(n Node) []*Project {
	return slice3BuildProjectsExcept(n, nil)
}

// slice3ProjectNames returns a Project's output column names in order.
func slice3ProjectNames(p *Project) []string {
	out := p.Output()
	got := make([]string, len(out))
	for i, c := range out {
		got[i] = c.Name
	}
	return got
}

// slice3NameSet returns the output names as a set.
func slice3NameSet(p *Project) map[string]bool {
	dst := make(map[string]bool, len(p.Output()))
	for _, c := range p.Output() {
		dst[c.Name] = true
	}
	return dst
}

// slice3SetEqual reports whether got holds exactly the want names.
func slice3SetEqual(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

// slice3FiltersMentioning returns every *Filter whose predicate mentions the
// named column at the current scope.
func slice3FiltersMentioning(n Node, col string) []*Filter {
	var out []*Filter
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if f, ok := n.(*Filter); ok && f.Predicate != nil {
			seen := false
			if walkExprRefs(f.Predicate, scopeIgnore, exprVisitor{
				Visit: func(e Expr) bool {
					if cr, isCol := e.(*ColumnRef); isCol && cr.Name == col {
						seen = true
						return false
					}
					return true
				},
			}) && seen {
				out = append(out, f)
			}
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return out
}

// slice3NullPads counts the NULL-padded targets of a boundary Project.
func slice3NullPads(p *Project) int {
	n := 0
	for _, tg := range p.Targets {
		if _, isPad := tg.(*NullConst); isPad {
			n++
		}
	}
	return n
}

// slice3HasSearchedTree reports whether any subtree root carries the
// searched-tree tag.
func slice3HasSearchedTree(n Node) bool {
	found := false
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || found {
			return
		}
		if isSearchedTree(n) {
			found = true
			return
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// TestJoinSubtreeNarrowablePrebuiltBoundary pins the second-cut gate change
// and its carry-overs: a prebuilt leaf is a narrowable boundary, a hash tree
// over prebuilt leaves derives, and nested-loop subtrees still decline (F3
// fixup inventory — untouched). B-01a admits merge subtrees (sort-key
// preservation proved in narrowMergeInput) and transparent sorts.
func TestJoinSubtreeNarrowablePrebuiltBoundary(t *testing.T) {
	pre := &Path{Kind: PathPrebuilt}
	seq := &Path{Kind: PathSeqScan}
	if !joinSubtreeNarrowable(pre) {
		t.Error("PathPrebuilt leaf: got false, want true (narrowable boundary)")
	}
	if !joinSubtreeNarrowable(seq) {
		t.Error("PathSeqScan leaf: got false, want true")
	}
	hash := &Path{Kind: PathHashJoin, Children: []*Path{pre, seq}}
	if !joinSubtreeNarrowable(hash) {
		t.Error("hash over prebuilt/scan: got false, want true")
	}
	if !joinSubtreeNarrowable(&Path{Kind: PathMergeJoin, Children: []*Path{pre, seq}}) {
		t.Error("merge over prebuilt/scan: got false, want true (B-01a admits merge inputs)")
	}
	if !joinSubtreeNarrowable(&Path{Kind: PathSort, Children: []*Path{seq}}) {
		t.Error("sort over scan: got false, want true (absorbed sorts are transparent)")
	}
	if joinSubtreeNarrowable(&Path{Kind: PathSort, Children: []*Path{
		{Kind: PathNestLoop, Children: []*Path{pre, seq}},
	}}) {
		t.Error("sort over nested-loop: got true, want false (transparency is not a pardon)")
	}
	if joinSubtreeNarrowable(&Path{Kind: PathNestLoop, Children: []*Path{pre, seq}}) {
		t.Error("nested-loop subtree: got true, want false (F3: probe internals uninventoried)")
	}
	if joinSubtreeNarrowable(nil) {
		t.Error("nil path: got true, want false")
	}
}

// TestSlice3LiveQ9ShapeDerivation is regression test (a): the live Q9 shape
// derivation. The DT-owned six-way tree narrows the witness build 10→7 —
// not the 10→6 of the task brief, corrected with justification: the three
// dropped columns are exactly the below-point join keys (s_suppkey consumed
// at the witness root, s_nationkey/n_nationkey inside it); the surviving
// l_orderkey/l_partkey/l_suppkey are read by joins ABOVE the witness level
// in this join order. A 10→6 needs the orders link inside the witness
// subtree (consuming l_orderkey below the narrow point) — same rule, one
// order step away. Widths and per-level sets below are the gate prediction.
func TestSlice3LiveQ9ShapeDerivation(t *testing.T) {
	cat := slice3Q9Catalog(t)
	plan, err := Plan(parseOne(t, slice3Q9Full), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	njoins := 0
	var walkJoins func(n Node)
	walkJoins = func(n Node) {
		if n == nil {
			return
		}
		if j, ok := n.(*Join); ok {
			njoins++
			if j.Algo != JoinAlgoHash {
				t.Errorf("join algo = %v, want JoinAlgoHash (the witness shape is hash-only)", j.Algo)
			}
		}
		for _, c := range boundaryWalkChildren(n) {
			walkJoins(c)
		}
	}
	walkJoins(plan)
	if njoins != 5 {
		t.Fatalf("joins = %d, want 5", njoins)
	}

	builds := slice3BuildProjects(plan)
	if len(builds) != 5 {
		t.Fatalf("narrow builds = %d, want 5 (every hash build narrows)", len(builds))
	}
	wantSets := []map[string]bool{
		{"l_orderkey": true, "l_partkey": true, "l_suppkey": true, "l_quantity": true, "l_extendedprice": true, "l_discount": true, "n_name": true},
		{"s_suppkey": true, "n_name": true},
		{"n_nationkey": true, "n_name": true},
		{"p_partkey": true},
		{"ps_partkey": true, "ps_suppkey": true, "ps_supplycost": true},
	}
	matched := make([]bool, len(wantSets))
	for _, b := range builds {
		got := slice3NameSet(b)
		hit := -1
		for i, want := range wantSets {
			if !matched[i] && slice3SetEqual(got, keysOf(want)...) {
				hit = i
				break
			}
		}
		if hit < 0 {
			t.Errorf("unexpected narrow build %v", slice3ProjectNames(b))
			continue
		}
		matched[hit] = true
	}
	for i, want := range wantSets {
		if !matched[i] {
			t.Errorf("missing narrow build %v", keysOf(want))
		}
	}

	if out := plan.Output(); len(out) != 3 || out[0].Name != "nation" || out[1].Name != "o_year" || out[2].Name != "sum_profit" {
		t.Errorf("outer output = %v, want [nation o_year sum_profit]", out)
	}
	// The narrowed-away columns leave licensed holes at the search boundary:
	// positions stay aligned (pads), so every above-tree reader is stable.
	pads := 0
	var walkBoundary func(n Node)
	walkBoundary = func(n Node) {
		if n == nil {
			return
		}
		if p, isProj := n.(*Project); isProj && isSearchedTree(n) {
			pads += slice3NullPads(p)
		}
		for _, c := range boundaryWalkChildren(n) {
			walkBoundary(c)
		}
	}
	walkBoundary(plan)
	if pads == 0 {
		t.Error("no NULL pads at any searched boundary; the dropped columns must leave licensed holes")
	}
}

// keysOf returns the keys of a name set (for diagnostics).
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSlice3FilterColumnSurvivesNarrowing is regression test (b): Q9's p_name
// LIKE filter survives parent-aware narrowing. Trace: p_name is in NEITHER
// the out seed (WHERE refs are tree-internal by design) NOR any ancestor/at
// qual set (single-relation, leaf-local) — it survives because its Filter
// runs BELOW every narrow point, inside the part prebuilt leaf. The part
// build drops p_name (2→1) while the LIKE still filters.
func TestSlice3FilterColumnSurvivesNarrowing(t *testing.T) {
	cat := slice3Q9Catalog(t)
	plan, err := Plan(parseOne(t, slice3Q9Inner), cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	like := slice3FiltersMentioning(plan, "p_name")
	if len(like) != 1 {
		t.Fatalf("p_name filters = %d, want exactly 1 (the leaf-local LIKE)", len(like))
	}
	// The LIKE runs on un-narrowed rows: its subtree reaches a scan whose
	// schema still carries p_name.
	found := false
	var walkScan func(n Node)
	walkScan = func(n Node) {
		if n == nil || found {
			return
		}
		if s, ok := n.(*SeqScan); ok {
			for _, c := range s.Output() {
				if c.Name == "p_name" {
					found = true
					return
				}
			}
		}
		for _, c := range boundaryWalkChildren(n) {
			walkScan(c)
		}
	}
	walkScan(like[0])
	if !found {
		t.Error("LIKE filter has no p_name-carrying scan below it; the filter must run before narrowing")
	}
	// And no narrow build above it keeps p_name: the column is dropped after
	// filtering, never before.
	for _, b := range slice3BuildProjects(plan) {
		if slice3NameSet(b)["p_name"] {
			t.Errorf("narrow build %v keeps p_name; filter columns drop after filtering", slice3ProjectNames(b))
		}
	}
	partKept := false
	for _, b := range slice3BuildProjects(plan) {
		if slice3SetEqual(slice3NameSet(b), "p_partkey") {
			partKept = true
		}
	}
	if !partKept {
		t.Error("no [p_partkey]-only part build; want the 2→1 filter-column drop")
	}
	// No above-root residual sits over the searched tree reading dropped
	// columns: every WHERE conjunct is placed in-tree or leaf-local here.
	var checkResidual func(n Node, aboveBoundary bool)
	checkResidual = func(n Node, aboveBoundary bool) {
		if n == nil {
			return
		}
		if _, isProj := n.(*Project); isProj && isSearchedTree(n) {
			aboveBoundary = false
		}
		if _, isFilter := n.(*Filter); isFilter && aboveBoundary {
			t.Errorf("Filter above the search boundary: %T would read narrowed-away columns", n)
			return
		}
		for _, c := range boundaryWalkChildren(n) {
			checkResidual(c, aboveBoundary)
		}
	}
	checkResidual(plan, true)
}

// TestSlice3LateralDeclinesDerivation is regression test (c): LATERAL
// derived tables decline. The outer statement's needed set is unknown (a
// lateral rangevar declines the collector), so no outer build narrows; the
// correlated body is corrAbove-marked (its WHERE reads the outer level), so
// it narrows by the Slice-2 arms only — the orders build keeps the filter
// column o_orderpriority (3 cols) that parent-aware keeps would drop (2).
func TestSlice3LateralDeclinesDerivation(t *testing.T) {
	c := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cc := make([]catalog.Column, len(cols))
		for i, cn := range cols {
			ty := "int4"
			switch cn {
			case "o_orderdate":
				ty = "date"
			case "o_orderpriority":
				ty = "text"
			}
			cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: ty}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
		if err != nil {
			t.Fatal(err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true})
	}
	mk("supplier", 10_000, "s_suppkey", "s_name", "s_address", "s_nationkey", "s_phone", "s_acctbal", "s_comment")
	mk("nation", 500_000, "n_nationkey", "n_name", "n_regionkey", "n_comment")
	mk("lineitem", 6_000_000, "l_orderkey", "l_partkey", "l_suppkey", "l_linenumber", "l_quantity", "l_extendedprice", "l_discount", "l_tax", "l_returnflag", "l_linestatus", "l_shipdate", "l_commitdate", "l_receiptdate", "l_shipinstruct", "l_shipmode", "l_comment")
	mk("orders", 1_500_000, "o_orderkey", "o_custkey", "o_orderstatus", "o_totalprice", "o_orderdate", "o_orderpriority", "o_clerk", "o_shippriority", "o_comment")
	sql := `select s_name, n_name, dt.o, dt.od from supplier s, nation n, lateral (select l_orderkey as o, o_orderdate as od from lineitem l, orders o where l_orderkey = o_orderkey and l_suppkey = s.s_suppkey and o_orderpriority = '1-URGENT') dt where s_nationkey = n_nationkey`
	stmt := parseOne(t, sql)
	if nc, known := neededColumnNames(stmt.(*parser.SelectStmt)); known || nc != nil {
		t.Errorf("outer needed = (%v, %v); a lateral rangevar must decline the set", nc, known)
	}
	plan, err := Plan(stmt, c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	lat := findLateralJoin(plan)
	if lat == nil {
		t.Fatal("no Lateral join in the plan; the shape under test is gone")
	}
	if out := plan.Output(); len(out) != 4 || out[0].Name != "s_name" || out[1].Name != "n_name" || out[2].Name != "o" || out[3].Name != "od" {
		t.Errorf("outer output = %v, want [s_name n_name o od]", out)
	}
	// The outer statement is never searched (seam lateral decline)…
	if slice3HasSearchedTreeExcept(plan, lat.Right) {
		t.Error("searched subtree outside the lateral body; the outer search must decline on lateral")
	}
	// …so no outer build narrows (the needed set is unknown there)…
	for _, b := range slice3BuildProjectsExcept(plan, lat.Right) {
		t.Errorf("outer narrow build %v; lateral statements narrow nothing outside the body", slice3ProjectNames(b))
	}
	// …while the correlated body IS searched (order selection still runs)…
	if !slice3HasSearchedTree(lat.Right) {
		t.Error("no searched subtree in the lateral body; the body search must still run")
	}
	// …but narrows by the Slice-2 arms only: the orders build keeps the
	// leaf-local filter column (3 cols, not the parent-aware 2).
	bodyBuilds := slice3BuildProjects(lat.Right)
	if len(bodyBuilds) != 1 {
		t.Fatalf("body narrow builds = %d, want 1 (the orders build side)", len(bodyBuilds))
	}
	if got := slice3NameSet(bodyBuilds[0]); !slice3SetEqual(got, "o_orderkey", "o_orderdate", "o_orderpriority") {
		t.Errorf("body orders build = %v, want [o_orderkey o_orderdate o_orderpriority] (statement-wide, filter kept)", slice3ProjectNames(bodyBuilds[0]))
	}
}

// slice3IsNarrowBuild reports whether p is a build-side narrowing Project:
// strictly narrower than its child and renaming nothing.
func slice3IsNarrowBuild(p *Project) bool {
	if p == nil || p.Child == nil || len(p.Output()) >= len(p.Child.Output()) {
		return false
	}
	childNames := make(map[string]bool, len(p.Child.Output()))
	for _, c := range p.Child.Output() {
		childNames[c.Name] = true
	}
	for _, c := range p.Output() {
		if !childNames[c.Name] {
			return false
		}
	}
	return true
}

// slice3HasSearchedTreeExcept reports a searched tag anywhere outside skip.
func slice3HasSearchedTreeExcept(n, skip Node) bool {
	found := false
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || found || n == skip {
			return
		}
		if isSearchedTree(n) {
			found = true
			return
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return found
}

// slice3BuildProjectsExcept collects narrow builds outside skip.
func slice3BuildProjectsExcept(n, skip Node) []*Project {
	var out []*Project
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || n == skip {
			return
		}
		if j, ok := n.(*Join); ok {
			for _, side := range []Node{j.Left, j.Right} {
				if p, isProj := side.(*Project); isProj && slice3IsNarrowBuild(p) {
					out = append(out, p)
				}
			}
		}
		for _, c := range boundaryWalkChildren(n) {
			walk(c)
		}
	}
	walk(n)
	return out
}

// TestSlice3DerivedTableAliasMapping is regression test (d1): published
// names never enter the inner tree. The DT publishes px/py while leaves
// carry base names; the inner derivation is self-consistent per level — the
// t1 build keeps base [k x] (dropping the filter column f1 after filtering)
// and no narrow output names px/py.
func TestSlice3DerivedTableAliasMapping(t *testing.T) {
	c := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cc := make([]catalog.Column, len(cols))
		for i, cn := range cols {
			cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: "int4"}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
		if err != nil {
			t.Fatal(err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true})
	}
	mk("t1", 300_000, "k", "x", "f1", "f2")
	mk("t2", 500_000, "k", "y", "g1", "g2")
	plan, err := Plan(parseOne(t, `select px, sum(py) from (select a.x as px, b.y as py from t1 a, t2 b where a.k = b.k and a.f1 > 10) dt group by px`), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	builds := slice3BuildProjects(plan)
	if len(builds) != 1 {
		t.Fatalf("narrow builds = %d, want 1 (the t1 build side)", len(builds))
	}
	if got := slice3ProjectNames(builds[0]); !slice3EqualNames(got, []string{"k", "x"}) {
		t.Errorf("t1 build = %v, want [k x] (base names; f1 drops after filtering)", got)
	}
	for _, b := range builds {
		for _, name := range slice3ProjectNames(b) {
			if name == "px" || name == "py" {
				t.Errorf("narrow build %v names a published alias; outer names never enter the inner tree", slice3ProjectNames(b))
			}
		}
	}
	if f := slice3FiltersMentioning(plan, "f1"); len(f) != 1 {
		t.Errorf("f1 filters = %d, want exactly 1 (leaf-local, below the narrow point)", len(f))
	}
	if out := plan.Output(); len(out) != 2 || out[0].Name != "px" || out[1].Name != "sum" {
		t.Errorf("outer output = %v, want [px sum]", out)
	}
}

// TestSlice3SelfJoinInDerivedTable is regression test (d2): a self-join
// inside a derived table over-keeps symmetric copies (F4). The (nation a ⋈
// nation b) build keeps BOTH n_name copies (and both n_regionkey copies the
// at-names match on both sides) — never exactly one of a pair — while
// below-only columns still drop.
func TestSlice3SelfJoinInDerivedTable(t *testing.T) {
	c := catalog.NewInMemory()
	mk := func(name string, rows int64, cols ...string) {
		t.Helper()
		cc := make([]catalog.Column, len(cols))
		for i, cn := range cols {
			ty := "int4"
			if cn == "n_name" || cn == "n_comment" || cn == "r_name" || cn == "r_comment" {
				ty = "text"
			}
			cc[i] = catalog.Column{Name: cn, Type: catalog.Type{Name: ty}}
		}
		tbl, err := c.CreateTable(parser.ObjectName{Name: name}, cc)
		if err != nil {
			t.Fatal(err)
		}
		c.SetTableStats(tbl, &catalog.TableStats{RowCount: rows, Pages: int(rows / 100), Analyzed: true})
	}
	mk("nation", 200_000, "n_nationkey", "n_name", "n_regionkey", "n_comment")
	mk("region", 1_000_000_000, "r_regionkey", "r_name", "r_comment")
	plan, err := Plan(parseOne(t, `select x, count(*) from (select a.n_name as x, r.r_name as y from nation a, nation b, region r where a.n_nationkey = b.n_regionkey and b.n_regionkey = r.r_regionkey and a.n_regionkey > 1) dt group by x`), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	selfKept := false
	for _, b := range slice3BuildProjects(plan) {
		got := slice3ProjectNames(b)
		if slice3EqualNames(got, []string{"n_name", "n_regionkey", "n_name", "n_regionkey"}) {
			selfKept = true
		}
		// F4 pair rule: a name occurring twice in the narrowed child's
		// schema is kept twice or not at all — never exactly once.
		counts := map[string]int{}
		for _, name := range got {
			counts[name]++
		}
		childCounts := map[string]int{}
		for _, col := range b.Child.Output() {
			childCounts[col.Name]++
		}
		for name, n := range counts {
			if childCounts[name] == 2 && n != 2 {
				t.Errorf("build %v keeps %d of 2 %q copies; self-joins over-keep symmetric pairs", got, n, name)
			}
		}
	}
	if !selfKept {
		t.Error("no [n_name n_regionkey n_name n_regionkey] self-join build; want the symmetric over-keep")
	}
	if out := plan.Output(); len(out) != 2 || out[0].Name != "x" || out[1].Name != "count" {
		t.Errorf("outer output = %v, want [x count]", out)
	}
}

// TestSlice3CorrelatedBodyDeclinesParentAware pins the corrAbove gate at the
// unit level: a current-scope outer reference declines, a plain predicate
// does not, and an outer reference sealed inside a subplan does not (it
// belongs to the body's own scope — scopeIgnore steps over it).
func TestSlice3CorrelatedBodyDeclinesParentAware(t *testing.T) {
	outer := &OuterColumnRef{Name: "p_partkey", Index: 3}
	local := func(name string, idx int) *ColumnRef { return &ColumnRef{Name: name, Index: idx} }
	corr := &BinaryOp{Op: parser.OpEq, Left: local("ps_partkey", 0), Right: outer}
	if !exprHasOuterRef(corr) {
		t.Error("correlated equality: got false, want true")
	}
	plain := &BinaryOp{Op: parser.OpEq, Left: local("a", 0), Right: local("b", 1)}
	if exprHasOuterRef(plain) {
		t.Error("plain equality: got true, want false")
	}
	if exprHasOuterRef(nil) {
		t.Error("nil expr: got true, want false")
	}
	sealed := &SubqueryExpr{Plan: &Filter{Predicate: corr}}
	if exprHasOuterRef(sealed) {
		t.Error("subplan-sealed outer ref: got true, want false (inner scopes are stepped over)")
	}
	if exprHasOuterRefList([]Expr{plain, nil}) {
		t.Error("plain list: got true, want false")
	}
	if !exprHasOuterRefList([]Expr{plain, corr}) {
		t.Error("mixed list: got false, want true")
	}
	// End to end, the correlated Q2 scalar-aggregate body still decorrelates
	// (the acceptance test that caught the first interaction): the decline
	// keeps the group key in the body's searched output.
	if agg := func() *Aggregate {
		saved := pgShapedDP
		pgShapedDP = true
		defer func() { pgShapedDP = saved }()
		stmts, err := parser.Parse(jsgQ2SQL)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		plan, err := Plan(stmts[0], jsgCatalog(t))
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return jsgDecorrelatedAgg(plan)
	}(); agg == nil {
		t.Error("Q2 scalar aggregate no longer decorrelates; corrAbove over-declines or the body lost its group key")
	}
}

// ---- B-01a P4-01 deferred slice (a): merge/NL input policy ----
//
// Merge inputs narrow under the same keep rule as hash build sides, on both
// sides, with the sort-key-preservation proof enforced in code
// (narrowMergeInput + mergeKeepCoversSortKeys). Nested-loop inputs decline
// (the NL policy: parameterised probe internals no qual walk can inventory,
// and a Project above the probe is a plan-time panic). These tests pin the
// two-sided derivation, the poison arms, the side-oriented coverage gate,
// key-tuple order preservation through narrowing, and the NL decline.

// mergeWitnessTree builds a Q12-shaped merge tree — merge(orders ⋈ lineitem)
// on o_orderkey = l_orderkey, with the above-tree set holding only the
// grouping key — the shape the B-01a gate prediction is stated in.
func mergeWitnessTree(t *testing.T) (root *Path, rels map[string]*RelOptInfo, needed, out map[string]bool) {
	t.Helper()
	out = map[string]bool{"l_shipmode": true}
	needed = map[string]bool{
		"l_shipmode": true,
		"o_orderkey": true, "l_orderkey": true,
		"l_commitdate": true, "l_receiptdate": true, "l_shipdate": true,
		"o_orderstatus": true,
	}
	rels = make(map[string]*RelOptInfo)
	var pOrders, pLine *Path
	rels["orders"], pOrders = slice3Base(t, 1,
		[]string{"o_orderkey", "o_custkey", "o_orderstatus", "o_orderdate"}, needed, out)
	rels["line"], pLine = slice3Base(t, 2,
		[]string{"l_orderkey", "l_shipmode", "l_commitdate", "l_receiptdate", "l_shipdate", "l_comment"}, needed, out)
	jrel := newRelOptInfo(1|2, 1000, 32)
	jrel.NeededCols, jrel.NeededColsKnown = needed, true
	jrel.OutputCols, jrel.OutputColsKnown = out, true
	rels["M"] = jrel
	root = &Path{Kind: PathMergeJoin, Rel: jrel, Rows: 1000,
		Children: []*Path{pOrders, pLine},
		HashKeys: []*restrictInfo{slice3Key("o_orderkey", "l_orderkey", 1, 2)}}
	return root, rels, needed, out
}

// TestDeriveJoinKeepsMergeStampsBothSides: a well-formed merge level stamps
// both inputs with out ∪ ancestors ∪ at-parent. The lineitem side drops from
// the statement-wide 5 to the parent-aware 2 (leaf-local filter columns run
// below the narrow point); the orders side drops to the bare sort key. Every
// sort-key column is in its side's keep — the preservation construction.
func TestDeriveJoinKeepsMergeStampsBothSides(t *testing.T) {
	root, rels, _, _ := mergeWitnessTree(t)
	deriveJoinKeeps(root)

	if !rels["orders"].JoinKeepKnown || !rels["line"].JoinKeepKnown {
		t.Fatalf("keeps known = (%v, %v), want (true, true): a merge stamps both sides",
			rels["orders"].JoinKeepKnown, rels["line"].JoinKeepKnown)
	}
	if got := slice3KeepNames(t, rels["line"], rels["line"].baseLeaf.Output()); !slice3EqualNames(got,
		[]string{"l_orderkey", "l_shipmode"}) {
		t.Errorf("line keep = %v, want [l_orderkey l_shipmode]", got)
	}
	if got := slice3KeepNames(t, rels["orders"], rels["orders"].baseLeaf.Output()); !slice3EqualNames(got,
		[]string{"o_orderkey"}) {
		t.Errorf("orders keep = %v, want [o_orderkey]", got)
	}
	for _, drop := range []string{"l_commitdate", "l_receiptdate", "l_shipdate", "l_comment", "o_orderstatus"} {
		if rels["line"].JoinKeep[drop] || rels["orders"].JoinKeep[drop] {
			t.Errorf("below-point column %q kept; want it dropped", drop)
		}
	}
}

// TestMergeNarrowWidthDeltaPrediction states the B-01a per-level gate
// prediction (F6) in columns: the statement-wide widths against the derived
// widths on the Q12-shaped merge, with the sort keys pinned present.
func TestMergeNarrowWidthDeltaPrediction(t *testing.T) {
	_, rels, needed, _ := mergeWitnessTree(t)
	deriveJoinKeeps(mergeRootFor(rels))
	lineSch := rels["line"].baseLeaf.Output()
	ordersSch := rels["orders"].baseLeaf.Output()
	if got := len(neededKeepSet(lineSch, needed)); got != 5 {
		t.Fatalf("statement-wide line width = %d, want 5", got)
	}
	if got := len(neededKeepSet(lineSch, rels["line"].JoinKeep)); got != 2 {
		t.Fatalf("derived line width = %d, want 2 (sort key + grouping key)", got)
	}
	if got := len(neededKeepSet(ordersSch, needed)); got != 2 {
		t.Fatalf("statement-wide orders width = %d, want 2", got)
	}
	if got := len(neededKeepSet(ordersSch, rels["orders"].JoinKeep)); got != 1 {
		t.Fatalf("derived orders width = %d, want 1 (bare sort key)", got)
	}
}

// mergeRootFor rebuilds the witness merge root from derived rels (the
// prediction test derives on a fresh tree to pin width arithmetic
// independently of the stamp test's tree).
func mergeRootFor(rels map[string]*RelOptInfo) *Path {
	oScan := &Path{Kind: PathSeqScan, Rel: rels["orders"]}
	lScan := &Path{Kind: PathSeqScan, Rel: rels["line"]}
	return &Path{Kind: PathMergeJoin, Rel: rels["M"], Rows: 1000,
		Children: []*Path{oScan, lScan},
		HashKeys: []*restrictInfo{slice3Key("o_orderkey", "l_orderkey", 1, 2)}}
}

// TestDeriveJoinKeepsMergePoison: an uncollectable merge qual stamps nothing
// at or below the merge, while a stamp from a hash join above still lands
// (poison propagates down, never up).
func TestDeriveJoinKeepsMergePoison(t *testing.T) {
	out := map[string]bool{"o_v": true}
	needed := map[string]bool{"o_v": true, "o_k": true, "a_k": true, "b_k": true, "a_v": true, "b_v": true}
	rels := make(map[string]*RelOptInfo)
	var pO, pA, pB *Path
	rels["o"], pO = slice3Base(t, 1, []string{"o_k", "o_v"}, needed, out)
	rels["a"], pA = slice3Base(t, 4, []string{"a_k", "a_v"}, needed, out)
	rels["b"], pB = slice3Base(t, 8, []string{"b_k", "b_v"}, needed, out)
	mRel := newRelOptInfo(4|8, 100, 32)
	mRel.NeededCols, mRel.NeededColsKnown = needed, true
	mRel.OutputCols, mRel.OutputColsKnown = out, true
	rels["m"] = mRel
	m := &Path{Kind: PathMergeJoin, Rel: mRel, Rows: 100, Children: []*Path{pA, pB},
		HashKeys: []*restrictInfo{slice3Key("a_k", "b_k", 4, 8)},
		Residual: []*restrictInfo{{clause: &OuterColumnRef{}}}}
	hRel := newRelOptInfo(1|4|8, 100, 32)
	hRel.NeededCols, hRel.NeededColsKnown = needed, true
	hRel.OutputCols, hRel.OutputColsKnown = out, true
	root := &Path{Kind: PathHashJoin, Rel: hRel, Rows: 100, Children: []*Path{pO, m},
		HashKeys: []*restrictInfo{slice3Key("o_k", "a_k", 1, 4|8)}}
	deriveJoinKeeps(root)

	if !mRel.JoinKeepKnown {
		t.Error("merge rel lost its stamp from the hash above; poison must not propagate up")
	}
	for _, name := range []string{"a", "b", "o"} {
		if rels[name].JoinKeepKnown {
			t.Errorf("rel %s stamped under an uncollectable merge qual; want fallback", name)
		}
	}
}

// TestNarrowMergeInputSortKeyPreservation pins the enforcement half of the
// proof: the cut runs exactly when every side-operand sort-key column
// survives in the keep, and declines (pair untouched) otherwise.
func TestNarrowMergeInputSortKeyPreservation(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	mkSide := func(joinKeep map[string]bool) (*noNode, outputLayout, *Path) {
		rel := newRelOptInfo(2, 100, 32)
		rel.baseLeaf = &noNode{sch: noSchema("k2", "v", "x")}
		rel.NeededCols, rel.NeededColsKnown = map[string]bool{"k2": true, "v": true, "x": true}, true
		rel.JoinKeep, rel.JoinKeepKnown = joinKeep, true
		return &noNode{sch: noSchema("k2", "v", "x")}, outputLayout{21, 22, 23},
			&Path{Kind: PathSeqScan, Rel: rel}
	}
	mkJoin := func(keys ...*restrictInfo) *Path {
		return &Path{Kind: PathMergeJoin, HashKeys: keys}
	}
	keys := []*restrictInfo{slice3Key("k", "k2", 1, 2)}

	t.Run("narrows keeping the sort key", func(t *testing.T) {
		node, lay, side := mkSide(map[string]bool{"k2": true, "v": true})
		got, gotLay := narrowMergeInput(node, lay, side, mkJoin(keys...))
		proj, ok := got.(*Project)
		if !ok {
			t.Fatalf("expected a *Project, got %T", got)
		}
		out := proj.Output()
		if len(out) != 2 || out[0].Name != "k2" || out[1].Name != "v" {
			t.Errorf("schema = %v, want [k2 v] (sort key first, in order)", out)
		}
		if len(gotLay) != 2 || gotLay[0] != 21 || gotLay[1] != 22 {
			t.Errorf("layout = %v, want the kept coordinates [21 22]", []int(gotLay))
		}
	})

	t.Run("other side's operand is not asked of this schema", func(t *testing.T) {
		// The join key names {k, k2}; the inner keep {k2, v} cannot contain
		// the outer-only k — the gate is side-oriented, so the cut runs.
		node, lay, side := mkSide(map[string]bool{"k2": true, "v": true})
		if got, _ := narrowMergeInput(node, lay, side, mkJoin(keys...)); got == nil {
			t.Fatal("side-oriented gate declined a keep holding every own-side sort key")
		} else if _, ok := got.(*Project); !ok {
			t.Fatalf("expected a *Project, got %T", got)
		}
	})

	t.Run("declines when the keep drops a sort key", func(t *testing.T) {
		node, lay, side := mkSide(map[string]bool{"v": true})
		got, gotLay := narrowMergeInput(node, lay, side, mkJoin(keys...))
		if _, ok := got.(*Project); ok {
			t.Error("emitted a Project dropping sort key k2; want the pair untouched")
		}
		if len(gotLay) != 3 || gotLay[0] != 21 {
			t.Errorf("layout moved to %v; a decline returns the pair untouched", []int(gotLay))
		}
	})

	t.Run("declines on an unwalkable own-side key", func(t *testing.T) {
		node, lay, side := mkSide(map[string]bool{"k2": true, "v": true})
		bad := &restrictInfo{
			leftKey: &ColumnRef{Name: "k"}, rightKey: &OuterColumnRef{},
			leftRelids: 1, rightRelids: 2, isEquijoin: true,
		}
		if got, _ := narrowMergeInput(node, lay, side, mkJoin(bad)); got == nil {
			t.Fatal("nil node back; a decline returns the pair untouched, never nil")
		} else if _, ok := got.(*Project); ok {
			t.Error("emitted a Project over an uninventoried sort key; want fallback")
		}
	})

	t.Run("declines keyless and flag-off", func(t *testing.T) {
		node, lay, side := mkSide(map[string]bool{"k2": true, "v": true})
		if got, _ := narrowMergeInput(node, lay, side, mkJoin()); got == nil {
			t.Fatal("nil node back; a decline returns the pair untouched")
		} else if _, ok := got.(*Project); ok {
			t.Error("keyless merge narrowed; a join with no keys has no ordering to preserve")
		}
		narrowBuild = false
		defer func() { narrowBuild = true }()
		if got, _ := narrowMergeInput(node, lay, side, mkJoin(keys...)); got == nil {
			t.Fatal("nil node back; a decline returns the pair untouched")
		} else if _, ok := got.(*Project); ok {
			t.Error("narrowed with the flag off; want the pair untouched")
		}
	})
}

// TestNarrowMergeInputKeyOrderPreserved: the key tuple order (the merge's
// sort order) survives narrowing — pairs come out in HashKeys list order,
// translated onto the narrowed merged row.
func TestNarrowMergeInputKeyOrderPreserved(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	boundKey := func(name string, index int, ids RelSet) *ColumnRef {
		return &ColumnRef{Index: index, Name: name}
	}
	mkKey := func(l, r string, li, ri int) *restrictInfo {
		lk, rk := boundKey(l, li, 1), boundKey(r, ri, 2)
		return &restrictInfo{leftKey: lk, rightKey: rk,
			leftRelids: 1, rightRelids: 2, isEquijoin: true,
			clause: &BinaryOp{Op: parser.OpEq, Left: lk, Right: rk}}
	}
	keys := []*restrictInfo{mkKey("a", "c", 10, 20), mkKey("b", "d", 11, 21)}
	join := &Path{Kind: PathMergeJoin, HashKeys: keys}

	oRel := newRelOptInfo(1, 100, 32)
	oRel.baseLeaf = &noNode{sch: noSchema("a", "b", "ax")}
	oRel.NeededCols, oRel.NeededColsKnown = map[string]bool{"a": true, "b": true, "ax": true}, true
	oRel.JoinKeep, oRel.JoinKeepKnown = map[string]bool{"a": true, "b": true}, true
	iRel := newRelOptInfo(2, 100, 32)
	iRel.baseLeaf = &noNode{sch: noSchema("c", "d", "dx")}
	iRel.NeededCols, iRel.NeededColsKnown = map[string]bool{"c": true, "d": true, "dx": true}, true
	iRel.JoinKeep, iRel.JoinKeepKnown = map[string]bool{"c": true, "d": true}, true
	oPath, iPath := &Path{Kind: PathSeqScan, Rel: oRel}, &Path{Kind: PathSeqScan, Rel: iRel}

	outer, outerLay := narrowMergeInput(&noNode{sch: noSchema("a", "b", "ax")}, outputLayout{10, 11, 12}, oPath, join)
	inner, innerLay := narrowMergeInput(&noNode{sch: noSchema("c", "d", "dx")}, outputLayout{20, 21, 22}, iPath, join)
	if _, ok := outer.(*Project); !ok {
		t.Fatalf("outer: expected a *Project, got %T", outer)
	}
	if _, ok := inner.(*Project); !ok {
		t.Fatalf("inner: expected a *Project, got %T", inner)
	}
	merged := append(append(Schema(nil), outer.Output()...), inner.Output()...)
	lay := append(append(outputLayout(nil), outerLay...), innerLay...)
	in := joinInputs{outer: outer, inner: inner, outerRelids: 1, innerRelids: 2,
		merged: merged, lay: lay, index: lay.bindingIndex()}
	pairs := in.keyPairs("PathMergeJoin", keys)
	if len(pairs) != 2 {
		t.Fatalf("pairs = %d, want 2 in HashKeys list order", len(pairs))
	}
	at := func(e Expr) int {
		cr, ok := e.(*ColumnRef)
		if !ok {
			t.Fatalf("pair operand = %T, want *ColumnRef", e)
		}
		return cr.Index
	}
	// Narrowed merged positions: [a b c d] = [0 1 2 3]; the tuple order is
	// the HashKeys order (a,c) then (b,d).
	for i, want := range [][2]int{{0, 2}, {1, 3}} {
		if got := [2]int{at(pairs[i].Left), at(pairs[i].Right)}; got != want {
			t.Errorf("pair %d = %v, want %v: key-tuple order moved under narrowing", i, got, want)
		}
	}
}

// TestNestedLoopInputsDeclinePolicy pins the B-01a NL verdict: no NL input
// narrows, at any level. A Project above an NLI probe would trip the
// `innerBase.(*IndexScan)` assertion (createplannl.go), and the probe keys
// live on the inner path's IndexClauses in outer coordinates — no qual walk
// over the join path can inventory them — so the policy is decline, with the
// plain-inner shape noted as a future resume point, not part of this cut.
func TestNestedLoopInputsDeclinePolicy(t *testing.T) {
	old := narrowBuild
	narrowBuild = true
	defer func() { narrowBuild = old }()

	t.Run("narrowBuildInput refuses NL kinds", func(t *testing.T) {
		rel := ptRel([]string{"a", "b"}, map[string]bool{"a": true}, true)
		p := ptScanPath(rel)
		node := rel.baseLeaf
		lay := outputLayout{1, 2}
		for _, kind := range []string{"PathNestLoop", "PathMergeJoin"} {
			got, gotLay := narrowBuildInput(kind, node, lay, p)
			if _, ok := got.(*Project); ok {
				t.Errorf("kind %s: hash-only entry narrowed; merge goes through narrowMergeInput, NL never", kind)
			}
			if len(gotLay) != 2 {
				t.Errorf("kind %s: layout moved; a refusal returns the pair untouched", kind)
			}
		}
	})

	t.Run("derivation poisons NL subtrees", func(t *testing.T) {
		out := map[string]bool{"o_v": true}
		needed := map[string]bool{"o_v": true, "o_k": true, "a_k": true, "b_k": true}
		rels := make(map[string]*RelOptInfo)
		var pO, pA *Path
		rels["o"], pO = slice3Base(t, 1, []string{"o_k", "o_v"}, needed, out)
		rels["a"], pA = slice3Base(t, 4, []string{"a_k", "a_v"}, needed, out)
		rels["b"], _ = slice3Base(t, 8, []string{"b_k", "b_v"}, needed, out)
		// Parameterised NLI shape: the inner carries an outer-bound probe
		// key no join-path qual walk can see.
		probe := &Path{Kind: PathIndexScan, Rel: rels["b"], RequiredOuter: 1,
			IndexClauses: []indexPathClause{{indexCol: 0, key: &ColumnRef{Name: "o_k", Index: 0}}}}
		nRel := newRelOptInfo(4|8, 100, 32)
		nRel.NeededCols, nRel.NeededColsKnown = needed, true
		nRel.OutputCols, nRel.OutputColsKnown = out, true
		rels["n"] = nRel
		n := &Path{Kind: PathNestLoop, Rel: nRel, Rows: 100, Children: []*Path{pA, probe}}
		hRel := newRelOptInfo(1|4|8, 100, 32)
		hRel.NeededCols, hRel.NeededColsKnown = needed, true
		hRel.OutputCols, hRel.OutputColsKnown = out, true
		root := &Path{Kind: PathHashJoin, Rel: hRel, Rows: 100, Children: []*Path{pO, n},
			HashKeys: []*restrictInfo{slice3Key("o_k", "a_k", 1, 4|8)}}
		deriveJoinKeeps(root)
		for _, name := range []string{"n", "a", "b", "o"} {
			if rels[name].JoinKeepKnown {
				t.Errorf("rel %s stamped under a nested loop; probe internals are uninventoried", name)
			}
		}
	})
}

// TestSlice3LiveDeltaModelArithmetic states the live-shape gate in MODEL
// currency for the measured 10→7 delta (same functions the search calls,
// pinned regime: bench work_mem 64 MB × hash_mem_multiplier 2).
func TestSlice3LiveDeltaModelArithmetic(t *testing.T) {
	const mb = int64(1) << 20
	// Estimate rows flip 2→1; actual rows stay 2→2 (honest: the 10→7 delta
	// buys one batch at estimate, not at actuals — the task's 10→6 flips
	// both, needing the orders link inside the witness subtree).
	if got := hashsize.Choose(242450, 10, 0, 128*mb); got.NBatch != 2 {
		t.Errorf("statement width @estimate: NBatch = %d, want 2", got.NBatch)
	}
	if got := hashsize.Choose(242450, 7, 0, 128*mb); got.NBatch != 1 {
		t.Errorf("derived width @estimate: NBatch = %d, want 1", got.NBatch)
	}
	if got := hashsize.Choose(321056, 10, 0, 128*mb); got.NBatch != 2 {
		t.Errorf("statement width @actual: NBatch = %d, want 2", got.NBatch)
	}
	if got := hashsize.Choose(321056, 7, 0, 128*mb); got.NBatch != 2 {
		t.Errorf("derived width @actual: NBatch = %d, want 2 (no flip at actuals)", got.NBatch)
	}
	// Width ≈116 MB, not 6 columns: EntryBytes(7) = 360.
	if got := hashsize.EntryBytes(7, 0); got != 360 {
		t.Errorf("EntryBytes(7) = %.0f, want 360", got)
	}
	if got := 321056 * hashsize.EntryBytes(7, 0); got < 115e6 || got > 116e6 {
		t.Errorf("narrowed inner bytes = %.1f MB, want ≈115.6 MB", got/1e6)
	}
	// DPPATH currency on the witness join (Slice-2 gate anchors): the
	// derived width costs join.hash below the 754717 bar.
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
	if got := hashJoinCost(cp, inputs(10)); got.Total < 754717 {
		t.Errorf("statement join.hash = %.0f, want at/above the 754717 bar", got.Total)
	}
	if got := hashJoinCost(cp, inputs(7)); got.Total >= 754717 {
		t.Errorf("derived join.hash = %.0f, want below the 754717 bar", got.Total)
	}
	if wide, narrow := hashJoinCost(cp, inputs(10)), hashJoinCost(cp, inputs(7)); narrow.Total >= wide.Total {
		t.Errorf("derived total %.0f not below statement total %.0f", narrow.Total, wide.Total)
	}
}
