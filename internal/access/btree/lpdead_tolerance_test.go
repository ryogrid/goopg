package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// C3-S1 (analysis/perf-optimize3/05-improvement-designs/03): every reader
// must tolerate/skip ItemIDDead line pointers. No production writer sets
// the flag yet — these tests set it directly on a synthetic single-leaf
// tree and pin the reader contracts:
//   - enumeration readers (PageItemKeys/PageLeafEntries/pageItems) skip it,
//   - Search/RangeScan never return it,
//   - binary-search descent still works (dead items keep ordering bytes),
//   - the storage helpers round-trip and PageGetItemRaw stays strict.

// markSlotDead finds key's slot on the (single) leaf and marks it Dead,
// returning the leaf block. The tree must be small enough that root==leaf.
func markSlotDead(t *testing.T, bt *BTree, key []byte) storage.BlockNumber {
	t.Helper()
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	leaf := meta.Root
	slot, err := bt.pinW(leaf)
	if err != nil {
		t.Fatalf("pinW: %v", err)
	}
	defer func() { slot.Unlock(); bt.pool.Unpin(slot) }()
	p := slot.Page()
	count, err := storage.PageLinePointerCount(p)
	if err != nil {
		t.Fatal(err)
	}
	for s := uint16(1); s <= uint16(count); s++ {
		raw, rerr := storage.PageGetItemRawAllowDead(p, s)
		if rerr != nil {
			continue
		}
		it, perr := parseItem(raw)
		if perr != nil {
			continue
		}
		if CompareKeys(it.key, key) == 0 {
			if derr := storage.PageSetItemIDDead(p, s); derr != nil {
				t.Fatalf("PageSetItemIDDead: %v", derr)
			}
			op := ParseOpaque(p)
			op.Flags |= BTHasGarbage
			writeOpaque(p, op)
			return leaf
		}
	}
	t.Fatalf("key not found on leaf %d", leaf)
	return 0
}

func TestLPDeadInvisibleToSearchAndRangeScan(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i, k := range []int32{10, 20, 30, 40, 50} {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(100 + i), Offset: uint16(i + 1)}
		if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
			t.Fatal(err)
		}
	}
	leaf := markSlotDead(t, bt, EncodeInt4(30))
	_ = leaf

	// Search must not return the dead entry.
	if _, ok, err := bt.Search(EncodeInt4(30)); err != nil {
		t.Fatalf("Search: %v", err)
	} else if ok {
		t.Fatal("Search returned an ItemIDDead entry")
	}
	// Neighbors still found (binary search over dead ordering bytes works).
	for _, k := range []int32{10, 20, 40, 50} {
		if _, ok, err := bt.Search(EncodeInt4(k)); err != nil || !ok {
			t.Fatalf("Search(%d) = ok=%v err=%v, want found", k, ok, err)
		}
	}
	// RangeScan skips the dead entry and returns the rest in order.
	var got []int32
	if err := bt.RangeScan(nil, nil, func(key []byte, _ storage.ItemPointer) (bool, error) {
		v, derr := DecodeInt4(key)
		if derr != nil {
			return false, derr
		}
		got = append(got, v)
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	want := []int32{10, 20, 40, 50}
	if len(got) != len(want) {
		t.Fatalf("RangeScan returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RangeScan returned %v, want %v", got, want)
		}
	}
}

func TestLPDeadSkippedByPageReaders(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i, k := range []int32{1, 2, 3} {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(200 + i), Offset: uint16(i + 1)}
		if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
			t.Fatal(err)
		}
	}
	leaf := markSlotDead(t, bt, EncodeInt4(2))

	slot, err := bt.pinR(leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { slot.RUnlock(); bt.pool.Unpin(slot) }()
	p := slot.Page()

	if keys, err := (IndexFormat{}).PageItemKeys(p); err != nil {
		t.Fatalf("PageItemKeys: %v", err)
	} else if len(keys) != 2 {
		t.Fatalf("PageItemKeys returned %d keys, want 2 (dead skipped)", len(keys))
	}
	if entries, err := (IndexFormat{}).PageLeafEntries(p); err != nil {
		t.Fatalf("PageLeafEntries: %v", err)
	} else if len(entries) != 2 {
		t.Fatalf("PageLeafEntries returned %d, want 2", len(entries))
	}
	if items, err := blobFormat.pageItems(p); err != nil {
		t.Fatalf("pageItems: %v", err)
	} else if len(items) != 2 {
		t.Fatalf("pageItems returned %d, want 2", len(items))
	}
	if !ParseOpaque(p).HasGarbage() {
		t.Fatal("BTHasGarbage not observed via HasGarbage()")
	}
}

func TestLPDeadStorageHelperContracts(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	if err := bt.Insert(EncodeInt4(7), storage.ItemPointer{Block: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	leaf := markSlotDead(t, bt, EncodeInt4(7))

	slot, err := bt.pinR(leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { slot.RUnlock(); bt.pool.Unpin(slot) }()
	p := slot.Page()

	dead, err := storage.PageItemIsDead(p, 1)
	if err != nil || !dead {
		t.Fatalf("PageItemIsDead = %v, %v; want true", dead, err)
	}
	// Strict reader must reject; AllowDead must return the bytes.
	if _, err := storage.PageGetItemRaw(p, 1); err == nil {
		t.Fatal("PageGetItemRaw returned an ItemIDDead slot without error")
	}
	raw, err := storage.PageGetItemRawAllowDead(p, 1)
	if err != nil {
		t.Fatalf("PageGetItemRawAllowDead: %v", err)
	}
	it, err := parseItem(raw)
	if err != nil || CompareKeys(it.key, EncodeInt4(7)) != 0 {
		t.Fatalf("dead item bytes not preserved: %v", err)
	}
}

// TestLPDeadInsertOntoLeafWithDeadItem pins the readPageItem AllowDead
// change (C3-S1 review SHOULD-FIX 2): insertItemSorted's binary-search
// probe panics on a Dead slot without it. Also covers the
// rightmost-leaf lower-bound probe reading a dead FIRST item.
func TestLPDeadInsertOntoLeafWithDeadItem(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i, k := range []int32{10, 20, 30, 40, 50} {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(300 + i), Offset: uint16(i + 1)}
		if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
			t.Fatal(err)
		}
	}
	markSlotDead(t, bt, EncodeInt4(30))

	// Inserts on both sides of the dead item (probe walks over it).
	if err := bt.Insert(EncodeInt4(25), storage.ItemPointer{Block: 500, Offset: 1}); err != nil {
		t.Fatalf("Insert(25) onto dead-carrying leaf: %v", err)
	}
	if err := bt.Insert(EncodeInt4(35), storage.ItemPointer{Block: 501, Offset: 1}); err != nil {
		t.Fatalf("Insert(35): %v", err)
	}
	// Rightmost-path probe with a dead FIRST item.
	markSlotDead(t, bt, EncodeInt4(10))
	if err := bt.Insert(EncodeInt4(60), storage.ItemPointer{Block: 502, Offset: 1}); err != nil {
		t.Fatalf("Insert(60) rightmost with dead first item: %v", err)
	}
	for _, k := range []int32{20, 25, 35, 40, 50, 60} {
		if _, ok, err := bt.Search(EncodeInt4(k)); err != nil || !ok {
			t.Fatalf("Search(%d) = ok=%v err=%v, want found", k, ok, err)
		}
	}
	for _, k := range []int32{10, 30} {
		if _, ok, _ := bt.Search(EncodeInt4(k)); ok {
			t.Fatalf("Search(%d) returned a dead entry", k)
		}
	}
}

// TestLPDeadVacuumDropsMarkedEntries pins the VacuumIndexPages fix (C3-S1
// review MUST-FIX 1): a dead-MARKED entry whose TID is NOT in deadTIDs
// must still be physically dropped by the vacuum rewrite (trusting the
// mark, D3) — never silently skipped and left to resurrect on replay.
func TestLPDeadVacuumDropsMarkedEntries(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	for i, k := range []int32{1, 2, 3} {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(400 + i), Offset: uint16(i + 1)}
		if err := bt.Insert(EncodeInt4(k), ptr); err != nil {
			t.Fatal(err)
		}
	}
	leaf := markSlotDead(t, bt, EncodeInt4(2))

	// A non-empty deadTIDs list (heap vacuum reclaimed SOMETHING — here a
	// TID unrelated to this index) activates the leaf walk; the marked
	// entry must be dropped by the same logged rewrite even though its own
	// TID is not in the list. (An empty list early-exits by design: no
	// heap reclaim => no resurrection hazard.)
	removed, err := bt.VacuumIndexPages([]storage.ItemPointer{{Block: 9999, Offset: 1}})
	if err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if removed != 1 {
		t.Fatalf("VacuumIndexPages removed %d, want 1 (the marked entry)", removed)
	}
	slot, err := bt.pinR(leaf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { slot.RUnlock(); bt.pool.Unpin(slot) }()
	count, err := storage.PageLinePointerCount(slot.Page())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("post-vacuum line pointers = %d, want 2 (marked entry physically gone)", count)
	}
	for s := uint16(1); s <= uint16(count); s++ {
		if dead, _ := storage.PageItemIsDead(slot.Page(), s); dead {
			t.Fatalf("slot %d still ItemIDDead after vacuum", s)
		}
	}
}

// TestRangeScanWithPosCoordinates pins the C3-S2 ScanPos contract: for
// every callback, (Blk, Slot) must address the exact line pointer whose
// item carries the delivered key/ptr, and PageLSN must equal the leaf's
// pd_lsn at scan time (D7's re-verify token). Multi-leaf coverage ensures
// per-leaf capture (not first-leaf reuse).
func TestRangeScanWithPosCoordinates(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	const n = 600 // enough to split across leaves
	for i := 0; i < n; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatal(err)
		}
	}
	type seen struct {
		ptr storage.ItemPointer
		pos ScanPos
	}
	var entries []seen
	blks := map[storage.BlockNumber]bool{}
	if err := bt.RangeScanWithPos(nil, nil, false, false, func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
		entries = append(entries, seen{ptr: ptr, pos: pos})
		blks[pos.Blk] = true
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("scanned %d entries, want %d", len(entries), n)
	}
	if len(blks) < 2 {
		t.Fatalf("expected >=2 leaves, got %d (test needs a split)", len(blks))
	}
	for _, e := range entries {
		slot, err := bt.pinR(e.pos.Blk)
		if err != nil {
			t.Fatal(err)
		}
		p := slot.Page()
		// ScanPos.Slot is a DATA slot (S11.2b): on a non-rightmost page the
		// physical offset is one higher, because P_HIKEY owns offset 1.
		raw, rerr := pgGetItemRawAllowDead(p, e.pos.Slot)
		if rerr != nil {
			t.Fatalf("blk=%d slot=%d: %v", e.pos.Blk, e.pos.Slot, rerr)
		}
		it, perr := parseItem(raw)
		if perr != nil {
			t.Fatalf("parseItem blk=%d slot=%d: %v", e.pos.Blk, e.pos.Slot, perr)
		}
		if it.ptr != e.ptr {
			t.Fatalf("blk=%d slot=%d: item ptr %v != callback ptr %v", e.pos.Blk, e.pos.Slot, it.ptr, e.ptr)
		}
		if got := storage.MustHeader(p).LSN(); got != e.pos.PageLSN {
			t.Fatalf("blk=%d: PageLSN %d != pd_lsn %d", e.pos.Blk, e.pos.PageLSN, got)
		}
		slot.RUnlock()
		bt.pool.Unpin(slot)
	}
}
