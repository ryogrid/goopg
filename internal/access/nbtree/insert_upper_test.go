package nbtree

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestInternalInsertFinishesChildSplit pins _bt_insertonpg's internal-page
// arm: a downlink inserted into an internal page without splitting it is
// logged through the XLOG_BTREE_INSERT_UPPER hook naming the child whose split
// it finishes, and that child no longer carries BTIncompleteSplit — the flag
// is cleared with the insert, not by a separate record afterwards.
func TestInternalInsertFinishesChildSplit(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()
	type call struct{ parent, child storage.BlockNumber }
	var calls []call
	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots: 256,
		LogBtreeInsertUpper: func(rel storage.RelFileNode, blk storage.BlockNumber, offnum uint16, item []byte, childBlk storage.BlockNumber) (storage.LSN, error) {
			if offnum == 0 || len(item) == 0 {
				t.Errorf("upper insert into %d: offnum %d, %d item bytes", blk, offnum, len(item))
			}
			calls = append(calls, call{blk, childBlk})
			return 0, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	bt, err := Create(pool, storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3000; n++ {
		key := EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n)))
		if err := bt.Insert(key, storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls) == 0 {
		t.Fatal("no downlink went through the INSERT_UPPER hook")
	}
	for _, c := range calls {
		slot, err := bt.pinR(c.child)
		if err != nil {
			t.Fatal(err)
		}
		op := readOpaque(slot.Page())
		bt.unpinR(slot)
		if op.Flags&BTIncompleteSplit != 0 {
			t.Errorf("child %d of an upper insert into %d still has BTIncompleteSplit", c.child, c.parent)
		}
		pslot, err := bt.pinR(c.parent)
		if err != nil {
			t.Fatal(err)
		}
		pop := readOpaque(pslot.Page())
		bt.unpinR(pslot)
		if pop.IsLeaf() {
			t.Errorf("upper insert logged for leaf page %d", c.parent)
		}
	}
}
