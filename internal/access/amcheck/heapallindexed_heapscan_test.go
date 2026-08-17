package amcheck

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// includeAllFormer is the trivial wire-layer stub: it forms an entry whose key is
// the tuple's first `keyLen` data bytes (after the 23-byte header) and always
// includes it. It stands in for the TupleDesc-coupled index_form_tuple the SQL
// surface supplies, letting the heap-walk skeleton be exercised in isolation.
func includeAllFormer(keyLen int) HeapEntryFormer {
	return func(tid storage.ItemPointer, tuple []byte) (nbtree.LeafEntry, bool, error) {
		start := int(storage.SizeOfHeapTupleHeaderData)
		end := min(start+keyLen, len(tuple))
		key := append([]byte(nil), tuple[start:end]...)
		return nbtree.LeafEntry{Key: key, TID: tid}, true, nil
	}
}

// TestCollectHeapIndexEntries_EmptyRelation: nblocks == 0 contributes nothing.
func TestCollectHeapIndexEntries_EmptyRelation(t *testing.T) {
	src := func(storage.BlockNumber) (storage.Page, error) {
		t.Fatalf("source should not be read for an empty relation")
		return nil, nil
	}
	entries, err := CollectHeapIndexEntries(src, 0, includeAllFormer(4))
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty relation produced %d entries", len(entries))
	}
}

// TestCollectHeapIndexEntries_NewPageSkipped: an all-zero (new) page yields no
// entries, mirroring the per-page heap checker's short-circuit.
func TestCollectHeapIndexEntries_NewPageSkipped(t *testing.T) {
	pages := map[storage.BlockNumber]storage.Page{
		0: make(storage.Page, storage.BlockSize), // all zero == new
	}
	entries, err := CollectHeapIndexEntries(heapMapSource(pages), 1, includeAllFormer(4))
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("new page produced %d entries", len(entries))
	}
}

// TestCollectHeapIndexEntries_MultiBlockOrder: two blocks of clean tuples produce
// one entry per LP_NORMAL tuple, in (block, offset) order, each carrying the
// tuple's own TID and the bytes the former saw.
func TestCollectHeapIndexEntries_MultiBlockOrder(t *testing.T) {
	p0 := newPage(t)
	addCleanTuple(t, p0, 16) // (0,1)
	addCleanTuple(t, p0, 16) // (0,2)
	p1 := newPage(t)
	addCleanTuple(t, p1, 16) // (1,1)

	pages := map[storage.BlockNumber]storage.Page{0: p0, 1: p1}

	// A former that records the TID and the length of bytes it received, so we
	// can assert the heap-walk handed it the right tuple body.
	var seenLens []int
	form := func(tid storage.ItemPointer, tuple []byte) (nbtree.LeafEntry, bool, error) {
		seenLens = append(seenLens, len(tuple))
		return nbtree.LeafEntry{Key: []byte{byte(tid.Block), byte(tid.Offset)}, TID: tid}, true, nil
	}

	entries, err := CollectHeapIndexEntries(heapMapSource(pages), 2, form)
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}

	wantTIDs := []storage.ItemPointer{{Block: 0, Offset: 1}, {Block: 0, Offset: 2}, {Block: 1, Offset: 1}}
	if len(entries) != len(wantTIDs) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(wantTIDs), entries)
	}
	for i, want := range wantTIDs {
		if entries[i].TID != want {
			t.Errorf("entry %d TID = %+v, want %+v", i, entries[i].TID, want)
		}
	}
	// Each tuple body is MAXALIGN(header(23) + 16 data) bytes; the former must
	// have seen the full LP_NORMAL slice, not a truncated one.
	wantLen := maxAlign(int(storage.SizeOfHeapTupleHeaderData) + 16)
	for i, n := range seenLens {
		if n != wantLen {
			t.Errorf("former saw %d bytes for tuple %d, want %d", n, i, wantLen)
		}
	}
}

// TestCollectHeapIndexEntries_FormerExcludes: a former returning include=false
// drops the tuple from the probe set — upstream's heapallindexedCallback skipping
// a dead / invisible / non-root-HOT tuple.
func TestCollectHeapIndexEntries_FormerExcludes(t *testing.T) {
	p0 := newPage(t)
	addCleanTuple(t, p0, 16) // (0,1) keep
	addCleanTuple(t, p0, 16) // (0,2) drop
	addCleanTuple(t, p0, 16) // (0,3) keep
	pages := map[storage.BlockNumber]storage.Page{0: p0}

	form := func(tid storage.ItemPointer, _ []byte) (nbtree.LeafEntry, bool, error) {
		return nbtree.LeafEntry{TID: tid}, tid.Offset != 2, nil
	}
	entries, err := CollectHeapIndexEntries(heapMapSource(pages), 1, form)
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (offset 2 excluded): %+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.TID.Offset == 2 {
			t.Errorf("excluded tuple (0,2) leaked into entries")
		}
	}
}

// TestCollectHeapIndexEntries_SkipsNonNormalItems: unused / dead / redirect line
// pointers carry no tuple body and must be skipped (a redirect target is reached
// on its own offset, so following it here would double-count).
func TestCollectHeapIndexEntries_SkipsNonNormalItems(t *testing.T) {
	p := newPage(t)
	slot1 := addCleanTuple(t, p, 16) // (0,1) LP_NORMAL
	slot2 := addCleanTuple(t, p, 16) // (0,2) -> mark DEAD
	slot3 := addCleanTuple(t, p, 16) // (0,3) LP_NORMAL

	// Demote slot2 to LP_DEAD (length 0, no body) and add a redirect + unused at
	// fresh slots beyond the live ones, so PageLinePointerCount covers them.
	setItemID(p, slot2, 0, storage.ItemIDDead, 0)
	setItemID(p, 4, slot1, storage.ItemIDRedirect, 0) // redirect -> slot1
	setItemID(p, 5, 0, storage.ItemIDUnused, 0)
	// Bump pd_lower so PageLinePointerCount covers slots 4 and 5 (header + 5*4).
	storage.MustHeader(p).SetLower(uint16(storage.SizeOfPageHeaderData + 5*4))

	pages := map[storage.BlockNumber]storage.Page{0: p}
	form := func(tid storage.ItemPointer, _ []byte) (nbtree.LeafEntry, bool, error) {
		return nbtree.LeafEntry{TID: tid}, true, nil
	}
	entries, err := CollectHeapIndexEntries(heapMapSource(pages), 1, form)
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	wantOffsets := []uint16{slot1, slot3}
	if len(entries) != len(wantOffsets) {
		t.Fatalf("got %d entries, want %d (only LP_NORMAL): %+v", len(entries), len(wantOffsets), entries)
	}
	for i, want := range wantOffsets {
		if entries[i].TID.Offset != want {
			t.Errorf("entry %d offset = %d, want %d", i, entries[i].TID.Offset, want)
		}
	}
}

// TestCollectHeapIndexEntries_OutOfBoundsLinePointer: a line pointer pointing
// past the page end is surfaced as an error, not a panic or a silently dropped
// tuple (a truncated probe set is unsound).
func TestCollectHeapIndexEntries_OutOfBoundsLinePointer(t *testing.T) {
	p := newPage(t)
	addCleanTuple(t, p, 16)
	// Corrupt slot 1 to point near the end of the page with an over-long length.
	setItemID(p, 1, uint16(storage.BlockSize-8), storage.ItemIDNormal, 64)
	pages := map[storage.BlockNumber]storage.Page{0: p}

	_, err := CollectHeapIndexEntries(heapMapSource(pages), 1, includeAllFormer(4))
	if err == nil {
		t.Fatalf("out-of-bounds line pointer did not error")
	}
}

// TestCollectHeapIndexEntries_FormerErrorPropagates: a former error aborts the
// walk and is surfaced (completeness over a partial set).
func TestCollectHeapIndexEntries_FormerErrorPropagates(t *testing.T) {
	p := newPage(t)
	addCleanTuple(t, p, 16)
	pages := map[storage.BlockNumber]storage.Page{0: p}

	want := fmt.Errorf("boom")
	form := func(storage.ItemPointer, []byte) (nbtree.LeafEntry, bool, error) {
		return nbtree.LeafEntry{}, false, want
	}
	_, err := CollectHeapIndexEntries(heapMapSource(pages), 1, form)
	if err == nil {
		t.Fatalf("former error was swallowed")
	}
}

// TestCollectHeapIndexEntries_NilArgs: nil source / nil former are rejected.
func TestCollectHeapIndexEntries_NilArgs(t *testing.T) {
	if _, err := CollectHeapIndexEntries(nil, 1, includeAllFormer(4)); err == nil {
		t.Errorf("nil source did not error")
	}
	src := func(storage.BlockNumber) (storage.Page, error) { return newPage(t), nil }
	if _, err := CollectHeapIndexEntries(src, 1, nil); err == nil {
		t.Errorf("nil former did not error")
	}
}

// TestCollectHeapIndexEntries_ReadErrorPropagates: a PageSource read failure is
// surfaced rather than yielding a truncated entry set.
func TestCollectHeapIndexEntries_ReadErrorPropagates(t *testing.T) {
	src := func(blk storage.BlockNumber) (storage.Page, error) {
		return nil, fmt.Errorf("disk gone at %d", blk)
	}
	if _, err := CollectHeapIndexEntries(src, 1, includeAllFormer(4)); err == nil {
		t.Fatalf("read error was swallowed")
	}
}

// TestCollectHeapIndexEntries_ComposesWithProbeCore is the sibling-path guard:
// the heap-side producer's entries, fed to VerifyBtreeHeapAllIndexed alongside a
// matching index leaf set, must yield NO reports (every heap tuple has its index
// entry); dropping one index entry must yield exactly one report for the orphaned
// heap tuple. This proves the producer and the pure probe core agree on the
// (key, TID) encoding (fingerprintLeafEntry) — the heapallindexed soundness
// invariant.
func TestCollectHeapIndexEntries_ComposesWithProbeCore(t *testing.T) {
	p0 := newPage(t)
	addCleanTuple(t, p0, 16)
	addCleanTuple(t, p0, 16)
	p1 := newPage(t)
	addCleanTuple(t, p1, 16)
	pages := map[storage.BlockNumber]storage.Page{0: p0, 1: p1}

	heapEntries, err := CollectHeapIndexEntries(heapMapSource(pages), 2, includeAllFormer(8))
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	if len(heapEntries) != 3 {
		t.Fatalf("expected 3 heap entries, got %d", len(heapEntries))
	}

	// A complete index: the same (key, TID) set. No tuple should be flagged.
	indexEntries := append([]nbtree.LeafEntry(nil), heapEntries...)
	const seed = 0x5eed
	if reports := VerifyBtreeHeapAllIndexed(indexEntries, heapEntries, "i", "t", seed); len(reports) != 0 {
		t.Fatalf("complete index produced %d false reports: %+v", len(reports), reports)
	}

	// Drop the entry for heap tuple (1,1): it must now be reported as lacking a
	// matching index tuple.
	var pruned []nbtree.LeafEntry
	for _, e := range indexEntries {
		if e.TID == (storage.ItemPointer{Block: 1, Offset: 1}) {
			continue
		}
		pruned = append(pruned, e)
	}
	reports := VerifyBtreeHeapAllIndexed(pruned, heapEntries, "i", "t", seed)
	if len(reports) != 1 {
		t.Fatalf("expected exactly 1 report for the orphaned tuple, got %d: %+v", len(reports), reports)
	}
	want := "heap tuple (1,1) from table \"t\" lacks matching index tuple within index \"i\""
	if reports[0].Msg != want {
		t.Errorf("report msg = %q, want %q", reports[0].Msg, want)
	}
	if reports[0].Block != 1 {
		t.Errorf("report block = %d, want 1", reports[0].Block)
	}

	// Sanity: the produced key bytes are the tuple data (non-empty), so the
	// fingerprint is not trivially the TID alone.
	if len(heapEntries[0].Key) == 0 || bytes.Equal(heapEntries[0].Key, nil) {
		t.Errorf("former produced empty key; fingerprint would ignore key bytes")
	}
}
