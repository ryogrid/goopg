package optimizer

import "testing"

// M0146-0214: relation_can_be_sorted_early searches the pathkey's
// equivalence class (find_computable_ec_member). A joinrel without item is
// sorted early on its ss_item_sk for a query ordered on i_item_sk, because a
// clause of the class equates the two; PG's (b 1..17) Gather Merge on TPC-DS
// SF1 Q64 eval 141 is that path.
func TestSortableEarlyMemberUsesTheEquivalenceClass(t *testing.T) {
	s := &searchCtx{}
	rel := newRelOptInfo(RelSet(0b011), 100, 8)
	item := &ColumnRef{Index: 9, Name: "i_item_sk"}
	ss := &ColumnRef{Index: 1, Name: "ss_item_sk"}
	other := &ColumnRef{Index: 7, Name: "d_year"}
	u := &pathkeyUsefulness{ecMembers: []classMember{
		{expr: item, relids: RelSet(0b100), id: 3},
		{expr: ss, relids: RelSet(0b001), id: 3},
	}}
	got, ok := s.sortableEarlyMember(rel, u, item)
	if !ok || !exprEqual(got, ss) {
		t.Fatalf("sortableEarlyMember(i_item_sk) = (%v, %v), want the rel's own member ss_item_sk", got, ok)
	}
	// An expression in no class the rel can compute is not sortable early.
	if _, ok := s.sortableEarlyMember(rel, u, other); ok {
		t.Fatalf("sortableEarlyMember admitted an expression with no computable member")
	}
	// A class whose only other member lies outside the rel does not help.
	u2 := &pathkeyUsefulness{ecMembers: []classMember{
		{expr: item, relids: RelSet(0b100), id: 3},
		{expr: ss, relids: RelSet(0b1000), id: 3},
	}}
	if _, ok := s.sortableEarlyMember(rel, u2, item); ok {
		t.Fatalf("sortableEarlyMember admitted a member outside the rel")
	}
}
