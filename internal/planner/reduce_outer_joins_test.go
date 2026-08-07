package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestReduceOuterJoins is the M0128-P4.1 strict-qual demotion matrix. Each case
// verifies that outer joins are demoted to inner only when a strict qual above
// them constrains the nullable side.

func TestReduceOuterJoinsLeftDemotion(t *testing.T) {
	// a LEFT JOIN b WHERE b.x = 5 → the LEFT JOIN becomes INNER.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "b", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with strict WHERE on nullable side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsLeftNoDemotionPreservedSide(t *testing.T) {
	// a LEFT JOIN b WHERE a.x = 5 → no demotion (strict qual on preserved side).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with strict WHERE on preserved side: got %v, want JoinLeft (no demotion)", got)
	}
}

func TestReduceOuterJoinsLeftNoWhere(t *testing.T) {
	// a LEFT JOIN b with no WHERE → no demotion.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}

	reduceOuterJoins(from, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with no WHERE: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsRightDemotion(t *testing.T) {
	// a RIGHT JOIN b WHERE a.x = 5 → RIGHT JOIN becomes INNER
	// (strict qual constrains the left/nulled side).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("RIGHT JOIN with strict WHERE on nullable (left) side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsRightNoDemotion(t *testing.T) {
	// a RIGHT JOIN b WHERE b.x = 5 → no demotion (strict qual on preserved
	// right side).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "b", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinRight {
		t.Errorf("RIGHT JOIN with strict WHERE on preserved side: got %v, want JoinRight", got)
	}
}

func TestReduceOuterJoinsFullDemotionBothSides(t *testing.T) {
	// a FULL JOIN b WHERE a.x = 5 AND b.y = 10 → FULL becomes INNER.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinFull, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op: parser.OpAnd,
		Left: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "a", Column: "x"},
			Right: &parser.IntegerConst{Value: 5},
		},
		Right: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "b", Column: "y"},
			Right: &parser.IntegerConst{Value: 10},
		},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("FULL JOIN with strict WHERE on both sides: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsFullDemotionOneSide(t *testing.T) {
	// a FULL JOIN b WHERE b.y = 10 → FULL becomes LEFT
	// (right side constrained → left is now the only nullable side → LEFT).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinFull, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "b", Column: "y"},
		Right: &parser.IntegerConst{Value: 10},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("FULL JOIN with strict WHERE on right side only: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsIsNotNull(t *testing.T) {
	// a LEFT JOIN b WHERE b.x IS NOT NULL → demote to INNER.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "x"},
		Negated: true, // IS NOT NULL
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with IS NOT NULL on nullable side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsIsNullNoDemotion(t *testing.T) {
	// a LEFT JOIN b WHERE b.x IS NULL → NO demotion.
	// IS NULL does not force the column to be non-null.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "x"},
		Negated: false, // IS NULL
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with IS NULL on nullable side: got %v, want JoinLeft (no demotion)", got)
	}
}

func TestReduceOuterJoinsOrNoDemotion(t *testing.T) {
	// a LEFT JOIN b WHERE b.x = 5 OR c.y = 10 → no demotion.
	// OR is too complex — conservative, no false demotion.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op: parser.OpOr,
		Left: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "b", Column: "x"},
			Right: &parser.IntegerConst{Value: 5},
		},
		Right: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "c", Column: "y"},
			Right: &parser.IntegerConst{Value: 10},
		},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with OR in WHERE: got %v, want JoinLeft (no demotion)", got)
	}
}

func TestReduceOuterJoinsMultiJoinChain(t *testing.T) {
	// a LEFT JOIN b LEFT JOIN c WHERE c.x = 5
	// → only the second (outermost) join demotes.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "c"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "c", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("multi-join: first LEFT JOIN with no constraint: got %v, want JoinLeft", got)
	}
	if got := from[0].Joins[1].Type; got != parser.JoinInner {
		t.Errorf("multi-join: second LEFT JOIN with constraint on nullable side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsLikeNoDemotion(t *testing.T) {
	// a LEFT JOIN b WHERE b.x LIKE 'foo' → no demotion (LIKE is not strict).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpLike,
		Left:  &parser.ColumnRef{Table: "b", Column: "x"},
		Right: &parser.StringConst{Value: "foo"},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with LIKE in WHERE: got %v, want JoinLeft (no demotion)", got)
	}
}

func TestReduceOuterJoinsAliasDemotion(t *testing.T) {
	// a LEFT JOIN b AS b2 WHERE b2.x = 5 → demote using alias.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b", Alias: "b2"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "b2", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with alias qual on nullable side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsNeOperator(t *testing.T) {
	// a LEFT JOIN b WHERE b.x <> 5 → demote (<> is strict).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpNe,
		Left:  &parser.ColumnRef{Table: "b", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with <> on nullable side: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsEmptyFromExprs(t *testing.T) {
	// Empty FromExprs: should not panic.
	reduceOuterJoins(nil, &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "x", Column: "y"},
		Right: &parser.IntegerConst{Value: 1},
	})
}

func TestReduceOuterJoinsInnerUnaffected(t *testing.T) {
	// a INNER JOIN b WHERE b.x = 5 → no change (already inner).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "b", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("INNER JOIN should stay INNER: got %v", got)
	}
}
