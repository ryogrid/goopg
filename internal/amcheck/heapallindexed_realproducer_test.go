package amcheck_test

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/amcheck"
	"github.com/goopg/goopg/internal/storage"
)

// blobFmt is the on-page key format the external tests build their pages in —
// goopg's opaque key blobs, the zero btree.IndexFormat. The amcheck tiers take
// the format from their caller (a per-index catalog property; see the note
// above amcheck.KeyComparator), so these bytes-only tests state it once here.
var blobFmt = btree.IndexFormat{}

// The heapallindexed tier (heapallindexed.go) rests on a single sibling-path
// invariant: the fingerprint phase (index leaf entries, read by
// CollectBtreeLeafEntries -> btree.PageLeafEntries) and the probe phase (heap
// tuples, scanned by CollectHeapIndexEntries -> a HeapEntryFormer that forms the
// index key) MUST encode an identical logical (key, heap TID) to identical bytes
// through fingerprintLeafEntry. If they ever diverge, the wire-later
// bt_index_check(index, heapallindexed => true) SRF would report "heap tuple ...
// lacks matching index tuple" on EVERY healthy table+index — the most expensive
// class of compatibility bug in this project (a false positive that makes the
// whole check useless).
//
// Every existing heapallindexed test exercises that invariant with HAND-BUILT
// entries: heapallindexed_test.go pairs synthetic LeafEntry slices, the loop-#25
// compose guard (heapallindexed_heapscan_test.go:ComposesWithProbeCore) feeds a
// stub former that returns pre-made entries, and the real-tree round-trip
// (verify_nbtree_realtree_test.go) feeds the index's OWN leaf entries back as the
// probe set (VerifyBtreeHeapAllIndexed(leaves, leaves)). None of them runs a REAL
// heap tuple, written by storage.PageAddHeapTuple, through a former that decodes
// the tuple bytes and re-encodes the indexed column, and compares the result
// against a REAL btree built by btree.Insert. That end-to-end producer pair — the
// exact path the SQL surface will take — has never been validated.
//
// These tests close that gap, mirroring the bug-finding discipline of the heap
// real-producer guard (verify_heapam_realpage_test.go, which surfaced the
// VacuumHeapPageBySlots MAXALIGN bug) and the B-tree real-producer guard
// (verify_nbtree_realtree_test.go, which surfaced the split right-sibling
// prev-link bug, M0110-0007). They build a real multi-leaf B-tree and a real
// multi-block heap whose tuples carry the indexed int4 column, scan both through
// the engine's relation walkers, and assert the round-trip is silent on healthy
// storage and pinpoints a deliberately missing index entry.

const (
	// realHAINTuples forces the producers past their single-page degenerate
	// cases: > btree.MaxItemsPerPage (680) keys split the leaf level into
	// several siblings (exercising CollectBtreeLeafEntries' leftmost-descent +
	// btpo_next walk), and at realHAINPerHeapPage tuples per heap block the heap
	// spans many blocks (exercising CollectHeapIndexEntries' block loop).
	realHAINTuples      = 2000
	realHAINPerHeapPage = 64
)

// realHAINHeapTID maps the i-th logical row to its heap location. Block and slot
// grow with i; slots are 1-based per page, matching the order PageAddHeapTuple
// assigns them, so the heap-scan TID and the index-insert TID agree by
// construction.
func realHAINHeapTID(i int) storage.ItemPointer {
	return storage.ItemPointer{
		Block:  storage.BlockNumber(i / realHAINPerHeapPage),
		Offset: uint16(i%realHAINPerHeapPage + 1),
	}
}

// realHAINValue is the int4 column value stored in row i. It is the REVERSE of i
// so the key order (sorted in the index) deliberately differs from the heap TID
// order: a former that paired a key with the wrong TID, or a fingerprint that
// mixed up the two halves, would then mismatch and the round-trip would not be
// silent. Values stay distinct so each (key, TID) entry is unique.
func realHAINValue(i int) int32 { return int32(realHAINTuples - 1 - i) }

// buildRealHeap lays realHAINTuples live tuples across multiple in-memory heap
// pages via the real storage producer (PageAddHeapTuple), each tuple's data area
// holding the little-endian int4 column value for its row. It returns a
// map-backed PageSource and the block count — the same shape the SQL surface
// will hand to CollectHeapIndexEntries from the buffer manager.
func buildRealHeap(t *testing.T) (amcheck.PageSource, storage.BlockNumber) {
	t.Helper()
	pages := map[storage.BlockNumber]storage.Page{}
	var nblocks storage.BlockNumber
	for i := range realHAINTuples {
		tid := realHAINHeapTID(i)
		p, ok := pages[tid.Block]
		if !ok {
			p = make(storage.Page, storage.BlockSize)
			if err := storage.InitPage(p); err != nil {
				t.Fatalf("InitPage(block %d): %v", tid.Block, err)
			}
			pages[tid.Block] = p
			nblocks = tid.Block + 1
		}
		var data [4]byte
		binary.LittleEndian.PutUint32(data[:], uint32(realHAINValue(i)))
		tup := storage.NewHeapTuple(storage.TransactionID(100+i), 0, data[:])
		tup.Header.SetNatts(1)
		slot, err := storage.PageAddHeapTuple(p, tup)
		if err != nil {
			t.Fatalf("PageAddHeapTuple(row %d): %v", i, err)
		}
		if slot != tid.Offset {
			t.Fatalf("row %d landed at slot %d, expected %d (heap/index TID drift)", i, slot, tid.Offset)
		}
	}
	src := func(blk storage.BlockNumber) (storage.Page, error) {
		p, ok := pages[blk]
		if !ok {
			t.Fatalf("heap source asked for absent block %d (nblocks=%d)", blk, nblocks)
		}
		cp := make(storage.Page, len(p))
		copy(cp, p)
		return cp, nil
	}
	return src, nblocks
}

// buildRealIndex inserts an int4 key for every row whose index is NOT in skip,
// using the canonical btree.EncodeInt4 encoder and the row's heap TID, into a
// real Pool-backed B-tree. It returns an index PageSource over the live pages and
// a cleanup. Passing a non-empty skip omits those rows' index entries so a probe
// against the heap (which still has them) is forced to report the gap.
func buildRealIndex(t *testing.T, skip map[int]bool) (amcheck.PageSource, func()) {
	t.Helper()
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 256})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9200, Fork: storage.MainFork}
	bt, err := btree.Create(pool, rel)
	if err != nil {
		_ = pool.Close()
		_ = mgr.Close()
		t.Fatalf("btree.Create: %v", err)
	}
	for i := range realHAINTuples {
		if skip[i] {
			continue
		}
		if err := bt.Insert(btree.EncodeInt4(realHAINValue(i)), realHAINHeapTID(i)); err != nil {
			_ = pool.Close()
			_ = mgr.Close()
			t.Fatalf("Insert(row %d): %v", i, err)
		}
	}
	cleanup := func() {
		_ = pool.Close()
		_ = mgr.Close()
	}
	return realPageSource(t, pool, rel), cleanup
}

// realHAINFormer is the test's HeapEntryFormer: it decodes the int4 column from
// the raw heap tuple bytes (data area starts at t_hoff = DefaultHeapTupleHoff for
// these no-null tuples) and re-encodes it with the same btree.EncodeInt4 used to
// build the index — the deterministic, page-bytes-only half of upstream's
// index_form_tuple. The wire-layer former will instead consult the index's
// TupleDesc; what matters here is that the heap-scan -> key-form path reaches the
// SAME (key, TID) bytes the index stored.
func realHAINFormer(tid storage.ItemPointer, tuple []byte) (btree.LeafEntry, bool, error) {
	hoff := int(storage.DefaultHeapTupleHoff)
	v := int32(binary.LittleEndian.Uint32(tuple[hoff : hoff+4]))
	return btree.LeafEntry{Key: btree.EncodeInt4(v), TID: tid}, true, nil
}

// A real heap scanned into probe entries and a real index fingerprinted into the
// filter must round-trip silently: every live heap tuple's formed (key, TID)
// matches an index leaf entry, so no "lacks matching index tuple" report fires.
// This is the false-positive guard for the whole heapallindexed producer pair.
func TestVerifyHeapAllIndexed_RealProducers_RoundTripSilent(t *testing.T) {
	const (
		name = "ix_real_hain"
		tbl  = "tbl_real_hain"
		seed = uint64(0x9e3779b97f4a7c15)
	)

	idxSrc, cleanup := buildRealIndex(t, nil)
	defer cleanup()
	heapSrc, nblocks := buildRealHeap(t)

	idxLeaves, err := amcheck.CollectBtreeLeafEntries(idxSrc, blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(idxLeaves) != realHAINTuples {
		t.Fatalf("index leaf walk collected %d entries, want %d", len(idxLeaves), realHAINTuples)
	}

	heapEntries, err := amcheck.CollectHeapIndexEntries(heapSrc, nblocks, realHAINFormer)
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}
	if len(heapEntries) != realHAINTuples {
		t.Fatalf("heap scan collected %d entries, want %d", len(heapEntries), realHAINTuples)
	}

	// Direct core: index fingerprints, heap probes.
	if r := amcheck.VerifyBtreeHeapAllIndexed(idxLeaves, heapEntries, name, tbl, seed); len(r) != 0 {
		t.Fatalf("real producer round-trip falsely reported %d corruptions: %+v", len(r), r)
	}

	// Composed driver: the index relation walk + probe, exactly as the SRF wires
	// it (index PageSource in, heap probe set in).
	r, err := amcheck.VerifyBtreeHeapAllIndexedRelation(idxSrc, blobFmt, heapEntries, name, tbl, seed)
	if err != nil {
		t.Fatalf("VerifyBtreeHeapAllIndexedRelation: %v", err)
	}
	if len(r) != 0 {
		t.Fatalf("composed real producer round-trip falsely reported %d corruptions: %+v", len(r), r)
	}
}

// When the index is genuinely missing one row's entry but the heap still holds
// the tuple, the probe must flag exactly that heap tuple — and only it. The
// Bloom filter has no false negatives, so every present row stays silent; the one
// absent (key, TID) is reported with the upstream-verbatim message naming its
// heap location. This proves the producer pair detects real "index lost an entry"
// corruption, not merely that it stays quiet.
func TestVerifyHeapAllIndexed_RealProducers_DetectsMissingIndexEntry(t *testing.T) {
	const (
		name = "ix_real_hain"
		tbl  = "tbl_real_hain"
		seed = uint64(0x9e3779b97f4a7c15)
	)
	// Omit a row in the middle of the key range so the gap is an interior leaf
	// entry, not a boundary artifact.
	const missing = realHAINTuples / 3
	wantTID := realHAINHeapTID(missing)

	idxSrc, cleanup := buildRealIndex(t, map[int]bool{missing: true})
	defer cleanup()
	heapSrc, nblocks := buildRealHeap(t)

	idxLeaves, err := amcheck.CollectBtreeLeafEntries(idxSrc, blobFmt)
	if err != nil {
		t.Fatalf("CollectBtreeLeafEntries: %v", err)
	}
	if len(idxLeaves) != realHAINTuples-1 {
		t.Fatalf("index leaf walk collected %d entries, want %d", len(idxLeaves), realHAINTuples-1)
	}

	heapEntries, err := amcheck.CollectHeapIndexEntries(heapSrc, nblocks, realHAINFormer)
	if err != nil {
		t.Fatalf("CollectHeapIndexEntries: %v", err)
	}

	reports := amcheck.VerifyBtreeHeapAllIndexed(idxLeaves, heapEntries, name, tbl, seed)
	if len(reports) != 1 {
		t.Fatalf("missing index entry: got %d reports, want exactly 1: %+v", len(reports), reports)
	}
	if reports[0].Block != wantTID.Block {
		t.Fatalf("report names block %d, want %d", reports[0].Block, wantTID.Block)
	}
	wantMsg := fmt.Sprintf(
		"heap tuple (%d,%d) from table %q lacks matching index tuple within index %q",
		wantTID.Block, wantTID.Offset, tbl, name)
	if reports[0].Msg != wantMsg {
		t.Fatalf("report message mismatch:\n got: %s\nwant: %s", reports[0].Msg, wantMsg)
	}
}
