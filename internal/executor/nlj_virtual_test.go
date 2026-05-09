package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestM0071NLIVirtualSlotCompose pins the Stage D-1 invariant:
// nestedLoopIndexJoinOp emits a VirtualSlot that composes the
// outer's row + the inner's row without an intermediate concat
// allocation. Rebinding the persistent outerMS / innerMS .row
// fields is the entire per-match work.
func TestM0071NLIVirtualSlotCompose(t *testing.T) {
	outerSchema := planner.Schema{
		{Name: "o0"}, {Name: "o1"},
	}
	innerSchema := planner.Schema{
		{Name: "i0"}, {Name: "i1"}, {Name: "i2"},
	}
	o := &nestedLoopIndexJoinOp{
		outerWidth: 2,
		innerWidth: 3,
	}
	o.outerMS = SlotFromRow(outerSchema, nil)
	o.innerMS = SlotFromRow(innerSchema, nil)
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 0},
		{sourceIdx: 0, sourceCol: 1},
		{sourceIdx: 1, sourceCol: 0},
		{sourceIdx: 1, sourceCol: 1},
		{sourceIdx: 1, sourceCol: 2},
	}
	o.virtualOut = NewVirtualSlot(append(planner.Schema{}, append(append(planner.Schema{}, outerSchema...), innerSchema...)...),
		[]TupleSlot{o.outerMS, o.innerMS}, cols)

	// First match: outer=[1,"alpha"], inner=[10,"x",100]
	o.outerMS.row = Row{NewIntDatum(1), NewStringDatum("alpha")}
	o.innerMS.row = Row{NewIntDatum(10), NewStringDatum("x"), NewIntDatum(100)}
	if o.virtualOut.Get(0).Int != 1 {
		t.Errorf("Get(0)=%d, want 1", o.virtualOut.Get(0).Int)
	}
	if o.virtualOut.Get(1).StringValue() != "alpha" {
		t.Errorf("Get(1)=%q, want alpha", o.virtualOut.Get(1).StringValue())
	}
	if o.virtualOut.Get(2).Int != 10 {
		t.Errorf("Get(2)=%d, want 10", o.virtualOut.Get(2).Int)
	}
	if o.virtualOut.Get(4).Int != 100 {
		t.Errorf("Get(4)=%d, want 100", o.virtualOut.Get(4).Int)
	}

	// Rebind only the inner row. virtualOut.Get re-reads via
	// innerMS — no additional allocations.
	o.innerMS.row = Row{NewIntDatum(20), NewStringDatum("y"), NewIntDatum(200)}
	if o.virtualOut.Get(0).Int != 1 {
		t.Errorf("after inner rebind: Get(0) outer-side changed: %d", o.virtualOut.Get(0).Int)
	}
	if o.virtualOut.Get(2).Int != 20 {
		t.Errorf("after inner rebind: Get(2)=%d, want 20", o.virtualOut.Get(2).Int)
	}
	if o.virtualOut.Get(4).Int != 200 {
		t.Errorf("after inner rebind: Get(4)=%d, want 200", o.virtualOut.Get(4).Int)
	}
}

// TestM0071NLIPredicateSlotEval exercises evalPredicateSlot
// against the virtualOut composition. The predicate should read
// directly via slot.Get without forcing a Row materialisation.
func TestM0071NLIPredicateSlotEval(t *testing.T) {
	outerSchema := planner.Schema{{Name: "o0"}}
	innerSchema := planner.Schema{{Name: "i0"}}
	pred := &planner.BinaryOp{
		Op:    "=",
		Left:  &planner.ColumnRef{Name: "o0", Index: 0},
		Right: &planner.ColumnRef{Name: "i0", Index: 1},
	}
	plan := &planner.NestedLoopIndexJoin{Predicate: pred}
	o := &nestedLoopIndexJoinOp{
		plan:       plan,
		outerWidth: 1,
		innerWidth: 1,
		ctx:        &Context{},
	}
	o.outerMS = SlotFromRow(outerSchema, nil)
	o.innerMS = SlotFromRow(innerSchema, nil)
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 0},
		{sourceIdx: 1, sourceCol: 0},
	}
	o.virtualOut = NewVirtualSlot(append(planner.Schema{}, append(append(planner.Schema{}, outerSchema...), innerSchema...)...),
		[]TupleSlot{o.outerMS, o.innerMS}, cols)

	// outer=[7], inner=[7] → equal → true
	o.outerMS.row = Row{NewIntDatum(7)}
	o.innerMS.row = Row{NewIntDatum(7)}
	ok, err := o.evalPredicateSlot()
	if err != nil {
		t.Fatalf("eval err: %v", err)
	}
	if !ok {
		t.Errorf("expected predicate TRUE for 7=7, got false")
	}

	// outer=[7], inner=[8] → not equal → false
	o.innerMS.row = Row{NewIntDatum(8)}
	ok, err = o.evalPredicateSlot()
	if err != nil {
		t.Fatalf("eval err: %v", err)
	}
	if ok {
		t.Errorf("expected predicate FALSE for 7=8, got true")
	}
}
