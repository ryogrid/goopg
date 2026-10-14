package nbtree

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestStructuralLockVacuumWithSplits drives page deletion concurrently with
// splits, the other half of what the structural lock protects (the split half
// is TestStructuralLockInteriorSplitWorkload). The key space is cut into
// segments: the even ones are inserted first and then vacuumed away segment by
// segment — their pages empty and are deleted — while eight handles insert the
// odd segments in random order, splitting the pages next to the ones being
// deleted. Afterwards every odd key is found with its TID, no even key is found,
// and a full scan returns exactly the odd keys, in order.
//
// NBTREE_UNSHARED_STRUCTURAL_LOCK=1 gives every handle (the vacuum handle
// included) its own mutex, to measure what still depends on the shared lock.
func TestStructuralLockVacuumWithSplits(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts and vacuums thousands of wide keys from several goroutines")
	}
	unshared := os.Getenv("NBTREE_UNSHARED_STRUCTURAL_LOCK") == "1"
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	var lsn atomic.Uint64
	nextLSN := func() storage.LSN { return storage.LSN(lsn.Add(1)) }
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots: 1024,
		LogBtreeUnlinkPage: func(storage.RelFileNode, storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
			return nextLSN(), nil
		},
		LogBtreeMarkPageHalfDead: func(storage.RelFileNode, storage.BtreeMarkPageHalfDeadRequest) (storage.LSN, error) {
			return nextLSN(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for round := 0; round < 10; round++ {
		rel := storage.RelFileNode{DBOid: 1, RelOid: uint32(9400 + round), Fork: storage.MainFork}
		if err := vacuumSplitRound(pool, rel, int64(round), unshared); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

func vacuumSplitRound(pool *storage.Pool, rel storage.RelFileNode, seed int64, unshared bool) error {
	const segments, perSegment, inserters = 40, 100, 8
	key := func(n int) []byte { return EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n))) }
	tid := func(n int) storage.ItemPointer {
		return storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}
	}
	victim := func(n int) bool { return (n/perSegment)%2 == 0 }

	first, err := Create(pool, rel)
	if err != nil {
		return err
	}
	for n := 0; n < segments*perSegment; n++ {
		if victim(n) {
			if err := first.Insert(key(n), tid(n)); err != nil {
				return err
			}
		}
	}
	open := func() (*BTree, error) {
		h, err := Open(pool, rel)
		if err == nil && unshared {
			h.sharedSplitMu = &sync.Mutex{}
		}
		return h, err
	}
	var survivors []int
	for n := 0; n < segments*perSegment; n++ {
		if !victim(n) {
			survivors = append(survivors, n)
		}
	}
	rand.New(rand.NewSource(seed)).Shuffle(len(survivors), func(i, j int) { survivors[i], survivors[j] = survivors[j], survivors[i] })

	var wg sync.WaitGroup
	errCh := make(chan error, inserters+1)
	for h := 0; h < inserters; h++ {
		bt, err := open()
		if err != nil {
			return err
		}
		wg.Add(1)
		go func(h int, bt *BTree) {
			defer wg.Done()
			for i := h; i < len(survivors); i += inserters {
				n := survivors[i]
				if err := bt.Insert(key(n), tid(n)); err != nil {
					errCh <- fmt.Errorf("insert %d: %w", n, err)
					return
				}
			}
		}(h, bt)
	}
	vac, err := open()
	if err != nil {
		return err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		order := rand.New(rand.NewSource(seed + 1000)).Perm(segments / 2)
		for _, s := range order {
			var dead []storage.ItemPointer
			for n := 2 * s * perSegment; n < (2*s+1)*perSegment; n++ {
				dead = append(dead, tid(n))
			}
			if _, err := vac.VacuumIndexPages(dead); err != nil {
				errCh <- fmt.Errorf("vacuum segment %d: %w", 2*s, err)
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		return fmt.Errorf("did not finish in 2 minutes (deadlock or livelock)")
	}
	close(errCh)
	for err := range errCh {
		return err
	}

	for n := 0; n < segments*perSegment; n++ {
		got, ok, err := first.Search(key(n))
		if err != nil {
			return fmt.Errorf("Search(%d): %w", n, err)
		}
		if victim(n) && ok {
			return fmt.Errorf("vacuumed key %d still found", n)
		}
		if !victim(n) && (!ok || got != tid(n)) {
			return fmt.Errorf("Search(%d) = %+v, %v; want %+v", n, got, ok, tid(n))
		}
	}
	var prev []byte
	seen := 0
	if err := first.RangeScan(nil, nil, func(k []byte, _ storage.ItemPointer) (bool, error) {
		if prev != nil && bytes.Compare(prev, k) >= 0 {
			return false, fmt.Errorf("scan out of order after %d keys", seen)
		}
		prev = append(prev[:0], k...)
		seen++
		return true, nil
	}); err != nil {
		return err
	}
	if seen != len(survivors) {
		return fmt.Errorf("full scan returned %d keys, want %d", seen, len(survivors))
	}
	// The round only means something if vacuum really deleted pages.
	nblocks, err := pool.Manager().NBlocks(rel)
	if err != nil {
		return err
	}
	deleted := 0
	for blk := storage.BlockNumber(1); blk < nblocks; blk++ {
		s, err := first.pinR(blk)
		if err != nil {
			return err
		}
		if op := readOpaque(s.Page()); op.IsDeleted() {
			deleted++
		}
		first.unpinR(s)
	}
	if deleted == 0 {
		return fmt.Errorf("no page was deleted: the round did not exercise page deletion")
	}
	return nil
}
