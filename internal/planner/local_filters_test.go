package planner

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

// TestShouldAttachBeforeMHJGate pins design 01 §4.1:
// pre-MHJ attachment fires when (a) FROM has ≥ 5 tables
// AND (b) at least one binding is on a small-dimension
// relation. M0125-0043: clause (b) is answered off the leaf
// SCAN (size-derived) with the catalog flag surviving as an
// explicit hint, so both spellings are exercised below. The
// SmallDim clause exists
// because Slice A regressed Q8 / Q21 from PASS to CANCEL
// when fragmenting their MHJ shape — those queries
// don't have a SmallDim local that would justify the
// trade-off. (M0077-0001.)
func TestShouldAttachBeforeMHJGate(t *testing.T) {
	smallDim := &catalog.Table{Name: "region", SmallDimension: true}
	largeFact := &catalog.Table{Name: "lineitem"}

	// fromCount=4 → never fires (below count threshold).
	bindings4 := []rangeBinding{
		{table: smallDim}, {table: largeFact}, {table: largeFact}, {table: largeFact},
	}
	if shouldAttachBeforeMHJ(bindings4, nil) {
		t.Error("fromCount=4 should NOT trigger pre-MHJ attachment")
	}

	// fromCount=5 with SmallDim → fires (Q5 shape).
	bindings5SmallDim := []rangeBinding{
		{table: smallDim}, {table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact},
	}
	if !shouldAttachBeforeMHJ(bindings5SmallDim, nil) {
		t.Error("fromCount=5 with SmallDim binding should trigger pre-MHJ attachment (Q5 shape)")
	}

	// fromCount=5 without SmallDim → does NOT fire (Q7-shape avoidance).
	bindings5NoSmallDim := []rangeBinding{
		{table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact},
	}
	if shouldAttachBeforeMHJ(bindings5NoSmallDim, nil) {
		t.Error("fromCount=5 without SmallDim should NOT trigger pre-MHJ attachment (avoids Q7/Q8/Q21 regression)")
	}

	// fromCount=8 with SmallDim → fires.
	bindings8SmallDim := []rangeBinding{
		{table: smallDim}, {table: largeFact}, {table: largeFact}, {table: largeFact},
		{table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact},
	}
	if !shouldAttachBeforeMHJ(bindings8SmallDim, nil) {
		t.Error("fromCount=8 with SmallDim should trigger pre-MHJ attachment")
	}

	// M0125-0043: the same gate, answered by the SIZE-derived tag on the leaf
	// scan instead of the catalog hint. No binding carries the flag; the tag
	// rides on scans[0] the way plan-build stamps it, and the gate must fire.
	// Without this the flag's extinction would silently disarm the guard that
	// keeps Q8 / Q21 out of the CANCEL regression.
	bindings5Derived := []rangeBinding{
		{table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact}, {table: largeFact},
	}
	scansDerived := []Node{
		&SeqScan{Table: largeFact, SmallDim: true}, nil, nil, nil, nil,
	}
	if !shouldAttachBeforeMHJ(bindings5Derived, scansDerived) {
		t.Error("fromCount=5 with a size-derived SmallDim leaf scan should trigger pre-MHJ attachment")
	}
	// And the negative: same bindings, same scans, tag off.
	scansPlain := []Node{
		&SeqScan{Table: largeFact}, nil, nil, nil, nil,
	}
	if shouldAttachBeforeMHJ(bindings5Derived, scansPlain) {
		t.Error("fromCount=5 with no small-dimension leaf should NOT trigger pre-MHJ attachment")
	}
}

// TestAttachRelationLocalFiltersWrapsLeafScan pins the
// attachment integration: `Filter(SeqScan)` wraps the
// leaf for the binding that owns local predicates.
func TestAttachRelationLocalFiltersWrapsLeafScan(t *testing.T) {
	// Two scan leaves; only binding 0 has a local pred.
	tbl0 := &catalog.Table{
		Name:    "t0",
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}},
	}
	tbl1 := &catalog.Table{
		Name:    "t1",
		Columns: []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}},
	}
	scan0 := &SeqScan{Table: tbl0, schema: Schema{{Name: "a", Type: catalog.Type{Name: "int4"}}}}
	scan1 := &SeqScan{Table: tbl1, schema: Schema{{Name: "x", Type: catalog.Type{Name: "int4"}}}}
	scans := []Node{scan0, scan1}
	bindings := []rangeBinding{
		{table: tbl0, offset: 0},
		{table: tbl1, offset: 1},
	}

	// Attach `a = 5` to binding 0.
	a := makeColRefBinding("a", 0, 1)
	pred := &BinaryOp{Op: parser.OpEq, Left: a, Right: &IntegerConst{Value: 5}}
	locals := relationLocalFilters{
		byBinding: map[int][]Expr{0: {pred}},
	}

	// Synthesise a join tree of scan0 ⋈ scan1 with no Filter on top.
	join := &Join{Left: scan0, Right: scan1}
	out := attachRelationLocalFilters(join, locals, scans, bindings)

	resJoin, ok := out.(*Join)
	if !ok {
		t.Fatalf("expected *Join, got %T", out)
	}
	leftFilter, ok := resJoin.Left.(*Filter)
	if !ok {
		t.Fatalf("expected Left to be *Filter (wrapping scan0), got %T", resJoin.Left)
	}
	if leftFilter.Child != scan0 {
		t.Errorf("Filter.Child should be scan0, got %v", leftFilter.Child)
	}
	// Right side (scan1) has no local pred → no Filter wrap.
	if _, ok := resJoin.Right.(*Filter); ok {
		t.Errorf("Right should NOT be wrapped (no local pred on binding 1)")
	}
}

// TestAttachRelationLocalFiltersNoLocalsIsNoOp pins
// the fast-path: no locals → tree returned unchanged.
func TestAttachRelationLocalFiltersNoLocalsIsNoOp(t *testing.T) {
	scan := &SeqScan{Table: &catalog.Table{Name: "t"}}
	out := attachRelationLocalFilters(scan, relationLocalFilters{byBinding: nil}, []Node{scan}, []rangeBinding{{}})
	if out != scan {
		t.Errorf("no-locals path should return tree unchanged; got %T", out)
	}
}

// TestAttachRelationLocalFiltersSetsLeafLocalFlag pins
// the contract that downstream remap passes
// (applyJoinTreePosMap, remapPosMapAfterRewrite) require:
// every Filter wrapper produced by Slice A MUST set
// LeafLocal=true. Without that flag, the cumulative-space
// posMap clobbers the leaf-local ColumnRef.Index values
// and the executor crashes with `index out of range
// [N] with length <leafWidth>` (observed pre-fix at
// 2026-05-10 on Q2/Q5/Q7/Q8/Q9 in the synthetic-data
// smoke test).
func TestAttachRelationLocalFiltersSetsLeafLocalFlag(t *testing.T) {
	tbl := &catalog.Table{
		Name:    "t",
		Columns: []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}},
	}
	scan := &SeqScan{Table: tbl, schema: Schema{{Name: "a", Type: catalog.Type{Name: "int4"}}}}
	a := makeColRefBinding("a", 0, 1)
	pred := &BinaryOp{Op: parser.OpEq, Left: a, Right: &IntegerConst{Value: 5}}
	locals := relationLocalFilters{byBinding: map[int][]Expr{0: {pred}}}
	out := attachRelationLocalFilters(scan, locals, []Node{scan}, []rangeBinding{{table: tbl, offset: 0}})
	f, ok := out.(*Filter)
	if !ok {
		t.Fatalf("expected *Filter wrapper, got %T", out)
	}
	if !f.LeafLocal {
		t.Error("Slice A Filter wrapper MUST set LeafLocal=true so cumulative posMap remaps skip it")
	}
}

// TestPlanQ5AttachesLeafLocalFiltersBeforeMHJRewrite is the
// end-to-end Slice A probe: Q5 must attach leaf-local Filter
// wrappers to the region and orders scans before the MHJ rewrite
// runs, which in turn must prevent the historical 6-table
// MultiHashJoin shape from appearing in the final plan.
func TestPlanQ5AttachesLeafLocalFiltersBeforeMHJRewrite(t *testing.T) {
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
	hasSixTableMHJ := false

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
		case *MultiHashJoin:
			if len(x.Tables) == 6 {
				hasSixTableMHJ = true
			}
			for _, child := range x.Tables {
				walk(child)
			}
		}
	}
	walk(node)

	if hasSixTableMHJ {
		t.Fatal("Q5 still planned as a 6-table MultiHashJoin; Slice A leaf-local Filters did not block MHJ rewrite")
	}
	if !foundRegionLeafLocal {
		t.Error("Q5 should attach a LeafLocal Filter wrapper above region before MHJ rewrite")
	}
	if !foundOrdersLeafLocal {
		t.Error("Q5 should attach a LeafLocal Filter wrapper above orders before MHJ rewrite")
	}
}
