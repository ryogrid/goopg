package storage

import (
	"encoding/binary"
	"testing"
)

// TestPageFreezeBySlotsReplaysFreezeMutations pins the M0080-0001
// replay kernel: applying `PageFreezeBySlots` with a given slot
// list rewrites exactly those tuples' xmin to FrozenTransactionID,
// leaving other slots untouched.
func TestPageFreezeBySlotsReplaysFreezeMutations(t *testing.T) {
	page := make(Page, BlockSize)
	if err := InitPage(page); err != nil {
		t.Fatal(err)
	}
	// Insert 5 tuples with successive xmin values.
	for i, body := range []string{"a", "b", "c", "d", "e"} {
		tup := NewHeapTuple(TransactionID(100+i), InvalidTransactionID, []byte(body))
		if _, err := PageAddHeapTuple(page, tup); err != nil {
			t.Fatal(err)
		}
	}

	// Freeze slots {2, 4} (1-based). Slot 1, 3, 5 must remain unchanged.
	if err := PageFreezeBySlots(page, []uint16{2, 4}); err != nil {
		t.Fatalf("PageFreezeBySlots: %v", err)
	}

	for slot := uint16(1); slot <= 5; slot++ {
		ht, err := PageGetHeapTuple(page, slot)
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		got := ht.Header.Xmin
		// Re-read raw bytes too — this is what the encoder would observe.
		item, _ := readItemID(page, int(slot)-1)
		off := int(item.Offset)
		raw := TransactionID(binary.LittleEndian.Uint32(page[off : off+4]))
		if got != raw {
			t.Fatalf("slot %d: header xmin=%d != raw=%d", slot, got, raw)
		}
		switch slot {
		case 2, 4:
			if got != FrozenTransactionID {
				t.Errorf("slot %d xmin=%d, want FrozenTransactionID(%d)", slot, got, FrozenTransactionID)
			}
		default:
			expected := TransactionID(100 + int(slot) - 1)
			if got != expected {
				t.Errorf("slot %d xmin=%d, want %d (untouched)", slot, got, expected)
			}
		}
	}
}

// TestPageFreezeBySlotsRejectsOutOfRange pins the defensive
// bounds check. (M0080-0001.)
func TestPageFreezeBySlotsRejectsOutOfRange(t *testing.T) {
	page := make(Page, BlockSize)
	_ = InitPage(page)
	tup := NewHeapTuple(100, InvalidTransactionID, []byte("a"))
	_, _ = PageAddHeapTuple(page, tup)

	cases := []struct {
		name string
		list []uint16
	}{
		{"zero-slot", []uint16{0}},
		{"out-of-range", []uint16{2}}, // page only has 1 slot
		{"mix", []uint16{1, 99}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := PageFreezeBySlots(page, c.list); err == nil {
				t.Error("PageFreezeBySlots must reject out-of-range slot")
			}
		})
	}
}

// TestPageFreezeBySlotsEmptyListIsNoOp pins the empty-list
// fast path. (M0080-0001.)
func TestPageFreezeBySlotsEmptyListIsNoOp(t *testing.T) {
	page := make(Page, BlockSize)
	_ = InitPage(page)
	tup := NewHeapTuple(100, InvalidTransactionID, []byte("a"))
	_, _ = PageAddHeapTuple(page, tup)
	before := make(Page, BlockSize)
	copy(before, page)
	if err := PageFreezeBySlots(page, nil); err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if page[i] != before[i] {
			t.Fatalf("page changed at offset %d: empty slot list must be no-op", i)
		}
	}
}

// TestPageFreezeOldTuplesReportsFrozenSlots pins that the
// producer-side `PageFreezeOldTuples` populates `FrozenSlots`
// with the same 1-based slot numbers a replay record needs.
// (M0080-0001.)
func TestPageFreezeOldTuplesReportsFrozenSlots(t *testing.T) {
	page := make(Page, BlockSize)
	_ = InitPage(page)
	// 4 tuples: xmin 100, 200, 50, 300. FreezeBelow=150 → slots 1 & 3 freeze.
	xmins := []TransactionID{100, 200, 50, 300}
	for _, xmin := range xmins {
		tup := NewHeapTuple(xmin, InvalidTransactionID, []byte("x"))
		if _, err := PageAddHeapTuple(page, tup); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := PageFreezeOldTuples(page, 150)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Frozen != 2 {
		t.Errorf("Frozen=%d, want 2", stats.Frozen)
	}
	if len(stats.FrozenSlots) != 2 || stats.FrozenSlots[0] != 1 || stats.FrozenSlots[1] != 3 {
		t.Errorf("FrozenSlots=%v, want [1 3]", stats.FrozenSlots)
	}
}
