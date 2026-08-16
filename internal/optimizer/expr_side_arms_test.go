package optimizer

// M0125-0002 commit 5 pins for exprSide — the join-side classifier,
// stated per kind.
//
// exprSide has exactly one live consumer: splitEqualityForHash, which
// scans a join predicate's `=` conjuncts for one whose operands
// classify cleanly as (left, right) and promotes that pair to hash
// build/probe keys; everything else stays on jn.Predicate as a
// per-match residual recheck. A kind the old 15-of-32 hand switch fell
// through therefore read as sideMixed and silently declined the hash
// path — a missed optimisation, never a wrong answer (this walker has
// always failed CLOSED, unlike the fail-open visitors of commits 3–4).
// D2 row 5 of the design doc: "decides which side a conjunct is pushed
// to".
//
// Three behaviours are pinned, mirroring cloneExprShiftIdx (commit 2),
// the series' other fail-closed classifier:
//
//   - pure same-scope containers the old switch never enumerated now
//     classify by the ColumnRefs under them, and plan-time-fixed /
//     row-independent leaves join the ParamRef class (sideUnknown,
//     combines with anything);
//   - the old declines are preserved: any node carrying an inner Plan
//     is sideMixed (scopeVeto), and *OuterColumnRef / *CTIDExpr are
//     vetoed explicitly — exprChildSlots correctly reports both as
//     childless leaves, and a completeness-driven conversion would
//     otherwise ADMIT them (an outer ref may go stale if a cached hash
//     table outlives one outer binding; ctid is injected per scan slot
//     and a side misattribution would hash the WRONG side's ctid);
//   - an unenumerated type still classifies sideMixed — fail-closed,
//     NOT the panic of commits 3–4, because here a decline costs an
//     optimisation, never a wrong answer.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// ---------------------------------------------------------------------
// Kinds the old 15-arm switch fell through to sideMixed: each must now
// classify by the references under it. One case per kind — covering
// IsNullExpr proves nothing about CollateExpr (how the original hole
// survived). Every subtest here FAILED against the old switch.
// ---------------------------------------------------------------------

func TestExprSide_NewlyClassifiedContainers(t *testing.T) {
	const leftWidth = 5
	mkL := func() *ColumnRef { return &ColumnRef{Index: 2, Name: "l"} }

	type tc struct {
		name string
		expr func(*ColumnRef) Expr
	}
	cases := []tc{
		{"IsNullExpr", func(r *ColumnRef) Expr { return &IsNullExpr{Operand: r} }},
		{"IsBoolExpr", func(r *ColumnRef) Expr { return &IsBoolExpr{Operand: r} }},
		{"CollateExpr", func(r *ColumnRef) Expr { return &CollateExpr{Operand: r} }},
		{"IsDistinctFromExpr/Left", func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: r, Right: &IntegerConst{}}
		}},
		{"IsDistinctFromExpr/Right", func(r *ColumnRef) Expr {
			return &IsDistinctFromExpr{Left: &IntegerConst{}, Right: r}
		}},
		{"RowExpr", func(r *ColumnRef) Expr { return &RowExpr{Elems: []Expr{r}} }},
		// The literal-list IN form is pure same-scope (commit 2 admits
		// the identical shape into the NLI unwrap).
		{"InExpr/Operand no Plan", func(r *ColumnRef) Expr {
			return &InExpr{Operand: r, List: []Expr{&IntegerConst{}}}
		}},
		{"InExpr/List no Plan", func(r *ColumnRef) Expr {
			return &InExpr{Operand: &IntegerConst{}, List: []Expr{r}}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exprSide(c.expr(mkL()), leftWidth); got != sideLeft {
				t.Errorf("exprSide(%s wrapping a left ColumnRef) = %v, want sideLeft — "+
					"the conjunct declines the hash path for no reason (RC-1a latency)",
					c.name, got)
			}
		})
	}
}

func TestExprSide_RowIndependentLeavesAreUnknown(t *testing.T) {
	const leftWidth = 5
	// Same classification commit 2 argued for cloneExprShiftIdx: these
	// leaves' values do not depend on which side's row evaluates them.
	// TableOidExpr carries its OID baked in at plan time
	// (resolveTableoidForBinding); Merge* read executor ctx, not the
	// join row; ExecParamRef is bound from the subplan param context
	// exactly like ParamRef, which the old switch already admitted.
	cases := map[string]Expr{
		"ExecParamRef":     &ExecParamRef{},
		"TableOidExpr":     &TableOidExpr{},
		"MergeActionExpr":  &MergeActionExpr{},
		"MergeWholeRowRef": &MergeWholeRowRef{},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exprSide(e, leftWidth); got != sideUnknown {
				t.Errorf("exprSide(%s) = %v, want sideUnknown", name, got)
			}
		})
	}
	// And sideUnknown must combine: a left ref plus a row-independent
	// leaf is still a usable left key.
	e := &BinaryOp{Op: parser.OpAdd,
		Left:  &ColumnRef{Index: 2, Name: "l"},
		Right: &ExecParamRef{}}
	if got := exprSide(e, leftWidth); got != sideLeft {
		t.Errorf("exprSide(left + ExecParamRef) = %v, want sideLeft", got)
	}
}

// ---------------------------------------------------------------------
// The old arms must keep working through the conversion.
// ---------------------------------------------------------------------

func TestExprSide_PreservedArms(t *testing.T) {
	const leftWidth = 5
	l := func() *ColumnRef { return &ColumnRef{Index: 2, Name: "l"} }
	r := func() *ColumnRef { return &ColumnRef{Index: 7, Name: "r"} }

	type tc struct {
		name string
		expr Expr
		want joinSide
	}
	cases := []tc{
		{"ColumnRef left", l(), sideLeft},
		{"ColumnRef right", r(), sideRight},
		{"IntegerConst", &IntegerConst{}, sideUnknown},
		{"StringConst", &StringConst{}, sideUnknown},
		{"NumericConst", &NumericConst{}, sideUnknown},
		{"NullConst", &NullConst{}, sideUnknown},
		{"BooleanConst", &BooleanConst{}, sideUnknown},
		{"ParamRef", &ParamRef{}, sideUnknown},
		{"TypedStringLit", &TypedStringLit{}, sideUnknown},
		{"IntervalLit", &IntervalLit{}, sideUnknown},
		{"BinaryOp merge left+const", &BinaryOp{Op: parser.OpAdd, Left: l(), Right: &IntegerConst{}}, sideLeft},
		{"BinaryOp merge left+left", &BinaryOp{Op: parser.OpAdd, Left: l(), Right: l()}, sideLeft},
		{"BinaryOp merge left+right", &BinaryOp{Op: parser.OpAdd, Left: l(), Right: r()}, sideMixed},
		{"UnaryOp", &UnaryOp{Operand: r()}, sideRight},
		{"CastExpr", &CastExpr{Operand: l()}, sideLeft},
		{"FuncCall", &FuncCall{Args: []Expr{&IntegerConst{}, r()}}, sideRight},
		{"ExtractExpr", &ExtractExpr{Source: l()}, sideLeft},
		{"CaseExpr/Operand", &CaseExpr{Operand: l(),
			Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}}}, sideLeft},
		{"CaseExpr/When", &CaseExpr{
			Whens: []CaseWhen{{When: r(), Then: &IntegerConst{}}}}, sideRight},
		{"CaseExpr/Then", &CaseExpr{
			Whens: []CaseWhen{{When: &IntegerConst{}, Then: l()}}}, sideLeft},
		{"CaseExpr/Else", &CaseExpr{
			Whens: []CaseWhen{{When: &IntegerConst{}, Then: &IntegerConst{}}},
			Else:  r()}, sideRight},
		{"CaseExpr spanning", &CaseExpr{
			Whens: []CaseWhen{{When: l(), Then: r()}}}, sideMixed},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := exprSide(c.expr, leftWidth); got != c.want {
				t.Errorf("exprSide(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Preserved declines. A hash key is evaluated per row against ONE
// side's slot; each of these either names a value that slot cannot
// supply or opens another scope, and every one classified sideMixed
// under the old switch too — the pin keeps the conversion honest about
// not ADMITTING them by completeness.
// ---------------------------------------------------------------------

func TestExprSide_PlanCarryingNodesStayMixed(t *testing.T) {
	const leftWidth = 5
	type tc struct {
		name string
		expr func(Node) Expr
	}
	cases := []tc{
		{"SubqueryExpr", func(p Node) Expr { return &SubqueryExpr{Plan: p} }},
		// Args referencing a single side must NOT rescue the node: the
		// veto fires on the Plan slot regardless of what the same-scope
		// Args merged to.
		{"SubqueryExpr/Args one-sided", func(p Node) Expr {
			return &SubqueryExpr{Plan: p, Args: []Expr{&ColumnRef{Index: 2, Name: "l"}}}
		}},
		{"ExistsExpr", func(p Node) Expr { return &ExistsExpr{Plan: p} }},
		{"InExpr with Plan", func(p Node) Expr {
			return &InExpr{Operand: &ColumnRef{Index: 2, Name: "l"}, Plan: p}
		}},
		{"ArraySubqueryExpr", func(p Node) Expr { return &ArraySubqueryExpr{Plan: p} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, _, _ := innerPlanWithOuterRef(5, 7)
			if got := exprSide(c.expr(p), leftWidth); got != sideMixed {
				t.Errorf("exprSide(%s) = %v, want sideMixed — a subplan is not a "+
					"per-row hashable key", c.name, got)
			}
		})
	}
}

func TestExprSide_OuterColumnRefAndCTIDStayMixed(t *testing.T) {
	const leftWidth = 5
	cases := map[string]Expr{
		"OuterColumnRef root":   &OuterColumnRef{Level: 1, Index: 3, Name: "o"},
		"OuterColumnRef nested": &IsNullExpr{Operand: &OuterColumnRef{Level: 1, Index: 3, Name: "o"}},
		"CTIDExpr root":         &CTIDExpr{},
		"CTIDExpr under cast":   &CastExpr{Operand: &CTIDExpr{}},
		// Absorbing: a clean left ref cannot rescue a vetoed sibling.
		"veto absorbs left ref": &BinaryOp{Op: parser.OpAdd,
			Left:  &ColumnRef{Index: 2, Name: "l"},
			Right: &OuterColumnRef{Level: 1, Index: 3, Name: "o"}},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exprSide(e, leftWidth); got != sideMixed {
				t.Errorf("exprSide(%s) = %v, want sideMixed", name, got)
			}
		})
	}
}

func TestExprSide_UnenumeratedTypeStaysMixed(t *testing.T) {
	const leftWidth = 5
	cases := map[string]Expr{
		"root":   &unknownExpr{},
		"nested": &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 2, Name: "l"}, Right: &unknownExpr{}},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exprSide(e, leftWidth); got != sideMixed {
				t.Errorf("exprSide(unenumerated %s) = %v, want sideMixed (fail-closed)", name, got)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The semantic pin on the ONE live consumer: an `=` conjunct whose
// operand is a newly-classified container now yields a hash key pair.
// This is commit 5's headline behaviour change — deliberate, per D2
// row 5 — pinned here so a future "restore the old fall-through"
// regression is a test failure, not a silent plan move.
// ---------------------------------------------------------------------

func TestSplitEqualityForHash_NewlyClassifiedOperandSplits(t *testing.T) {
	const leftWidth = 5
	lKey := &IsNullExpr{Operand: &ColumnRef{Index: 2, Name: "l"}}
	rKey := &ColumnRef{Index: 7, Name: "r"}
	pred := &BinaryOp{Op: parser.OpEq, Left: lKey, Right: rKey}

	gotL, gotR, ok := splitEqualityForHash(pred, leftWidth)
	if !ok {
		t.Fatal("splitEqualityForHash declined `(l IS NULL) = r`; the old " +
			"fall-through is back and the conjunct is stranded on the NL path")
	}
	if gotL != Expr(lKey) || gotR != Expr(rKey) {
		t.Errorf("splitEqualityForHash returned (%T, %T), want the IsNullExpr "+
			"as the left key and the ColumnRef as the right key", gotL, gotR)
	}
}

func TestSplitEqualityForHash_OuterRefOperandStillDeclines(t *testing.T) {
	const leftWidth = 5
	pred := &BinaryOp{Op: parser.OpEq,
		Left: &BinaryOp{Op: parser.OpAdd,
			Left:  &ColumnRef{Index: 2, Name: "l"},
			Right: &OuterColumnRef{Level: 1, Index: 3, Name: "o"}},
		Right: &ColumnRef{Index: 7, Name: "r"}}
	if _, _, ok := splitEqualityForHash(pred, leftWidth); ok {
		t.Error("splitEqualityForHash admitted a key containing an *OuterColumnRef; " +
			"a cached hash table would go stale across outer bindings")
	}
}
