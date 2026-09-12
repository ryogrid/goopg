package optimizer

// R95 (plan-parity-fix-take2): lateral-probe nested-loop worker semantics.
//
// Pins the probe predicate, the three-way walk agreement, the path
// classifier's parameterized arm, and the prebuild-descent agreement —
// together, so no arm admits a shape another refuses.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// latProbeNode builds the bare equality probe Q96 carries: a single-column
// index probe. mutate optionally reshapes it into a refusal case.
func latProbeNode(mutate func(*IndexScan)) Node {
	n := &IndexScan{
		Table: &catalog.Table{Name: "d"},
		Index: &catalog.Index{},
		Key:   &OuterColumnRef{Level: 1, Index: 1, Name: "fk"},
	}
	if mutate != nil {
		mutate(n)
	}
	return n
}

// latTestJoin builds the node shape under test: a lateral Join over the
// bare probe (never an NLI node, never a wrapped inner).
func latTestJoin(typ JoinType, lateral bool, right Node) *Join {
	return &Join{
		Algo: JoinAlgoNestedLoop, Type: typ, Lateral: lateral,
		Left: &SeqScan{schema: Schema{{Name: "a"}}}, Right: right,
	}
}

func TestLateralProbeIsPartialProbe(t *testing.T) {
	if !lateralProbeIsPartialProbe(latProbeNode(nil)) {
		t.Fatal("bare equality probe must be admitted")
	}
	multi := latProbeNode(nil).(*IndexScan)
	multi.Key = nil
	multi.Keys = []Expr{&OuterColumnRef{Level: 1, Index: 1}}
	if !lateralProbeIsPartialProbe(multi) {
		t.Fatal("multi-column equality probe must be admitted")
	}
	ios := &IndexOnlyScan{Table: &catalog.Table{Name: "d"}, Index: &catalog.Index{},
		Key: &OuterColumnRef{Level: 1, Index: 1}}
	if !lateralProbeIsPartialProbe(ios) {
		t.Fatal("bare index-only probe must be admitted")
	}
	refusals := map[string]Node{
		"nil":          nil,
		"seq":          &SeqScan{},
		"no-index":     &IndexScan{Key: &OuterColumnRef{Level: 1}},
		"saop":         latProbeNode(func(n *IndexScan) { n.SAOPKeys = []Expr{&OuterColumnRef{Level: 1}} }),
		"range-low":    latProbeNode(func(n *IndexScan) { n.Key = nil; n.LowKey = &OuterColumnRef{Level: 1} }),
		"range-high":   latProbeNode(func(n *IndexScan) { n.Key = nil; n.HighKey = &OuterColumnRef{Level: 1} }),
		"keyless":      latProbeNode(func(n *IndexScan) { n.Key = nil }),
		"ios-range":    &IndexOnlyScan{Index: &catalog.Index{}, LowKey: &OuterColumnRef{Level: 1}},
		"filter-wrap":  &Filter{Child: latProbeNode(nil)},
		"project-wrap": &Project{Child: latProbeNode(nil)},
	}
	for name, n := range refusals {
		if lateralProbeIsPartialProbe(n) {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestLateralProbeJoinIsPartialCapable(t *testing.T) {
	if !lateralProbeJoinIsPartialCapable(latTestJoin(JoinTypeInner, true, latProbeNode(nil))) {
		t.Fatal("lateral probe INNER must be partial-capable")
	}
	refusals := map[string]*Join{
		"nil":        nil,
		"non-lateral": latTestJoin(JoinTypeInner, false, latProbeNode(nil)),
		"cross":      latTestJoin(JoinTypeCross, true, latProbeNode(nil)),
		"semi":       latTestJoin(JoinTypeSemi, true, latProbeNode(nil)),
		"left":       latTestJoin(JoinTypeLeft, true, latProbeNode(nil)),
		"hash":       {Algo: JoinAlgoHash, Type: JoinTypeInner, Lateral: true, Left: &SeqScan{}, Right: latProbeNode(nil)},
		"seq-inner":  latTestJoin(JoinTypeInner, true, &SeqScan{}),
		"wrapped-inner": latTestJoin(JoinTypeInner, true, &Filter{Child: latProbeNode(nil)}),
		"saop-inner": latTestJoin(JoinTypeInner, true, latProbeNode(func(n *IndexScan) {
			n.SAOPKeys = []Expr{&OuterColumnRef{Level: 1}}
		})),
		"nil-left":  {Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true, Right: latProbeNode(nil)},
		"nil-right": {Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true, Left: &SeqScan{}},
	}
	for name, j := range refusals {
		if lateralProbeJoinIsPartialCapable(j) {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestPartialLateralWalkAgreement pins the four walks to the same decision:
// eligibility, label, unstamping, and the sort-crossing guard.
func TestPartialLateralWalkAgreement(t *testing.T) {
	outer := &SeqScan{schema: Schema{{Name: "a"}}}
	n := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true,
		Left: outer, Right: latProbeNode(nil)}

	if got := drivingScan(n); got != Node(outer) {
		t.Fatalf("drivingScan must reach the OUTER scan, got %T", got)
	}
	if drivingScanCrossesSort(n) {
		t.Fatal("no Sort on the spine: crossesSort must be false")
	}
	stamped := stampParallelScan(n)
	sj, ok := stamped.(*Join)
	if !ok {
		t.Fatalf("stamp must copy the Join, got %T", stamped)
	}
	if ss, ok := sj.Left.(*SeqScan); !ok || !ss.Parallel {
		t.Fatal("stamp must label the outer scan Parallel")
	}
	if outer.Parallel {
		t.Fatal("stamp mutated the input tree")
	}
	if uj, ok := unstampParallelScan(stamped).(*Join); !ok {
		t.Fatal("unstamp must return a Join")
	} else if us, ok := uj.Left.(*SeqScan); !ok || us.Parallel {
		t.Error("unstamp must clear the outer Parallel label")
	}

	// Refusals pin all walks at once. (A non-lateral INNER over the
	// probe is NOT a refusal — R94's ordinary rule admits it; the shape
	// is worker-sound either way, which is the point of the agreement.)
	for name, j := range map[string]*Join{
		"semi-lateral":  latTestJoin(JoinTypeSemi, true, latProbeNode(nil)),
		"cross-lateral": latTestJoin(JoinTypeCross, true, latProbeNode(nil)),
		"lateral-seq-inner": latTestJoin(JoinTypeInner, true, &SeqScan{}),
	} {
		if drivingScan(j) != nil {
			t.Errorf("%s: drivingScan must refuse", name)
		}
		if got := stampParallelScan(j); got != Node(j) {
			t.Errorf("%s: stamp must return the tree unchanged", name)
		}
		if drivingScanCrossesSort(j) {
			t.Errorf("%s: crossesSort must be false on refusal", name)
		}
	}
}

// latParamInner builds a filed parameterized probe path (R60's producer
// shape): PathIndexScan with a requirement on the outer and index clauses.
func latParamInner(rel *RelOptInfo, req RelSet) *Path {
	return &Path{
		Kind: PathIndexScan, Rel: rel, Rows: 5, Cost: Cost{Total: 1},
		IndexClauses:  []indexPathClause{{indexCol: 0, key: &ColumnRef{Index: 0}}},
		RequiredOuter: req,
	}
}

func latClassifyFixture() (joinrel, outer, inner *RelOptInfo, a, b RelSet) {
	a, b = relsetOf(0), relsetOf(1)
	outer = nlPartialOuter(a)
	inner = newRelOptInfo(b, 6000000, 32)
	setCheapest(inner)
	joinrel = newRelOptInfo(a|b, 57000, 64)
	return joinrel, outer, inner, a, b
}

func latClassifyPath(joinrel, outer, inner *RelOptInfo, jt parser.JoinType, probe *Path) *Path {
	return &Path{
		Kind: PathNestLoop, Jointype: jt, Rel: joinrel,
		Rows: 57000, Cost: Cost{Total: 500},
		Children:          []*Path{outer.PartialPathlist[0], probe},
		RequiredOuter:     0,
		OuterRelids:       outer.Relids,
		InnerRelids:       inner.Relids,
		ParallelSafe:      true,
		ParallelWorkers:   2,
	}
}

func TestPartialPathDrivingKindLateralProbe(t *testing.T) {
	joinrel, outer, inner, a, _ := latClassifyFixture()
	probe := latParamInner(inner, a)
	if got := partialPathDrivingKind(latClassifyPath(joinrel, outer, inner, parser.JoinInner, probe)); got != PathSeqScan {
		t.Fatalf("satisfiable parameterized probe must drive on the outer scan, got %v", got)
	}

	cases := map[string]func(joinrel, outer, inner *RelOptInfo, probe *Path) *Path{
		"semi-jointype": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			return latClassifyPath(jr, o, i, parser.JoinSemi, pr)
		},
		"unsatisfiable-req": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.RequiredOuter = relsetOf(2)
			return latClassifyPath(jr, o, i, parser.JoinInner, &bad)
		},
		"memoize-inner": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.Kind = PathMemoize
			return latClassifyPath(jr, o, i, parser.JoinInner, &bad)
		},
		"clauseless-probe": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.IndexClauses = nil
			return latClassifyPath(jr, o, i, parser.JoinInner, &bad)
		},
		"seq-inner-param": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.Kind = PathSeqScan
			return latClassifyPath(jr, o, i, parser.JoinInner, &bad)
		},
		"unpartitioned": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			p := latClassifyPath(jr, o, i, parser.JoinInner, pr)
			p.OuterRelids = 0
			return p
		},
		"param-outer": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			p := latClassifyPath(jr, o, i, parser.JoinInner, pr)
			p.Children[0].RequiredOuter = relsetOf(1)
			return p
		},
	}
	for name, build := range cases {
		jr, o, i, _, _ := latClassifyFixture()
		// Fresh probe per case: mutations must not leak across cases.
		pr := latParamInner(i, o.Relids)
		if got := partialPathDrivingKind(build(jr, o, i, pr)); got != PathPrebuilt {
			t.Errorf("%s: must refuse, got %v", name, got)
		}
	}
}

// TestUpperSplitAdmitsLateralProbeChild pins item 4's selection: the
// split producer admits a lateral-probe child (previously refused at the
// no-driving-scan gate) and files the Finalize->Gather->Partial candidate.
// The rendered-child route suffices — nothing is rebuilt from a Path, so
// no prebuilt copying is needed; this test would catch a regression to
// refusal.
func TestUpperSplitAdmitsLateralProbeChild(t *testing.T) {
	restore := setPartialAggPathsModeForTest(partialAggPathsOn)
	defer restore()

	agg := sizedAggFixture(t, 5_900_000, 2, 8, 2)
	outer, ok := agg.Child.(*SeqScan)
	if !ok {
		t.Fatal("fixture child must be a SeqScan outer")
	}
	agg.Child = &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true,
		Left: outer, Right: latProbeNode(nil)}
	_, split := addSplitFor(t, agg, upperSplitSettings())
	if split == nil {
		t.Fatal("split producer must admit the lateral-probe child")
	}
	if split.Kind != PathFinalizeAgg {
		t.Errorf("split candidate has kind %v, want PathFinalizeAgg", split.Kind)
	}
}

// TestShareableHashDescendsApprovedNL pins the prebuild-descent agreement:
// hashes below an approved NL outer (ordinary or lateral) are collected;
// anything else is untouched.
func TestShareableHashDescendsApprovedNL(t *testing.T) {
	hashCapable := func() *Join {
		return &Join{Algo: JoinAlgoHash, Type: JoinTypeInner,
			Left: &SeqScan{}, Right: &SeqScan{}}
	}
	// Planner side: descent through the approved outer.
	approved := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner,
		Left: hashCapable(), Right: &SeqScan{}}
	if !HasShareableHashJoin(approved) {
		t.Fatal("hash below ordinary-NL outer must be prebuild-visible")
	}
	latApproved := &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true,
		Left: hashCapable(), Right: latProbeNode(nil)}
	if !HasShareableHashJoin(latApproved) {
		t.Fatal("hash below lateral-probe outer must be prebuild-visible")
	}
	for name, n := range map[string]Node{
		"refused-nl": &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeRight,
			Left: hashCapable(), Right: &SeqScan{}},
		"lateral-nonprobe": &Join{Algo: JoinAlgoNestedLoop, Type: JoinTypeInner, Lateral: true,
			Left: hashCapable(), Right: &SeqScan{}},
		"plain-seq": &SeqScan{},
	} {
		if HasShareableHashJoin(n) {
			t.Errorf("%s: must report no shareable hash", name)
		}
	}
}
