package nbtree

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestVacuumRefusesIncompleteSplitHalves pins _bt_pagedel's refusal to delete
// either half of an incomplete split. An emptied leaf whose left sibling still
// carries BTIncompleteSplit and points at it is the right half of an
// unfinished split — it has no downlink of its own yet — so vacuum must leave
// it linked and live (_bt_leftsib_splitflag). The same leaf with the flag
// cleared is deleted, so the refusal is what keeps it. Both the WAL path
// (emitters wired) and the FPI fallback are checked.
func TestVacuumRefusesIncompleteSplitHalves(t *testing.T) {
	for _, wal := range []bool{true, false} {
		for _, flagged := range []bool{true, false} {
			t.Run(fmt.Sprintf("wal=%v/flagged=%v", wal, flagged), func(t *testing.T) {
				mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
				defer mgr.Close()
				cfg := storage.PoolConfig{Slots: 256}
				if wal {
					cfg.LogBtreeUnlinkPage = func(storage.RelFileNode, storage.BtreeUnlinkPageRequest) (storage.LSN, error) { return 10, nil }
					cfg.LogBtreeMarkPageHalfDead = func(storage.RelFileNode, storage.BtreeMarkPageHalfDeadRequest) (storage.LSN, error) { return 10, nil }
				}
				pool, err := storage.NewPool(mgr, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				bt, err := Create(pool, storage.RelFileNode{DBOid: 1, RelOid: 9300, Fork: storage.MainFork})
				if err != nil {
					t.Fatal(err)
				}
				for n := 0; n < 3000; n++ {
					key := EncodeVarchar([]byte(fmt.Sprintf("%08d-%0190d", n, n)))
					if err := bt.Insert(key, storage.ItemPointer{Block: storage.BlockNumber(n/100 + 1), Offset: uint16(n%100 + 1)}); err != nil {
						t.Fatal(err)
					}
				}
				// The third leaf from the left, and its left sibling.
				leaf, err := bt.findLeftmostLeaf()
				if err != nil {
					t.Fatal(err)
				}
				var left storage.BlockNumber
				for i := 0; i < 2; i++ {
					s, err := bt.pinR(leaf)
					if err != nil {
						t.Fatal(err)
					}
					left, leaf = leaf, readOpaque(s.Page()).Next
					bt.unpinR(s)
				}
				if flagged {
					s, err := bt.pinW(left)
					if err != nil {
						t.Fatal(err)
					}
					op := readOpaque(s.Page())
					op.Flags |= BTIncompleteSplit
					writeOpaque(s.Page(), op)
					bt.pool.MarkDirty(s)
					bt.unpinW(s)
				}
				s, err := bt.pinR(leaf)
				if err != nil {
					t.Fatal(err)
				}
				items, err := bt.format().pageItems(s.Page())
				bt.unpinR(s)
				if err != nil {
					t.Fatal(err)
				}
				var dead []storage.ItemPointer
				for _, it := range items {
					dead = append(dead, it.ptr)
				}
				if _, err := bt.VacuumIndexPages(dead); err != nil {
					t.Fatal(err)
				}
				ls, err := bt.pinR(left)
				if err != nil {
					t.Fatal(err)
				}
				stillLinked := readOpaque(ls.Page()).Next == leaf
				bt.unpinR(ls)
				ts, err := bt.pinR(leaf)
				if err != nil {
					t.Fatal(err)
				}
				top := readOpaque(ts.Page())
				bt.unpinR(ts)
				if flagged && (!stillLinked || top.IsDeleted() || top.IsHalfDead()) {
					t.Errorf("the right half of an incomplete split was deleted (linked=%v flags=%#x)", stillLinked, top.Flags)
				}
				if !flagged && stillLinked {
					t.Error("control: the emptied leaf was not deleted, so the refusal is not what kept it")
				}
			})
		}
	}
}
