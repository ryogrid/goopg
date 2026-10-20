package nbtree

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestUnlinkRechecksEmptinessUnderTheTargetLatch pins the second half of
// M-NIGHTLY AI-20261020-013101-001. unlinkEmptyLeaf's first emptiness check
// releases the leaf's latch, and writers still insert into a phase-1
// (BTDeleted|BTHalfDead) leaf because it keeps its key range until the parent
// is retargeted. _bt_pagedel tests emptiness again under the target's
// exclusive lock; without that test the tombstone rewrite destroys whatever
// was inserted in between. The hook inserts into the emptied leaf exactly in
// that window, and the key must survive the vacuum.
func TestUnlinkRechecksEmptinessUnderTheTargetLatch(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	var lsn atomic.Uint64
	nextLSN := func() storage.LSN { return storage.LSN(lsn.Add(1)) }
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots: 256,
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
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9500, Fork: storage.MainFork}
	bt, err := Create(pool, rel)
	if err != nil {
		t.Fatal(err)
	}
	key := func(n int) []byte { return EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n))) }
	tid := func(n int) storage.ItemPointer {
		return storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}
	}
	for n := 0; n < 400; n++ {
		if err := bt.Insert(key(n), tid(n)); err != nil {
			t.Fatal(err)
		}
	}
	// Vacuum keys 100..199: the leaves wholly inside that range go empty.
	var dead []storage.ItemPointer
	for n := 100; n < 200; n++ {
		dead = append(dead, tid(n))
	}
	revived := -1
	live := storage.ItemPointer{Block: 999, Offset: 1}
	bt.beforeUnlinkPins = func(blk storage.BlockNumber) {
		if revived >= 0 {
			return
		}
		for n := 100; n < 200; n++ {
			if leaf, _, err := bt.descendToLeaf(key(n)); err == nil && leaf == blk {
				if err := bt.Insert(key(n), live); err != nil {
					t.Errorf("insert into the emptied leaf: %v", err)
				}
				revived = n
				return
			}
		}
	}
	if _, err := bt.VacuumIndexPages(dead); err != nil {
		t.Fatal(err)
	}
	if revived < 0 {
		t.Fatal("no leaf was emptied: the test did not reach unlinkEmptyLeaf")
	}
	got, ok, err := bt.Search(key(revived))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != live {
		t.Fatalf("Search(%d) = %+v, %v after vacuum; want %+v: the deletion destroyed a tuple inserted after its first emptiness check", revived, got, ok, live)
	}
}
