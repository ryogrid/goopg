package planner

import "testing"

// TestShiftColumnRefsByInExpr pins the M0097-0023 virtual-table LEFT JOIN
// crash: shiftColumnRefsBy must recurse into InExpr just like its sibling
// classifier walkColumnRefs does.
//
// Background: for a LEFT JOIN, planFromItem pushes inner-only ON conjuncts
// to a Filter on the inner plan and rebases their ColumnRef indices by
// -leftWidth (planner.go, the joinType == JoinTypeLeft branch). The side
// classifier (classifyConjunctSide -> walkColumnRefs) recurses into the
// literal-list form of InExpr, so `col IN (a, b, ...)` is correctly tagged
// inner-only and pushed down. But shiftColumnRefsBy formerly had no InExpr
// case, so the operand/list ColumnRef indices were left at their
// outer-cumulative value. At execution the inner Filter row is only as wide
// as the inner relation, so an unshifted index addressed one past the end
// and Slot.Get panicked "index out of range".
//
// The concrete trigger is psql `\d`'s join:
//
//	pg_index i LEFT JOIN pg_constraint con
//	  ON (... AND con.contype IN ('p','u','x'))
//
// where con.contype resolves to absolute index leftWidth(22)+ordinal(3)=25
// and the inner row width is pg_constraint's 25 columns -> index 25 in a
// length-25 slot.
func TestShiftColumnRefsByInExpr(t *testing.T) {
	const leftWidth = 22

	// `con.contype IN ('p','u','x')` resolved against the merged outer
	// schema: contype's absolute index is leftWidth + its inner ordinal (3).
	operand := &ColumnRef{Index: leftWidth + 3, Name: "contype"}
	in := &InExpr{
		Operand: operand,
		List: []Expr{
			&StringConst{Value: "p"},
			&StringConst{Value: "u"},
			&StringConst{Value: "x"},
		},
	}

	shifted := shiftColumnRefsBy(in, -leftWidth)

	got, ok := shifted.(*InExpr)
	if !ok {
		t.Fatalf("shiftColumnRefsBy returned %T, want *InExpr", shifted)
	}
	gotOperand, ok := got.Operand.(*ColumnRef)
	if !ok {
		t.Fatalf("operand is %T, want *ColumnRef", got.Operand)
	}
	if gotOperand.Index != 3 {
		t.Errorf("operand index = %d, want 3 (inner-relative ordinal)", gotOperand.Index)
	}
	// The original expression must be left untouched (shiftColumnRefsBy
	// returns a rewritten copy; callers still hold the pre-shift tree).
	if operand.Index != leftWidth+3 {
		t.Errorf("original operand index mutated to %d, want %d", operand.Index, leftWidth+3)
	}
	if len(got.List) != 3 {
		t.Fatalf("shifted list len = %d, want 3", len(got.List))
	}
}

// TestShiftColumnRefsByInExprNested guards the common shape where the IN is
// one conjunct of a larger AND/comparison tree: the rewriter must descend
// through BinaryOp into the nested InExpr.
func TestShiftColumnRefsByInExprNested(t *testing.T) {
	const leftWidth = 10

	// (col5 = $outer) AND (col7 IN (1, 2))  — only the inner-relative
	// indices matter here; both refs sit on the inner side.
	expr := &BinaryOp{
		Op:   0, // operator value irrelevant for the rewrite
		Left: &ColumnRef{Index: leftWidth + 5, Name: "a"},
		Right: &InExpr{
			Operand: &ColumnRef{Index: leftWidth + 7, Name: "b"},
			List:    []Expr{&StringConst{Value: "1"}},
		},
	}

	shifted := shiftColumnRefsBy(expr, -leftWidth).(*BinaryOp)

	if l := shifted.Left.(*ColumnRef).Index; l != 5 {
		t.Errorf("left ColumnRef index = %d, want 5", l)
	}
	inOperand := shifted.Right.(*InExpr).Operand.(*ColumnRef)
	if inOperand.Index != 7 {
		t.Errorf("nested IN operand index = %d, want 7", inOperand.Index)
	}
}

// TestShiftColumnRefsByIsNullExpr pins the pg_amcheck exclude-pattern
// anti-join crash (M0110-0003 residual #2): shiftColumnRefsBy must recurse
// into IsNullExpr just like its sibling classifier walkColumnRefs now does.
//
// pg_amcheck's toast/index/toast_index exclusion CTEs build the shape
//
//	... LEFT OUTER JOIN exclude_pat ep
//	    ON ('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)
//	   AND (t.relname  ~ ep.rel_regex OR ep.rel_regex IS NULL)
//	   AND ep.heap_only
//	  WHERE ep.pattern_id IS NULL
//
// where exclude_pat is a 5-column VALUES build relation. The conjunct
// `('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)` references only the
// inner (right) side, so classifyConjunctSide tags it sideRight and it is
// pushed to a Filter on the inner plan and rebased by -leftWidth. The `~`
// BinaryOp operand shifted correctly, but the `ep.nsp_regex IS NULL`
// operand sat inside an IsNullExpr that shiftColumnRefsBy did not recurse
// into, so it kept its outer-cumulative index (43) and addressed past the
// 5-wide inner row -> "index out of range [43] with length 5" panic in
// MaterializedSlot.Get at join build time.
func TestShiftColumnRefsByIsNullExpr(t *testing.T) {
	const leftWidth = 42 // width of (pg_class t JOIN relation r)

	// nsp_regex is inner ordinal 1; absolute index leftWidth+1 = 43.
	// `('pg_toast' ~ ep.nsp_regex OR ep.nsp_regex IS NULL)`
	expr := &BinaryOp{
		Op: 0, // OR — value irrelevant for the rewrite
		Left: &BinaryOp{
			Op:    0, // ~ regex match
			Left:  &StringConst{Value: "pg_toast"},
			Right: &ColumnRef{Index: leftWidth + 1, Name: "nsp_regex"},
		},
		Right: &IsNullExpr{
			Operand: &ColumnRef{Index: leftWidth + 1, Name: "nsp_regex"},
		},
	}

	// Both refs must classify the conjunct as inner-only (sideRight).
	if side := classifyConjunctSide(expr, leftWidth, leftWidth+5); side != sideRight {
		t.Fatalf("classifyConjunctSide = %v, want sideRight (%v)", side, sideRight)
	}

	shifted := shiftColumnRefsBy(expr, -leftWidth).(*BinaryOp)

	matchRef := shifted.Left.(*BinaryOp).Right.(*ColumnRef)
	if matchRef.Index != 1 {
		t.Errorf("regex-match ColumnRef index = %d, want 1", matchRef.Index)
	}
	isNullRef := shifted.Right.(*IsNullExpr).Operand.(*ColumnRef)
	if isNullRef.Index != 1 {
		t.Errorf("IS NULL operand index = %d, want 1 (was unshifted before the fix)", isNullRef.Index)
	}
}
