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

// TestTruncateUselessPathkeysGroupingAndDistinct pins M0146-0223's
// pathkeys_useful_for_grouping / _for_distinct: a join ordered on a group key
// that is not the leading one keeps that key, matched by equivalence class
// (TPC-DS Q39's inv ⋈ item merge join ordered on inv_item_sk under
// GROUP BY w_warehouse_sk, i_item_sk, d_moy); a non-member stops the count,
// and the direction must agree.
func TestTruncateUselessPathkeysGroupingAndDistinct(t *testing.T) {
	invItem := &ColumnRef{Index: 0, Name: "inv_item_sk"}
	iItem := &ColumnRef{Index: 1, Name: "i_item_sk"}
	wh := &ColumnRef{Index: 2, Name: "w_warehouse_sk"}
	moy := &ColumnRef{Index: 3, Name: "d_moy"}
	other := &ColumnRef{Index: 4, Name: "x"}
	s := &searchCtx{
		clauses: &restrictInfoList{
			nclasses: 1,
			all: []*restrictInfo{
				{isEquijoin: true, leftKey: invItem, rightKey: iItem, leftRelids: relsetOf(0), rightRelids: relsetOf(1), ecID: 0},
			},
		},
		groupPathkeys: []PathKey{{Expr: wh, SortAsc: true}, {Expr: iItem, SortAsc: true}, {Expr: moy, SortAsc: true}},
	}
	u := s.pathkeyUsefulnessFor(relsetOf(0, 1))
	keys := []PathKey{{Expr: invItem, SortAsc: true}, {Expr: moy, SortAsc: true}, {Expr: other, SortAsc: true}}
	if got := truncateUselessPathkeys(u, keys); len(got) != 2 {
		t.Fatalf("group members inv_item_sk (by class), d_moy: got %d keys, want 2", len(got))
	}
	// The class is matched wherever it is enforced: a rel holding only
	// inventory still keeps its inv_item_sk order for the group key i_item_sk.
	if got := truncateUselessPathkeys(s.pathkeyUsefulnessFor(relsetOf(0, 2)), keys[:1]); len(got) != 1 {
		t.Fatalf("class membership outside the rel: got %d keys, want 1", len(got))
	}
	if got := truncateUselessPathkeys(u, []PathKey{{Expr: other, SortAsc: true}, {Expr: moy, SortAsc: true}}); got != nil {
		t.Fatalf("a leading non-member stops the count, got %d keys", len(got))
	}
	if got := truncateUselessPathkeys(u, []PathKey{{Expr: moy, SortAsc: false}}); got != nil {
		t.Fatalf("a descending key is not a member of an ascending group list, got %d keys", len(got))
	}
	d := &pathkeyUsefulness{distinctPathkeys: []PathKey{{Expr: other, SortAsc: true}, {Expr: moy, SortAsc: true}}}
	if got := truncateUselessPathkeys(d, []PathKey{{Expr: moy, SortAsc: true}, {Expr: other, SortAsc: true}}); len(got) != 2 {
		t.Fatalf("distinct members in any order: got %d keys, want 2", len(got))
	}
}
