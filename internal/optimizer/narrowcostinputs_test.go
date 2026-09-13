package optimizer

import (
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R121 Slice A pins. See
// docs/design/not_ralph/plan_parity_fix_take2/r121-narrow-cost-inputs/SCOPE.md.

// ncRel builds a base rel with a leaf schema, a needed-column set and a
// per-column variable-bytes map, i.e. the three inputs relNarrowedWidths reads.
func ncRel(cols []string, needed map[string]bool, known bool, colVar map[string]float64) *RelOptInfo {
	rel := newRelOptInfo(1, 1000, 32)
	rel.baseLeaf = &noNode{sch: noSchema(cols...)}
	rel.NCols = len(cols)
	rel.NeededCols, rel.NeededColsKnown = needed, known
	rel.AvgVarBytes = 999 // the relation-wide over-charge fallback
	rel.ColVarBytes = colVar
	return rel
}

func ncNeeded(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestNarrowCostInputsFlagIsStrictAndDefaultOff(t *testing.T) {
	for _, v := range []string{"", "0", "true", "on", "yes", " 1", "1 ", "01"} {
		if narrowCostInputsFromEnv(v) {
			t.Errorf("value %q must NOT enable cost-input narrowing", v)
		}
	}
	if !narrowCostInputsFromEnv("1") {
		t.Error(`"1" must enable cost-input narrowing`)
	}
}

func TestRelNarrowedWidthsNarrowsToNeededColumns(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	rel := ncRel([]string{"a", "b", "c"}, ncNeeded("a", "c"), true,
		map[string]float64{"a": 10, "b": 100, "c": 20})
	got := relNarrowedWidths(rel)
	if !got.ok {
		t.Fatal("expected narrowing")
	}
	if got.ncols != 2 {
		t.Errorf("ncols = %d, want 2", got.ncols)
	}
	// Only the kept columns' bytes: b's 100 must NOT appear.
	if math.Abs(got.avgVarBytes-30) > 1e-9 {
		t.Errorf("avgVarBytes = %v, want 30 (a=10 + c=20)", got.avgVarBytes)
	}
	if got.outputWidth <= 0 {
		t.Errorf("outputWidth = %d, want > 0", got.outputWidth)
	}
}

// The decline contract. Each of these must leave the path at the
// relation-wide fallback rather than narrow to something smaller than truth.
func TestRelNarrowedWidthsDeclines(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	colVar := map[string]float64{"a": 10, "b": 100}

	cases := []struct {
		name string
		rel  *RelOptInfo
	}{
		{"collector declined (NeededColsKnown=false)",
			ncRel([]string{"a", "b"}, ncNeeded("a"), false, colVar)},
		{"nil needed set",
			ncRel([]string{"a", "b"}, nil, true, colVar)},
		{"empty keep-set (SELECT count(*)) — NCols>0 is unrepresentable",
			ncRel([]string{"a", "b"}, ncNeeded("zzz"), true, colVar)},
		{"no ColVarBytes (un-ANALYZEd / subquery / CTE / VALUES)",
			ncRel([]string{"a", "b"}, ncNeeded("a"), true, nil)},
		{"kept column unattributed in ColVarBytes — must fail HIGH",
			ncRel([]string{"a", "q"}, ncNeeded("a", "q"), true, colVar)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relNarrowedWidths(tc.rel); got.ok {
				t.Fatalf("expected decline, got %+v", got)
			}
		})
	}
}

func TestRelNarrowedWidthsInertWhenFlagOff(t *testing.T) {
	defer setNarrowCostInputsForTest(false)()
	rel := ncRel([]string{"a", "b"}, ncNeeded("a"), true, map[string]float64{"a": 10, "b": 100})
	if got := relNarrowedWidths(rel); got.ok {
		t.Fatalf("flag off must narrow nothing, got %+v", got)
	}
}

// Case coordinates: neededKeepSet matches names case-SENSITIVELY while
// ColVarBytes is lowercase-keyed, so the ToLower conversion is required. A
// mixed-case column must still be attributed, not declined.
func TestRelNarrowedWidthsLowercasesColVarLookup(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	rel := ncRel([]string{"MixedCase", "b"}, ncNeeded("MixedCase"), true,
		map[string]float64{"mixedcase": 42, "b": 100})
	got := relNarrowedWidths(rel)
	if !got.ok {
		t.Fatal("mixed-case column must be attributed via ToLower, not declined")
	}
	if math.Abs(got.avgVarBytes-42) > 1e-9 {
		t.Errorf("avgVarBytes = %v, want 42", got.avgVarBytes)
	}
}

// Rule 2: all three fields move together, never a narrowed NCols with a
// full-width pathWidth (that is the two-currency defect R120 diagnosed).
func TestNarrowedWidthsApplyToSetsAllThreeOrNone(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	n := narrowedWidths{ncols: 3, avgVarBytes: 30, outputWidth: 44, ok: true}
	p := &Path{}
	n.applyTo(p)
	if p.NCols != 3 || p.AvgVarBytes != 30 || p.OutputWidth != 44 {
		t.Fatalf("all three must be set, got %+v", p)
	}

	declined := narrowedWidths{}
	q := &Path{}
	declined.applyTo(q)
	if q.NCols != 0 || q.AvgVarBytes != 0 || q.OutputWidth != 0 {
		t.Fatalf("a decline must touch nothing, got %+v", q)
	}
}

// Rule: a narrowed AvgVarBytes is only visible when NCols > 0, because
// pathAvgVarBytes gates on NCols. This pins the accessor contract the design
// depends on.
func TestPathAvgVarBytesRequiresNCols(t *testing.T) {
	rel := &RelOptInfo{AvgVarBytes: 500}
	// Narrowed bytes without NCols: silently ignored, reverts to the rel.
	p := &Path{Rel: rel, AvgVarBytes: 30}
	if got := pathAvgVarBytes(p); got != 500 {
		t.Errorf("without NCols the rel figure must win, got %v", got)
	}
	// With NCols the narrowed value is read.
	p.NCols = 2
	if got := pathAvgVarBytes(p); got != 30 {
		t.Errorf("with NCols the path figure must win, got %v", got)
	}
}

// A(ii): single-child wrappers copy the child's triple, so narrowing survives
// Gather/GatherMerge/Sort/Memoize. Without this, narrowing dies at the first
// wrapper — and goopg's TPC-H plans are all parallel.
func TestInheritNarrowedWidthsCopiesChildTriple(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	child := &Path{NCols: 4, AvgVarBytes: 88, OutputWidth: 120}
	parent := &Path{}
	inheritNarrowedWidths(parent, child)
	if parent.NCols != 4 || parent.AvgVarBytes != 88 || parent.OutputWidth != 120 {
		t.Fatalf("wrapper must inherit the child triple, got %+v", parent)
	}
}

func TestInheritNarrowedWidthsNoOpOnUnnarrowedChild(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	parent := &Path{}
	inheritNarrowedWidths(parent, &Path{NCols: 0, AvgVarBytes: 77})
	if parent.NCols != 0 || parent.AvgVarBytes != 0 {
		t.Fatalf("an un-narrowed child must leave the wrapper untouched, got %+v", parent)
	}
}

func TestInheritNarrowedWidthsInertWhenFlagOff(t *testing.T) {
	defer setNarrowCostInputsForTest(false)()
	parent := &Path{}
	inheritNarrowedWidths(parent, &Path{NCols: 4, AvgVarBytes: 88, OutputWidth: 120})
	if parent.NCols != 0 {
		t.Fatalf("flag off must inherit nothing, got %+v", parent)
	}
}

// A(iii): all-or-none PER REL among the paths this round writes, with
// index-only exempt. A mixed rel would let addPath compare two candidate costs
// computed in different currencies.
func TestNarrowBaseRelCostWidthsIsAllOrNonePerRel(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	rel := ncRel([]string{"a", "b"}, ncNeeded("a"), true, map[string]float64{"a": 10, "b": 100})
	seq := &Path{Kind: PathSeqScan, Rel: rel}
	partial := &Path{Kind: PathSeqScan, Rel: rel}
	// An index-only path arrives already narrowed by its own covered set.
	idxOnly := &Path{Kind: PathIndexScan, Rel: rel, NCols: 1, AvgVarBytes: 7, OutputWidth: 9}
	rel.Pathlist = []*Path{seq, idxOnly}
	rel.PartialPathlist = []*Path{partial}

	s := &searchCtx{joinrels: [][]*RelOptInfo{nil, {rel}}}
	s.narrowBaseRelCostWidths()

	for name, p := range map[string]*Path{"serial": seq, "partial": partial} {
		if p.NCols != 1 || math.Abs(p.AvgVarBytes-10) > 1e-9 {
			t.Errorf("%s path not narrowed: %+v", name, p)
		}
	}
	// The exemption held: index-only keeps its own tighter figures.
	if idxOnly.AvgVarBytes != 7 || idxOnly.OutputWidth != 9 {
		t.Errorf("index-only path must keep its own triple, got %+v", idxOnly)
	}
}

func TestNarrowBaseRelCostWidthsDeclinesWholeRel(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	// One kept column is unattributed ⇒ the WHOLE rel declines, so no path of
	// it may carry a triple (rule 3).
	rel := ncRel([]string{"a", "q"}, ncNeeded("a", "q"), true, map[string]float64{"a": 10})
	p1, p2 := &Path{Rel: rel}, &Path{Rel: rel}
	rel.Pathlist = []*Path{p1, p2}

	s := &searchCtx{joinrels: [][]*RelOptInfo{nil, {rel}}}
	s.narrowBaseRelCostWidths()

	if p1.NCols != 0 || p2.NCols != 0 {
		t.Fatalf("a declining rel must leave every path un-narrowed: %+v %+v", p1, p2)
	}
}

// TestNarrowCostInputsReachesTheLiveSearch is the END-TO-END pin the SCOPE
// mandated and the unit pins above provably do NOT provide.
//
// Every other test here builds `searchCtx{joinrels: …}` by hand and calls the
// sweep directly, which is exactly the failure mode the SCOPE warned about: a
// pin over a hand-built tree that never runs the search. Nothing in those pins
// would notice if the production call site in relfromjoinlist.go were deleted.
//
// This one plans a real statement through PlanWithSettings and asserts the
// costs differ between the OFF and ON arms. A cost delta is only reachable if
// narrowBaseRelCostWidths actually ran inside the live search and its narrowed
// figures reached a cost function — so deleting the call site fails this test.
func TestNarrowCostInputsReachesTheLiveSearch(t *testing.T) {
	// A wide relation whose statement needs 2 of 12 columns: 48*ncols then
	// differs by 480 bytes per row, which at 2M rows is ~1GB of hash
	// footprint — enough to move the geometry either side of a budget.
	cat := catalog.NewInMemory()
	mk := func(name string) {
		cols := []catalog.Column{
			{Name: name + "_k", Type: catalog.Type{Name: "int4"}},
			{Name: name + "_v", Type: catalog.Type{Name: "int4"}},
		}
		colStats := []catalog.ColumnStats{{AvgWidth: 4}, {AvgWidth: 4}}
		for i := 0; i < 10; i++ {
			cols = append(cols, catalog.Column{
				Name: name + "_pad" + string(rune('a'+i)),
				Type: catalog.Type{Name: "text"},
			})
			colStats = append(colStats, catalog.ColumnStats{AvgWidth: 64})
		}
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: 2_000_000, Analyzed: true, Columns: colStats}
	}
	mk("nci1")
	mk("nci2")

	stmts, err := parser.Parse(
		"select nci1.nci1_v from nci1, nci2 where nci1.nci1_k = nci2.nci2_k")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)

	plan := func(on bool) Node {
		defer setNarrowCostInputsForTest(on)()
		n, err := PlanWithSettings(sel, cat, DefaultPlannerSettings())
		if err != nil {
			t.Fatalf("plan (narrow=%v): %v", on, err)
		}
		return n
	}

	off := searchedInnerJoinCosts(plan(false))
	on := searchedInnerJoinCosts(plan(true))
	if len(off) == 0 || len(on) == 0 {
		t.Fatalf("fixture produced no searched inner join (off=%v on=%v) — "+
			"the pin cannot observe the sweep", off, on)
	}
	if len(off) != len(on) {
		return // shape moved: narrowing plainly reached the search
	}
	for i := range off {
		if math.Abs(off[i]-on[i]) > 1e-9 {
			return // a join cost moved: the sweep ran and reached costing
		}
	}
	t.Fatalf("no searched join cost moved between the OFF and ON arms — the "+
		"production call site in relfromjoinlist.go may be missing.\n"+
		"off=%v\non=%v", off, on)
}

// TestInheritNarrowedWidthsRefusesIndexOnlyChild pins the A(iii) leak the
// review found: index-only paths carry NCols unconditionally (pathindexonly.go
// writes it whether or not this flag is set), so without a guard a wrapper
// over one would be stamped while its sibling wrapper over a declining rel's
// SeqScan was not — two Gather paths on ONE rel in different currencies, which
// is exactly the addPath bias rule 3 exists to prevent, re-entering through
// the wrapper instead of the scan.
func TestInheritNarrowedWidthsRefusesIndexOnlyChild(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	idxOnly := &Path{IndexOnly: true, NCols: 2, AvgVarBytes: 8, OutputWidth: 16}
	gather := &Path{}
	inheritNarrowedWidths(gather, idxOnly)
	if gather.NCols != 0 || gather.AvgVarBytes != 0 || gather.OutputWidth != 0 {
		t.Fatalf("a wrapper must NOT launder an index-only triple: %+v", gather)
	}

	// The ordinary case still inherits.
	narrowed := &Path{NCols: 3, AvgVarBytes: 12, OutputWidth: 24}
	g2 := &Path{}
	inheritNarrowedWidths(g2, narrowed)
	if g2.NCols != 3 {
		t.Fatalf("a non-index-only narrowed child must still be inherited: %+v", g2)
	}
}

// TestGetMemoizePathDeclinesIndexOnlyInner pins the invariant that makes
// R121's UNCONDITIONAL relNCols -> pathNCols swap in getMemoizePath inert with
// the flag OFF.
//
// With GOOPG_NARROW_COST_INPUTS off, the only writer of Path.NCols is
// pathindexonly.go. So the swap can only change a cost if a Memoize can wrap
// an index-only inner — and it cannot, because memoizeCacheKeys declines an
// inner with no IndexClauses and the index-only producer emits the
// full-index-scan shape with none. If a parameterised index-only path is ever
// added, this pin fails rather than the DEFAULT arm silently re-pricing.
func TestGetMemoizePathDeclinesIndexOnlyInner(t *testing.T) {
	inner := &Path{IndexOnly: true, NCols: 2}
	if len(inner.IndexClauses) != 0 {
		t.Fatal("fixture: an index-only path is expected to carry no index clauses")
	}
	if _, ok := memoizeCacheKeys(&searchCtx{}, inner, RelSet(0)); ok {
		t.Fatal("memoizeCacheKeys must decline an inner with no IndexClauses; " +
			"getMemoizePath's pathNCols swap is no longer inert with the flag off")
	}
}
