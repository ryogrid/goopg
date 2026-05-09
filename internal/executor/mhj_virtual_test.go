package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestM0071MHJVirtualSlotCompose pins the Stage D-2 invariant for
// multiHashJoinOp: the per-match VirtualSlot composition resolves
// every output column to the correct (table, col) source. With 3
// tables of varying widths and an OID-sorted output layout, an
// off-by-one in tableOff[] / cols[] would surface as a wrong-column
// read which the planner's runtime would otherwise mask.
func TestM0071MHJVirtualSlotCompose(t *testing.T) {
	// Three "tables": probe T0 width 2, build T1 width 3, build T2
	// width 1. Total output width = 6; tableOff = [0, 2, 5].
	t0Schema := planner.Schema{{Name: "t0c0"}, {Name: "t0c1"}}
	t1Schema := planner.Schema{{Name: "t1c0"}, {Name: "t1c1"}, {Name: "t1c2"}}
	t2Schema := planner.Schema{{Name: "t2c0"}}
	outSchema := append(append(append(planner.Schema{},
		t0Schema...), t1Schema...), t2Schema...)

	o := &multiHashJoinOp{
		schema: outSchema,
		nulls: []Row{
			nullRow(2),
			nullRow(3),
			nullRow(1),
		},
		tableOff: []int{0, 2, 5},
	}
	o.plan = &planner.MultiHashJoin{
		Tables:     []planner.Node{nil, nil, nil},
		ProbeTable: 0,
	}
	nTables := 3

	// Build virtualOut the same way Open does.
	o.tableSlots = make([]*MaterializedSlot, nTables)
	o.tableSlots[0] = SlotFromRow(t0Schema, o.nulls[0])
	o.tableSlots[1] = SlotFromRow(t1Schema, o.nulls[1])
	o.tableSlots[2] = SlotFromRow(t2Schema, o.nulls[2])
	sources := []TupleSlot{o.tableSlots[0], o.tableSlots[1], o.tableSlots[2]}
	cols := make([]virtualCol, 6)
	for tIdx := 0; tIdx < nTables; tIdx++ {
		width := len(o.nulls[tIdx])
		off := o.tableOff[tIdx]
		for col := 0; col < width; col++ {
			cols[off+col] = virtualCol{
				sourceIdx: int16(tIdx),
				sourceCol: int16(col),
			}
		}
	}
	o.virtualOut = NewVirtualSlot(o.schema, sources, cols)

	// Probe T0=[1,"alpha"], match T1=[10,"x",100], T2=[42]
	o.tableSlots[0].row = Row{NewIntDatum(1), NewStringDatum("alpha")}
	o.tableSlots[1].row = Row{NewIntDatum(10), NewStringDatum("x"), NewIntDatum(100)}
	o.tableSlots[2].row = Row{NewIntDatum(42)}

	expect := []struct {
		col  int
		want any
	}{
		{0, int64(1)},      // t0c0
		{1, "alpha"},       // t0c1
		{2, int64(10)},     // t1c0
		{3, "x"},           // t1c1
		{4, int64(100)},    // t1c2
		{5, int64(42)},     // t2c0
	}
	for _, e := range expect {
		got := o.virtualOut.Get(e.col)
		switch w := e.want.(type) {
		case int64:
			if got.Int != w {
				t.Errorf("Get(%d).Int = %d, want %d", e.col, got.Int, w)
			}
		case string:
			if got.StringValue() != w {
				t.Errorf("Get(%d).StringValue() = %q, want %q",
					e.col, got.StringValue(), w)
			}
		}
	}

	// Re-bind the build T2 to a different match. virtualOut.Get(5)
	// should follow without affecting other tables.
	o.tableSlots[2].row = Row{NewIntDatum(99)}
	if got := o.virtualOut.Get(5); got.Int != 99 {
		t.Errorf("after T2 rebind: Get(5)=%d, want 99", got.Int)
	}
	if got := o.virtualOut.Get(0); got.Int != 1 {
		t.Errorf("after T2 rebind: T0 leaked: Get(0)=%d", got.Int)
	}
	if got := o.virtualOut.Get(2); got.Int != 10 {
		t.Errorf("after T2 rebind: T1 leaked: Get(2)=%d", got.Int)
	}
}

// TestM0071MHJEvalFiltersViaSlot exercises evalFilters going
// through evalExprSlot against virtualOut — the per-step filter
// evaluation path that was previously evaluating evalExpr against
// o.lazyOut.
func TestM0071MHJEvalFiltersViaSlot(t *testing.T) {
	t0Schema := planner.Schema{{Name: "t0c0"}}
	t1Schema := planner.Schema{{Name: "t1c0"}}
	out := append(append(planner.Schema{}, t0Schema...), t1Schema...)
	o := &multiHashJoinOp{
		schema:   out,
		nulls:    []Row{nullRow(1), nullRow(1)},
		tableOff: []int{0, 1},
		ctx:      &Context{},
	}
	o.tableSlots = []*MaterializedSlot{
		SlotFromRow(t0Schema, o.nulls[0]),
		SlotFromRow(t1Schema, o.nulls[1]),
	}
	o.virtualOut = NewVirtualSlot(out,
		[]TupleSlot{o.tableSlots[0], o.tableSlots[1]},
		[]virtualCol{
			{sourceIdx: 0, sourceCol: 0},
			{sourceIdx: 1, sourceCol: 0},
		},
	)
	pred := &planner.BinaryOp{
		Op:    "=",
		Left:  &planner.ColumnRef{Index: 0},
		Right: &planner.ColumnRef{Index: 1},
	}
	o.tableSlots[0].row = Row{NewIntDatum(7)}
	o.tableSlots[1].row = Row{NewIntDatum(7)}
	ok, err := o.evalFilters([]planner.Expr{pred})
	if err != nil {
		t.Fatalf("evalFilters err: %v", err)
	}
	if !ok {
		t.Errorf("expected TRUE for 7=7, got false")
	}
	o.tableSlots[1].row = Row{NewIntDatum(8)}
	ok, err = o.evalFilters([]planner.Expr{pred})
	if err != nil {
		t.Fatalf("evalFilters err: %v", err)
	}
	if ok {
		t.Errorf("expected FALSE for 7=8, got true")
	}
}
