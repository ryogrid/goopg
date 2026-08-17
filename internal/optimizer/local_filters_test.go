package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// makeColRefBinding builds a *ColumnRef pointing at
// FROM-cumulative position `cumIdx` for an int4 column.
func makeColRefBinding(name string, cumIdx int, srcIdx int16) *ColumnRef {
	return &ColumnRef{
		Index:          cumIdx,
		Name:           name,
		Type:           catalog.Type{Name: "int4"},
		SourceTableIdx: srcIdx,
	}
}

// TestPartitionConjunctsSplitsByBinding pins the
// fundamental partition contract: single-binding
// conjuncts go to `locals`; multi-binding conjuncts go
// to `joinConjuncts`. (M0077-0001.)
func TestPartitionConjunctsSplitsByBinding(t *testing.T) {
	// Two bindings: t0 has 3 cols (offsets 0..2); t1 has 2 cols
	// (offsets 3..4). cumOffsets = [0, 3, 5].
	cumOffsets := []int{0, 3, 5}
	t0col0 := makeColRefBinding("a", 0, 1) // t0.a
	t0col1 := makeColRefBinding("b", 1, 1) // t0.b
	t1col0 := makeColRefBinding("x", 3, 2) // t1.x

	// t0.a = 5  (single-binding, t0)
	localT0 := &BinaryOp{Op: parser.OpEq, Left: t0col0, Right: &IntegerConst{Value: 5}}
	// t0.b = t1.x  (multi-binding)
	joinAB := &BinaryOp{Op: parser.OpEq, Left: t0col1, Right: t1col0}

	conjuncts := []Expr{localT0, joinAB}
	jc, locals := partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)

	if len(jc) != 1 || jc[0] != joinAB {
		t.Errorf("expected joinConjuncts = [joinAB], got %v", jc)
	}
	if got := locals.byBinding[0]; len(got) != 1 || got[0] != localT0 {
		t.Errorf("expected locals[0] = [localT0], got %v", got)
	}
	if got, ok := locals.byBinding[1]; ok {
		t.Errorf("expected no locals for binding 1, got %v", got)
	}
}

// TestPartitionConjunctsSubqueryStaysJoinSide pins
// design 01 §3.1.3: predicates with SubqueryExpr /
// ExistsExpr / OuterColumnRef / InExpr-with-Plan are
// INELIGIBLE for local attachment.
func TestPartitionConjunctsSubqueryStaysJoinSide(t *testing.T) {
	cumOffsets := []int{0, 3}
	col := makeColRefBinding("a", 0, 1)
	// `a IN (subquery)` — InExpr with Plan != nil.
	subq := &InExpr{
		Operand: col,
		Plan:    &SeqScan{Table: &catalog.Table{Name: "x"}},
	}
	conjuncts := []Expr{subq}
	jc, locals := partitionConjunctsForJoinPlanning(conjuncts, cumOffsets)

	if len(jc) != 1 || jc[0] != subq {
		t.Errorf("subquery-bearing predicate should stay join-side, got jc=%v", jc)
	}
	if len(locals.byBinding) != 0 {
		t.Errorf("subquery predicate should NOT be in locals, got %v", locals.byBinding)
	}
}

// TestConjunctIsLocalEligibleNestedSubquery pins the
// recursive walk: subquery nested inside a BinaryOp
// is detected.
func TestConjunctIsLocalEligibleNestedSubquery(t *testing.T) {
	// `a > (subquery)` — wrapped in a BinaryOp.
	col := makeColRefBinding("a", 0, 1)
	expr := &BinaryOp{
		Op:    parser.OpGt,
		Left:  col,
		Right: &SubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}},
	}
	if conjunctIsLocalEligible(expr) {
		t.Error("BinaryOp wrapping a SubqueryExpr should be ineligible for local attachment")
	}
}

// TestConjunctIsLocalEligibleSimpleEqIsEligible pins
// the happy path.
func TestConjunctIsLocalEligibleSimpleEqIsEligible(t *testing.T) {
	col := makeColRefBinding("a", 0, 1)
	expr := &BinaryOp{Op: parser.OpEq, Left: col, Right: &IntegerConst{Value: 5}}
	if !conjunctIsLocalEligible(expr) {
		t.Error("simple a = 5 should be eligible")
	}
}

// TestLocalizeExprToLeafSubtractsBindingOffset pins
// the index rebase: ColumnRef.Index changes from
// FROM-cumulative to leaf-local; SourceTableIdx is
// preserved. (M0077-0001.)
func TestLocalizeExprToLeafSubtractsBindingOffset(t *testing.T) {
	binding := rangeBinding{offset: 16, sourceIdx: 3}
	// Cumulative position 18 → leaf-local position 2.
	cr := makeColRefBinding("c", 18, 3)
	localised := localizeExprToLeaf(cr, binding)
	out, ok := localised.(*ColumnRef)
	if !ok {
		t.Fatalf("expected *ColumnRef after localise, got %T", localised)
	}
	if out.Index != 2 {
		t.Errorf("expected index 18 - 16 = 2, got %d", out.Index)
	}
	if out.SourceTableIdx != 3 {
		t.Errorf("SourceTableIdx should be preserved, got %d", out.SourceTableIdx)
	}
	// Source pointer should be a fresh Datum (defensive copy).
	if out == cr {
		t.Error("localizeExprToLeaf must NOT mutate the input ColumnRef in place")
	}
}

// TestLocalizeExprToLeafBinaryOpRecursive pins
// recursive descent into BinaryOp + IntegerConst pass-through.
func TestLocalizeExprToLeafBinaryOpRecursive(t *testing.T) {
	binding := rangeBinding{offset: 5}
	// `col(idx=8) = 'ASIA'`  → after localise: idx=3.
	cr := makeColRefBinding("r_name", 8, 1)
	expr := &BinaryOp{
		Op:    parser.OpEq,
		Left:  cr,
		Right: &StringConst{Value: "ASIA"},
	}
	localised := localizeExprToLeaf(expr, binding)
	out, ok := localised.(*BinaryOp)
	if !ok {
		t.Fatalf("expected *BinaryOp, got %T", localised)
	}
	leftCR, ok := out.Left.(*ColumnRef)
	if !ok || leftCR.Index != 3 {
		t.Errorf("Left ColumnRef.Index should be 3, got %v", out.Left)
	}
	if _, ok := out.Right.(*StringConst); !ok {
		t.Errorf("Right StringConst should pass through, got %T", out.Right)
	}
}

// The Slice-A rollout gate (`TestShouldAttachBeforeMHJGate`) and the three
// `attachRelationLocalFilters` pins lived here until M0127-P6.3 deleted both
// functions with the old subset-bitmask DP, their only caller (08 §4). The
// behaviour they guarded — leaf-local filters above the scans — is now
// produced unconditionally by the PG-shaped seam (joinsearchseam.go) and
// pinned end-to-end by TestPlanQ5AttachesLeafLocalFilters below.

// TestPlanQ5AttachesLeafLocalFilters is the end-to-end Slice A probe: Q5 must
// attach leaf-local Filter wrappers to the region and orders scans.
//
// Until M0127-P6.2 the test also asserted the consequence that motivated Slice
// A — that those wrappers kept the historical 6-table MultiHashJoin shape out
// of the final plan, because the packer's chain detector declined on
// Filter(SeqScan) leaves. The packed node is gone, so only the attachment
// itself is still assertable; the shape half is now true by construction.
func TestPlanQ5AttachesLeafLocalFilters(t *testing.T) {
	cat, err := tpch.Catalog()
	if err != nil {
		t.Fatalf("tpch.Catalog: %v", err)
	}
	queries := tpch.Queries()
	stmts, err := parser.Parse(queries[5])
	if err != nil {
		t.Fatalf("parse Q5: %v", err)
	}
	if len(stmts) == 0 {
		t.Fatal("parse Q5: no statements")
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan Q5: %v", err)
	}

	foundRegionLeafLocal := false
	foundOrdersLeafLocal := false

	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		switch x := n.(type) {
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *Filter:
			if x.LeafLocal {
				if scan, ok := x.Child.(*SeqScan); ok && scan.Table != nil {
					switch scan.Table.Name {
					case "region":
						foundRegionLeafLocal = true
					case "orders":
						foundOrdersLeafLocal = true
					}
				}
			}
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		}
	}
	walk(node)

	if !foundRegionLeafLocal {
		t.Error("Q5 should attach a LeafLocal Filter wrapper above region")
	}
	if !foundOrdersLeafLocal {
		t.Error("Q5 should attach a LeafLocal Filter wrapper above orders")
	}
}
