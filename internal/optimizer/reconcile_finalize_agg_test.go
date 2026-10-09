package optimizer

import "testing"

// TestReconcileLeavesFinalizeKeysOnPartialInput pins the Finalize arm of
// reconcileNLILayoutBody (M0146-0005ap). A Finalize aggregate is a copy of the
// original: its group keys address the Partial aggregate's INPUT row, which
// the executor resolves through PartialSource, not its own child (the Gather
// over the partial-state row, where the key sits at column 0). Resolving them
// against that child moved TPC-H Q15's `l_suppkey` from column 4 to 0 and
// tripped assertSearchedTreeNeedsNoReconcile once the view's split aggregate
// sat inside an outer join search.
func TestReconcileLeavesFinalizeKeysOnPartialInput(t *testing.T) {
	input := SeqScanWithSchemaForTest(nil, Schema{
		{Name: "l_shipdate"}, {Name: "l_orderkey"}, {Name: "l_discount"},
		{Name: "l_extendedprice"}, {Name: "l_suppkey"},
	})
	key := func() *ColumnRef { return &ColumnRef{Index: 4, Name: "l_suppkey"} }
	partial := &Aggregate{Mode: AggModePartial, Child: input, GroupExprs: []Expr{key()},
		schema: Schema{{Name: "l_suppkey"}, {Name: "sum"}}}
	gather := NewGather(0, partial, 2)
	finalKey := key()
	final := &Aggregate{Mode: AggModeFinal, Child: gather, GroupExprs: []Expr{finalKey}, PartialSource: partial}
	if got := gather.Output(); len(got) == 0 || got[0].Name != "l_suppkey" {
		t.Fatalf("fixture: the partial-state row should lead with the key, got %v", got)
	}

	assertSearchedTreeNeedsNoReconcile(final)
	if finalKey.Index != 4 {
		t.Fatalf("Finalize key must keep addressing the partial input (column 4), got %d", finalKey.Index)
	}
}
