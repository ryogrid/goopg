package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestM0072BindOuterAcceptsSlotView pins the M0072-0001 contract:
// indexScanOp.BindOuter accepts a SlotView + outerWidth, stores
// them, and the persistent fields are reachable from the lookup
// path via evalExprSlot.
func TestM0072BindOuterAcceptsSlotView(t *testing.T) {
	o := &indexScanOp{}
	outerSchema := optimizer.Schema{{Name: "o0"}, {Name: "o1"}}
	outerMS := SlotFromRow(outerSchema, Row{NewIntDatum(7), NewIntDatum(99)})

	o.BindOuter(outerMS, 2)

	if o.outerSlot == nil {
		t.Fatalf("BindOuter did not set outerSlot")
	}
	if o.outerWidth != 2 {
		t.Errorf("outerWidth = %d, want 2", o.outerWidth)
	}

	// Read via slot.Get to verify the stored slot is functional.
	if v := o.outerSlot.Get(0); v.Int != 7 {
		t.Errorf("outerSlot.Get(0).Int = %d, want 7", v.Int)
	}
	if v := o.outerSlot.Get(1); v.Int != 99 {
		t.Errorf("outerSlot.Get(1).Int = %d, want 99", v.Int)
	}
}

// TestM0072BindOuterNilSlotStandalonePath pins the historical
// single-table-IndexScan contract: Open()→Rescan(nil, 0) leaves
// outerSlot == nil and outerWidth == 0, matching the legacy
// "no outer correlation" semantics. evalExprSlot honours nil
// SlotView for ColumnRef-free key expressions.
func TestM0072BindOuterNilSlotStandalonePath(t *testing.T) {
	o := &indexScanOp{}
	o.outerSlot = SlotFromRow(nil, Row{NewIntDatum(1)})
	o.outerWidth = 1
	o.BindOuter(nil, 0)
	if o.outerSlot != nil {
		t.Errorf("BindOuter(nil) should clear outerSlot; got %T", o.outerSlot)
	}
	if o.outerWidth != 0 {
		t.Errorf("BindOuter(nil) should reset outerWidth; got %d", o.outerWidth)
	}
}
