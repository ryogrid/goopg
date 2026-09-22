package nbtree

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestOpenedHandlesShareStructuralLock pins the cross-connection half of the
// structural-write protocol. Each backend opens its own BTree handle, but the
// split and unlink paths must not interleave for one relation.
func TestOpenedHandlesShareStructuralLock(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()

	other, err := Open(pool, bt.rel)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	if bt.sharedSplitMu == nil || other.sharedSplitMu == nil || bt.sharedSplitMu != other.sharedSplitMu {
		t.Fatal("opened handles do not share a relation structural lock")
	}

	bt.lockStructural()
	acquired := make(chan struct{})
	go func() {
		other.lockStructural()
		close(acquired)
		other.unlockStructural()
	}()
	select {
	case <-acquired:
		t.Fatal("second handle acquired the structural lock while first held it")
	case <-time.After(20 * time.Millisecond):
	}
	bt.unlockStructural()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("second handle did not acquire structural lock after release")
	}
}

// TestOpenedHandlesConcurrentlyInsertAcrossSplits exercises the shared gate
// through its production call sites. The earlier ownership test proves that
// both handles receive one mutex; this test proves their split paths can use
// it while inserting distinct keys into the same relation.
//
// Read it as a functional smoke test, NOT as a race pin. Measured 2026-09-22:
// with `sharedSplitMu` removed from the constructors — i.e. each handle back on
// its own mutex, the bug this protocol fixes — this test still PASSES under
// `-race -count=3`. Its per-handle keys ascend, so the splits it drives are
// rightmost-page splits, and the buffer-pool page locks keep that path
// consistent on their own. `TestOpenedHandlesShareStructuralLock` is the
// assertion that actually fails without the fix; if you want a reproducer for
// the interleaved-structural-change failure itself, it has to be written, and
// no existing test is one.
func TestOpenedHandlesConcurrentlyInsertAcrossSplits(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()

	other, err := Open(pool, bt.rel)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	const perHandle = 450 // enough combined rows to split several leaf pages
	handles := []*BTree{bt, other}
	errCh := make(chan error, len(handles))
	var wg sync.WaitGroup
	for handleID, handle := range handles {
		wg.Add(1)
		go func(handleID int, handle *BTree) {
			defer wg.Done()
			for i := 0; i < perHandle; i++ {
				key := int32(2*i + handleID)
				ptr := storage.ItemPointer{Block: storage.BlockNumber(handleID + 1), Offset: uint16(i + 1)}
				if err := handle.Insert(EncodeInt4(key), ptr); err != nil {
					errCh <- fmt.Errorf("handle %d insert %d: %w", handleID, key, err)
					return
				}
			}
		}(handleID, handle)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	for handleID := range handles {
		for i := 0; i < perHandle; i++ {
			key := int32(2*i + handleID)
			got, ok, err := bt.Search(EncodeInt4(key))
			if err != nil {
				t.Fatalf("Search(%d): %v", key, err)
			}
			if !ok {
				t.Fatalf("Search(%d): not found after cross-handle split", key)
			}
			want := storage.ItemPointer{Block: storage.BlockNumber(handleID + 1), Offset: uint16(i + 1)}
			if got != want {
				t.Fatalf("Search(%d) = %+v, want %+v", key, got, want)
			}
		}
	}
}
