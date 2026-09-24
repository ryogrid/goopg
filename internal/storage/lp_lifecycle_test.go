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

// TestPageAddHeapTupleReusesFreeLine pins PageAddItemExtended's reuse arm
// (M0145-0008v S3b): on a heap_lp_lifecycle cluster an insert takes the first
// LP_UNUSED item without storage, and appends (clearing PD_HAS_FREE_LINES) when
// none is left; without the capability it always appends. A page at the
// line-pointer ceiling reports free space only while a free line exists.
func TestPageAddHeapTupleReusesFreeLine(t *testing.T) {
	build := func(t *testing.T) Page {
		t.Helper()
		p := make(Page, BlockSize)
		if err := InitPage(p); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			if _, err := PageAddHeapTuple(p, NewHeapTuple(1, InvalidTransactionID, []byte("row"))); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := PruneHeapPageBySlots(p, nil, []uint16{2, 3}); err != nil {
			t.Fatal(err)
		}
		if _, err := PageVacuumDeadItems(p, []uint16{2, 3}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	add := func(t *testing.T, p Page) uint16 {
		t.Helper()
		s, err := PageAddHeapTuple(p, NewHeapTuple(1, InvalidTransactionID, []byte("new")))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	SetHeapLinePointerLifecycle(false)
	if s := add(t, build(t)); s != 6 {
		t.Fatalf("legacy cluster: slot %d, want an append (6)", s)
	}

	SetHeapLinePointerLifecycle(true)
	defer SetHeapLinePointerLifecycle(false)
	p := build(t)
	for _, want := range []uint16{2, 3, 6} {
		if s := add(t, p); s != want {
			t.Fatalf("slot %d, want %d", s, want)
		}
	}
	if MustHeader(p).Flags()&PDHasFreeLines != 0 {
		t.Fatal("PD_HAS_FREE_LINES must be cleared once no free line is left")
	}

	// The ceiling: fill the array to MaxHeapTuplesPerPage with tiny tuples,
	// free one interior item, and the page reports room again.
	q := make(Page, BlockSize)
	if err := InitPage(q); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxHeapTuplesPerPage; i++ {
		if _, err := PageAddHeapTuple(q, NewHeapTuple(1, InvalidTransactionID, nil)); err != nil {
			t.Fatalf("filling slot %d: %v", i+1, err)
		}
	}
	if PageGetHeapFreeSpace(q) != 0 {
		t.Fatal("a page at the ceiling with no free line must report 0")
	}
	if _, err := PruneHeapPageBySlots(q, nil, []uint16{10}); err != nil {
		t.Fatal(err)
	}
	if _, err := PageVacuumDeadItems(q, []uint16{10}); err != nil {
		t.Fatal(err)
	}
	if PageGetHeapFreeSpace(q) == 0 {
		t.Fatal("a page at the ceiling with a free line must report its space")
	}
	if s := add(t, q); s != 10 {
		t.Fatalf("ceiling page reuse: slot %d, want 10", s)
	}
}
