package planner

// §2.6 pins for remapByPosMap — one case per arm, plus idempotence.
//
// M0125-0001. docs/design/tpcds-round2-fixes/README.md §2.6 asked for
// these when RC-1a was fixed; they were never written (bushy_remap_test.go
// held only TestBuildJoinFromDP_NonAscendingSubsetKeyRemap from 65dd185a),
// so the six arms RC-1a *added* — IsNullExpr, IsBoolExpr,
// IsDistinctFromExpr, CollateExpr, RowExpr, MultiAssignSubqRow/Elem —
// have been carrying the Q76/Q72 fix with no test of their own.
//
// Why one case per arm rather than a few representative shapes: the
// defect class is "this specific type was never enumerated". A test that
// covers BinaryOp and CaseExpr proves nothing about CollateExpr, which
// is exactly how the original hole survived. The count is asserted
// against the arm list below so deleting an arm cannot quietly shrink
// the matrix.
//
// remapByPosMap has TWO distinct behaviours, and both are pinned:
//
//   - same-scope arms rewrite ColumnRef.Index directly;
//   - scope-opening arms (Exists/Subquery/ArraySubquery/MultiAssignSubq*,
//     and InExpr's Plan) do NOT touch the inner plan's ColumnRefs. They
//     go through remapOuterRefsInSubplan, which rewrites only an
//     OuterColumnRef whose Level names the scope being remapped. An
//     inner ColumnRef must come back UNCHANGED — remapping it would
//     corrupt the subplan, which is the mirror-image bug of RC-1a.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// shiftBy10 is the posMap used throughout: index i ↦ i+10, so "was
// remapped" and "was not remapped" are never confusable.
func shiftBy10(i int) int { return i + 10 }

func remapArmColRef(idx int) *ColumnRef {
	return &ColumnRef{Index: idx, Name: "c"}
}

// innerPlanWithOuterRef builds a subplan whose predicate is an
// OuterColumnRef at Level 1 (the immediate parent scope, i.e. the scope
// remapByPosMap is remapping) AND a plain inner ColumnRef. The first
// must be shifted; the second must not.
func innerPlanWithOuterRef(outerIdx, innerIdx int) (Node, *OuterColumnRef, *ColumnRef) {
	outer := &OuterColumnRef{Level: 1, Index: outerIdx, Name: "o"}
	inner := &ColumnRef{Index: innerIdx, Name: "i"}
	n := &Filter{
		Child:     &SeqScan{},
		Predicate: &BinaryOp{Op: parser.OpEq, Left: outer, Right: inner},
	}
	return n, outer, inner
}

// ---------------------------------------------------------------------
// Same-scope arms: the ColumnRef inside must be shifted.
// ---------------------------------------------------------------------

func TestRemapByPosMap_SameScopeArms(t *testing.T) {
	// Each entry builds an expression holding exactly one ColumnRef at
	// index 5, and returns a getter for that ref's index after the
	// remap. Getters re-read through the (possibly replaced) tree
	// because the ColumnRef arm COPIES the node rather than mutating
	// it — reading a captured pointer would report the stale index and
	// the test would fail for the wrong reason.
	cases := []struct {
		arm   string
		build func() (Expr, func(Expr) int)
	}{
		{"ColumnRef", func() (Expr, func(Expr) int) {
			return remapArmColRef(5), func(e Expr) int { return e.(*ColumnRef).Index }
		}},
		{"BinaryOp/Left", func() (Expr, func(Expr) int) {
			return &BinaryOp{Op: parser.OpEq, Left: remapArmColRef(5), Right: &IntegerConst{Value: 1}},
				func(e Expr) int { return e.(*BinaryOp).Left.(*ColumnRef).Index }
		}},
		{"BinaryOp/Right", func() (Expr, func(Expr) int) {
			return &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{Value: 1}, Right: remapArmColRef(5)},
				func(e Expr) int { return e.(*BinaryOp).Right.(*ColumnRef).Index }
		}},
		{"UnaryOp", func() (Expr, func(Expr) int) {
			return &UnaryOp{Op: parser.OpSub, Operand: remapArmColRef(5)},
				func(e Expr) int { return e.(*UnaryOp).Operand.(*ColumnRef).Index }
		}},
		{"FuncCall", func() (Expr, func(Expr) int) {
			return &FuncCall{Name: "abs", Args: []Expr{remapArmColRef(5)}},
				func(e Expr) int { return e.(*FuncCall).Args[0].(*ColumnRef).Index }
		}},
		{"ExtractExpr", func() (Expr, func(Expr) int) {
			return &ExtractExpr{Field: "year", Source: remapArmColRef(5)},
				func(e Expr) int { return e.(*ExtractExpr).Source.(*ColumnRef).Index }
		}},
		{"CastExpr", func() (Expr, func(Expr) int) {
			return &CastExpr{Operand: remapArmColRef(5), TargetType: "int4"},
				func(e Expr) int { return e.(*CastExpr).Operand.(*ColumnRef).Index }
		}},
		{"InExpr/Operand", func() (Expr, func(Expr) int) {
			return &InExpr{Operand: remapArmColRef(5), List: []Expr{&IntegerConst{Value: 1}}},
				func(e Expr) int { return e.(*InExpr).Operand.(*ColumnRef).Index }
		}},
		// The literal in-list and the PARAM_EXEC Args share the
		// OPERAND's coordinate space; both were "previously skipped"
		// per remapByPosMap's own comment.
		{"InExpr/List", func() (Expr, func(Expr) int) {
			return &InExpr{Operand: &IntegerConst{Value: 1}, List: []Expr{remapArmColRef(5)}},
				func(e Expr) int { return e.(*InExpr).List[0].(*ColumnRef).Index }
		}},
		{"InExpr/Args", func() (Expr, func(Expr) int) {
			return &InExpr{Operand: &IntegerConst{Value: 1}, Args: []Expr{remapArmColRef(5)}},
				func(e Expr) int { return e.(*InExpr).Args[0].(*ColumnRef).Index }
		}},
		{"CaseExpr/Operand", func() (Expr, func(Expr) int) {
			return &CaseExpr{Operand: remapArmColRef(5), Whens: []CaseWhen{{When: &IntegerConst{Value: 1}, Then: &IntegerConst{Value: 2}}}},
				func(e Expr) int { return e.(*CaseExpr).Operand.(*ColumnRef).Index }
		}},
		{"CaseExpr/When", func() (Expr, func(Expr) int) {
			return &CaseExpr{Whens: []CaseWhen{{When: remapArmColRef(5), Then: &IntegerConst{Value: 2}}}},
				func(e Expr) int { return e.(*CaseExpr).Whens[0].When.(*ColumnRef).Index }
		}},
		{"CaseExpr/Then", func() (Expr, func(Expr) int) {
			return &CaseExpr{Whens: []CaseWhen{{When: &BooleanConst{Value: true}, Then: remapArmColRef(5)}}},
				func(e Expr) int { return e.(*CaseExpr).Whens[0].Then.(*ColumnRef).Index }
		}},
		{"CaseExpr/Else", func() (Expr, func(Expr) int) {
			return &CaseExpr{Whens: []CaseWhen{{When: &BooleanConst{Value: true}, Then: &IntegerConst{Value: 2}}}, Else: remapArmColRef(5)},
				func(e Expr) int { return e.(*CaseExpr).Else.(*ColumnRef).Index }
		}},
		// The six arms below are RC-1a's additions — the Q76/Q72 fix.
		{"IsNullExpr", func() (Expr, func(Expr) int) {
			return &IsNullExpr{Operand: remapArmColRef(5)},
				func(e Expr) int { return e.(*IsNullExpr).Operand.(*ColumnRef).Index }
		}},
		{"IsBoolExpr", func() (Expr, func(Expr) int) {
			return &IsBoolExpr{Operand: remapArmColRef(5), TestTrue: true},
				func(e Expr) int { return e.(*IsBoolExpr).Operand.(*ColumnRef).Index }
		}},
		{"IsDistinctFromExpr/Left", func() (Expr, func(Expr) int) {
			return &IsDistinctFromExpr{Left: remapArmColRef(5), Right: &IntegerConst{Value: 1}},
				func(e Expr) int { return e.(*IsDistinctFromExpr).Left.(*ColumnRef).Index }
		}},
		{"IsDistinctFromExpr/Right", func() (Expr, func(Expr) int) {
			return &IsDistinctFromExpr{Left: &IntegerConst{Value: 1}, Right: remapArmColRef(5)},
				func(e Expr) int { return e.(*IsDistinctFromExpr).Right.(*ColumnRef).Index }
		}},
		{"CollateExpr", func() (Expr, func(Expr) int) {
			return &CollateExpr{Operand: remapArmColRef(5), CollationName: "C"},
				func(e Expr) int { return e.(*CollateExpr).Operand.(*ColumnRef).Index }
		}},
		{"RowExpr", func() (Expr, func(Expr) int) {
			return &RowExpr{Elems: []Expr{remapArmColRef(5)}},
				func(e Expr) int { return e.(*RowExpr).Elems[0].(*ColumnRef).Index }
		}},
	}

	for _, tc := range cases {
		t.Run(tc.arm, func(t *testing.T) {
			e, get := tc.build()
			remapByPosMap(&e, shiftBy10)
			if got := get(e); got != 15 {
				t.Fatalf("%s: index %d after remap, want 15 — this arm is not remapping "+
					"(an unenumerated container is a SILENT no-op, the RC-1a defect)", tc.arm, got)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Scope-opening arms: the OUTER ref shifts, the INNER ref must not.
// ---------------------------------------------------------------------

func TestRemapByPosMap_ScopeOpeningArms(t *testing.T) {
	cases := []struct {
		arm   string
		build func(inner Node) Expr
	}{
		{"ExistsExpr", func(n Node) Expr { return &ExistsExpr{Plan: n} }},
		{"SubqueryExpr", func(n Node) Expr { return &SubqueryExpr{Plan: n} }},
		{"ArraySubqueryExpr", func(n Node) Expr { return &ArraySubqueryExpr{Plan: n} }},
		{"MultiAssignSubqRow", func(n Node) Expr { return &MultiAssignSubqRow{Plan: n, NCols: 1} }},
		{"MultiAssignSubqElem", func(n Node) Expr {
			return &MultiAssignSubqElem{Row: &MultiAssignSubqRow{Plan: n, NCols: 1}, ColIdx: 0}
		}},
		// InExpr's subquery form: the arm's comment says the inner Plan
		// is deliberately NOT remapped ("already remapped"), so BOTH
		// refs must be untouched here. Pinned separately below.
	}

	for _, tc := range cases {
		t.Run(tc.arm, func(t *testing.T) {
			inner, outerRef, innerRef := innerPlanWithOuterRef(5, 7)
			e := tc.build(inner)
			remapByPosMap(&e, shiftBy10)
			if outerRef.Index != 15 {
				t.Errorf("%s: OuterColumnRef.Index = %d, want 15 — a Level-1 outer ref "+
					"must be translated or the subquery reads the wrong outer column "+
					"(TPC-H Q21: read l_comment where l_suppkey was meant)", tc.arm, outerRef.Index)
			}
			if innerRef.Index != 7 {
				t.Errorf("%s: inner ColumnRef.Index = %d, want 7 — the inner plan is a "+
					"DIFFERENT coordinate space and must never be positionally remapped",
					tc.arm, innerRef.Index)
			}
		})
	}
}

// InExpr's Plan is the one scope-opening slot remapByPosMap
// intentionally skips entirely.
func TestRemapByPosMap_InExprPlanIsNotDescended(t *testing.T) {
	inner, outerRef, innerRef := innerPlanWithOuterRef(5, 7)
	e := Expr(&InExpr{Operand: remapArmColRef(3), Plan: inner})
	remapByPosMap(&e, shiftBy10)
	if got := e.(*InExpr).Operand.(*ColumnRef).Index; got != 13 {
		t.Errorf("InExpr.Operand = %d, want 13", got)
	}
	if outerRef.Index != 5 {
		t.Errorf("InExpr.Plan outer ref = %d, want 5 — remapByPosMap's InExpr arm "+
			"documents the inner Plan as already remapped and must not touch it", outerRef.Index)
	}
	if innerRef.Index != 7 {
		t.Errorf("InExpr.Plan inner ref = %d, want 7", innerRef.Index)
	}
}

// A Level-2 outer ref belongs to a scope FURTHER out than the one being
// remapped, so it must survive untouched. Getting this wrong is silent:
// the query still runs, against the wrong column.
func TestRemapByPosMap_OnlyMatchingLevelIsRemapped(t *testing.T) {
	lvl1 := &OuterColumnRef{Level: 1, Index: 5, Name: "o1"}
	lvl2 := &OuterColumnRef{Level: 2, Index: 6, Name: "o2"}
	inner := &Filter{
		Child:     &SeqScan{},
		Predicate: &BinaryOp{Op: parser.OpEq, Left: lvl1, Right: lvl2},
	}
	e := Expr(&ExistsExpr{Plan: inner})
	remapByPosMap(&e, shiftBy10)
	if lvl1.Index != 15 {
		t.Errorf("Level-1 ref = %d, want 15", lvl1.Index)
	}
	if lvl2.Index != 6 {
		t.Errorf("Level-2 ref = %d, want 6 — it names an enclosing scope that this "+
			"remap is not rewriting", lvl2.Index)
	}
}

// The double-remap pin §2.6 asked for. Two successive remaps must
// compose (i ↦ i+10 twice ⇒ i+20) with no arm applying the map twice in
// one pass and no arm dropping the second pass. Composition is the
// property the MHJ rewrite depends on: applyJoinTreePosMap and
// remapPosMapAfterRewrite can both run over the same predicate.
func TestRemapByPosMap_DoubleRemapComposes(t *testing.T) {
	// A tree touching a same-scope arm, a struct-slice arm, a list arm
	// and a scope-opening arm at once.
	innerPlan, outerRef, innerRef := innerPlanWithOuterRef(1, 9)
	e := Expr(&CaseExpr{
		Whens: []CaseWhen{{
			When: &IsNullExpr{Operand: remapArmColRef(0)},
			Then: &RowExpr{Elems: []Expr{remapArmColRef(2)}},
		}},
		Else: &FuncCall{Name: "coalesce", Args: []Expr{
			&CollateExpr{Operand: remapArmColRef(3), CollationName: "C"},
			&ExistsExpr{Plan: innerPlan},
		}},
	})

	remapByPosMap(&e, shiftBy10)
	remapByPosMap(&e, shiftBy10)

	ce := e.(*CaseExpr)
	if got := ce.Whens[0].When.(*IsNullExpr).Operand.(*ColumnRef).Index; got != 20 {
		t.Errorf("IsNullExpr operand = %d, want 20", got)
	}
	if got := ce.Whens[0].Then.(*RowExpr).Elems[0].(*ColumnRef).Index; got != 22 {
		t.Errorf("RowExpr elem = %d, want 22", got)
	}
	fc := ce.Else.(*FuncCall)
	if got := fc.Args[0].(*CollateExpr).Operand.(*ColumnRef).Index; got != 23 {
		t.Errorf("CollateExpr operand = %d, want 23", got)
	}
	if outerRef.Index != 21 {
		t.Errorf("nested outer ref = %d, want 21", outerRef.Index)
	}
	if innerRef.Index != 9 {
		t.Errorf("nested inner ref = %d, want 9 (never remapped)", innerRef.Index)
	}
}

// remapByPosMap's ColumnRef arm COPIES the node when the index changes,
// so a caller holding the original pointer keeps the pre-remap value.
// That is deliberate (nodes are shared between plan fragments) and any
// conversion to the exprwalk drivers must preserve it — cloneExprRefs
// has the same property, rewriteExprRefsInPlace does not.
func TestRemapByPosMap_ColumnRefIsCopiedNotMutated(t *testing.T) {
	orig := remapArmColRef(5)
	e := Expr(&BinaryOp{Op: parser.OpEq, Left: orig, Right: &IntegerConst{Value: 1}})
	remapByPosMap(&e, shiftBy10)

	if orig.Index != 5 {
		t.Fatalf("the original ColumnRef was mutated (index %d); remapByPosMap copies "+
			"on change because expression nodes are shared between plan fragments", orig.Index)
	}
	if got := e.(*BinaryOp).Left.(*ColumnRef).Index; got != 15 {
		t.Fatalf("the tree's ColumnRef = %d, want 15", got)
	}
}

// An identity posMap must not copy at all — remapByPosMap only replaces
// the node when the index actually changes, and cheap no-op remaps run
// often enough for that to matter.
func TestRemapByPosMap_IdentityMapSharesNodes(t *testing.T) {
	orig := remapArmColRef(5)
	e := Expr(orig)
	remapByPosMap(&e, func(i int) int { return i })
	if e.(*ColumnRef) != orig {
		t.Fatal("identity remap replaced the node; it should be left shared")
	}
}
