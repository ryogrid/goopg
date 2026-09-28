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
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	innerCol := &optimizer.ColumnRef{Index: 1, Name: "a", SourceTableIdx: 2}
	joinQual := &optimizer.BinaryOp{Op: parser.OpNe, Left: innerCol, Right: outerCol}
	outerOnly := &optimizer.BinaryOp{Op: parser.OpGt, Left: outerCol, Right: &optimizer.IntegerConst{Value: 1}}

	nli := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeAnti, Outer: outer, Inner: inner, Predicate: joinQual}
	q, _ := renderedParamQual(nli)
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
	if movedParamQual(mixed) {
		t.Error("a conjunct that reads only the outer side must keep the residual on the join")
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
	if q, _ := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: ecEq}); q == nil {
		t.Error("an inner = outer equality within required_outer renders under the probe, as TPC-DS Q50's")
	}
	ecOther := &optimizer.BinaryOp{Op: parser.OpEq, Left: innerCol, Right: &optimizer.ColumnRef{Index: 0, Name: "z", SourceTableIdx: 3}}
	if movedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: ecOther}) {
		t.Error("an equality naming a relation outside required_outer stays a join filter, as TPC-H Q9's")
	}

	lateral := &optimizer.Join{Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop, Lateral: true,
		Left: outer, Right: inner, Predicate: joinQual}
	if q, _ := renderedParamQual(lateral); q == nil || !paramInnerChild(lateral, inner) {
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
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "a", SourceTableIdx: 1}
	innerCol := &optimizer.ColumnRef{Index: 1, Name: "s", SourceTableIdx: 2}
	arm := func(lo int64) optimizer.Expr {
		return &optimizer.BinaryOp{Op: parser.OpAnd,
			Left:  &optimizer.InExpr{Operand: innerCol, List: []optimizer.Expr{&optimizer.IntegerConst{Value: lo}, &optimizer.IntegerConst{Value: lo + 1}}},
			Right: &optimizer.BinaryOp{Op: parser.OpGe, Left: outerCol, Right: &optimizer.IntegerConst{Value: lo}}}
	}
	orQual := &optimizer.BinaryOp{Op: parser.OpOr, Left: arm(0), Right: arm(10)}
	nli := &optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: orQual}
	q, _ := renderedParamQual(nli)
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
	outerCol := &optimizer.ColumnRef{Index: 0, Name: "c_cdemo", SourceTableIdx: 1}
	keyCol := &optimizer.ColumnRef{Index: 1, Name: "cd_demo_sk", SourceTableIdx: 2}
	restated := &optimizer.BinaryOp{Op: parser.OpEq, Left: outerCol, Right: keyCol}

	q, moved := renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner, Predicate: restated})
	if !moved || q != nil {
		t.Fatalf("a restated key equality must print nowhere, got moved=%v qual=%v", moved, q)
	}

	// With another inner-reading conjunct, only that conjunct renders.
	other := &optimizer.BinaryOp{Op: parser.OpNe, Left: &optimizer.ColumnRef{Index: 2, Name: "cd_x", SourceTableIdx: 2}, Right: outerCol}
	q, moved = renderedParamQual(&optimizer.NestedLoopIndexJoin{Type: optimizer.JoinTypeInner, Outer: outer, Inner: inner,
		Predicate: &optimizer.BinaryOp{Op: parser.OpAnd, Left: restated, Right: other}})
	if b, ok := q.(*optimizer.BinaryOp); !moved || !ok || b.Op != parser.OpNe {
		t.Fatalf("only the non-key conjunct renders under the probe, got moved=%v qual=%#v", moved, q)
	}
}

func movedParamQual(n optimizer.Node) bool {
	_, moved := renderedParamQual(n)
	return moved
}
