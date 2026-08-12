package btree

import (
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSplitBareBlocksKeepFirstTouchFPI is the M0131-S26b regression guard.
//
// `MarkDirtyWithLSNLocked` advances the slot's native-image watermark, which
// asserts "an image of this page exists in WAL at that LSN" and suppresses the
// page's first-touch full-page image for the rest of the checkpoint epoch. That
// is true for the blocks an xl_btree_split rebuilds from the record (the right
// half, registered WILL_INIT with the full item list) and false for the ones it
// registers BARE — the old right sibling (block 2) and the incomplete-split
// child (block 3), which carry neither image nor data and whose single mutation
// redo re-derives from the page it finds. Suppressing their FPI leaves those
// re-derivations reading a page that may be torn.
//
// The test publishes a redo pointer above every page's watermark (so every page
// owes a fresh image), then drives one split of a NON-rightmost leaf and asserts
// the sibling was imaged. Before the fix no image was emitted for it.
func TestSplitBareBlocksKeepFirstTouchFPI(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()

	var mu sync.Mutex
	nextLSN := storage.LSN(1 << 20) // comfortably above the published redo
	var fpiBlocks []storage.BlockNumber
	var recording bool

	logFPI := func(_ storage.RelFileNode, blk storage.BlockNumber, _ storage.Page) (storage.LSN, error) {
		mu.Lock()
		defer mu.Unlock()
		nextLSN += 8
		if recording {
			fpiBlocks = append(fpiBlocks, blk)
		}
		return nextLSN, nil
	}

	type splitCall struct {
		left, right, sib storage.BlockNumber
		leftIncremental  bool
	}
	var splits []splitCall
	logSplit := func(_ storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, prePage, leftPage, rightPage storage.Page, newItem []byte, sibBlk storage.BlockNumber, _ storage.Page, _ storage.BlockNumber) (storage.LSN, error) {
		mu.Lock()
		defer mu.Unlock()
		nextLSN += 8
		// The primary must pick its left-page stamp by the same predicate the
		// encoder picks the record's left-block form with.
		_, incremental := SplitLeftIsIncremental(prePage, leftPage, rightPage, newItem, readOpaque(leftPage).Level, rightBlk)
		if recording {
			splits = append(splits, splitCall{leftBlk, rightBlk, sibBlk, incremental})
		}
		return nextLSN, nil
	}

	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots:          64,
		LogPageImage:   logFPI,
		FullPageWrites: true,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9311, Fork: storage.MainFork}

	bt, err := CreateWithOptions(pool, rel, Options{LogSplit: logSplit})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Ascending even keys build a multi-leaf tree; every split here is of the
	// rightmost leaf, so none of them carries a sibling block.
	for i := 0; i < 3000; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 1}
		if err := bt.Insert(EncodeInt4(int32(i*2)), ptr); err != nil {
			t.Fatalf("seed Insert(%d): %v", i*2, err)
		}
	}

	// Every page's watermark is now below the published redo: the next mutation
	// of each owes a first-touch image, exactly as after a checkpoint.
	mu.Lock()
	pool.PublishRedoRecPtr(uint64(nextLSN))
	recording = true
	mu.Unlock()

	// Odd keys land in the interior leaves, so these splits DO relink an old
	// right sibling.
	for i := 0; i < 3000 && len(splits) == 0; i++ {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i + 1), Offset: 2}
		if err := bt.Insert(EncodeInt4(int32(i*2+1)), ptr); err != nil {
			t.Fatalf("Insert(%d): %v", i*2+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(splits) == 0 {
		t.Fatal("no split observed after the redo publication")
	}
	sc := splits[0]
	if sc.sib == storage.InvalidBlockNumber {
		t.Fatalf("first observed split (left=%d right=%d) was rightmost; the test needs an interior split", sc.left, sc.right)
	}
	imaged := func(blk storage.BlockNumber) bool {
		for _, b := range fpiBlocks {
			if b == blk {
				return true
			}
		}
		return false
	}
	if !imaged(sc.sib) {
		t.Errorf("sibling block %d took no first-touch FPI across the split (imaged blocks: %v); "+
			"a bare block reference must not advance the native-image watermark", sc.sib, fpiBlocks)
	}
	if sc.leftIncremental && !imaged(sc.left) {
		t.Errorf("left block %d was logged incrementally (no image in the record) yet took no "+
			"first-touch FPI (imaged blocks: %v)", sc.left, fpiBlocks)
	}
}
