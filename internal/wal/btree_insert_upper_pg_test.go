package wal

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// --- M0131-S21b part 1: the metapage-and-downlink btree opcodes -------------
//
// XLOG_BTREE_INSERT_UPPER (0x10), XLOG_BTREE_INSERT_META (0x20) and
// XLOG_BTREE_META_CLEANUP (0xE0). goopg emits none of them — its own downlink
// inserts ride RecordKindBtreeSplit/NewRoot and it has no _bt_set_cleanup_info
// — so before this slice every one of them fell into RM_BTREE's `default:` arm
// and, since S16.3, REFUSED the start unless every mutated block happened to
// carry a full-page image. A real PG index of more than one level emits
// INSERT_UPPER on essentially every root-ward split, so the refusal fired on
// ordinary traffic.
//
// The record bytes are hand-built here because there is no goopg encoder for a
// record goopg does not produce.
//
// Design: docs/design/0131-0015-pg-wal-opcode-coverage.md §S21b.

// btreeInternalPage builds an internal (non-leaf) btree page at `level`
// carrying the given pivot downlinks, in item order.
func btreeInternalPage(t *testing.T, level uint32, downlinks ...storage.BlockNumber) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(page, btree.PGBTPageOpaque{Prev: btree.PNone, Next: btree.PNone, Level: level})
	for i, blk := range downlinks {
		var key []byte
		if i > 0 { // slot 1 is the minus-infinity downlink: no key
			key = []byte("k")
		}
		if _, err := storage.PageAddItemRaw(page, btree.PGBTPivotRaw(key, blk)); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

// btreeLeafPage builds an empty leaf page, optionally still flagged
// BTP_INCOMPLETE_SPLIT the way a child is between its split and the parent's
// downlink insert.
func btreeLeafPage(t *testing.T, incompleteSplit bool) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	flags := btree.BTPLeaf
	if incompleteSplit {
		flags |= btree.BTPIncompleteSplit
	}
	btree.WritePGOpaque(page, btree.PGBTPageOpaque{Prev: btree.PNone, Next: btree.PNone, Flags: flags})
	return page
}

// buildBtreeInsertPG assembles a real xl_btree_insert record: main data is the
// 2-byte offnum, block 0 is the target page's new item, block 1 (when
// childBlk is valid) the child whose incomplete split this insert finishes, and
// block 2 (when md != nil) the WILL_INIT metapage carrying xl_btree_metadata.
// Mirrors _bt_insertonpg's XLogRegisterBuffer calls (nbtinsert.c:1305-1365).
func buildBtreeInsertPG(t *testing.T, info uint8, rel storage.RelFileNode, blk storage.BlockNumber,
	offnum uint16, item []byte, childBlk storage.BlockNumber, md *btree.PGBTMetaPage,
) []byte {
	t.Helper()
	mainData := make([]byte, sizeOfXLogBtreeInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], offnum)

	refs := []BlockRef{{ID: 0, Rel: rel, Block: blk, Data: item}}
	if childBlk != storage.InvalidBlockNumber {
		refs = append(refs, BlockRef{ID: 1, Rel: rel, Block: childBlk, SameRel: true})
	}
	if md != nil {
		refs = append(refs, BlockRef{
			ID: 2, Rel: rel, Block: btree.MetaBlock, SameRel: true,
			Data: encodeXLogBtreeMetadata(*md), WillInit: true,
		})
	}
	body, err := assembleXLogRecord(mainData, refs)
	if err != nil {
		t.Fatalf("assembleXLogRecord: %v", err)
	}
	return framePGAssembled(RmgrBtree, info, 0, body)
}

// btreeSeedRel extends `rel` with the given pages, block 0 first.
func btreeSeedRel(t *testing.T, mgr *storage.Manager, rel storage.RelFileNode, pages ...storage.Page) {
	t.Helper()
	for _, p := range pages {
		if _, err := mgr.Extend(rel, p); err != nil {
			t.Fatal(err)
		}
	}
}

// TestApplyRecordReplaysPGBtreeInsertUpper is the ordinary internal-page
// downlink insert: the new pivot lands at the recorded offset number AND the
// child's BTP_INCOMPLETE_SPLIT flag is cleared. Both halves matter — upstream's
// btree_xlog_insert clears the child first (nbtxlog.c:172-173), and leaving the
// flag set makes every later insert descending through that child try to finish
// a split that already completed.
func TestApplyRecordReplaysPGBtreeInsertUpper(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5101, Fork: storage.MainFork}
	const metaBlk, childBlk, rootBlk = storage.BlockNumber(0), storage.BlockNumber(1), storage.BlockNumber(2)

	meta := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGMetaPage(meta, rootBlk, 1, true); err != nil {
		t.Fatal(err)
	}
	btreeSeedRel(t, mgr, rel,
		meta,
		btreeLeafPage(t, true),            // block 1: child mid-split
		btreeInternalPage(t, 1, childBlk), // block 2: root with one downlink
	)

	newItem := btree.PGBTPivotRaw([]byte("sep"), storage.BlockNumber(3))
	framed := buildBtreeInsertPG(t, xlogBtreeInsertUpper, rel, rootBlk, 2, newItem, childBlk, nil)
	applyPGRecord(t, mgr, framed, 4100)

	root := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, root); err != nil {
		t.Fatal(err)
	}
	if n, err := storage.PageLinePointerCount(root); err != nil || n != 2 {
		t.Fatalf("root has %d items (err %v), want 2", n, err)
	}
	got, err := storage.PageGetItemRaw(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newItem) {
		t.Errorf("item at offset 2 is not the record's new pivot")
	}
	if lsn := storage.MustHeader(root).LSN(); lsn != 4100 {
		t.Errorf("root pd_lsn = %d, want 4100", lsn)
	}

	child := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, childBlk, child); err != nil {
		t.Fatal(err)
	}
	if btree.ReadPGOpaque(child).Flags&btree.BTPIncompleteSplit != 0 {
		t.Errorf("replay left BTP_INCOMPLETE_SPLIT set on the child")
	}
}

// TestApplyRecordRefusesPGBtreeInsertUpperWithoutChild pins the "block 1 is not
// optional" reading of upstream: _bt_insertonpg registers cbuf unconditionally
// on the !isleaf path, and btree_xlog_insert calls
// _bt_clear_incomplete_split(record, 1) unconditionally in return — upstream
// PANICs on an unregistered block id. Silently skipping the limb instead would
// leave a permanently half-split child with replay reporting success.
func TestApplyRecordRefusesPGBtreeInsertUpperWithoutChild(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5102, Fork: storage.MainFork}
	btreeSeedRel(t, mgr, rel, btreeInternalPage(t, 1, storage.BlockNumber(1)))

	framed := buildBtreeInsertPG(t, xlogBtreeInsertUpper, rel, 0, 2,
		btree.PGBTPivotRaw([]byte("sep"), 3), storage.InvalidBlockNumber, nil)
	err := applyPGRecordErr(t, mgr, framed, 4200)
	if err == nil || !strings.Contains(err.Error(), "missing block 1") {
		t.Fatalf("err = %v, want a refusal naming the missing child block", err)
	}
}

// TestApplyRecordReplaysPGBtreeInsertMeta adds the metapage limb: the same
// internal insert PLUS a rebuilt metapage. The metapage block is WILL_INIT, so
// redo re-initialises it from the carried xl_btree_metadata rather than
// read-modify-writing whatever was there — this asserts the new fast-root
// values land and BTP_META survives.
func TestApplyRecordReplaysPGBtreeInsertMeta(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5103, Fork: storage.MainFork}
	const childBlk, rootBlk = storage.BlockNumber(1), storage.BlockNumber(2)

	stale := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGMetaPage(stale, childBlk, 0, true); err != nil {
		t.Fatal(err)
	}
	btreeSeedRel(t, mgr, rel, stale, btreeLeafPage(t, true), btreeInternalPage(t, 1, childBlk))

	md := btree.PGBTMetaPage{
		Version: btree.BTreeVersionPG, Root: rootBlk, Level: 1,
		FastRoot: rootBlk, FastLevel: 1, LastCleanupNumDelpages: 7, AllEqualImage: true,
	}
	framed := buildBtreeInsertPG(t, xlogBtreeInsertMeta, rel, rootBlk, 2,
		btree.PGBTPivotRaw([]byte("sep"), 3), childBlk, &md)
	applyPGRecord(t, mgr, framed, 4300)

	gotMeta := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, btree.MetaBlock, gotMeta); err != nil {
		t.Fatal(err)
	}
	m := btree.ReadPGMetaPage(gotMeta)
	if m.Magic != btree.BTreeMagicPG || m.Root != rootBlk || m.FastRoot != rootBlk || m.Level != 1 || m.FastLevel != 1 {
		t.Errorf("replayed metapage = %+v, want magic + root/fastroot %d at level 1", m, rootBlk)
	}
	if m.LastCleanupNumDelpages != 7 {
		t.Errorf("btm_last_cleanup_num_delpages = %d, want 7", m.LastCleanupNumDelpages)
	}
	if !btree.ReadPGOpaque(gotMeta).IsMeta() {
		t.Errorf("replayed metapage lost BTP_META")
	}

	root := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, rootBlk, root); err != nil {
		t.Fatal(err)
	}
	if n, err := storage.PageLinePointerCount(root); err != nil || n != 2 {
		t.Errorf("INSERT_META dropped the block-0 insert: %d items (err %v), want 2", n, err)
	}
}

// TestApplyRecordPGBtreeInsertMetaRestoresMetaBehindABlock0Image is the limb
// that is easy to get wrong: when block 0 arrives as a full-page image, redo
// must NOT return early. Upstream's XLogReadBufferForRedo reports BLK_RESTORED
// for block 0 only, and btree_xlog_insert still falls through to
// `if (ismeta) _bt_restore_meta(record, 2)`. Returning after restoring the
// image — the shape the pre-S21b insert replay had — leaves the metapage
// pointing at a stale root while replay reports success.
func TestApplyRecordPGBtreeInsertMetaRestoresMetaBehindABlock0Image(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5104, Fork: storage.MainFork}
	const childBlk, rootBlk = storage.BlockNumber(1), storage.BlockNumber(2)

	stale := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGMetaPage(stale, childBlk, 0, true); err != nil {
		t.Fatal(err)
	}
	btreeSeedRel(t, mgr, rel, stale, btreeLeafPage(t, true), btreeInternalPage(t, 1, childBlk))

	// Block 0 as an image of the post-insert root.
	image := btreeInternalPage(t, 1, childBlk, storage.BlockNumber(3))
	md := btree.PGBTMetaPage{
		Version: btree.BTreeVersionPG, Root: rootBlk, Level: 1,
		FastRoot: rootBlk, FastLevel: 1, AllEqualImage: true,
	}
	mainData := make([]byte, sizeOfXLogBtreeInsertData)
	binary.LittleEndian.PutUint16(mainData[0:2], 2)
	body, err := assembleXLogRecord(mainData, []BlockRef{
		{ID: 0, Rel: rel, Block: rootBlk, Image: &FullPageImage{Page: image, Apply: true}},
		{ID: 1, Rel: rel, Block: childBlk, SameRel: true},
		{ID: 2, Rel: rel, Block: btree.MetaBlock, SameRel: true, Data: encodeXLogBtreeMetadata(md), WillInit: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framePGAssembled(RmgrBtree, xlogBtreeInsertMeta, 0, body), 4400)

	gotMeta := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, btree.MetaBlock, gotMeta); err != nil {
		t.Fatal(err)
	}
	if m := btree.ReadPGMetaPage(gotMeta); m.Root != rootBlk || m.Level != 1 {
		t.Errorf("metapage = %+v — the image branch skipped _bt_restore_meta", m)
	}
}

// TestApplyRecordReplaysPGBtreeMetaCleanup covers the opcode whose entire
// upstream redo is `_bt_restore_meta(record, 0)`: VACUUM's _bt_set_cleanup_info
// stamping btm_last_cleanup_num_delpages, no other page touched. The metapage
// is block 0 here — not block 2 as in the insert/newroot records — so a shared
// restore-meta helper that hard-coded the id would silently rebuild the wrong
// page. Re-applying at the same LSN must change nothing (pd_lsn idempotency).
func TestApplyRecordReplaysPGBtreeMetaCleanup(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 5105, Fork: storage.MainFork}
	const rootBlk = storage.BlockNumber(1)

	meta := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGMetaPage(meta, rootBlk, 0, true); err != nil {
		t.Fatal(err)
	}
	leaf := btreeLeafPage(t, false)
	btreeSeedRel(t, mgr, rel, meta, leaf)

	md := btree.PGBTMetaPage{
		Version: btree.BTreeVersionPG, Root: rootBlk, Level: 0,
		FastRoot: rootBlk, FastLevel: 0, LastCleanupNumDelpages: 12, AllEqualImage: true,
	}
	body, err := assembleXLogRecord(nil, []BlockRef{
		{ID: 0, Rel: rel, Block: btree.MetaBlock, Data: encodeXLogBtreeMetadata(md), WillInit: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	framed := framePGAssembled(RmgrBtree, xlogBtreeMetaCleanup, 0, body)
	applyPGRecord(t, mgr, framed, 4500)

	got := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, btree.MetaBlock, got); err != nil {
		t.Fatal(err)
	}
	m := btree.ReadPGMetaPage(got)
	if m.Magic != btree.BTreeMagicPG || m.Root != rootBlk || m.LastCleanupNumDelpages != 12 {
		t.Errorf("replayed metapage = %+v, want magic + root %d + num_delpages 12", m, rootBlk)
	}
	if !btree.ReadPGOpaque(got).IsMeta() {
		t.Errorf("replayed metapage lost BTP_META")
	}

	// The leaf must be untouched: this opcode names exactly one block.
	gotLeaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, 1, gotLeaf); err != nil {
		t.Fatal(err)
	}
	if storage.MustHeader(gotLeaf).LSN() != storage.MustHeader(leaf).LSN() {
		t.Errorf("META_CLEANUP touched block 1")
	}

	applyPGRecord(t, mgr, framed, 4500)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, btree.MetaBlock, again); err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Errorf("second apply at the same LSN rewrote the metapage")
	}
}
