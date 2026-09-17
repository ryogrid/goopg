package optimizer

// M0141-S7-exec-a: standalone tests for the IncrementalSort Node type.
// Constructed directly here — createPlanNode has no arm producing it yet
// (M0141-S7-exec-b) — mirroring the zero-caller posture already used by
// pathkeysCountContainedIn/costIncrementalSort's own tests.

import "testing"

type incSortLeaf struct {
	pos int
	sch Schema
}

func (n *incSortLeaf) Pos() int       { return n.pos }
func (n *incSortLeaf) Output() Schema { return n.sch }

func TestIncrementalSort_PosAndOutput(t *testing.T) {
	child := &incSortLeaf{pos: 7, sch: Schema{{Name: "a"}, {Name: "b"}}}
	n := &IncrementalSort{
		pos:   3,
		Child: child,
		Keys: []SortKey{
			{Expr: &ColumnRef{Index: 0}},
			{Expr: &ColumnRef{Index: 1}, Desc: true},
		},
		PresortedCount: 1,
	}
	if got := n.Pos(); got != 3 {
		t.Errorf("Pos() = %d, want 3 (own pos, not the child's)", got)
	}
	out := n.Output()
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Errorf("Output() = %+v, want the child's schema unchanged", out)
	}
	if n.PresortedCount != 1 || len(n.Keys) != 2 {
		t.Errorf("PresortedCount/Keys not stored as constructed: got PresortedCount=%d len(Keys)=%d",
			n.PresortedCount, len(n.Keys))
	}
	var _ Node = n // IncrementalSort must satisfy the Node interface.
}
