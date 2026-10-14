package nbtree

import (
	"bytes"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestStructuralLockInteriorSplitWorkload drives the workload the structural
// lock existed for: several handles of one index (one per backend)
// inserting interleaved keys in random order, with wide keys so that leaves
// and then interior pages split while other handles descend and split
// through them. Afterwards every key must be found with its TID, a full scan
// must return each key once in order, and the tree must have grown interior
// levels (meta level >= 2), so interior splits really happened.
func TestStructuralLockInteriorSplitWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("inserts thousands of wide keys from several goroutines")
	}
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = pool.Close()
		_ = mgr.Close()
	}()
	// Measured 2026-10-14 on 16 CPUs before PG's lock coupling, with the
	// shared structural lock removed: one 8-handle round failed about 1
	// time in 13, mostly as "index out of range [-1]" in insertIntoBlock;
	// 40 rounds of 16 handles failed 15 invocations out of 15. Since
	// 2026-10-15 no structural lock exists (M0122-0010 gap 8). Odd rounds
	// insert through one handle shared by every goroutine, as when one
	// handle serves several workers.
	for round := 0; round < structuralRounds; round++ {
		rel := storage.RelFileNode{DBOid: 1, RelOid: uint32(9100 + round), Fork: storage.MainFork}
		if err := structuralWorkloadRound(pool, rel, int64(round), round%2 == 1); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
	}
}

const structuralRounds = 40

// structuralWorkloadRound is one round of TestStructuralLockInteriorSplitWorkload
// on a fresh relation.
func structuralWorkloadRound(pool *storage.Pool, rel storage.RelFileNode, seed int64, oneHandle bool) error {
	first, err := Create(pool, rel)
	if err != nil {
		return err
	}
	const handles, perHandle = 16, 600
	hs := []*BTree{first}
	for len(hs) < handles {
		h := first
		if !oneHandle {
			if h, err = Open(pool, rel); err != nil {
				return err
			}
		}
		hs = append(hs, h)
	}
	key := func(n int) []byte {
		return EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n)))
	}
	tid := func(n int) storage.ItemPointer {
		return storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}
	}
	var wg sync.WaitGroup
	errCh := make(chan error, handles)
	for h := range hs {
		wg.Add(1)
		go func(h int) {
			defer wg.Done()
			order := rand.New(rand.NewSource(seed*handles + int64(h) + 1)).Perm(perHandle)
			for _, i := range order {
				n := i*handles + h
				if err := hs[h].Insert(key(n), tid(n)); err != nil {
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
		return fmt.Errorf("inserts did not finish in 2 minutes (deadlock or livelock)")
	}
	close(errCh)
	for err := range errCh {
		return err
	}

	total := handles * perHandle
	missing := 0
	var firstMissing error
	for n := 0; n < total; n++ {
		got, ok, err := first.Search(key(n))
		if err != nil {
			return fmt.Errorf("Search(%d): %w", n, err)
		}
		if !ok || got != tid(n) {
			if firstMissing == nil {
				firstMissing = fmt.Errorf("Search(%d) = %+v, %v; want %+v", n, got, ok, tid(n))
			}
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d of %d keys not found with their TID; first: %v", missing, total, firstMissing)
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
	if seen != total {
		return fmt.Errorf("full scan returned %d keys, want %d", seen, total)
	}
	meta, err := first.readMeta()
	if err != nil {
		return err
	}
	if meta.Level < 2 {
		return fmt.Errorf("tree level %d: the workload did not split interior pages", meta.Level)
	}
	return nil
}
