package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// pairDesc is a two-attribute (int4, int4) key, the shape of TPC-H's
// partsupp_pk (ps_partkey, ps_suppkey) in miniature.
func pairDesc() *PGIndexKeyDesc {
	a := int4Attr()
	a.Compare = PGCompareInt4
	b := int4Attr()
	b.Compare = PGCompareInt4
	return &PGIndexKeyDesc{Attrs: []PGKeyAttr{a, b}}
}

// TestAfterNewItemOffDecisions pins afterNewItemOff to _bt_afternewitemoff
// (nbtsplitloc.c) case by case: when the "split after new item" arm applies,
// and with which split.
func TestAfterNewItemOffDecisions(t *testing.T) {
	desc := pairDesc()
	f := indexFormat{desc: desc}
	mk := func(k1, k2 int32, blk storage.BlockNumber, off uint16) item {
		tid := storage.ItemPointer{Block: blk, Offset: off}
		return item{ptr: tid, key: tup(t, desc.Attrs, [][]byte{int4Val(k1), int4Val(k2)}, tid)}
	}
	// A page of (k, 1..4) groups. Heap TIDs ascend in insertion order.
	page := func(n int) []item {
		var out []item
		for i := 0; i < n; i++ {
			out = append(out, mk(int32(i/4), int32(i%4+1), storage.BlockNumber(10+i/100), uint16(i%100+1)))
		}
		return out
	}
	insertAt := func(items []item, at int, it item) []item {
		out := append([]item(nil), items[:at]...)
		out = append(out, it)
		return append(out, items[at:]...)
	}

	t.Run("new item right of every item, shared first attribute: fillfactor", func(t *testing.T) {
		items := page(40) // last item (9, 4)
		items = append(items, mk(9, 5, 99, 1))
		usemult, ok := f.afterNewItemOff(items, len(items)-1)
		if !ok || !usemult {
			t.Fatalf("got (usemult=%v, ok=%v), want (true, true)", usemult, ok)
		}
	})
	t.Run("new item right of every item, new grouping: declined", func(t *testing.T) {
		items := page(40)
		items = append(items, mk(10, 1, 99, 1))
		if _, ok := f.afterNewItemOff(items, len(items)-1); ok {
			t.Fatal("a new leading value is not a localized ascending insert")
		}
	})
	t.Run("mid page, adjacent heap TID, early on the page: exact split after it", func(t *testing.T) {
		items := page(40)
		// (2, 4) is items[11] at TID (10,12); the new (2, 5) follows it in key
		// and heap order.
		items = insertAt(items, 12, mk(2, 5, 10, 13))
		usemult, ok := f.afterNewItemOff(items, 12)
		if !ok || usemult {
			t.Fatalf("got (usemult=%v, ok=%v), want (false, true)", usemult, ok)
		}
	})
	t.Run("mid page, adjacent heap TID, past the fillfactor point: fillfactor", func(t *testing.T) {
		// page(38) ends with (9,1) and (9,2) at TIDs (10,37) and (10,38). The
		// new (9,3) follows at (10,39) with one later item, (9,5), to its
		// right: newitemoff 40 of maxoff 40, interp 40/41 > 0.90.
		items := page(38)
		items = append(items, mk(9, 3, 10, 39), mk(9, 5, 10, 40))
		usemult, ok := f.afterNewItemOff(items, 38)
		if !ok || !usemult {
			t.Fatalf("got (usemult=%v, ok=%v), want (true, true): interp > fillfactor", usemult, ok)
		}
	})
	t.Run("mid page, heap TID not adjacent: declined", func(t *testing.T) {
		items := page(40)
		items = insertAt(items, 12, mk(2, 5, 500, 7))
		if _, ok := f.afterNewItemOff(items, 12); ok {
			t.Fatal("a non-adjacent heap TID must decline the mid-page arm")
		}
	})
	t.Run("heap TID on the next block's first offset is adjacent", func(t *testing.T) {
		if !adjacentHeapTID(storage.ItemPointer{Block: 4, Offset: 90}, storage.ItemPointer{Block: 5, Offset: 1}) {
			t.Fatal("next block, offset 1 must count as adjacent")
		}
		if adjacentHeapTID(storage.ItemPointer{Block: 4, Offset: 90}, storage.ItemPointer{Block: 5, Offset: 2}) {
			t.Fatal("next block, offset 2 must not count as adjacent")
		}
	})
	t.Run("new item first on the page: declined", func(t *testing.T) {
		items := append([]item{mk(-1, 1, 10, 1)}, page(40)...)
		if _, ok := f.afterNewItemOff(items, 0); ok {
			t.Fatal("newitemoff == P_FIRSTKEY never infers an ascending pattern")
		}
	})
	t.Run("posting list on the page: declined", func(t *testing.T) {
		items := page(40)
		dup := mk(3, 2, 11, 50) // same key as an existing item: a posting on disk
		items = insertAt(items, 14, dup) // right after its twin (3,2) at index 13
		items = append(items, mk(9, 5, 99, 1))
		if _, ok := f.afterNewItemOff(items, len(items)-1); ok {
			t.Fatal("unequal tuple sizes (a posting) must decline")
		}
	})
	t.Run("single-attribute index: declined", func(t *testing.T) {
		single := int4Desc()
		g := indexFormat{desc: single}
		var items []item
		for i := 0; i < 10; i++ {
			tid := storage.ItemPointer{Block: 1, Offset: uint16(i + 1)}
			items = append(items, item{ptr: tid, key: tup(t, single.Attrs, [][]byte{int4Val(int32(i))}, tid)})
		}
		if _, ok := g.afterNewItemOff(items, 9); ok {
			t.Fatal("single key indexes are not considered")
		}
	})
	t.Run("blob format: declined", func(t *testing.T) {
		if _, ok := (indexFormat{}).afterNewItemOff(page(4), 3); ok {
			t.Fatal("a descriptor-less index has no attributes to compare")
		}
	})
}

// TestLocalizedAscendingInsertsPackLeaves is the M0146-0213 regression. Four
// interleaved streams, each inserting its own key range in ascending order
// (the HammerDB-loaded partsupp the 2026-10-15 INSERT replay reproduced), used
// to split every non-rightmost leaf 50:50 and leave it half empty for good.
// With _bt_afternewitemoff ported, the interleaved build must come within 10 %
// of a single ascending stream's size, which the rightmost-split fillfactor
// already packs to 90 %. Before the port it was ~1.4x.
func TestLocalizedAscendingInsertsPackLeaves(t *testing.T) {
	const parts, perPart, streams = 6000, 4, 4
	build := func(interleaved bool) storage.BlockNumber {
		desc := pairDesc()
		dir := t.TempDir()
		mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
		pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer func() { _ = pool.Close(); _ = mgr.Close() }()
		rel := storage.RelFileNode{DBOid: 1, RelOid: 9213, Fork: storage.MainFork}
		bt, err := CreateWithOptions(pool, rel, Options{KeyDesc: desc})
		if err != nil {
			t.Fatalf("CreateWithOptions: %v", err)
		}
		type row struct{ k1, k2 int32 }
		var rows []row
		for p := 0; p < parts; p++ {
			for s := 0; s < perPart; s++ {
				rows = append(rows, row{int32(p), int32(s)})
			}
		}
		order := rows
		if interleaved {
			// Round-robin batches of 23 rows from four contiguous key ranges.
			q := len(rows) / streams
			order = nil
			for off := 0; off < q; off += 23 {
				for st := 0; st < streams; st++ {
					end := off + 23
					if end > q {
						end = q
					}
					order = append(order, rows[st*q+off:st*q+end]...)
				}
			}
		}
		// Heap TIDs ascend in insertion order, as the heap a load writes does.
		for i, r := range order {
			tid := storage.ItemPointer{Block: storage.BlockNumber(1 + i/100), Offset: uint16(i%100 + 1)}
			if err := bt.Insert(tup(t, desc.Attrs, [][]byte{int4Val(r.k1), int4Val(r.k2)}, tid), tid); err != nil {
				t.Fatalf("Insert(%d,%d): %v", r.k1, r.k2, err)
			}
		}
		n, err := pool.NBlocks(rel)
		if err != nil {
			t.Fatalf("NBlocks: %v", err)
		}
		return n
	}
	ascending := build(false)
	interleaved := build(true)
	t.Logf("blocks: single ascending stream %d, four interleaved streams %d", ascending, interleaved)
	if float64(interleaved) > 1.10*float64(ascending) {
		t.Fatalf("interleaved ascending streams built %d blocks, single stream %d: leaves left half empty (want within 10%%)",
			interleaved, ascending)
	}
}
