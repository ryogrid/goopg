package optimizer

// Regression pins for M0125-0002 commit 2: cloneExprShiftIdx converted onto
// cloneExprRefs / exprChildSlots.
//
// docs/design/0125-0002-walker-conversion-and-mhj-composition-risk.md, D2 row 2.
//
// Commit 1 (remapByPosMap) carried an empty-plan-diff expectation because
// 0125-0001 D6's 18-arm table pinned its behaviour exactly. This one does not:
// cloneExprShiftIdx is a fail-CLOSED admission test whose `return nil, false`
// makes the caller (nl_index_join.go:363) set `okAll = false` and abandon the
// inner-Filter unwrap altogether. Completing the walker therefore does not
// "fix a silent no-op" — it OPENS the NLI inner unwrap on conjunct shapes that
// declined before, which moves plans.
//
// That makes the interesting assertions two-sided, and this file states both
// sides rather than only the happy one:
//
//   - what is now ADMITTED (and shifted correctly), where the walker used to
//     decline the whole rewrite; and
//   - what is still DECLINED, because exprChildSlots is complete over all 32
//     types and a conversion driven only by completeness would have silently
//     admitted three shapes a flat outer++inner row cannot serve.
//
// The second group is the one worth a test. `*OuterColumnRef` and `*CTIDExpr`
// are CHILDLESS LEAVES to exprChildSlots — entirely correct as a description
// of their child structure, and exactly why they need an explicit veto here:
// nothing in the driver knows that their VALUE is bound to the row they were
// resolved against.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// shiftedIdx returns the Index of the ColumnRef reachable at fn(cloned),
// failing the test when the clone was declined.
func mustShift(t *testing.T, e Expr, shift int) Expr {
	t.Helper()
	cl, ok := cloneExprShiftIdx(e, shift)
	if !ok {
		t.Fatalf("cloneExprShiftIdx declined %T; expected it to be admitted", e)
	}
	return cl
}

func mustDecline(t *testing.T, e Expr, why string) {
	t.Helper()
	if cl, ok := cloneExprShiftIdx(e, 5); ok {
		t.Fatalf("cloneExprShiftIdx ADMITTED %T (%s); got %#v", e, why, cl)
	}
}

// TestCloneExprShiftIdx_AdmitsSameScopeContainers pins the arms the conversion
// gained. Every one of these declined the whole inner-Filter unwrap before
// commit 2.
//
// *IsNullExpr leads deliberately: a missing *IsNullExpr arm in a sibling
// walker (remapByPosMap) is the RC-1a defect the whole milestone is named
// after — TPC-DS Q76's `WHERE ss_customer_sk IS NULL` kept a pre-rewrite index
// and the query returned 0 rows instead of 100 (round-2 README §2).
func TestCloneExprShiftIdx_AdmitsSameScopeContainers(t *testing.T) {
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: "c"} }

	cases := []struct {
		name string
		in   Expr
		// want maps the clone to the single shifted index it should carry.
		want func(Expr) int
	}{
		{
			name: "IsNullExpr",
			in:   &IsNullExpr{Operand: col(2), Negated: true},
			want: func(e Expr) int { return e.(*IsNullExpr).Operand.(*ColumnRef).Index },
		},
		{
			name: "IsBoolExpr",
			in:   &IsBoolExpr{Operand: col(2), TestTrue: true},
			want: func(e Expr) int { return e.(*IsBoolExpr).Operand.(*ColumnRef).Index },
		},
		{
			name: "CollateExpr",
			in:   &CollateExpr{Operand: col(2), CollationName: "C"},
			want: func(e Expr) int { return e.(*CollateExpr).Operand.(*ColumnRef).Index },
		},
		{
			name: "ExtractExpr",
			in:   &ExtractExpr{Source: col(2), Field: "year"},
			want: func(e Expr) int { return e.(*ExtractExpr).Source.(*ColumnRef).Index },
		},
		{
			name: "IsDistinctFromExpr",
			in:   &IsDistinctFromExpr{Left: col(2), Right: &NullConst{}},
			want: func(e Expr) int { return e.(*IsDistinctFromExpr).Left.(*ColumnRef).Index },
		},
		{
			name: "RowExpr",
			in:   &RowExpr{Elems: []Expr{col(2)}},
			want: func(e Expr) int { return e.(*RowExpr).Elems[0].(*ColumnRef).Index },
		},
		{
			// `col IN (1,2,3)` — a Plan-LESS InExpr is pure same-scope and is
			// the most common of the newly admitted shapes in real SQL.
			name: "InExpr without Plan",
			in:   &InExpr{Operand: col(2), List: []Expr{&IntegerConst{Value: 1}}},
			want: func(e Expr) int { return e.(*InExpr).Operand.(*ColumnRef).Index },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := mustShift(t, tc.in, 5)
			if got := tc.want(cl); got != 7 {
				t.Fatalf("shifted index = %d, want 7 (2+5)", got)
			}
		})
	}
}

// TestCloneExprShiftIdx_CaseExprWhensAreShifted is separate from the table
// above because CaseExpr.Whens is a []CaseWhen — a STRUCT slice, not []Expr.
// A hand-written arm that ranged it BY VALUE and rewrote &w.When would address
// the loop copy and drop every shift silently, which is precisely the trap
// exprChildSlots documents at its own CaseExpr arm. Pinning both the WHEN and
// the THEN side proves the slots address the backing array.
func TestCloneExprShiftIdx_CaseExprWhensAreShifted(t *testing.T) {
	in := &CaseExpr{
		Whens: []CaseWhen{{
			When: &ColumnRef{Index: 1, Name: "w"},
			Then: &ColumnRef{Index: 2, Name: "t"},
		}},
		Else: &ColumnRef{Index: 3, Name: "e"},
	}
	cl := mustShift(t, in, 10).(*CaseExpr)

	if got := cl.Whens[0].When.(*ColumnRef).Index; got != 11 {
		t.Errorf("Whens[0].When index = %d, want 11", got)
	}
	if got := cl.Whens[0].Then.(*ColumnRef).Index; got != 12 {
		t.Errorf("Whens[0].Then index = %d, want 12", got)
	}
	if got := cl.Else.(*ColumnRef).Index; got != 13 {
		t.Errorf("Else index = %d, want 13", got)
	}
	// The original must be untouched: the caller only commits the unwrap
	// after every conjunct is accepted, and restores j.Right otherwise.
	if got := in.Whens[0].When.(*ColumnRef).Index; got != 1 {
		t.Errorf("ORIGINAL Whens[0].When mutated to %d; clone aliased the struct slice", got)
	}
}

// TestCloneExprShiftIdx_DeclinesRowBoundLeaves is the correctness half of the
// conversion. Both types below are childless leaves to exprChildSlots — a
// correct description of their child structure — so a conversion driven only
// by "the primitive knows this type" would have admitted them.
func TestCloneExprShiftIdx_DeclinesRowBoundLeaves(t *testing.T) {
	// A correlation into a scope ABOVE this join. The flat outer++inner row
	// the NLI residual is evaluated against cannot supply it, and shifting
	// Index by the outer width would silently re-point it at a real column
	// of the joined row — a wrong answer dressed as a valid index.
	mustDecline(t, &OuterColumnRef{Level: 1, Index: 0, Name: "o"},
		"correlation above this join")

	// Worse than unsupported: seqScanOp injects the block/offset pair into
	// the SCANNED row's slot (MaterializedSlot.hasCTID). Hoisted to the NLI
	// residual it would read the OUTER side's ctid.
	mustDecline(t, &CTIDExpr{}, "ctid is bound to the scanned slot")

	// Nested, not just at the root — the veto must survive the driver's
	// bottom-up dispatch rather than being a root-only guard.
	mustDecline(t, &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 0},
		Right: &OuterColumnRef{Level: 1, Index: 0},
	}, "OuterColumnRef nested under a BinaryOp")
}

// TestCloneExprShiftIdx_DeclinesInnerPlans pins the scopeVeto policy choice.
// A subplan's OuterColumnRefs are resolved against the INNER scan's scope;
// hoisting the conjunct one level out changes what Level 1 names, and no
// positional shift of the enclosing expression can repair that.
func TestCloneExprShiftIdx_DeclinesInnerPlans(t *testing.T) {
	plan := &SeqScan{}

	mustDecline(t, &ExistsExpr{Plan: plan}, "EXISTS carries an inner plan")
	mustDecline(t, &SubqueryExpr{Plan: plan}, "scalar subquery carries an inner plan")
	mustDecline(t, &ArraySubqueryExpr{Plan: plan}, "ARRAY(...) carries an inner plan")
	mustDecline(t, &InExpr{Operand: &ColumnRef{Index: 0}, Plan: plan},
		"IN (subquery) carries an inner plan")
	mustDecline(t, &MultiAssignSubqRow{Plan: plan}, "multi-assign row carries an inner plan")
	mustDecline(t, &MultiAssignSubqElem{Row: &MultiAssignSubqRow{Plan: plan}},
		"multi-assign elem reaches its plan through Row")
}

// TestCloneExprShiftIdx_PreservesNonChildFields pins a defect the conversion
// removed as a side effect. The hand-written arms REBUILT three node types
// from a field list instead of copying them, and the field lists were stale:
// BinaryOp dropped ResultType ("non-empty for arithmetic with typed result"),
// UnaryOp likewise, and FuncCall dropped both Variadic and ReturnType. Every
// dropped field is read downstream by the executor's type dispatch, so this
// was a silent type-metadata loss on exactly the conjuncts the NLI hoists.
// shallowCloneExpr copies the whole struct, so it cannot recur.
func TestCloneExprShiftIdx_PreservesNonChildFields(t *testing.T) {
	bin := &BinaryOp{
		Op:         parser.OpAdd,
		Left:       &ColumnRef{Index: 0},
		Right:      &IntegerConst{Value: 1},
		ResultType: "int2",
	}
	if got := mustShift(t, bin, 3).(*BinaryOp).ResultType; got != "int2" {
		t.Errorf("BinaryOp.ResultType = %q, want %q", got, "int2")
	}

	fn := &FuncCall{
		Name:       "myfunc",
		Args:       []Expr{&ColumnRef{Index: 0}},
		Variadic:   true,
		ReturnType: "numeric",
	}
	clFn := mustShift(t, fn, 3).(*FuncCall)
	if !clFn.Variadic || clFn.ReturnType != "numeric" {
		t.Errorf("FuncCall lost fields: Variadic=%t ReturnType=%q", clFn.Variadic, clFn.ReturnType)
	}
	if got := clFn.Args[0].(*ColumnRef).Index; got != 3 {
		t.Errorf("FuncCall arg index = %d, want 3", got)
	}
	if got := fn.Args[0].(*ColumnRef).Index; got != 0 {
		t.Errorf("ORIGINAL FuncCall arg mutated to %d; Args slice was aliased", got)
	}
}

// TestCloneExprShiftIdx_UnresolvedRefIsNotShifted keeps the one guard the old
// arm had that the driver does not supply. Index < 0 marks a ref the binder
// never resolved; adding the outer width to it would turn a recognisably
// invalid position into a plausible one.
func TestCloneExprShiftIdx_UnresolvedRefIsNotShifted(t *testing.T) {
	cl := mustShift(t, &ColumnRef{Index: -1, Name: "unresolved"}, 7).(*ColumnRef)
	if cl.Index != -1 {
		t.Fatalf("unresolved ColumnRef shifted to %d; want -1 preserved", cl.Index)
	}
}

// TestCloneExprShiftIdx_NilIsAdmitted pins the `case nil` the old switch had
// explicitly. splitAnd can hand a nil conjunct through, and the driver's own
// nil handling must agree.
func TestCloneExprShiftIdx_NilIsAdmitted(t *testing.T) {
	cl, ok := cloneExprShiftIdx(nil, 4)
	if !ok || cl != nil {
		t.Fatalf("cloneExprShiftIdx(nil) = (%#v, %t), want (nil, true)", cl, ok)
	}
}
