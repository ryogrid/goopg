package btree

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
		LogBtreeVacuum: func(_ storage.RelFileNode, _ storage.BlockNumber, _ [][]byte, _ uint16) (storage.LSN, error) {
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
		t.Fatalf("btree.Create: %v", err)
	}
	return bt, pool, func() { _ = pool.Close(); _ = mgr.Close() }
}

func scanPositions(t *testing.T, bt *BTree) map[int32]KillItem {
	t.Helper()
	out := map[int32]KillItem{}
	if err := bt.RangeScanWithPos(nil, nil, func(key []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
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
