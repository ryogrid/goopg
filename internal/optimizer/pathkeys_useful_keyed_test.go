package optimizer

import "testing"

// TestPathkeyUsefulnessKeyedMatchesExprEqual pins slice 32: the usefulness
// methods compare expressions by precomputed / memoised identity keys
// (newClassMember, pathkeyUsefulness.keyOf) instead of calling exprEqual —
// which formats both keys on every comparison — and must decide exactly as
// exprEqual does. Structurally equal expressions at different addresses
// match, distinct ones do not, a nil only matches a nil, and members built
// with and without precomputed keys behave alike.
func TestPathkeyUsefulnessKeyedMatchesExprEqual(t *testing.T) {
	col := func(i int, n string) *ColumnRef { return &ColumnRef{Index: i, Name: n} }
	a1, a2 := col(1, "a"), col(1, "a") // equal, distinct pointers
	b := col(2, "b")
	c := col(3, "c")
	for _, keyed := range []bool{true, false} {
		mk := func(e Expr, id int) classMember {
			if keyed {
				return newClassMember(e, RelSet(1), id)
			}
			return classMember{expr: e, relids: RelSet(1), id: id}
		}
		u := &pathkeyUsefulness{
			ecMembers:     []classMember{mk(a1, 1), mk(b, 1), mk(c, 2)},
			classMembers:  []classMember{mk(a1, 1), mk(b, 1), mk(c, 2)},
			queryPathkeys: []PathKey{{Expr: a1, SortAsc: false}},
			mergeExprs:    []Expr{b},
		}
		for _, tc := range []struct {
			x, y Expr
			want bool
		}{
			{a2, b, true},  // a2 matches a1 by key, a1 and b share class 1
			{a1, a2, true}, // same key
			{a2, c, false}, // different classes
			{c, c, true},
			{col(9, "z"), a1, false}, // z is in no class
		} {
			if got := u.sameClass(tc.x, tc.y); got != tc.want {
				t.Errorf("keyed=%v sameClass(%v, %v) = %v, want %v", keyed, tc.x, tc.y, got, tc.want)
			}
			if got := u.equivalentWithin(tc.x, tc.y); got != tc.want {
				t.Errorf("keyed=%v equivalentWithin(%v, %v) = %v, want %v", keyed, tc.x, tc.y, got, tc.want)
			}
		}
		// rightMergeDirection finds the query key through a structurally equal
		// expression: a1 is ordered DESC in the query, so an ASC key on a2 is
		// against the direction.
		if u.rightMergeDirection(PathKey{Expr: a2, SortAsc: true}) {
			t.Errorf("keyed=%v rightMergeDirection(a2 ASC) = true, want false (query orders a DESC)", keyed)
		}
		for _, p := range [][2]Expr{{a1, a2}, {a1, b}, {nil, nil}, {nil, a1}, {a1, nil}} {
			if got, want := u.equalKeyed(p[0], p[1]), exprEqual(p[0], p[1]); got != want {
				t.Errorf("keyed=%v equalKeyed(%v, %v) = %v, exprEqual says %v", keyed, p[0], p[1], got, want)
			}
		}
	}
}
