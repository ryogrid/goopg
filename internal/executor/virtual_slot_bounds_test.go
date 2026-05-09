package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// TestVirtualSlotVirtualColAccessor pins the VirtualCol(col)
// accessor introduced in M0074-0002 for chained-NLI
// diagnostics + future planner-side rebind.
func TestVirtualSlotVirtualColAccessor(t *testing.T) {
	schema := planner.Schema{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	src1 := SlotFromRow(planner.Schema{{Name: "x"}, {Name: "y"}},
		Row{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 2}})
	src2 := SlotFromRow(planner.Schema{{Name: "z"}},
		Row{{Kind: KindInt, Int: 3}})
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 0}, // a → src1.x
		{sourceIdx: 1, sourceCol: 0}, // b → src2.z
		{sourceIdx: 0, sourceCol: 1}, // c → src1.y
	}
	vs := NewVirtualSlot(schema, []TupleSlot{src1, src2}, cols)

	cases := []struct {
		col            int
		wantSourceIdx  int
		wantSourceCol  int
	}{
		{0, 0, 0},
		{1, 1, 0},
		{2, 0, 1},
	}
	for _, tc := range cases {
		gotIdx, gotCol := vs.VirtualCol(tc.col)
		if gotIdx != tc.wantSourceIdx || gotCol != tc.wantSourceCol {
			t.Errorf("VirtualCol(%d) = (%d, %d) want (%d, %d)",
				tc.col, gotIdx, gotCol, tc.wantSourceIdx, tc.wantSourceCol)
		}
	}
}

// TestEvalExprSlotVirtualSlotBoundsCheck pins the
// defensive bounds check in evalExprSlot's ColumnRef arm
// for *VirtualSlot inputs (M0074-0002). An out-of-range
// ColumnRef.Index against a VirtualSlot must surface as a
// hard error rather than panic or read wrong column.
func TestEvalExprSlotVirtualSlotBoundsCheck(t *testing.T) {
	schema := planner.Schema{{Name: "a"}, {Name: "b"}}
	src := SlotFromRow(planner.Schema{{Name: "x"}, {Name: "y"}},
		Row{{Kind: KindInt, Int: 1}, {Kind: KindInt, Int: 2}})
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 0},
		{sourceIdx: 0, sourceCol: 1},
	}
	vs := NewVirtualSlot(schema, []TupleSlot{src}, cols)

	// Out-of-range ColumnRef must produce a clear error
	// mentioning "VirtualSlot range".
	cref := &planner.ColumnRef{Name: "ghost", Index: 5}
	_, err := evalExprSlot(cref, vs, &Context{})
	if err == nil {
		t.Fatal("expected error for out-of-range ColumnRef on VirtualSlot, got nil")
	}
	if !strings.Contains(err.Error(), "VirtualSlot range") {
		t.Errorf("error message should mention VirtualSlot range; got: %v", err)
	}
}

// TestEvalExprSlotVirtualSlotInRange pins the happy path:
// a valid ColumnRef.Index against a VirtualSlot returns
// the correct datum routed via virtualCol.
func TestEvalExprSlotVirtualSlotInRange(t *testing.T) {
	schema := planner.Schema{{Name: "a"}, {Name: "b"}}
	src := SlotFromRow(planner.Schema{{Name: "x"}, {Name: "y"}},
		Row{{Kind: KindInt, Int: 100}, {Kind: KindInt, Int: 200}})
	cols := []virtualCol{
		{sourceIdx: 0, sourceCol: 1}, // VirtualSlot col 0 → src.y
		{sourceIdx: 0, sourceCol: 0}, // VirtualSlot col 1 → src.x
	}
	vs := NewVirtualSlot(schema, []TupleSlot{src}, cols)

	// Read VirtualSlot col 0 → should resolve to src.y = 200.
	cref0 := &planner.ColumnRef{Name: "a", Index: 0}
	v, err := evalExprSlot(cref0, vs, &Context{})
	if err != nil {
		t.Fatalf("evalExprSlot col 0: %v", err)
	}
	if v.Int != 200 {
		t.Errorf("col 0 (mapped to src.y=200) got %d", v.Int)
	}

	// Read VirtualSlot col 1 → should resolve to src.x = 100.
	cref1 := &planner.ColumnRef{Name: "b", Index: 1}
	v, err = evalExprSlot(cref1, vs, &Context{})
	if err != nil {
		t.Fatalf("evalExprSlot col 1: %v", err)
	}
	if v.Int != 100 {
		t.Errorf("col 1 (mapped to src.x=100) got %d", v.Int)
	}
}
