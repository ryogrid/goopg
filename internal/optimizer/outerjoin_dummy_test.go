package optimizer

import "testing"

// TestConstantFalseOuterJoinDummiesNullableSide pins M0146-0129: PG's
// populate_joinrel_with_paths marks the nullable input of a LEFT join dummy
// when the join's restriction list is constant false (`t LEFT JOIN u ON
// false` plans `Join Filter: false` over `Result  One-Time Filter: false`).
// A FULL join preserves both sides and an inner join is not touched here.
func TestConstantFalseOuterJoinDummiesNullableSide(t *testing.T) {
	scan := func(name string) *SeqScan { return &SeqScan{schema: Schema{{Name: name}}} }
	falseQ := &BooleanConst{Value: false}
	mk := func(jt JoinType, pred Expr) *Join {
		return &Join{Type: jt, Left: scan("a"), Right: scan("b"), Predicate: pred}
	}

	left := mk(JoinTypeLeft, combineAnd([]Expr{&ColumnRef{Name: "a"}, falseQ}))
	dummyConstantFalseOuterJoinSides(left)
	if !isDummyRelNode(left.Right) || isDummyRelNode(left.Left) {
		t.Fatalf("LEFT JOIN ON (a AND false): right %T, left %T — want only the nullable right side dummy", left.Right, left.Left)
	}
	if got := left.Right.Output(); len(got) != 1 || got[0].Name != "b" {
		t.Errorf("dummy side emits %v, want the replaced side's schema", got)
	}

	right := mk(JoinTypeRight, &NullConst{})
	dummyConstantFalseOuterJoinSides(right)
	if !isDummyRelNode(right.Left) || isDummyRelNode(right.Right) {
		t.Errorf("RIGHT JOIN ON NULL: want only the nullable left side dummy")
	}

	for _, j := range []*Join{mk(JoinTypeFull, falseQ), mk(JoinTypeInner, falseQ), mk(JoinTypeLeft, &ColumnRef{Name: "a"})} {
		dummyConstantFalseOuterJoinSides(j)
		if isDummyRelNode(j.Left) || isDummyRelNode(j.Right) {
			t.Errorf("join type %v pred %T: a side was made dummy", j.Type, j.Predicate)
		}
	}

	// Nested: the walk reaches a LEFT join below another node.
	inner := mk(JoinTypeLeft, falseQ)
	top := &Filter{Child: inner, Predicate: &ColumnRef{Name: "a"}}
	dummyConstantFalseOuterJoinSides(top)
	if !isDummyRelNode(inner.Right) {
		t.Error("nested LEFT JOIN ON false was not reached")
	}
}
