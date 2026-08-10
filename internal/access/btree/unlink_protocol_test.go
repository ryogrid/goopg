package btree

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestUnlinkRecordEmittedUnderWriteLatches is the M0130-S11.5d-3b guard.
//
// Before S11.5d-3b, `unlinkEmptyLeaf` computed the nearest live siblings with
// NOTHING latched, emitted the unlink record with those values, and then
// re-derived each link again under the write latch that performed the write
// (AI-20260709-010336-082: a split on another connection's *BTree can splice a
// live page into the chain in between, and stamping the stale value stomped
// that split's relink). Correct for the tree, fatal for the record: the
// primary deliberately wrote something other than what it had logged, so the
// record's link fields were advisory — which no PG-shaped record may be, and
// which is precisely what blocks emitting xl_btree_mark_page_halfdead /
// xl_btree_unlink_page here.
//
// Upstream's protocol (`_bt_unlink_halfdead_page`, nbtpage.c) holds the
// latches instead: pin left/target/right (plus the parent), compute, emit,
// write. This test pins BOTH halves of that:
//
//  1. every page the record names is still exclusively latched at the moment
//     the record is emitted (checked from inside the emitter hook, which is
//     where a real encoder will read the page images from in S11.5d-3b-2), and
//  2. the values the record carries are the values that end up on the pages —
//     no post-emit re-derivation anywhere.
func TestUnlinkRecordEmittedUnderWriteLatches(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16999, Fork: storage.MainFork}

	var pool *storage.Pool
	var emissions []storage.BtreeUnlinkPageRequest
	var unlatched []storage.BlockNumber

	vacCap := &captureLogBtreeVacuum{}
	logUnlink := func(r storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
		emissions = append(emissions, req)
		// Every block the record names must be write-latched right now.
		// Pinning is not latching, so this cannot deadlock: TryRLock
		// fails exactly when someone (us, the caller up the stack) holds
		// the exclusive content latch.
		named := []storage.BlockNumber{req.LeafBlk}
		if req.HasLeftSib {
			named = append(named, req.LeftSibBlk)
		}
		if req.HasRightSib {
			named = append(named, req.RightSibBlk)
		}
		if req.HasParent {
			named = append(named, req.ParentBlk)
		}
		for _, blk := range named {
			s, err := pool.Pin(storage.BufferTag{Rel: r, Block: blk})
			if err != nil {
				return 0, err
			}
			if s.TryRLock() {
				s.RUnlock()
				unlatched = append(unlatched, blk)
			}
			pool.Unpin(s)
		}
		return storage.LSN(1234), nil
	}
	noopRoot := func(storage.RelFileNode, storage.BlockNumber, storage.Page, storage.BlockNumber, storage.BlockNumber, storage.Page) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	noopHalfDead := func(storage.RelFileNode, storage.BlockNumber, uint16) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:                    256,
		LogBtreeVacuum:           vacCap.emit,
		LogBtreeUnlinkPage:       logUnlink,
		LogBtreeNewRoot:          noopRoot,
		LogBtreeMarkPageHalfDead: noopHalfDead,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	// Kill three quarters of the entries: enough to empty and unlink many
	// leaves, but not the whole tree — an all-dead vacuum ends by RESETTING
	// the tree to a single empty root, which legitimately rewrites the flags
	// the last unlink record logged.
	const n = 5000
	entries := make([]BulkEntry, n)
	var dead []storage.ItemPointer
	for i := range n {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
		if i < n*3/4 {
			dead = append(dead, entries[i].Ptr)
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	if _, err := tree.VacuumIndexPages(dead); err != nil {
		t.Fatalf("VacuumIndexPages: %v", err)
	}

	if len(emissions) == 0 {
		t.Fatal("no unlink emissions despite every entry being dead")
	}
	if len(unlatched) > 0 {
		t.Errorf("unlink record emitted while blocks %v were NOT write-latched — "+
			"the record's fields are advisory again (M0130-S11.5d-3b)", unlatched)
	}

	// The record must describe the page state that was actually written. A
	// later unlink in the same pass may legitimately rewrite a sibling link
	// again, so each page is checked against the LAST emission naming it.
	wantNext := map[storage.BlockNumber]storage.BlockNumber{}
	wantPrev := map[storage.BlockNumber]storage.BlockNumber{}
	wantFlags := map[storage.BlockNumber]uint16{}
	for _, req := range emissions {
		if req.HasLeftSib {
			wantNext[req.LeftSibBlk] = req.LeftSibNewNext
		}
		if req.HasRightSib {
			wantPrev[req.RightSibBlk] = req.RightSibNewPrev
		}
		wantFlags[req.LeafBlk] = req.LeafFlagsAfter
	}
	readOp := func(blk storage.BlockNumber) BTPageOpaque {
		t.Helper()
		s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin %d: %v", blk, err)
		}
		s.RLock()
		op := readOpaque(s.Page())
		s.RUnlock()
		pool.Unpin(s)
		return op
	}
	for blk, want := range wantNext {
		if got := readOp(blk).Next; got != want {
			t.Errorf("block %d btpo_next=%d but the last record naming it logged %d", blk, got, want)
		}
	}
	for blk, want := range wantPrev {
		if got := readOp(blk).Prev; got != want {
			t.Errorf("block %d btpo_prev=%d but the last record naming it logged %d", blk, got, want)
		}
	}
	for blk, want := range wantFlags {
		if got := readOp(blk).Flags; got != want {
			t.Errorf("block %d flags=%#x but its unlink record logged %#x", blk, got, want)
		}
	}
}

// TestAcquireUnlinkPinsRefusesCyclicDeadRun pins the one case
// acquireUnlinkPins must refuse outright rather than retry: a dead-page run
// that loops back onto the deletion target (or onto the other neighbour)
// would have the protocol latch the same block twice and deadlock the
// goroutine against itself. Upstream never builds such a chain; goopg must
// still not hang if a corrupt index produces one. M0130-S11.5d-3b.
func TestAcquireUnlinkPinsRefusesCyclicDeadRun(t *testing.T) {
	pool, rel := newVacuumTestPool(t)

	const n = 600
	entries := make([]BulkEntry, n)
	for i := range n {
		entries[i] = BulkEntry{
			Key: EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
	}
	tree, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	meta, err := tree.readMeta()
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	// Find a leaf with a left neighbour, then bend that neighbour's btpo_prev
	// back onto the leaf itself and mark it dead, so walking left from the
	// leaf lands back on the leaf.
	leaf, err := tree.findLeftmostLeaf()
	if err != nil {
		t.Fatalf("findLeftmostLeaf: %v", err)
	}
	s, err := tree.pinR(leaf)
	if err != nil {
		t.Fatalf("pinR: %v", err)
	}
	next := readOpaque(s.Page()).Next
	tree.unpinR(s)
	if next == storage.InvalidBlockNumber {
		t.Skip("single-leaf tree; nothing to bend")
	}
	// `next` becomes a dead page whose btpo_next points back at `leaf`.
	w, err := tree.pinW(next)
	if err != nil {
		t.Fatalf("pinW: %v", err)
	}
	op := readOpaque(w.Page())
	op.Flags |= BTDeleted
	op.Next = leaf
	writeOpaque(w.Page(), op)
	tree.pool.MarkDirty(w)
	tree.unpinW(w)

	pins, err := tree.acquireUnlinkPins(leaf, meta.Root, true)
	if err == nil {
		pins.release(tree)
		t.Fatal("acquireUnlinkPins accepted a dead run that loops back onto the target")
	}
	if !errors.Is(err, errUnlinkChainUnstable) {
		t.Fatalf("acquireUnlinkPins: got %v, want errUnlinkChainUnstable", err)
	}
}
