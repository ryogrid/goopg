package nbtree

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestLeafScanStartMatchesLinearWalk pins M0146-0088: scanLeafItems starts at
// the slot leafScanStart binary-searches for (_bt_binsrch's role) instead of
// comparing every item from slot 1. The two must deliver exactly the same
// entries in the same order, for both key formats, both lower-bound kinds,
// duplicates (posting items) and LP_DEAD items, since the search reads a dead
// item's key while the scan skips the item itself.
func TestLeafScanStartMatchesLinearWalk(t *testing.T) {
	for _, format := range []string{"blob", "tuple"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
			pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
			if err != nil {
				t.Fatalf("NewPool: %v", err)
			}
			defer func() { _ = pool.Close(); _ = mgr.Close() }()
			rel := storage.RelFileNode{DBOid: 1, RelOid: 9300, Fork: storage.MainFork}

			var bt *BTree
			var key func(v int32, tid storage.ItemPointer) []byte
			if format == "blob" {
				bt, err = Create(pool, rel)
				key = func(v int32, _ storage.ItemPointer) []byte { return EncodeInt4(v) }
			} else {
				desc := int4Desc()
				bt, err = CreateWithOptions(pool, rel, Options{KeyDesc: desc})
				key = func(v int32, tid storage.ItemPointer) []byte {
					return tup(t, desc.Attrs, [][]byte{int4Val(v)}, tid)
				}
			}
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			// 4000 entries over 700 distinct keys: several leaves, runs of
			// duplicates, and in-order and reverse insertion mixed.
			for i := 0; i < 4000; i++ {
				v := int32((i * 389) % 700)
				tid := storage.ItemPointer{Block: storage.BlockNumber(1 + i/50), Offset: uint16(1 + i%50)}
				if err := bt.Insert(key(v, tid), tid); err != nil {
					t.Fatalf("Insert(%d): %v", v, err)
				}
			}
			// Mark every 7th item of the first leaf blocks LP_DEAD: the scan
			// skips them, the search still reads their keys.
			nblocks, err := pool.NBlocks(rel)
			if err != nil {
				t.Fatalf("NBlocks: %v", err)
			}
			for blk := storage.BlockNumber(1); blk < nblocks && blk < 6; blk++ {
				slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
				if err != nil {
					t.Fatalf("Pin: %v", err)
				}
				slot.Lock()
				if readOpaque(slot.Page()).IsLeaf() {
					if n, cerr := PGDataItemCount(slot.Page()); cerr == nil {
						for s := 3; s <= n; s += 7 {
							_ = pgSetItemIDDead(slot.Page(), uint16(s))
						}
					}
				}
				slot.Unlock()
				pool.Unpin(slot)
			}

			collect := func(lo, hi []byte, loEx, hiEx bool) []string {
				t.Helper()
				var out []string
				err := bt.RangeScanWithPos(lo, hi, loEx, hiEx, func(_ []byte, ptr storage.ItemPointer, pos ScanPos) (bool, error) {
					out = append(out, fmt.Sprintf("%d/%d@%d.%d", ptr.Block, ptr.Offset, pos.Blk, pos.Slot))
					return true, nil
				})
				if err != nil {
					t.Fatalf("RangeScanWithPos: %v", err)
				}
				return out
			}
			bound := func(v int32) []byte { return key(v, storage.ItemPointer{}) }

			checked := 0
			for _, lo := range []int32{-1, 0, 1, 5, 349, 350, 351, 698, 699, 700, 1000} {
				for _, hi := range []*int32{nil, ptrInt32(lo + 3), ptrInt32(699)} {
					for _, loEx := range []bool{false, true} {
						for _, hiEx := range []bool{false, true} {
							var hiB []byte
							if hi != nil {
								hiB = bound(*hi)
							}
							leafBinarySearch = false
							linear := collect(bound(lo), hiB, loEx, hiEx)
							leafBinarySearch = true
							binary := collect(bound(lo), hiB, loEx, hiEx)
							if fmt.Sprint(linear) != fmt.Sprint(binary) {
								t.Fatalf("lo=%d(excl %v) hi=%v(excl %v): binary-search start returned %d entries, linear walk %d\nlinear %v\nbinary %v",
									lo, loEx, hi, hiEx, len(binary), len(linear), linear, binary)
							}
							checked++
						}
					}
				}
			}
			if checked == 0 {
				t.Fatal("no bounds checked")
			}
		})
	}
}

func ptrInt32(v int32) *int32 { return &v }
