package nbtree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestFinishIncompleteSplitOnContact pins gap 5 of design 0055-0002 §4.2:
// _bt_finish_split. testHookAbandonSplit stops one split after its record and
// before its parent step, which leaves what a crash there leaves after redo:
// the left half flagged BTIncompleteSplit and the right half with no downlink.
// Readers still find every key (they move right). The next writer whose
// descent reaches the left half must finish the split before going on, as
// _bt_moveright does with forupdate: afterwards no page is flagged, the level
// above holds a downlink to the right half, and every key is found and
// scanned in order. Covered for a leaf split, an internal split and a root
// split.
func TestFinishIncompleteSplitOnContact(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preload int    // keys inserted before the hook is armed
		level   uint32 // level of the split to abandon
	}{
		{"root", 0, 0},
		{"leaf", 1500, 0},
		{"internal", 3000, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bt, cleanup := newFinishSplitTree(t)
			defer cleanup()
			keys := rand.New(rand.NewSource(7)).Perm(8000)
			inserted := map[int]bool{}
			for _, n := range keys[:tc.preload] {
				if err := bt.Insert(finishKey(n), finishTID(n)); err != nil {
					t.Fatal(err)
				}
				inserted[n] = true
			}

			armed := true
			testHookAbandonSplit = func(level uint32) bool {
				if armed && level == tc.level {
					armed = false
					return true
				}
				return false
			}
			abandoned := -1 // the key whose insert stopped the split
			for _, n := range keys[tc.preload:] {
				err := bt.Insert(finishKey(n), finishTID(n))
				inserted[n] = true // the split wrote the key before stopping
				if errors.Is(err, errSplitAbandoned) {
					abandoned = n
					break
				}
				if err != nil {
					testHookAbandonSplit = nil
					t.Fatal(err)
				}
			}
			testHookAbandonSplit = nil
			if abandoned < 0 {
				t.Fatalf("no split at level %d happened", tc.level)
			}

			flagged := flaggedPages(t, bt)
			if len(flagged) != 1 {
				for _, b := range flagged {
					op := pageOpaque(t, bt, b)
					t.Logf("flagged %d level %d next %d prev %d", b, op.Level, op.Next, op.Prev)
				}
				t.Fatalf("flagged pages after the abandoned split: %v, want one", flagged)
			}
			left := flagged[0]
			right := pageOpaque(t, bt, left).Next
			if tc.name == "root" {
				if meta, err := bt.readMeta(); err != nil || meta.Root != left {
					t.Fatalf("metapage root %d (%v), want the abandoned split's left half %d", meta.Root, err, left)
				}
			} else if hasDownlinkAtLevel(t, bt, right, tc.level+1) {
				t.Fatalf("right half %d already has a downlink", right)
			}
			checkKeys(t, bt, inserted)

			// A writer reaching the left half finishes the split: a second
			// copy of the left half's first key, with another TID. (A key
			// past the left half could take the rightmost-leaf fast path
			// straight to the right half, which PG's fastpath does too.)
			if err := bt.Insert(firstKeyOn(t, bt, left), storage.ItemPointer{Block: 9999, Offset: 1}); err != nil {
				t.Fatal(err)
			}
			if f := flaggedPages(t, bt); len(f) != 0 {
				t.Fatalf("pages still flagged after a writer reached them: %v", f)
			}
			if !hasDownlinkAtLevel(t, bt, right, tc.level+1) {
				t.Fatalf("right half %d has no downlink after the split was finished", right)
			}
			if tc.name == "root" {
				if meta, err := bt.readMeta(); err != nil || meta.Level != 1 || meta.Root == left {
					t.Fatalf("metapage root %d level %d (%v), want a new level-1 root", meta.Root, meta.Level, err)
				}
			}
			checkKeys(t, bt, inserted)
		})
	}
}

// TestGetStackBufFinishesIncompleteSplit pins _bt_getstackbuf's half of gap
// 5: a split's parent search that meets an incomplete split on the parent
// level finishes it before looking for the downlink, so the downlink is never
// inserted into half of a split.
func TestGetStackBufFinishesIncompleteSplit(t *testing.T) {
	bt, cleanup := newFinishSplitTree(t)
	defer cleanup()
	keys := rand.New(rand.NewSource(11)).Perm(8000)
	armed := true
	testHookAbandonSplit = func(level uint32) bool {
		if armed && level == 1 {
			armed = false
			return true
		}
		return false
	}
	var abandoned bool
	for _, n := range keys {
		err := bt.Insert(finishKey(n), finishTID(n))
		if errors.Is(err, errSplitAbandoned) {
			abandoned = true
			break
		}
		if err != nil {
			testHookAbandonSplit = nil
			t.Fatal(err)
		}
	}
	testHookAbandonSplit = nil
	if !abandoned {
		t.Fatal("no internal split happened")
	}
	flagged := flaggedPages(t, bt)
	if len(flagged) != 1 {
		t.Fatalf("flagged pages: %v, want one", flagged)
	}
	right := pageOpaque(t, bt, flagged[0]).Next
	child := firstDownlink(t, bt, right)
	// No path hint: the search starts at the leftmost level-1 page and moves
	// right through the flagged page to the right half.
	parent, _, err := bt.getStackBuf(nil, child, 0)
	if err != nil {
		t.Fatal(err)
	}
	if parent != right {
		t.Errorf("getStackBuf found child %d under %d, want %d", child, parent, right)
	}
	if f := flaggedPages(t, bt); len(f) != 0 {
		t.Errorf("pages still flagged after getStackBuf passed them: %v", f)
	}
	if !hasDownlinkAtLevel(t, bt, right, 2) {
		t.Errorf("right half %d has no downlink after getStackBuf", right)
	}
}

func newFinishSplitTree(t *testing.T) (*BTree, func()) {
	t.Helper()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatal(err)
	}
	bt, err := Create(pool, storage.RelFileNode{DBOid: 1, RelOid: 9320, Fork: storage.MainFork})
	if err != nil {
		t.Fatal(err)
	}
	return bt, func() {
		testHookAbandonSplit = nil
		_ = pool.Close()
		_ = mgr.Close()
	}
}

func finishKey(n int) []byte { return EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n))) }

func finishTID(n int) storage.ItemPointer {
	return storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}
}

func pageOpaque(t *testing.T, bt *BTree, blk storage.BlockNumber) BTPageOpaque {
	t.Helper()
	s, err := bt.pinR(blk)
	if err != nil {
		t.Fatal(err)
	}
	defer bt.unpinR(s)
	return readOpaque(s.Page())
}

// flaggedPages lists the live pages carrying BTIncompleteSplit.
func flaggedPages(t *testing.T, bt *BTree) []storage.BlockNumber {
	t.Helper()
	n, err := bt.pool.NBlocks(bt.rel)
	if err != nil {
		t.Fatal(err)
	}
	var out []storage.BlockNumber
	for blk := storage.BlockNumber(1); blk < n; blk++ {
		if op := pageOpaque(t, bt, blk); op.HasIncompleteSplit() && !op.IsDeleted() {
			out = append(out, blk)
		}
	}
	return out
}

// hasDownlinkAtLevel reports whether a page on level holds a downlink to child.
func hasDownlinkAtLevel(t *testing.T, bt *BTree, child storage.BlockNumber, level uint32) bool {
	t.Helper()
	meta, err := bt.readMeta()
	if err != nil {
		t.Fatal(err)
	}
	if meta.Level < level {
		return false
	}
	blk, _, err := bt.leftmostAtLevel(level)
	if err != nil {
		t.Fatal(err)
	}
	for blk != storage.InvalidBlockNumber {
		s, err := bt.pinR(blk)
		if err != nil {
			t.Fatal(err)
		}
		items, err := bt.format().pageItems(s.Page())
		next := readOpaque(s.Page()).Next
		bt.unpinR(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if it.ptr.Block == child {
				return true
			}
		}
		blk = next
	}
	return false
}

func firstKeyOn(t *testing.T, bt *BTree, blk storage.BlockNumber) []byte {
	t.Helper()
	s, err := bt.pinR(blk)
	if err != nil {
		t.Fatal(err)
	}
	defer bt.unpinR(s)
	items, err := bt.format().pageItems(s.Page())
	if err != nil || len(items) == 0 {
		t.Fatalf("page %d items: %d, %v", blk, len(items), err)
	}
	return append([]byte(nil), items[0].key...)
}

func firstDownlink(t *testing.T, bt *BTree, blk storage.BlockNumber) storage.BlockNumber {
	t.Helper()
	s, err := bt.pinR(blk)
	if err != nil {
		t.Fatal(err)
	}
	defer bt.unpinR(s)
	items, err := bt.format().pageItems(s.Page())
	if err != nil || len(items) == 0 {
		t.Fatalf("page %d items: %d, %v", blk, len(items), err)
	}
	return items[0].ptr.Block
}

// checkKeys requires every inserted key to be found with its TID and a full
// scan to return them once each, in order.
func checkKeys(t *testing.T, bt *BTree, inserted map[int]bool) {
	t.Helper()
	for n := range inserted {
		got, ok, err := bt.Search(finishKey(n))
		if err != nil || !ok {
			t.Fatalf("Search(%d) = %v, %v", n, ok, err)
		}
		if got != finishTID(n) && got.Block != 9999 {
			t.Fatalf("Search(%d) = %+v, want %+v", n, got, finishTID(n))
		}
	}
	var prev []byte
	seen := 0
	if err := bt.RangeScan(nil, nil, func(k []byte, _ storage.ItemPointer) (bool, error) {
		if prev != nil && bytes.Compare(prev, k) > 0 {
			return false, fmt.Errorf("scan out of order at %d", seen)
		}
		prev = append(prev[:0], k...)
		seen++
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen < len(inserted) {
		t.Fatalf("full scan returned %d keys, want at least %d", seen, len(inserted))
	}
}

// TestIncompleteSplitsFinishedConcurrently leaves every 10th split incomplete
// while 16 handles insert interleaved keys in random order, so writers meet
// half splits — on leaves and on internal pages — and finish them while
// other writers split around them, several possibly racing for the same one.
// Afterwards a second insert of every 40th key reaches more of them, and
// CompleteDeferredSplits finishes the rest. No page may stay flagged, no
// writer may deadlock, and every key must be found and scanned in order.
func TestIncompleteSplitsFinishedConcurrently(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts thousands of wide keys from several goroutines")
	}
	bt, cleanup := newFinishSplitTree(t)
	defer cleanup()
	const handles, perHandle = 16, 800
	hs := []*BTree{bt}
	for len(hs) < handles {
		h, err := Open(bt.pool, bt.rel)
		if err != nil {
			t.Fatal(err)
		}
		hs = append(hs, h)
	}
	var splits, abandoned atomic.Int64
	testHookAbandonSplit = func(uint32) bool {
		if splits.Add(1)%10 == 0 {
			abandoned.Add(1)
			return true
		}
		return false
	}
	var wg sync.WaitGroup
	errCh := make(chan error, handles)
	// failed[h] lists the keys whose Insert on handle h returned
	// errSplitAbandoned. That error does not mean the key went in. A writer
	// that meets a half split on its way down finishes it first
	// (insertIntoBlock -> finishSplit, as _bt_moveright calls
	// _bt_finish_split), and when the hook abandons the parent split that
	// finish cascades into, the error returns before the writer's own key is
	// placed, just as an ERROR there aborts the insert in PostgreSQL. Only the
	// writer whose OWN split was abandoned has its key on the page. Nightly
	// AI-20261019-001758-002 was this test treating every failed insert as
	// landed: about 8% of runs under host load, never on an idle host.
	failed := make([][]int, handles)
	for h := range hs {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			for _, i := range rand.New(rand.NewSource(int64(h) + 1)).Perm(perHandle) {
				n := i*handles + h
				err := hs[h].Insert(finishKey(n), finishTID(n))
				if errors.Is(err, errSplitAbandoned) {
					failed[h] = append(failed[h], n)
					continue
				}
				if err != nil {
					errCh <- fmt.Errorf("handle %d insert %d: %w", h, n, err)
					return
				}
			}
		}(h)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("inserts did not finish in 2 minutes (deadlock or livelock)")
	}
	testHookAbandonSplit = nil
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if abandoned.Load() == 0 {
		t.Fatal("no split was left incomplete")
	}
	inserted := map[int]bool{}
	for n := 0; n < handles*perHandle; n++ {
		inserted[n] = true
	}
	for n := 0; n < handles*perHandle; n += 40 {
		if err := bt.Insert(finishKey(n), storage.ItemPointer{Block: 9999, Offset: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// A failed insert is retried, as the client would, and must then succeed.
	// The retry uses the sweep's TID, which checkKeys accepts, so a key that
	// did land the first time is not re-inserted as an exact duplicate.
	retried := 0
	for _, keys := range failed {
		for _, n := range keys {
			if err := bt.Insert(finishKey(n), storage.ItemPointer{Block: 9999, Offset: 1}); err != nil {
				t.Fatalf("retry of failed insert %d: %v", n, err)
			}
			retried++
		}
	}
	swept, err := bt.CompleteDeferredSplits()
	if err != nil {
		t.Fatal(err)
	}
	if f := flaggedPages(t, bt); len(f) != 0 {
		t.Fatalf("pages still flagged: %v", f)
	}
	checkKeys(t, bt, inserted)
	t.Logf("splits %d, left incomplete %d, finished by the final sweep %d (the rest by writers on contact), failed inserts retried %d",
		splits.Load(), abandoned.Load(), swept, retried)
}
