package wal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// splitTestPage builds an 8 KiB page with a standard header, content outside the
// free-space hole, and zeros inside it, so its full-page image round-trips
// byte-for-byte (the hole is reconstructed as zeros). Shared with the vacuum /
// newroot / page-image FPI tests; it predates this file's split-specific
// helpers and is not btree-shaped.
func splitTestPage(marker byte) storage.Page {
	p := make(storage.Page, storage.BlockSize)
	for i := storage.SizeOfPageHeaderData; i < 100; i++ {
		p[i] = marker
	}
	for i := 7000; i < storage.BlockSize; i++ {
		p[i] = marker ^ 0xFF
	}
	h := storage.MustHeader(p)
	h.SetLower(100)
	h.SetUpper(7000)
	return p
}

// splitTestPages builds the three pages _bt_split mutates: the post-split left
// half (which the record carries as an image), the brand-new right sibling
// (carried as content), and — when sibBlk is valid — the page that used to be
// left's right sibling, already relinked.
//
// Items are PG pivot tuples at every level on purpose: _bt_restore_page and its
// producer are FORMAT-FREE (framed only by each tuple's own t_info size), so the
// item payload's meaning is irrelevant to what this record does with it.
func splitTestPages(t *testing.T, level uint32, leftBlk, rightBlk, sibBlk storage.BlockNumber, nright int) (left, right, sib storage.Page) {
	t.Helper()

	pgSib := btree.PNone
	if sibBlk != storage.InvalidBlockNumber {
		pgSib = sibBlk
	}
	flags := uint16(0)
	if level == 0 {
		flags = btree.BTPLeaf
	}

	right = make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(right); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(right, btree.PGBTPageOpaque{Prev: leftBlk, Next: pgSib, Level: level, Flags: flags})
	// A non-rightmost right page inherits the split page's high key at P_HIKEY;
	// it is simply the first line pointer, which is why the restore payload
	// needs no special case for it.
	if pgSib != btree.PNone {
		if _, err := storage.PageAddItemRaw(right, btree.PGBTPivotRaw([]byte("inherited-hikey"), btree.PNone)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range nright {
		key := []byte{byte('r'), byte('0' + i)}
		if _, err := storage.PageAddItemRaw(right, btree.PGBTPivotRaw(key, storage.BlockNumber(100+i))); err != nil {
			t.Fatal(err)
		}
	}

	left = make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(left); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(left, btree.PGBTPageOpaque{
		Prev: btree.PNone, Next: rightBlk, Level: level,
		Flags: flags | btree.BTPIncompleteSplit,
	})
	for _, raw := range [][]byte{
		btree.PGBTPivotRaw([]byte("new-separator"), btree.PNone), // P_HIKEY
		btree.PGBTPivotRaw([]byte("l0"), 10),
		btree.PGBTPivotRaw([]byte("l1"), 11),
	} {
		if _, err := storage.PageAddItemRaw(left, raw); err != nil {
			t.Fatal(err)
		}
	}

	if sibBlk != storage.InvalidBlockNumber {
		sib = make(storage.Page, storage.BlockSize)
		if err := btree.InitPGBTPage(sib); err != nil {
			t.Fatal(err)
		}
		btree.WritePGOpaque(sib, btree.PGBTPageOpaque{Prev: rightBlk, Next: btree.PNone, Level: level, Flags: flags})
	}
	return left, right, sib
}

func pageItemsRaw(t *testing.T, page storage.Page) [][]byte {
	t.Helper()
	n, err := storage.PageLinePointerCount(page)
	if err != nil {
		t.Fatal(err)
	}
	items := make([][]byte, 0, n)
	for slot := 1; slot <= n; slot++ {
		raw, err := storage.PageGetItemRaw(page, uint16(slot))
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, raw)
	}
	return items
}

// TestEncodeBtreeSplitPGIsContentParity pins the record SHAPE against upstream
// _bt_split (nbtinsert.c:1966-2060) and the redo that consumes it: an
// xl_btree_split main-data struct — the thing the previous image-only encoder
// omitted entirely, and the first thing btree_xlog_split casts — plus a right
// sibling carried as _bt_restore_page CONTENT rather than as an image, because
// redo rebuilds that page from scratch whether or not an image is present.
func TestEncodeBtreeSplitPGIsContentParity(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 81, Fork: storage.MainFork}
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(4), storage.BlockNumber(9), storage.BlockNumber(5)
	left, right, sib := splitTestPages(t, 0, leftBlk, rightBlk, sibBlk, 3)

	framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, left, right, sibBlk, sib)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeSplitR {
		t.Fatalf("rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, xlogBtreeSplitR)
	}
	if got := len(dec.XLog.MainData); got != sizeOfXLogBtreeSplitData {
		t.Fatalf("main data %d bytes, want %d (SizeOfBtreeSplit)", got, sizeOfXLogBtreeSplitData)
	}
	md := dec.XLog.MainData
	if got := binary.LittleEndian.Uint32(md[0:4]); got != 0 {
		t.Errorf("xl_btree_split.level = %d, want 0", got)
	}
	// The post-split left page holds a high key plus two data items, and it is
	// never rightmost (it links to the new right page), so its first data offset
	// is P_HIKEY+1 = 2 and the right half begins at offset 4.
	const wantFirstRight = 4
	if got := binary.LittleEndian.Uint16(md[4:6]); got != wantFirstRight {
		t.Errorf("xl_btree_split.firstrightoff = %d, want %d", got, wantFirstRight)
	}
	if got := binary.LittleEndian.Uint16(md[6:8]); got != wantFirstRight {
		t.Errorf("xl_btree_split.newitemoff = %d, want %d (SPLIT_R: the new item is the first right item)", got, wantFirstRight)
	}
	if got := binary.LittleEndian.Uint16(md[8:10]); got != 0 {
		t.Errorf("xl_btree_split.postingoff = %d, want 0", got)
	}

	byID := map[byte]XLogBlockRef{}
	for _, b := range dec.XLog.Blocks {
		byID[b.ID] = b
	}
	if len(byID) != 3 {
		t.Fatalf("want block-ids 0/1/2, got %d refs", len(dec.XLog.Blocks))
	}
	if b := byID[0]; b.Block != leftBlk || !b.HasImage || !b.ImageApply {
		t.Errorf("block 0 = blk %d image=%v apply=%v, want %d/true/true", b.Block, b.HasImage, b.ImageApply, leftBlk)
	}
	// The right page must NOT be an image: btree_xlog_split's block-1 arm runs
	// _bt_pageinit + _bt_restore_page unconditionally, so an image would be
	// restored and then immediately overwritten by whatever the data says.
	b1 := byID[1]
	if b1.Block != rightBlk || b1.HasImage || !b1.WillInit {
		t.Errorf("block 1 = blk %d image=%v willinit=%v, want %d/false/true", b1.Block, b1.HasImage, b1.WillInit, rightBlk)
	}
	wantData, err := btree.PGRestorePageData(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1.Data, wantData) {
		t.Errorf("block 1 data is not the _bt_restore_page item area")
	}
	if len(b1.Data) >= storage.BlockSize/4 {
		t.Errorf("block 1 data is %d bytes for four pivots — suspiciously page-sized", len(b1.Data))
	}
	// The old right sibling only loses its back-link, which redo derives from
	// block 1's tag — so the record carries nothing for it.
	if b := byID[2]; b.Block != sibBlk || b.HasImage || len(b.Data) != 0 {
		t.Errorf("block 2 = blk %d image=%v %d data bytes, want %d/false/0", b.Block, b.HasImage, len(b.Data), sibBlk)
	}
}

// TestEncodeBtreeSplitPGRightmostHasNoSiblingBlock pins the rightmost split: no
// block 2, and the right page's forward link is P_NONE — which is exactly what
// redo reconstructs from the record's own missing block tag.
func TestEncodeBtreeSplitPGRightmostHasNoSiblingBlock(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 82, Fork: storage.MainFork}
	const leftBlk, rightBlk = storage.BlockNumber(4), storage.BlockNumber(9)
	left, right, _ := splitTestPages(t, 0, leftBlk, rightBlk, storage.InvalidBlockNumber, 2)

	framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, left, right, storage.InvalidBlockNumber, nil)
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
	if len(dec.XLog.Blocks) != 2 {
		t.Fatalf("rightmost split registered %d blocks, want 2", len(dec.XLog.Blocks))
	}
	if _, ok := xlogBlockRefByID(dec.XLog, 2); ok {
		t.Error("rightmost split registered a block 2")
	}
}

// TestEncodeBtreeSplitPGRejectsUnredoableRightOpaque is the mutation guard on
// the encoder's central claim: the right page's opaque header is NOT carried,
// so redo derives it from the record's level and block tags. A right page whose
// header differs from that derivation would replay into a DIFFERENT page than
// the primary wrote — silently, since every field involved is metadata rather
// than an item. Refusing at emit time is the only place goopg can see it.
func TestEncodeBtreeSplitPGRejectsUnredoableRightOpaque(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 83, Fork: storage.MainFork}
	const leftBlk, rightBlk, sibBlk = storage.BlockNumber(4), storage.BlockNumber(9), storage.BlockNumber(5)

	for _, tc := range []struct {
		name   string
		mutate func(op *btree.PGBTPageOpaque)
	}{
		{"garbage hint", func(op *btree.PGBTPageOpaque) { op.Flags |= btree.BTPHasGarbage }},
		{"wrong back-link", func(op *btree.PGBTPageOpaque) { op.Prev = 77 }},
		{"wrong forward link", func(op *btree.PGBTPageOpaque) { op.Next = 78 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right, sib := splitTestPages(t, 0, leftBlk, rightBlk, sibBlk, 2)
			op := btree.ReadPGOpaque(right)
			tc.mutate(&op)
			btree.WritePGOpaque(right, op)
			if _, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, left, right, sibBlk, sib); err == nil {
				t.Fatalf("want an error for a right page redo cannot reproduce (%s)", tc.name)
			}
		})
	}
}

// TestApplyRecordReplaysPGBtreeSplit drives the record through emit → encode →
// decode → ApplyRecord and asserts the replayed pages REPRODUCE the writer's:
// the right sibling rebuilt from block data with the same items at the same
// offsets and the same opaque, the left half restored from its image, and the
// old sibling's back-link swung to the new right page. The right block does not
// exist before replay — WILL_INIT means redo extends it, exactly as
// XLogInitBufferForRedo does.
func TestApplyRecordReplaysPGBtreeSplit(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 84, Fork: storage.MainFork}
	// Block 0 is the metapage in any real tree, and P_NONE is block 0 — a left
	// half at block 0 would be indistinguishable from "leftmost" in the right
	// page's back-link. Lay the tree out the way a real one is.
	const leftBlk, sibBlk, rightBlk = storage.BlockNumber(1), storage.BlockNumber(2), storage.BlockNumber(3)
	left, right, sib := splitTestPages(t, 0, leftBlk, rightBlk, sibBlk, 3)

	// Blocks 0..2 pre-exist: a metapage placeholder, the page about to be split
	// (still PRE-split on disk) and its current right sibling (still pointing
	// back at it).
	meta := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(meta); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, meta); err != nil {
		t.Fatal(err)
	}
	preLeft := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(preLeft); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(preLeft, btree.PGBTPageOpaque{Prev: btree.PNone, Next: sibBlk, Flags: btree.BTPLeaf})
	preSib := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(preSib); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(preSib, btree.PGBTPageOpaque{Prev: leftBlk, Next: btree.PNone, Flags: btree.BTPLeaf})
	for _, page := range []storage.Page{preLeft, preSib} {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}

	framed, err := EncodeBtreeSplitPG(rel, leftBlk, rightBlk, left, right, sibBlk, sib)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 1200)

	gotRight := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rightBlk, gotRight); err != nil {
		t.Fatal(err)
	}
	wantItems, gotItems := pageItemsRaw(t, right), pageItemsRaw(t, gotRight)
	if len(gotItems) != len(wantItems) {
		t.Fatalf("replayed right page has %d items, want %d", len(gotItems), len(wantItems))
	}
	for i := range wantItems {
		if !bytes.Equal(gotItems[i], wantItems[i]) {
			t.Errorf("replayed right item at offset %d differs from the writer's", i+1)
		}
	}
	if got, want := btree.ReadPGOpaque(gotRight), btree.ReadPGOpaque(right); got != want {
		t.Errorf("replayed right opaque = %+v, want %+v", got, want)
	}
	if lsn := storage.MustHeader(gotRight).LSN(); lsn != 1200 {
		t.Errorf("replayed right pd_lsn = %d, want 1200", lsn)
	}

	gotLeft := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leftBlk, gotLeft); err != nil {
		t.Fatal(err)
	}
	if got, want := btree.ReadPGOpaque(gotLeft), btree.ReadPGOpaque(left); got != want {
		t.Errorf("replayed left opaque = %+v, want the post-split %+v", got, want)
	}
	if n := len(pageItemsRaw(t, gotLeft)); n != 3 {
		t.Errorf("replayed left page has %d items, want the post-split 3", n)
	}

	gotSib := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, sibBlk, gotSib); err != nil {
		t.Fatal(err)
	}
	if op := btree.ReadPGOpaque(gotSib); op.Prev != rightBlk {
		t.Errorf("replayed sibling btpo_prev = %d, want the new right page %d", op.Prev, rightBlk)
	}

	// Per-block pd_lsn idempotency: re-applying at the same LSN is a no-op.
	applyPGRecord(t, mgr, framed, 1200)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rightBlk, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, gotRight) {
		t.Error("second apply at the same LSN changed the right page")
	}
}
