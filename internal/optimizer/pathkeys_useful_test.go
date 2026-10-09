package optimizer

import "testing"

// TestTruncateUselessPathkeys pins M0146-0005n's truncate_useless_pathkeys
// port: a join path keeps the longer of the merge-useful prefix and the
// ORDER BY prefix, and nothing when neither applies.
func TestTruncateUselessPathkeys(t *testing.T) {
	a := &ColumnRef{Index: 0, Name: "a"}
	b := &ColumnRef{Index: 1, Name: "b"}
	c := &ColumnRef{Index: 2, Name: "c"}
	keys := []PathKey{{Expr: a, SortAsc: true}, {Expr: b, SortAsc: true}, {Expr: c, SortAsc: true}}

	if got := truncateUselessPathkeys(nil, keys); len(got) != 3 {
		t.Fatalf("nil usefulness must keep the keys whole, got %d", len(got))
	}
	if got := truncateUselessPathkeys(&pathkeyUsefulness{}, keys); got != nil {
		t.Fatalf("nothing useful must truncate to nil, got %d keys", len(got))
	}
	merge := &pathkeyUsefulness{mergeExprs: []Expr{a}}
	if got := truncateUselessPathkeys(merge, keys); len(got) != 1 {
		t.Fatalf("merge-useful prefix: got %d keys, want 1", len(got))
	}
	// A merge-useless first key stops the prefix even if a later one is useful.
	if got := truncateUselessPathkeys(&pathkeyUsefulness{mergeExprs: []Expr{b}}, keys); got != nil {
		t.Fatalf("prefix must stop at the first useless key, got %d", len(got))
	}
	ordered := &pathkeyUsefulness{mergeExprs: []Expr{a}, queryPathkeys: keys[:2]}
	if got := truncateUselessPathkeys(ordered, keys); len(got) != 2 {
		t.Fatalf("ORDER BY prefix longer than the merge prefix: got %d keys, want 2", len(got))
	}
	// right_merge_direction: without query pathkeys only ascending keys merge.
	desc := []PathKey{{Expr: a, SortAsc: false}}
	if got := truncateUselessPathkeys(merge, desc); got != nil {
		t.Fatalf("a descending key is not merge-useful without a query order, got %d", len(got))
	}
}

// TestPathkeyUsefulnessForCrossingClass: an equivalence class with a member
// outside the joinrel makes its inside members merge-useful; a class wholly
// inside the joinrel does not.
func TestPathkeyUsefulnessForCrossingClass(t *testing.T) {
	a := &ColumnRef{Index: 0, Name: "a"}
	b := &ColumnRef{Index: 1, Name: "b"}
	c := &ColumnRef{Index: 2, Name: "c"}
	s := &searchCtx{clauses: &restrictInfoList{
		nclasses: 1,
		all: []*restrictInfo{
			{isEquijoin: true, leftKey: a, rightKey: b, leftRelids: relsetOf(0), rightRelids: relsetOf(1), ecID: 0},
			{isEquijoin: true, leftKey: b, rightKey: c, leftRelids: relsetOf(1), rightRelids: relsetOf(2), ecID: 0},
		},
	}}
	u := s.pathkeyUsefulnessFor(relsetOf(0, 1))
	if n := u.usefulForMerging([]PathKey{{Expr: a, SortAsc: true}}); n != 1 {
		t.Fatalf("a's class reaches rel 2, so a is merge-useful: got %d", n)
	}
	whole := s.pathkeyUsefulnessFor(relsetOf(0, 1, 2))
	if n := whole.usefulForMerging([]PathKey{{Expr: a, SortAsc: true}}); n != 0 {
		t.Fatalf("a class wholly inside the joinrel is not merge-useful: got %d", n)
	}
}
