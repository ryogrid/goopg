package storage

import (
	"errors"
	"testing"
)

// TestPageVacuumDeadItemsTruncates pins lazy_vacuum_heap_page's page step
// and PageTruncateLinePointerArray (M0145-0008v): LP_DEAD items become
// LP_UNUSED, trailing unused items leave the array, an interior unused item
// sets PD_HAS_FREE_LINES, the next insert takes the first truncated offset,
// and a slot that is not a storage-less LP_DEAD item is refused untouched.
func TestPageVacuumDeadItemsTruncates(t *testing.T) {
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := PageAddHeapTuple(p, NewHeapTuple(1, InvalidTransactionID, []byte("row"))); err != nil {
			t.Fatal(err)
		}
	}
	// Slots 2, 4 and 5 die and are pruned to LP_DEAD.
	if _, err := PruneHeapPageBySlots(p, nil, []uint16{2, 4, 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := PageVacuumDeadItems(p, []uint16{1}); !errors.Is(err, ErrUnsupportedItem) {
		t.Fatalf("an LP_NORMAL slot must be refused, got %v", err)
	}
	if n, _ := PageLinePointerCount(p); n != 5 {
		t.Fatalf("a refused call changed the array: count=%d", n)
	}
	n, err := PageVacuumDeadItems(p, []uint16{2, 4, 5})
	if err != nil || n != 3 {
		t.Fatalf("PageVacuumDeadItems = %d, %v", n, err)
	}
	if count, _ := PageLinePointerCount(p); count != 3 {
		t.Fatalf("trailing unused items 4,5 not truncated: count=%d", count)
	}
	if MustHeader(p).Flags()&PDHasFreeLines == 0 {
		t.Fatal("interior unused item 2 must set PD_HAS_FREE_LINES")
	}
	if dead, _ := PageDeadItems(p); len(dead) != 0 {
		t.Fatalf("LP_DEAD items left: %v", dead)
	}
	if slot, err := PageAddHeapTuple(p, NewHeapTuple(1, InvalidTransactionID, []byte("new"))); err != nil || slot != 4 {
		t.Fatalf("next insert slot = %d (%v), want 4", slot, err)
	}
}
