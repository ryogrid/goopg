package nbtree

// review/260831-2 NB-3 — readInternalFirstChildBlock decoded the downlink by
// hand and got the bytes wrong.
//
// A pivot tuple's downlink lives in t_tid: bi_hi at [0:2], bi_lo at [2:4].
// The function read a bare little-endian uint32 at [2:6], which drops the high
// 16 bits of the block number and folds in the offset/status half — so it
// returned a wrong block whenever either was non-zero. It had no callers, so
// nothing noticed; this test is now its caller.

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

func TestReadInternalFirstChildBlockMatchesTheDownlinkDecoder(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	// Enough keys to lift a root above the leaf level.
	for i := range 2000 {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i), Offset: uint16(i%100 + 1)}
		if err := bt.Insert(EncodeInt4(int32(i)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i, err)
		}
	}
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}

	slot, err := bt.pinR(meta.Root)
	if err != nil {
		t.Fatalf("pinR(root=%d): %v", meta.Root, err)
	}
	// unpinned explicitly below — the page is re-pinned for writing.
	if readOpaque(slot.Page()).IsLeaf() {
		t.Fatalf("root %d is still a leaf; the tree never grew a level", meta.Root)
	}
	raw, err := pgGetItemRaw(slot.Page(), 1)
	if err != nil {
		t.Fatalf("pgGetItemRaw: %v", err)
	}
	want := BTreeTupleGetDownLink(raw)
	if got := readInternalFirstChildBlock(slot.Page()); got != want {
		t.Errorf("readInternalFirstChildBlock = %d, want %d (the downlink the shared decoder reads)", got, want)
	}

	// The child has to be a real page of the tree, not a byte-mangled
	// number: pinning it must succeed and it must sit below the root.
	child, err := bt.pinR(want)
	if err != nil {
		t.Fatalf("pinR(child=%d): %v", want, err)
	}
	if lvl := readOpaque(child.Page()).Level; lvl >= readOpaque(slot.Page()).Level {
		t.Errorf("child level %d is not below root level %d", lvl, readOpaque(slot.Page()).Level)
	}
	bt.unpinR(child)
	bt.unpinR(slot)

	// A tree this size only ever has small block numbers with a zero
	// offset half, where the hand-rolled read happens to agree. Stamp a
	// downlink that uses both halves — 0x00012345 needs bi_hi — and a
	// non-zero offset half, the two things the old expression got wrong.
	const bigChild = storage.BlockNumber(0x00012345)
	w, err := bt.pinW(meta.Root)
	if err != nil {
		t.Fatalf("pinW(root): %v", err)
	}
	rawInPlace, err := pgGetItemRawNoCopy(w.Page(), 1)
	if err != nil {
		bt.unpinW(w)
		t.Fatalf("pgGetItemRawNoCopy: %v", err)
	}
	BTreeTupleSetDownLink(rawInPlace, bigChild)
	rawInPlace[4], rawInPlace[5] = 0x07, 0x00 // offset/status half, not part of the block
	got := readInternalFirstChildBlock(w.Page())
	bt.unpinW(w)
	if got != bigChild {
		t.Errorf("readInternalFirstChildBlock = %#x, want %#x", got, bigChild)
	}
}
