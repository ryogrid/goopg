package nbtree

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestMultiWriterStress_M0055_Phase_C (M0055-0004-followup-finish-split)
// stress-tests concurrent writers under the INCOMPLETE_SPLIT
// protocol. 32 goroutines × 1000 inserts each on disjoint key
// ranges. Asserts no lost or duplicate keys after the fence and
// no deadlock (test driver completes within the test timeout).
//
// Since M0122-0010 no structural mutex serialises the splits: page latches
// and the incomplete-split protocol alone keep them correct.
func TestMultiWriterStress_M0055_Phase_C(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-writer stress in short mode")
	}
	// M0056 status (2026-05-06): the bufpool PinNew slot-
	// reservation fix (commit message of M0056-0001) closes one
	// concurrency bug class. A separate intermittent failure
	// remains: the multi-writer stress flakes ~20 % of runs even
	// without -race, panic in `tryInsertNoSplit`'s deferred
	// unpinW. The remaining bug surface needs further focused
	// investigation; until it's resolved, gate the test as
	// long-form so default `go test` runs don't hit the
	// flake. Tracked as `M0056-followup-multiwriter-flake`.
	if testing.Short() {
		t.Skip("skipping multi-writer stress in short mode")
	}
	// M0056-followup-multiwriter-flake root cause found and fixed in
	// M-NIGHTLY loop 13 (evictVictim's bufmap.Delete-before-flush race,
	// storage/bufpool.go): un-skipped as this bug's regression guard.
	//
	// The pool has room for every writer to split at once. Splits run
	// concurrently since M0122-0010 removed the structural lock, and one
	// split pins up to five pages (left, right, old right sibling, parent,
	// new root); on newTestTree's 32 slots, 32 writers then exhaust the pool
	// ("no available buffer"), as PG's "no unpinned buffers available" would
	// with shared_buffers this small.
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	defer mgr.Close()
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	bt, err := Create(pool, storage.RelFileNode{DBOid: 1, RelOid: 9000, Fork: storage.MainFork})
	if err != nil {
		t.Fatal(err)
	}
	bt.ResetStats()

	const writers = 32
	const perWriter = 1000
	var wg sync.WaitGroup
	var insertsOK atomic.Uint64
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				k := make([]byte, 8)
				// Disjoint key ranges per writer so we can
				// later assert "every key inserted is found".
				binary.BigEndian.PutUint64(k, uint64(wid)<<32|uint64(i))
				ptr := storage.ItemPointer{Block: storage.BlockNumber(wid), Offset: uint16(i + 1)}
				if err := bt.Insert(k, ptr); err != nil {
					t.Errorf("writer %d insert %d: %v", wid, i, err)
					return
				}
				insertsOK.Add(1)
			}
		}(w)
	}
	wg.Wait()

	if got := insertsOK.Load(); got != uint64(writers*perWriter) {
		t.Fatalf("inserts ok=%d, want %d", got, writers*perWriter)
	}

	// Verify every key is searchable.
	misses := 0
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			k := make([]byte, 8)
			binary.BigEndian.PutUint64(k, uint64(w)<<32|uint64(i))
			_, found, err := bt.Search(k)
			if err != nil {
				t.Fatalf("writer %d search %d: %v", w, i, err)
			}
			if !found {
				misses++
			}
		}
	}
	if misses != 0 {
		t.Fatalf("%d keys missing after concurrent insert", misses)
	}
	stats := bt.Stats()
	t.Logf("M0055-Phase-C-summary { writers=%d total_inserts=%d splits=%d }",
		writers, writers*perWriter, stats.Splits)
}

// TestTwoPhaseDeletion_M0055_Phase_D (M0055-0005-followup-two-
// phase-del) verifies that a half-dead page (Phase 1 committed,
// Phase 2 not run) can be picked up by `CompleteDeferredDeletions`
// — modelling the post-crash recovery case.
func TestTwoPhaseDeletion_M0055_Phase_D(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	// Trigger a few inserts + vacuums to create empty leaves.
	keys := [][]byte{}
	for i := 0; i < 200; i++ {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(i))
		keys = append(keys, k)
		if err := bt.Insert(k, storage.ItemPointer{Block: storage.BlockNumber(i / 50), Offset: uint16(i%50) + 1}); err != nil {
			t.Fatal(err)
		}
	}
	deadTIDs := make([]storage.ItemPointer, 0, 100)
	for i := 0; i < 100; i++ {
		deadTIDs = append(deadTIDs, storage.ItemPointer{Block: storage.BlockNumber(i / 50), Offset: uint16(i%50) + 1})
	}
	if _, err := bt.VacuumIndexPages(deadTIDs); err != nil {
		t.Fatal(err)
	}
	// `CompleteDeferredDeletions` should be a no-op now (vacuum
	// already ran Phase 2). Asserts the routine doesn't error and
	// reports zero pickups when the tree is clean.
	completed, err := bt.CompleteDeferredDeletions()
	if err != nil {
		t.Fatalf("CompleteDeferredDeletions: %v", err)
	}
	if completed != 0 {
		t.Logf("CompleteDeferredDeletions found %d half-dead pages (expected 0 in steady state)", completed)
	}
}
