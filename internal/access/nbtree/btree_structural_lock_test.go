package nbtree

import (
	"fmt"
	"sync"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestOpenedHandlesConcurrentlyInsertAcrossSplits inserts distinct keys
// through two independently opened handles of one index, as two backends
// would, and checks every pointer after several leaf splits. It is a
// functional smoke test: its per-handle keys ascend, so it only drives
// rightmost-page splits. The concurrency pins are the stress tests in
// btree_structural_race_test.go and btree_vacuum_split_race_test.go. (Before
// M0122-0010 this file also pinned that both handles shared one structural
// mutex; that mutex is gone.)
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
