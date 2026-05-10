package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestM0071SlotViewRowAdapter pins the rowSlotView Row → SlotView
// adapter used at evalExpr's legacy entry point.
func TestM0071SlotViewRowAdapter(t *testing.T) {
	r := Row{NewIntDatum(7), NewStringDatum("hello"), {}}
	v := rowSlotView(r)
	if v.Get(0).Int != 7 {
		t.Errorf("rowSlotView.Get(0).Int = %d, want 7", v.Get(0).Int)
	}
	if v.Get(1).StringValue() != "hello" {
		t.Errorf("rowSlotView.Get(1) = %q, want %q", v.Get(1).StringValue(), "hello")
	}
	if !v.IsNull(2) {
		t.Errorf("rowSlotView.IsNull(2) = false, want true")
	}
	if v.IsNull(0) {
		t.Errorf("rowSlotView.IsNull(0) = true, want false")
	}
}

// TestM0071SlotToRowConversions covers the slotToRow helper used
// by evalExprSlot for legacy Row-only helpers (Subquery / In /
// Exists / Extract / FuncCall / CaseExpr).
func TestM0071SlotToRowConversions(t *testing.T) {
	if slotToRow(nil) != nil {
		t.Errorf("slotToRow(nil) should be nil")
	}
	r := Row{NewIntDatum(1), NewIntDatum(2)}
	if got := slotToRow(rowSlotView(r)); &got[0] != &r[0] {
		t.Errorf("rowSlotView round-trip should be zero-copy")
	}
	mat := SlotFromRow(nil, r)
	if got := slotToRow(mat); &got[0] != &r[0] {
		t.Errorf("MaterializedSlot round-trip should be zero-copy")
	}
	left := SlotFromRow(nil, Row{NewIntDatum(10)})
	right := SlotFromRow(nil, Row{NewIntDatum(20)})
	v := NewVirtualSlot(nil,
		[]TupleSlot{left, right},
		[]virtualCol{{0, 0}, {1, 0}})
	got := slotToRow(v)
	if len(got) != 2 || got[0].Int != 10 || got[1].Int != 20 {
		t.Errorf("VirtualSlot materialise via slotToRow: got %v, want [10,20]", got)
	}
}

// TestM0071EvalExprSlotMatchesRow fuzzes evalExpr (Row entry) and
// evalExprSlot (slot entry) against a corpus of expressions that
// exercise ColumnRef / OuterColumnRef / UnaryOp / BinaryOp /
// Constants. The two paths must agree byte-for-byte.
func TestM0071EvalExprSlotMatchesRow(t *testing.T) {
	row := Row{
		NewIntDatum(7),
		NewStringDatum("alpha"),
		NewIntDatum(13),
		NewBoolDatum(true),
		{}, // null
	}
	slot := rowSlotView(row)

	exprs := []planner.Expr{
		// Constants
		&planner.IntegerConst{Value: 42},
		&planner.StringConst{Value: "x"},
		&planner.NullConst{},
		&planner.BooleanConst{Value: true},
		// Column refs
		&planner.ColumnRef{Name: "c0", Index: 0},
		&planner.ColumnRef{Name: "c1", Index: 1},
		&planner.ColumnRef{Name: "c2", Index: 2},
		&planner.ColumnRef{Name: "c4null", Index: 4},
		// UnaryOp
		&planner.UnaryOp{Op: parser.OpSub, Operand: &planner.ColumnRef{Index: 0}},
		&planner.UnaryOp{Op: parser.OpNot, Operand: &planner.ColumnRef{Index: 3}},
		// BinaryOp
		&planner.BinaryOp{Op: parser.OpAdd,
			Left:  &planner.ColumnRef{Index: 0},
			Right: &planner.ColumnRef{Index: 2}},
		&planner.BinaryOp{Op: parser.OpEq,
			Left:  &planner.ColumnRef{Index: 0},
			Right: &planner.IntegerConst{Value: 7}},
		&planner.BinaryOp{Op: parser.OpLt,
			Left:  &planner.ColumnRef{Index: 0},
			Right: &planner.ColumnRef{Index: 2}},
		&planner.BinaryOp{Op: parser.OpAnd,
			Left:  &planner.BooleanConst{Value: true},
			Right: &planner.ColumnRef{Index: 3}},
	}

	ctx := &Context{}
	for i, e := range exprs {
		viaRow, errRow := evalExpr(e, row, ctx)
		viaSlot, errSlot := evalExprSlot(e, slot, ctx)
		if (errRow == nil) != (errSlot == nil) {
			t.Errorf("expr %d (%T): err mismatch row=%v slot=%v", i, e, errRow, errSlot)
			continue
		}
		if errRow != nil {
			continue
		}
		if !datumEq(viaRow, viaSlot) {
			t.Errorf("expr %d (%T): row=%+v slot=%+v", i, e, viaRow, viaSlot)
		}
	}
}

// TestM0071EvalExprSlotNilSlot pins the contract that ColumnRef
// against a nil slot returns a structured error (not a panic),
// matching the prior nil-Row contract at expr.go.
func TestM0071EvalExprSlotNilSlot(t *testing.T) {
	ctx := &Context{}
	cr := &planner.ColumnRef{Name: "c", Index: 0}
	_, err := evalExprSlot(cr, nil, ctx)
	if err == nil {
		t.Fatalf("expected error for ColumnRef on nil slot, got nil")
	}
	// Constants must succeed even with nil slot.
	v, err := evalExprSlot(&planner.IntegerConst{Value: 1}, nil, ctx)
	if err != nil {
		t.Errorf("constant on nil slot: unexpected err %v", err)
	}
	if v.Int != 1 {
		t.Errorf("constant on nil slot: got %d, want 1", v.Int)
	}
}

func datumEq(a, b Datum) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.IsNull() != b.IsNull() {
		return false
	}
	if a.IsNull() {
		return true
	}
	switch a.Kind {
	case KindInt:
		return a.Int == b.Int
	case KindBool:
		return a.BoolValue() == b.BoolValue()
	case KindString:
		return a.StringValue() == b.StringValue()
	default:
		return false
	}
}
