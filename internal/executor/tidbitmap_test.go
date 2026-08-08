package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// TIDBitmap basic tests
// ---------------------------------------------------------------------------

func TestTIDBitmapEmpty(t *testing.T) {
	tbm := &TIDBitmap{}
	if !tbmIsEmpty(tbm) {
		t.Error("empty TIDBitmap should report empty")
	}

	it := tbmBeginIterate(tbm)
	_, _, _, _, ok := it.next()
	if ok {
		t.Error("empty iterator should return ok=false")
	}
}

func TestTIDBitmapExactSingle(t *testing.T) {
	tbm := &TIDBitmap{}
	tbmAddTuples(tbm, []storage.ItemPointer{{Block: 0, Offset: 5}}, false)

	if tbmIsEmpty(tbm) {
		t.Fatal("bitmap should not be empty")
	}
	if tbm.npages != 1 {
		t.Fatalf("expected 1 exact page, got %d", tbm.npages)
	}

	it := tbmBeginIterate(tbm)
	block, offset, lossy, recheck, ok := it.next()
	if !ok {
		t.Fatal("iterator should return one entry")
	}
	if block != 0 {
		t.Errorf("expected block 0, got %d", block)
	}
	if offset != 5 {
		t.Errorf("expected offset 5, got %d", offset)
	}
	if lossy {
		t.Error("expected exact (not lossy)")
	}
	if recheck {
		t.Error("expected recheck=false")
	}

	_, _, _, _, ok = it.next()
	if ok {
		t.Error("iterator should be exhausted after one entry")
	}
}

func TestTIDBitmapExactMultiple(t *testing.T) {
	tbm := &TIDBitmap{}
	// Add TIDs across multiple pages in non-sorted order.
	tbmAddTuples(tbm, []storage.ItemPointer{
		{Block: 3, Offset: 10},
		{Block: 1, Offset: 5},
		{Block: 1, Offset: 1},
		{Block: 2, Offset: 3},
		{Block: 3, Offset: 7},
	}, false)

	if tbm.npages != 3 {
		t.Fatalf("expected 3 exact pages, got %d", tbm.npages)
	}

	it := tbmBeginIterate(tbm)

	// Should emit in block-ascending order.
	expected := []struct {
		block  storage.BlockNumber
		offset uint16
	}{
		{1, 1}, {1, 5},
		{2, 3},
		{3, 7}, {3, 10},
	}

	for i, exp := range expected {
		block, offset, lossy, _, ok := it.next()
		if !ok {
			t.Fatalf("expected %d entries, got %d", len(expected), i)
		}
		if block != exp.block || offset != exp.offset {
			t.Errorf("entry %d: want (%d,%d), got (%d,%d)", i, exp.block, exp.offset, block, offset)
		}
		if lossy {
			t.Errorf("entry %d: expected exact", i)
		}
	}

	_, _, _, _, ok := it.next()
	if ok {
		t.Error("iterator should be exhausted")
	}
}

func TestTIDBitmapRecheck(t *testing.T) {
	tbm := &TIDBitmap{}
	tbmAddTuples(tbm, []storage.ItemPointer{{Block: 0, Offset: 1}}, true)

	if !tbm.recheckAny {
		t.Error("recheckAny should be true after adding recheck=true entry")
	}

	it := tbmBeginIterate(tbm)
	_, _, _, recheck, ok := it.next()
	if !ok {
		t.Fatal("expected one entry")
	}
	if !recheck {
		t.Error("entry should have recheck=true")
	}
}

func TestTIDBitmapUnion(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{{Block: 0, Offset: 1}, {Block: 1, Offset: 2}}, false)

	tbm2 := &TIDBitmap{}
	tbmAddTuples(tbm2, []storage.ItemPointer{{Block: 1, Offset: 3}, {Block: 2, Offset: 4}}, false)

	tbmUnion(tbm1, tbm2)

	if tbm1.npages != 3 {
		t.Fatalf("expected 3 pages after union, got %d", tbm1.npages)
	}

	it := tbmBeginIterate(tbm1)

	// Should have all TIDs: (0,1), (1,2), (1,3), (2,4)
	var entries [][2]uint32
	for {
		block, offset, _, _, ok := it.next()
		if !ok {
			break
		}
		entries = append(entries, [2]uint32{uint32(block), uint32(offset)})
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %v", len(entries), entries)
	}
}

func TestTIDBitmapUnionWithLossy(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{{Block: 0, Offset: 1}}, false)

	// Create a lossy entry manually.
	tbm2 := &TIDBitmap{}
	tbmAddTuples(tbm2, []storage.ItemPointer{{Block: 0, Offset: 5}}, false)
	// Force lossy on block 0
	tbm2.entries[0].isLossy = true
	tbm2.entries[0].bitmap = nil
	tbm2.npages = 0
	tbm2.nchunks = 1

	tbmUnion(tbm1, tbm2)

	e := tbm1.entries[0]
	if !e.isLossy {
		t.Error("exact + lossy union should result in lossy")
	}
}

func TestTIDBitmapIntersectExact(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{
		{Block: 0, Offset: 1}, {Block: 0, Offset: 2}, {Block: 1, Offset: 3},
	}, false)

	tbm2 := &TIDBitmap{}
	tbmAddTuples(tbm2, []storage.ItemPointer{
		{Block: 0, Offset: 2}, {Block: 0, Offset: 3}, {Block: 1, Offset: 3},
	}, false)

	tbmIntersect(tbm1, tbm2)

	// Should have: (0,2), (1,3) — (0,1) and (0,3) are not in both.
	it := tbmBeginIterate(tbm1)
	var entries [][2]uint32
	for {
		block, offset, _, _, ok := it.next()
		if !ok {
			break
		}
		entries = append(entries, [2]uint32{uint32(block), uint32(offset)})
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	if entries[0] != [2]uint32{0, 2} {
		t.Errorf("expected (0,2), got %v", entries[0])
	}
	if entries[1] != [2]uint32{1, 3} {
		t.Errorf("expected (1,3), got %v", entries[1])
	}
}

func TestTIDBitmapIntersectWithLossy(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{{Block: 0, Offset: 1}}, false)

	// Create a lossy entry.
	tbm2 := &TIDBitmap{}
	tbmAddTuples(tbm2, []storage.ItemPointer{{Block: 0, Offset: 5}}, false)
	tbm2.entries[0].isLossy = true
	tbm2.entries[0].bitmap = nil
	tbm2.npages = 0
	tbm2.nchunks = 1

	tbmIntersect(tbm1, tbm2)

	e := tbm1.entries[0]
	if !e.isLossy {
		t.Error("exact intersect lossy should result in lossy")
	}
}

func TestTIDBitmapIntersectWithEmpty(t *testing.T) {
	tbm1 := &TIDBitmap{}
	tbmAddTuples(tbm1, []storage.ItemPointer{{Block: 0, Offset: 1}}, false)

	tbm2 := &TIDBitmap{} // empty

	tbmIntersect(tbm1, tbm2)

	if !tbmIsEmpty(tbm1) {
		t.Error("intersection with empty should be empty")
	}
}

func TestTIDBitmapLossify(t *testing.T) {
	// maxEntries=6 → effective budget is 6 units.
	// Exact pages cost 5 each, lossy cost 1 each.
	// With 5 pages: 5*5 = 25 > 6 → over budget.
	// After lossifying 4 pages: 1*5 + 4*1 = 9 > 6 → still over.
	// After lossifying 5 pages: 0*5 + 5*1 = 5 ≤ 6 → stops.
	tbm := &TIDBitmap{maxEntries: 6}

	// Add 5 distinct TIDs across 5 distinct pages.
	for i := storage.BlockNumber(0); i < 5; i++ {
		tbmAddTuples(tbm, []storage.ItemPointer{{Block: i, Offset: 1}}, false)
	}

	if tbm.npages != 5 {
		t.Fatalf("expected 5 pages, got %d", tbm.npages)
	}
	if tbm.nchunks != 0 {
		t.Fatalf("expected 0 chunks, got %d", tbm.nchunks)
	}

	tbmLossify(tbm)

	// After lossify: effective cost = npages*5 + nchunks ≤ maxEntries.
	effective := tbm.npages*5 + tbm.nchunks
	if effective > tbm.maxEntries {
		t.Errorf("effective entries %d > maxEntries %d (exact=%d lossy=%d)",
			effective, tbm.maxEntries, tbm.npages, tbm.nchunks)
	}
	if tbm.nchunks == 0 {
		t.Error("expected some lossy chunks after lossify")
	}
}

func TestTIDBitmapIteratorEmptyBitmap(t *testing.T) {
	tbm := &TIDBitmap{}
	it := tbmBeginIterate(tbm)
	_, _, _, _, ok := it.next()
	if ok {
		t.Error("empty bitmap iterator should return ok=false on first call")
	}
}

func TestTIDBitmapCalculateMaxEntries(t *testing.T) {
	// work_mem = 0 → unlimited
	if n := tbmCalculateMaxEntries(0); n != 0 {
		t.Errorf("work_mem=0 should give 0 (unlimited), got %d", n)
	}
	// work_mem = 4096 bytes → some positive number
	if n := tbmCalculateMaxEntries(4096); n < 16 {
		t.Errorf("work_mem=4096 should give >= 16, got %d", n)
	}
}
