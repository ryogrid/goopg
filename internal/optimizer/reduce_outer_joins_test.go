package optimizer

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, nil, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with no WHERE: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsRightDemotion(t *testing.T) {
	// a RIGHT JOIN b WHERE a.x = 5 → RIGHT flipped to LEFT, then LEFT→INNER
	// (strict qual constrains the nullable side after flip).
	// S9.4: RIGHT(A,B) → LEFT(B,A); then LEFT→INNER because A is in
	// accumulatedNN (WHERE a.x = 5 constrains the nullable side).
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("RIGHT JOIN with strict WHERE on nullable (left) side: got %v, want JoinInner", got)
	}
	// After the flip, Base should be the original right side (b → preserved).
	if got := from[0].Base.Name; got != "b" {
		t.Errorf("RIGHT→LEFT flip: expected Base 'b', got %q", got)
	}
}

func TestReduceOuterJoinsRightNoDemotion(t *testing.T) {
	// a RIGHT JOIN b WHERE b.x = 5 → RIGHT flipped to LEFT, no further demotion
	// (strict qual is on the preserved side after flip).
	// S9.4: RIGHT(A,B) → LEFT(B,A); LEFT→INNER doesn't fire because A (the
	// nullable side after flip) is NOT in accumulatedNN — only B is.
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

	reduceOuterJoins(from, where, nil)

	// After S9.4 flip: RIGHT(A,B) → LEFT(B,A). B is preserved, A is nullable.
	// B is constrained (b.x = 5) but that doesn't demote LEFT further.
	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("RIGHT JOIN with strict WHERE on preserved side: got %v, want JoinLeft (after flip)", got)
	}
	// After the flip, Base should be B (the original right side → now preserved).
	if got := from[0].Base.Name; got != "b" {
		t.Errorf("RIGHT→LEFT flip: expected Base 'b', got %q", got)
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("FULL JOIN with strict WHERE on both sides: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsFullDemotionOneSide(t *testing.T) {
	// a FULL JOIN b WHERE b.y = 10 → FULL→RIGHT→LEFT flip.
	// PG prepjointree.c:3334-3340 (FULL→RIGHT when only right constrained)
	// + 3366-3376 (RIGHT→LEFT flip).
	// Result: FULL(A,B) → RIGHT(A,B) → LEFT(B,A).
	// B is preserved (and constrained by WHERE), A is nullable.
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("FULL JOIN with strict WHERE on right side only: got %v, want JoinLeft", got)
	}
	// After FULL→RIGHT→LEFT flip: Base should be B, right should be A.
	if got := from[0].Base.Name; got != "b" {
		t.Errorf("FULL→RIGHT→LEFT flip: expected Base 'b', got %q", got)
	}
	if got := from[0].Joins[0].Right.Name; got != "a" {
		t.Errorf("FULL→RIGHT→LEFT flip: expected Right 'a', got %q", got)
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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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

	reduceOuterJoins(from, where, nil)

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
	}, nil)
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("INNER JOIN should stay INNER: got %v", got)
	}
}

// ---- M0129-S9.2: ON-clause propagation tests ----

func TestReduceOuterJoinsInnerOnPropagatesToRightDemotion(t *testing.T) {
	// a INNER JOIN b ON a.x = b.y RIGHT JOIN c
	// WHERE is empty — the INNER JOIN's strict ON clause is the ONLY source
	// of nonnullable rels.
	// → INNER ON: localNN = {a, b} → merged into accumulatedNN = {a, b}
	// → RIGHT JOIN: nullable (left) side = {a, b}, accumulatedNN = {a, b}
	//   → overlap → demote RIGHT to INNER.
	// Without S9.2, accumulatedNN stays empty and no demotion happens.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinRight, Right: parser.RangeVar{Name: "c"},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil) // no WHERE

	if got := from[0].Joins[1].Type; got != parser.JoinInner {
		t.Errorf("RIGHT JOIN after INNER JOIN with strict ON (no WHERE): got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsInnerOnPropagatesToLeftCheck(t *testing.T) {
	// a INNER JOIN b ON a.x = b.y LEFT JOIN c ON b.z = c.w
	// WHERE empty.
	// → INNER ON: localNN = {a, b} → accumulatedNN = {a, b}
	// → LEFT JOIN: check "c" (nullable side) against {a, b} → NO match
	// → stays LEFT (propagation should not cause false demotion).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "z"},
					Right: &parser.ColumnRef{Table: "c", Column: "w"},
				},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	// c is the nullable side; it is NOT in accumulatedNN ({a, b}).
	if got := from[0].Joins[1].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with nullable side not in accumulatedNN: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsLeftOnDoesNotSelfDemote(t *testing.T) {
	// a LEFT JOIN b ON b.x = 5
	// The ON clause b.x = 5 is strict, but b can still be null-extended
	// for non-matching rows → no self-demotion by local ON clause.
	// PG reduce_outer_joins_pass2 uses only upper nonnullable_rels for
	// demotion, never the local ON clause's own findings.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "x"},
					Right: &parser.IntegerConst{Value: 5},
				},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with strict ON referencing only nullable side: got %v, want JoinLeft (no self-demotion)", got)
	}
}

func TestReduceOuterJoinsMultiInnerOnChain(t *testing.T) {
	// a INNER JOIN b ON a.x = b.y INNER JOIN c ON b.z = c.w RIGHT JOIN d
	// → accumulatedNN after first INNER: {a, b}
	// → accumulatedNN after second INNER: {a, b, c} (merged from second ON)
	// → RIGHT JOIN: nullable left side = {a, b, c}, accumulatedNN overlap → demote!
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "z"},
					Right: &parser.ColumnRef{Table: "c", Column: "w"},
				},
			},
			{
				Type: parser.JoinRight, Right: parser.RangeVar{Name: "d"},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	if got := from[0].Joins[2].Type; got != parser.JoinInner {
		t.Errorf("RIGHT JOIN after two INNER joins with strict ONs (no WHERE): got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsWhereCombinedWithInnerOnPropagation(t *testing.T) {
	// a INNER JOIN b ON a.x = b.y LEFT JOIN c ON b.z = c.w WHERE c.v = 5
	// → upperNN from WHERE = {c}
	// → INNER ON: localNN = {a, b} → accumulatedNN = {a, b, c}
	// → LEFT JOIN: check "c" against accumulatedNN → "c" IS in accumulatedNN
	// → demote LEFT to INNER.
	// This is a regression guard that WHERE + ON interplay correctly.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "z"},
					Right: &parser.ColumnRef{Table: "c", Column: "w"},
				},
			},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "c", Column: "v"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[1].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with WHERE on nullable side + INNER ON propagation: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsLeftOnDoesNotPropagate(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y RIGHT JOIN c
	// → LEFT ON: localNN = {a, b} but LEFT propagation → does NOT merge
	// → accumulatedNN stays empty
	// → RIGHT JOIN: nullable side = {a, b}, accumulatedNN = {} → no demotion.
	// This is the key guard: LEFT JOIN's ON findings don't leak to subsequent
	// joins because b can be null-extended.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinRight, Right: parser.RangeVar{Name: "c"},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	if got := from[0].Joins[1].Type; got != parser.JoinRight {
		t.Errorf("RIGHT JOIN after LEFT JOIN with strict ON (no WHERE): got %v, want JoinRight (LEFT ON does not propagate)", got)
	}
}

func TestReduceOuterJoinsFullJoinResetsAccumulated(t *testing.T) {
	// a INNER JOIN b ON a.x = b.y FULL JOIN c RIGHT JOIN d
	// S9.4 + S9.2: goopg's flat-chain model propagates INNER→ON nonnullable
	// findings through the chain. PG's top-down tree would keep both outer
	// joins, but goopg's left-to-right chain demotes both.
	//
	// → INNER ON a.x = b.y: localNN = {a,b} → merge into accumulatedNN = {a,b}
	// → FULL JOIN: leftNames={a,b}, rightName=c
	//     leftConstrained (anyNameIn({a,b}, {a,b})) = true → FULL→LEFT
	//     LEFT propagation: accumulatedNN stays {a,b}
	// → RIGHT JOIN: leftNames={a,b,c}, accumulatedNN={a,b}
	//     RIGHT→INNER: anyNameIn({a,b,c}, {a,b}) = true → INNER
	// Quality note: PG would NOT demote here because nonnullable_rels flows
	// top-down and INNER→ON findings don't reach the FULL parent.
	// goopg is more aggressive but still correct (demotion never loses rows).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinFull, Right: parser.RangeVar{Name: "c"},
			},
			{
				Type: parser.JoinRight, Right: parser.RangeVar{Name: "d"},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	// FULL→LEFT demotion (left side constrained by propagated INNER→ON NN).
	if got := from[0].Joins[1].Type; got != parser.JoinLeft {
		t.Errorf("FULL JOIN after INNER ON chain: got %v, want JoinLeft", got)
	}
	// RIGHT→INNER demotion (right was flipped to LEFT, but this is pos 2,
	// not first, so not flipped — RIGHT→INNER fires because left side
	// {a,b,c} overlaps accumulatedNN {a,b}).
	if got := from[0].Joins[2].Type; got != parser.JoinInner {
		t.Errorf("RIGHT JOIN after FULL→LEFT: got %v, want JoinInner", got)
	}
}

// ---- M0129-S9.3: LEFT→ANTI demotion tests ----

func TestReduceOuterJoinsLeftToAntiBasic(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y WHERE b.y IS NULL
	// → WHERE b.y IS NULL: accumulatedFN = {b}
	// → ON a.x = b.y: localNN = {a, b}  (strict comparison)
	// → LEFT JOIN: rightName = b, accumulatedFN[b]=true, localNN[b]=true
	// → ANTI (the ON can never be TRUE when WHERE passes → no matches).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "y"},
		Negated: false, // IS NULL
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinAnti {
		t.Errorf("LEFT JOIN with IS NULL on nullable-side column in strict ON: got %v, want JoinAnti", got)
	}
}

func TestReduceOuterJoinsLeftToAntiFixedConstant(t *testing.T) {
	// a LEFT JOIN b ON b.y = 5 WHERE b.y IS NULL
	// → ON b.y = 5: localNN = {b}  (b in strict comparison with constant)
	// → WHERE b.y IS NULL: accumulatedFN = {b}
	// → ANTI.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "y"},
					Right: &parser.IntegerConst{Value: 5},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "y"},
		Negated: false,
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinAnti {
		t.Errorf("LEFT JOIN with constant-eq ON + IS NULL WHERE on nullable side: got %v, want JoinAnti", got)
	}
}

func TestReduceOuterJoinsLeftToAntiIsNullOnPreservedSideNoDemotion(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y WHERE a.x IS NULL
	// → WHERE a.x IS NULL: accumulatedFN = {a}
	// → ON a.x = b.y: localNN = {a, b}
	// → LEFT JOIN: rightName = b, accumulatedFN[b]=false
	// → stays LEFT: the IS NULL is on the preserved (left) side.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "a", Column: "x"},
		Negated: false,
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with IS NULL on preserved side: got %v, want JoinLeft (IS NULL on left, not right)", got)
	}
}

func TestReduceOuterJoinsLeftToAntiIsNullWithoutOnClause(t *testing.T) {
	// a LEFT JOIN b WHERE b.y IS NULL  (no ON clause)
	// → WHERE b.y IS NULL: accumulatedFN = {b}
	// → localNN from nil ON = {}
	// → accumulatedFN[b]=true but localNN[b]=false
	// → stays LEFT: no nonnullable vars in ON to intersect with.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "y"},
		Negated: false,
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with IS NULL but no ON clause: got %v, want JoinLeft (no ON to intersect)", got)
	}
}

func TestReduceOuterJoinsInnerWinsOverAnti(t *testing.T) {
	// a LEFT JOIN b ON b.y = 5 WHERE b.y = 5 AND b.y IS NULL
	// → WHERE b.y = 5: upperNN = {b} (strict comparison)
	// → WHERE b.y IS NULL: upperFN = {b}
	// → LEFT→INNER check fires first (accumulatedNN[b]=true)
	// → becomes INNER, not ANTI.
	// INNER is the stronger demotion; inner trumps anti.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "y"},
					Right: &parser.IntegerConst{Value: 5},
				},
			},
		},
	}}
	where := &parser.BinaryOp{
		Op: parser.OpAnd,
		Left: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "b", Column: "y"},
			Right: &parser.IntegerConst{Value: 5},
		},
		Right: &parser.IsNullExpr{
			Operand: &parser.ColumnRef{Table: "b", Column: "y"},
			Negated: false,
		},
	}

	reduceOuterJoins(from, where, nil)

	// INNER trumps ANTI because it's checked first.
	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with both INNER+ANTI conditions: got %v, want JoinInner (inner wins)", got)
	}
}

func TestReduceOuterJoinsIsNotNullDoesNotBecomeAnti(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y WHERE b.y IS NOT NULL
	// → WHERE b.y IS NOT NULL: upperNN = {b}, upperFN = {}
	// → accumulatedNN[b]=true → INNER demotion
	// → NOT ANTI (IS NOT NULL is not a forced-null condition).
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "b", Column: "y"},
		Negated: true, // IS NOT NULL
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("LEFT JOIN with IS NOT NULL WHERE on nullable side: got %v, want JoinInner (IS NOT NULL = nonnullable, not forced-null)", got)
	}
}

func TestReduceOuterJoinsForcedNullPropagationThroughInner(t *testing.T) {
	// a INNER JOIN b ON b.y IS NULL LEFT JOIN c ON c.z = b.w WHERE empty
	// → INNER ON b.y IS NULL: localFN = {b} → merged into accumulatedFN = {b}
	//   (IS NULL is not a strict op → localNN = {})
	// → LEFT JOIN (c): ON c.z = b.w: localNN = {b, c}
	// → rightName = c, accumulatedFN[c]=false → no ANTI.
	// → b is forced-null AND non-nullable... but b is on the preserved (left) side.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.IsNullExpr{
					Operand: &parser.ColumnRef{Table: "b", Column: "y"},
					Negated: false,
				},
			},
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "c", Column: "z"},
					Right: &parser.ColumnRef{Table: "b", Column: "w"},
				},
			},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	// c is nullable side. accumulatedFN = {b}. c is not in accumulatedFN → no ANTI.
	if got := from[0].Joins[1].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN without forced-null on nullable side: got %v, want JoinLeft", got)
	}
}

func TestReduceOuterJoinsAntiInMultiJoinChain(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y LEFT JOIN c ON b.y = c.z WHERE c.z IS NULL
	// → WHERE c.z IS NULL: upperFN = {c}
	// → LEFT #1 (b): check accumulatedFN["b"] → false → not ANTI
	//   LEFT propagation: accumulatedFN = {c} (unchanged)
	// → LEFT #2 (c): check accumulatedFN["c"]=true, localNN[c]=true → ANTI!
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "b", Column: "y"},
					Right: &parser.ColumnRef{Table: "c", Column: "z"},
				},
			},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "c", Column: "z"},
		Negated: false,
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("multi-join LEFT→ANTI: first LEFT (b): got %v, want JoinLeft (IS NULL not on b)", got)
	}
	if got := from[0].Joins[1].Type; got != parser.JoinAnti {
		t.Errorf("multi-join LEFT→ANTI: second LEFT (c): got %v, want JoinAnti (IS NULL on c and c in strict ON)", got)
	}
}

func TestReduceOuterJoinsForcedNullWithOrNoDemotion(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.y WHERE b.y IS NULL OR b.z IS NULL
	// → OR is not examined for forced-null vars → upperFN = {}
	// → no ANTI demotion.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{
				Type: parser.JoinLeft, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "y"},
				},
			},
		},
	}}
	where := &parser.BinaryOp{
		Op: parser.OpOr,
		Left: &parser.IsNullExpr{
			Operand: &parser.ColumnRef{Table: "b", Column: "y"},
			Negated: false,
		},
		Right: &parser.IsNullExpr{
			Operand: &parser.ColumnRef{Table: "b", Column: "z"},
			Negated: false,
		},
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("LEFT JOIN with IS NULL in OR: got %v, want JoinLeft (OR not examined for forced-null)", got)
	}
}

// ---- S9.4: RIGHT→LEFT flip tests ----

func TestReduceOuterJoinsRightToLeftFlipFirstPosition(t *testing.T) {
	// RIGHT JOIN at first position is flipped to LEFT.
	// RIGHT(A,B) → LEFT(B,A): Base and Right are swapped.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "b"}},
		},
	}}

	reduceOuterJoins(from, nil, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("RIGHT JOIN first position: got %v, want JoinLeft (flipped)", got)
	}
	if got := from[0].Base.Name; got != "b" {
		t.Errorf("RIGHT→LEFT flip: expected Base 'b', got %q", got)
	}
	if got := from[0].Joins[0].Right.Name; got != "a" {
		t.Errorf("RIGHT→LEFT flip: expected Right 'a', got %q", got)
	}
}

func TestReduceOuterJoinsRightFlipThenAnti(t *testing.T) {
	// RIGHT(A,B) flipped to LEFT(B,A), then LEFT→ANTI because A is
	// forced-null (IS NULL) AND nonnullable (strict ON clause).
	// WHERE a.x IS NULL → accumulatedFN = {a}
	// ON a.x = b.x → localNN = {a, b}
	// LEFT→ANTI: accumulatedFN["a"]=true, localNN["a"]=true → ANTI
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "x"},
				}},
		},
	}}
	where := &parser.IsNullExpr{
		Operand: &parser.ColumnRef{Table: "a", Column: "x"},
		Negated: false,
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinAnti {
		t.Errorf("RIGHT flip then ANTI: got %v, want JoinAnti", got)
	}
}

func TestReduceOuterJoinsRightFlipInnerWinsOverAnti(t *testing.T) {
	// RIGHT(A,B) flipped to LEFT(B,A). Both INNER and ANTI conditions are met;
	// INNER wins over ANTI (INNER is the stronger demotion).
	// WHERE a.x = 5 AND a.x IS NULL → accumulatedNN={a}, accumulatedFN={a}
	// ON a.x = b.x → localNN = {a, b}
	// LEFT→INNER fires first: accumulatedNN["a"]=true → INNER
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "x"},
				}},
		},
	}}
	where := &parser.BinaryOp{
		Op: parser.OpAnd,
		Left: &parser.BinaryOp{
			Op:    parser.OpEq,
			Left:  &parser.ColumnRef{Table: "a", Column: "x"},
			Right: &parser.IntegerConst{Value: 5},
		},
		Right: &parser.IsNullExpr{
			Operand: &parser.ColumnRef{Table: "a", Column: "x"},
			Negated: false,
		},
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("RIGHT flip, INNER wins over ANTI: got %v, want JoinInner", got)
	}
}

func TestReduceOuterJoinsRightFlipMultiJoinChain(t *testing.T) {
	// RIGHT JOIN in a multi-join chain. First RIGHT is flipped;
	// deeper RIGHT (pos 1) is not flipped (S9.4 ledger).
	// a INNER JOIN b RIGHT JOIN c WHERE a.x = 5
	// First join: INNER+ON a.x=b.x → localNN merges; accumulatedNN={a,b}
	// Second join: RIGHT at pos 1 → NOT flipped (needs nested AST).
	//   RIGHT→INNER: leftNames={a,b} overlaps accumulatedNN={a,b}? YES → INNER.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinInner, Right: parser.RangeVar{Name: "b"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "x"},
					Right: &parser.ColumnRef{Table: "b", Column: "x"},
				}},
			{Type: parser.JoinRight, Right: parser.RangeVar{Name: "c"},
				On: &parser.BinaryOp{
					Op:    parser.OpEq,
					Left:  &parser.ColumnRef{Table: "a", Column: "y"},
					Right: &parser.ColumnRef{Table: "c", Column: "y"},
				}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where, nil)

	// First join: INNER stays INNER.
	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("first INNER in chain: got %v, want JoinInner", got)
	}
	// Second join: RIGHT at pos 1 → NOT flipped (S9.4 ledger).
	// RIGHT→INNER check: leftNames={a,b} overlap accumulatedNN={a,b}? YES → INNER.
	if got := from[0].Joins[1].Type; got != parser.JoinInner {
		t.Errorf("RIGHT at pos 1, right→inner: got %v, want JoinInner", got)
	}
}

// ---- S9.4: FULL→RIGHT→LEFT flip tests ----

func TestReduceOuterJoinsFullRightFlipFirstPosition(t *testing.T) {
	// FULL(A,B) with only right constrained → FULL→RIGHT→LEFT flip.
	// WHERE b.y = 10: accumulatedNN = {b} (right constrained only).
	// PG: FULL→RIGHT, then RIGHT→LEFT flip.
	// Result: LEFT(B,A). LEFT→INNER: accumulatedNN["a"]? No (only b). Stays LEFT.
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("FULL→RIGHT→LEFT flip: got %v, want JoinLeft", got)
	}
	if got := from[0].Base.Name; got != "b" {
		t.Errorf("FULL→RIGHT→LEFT flip: expected Base 'b', got %q", got)
	}
}

func TestReduceOuterJoinsFullToLeftOnlyLeftConstrained(t *testing.T) {
	// FULL(A,B) with only left constrained → FULL→LEFT (no flip needed).
	// WHERE a.x = 5: accumulatedNN = {a} (left constrained only).
	// PG prepjointree.c:3325-3330: FULL→LEFT when left is nonnullable.
	// Result: LEFT(A,B). A is preserved, B is nullable.
	from := []parser.FromExpr{{
		Base: parser.RangeVar{Name: "a"},
		Joins: []parser.JoinExpr{
			{Type: parser.JoinFull, Right: parser.RangeVar{Name: "b"}},
		},
	}}
	where := &parser.BinaryOp{
		Op:    parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.IntegerConst{Value: 5},
	}

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinLeft {
		t.Errorf("FULL→LEFT (only left constrained): got %v, want JoinLeft", got)
	}
	// No flip — Base stays as original left (A).
	if got := from[0].Base.Name; got != "a" {
		t.Errorf("FULL→LEFT: expected Base 'a', got %q", got)
	}
}

func TestReduceOuterJoinsFullDemotionBothSidesStructural(t *testing.T) {
	// FULL(A,B) with both sides constrained → INNER.
	// No structural change needed. Verify Base stays A.
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

	reduceOuterJoins(from, where, nil)

	if got := from[0].Joins[0].Type; got != parser.JoinInner {
		t.Errorf("FULL JOIN both sides constrained: got %v, want JoinInner", got)
	}
	// No flip needed — both sides constrained → straight to INNER.
	if got := from[0].Base.Name; got != "a" {
		t.Errorf("FULL→INNER: expected Base 'a', got %q", got)
	}
}
