package xlog

// Guards for M0130-S11.5b-2 — the split record's INCREMENTAL left half at the
// RECORD level: the block-0 shape, the SPLIT_L/_R opcode that tells redo how
// many tuples the untagged block data holds, the fallback to a full-page image
// when the primary's split is not describable, and a replay reproduction of the
// left half at matching OFFSETS.
//
// The btree package's pgsplitleft_test.go owns the page-level half (offset
// derivation, framing, and the premise test that a REAL tree's splits are
// describable at all).

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// splitLeftTrio builds the three pages the encoder reconciles, in the shape
// `splitPage` writes them: a pre-split leaf with four items under an inherited
// high key, and the two halves with a fifth (new) item spliced in at
// `spliceAt`, `leftCount` of the merged items staying on the left.
func splitLeftTrio(t *testing.T, spliceAt, leftCount int, leftBlk, rightBlk, sibBlk storage.BlockNumber) (pre, left, right, sib storage.Page, newItem []byte) {
	t.Helper()
	newItem = nbtree.PGBTPivotRaw([]byte("newitem"), 77)
	preItems := make([][]byte, 4)
	for i := range preItems {
		preItems[i] = nbtree.PGBTPivotRaw([]byte{'p', byte('0' + i)}, storage.BlockNumber(50+i))
	}
	merged := make([][]byte, 0, len(preItems)+1)
	merged = append(merged, preItems[:spliceAt]...)
	merged = append(merged, newItem)
	merged = append(merged, preItems[spliceAt:]...)

	inheritedHK := nbtree.PGBTPivotRaw([]byte("inherited-hikey"), nbtree.PNone)
	newHK := nbtree.PGBTPivotRaw([]byte("new-separator"), nbtree.PNone)
	build := func(op nbtree.PGBTPageOpaque, hk []byte, items [][]byte) storage.Page {
		p := make(storage.Page, storage.BlockSize)
		if err := nbtree.InitPGBTPage(p); err != nil {
			t.Fatal(err)
		}
		nbtree.WritePGOpaque(p, op)
		for _, raw := range append([][]byte{hk}, items...) {
			if _, err := storage.PageAddItemRaw(p, raw); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	pre = build(nbtree.PGBTPageOpaque{Prev: nbtree.PNone, Next: sibBlk, Flags: nbtree.BTPLeaf}, inheritedHK, preItems)
	left = build(nbtree.PGBTPageOpaque{Prev: nbtree.PNone, Next: rightBlk, Flags: nbtree.BTPLeaf | nbtree.BTPIncompleteSplit}, newHK, merged[:leftCount])
	right = build(nbtree.PGBTPageOpaque{Prev: leftBlk, Next: sibBlk, Flags: nbtree.BTPLeaf}, inheritedHK, merged[leftCount:])
	sib = make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(sib); err != nil {
		t.Fatal(err)
	}
	nbtree.WritePGOpaque(sib, nbtree.PGBTPageOpaque{Prev: rightBlk, Next: nbtree.PNone, Flags: nbtree.BTPLeaf})
	return pre, left, right, sib, newItem
}

// TestEncodeBtreeSplitPGLeftHalfIsIncremental pins the default form against
// upstream `_bt_split` (nbtinsert.c:1990-2010): block 0 carries the new item
// (only when it landed on the left) followed by the page's new high key, and NO
// image — the whole point of the slice, since an image is a page per split.
func TestEncodeBtreeSplitPGLeftHalfIsIncremental(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 91, Fork: storage.MainFork}
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(4), storage.BlockNumber(9), storage.BlockNumber(5)

	for _, tc := range []struct {
		name                string
		spliceAt, leftCount int
		wantInfo            uint8
		wantFirstRight      uint16
		wantNewItemOff      uint16
	}{
		{"new item on the left", 1, 3, xlogBtreeSplitL, 4, 3},
		{"new item on the right", 2, 2, xlogBtreeSplitR, 4, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pre, left, right, sib, newItem := splitLeftTrio(t, tc.spliceAt, tc.leftCount, leftBlk, rightBlk, sibBlk)
			framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, pre, left, right, newItem, sibBlk, sib, storage.InvalidBlockNumber)
			if err != nil {
				t.Fatal(err)
			}
			rec, _, err := encodeRecordXLog(framed, 0)
			if err != nil {
				t.Fatal(err)
			}
			dec, err := decodeRecordXLogDetailed(rec)
			if err != nil {
				t.Fatal(err)
			}
			if dec.Header.Info != tc.wantInfo {
				t.Fatalf("info = %#x, want %#x (the opcode is redo's only record of which half the new item landed on)", dec.Header.Info, tc.wantInfo)
			}
			md := dec.XLog.MainData
			if got := binary.LittleEndian.Uint16(md[4:6]); got != tc.wantFirstRight {
				t.Errorf("firstrightoff = %d, want %d", got, tc.wantFirstRight)
			}
			if got := binary.LittleEndian.Uint16(md[6:8]); got != tc.wantNewItemOff {
				t.Errorf("newitemoff = %d, want %d", got, tc.wantNewItemOff)
			}
			b0, ok := xlogBlockRefByID(dec.XLog, 0)
			if !ok {
				t.Fatal("no block 0")
			}
			if b0.HasImage {
				t.Fatal("block 0 is a full-page image — the incremental form was not taken")
			}
			gotNew, gotHK, err := nbtree.ParseSplitLeftBlockData(b0.Data, tc.wantInfo == xlogBtreeSplitL)
			if err != nil {
				t.Fatalf("block 0 data: %v", err)
			}
			wantHK, _, err := nbtree.PGHighKeyRaw(left)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotHK, wantHK) {
				t.Error("block 0 does not carry the left page's new high key")
			}
			if tc.wantInfo == xlogBtreeSplitL && !bytes.Equal(gotNew, newItem) {
				t.Error("block 0 does not carry the new item")
			}
			if tc.wantInfo == xlogBtreeSplitR && gotNew != nil {
				t.Error("a SPLIT_R record carried the new item (upstream registers it only under newitemonleft)")
			}
			if len(b0.Data) > 200 {
				t.Errorf("block 0 data is %d bytes for two pivots — suspiciously page-sized", len(b0.Data))
			}
		})
	}
}

// TestEncodeBtreeSplitPGFallsBackToImage pins the other half of the decision:
// the encoder does not TRUST that goopg's split is upstream-shaped, it verifies
// it against the pages. A left half holding an item that was never on the
// pre-split page (goopg's dedup pass merging into a posting tuple) must be
// logged as an image, because no set of offsets describes it.
func TestEncodeBtreeSplitPGFallsBackToImage(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 92, Fork: storage.MainFork}
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(4), storage.BlockNumber(9), storage.BlockNumber(5)

	pre, left, right, sib, newItem := splitLeftTrio(t, 1, 3, leftBlk, rightBlk, sibBlk)
	blockZero := func(t *testing.T, pre, left, right storage.Page, newItem []byte) XLogBlockRef {
		t.Helper()
		framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, pre, left, right, newItem, sibBlk, sib, storage.InvalidBlockNumber)
		if err != nil {
			t.Fatal(err)
		}
		rec, _, err := encodeRecordXLog(framed, 0)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := decodeRecordXLogDetailed(rec)
		if err != nil {
			t.Fatal(err)
		}
		b0, ok := xlogBlockRefByID(dec.XLog, 0)
		if !ok {
			t.Fatal("no block 0")
		}
		return b0
	}

	if b := blockZero(t, pre, left, right, newItem); b.HasImage {
		t.Fatal("the describable control case was logged as an image")
	}
	// (a) an item on the left half that the pre-split page never held.
	mutated := make(storage.Page, storage.BlockSize)
	copy(mutated, left)
	if err := storage.PageReplaceItemRaw(mutated, 3, nbtree.PGBTPivotRaw([]byte("mergedposting"), 42)); err != nil {
		t.Fatal(err)
	}
	if b := blockZero(t, pre, mutated, right, newItem); !b.HasImage || !b.ImageApply {
		t.Error("an undescribable left half was logged incrementally")
	}
	// (b) no pre-split page at all (the pre-runtime / bulk callers).
	if b := blockZero(t, nil, left, right, newItem); !b.HasImage || !b.ImageApply {
		t.Error("a record with no pre-split page was logged incrementally")
	}
	// (c) no new item.
	if b := blockZero(t, pre, left, right, nil); !b.HasImage || !b.ImageApply {
		t.Error("a record with no new item was logged incrementally")
	}
}

// TestApplyRecordReplaysPGBtreeSplitLeftIncrementally is the end-to-end guard:
// the primary's pre-split page is on disk, the record carries no image for it,
// and replay must land the same items at the same OFFSETS as the primary wrote
// — the property an image gives for free and the incremental arm has to earn.
func TestApplyRecordReplaysPGBtreeSplitLeftIncrementally(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 93, Fork: storage.MainFork}
	const leftBlk, sibBlk, rightBlk = storage.BlockNumber(1), storage.BlockNumber(2), storage.BlockNumber(3)
	pre, left, right, _, newItem := splitLeftTrio(t, 1, 3, leftBlk, rightBlk, sibBlk)

	meta := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(meta); err != nil {
		t.Fatal(err)
	}
	preSib := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(preSib); err != nil {
		t.Fatal(err)
	}
	nbtree.WritePGOpaque(preSib, nbtree.PGBTPageOpaque{Prev: leftBlk, Next: nbtree.PNone, Flags: nbtree.BTPLeaf})
	for _, page := range []storage.Page{meta, pre, preSib} {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}

	framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, pre, left, right, newItem, sibBlk, preSib, storage.InvalidBlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 1300)

	gotLeft := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leftBlk, gotLeft); err != nil {
		t.Fatal(err)
	}
	wantItems, gotItems := pageItemsRaw(t, left), pageItemsRaw(t, gotLeft)
	if len(gotItems) != len(wantItems) {
		t.Fatalf("replayed left page has %d items, want the primary's %d", len(gotItems), len(wantItems))
	}
	for i := range wantItems {
		if !bytes.Equal(gotItems[i], wantItems[i]) {
			t.Errorf("replayed left item at offset %d differs from the primary's", i+1)
		}
	}
	if got, want := nbtree.ReadPGOpaque(gotLeft), nbtree.ReadPGOpaque(left); got != want {
		t.Errorf("replayed left opaque = %+v, want the post-split %+v", got, want)
	}
	if lsn := storage.MustHeader(gotLeft).LSN(); lsn != 1300 {
		t.Errorf("replayed left pd_lsn = %d, want 1300", lsn)
	}

	// pd_lsn idempotency: the incremental arm reads the page it rewrites, so a
	// second apply that did NOT skip would cut the already-split page again.
	applyPGRecord(t, mgr, framed, 1300)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leftBlk, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, gotLeft) {
		t.Error("second apply at the same LSN changed the left page")
	}
}
