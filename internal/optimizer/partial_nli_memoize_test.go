package optimizer

// M0142-0005a: fused NestedLoopIndexJoin worker semantics — the shape PG's
// try_partial_nestloop_path builds when its inner is the cheapest
// parameterized path, INCLUDING the get_memoize_path variant
// (joinpath.c:2194-2199, the mpath call pair — TPC-DS Q34/Q73's
// `Gather > Nested Loop > Memoize > Index Scan`).
//
// goopg emits the memoized NLI FUSED: the cache rides on
// NestedLoopIndexJoin.InnerMemo, never as a free-standing *Memoize child
// (createPlan panics on PathMemoize anywhere else). So the partial-capable
// siblings are the NLI arms, not lateral-probe Memoize cases — the inner
// the checks see is always the bare probe node.
//
// Pins the node predicate, the four-walk agreement, the path classifier's
// memoized-probe arm, and the SetOp-branch mirror — together, so no arm
// admits a shape another refuses.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// nliTestJoin builds the fused node shape under test: INNER NLI over a
// SeqScan outer with the given inner probe. memo=true wraps Inner in the
// InnerMemo field — the field aliases the same *IndexScan, exactly as
// createNestLoopPlan's fused arm emits it.
func nliTestJoin(typ JoinType, inner Node, memo bool) *NestedLoopIndexJoin {
	nli := &NestedLoopIndexJoin{
		Type:  typ,
		Outer: &SeqScan{schema: Schema{{Name: "a"}}},
		Inner: inner,
	}
	if memo {
		is, ok := inner.(*IndexScan)
		if !ok {
			panic("nliTestJoin: InnerMemo aliases an *IndexScan only")
		}
		nli.InnerMemo = &Memoize{Child: is}
	}
	return nli
}

func TestNestedLoopIndexJoinIsPartialCapable(t *testing.T) {
	if !NestedLoopIndexJoinIsPartialCapable(nliTestJoin(JoinTypeInner, latProbeNode(nil), false)) {
		t.Fatal("bare-probe INNER NLI must be partial-capable")
	}
	if !NestedLoopIndexJoinIsPartialCapable(nliTestJoin(JoinTypeInner, latProbeNode(nil), true)) {
		t.Fatal("memoized INNER NLI must be partial-capable — InnerMemo is per-worker-local")
	}
	ios := &IndexOnlyScan{Table: &catalog.Table{Name: "d"}, Index: &catalog.Index{},
		Key: &OuterColumnRef{Level: 1, Index: 1}}
	if !NestedLoopIndexJoinIsPartialCapable(nliTestJoin(JoinTypeInner, ios, false)) {
		t.Fatal("index-only probe INNER NLI must be partial-capable")
	}
	refusals := map[string]*NestedLoopIndexJoin{
		"nil":          nil,
		"cross":        nliTestJoin(JoinTypeCross, latProbeNode(nil), false),
		"semi":         nliTestJoin(JoinTypeSemi, latProbeNode(nil), false),
		"anti":         nliTestJoin(JoinTypeAnti, latProbeNode(nil), false),
		"left":         nliTestJoin(JoinTypeLeft, latProbeNode(nil), false),
		"right":        nliTestJoin(JoinTypeRight, latProbeNode(nil), false),
		"full":         nliTestJoin(JoinTypeFull, latProbeNode(nil), false),
		"seq-inner":    nliTestJoin(JoinTypeInner, &SeqScan{}, false),
		"bitmap-inner": nliTestJoin(JoinTypeInner, &BitmapHeapScan{}, false),
		"saop-inner": nliTestJoin(JoinTypeInner, latProbeNode(func(n *IndexScan) {
			n.SAOPKeys = []Expr{&OuterColumnRef{Level: 1}}
		}), false),
		"range-inner": nliTestJoin(JoinTypeInner, latProbeNode(func(n *IndexScan) {
			n.Key = nil
			n.LowKey = &OuterColumnRef{Level: 1}
		}), false),
		"keyless-inner": nliTestJoin(JoinTypeInner, latProbeNode(func(n *IndexScan) {
			n.Key = nil
		}), false),
		"nil-outer": {Type: JoinTypeInner, Inner: latProbeNode(nil)},
		"nil-inner": {Type: JoinTypeInner, Outer: &SeqScan{}},
	}
	for name, nli := range refusals {
		if NestedLoopIndexJoinIsPartialCapable(nli) {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestPartialNLIWalkAgreement pins the four walks to the same decision:
// eligibility, label, unstamping, and the sort-crossing guard — memoized
// and bare alike, since the walks never see the cache (it is a field, not
// a child).
func TestPartialNLIWalkAgreement(t *testing.T) {
	for _, memo := range []bool{false, true} {
		outer := &SeqScan{schema: Schema{{Name: "a"}}}
		n := nliTestJoin(JoinTypeInner, latProbeNode(nil), memo)
		n.Outer = outer

		if got := drivingScan(n); got != Node(outer) {
			t.Fatalf("memo=%v: drivingScan must reach the OUTER scan, got %T", memo, got)
		}
		if drivingScanCrossesSort(n) {
			t.Fatalf("memo=%v: no Sort on the spine: crossesSort must be false", memo)
		}
		stamped := stampParallelScan(n)
		sj, ok := stamped.(*NestedLoopIndexJoin)
		if !ok {
			t.Fatalf("memo=%v: stamp must copy the NLI, got %T", memo, stamped)
		}
		if ss, ok := sj.Outer.(*SeqScan); !ok || !ss.Parallel {
			t.Fatalf("memo=%v: stamp must label the outer scan Parallel", memo)
		}
		if outer.Parallel {
			t.Fatalf("memo=%v: stamp mutated the input tree", memo)
		}
		if uj, ok := unstampParallelScan(stamped).(*NestedLoopIndexJoin); !ok {
			t.Fatalf("memo=%v: unstamp must return an NLI", memo)
		} else if us, ok := uj.Outer.(*SeqScan); !ok || us.Parallel {
			t.Errorf("memo=%v: unstamp must clear the outer Parallel label", memo)
		}
	}

	// Refusals pin all walks at once.
	for name, nli := range map[string]*NestedLoopIndexJoin{
		"semi":         nliTestJoin(JoinTypeSemi, latProbeNode(nil), false),
		"cross":        nliTestJoin(JoinTypeCross, latProbeNode(nil), false),
		"seq-inner":    nliTestJoin(JoinTypeInner, &SeqScan{}, false),
		"bitmap-inner": nliTestJoin(JoinTypeInner, &BitmapHeapScan{}, false),
	} {
		if drivingScan(nli) != nil {
			t.Errorf("%s: drivingScan must refuse", name)
		}
		if got := stampParallelScan(nli); got != Node(nli) {
			t.Errorf("%s: stamp must return the tree unchanged", name)
		}
		if drivingScanCrossesSort(nli) {
			t.Errorf("%s: crossesSort must be false on refusal", name)
		}
	}
}

// nliMemoizeInner wraps a filed parameterized probe path in the
// get_memoize_path shape (PathMemoize over exactly one child).
func nliMemoizeInner(probe *Path) *Path {
	return &Path{
		Kind: PathMemoize, Rel: probe.Rel, Rows: probe.Rows,
		Cost:          probe.Cost,
		Children:      []*Path{probe},
		RequiredOuter: probe.RequiredOuter,
	}
}

func TestPartialPathDrivingKindMemoizedNLI(t *testing.T) {
	joinrel, outer, inner, a, _ := latClassifyFixture()
	probe := latParamInner(inner, a)
	if got := partialPathDrivingKind(latClassifyPath(joinrel, outer, inner, parser.JoinInner, nliMemoizeInner(probe))); got != PathSeqScan {
		t.Fatalf("memoized satisfiable probe must drive on the outer scan, got %v", got)
	}

	cases := map[string]func(joinrel, outer, inner *RelOptInfo, probe *Path) *Path{
		"semi-jointype": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			return latClassifyPath(jr, o, i, parser.JoinSemi, nliMemoizeInner(pr))
		},
		"memoize-empty": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *nliMemoizeInner(pr)
			bad.Children = nil
			return latClassifyPath(jr, o, i, parser.JoinInner, &bad)
		},
		"memoize-seq-child": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.Kind = PathSeqScan
			return latClassifyPath(jr, o, i, parser.JoinInner, nliMemoizeInner(&bad))
		},
		"memoize-clauseless": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.IndexClauses = nil
			return latClassifyPath(jr, o, i, parser.JoinInner, nliMemoizeInner(&bad))
		},
		"unsatisfiable-req": func(jr, o, i *RelOptInfo, pr *Path) *Path {
			bad := *pr
			bad.RequiredOuter = relsetOf(2)
			return latClassifyPath(jr, o, i, parser.JoinInner, nliMemoizeInner(&bad))
		},
	}
	for name, build := range cases {
		jr, o, i, _, _ := latClassifyFixture()
		pr := latParamInner(i, o.Relids)
		if got := partialPathDrivingKind(build(jr, o, i, pr)); got != PathPrebuilt {
			t.Errorf("%s: must refuse, got %v", name, got)
		}
	}
}

// TestSetOpBranchMemoizedNLI keeps the SetOp-branch arm guard-for-guard
// with the general classifier it documents itself as mirroring.
func TestSetOpBranchMemoizedNLI(t *testing.T) {
	joinrel, outer, inner, a, _ := latClassifyFixture()
	probe := latParamInner(inner, a)
	nl := latClassifyPath(joinrel, outer, inner, parser.JoinInner, nliMemoizeInner(probe))
	if !setOpBranchDrivingKindIsSupported(nl) {
		t.Fatal("branch arm must admit the memoized probe its general twin admits")
	}

	jr, o, i, _, _ := latClassifyFixture()
	bad := *nliMemoizeInner(latParamInner(i, o.Relids))
	bad.Children = nil
	if setOpBranchDrivingKindIsSupported(latClassifyPath(jr, o, i, parser.JoinInner, &bad)) {
		t.Error("branch arm must refuse an empty PathMemoize")
	}
}
