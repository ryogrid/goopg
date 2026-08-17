package nbtree

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
	var halfDead []storage.BtreeMarkPageHalfDeadRequest
	var unlatched []storage.BlockNumber

	vacCap := &captureLogBtreeVacuum{}
	// latchCheck asserts that every block a record names is still exclusively
	// latched at the moment the record is emitted. Pinning is not latching, so
	// this cannot deadlock: TryRLock fails exactly when someone (us, the caller
	// up the stack) holds the exclusive content latch.
	latchCheck := func(r storage.RelFileNode, named ...storage.BlockNumber) error {
		for _, blk := range named {
			if blk == storage.InvalidBlockNumber {
				continue
			}
			s, err := pool.Pin(storage.BufferTag{Rel: r, Block: blk})
			if err != nil {
				return err
			}
			if s.TryRLock() {
				s.RUnlock()
				unlatched = append(unlatched, blk)
			}
			pool.Unpin(s)
		}
		return nil
	}
	logUnlink := func(r storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
		emissions = append(emissions, req)
		op := ReadPGOpaque(req.TargetPage)
		if err := latchCheck(r, req.TargetBlk, legacySibling(op.Prev), legacySibling(op.Next)); err != nil {
			return 0, err
		}
		return storage.LSN(1234), nil
	}
	logHalfDead := func(r storage.RelFileNode, req storage.BtreeMarkPageHalfDeadRequest) (storage.LSN, error) {
		halfDead = append(halfDead, req)
		if err := latchCheck(r, req.LeafBlk, req.ParentBlk); err != nil {
			return 0, err
		}
		return storage.LSN(1234), nil
	}
	noopRoot := func(storage.RelFileNode, storage.BlockNumber, storage.Page, storage.BlockNumber, storage.BlockNumber, storage.Page) (storage.LSN, error) {
		return storage.LSN(1234), nil
	}
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:                    256,
		LogBtreeVacuum:           vacCap.emit,
		LogBtreeUnlinkPage:       logUnlink,
		LogBtreeNewRoot:          noopRoot,
		LogBtreeMarkPageHalfDead: logHalfDead,
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

	// Every unlink is preceded by its own phase-1 record: since S11.5d-3b-2 the
	// deletion is upstream's PAIR (xl_btree_mark_page_halfdead then
	// xl_btree_unlink_page), and a phase-2 record for a page nothing ever
	// marked half-dead would describe a mutation the primary never made.
	if len(halfDead) != len(emissions) {
		t.Errorf("%d mark-halfdead records for %d unlink records — the two phases must pair up",
			len(halfDead), len(emissions))
	}
	for i, req := range halfDead {
		if req.POffset == 0 {
			t.Errorf("halfdead[%d]: poffset 0 is not a valid OffsetNumber", i)
		}
		if req.ParentBlk == storage.InvalidBlockNumber {
			t.Errorf("halfdead[%d]: no parent block; redo reads block 1 unconditionally", i)
		}
		if req.TopParent != storage.InvalidBlockNumber {
			t.Errorf("halfdead[%d]: topparent=%d, want InvalidBlockNumber (goopg deletes one page at a time)",
				i, req.TopParent)
		}
	}

	// The record must describe the page state that was actually written. A
	// later unlink in the same pass may legitimately rewrite a sibling link
	// again, so each page is checked against the LAST emission naming it.
	wantNext := map[storage.BlockNumber]storage.BlockNumber{}
	wantPrev := map[storage.BlockNumber]storage.BlockNumber{}
	wantTarget := map[storage.BlockNumber]PGBTPageOpaque{}
	for _, req := range emissions {
		op := ReadPGOpaque(req.TargetPage)
		if op.Next == PNone {
			t.Errorf("unlink of block %d has no right sibling; upstream's redo reads block 2 unconditionally",
				req.TargetBlk)
		}
		if left := legacySibling(op.Prev); left != storage.InvalidBlockNumber {
			wantNext[left] = legacySibling(op.Next)
		}
		wantPrev[legacySibling(op.Next)] = legacySibling(op.Prev)
		wantTarget[req.TargetBlk] = op
	}
	readOp := func(blk storage.BlockNumber) PGBTPageOpaque {
		t.Helper()
		s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin %d: %v", blk, err)
		}
		s.RLock()
		op := ReadPGOpaque(s.Page())
		s.RUnlock()
		pool.Unpin(s)
		return op
	}
	for blk, want := range wantNext {
		if got := legacySibling(readOp(blk).Next); got != want {
			t.Errorf("block %d btpo_next=%d but the last record naming it logged %d", blk, got, want)
		}
	}
	for blk, want := range wantPrev {
		if got := legacySibling(readOp(blk).Prev); got != want {
			t.Errorf("block %d btpo_prev=%d but the last record naming it logged %d", blk, got, want)
		}
	}
	for blk, want := range wantTarget {
		got := readOp(blk)
		if got != want {
			t.Errorf("block %d opaque=%+v but its unlink record logged the image %+v", blk, got, want)
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
