package storage

import (
	"bytes"
	"testing"
)

func newTestPage(t *testing.T) Page {
	t.Helper()
	p := make(Page, BlockSize)
	if err := InitPage(p); err != nil {
		t.Fatalf("InitPage: %v", err)
	}
	return p
}

// PageReserveLinePointer must make the slot "appear allocated" — visible in
// the line-pointer count — without consuming any tuple space, because that is
// exactly what _bt_blnewpage does for P_HIKEY.
func TestPageReserveLinePointerAllocatesSlotWithoutPayload(t *testing.T) {
	p := newTestPage(t)
	upperBefore := MustHeader(p).Upper()

	slot, err := PageReserveLinePointer(p)
	if err != nil {
		t.Fatalf("PageReserveLinePointer: %v", err)
	}
	if slot != 1 {
		t.Fatalf("reserved slot = %d, want 1", slot)
	}
	count, err := PageLinePointerCount(p)
	if err != nil {
		t.Fatalf("PageLinePointerCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("line pointer count = %d, want 1", count)
	}
	if got := MustHeader(p).Upper(); got != upperBefore {
		t.Fatalf("pd_upper moved: %d -> %d (reservation must not allocate payload)", upperBefore, got)
	}
	id, err := PageGetItemID(p, 1)
	if err != nil {
		t.Fatalf("PageGetItemID: %v", err)
	}
	if id.Flags != ItemIDUnused {
		t.Fatalf("reserved line pointer flags = %d, want LP_UNUSED", id.Flags)
	}

	// A subsequent item must land at slot 2, not overwrite the reservation.
	next, err := PageAddItemRaw(p, []byte("first-data-key"))
	if err != nil {
		t.Fatalf("PageAddItemRaw: %v", err)
	}
	if next != 2 {
		t.Fatalf("item after reservation landed at slot %d, want 2", next)
	}
}

// PageDeleteLinePointerAt is _bt_slideleft: the removed slot's successors move
// DOWN one offset number (unlike PageRemoveHeapTuple, which blanks in place).
func TestPageDeleteLinePointerAtSlidesSuccessorsDown(t *testing.T) {
	p := newTestPage(t)
	if _, err := PageReserveLinePointer(p); err != nil {
		t.Fatalf("PageReserveLinePointer: %v", err)
	}
	want := [][]byte{[]byte("aaa"), []byte("bbbb"), []byte("ccccc")}
	for _, raw := range want {
		if _, err := PageAddItemRaw(p, raw); err != nil {
			t.Fatalf("PageAddItemRaw: %v", err)
		}
	}

	if err := PageDeleteLinePointerAt(p, 1); err != nil {
		t.Fatalf("PageDeleteLinePointerAt: %v", err)
	}

	count, err := PageLinePointerCount(p)
	if err != nil {
		t.Fatalf("PageLinePointerCount: %v", err)
	}
	if count != len(want) {
		t.Fatalf("line pointer count after slide = %d, want %d", count, len(want))
	}
	for i, exp := range want {
		got, err := PageGetItemRaw(p, uint16(i+1))
		if err != nil {
			t.Fatalf("PageGetItemRaw(%d): %v", i+1, err)
		}
		if !bytes.Equal(got, exp) {
			t.Fatalf("slot %d = %q, want %q", i+1, got, exp)
		}
	}
}

func TestPageDeleteLinePointerAtRejectsOutOfRange(t *testing.T) {
	p := newTestPage(t)
	if _, err := PageAddItemRaw(p, []byte("only")); err != nil {
		t.Fatalf("PageAddItemRaw: %v", err)
	}
	if err := PageDeleteLinePointerAt(p, 2); err == nil {
		t.Fatal("PageDeleteLinePointerAt(2) on a 1-item page: want error, got nil")
	}
	if err := PageDeleteLinePointerAt(p, 0); err == nil {
		t.Fatal("PageDeleteLinePointerAt(0): want error, got nil")
	}
}
