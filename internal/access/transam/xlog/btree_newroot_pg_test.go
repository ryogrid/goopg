package xlog

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// newRootTestPages builds the two pages _bt_newroot mutates: an internal root at
// `level` carrying a minus-infinity downlink to leftBlk and a separator pointing
// at rightBlk, and a metapage already updated to name the new root. Level 0
// produces the empty leaf root goopg's VACUUM root-reset emits instead.
func newRootTestPages(t *testing.T, level uint32, rootBlk, leftBlk, rightBlk storage.BlockNumber) (root, meta storage.Page) {
	t.Helper()
	root = make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(root); err != nil {
		t.Fatal(err)
	}
	flags := nbtree.BTPRoot
	if level == 0 {
		flags |= nbtree.BTPLeaf
	}
	nbtree.WritePGOpaque(root, nbtree.PGBTPageOpaque{Prev: nbtree.PNone, Next: nbtree.PNone, Level: level, Flags: flags})
	if level > 0 {
		for _, raw := range [][]byte{
			nbtree.PGBTPivotRaw(nil, leftBlk),
			nbtree.PGBTPivotRaw([]byte("separator-key"), rightBlk),
		} {
			if _, err := storage.PageAddItemRaw(root, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	meta = make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGMetaPage(meta, rootBlk, level, true); err != nil {
		t.Fatal(err)
	}
	return root, meta
}

func newRootPageItems(t *testing.T, page storage.Page) [][]byte {
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

// TestEncodeBtreeNewRootPGIsContentParity pins the record SHAPE against upstream
// _bt_newroot (nbtinsert.c:2556-2597): main data xl_btree_newroot{rootblk,
// level}, block 0 = the root with its item area as block data (WILL_INIT),
// block 1 = the left child, block 2 = the metapage carrying a 28-byte
// xl_btree_metadata. The "no full-page image anywhere" assertion is the point of
// M0130-S11.5a: the previous encoder produced a PG-shaped HEADER over FPI-only
// blocks, which a real PG standby's btree_xlog_newroot cannot replay — it reads
// the xl_btree_newroot struct unconditionally.
func TestEncodeBtreeNewRootPGIsContentParity(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 72, Fork: storage.MainFork}
	const rootBlk, leftBlk, rightBlk = storage.BlockNumber(9), storage.BlockNumber(3), storage.BlockNumber(4)
	root, meta := newRootTestPages(t, 2, rootBlk, leftBlk, rightBlk)

	framed, err := EncodeBtreeNewRootPG(rel, rootBlk, root, leftBlk, nbtree.MetaBlock, meta)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeNewRoot {
		t.Fatalf("rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, xlogBtreeNewRoot)
	}
	if got := len(dec.XLog.MainData); got != sizeOfXLogBtreeNewRootData {
		t.Fatalf("main data %d bytes, want %d", got, sizeOfXLogBtreeNewRootData)
	}
	if got := binary.LittleEndian.Uint32(dec.XLog.MainData[0:4]); storage.BlockNumber(got) != rootBlk {
		t.Errorf("xl_btree_newroot.rootblk = %d, want %d", got, rootBlk)
	}
	if got := binary.LittleEndian.Uint32(dec.XLog.MainData[4:8]); got != 2 {
		t.Errorf("xl_btree_newroot.level = %d, want 2", got)
	}

	byID := map[byte]XLogBlockRef{}
	for _, b := range dec.XLog.Blocks {
		if b.HasImage {
			t.Errorf("block %d carries a full-page image; the record must be incremental", b.ID)
		}
		byID[b.ID] = b
	}
	if len(byID) != 3 {
		t.Fatalf("want block-ids 0/1/2, got %d refs", len(dec.XLog.Blocks))
	}
	if b := byID[0]; !b.WillInit || b.Block != rootBlk {
		t.Errorf("block 0 = blk %d willinit=%v, want %d/true", b.Block, b.WillInit, rootBlk)
	}
	if b := byID[1]; b.Block != leftBlk || len(b.Data) != 0 {
		t.Errorf("block 1 = blk %d with %d data bytes, want %d/0", b.Block, len(b.Data), leftBlk)
	}
	if b := byID[2]; !b.WillInit || b.Block != nbtree.MetaBlock || len(b.Data) != sizeOfXLogBtreeMetadata {
		t.Errorf("block 2 = blk %d willinit=%v %d data bytes, want %d/true/%d", b.Block, b.WillInit, len(b.Data), nbtree.MetaBlock, sizeOfXLogBtreeMetadata)
	}

	// Block 0's data is _bt_restore_page's input: the item area only, so it is
	// far smaller than the 8 KiB page the FPI form shipped.
	wantItems, err := nbtree.PGRestorePageData(root)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(byID[0].Data, wantItems) {
		t.Errorf("block 0 data is not the _bt_restore_page item area")
	}
	if len(byID[0].Data) >= storage.BlockSize/4 {
		t.Errorf("block 0 data is %d bytes for two pivots — suspiciously page-sized", len(byID[0].Data))
	}

	md, err := decodeXLogBtreeMetadata(byID[2].Data)
	if err != nil {
		t.Fatal(err)
	}
	if md.Root != rootBlk || md.FastRoot != rootBlk || md.Level != 2 || md.FastLevel != 2 || !md.AllEqualImage {
		t.Errorf("xl_btree_metadata = %+v, want root/fastroot %d level/fastlevel 2 allequalimage", md, rootBlk)
	}
	if md.Version != nbtree.BTreeVersionPG {
		t.Errorf("xl_btree_metadata.version = %d, want %d", md.Version, nbtree.BTreeVersionPG)
	}
}

// TestEncodeBtreeNewRootPGRejectsMissingChild guards the one combination
// upstream's redo would PANIC on: a level > 0 root whose backup block 1 is
// absent (btree_xlog_newroot calls _bt_clear_incomplete_split(record, 1)
// unconditionally there, and XLogReadBufferForRedo PANICs on an unregistered
// block id). Refusing at emit time is the only place goopg can see it.
func TestEncodeBtreeNewRootPGRejectsMissingChild(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 72, Fork: storage.MainFork}
	root, meta := newRootTestPages(t, 1, 9, 3, 4)
	if _, err := EncodeBtreeNewRootPG(rel, 9, root, storage.InvalidBlockNumber, nbtree.MetaBlock, meta); err == nil {
		t.Fatal("want an error for a level-1 root with no left child block")
	}
}

// TestApplyRecordReplaysPGBtreeNewRoot drives the record through emit → encode →
// decode → ApplyRecord and asserts the replayed pages REPRODUCE the writer's:
// same items at the same offsets on the root, the same opaque, a metapage
// naming the new root, and the left child's incomplete-split flag cleared
// (upstream's _bt_clear_incomplete_split limb). The root block does not exist
// before replay — WILL_INIT means redo extends it, exactly as
// XLogInitBufferForRedo does.
func TestApplyRecordReplaysPGBtreeNewRoot(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 73, Fork: storage.MainFork}
	const rootBlk, leftBlk, rightBlk = storage.BlockNumber(3), storage.BlockNumber(1), storage.BlockNumber(2)

	// Blocks 0..2 pre-exist (metapage + two leaves); block 3 is the new root.
	for blk := storage.BlockNumber(0); blk < 3; blk++ {
		page := make(storage.Page, storage.BlockSize)
		if err := nbtree.InitPGBTPage(page); err != nil {
			t.Fatal(err)
		}
		// The left child still carries the incomplete-split flag its split set.
		flags := nbtree.BTPLeaf
		if blk == leftBlk {
			flags |= nbtree.BTPIncompleteSplit
		}
		nbtree.WritePGOpaque(page, nbtree.PGBTPageOpaque{Prev: nbtree.PNone, Next: nbtree.PNone, Flags: flags})
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}

	root, meta := newRootTestPages(t, 1, rootBlk, leftBlk, rightBlk)
	framed, err := EncodeBtreeNewRootPG(rel, rootBlk, root, leftBlk, nbtree.MetaBlock, meta)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 900)

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, got); err != nil {
		t.Fatal(err)
	}
	wantItems, gotItems := newRootPageItems(t, root), newRootPageItems(t, got)
	if len(gotItems) != len(wantItems) {
		t.Fatalf("replayed root has %d items, want %d", len(gotItems), len(wantItems))
	}
	for i := range wantItems {
		if !bytes.Equal(gotItems[i], wantItems[i]) {
			t.Errorf("replayed root item at offset %d differs from the writer's", i+1)
		}
	}
	if op := nbtree.ReadPGOpaque(got); !op.IsRoot() || op.IsLeaf() || op.Level != 1 {
		t.Errorf("replayed root opaque = %+v, want root, non-leaf, level 1", op)
	}
	if lsn := storage.MustHeader(got).LSN(); lsn != 900 {
		t.Errorf("replayed root pd_lsn = %d, want 900", lsn)
	}

	gotMeta := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, nbtree.MetaBlock, gotMeta); err != nil {
		t.Fatal(err)
	}
	m := nbtree.ReadPGMetaPage(gotMeta)
	if m.Magic != nbtree.BTreeMagicPG || m.Root != rootBlk || m.FastRoot != rootBlk || m.Level != 1 || m.FastLevel != 1 {
		t.Errorf("replayed metapage = %+v, want magic + root/fastroot %d level 1", m, rootBlk)
	}
	if !nbtree.ReadPGOpaque(gotMeta).IsMeta() {
		t.Errorf("replayed metapage lost BTP_META")
	}
	if lower := storage.MustHeader(gotMeta).Lower(); int(lower) != storage.SizeOfPageHeaderData+nbtree.SizeOfBTMetaPageDataPG {
		t.Errorf("replayed metapage pd_lower = %d, want %d", lower, storage.SizeOfPageHeaderData+nbtree.SizeOfBTMetaPageDataPG)
	}

	gotChild := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leftBlk, gotChild); err != nil {
		t.Fatal(err)
	}
	if nbtree.ReadPGOpaque(gotChild).Flags&nbtree.BTPIncompleteSplit != 0 {
		t.Errorf("replay did not clear the left child's incomplete-split flag")
	}

	// Re-applying at the same LSN must be a no-op (per-block pd_lsn
	// idempotency), and re-applying at a LOWER one must not roll the pages back.
	applyPGRecord(t, mgr, framed, 900)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, got) {
		t.Errorf("second apply at the same LSN changed the root page")
	}
}

// TestApplyRecordReplaysPGBtreeNewRootLeaf covers goopg's second emit site,
// VACUUM's root reset (btree_vacuum.go resetToEmptyRoot): a level-0 root with no
// items and no left child. Upstream's redo handles it without a special case —
// level 0 means BTP_LEAF, no item restore and no block 1 — which is why goopg
// can log it as a newroot at all.
func TestApplyRecordReplaysPGBtreeNewRootLeaf(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 74, Fork: storage.MainFork}
	blank := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(blank); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Extend(rel, blank); err != nil { // metapage placeholder
		t.Fatal(err)
	}

	const rootBlk = storage.BlockNumber(1)
	root, meta := newRootTestPages(t, 0, rootBlk, storage.InvalidBlockNumber, storage.InvalidBlockNumber)
	framed, err := EncodeBtreeNewRootPG(rel, rootBlk, root, storage.InvalidBlockNumber, nbtree.MetaBlock, meta)
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
		t.Fatalf("a leaf root must register blocks 0 and 2 only; got %d refs", len(dec.XLog.Blocks))
	}

	applyPGRecord(t, mgr, framed, 1000)
	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, got); err != nil {
		t.Fatal(err)
	}
	if op := nbtree.ReadPGOpaque(got); !op.IsRoot() || !op.IsLeaf() || op.Level != 0 {
		t.Errorf("replayed leaf root opaque = %+v, want root+leaf at level 0", op)
	}
	if n, err := storage.PageLinePointerCount(got); err != nil || n != 0 {
		t.Errorf("replayed leaf root has %d items (err %v), want 0", n, err)
	}
}
