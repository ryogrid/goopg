package xlog

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/storage"
)

// unlinkTestPage builds a PG-format btree page with the given links/level/flags.
func unlinkTestPage(t *testing.T, prev, next storage.BlockNumber, level uint32, flags uint16) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	nbtree.WritePGOpaque(page, nbtree.PGBTPageOpaque{Prev: prev, Next: next, Level: level, Flags: flags})
	return page
}

// unlinkTestHalfDeadLeaf builds the page phase 1 leaves behind: BTP_HALF_DEAD,
// one dummy high key whose downlink field carries the next child down.
func unlinkTestHalfDeadLeaf(t *testing.T, prev, next, topparent storage.BlockNumber) storage.Page {
	t.Helper()
	page := unlinkTestPage(t, prev, next, 0, nbtree.BTPHalfDead|nbtree.BTPLeaf)
	if _, err := storage.PageAddItemRaw(page, nbtree.PGBTPivotRaw(nil, topparent)); err != nil {
		t.Fatal(err)
	}
	return page
}

func unlinkTestMetaPage(t *testing.T, root storage.BlockNumber, level uint32) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := nbtree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	nbtree.WritePGMetaPage(page, nbtree.PGBTMetaPage{
		Magic: nbtree.BTreeMagicPG, Version: nbtree.BTreeVersionPG,
		Root: root, Level: level, FastRoot: root, FastLevel: level,
		LastCleanupNumHeapTuples: -1,
	})
	return page
}

// TestEncodeBtreeUnlinkPagePGIsContentParity pins the record SHAPE for the
// ordinary case — a leaf target, which is also the half-dead leaf — against
// upstream _bt_unlink_halfdead_page (nbtpage.c:2680-2740) /
// btree_xlog_unlink_page (nbtxlog.c:850-1005): a 36-byte
// xl_btree_unlink_page main data, block 0 the target WILL_INIT with no data,
// blocks 1/2 the siblings, and NO block 3 (the leaf is the target) and no
// block 4. The no-full-page-image assertion is the point of M0130-S11.5d-2:
// goopg's native page-deletion record is emitted under this same RM_BTREE/0x80
// header, so a standby casts its 41-byte native payload to the struct and then
// PANICs in XLogInitBufferForRedo on block 0, which was never registered.
func TestEncodeBtreeUnlinkPagePGIsContentParity(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 101, Fork: storage.MainFork}
	const targetBlk = storage.BlockNumber(5)
	const prev, next = storage.BlockNumber(4), storage.BlockNumber(6)
	const safexid = uint64(0x1_0000_002A)

	framed, err := EncodeBtreeUnlinkPagePG(BtreeUnlinkPagePGRequest{
		Rel:        rel,
		TargetBlk:  targetBlk,
		TargetPage: unlinkTestPage(t, prev, next, 0, nbtree.BTPHalfDead|nbtree.BTPLeaf),
		SafeXid:    safexid,
		LeafBlk:    targetBlk,
		MetaBlk:    storage.InvalidBlockNumber,
	})
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeUnlinkPage {
		t.Fatalf("rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, xlogBtreeUnlinkPage)
	}
	md := dec.XLog.MainData
	if len(md) != sizeOfXLogBtreeUnlinkPageData {
		t.Fatalf("main data %d bytes, want %d", len(md), sizeOfXLogBtreeUnlinkPageData)
	}
	le := binary.LittleEndian
	for _, c := range []struct {
		name string
		off  int
		want storage.BlockNumber
	}{
		{"leftsib", 0, prev},
		{"rightsib", 4, next},
		// Target IS the leaf: upstream copies the target's own links here and
		// writes InvalidBlockNumber for leaftopparent (there is no next child
		// down — this is the last page of the subtree).
		{"leafleftsib", 24, prev},
		{"leafrightsib", 28, next},
		{"leaftopparent", 32, storage.InvalidBlockNumber},
	} {
		if got := storage.BlockNumber(le.Uint32(md[c.off : c.off+4])); got != c.want {
			t.Errorf("xl_btree_unlink_page.%s = %d, want %d", c.name, got, c.want)
		}
	}
	if got := le.Uint32(md[8:12]); got != 0 {
		t.Errorf("xl_btree_unlink_page.level = %d, want 0", got)
	}
	if got := le.Uint32(md[12:16]); got != 0 {
		t.Errorf("struct alignment padding before safexid = %d, want 0", got)
	}
	if got := le.Uint64(md[16:24]); got != safexid {
		t.Errorf("xl_btree_unlink_page.safexid = %#x, want %#x", got, safexid)
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
	if b := byID[0]; !b.WillInit || b.Block != targetBlk || len(b.Data) != 0 {
		t.Errorf("block 0 = blk %d willinit=%v %d data bytes, want %d/true/0", b.Block, b.WillInit, len(b.Data), targetBlk)
	}
	if b := byID[1]; b.WillInit || b.Block != prev || len(b.Data) != 0 {
		t.Errorf("block 1 = blk %d willinit=%v %d data bytes, want %d/false/0", b.Block, b.WillInit, len(b.Data), prev)
	}
	if b := byID[2]; b.WillInit || b.Block != next || len(b.Data) != 0 {
		t.Errorf("block 2 = blk %d willinit=%v %d data bytes, want %d/false/0", b.Block, b.WillInit, len(b.Data), next)
	}
}

// TestEncodeBtreeUnlinkPagePGInternalTargetLogsLeafAndMeta covers the two
// optional block references together: an INTERNAL target adds block 3 (the
// half-dead leaf, WILL_INIT so redo can recreate it with the next child down)
// and a metapage makes it the XLOG_BTREE_UNLINK_PAGE_META opcode with block 4
// carrying xl_btree_metadata. leafleftsib/leafrightsib/leaftopparent must now
// come from the LEAF, not the target.
func TestEncodeBtreeUnlinkPagePGInternalTargetLogsLeafAndMeta(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 102, Fork: storage.MainFork}
	const targetBlk, leafBlk, metaBlk = storage.BlockNumber(5), storage.BlockNumber(11), storage.BlockNumber(0)
	const leafPrev, leafNext, nextChild = storage.BlockNumber(10), storage.BlockNumber(12), storage.BlockNumber(13)

	framed, err := EncodeBtreeUnlinkPagePG(BtreeUnlinkPagePGRequest{
		Rel:        rel,
		TargetBlk:  targetBlk,
		TargetPage: unlinkTestPage(t, nbtree.PNone, 6, 2, 0),
		SafeXid:    99,
		LeafBlk:    leafBlk,
		LeafPage:   unlinkTestHalfDeadLeaf(t, leafPrev, leafNext, nextChild),
		MetaBlk:    metaBlk,
		MetaPage:   unlinkTestMetaPage(t, 6, 1),
	})
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
	if dec.Header.Info != xlogBtreeUnlinkPageMeta {
		t.Fatalf("info = %#x, want XLOG_BTREE_UNLINK_PAGE_META (%#x)", dec.Header.Info, xlogBtreeUnlinkPageMeta)
	}
	le := binary.LittleEndian
	md := dec.XLog.MainData
	if got := storage.BlockNumber(le.Uint32(md[0:4])); got != nbtree.PNone {
		t.Errorf("leftsib = %d, want P_NONE", got)
	}
	for _, c := range []struct {
		name string
		off  int
		want storage.BlockNumber
	}{
		{"leafleftsib", 24, leafPrev},
		{"leafrightsib", 28, leafNext},
		{"leaftopparent", 32, nextChild},
	} {
		if got := storage.BlockNumber(le.Uint32(md[c.off : c.off+4])); got != c.want {
			t.Errorf("xl_btree_unlink_page.%s = %d, want %d", c.name, got, c.want)
		}
	}

	byID := map[byte]XLogBlockRef{}
	for _, b := range dec.XLog.Blocks {
		byID[b.ID] = b
	}
	// No block 1: the target is leftmost on its level.
	if _, ok := byID[1]; ok {
		t.Errorf("block 1 registered for a leftmost target")
	}
	if len(byID) != 4 {
		t.Fatalf("want block-ids 0/2/3/4, got %d refs", len(dec.XLog.Blocks))
	}
	if b := byID[3]; !b.WillInit || b.Block != leafBlk || len(b.Data) != 0 {
		t.Errorf("block 3 = blk %d willinit=%v %d data bytes, want %d/true/0", b.Block, b.WillInit, len(b.Data), leafBlk)
	}
	if b := byID[4]; !b.WillInit || b.Block != metaBlk || len(b.Data) != sizeOfXLogBtreeMetadata {
		t.Errorf("block 4 = blk %d willinit=%v %d data bytes, want %d/true/%d",
			b.Block, b.WillInit, len(b.Data), metaBlk, sizeOfXLogBtreeMetadata)
	}
}

// TestEncodeBtreeUnlinkPagePGRefusesUndescribableRecords guards the inputs whose
// record upstream's redo could not replay as written. The rightmost case is the
// structural one: redo reads block 2 without testing rightsib, so "no right
// sibling" has no representation at all — and correspondingly _bt_pagedel never
// deletes a rightmost page.
func TestEncodeBtreeUnlinkPagePGRefusesUndescribableRecords(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 103, Fork: storage.MainFork}
	leafTarget := unlinkTestPage(t, 4, 6, 0, nbtree.BTPHalfDead|nbtree.BTPLeaf)
	internalTarget := unlinkTestPage(t, 4, 6, 2, 0)
	for _, tc := range []struct {
		name string
		req  BtreeUnlinkPagePGRequest
	}{
		{"rightmost target", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: unlinkTestPage(t, 4, nbtree.PNone, 0, nbtree.BTPLeaf),
			LeafBlk: 5, MetaBlk: storage.InvalidBlockNumber,
		}},
		{"internal target without a leaf page", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: internalTarget,
			LeafBlk: 11, MetaBlk: storage.InvalidBlockNumber,
		}},
		{"leaf page logged for a leaf target", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: leafTarget,
			LeafBlk: 5, LeafPage: leafTarget, MetaBlk: storage.InvalidBlockNumber,
		}},
		{"leaf differs from a level-0 target", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: leafTarget,
			LeafBlk: 11, LeafPage: unlinkTestHalfDeadLeaf(t, 10, 12, storage.InvalidBlockNumber),
			MetaBlk: storage.InvalidBlockNumber,
		}},
		{"leaf is not half-dead", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: internalTarget,
			LeafBlk: 11, LeafPage: unlinkTestPage(t, 10, 12, 0, nbtree.BTPLeaf),
			MetaBlk: storage.InvalidBlockNumber,
		}},
		{"leaftopparent below a level-1 target", BtreeUnlinkPagePGRequest{
			Rel: rel, TargetBlk: 5, TargetPage: unlinkTestPage(t, 4, 6, 1, 0),
			LeafBlk: 11, LeafPage: unlinkTestHalfDeadLeaf(t, 10, 12, 13),
			MetaBlk: storage.InvalidBlockNumber,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeBtreeUnlinkPagePG(tc.req); err == nil {
				t.Fatalf("want an error for %s", tc.name)
			}
		})
	}
}

// TestApplyRecordReplaysPGBtreeUnlinkPage drives the internal-target record
// through emit → encode → decode → ApplyRecord and asserts every limb matches
// upstream's redo: the siblings skip the target in both directions, the target
// becomes an empty BTP_DELETED|BTP_HAS_FULLXID page whose only contents are the
// safexid, and the half-dead leaf is recreated pointing at the next child down.
func TestApplyRecordReplaysPGBtreeUnlinkPage(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 104, Fork: storage.MainFork}
	// Blocks 0..4: 0 = metapage (unused here), 1 = left sib, 2 = target,
	// 3 = right sib, 4 = the half-dead leaf.
	const leftBlk, targetBlk, rightBlk, leafBlk = storage.BlockNumber(1), storage.BlockNumber(2), storage.BlockNumber(3), storage.BlockNumber(4)
	const nextChild = storage.BlockNumber(9)
	const safexid = uint64(0x2_0000_00FF)

	for _, page := range []storage.Page{
		make(storage.Page, storage.BlockSize),
		unlinkTestPage(t, nbtree.PNone, targetBlk, 2, 0),
		unlinkTestPage(t, leftBlk, rightBlk, 2, 0),
		unlinkTestPage(t, targetBlk, nbtree.PNone, 2, 0),
		unlinkTestHalfDeadLeaf(t, 7, 8, nextChild),
	} {
		if _, err := mgr.Extend(rel, page); err != nil {
			t.Fatal(err)
		}
	}
	target := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, targetBlk, target); err != nil {
		t.Fatal(err)
	}
	leaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, leaf); err != nil {
		t.Fatal(err)
	}

	framed, err := EncodeBtreeUnlinkPagePG(BtreeUnlinkPagePGRequest{
		Rel: rel, TargetBlk: targetBlk, TargetPage: target, SafeXid: safexid,
		LeafBlk: leafBlk, LeafPage: leaf, MetaBlk: storage.InvalidBlockNumber,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 4200)

	read := func(blk storage.BlockNumber) storage.Page {
		t.Helper()
		p := make(storage.Page, storage.BlockSize)
		if err := mgr.ReadBlock(rel, blk, p); err != nil {
			t.Fatal(err)
		}
		if lsn := storage.MustHeader(p).LSN(); lsn != 4200 {
			t.Errorf("block %d pd_lsn = %d, want 4200", blk, lsn)
		}
		return p
	}

	if op := nbtree.ReadPGOpaque(read(leftBlk)); op.Next != rightBlk {
		t.Errorf("left sibling btpo_next = %d, want %d", op.Next, rightBlk)
	}
	if op := nbtree.ReadPGOpaque(read(rightBlk)); op.Prev != leftBlk {
		t.Errorf("right sibling btpo_prev = %d, want %d", op.Prev, leftBlk)
	}

	gotTarget := read(targetBlk)
	op := nbtree.ReadPGOpaque(gotTarget)
	if op.Flags != nbtree.BTPDeleted|nbtree.BTPHasFullXID {
		t.Errorf("target flags = %#x, want BTP_DELETED|BTP_HAS_FULLXID (%#x)", op.Flags, nbtree.BTPDeleted|nbtree.BTPHasFullXID)
	}
	if op.Prev != leftBlk || op.Next != rightBlk || op.Level != 2 {
		t.Errorf("target opaque = %+v, want prev %d next %d level 2", op, leftBlk, rightBlk)
	}
	// A deleted page's "items" are not items: pd_lower covers exactly one
	// BTDeletedPageData and pd_upper is closed up against the special area, so
	// there is no free space and nothing can be added. (PageGetMaxOffsetNumber
	// derives a nonsense count of 2 from that pd_lower on upstream too — it is
	// simply never asked of a deleted page.)
	h := storage.MustHeader(gotTarget)
	if got := h.Lower(); got != storage.SizeOfPageHeaderData+nbtree.SizeOfBTDeletedPageData {
		t.Errorf("deleted target pd_lower = %d, want %d", got, storage.SizeOfPageHeaderData+nbtree.SizeOfBTDeletedPageData)
	}
	if h.Upper() != h.Special() {
		t.Errorf("deleted target pd_upper = %d, want pd_special %d", h.Upper(), h.Special())
	}
	if got, ok := nbtree.PGDeletedPageSafeXid(gotTarget); !ok || got != safexid {
		t.Errorf("target safexid = %#x (ok=%v), want %#x", got, ok, safexid)
	}

	gotLeaf := read(leafBlk)
	if op := nbtree.ReadPGOpaque(gotLeaf); op.Flags != nbtree.BTPHalfDead|nbtree.BTPLeaf || op.Prev != 7 || op.Next != 8 {
		t.Errorf("leaf opaque = %+v, want half-dead leaf between 7 and 8", op)
	}
	hikey, err := storage.PageGetItemRaw(gotLeaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := nbtree.BTreeTupleGetDownLink(hikey); got != nextChild {
		t.Errorf("recreated leaf top parent = %d, want %d", got, nextChild)
	}
}
