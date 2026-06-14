package amcheck

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// fixedSeed pins the Bloom hash seed so the false-positive set is deterministic
// across runs (the wire-later SQL surface randomizes it, like upstream).
const fixedSeed = 0x9e3779b97f4a7c15

// makeLeafEntries builds n distinct (key, heap TID) entries: key = int4(i),
// TID = (i, 1), for i in [0, n).
func makeLeafEntries(n int) []btree.LeafEntry {
	out := make([]btree.LeafEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, btree.LeafEntry{
			Key: btree.EncodeInt4(int32(i)),
			TID: storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1},
		})
	}
	return out
}

// TestHeapAllIndexed_NoFalseNegatives is the load-bearing soundness test: when
// every heap-formed entry has an identical (key, TID) in the index, the Bloom
// filter's no-false-negative guarantee must yield ZERO reports. A single false
// "lacks matching index tuple" here would make the check report phantom
// corruption on a healthy index. n is large so the filter operates in its real
// ~0.5-density regime rather than the over-provisioned 1MB-floor regime.
func TestHeapAllIndexed_NoFalseNegatives(t *testing.T) {
	const n = 100_000
	index := makeLeafEntries(n)
	heap := makeLeafEntries(n) // identical logical entries

	reports := VerifyBtreeHeapAllIndexed(index, heap, "idx", "tbl", fixedSeed)
	if len(reports) != 0 {
		t.Fatalf("healthy index produced %d false-negative reports (want 0); first: %q",
			len(reports), reports[0].Msg)
	}
}

// TestHeapAllIndexed_DetectsMissingHeapTuple removes one index entry and
// confirms the corresponding heap tuple is reported as lacking a matching index
// tuple, with the exact upstream message and block. The other n-1 healthy heap
// tuples must NOT be reported.
func TestHeapAllIndexed_DetectsMissingHeapTuple(t *testing.T) {
	const n = 100_000
	const missing = 42_777

	heap := makeLeafEntries(n)
	// Index has every entry except `missing`.
	index := make([]btree.LeafEntry, 0, n-1)
	for i, e := range heap {
		if i == missing {
			continue
		}
		index = append(index, e)
	}

	reports := VerifyBtreeHeapAllIndexed(index, heap, "idx", "tbl", fixedSeed)
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want exactly 1 (the missing entry)", len(reports))
	}
	wantMsg := fmt.Sprintf(
		"heap tuple (%d,1) from table \"tbl\" lacks matching index tuple within index \"idx\"",
		missing)
	if reports[0].Msg != wantMsg {
		t.Errorf("message mismatch:\n got %q\nwant %q", reports[0].Msg, wantMsg)
	}
	if reports[0].Block != storage.BlockNumber(missing) {
		t.Errorf("block mismatch: got %d want %d", reports[0].Block, missing)
	}
}

// TestHeapAllIndexed_DistinguishesByTID verifies the fingerprint includes the
// heap TID, not just the key: a heap entry with a key present in the index but a
// DIFFERENT TID is a genuine mismatch (the index points the key at another row)
// and must be reported. If the fingerprint ignored the TID this would slip
// through as a false negative.
func TestHeapAllIndexed_DistinguishesByTID(t *testing.T) {
	index := []btree.LeafEntry{
		{Key: btree.EncodeInt4(7), TID: storage.ItemPointer{Block: 3, Offset: 1}},
	}
	// Same key, different TID — not the same logical index entry.
	heap := []btree.LeafEntry{
		{Key: btree.EncodeInt4(7), TID: storage.ItemPointer{Block: 9, Offset: 4}},
	}

	reports := VerifyBtreeHeapAllIndexed(index, heap, "idx", "tbl", fixedSeed)
	if len(reports) != 1 {
		t.Fatalf("key-present/TID-mismatch: got %d reports, want 1", len(reports))
	}
	if reports[0].Block != 9 || reports[0].Msg !=
		"heap tuple (9,4) from table \"tbl\" lacks matching index tuple within index \"idx\"" {
		t.Errorf("unexpected report: block=%d msg=%q", reports[0].Block, reports[0].Msg)
	}
}

// TestHeapAllIndexed_EmptyIndex confirms that an index with zero entries reports
// every heap tuple as missing — the correct outcome when a heap has rows but the
// index is empty (e.g. a truncated/corrupt index relation).
func TestHeapAllIndexed_EmptyIndex(t *testing.T) {
	heap := makeLeafEntries(5)
	reports := VerifyBtreeHeapAllIndexed(nil, heap, "idx", "tbl", fixedSeed)
	if len(reports) != len(heap) {
		t.Fatalf("empty index over %d heap rows: got %d reports, want %d",
			len(heap), len(reports), len(heap))
	}
}

// TestHeapAllIndexed_EmptyHeap confirms that an empty heap yields no reports
// regardless of index contents — heapallindexed only flags heap tuples missing
// from the index, never the reverse.
func TestHeapAllIndexed_EmptyHeap(t *testing.T) {
	index := makeLeafEntries(5)
	reports := VerifyBtreeHeapAllIndexed(index, nil, "idx", "tbl", fixedSeed)
	if len(reports) != 0 {
		t.Fatalf("empty heap: got %d reports, want 0", len(reports))
	}
}

// TestHeapAllIndexed_SharedKeyDistinctTIDs mirrors the posting-list semantics at
// the engine level: a posting-list item explodes to several entries sharing one
// key but pointing at distinct heap TIDs (btree.PageLeafEntries does the
// expansion; that reader is exercised in the btree package's TestPageLeafEntries).
// Here we confirm the engine treats each such entry independently — dropping one
// shared-key entry from the index flags exactly the one heap tuple whose TID went
// missing, while the siblings on the same key stay healthy.
func TestHeapAllIndexed_SharedKeyDistinctTIDs(t *testing.T) {
	key := btree.EncodeInt4(2)
	// Three heap rows indexed under one shared key (a posting list of 3 TIDs).
	heap := []btree.LeafEntry{
		{Key: key, TID: storage.ItemPointer{Block: 0, Offset: 2}},
		{Key: key, TID: storage.ItemPointer{Block: 0, Offset: 3}},
		{Key: key, TID: storage.ItemPointer{Block: 1, Offset: 1}},
	}

	// Full index → no reports.
	if reports := VerifyBtreeHeapAllIndexed(heap, heap, "idx", "tbl", fixedSeed); len(reports) != 0 {
		t.Fatalf("full shared-key index: got %d reports, want 0; first: %q", len(reports), reports[0].Msg)
	}

	// Drop the middle TID from the index: exactly that heap tuple is flagged.
	index := []btree.LeafEntry{heap[0], heap[2]}
	reports := VerifyBtreeHeapAllIndexed(index, heap, "idx", "tbl", fixedSeed)
	if len(reports) != 1 {
		t.Fatalf("dropping one shared-key TID: got %d reports, want 1", len(reports))
	}
	if reports[0].Msg != "heap tuple (0,3) from table \"tbl\" lacks matching index tuple within index \"idx\"" {
		t.Errorf("unexpected report: %q", reports[0].Msg)
	}
}
