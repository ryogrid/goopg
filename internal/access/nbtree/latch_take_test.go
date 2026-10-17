package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestWlatchTakeTransfersOwnership pins wlatch.take: the slot comes back still
// latched and the holder's release no longer touches it, so a frame can hand
// its latched page to a callee (the split keeping its left half latched across
// the parent insert) without the deferred release dropping it underneath.
func TestWlatchTakeTransfersOwnership(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	if err := bt.Insert(EncodeInt4(1), storage.ItemPointer{Block: 1, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatal(err)
	}
	slot, err := bt.pinW(meta.Root)
	if err != nil {
		t.Fatal(err)
	}
	w := wlatch{bt: bt}
	w.hold(slot)
	got := w.take()
	if got != slot {
		t.Fatal("take did not return the held slot")
	}
	w.release() // must not unlatch: ownership moved to got
	if slot.TryRLock() {
		slot.RUnlock()
		t.Fatal("release after take dropped the latch")
	}
	if w.take() != nil {
		t.Error("a second take returned a slot")
	}
	bt.unpinW(got)
}
