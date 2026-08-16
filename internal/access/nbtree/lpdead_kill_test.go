package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// C3-S3 mandatory tests (design §6 S3): the deferred marking pass must
// apply only when the leaf is provably unchanged (page-LSN equality, D7)
// and must never bump pd_lsn itself.

// newTestTreeWAL builds a tree whose pool has counting WAL emitters wired
// (LogPageImage + LogBtreeVacuum + change records) so pd_lsn ADVANCES on
// logged changes — the environment KillItems' D7 token needs. newTestTree
// (no hooks) is the vacuous-token case: KillItems must refuse to mark
// there (pinned by TestKillItemsRefusesWithoutWALHook).
func newTestTreeWAL(t *testing.T) (*BTree, *storage.Pool, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	var lsn storage.LSN = 100
	next := func() storage.LSN { lsn += 8; return lsn }
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          32,
		FullPageWrites: true,
		LogPageImage: func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
			return next(), nil
		},
		LogBtreeVacuum: func(_ storage.RelFileNode, _ storage.BlockNumber, _, _ storage.Page, _ []uint16) (storage.LSN, error) {
			return next(), nil
		},
		WALFrontier: func() uint64 { return uint64(lsn) },
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9001, Fork: storage.MainFork}
	bt, err := Create(pool, rel)
	if err != nil {
		t.Fatalf("nbtree.Create: %v", err)
	}
	return bt, pool, func() { _ = pool.Close(); _ = mgr.Close() }
}

func scanPositions(t *testing.T, bt *BTree) map[int32]KillItem {
	t.Helper()
	out := map[int32]KillItem{}
	if err := bt.RangeScanWithPos(nil, nil, false, false, func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
		v, err := DecodeInt4(key)
		if err != nil {
			return false, err
		}
		out[v] = KillItem{Pos: pos, Ptr: ptr}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestKillItemsMarksOnMatchingLSN(t *testing.T) {
	bt, _, cleanup := newTestTreeWAL(t)
	defer cleanup()
	for i, k := range []int32{10, 20, 30} {
		if err := bt.Insert(EncodeInt4(k), storage.ItemPointer{Block: storage.BlockNumber(600 + i), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	kills := scanPositions(t, bt)
	k := kills[20]
	lsnBefore := k.Pos.PageLSN

	bt.KillItems([]KillItem{k})

	// Marked: invisible to Search; pd_lsn UNCHANGED (unlogged hint, D7).
	if _, ok, _ := bt.Search(EncodeInt4(20)); ok {
		t.Fatal("killed entry still visible")
	}
	slot, err := bt.pinR(k.Pos.Blk)
	if err != nil {
		t.Fatal(err)
	}
	if got := storage.MustHeader(slot.Page()).LSN(); got != lsnBefore {
		t.Fatalf("marking pass bumped pd_lsn %d -> %d (self-invalidates D7)", lsnBefore, got)
	}
	if !ParseOpaque(slot.Page()).HasGarbage() {
		t.Fatal("BTHasGarbage not set")
	}
	slot.RUnlock()
	bt.pool.Unpin(slot)
}

// (a) design case: the leaf changed between capture and mark — a
// WAL-logged change (here: the dedup/vacuum-style rewrite path via
// VacuumIndexPages, which shifts slots and bumps pd_lsn) must fail the
// LSN re-verify and DROP the kill, protecting a live entry that re-used
// the (key, TID) coordinates.
func TestKillItemsDroppedOnLSNChange(t *testing.T) {
	bt, _, cleanup := newTestTreeWAL(t)
	defer cleanup()
	for i, k := range []int32{10, 20, 30} {
		if err := bt.Insert(EncodeInt4(k), storage.ItemPointer{Block: storage.BlockNumber(700 + i), Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	kills := scanPositions(t, bt)
	stale := kills[20]

	// Concurrent "vacuum" removes 10 (slot shift: 20 moves to slot 1) and
	// bumps pd_lsn via the logged kept-items rewrite.
	if _, err := bt.VacuumIndexPages([]storage.ItemPointer{{Block: 700, Offset: 1}}); err != nil {
		t.Fatal(err)
	}

	bt.KillItems([]KillItem{stale})

	// The stale kill must be dropped: 20 and 30 both stay live.
	for _, k := range []int32{20, 30} {
		if _, ok, err := bt.Search(EncodeInt4(k)); err != nil || !ok {
			t.Fatalf("Search(%d) = ok=%v err=%v — stale kill corrupted a live entry", k, ok, err)
		}
	}
}

// (b) design case: heap-TID recycle — a live entry re-created at the SAME
// (key, TID) after the original was vacuumed away. The TID pre-filter
// alone would match; only the LSN check saves it.
func TestKillItemsDroppedOnTIDRecycle(t *testing.T) {
	bt, _, cleanup := newTestTreeWAL(t)
	defer cleanup()
	ptr := storage.ItemPointer{Block: 800, Offset: 1}
	if err := bt.Insert(EncodeInt4(42), ptr); err != nil {
		t.Fatal(err)
	}
	if err := bt.Insert(EncodeInt4(43), storage.ItemPointer{Block: 801, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	kills := scanPositions(t, bt)
	stale := kills[42]

	// Vacuum away 42, then re-insert the SAME (key, TID) — a legitimately
	// LIVE entry now occupies the same coordinates (LSN has advanced).
	if _, err := bt.VacuumIndexPages([]storage.ItemPointer{ptr}); err != nil {
		t.Fatal(err)
	}
	if err := bt.Insert(EncodeInt4(42), ptr); err != nil {
		t.Fatal(err)
	}

	bt.KillItems([]KillItem{stale})

	if _, ok, err := bt.Search(EncodeInt4(42)); err != nil || !ok {
		t.Fatalf("live re-created (K,T) entry was killed by a stale mark (ok=%v err=%v)", ok, err)
	}
}

// TestKillItemsRefusesWithoutWALHook: with no vacuum-record hook wired,
// pd_lsn never advances, the D7 token is vacuous, and KillItems must not
// mark anything.
func TestKillItemsRefusesWithoutWALHook(t *testing.T) {
	bt, _, cleanup := newTestTree(t) // hook-less harness
	defer cleanup()
	if err := bt.Insert(EncodeInt4(5), storage.ItemPointer{Block: 900, Offset: 1}); err != nil {
		t.Fatal(err)
	}
	kills := scanPositions(t, bt)
	bt.KillItems([]KillItem{kills[5]})
	if _, ok, err := bt.Search(EncodeInt4(5)); err != nil || !ok {
		t.Fatalf("entry killed despite vacuous LSN token (ok=%v err=%v)", ok, err)
	}
}

// TestNoSpaceRewritePurgesDeadItems (C3-S4): when an insert hits a full
// leaf, the no-space recovery rewrite must physically DROP ItemIDDead
// entries and log the survivor set as a kept-items record (the
// _bt_simpledel_pass analog — S1's reader skip supplies the drop, the
// S3 blocker-A fix supplies the record). The recorder asserts the dead
// key is absent from keptRaw, so a crash replay reconstructs the page
// without it.
func TestNoSpaceRewritePurgesDeadItems(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()
	var lsn storage.LSN = 100
	next := func() storage.LSN { lsn += 8; return lsn }
	var lastPage storage.Page
	var vacCalls int
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          64,
		FullPageWrites: true,
		LogPageImage: func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
			return next(), nil
		},
		LogBtreeVacuum: func(_ storage.RelFileNode, _ storage.BlockNumber, _ storage.Page, page storage.Page, _ []uint16) (storage.LSN, error) {
			vacCalls++
			lastPage = append(storage.Page(nil), page...)
			return next(), nil
		},
		WALFrontier: func() uint64 { return uint64(lsn) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9002, Fork: storage.MainFork}
	bt, err := Create(pool, rel)
	if err != nil {
		t.Fatal(err)
	}

	// Fill one leaf with wide keys until the NEXT insert would not fit.
	wide := func(i int) []byte {
		k := make([]byte, 512)
		copy(k, EncodeInt4(int32(i)))
		return k
	}
	i := 0
	filled := false
	for ; i < 64; i++ {
		if err := bt.Insert(wide(i), storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}); err != nil {
			t.Fatal(err)
		}
		meta, _ := bt.readMeta()
		slot, perr := bt.pinR(meta.Root)
		if perr != nil {
			t.Fatal(perr)
		}
		full := !blobFormat.pageHasSpaceFor(slot.Page(), item{key: make([]byte, 512)})
		slot.RUnlock()
		bt.pool.Unpin(slot)
		if full {
			filled = true
			break
		}
	}
	if !filled {
		t.Skip("64 x 512B keys never filled the leaf — page sizing drifted")
	}

	// Mark one mid-page entry dead, then insert one more (no space →
	// recovery rewrite fires).
	kills := map[int32]KillItem{}
	if err := bt.RangeScanWithPos(nil, nil, false, false, func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
		v, _ := DecodeInt4(key[:4])
		kills[v] = KillItem{Pos: pos, Ptr: ptr}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	bt.KillItems([]KillItem{kills[2]})
	if _, ok, _ := bt.Search(wide(2)); ok {
		t.Fatal("kill did not apply")
	}

	vacCalls = 0
	if err := bt.Insert(wide(1000), storage.ItemPointer{Block: 999, Offset: 1}); err != nil {
		t.Fatalf("insert onto full dead-carrying leaf: %v", err)
	}
	if vacCalls == 0 {
		t.Fatal("no-space rewrite did not emit the kept-items record (purge unlogged or split taken — S3 blocker-A regression)")
	}
	// A8: the record now carries the post-vacuum page as a full-page image
	// (what crash-replay restores verbatim). It must exclude the dead key.
	deadKey := wide(2)
	keys, err := (IndexFormat{}).PageItemKeys(lastPage)
	if err != nil {
		t.Fatalf("post-vacuum page keys unreadable (format drift): %v", err)
	}
	for _, k := range keys {
		if CompareKeys(k, deadKey) == 0 {
			t.Fatal("post-vacuum page image still carries the dead entry — purge not logged")
		}
	}
	// Total survivor accounting: i+1 inserted, minus the dead one, plus
	// the incoming key — dedup cannot legally shrink further (no dups).
	if want := i + 1; len(keys) != want {
		t.Fatalf("post-vacuum page carries %d entries, want %d (a LIVE item was dropped)", len(keys), want)
	}
	// And the live neighbors + the new key survive.
	for _, k := range [][]byte{wide(0), wide(1), wide(3), wide(1000)} {
		if _, ok, err := bt.Search(k); err != nil || !ok {
			t.Fatalf("live key lost across the purge rewrite (ok=%v err=%v)", ok, err)
		}
	}
}
