package nbtree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestSplitRefillFitsPostingHeavyPage is the regression gate for M0134-0177.
//
// pageItems EXPANDS every posting list into one item per heap TID, so the
// split path's working set is several times the size of the page it came from
// (measured: a leaf holding 8132 bytes expanded to 21960). The refill then
// wrote each expanded entry back as its own plain line pointer, so neither
// half of the split could fit and the rewrite panicked out of
// mustInsertItemSorted with "storage: not enough free space in page" — a
// backend crash mid-COPY, not an error the client could see as anything but a
// lost connection.
//
// The shape that produces it is ordinary: a low-cardinality index over enough
// rows that one key's TID run fills a leaf. Here 24 keys × 900 duplicates is
// well past the threshold on an 8 KiB page.
func TestSplitRefillFitsPostingHeavyPage(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	const (
		distinctKeys = 24
		dupsPerKey   = 900
	)
	want := make(map[storage.ItemPointer]int32, distinctKeys*dupsPerKey)
	for d := 0; d < dupsPerKey; d++ {
		for k := 0; k < distinctKeys; k++ {
			ptr := storage.ItemPointer{Block: storage.BlockNumber(d), Offset: uint16(k + 1)}
			key := int32(k)
			if err := bt.Insert(EncodeInt4(key), ptr); err != nil {
				t.Fatalf("Insert(key=%d, ptr=%+v): %v", key, ptr, err)
			}
			want[ptr] = key
		}
	}

	// Every (key, TID) pair must still be reachable. A split that silently
	// dropped entries would otherwise pass the no-panic bar: the pre-fix code
	// took the backend down, but the class of bug next door is a rewrite that
	// loses items, and only a full readback distinguishes them.
	got := make(map[storage.ItemPointer]int32, len(want))
	err := bt.RangeScan(EncodeInt4(0), EncodeInt4(distinctKeys), func(key []byte, ptr storage.ItemPointer) (bool, error) {
		k, derr := DecodeInt4(key)
		if derr != nil {
			return false, derr
		}
		got[ptr] = k
		return true, nil
	})
	if err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("RangeScan returned %d entries, want %d", len(got), len(want))
	}
	for ptr, key := range want {
		if got[ptr] != key {
			t.Fatalf("entry %+v: key %d, want %d", ptr, got[ptr], key)
		}
	}
}

// TestRunFootprintMatchesDeduplicateToRawItems is the anti-drift guard.
//
// The split path budgets a page from runFootprint and then writes it with
// deduplicateToRawItems. The two were independent expressions of the same
// chunking rule until M0134-0177, and a disagreement between a space estimate
// and the code that consumes it is precisely how the "not enough free space in
// page" panic reaches a user (root-0040 was the same shape one layer down).
// Both now go through postingChunkLens; this pins that they agree exactly, for
// runs on both sides of the per-item chunk limit.
func TestRunFootprintMatchesDeduplicateToRawItems(t *testing.T) {
	const itemIDSize = 4
	for _, f := range []indexFormat{blobFormat} {
		for _, keyLen := range []int{4, 64, 400} {
			key := make([]byte, keyLen)
			for i := range key {
				key[i] = byte('a' + i%26)
			}
			// 1 is a plain item; 2 is the shortest legal posting; the large
			// counts cross maxRawItemSize and force multi-chunk runs, where
			// a trailing one-TID remainder falls back to a plain item.
			for _, n := range []int{1, 2, 3, 100, 1289, 1290, 1291, 5000} {
				items := make([]item, n)
				for i := range items {
					items[i] = item{key: key, ptr: storage.ItemPointer{Block: storage.BlockNumber(i / 100), Offset: uint16(i%100 + 1)}}
				}
				raws := deduplicateToRawItems(f, items)
				written := 0
				for _, r := range raws {
					written += itemIDSize + MaxAlign(len(r.raw))
				}
				if budgeted := f.runFootprint(key, n); budgeted != written {
					t.Errorf("keyLen=%d n=%d: runFootprint=%d but deduplicateToRawItems writes %d bytes in %d items",
						keyLen, n, budgeted, written, len(raws))
				}
			}
		}
	}
}

// TestDeduplicateToRawItemsSpansCoverEveryEntry pins the span contract
// refillDeduplicated's insert trace depends on: the spans partition the input,
// so every expanded (key, TID) pair is attributed to exactly one line pointer.
func TestDeduplicateToRawItemsSpansCoverEveryEntry(t *testing.T) {
	var items []item
	for k := 0; k < 5; k++ {
		for d := 0; d < 7; d++ {
			items = append(items, item{
				key: EncodeInt4(int32(k)),
				ptr: storage.ItemPointer{Block: storage.BlockNumber(d), Offset: uint16(k + 1)},
			})
		}
	}
	raws, spans := deduplicateToRawItemsWithSpans(blobFormat, items)
	if len(raws) != len(spans) {
		t.Fatalf("%d raws but %d spans", len(raws), len(spans))
	}
	total := 0
	for _, s := range spans {
		if s < 1 {
			t.Fatalf("span %d is not positive", s)
		}
		total += s
	}
	if total != len(items) {
		t.Fatalf("spans cover %d entries, want %d", total, len(items))
	}
}

// TestCompactSplitLocRefusesRatherThanOverflows pins the second half of the
// fix: when no cut fits, compactSplitLoc says so instead of returning a
// midpoint the refill cannot honour. The caller turns that into an error with
// the latch still coherent, rather than resetting the page and panicking
// partway through writing it back.
func TestCompactSplitLocRefusesRatherThanOverflows(t *testing.T) {
	f := blobFormat
	budget := pageDataBudget()

	// A posting-heavy page: 1300 entries under two keys. As plain items that
	// is ~26 KB — over three pages, which is what used to make the split
	// unsatisfiable — but a posting pays the key overhead once per run rather
	// than once per TID, so the same content compacts to well under two pages
	// and a fitting cut exists. (A posting does NOT compress the TIDs
	// themselves: they cost six bytes each either way, which is what keeps a
	// page's compacted content bounded by the page it came from.)
	var heavy []item
	for k := 0; k < 2; k++ {
		for d := 0; d < 650; d++ {
			heavy = append(heavy, item{
				key: EncodeInt4(int32(k)),
				ptr: storage.ItemPointer{Block: storage.BlockNumber(d), Offset: uint16(k + 1)},
			})
		}
	}
	mid, ok := f.compactSplitLoc(heavy, budget, budget)
	if !ok {
		t.Fatal("compactSplitLoc found no cut for a run that compacts to a few postings")
	}
	if mid < 1 || mid >= len(heavy) {
		t.Fatalf("mid=%d is not a proper cut of %d entries", mid, len(heavy))
	}
	if left := f.compactFootprint(heavy[:mid]); left > budget {
		t.Errorf("left half compacts to %d bytes, over the %d-byte page budget", left, budget)
	}
	if right := f.compactFootprint(heavy[mid:]); right > budget {
		t.Errorf("right half compacts to %d bytes, over the %d-byte page budget", right, budget)
	}

	// Distinct keys that cannot compact at all, and far too many of them to
	// fit on two pages: there is no cut, and saying so is the correct answer.
	var wide []item
	for i := 0; i < 4000; i++ {
		wide = append(wide, item{
			key: EncodeInt4(int32(i)),
			ptr: storage.ItemPointer{Block: storage.BlockNumber(i), Offset: 1},
		})
	}
	if mid, ok := f.compactSplitLoc(wide, budget, budget); ok {
		t.Errorf("compactSplitLoc returned mid=%d for %d incompressible entries that cannot fit two pages", mid, len(wide))
	}
}
