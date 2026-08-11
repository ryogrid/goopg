package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// Guards for M0130-S11.5d-3c: a page-deletion tombstone. Before this slice
// goopg unlinked a leaf and handed its block to the very next allocation in
// the same call; upstream stamps the deleted page with the XID counter's
// current value and refuses to reuse the block until no snapshot can still be
// descending to it (`BTPageIsRecyclable`, nbtree.h:290-318, consulted from
// `_bt_allocbuf`). Each test below is one way that refusal could be lost.

// TestPGPageIsRecyclable covers the predicate itself over the three page
// shapes it must distinguish: a live page (never recyclable, whatever the
// horizon), a page deleted with a safexid (recyclable only once the horizon
// has moved past it), and goopg's legacy deleted stamp that carries no
// safexid at all (recyclable at once, preserving pre-S11.5d-3c behaviour for
// the non-WAL deletion paths).
func TestPGPageIsRecyclable(t *testing.T) {
	live := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(live); err != nil {
		t.Fatal(err)
	}
	WritePGOpaque(live, PGBTPageOpaque{Prev: 1, Next: 3, Flags: BTPLeaf})
	if PGPageIsRecyclable(live, ^uint64(0)) {
		t.Error("a live page must never be recyclable, even at an infinite horizon")
	}

	deleted := make(storage.Page, storage.BlockSize)
	copy(deleted, live)
	if err := ReplayUnlinkTargetPage(deleted, 1, 3, 0, 100); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		horizon uint64
		want    bool
	}{
		{99, false},  // horizon behind the deletion: scans may still reach it
		{100, false}, // exactly at it: upstream's test is strict precedence
		{101, true},  // past it: no snapshot can hold the downlink any more
	} {
		if got := PGPageIsRecyclable(deleted, tc.horizon); got != tc.want {
			t.Errorf("PGPageIsRecyclable(safexid=100, oldestVisible=%d) = %v, want %v",
				tc.horizon, got, tc.want)
		}
	}

	// The legacy stamp: BTDeleted written through the legacy opaque with no
	// BTDeletedPageData behind it.
	legacy := make(storage.Page, storage.BlockSize)
	copy(legacy, live)
	op := readOpaque(legacy)
	op.Flags |= BTDeleted
	writeOpaque(legacy, op)
	if _, ok := PGDeletedPageSafeXid(legacy); ok {
		t.Fatal("legacy stamp unexpectedly carries a safexid")
	}
	if !PGPageIsRecyclable(legacy, 0) {
		t.Error("a legacy deleted page (no safexid recorded) must stay immediately recyclable")
	}
}

// TestPinNewOrRecycledHonoursTombstone is the allocation-side half: a block on
// the free list is a CANDIDATE. While its safexid is still visible the
// allocator must extend the relation instead of handing the block out — and
// must put the candidate BACK, because a tombstone becomes reusable later
// rather than being garbage. Handing it out early is the concrete corruption
// this slice prevents: a scan holding the old downlink would land on a page an
// unrelated split had meanwhile filled with foreign keys.
func TestPinNewOrRecycledHonoursTombstone(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	horizon := uint64(100)
	pool.SetBtreeRecycleHorizon(func() (next, oldestVisible uint64) {
		return horizon, horizon
	})
	tree, err := BulkCreate(pool, rel, []BulkEntry{
		{Key: EncodeInt4(1), Ptr: storage.ItemPointer{Block: 0, Offset: 1}},
	})
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	// Allocate a block, stamp it as a page deleted at safexid 100, and hand it
	// to the free list — the exact state unlinkEmptyLeaf leaves behind.
	slot, tomb, err := tree.pinNewLocked()
	if err != nil {
		t.Fatalf("pinNewLocked: %v", err)
	}
	if err := ReplayUnlinkTargetPage(slot.Page(), 1, 3, 0, 100); err != nil {
		t.Fatal(err)
	}
	pool.MarkDirtyWithLSNLocked(slot, storage.LSN(1))
	slot.Unlock()
	pool.Unpin(slot)
	tree.recycleBlock(tomb)

	// Horizon == safexid: not yet removable, so the allocator must extend.
	got, blk, err := tree.pinNewOrRecycled()
	if err != nil {
		t.Fatalf("pinNewOrRecycled: %v", err)
	}
	got.Unlock()
	pool.Unpin(got)
	if blk == tomb {
		t.Fatalf("allocator reused block %d while its safexid was still visible", tomb)
	}
	if n := tree.RecycledPageCount(); n != 1 {
		t.Fatalf("free list holds %d blocks, want the tombstone put back (1)", n)
	}

	// Advance the horizon past the deletion; now the block is free.
	horizon = 101
	got, blk, err = tree.pinNewOrRecycled()
	if err != nil {
		t.Fatalf("pinNewOrRecycled: %v", err)
	}
	got.Unlock()
	pool.Unpin(got)
	if blk != tomb {
		t.Errorf("allocator extended the relation (block %d) instead of reusing the now-recyclable tombstone %d", blk, tomb)
	}
	if n := tree.RecycledPageCount(); n != 0 {
		t.Errorf("free list holds %d blocks after the tombstone was taken, want 0", n)
	}
}

// TestPinNewOrRecycledUngatedKeepsLegacyBehaviour pins the fallback contract:
// with no horizon source wired (every bare-pool unit test, and any embedding
// that never calls SetBtreeRecycleHorizon) the free list stays authoritative
// and the page is not consulted at all. Without this, the legacy non-WAL
// deletion paths — which stamp no safexid — would strand every block they free.
func TestPinNewOrRecycledUngatedKeepsLegacyBehaviour(t *testing.T) {
	pool, rel := newVacuumTestPool(t)
	tree, err := BulkCreate(pool, rel, []BulkEntry{
		{Key: EncodeInt4(1), Ptr: storage.ItemPointer{Block: 0, Offset: 1}},
	})
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	slot, blk, err := tree.pinNewLocked()
	if err != nil {
		t.Fatalf("pinNewLocked: %v", err)
	}
	slot.Unlock()
	pool.Unpin(slot)
	tree.recycleBlock(blk)

	got, reused, err := tree.pinNewOrRecycled()
	if err != nil {
		t.Fatalf("pinNewOrRecycled: %v", err)
	}
	got.Unlock()
	pool.Unpin(got)
	if reused != blk {
		t.Errorf("ungated allocator returned block %d, want the free-list block %d", reused, blk)
	}
}

// TestUnlinkStampsSafeXidFromHorizon closes the emit half: the record and the
// page must carry the horizon source's `next`, not a placeholder. The old code
// hard-coded `const safexid = 0`, which every recyclability test reads as
// "removable immediately" — i.e. a wired horizon that silently did nothing
// would still look correct from the redo side alone.
func TestUnlinkStampsSafeXidFromHorizon(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	vacCap := &captureLogBtreeVacuum{}
	var safexids []uint64
	logUnlink := func(rel storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
		safexids = append(safexids, req.SafeXid)
		if got, ok := PGDeletedPageSafeXid(req.TargetPage); !ok || got != req.SafeXid {
			t.Errorf("target image safexid = %#x (ok=%v), want the record's %#x", got, ok, req.SafeXid)
		}
		return storage.LSN(1234), nil
	}
	noop := func(rel storage.RelFileNode, rootBlk storage.BlockNumber, rootPage storage.Page, leftChildBlk storage.BlockNumber, metaBlk storage.BlockNumber, metaPage storage.Page) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	logMarkHalfDead := func(rel storage.RelFileNode, req storage.BtreeMarkPageHalfDeadRequest) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:                    256,
		LogBtreeVacuum:           vacCap.emit,
		LogBtreeUnlinkPage:       logUnlink,
		LogBtreeNewRoot:          noop,
		LogBtreeMarkPageHalfDead: logMarkHalfDead,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	const wantSafeXid = uint64(0xDEAD)
	pool.SetBtreeRecycleHorizon(func() (next, oldestVisible uint64) {
		// oldestVisible sits BEHIND next, as it always does on a live
		// cluster: nothing deleted by this pass is recyclable within it.
		return wantSafeXid, wantSafeXid - 1
	})

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16406, Fork: storage.MainFork}
	const n = 5000
	entries := make([]BulkEntry, n)
	dead := make([]storage.ItemPointer, n)
	for i := range entries {
		ptr := storage.ItemPointer{Block: 0, Offset: uint16(i + 1)}
		entries[i] = BulkEntry{Key: EncodeInt4(int32(i)), Ptr: ptr}
		dead[i] = ptr
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	if _, err := tree.VacuumIndexPages(dead); err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}
	if len(safexids) == 0 {
		t.Fatal("no unlink emissions — expected at least one leaf unlink")
	}
	for i, x := range safexids {
		if x != wantSafeXid {
			t.Errorf("emission[%d]: safexid = %#x, want %#x", i, x, wantSafeXid)
		}
	}
	// And the pages it freed are tombstones, not free space: every block the
	// pass recycled is still on the list, because oldestVisible never reached
	// the safexid it stamped.
	if n := tree.RecycledPageCount(); n == 0 {
		t.Error("vacuum freed no tombstoned blocks; the horizon gate has nothing to hold back")
	}
	slot, blk, err := tree.pinNewOrRecycled()
	if err != nil {
		t.Fatalf("pinNewOrRecycled: %v", err)
	}
	slot.Unlock()
	pool.Unpin(slot)
	nblocks, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatal(err)
	}
	if blk != nblocks-1 {
		t.Errorf("allocator returned block %d (relation has %d blocks) — expected a freshly extended block, not a tombstone", blk, nblocks)
	}
}
