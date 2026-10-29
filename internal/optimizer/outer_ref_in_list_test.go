package optimizer

import "testing"

// TestEscapingOuterRefInsideInList pins the M0122-0021 wrong-results fix: an
// outer reference that appears only inside an IN list (`mi.id IN (mo.k, 99)`)
// or only as an IN operand is an outer reference. The escape check walked
// with walkExprTree, whose hand list never descends into InExpr's Operand or
// List, so a sublink whose correlation lived there read as uncorrelated; an
// uncorrelated ARRAY sublink is computed once per statement, and on a live
// server every row then got the first row's array ({100} five times where
// PG 18.3 returns {100},{200},{300},{400},{800}).
func TestEscapingOuterRefInsideInList(t *testing.T) {
	outer := func() *OuterColumnRef { return &OuterColumnRef{Level: 1, Index: 0, Name: "k"} }
	local := &ColumnRef{Index: 0, Name: "id"}
	cases := []struct {
		name string
		e    Expr
	}{
		{"outer ref in the IN list", &InExpr{Operand: local, List: []Expr{outer(), &IntegerConst{Value: 99}}}},
		{"outer ref as the IN operand", &InExpr{Operand: outer(), List: []Expr{&IntegerConst{Value: 1}}}},
	}
	for _, tc := range cases {
		if !exprsHaveEscapingOuterRef(1, outerRefReachesPast, tc.e) {
			t.Errorf("%s: escape check missed the outer reference", tc.name)
		}
		filter := &Filter{Child: &Values{}, Predicate: tc.e}
		if !planHasOuterRef(filter) {
			t.Errorf("%s: planHasOuterRef(Filter) missed the outer reference", tc.name)
		}
	}
	if exprsHaveEscapingOuterRef(1, outerRefReachesPast, &InExpr{Operand: local, List: []Expr{&IntegerConst{Value: 1}}}) {
		t.Errorf("an IN list without outer references read as escaping")
	}
}
