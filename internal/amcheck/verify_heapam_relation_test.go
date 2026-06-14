package amcheck

import (
	"errors"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// heapMapSource builds a PageSource over an in-memory block→page map, the heap-walk
// analogue of how the verify_heapam SRF would fill the seam from the buffer
// manager. A requested block absent from the map returns an error, mirroring a
// buffer read failure.
func heapMapSource(pages map[storage.BlockNumber]storage.Page) PageSource {
	return func(blk storage.BlockNumber) (storage.Page, error) {
		p, ok := pages[blk]
		if !ok {
			return nil, errors.New("no such block")
		}
		return p, nil
	}
}

// corruptPage returns a page with a single unaligned line pointer, which trips
// the "is not maximally aligned" structural check, plus the expected report
// offset and message.
func corruptPage(t *testing.T) (storage.Page, uint16, string) {
	t.Helper()
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	item, _ := storage.PageGetItemID(p, slot)
	setItemID(p, slot, item.Offset+1, storage.ItemIDNormal, item.Length)
	return p, slot, "line pointer to page offset " + itoa(int(item.Offset)+1) + " is not maximally aligned"
}

func int64p(v int64) *int64 { return &v }

// A clean multi-block relation yields no findings, and every in-range block is
// actually read.
func TestVerifyHeapRelation_CleanRelationNoReports(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{}
	for b := storage.BlockNumber(0); b < 3; b++ {
		p := newPage(t)
		addCleanTuple(t, p, 16)
		pages[b] = p
	}
	read := map[storage.BlockNumber]bool{}
	src := func(blk storage.BlockNumber) (storage.Page, error) {
		read[blk] = true
		return pages[blk], nil
	}

	reports, err := VerifyHeapRelation(src, 3, HeapRelOptions{})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("clean relation: got %d reports, want 0: %+v", len(reports), reports)
	}
	for b := storage.BlockNumber(0); b < 3; b++ {
		if !read[b] {
			t.Errorf("block %d was not read", b)
		}
	}
}

// An empty relation (nblocks == 0) returns no rows and never touches the source,
// mirroring verify_heapam.c's PG_RETURN_NULL early exit.
func TestVerifyHeapRelation_EmptyRelation(t *testing.T) {
	called := false
	src := func(storage.BlockNumber) (storage.Page, error) {
		called = true
		return nil, nil
	}
	reports, err := VerifyHeapRelation(src, 0, HeapRelOptions{})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if reports != nil {
		t.Fatalf("empty relation: got %+v, want nil", reports)
	}
	if called {
		t.Fatal("empty relation must not read any block")
	}
}

// A finding on a non-zero block is tagged with that block number, so the SRF can
// emit the correct blkno column.
func TestVerifyHeapRelation_FindingTaggedWithBlock(t *testing.T) {
	clean := newPage(t)
	addCleanTuple(t, clean, 16)
	bad, slot, msg := corruptPage(t)

	pages := map[storage.BlockNumber]storage.Page{0: clean, 1: clean, 2: bad}
	reports, err := VerifyHeapRelation(heapMapSource(pages), 3, HeapRelOptions{})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(reports), reports)
	}
	r := reports[0]
	if r.Blkno != 2 || r.Offset != slot || r.Msg != msg {
		t.Fatalf("report = {blkno %d, off %d, %q}, want {blkno 2, off %d, %q}",
			r.Blkno, r.Offset, r.Msg, slot, msg)
	}
}

// Findings come back in (block, offset) order across multiple corrupt blocks.
func TestVerifyHeapRelation_OrderedAcrossBlocks(t *testing.T) {
	b0, _, _ := corruptPage(t)
	b2, _, _ := corruptPage(t)
	pages := map[storage.BlockNumber]storage.Page{0: b0, 1: newPage(t), 2: b2}

	reports, err := VerifyHeapRelation(heapMapSource(pages), 3, HeapRelOptions{})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2: %+v", len(reports), reports)
	}
	if reports[0].Blkno != 0 || reports[1].Blkno != 2 {
		t.Fatalf("block order = [%d %d], want [0 2]", reports[0].Blkno, reports[1].Blkno)
	}
}

// startblock / endblock restrict the walk to the requested sub-range.
func TestVerifyHeapRelation_BlockRangeRestrictsWalk(t *testing.T) {
	b0, _, _ := corruptPage(t)
	b1, _, _ := corruptPage(t)
	b2, _, _ := corruptPage(t)
	pages := map[storage.BlockNumber]storage.Page{0: b0, 1: b1, 2: b2}

	// Only block 1.
	reports, err := VerifyHeapRelation(heapMapSource(pages), 3, HeapRelOptions{
		StartBlock: int64p(1), EndBlock: int64p(1),
	})
	if err != nil {
		t.Fatalf("VerifyHeapRelation: %v", err)
	}
	if len(reports) != 1 || reports[0].Blkno != 1 {
		t.Fatalf("range [1,1]: got %+v, want one finding on block 1", reports)
	}
}

// Out-of-range startblock / endblock raise the upstream-worded errors.
func TestVerifyHeapRelation_BlockRangeValidation(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{0: newPage(t), 1: newPage(t), 2: newPage(t)}
	src := heapMapSource(pages)

	if _, err := VerifyHeapRelation(src, 3, HeapRelOptions{StartBlock: int64p(3)}); err == nil ||
		err.Error() != "starting block number must be between 0 and 2" {
		t.Fatalf("startblock=3: err = %v, want upstream starting-block message", err)
	}
	if _, err := VerifyHeapRelation(src, 3, HeapRelOptions{StartBlock: int64p(-1)}); err == nil ||
		err.Error() != "starting block number must be between 0 and 2" {
		t.Fatalf("startblock=-1: err = %v, want upstream starting-block message", err)
	}
	if _, err := VerifyHeapRelation(src, 3, HeapRelOptions{EndBlock: int64p(3)}); err == nil ||
		err.Error() != "ending block number must be between 0 and 2" {
		t.Fatalf("endblock=3: err = %v, want upstream ending-block message", err)
	}
}

// A read error from the source is surfaced, not swallowed.
func TestVerifyHeapRelation_ReadErrorSurfaced(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{0: newPage(t)} // block 1 missing
	_, err := VerifyHeapRelation(heapMapSource(pages), 2, HeapRelOptions{})
	if err == nil {
		t.Fatal("expected an error for the unreadable block")
	}
}

// A nil source is rejected.
func TestVerifyHeapRelation_NilSource(t *testing.T) {
	if _, err := VerifyHeapRelation(nil, 1, HeapRelOptions{}); err == nil {
		t.Fatal("expected an error for a nil source")
	}
}

// The Rel / XidStatus options thread through to the per-page checker: a tuple
// whose stored natts exceeds RelDesc.Natts is reported only when Rel is set,
// confirming the driver forwards relation metadata (and, by the same seam, the
// XidStatusFunc) to verifyHeapPage.
func TestVerifyHeapRelation_RelOptionThreadsThrough(t *testing.T) {
	p := newPage(t)
	slot := addCleanTuple(t, p, 16)
	setNatts(t, p, slot, 5) // stored natts 5 > table natts 2
	pages := map[storage.BlockNumber]storage.Page{0: p}

	// Without Rel: natts check disabled, no finding.
	reports, err := VerifyHeapRelation(heapMapSource(pages), 1, HeapRelOptions{})
	if err != nil {
		t.Fatalf("VerifyHeapRelation (no rel): %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("no-rel: got %d reports, want 0: %+v", len(reports), reports)
	}

	// With Rel.Natts = 2: the over-count is reported.
	reports, err = VerifyHeapRelation(heapMapSource(pages), 1, HeapRelOptions{Rel: RelDesc{Natts: 2}})
	if err != nil {
		t.Fatalf("VerifyHeapRelation (rel): %v", err)
	}
	if len(reports) != 1 || reports[0].Blkno != 0 || reports[0].Offset != slot {
		t.Fatalf("rel: got %+v, want one finding on block 0 offset %d", reports, slot)
	}
}
