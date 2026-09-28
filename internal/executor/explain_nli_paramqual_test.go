package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestInnerParamQualPlacement pins M0146-0005aj's rule: a parameterized
// nested loop's residual renders under its inner scan (PG's ppi_clauses)
// only when every conjunct reads the inner relation and no Memoize sits in
// between; its outer-side columns render as correlated references.
func TestInnerParamQualPlacement(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "o"), optimizer.Schema{{Name: "a"}})
	// The probe is parameterized by the outer relation (source 1) through its
	// index key and reads its own table (source 2) in its Cond.
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "i"),
		Key:  &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1},
		Cond: &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 0}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "p", SourceTableIdx: 2}})
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	innerCol := &optimizer.ColumnRef{Index: 1, Name: "a", SourceTableIdx: 2}
	joinQual := &optimizer.BinaryOp{Op: parser.OpNe, Left: innerCol, Right: outerCol}
	outerOnly := &optimizer.BinaryOp{Op: parser.OpGt, Left: outerCol, Right: &optimizer.IntegerConst{Value: 1}}

	nli := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeAnti, Outer: outer, Inner: inner, Predicate: joinQual}
	q, _, _ := renderedParamQual(nli)
	if q == nil {
		t.Fatal("inner-reading residual not moved to the inner scan")
	}
	b, ok := q.(*optimizer.BinaryOp)
	if !ok {
		t.Fatalf("display qual = %T", q)
	}
	if _, ok := b.Left.(*optimizer.ColumnRef); !ok {
		t.Errorf("inner column must stay a scan column, got %T", b.Left)
	}
	if _, ok := b.Right.(*optimizer.OuterColumnRef); !ok {
		t.Errorf("outer column must render as a correlated reference, got %T", b.Right)
	}
	if nli.Predicate != joinQual {
		t.Error("rendering must not mutate the plan")
	}

	mixed := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner,
		Predicate: &optimizer.BinaryOp{Op: parser.OpAnd, Left: joinQual, Right: outerOnly}}
	// PG places clauses one by one: the inner-reading conjunct moves to the
	// probe and the outer-only one stays on the join (TPC-DS Q24's split).
	mq, mj, applies := renderedParamQual(mixed)
	if !applies || mq == nil || mj != outerOnly {
		t.Errorf("mixed residual must split: probe=%v join=%v applies=%v", mq, mj, applies)
	}

	memo := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner,
		Predicate: joinQual, InnerMemo: &optimizer.Memoize{Child: inner}}
	if movedParamQual(memo) {
		t.Error("a Memoize between join and probe must keep the residual on the join")
	}

	// A clause reaching an outer relation the probe is not parameterized by
	// (source 3) is not movable into it (join_clause_is_movable_into).
	otherRel := &optimizer.BinaryOp{Op: parser.OpNe, Left: innerCol, Right: &optimizer.ColumnRef{Index: 0, Name: "z", SourceTableIdx: 3}}
	if movedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: otherRel}) {
		t.Error("a clause naming a relation outside the probe's required_outer must stay a join filter")
	}

	// An inner = outer equality within required_outer is the class's
	// ppi_clause and moves (TPC-DS Q50); one naming a relation outside it
	// stays on the join (TPC-H Q9's supplier.s_suppkey = lineitem.l_suppkey).
	ecEq := &optimizer.BinaryOp{Op: parser.OpEq, Left: innerCol, Right: outerCol}
	if q, _, _ := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: ecEq}); q == nil {
		t.Error("an inner = outer equality within required_outer renders under the probe, as TPC-DS Q50's")
	}
	ecOther := &optimizer.BinaryOp{Op: parser.OpEq, Left: innerCol, Right: &optimizer.ColumnRef{Index: 0, Name: "z", SourceTableIdx: 3}}
	if movedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: ecOther}) {
		t.Error("an equality naming a relation outside required_outer stays a join filter, as TPC-H Q9's")
	}

	lateral := &optimizer.Join{Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop, Lateral: true,
		Left: outer, Right: inner, Predicate: joinQual}
	if q, _, _ := renderedParamQual(lateral); q == nil || !paramInnerChild(lateral, inner) {
		t.Error("a lateral nested-loop join over a probe must move its residual too")
	}
	plain := &optimizer.Join{Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop,
		Left: outer, Right: inner, Predicate: joinQual}
	if movedParamQual(plain) {
		t.Error("a non-parameterized nested loop keeps its Join Filter")
	}
}

// TestInnerParamQualSeesInListOperand pins the TPC-DS Q48 case: a residual
// whose only inner reads sit in `col = ANY (list)` operands still reads the
// inner relation, so it renders under the probe as PG's ppi_clauses do. The
// shallow expression walker treated the IN node as a leaf and kept the
// clause on the join.
func TestInnerParamQualSeesInListOperand(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "o"), optimizer.Schema{{Name: "a"}})
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "i"),
		Key:  &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1},
		Cond: &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "s", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 0}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "p", SourceTableIdx: 2}})
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	innerCol := &optimizer.ColumnRef{Index: 1, Name: "s", SourceTableIdx: 2}
	arm := func(lo int64) optimizer.Expr {
		return &optimizer.BinaryOp{Op: parser.OpAnd,
			Left:  &optimizer.InExpr{Operand: innerCol, List: []optimizer.Expr{&optimizer.IntegerConst{Value: lo}, &optimizer.IntegerConst{Value: lo + 1}}},
			Right: &optimizer.BinaryOp{Op: parser.OpGe, Left: outerCol, Right: &optimizer.IntegerConst{Value: lo}}}
	}
	orQual := &optimizer.BinaryOp{Op: parser.OpOr, Left: arm(0), Right: arm(10)}
	nli := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: orQual}
	q, _, _ := renderedParamQual(nli)
	if q == nil {
		t.Fatal("a residual reading the inner only inside IN operands must render under the probe")
	}
	var in *optimizer.InExpr
	optimizer.WalkExprTree(q, func(x optimizer.Expr) {
		if e, ok := x.(*optimizer.InExpr); ok && in == nil {
			in = e
		}
	})
	if in == nil {
		t.Fatal("rendered qual lost its IN arm")
	}
	if _, ok := in.Operand.(*optimizer.ColumnRef); !ok {
		t.Errorf("the IN operand is an inner column and must stay a scan column, got %T", in.Operand)
	}
}

// TestInnerParamQualDropsRestatedProbeKey pins the TPC-DS Q84 case: a
// residual equality that restates the probe's own index key binding is
// enforced by the Index Cond, so it prints neither as a Join Filter nor as
// the probe's Filter (PG removes the clause the index consumes).
func TestInnerParamQualDropsRestatedProbeKey(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "o"), optimizer.Schema{{Name: "c_cdemo", SourceTableIdx: 1}})
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "cd"),
		Index: &catalog.Index{Name: "cd_pkey", Columns: []string{"cd_demo_sk"}},
		Key:   &optimizer.ColumnRef{Index: 0, Name: "c_cdemo", SourceTableIdx: 1},
		Cond:  &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "cd_demo_sk", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 0}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "p", SourceTableIdx: 2}})
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "c_cdemo", SourceTableIdx: 1}
	keyCol := &optimizer.ColumnRef{Index: 1, Name: "cd_demo_sk", SourceTableIdx: 2}
	restated := &optimizer.BinaryOp{Op: parser.OpEq, Left: outerCol, Right: keyCol}

	q, j, moved := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: restated})
	if !moved || q != nil || j != nil {
		t.Fatalf("a restated key equality must print nowhere, got moved=%v qual=%v", moved, q)
	}

	// With another inner-reading conjunct, only that conjunct renders.
	other := &optimizer.BinaryOp{Op: parser.OpNe, Left: &optimizer.ColumnRef{Index: 2, Name: "cd_x", SourceTableIdx: 2}, Right: outerCol}
	q, j, moved = renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner,
		Predicate: &optimizer.BinaryOp{Op: parser.OpAnd, Left: restated, Right: other}})
	if b, ok := q.(*optimizer.BinaryOp); !moved || !ok || b.Op != parser.OpNe || j != nil {
		t.Fatalf("only the non-key conjunct renders under the probe, got moved=%v qual=%#v", moved, q)
	}
}

// movedParamQual reports whether any of n's residual renders differently
// from the unsplit join line.
func movedParamQual(n optimizer.Node) bool {
	_, _, applies := renderedParamQual(n)
	return applies
}

// TestSplitParamQualRejections pins how ANALYZE divides a parameterized
// nested loop's residual rejections: a split residual reports the operator's
// probe share on the inner scan and the rest on the join; an unsplit one
// reports everything on whichever line prints it.
func TestSplitParamQualRejections(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "o"), optimizer.Schema{{Name: "a"}})
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "i"),
		Key:  &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1},
		Cond: &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 0}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "p", SourceTableIdx: 2}})
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	innerCol := &optimizer.ColumnRef{Index: 1, Name: "a", SourceTableIdx: 2}
	movable := &optimizer.BinaryOp{Op: parser.OpNe, Left: innerCol, Right: outerCol}
	staying := &optimizer.BinaryOp{Op: parser.OpNe, Left: innerCol, Right: &optimizer.ColumnRef{Index: 0, Name: "z", SourceTableIdx: 3}}
	nli := func(pred optimizer.Expr) optimizer.Node {
		return &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: pred}
	}
	st := &nodeStats{joinFilterRejected: 10, probeFilterRejected: 4}
	for _, tc := range []struct {
		name        string
		n           optimizer.Node
		probe, join int64
	}{
		{"split", nli(&optimizer.BinaryOp{Op: parser.OpAnd, Left: movable, Right: staying}), 4, 6},
		{"all moved", nli(movable), 10, 0},
		{"none moved", nli(staying), 0, 10},
	} {
		p, j := splitParamQualRejections(tc.n, st)
		if p != tc.probe || j != tc.join {
			t.Errorf("%s: got probe=%d join=%d, want %d/%d", tc.name, p, j, tc.probe, tc.join)
		}
	}
}

// TestInnerParamQualSourceIdCollision pins the fail-closed rule for source
// ids: they are FROM bindings within one planning scope, so in
// `(a JOIN b) JOIN c` the nested join's b can carry the probe's own id. An
// outer column whose id is also the probe's is not provably within
// required_outer, so its conjunct stays on the join while its sibling moves.
func TestInnerParamQualSourceIdCollision(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "o"), optimizer.Schema{{Name: "k"}, {Name: "v"}, {Name: "zv"}})
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "i"),
		Key:  &optimizer.ColumnRef{Index: 0, Name: "k", SourceTableIdx: 1},
		Cond: &optimizer.BinaryOp{Op: parser.OpGt, Left: &optimizer.ColumnRef{Index: 0, Name: "w", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 0}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "p", SourceTableIdx: 2}})
	w := &optimizer.ColumnRef{Index: 4, Name: "w", SourceTableIdx: 2}
	movable := &optimizer.BinaryOp{Op: parser.OpNe, Left: w, Right: &optimizer.ColumnRef{Index: 1, Name: "v", SourceTableIdx: 1}}
	colliding := &optimizer.BinaryOp{Op: parser.OpGt, Left: w, Right: &optimizer.ColumnRef{Index: 2, Name: "zv", SourceTableIdx: 2}}
	q, j, applies := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner,
		Predicate: &optimizer.BinaryOp{Op: parser.OpAnd, Left: movable, Right: colliding}})
	if !applies || q == nil || j != colliding {
		t.Fatalf("the colliding conjunct must stay on the join: probe=%v join=%v applies=%v", q, j, applies)
	}
}

// TestInnerParamQualIgnoresOuterRefsInProbeQuals pins TPC-H Q19's shape: a
// bitmap probe's Recheck Cond repeats the key's outer column, which must not
// make the outer relation look like the probe's own — the residual reading
// both relations still moves to the probe.
func TestInnerParamQualIgnoresOuterRefsInProbeQuals(t *testing.T) {
	outer := optimizer.SeqScanWithSchemaForTest(parallelLabelTestTable(t, "part"), optimizer.Schema{{Name: "p_partkey", SourceTableIdx: 1}, {Name: "p_brand", SourceTableIdx: 1}})
	key := &optimizer.ColumnRef{Index: 0, Name: "p_partkey", SourceTableIdx: 1}
	inner := &optimizer.IndexScan{Table: parallelLabelTestTable(t, "lineitem"), Key: key,
		Cond: &optimizer.BinaryOp{Op: parser.OpEq, Left: &optimizer.ColumnRef{Index: 0, Name: "l_partkey", SourceTableIdx: 2},
			Right: &optimizer.ColumnRef{Index: 0, Name: "p_partkey", SourceTableIdx: 1}}}
	inner.WithSchemaForTest(optimizer.Schema{{Name: "l_partkey", SourceTableIdx: 2}, {Name: "l_quantity", SourceTableIdx: 2}})
	resid := &optimizer.BinaryOp{Op: parser.OpOr,
		Left:  &optimizer.BinaryOp{Op: parser.OpEq, Left: &optimizer.ColumnRef{Index: 1, Name: "p_brand", SourceTableIdx: 1}, Right: &optimizer.IntegerConst{Value: 12}},
		Right: &optimizer.BinaryOp{Op: parser.OpGe, Left: &optimizer.ColumnRef{Index: 3, Name: "l_quantity", SourceTableIdx: 2}, Right: &optimizer.IntegerConst{Value: 1}}}
	q, j, applies := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: resid})
	if !applies || q == nil || j != nil {
		t.Fatalf("Q19's residual must move whole: probe=%v join=%v applies=%v", q, j, applies)
	}
}
