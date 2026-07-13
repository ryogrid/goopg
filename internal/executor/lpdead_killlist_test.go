package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// C3-S2: heapChainDeadToAll is the kill-list admission oracle — it must
// only admit entries whose WHOLE HOT chain is provably dead to all
// snapshots (storage.TupleDeadToAll per member; design D6).
func TestHeapChainDeadToAllOracle(t *testing.T) {
	const oldestXmin = storage.TransactionID(10)

	newPage := func() storage.Page {
		p := make(storage.Page, storage.BlockSize)
		if err := storage.InitPage(p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("dead_single", func(t *testing.T) {
		p := newPage()
		tup := storage.NewHeapTuple(1, 5, []byte("d")) // xmax 5 < 10
		slot, err := storage.PageAddHeapTuple(p, tup)
		if err != nil {
			t.Fatal(err)
		}
		if !heapChainDeadToAll(p, slot, oldestXmin) {
			t.Fatal("single dead tuple: want dead-to-all")
		}
	})
	t.Run("live_single", func(t *testing.T) {
		p := newPage()
		tup := storage.NewHeapTuple(1, storage.InvalidTransactionID, []byte("l"))
		slot, err := storage.PageAddHeapTuple(p, tup)
		if err != nil {
			t.Fatal(err)
		}
		if heapChainDeadToAll(p, slot, oldestXmin) {
			t.Fatal("live tuple admitted to kill list")
		}
	})
	t.Run("recent_deleter_above_horizon", func(t *testing.T) {
		p := newPage()
		tup := storage.NewHeapTuple(1, 50, []byte("r")) // xmax 50 >= 10
		slot, err := storage.PageAddHeapTuple(p, tup)
		if err != nil {
			t.Fatal(err)
		}
		if heapChainDeadToAll(p, slot, oldestXmin) {
			t.Fatal("recently-deleted tuple admitted (deleter above horizon)")
		}
	})
	t.Run("lock_only_xmax", func(t *testing.T) {
		p := newPage()
		tup := storage.NewHeapTuple(1, 5, []byte("k"))
		tup.Header.Infomask |= storage.HeapXmaxLockOnly
		slot, err := storage.PageAddHeapTuple(p, tup)
		if err != nil {
			t.Fatal(err)
		}
		if heapChainDeadToAll(p, slot, oldestXmin) {
			t.Fatal("lock-only tuple admitted")
		}
	})
	t.Run("chain_with_live_successor", func(t *testing.T) {
		p := newPage()
		// root: dead, HOT-updated -> slot 2 (live).
		root := storage.NewHeapTuple(1, 5, []byte("root"))
		root.Header.Infomask |= storage.HeapHotUpdated
		root.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
		s1, err := storage.PageAddHeapTuple(p, root)
		if err != nil {
			t.Fatal(err)
		}
		live := storage.NewHeapTuple(5, storage.InvalidTransactionID, []byte("succ"))
		if _, err := storage.PageAddHeapTuple(p, live); err != nil {
			t.Fatal(err)
		}
		if heapChainDeadToAll(p, s1, oldestXmin) {
			t.Fatal("chain with live successor admitted")
		}
	})
	t.Run("chain_all_dead", func(t *testing.T) {
		p := newPage()
		root := storage.NewHeapTuple(1, 4, []byte("root"))
		root.Header.Infomask |= storage.HeapHotUpdated
		root.Header.CTID = storage.ItemPointer{Block: 0, Offset: 2}
		s1, err := storage.PageAddHeapTuple(p, root)
		if err != nil {
			t.Fatal(err)
		}
		succ := storage.NewHeapTuple(4, 6, []byte("succ")) // also dead
		if _, err := storage.PageAddHeapTuple(p, succ); err != nil {
			t.Fatal(err)
		}
		if !heapChainDeadToAll(p, s1, oldestXmin) {
			t.Fatal("fully-dead chain rejected")
		}
	})
	t.Run("unused_slot_conservative", func(t *testing.T) {
		p := newPage()
		if heapChainDeadToAll(p, 1, oldestXmin) {
			t.Fatal("nonexistent slot admitted")
		}
	})
}
