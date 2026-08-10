package btree

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// halfDeadParentPage builds an internal page downlinking to `children`, with a
// high key when `hasHighKey` (i.e. the page is not rightmost). The two cases
// differ in P_FIRSTDATAKEY — 2 vs 1 — which is the whole reason the record
// carries a PHYSICAL offset rather than a data-slot index.
func halfDeadParentPage(t *testing.T, hasHighKey bool, children []storage.BlockNumber) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	next := PNone
	if hasHighKey {
		next = 99
	}
	WritePGOpaque(page, PGBTPageOpaque{Prev: PNone, Next: next, Level: 1})
	if hasHighKey {
		if _, err := storage.PageAddItemRaw(page, PGBTPivotRaw([]byte("hk"), PNone)); err != nil {
			t.Fatal(err)
		}
	}
	for i, child := range children {
		var key []byte
		if i > 0 {
			key = []byte{byte('a' + i)}
		}
		if _, err := storage.PageAddItemRaw(page, PGBTPivotRaw(key, child)); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

func halfDeadDownlinks(t *testing.T, page storage.Page) []storage.BlockNumber {
	t.Helper()
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	out := []storage.BlockNumber{}
	for slot := PGFirstDataKey(ReadPGOpaque(page)); int(slot) <= count; slot++ {
		raw, err := storage.PageGetItemRaw(page, slot)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, BTreeTupleGetDownLink(raw))
	}
	return out
}

// TestReplayHalfDeadParentRetargetsRatherThanDeletes pins upstream's
// btree_xlog_mark_page_halfdead parent mutation (nbtxlog.c:775-800) on BOTH page
// shapes: the item at poffset keeps its own key but adopts the RIGHT
// neighbour's downlink, and the neighbour's item is the one that disappears.
// The distinction matters because goopg's own ReplayRemoveParentDownlink
// deletes poffset outright, which absorbs the deleted subtree's key range
// leftward instead of rightward — a different parent page from the same input.
// (M0130-S11.5d-1.)
func TestReplayHalfDeadParentRetargetsRatherThanDeletes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hasHighKey bool
		poffset    uint16
	}{
		{"non-rightmost parent (P_FIRSTDATAKEY=2)", true, 2},
		{"rightmost parent (P_FIRSTDATAKEY=1)", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := halfDeadParentPage(t, tc.hasHighKey, []storage.BlockNumber{5, 7, 8})
			if err := ReplayHalfDeadParent(page, tc.poffset); err != nil {
				t.Fatal(err)
			}
			got := halfDeadDownlinks(t, page)
			want := []storage.BlockNumber{7, 8}
			if len(got) != len(want) {
				t.Fatalf("downlinks = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("downlinks = %v, want %v", got, want)
				}
			}
			// The high key must survive untouched — resetPageItems re-installs
			// it, and losing it would renumber every data slot on the page.
			hk, hasHK, err := PGHighKeyRaw(page)
			if err != nil {
				t.Fatal(err)
			}
			if hasHK != tc.hasHighKey {
				t.Fatalf("high key present = %v, want %v", hasHK, tc.hasHighKey)
			}
			if hasHK && len(hk) <= SizeOfIndexTupleData {
				t.Errorf("high key lost its key bytes (%d bytes)", len(hk))
			}
			// The minus-infinity pivot must still be minus-infinity: upstream
			// rewrites only t_tid, never the key attributes.
			first, err := storage.PageGetItemRaw(page, tc.poffset)
			if err != nil {
				t.Fatal(err)
			}
			if len(first) != SizeOfIndexTupleData {
				t.Errorf("poffset item is %d bytes, want the %d-byte minus-infinity pivot", len(first), SizeOfIndexTupleData)
			}
		})
	}
}

// TestReplayHalfDeadParentOutOfRangeIsNoOp covers the idempotency contract: WAL
// recovery gates the call on pd_lsn, so a poffset that no longer names a pair of
// items means the page already has the post-removal layout. Corrupting it would
// be worse than doing nothing.
func TestReplayHalfDeadParentOutOfRangeIsNoOp(t *testing.T) {
	for _, poffset := range []uint16{0, 1, 4, 9} {
		page := halfDeadParentPage(t, true, []storage.BlockNumber{5, 7, 8})
		before := append(storage.Page(nil), page...)
		if err := ReplayHalfDeadParent(page, poffset); err != nil {
			t.Fatalf("poffset %d: %v", poffset, err)
		}
		if string(before) != string(page) {
			t.Errorf("poffset %d mutated the page; want a no-op", poffset)
		}
	}
}

// TestReplayMarkHalfDeadLeafRebuildsFromScratch pins block 0 of the record
// (nbtxlog.c:815-848): the page is recreated, not patched, so whatever it held
// before is gone and the only survivor is the dummy high key carrying the top
// parent link. Here the page starts with live items to prove the rebuild.
func TestReplayMarkHalfDeadLeafRebuildsFromScratch(t *testing.T) {
	page := make(storage.Page, storage.BlockSize)
	if err := InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	WritePGOpaque(page, PGBTPageOpaque{Prev: 1, Next: 2, Flags: BTPLeaf})
	if _, err := storage.PageAddItemRaw(page, PGBTItemRaw([]byte("k"), storage.ItemPointer{Block: 3, Offset: 1})); err != nil {
		t.Fatal(err)
	}

	if err := ReplayMarkHalfDeadLeaf(page, 4, 6, 12); err != nil {
		t.Fatal(err)
	}
	op := ReadPGOpaque(page)
	if op.Flags != BTPHalfDead|BTPLeaf || op.Prev != 4 || op.Next != 6 || op.Level != 0 {
		t.Fatalf("opaque = %+v, want half-dead leaf prev 4 next 6 level 0", op)
	}
	count, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("page has %d items, want exactly the dummy high key", count)
	}
	raw, err := storage.PageGetItemRaw(page, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != SizeOfIndexTupleData {
		t.Errorf("dummy high key is %d bytes, want %d", len(raw), SizeOfIndexTupleData)
	}
	if got := BTreeTupleGetDownLink(raw); got != 12 {
		t.Errorf("top parent link = %d, want 12", got)
	}
}
