package wal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// halfDeadTestLeaf builds the leaf _bt_mark_page_halfdead is about to convert:
// an ordinary empty leaf linked between two siblings.
func halfDeadTestLeaf(t *testing.T, prev, next storage.BlockNumber) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(page, btree.PGBTPageOpaque{Prev: prev, Next: next, Flags: btree.BTPLeaf})
	return page
}

// halfDeadTestParent builds an internal page whose data items downlink to
// `children` in order, behind a high key at P_HIKEY (so P_FIRSTDATAKEY is 2 and
// the physical/data-slot distinction the record's poffset must respect is live).
func halfDeadTestParent(t *testing.T, children []storage.BlockNumber) storage.Page {
	t.Helper()
	page := make(storage.Page, storage.BlockSize)
	if err := btree.InitPGBTPage(page); err != nil {
		t.Fatal(err)
	}
	btree.WritePGOpaque(page, btree.PGBTPageOpaque{Prev: btree.PNone, Next: 77, Level: 1})
	raws := [][]byte{btree.PGBTPivotRaw([]byte("high-key"), btree.PNone)}
	for i, child := range children {
		key := []byte{byte('a' + i)}
		if i == 0 {
			key = nil // minus infinity
		}
		raws = append(raws, btree.PGBTPivotRaw(key, child))
	}
	for _, raw := range raws {
		if _, err := storage.PageAddItemRaw(page, raw); err != nil {
			t.Fatal(err)
		}
	}
	return page
}

// TestEncodeBtreeMarkPageHalfDeadPGIsContentParity pins the record SHAPE against
// upstream _bt_mark_page_halfdead (nbtpage.c) / btree_xlog_mark_page_halfdead
// (nbtxlog.c:762-848): a 20-byte xl_btree_mark_page_halfdead main data, block 0
// = the leaf WILL_INIT with NO block data (redo rebuilds it from the struct),
// block 1 = the subtree parent. The "no full-page image" assertion is the point
// of M0130-S11.5d-1 — the record goopg emitted before this carried a PG-shaped
// header over a 16-byte native payload with no registered blocks at all, and
// upstream's redo calls XLogInitBufferForRedo(record, 0) unconditionally, which
// PANICs on an unregistered block id.
func TestEncodeBtreeMarkPageHalfDeadPGIsContentParity(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 91, Fork: storage.MainFork}
	const leafBlk, parentBlk = storage.BlockNumber(5), storage.BlockNumber(9)
	const prev, next = storage.BlockNumber(4), storage.BlockNumber(6)
	leaf := halfDeadTestLeaf(t, prev, next)

	framed, err := EncodeBtreeMarkPageHalfDeadPG(rel, leafBlk, leaf, parentBlk, 3, storage.InvalidBlockNumber)
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
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != xlogBtreeMarkPageHalfDead {
		t.Fatalf("rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, xlogBtreeMarkPageHalfDead)
	}
	if got := len(dec.XLog.MainData); got != sizeOfXLogBtreeMarkPageHalfDeadData {
		t.Fatalf("main data %d bytes, want %d", got, sizeOfXLogBtreeMarkPageHalfDeadData)
	}
	md := dec.XLog.MainData
	le := binary.LittleEndian
	if got := le.Uint16(md[0:2]); got != 3 {
		t.Errorf("xl_btree_mark_page_halfdead.poffset = %d, want 3", got)
	}
	if got := le.Uint16(md[2:4]); got != 0 {
		t.Errorf("struct alignment padding = %d, want 0", got)
	}
	for _, c := range []struct {
		name string
		off  int
		want storage.BlockNumber
	}{
		{"leafblk", 4, leafBlk},
		{"leftblk", 8, prev},
		{"rightblk", 12, next},
		{"topparent", 16, storage.InvalidBlockNumber},
	} {
		if got := storage.BlockNumber(le.Uint32(md[c.off : c.off+4])); got != c.want {
			t.Errorf("xl_btree_mark_page_halfdead.%s = %d, want %d", c.name, got, c.want)
		}
	}

	byID := map[byte]XLogBlockRef{}
	for _, b := range dec.XLog.Blocks {
		if b.HasImage {
			t.Errorf("block %d carries a full-page image; the record must be incremental", b.ID)
		}
		byID[b.ID] = b
	}
	if len(byID) != 2 {
		t.Fatalf("want block-ids 0/1, got %d refs", len(dec.XLog.Blocks))
	}
	if b := byID[0]; !b.WillInit || b.Block != leafBlk || len(b.Data) != 0 {
		t.Errorf("block 0 = blk %d willinit=%v %d data bytes, want %d/true/0", b.Block, b.WillInit, len(b.Data), leafBlk)
	}
	if b := byID[1]; b.WillInit || b.Block != parentBlk || len(b.Data) != 0 {
		t.Errorf("block 1 = blk %d willinit=%v %d data bytes, want %d/false/0", b.Block, b.WillInit, len(b.Data), parentBlk)
	}
}

// TestEncodeBtreeMarkPageHalfDeadPGRefusesUndescribableRecords guards the three
// inputs whose record upstream's redo cannot replay as written: no parent block
// (block 1 is read unconditionally, and an unregistered id PANICs), poffset 0
// (not a legal OffsetNumber — PageGetItemId would read the page header), and a
// topparent naming the leaf itself, which upstream encodes as
// InvalidBlockNumber and whose literal form would make _bt_unlink_halfdead_page
// descend into the page it is deleting.
func TestEncodeBtreeMarkPageHalfDeadPGRefusesUndescribableRecords(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 91, Fork: storage.MainFork}
	leaf := halfDeadTestLeaf(t, 4, 6)
	for _, tc := range []struct {
		name      string
		parent    storage.BlockNumber
		poffset   uint16
		topparent storage.BlockNumber
	}{
		{"no parent", storage.InvalidBlockNumber, 3, storage.InvalidBlockNumber},
		{"poffset 0", 9, 0, storage.InvalidBlockNumber},
		{"topparent is the leaf", 9, 3, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodeBtreeMarkPageHalfDeadPG(rel, 5, leaf, tc.parent, tc.poffset, tc.topparent); err == nil {
				t.Fatalf("want an error for %s", tc.name)
			}
		})
	}
}

// TestApplyRecordReplaysPGBtreeMarkPageHalfDead drives the record through emit →
// encode → decode → ApplyRecord and asserts both limbs reproduce what upstream's
// redo produces: a leaf rebuilt as BTP_HALF_DEAD|BTP_LEAF carrying exactly one
// dummy high key whose downlink field is the top parent, and a parent whose
// poffset item now downlinks at the RIGHT neighbour's child with the neighbour's
// item gone (upstream's retarget-and-delete, not a plain delete-at-poffset).
func TestApplyRecordReplaysPGBtreeMarkPageHalfDead(t *testing.T) {
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: t.TempDir()})
	defer mgr.Close()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 92, Fork: storage.MainFork}
	const leafBlk, parentBlk = storage.BlockNumber(1), storage.BlockNumber(2)
	children := []storage.BlockNumber{leafBlk, 7, 8}

	// Block 0 stands in for the metapage; 1 = the leaf, 2 = the parent.
	if _, err := mgr.Extend(rel, make(storage.Page, storage.BlockSize)); err != nil {
		t.Fatal(err)
	}
	leaf := halfDeadTestLeaf(t, btree.PNone, 7)
	if _, err := mgr.Extend(rel, leaf); err != nil {
		t.Fatal(err)
	}
	parent := halfDeadTestParent(t, children)
	if _, err := mgr.Extend(rel, parent); err != nil {
		t.Fatal(err)
	}

	// The leaf's downlink is the first data item, physical offset 2 (P_HIKEY
	// occupies 1).
	framed, err := EncodeBtreeMarkPageHalfDeadPG(rel, leafBlk, leaf, parentBlk, 2, storage.InvalidBlockNumber)
	if err != nil {
		t.Fatal(err)
	}
	applyPGRecord(t, mgr, framed, 1200)

	gotLeaf := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, leafBlk, gotLeaf); err != nil {
		t.Fatal(err)
	}
	op := btree.ReadPGOpaque(gotLeaf)
	if op.Flags != btree.BTPHalfDead|btree.BTPLeaf {
		t.Errorf("replayed leaf flags = %#x, want BTP_HALF_DEAD|BTP_LEAF (%#x)", op.Flags, btree.BTPHalfDead|btree.BTPLeaf)
	}
	if op.Prev != btree.PNone || op.Next != 7 || op.Level != 0 {
		t.Errorf("replayed leaf opaque = %+v, want prev P_NONE next 7 level 0", op)
	}
	n, err := storage.PageLinePointerCount(gotLeaf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("replayed half-dead leaf has %d items, want exactly the dummy high key", n)
	}
	hikey, err := storage.PageGetItemRaw(gotLeaf, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hikey) != btree.SizeOfIndexTupleData {
		t.Errorf("dummy high key is %d bytes, want %d", len(hikey), btree.SizeOfIndexTupleData)
	}
	if got := btree.BTreeTupleGetDownLink(hikey); got != storage.InvalidBlockNumber {
		t.Errorf("dummy high key top parent = %d, want InvalidBlockNumber", got)
	}
	if lsn := storage.MustHeader(gotLeaf).LSN(); lsn != 1200 {
		t.Errorf("replayed leaf pd_lsn = %d, want 1200", lsn)
	}

	gotParent := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, parentBlk, gotParent); err != nil {
		t.Fatal(err)
	}
	pn, err := storage.PageLinePointerCount(gotParent)
	if err != nil {
		t.Fatal(err)
	}
	if pn != 3 { // high key + two surviving downlinks
		t.Fatalf("replayed parent has %d items, want 3", pn)
	}
	// Offset 2 kept its own key bytes but now points at what offset 3 pointed at
	// (child 7); the old offset 3 is gone, so offset 3 is now the last child.
	first, err := storage.PageGetItemRaw(gotParent, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := btree.BTreeTupleGetDownLink(first); got != 7 {
		t.Errorf("parent offset 2 downlink = %d, want 7 (the right neighbour's child)", got)
	}
	if len(first) != btree.SizeOfIndexTupleData {
		t.Errorf("parent offset 2 is %d bytes — the minus-infinity pivot must keep its shape", len(first))
	}
	last, err := storage.PageGetItemRaw(gotParent, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := btree.BTreeTupleGetDownLink(last); got != 8 {
		t.Errorf("parent offset 3 downlink = %d, want 8", got)
	}

	// Per-block pd_lsn idempotency: a second apply at the same LSN changes
	// nothing, which is what lets an interrupted replay resume between limbs.
	applyPGRecord(t, mgr, framed, 1200)
	again := make(storage.Page, storage.BlockSize)
	if err := mgr.ReadBlock(rel, parentBlk, again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, gotParent) {
		t.Errorf("second apply at the same LSN changed the parent page")
	}
}
