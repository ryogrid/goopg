package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestKeyDatumsIntoMatchesAllocatingDecode pins M0122-0012 slice 82: the
// index-only scan decodes each matched entry with pgIndexTupleKeyDatumsInto
// into per-operator scratch reused across entries. Every entry must decode
// exactly as pgIndexTupleKeyDatums decodes it — int and varchar attributes,
// NULLs, and an entry following one whose values (and nulls) differ, so no
// cell of the previous entry survives in the reused buffers.
func TestKeyDatumsIntoMatchesAllocatingDecode(t *testing.T) {
	ctx, cleanup := setupSkipFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE INDEX items_acb ON items (a, c, b)"); err != nil {
		t.Fatalf("CREATE INDEX: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "items_acb"})
	if !ok {
		t.Fatal("lookup items_acb")
	}
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("not a tuple-format index")
	}
	cols := pgIndexKeyColumns(idx)
	n := desc.NKeyAtts()
	imgs, nulls, vals := make([][]byte, n), make([]bool, n), make([]Datum, n)
	entries := [][]Datum{
		{NewIntDatum(1), NewStringDatum("a longer varchar value"), NewIntDatum(10)},
		{NullDatum, NewStringDatum(""), NullDatum},
		{NewIntDatum(-7), NullDatum, NewIntDatum(2147483647)},
		{NewIntDatum(3), NewStringDatum("x"), NewIntDatum(0)},
	}
	for ei, e := range entries {
		key, _, err := pgIndexTupleKey(desc, cols, e, storage.ItemPointer{Block: 9, Offset: uint16(ei + 1)})
		if err != nil {
			t.Fatalf("entry %d: %v", ei, err)
		}
		want, werr := pgIndexTupleKeyDatums(desc, cols, key)
		got, gerr := pgIndexTupleKeyDatumsInto(desc, cols, key, imgs, nulls, vals)
		if werr != nil || gerr != nil {
			t.Fatalf("entry %d: errors %v / %v", ei, werr, gerr)
		}
		if len(got) != len(want) {
			t.Fatalf("entry %d: %d values, want %d", ei, len(got), len(want))
		}
		for i := range want {
			if !sameResult(got[i], nil, want[i], nil) || got[i].IsNull() != e[i].IsNull() {
				t.Fatalf("entry %d attr %d: %v, want %v", ei, i, got[i], want[i])
			}
		}
	}
}

// TestAppendSlotCellsMatchesSlotRow pins the lateral stream's outer-row copy
// (slice 82): appendSlotCells appends exactly slotRow's values, for a join's
// VirtualSlot (read cell by cell instead of through a materialised row), a
// materialised slot and no slot, onto a reused buffer.
func TestAppendSlotCellsMatchesSlotRow(t *testing.T) {
	left := &MaterializedSlot{row: Row{NewIntDatum(1), NewStringDatum("l")}}
	right := &MaterializedSlot{row: Row{NewStringDatum("r"), NullDatum}}
	vs := &VirtualSlot{sources: []TupleSlot{left, right}, cols: []virtualCol{{0, 0}, {0, 1}, {1, 0}, {1, 1}}}
	buf := Row{NewIntDatum(99), NewIntDatum(98), NewIntDatum(97), NewIntDatum(96), NewIntDatum(95)}
	for name, slot := range map[string]TupleSlot{"virtual": vs, "materialized": left, "nil": nil} {
		got := appendSlotCells(buf[:0], slot)
		want := slotRow(slot)
		if len(got) != len(want) {
			t.Fatalf("%s: %d cells, want %d", name, len(got), len(want))
		}
		for i := range want {
			if !sameResult(got[i], nil, want[i], nil) {
				t.Fatalf("%s cell %d: %v, want %v", name, i, got[i], want[i])
			}
		}
	}
}
