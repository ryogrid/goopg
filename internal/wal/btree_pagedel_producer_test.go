package wal

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/storage"
)

// TestVacuumEmitsPGPageDeletionPair is the M0130-S11.5d-3b-2 producer guard.
//
// S11.5d-1 and S11.5d-2 gave the two upstream page-deletion records their
// encoders, but nothing on the primary produced them: `unlinkEmptyLeaf` still
// emitted ONE goopg-native `RecordKindBtreeUnlinkPage` covering the union of
// both phases. The encoders' own tests hand-build their inputs, so they cannot
// catch the failure mode that swap introduces — a REAL vacuum reaching an
// encoder with an input it rejects (poffset 0, a rightmost target, a topparent
// equal to the leaf, a target image whose links are not the blocks being
// relinked). That surfaces as a WAL append error in production and in nothing
// else, so this test drives a real BTree vacuum through the real encoders and
// then decodes what came out.
//
// It asserts the pair: every deletion emits xl_btree_mark_page_halfdead
// immediately followed by xl_btree_unlink_page, in that order, each with the
// block registrations upstream's redo reads unconditionally.
func TestVacuumEmitsPGPageDeletionPair(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	t.Cleanup(func() { _ = mgr.Close() })

	rel := storage.RelFileNode{DBOid: 1, RelOid: 17001, Fork: storage.MainFork}

	type emission struct {
		info    uint8
		payload []byte
	}
	var emitted []emission
	var lsn storage.LSN
	record := func(info uint8, payload []byte, err error) (storage.LSN, error) {
		if err != nil {
			return 0, err
		}
		emitted = append(emitted, emission{info: info, payload: payload})
		lsn += storage.LSN(len(payload))
		return lsn, nil
	}

	pool, err := storage.NewPool(mgr, storage.PoolConfig{
		Slots: 256,
		LogBtreeVacuum: func(r storage.RelFileNode, blk storage.BlockNumber, prePage, page storage.Page, deleted []uint16) (storage.LSN, error) {
			p, err := EncodeBtreeVacuumPG(r, blk, prePage, page, deleted)
			return record(xlogBtreeVacuum, p, err)
		},
		LogBtreeNewRoot: func(r storage.RelFileNode, rootBlk storage.BlockNumber, rootPage storage.Page, leftChildBlk storage.BlockNumber, metaBlk storage.BlockNumber, metaPage storage.Page) (storage.LSN, error) {
			p, err := EncodeBtreeNewRootPG(r, rootBlk, rootPage, leftChildBlk, metaBlk, metaPage)
			return record(xlogBtreeNewRoot, p, err)
		},
		LogBtreeMarkPageHalfDead: func(r storage.RelFileNode, req storage.BtreeMarkPageHalfDeadRequest) (storage.LSN, error) {
			p, err := EncodeBtreeMarkPageHalfDeadPG(r, req.LeafBlk, req.LeafPage, req.ParentBlk, req.POffset, req.TopParent)
			return record(xlogBtreeMarkPageHalfDead, p, err)
		},
		LogBtreeUnlinkPage: func(r storage.RelFileNode, req storage.BtreeUnlinkPageRequest) (storage.LSN, error) {
			p, err := EncodeBtreeUnlinkPagePG(BtreeUnlinkPagePGRequest{
				Rel:        r,
				TargetBlk:  req.TargetBlk,
				TargetPage: req.TargetPage,
				SafeXid:    req.SafeXid,
				LeafBlk:    req.LeafBlk,
				LeafPage:   req.LeafPage,
				MetaBlk:    req.MetaBlk,
				MetaPage:   req.MetaPage,
			})
			return record(xlogBtreeUnlinkPage, p, err)
		},
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	// Enough entries for a multi-level tree; three quarters dead, so leaves are
	// emptied and unlinked but the tree is never reset to an empty root (which
	// would rewrite pages the records describe).
	const n = 5000
	entries := make([]btree.BulkEntry, n)
	var dead []storage.ItemPointer
	for i := range n {
		entries[i] = btree.BulkEntry{
			Key: btree.EncodeInt4(int32(i)),
			Ptr: storage.ItemPointer{Block: 0, Offset: uint16(i + 1)},
		}
		if i < n*3/4 {
			dead = append(dead, entries[i].Ptr)
		}
	}
	tree, err := btree.BulkCreate(pool, rel, entries)
	if err != nil {
		t.Fatalf("BulkCreate: %v", err)
	}
	if _, err := tree.VacuumIndexPages(dead); err != nil {
		// An encoder refusing a real vacuum's input lands here.
		t.Fatalf("VacuumIndexPages: %v", err)
	}

	pairs := 0
	for i, e := range emitted {
		if e.info != xlogBtreeMarkPageHalfDead {
			continue
		}
		pairs++
		if i+1 >= len(emitted) || emitted[i+1].info != xlogBtreeUnlinkPage {
			t.Fatalf("emission %d: mark-halfdead is not immediately followed by unlink-page", i)
		}
		leafBlk := checkHalfDeadRecord(t, e.payload)
		checkUnlinkRecord(t, emitted[i+1].payload, leafBlk)
	}
	if pairs == 0 {
		t.Fatal("no page deletions emitted despite three quarters of the entries being dead")
	}
}

// checkHalfDeadRecord decodes one emitted xl_btree_mark_page_halfdead and
// returns its leafblk. Blocks 0 (the leaf, WILL_INIT) and 1 (the parent) are
// what `btree_xlog_mark_page_halfdead` reads unconditionally.
func checkHalfDeadRecord(t *testing.T, payload []byte) storage.BlockNumber {
	t.Helper()
	dec := decodePageDelRecord(t, payload, xlogBtreeMarkPageHalfDead)
	if got := len(dec.XLog.MainData); got != sizeOfXLogBtreeMarkPageHalfDeadData {
		t.Fatalf("mark-halfdead main data %d bytes, want %d", got, sizeOfXLogBtreeMarkPageHalfDeadData)
	}
	md := dec.XLog.MainData
	leafBlk := storage.BlockNumber(binary.LittleEndian.Uint32(md[4:8]))
	if poffset := binary.LittleEndian.Uint16(md[0:2]); poffset == 0 {
		t.Errorf("mark-halfdead poffset 0 is not a valid OffsetNumber")
	}
	if top := storage.BlockNumber(binary.LittleEndian.Uint32(md[16:20])); top != storage.InvalidBlockNumber {
		t.Errorf("mark-halfdead topparent = %d, want InvalidBlockNumber (goopg deletes one page at a time)", top)
	}
	byID := blocksByID(t, dec)
	if b, ok := byID[0]; !ok || !b.WillInit || b.Block != leafBlk {
		t.Errorf("mark-halfdead block 0 = %+v, want WILL_INIT leaf %d", b, leafBlk)
	}
	if _, ok := byID[1]; !ok {
		t.Error("mark-halfdead has no block 1; redo reads the parent unconditionally")
	}
	return leafBlk
}

// checkUnlinkRecord decodes one emitted xl_btree_unlink_page and checks it is
// the phase-2 half of the deletion of leafBlk: block 0 the target (WILL_INIT)
// and block 2 the right sibling, both read unconditionally by
// `btree_xlog_unlink_page`, with no block 3 (goopg's target IS the leaf).
func checkUnlinkRecord(t *testing.T, payload []byte, leafBlk storage.BlockNumber) {
	t.Helper()
	dec := decodePageDelRecord(t, payload, xlogBtreeUnlinkPage)
	if got := len(dec.XLog.MainData); got != sizeOfXLogBtreeUnlinkPageData {
		t.Fatalf("unlink-page main data %d bytes, want %d", got, sizeOfXLogBtreeUnlinkPageData)
	}
	md := dec.XLog.MainData
	rightsib := storage.BlockNumber(binary.LittleEndian.Uint32(md[4:8]))
	if rightsib == btree.PNone {
		t.Error("unlink-page rightsib = P_NONE; upstream never deletes a rightmost page")
	}
	if level := binary.LittleEndian.Uint32(md[8:12]); level != 0 {
		t.Errorf("unlink-page level = %d, want 0 (goopg deletes leaves only)", level)
	}
	byID := blocksByID(t, dec)
	if b, ok := byID[0]; !ok || !b.WillInit || b.Block != leafBlk {
		t.Errorf("unlink-page block 0 = %+v, want WILL_INIT target %d (the leaf phase 1 marked)", b, leafBlk)
	}
	if b, ok := byID[2]; !ok || b.Block != rightsib {
		t.Errorf("unlink-page block 2 = %+v, want right sibling %d", b, rightsib)
	}
	if _, ok := byID[3]; ok {
		t.Error("unlink-page registered block 3; the target is the half-dead leaf itself")
	}
}

func decodePageDelRecord(t *testing.T, payload []byte, wantInfo uint8) decodedXLogRecord {
	t.Helper()
	rec, _, err := encodeRecordXLog(payload, 0)
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	dec, err := decodeRecordXLogDetailed(rec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.Header.Rmid != RmgrBtree || dec.Header.Info != wantInfo {
		t.Fatalf("rmid/info = %d/%#x, want %d/%#x", dec.Header.Rmid, dec.Header.Info, RmgrBtree, wantInfo)
	}
	return dec
}

func blocksByID(t *testing.T, dec decodedXLogRecord) map[byte]XLogBlockRef {
	t.Helper()
	byID := map[byte]XLogBlockRef{}
	for _, b := range dec.XLog.Blocks {
		if b.HasImage {
			t.Errorf("block %d carries a full-page image; page deletion must be incremental", b.ID)
		}
		byID[b.ID] = b
	}
	return byID
}
