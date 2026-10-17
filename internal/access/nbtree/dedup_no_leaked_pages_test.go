package nbtree

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestDuplicateInsertsLeakNoPages pins M0146-0195's root cause. A full leaf
// first tries dedup (and the dead-entry purge) and splits only when that
// fails, as _bt_findinsertloc calls _bt_delete_or_dedup_one_page before
// _bt_split allocates the right page (nbtinsert.c:998, 1720). goopg used to
// allocate the right page first and, when dedup then made room, abandon it:
// a live page nothing linked in, which popRecyclableBlock never reuses. With
// 4-way duplicate keys inserted in order, 8 of every 9 leaf blocks were empty
// and the index was 9 times its size (TPC-H idx_lineitem_orderkey_fkidx
// after the 2026-10-15 realign: 117050 blocks against PG's 16468).
func TestDuplicateInsertsLeakNoPages(t *testing.T) {
	bt, pool, cleanup := newTestTree(t)
	defer cleanup()
	// A horizon source, as every server wires one: without it the free list
	// is authoritative and an abandoned allocation is simply reused, which
	// hides the leak.
	pool.SetBtreeRecycleHorizon(func() (next, oldestVisible uint64) { return 100, 100 })
	const rows = 40000
	for g := 1; g <= rows; g++ {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(g/4))
		if err := bt.Insert(k, storage.ItemPointer{Block: storage.BlockNumber(g/100 + 1), Offset: uint16(g%100 + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := pool.NBlocks(bt.rel)
	if err != nil {
		t.Fatal(err)
	}
	empty, leaves := 0, 0
	for blk := storage.BlockNumber(1); blk < n; blk++ {
		s, err := bt.pinR(blk)
		if err != nil {
			t.Fatal(err)
		}
		op := readOpaque(s.Page())
		count, cerr := PGDataItemCount(s.Page())
		bt.unpinR(s)
		if cerr != nil {
			t.Fatal(cerr)
		}
		if op.Level != 0 {
			continue
		}
		leaves++
		if count == 0 {
			empty++
		}
	}
	if empty > 0 {
		t.Errorf("%d of %d leaf blocks hold no item (%d blocks in all): abandoned split allocations", empty, leaves, n)
	}
	for g := 4; g <= rows; g += 997 {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(g/4))
		if _, ok, err := bt.Search(k); err != nil || !ok {
			t.Fatalf("Search(%d) = %v, %v", g/4, ok, err)
		}
	}
}
