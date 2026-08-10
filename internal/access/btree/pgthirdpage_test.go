package btree

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-3d guards — `_bt_check_third_page` replaces
// MaxHighKeyLen, and the bulk loader's separator reserve becomes exact.
//
// The two halves fail in opposite directions, which is why both are pinned:
//
//   - the GATE half is a rejection. goopg used to bound the SEPARATOR at 256
//     bytes and never bounded a leaf row at all, so an over-wide index row was
//     admitted and only blew up later, at the split that had to turn it into a
//     high key. Upstream bounds the ROW (a third of a page) and lets the
//     separator be whatever truncation leaves.
//   - the RESERVE half is an admission. The old constant reserve held back 268
//     bytes of every page for a separator that suffix truncation now usually
//     makes far smaller; reserving the exact separator gives that space back.
//     A reserve that is too SMALL is the dangerous direction — the page fills,
//     the separator no longer fits, and flushPage fails — so the structural
//     test below builds trees whose separators are wide and variable.

// TestCheckPGBTThirdPageLevelBounds pins the leaf/internal asymmetry, which is
// the whole point of upstream having two constants. A leaf tuple is charged
// BTMaxItemSize because `_bt_truncate` may append a tiebreaker heap TID to the
// separator derived from it; the internal level is charged
// BTMaxItemSizeNoHeapTid precisely so it can accept that grown pivot.
func TestCheckPGBTThirdPageLevelBounds(t *testing.T) {
	if BTMaxItemSizeNoHeapTid-BTMaxItemSize != 8 {
		t.Fatalf("the two bounds differ by %d, not MAXALIGN(sizeof(ItemPointerData))=8",
			BTMaxItemSizeNoHeapTid-BTMaxItemSize)
	}
	cases := []struct {
		name    string
		leaf    bool
		size    int
		wantErr string
	}{
		{"leaf at the limit", true, BTMaxItemSize, ""},
		{"leaf one quantum over", true, BTMaxItemSize + 8, "index row size"},
		{"internal in the reserved band", false, BTMaxItemSize + 8, ""},
		{"internal at its limit", false, BTMaxItemSizeNoHeapTid, ""},
		{"internal over its limit", false, BTMaxItemSizeNoHeapTid + 8, "oversized tuple"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckPGBTThirdPage(tc.leaf, tc.size)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("CheckPGBTThirdPage(%v, %d) = %v, want nil", tc.leaf, tc.size, err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("CheckPGBTThirdPage(%v, %d) = nil, want %q", tc.leaf, tc.size, tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("CheckPGBTThirdPage(%v, %d) = %v, want it to mention %q", tc.leaf, tc.size, err, tc.wantErr)
			}
		})
	}
}

// TestInsertRejectsOversizedLeafRow is the gate reached through the front door.
// Upstream raises ERRCODE_PROGRAM_LIMIT_EXCEEDED here ("Values larger than 1/3
// of a buffer page cannot be indexed"); the point of the guard is that the row
// is refused at INSERT rather than accepted and then discovered at split time.
func TestInsertRejectsOversizedLeafRow(t *testing.T) {
	bt, _, cleanup := newTestTree(t)
	defer cleanup()

	f := bt.format()
	ptr := storage.ItemPointer{Block: 1, Offset: 1}

	// Largest key whose MAXALIGNed body still fits BTMaxItemSize, and the next
	// quantum up. Sizing from bodySize rather than from a literal keeps the
	// boundary correct if the tuple header ever changes.
	okKey := make([]byte, BTMaxItemSize-MaxAlign(f.bodySize(nil)))
	if got := MaxAlign(f.bodySize(okKey)); got != BTMaxItemSize {
		t.Fatalf("sizing is off: body %d, want exactly BTMaxItemSize %d", got, BTMaxItemSize)
	}
	if err := bt.Insert(okKey, ptr); err != nil {
		t.Fatalf("Insert of a key exactly at BTMaxItemSize: %v", err)
	}

	tooBig := make([]byte, len(okKey)+8)
	tooBig[0] = 1 // sort it after okKey; irrelevant, it must never land
	err := bt.Insert(tooBig, storage.ItemPointer{Block: 1, Offset: 2})
	if err == nil {
		t.Fatal("Insert of an oversized index row succeeded; _bt_check_third_page must reject it")
	}
	if !strings.Contains(err.Error(), "index row size") {
		t.Fatalf("Insert error = %v, want upstream's \"index row size ... exceeds btree version 4 maximum\"", err)
	}
}

// TestBulkSeparatorReserveIsExactAndSufficient drives the bulk loader with wide,
// VARIABLE-length keys — the shape that makes a constant reserve either wrong or
// wasteful — and asserts both directions at once:
//
//   - sufficient: every page flushed, and every non-rightmost page carries a
//     parseable pivot high key (walkPivotInvariants). A reserve that under-shot
//     would surface as a flushPage error or a missing separator.
//   - exact: at least one leaf page is packed tighter than the old 268-byte
//     constant reserve (4 ItemIdData + 8 IndexTupleData + 256 MaxHighKeyLen)
//     could ever have allowed. This is the assertion that fails if a later
//     cleanup quietly restores a worst-case constant.
func TestBulkSeparatorReserveIsExactAndSufficient(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 64})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() {
		_ = pool.Close()
		_ = mgr.Close()
	}()

	// Keys of swinging width: 8..~120 bytes, still strictly ascending so the
	// loader sees a sorted run. BulkCreate sorts internally, so ordering only
	// has to be well defined, but ascending keeps the intent readable.
	const n = 6000
	entries := make([]BulkEntry, n)
	for i := range entries {
		width := 8 + (i%15)*8
		key := make([]byte, 0, width+8)
		key = append(key, EncodeInt4(int32(i))...)
		for len(key) < width {
			key = append(key, byte(i))
		}
		entries[i] = BulkEntry{
			Key: key,
			Ptr: storage.ItemPointer{Block: storage.BlockNumber(i/100 + 1), Offset: uint16(i%100 + 1)},
		}
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 9310, Fork: storage.MainFork}
	bt, err := BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}

	nblocks, err := pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks: %v", err)
	}
	internal, leafHK := walkPivotInvariants(t, pool, rel, nblocks)
	if internal == 0 || leafHK == 0 {
		t.Fatalf("tree has %d internal pages and %d non-rightmost leaves — the guard checked nothing",
			internal, leafHK)
	}

	// The old worst-case constant, restated so the comparison is not a magic
	// number: ItemIdData + IndexTupleData + the retired MaxHighKeyLen.
	const legacyReserve = 4 + SizeOfIndexTupleData + 256
	tightest := storage.BlockSize
	for blk := rootStart; blk < nblocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin block %d: %v", blk, err)
		}
		slot.RLock()
		op := ParseOpaque(slot.Page())
		h := storage.MustHeader(slot.Page())
		free := int(h.Upper()) - int(h.Lower())
		leaf := op.IsLeaf() && !op.IsDeleted() && op.HasHighKey()
		slot.RUnlock()
		pool.Unpin(slot)
		if leaf && free < tightest {
			tightest = free
		}
	}
	if tightest >= legacyReserve {
		t.Fatalf("tightest packed leaf still has %d free bytes; the constant %d-byte reserve was never given back",
			tightest, legacyReserve)
	}

	// The regained space must not have cost correctness: the tree still answers.
	for _, i := range []int{0, 1, n / 3, n - 1} {
		if _, ok, err := bt.Search(entries[i].Key); err != nil || !ok {
			t.Errorf("Search(entry %d) = ok %v, err %v", i, ok, err)
		}
	}
}
