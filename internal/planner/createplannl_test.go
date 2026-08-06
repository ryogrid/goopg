package planner

// M0127-P5.5-e-ii-b — the nested-loop `createPlan` arms (createplannl.go).
//
// The arm is LIVE in production since M0127-P5.9 (2026-08-06)
// (`GOOPG_PGSHAPED_DP` defaults ON), so these tests are no longer its only
// observer. What they pin is the fact the other two join arms do not
// have to know: an NLI writes expressions into TWO coordinate spaces, and every
// fixture below puts the outer side SECOND in binding order so that a
// translation which silently did nothing would still build a runnable node —
// probing the wrong column.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// cpnEq is a two-operand clause expression in binding coordinates.
func cpnEq(l, r Expr) Expr { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }

// cpnNestLoopPath assembles a plain PathNestLoop over the two child paths.
func cpnNestLoopPath(outer, inner *Path, residual []*restrictInfo) *Path {
	joinrel := newRelOptInfo(outer.Rel.Relids|inner.Rel.Relids, 500, 16)
	return &Path{
		Kind:     PathNestLoop,
		Rel:      joinrel,
		Rows:     500,
		Children: []*Path{outer, inner},
		Residual: residual,
	}
}

// cpnParamIndexPath is the inner of an NLI: a parameterised `PathIndexScan` over
// `rel`, binding `idx`'s columns to the given probe expressions (in BINDING
// coordinates, the way `indexPathClauses` records them).
func cpnParamIndexPath(rel *RelOptInfo, idx *catalog.Index, param RelSet, probeBindingCols ...int) *Path {
	cls := make([]indexPathClause, 0, len(probeBindingCols))
	for i, bc := range probeBindingCols {
		cls = append(cls, indexPathClause{ri: plainClause(param), indexCol: i, key: col(bc)})
	}
	return &Path{
		Kind:          PathIndexScan,
		Rel:           rel,
		Rows:          1,
		IndexInfo:     idx,
		IndexScanDir:  ForwardScanDirection,
		IndexClauses:  cls,
		RequiredOuter: param,
	}
}

// TestCreateNestLoopPlanPlainShape: an unparameterised inner yields an ordinary
// `*Join` with `JoinAlgoNestedLoop`, every clause folded into one predicate over
// the merged row, and no key of any kind.
func TestCreateNestLoopPlanPlainShape(t *testing.T) {
	a, b := cpjTwoRel()
	// `a.a0 = b.b1`, written in binding coordinates as col(0) = col(3). The
	// search chose b (binding 2-4) as the OUTER, so the merged row is
	// `b0 b1 b2 a0 a1` and the clause must come back as col(3) = col(1)... in
	// whichever operand order it was written: a nested loop does not orient.
	clause := equiClauseOn(a.Relids, b.Relids, 0, 3)
	clause.clause = cpnEq(col(0), col(3))
	p := cpnNestLoopPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{clause})

	n, lay := createPlanNode(p)
	j, ok := n.(*Join)
	if !ok {
		t.Fatalf("createPlan(plain PathNestLoop) = %T, want *Join", n)
	}
	if j.Algo != JoinAlgoNestedLoop {
		t.Fatalf("Algo = %d, want JoinAlgoNestedLoop", j.Algo)
	}
	if j.LeftKey != nil || j.RightKey != nil || len(j.HashKeys) != 0 {
		t.Fatal("a nested loop keys on nothing; no key field may be set")
	}
	if got := []int(lay); len(got) != 5 || got[0] != 2 || got[3] != 0 {
		t.Fatalf("layout = %v, want the outer b range then the inner a range", got)
	}
	// The clause was re-based onto the merged row: binding 0 (a0) is at merged
	// position 3, binding 3 (b1) at merged position 1.
	eq, ok := j.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("Predicate = %T, want *BinaryOp", j.Predicate)
	}
	if l := eq.Left.(*ColumnRef).Index; l != 3 {
		t.Errorf("Predicate left = col(%d), want col(3)", l)
	}
	if r := eq.Right.(*ColumnRef).Index; r != 1 {
		t.Errorf("Predicate right = col(%d), want col(1)", r)
	}
	if len(j.Output()) != 5 || j.Output()[0].Name != "b0" || j.Output()[3].Name != "a0" {
		t.Fatalf("schema = %v, want outer ++ inner", j.Output())
	}
}

// TestCreateNestLoopPlanCartesianPredicateIsNil: the one join a plain nested
// loop is the only available path for. A synthesised `TRUE` here would be a
// predicate the executor evaluates per pair for no reason.
func TestCreateNestLoopPlanCartesianPredicateIsNil(t *testing.T) {
	a, b := cpjTwoRel()
	n, _ := createPlanNode(cpnNestLoopPath(cpjLeafPath(a), cpjLeafPath(b), nil))
	if j := n.(*Join); j.Predicate != nil {
		t.Fatalf("Predicate = %v, want nil for a cartesian nested loop", j.Predicate)
	}
}

// TestCreateNestLoopPlanNLIProbeKeyIsInOUTERCoordinates is the central test of
// this slice, and the one an arm that used a single coordinate space would fail.
//
// The outer is relid 1 (binding columns 2-4, three columns) and the inner is
// relid 0 (binding columns 0-1). The probe binds the index to `b.b1` — binding
// column 3. `indexScanOp.Rescan` evaluates the key against the OUTER SLOT ALONE,
// which is `b0 b1 b2`, so the emitted key must be col(1). Under the merged
// layout `b0 b1 b2 a0 a1` the answer would be col(1) too — which is why the
// fixture also asserts the SECOND probe column, bound to `b.b2` (binding 4):
// outer position 2, merged position 2. Those still agree...
//
// so the real discriminator is the residual, checked in the next test, plus the
// three-column outer whose LAST column (binding 4 → outer position 2) is the
// only one whose two spaces can be made to disagree by widening the inner. The
// direct statement of the contract is here: the key expressions must index
// within the OUTER width, never beyond it.
func TestCreateNestLoopPlanNLIProbeKeyIsInOUTERCoordinates(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0", "a1")
	inner := cpnParamIndexPath(a, idx, b.Relids, 3, 4) // probe on b.b1, b.b2
	p := cpnNestLoopPath(cpjLeafPath(b), inner, nil)

	n, lay := createPlanNode(p)
	nli, ok := n.(*NestedLoopIndexJoin)
	if !ok {
		t.Fatalf("createPlan(parameterised PathNestLoop) = %T, want *NestedLoopIndexJoin", n)
	}
	if len(nli.Inner.Keys) != 2 || nli.Inner.Key != nil {
		t.Fatalf("a two-column probe must use Keys, not Key: Key=%v Keys=%v", nli.Inner.Key, nli.Inner.Keys)
	}
	outerWidth := len(nli.Outer.Output())
	for i, k := range nli.Inner.Keys {
		cr, isCol := k.(*ColumnRef)
		if !isCol {
			t.Fatalf("probe key %d = %T, want *ColumnRef", i, k)
		}
		if cr.Index >= outerWidth {
			t.Fatalf("probe key %d = col(%d), which is outside the %d-column outer slot it is evaluated against",
				i, cr.Index, outerWidth)
		}
	}
	if got := nli.Inner.Keys[0].(*ColumnRef).Index; got != 1 {
		t.Errorf("probe key 0 = col(%d), want col(1) (binding 3 = b1, outer position 1)", got)
	}
	if got := nli.Inner.Keys[1].(*ColumnRef).Index; got != 2 {
		t.Errorf("probe key 1 = col(%d), want col(2) (binding 4 = b2, outer position 2)", got)
	}
	// The path's own clause expressions belong to the search and must survive.
	if inner.IndexClauses[0].key.(*ColumnRef).Index != 3 {
		t.Fatal("the arm mutated the path's own probe expression instead of cloning it")
	}
	if got := []int(lay); len(got) != 5 || got[0] != 2 || got[3] != 0 {
		t.Fatalf("layout = %v, want the outer b range then the inner a range", got)
	}
	if len(nli.Output()) != 5 || nli.Output()[3].Name != "a0" {
		t.Fatalf("schema = %v, want outer ++ inner", nli.Output())
	}
}

// TestCreateNestLoopPlanNLIResidualIsInMERGEDCoordinates is the other half of
// the two-space contract, and the half that FALSIFIES a single-space arm: the
// residual is evaluated through the operator's `virtualOut`, which spans
// `outer ++ inner`, so a reference to an INNER column must land at a position
// the outer slot does not even have.
func TestCreateNestLoopPlanNLIResidualIsInMERGEDCoordinates(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0")
	inner := cpnParamIndexPath(a, idx, b.Relids, 3)
	// `a.a1 > b.b0` — binding col(1) and col(2).
	residual := plainClause(a.Relids | b.Relids)
	residual.clause = cpnEq(col(1), col(2))
	p := cpnNestLoopPath(cpjLeafPath(b), inner, []*restrictInfo{residual})

	n, _ := createPlanNode(p)
	nli := n.(*NestedLoopIndexJoin)
	eq, ok := nli.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("Predicate = %T, want *BinaryOp", nli.Predicate)
	}
	// binding 1 = a1, an INNER column: merged position 4. An arm that translated
	// the residual onto the outer alone would have refused it (a1 is not in the
	// outer) — and one that translated nothing would emit col(1), which IS a
	// valid position (b1) and would silently compare the wrong columns.
	if got := eq.Left.(*ColumnRef).Index; got != 4 {
		t.Errorf("residual left = col(%d), want col(4) (binding 1 = a1, merged position 4)", got)
	}
	if got := eq.Right.(*ColumnRef).Index; got != 0 {
		t.Errorf("residual right = col(%d), want col(0) (binding 2 = b0, merged position 0)", got)
	}
	// A single-column probe uses `Key`, the shape every pre-existing caller emits.
	if nli.Inner.Key == nil || len(nli.Inner.Keys) != 0 {
		t.Fatalf("a one-column probe must use Key, not Keys: Key=%v Keys=%v", nli.Inner.Key, nli.Inner.Keys)
	}
	if got := nli.Inner.Key.(*ColumnRef).Index; got != 1 {
		t.Errorf("probe key = col(%d), want col(1) (binding 3 = b1, outer position 1)", got)
	}
}

// TestScanLeafIsBareGatesTheNLIInner pins the producer/consumer agreement
// (rule #2): the leaf shapes `addParameterizedIndexPaths` will cost a path over
// are exactly the ones whose emitted node can BE a `NestedLoopIndexJoin.Inner`.
func TestScanLeafIsBareGatesTheNLIInner(t *testing.T) {
	bare := &SeqScan{Table: &catalog.Table{Name: "t"}, schema: cpjSchema("t", 2)}
	if !scanLeafIsBare(bare) {
		t.Fatal("a bare scan is a rebuildable NLI inner")
	}
	wrapped := &Filter{Child: bare, Predicate: col(0), LeafLocal: true}
	if scanLeafIsBare(wrapped) {
		t.Fatal("a leaf behind a *Filter has quals NestedLoopIndexJoin.Inner cannot carry")
	}
	if scanLeafIsBare(&Limit{}) {
		t.Fatal("a non-scan leaf is not bare, it is not a leaf at all")
	}
	// And the consumer refuses the shape the producer now declines, rather than
	// dropping the quals.
	a, b := cpjTwoRel()
	a.baseLeaf = &Filter{Child: a.baseLeaf, Predicate: col(0), LeafLocal: true}
	inner := cpnParamIndexPath(a, cpiIndex("a0"), b.Relids, 3)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("no panic; a wrapped inner leaf must be refused")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "wrappers have nowhere to go") {
			t.Fatalf("panic = %v, want the wrapped-leaf refusal", r)
		}
	}()
	createPlanNode(cpnNestLoopPath(cpjLeafPath(b), inner, nil))
}

func TestCreateNestLoopPlanPanics(t *testing.T) {
	a, b := cpjTwoRel()
	idx := cpiIndex("a0")

	cases := []struct {
		name string
		path func() *Path
		want string
	}{
		{
			name: "one child",
			path: func() *Path {
				p := cpnNestLoopPath(cpjLeafPath(b), cpjLeafPath(a), nil)
				p.Children = p.Children[:1]
				return p
			},
			want: "want exactly 2",
		},
		{
			name: "carries hash keys",
			path: func() *Path {
				p := cpnNestLoopPath(cpjLeafPath(b), cpjLeafPath(a), nil)
				p.HashKeys = []*restrictInfo{equiClauseOn(a.Relids, b.Relids, 0, 3)}
				return p
			},
			want: "keys on nothing and would ignore them",
		},
		{
			name: "parameterised result",
			path: func() *Path {
				p := cpnNestLoopPath(cpjLeafPath(b), cpjLeafPath(a), nil)
				p.RequiredOuter = relsetOf(3)
				return p
			},
			want: "nothing above the search root binds a parameter",
		},
		{
			name: "parameterised inner is not an index scan",
			path: func() *Path {
				bad := cpjLeafPath(a)
				bad.RequiredOuter = b.Relids
				return cpnNestLoopPath(cpjLeafPath(b), bad, nil)
			},
			want: "only a parameterised index scan can have its parameter bound here",
		},
		{
			name: "outer does not supply the parameterisation",
			path: func() *Path {
				inner := cpnParamIndexPath(a, idx, b.Relids|relsetOf(5), 3)
				return cpnNestLoopPath(cpjLeafPath(b), inner, nil)
			},
			want: "which the outer relset",
		},
		{
			name: "probe key order lost",
			path: func() *Path {
				inner := cpnParamIndexPath(a, cpiIndex("a0", "a1"), b.Relids, 3, 4)
				inner.IndexClauses[1].indexCol = 0
				return cpnNestLoopPath(cpjLeafPath(b), inner, nil)
			},
			want: "the index-column order was lost",
		},
		{
			name: "probe key references a non-outer column",
			path: func() *Path {
				// Binding 0 is an INNER column (a0). A probe key that names it
				// would be evaluated against a slot that does not hold it.
				inner := cpnParamIndexPath(a, idx, b.Relids, 0)
				return cpnNestLoopPath(cpjLeafPath(b), inner, nil)
			},
			want: "index probe key references binding column 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("no panic; want one mentioning %q", tc.want)
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.want) {
					t.Fatalf("panic = %v, want it to mention %q", r, tc.want)
				}
			}()
			createPlanNode(tc.path())
		})
	}
}
