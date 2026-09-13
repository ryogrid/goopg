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

// ---- R122 Slice B: join-path propagation ----

func njPath(kind PathKind, jt parser.JoinType, outer, inner *Path) *Path {
	return &Path{Kind: kind, Jointype: jt, Children: []*Path{outer, inner}}
}

func njNarrowed(n int, avg float64, w int) *Path {
	return &Path{NCols: n, AvgVarBytes: avg, OutputWidth: w}
}

func TestNarrowJoinWidthsSumsChildren(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	for _, kind := range []PathKind{PathHashJoin, PathMergeJoin, PathNestLoop} {
		p := njPath(kind, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
		narrowJoinWidths(p)
		if p.NCols != 5 || p.AvgVarBytes != 15 || p.OutputWidth != 64 {
			t.Errorf("kind %v: want (5,15,64), got (%d,%v,%d)", kind, p.NCols, p.AvgVarBytes, p.OutputWidth)
		}
	}
}

// SEMI/ANTI publish the LHS only — the same rule joinPublishesInner applies at
// the rel level. JoinRight publishes BOTH, and JoinFull is deliberately in the
// publishes-both arm so a future FULL executor cannot inherit the SEMI branch
// by falling through.
func TestNarrowJoinWidthsSemiAntiTakeOuterOnly(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	for _, jt := range []parser.JoinType{parser.JoinSemi, parser.JoinAnti} {
		p := njPath(PathHashJoin, jt, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
		narrowJoinWidths(p)
		if p.NCols != 3 || p.AvgVarBytes != 10 || p.OutputWidth != 40 {
			t.Errorf("jointype %v must publish the LHS only, got (%d,%v,%d)",
				jt, p.NCols, p.AvgVarBytes, p.OutputWidth)
		}
	}
	for _, jt := range []parser.JoinType{parser.JoinLeft, parser.JoinRight, parser.JoinFull} {
		p := njPath(PathHashJoin, jt, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
		narrowJoinWidths(p)
		if p.NCols != 5 {
			t.Errorf("jointype %v must publish BOTH sides, got NCols=%d", jt, p.NCols)
		}
	}
}

// Rule 1: a sum that is narrow on one side and full on the other is not a
// currency.
func TestNarrowJoinWidthsDeclinesOnUnnarrowedChild(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	for name, p := range map[string]*Path{
		"outer un-narrowed": njPath(PathHashJoin, parser.JoinInner, &Path{}, njNarrowed(2, 5, 24)),
		"inner un-narrowed": njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), &Path{}),
		"both un-narrowed":  njPath(PathHashJoin, parser.JoinInner, &Path{}, &Path{}),
	} {
		narrowJoinWidths(p)
		if p.NCols != 0 || p.AvgVarBytes != 0 || p.OutputWidth != 0 {
			t.Errorf("%s: must decline, got (%d,%v,%d)", name, p.NCols, p.AvgVarBytes, p.OutputWidth)
		}
	}
}

// Rule 4 — the R121 leak one level up. Index-only paths carry the triple
// UNCONDITIONALLY, so NCols>0 is not a proxy for "this round narrowed it".
func TestNarrowJoinWidthsDeclinesOnIndexOnlyChild(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	ios := &Path{IndexOnly: true, NCols: 2, AvgVarBytes: 5, OutputWidth: 24}
	for name, p := range map[string]*Path{
		"index-only outer": njPath(PathHashJoin, parser.JoinInner, ios, njNarrowed(3, 10, 40)),
		"index-only inner": njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), ios),
	} {
		narrowJoinWidths(p)
		if p.NCols != 0 {
			t.Errorf("%s: an index-only child must not be laundered into a join sum, got NCols=%d",
				name, p.NCols)
		}
	}
}

// The Kind test must be an explicit whitelist: parser.JoinInner is the ZERO
// value and PathSetOp has exactly two children, so "has 2 children" or "has a
// Jointype" would both read a set-op as an inner join.
func TestNarrowJoinWidthsRefusesNonJoinKinds(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	for _, kind := range []PathKind{PathSetOp, PathGather, PathSort, PathMemoize, PathSeqScan} {
		p := &Path{Kind: kind, Children: []*Path{njNarrowed(3, 10, 40), njNarrowed(2, 5, 24)}}
		narrowJoinWidths(p)
		if p.NCols != 0 {
			t.Errorf("kind %v is not a join and must not be summed, got NCols=%d", kind, p.NCols)
		}
	}
}

func TestNarrowJoinWidthsIsIdempotentAndNilSafeAndFlagGated(t *testing.T) {
	restore := setNarrowCostInputsForTest(true)
	// Rule 3: never overwrite an existing stamp.
	p := njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
	p.NCols, p.AvgVarBytes, p.OutputWidth = 99, 99, 99
	narrowJoinWidths(p)
	if p.NCols != 99 {
		t.Errorf("must not overwrite an existing stamp, got %d", p.NCols)
	}
	narrowJoinWidths(nil) // must not panic
	restore()

	defer setNarrowCostInputsForTest(false)()
	off := njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
	narrowJoinWidths(off)
	if off.NCols != 0 {
		t.Errorf("flag off must stamp nothing, got %d", off.NCols)
	}
}

// OutputWidth > 0 whenever NCols > 0, on every path this round writes. Both
// producers route through tupleWidth, which floors at 1 — pinned so a future
// change to that floor cannot silently let a relation width be summed into a
// narrowed one.
func TestNarrowedTripleOutputWidthIsAlwaysPositive(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	rel := ncRel([]string{"a", "b"}, ncNeeded("a"), true, map[string]float64{"a": 0, "b": 100})
	got := relNarrowedWidths(rel)
	if !got.ok {
		t.Fatal("expected narrowing")
	}
	if got.ncols > 0 && got.outputWidth <= 0 {
		t.Fatalf("NCols>0 must imply OutputWidth>0, got ncols=%d width=%d", got.ncols, got.outputWidth)
	}
	j := njPath(PathHashJoin, parser.JoinInner, njNarrowed(1, 0, 1), njNarrowed(1, 0, 1))
	narrowJoinWidths(j)
	if j.NCols > 0 && j.OutputWidth <= 0 {
		t.Fatalf("join sum: NCols>0 must imply OutputWidth>0, got %+v", j)
	}
}

// Slice B must never write a RelOptInfo field. The sum is computed from the
// children, and the temptation to "also refresh joinrel.NCols" is one line
// away — but the rel figures are buildAvgVarBytes's over-charge decline for
// the EXECUTOR's hash entry, and rels are singletons shared across candidates.
func TestNarrowJoinWidthsWritesNoRelField(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	rel := newRelOptInfo(3, 1000, 32)
	rel.NCols, rel.AvgVarBytes = 37, 2080
	rel.ColVarBytes = map[string]float64{"a": 1}
	p := njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
	p.Rel = rel
	narrowJoinWidths(p)
	if rel.NCols != 37 || rel.AvgVarBytes != 2080 || len(rel.ColVarBytes) != 1 {
		t.Fatalf("no RelOptInfo field may be written: %+v", rel)
	}
	if p.NCols != 5 {
		t.Fatalf("the path itself should still be stamped, got %d", p.NCols)
	}
}

// TestAddPathStampsJoinWidths pins the WIRING: addPath and addPartialPath must
// call narrowJoinWidths. Deleting either call makes this fail.
//
// Why this rather than a query-level A/B: Slice B shares Slice A's flag, and
// Slice A alone already moves every join cost in a multi-table plan, so an
// ON/OFF comparison over a planned statement passes whether or not Slice B is
// wired — it cannot isolate this slice. (Verified: removing the addPath call
// left such a test green.) The reachability argument is instead: every join
// path is created inside an addPath/addPartialPath call — Pathlist and
// PartialPathlist are written at exactly those two sites and nowhere else —
// so pinning the funnel pins every producer, including future ones.
func TestAddPathStampsJoinWidths(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()

	serial := newRelOptInfo(3, 100, 32)
	p := njPath(PathHashJoin, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
	p.Rel = serial
	addPath(serial, p, "test.slice-b-wiring")
	if p.NCols != 5 || p.AvgVarBytes != 15 || p.OutputWidth != 64 {
		t.Errorf("addPath must stamp a join path's triple, got (%d,%v,%d)",
			p.NCols, p.AvgVarBytes, p.OutputWidth)
	}

	partial := newRelOptInfo(3, 100, 32)
	partial.ConsiderParallel = true
	q := njPath(PathMergeJoin, parser.JoinInner, njNarrowed(3, 10, 40), njNarrowed(2, 5, 24))
	q.Rel, q.ParallelSafe = partial, true
	addPartialPath(partial, q, "test.slice-b-wiring")
	if q.NCols != 5 {
		t.Errorf("addPartialPath must stamp a join path's triple, got NCols=%d", q.NCols)
	}
}

// TestNarrowJoinWidthsNarrowsANonTopJoinInALiveSearch is R122's P4 turned into
// a PERMANENT guard, replacing the temporary dump the round used to measure it.
//
// It runs the real search (buildInitialRels -> base producers -> joinSearch,
// the same protocol searchOneProblem uses) over THREE relations and asserts
// that a level-2 join path — a NON-TOP join, i.e. one whose triple actually
// feeds a parent's cost — publishes a narrowed NCols strictly below the
// joinrel's own full width.
//
// Non-top matters: the top join's triple has no consumer, so asserting there
// would prove nothing.
func TestNarrowJoinWidthsNarrowsANonTopJoinInALiveSearch(t *testing.T) {
	defer setNarrowCostInputsForTest(true)()
	names := []string{"a", "b", "c"}
	prob := cpBigProblem(names)

	// Give every rel per-column stats so ColVarBytes is populated (otherwise
	// every rel declines and the search narrows nothing), and a needed-column
	// set that keeps strictly fewer columns than the leaves emit.
	needed := map[string]bool{}
	for i := range names {
		tbl := prob.relInfos[i].table
		colStats := make([]catalog.ColumnStats, len(tbl.Columns))
		for c := range tbl.Columns {
			colStats[c] = catalog.ColumnStats{AvgWidth: 8}
		}
		tbl.Stats.Columns = colStats
		// Keep only the FIRST column of each relation (leaves emit rfjWidth=2),
		// so narrowing genuinely reduces.
		needed[tbl.Columns[0].Name] = true
	}
	prob.neededCols, prob.neededColsKnown = needed, true

	// The real protocol, in searchOneProblem's order. Inlined rather than via
	// cpSearch because that helper is a hand-rolled replica that predates the
	// R121 sweep and so would narrow nothing — worth knowing: it has drifted
	// from production.
	sc, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, prob.cp, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc.clauses = buildRestrictInfos(prob.conjuncts, 0, prob.cumOffsets)
	sc.neededCols, sc.neededColsKnown = prob.neededCols, prob.neededColsKnown
	sc.stampNeededColsOnRels()
	sc.setBaseRelConsiderParallel(prob.cat)
	sc.addBaseRelPartialPaths()
	sc.addBaseRelIndexPaths(prob.cat)
	sc.narrowBaseRelCostWidths() // R121 Slice A
	if _, err := sc.joinSearch(sc.clauses, newJoinRelBuilder(sc, prob.cat)); err != nil {
		t.Fatal(err)
	}
	s := sc

	// Level 2 = joins of exactly two base rels: non-top for a 3-rel problem.
	if len(s.joinrels) < 3 {
		t.Fatalf("expected at least 3 levels, got %d", len(s.joinrels))
	}
	narrowed := 0
	for _, rel := range s.joinrels[2] {
		if rel == nil {
			continue
		}
		full := relNCols(rel)
		for _, p := range rel.Pathlist {
			if p == nil || p.NCols == 0 {
				continue
			}
			if p.NCols >= full {
				t.Errorf("a narrowed non-top join must publish fewer columns than its "+
					"joinrel's full width: NCols=%d full=%d", p.NCols, full)
			}
			if p.OutputWidth <= 0 {
				t.Errorf("NCols>0 must imply OutputWidth>0, got %+v", p)
			}
			narrowed++
		}
	}
	if narrowed == 0 {
		t.Fatal("no non-top join path was narrowed — Slice B did not reach the " +
			"live search (this is P4, and it is the pin that replaces the dump)")
	}
}
